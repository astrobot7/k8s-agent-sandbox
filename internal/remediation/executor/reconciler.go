// Package executor is the remediation module of the triage controller. It runs
// Remediations the coordinator releases: it launches the remediation agent's
// sandbox, takes the agent's decision over the agent API, checks the proposal
// against the skill and the run's RBAC, waits for approval when the skill
// requires it, and performs the action through a Driver.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/remediation"
	"k8s-agent-sandbox/internal/report"
	"k8s-agent-sandbox/internal/sandbox"
)

// Mode is the executor's kill switch.
type Mode string

// Modes.
const (
	ModeLive   Mode = "live"    // perform actions
	ModeDryRun Mode = "dry-run" // record what would have been done
	ModeOff    Mode = "off"     // reject every proposal
)

// Config tunes the executor.
type Config struct {
	Namespace       string        // sandbox namespace: Remediations and their Roles
	Mode            Mode          // kill switch
	ApprovalTimeout time.Duration // a proposal nobody approves is rejected after this
	Cooldown        time.Duration // at most one action per target within this
}

// Launcher starts and removes agent sandboxes.
type Launcher interface {
	Launch(ctx context.Context, spec sandbox.Spec) (string, error)
	Delete(ctx context.Context, name string) error
}

// Authorizer runs SubjectAccessReviews; identity.Verifier implements it.
type Authorizer interface {
	Authorize(ctx context.Context, c identity.Caller, attrs authzv1.ResourceAttributes) error
}

// Driver performs a checked proposal.
type Driver interface {
	Do(ctx context.Context, p remediation.Proposal) (string, error)
}

// Notifier posts a message to on-call.
type Notifier func(ctx context.Context, text string) error

// Reconciler drives Remediations. It writes only their status.
type Reconciler struct {
	Client   client.Client
	Launcher Launcher
	Access   Authorizer
	Driver   Driver
	Notify   Notifier
	Config   Config
	Log      *slog.Logger
	Now      func() time.Time
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("remediation").For(&v1alpha1.Remediation{}).Complete(r)
}

// Reconcile implements reconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rem v1alpha1.Remediation
	if err := r.Client.Get(ctx, req.NamespacedName, &rem); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if rem.Spec.Suspend {
		return ctrl.Result{}, nil // identity not ready yet
	}
	switch {
	case rem.Status.Phase.Terminal():
		return ctrl.Result{}, r.publish(ctx, &rem)
	case rem.Status.Phase == v1alpha1.RemediationProposed:
		return r.proposed(ctx, &rem)
	case rem.Status.Phase == v1alpha1.RemediationAwaitingApproval:
		return r.awaitingApproval(ctx, &rem)
	}
	// Pending or Running: the agent part.
	if !r.Now().Before(rem.Spec.Deadline.Time) {
		return ctrl.Result{}, r.finish(ctx, &rem, v1alpha1.RemediationFailed, "the agent did not decide by its deadline")
	}
	if rem.Status.Sandbox == "" {
		return r.launch(ctx, &rem)
	}
	return ctrl.Result{RequeueAfter: rem.Spec.Deadline.Sub(r.Now())}, nil
}

// SandboxSpec is the sandbox a Remediation gets: the agent image in
// remediation mode.
func SandboxSpec(rem *v1alpha1.Remediation) sandbox.Spec {
	return sandbox.Spec{
		Step: sandbox.StepRemediate, ID: rem.Name, OwnerKind: "Remediation", OwnerUID: rem.UID,
		ServiceAccount: rem.Spec.ServiceAccount, Model: rem.Spec.Model, Deadline: rem.Spec.Deadline.Time,
		Env: map[string]string{"AGENT_MODE": "remediate"},
	}
}

func (r *Reconciler) launch(ctx context.Context, rem *v1alpha1.Remediation) (ctrl.Result, error) {
	name, err := r.Launcher.Launch(ctx, SandboxSpec(rem))
	if err != nil {
		r.Log.Error("launch failed", "remediation", rem.Name, "err", err)
		return ctrl.Result{}, r.finish(ctx, rem, v1alpha1.RemediationFailed, fmt.Sprintf("launch sandbox: %v", err))
	}
	now := metav1.NewTime(r.Now())
	rem.Status.Phase, rem.Status.Sandbox, rem.Status.StartedAt = v1alpha1.RemediationRunning, name, &now
	if err := r.Client.Status().Update(ctx, rem); err != nil {
		return ctrl.Result{}, err
	}
	r.Log.Info("remediation started", "remediation", rem.Name, "target", targetOf(rem).String(),
		"skill", rem.Spec.SkillName, "sandbox", name)
	return ctrl.Result{RequeueAfter: rem.Spec.Deadline.Sub(r.Now())}, nil
}

// proposed handles a fresh proposal: the agent is done, so its sandbox goes;
// the skill decides whether a human must approve.
func (r *Reconciler) proposed(ctx context.Context, rem *v1alpha1.Remediation) (ctrl.Result, error) {
	if err := r.deleteSandbox(ctx, rem); err != nil {
		return ctrl.Result{}, err
	}
	if rem.Status.ExecutedAt != nil {
		// The executor stopped between starting the action and recording the
		// result. Actions run at most once, so do not repeat it.
		return ctrl.Result{}, r.finish(ctx, rem, v1alpha1.RemediationFailed,
			"the executor restarted while performing the action; check "+targetOf(rem).String())
	}
	p := proposalOf(rem)
	mode := skillOf(rem).ApprovalFor(p.Action)
	rem.Status.ApprovalMode = string(mode)
	if mode == remediation.ApprovalAuto {
		return ctrl.Result{}, r.execute(ctx, rem)
	}
	rem.Status.Phase = v1alpha1.RemediationAwaitingApproval
	if err := r.Client.Status().Update(ctx, rem); err != nil {
		return ctrl.Result{}, err
	}
	r.post(ctx, fmt.Sprintf("Remediation %s needs approval: %s on %s.\nWhy: %s\n"+
		"Approve: kubectl -n %s patch remediation %s --type merge -p '{\"spec\":{\"approval\":\"Approved\"}}'\n"+
		"Reject: the same with \"Rejected\". Expires in %s.",
		rem.Name, p.Action, p.Target, p.Rationale, rem.Namespace, rem.Name, r.Config.ApprovalTimeout))
	return ctrl.Result{RequeueAfter: r.Config.ApprovalTimeout}, nil
}

func (r *Reconciler) awaitingApproval(ctx context.Context, rem *v1alpha1.Remediation) (ctrl.Result, error) {
	switch rem.Spec.Approval {
	case v1alpha1.ApprovalApproved:
		return ctrl.Result{}, r.execute(ctx, rem)
	case v1alpha1.ApprovalRejected:
		return ctrl.Result{}, r.finish(ctx, rem, v1alpha1.RemediationRejected, "rejected by a human")
	}
	since := rem.CreationTimestamp.Time
	if rem.Status.ProposedAt != nil {
		since = rem.Status.ProposedAt.Time
	}
	expires := since.Add(r.Config.ApprovalTimeout)
	if !r.Now().Before(expires) {
		return ctrl.Result{}, r.finish(ctx, rem, v1alpha1.RemediationRejected, "nobody approved it within "+r.Config.ApprovalTimeout.String())
	}
	return ctrl.Result{RequeueAfter: expires.Sub(r.Now())}, nil
}

// execute re-checks everything, then acts once. The checks repeat the agent
// API's because the run's RBAC may have been revoked while a human decided.
func (r *Reconciler) execute(ctx context.Context, rem *v1alpha1.Remediation) error {
	p := proposalOf(rem)
	if r.Config.Mode == ModeOff {
		return r.finish(ctx, rem, v1alpha1.RemediationRejected, "remediation is switched off (REMEDIATION_MODE=off)")
	}
	if err := Check(ctx, r.Access, rem, p); err != nil {
		return r.finish(ctx, rem, v1alpha1.RemediationRejected, err.Error())
	}
	last, cooling, err := r.lastActionOn(ctx, rem)
	if err != nil {
		return err
	}
	if cooling {
		return r.finish(ctx, rem, v1alpha1.RemediationRejected,
			fmt.Sprintf("cooldown: remediation %s acted on %s less than %s ago", last, p.Target, r.Config.Cooldown))
	}
	if r.Config.Mode == ModeDryRun {
		return r.finish(ctx, rem, v1alpha1.RemediationSucceeded, fmt.Sprintf("dry run: would %s %s", p.Action, p.Target))
	}
	// Record the attempt before acting, so a crash never repeats the action.
	now := metav1.NewTime(r.Now())
	rem.Status.ExecutedAt = &now
	if err := r.Client.Status().Update(ctx, rem); err != nil {
		return err
	}
	r.Log.Info("performing remediation", "remediation", rem.Name, "action", p.Action, "target", p.Target.String())
	msg, err := r.Driver.Do(ctx, p)
	if err != nil {
		return r.finish(ctx, rem, v1alpha1.RemediationFailed, err.Error())
	}
	return r.finish(ctx, rem, v1alpha1.RemediationSucceeded, msg)
}

// Check validates p against the remediation's skill and target, then asks
// RBAC whether the remediation's own ServiceAccount may perform it.
func Check(ctx context.Context, access Authorizer, rem *v1alpha1.Remediation, p remediation.Proposal) error {
	if err := skillOf(rem).Check(p, targetOf(rem)); err != nil {
		return err
	}
	sa := identity.Caller{
		Username:       "system:serviceaccount:" + rem.Namespace + ":" + rem.Spec.ServiceAccount,
		Groups:         []string{"system:serviceaccounts", "system:serviceaccounts:" + rem.Namespace, "system:authenticated"},
		Namespace:      rem.Namespace,
		ServiceAccount: rem.Spec.ServiceAccount,
	}
	return access.Authorize(ctx, sa, remediation.AccessFor(p, rem.Namespace))
}

// lastActionOn finds another remediation that acted on the same target within
// the cooldown.
func (r *Reconciler) lastActionOn(ctx context.Context, rem *v1alpha1.Remediation) (string, bool, error) {
	var list v1alpha1.RemediationList
	if err := r.Client.List(ctx, &list, client.InNamespace(rem.Namespace)); err != nil {
		return "", false, fmt.Errorf("list remediations: %w", err)
	}
	for _, o := range list.Items {
		if o.Name == rem.Name || o.Spec.Target != rem.Spec.Target || o.Status.ExecutedAt == nil {
			continue
		}
		if r.Now().Sub(o.Status.ExecutedAt.Time) < r.Config.Cooldown {
			return o.Name, true, nil
		}
	}
	return "", false, nil
}

func (r *Reconciler) finish(ctx context.Context, rem *v1alpha1.Remediation, phase v1alpha1.RemediationPhase, result string) error {
	return finishStatus(ctx, r.Client, rem, phase, result, r.Now())
}

func finishStatus(ctx context.Context, c client.Client, rem *v1alpha1.Remediation, phase v1alpha1.RemediationPhase, result string, now time.Time) error {
	t := metav1.NewTime(now)
	rem.Status.Phase, rem.Status.Result, rem.Status.FinishedAt = phase, truncate(result, 2000), &t
	if err := c.Status().Update(ctx, rem); err != nil {
		return fmt.Errorf("update remediation %s status: %w", rem.Name, err)
	}
	return nil
}

// publish posts the outcome once and removes the sandbox. It claims the
// notification (Notified=true) before posting: a reconcile working from a
// stale cached copy then fails the update with a conflict instead of posting
// a second time. If the post itself fails, the message is lost and logged.
func (r *Reconciler) publish(ctx context.Context, rem *v1alpha1.Remediation) error {
	if !rem.Status.Notified {
		rem.Status.Notified = true
		if err := r.Client.Status().Update(ctx, rem); err != nil {
			return err
		}
		r.Log.Info("remediation finished", "remediation", rem.Name, "phase", rem.Status.Phase, "result", rem.Status.Result)
		r.post(ctx, Summary(rem))
	}
	return r.deleteSandbox(ctx, rem)
}

func (r *Reconciler) deleteSandbox(ctx context.Context, rem *v1alpha1.Remediation) error {
	if rem.Status.Sandbox == "" {
		return nil
	}
	return r.Launcher.Delete(ctx, rem.Status.Sandbox)
}

// post notifies on-call. A failure is logged, not returned: on-call may miss a
// message, but the Remediation's status always holds the outcome.
func (r *Reconciler) post(ctx context.Context, text string) {
	if r.Notify == nil {
		return
	}
	if err := r.Notify(ctx, text); err != nil {
		r.Log.Error("notify failed", "err", err)
	}
}

// Summary is the message posted when a remediation ends.
func Summary(rem *v1alpha1.Remediation) string {
	what := "none proposed"
	if p := rem.Status.Proposal; p != nil {
		what = string(p.Action)
		if p.Replicas != nil {
			what += fmt.Sprintf(" to %d replicas", *p.Replicas)
		}
	}
	return fmt.Sprintf("Remediation %s (%s) on %s: %s\nAction: %s\nResult: %s",
		rem.Name, rem.Spec.SkillName, targetOf(rem), rem.Status.Phase, what, rem.Status.Result)
}

func skillOf(rem *v1alpha1.Remediation) remediation.Skill {
	return remediation.SkillFrom(rem.Spec.SkillName, rem.Spec.Skill)
}

func targetOf(rem *v1alpha1.Remediation) remediation.Target {
	return remediation.TargetFrom(rem.Spec.Target)
}

func proposalOf(rem *v1alpha1.Remediation) remediation.Proposal {
	if rem.Status.Proposal == nil {
		return remediation.Proposal{Target: targetOf(rem)}
	}
	return remediation.ProposalFrom(*rem.Status.Proposal, rem.Spec.Target)
}

// reportOf decodes the triage report the remediation acts on.
func reportOf(rem *v1alpha1.Remediation) (*report.Report, error) {
	if len(rem.Spec.Report.Raw) == 0 {
		return nil, nil
	}
	var rep report.Report
	if err := json.Unmarshal(rem.Spec.Report.Raw, &rep); err != nil {
		return nil, fmt.Errorf("decode triage report: %w", err)
	}
	return &rep, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
