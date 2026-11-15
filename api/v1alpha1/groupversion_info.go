// Package v1alpha1 contains the custom resources that carry an alert through
// its steps: Incident (one alert, owned by the coordinator) and TriageRun (one
// triage step, run by the triage controller).
//
// +kubebuilder:object:generate=true
// +groupName=triage.k8s-agent-sandbox.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version of these resources.
	GroupVersion = schema.GroupVersion{Group: "triage.k8s-agent-sandbox.io", Version: "v1alpha1"}

	// SchemeBuilder registers the types with a runtime.Scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
