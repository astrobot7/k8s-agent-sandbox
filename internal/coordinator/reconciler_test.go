package coordinator

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"k8s-agent-sandbox/api/v1alpha1"
)

const (
	incNS = "alert-triage"
	sbNS  = "agent-sandboxes"
)

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newIncident(name string) *v1alpha1.Incident {
	return &v1alpha1.Incident{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: incNS},
		Spec: v1alpha1.IncidentSpec{Alert: v1alpha1.Alert{Fingerprint: "fp-" + name,
			Labels: map[string]string{"alertname": "K8s warning events"}}},
	}
}

func runFor(name string, phase v1alpha1.TriageRunPhase, deadline time.Time) *v1alpha1.TriageRun {
	return &v1alpha1.TriageRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sbNS, UID: types.UID("uid-" + name)},
		Spec: v1alpha1.TriageRunSpec{Incident: name, ServiceAccount: IdentityName(name), Model: "m",
			Deadline: metav1.NewTime(deadline)},
		Status: v1alpha1.TriageRunStatus{Phase: phase},
	}
}

type harness struct {
	r   *Reconciler
	c   client.Client
	ops []string // create/patch calls in order, for ordering checks
}

func newHarness(t *testing.T, now time.Time, objs ...client.Object) *harness {
	t.Helper()
	h := &harness{}
	h.c = fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&v1alpha1.Incident{}, &v1alpha1.TriageRun{}, &v1alpha1.Remediation{}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				h.ops = append(h.ops, "create "+kindOf(o))
				return c.Create(ctx, o, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
				h.ops = append(h.ops, "patch "+kindOf(o))
				return c.Patch(ctx, o, p, opts...)
			},
		}).
		Build()
	h.r = &Reconciler{Client: h.c, TargetReader: h.c, Now: func() time.Time { return now }, Config: Config{
		IncidentNamespace: incNS, SandboxNamespace: sbNS, Model: "claude-sonnet-5-5",
		RunTimeout: 15 * time.Minute, Grace: 2 * time.Minute, MaxConcurrent: 2, Retention: time.Hour,
	}}
	return h
}

func kindOf(o client.Object) string {
	switch o.(type) {
	case *v1alpha1.TriageRun:
		return "TriageRun"
	case *v1alpha1.Remediation:
		return "Remediation"
	case *corev1.ServiceAccount:
		return "ServiceAccount"
	case *rbacv1.Role:
		return "Role"
	case *rbacv1.RoleBinding:
		return "RoleBinding"
	}
	return "other"
}

func (h *harness) reconcile(t *testing.T, name string) ctrl.Result {
	t.Helper()
	res, err := h.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: incNS, Name: name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func (h *harness) incident(t *testing.T, name string) *v1alpha1.Incident {
	t.Helper()
	var inc v1alpha1.Incident
	if err := h.c.Get(t.Context(), types.NamespacedName{Namespace: incNS, Name: name}, &inc); err != nil {
		t.Fatalf("get incident: %v", err)
	}
	return &inc
}

func (h *harness) identityExists(t *testing.T, name string) bool {
	t.Helper()
	return h.identityNamed(t, IdentityName(name))
}

func (h *harness) identityNamed(t *testing.T, idName string) bool {
	t.Helper()
	key := types.NamespacedName{Namespace: sbNS, Name: idName}
	n := 0
	for _, o := range []client.Object{&corev1.ServiceAccount{}, &rbacv1.Role{}, &rbacv1.RoleBinding{}} {
		err := h.c.Get(t.Context(), key, o)
		switch {
		case err == nil:
			n++
		case !apierrors.IsNotFound(err):
			t.Fatal(err)
		}
	}
	if n != 0 && n != 3 {
		t.Fatalf("identity %s is partial: %d of 3 objects", key.Name, n)
	}
	return n == 3
}

func TestNewIncidentStartsTriageAfterIdentity(t *testing.T) {
	t.Parallel()
	h := newHarness(t, t0, newIncident("a1"))
	res := h.reconcile(t, "a1")

	var run v1alpha1.TriageRun
	if err := h.c.Get(t.Context(), types.NamespacedName{Namespace: sbNS, Name: "a1"}, &run); err != nil {
		t.Fatalf("triage run not created: %v", err)
	}
	if run.Spec.Suspend || run.Spec.ServiceAccount != "triage-a1" || run.Spec.Model != "claude-sonnet-5-5" ||
		!run.Spec.Deadline.Time.Equal(t0.Add(15*time.Minute)) || run.Spec.Alert.Fingerprint != "fp-a1" {
		t.Errorf("triage run spec = %+v", run.Spec)
	}
	if !h.identityExists(t, "a1") {
		t.Error("identity not provisioned")
	}
	// The run must be created suspended and resumed only after its binding exists.
	want := []string{"create TriageRun", "create ServiceAccount", "create Role", "create RoleBinding", "patch TriageRun"}
	if len(h.ops) != len(want) {
		t.Fatalf("ops = %v, want %v", h.ops, want)
	}
	for i := range want {
		if h.ops[i] != want[i] {
			t.Fatalf("ops = %v, want %v", h.ops, want)
		}
	}
	inc := h.incident(t, "a1")
	if inc.Status.Phase != v1alpha1.IncidentTriaging || inc.Status.TriageRun != "a1" ||
		!meta.IsStatusConditionTrue(inc.Status.Conditions, v1alpha1.ConditionIdentityProvisioned) {
		t.Errorf("incident status = %+v", inc.Status)
	}
	if res.RequeueAfter != 17*time.Minute {
		t.Errorf("RequeueAfter = %v, want deadline + grace", res.RequeueAfter)
	}
}

// A coordinator that crashed after creating the run (still suspended) picks up
// where it stopped.
func TestResumesAfterCrashMidStart(t *testing.T) {
	t.Parallel()
	run := runFor("a1", "", t0.Add(10*time.Minute))
	run.Spec.Suspend = true
	h := newHarness(t, t0, newIncident("a1"), run)
	h.reconcile(t, "a1")
	if !h.identityExists(t, "a1") || h.incident(t, "a1").Status.Phase != v1alpha1.IncidentTriaging {
		t.Fatal("did not resume the suspended run")
	}
	var got v1alpha1.TriageRun
	if err := h.c.Get(t.Context(), client.ObjectKeyFromObject(run), &got); err != nil || got.Spec.Suspend {
		t.Errorf("run still suspended: %+v, %v", got.Spec, err)
	}
}

func TestIncidentWaitsWhenRunSlotsAreFull(t *testing.T) {
	t.Parallel()
	h := newHarness(t, t0, newIncident("a3"),
		runFor("a1", v1alpha1.TriageRunning, t0), runFor("a2", "", t0), runFor("old", v1alpha1.TriageSucceeded, t0))
	res := h.reconcile(t, "a3")
	if res.RequeueAfter != pendingRetry {
		t.Errorf("RequeueAfter = %v", res.RequeueAfter)
	}
	if inc := h.incident(t, "a3"); inc.Status.Phase != v1alpha1.IncidentPending || inc.Status.Message != "waiting: 2 triage runs active" {
		t.Errorf("status = %+v", inc.Status)
	}
	if len(h.ops) != 0 || h.identityExists(t, "a3") {
		t.Errorf("created objects while full: %v", h.ops)
	}
}

func TestFinishedRunRevokesIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		run     *v1alpha1.TriageRun
		now     time.Time
		phase   v1alpha1.IncidentPhase
		message string
	}{
		{"succeeded", runFor("a1", v1alpha1.TriageSucceeded, t0.Add(time.Minute)), t0, v1alpha1.IncidentDone, ""},
		{"failed", func() *v1alpha1.TriageRun {
			r := runFor("a1", v1alpha1.TriageFailed, t0.Add(time.Minute))
			r.Status.Error = "agent finished without a valid report"
			return r
		}(), t0, v1alpha1.IncidentFailed, "triage failed: agent finished without a valid report"},
		{"stuck past deadline and grace", runFor("a1", v1alpha1.TriageRunning, t0.Add(-3*time.Minute)), t0,
			v1alpha1.IncidentFailed, "triage run did not finish by its deadline"},
		{"run deleted", nil, t0, v1alpha1.IncidentFailed, "triage run a1 was deleted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inc := newIncident("a1")
			inc.Status = v1alpha1.IncidentStatus{Phase: v1alpha1.IncidentTriaging, TriageRun: "a1"}
			objs := []client.Object{inc}
			run := tc.run
			if run == nil {
				run = runFor("a1", "", t0) // only to own the identity
			} else {
				objs = append(objs, run)
			}
			id := TriageIdentity(run)
			objs = append(objs, id.ServiceAccount, id.Role, id.RoleBinding)
			h := newHarness(t, tc.now, objs...)
			res := h.reconcile(t, "a1")

			if h.identityExists(t, "a1") {
				t.Error("identity still exists after the step ended")
			}
			got := h.incident(t, "a1")
			if got.Status.Phase != tc.phase || got.Status.Message != tc.message || got.Status.FinishedAt == nil {
				t.Errorf("status = %+v", got.Status)
			}
			if meta.IsStatusConditionTrue(got.Status.Conditions, v1alpha1.ConditionIdentityProvisioned) {
				t.Error("IdentityProvisioned still true")
			}
			if res.RequeueAfter != time.Hour {
				t.Errorf("RequeueAfter = %v, want retention", res.RequeueAfter)
			}
		})
	}
}

func TestRunningRunWaitsUntilDeadlinePlusGrace(t *testing.T) {
	t.Parallel()
	inc := newIncident("a1")
	inc.Status = v1alpha1.IncidentStatus{Phase: v1alpha1.IncidentTriaging, TriageRun: "a1"}
	run := runFor("a1", v1alpha1.TriageRunning, t0.Add(5*time.Minute))
	id := TriageIdentity(run)
	h := newHarness(t, t0, inc, run, id.ServiceAccount, id.Role, id.RoleBinding)
	if res := h.reconcile(t, "a1"); res.RequeueAfter != 7*time.Minute {
		t.Errorf("RequeueAfter = %v", res.RequeueAfter)
	}
	if !h.identityExists(t, "a1") {
		t.Error("identity revoked while the run is active")
	}
}

func TestExpiredIncidentIsDeletedWithItsRun(t *testing.T) {
	t.Parallel()
	inc := newIncident("a1")
	done := metav1.NewTime(t0.Add(-2 * time.Hour))
	inc.Status = v1alpha1.IncidentStatus{Phase: v1alpha1.IncidentDone, TriageRun: "a1", FinishedAt: &done}
	h := newHarness(t, t0, inc, runFor("a1", v1alpha1.TriageSucceeded, t0))
	h.reconcile(t, "a1")
	if err := h.c.Get(t.Context(), types.NamespacedName{Namespace: incNS, Name: "a1"}, &v1alpha1.Incident{}); !apierrors.IsNotFound(err) {
		t.Errorf("incident not deleted: %v", err)
	}
	if err := h.c.Get(t.Context(), types.NamespacedName{Namespace: sbNS, Name: "a1"}, &v1alpha1.TriageRun{}); !apierrors.IsNotFound(err) {
		t.Errorf("triage run not deleted: %v", err)
	}
}
