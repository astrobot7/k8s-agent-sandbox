package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/remediation"
	"k8s-agent-sandbox/internal/sandbox"
)

const ns = "agent-sandboxes"

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// fakeAccess is RBAC as the coordinator grants it: remediate-r1 may do
// exactly the verbs in grants on its target's resource name.
type fakeAccess struct{ grants []string }

func (f fakeAccess) Authorize(_ context.Context, c identity.Caller, a authzv1.ResourceAttributes) error {
	if c.Username == "system:serviceaccount:"+ns+":remediate-r1" && a.Namespace == ns && a.Group == identity.Group &&
		a.Resource == "deployments" && a.Name == "workloads.payments-api" && slices.Contains(f.grants, a.Verb) {
		return nil
	}
	return fmt.Errorf("%w: %s may not %s %s/%s", identity.ErrForbidden, c.Username, a.Verb, a.Resource, a.Name)
}

type fakeDriver struct {
	mu   sync.Mutex
	done []remediation.Proposal
}

func (d *fakeDriver) Do(_ context.Context, p remediation.Proposal) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.done = append(d.done, p)
	return string(p.Action) + " done", nil
}

type fakeLauncher struct {
	launched []sandbox.Spec
	deleted  []string
}

func (f *fakeLauncher) Launch(_ context.Context, s sandbox.Spec) (string, error) {
	f.launched = append(f.launched, s)
	return s.Name(), nil
}

func (f *fakeLauncher) Delete(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

func skillSpec() v1alpha1.RemediationSkillSpec {
	return v1alpha1.RemediationSkillSpec{
		Description: "restart or scale payments", Instructions: "restart transient crashes",
		AllowedActions: []v1alpha1.RemediationAllowedAction{
			{Type: "rollout-restart", Constraints: v1alpha1.RemediationActionConstraints{Namespaces: []string{"workloads"}}},
			{Type: "scale", Constraints: v1alpha1.RemediationActionConstraints{Namespaces: []string{"workloads"}, MaxReplicas: 4}},
		},
	}
}

func newRemediation(name string, phase v1alpha1.RemediationPhase) *v1alpha1.Remediation {
	return &v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(t0)},
		Spec: v1alpha1.RemediationSpec{
			Incident: name, TriageRun: name, SkillName: "payments", Skill: skillSpec(),
			Report:         runtime.RawExtension{Raw: []byte(`{"summary":"db host does not resolve","likely_cause":"bad DB_HOST","confidence":"high","evidence":[],"next_steps":[]}`)},
			Target:         v1alpha1.RemediationTarget{Namespace: "workloads", Kind: "Deployment", Name: "payments-api"},
			ServiceAccount: "remediate-" + name, Model: "m", Deadline: metav1.NewTime(t0.Add(10 * time.Minute)),
		},
		Status: v1alpha1.RemediationStatus{Phase: phase},
	}
}

func proposed(rem *v1alpha1.Remediation, action string, replicas *int32) *v1alpha1.Remediation {
	at := metav1.NewTime(t0)
	rem.Status.Phase, rem.Status.ProposedAt, rem.Status.Sandbox = v1alpha1.RemediationProposed, &at, "remediate-"+rem.Name
	rem.Status.Proposal = &v1alpha1.RemediationProposal{Action: v1alpha1.RemediationAction(action), Replicas: replicas, Rationale: "transient"}
	return rem
}

type harness struct {
	r        *Reconciler
	c        client.Client
	launcher *fakeLauncher
	driver   *fakeDriver
	posts    []string
}

func newHarness(t *testing.T, now time.Time, mode Mode, objs ...client.Object) *harness {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	h := &harness{launcher: &fakeLauncher{}, driver: &fakeDriver{}}
	h.c = fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.Remediation{}).WithObjects(objs...).Build()
	h.r = &Reconciler{
		Client: h.c, Launcher: h.launcher, Access: fakeAccess{grants: []string{"rollout-restart", "scale"}}, Driver: h.driver,
		Notify: func(_ context.Context, text string) error { h.posts = append(h.posts, text); return nil },
		Config: Config{Namespace: ns, Mode: mode, ApprovalTimeout: time.Hour, Cooldown: 30 * time.Minute},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now },
	}
	return h
}

func (h *harness) reconcile(t *testing.T, name string) ctrl.Result {
	t.Helper()
	res, err := h.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func (h *harness) get(t *testing.T, name string) *v1alpha1.Remediation {
	t.Helper()
	var rem v1alpha1.Remediation
	if err := h.c.Get(t.Context(), types.NamespacedName{Namespace: ns, Name: name}, &rem); err != nil {
		t.Fatal(err)
	}
	return &rem
}

func TestReleasedRemediationLaunchesInRemediateMode(t *testing.T) {
	t.Parallel()
	suspended := newRemediation("r1", "")
	suspended.Spec.Suspend = true
	h := newHarness(t, t0, ModeLive, suspended)
	h.reconcile(t, "r1")
	if len(h.launcher.launched) != 0 {
		t.Fatal("launched a suspended remediation: its identity may not exist yet")
	}

	h = newHarness(t, t0, ModeLive, newRemediation("r1", ""))
	h.reconcile(t, "r1")
	if len(h.launcher.launched) != 1 {
		t.Fatalf("launched %d", len(h.launcher.launched))
	}
	s := h.launcher.launched[0]
	if s.Step != sandbox.StepRemediate || s.ServiceAccount != "remediate-r1" || s.OwnerKind != "Remediation" ||
		s.Env["AGENT_MODE"] != "remediate" || s.Name() != "remediate-r1" {
		t.Errorf("sandbox spec = %+v", s)
	}
	if st := h.get(t, "r1").Status; st.Phase != v1alpha1.RemediationRunning || st.Sandbox != "remediate-r1" {
		t.Errorf("status = %+v", st)
	}
}

func TestAutoApprovedProposalRunsOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, t0, ModeLive, proposed(newRemediation("r1", ""), "rollout-restart", nil))
	h.reconcile(t, "r1")
	st := h.get(t, "r1").Status
	if st.Phase != v1alpha1.RemediationSucceeded || st.ExecutedAt == nil || st.ApprovalMode != "auto" || st.Result != "rollout-restart done" {
		t.Fatalf("status = %+v", st)
	}
	if len(h.driver.done) != 1 || h.driver.done[0].Target.String() != "workloads/Deployment/payments-api" {
		t.Errorf("driver calls = %+v", h.driver.done)
	}
	if !slices.Equal(h.launcher.deleted, []string{"remediate-r1"}) {
		t.Errorf("sandbox not removed after the decision: %v", h.launcher.deleted)
	}
	h.reconcile(t, "r1") // terminal: publish once
	h.reconcile(t, "r1")
	if len(h.driver.done) != 1 {
		t.Error("action ran twice")
	}
	if len(h.posts) != 1 || !strings.Contains(h.posts[0], "Succeeded") {
		t.Errorf("posts = %q", h.posts)
	}
}

// Two reconciles can see the same finished remediation before either has
// recorded the notification (the cache lags the status update). Only one may post.
func TestOutcomeIsPostedOnceFromStaleCopies(t *testing.T) {
	t.Parallel()
	done := newRemediation("r1", v1alpha1.RemediationSucceeded)
	h := newHarness(t, t0, ModeLive, done)
	first, second := h.get(t, "r1"), h.get(t, "r1") // same resourceVersion
	if err := h.r.publish(t.Context(), first); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := h.r.publish(t.Context(), second); err == nil {
		t.Error("publish from a stale copy succeeded; it must conflict")
	}
	if len(h.posts) != 1 {
		t.Errorf("posted %d times: %q", len(h.posts), h.posts)
	}
}

func TestProposalNeedingApproval(t *testing.T) {
	t.Parallel()
	replicas := int32(3)
	tests := []struct {
		name     string
		approval string
		now      time.Time
		phase    v1alpha1.RemediationPhase
		ran      bool
	}{
		{"waits for a human", "", t0.Add(time.Minute), v1alpha1.RemediationAwaitingApproval, false},
		{"approved", v1alpha1.ApprovalApproved, t0.Add(time.Minute), v1alpha1.RemediationSucceeded, true},
		{"rejected", v1alpha1.ApprovalRejected, t0.Add(time.Minute), v1alpha1.RemediationRejected, false},
		{"nobody approved in time", "", t0.Add(2 * time.Hour), v1alpha1.RemediationRejected, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rem := proposed(newRemediation("r1", ""), "scale", &replicas) // scale needs approval by default
			h := newHarness(t, tc.now, ModeLive, rem)
			h.reconcile(t, "r1")
			if got := h.get(t, "r1"); got.Status.Phase != v1alpha1.RemediationAwaitingApproval || len(h.posts) != 1 ||
				!strings.Contains(h.posts[0], "needs approval") {
				t.Fatalf("after proposal: status = %+v, posts = %q", got.Status, h.posts)
			}
			if tc.approval != "" {
				got := h.get(t, "r1")
				got.Spec.Approval = tc.approval
				if err := h.c.Update(t.Context(), got); err != nil {
					t.Fatal(err)
				}
			}
			h.reconcile(t, "r1")
			if got := h.get(t, "r1").Status; got.Phase != tc.phase {
				t.Errorf("phase = %s (%s), want %s", got.Phase, got.Result, tc.phase)
			}
			if ran := len(h.driver.done) > 0; ran != tc.ran {
				t.Errorf("driver ran = %v, want %v", ran, tc.ran)
			}
		})
	}
}

func TestExecutorGuards(t *testing.T) {
	t.Parallel()
	recentlyActed := func() client.Object {
		other := newRemediation("r0", v1alpha1.RemediationSucceeded)
		at := metav1.NewTime(t0.Add(-10 * time.Minute))
		other.Status.ExecutedAt = &at
		return other
	}
	tests := []struct {
		name   string
		mode   Mode
		access Authorizer
		extra  []client.Object
		mutate func(*v1alpha1.Remediation)
		phase  v1alpha1.RemediationPhase
		result string
	}{
		{"kill switch off", ModeOff, nil, nil, nil, v1alpha1.RemediationRejected, "switched off"},
		{"dry run", ModeDryRun, nil, nil, nil, v1alpha1.RemediationSucceeded, "dry run: would rollout-restart"},
		{"identity revoked", ModeLive, fakeAccess{}, nil, nil, v1alpha1.RemediationRejected, "forbidden"},
		{"cooldown on the target", ModeLive, nil, []client.Object{recentlyActed()}, nil, v1alpha1.RemediationRejected, "cooldown"},
		{"skill no longer allows the action", ModeLive, nil, nil, func(r *v1alpha1.Remediation) {
			r.Spec.Skill.AllowedActions = r.Spec.Skill.AllowedActions[1:]
		}, v1alpha1.RemediationRejected, "not allowed by skill"},
		{"executor restarted mid-action", ModeLive, nil, nil, func(r *v1alpha1.Remediation) {
			at := metav1.NewTime(t0)
			r.Status.ExecutedAt = &at
		}, v1alpha1.RemediationFailed, "restarted while performing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rem := proposed(newRemediation("r1", ""), "rollout-restart", nil)
			if tc.mutate != nil {
				tc.mutate(rem)
			}
			h := newHarness(t, t0, tc.mode, append(tc.extra, rem)...)
			if tc.access != nil {
				h.r.Access = tc.access
			}
			h.reconcile(t, "r1")
			st := h.get(t, "r1").Status
			if st.Phase != tc.phase || !strings.Contains(st.Result, tc.result) {
				t.Errorf("status = %s %q, want %s mentioning %q", st.Phase, st.Result, tc.phase, tc.result)
			}
			if len(h.driver.done) != 0 {
				t.Errorf("driver ran: %+v", h.driver.done)
			}
		})
	}
}

func TestAgentThatMissesItsDeadlineFails(t *testing.T) {
	t.Parallel()
	rem := newRemediation("r1", v1alpha1.RemediationRunning)
	rem.Status.Sandbox = "remediate-r1"
	h := newHarness(t, t0.Add(11*time.Minute), ModeLive, rem)
	h.reconcile(t, "r1")
	if st := h.get(t, "r1").Status; st.Phase != v1alpha1.RemediationFailed || !strings.Contains(st.Result, "deadline") {
		t.Errorf("status = %+v", st)
	}
}

// fakeAuth: token "rem:<id>" is the agent of remediation <id>.
type fakeAuth struct{}

func (fakeAuth) RunID(_ context.Context, r *http.Request, _ string) (string, error) {
	tok, ok := identity.BearerToken(r)
	if !ok || !strings.HasPrefix(tok, "rem:") {
		return "", identity.ErrUnauthenticated
	}
	return strings.TrimPrefix(tok, "rem:"), nil
}

func serverFor(t *testing.T, objs ...client.Object) (*Server, client.Client) {
	t.Helper()
	h := newHarness(t, t0, ModeLive, objs...)
	return &Server{Client: h.c, Reader: h.c, Auth: fakeAuth{}, Access: fakeAccess{grants: []string{"rollout-restart", "scale"}},
		NS: ns, Log: h.r.Log, Now: func() time.Time { return t0 }}, h.c
}

func call(t *testing.T, s *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestAssignmentListsOnlyActionsForTheTarget(t *testing.T) {
	t.Parallel()
	rem := newRemediation("r1", v1alpha1.RemediationRunning)
	rem.Spec.Skill.AllowedActions = append(rem.Spec.Skill.AllowedActions, v1alpha1.RemediationAllowedAction{
		Type: "restart-pod", Constraints: v1alpha1.RemediationActionConstraints{Namespaces: []string{"workloads"}}})
	s, _ := serverFor(t, rem)
	w := call(t, s, http.MethodGet, "/v1/remediation/assignment", "rem:r1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var a Assignment
	if err := json.NewDecoder(w.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	var got []remediation.ActionType
	for _, act := range a.Actions {
		got = append(got, act.Type)
	}
	// restart-pod applies to Pods, not to the Deployment target.
	if !slices.Equal(got, []remediation.ActionType{"rollout-restart", "scale"}) || a.Report == nil || a.Target.Name != "payments-api" {
		t.Errorf("assignment = %+v", a)
	}
}

func TestDecisions(t *testing.T) {
	t.Parallel()
	five, two := int32(5), int32(2)
	tests := []struct {
		name  string
		body  Decision
		code  int
		phase v1alpha1.RemediationPhase
	}{
		{"valid proposal", Decision{Proposal: &ProposedAction{Action: "rollout-restart", Rationale: "transient"}}, http.StatusNoContent, v1alpha1.RemediationProposed},
		{"scale within the limit", Decision{Proposal: &ProposedAction{Action: "scale", Replicas: &two, Rationale: "load"}}, http.StatusNoContent, v1alpha1.RemediationProposed},
		{"scale past the limit", Decision{Proposal: &ProposedAction{Action: "scale", Replicas: &five, Rationale: "load"}}, http.StatusUnprocessableEntity, v1alpha1.RemediationRunning},
		{"action the skill lacks", Decision{Proposal: &ProposedAction{Action: "restart-pod", Rationale: "x"}}, http.StatusUnprocessableEntity, v1alpha1.RemediationRunning},
		{"no rationale", Decision{Proposal: &ProposedAction{Action: "rollout-restart"}}, http.StatusUnprocessableEntity, v1alpha1.RemediationRunning},
		{"no action", Decision{NoAction: "a restart will not fix a bad DB_HOST"}, http.StatusNoContent, v1alpha1.RemediationNoAction},
		{"agent error", Decision{Error: "model timed out"}, http.StatusNoContent, v1alpha1.RemediationFailed},
		{"proposal and no_action together", Decision{Proposal: &ProposedAction{Action: "rollout-restart", Rationale: "x"}, NoAction: "x"}, http.StatusBadRequest, v1alpha1.RemediationRunning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, c := serverFor(t, newRemediation("r1", v1alpha1.RemediationRunning))
			w := call(t, s, http.MethodPost, "/v1/remediation/decision", "rem:r1", tc.body)
			if w.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.code, w.Body)
			}
			var rem v1alpha1.Remediation
			if err := c.Get(t.Context(), types.NamespacedName{Namespace: ns, Name: "r1"}, &rem); err != nil {
				t.Fatal(err)
			}
			if rem.Status.Phase != tc.phase {
				t.Errorf("phase = %s, want %s", rem.Status.Phase, tc.phase)
			}
			if tc.phase == v1alpha1.RemediationProposed && (rem.Status.ProposedAt == nil || rem.Status.Proposal == nil) {
				t.Errorf("proposal not recorded: %+v", rem.Status)
			}
		})
	}
}

func TestOneDecisionPerRemediation(t *testing.T) {
	t.Parallel()
	s, _ := serverFor(t, newRemediation("r1", v1alpha1.RemediationRunning))
	first := Decision{Proposal: &ProposedAction{Action: "rollout-restart", Rationale: "transient"}}
	if w := call(t, s, http.MethodPost, "/v1/remediation/decision", "rem:r1", first); w.Code != http.StatusNoContent {
		t.Fatalf("first decision: %d %s", w.Code, w.Body)
	}
	if w := call(t, s, http.MethodPost, "/v1/remediation/decision", "rem:r1", Decision{NoAction: "changed my mind"}); w.Code != http.StatusConflict {
		t.Errorf("second decision: %d, want 409", w.Code)
	}
}
