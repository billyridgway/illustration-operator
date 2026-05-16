package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "illustrations.poc", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	// Register manually defined types here.
	SchemeBuilder.Register(&IllustrationProject{}, &IllustrationProjectList{})
}

// +kubebuilder:object:generate=true
// +groupName=illustrations.poc

// Type metadata constants for convenience.
var (
	IllustrationProjectKind     = "IllustrationProject"
	IllustrationProjectGVK      = GroupVersion.WithKind(IllustrationProjectKind)
	IllustrationProjectListKind = "IllustrationProjectList"
)
