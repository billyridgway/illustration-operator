package controllers

import (
	context "context"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/logr"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// Trigger Dagster jobs for this product (ingestion + projection) via the
	// Dagster GraphQL API. This is intentionally generic: job names come from
	// the product registry, and prefixes are passed as tags so Dagster can
	// route work appropriately.
	runID, err := triggerDagsterForProject(ctx, log, &productCfg, proj, filingsPrefix, policiesPrefix, projectionsPrefix)
	if err != nil {
		log.Error(err, "failed to trigger Dagster run")
		return r.updateStatusError(ctx, proj, "Failed", err)
	}

	now := metav1.Now()
	proj.Status.Phase = "Succeeded"
	proj.Status.LastRunTime = &now
	proj.Status.LastRunID = runID
	proj.Status.LastError = ""

	if err := r.Status().Update(ctx, proj); err != nil {
		log.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
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

	Dagster struct {
		IngestionJobs []string `json:"ingestionJobs" yaml:"ingestionJobs"`
		ProjectionJob string   `json:"projectionJob" yaml:"projectionJob"`
	} `json:"dagster" yaml:"dagster"`
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

// triggerDagsterForProject calls Dagster's GraphQL API to launch the
// configured ingestion + projection jobs for a given product. It returns the
// run ID of the final projection job (if any), which is suitable for
// recording in IllustrationProject.status.lastRunId.
func triggerDagsterForProject(
	ctx context.Context,
	log logr.Logger,
	productCfg *ProductConfig,
	proj *illustrationsv1alpha1.IllustrationProject,
	filingsPrefix, policiesPrefix, projectionsPrefix string,
) (string, error) {
	baseURL := os.Getenv("DAGSTER_GRAPHQL_URL")
	if baseURL == "" {
		// Default in-cluster Dagster dev service; tweak as needed for your
		// environment.
		baseURL = "http://dagster-dev.illustrations-poc.svc.cluster.local:3030/graphql"
	}

	client := &http.Client{Timeout: 30 * time.Second}

	tags := []map[string]string{
		{"key": "illustrations.poc/productId", "value": proj.Spec.ProductId},
		{"key": "illustrations.poc/projectName", "value": proj.Name},
		{"key": "illustrations.poc/filingsPrefix", "value": filingsPrefix},
		{"key": "illustrations.poc/policiesPrefix", "value": policiesPrefix},
		{"key": "illustrations.poc/projectionsPrefix", "value": projectionsPrefix},
	}

	launch := func(jobName string) (string, error) {
		if jobName == "" {
			return "", fmt.Errorf("dagster job name is empty")
		}

		query := `mutation LaunchRun($jobName: String!, $tags: [PipelineTag!]) {
  launchRun(executionParams: {
    selector: { jobName: $jobName },
    runConfig: {},
    tags: $tags
  }) {
    __typename
    ... on LaunchRunSuccess { run { runId } }
    ... on PythonError { message }
    ... on LaunchRunFailure { message }
  }
}`

		payload := map[string]any{
			"query": query,
			"variables": map[string]any{
				"jobName": jobName,
				"tags":    tags,
			},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("dagster HTTP %d", resp.StatusCode)
		}

		var result struct {
			Data struct {
				LaunchRun struct {
					Typename string `json:"__typename"`
					Run      struct {
						RunID string `json:"runId"`
					} `json:"run"`
					Message string `json:"message"`
				} `json:"launchRun"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", err
		}

		if len(result.Errors) > 0 {
			return "", fmt.Errorf("dagster graphql error: %s", result.Errors[0].Message)
		}

		lr := result.Data.LaunchRun
		if lr.Typename != "LaunchRunSuccess" {
			if lr.Message != "" {
				return "", fmt.Errorf("dagster launchRun failed: %s", lr.Message)
			}
			return "", fmt.Errorf("dagster launchRun returned %s", lr.Typename)
		}

		if lr.Run.RunID == "" {
			return "", fmt.Errorf("dagster launchRun succeeded but runId empty")
		}

		log.Info("launched Dagster job", "job", jobName, "runId", lr.Run.RunID)
		return lr.Run.RunID, nil
	}

	// Run ingestion jobs first (if any), then projection job. We only record
	// the projection run ID in status for now.
	for _, job := range productCfg.Dagster.IngestionJobs {
		if _, err := launch(job); err != nil {
			return "", fmt.Errorf("launching ingestion job %q: %w", job, err)
		}
	}

	if productCfg.Dagster.ProjectionJob == "" {
		return "", fmt.Errorf("no projectionJob configured for product %q", proj.Spec.ProductId)
	}

	projRunID, err := launch(productCfg.Dagster.ProjectionJob)
	if err != nil {
		return "", fmt.Errorf("launching projection job %q: %w", productCfg.Dagster.ProjectionJob, err)
	}

	return fmt.Sprintf("dagster:%s", projRunID), nil
}
