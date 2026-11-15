package remediation

import (
	"path"
	"slices"
	"strings"

	authzv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	"k8s-agent-sandbox/internal/identity"
)

// resourceFor maps a target kind to the RBAC resource name used in the triage
// API group, e.g. Deployment -> deployments.
func resourceFor(kind string) string {
	return strings.ToLower(kind) + "s"
}

// ResourceName is how a target appears in RBAC rules and access reviews:
// "<namespace>.<name>". The Role lives in the sandbox namespace, not the
// target's, so the target's namespace must be part of the name. Namespaces
// cannot contain dots, so the form is unambiguous.
func ResourceName(t Target) string { return t.Namespace + "." + t.Name }

// RunRules are the RBAC rules for one remediation run's ServiceAccount: one
// rule per action the skill allows on the alert's target, pinned to that
// object by resourceNames (see ResourceName). Actions the skill cannot use there (protected or
// out-of-scope namespace) produce no rule, so the run gets no permission.
// The rules live in the triage API group; they authorize the executor's
// SubjectAccessReview, not direct Kubernetes API calls.
func RunRules(s Skill, target Target) []rbacv1.PolicyRule {
	var verbs []string
	for _, a := range s.ActionsFor(target) {
		if !slices.Contains(verbs, string(a.Type)) {
			verbs = append(verbs, string(a.Type))
		}
	}
	if len(verbs) == 0 {
		return nil
	}
	slices.Sort(verbs)
	return []rbacv1.PolicyRule{{
		APIGroups:     []string{identity.Group},
		Resources:     []string{resourceFor(target.Kind)},
		ResourceNames: []string{ResourceName(target)},
		Verbs:         verbs,
	}}
}

// AccessFor is the SubjectAccessReview the executor runs, as the run's
// ServiceAccount, before acting on a proposal. It matches RunRules exactly;
// roleNamespace is where the run's Role lives (the sandbox namespace).
func AccessFor(p Proposal, roleNamespace string) authzv1.ResourceAttributes {
	return authzv1.ResourceAttributes{
		Namespace: roleNamespace,
		Group:     identity.Group,
		Resource:  resourceFor(p.Target.Kind),
		Name:      ResourceName(p.Target),
		Verb:      string(p.Action),
	}
}

func namespaceAllowed(patterns []string, ns string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool {
		ok, err := path.Match(p, ns)
		return err == nil && ok
	})
}
