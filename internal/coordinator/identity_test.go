package coordinator

import (
	"errors"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/remediation"
)

func testRun() *v1alpha1.TriageRun {
	return &v1alpha1.TriageRun{
		ObjectMeta: metav1.ObjectMeta{Name: "ab12cd34ef", Namespace: "agent-sandboxes", UID: "uid-1"},
		Spec:       v1alpha1.TriageRunSpec{Incident: "ab12cd34ef", ServiceAccount: IdentityName("ab12cd34ef"), Model: "claude-sonnet-5-5"},
	}
}

func TestTriageIdentityIsPinnedToItsRun(t *testing.T) {
	t.Parallel()
	id := TriageIdentity(testRun())
	if err := id.Allowed(); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if id.ServiceAccount.Name != "triage-ab12cd34ef" || *id.ServiceAccount.AutomountServiceAccountToken {
		t.Errorf("service account = %+v", id.ServiceAccount)
	}
	want := map[string][]string{"triageruns": {"ab12cd34ef"}, "models": {"claude-sonnet-5-5"}}
	for _, r := range id.Role.Rules {
		if got := r.ResourceNames; len(got) != 1 || got[0] != want[r.Resources[0]][0] {
			t.Errorf("rule on %v pinned to %v", r.Resources, got)
		}
	}
	for _, o := range []metav1.Object{id.ServiceAccount, id.Role, id.RoleBinding} {
		refs := o.GetOwnerReferences()
		if len(refs) != 1 || refs[0].UID != "uid-1" || refs[0].Kind != "TriageRun" {
			t.Errorf("%s owner refs = %+v", o.GetName(), refs)
		}
		if o.GetLabels()[v1alpha1.IncidentLabel] != "ab12cd34ef" {
			t.Errorf("%s labels = %v", o.GetName(), o.GetLabels())
		}
	}
}

// Remediation identities (built from remediation.RunRules) must pass the same
// check, so the admission policy will not need to change when they land.
func TestRemediationRulesAreAllowed(t *testing.T) {
	t.Parallel()
	skill := remediation.Skill{Name: "crashloop-restart", AllowedActions: []remediation.AllowedAction{
		{Type: remediation.ActionRolloutRestart, Constraints: remediation.Constraints{Namespaces: []string{"workloads"}}},
	}}
	rules := remediation.RunRules(skill, remediation.Target{Namespace: "workloads", Kind: "Deployment", Name: "payments-api"})
	id := stepIdentity("remediate-ab12cd34ef", "agent-sandboxes", metav1.OwnerReference{}, nil, rules)
	if err := id.Allowed(); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
}

func TestAllowedRejectsBroadIdentities(t *testing.T) {
	t.Parallel()
	pinned := rbacv1.PolicyRule{APIGroups: []string{identity.Group}, Resources: []string{"triageruns"}, ResourceNames: []string{"x"}, Verbs: []string{"get"}}
	with := func(f func(*rbacv1.PolicyRule)) []rbacv1.PolicyRule {
		r := *pinned.DeepCopy()
		f(&r)
		return []rbacv1.PolicyRule{r}
	}
	tests := []struct {
		name string
		id   Identity
	}{
		{"core API group", stepIdentity("triage-x", "ns", metav1.OwnerReference{}, nil, with(func(r *rbacv1.PolicyRule) { r.APIGroups = []string{""}; r.Resources = []string{"pods"} }))},
		{"not pinned", stepIdentity("triage-x", "ns", metav1.OwnerReference{}, nil, with(func(r *rbacv1.PolicyRule) { r.ResourceNames = nil }))},
		{"wildcard verb", stepIdentity("triage-x", "ns", metav1.OwnerReference{}, nil, with(func(r *rbacv1.PolicyRule) { r.Verbs = []string{"*"} }))},
		{"no rules", stepIdentity("triage-x", "ns", metav1.OwnerReference{}, nil, nil)},
		{"name without step prefix", stepIdentity("controller", "ns", metav1.OwnerReference{}, nil, []rbacv1.PolicyRule{pinned})},
		{"binding to another subject", func() Identity {
			id := stepIdentity("triage-x", "ns", metav1.OwnerReference{}, nil, []rbacv1.PolicyRule{pinned})
			id.RoleBinding.Subjects[0].Name = "default"
			return id
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.id.Allowed(); !errors.Is(err, ErrRuleNotAllowed) {
				t.Errorf("Allowed = %v, want ErrRuleNotAllowed", err)
			}
		})
	}
}
