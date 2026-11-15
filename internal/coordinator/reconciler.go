package coordinator

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s-agent-sandbox/api/v1alpha1"
)

// Config tunes the coordinator.
type Config struct {
	// IncidentNamespace holds Incidents; SandboxNamespace holds step
	// resources, their identities, and the sandboxes.
	IncidentNamespace string
	SandboxNamespace  string
	// Model every triage run may use.
	Model string
	// RunTimeout is each TriageRun's deadline. The triage controller enforces
	// it; the coordinator gives up Grace later if the run is still not done.
	RunTimeout time.Duration
	Grace      time.Duration
	// MaxConcurrent caps active triage runs; further incidents wait as Pending.
	MaxConcurrent int
	// Retention is how long finished incidents and their runs are kept.
	Retention time.Duration
	// Remediation enables the remediation step after a successful triage.
	Remediation bool
	// RemediationTimeout is the remediation agent's deadline to decide.
	RemediationTimeout time.Duration
}

// pendingRetry is how often a Pending incident checks for a free run slot.
const pendingRetry = 10 * time.Second

// Reconciler drives Incidents: it provisions a step's identity, starts the
// step, waits for it, revokes the identity, and records the outcome.
type Reconciler struct {
	Client client.Client
	// TargetReader reads pods and workloads to resolve a remediation target.
	// It should bypass the cache: the coordinator may get, not watch, them.
	TargetReader client.Reader
	Config       Config
	Now          func() time.Time
}

// SetupWithManager registers the reconciler. TriageRun and Remediation
// changes wake the Incident named in their spec.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	toIncident := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		var incident string
		switch step := o.(type) {
		case *v1alpha1.TriageRun:
			incident = step.Spec.Incident
		case *v1alpha1.Remediation:
			incident = step.Spec.Incident
		}
		if incident == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: r.Config.IncidentNamespace, Name: incident}}}
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("coordinator").
		For(&v1alpha1.Incident{}).
		Watches(&v1alpha1.TriageRun{}, toIncident)
	if r.Config.Remediation {
		b = b.Watches(&v1alpha1.Remediation{}, toIncident)
	}
	return b.Complete(r)
}

// Reconcile implements reconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var inc v1alpha1.Incident
	if err := r.Client.Get(ctx, req.NamespacedName, &inc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if inc.Status.Phase.Terminal() {
		return r.finished(ctx, &inc)
	}
	switch {
	case inc.Status.Remediation != "":
		return r.watchRemediation(ctx, &inc)
	case inc.Status.TriageRun == "":
		return r.startTriage(ctx, &inc)
	}
	return r.watchTriage(ctx, &inc)
}

// startTriage creates the TriageRun suspended, gives it an identity, and only
// then lets the triage controller start it.
func (r *Reconciler) startTriage(ctx context.Context, inc *v1alpha1.Incident) (ctrl.Result, error) {
	run := &v1alpha1.TriageRun{}
	key := types.NamespacedName{Namespace: r.Config.SandboxNamespace, Name: inc.Name}
	err := r.Client.Get(ctx, key, run)
	switch {
	case apierrors.IsNotFound(err):
		active, err := r.activeRuns(ctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		if active >= r.Config.MaxConcurrent {
			msg := fmt.Sprintf("waiting: %d triage runs active", active)
			if inc.Status.Phase != v1alpha1.IncidentPending || inc.Status.Message != msg {
				inc.Status.Phase, inc.Status.Message = v1alpha1.IncidentPending, msg
				if err := r.Client.Status().Update(ctx, inc); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{RequeueAfter: pendingRetry}, nil
		}
		run = r.newTriageRun(inc)
		if err := r.Client.Create(ctx, run); err != nil {
			return ctrl.Result{}, fmt.Errorf("create triage run %s: %w", run.Name, err)
		}
		log.FromContext(ctx).Info("triage run created", "run", run.Name, "alert", inc.Spec.Alert.Labels["alertname"])
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get triage run %s: %w", key.Name, err)
	}

	if err := r.provision(ctx, TriageIdentity(run)); err != nil {
		return ctrl.Result{}, err
	}
	if run.Spec.Suspend {
		patch := client.MergeFrom(run.DeepCopy())
		run.Spec.Suspend = false
		if err := r.Client.Patch(ctx, run, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("resume triage run %s: %w", run.Name, err)
		}
	}
	inc.Status.Phase, inc.Status.Message, inc.Status.TriageRun = v1alpha1.IncidentTriaging, "", run.Name
	meta.SetStatusCondition(&inc.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionIdentityProvisioned,
		Status: metav1.ConditionTrue, Reason: "TriageStarted", Message: "service account " + run.Spec.ServiceAccount, ObservedGeneration: inc.Generation})
	if err := r.Client.Status().Update(ctx, inc); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: run.Spec.Deadline.Add(r.Config.Grace).Sub(r.Now())}, nil
}

func (r *Reconciler) newTriageRun(inc *v1alpha1.Incident) *v1alpha1.TriageRun {
	return &v1alpha1.TriageRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      inc.Name,
			Namespace: r.Config.SandboxNamespace,
			Labels:    map[string]string{v1alpha1.IncidentLabel: inc.Name, v1alpha1.StepLabel: StepTriage},
		},
		Spec: v1alpha1.TriageRunSpec{
			Incident:       inc.Name,
			Alert:          inc.Spec.Alert,
			ServiceAccount: IdentityName(inc.Name),
			Model:          r.Config.Model,
			Deadline:       metav1.NewTime(r.Now().Add(r.Config.RunTimeout)),
			Suspend:        true,
		},
	}
}

// watchTriage waits for the TriageRun to finish, then revokes its identity and
// records the outcome. A run still unfinished Grace after its deadline is
// abandoned: the triage controller is down or stuck, and the agent must not
// keep its credentials.
func (r *Reconciler) watchTriage(ctx context.Context, inc *v1alpha1.Incident) (ctrl.Result, error) {
	var run v1alpha1.TriageRun
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Config.SandboxNamespace, Name: inc.Status.TriageRun}, &run)
	if apierrors.IsNotFound(err) {
		return r.finish(ctx, inc, v1alpha1.IncidentFailed, "triage run "+inc.Status.TriageRun+" was deleted")
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get triage run %s: %w", inc.Status.TriageRun, err)
	}
	switch run.Status.Phase {
	case v1alpha1.TriageSucceeded:
		if r.Config.Remediation {
			return r.startRemediation(ctx, inc, &run)
		}
		return r.finish(ctx, inc, v1alpha1.IncidentDone, "")
	case v1alpha1.TriageFailed:
		return r.finish(ctx, inc, v1alpha1.IncidentFailed, "triage failed: "+run.Status.Error)
	}
	giveUp := run.Spec.Deadline.Add(r.Config.Grace)
	if !r.Now().Before(giveUp) {
		return r.finish(ctx, inc, v1alpha1.IncidentFailed, "triage run did not finish by its deadline")
	}
	return ctrl.Result{RequeueAfter: giveUp.Sub(r.Now())}, nil
}

// finish revokes every step identity and marks the incident terminal.
func (r *Reconciler) finish(ctx context.Context, inc *v1alpha1.Incident, phase v1alpha1.IncidentPhase, msg string) (ctrl.Result, error) {
	if err := r.revokeAll(ctx, inc.Name); err != nil {
		return ctrl.Result{}, err
	}
	now := metav1.NewTime(r.Now())
	inc.Status.Phase, inc.Status.Message, inc.Status.FinishedAt = phase, msg, &now
	meta.SetStatusCondition(&inc.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionIdentityProvisioned,
		Status: metav1.ConditionFalse, Reason: "StepFinished", Message: "identity revoked", ObservedGeneration: inc.Generation})
	if err := r.Client.Status().Update(ctx, inc); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("incident finished", "incident", inc.Name, "phase", phase, "message", msg)
	return ctrl.Result{RequeueAfter: r.Config.Retention}, nil
}

// finished keeps a terminal incident's identities revoked and deletes the
// incident and its steps after Retention.
func (r *Reconciler) finished(ctx context.Context, inc *v1alpha1.Incident) (ctrl.Result, error) {
	if err := r.revokeAll(ctx, inc.Name); err != nil {
		return ctrl.Result{}, err
	}
	if inc.Status.FinishedAt == nil {
		return ctrl.Result{}, nil
	}
	expiry := inc.Status.FinishedAt.Add(r.Config.Retention)
	if r.Now().Before(expiry) {
		return ctrl.Result{RequeueAfter: expiry.Sub(r.Now())}, nil
	}
	if inc.Status.TriageRun != "" {
		run := &v1alpha1.TriageRun{ObjectMeta: metav1.ObjectMeta{Namespace: r.Config.SandboxNamespace, Name: inc.Status.TriageRun}}
		if err := r.Client.Delete(ctx, run); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("delete triage run %s: %w", run.Name, err)
		}
	}
	if inc.Status.Remediation != "" {
		rem := &v1alpha1.Remediation{ObjectMeta: metav1.ObjectMeta{Namespace: r.Config.SandboxNamespace, Name: inc.Status.Remediation}}
		if err := r.Client.Delete(ctx, rem); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("delete remediation %s: %w", rem.Name, err)
		}
	}
	if err := r.Client.Delete(ctx, inc); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("delete incident %s: %w", inc.Name, err)
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) activeRuns(ctx context.Context) (int, error) {
	var runs v1alpha1.TriageRunList
	if err := r.Client.List(ctx, &runs, client.InNamespace(r.Config.SandboxNamespace)); err != nil {
		return 0, fmt.Errorf("list triage runs: %w", err)
	}
	n := 0
	for _, run := range runs.Items {
		if !run.Status.Phase.Terminal() {
			n++
		}
	}
	if !r.Config.Remediation {
		return n, nil
	}
	// A remediation holds a slot while its agent sandbox runs.
	var rems v1alpha1.RemediationList
	if err := r.Client.List(ctx, &rems, client.InNamespace(r.Config.SandboxNamespace)); err != nil {
		return 0, fmt.Errorf("list remediations: %w", err)
	}
	for _, rem := range rems.Items {
		if p := rem.Status.Phase; p == "" || p == v1alpha1.RemediationPending || p == v1alpha1.RemediationRunning {
			n++
		}
	}
	return n, nil
}

// provision creates the identity's objects, ServiceAccount and Role before the
// RoleBinding that joins them. Existing objects are left as they are: the
// names are unique per run and nothing else may create them.
func (r *Reconciler) provision(ctx context.Context, id Identity) error {
	if err := id.Allowed(); err != nil {
		return err
	}
	for _, o := range []client.Object{id.ServiceAccount, id.Role, id.RoleBinding} {
		if err := r.Client.Create(ctx, o); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create %T %s: %w", o, o.GetName(), err)
		}
	}
	return nil
}

// revokeAll deletes every step identity of an incident.
func (r *Reconciler) revokeAll(ctx context.Context, incident string) error {
	if err := r.revoke(ctx, IdentityName(incident)); err != nil {
		return err
	}
	return r.revoke(ctx, RemediationIdentityName(incident))
}

// revoke deletes a step identity, binding first so the ServiceAccount loses
// its permissions before it disappears. Deleting the ServiceAccount
// invalidates every token issued for it.
func (r *Reconciler) revoke(ctx context.Context, name string) error {
	id := stepIdentity(name, r.Config.SandboxNamespace, metav1.OwnerReference{}, nil, nil)
	for _, o := range []client.Object{id.RoleBinding, id.Role, id.ServiceAccount} {
		if err := r.Client.Delete(ctx, o); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete %T %s: %w", o, name, err)
		}
	}
	return nil
}
