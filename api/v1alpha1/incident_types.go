package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IncidentPhase is where an incident is in its steps.
// +kubebuilder:validation:Enum=Pending;Triaging;Remediating;Done;Failed
type IncidentPhase string

// Incident phases.
const (
	IncidentPending  IncidentPhase = "Pending"  // waiting for a free run slot
	IncidentTriaging IncidentPhase = "Triaging" // a TriageRun is in progress
	// IncidentRemediating: a Remediation is in progress.
	IncidentRemediating IncidentPhase = "Remediating"
	IncidentDone        IncidentPhase = "Done"   // every step finished
	IncidentFailed      IncidentPhase = "Failed" // a step failed or timed out
)

// Terminal reports whether no further step will run.
func (p IncidentPhase) Terminal() bool { return p == IncidentDone || p == IncidentFailed }

// Condition types on an incident.
const (
	// ConditionIdentityProvisioned is True while the current step's
	// ServiceAccount, Role, and RoleBinding exist.
	ConditionIdentityProvisioned = "IdentityProvisioned"
)

// IncidentSpec is the alert. The triage controller writes it once.
type IncidentSpec struct {
	Alert Alert `json:"alert"`
}

// IncidentStatus is written only by the coordinator.
type IncidentStatus struct {
	// +optional
	Phase IncidentPhase `json:"phase,omitempty"`
	// TriageRun is the name of the triage step, in the sandbox namespace.
	// +optional
	TriageRun string `json:"triageRun,omitempty"`
	// Remediation is the name of the remediation step, in the sandbox
	// namespace, once triage succeeded and a RemediationSkill applied.
	// +optional
	Remediation string `json:"remediation,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Incident is one firing alert and the steps taken for it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=inc
// +kubebuilder:printcolumn:name="Alert",type=string,JSONPath=`.spec.alert.labels.alertname`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Incident struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IncidentSpec   `json:"spec"`
	Status IncidentStatus `json:"status,omitempty"`
}

// IncidentList is a list of incidents.
//
// +kubebuilder:object:root=true
type IncidentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Incident `json:"items"`
}

func init() { SchemeBuilder.Register(&Incident{}, &IncidentList{}) }
