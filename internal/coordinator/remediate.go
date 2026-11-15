package coordinator

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/remediation"
)

// startRemediation is the step after a successful triage. It revokes the
// triage identity first: no two step identities exist at once. It resolves
// the alert's target, picks a RemediationSkill, creates the Remediation
// suspended, gives it an identity pinned to that target, and releases it. With
// no target or no matching skill the incident is done after triage.
func (r *Reconciler) startRemediation(ctx context.Context, inc *v1alpha1.Incident, run *v1alpha1.TriageRun) (ctrl.Result, error) {
	if err := r.revoke(ctx, IdentityName(inc.Name)); err != nil {
		return ctrl.Result{}, err
	}
	rem := &v1alpha1.Remediation{}
	key := types.NamespacedName{Namespace: r.Config.SandboxNamespace, Name: inc.Name}
	err := r.Client.Get(ctx, key, rem)
	switch {
	case apierrors.IsNotFound(err):
		var reason string
		rem, reason, err = r.newRemediation(ctx, inc, run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if rem == nil {
			return r.finish(ctx, inc, v1alpha1.IncidentDone, "triaged; no remediation: "+reason)
		}
		if err := r.Client.Create(ctx, rem); err != nil {
			return ctrl.Result{}, fmt.Errorf("create remediation %s: %w", rem.Name, err)
		}
		log.FromContext(ctx).Info("remediation created", "remediation", rem.Name, "skill", rem.Spec.SkillName,
			"target", remediation.TargetFrom(rem.Spec.Target).String())
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get remediation %s: %w", key.Name, err)
	}

	if err := r.provision(ctx, RemediationIdentity(rem)); err != nil {
		return ctrl.Result{}, err
	}
	if rem.Spec.Suspend {
		patch := client.MergeFrom(rem.DeepCopy())
		rem.Spec.Suspend = false
		if err := r.Client.Patch(ctx, rem, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("resume remediation %s: %w", rem.Name, err)
		}
	}
	inc.Status.Phase, inc.Status.Message, inc.Status.Remediation = v1alpha1.IncidentRemediating, "", rem.Name
	meta.SetStatusCondition(&inc.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionIdentityProvisioned,
		Status: metav1.ConditionTrue, Reason: "RemediationStarted", Message: "service account " + rem.Spec.ServiceAccount, ObservedGeneration: inc.Generation})
	if err := r.Client.Status().Update(ctx, inc); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: rem.Spec.Deadline.Add(r.Config.Grace).Sub(r.Now())}, nil
}

// newRemediation builds the Remediation for a triaged incident, or returns nil
// and the reason there is none.
func (r *Reconciler) newRemediation(ctx context.Context, inc *v1alpha1.Incident, run *v1alpha1.TriageRun) (*v1alpha1.Remediation, string, error) {
	if run.Status.Report == nil {
		return nil, "the triage run has no report", nil
	}
	labels := inc.Spec.Alert.Labels
	target, err := ResolveTarget(ctx, r.TargetReader, labels)
	switch {
	case errors.Is(err, ErrNoTarget), apierrors.IsNotFound(err), apierrors.IsForbidden(err):
		// Forbidden: the coordinator may read only namespaces open to remediation.
		return nil, "no target: " + err.Error(), nil
	case err != nil:
		return nil, "", fmt.Errorf("resolve target: %w", err)
	}
	var list v1alpha1.RemediationSkillList
	if err := r.Client.List(ctx, &list); err != nil {
		return nil, "", fmt.Errorf("list remediation skills: %w", err)
	}
	skill, ok := remediation.Choose(list.Items, labels, target)
	if !ok {
		return nil, "no RemediationSkill matches the alert and allows an action on " + target.String(), nil
	}
	i := -1
	for j := range list.Items {
		if list.Items[j].Name == skill.Name {
			i = j
		}
	}
	return &v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      inc.Name,
			Namespace: r.Config.SandboxNamespace,
			Labels:    map[string]string{v1alpha1.IncidentLabel: inc.Name, v1alpha1.StepLabel: StepRemediate},
		},
		Spec: v1alpha1.RemediationSpec{
			Incident:       inc.Name,
			Alert:          inc.Spec.Alert,
			TriageRun:      run.Name,
			Report:         *run.Status.Report.DeepCopy(),
			SkillName:      skill.Name,
			Skill:          *list.Items[i].Spec.DeepCopy(),
			Target:         v1alpha1.RemediationTarget{Namespace: target.Namespace, Kind: target.Kind, Name: target.Name},
			ServiceAccount: RemediationIdentityName(inc.Name),
			Model:          r.Config.Model,
			Deadline:       metav1.NewTime(r.Now().Add(r.Config.RemediationTimeout)),
			Suspend:        true,
		},
	}, "", nil
}

// watchRemediation waits for the Remediation to finish. The agent part has a
// deadline; a proposal waiting for a human does not, since the executor
// enforces its own approval timeout.
func (r *Reconciler) watchRemediation(ctx context.Context, inc *v1alpha1.Incident) (ctrl.Result, error) {
	var rem v1alpha1.Remediation
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Config.SandboxNamespace, Name: inc.Status.Remediation}, &rem)
	if apierrors.IsNotFound(err) {
		return r.finish(ctx, inc, v1alpha1.IncidentFailed, "remediation "+inc.Status.Remediation+" was deleted")
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get remediation %s: %w", inc.Status.Remediation, err)
	}
	if rem.Spec.Suspend {
		// Crashed between creating and releasing it: finish the start.
		var run v1alpha1.TriageRun
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Config.SandboxNamespace, Name: inc.Status.TriageRun}, &run); err != nil {
			return ctrl.Result{}, fmt.Errorf("get triage run %s: %w", inc.Status.TriageRun, err)
		}
		return r.startRemediation(ctx, inc, &run)
	}
	msg := fmt.Sprintf("remediation %s: %s", rem.Status.Phase, rem.Status.Result)
	switch rem.Status.Phase {
	case v1alpha1.RemediationSucceeded, v1alpha1.RemediationNoAction, v1alpha1.RemediationRejected:
		return r.finish(ctx, inc, v1alpha1.IncidentDone, msg)
	case v1alpha1.RemediationFailed:
		return r.finish(ctx, inc, v1alpha1.IncidentFailed, msg)
	case v1alpha1.RemediationProposed, v1alpha1.RemediationAwaitingApproval:
		return ctrl.Result{RequeueAfter: r.Config.Grace}, nil
	}
	giveUp := rem.Spec.Deadline.Add(r.Config.Grace)
	if !r.Now().Before(giveUp) {
		return r.finish(ctx, inc, v1alpha1.IncidentFailed, "remediation agent did not decide by its deadline")
	}
	return ctrl.Result{RequeueAfter: giveUp.Sub(r.Now())}, nil
}
