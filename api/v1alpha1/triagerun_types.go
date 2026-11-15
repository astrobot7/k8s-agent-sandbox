package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TriageRunPhase is the state of one triage step.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type TriageRunPhase string

// TriageRun phases.
const (
	TriagePending   TriageRunPhase = "Pending"
	TriageRunning   TriageRunPhase = "Running"
	TriageSucceeded TriageRunPhase = "Succeeded"
	TriageFailed    TriageRunPhase = "Failed"
)

// Terminal reports whether the run has finished.
func (p TriageRunPhase) Terminal() bool { return p == TriageSucceeded || p == TriageFailed }

// TriageRunSpec is written only by the coordinator.
type TriageRunSpec struct {
	// Incident is the name of the owning Incident in the controller namespace.
	Incident string `json:"incident"`
	Alert    Alert  `json:"alert"`
	// ServiceAccount the sandbox runs as. The coordinator creates it with a
	// Role pinned to this run and deletes it when the run ends.
	ServiceAccount string `json:"serviceAccount"`
	// Model the agent asks the llm-proxy for; the run's Role allows only this one.
	Model string `json:"model"`
	// Deadline after which the triage controller fails the run.
	Deadline metav1.Time `json:"deadline"`
	// Suspend holds the run until the coordinator has provisioned its identity.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// TriageRunStatus is written only by the triage controller.
type TriageRunStatus struct {
	// +optional
	Phase TriageRunPhase `json:"phase,omitempty"`
	// +optional
	Sandbox string `json:"sandbox,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Report is the agent's validated triage report.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	Report *runtime.RawExtension `json:"report,omitempty"`
	// +optional
	Error string `json:"error,omitempty"`
	// Notified is set once the result was posted to on-call.
	// +optional
	Notified bool `json:"notified,omitempty"`
}

// TriageRun is one sandboxed triage of an incident's alert.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tr
// +kubebuilder:printcolumn:name="Incident",type=string,JSONPath=`.spec.incident`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Sandbox",type=string,JSONPath=`.status.sandbox`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type TriageRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TriageRunSpec   `json:"spec"`
	Status TriageRunStatus `json:"status,omitempty"`
}

// TriageRunList is a list of triage runs.
//
// +kubebuilder:object:root=true
type TriageRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TriageRun `json:"items"`
}

func init() { SchemeBuilder.Register(&TriageRun{}, &TriageRunList{}) }
