package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// RemediationAction is an action the remediation executor can perform.
// +kubebuilder:validation:Enum=restart-pod;rollout-restart;scale;open-pr
type RemediationAction string

// RemediationActionConstraints bound where and how far an action may go.
type RemediationActionConstraints struct {
	// Namespaces the action may touch, as globs ("shop-*"). Protected system
	// namespaces are refused whatever this says.
	// +kubebuilder:validation:MinItems=1
	Namespaces []string `json:"namespaces"`
	// MaxReplicas caps scale.
	// +optional
	MaxReplicas int32 `json:"maxReplicas,omitempty"`
}

// RemediationAllowedAction is one action a skill permits.
type RemediationAllowedAction struct {
	Type        RemediationAction            `json:"type"`
	Constraints RemediationActionConstraints `json:"constraints"`
}

// RemediationSkillSpec is a remediation playbook and the actions it may
// propose. It is trusted: grant write on remediationskills only to the people
// who own remediation.
type RemediationSkillSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Description string `json:"description"`
	// Match selects alerts by label, like TriageSkill.match. A skill without
	// match is never chosen.
	// +optional
	Match map[string][]string `json:"match,omitempty"`
	// Instructions tell the remediation agent when each action is right.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32768
	Instructions string `json:"instructions"`
	// Approval overrides the per-action default (restarts auto, the rest required).
	// +kubebuilder:validation:Enum=auto;required
	// +optional
	Approval string `json:"approval,omitempty"`
	// +kubebuilder:validation:MinItems=1
	AllowedActions []RemediationAllowedAction `json:"allowedActions"`
}

// RemediationSkill is one remediation playbook.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=rskill
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Approval",type=string,JSONPath=`.spec.approval`
type RemediationSkill struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RemediationSkillSpec `json:"spec"`
}

// RemediationSkillList is a list of remediation skills.
//
// +kubebuilder:object:root=true
type RemediationSkillList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RemediationSkill `json:"items"`
}

// RemediationTarget is the object an action applies to.
type RemediationTarget struct {
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:Enum=Pod;Deployment;StatefulSet;DaemonSet
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// RemediationPhase is the state of one remediation step.
// +kubebuilder:validation:Enum=Pending;Running;Proposed;AwaitingApproval;Succeeded;NoAction;Rejected;Failed
type RemediationPhase string

// Remediation phases.
const (
	RemediationPending          RemediationPhase = "Pending"          // waiting to launch
	RemediationRunning          RemediationPhase = "Running"          // the agent is deciding
	RemediationProposed         RemediationPhase = "Proposed"         // a valid proposal waits for the executor
	RemediationAwaitingApproval RemediationPhase = "AwaitingApproval" // a human must set spec.approval
	RemediationSucceeded        RemediationPhase = "Succeeded"        // the action ran
	RemediationNoAction         RemediationPhase = "NoAction"         // the agent chose not to act
	RemediationRejected         RemediationPhase = "Rejected"         // a human, cooldown, or the kill switch refused it
	RemediationFailed           RemediationPhase = "Failed"           // the agent or the action failed
)

// Terminal reports whether the remediation has finished.
func (p RemediationPhase) Terminal() bool {
	return p == RemediationSucceeded || p == RemediationNoAction || p == RemediationRejected || p == RemediationFailed
}

// Approval values a human writes to spec.approval.
const (
	ApprovalApproved = "Approved"
	ApprovalRejected = "Rejected"
)

// RemediationSpec is written by the coordinator, except Approval, which a
// human with patch on remediations sets.
type RemediationSpec struct {
	Incident string `json:"incident"`
	Alert    Alert  `json:"alert"`
	// TriageRun whose report this remediation acts on.
	TriageRun string `json:"triageRun"`
	// Report is the triage report, copied so the step does not depend on the run.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	Report runtime.RawExtension `json:"report"`
	// SkillName and Skill are the chosen RemediationSkill, copied so a later
	// edit cannot change what this step may do.
	SkillName string               `json:"skillName"`
	Skill     RemediationSkillSpec `json:"skill"`
	// Target is resolved by the coordinator from the alert; the run's Role is
	// pinned to it and the executor acts on nothing else.
	Target         RemediationTarget `json:"target"`
	ServiceAccount string            `json:"serviceAccount"`
	Model          string            `json:"model"`
	// Deadline for the agent's decision.
	Deadline metav1.Time `json:"deadline"`
	// +optional
	Suspend bool `json:"suspend,omitempty"`
	// Approval is set by a human for actions that need it.
	// +kubebuilder:validation:Enum=Approved;Rejected
	// +optional
	Approval string `json:"approval,omitempty"`
}

// RemediationProposal is the action the agent proposed.
type RemediationProposal struct {
	Action RemediationAction `json:"action"`
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
	// +kubebuilder:validation:MaxLength=4000
	Rationale string `json:"rationale"`
}

// RemediationStatus is written by the triage controller's remediation module.
type RemediationStatus struct {
	// +optional
	Phase RemediationPhase `json:"phase,omitempty"`
	// +optional
	Sandbox string `json:"sandbox,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	Proposal *RemediationProposal `json:"proposal,omitempty"`
	// ApprovalMode is "auto" or "required", from the skill.
	// +optional
	ApprovalMode string `json:"approvalMode,omitempty"`
	// ProposedAt starts the approval timeout.
	// +optional
	ProposedAt *metav1.Time `json:"proposedAt,omitempty"`
	// Result says what was done, or why nothing was.
	// +optional
	Result string `json:"result,omitempty"`
	// +optional
	ExecutedAt *metav1.Time `json:"executedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +optional
	Notified bool `json:"notified,omitempty"`
}

// Remediation is one remediation step for an incident: a sandboxed agent
// proposes at most one action, and the executor performs it once allowed.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rem
// +kubebuilder:printcolumn:name="Incident",type=string,JSONPath=`.spec.incident`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.name`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.status.proposal.action`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Remediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RemediationSpec   `json:"spec"`
	Status RemediationStatus `json:"status,omitempty"`
}

// RemediationList is a list of remediations.
//
// +kubebuilder:object:root=true
type RemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Remediation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RemediationSkill{}, &RemediationSkillList{}, &Remediation{}, &RemediationList{})
}
