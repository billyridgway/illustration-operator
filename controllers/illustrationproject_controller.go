package controllers

import (
	context "context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"gopkg.in/yaml.v3"
	illustrationsv1alpha1 "illustration-operator/api/v1alpha1"
)

var (
	// Basic Prometheus metrics for the POC operator. These intentionally avoid
	// embedding any policyholder- or PAS-level data; labels are limited to
	// coarse-grained fields like product id and outcome.
	reconcileTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "illustrationproject_reconciles_total",
		Help: "Total number of IllustrationProject reconciles.",
	})
	reconcileErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "illustrationproject_reconcile_errors_total",
		Help: "Total number of IllustrationProject reconciles that ended in error.",
	})
	jobsCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "illustrationproject_jobs_created_total",
		Help: "Number of illustration Jobs created by the operator.",
	}, []string{"product"})
	jobFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "illustrationproject_jobs_failed_total",
		Help: "Number of illustration Jobs that reached a Failed condition.",
	}, []string{"product"})
	assumptionDurations = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "illustrationproject_assumption_run_seconds",
		Help:    "Duration of LLM assumption extraction Jobs in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"product"})
	illustrationDurations = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "illustrationproject_run_seconds",
		Help:    "Duration of illustration Jobs in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"product"})
)

func init() {
	metrics.Registry.MustRegister(
		reconcileTotal,
		reconcileErrors,
		jobsCreated,
		jobFailures,
		assumptionDurations,
		illustrationDurations,
	)
}

// IllustrationProjectReconciler reconciles an IllustrationProject object.
type IllustrationProjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile implements the core reconciliation loop.
//
// High-level flow:
//  1. Load the IllustrationProject.
//  2. Look up product config from the product registry (config/products.yaml).
//  3. Derive MinIO prefixes from productId and registry (no prefixes in CRD).
//  4. Trigger/ensure an illustration run via Kubernetes Jobs.
//  5. Update status.phase / lastRunTime / lastRunId / lastError.
func (r *IllustrationProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconcileTotal.Inc()

	log := ctrl.LoggerFrom(ctx).WithValues("illustrationproject", req.NamespacedName)

	proj := &illustrationsv1alpha1.IllustrationProject{}
	if err := r.Get(ctx, req.NamespacedName, proj); err != nil {
		if kerrors.IsNotFound(err) {
			// Object deleted; nothing to do.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// If deletion timestamp is set, skip active work for now.
	if !proj.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Load product config from registry.
	cfg, err := loadProductConfig()
	if err != nil {
		log.Error(err, "failed to load product registry")
		reconcileErrors.Inc()
		return r.updateStatusError(ctx, proj, "Failed", err)
	}

	productCfg, ok := cfg.Products[proj.Spec.ProductId]
	if !ok {
		reconcileErrors.Inc()
		return r.updateStatusError(ctx, proj, "Failed", fmt.Errorf("unknown productId %q", proj.Spec.ProductId))
	}

	prefixBase := productCfg.PrefixBase
	if prefixBase == "" {
		prefixBase = proj.Spec.ProductId
	}

	// Derive prefixes via convention.
	filingsPrefix := fmt.Sprintf("filings/%s/", prefixBase)
	policiesPrefix := fmt.Sprintf("%s/", prefixBase)
	projectionsPrefix := fmt.Sprintf("projections/%s/", prefixBase)

	// Work out LLM doc/assumption identifiers so we can surface them in status.
	docPrefix := productCfg.LLM.DocPrefix
	if docPrefix == "" && (productCfg.LLM.DocPrefix != "" || productCfg.LLM.AssumptionID != "") {
		docPrefix = fmt.Sprintf("docs/%s/", prefixBase)
	}
	assumptionID := productCfg.LLM.AssumptionID
	if assumptionID == "" && (productCfg.LLM.DocPrefix != "" || productCfg.LLM.AssumptionID != "") {
		assumptionID = fmt.Sprintf("%s-llm-v1", proj.Spec.ProductId)
	}

	// Capture a human-friendly summary of the resolved wiring on status.
	proj.Status.Resolved = &illustrationsv1alpha1.ResolvedRefs{
		ProductId:     proj.Spec.ProductId,
		PasConfigMap:  proj.Spec.PasConfigMap,
		PasKey:        "pas.json",
		DSLFile:       productCfg.DSLFile,
		AssumptionId:  assumptionID,
		FilingsPrefix: filingsPrefix,
		DocPrefix:     docPrefix,
	}
	// Record the logical AssumptionSet id on status so UIs can see which
	// set is intended for this project without exposing its contents.
	proj.Status.AssumptionSetId = assumptionID

	log.Info("reconciling project", "productId", proj.Spec.ProductId, "filingsPrefix", filingsPrefix, "policiesPrefix", policiesPrefix, "projectionsPrefix", projectionsPrefix)

	// Ensure or create a Kubernetes Job to LLM-extract assumptions for this
	// product (when configured) before kicking off the projection pipeline.
	if err := r.ensureLLMJob(ctx, log, &productCfg, proj, prefixBase); err != nil {
		log.Error(err, "failed to ensure LLM assumptions Job")
		// Don't hard-fail the reconcile; projections can still run with default
		// or pre-populated assumptions.
	}

	// If LLM extraction is configured for this product, wait for the
	// assumptions Job to reach a terminal state before starting the
	// illustration Job. This ensures that, when the LLM succeeds, the latest
	// AssumptionSet is available to the projection pipeline. On failure we
	// still allow projections to run with existing assumptions.
	if productCfg.LLM.DocPrefix != "" || productCfg.LLM.AssumptionID != "" {
		llmJobName := fmt.Sprintf("assumptions-%s", proj.Spec.ProductId)
		llmNN := types.NamespacedName{Name: llmJobName, Namespace: proj.Namespace}
		llmJob := &batchv1.Job{}

		if err := r.Get(ctx, llmNN, llmJob); err != nil {
			if kerrors.IsNotFound(err) {
				// Job has just been created or not yet visible; requeue and wait.
				log.Info("LLM assumptions Job not yet created; requeuing before projection", "job", llmJobName)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// On read errors, log and continue to illustration so we don't wedge
			// the controller; projections will use whatever assumptions are
			// already present.
			log.Error(err, "failed to fetch LLM assumptions Job status", "job", llmJobName)
		} else {
			terminal := false
			for _, cond := range llmJob.Status.Conditions {
				if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
					log.Info("LLM assumptions Job completed", "job", llmJobName)
					terminal = true
					// Record duration when start time is available.
					if llmJob.Status.StartTime != nil {
						dur := time.Since(llmJob.Status.StartTime.Time).Seconds()
						assumptionDurations.WithLabelValues(proj.Spec.ProductId).Observe(dur)
					}
					break
				}
				if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
					log.Info("LLM assumptions Job failed; proceeding with existing assumptions", "job", llmJobName, "reason", cond.Reason, "message", cond.Message)
					terminal = true
					break
				}
			}

			if !terminal {
				// Job exists but is still Pending/Running; wait before creating or
				// updating the illustration Job.
				log.Info("LLM assumptions Job not yet in terminal state; requeuing before projection", "job", llmJobName)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// If LLM extraction is configured and has just completed successfully,
			// gate projection on an explicit approval step. For the POC, this is
			// represented by status.AssumptionApproved; external tooling (e.g. a
			// small UI or kubectl patch) is expected to flip this once a human has
			// reviewed/approved the extracted set.
			if !proj.Status.AssumptionApproved {
				now := metav1.Now()
				proj.Status.Phase = "AwaitingApproval"
				proj.Status.LastRunTime = &now
				proj.Status.LastRunID = llmJobName
				proj.Status.LastError = ""
				if err := r.Status().Update(ctx, proj); err != nil {
					return ctrl.Result{}, err
				}
				// Do not start the illustration Job until approval is recorded.
				return ctrl.Result{}, nil
			}
		}
	}

	// Ensure or create a Kubernetes Job to run the illustration pipeline for
	// this project, and map its status back onto the IllustrationProject.
	res, err := r.ensureIllustrationJob(ctx, log, &productCfg, proj, filingsPrefix, policiesPrefix, projectionsPrefix)
	if err != nil {
		log.Error(err, "failed to ensure illustration Job")
		reconcileErrors.Inc()
		return r.updateStatusError(ctx, proj, "Failed", err)
	}

	return res, nil
}

func (r *IllustrationProjectReconciler) updateStatusError(ctx context.Context, proj *illustrationsv1alpha1.IllustrationProject, phase string, cause error) (ctrl.Result, error) {
	now := metav1.Now()
	proj.Status.Phase = phase
	proj.Status.LastRunTime = &now
	proj.Status.LastError = cause.Error()

	if err := r.Status().Update(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}

	// Back off slightly on repeated errors.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager wires the controller into the manager.
func (r *IllustrationProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&illustrationsv1alpha1.IllustrationProject{}).
		Complete(r)
}

// ProductRegistry represents the on-cluster product configuration.
type ProductRegistry struct {
	Products map[string]ProductConfig `json:"products" yaml:"products"`
}

// ProductConfig holds config for a single product.
type ProductConfig struct {
	DisplayName string `json:"displayName" yaml:"displayName"`
	DSLFile     string `json:"dslFile" yaml:"dslFile"`

	// Optional hints for MinIO prefixes and mortality.
	PrefixBase string `json:"prefixBase" yaml:"prefixBase"`

	Mortality struct {
		Kind        string `json:"kind" yaml:"kind"`
		TablePrefix string `json:"tablePrefix" yaml:"tablePrefix"`
	} `json:"mortality" yaml:"mortality"`

	// Optional LLM extraction config. When provided, the operator will ensure
	// a one-shot Job that calls the actuarypoc CLI to extract an AssumptionSet
	// from the latest document under the given MinIO prefix.
	LLM struct {
		DocPrefix    string `json:"docPrefix" yaml:"docPrefix"`
		AssumptionID string `json:"assumptionId" yaml:"assumptionId"`
	} `json:"llm" yaml:"llm"`
}

// loadProductConfig loads the product registry from a mounted file.
//
// For the POC we expect a ConfigMap or file mounted at
// `/config/products.yaml`. This keeps the operator generic and lets
// products be added without code changes.
func loadProductConfig() (*ProductRegistry, error) {
	path := "/config/products.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg ProductRegistry
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ensureLLMJob creates a one-shot Kubernetes Job that uses the actuarypoc
// image to extract an AssumptionSet from the latest doc under a MinIO prefix
// and upsert it into the registry. It is intentionally idempotent: if the Job
// already exists, it is left as-is.
func (r *IllustrationProjectReconciler) ensureLLMJob(
	ctx context.Context,
	log logr.Logger,
	productCfg *ProductConfig,
	proj *illustrationsv1alpha1.IllustrationProject,
	prefixBase string,
) error {
	// If no LLM config is present, nothing to do.
	if productCfg.LLM.DocPrefix == "" && productCfg.LLM.AssumptionID == "" {
		return nil
	}

	jobName := fmt.Sprintf("assumptions-%s", proj.Spec.ProductId)
	nn := types.NamespacedName{Name: jobName, Namespace: proj.Namespace}
	job := &batchv1.Job{}

	if err := r.Get(ctx, nn, job); err == nil {
		// Job already exists; leave it alone for now.
		return nil
	} else if !kerrors.IsNotFound(err) {
		return err
	}

	docPrefix := productCfg.LLM.DocPrefix
	if docPrefix == "" {
		// Fallback convention if DocPrefix was omitted in the registry.
		docPrefix = fmt.Sprintf("docs/%s/", prefixBase)
	}

	assumptionID := productCfg.LLM.AssumptionID
	if assumptionID == "" {
		assumptionID = fmt.Sprintf("%s-llm-v1", proj.Spec.ProductId)
	}

	job = &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: proj.Namespace,
			Labels: map[string]string{
				"app":                         "llm-assumptions-runner",
				"illustrations.poc/productId": proj.Spec.ProductId,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: int32Ptr(0),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "llm-extractor",
							Image:           os.Getenv("ILLUSTRATION_RUNNER_IMAGE"),
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/bin/sh", "-c"},
							Args: []string{
								"set -euo pipefail; " +
									"cd /opt/dagster/app; " +
									"python -m actuarypoc.cli.main extract-assumptions-minio",
							},
							Env: []corev1.EnvVar{
								{Name: "PYTHONPATH", Value: "/opt/dagster/app/src"},
								// MinIO configuration for pulling docs and writing assumptions.
								{Name: "MINIO_ENDPOINT", Value: "minio.minio-system.svc.cluster.local:9000"},
								{Name: "MINIO_ACCESS_KEY", Value: "admin"},
								{Name: "MINIO_SECRET_KEY", Value: "password"},
								{Name: "MINIO_BUCKET", Value: "illuminet"},
								{Name: "MINIO_SECURE", Value: "false"},
								// LLM extraction parameters.
								{Name: "LLM_DOC_PREFIX", Value: docPrefix},
								{Name: "LLM_PRODUCT_CODE", Value: proj.Spec.ProductId},
								{Name: "LLM_ASSUMPTION_ID", Value: assumptionID},
								// OpenAI API key from Kubernetes Secret (key: api-key).
								{
									Name: "OPENAI_API_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: "openai-api-key"},
											Key:                  "api-key",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if job.Spec.Template.Spec.Containers[0].Image == "" {
		job.Spec.Template.Spec.Containers[0].Image = "python:3.11-slim"
	}

	if err := ctrl.SetControllerReference(proj, job, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, job); err != nil {
		return err
	}

	log.Info("created LLM assumptions Job", "job", jobName, "docPrefix", docPrefix, "assumptionId", assumptionID)
	return nil
}

// ensureIllustrationJob creates or updates a Kubernetes Job responsible for
// running the illustration pipeline for a given project. It also maps the Job
// status back to the IllustrationProject status.
func (r *IllustrationProjectReconciler) ensureIllustrationJob(
	ctx context.Context,
	log logr.Logger,
	productCfg *ProductConfig,
	proj *illustrationsv1alpha1.IllustrationProject,
	filingsPrefix, policiesPrefix, projectionsPrefix string,
) (ctrl.Result, error) {
	jobName := fmt.Sprintf("illustration-%s", proj.Name)
	runID := string(proj.UID)
	nn := types.NamespacedName{Name: jobName, Namespace: proj.Namespace}
	job := &batchv1.Job{}

	// Choose deterministic, run-specific object keys so we can both write
	// artefacts from the runner and surface them in status without exposing
	// any raw PAS data.
	projectionObject := fmt.Sprintf("projections/%s/%s/projection.json", proj.Spec.ProductId, runID)
	auditObject := fmt.Sprintf("audit/%s/%s/audit.json", proj.Spec.ProductId, runID)
	inputSnapshotObject := fmt.Sprintf("audit/%s/%s/inputs.json", proj.Spec.ProductId, runID)

	if err := r.Get(ctx, nn, job); err != nil {
		if !kerrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		// Create a new Job for this project.
		job = &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      jobName,
				Namespace: proj.Namespace,
				Labels: map[string]string{
					"app":                         "illustration-runner",
					"illustrations.poc/project":   proj.Name,
					"illustrations.poc/productId": proj.Spec.ProductId,
				},
			},
			Spec: batchv1.JobSpec{
				BackoffLimit: int32Ptr(0),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Volumes: func() []corev1.Volume {
							if proj.Spec.PasConfigMap == "" {
								return nil
							}
							return []corev1.Volume{
								{
									Name: "pas-config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{Name: proj.Spec.PasConfigMap},
											Items:                []corev1.KeyToPath{{Key: "pas.json", Path: "pas.json"}},
										},
									},
								},
							}
						}(),
						Containers: []corev1.Container{
							{
								Name:            "runner",
								Image:           os.Getenv("ILLUSTRATION_RUNNER_IMAGE"),
								ImagePullPolicy: corev1.PullAlways,
								Command:         []string{"/bin/sh", "-c"},
								Args: []string{
									"set -euo pipefail; " +
										"echo 'Starting illustration run for ' $PRODUCT_ID ' project ' $PROJECT_NAME; " +
										"echo 'FILINGS_PREFIX=' $FILINGS_PREFIX ' POLICIES_PREFIX=' $POLICIES_PREFIX ' PROJECTIONS_PREFIX=' $PROJECTIONS_PREFIX; " +
										"cd /opt/dagster/app; " +
										"python -m actuarypoc.cli.main project-minio",
								},
								Env: func() []corev1.EnvVar {
									vars := []corev1.EnvVar{
										{Name: "PRODUCT_ID", Value: proj.Spec.ProductId},
										{Name: "PROJECT_NAME", Value: proj.Name},
										{Name: "RUN_ID", Value: runID},
										{Name: "FILINGS_PREFIX", Value: filingsPrefix},
										{Name: "POLICIES_PREFIX", Value: policiesPrefix},
										{Name: "PROJECTIONS_PREFIX", Value: projectionsPrefix},
										// Ensure Python can import actuarypoc from the source tree in the runner image.
									{Name: "PYTHONPATH", Value: "/opt/dagster/app/src"},
										// Projection artefact location for the runner → MinIO,
										// and surfaced back on IllustrationProject.Status.
										{Name: "PROJECTION_OBJECT_NAME", Value: projectionObject},
										// Additional audit + input snapshot artefacts.
										{Name: "AUDIT_OBJECT_NAME", Value: auditObject},
										{Name: "INPUT_SNAPSHOT_OBJECT_NAME", Value: inputSnapshotObject},
										// MinIO configuration for connectors → MinIO ingestion.
										{Name: "MINIO_ENDPOINT", Value: "minio.minio-system.svc.cluster.local:9000"},
										{Name: "MINIO_ACCESS_KEY", Value: "admin"},
										{Name: "MINIO_SECRET_KEY", Value: "password"},
										{Name: "MINIO_BUCKET", Value: "illuminet"},
										{Name: "MINIO_SECURE", Value: "false"},
									}
									// Inject Postgres DSN from secret when available so that the
									// runner can record illustration_runs metadata in SQL.
									vars = append(vars, corev1.EnvVar{
										Name: "POSTGRES_DSN",
										ValueFrom: &corev1.EnvVarSource{
											SecretKeyRef: &corev1.SecretKeySelector{
												LocalObjectReference: corev1.LocalObjectReference{Name: "postgres-secret"},
												Key:                  "POSTGRES_DSN",
											},
										},
									})
									if proj.Spec.PasConfigMap != "" {
										vars = append(vars, corev1.EnvVar{Name: "PAS_JSON_PATH", Value: "/config/pas/pas.json"})
									}
									return vars
								}(),
								VolumeMounts: func() []corev1.VolumeMount {
									if proj.Spec.PasConfigMap == "" {
										return nil
									}
									return []corev1.VolumeMount{
										{
											Name:      "pas-config",
											MountPath: "/config/pas",
											ReadOnly:  true,
										},
									}
								}(),
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("100m"),
										corev1.ResourceMemory: resource.MustParse("128Mi"),
									},
								},
							},
						},
					},
				},
			},
		}

		// Record the planned artefact locations in status before we launch the
		// Job. On success the runner will have written to these keys.
		proj.Status.Phase = "Running"
		proj.Status.LastRunID = runID
		proj.Status.LastError = ""
		proj.Status.ProjectionObject = projectionObject
		proj.Status.AuditObject = auditObject
		proj.Status.InputSnapshotObject = inputSnapshotObject
		proj.Status.EngineVersion = os.Getenv("ILLUSTRATION_ENGINE_VERSION")
		proj.Status.RunnerImage = job.Spec.Template.Spec.Containers[0].Image
		if err := r.Status().Update(ctx, proj); err != nil {
			return ctrl.Result{}, err
		}

		if job.Spec.Template.Spec.Containers[0].Image == "" {
			job.Spec.Template.Spec.Containers[0].Image = "python:3.11-slim"
		}

		if err := ctrl.SetControllerReference(proj, job, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}

		if err := r.Create(ctx, job); err != nil {
			return ctrl.Result{}, err
		}
		jobsCreated.WithLabelValues(proj.Spec.ProductId).Inc()

		log.Info("created illustration Job", "job", jobName)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Job already exists; inspect its status.
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			proj.Status.Phase = "Failed"
			proj.Status.LastRunID = runID
			msg := cond.Message
			if msg == "" {
				msg = cond.Reason
			}
			proj.Status.LastError = msg
			if err := r.Status().Update(ctx, proj); err != nil {
				return ctrl.Result{}, err
			}
			jobFailures.WithLabelValues(proj.Spec.ProductId).Inc()
			return ctrl.Result{}, nil
		}
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			proj.Status.Phase = "Succeeded"
			proj.Status.LastRunID = runID
			proj.Status.LastError = ""
			if err := r.Status().Update(ctx, proj); err != nil {
				return ctrl.Result{}, err
			}
			if job.Status.StartTime != nil {
				dur := time.Since(job.Status.StartTime.Time).Seconds()
				illustrationDurations.WithLabelValues(proj.Spec.ProductId).Observe(dur)
			}
			return ctrl.Result{}, nil
		}
	}

	// Job is still running or pending; mark project as Running and requeue.
	proj.Status.Phase = "Running"
	proj.Status.LastRunID = job.Name
	proj.Status.LastError = ""
	if err := r.Status().Update(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func int32Ptr(v int32) *int32 { return &v }
