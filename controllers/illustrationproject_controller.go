package controllers

import (
	context "context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"gopkg.in/yaml.v3"
	illustrationsv1alpha1 "illustration-operator/api/v1alpha1"
)

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
//  4. Trigger/ensure an illustration run (TODO: Dagster / Job integration).
//  5. Update status.phase / lastRunTime / lastRunId / lastError.
func (r *IllustrationProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
		return r.updateStatusError(ctx, proj, "Failed", err)
	}

	productCfg, ok := cfg.Products[proj.Spec.ProductId]
	if !ok {
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
					log.Info("LLM assumptions Job completed; proceeding to projection", "job", llmJobName)
					terminal = true
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
		}
	}

	// Ensure or create a Kubernetes Job to run the illustration pipeline for
	// this project, and map its status back onto the IllustrationProject.
	res, err := r.ensureIllustrationJob(ctx, log, &productCfg, proj, filingsPrefix, policiesPrefix, projectionsPrefix)
	if err != nil {
		log.Error(err, "failed to ensure illustration Job")
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
	nn := types.NamespacedName{Name: jobName, Namespace: proj.Namespace}
	job := &batchv1.Job{}

	// Choose a deterministic object key for the projection artifact so we can
	// both write it from the runner and surface it in status.
	projectionObject := fmt.Sprintf("%srun-%d.json", projectionsPrefix, time.Now().Unix())

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
										"python -m actuarypoc.cli.main load-sample /opt/dagster/app/src/actuarypoc/sample_data/policies_p12trf.csv; " +
										"python -m actuarypoc.cli.main load-sample /opt/dagster/app/src/actuarypoc/sample_data/pas_export.csv; " +
										"python -m actuarypoc.cli.main load-sample /opt/dagster/app/src/actuarypoc/sample_data/actuarial_tables.csv; " +
										"python -m actuarypoc.cli.main load-sample /opt/dagster/app/src/actuarypoc/sample_data/actuarial_tables_term23.csv; " +
										"python -m actuarypoc.cli.main load-sample /opt/dagster/app/src/actuarypoc/sample_data/crm_accounts.csv; " +
										"python -m actuarypoc.cli.main load-sample /opt/dagster/app/src/actuarypoc/sample_data/rate_curves.csv; " +
										"python -m actuarypoc.cli.main project-minio",
								},
								Env: []corev1.EnvVar{
									{Name: "PRODUCT_ID", Value: proj.Spec.ProductId},
									{Name: "PROJECT_NAME", Value: proj.Name},
									{Name: "FILINGS_PREFIX", Value: filingsPrefix},
									{Name: "POLICIES_PREFIX", Value: policiesPrefix},
									{Name: "PROJECTIONS_PREFIX", Value: projectionsPrefix},
									// Ensure Python can import actuarypoc from the source tree in the runner image.
									{Name: "PYTHONPATH", Value: "/opt/dagster/app/src"},
									// Projection artifact location for the runner → MinIO,
									// and surfaced back on IllustrationProject.Status.
									{Name: "PROJECTION_OBJECT_NAME", Value: projectionObject},
									// MinIO configuration for connectors → MinIO ingestion.
									{Name: "MINIO_ENDPOINT", Value: "minio.minio-system.svc.cluster.local:9000"},
									{Name: "MINIO_ACCESS_KEY", Value: "admin"},
									{Name: "MINIO_SECRET_KEY", Value: "password"},
									{Name: "MINIO_BUCKET", Value: "illuminet"},
									{Name: "MINIO_SECURE", Value: "false"},
								},
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

		// Record the planned projection object location in status before we
		// launch the Job. On success the runner will have written to this key.
		proj.Status.Phase = "Running"
		proj.Status.LastRunID = jobName
		proj.Status.LastError = ""
		proj.Status.ProjectionObject = projectionObject
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

		log.Info("created illustration Job", "job", jobName)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Job already exists; inspect its status.
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			proj.Status.Phase = "Failed"
			proj.Status.LastRunID = job.Name
			msg := cond.Message
			if msg == "" {
				msg = cond.Reason
			}
			proj.Status.LastError = msg
			if err := r.Status().Update(ctx, proj); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			proj.Status.Phase = "Succeeded"
			proj.Status.LastRunID = job.Name
			proj.Status.LastError = ""
			if err := r.Status().Update(ctx, proj); err != nil {
				return ctrl.Result{}, err
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
