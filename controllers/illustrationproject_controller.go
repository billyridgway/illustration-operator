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
	"k8s.io/apimachinery/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
//   1. Load the IllustrationProject.
//   2. Look up product config from the product registry (config/products.yaml).
//   3. Derive MinIO prefixes from productId and registry (no prefixes in CRD).
//   4. Trigger/ensure an illustration run (TODO: Dagster / Job integration).
//   5. Update status.phase / lastRunTime / lastRunId / lastError.
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
									Name:  "runner",
									Image: os.Getenv("ILLUSTRATION_RUNNER_IMAGE"),
									ImagePullPolicy: corev1.PullAlways,
									Command: []string{"/bin/sh", "-c"},
									Args: []string{
										"set -euo pipefail; " +
											"echo 'Starting illustration run for ' $PRODUCT_ID ' project ' $PROJECT_NAME; " +
											"echo 'FILINGS_PREFIX=' $FILINGS_PREFIX ' POLICIES_PREFIX=' $POLICIES_PREFIX ' PROJECTIONS_PREFIX=' $PROJECTIONS_PREFIX; " +
											"cd /opt/dagster/app/src; " +
											"python -m actuarypoc.cli.main load-sample src/actuarypoc/sample_data/policies_p12trf.csv; " +
											"python -m actuarypoc.cli.main load-sample src/actuarypoc/sample_data/pas_export.csv; " +
											"python -m actuarypoc.cli.main load-sample src/actuarypoc/sample_data/actuarial_tables.csv; " +
											"python -m actuarypoc.cli.main load-sample src/actuarypoc/sample_data/actuarial_tables_term23.csv; " +
											"python -m actuarypoc.cli.main load-sample src/actuarypoc/sample_data/crm_accounts.csv; " +
											"python -m actuarypoc.cli.main load-sample src/actuarypoc/sample_data/rate_curves.csv; " +
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
