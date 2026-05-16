package controllers

import (
	context "context"
	"fmt"
	"os"
	"time"

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

	// TODO: Integrate with Dagster or a Kubernetes Job that runs the
	// actuarypoc CLI to:
	//   - ensure filings/policies/actuarial tables are present for this product
	//   - run a projection using the configured DSL file
	//   - write results under projectionsPrefix
	//   - return an external run identifier
	// For now we simply mark the project as Succeeded to validate the CRD
	// and product registry wiring.

	now := metav1.Now()
	proj.Status.Phase = "Succeeded"
	proj.Status.LastRunTime = &now
	proj.Status.LastRunID = fmt.Sprintf("noop-%d", time.Now().Unix())
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
		Kind         string `json:"kind" yaml:"kind"`
		TablePrefix  string `json:"tablePrefix" yaml:"tablePrefix"`
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
