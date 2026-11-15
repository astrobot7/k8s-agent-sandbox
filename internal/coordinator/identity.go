// Package coordinator moves each Incident through its steps and gives every
// step its own short-lived identity: a ServiceAccount, a Role pinned to that
// step's objects, and a RoleBinding, deleted as soon as the step ends.
package coordinator

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/remediation"
)

// Step names: the StepLabel value and the name prefix of each step's identity.
const (
	StepTriage    = "triage"
	StepRemediate = "remediate"
)

// identityPrefixes are the only names a step identity may have. The admission
// policy in deploy/coordinator enforces the same list.
var identityPrefixes = []string{StepTriage + "-", StepRemediate + "-"}

// ErrRuleNotAllowed means a generated Role would grant more than a step
// identity may hold.
var ErrRuleNotAllowed = errors.New("rule not allowed for a step identity")

// Identity is the ServiceAccount, Role, and RoleBinding of one step. All
// three share a name and are owned by the step's resource, so deleting the
// step removes them even if the coordinator never gets to.
type Identity struct {
	ServiceAccount *corev1.ServiceAccount
	Role           *rbacv1.Role
	RoleBinding    *rbacv1.RoleBinding
}

// IdentityName is the shared name of a triage run's identity objects.
func IdentityName(run string) string { return StepTriage + "-" + run }

// TriageIdentity is the identity for one TriageRun: it may fetch its own
// assignment and submit its own report (get/update on its TriageRun only) and
// call one model through the llm-proxy.
func TriageIdentity(run *v1alpha1.TriageRun) Identity {
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{identity.Group}, Resources: []string{"triageruns"},
			ResourceNames: []string{run.Name}, Verbs: []string{"get", "update"}},
		{APIGroups: []string{identity.Group}, Resources: []string{"models"},
			ResourceNames: []string{run.Spec.Model}, Verbs: []string{"use"}},
	}
	owner := metav1.NewControllerRef(run, v1alpha1.GroupVersion.WithKind("TriageRun"))
	labels := map[string]string{v1alpha1.IncidentLabel: run.Spec.Incident, v1alpha1.StepLabel: StepTriage}
	return stepIdentity(run.Spec.ServiceAccount, run.Namespace, *owner, labels, rules)
}

// RemediationIdentityName is the shared name of a remediation's identity objects.
func RemediationIdentityName(incident string) string { return StepRemediate + "-" + incident }

// RemediationIdentity is the identity for one Remediation: it may fetch its
// assignment and submit its proposal (get/update on its Remediation only),
// call one model, and propose exactly the actions its skill allows on its
// target (remediation.RunRules). The executor checks the last with a
// SubjectAccessReview before it acts.
func RemediationIdentity(rem *v1alpha1.Remediation) Identity {
	skill := remediation.SkillFrom(rem.Spec.SkillName, rem.Spec.Skill)
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{identity.Group}, Resources: []string{"remediations"},
			ResourceNames: []string{rem.Name}, Verbs: []string{"get", "update"}},
		{APIGroups: []string{identity.Group}, Resources: []string{"models"},
			ResourceNames: []string{rem.Spec.Model}, Verbs: []string{"use"}},
	}
	rules = append(rules, remediation.RunRules(skill, remediation.TargetFrom(rem.Spec.Target))...)
	owner := metav1.NewControllerRef(rem, v1alpha1.GroupVersion.WithKind("Remediation"))
	labels := map[string]string{v1alpha1.IncidentLabel: rem.Spec.Incident, v1alpha1.StepLabel: StepRemediate}
	return stepIdentity(rem.Spec.ServiceAccount, rem.Namespace, *owner, labels, rules)
}

func stepIdentity(name, namespace string, owner metav1.OwnerReference, labels map[string]string, rules []rbacv1.PolicyRule) Identity {
	meta := func() metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{owner}}
	}
	no := false
	return Identity{
		// The agent never calls the Kubernetes API; its only token is the
		// projected, audience-bound one the sandbox mounts explicitly.
		ServiceAccount: &corev1.ServiceAccount{ObjectMeta: meta(), AutomountServiceAccountToken: &no},
		Role:           &rbacv1.Role{ObjectMeta: meta(), Rules: rules},
		RoleBinding: &rbacv1.RoleBinding{ObjectMeta: meta(),
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: namespace}}},
	}
}

// Allowed checks an identity against the rules the admission policy enforces
// on the coordinator: a step name prefix, only the triage API group, no
// wildcards, every rule pinned by resourceNames, and a binding to its own Role
// and ServiceAccount. The coordinator checks before creating anything.
func (id Identity) Allowed() error {
	name := id.Role.Name
	if !slices.ContainsFunc(identityPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
		return fmt.Errorf("%w: name %q lacks a step prefix", ErrRuleNotAllowed, name)
	}
	if len(id.Role.Rules) == 0 {
		return fmt.Errorf("%w: role %s has no rules", ErrRuleNotAllowed, name)
	}
	for i, r := range id.Role.Rules {
		switch {
		case !slices.Equal(r.APIGroups, []string{identity.Group}):
			return fmt.Errorf("%w: rule %d names API groups %v", ErrRuleNotAllowed, i, r.APIGroups)
		case len(r.ResourceNames) == 0:
			return fmt.Errorf("%w: rule %d is not pinned by resourceNames", ErrRuleNotAllowed, i)
		case len(r.NonResourceURLs) > 0:
			return fmt.Errorf("%w: rule %d has non-resource URLs", ErrRuleNotAllowed, i)
		case slices.Contains(r.Verbs, "*") || slices.Contains(r.Resources, "*") || slices.Contains(r.ResourceNames, "*"):
			return fmt.Errorf("%w: rule %d uses a wildcard", ErrRuleNotAllowed, i)
		}
	}
	rb := id.RoleBinding
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != name {
		return fmt.Errorf("%w: binding %s must reference Role %s", ErrRuleNotAllowed, rb.Name, name)
	}
	want := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: id.Role.Namespace}
	if len(rb.Subjects) != 1 || rb.Subjects[0] != want {
		return fmt.Errorf("%w: binding %s must have the single subject %s/%s", ErrRuleNotAllowed, rb.Name, want.Namespace, want.Name)
	}
	return nil
}
