package remediation

import (
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"

	"k8s-agent-sandbox/internal/identity"
)

func TestRunRulesPinnedToTarget(t *testing.T) {
	t.Parallel()
	rules := RunRules(crashloopSkill(), alertTarget)
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want one rule", rules)
	}
	r := rules[0]
	if !slices.Equal(r.APIGroups, []string{identity.Group}) || !slices.Equal(r.Resources, []string{"deployments"}) ||
		!slices.Equal(r.ResourceNames, []string{"workloads.payments-api"}) || !slices.Equal(r.Verbs, []string{"rollout-restart", "scale"}) {
		t.Errorf("rule = %+v", r)
	}
}

func TestRunRulesOnlyActionsValidInTargetNamespace(t *testing.T) {
	t.Parallel()
	// scale is limited to "workloads"; rollout-restart also covers shop-*.
	shop := Target{Namespace: "shop-eu", Kind: "Deployment", Name: "cart"}
	rules := RunRules(crashloopSkill(), shop)
	if len(rules) != 1 || !slices.Equal(rules[0].Verbs, []string{"rollout-restart"}) {
		t.Errorf("rules in shop-eu = %+v, want only rollout-restart", rules)
	}
}

func TestRunRulesGrantNothingOutsideScope(t *testing.T) {
	t.Parallel()
	for _, target := range []Target{
		{Namespace: "billing", Kind: "Deployment", Name: "x"},     // outside the skill's namespaces
		{Namespace: "kube-system", Kind: "Deployment", Name: "x"}, // protected
	} {
		if rules := RunRules(crashloopSkill(), target); rules != nil {
			t.Errorf("RunRules(%s) = %+v, want none", target, rules)
		}
	}
	s := crashloopSkill()
	s.AllowedActions[0].Constraints.Namespaces = []string{"*"}
	if rules := RunRules(s, Target{Namespace: "alert-triage", Kind: "Deployment", Name: "x"}); rules != nil {
		t.Errorf("a wildcard skill still got rules in a protected namespace: %+v", rules)
	}
}

// The executor's access check must ask exactly what RunRules grants.
func TestAccessForMatchesRunRules(t *testing.T) {
	t.Parallel()
	rules := RunRules(crashloopSkill(), alertTarget)
	attrs := AccessFor(Proposal{Action: ActionScale, Target: alertTarget}, "agent-sandboxes")
	if !ruleAllows(rules, attrs.Group, attrs.Resource, attrs.Name, attrs.Verb) || attrs.Namespace != "agent-sandboxes" {
		t.Errorf("rules %+v do not allow %+v", rules, attrs)
	}
	other := AccessFor(Proposal{Action: ActionScale, Target: Target{Namespace: "workloads", Kind: "Deployment", Name: "other"}}, "agent-sandboxes")
	if ruleAllows(rules, other.Group, other.Resource, other.Name, other.Verb) {
		t.Error("rules allow a different object than the alert's target")
	}
	sameNameElsewhere := AccessFor(Proposal{Action: ActionScale, Target: Target{Namespace: "shop-eu", Kind: "Deployment", Name: "payments-api"}}, "agent-sandboxes")
	if ruleAllows(rules, sameNameElsewhere.Group, sameNameElsewhere.Resource, sameNameElsewhere.Name, sameNameElsewhere.Verb) {
		t.Error("rules allow an object with the target's name in another namespace")
	}
	restartPod := AccessFor(Proposal{Action: ActionRestartPod, Target: alertTarget}, "agent-sandboxes")
	if ruleAllows(rules, restartPod.Group, restartPod.Resource, restartPod.Name, restartPod.Verb) {
		t.Error("rules allow an action the skill does not list")
	}
}

// ruleAllows evaluates rules the way RBAC does for a namespaced resource request.
func ruleAllows(rules []rbacv1.PolicyRule, group, resource, name, verb string) bool {
	return slices.ContainsFunc(rules, func(r rbacv1.PolicyRule) bool {
		return slices.Contains(r.APIGroups, group) && slices.Contains(r.Resources, resource) &&
			(len(r.ResourceNames) == 0 || slices.Contains(r.ResourceNames, name)) && slices.Contains(r.Verbs, verb)
	})
}
