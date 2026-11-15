package triage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/sandbox"
	"k8s-agent-sandbox/internal/skills"
)

const (
	incNS = "alert-triage"
	sbNS  = "agent-sandboxes"
)

type fakeLauncher struct {
	mu       sync.Mutex
	launched []sandbox.Spec
	deleted  []string
	err      error
}

func (f *fakeLauncher) Launch(_ context.Context, run sandbox.Spec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.launched = append(f.launched, run)
	return "triage-" + run.ID, nil
}

func (f *fakeLauncher) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, name)
	return nil
}

type fakeNotifier struct {
	mu   sync.Mutex
	runs []Run
}

func (f *fakeNotifier) Notify(_ context.Context, run Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	return nil
}

func webhook(fingerprints ...string) string {
	var alerts []string
	for _, fp := range fingerprints {
		alerts = append(alerts, `{"status":"firing","fingerprint":"`+fp+`","labels":{"alertname":"K8sWarningEvents","k8s.namespace.name":"workloads"}}`)
	}
	return `{"status":"firing","alerts":[` + strings.Join(alerts, ",") + `]}`
}

const validReport = `{"report":{"summary":"api crashloops","likely_cause":"OOMKilled","confidence":"high",
 "evidence":[{"source":"pods_get","detail":"reason=OOMKilled"}],"next_steps":["raise limit"]}}`

// fakeAuth stands in for PodAuth: token "run:<id>" is the agent of run <id>,
// "forbidden" is an authenticated caller RBAC denies, anything else is invalid.
type fakeAuth struct{}

func (fakeAuth) RunID(_ context.Context, r *http.Request, _ string) (string, error) {
	tok, _ := identity.BearerToken(r)
	if id, ok := strings.CutPrefix(tok, "run:"); ok {
		return id, nil
	}
	if tok == "forbidden" {
		return "", identity.ErrForbidden
	}
	return "", identity.ErrUnauthenticated
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

type harness struct {
	srv      *Server
	h        http.Handler
	c        client.Client
	runs     *RunReconciler
	launcher *fakeLauncher
	notifier *fakeNotifier
	now      time.Time
}

func newHarness(t *testing.T, objs ...client.Object) *harness {
	t.Helper()
	hs := &harness{launcher: &fakeLauncher{}, notifier: &fakeNotifier{}, now: time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)}
	hs.c = fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&v1alpha1.Incident{}, &v1alpha1.TriageRun{}).
		WithObjects(objs...).Build()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{IncidentNamespace: incNS, SandboxNamespace: sbNS, DedupWindow: 30 * time.Minute}
	hs.srv = NewServer(cfg, hs.c, hs.c, fakeAuth{}, log)
	hs.srv.now = func() time.Time { return hs.now }
	hs.h = hs.srv.Handler()
	hs.runs = &RunReconciler{Client: hs.c, Launcher: hs.launcher, Notifier: hs.notifier, Log: log,
		Now: func() time.Time { return hs.now }}
	return hs
}

func (hs *harness) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	hs.h.ServeHTTP(rec, req)
	return rec
}

func (hs *harness) reconcile(t *testing.T, id string) ctrl.Result {
	t.Helper()
	res, err := hs.runs.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: sbNS, Name: id}})
	if err != nil {
		t.Fatalf("Reconcile %s: %v", id, err)
	}
	return res
}

func (hs *harness) run(t *testing.T, id string) *v1alpha1.TriageRun {
	t.Helper()
	var tr v1alpha1.TriageRun
	if err := hs.c.Get(t.Context(), types.NamespacedName{Namespace: sbNS, Name: id}, &tr); err != nil {
		t.Fatal(err)
	}
	return &tr
}

// triageRun is a run as the coordinator creates it, already resumed.
func triageRun(id string, deadline time.Time) *v1alpha1.TriageRun {
	return &v1alpha1.TriageRun{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: sbNS, UID: types.UID("uid-" + id)},
		Spec: v1alpha1.TriageRunSpec{Incident: id, ServiceAccount: "triage-" + id, Model: "claude-sonnet-5-5",
			Deadline: metav1.NewTime(deadline),
			Alert:    v1alpha1.Alert{Fingerprint: "a", Labels: map[string]string{"alertname": "K8sWarningEvents"}}},
	}
}

// startedRun is a run the reconciler has launched.
func startedRun(t *testing.T, id string) *harness {
	t.Helper()
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	hs := newHarness(t, triageRun(id, now.Add(15*time.Minute)))
	hs.reconcile(t, id)
	return hs
}

func (hs *harness) incidents(t *testing.T) []v1alpha1.Incident {
	t.Helper()
	var list v1alpha1.IncidentList
	if err := hs.c.List(t.Context(), &list, client.InNamespace(incNS)); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestWebhookCreatesOneIncidentPerFiringAlert(t *testing.T) {
	t.Parallel()
	hs := newHarness(t)
	rec := hs.do(t, http.MethodPost, "/webhook/signoz", "", webhook("a", "b"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	incs := hs.incidents(t)
	if len(incs) != 2 || incs[0].Name == incs[1].Name {
		t.Fatalf("incidents = %+v", incs)
	}
	for _, inc := range incs {
		if inc.Labels[v1alpha1.FingerprintLabel] != fingerprintLabel(inc.Spec.Alert.Fingerprint) || inc.Spec.Alert.Labels["alertname"] != "K8sWarningEvents" {
			t.Errorf("incident %s = %+v", inc.Name, inc)
		}
	}
	if len(hs.launcher.launched) != 0 {
		t.Error("the webhook launched a sandbox; only the coordinator may start runs")
	}
}

func TestWebhookDedup(t *testing.T) {
	t.Parallel()
	hs := newHarness(t)
	hs.do(t, http.MethodPost, "/webhook/signoz", "", webhook("a"))

	var resp webhookResponse
	if err := json.NewDecoder(hs.do(t, http.MethodPost, "/webhook/signoz", "", webhook("a", "b")).Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Started) != 1 || len(resp.Skipped) != 1 || !strings.Contains(resp.Skipped[0], "duplicate of incident") {
		t.Fatalf("started %v skipped %v; want b started, a a duplicate of the open incident", resp.Started, resp.Skipped)
	}

	// Finish a's incident; it still dedups within the window, not after.
	for _, inc := range hs.incidents(t) {
		if inc.Spec.Alert.Fingerprint != "a" {
			continue
		}
		finished := metav1.NewTime(hs.now)
		inc.Status = v1alpha1.IncidentStatus{Phase: v1alpha1.IncidentDone, FinishedAt: &finished}
		if err := hs.c.Status().Update(t.Context(), &inc); err != nil {
			t.Fatal(err)
		}
	}
	hs.now = hs.now.Add(29 * time.Minute)
	hs.do(t, http.MethodPost, "/webhook/signoz", "", webhook("a"))
	if n := len(hs.incidents(t)); n != 2 {
		t.Fatalf("%d incidents within the dedup window, want 2", n)
	}
	hs.now = hs.now.Add(2 * time.Minute)
	hs.do(t, http.MethodPost, "/webhook/signoz", "", webhook("a"))
	if n := len(hs.incidents(t)); n != 3 {
		t.Errorf("%d incidents after the dedup window, want 3", n)
	}
}

func TestRunStartsOnlyWhenResumed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	tr := triageRun("r1", now.Add(15*time.Minute))
	tr.Spec.Suspend = true
	hs := newHarness(t, tr)
	hs.reconcile(t, "r1")
	if len(hs.launcher.launched) != 0 {
		t.Fatal("launched a suspended run: its identity may not exist yet")
	}

	got := hs.run(t, "r1")
	got.Spec.Suspend = false
	if err := hs.c.Update(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	if res := hs.reconcile(t, "r1"); res.RequeueAfter != 15*time.Minute {
		t.Errorf("RequeueAfter = %v, want the time to the deadline", res.RequeueAfter)
	}
	if len(hs.launcher.launched) != 1 {
		t.Fatalf("launched %d", len(hs.launcher.launched))
	}
	l := hs.launcher.launched[0]
	if l.ServiceAccount != "triage-r1" || l.Model != "claude-sonnet-5-5" || l.OwnerUID != "uid-r1" || l.Step != sandbox.StepTriage || !l.Deadline.Equal(now.Add(15*time.Minute)) {
		t.Errorf("launched run = %+v", l)
	}
	if st := hs.run(t, "r1").Status; st.Phase != v1alpha1.TriageRunning || st.Sandbox != "triage-r1" || st.StartedAt == nil {
		t.Errorf("status = %+v", st)
	}
	hs.reconcile(t, "r1")
	if len(hs.launcher.launched) != 1 {
		t.Error("a running run was launched twice")
	}
}

func TestLaunchFailureFailsRun(t *testing.T) {
	t.Parallel()
	hs := newHarness(t, triageRun("r1", time.Date(2026, 11, 15, 5, 0, 0, 0, time.UTC)))
	hs.launcher.err = errors.New("sandboxes is forbidden")
	hs.reconcile(t, "r1")
	if st := hs.run(t, "r1").Status; st.Phase != v1alpha1.TriageFailed || !strings.Contains(st.Error, "sandboxes is forbidden") {
		t.Errorf("status = %+v", st)
	}
}

func TestAssignmentRequiresIdentity(t *testing.T) {
	t.Parallel()
	hs := startedRun(t, "r1")
	for tok, want := range map[string]int{"": 401, "wrong": 401, "forbidden": 403, "run:nosuchrun": 404} {
		if rec := hs.do(t, http.MethodGet, "/v1/assignment", tok, ""); rec.Code != want {
			t.Errorf("token %q: status %d, want %d", tok, rec.Code, want)
		}
	}
	rec := hs.do(t, http.MethodGet, "/v1/assignment", "run:r1", "")
	var a Assignment
	if err := json.NewDecoder(rec.Body).Decode(&a); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("assignment: status %d err %v", rec.Code, err)
	}
	if a.RunID != "r1" || a.Alert.Key() != "a" || a.Alert.Name() != "K8sWarningEvents" || a.Deadline.IsZero() {
		t.Errorf("assignment = %+v", a)
	}
}

type fakeSkills struct {
	list []skills.Skill
	err  error
}

func (f fakeSkills) Skills(context.Context) ([]skills.Skill, error) { return f.list, f.err }

func TestAssignmentCarriesSkills(t *testing.T) {
	t.Parallel()
	hs := startedRun(t, "r1")
	hs.srv.WithSkills(fakeSkills{list: []skills.Skill{{Name: "oomkilled", Description: "d", Instructions: "i"}}})
	var a Assignment
	if err := json.NewDecoder(hs.do(t, http.MethodGet, "/v1/assignment", "run:r1", "").Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	if len(a.Skills) != 1 || a.Skills[0].Name != "oomkilled" || a.Skills[0].Instructions != "i" {
		t.Errorf("assignment skills = %+v", a.Skills)
	}
}

func TestAssignmentSurvivesSkillSourceFailure(t *testing.T) {
	t.Parallel()
	hs := startedRun(t, "r1")
	hs.srv.WithSkills(fakeSkills{err: errors.New("apiserver down")})
	if rec := hs.do(t, http.MethodGet, "/v1/assignment", "run:r1", ""); rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 without skills", rec.Code)
	}
}

func TestReportFinishesRunThenReconcilerPublishesOnce(t *testing.T) {
	t.Parallel()
	hs := startedRun(t, "r1")
	if rec := hs.do(t, http.MethodPost, "/v1/report", "run:r1", `{"report":{"summary":"x"}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid report: status %d, want 400", rec.Code)
	}
	if rec := hs.do(t, http.MethodPost, "/v1/report", "run:r1", validReport); rec.Code != http.StatusNoContent {
		t.Fatalf("valid report: status %d body %s", rec.Code, rec.Body)
	}
	if st := hs.run(t, "r1").Status; st.Phase != v1alpha1.TriageSucceeded || st.Report == nil || st.FinishedAt == nil {
		t.Fatalf("status = %+v", st)
	}
	if rec := hs.do(t, http.MethodPost, "/v1/report", "run:r1", validReport); rec.Code != http.StatusConflict {
		t.Errorf("second report: status %d, want 409", rec.Code)
	}

	hs.reconcile(t, "r1")
	hs.reconcile(t, "r1")
	if len(hs.notifier.runs) != 1 || hs.notifier.runs[0].Status != StatusDone || hs.notifier.runs[0].Report.LikelyCause != "OOMKilled" {
		t.Errorf("notified runs = %+v", hs.notifier.runs)
	}
	if len(hs.launcher.deleted) != 1 || hs.launcher.deleted[0] != "triage-r1" {
		t.Errorf("deleted sandboxes = %v", hs.launcher.deleted)
	}
	if !hs.run(t, "r1").Status.Notified {
		t.Error("Notified not recorded")
	}
}

func TestAgentErrorFailsRun(t *testing.T) {
	t.Parallel()
	hs := startedRun(t, "r1")
	hs.do(t, http.MethodPost, "/v1/report", "run:r1", `{"error":"anthropic: 401"}`)
	hs.reconcile(t, "r1")
	if got := hs.notifier.runs[0]; got.Status != StatusFailed || !strings.Contains(got.Error, "anthropic: 401") {
		t.Errorf("run = %+v", got)
	}
}

func TestRunFailsAtDeadline(t *testing.T) {
	t.Parallel()
	hs := startedRun(t, "r1")
	hs.reconcile(t, "r1")
	if len(hs.notifier.runs) != 0 {
		t.Fatal("run expired before its deadline")
	}
	hs.now = hs.now.Add(16 * time.Minute)
	hs.reconcile(t, "r1") // fails it
	hs.reconcile(t, "r1") // publishes it
	if len(hs.notifier.runs) != 1 || hs.notifier.runs[0].Error != "timed out without a report" {
		t.Fatalf("notified = %+v", hs.notifier.runs)
	}
	if len(hs.launcher.deleted) != 1 {
		t.Error("expired run's sandbox was not deleted")
	}
	if rec := hs.do(t, http.MethodPost, "/v1/report", "run:r1", validReport); rec.Code != http.StatusConflict {
		t.Errorf("late report: status %d, want 409", rec.Code)
	}
}

func TestReportRequiresIdentity(t *testing.T) {
	t.Parallel()
	hs := startedRun(t, "r1")
	if rec := hs.do(t, http.MethodPost, "/v1/report", "forbidden", validReport); rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}
	if hs.run(t, "r1").Status.Phase.Terminal() {
		t.Error("a forbidden caller finished the run")
	}
}

func TestListRunsNewestFirst(t *testing.T) {
	t.Parallel()
	old, recent := triageRun("old", time.Now()), triageRun("new", time.Now())
	old.CreationTimestamp = metav1.NewTime(time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC))
	recent.CreationTimestamp = metav1.NewTime(time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC))
	hs := newHarness(t, old, recent)
	var runs []Run
	if err := json.NewDecoder(hs.do(t, http.MethodGet, "/v1/runs", "", "").Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != "new" || runs[0].Status != StatusPending || runs[0].Alert.Name() != "K8sWarningEvents" {
		t.Errorf("runs = %+v", runs)
	}
	if rec := hs.do(t, http.MethodGet, "/v1/runs/old", "", ""); rec.Code != http.StatusOK {
		t.Errorf("get run: %d", rec.Code)
	}
}
