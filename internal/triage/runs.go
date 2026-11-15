package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/alert"
	"k8s-agent-sandbox/internal/report"
	"k8s-agent-sandbox/internal/sandbox"
)

// Status is a run's state as the HTTP API and notifiers show it.
type Status string

// Run statuses.
const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

var statusOf = map[v1alpha1.TriageRunPhase]Status{
	"":                       StatusPending,
	v1alpha1.TriagePending:   StatusPending,
	v1alpha1.TriageRunning:   StatusRunning,
	v1alpha1.TriageSucceeded: StatusDone,
	v1alpha1.TriageFailed:    StatusFailed,
}

// Run is one triage run as the HTTP API, the launcher, and notifiers see it.
// The TriageRun custom resource is the source of truth.
type Run struct {
	ID             string         `json:"id"`
	UID            types.UID      `json:"-"`
	Alert          alert.Alert    `json:"alert"`
	Status         Status         `json:"status"`
	ServiceAccount string         `json:"service_account"`
	Model          string         `json:"model"`
	Sandbox        string         `json:"sandbox,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	Deadline       time.Time      `json:"deadline"`
	FinishedAt     time.Time      `json:"finished_at,omitzero"`
	Report         *report.Report `json:"report,omitempty"`
	Error          string         `json:"error,omitempty"`
}

// RunFrom converts a TriageRun. A report that no longer decodes is reported
// as the run's error rather than dropped silently.
func RunFrom(tr *v1alpha1.TriageRun) Run {
	r := Run{
		ID:             tr.Name,
		UID:            tr.UID,
		Alert:          tr.Spec.Alert.Webhook(),
		Status:         statusOf[tr.Status.Phase],
		ServiceAccount: tr.Spec.ServiceAccount,
		Model:          tr.Spec.Model,
		Sandbox:        tr.Status.Sandbox,
		CreatedAt:      tr.CreationTimestamp.Time,
		Deadline:       tr.Spec.Deadline.Time,
		Error:          tr.Status.Error,
	}
	if tr.Status.FinishedAt != nil {
		r.FinishedAt = tr.Status.FinishedAt.Time
	}
	if tr.Status.Report != nil && len(tr.Status.Report.Raw) > 0 {
		var rep report.Report
		if err := json.Unmarshal(tr.Status.Report.Raw, &rep); err != nil {
			r.Error = fmt.Sprintf("stored report does not decode: %v", err)
		} else {
			r.Report = &rep
		}
	}
	return r
}

// Launcher starts and removes the sandbox for a run.
type Launcher interface {
	Launch(ctx context.Context, spec sandbox.Spec) (sandboxName string, err error)
	Delete(ctx context.Context, sandboxName string) error
}

// SandboxSpec is the sandbox a TriageRun gets.
func SandboxSpec(tr *v1alpha1.TriageRun) sandbox.Spec {
	return sandbox.Spec{
		Step: sandbox.StepTriage, ID: tr.Name, OwnerKind: "TriageRun", OwnerUID: tr.UID,
		ServiceAccount: tr.Spec.ServiceAccount, Model: tr.Spec.Model, Deadline: tr.Spec.Deadline.Time,
	}
}

// RunOwner finds a TriageRun's ServiceAccount for sandbox.PodAuth.
func RunOwner(c client.Reader, namespace string) sandbox.OwnerLookup {
	return func(ctx context.Context, name string) (string, error) {
		var tr v1alpha1.TriageRun
		err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &tr)
		if apierrors.IsNotFound(err) {
			return "", sandbox.ErrNoStep
		}
		if err != nil {
			return "", err
		}
		return tr.Spec.ServiceAccount, nil
	}
}

// Notifier publishes a finished run.
type Notifier interface {
	Notify(ctx context.Context, run Run) error
}

// RunReconciler runs TriageRuns: it launches the sandbox once the coordinator
// has released the run, fails it at its deadline, and when it ends posts the
// result and removes the sandbox. The coordinator owns the run's spec and
// identity; this reconciler writes only its status.
type RunReconciler struct {
	Client   client.Client
	Launcher Launcher
	Notifier Notifier
	Log      *slog.Logger
	Now      func() time.Time
}

// SetupWithManager registers the reconciler.
func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("triagerun").For(&v1alpha1.TriageRun{}).Complete(r)
}

// Reconcile implements reconcile.Reconciler.
func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var tr v1alpha1.TriageRun
	if err := r.Client.Get(ctx, req.NamespacedName, &tr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch {
	case tr.Spec.Suspend:
		return ctrl.Result{}, nil // identity not ready yet
	case tr.Status.Phase.Terminal():
		return ctrl.Result{}, r.publish(ctx, &tr)
	case !r.Now().Before(tr.Spec.Deadline.Time):
		return ctrl.Result{}, r.setFinished(ctx, &tr, nil, "timed out without a report")
	case tr.Status.Sandbox == "":
		return r.launch(ctx, &tr)
	}
	return ctrl.Result{RequeueAfter: tr.Spec.Deadline.Sub(r.Now())}, nil
}

func (r *RunReconciler) launch(ctx context.Context, tr *v1alpha1.TriageRun) (ctrl.Result, error) {
	name, err := r.Launcher.Launch(ctx, SandboxSpec(tr))
	if err != nil {
		r.Log.Error("launch failed", "run", tr.Name, "err", err)
		return ctrl.Result{}, r.setFinished(ctx, tr, nil, fmt.Sprintf("launch sandbox: %v", err))
	}
	now := metav1.NewTime(r.Now())
	tr.Status.Phase, tr.Status.Sandbox, tr.Status.StartedAt = v1alpha1.TriageRunning, name, &now
	if err := r.Client.Status().Update(ctx, tr); err != nil {
		return ctrl.Result{}, err
	}
	r.Log.Info("run started", "run", tr.Name, "alert", tr.Spec.Alert.Labels["alertname"],
		"sandbox", name, "service_account", tr.Spec.ServiceAccount)
	return ctrl.Result{RequeueAfter: tr.Spec.Deadline.Sub(r.Now())}, nil
}

// publish posts a finished run once and removes its sandbox. If the status
// update after notifying fails, the next reconcile notifies again: on-call may
// see a duplicate, never nothing.
func (r *RunReconciler) publish(ctx context.Context, tr *v1alpha1.TriageRun) error {
	if tr.Status.Notified {
		return nil
	}
	run := RunFrom(tr)
	r.Log.Info("run finished", "run", run.ID, "status", run.Status, "alert", run.Alert.Name(), "error", run.Error)
	if err := r.Notifier.Notify(ctx, run); err != nil {
		r.Log.Error("notify failed", "run", run.ID, "err", err)
	}
	if run.Sandbox != "" {
		if err := r.Launcher.Delete(ctx, run.Sandbox); err != nil {
			return err
		}
	}
	tr.Status.Notified = true
	return r.Client.Status().Update(ctx, tr)
}

// setFinished records a terminal result. rep is nil for a failure.
func (r *RunReconciler) setFinished(ctx context.Context, tr *v1alpha1.TriageRun, rep *report.Report, errMsg string) error {
	return finishStatus(ctx, r.Client, tr, rep, errMsg, r.Now())
}

func finishStatus(ctx context.Context, c client.Client, tr *v1alpha1.TriageRun, rep *report.Report, errMsg string, now time.Time) error {
	t := metav1.NewTime(now)
	tr.Status.FinishedAt = &t
	if rep != nil {
		raw, err := json.Marshal(rep)
		if err != nil {
			return fmt.Errorf("encode report: %w", err)
		}
		tr.Status.Phase, tr.Status.Report = v1alpha1.TriageSucceeded, &runtime.RawExtension{Raw: raw}
	} else {
		tr.Status.Phase, tr.Status.Error = v1alpha1.TriageFailed, errMsg
	}
	if err := c.Status().Update(ctx, tr); err != nil {
		return fmt.Errorf("update triage run %s status: %w", tr.Name, err)
	}
	return nil
}
