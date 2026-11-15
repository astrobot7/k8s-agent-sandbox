package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/alert"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/remediation"
	"k8s-agent-sandbox/internal/report"
	"k8s-agent-sandbox/internal/sandbox"
)

// AgentAuth identifies which Remediation an agent request belongs to;
// sandbox.PodAuth implements it.
type AgentAuth interface {
	RunID(ctx context.Context, r *http.Request, verb string) (string, error)
}

// Assignment is what a remediation agent receives.
type Assignment struct {
	RunID    string                      `json:"run_id"`
	Alert    alert.Alert                 `json:"alert"`
	Report   *report.Report              `json:"report,omitempty"`
	Skill    remediation.Skill           `json:"skill"`
	Target   remediation.Target          `json:"target"`
	Actions  []remediation.AllowedAction `json:"actions"` // what the skill allows on the target
	Deadline time.Time                   `json:"deadline"`
}

// Decision is what a remediation agent posts: one proposal, a reason for not
// acting, or the error that stopped it.
type Decision struct {
	Proposal *ProposedAction `json:"proposal,omitempty"`
	NoAction string          `json:"no_action,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// ProposedAction is the agent's proposal. It has no target: the target is the
// remediation's own.
type ProposedAction struct {
	Action    remediation.ActionType `json:"action"`
	Replicas  *int32                 `json:"replicas,omitempty"`
	Rationale string                 `json:"rationale"`
}

// Server is the remediation agent API, mounted under /v1/remediation/.
type Server struct {
	Client client.Client // cached reads, status writes
	Reader client.Reader // uncached reads for read-modify-write
	Auth   AgentAuth
	Access Authorizer
	NS     string
	Log    *slog.Logger
	Now    func() time.Time
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/remediation/assignment", s.handleAssignment)
	mux.HandleFunc("POST /v1/remediation/decision", s.handleDecision)
	return mux
}

// Owner finds a Remediation's ServiceAccount for sandbox.PodAuth.
func Owner(c client.Reader, namespace string) sandbox.OwnerLookup {
	return func(ctx context.Context, name string) (string, error) {
		var rem v1alpha1.Remediation
		err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &rem)
		if apierrors.IsNotFound(err) {
			return "", sandbox.ErrNoStep
		}
		if err != nil {
			return "", err
		}
		return rem.Spec.ServiceAccount, nil
	}
}

// remediationFor authenticates an agent and returns its Remediation if the
// agent part is still open. On failure it has written the HTTP error.
func (s *Server) remediationFor(w http.ResponseWriter, r *http.Request, verb string) (*v1alpha1.Remediation, bool) {
	id, err := s.Auth.RunID(r.Context(), r, verb)
	if err != nil {
		code := identity.HTTPStatus(err)
		if code == http.StatusInternalServerError {
			s.Log.Error("remediation agent authentication failed", "err", err)
		}
		http.Error(w, err.Error(), code)
		return nil, false
	}
	var rem v1alpha1.Remediation
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.NS, Name: id}, &rem); err != nil {
		code := http.StatusInternalServerError
		if apierrors.IsNotFound(err) {
			code = http.StatusNotFound
		}
		http.Error(w, "remediation "+id+": "+err.Error(), code)
		return nil, false
	}
	if !agentOpen(rem.Status.Phase) {
		http.Error(w, "remediation is "+string(rem.Status.Phase), http.StatusConflict)
		return nil, false
	}
	return &rem, true
}

func agentOpen(p v1alpha1.RemediationPhase) bool {
	return p == "" || p == v1alpha1.RemediationPending || p == v1alpha1.RemediationRunning
}

func (s *Server) handleAssignment(w http.ResponseWriter, r *http.Request) {
	rem, ok := s.remediationFor(w, r, "get")
	if !ok {
		return
	}
	rep, err := reportOf(rem)
	if err != nil {
		s.Log.Warn("assigning remediation without its report", "remediation", rem.Name, "err", err)
	}
	skill := skillOf(rem)
	target := targetOf(rem)
	writeJSON(w, http.StatusOK, Assignment{
		RunID: rem.Name, Alert: rem.Spec.Alert.Webhook(), Report: rep,
		Skill: skill, Target: target, Actions: skill.ActionsFor(target), Deadline: rem.Spec.Deadline.Time,
	})
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	var d Decision
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&d); err != nil {
		http.Error(w, "decode decision: "+err.Error(), http.StatusBadRequest)
		return
	}
	set := 0
	for _, b := range []bool{d.Proposal != nil, strings.TrimSpace(d.NoAction) != "", d.Error != ""} {
		if b {
			set++
		}
	}
	if set != 1 {
		http.Error(w, "a decision needs exactly one of proposal, no_action, error", http.StatusBadRequest)
		return
	}
	rem, ok := s.remediationFor(w, r, "update")
	if !ok {
		return
	}
	var apply func(*v1alpha1.Remediation)
	switch {
	case d.Proposal != nil:
		p := remediation.Proposal{Action: d.Proposal.Action, Target: targetOf(rem),
			Params: remediation.Params{Replicas: d.Proposal.Replicas}, Rationale: truncate(d.Proposal.Rationale, 4000)}
		if err := Check(r.Context(), s.Access, rem, p); err != nil {
			code := http.StatusUnprocessableEntity
			if !errors.Is(err, remediation.ErrInvalid) && !errors.Is(err, remediation.ErrNotAllowed) && !errors.Is(err, identity.ErrForbidden) {
				s.Log.Error("check proposal failed", "remediation", rem.Name, "err", err)
				code = http.StatusInternalServerError
			}
			http.Error(w, err.Error(), code)
			return
		}
		apply = func(fresh *v1alpha1.Remediation) {
			now := metav1.NewTime(s.Now())
			fresh.Status.Phase, fresh.Status.ProposedAt = v1alpha1.RemediationProposed, &now
			fresh.Status.Proposal = &v1alpha1.RemediationProposal{
				Action: v1alpha1.RemediationAction(p.Action), Replicas: p.Params.Replicas, Rationale: p.Rationale}
		}
	case d.NoAction != "":
		apply = func(fresh *v1alpha1.Remediation) {
			now := metav1.NewTime(s.Now())
			fresh.Status.Phase, fresh.Status.FinishedAt = v1alpha1.RemediationNoAction, &now
			fresh.Status.Result = "the agent chose not to act: " + truncate(d.NoAction, 2000)
		}
	default:
		apply = func(fresh *v1alpha1.Remediation) {
			now := metav1.NewTime(s.Now())
			fresh.Status.Phase, fresh.Status.FinishedAt = v1alpha1.RemediationFailed, &now
			fresh.Status.Result = "agent: " + truncate(d.Error, 2000)
		}
	}
	// The reconciler may update the status concurrently (it just launched the
	// sandbox); retry on conflict against a fresh copy.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh v1alpha1.Remediation
		if err := s.Reader.Get(r.Context(), client.ObjectKeyFromObject(rem), &fresh); err != nil {
			return err
		}
		if !agentOpen(fresh.Status.Phase) {
			return errDecided
		}
		apply(&fresh)
		return s.Client.Status().Update(r.Context(), &fresh)
	})
	switch {
	case errors.Is(err, errDecided):
		http.Error(w, "remediation already decided", http.StatusConflict)
	case err != nil:
		s.Log.Error("store decision failed", "remediation", rem.Name, "err", err)
		http.Error(w, fmt.Sprintf("store decision: %v", err), http.StatusInternalServerError)
	default:
		s.Log.Info("remediation decided", "remediation", rem.Name, "proposal", d.Proposal != nil, "no_action", d.NoAction != "")
		w.WriteHeader(http.StatusNoContent)
	}
}

var errDecided = errors.New("remediation already decided")

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
