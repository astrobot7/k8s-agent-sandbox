// Package triage is the triage controller: it turns SigNoz alert webhooks into
// Incidents, runs the TriageRuns the coordinator starts for them, serves agents
// their assignments, and accepts their reports.
package triage

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/alert"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/report"
	"k8s-agent-sandbox/internal/skills"
)

// Config tunes the server.
type Config struct {
	IncidentNamespace string        // where Incidents are created
	SandboxNamespace  string        // where TriageRuns live
	DedupWindow       time.Duration // ignore the same alert again within this window
}

// Server is the controller's HTTP API.
type Server struct {
	cfg    Config
	client client.Client // cached reads, writes
	reader client.Reader // uncached reads, for dedup
	auth   AgentAuth
	skills SkillSource  // optional
	extra  http.Handler // optional: the remediation module's agent API
	log    *slog.Logger
	now    func() time.Time

	admitMu sync.Mutex // serializes dedup check and Incident creation
}

// NewServer returns a Server. reader must read through to the API server:
// dedup checks cannot trust a cache that may not yet hold an Incident created
// a moment ago.
func NewServer(cfg Config, c client.Client, reader client.Reader, auth AgentAuth, log *slog.Logger) *Server {
	return &Server{cfg: cfg, client: c, reader: reader, auth: auth, log: log, now: time.Now}
}

// WithSkills makes assignments carry the skills from src.
func (s *Server) WithSkills(src SkillSource) *Server {
	s.skills = src
	return s
}

// WithRemediation serves h under /v1/remediation/ on the same listener, so
// agents of both steps reach one controller address.
func (s *Server) WithRemediation(h http.Handler) *Server {
	s.extra = h
	return s
}

// AgentAuth identifies which run an agent request belongs to and checks the
// agent may perform verb on it ("get" for the assignment, "update" for the
// report). sandbox.PodAuth implements it.
type AgentAuth interface {
	RunID(ctx context.Context, r *http.Request, verb string) (string, error)
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/signoz", s.handleWebhook)
	mux.HandleFunc("GET /v1/assignment", s.handleAssignment)
	mux.HandleFunc("POST /v1/report", s.handleReport)
	mux.HandleFunc("GET /v1/runs", s.handleListRuns)
	mux.HandleFunc("GET /v1/runs/{id}", s.handleGetRun)
	if s.extra != nil {
		mux.Handle("/v1/remediation/", s.extra)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

type webhookResponse struct {
	Started []string `json:"started"`
	Skipped []string `json:"skipped"`
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	hook, err := alert.Parse(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := webhookResponse{Started: []string{}, Skipped: []string{}}
	for _, a := range hook.Firing() {
		id, reason, err := s.admit(r.Context(), a)
		switch {
		case err != nil:
			s.log.Error("incident not created", "alert", a.Name(), "key", a.Key(), "err", err)
			http.Error(w, "create incident: "+err.Error(), http.StatusServiceUnavailable)
			return
		case id == "":
			s.log.Info("alert skipped", "alert", a.Name(), "key", a.Key(), "reason", reason)
			resp.Skipped = append(resp.Skipped, a.Key()+": "+reason)
		default:
			s.log.Info("incident created", "incident", id, "alert", a.Name(), "key", a.Key())
			resp.Started = append(resp.Started, id)
		}
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// admit creates an Incident for the alert, or says why it skipped it: the same
// alert has an incident that is still open or finished within DedupWindow.
// The coordinator decides when its triage starts.
func (s *Server) admit(ctx context.Context, a alert.Alert) (id, reason string, err error) {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	fp := fingerprintLabel(a.Key())
	var existing v1alpha1.IncidentList
	if err := s.reader.List(ctx, &existing, client.InNamespace(s.cfg.IncidentNamespace),
		client.MatchingLabels{v1alpha1.FingerprintLabel: fp}); err != nil {
		return "", "", fmt.Errorf("list incidents: %w", err)
	}
	now := s.now()
	for _, inc := range existing.Items {
		finished := inc.Status.FinishedAt
		if !inc.Status.Phase.Terminal() || finished == nil || now.Sub(finished.Time) < s.cfg.DedupWindow {
			return "", "duplicate of incident " + inc.Name, nil
		}
	}
	inc := &v1alpha1.Incident{
		ObjectMeta: metav1.ObjectMeta{
			Name: randomHex(5), Namespace: s.cfg.IncidentNamespace,
			Labels: map[string]string{v1alpha1.FingerprintLabel: fp},
		},
		Spec: v1alpha1.IncidentSpec{Alert: v1alpha1.AlertFrom(a)},
	}
	if err := s.client.Create(ctx, inc); err != nil {
		return "", "", err
	}
	return inc.Name, "", nil
}

// fingerprintLabel turns an alert key into a valid label value.
func fingerprintLabel(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// runFor authenticates an agent request, checks RBAC for verb on its run, and
// returns the run. On failure it has already written the HTTP error.
func (s *Server) runFor(w http.ResponseWriter, r *http.Request, verb string) (*v1alpha1.TriageRun, bool) {
	id, err := s.auth.RunID(r.Context(), r, verb)
	if err != nil {
		code := identity.HTTPStatus(err)
		if code == http.StatusInternalServerError {
			s.log.Error("agent authentication failed", "err", err)
		}
		http.Error(w, err.Error(), code)
		return nil, false
	}
	var tr v1alpha1.TriageRun
	if err := s.client.Get(r.Context(), s.runKey(id), &tr); err != nil {
		code := http.StatusInternalServerError
		if apierrors.IsNotFound(err) {
			code = http.StatusNotFound
		}
		http.Error(w, "run "+id+": "+err.Error(), code)
		return nil, false
	}
	if tr.Status.Phase.Terminal() {
		http.Error(w, "run is "+string(statusOf[tr.Status.Phase]), http.StatusConflict)
		return nil, false
	}
	return &tr, true
}

func (s *Server) runKey(id string) types.NamespacedName {
	return types.NamespacedName{Namespace: s.cfg.SandboxNamespace, Name: id}
}

// Assignment is what an agent receives: the alert and the current triage skills.
type Assignment struct {
	RunID    string         `json:"run_id"`
	Alert    alert.Alert    `json:"alert"`
	Deadline time.Time      `json:"deadline"`
	Skills   []skills.Skill `json:"skills,omitempty"`
}

func (s *Server) handleAssignment(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.runFor(w, r, "get")
	if !ok {
		return
	}
	a := Assignment{RunID: tr.Name, Alert: tr.Spec.Alert.Webhook(), Deadline: tr.Spec.Deadline.Time}
	if s.skills != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		list, err := s.skills.Skills(ctx)
		cancel()
		if err != nil {
			// Triage still runs without skills; the agent has its base instructions.
			s.log.Error("loading skills failed; assigning without skills", "run", a.RunID, "err", err)
		}
		a.Skills = list
	}
	writeJSON(w, http.StatusOK, a)
}

// Submission is what an agent posts when it finishes: a report or an error.
type Submission struct {
	Report *report.Report `json:"report,omitempty"`
	Error  string         `json:"error,omitempty"`
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	var sub Submission
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&sub); err != nil {
		http.Error(w, "decode submission: "+err.Error(), http.StatusBadRequest)
		return
	}
	if sub.Report == nil && sub.Error == "" {
		http.Error(w, "submission needs a report or an error", http.StatusBadRequest)
		return
	}
	if sub.Report != nil {
		if err := sub.Report.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	tr, ok := s.runFor(w, r, "update")
	if !ok {
		return
	}
	errMsg := ""
	if sub.Report == nil {
		errMsg = "agent: " + truncate(sub.Error, 2000)
	}
	// The run reconciler may update the status concurrently (it just launched
	// the sandbox); retry on conflict against a fresh copy.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh v1alpha1.TriageRun
		if err := s.reader.Get(r.Context(), client.ObjectKeyFromObject(tr), &fresh); err != nil {
			return err
		}
		if fresh.Status.Phase.Terminal() {
			return errAlreadyFinished
		}
		return finishStatus(r.Context(), s.client, &fresh, sub.Report, errMsg, s.now())
	})
	switch {
	case errors.Is(err, errAlreadyFinished):
		http.Error(w, "run already finished", http.StatusConflict)
	case err != nil:
		s.log.Error("store report failed", "run", tr.Name, "err", err)
		http.Error(w, "store report: "+err.Error(), http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

var errAlreadyFinished = errors.New("run already finished")

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	var list v1alpha1.TriageRunList
	if err := s.client.List(r.Context(), &list, client.InNamespace(s.cfg.SandboxNamespace)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]Run, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, RunFrom(&list.Items[i]))
	}
	slices.SortFunc(out, func(a, b Run) int { return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(a.ID, b.ID)) })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	var tr v1alpha1.TriageRun
	if err := s.client.Get(r.Context(), s.runKey(r.PathValue("id")), &tr); err != nil {
		if apierrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, RunFrom(&tr))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("crypto/rand: %w", err)) // only fails if the OS RNG is broken
	}
	return hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
