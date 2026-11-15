// Package remediation decides what an agent may do about a triaged alert.
//
// A remediation agent proposes one action; the executor runs it only if the
// proposal passes Skill.Check: the action is one the skill allows, it targets
// the alert's own object, it stays within the skill's constraints, and it never
// touches a protected namespace. Whether a human must approve it first comes
// from Skill.ApprovalFor.
package remediation

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"k8s-agent-sandbox/internal/skills"
)

// ActionType is a kind of remediation the executor knows how to perform.
type ActionType string

// Supported actions. Anything else is rejected.
const (
	ActionRestartPod     ActionType = "restart-pod"
	ActionRolloutRestart ActionType = "rollout-restart"
	ActionScale          ActionType = "scale"
	ActionOpenPR         ActionType = "open-pr" // driver deferred; dry-run until configured
)

var knownActions = []ActionType{ActionRestartPod, ActionRolloutRestart, ActionScale, ActionOpenPR}

// ApprovalMode says whether an action runs immediately or waits for a human.
type ApprovalMode string

// Approval modes.
const (
	ApprovalAuto     ApprovalMode = "auto"
	ApprovalRequired ApprovalMode = "required"
)

// defaultApproval: restarts are low risk and reversible; scaling and code
// changes need a human.
var defaultApproval = map[ActionType]ApprovalMode{
	ActionRestartPod:     ApprovalAuto,
	ActionRolloutRestart: ApprovalAuto,
	ActionScale:          ApprovalRequired,
	ActionOpenPR:         ApprovalRequired,
}

// ProtectedNamespaces are never acted on, whatever a skill allows: the cluster
// system and the triage system itself.
var ProtectedNamespaces = []string{
	"kube-system", "kube-public", "kube-node-lease", "tigera-operator", "calico-system",
	"agent-sandbox-system", "alert-triage", "agent-sandboxes", "llm-proxy", "egress",
	"mcp-gateway", "signoz", "k8s-infra",
}

// Errors: ErrInvalid for malformed input, ErrNotAllowed for well-formed
// requests the policy refuses.
var (
	ErrInvalid    = errors.New("invalid remediation")
	ErrNotAllowed = errors.New("remediation not allowed")
)

// Constraints bound where and how far an action may go.
type Constraints struct {
	Namespaces  []string `json:"namespaces"`            // globs, e.g. "shop-*"
	MaxReplicas int32    `json:"maxReplicas,omitempty"` // scale only
}

// AllowedAction is one action a skill permits, with its limits.
type AllowedAction struct {
	Type        ActionType  `json:"type"`
	Constraints Constraints `json:"constraints"`
}

// Skill is a RemediationSkill: a playbook plus the actions it may propose.
type Skill struct {
	Name           string              `json:"name"`
	Description    string              `json:"description"`
	Match          map[string][]string `json:"match,omitempty"`
	Instructions   string              `json:"instructions"`
	Approval       *ApprovalMode       `json:"approval,omitempty"` // overrides the per-action default
	AllowedActions []AllowedAction     `json:"allowedActions"`
}

// Target is the object an action applies to.
type Target struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

func (t Target) String() string { return t.Namespace + "/" + t.Kind + "/" + t.Name }

// Params are action-specific inputs.
type Params struct {
	Replicas *int32 `json:"replicas,omitempty"` // scale
}

// Proposal is what a remediation agent asks the executor to do.
type Proposal struct {
	Action    ActionType `json:"action"`
	Target    Target     `json:"target"`
	Params    Params     `json:"params"`
	Rationale string     `json:"rationale"`
}

// Validate checks the skill definition itself.
func (s Skill) Validate() error {
	var errs []error
	base := skills.Skill{Name: s.Name, Description: s.Description, Match: s.Match, Instructions: s.Instructions}
	if err := base.Validate(); err != nil {
		errs = append(errs, err)
	}
	if s.Approval != nil && *s.Approval != ApprovalAuto && *s.Approval != ApprovalRequired {
		errs = append(errs, fmt.Errorf("approval must be auto or required, got %q", *s.Approval))
	}
	if len(s.AllowedActions) == 0 {
		errs = append(errs, errors.New("allowedActions must list at least one action"))
	}
	for i, a := range s.AllowedActions {
		if !slices.Contains(knownActions, a.Type) {
			errs = append(errs, fmt.Errorf("allowedActions[%d]: unknown action %q", i, a.Type))
		}
		if len(a.Constraints.Namespaces) == 0 {
			errs = append(errs, fmt.Errorf("allowedActions[%d]: constraints.namespaces is required", i))
		}
		for _, ns := range a.Constraints.Namespaces {
			if _, err := path.Match(ns, ""); err != nil {
				errs = append(errs, fmt.Errorf("allowedActions[%d]: bad namespace pattern %q", i, ns))
			}
		}
		if a.Type == ActionScale && a.Constraints.MaxReplicas <= 0 {
			errs = append(errs, fmt.Errorf("allowedActions[%d]: scale needs constraints.maxReplicas > 0", i))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: skill %q: %w", ErrInvalid, s.Name, errors.Join(errs...))
	}
	return nil
}

// Check decides whether p may run under this skill. alertTarget is the object
// the triaged alert is about; a proposal may act on nothing else.
func (s Skill) Check(p Proposal, alertTarget Target) error {
	if !slices.Contains(knownActions, p.Action) {
		return fmt.Errorf("%w: unknown action %q", ErrInvalid, p.Action)
	}
	if strings.TrimSpace(p.Rationale) == "" {
		return fmt.Errorf("%w: rationale is required", ErrInvalid)
	}
	if p.Action == ActionScale && p.Params.Replicas == nil {
		return fmt.Errorf("%w: scale needs params.replicas", ErrInvalid)
	}
	if slices.Contains(ProtectedNamespaces, p.Target.Namespace) {
		return fmt.Errorf("%w: namespace %s is protected", ErrNotAllowed, p.Target.Namespace)
	}
	if p.Target != alertTarget {
		return fmt.Errorf("%w: target %s is not the alert's target %s", ErrNotAllowed, p.Target, alertTarget)
	}
	i := slices.IndexFunc(s.AllowedActions, func(a AllowedAction) bool { return a.Type == p.Action })
	if i < 0 {
		return fmt.Errorf("%w: action %s is not allowed by skill %s", ErrNotAllowed, p.Action, s.Name)
	}
	if !actionFits(p.Action, p.Target.Kind) {
		return fmt.Errorf("%w: action %s does not apply to a %s", ErrNotAllowed, p.Action, p.Target.Kind)
	}
	c := s.AllowedActions[i].Constraints
	if !slices.ContainsFunc(c.Namespaces, func(pattern string) bool {
		ok, err := path.Match(pattern, p.Target.Namespace)
		return err == nil && ok
	}) {
		return fmt.Errorf("%w: namespace %s is outside skill %s's namespaces", ErrNotAllowed, p.Target.Namespace, s.Name)
	}
	if p.Action == ActionScale {
		if r := *p.Params.Replicas; r < 0 || r > c.MaxReplicas {
			return fmt.Errorf("%w: replicas %d exceeds maxReplicas %d", ErrNotAllowed, r, c.MaxReplicas)
		}
	}
	return nil
}

// ApprovalFor returns whether action needs human approval under this skill.
func (s Skill) ApprovalFor(action ActionType) ApprovalMode {
	if s.Approval != nil {
		return *s.Approval
	}
	if m, ok := defaultApproval[action]; ok {
		return m
	}
	return ApprovalRequired
}
