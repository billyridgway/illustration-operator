package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// IllustrationProjectSpec defines the desired state of IllustrationProject.
//
// The CRD is intentionally minimal: the operator derives most details from
// the product registry and MinIO prefix conventions.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=illustrationprojects,scope=Namespaced,shortName=ilproj
// +kubebuilder:printcolumn:name="Product",type=string,JSONPath=`.spec.productId`
// +kubebuilder:printcolumn:name="Horizon",type=integer,JSONPath=`.spec.horizonYears`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

type IllustrationProjectSpec struct {
	// ProductId is the logical product identifier (must exist in the
	// product registry).
	// +kubebuilder:validation:MinLength=1
	ProductId string `json:"productId"`

	// HorizonYears is the projection horizon in years.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=40
	HorizonYears int32 `json:"horizonYears,omitempty"`

	// Mode controls whether this project is adhoc or scheduled.
	// +kubebuilder:validation:Enum=adhoc;scheduled
	// +kubebuilder:default=adhoc
	Mode string `json:"mode,omitempty"`

	// RunPolicy contains optional scheduling / concurrency hints for
	// the operator. For now this is informational; integration with a
	// real scheduler (CronJob or another controller) can consume it later.
	RunPolicy *RunPolicy `json:"runPolicy,omitempty"`

	// PasConfigMap optionally names a ConfigMap in the same namespace
	// that contains PAS policy records under the key "pas.json".
	// When set, the runner mounts this ConfigMap and the projection
	// service reads PAS JSON from PAS_JSON_PATH instead of MinIO
	// pas_export/ prefixes.
	PasConfigMap string `json:"pasConfigMap,omitempty"`

	// Notes is free-form text for humans.
	Notes string `json:"notes,omitempty"`
}

// RunPolicy captures basic scheduling hints.
type RunPolicy struct {
	// Cron schedule in cluster-local time (optional).
	Schedule string `json:"schedule,omitempty"`

	// ConcurrencyPolicy indicates how to handle overlapping runs.
	// Allowed values: "Allow", "Forbid", "Replace".
	Concurrency string `json:"concurrency,omitempty"`
}

// ResolvedRefs summarizes the concrete inputs and artefacts used for
// a projection run so that humans and UIs can see the wiring at a glance.
type ResolvedRefs struct {
	// ProductId is the logical product identifier (copied from spec).
	ProductId string `json:"productId,omitempty"`

	// PasConfigMap is the name of the ConfigMap providing PAS JSON, when set.
	PasConfigMap string `json:"pasConfigMap,omitempty"`

	// PasKey is the key inside the ConfigMap that contains PAS JSON (typically "pas.json").
	PasKey string `json:"pasKey,omitempty"`

	// DSLFile is the product DSL file selected from the product registry.
	DSLFile string `json:"dslFile,omitempty"`

	// AssumptionId is the logical AssumptionSet identifier the product is
	// expected to use (from product LLM config).
	AssumptionId string `json:"assumptionId,omitempty"`

	// FilingsPrefix is the MinIO prefix under which policy filings/docs live.
	FilingsPrefix string `json:"filingsPrefix,omitempty"`

	// DocPrefix is the prefix used by the LLM assumptions extractor, when configured.
	DocPrefix string `json:"docPrefix,omitempty"`
}

// IllustrationProjectStatus defines the observed state of IllustrationProject.

type IllustrationProjectStatus struct {
	// Phase is a coarse state indicator, e.g. Pending, Running, Succeeded, Failed.
	Phase string `json:"phase,omitempty"`

	// LastRunTime records when the last illustration run was started/completed.
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`

	// LastRunID can hold an external run identifier (e.g. Kubernetes Job
	// name or other orchestrator-specific id).
	LastRunID string `json:"lastRunId,omitempty"`

	// LastError contains a human-readable error message from the most recent
	// failed run, if any.
	LastError string `json:"lastError,omitempty"`

	// ProjectionObject holds the MinIO object key where the most recent
	// projection artifact was written (e.g. projections/p12trf/run-123.json).
	ProjectionObject string `json:"projectionObject,omitempty"`

	// AuditObject holds the MinIO object key for a sanitized audit artefact
	// describing the run (inputs, metadata, checks), separate from the
	// projection payload.
	AuditObject string `json:"auditObject,omitempty"`

	// InputSnapshotObject, when set, points at a MinIO object that captures a
	// lightweight snapshot of which input objects / versions were used for
	// the run (e.g. PAS export object name, actuarial table object name).
	InputSnapshotObject string `json:"inputSnapshotObject,omitempty"`

	// AssumptionSetId is the logical identifier of the AssumptionSet the run
	// is expected to use. For LLM-refreshed products this is typically the
	// id passed to the extractor (and later approved by a human).
	AssumptionSetId string `json:"assumptionSetId,omitempty"`

	// AssumptionApproved indicates whether the AssumptionSet referenced by
	// AssumptionSetId has been approved for use. The operator will not start
	// a projection run while this remains false for products that use LLM
	// extraction in the POC.
	AssumptionApproved bool `json:"assumptionApproved,omitempty"`

	// EngineVersion records the version of the projection engine that
	// produced the most recent run (when known).
	EngineVersion string `json:"engineVersion,omitempty"`

	// RunnerImage records the container image used for the illustration Job
	// that produced the most recent run (tag or digest when available).
	RunnerImage string `json:"runnerImage,omitempty"`

	// Resolved describes the concrete inputs and artefacts used for the
	// most recent run (product, PAS source, DSL, assumptions, filings).
	Resolved *ResolvedRefs `json:"resolved,omitempty"`
}

// +kubebuilder:object:root=true

// IllustrationProject is the Schema for the illustrationprojects API.
type IllustrationProject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IllustrationProjectSpec   `json:"spec,omitempty"`
	Status IllustrationProjectStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IllustrationProjectList contains a list of IllustrationProject.
type IllustrationProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IllustrationProject `json:"items"`
}

// DeepCopyObject implements runtime.Object for IllustrationProject.
func (in *IllustrationProject) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(IllustrationProject)
	*out = *in
	return out
}

// DeepCopyObject implements runtime.Object for IllustrationProjectList.
func (in *IllustrationProjectList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(IllustrationProjectList)
	*out = *in
	return out
}
