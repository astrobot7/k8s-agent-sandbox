package coordinator

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/identity"
)

// workload is payments-api in workloads: Deployment -> ReplicaSet -> Pod.
func workload() []client.Object {
	ctl := func(kind, name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, UID: types.UID(name), Controller: ptr.To(true)}}
	}
	return []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: "workloads"}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "payments-api-7d9", Namespace: "workloads",
			OwnerReferences: ctl("Deployment", "payments-api")}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "payments-api-7d9-x2x", Namespace: "workloads",
			OwnerReferences: ctl("ReplicaSet", "payments-api-7d9")}},
	}
}

func restartSkill() *v1alpha1.RemediationSkill {
	return &v1alpha1.RemediationSkill{ObjectMeta: metav1.ObjectMeta{Name: "crashloop-restart"},
		Spec: v1alpha1.RemediationSkillSpec{
			Description:  "restart a crashlooping workload",
			Match:        map[string][]string{"k8s.object.kind": {"Pod"}},
			Instructions: "restart it if the crash is transient",
			AllowedActions: []v1alpha1.RemediationAllowedAction{
				{Type: "rollout-restart", Constraints: v1alpha1.RemediationActionConstraints{Namespaces: []string{"workloads"}}},
				{Type: "restart-pod", Constraints: v1alpha1.RemediationActionConstraints{Namespaces: []string{"workloads"}}},
			},
		}}
}

// triagedIncident is a1, whose triage run succeeded with a report about the pod.
func triagedIncident() (*v1alpha1.Incident, *v1alpha1.TriageRun) {
	inc := newIncident("a1")
	inc.Spec.Alert.Labels = map[string]string{"alertname": "K8s warning events",
		"k8s.namespace.name": "workloads", "k8s.object.kind": "Pod", "k8s.object.name": "payments-api-7d9-x2x"}
	inc.Status = v1alpha1.IncidentStatus{Phase: v1alpha1.IncidentTriaging, TriageRun: "a1"}
	run := runFor("a1", v1alpha1.TriageSucceeded, t0.Add(5*time.Minute))
	run.Status.Report = &runtime.RawExtension{Raw: []byte(`{"summary":"db host does not resolve"}`)}
	return inc, run
}

func newRemediationHarness(t *testing.T, objs ...client.Object) *harness {
	t.Helper()
	h := newHarness(t, t0, objs...)
	h.r.Config.Remediation = true
	h.r.Config.RemediationTimeout = 10 * time.Minute
	return h
}

func TestSuccessfulTriageStartsRemediationOnTheWorkload(t *testing.T) {
	t.Parallel()
	inc, run := triagedIncident()
	id := TriageIdentity(run)
	objs := append(workload(), inc, run, restartSkill(), id.ServiceAccount, id.Role, id.RoleBinding)
	h := newRemediationHarness(t, objs...)
	h.reconcile(t, "a1")

	if h.identityExists(t, "a1") {
		t.Error("triage identity still exists: two step identities at once")
	}
	if !h.identityNamed(t, "remediate-a1") {
		t.Fatal("remediation identity not provisioned")
	}
	var rem v1alpha1.Remediation
	if err := h.c.Get(t.Context(), types.NamespacedName{Namespace: sbNS, Name: "a1"}, &rem); err != nil {
		t.Fatalf("remediation not created: %v", err)
	}
	want := v1alpha1.RemediationTarget{Namespace: "workloads", Kind: "Deployment", Name: "payments-api"}
	if rem.Spec.Target != want || rem.Spec.Suspend || rem.Spec.SkillName != "crashloop-restart" ||
		rem.Spec.ServiceAccount != "remediate-a1" || string(rem.Spec.Report.Raw) != `{"summary":"db host does not resolve"}` ||
		!rem.Spec.Deadline.Time.Equal(t0.Add(10*time.Minute)) {
		t.Errorf("remediation spec = %+v", rem.Spec)
	}
	ops := []string{"create Remediation", "create ServiceAccount", "create Role", "create RoleBinding", "patch Remediation"}
	if !slices.Equal(h.ops, ops) {
		t.Errorf("ops = %v, want %v", h.ops, ops)
	}
	var role rbacv1.Role
	if err := h.c.Get(t.Context(), types.NamespacedName{Namespace: sbNS, Name: "remediate-a1"}, &role); err != nil {
		t.Fatal(err)
	}
	// restart-pod does not apply to a Deployment, so only rollout-restart is granted.
	actionRule := rbacv1.PolicyRule{APIGroups: []string{identity.Group}, Resources: []string{"deployments"},
		ResourceNames: []string{"workloads.payments-api"}, Verbs: []string{"rollout-restart"}}
	if !slices.ContainsFunc(role.Rules, func(r rbacv1.PolicyRule) bool {
		return slices.Equal(r.Resources, actionRule.Resources) && slices.Equal(r.ResourceNames, actionRule.ResourceNames) &&
			slices.Equal(r.Verbs, actionRule.Verbs)
	}) {
		t.Errorf("role rules = %+v, want %+v among them", role.Rules, actionRule)
	}
	if got := h.incident(t, "a1").Status; got.Phase != v1alpha1.IncidentRemediating || got.Remediation != "a1" {
		t.Errorf("incident status = %+v", got)
	}
}

func TestTriageWithoutRemediationIsDone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		objs    []client.Object
		message string
	}{
		{"no matching skill", workload(), "no RemediationSkill matches"},
		{"target gone", []client.Object{restartSkill()}, "no target"},
		{"skill for another namespace", append(workload(), func() client.Object {
			s := restartSkill()
			for i := range s.Spec.AllowedActions {
				s.Spec.AllowedActions[i].Constraints.Namespaces = []string{"shop-*"}
			}
			return s
		}()), "no RemediationSkill matches"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inc, run := triagedIncident()
			h := newRemediationHarness(t, append(tc.objs, inc, run)...)
			h.reconcile(t, "a1")
			got := h.incident(t, "a1").Status
			if got.Phase != v1alpha1.IncidentDone || !strings.Contains(got.Message, tc.message) {
				t.Errorf("status = %+v, want Done mentioning %q", got, tc.message)
			}
			if err := h.c.Get(t.Context(), types.NamespacedName{Namespace: sbNS, Name: "a1"}, &v1alpha1.Remediation{}); !apierrors.IsNotFound(err) {
				t.Errorf("remediation created: %v", err)
			}
		})
	}
}

func remediationFor(phase v1alpha1.RemediationPhase, deadline time.Time) *v1alpha1.Remediation {
	return &v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: sbNS, UID: "uid-rem-a1"},
		Spec: v1alpha1.RemediationSpec{Incident: "a1", TriageRun: "a1", ServiceAccount: "remediate-a1", Model: "m",
			SkillName: "crashloop-restart", Skill: restartSkill().Spec,
			Target:   v1alpha1.RemediationTarget{Namespace: "workloads", Kind: "Deployment", Name: "payments-api"},
			Deadline: metav1.NewTime(deadline)},
		Status: v1alpha1.RemediationStatus{Phase: phase, Result: "rolled out a restart"},
	}
}

func TestRemediationOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rem     *v1alpha1.Remediation
		phase   v1alpha1.IncidentPhase // "" means still remediating
		revoked bool
	}{
		{"succeeded", remediationFor(v1alpha1.RemediationSucceeded, t0), v1alpha1.IncidentDone, true},
		{"agent chose no action", remediationFor(v1alpha1.RemediationNoAction, t0), v1alpha1.IncidentDone, true},
		{"failed", remediationFor(v1alpha1.RemediationFailed, t0), v1alpha1.IncidentFailed, true},
		{"agent stuck past deadline and grace", remediationFor(v1alpha1.RemediationRunning, t0.Add(-3*time.Minute)), v1alpha1.IncidentFailed, true},
		{"waiting for a human past the agent deadline", remediationFor(v1alpha1.RemediationAwaitingApproval, t0.Add(-time.Hour)), "", false},
		{"agent still deciding", remediationFor(v1alpha1.RemediationRunning, t0.Add(5*time.Minute)), "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inc := newIncident("a1")
			inc.Status = v1alpha1.IncidentStatus{Phase: v1alpha1.IncidentRemediating, TriageRun: "a1", Remediation: "a1"}
			id := RemediationIdentity(tc.rem)
			h := newRemediationHarness(t, inc, tc.rem, id.ServiceAccount, id.Role, id.RoleBinding)
			h.reconcile(t, "a1")
			got := h.incident(t, "a1").Status
			want := tc.phase
			if want == "" {
				want = v1alpha1.IncidentRemediating
			}
			if got.Phase != want {
				t.Errorf("phase = %s, want %s (%s)", got.Phase, want, got.Message)
			}
			if exists := h.identityNamed(t, "remediate-a1"); exists == tc.revoked {
				t.Errorf("remediation identity exists = %v, want %v", exists, !tc.revoked)
			}
		})
	}
}
