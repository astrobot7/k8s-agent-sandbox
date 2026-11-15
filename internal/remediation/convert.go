package remediation

import (
	"cmp"
	"slices"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/skills"
)

// SkillFrom converts a RemediationSkill spec to the policy type.
func SkillFrom(name string, spec v1alpha1.RemediationSkillSpec) Skill {
	s := Skill{Name: name, Description: spec.Description, Match: spec.Match, Instructions: spec.Instructions}
	if spec.Approval != "" {
		m := ApprovalMode(spec.Approval)
		s.Approval = &m
	}
	for _, a := range spec.AllowedActions {
		s.AllowedActions = append(s.AllowedActions, AllowedAction{
			Type:        ActionType(a.Type),
			Constraints: Constraints{Namespaces: a.Constraints.Namespaces, MaxReplicas: a.Constraints.MaxReplicas},
		})
	}
	return s
}

// TargetFrom converts an API target.
func TargetFrom(t v1alpha1.RemediationTarget) Target {
	return Target{Namespace: t.Namespace, Kind: t.Kind, Name: t.Name}
}

// ProposalFrom builds the proposal the executor checks from a stored one. The
// target is always the remediation's own: the agent cannot name another.
func ProposalFrom(p v1alpha1.RemediationProposal, target v1alpha1.RemediationTarget) Proposal {
	return Proposal{Action: ActionType(p.Action), Target: TargetFrom(target),
		Params: Params{Replicas: p.Replicas}, Rationale: p.Rationale}
}

// Choose picks the skill for an alert: the first valid skill, by name, whose
// match selects the labels and that allows at least one action on target.
func Choose(list []v1alpha1.RemediationSkill, labels map[string]string, target Target) (Skill, bool) {
	sorted := slices.Clone(list)
	slices.SortFunc(sorted, func(a, b v1alpha1.RemediationSkill) int { return cmp.Compare(a.Name, b.Name) })
	for _, item := range sorted {
		s := SkillFrom(item.Name, item.Spec)
		base := skills.Skill{Name: s.Name, Match: s.Match}
		if s.Validate() != nil || !base.Matches(labels) || len(s.ActionsFor(target)) == 0 {
			continue
		}
		return s, true
	}
	return Skill{}, false
}

// ActionsFor lists the actions the skill allows on target, in skill order:
// the target's namespace is in the action's constraints, the namespace is not
// protected, and the action applies to the target's kind.
func (s Skill) ActionsFor(target Target) []AllowedAction {
	if slices.Contains(ProtectedNamespaces, target.Namespace) {
		return nil
	}
	var out []AllowedAction
	for _, a := range s.AllowedActions {
		if namespaceAllowed(a.Constraints.Namespaces, target.Namespace) && actionFits(a.Type, target.Kind) {
			out = append(out, a)
		}
	}
	return out
}

// actionFits says whether an action applies to a kind of object.
func actionFits(a ActionType, kind string) bool {
	switch a {
	case ActionRestartPod:
		return kind == "Pod"
	case ActionRolloutRestart:
		return kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet"
	case ActionScale:
		return kind == "Deployment" || kind == "StatefulSet"
	case ActionOpenPR:
		return kind != "Pod"
	}
	return false
}
