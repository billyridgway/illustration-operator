package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// real scheduler (CronJob or Dagster) can consume it later.
	RunPolicy *RunPolicy `json:"runPolicy,omitempty"`

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

// IllustrationProjectStatus defines the observed state of IllustrationProject.

type IllustrationProjectStatus struct {
	// Phase is a coarse state indicator, e.g. Pending, Running, Succeeded, Failed.
	Phase string `json:"phase,omitempty"`

	// LastRunTime records when the last illustration run was started/completed.
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`

	// LastRunID can hold an external run identifier (e.g. Dagster run id,
	// Kubernetes Job name, etc.).
	LastRunID string `json:"lastRunId,omitempty"`

	// LastError contains a human-readable error message from the most recent
	// failed run, if any.
	LastError string `json:"lastError,omitempty"`
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
