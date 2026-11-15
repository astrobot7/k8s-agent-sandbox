// Package actions performs remediation actions on the cluster. It is the only
// code in the project that holds Kubernetes write access to workloads; callers
// must have checked the proposal (remediation.Skill.Check and the run's RBAC)
// first.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"k8s-agent-sandbox/internal/remediation"
)

// ErrUnsupported means the action has no driver for the target kind.
var ErrUnsupported = errors.New("unsupported action")

// restartedAtAnnotation is what `kubectl rollout restart` sets.
const restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

// Kube performs actions with a typed client. Its RBAC should be a Role per
// namespace open to remediation, never cluster-wide.
type Kube struct {
	Client kubernetes.Interface
	Now    func() time.Time
}

// Do performs p and says what it did.
func (k Kube) Do(ctx context.Context, p remediation.Proposal) (string, error) {
	t := p.Target
	switch p.Action {
	case remediation.ActionRestartPod:
		if t.Kind != "Pod" {
			break
		}
		if err := k.Client.CoreV1().Pods(t.Namespace).Delete(ctx, t.Name, metav1.DeleteOptions{}); err != nil {
			return "", fmt.Errorf("delete pod %s: %w", t, err)
		}
		return "deleted pod " + t.String() + "; its controller recreates it", nil
	case remediation.ActionRolloutRestart:
		return k.rolloutRestart(ctx, t)
	case remediation.ActionScale:
		return k.scale(ctx, t, *p.Params.Replicas)
	case remediation.ActionOpenPR:
		// No PR driver is configured yet; record what would have happened.
		return "open-pr is not configured: nothing changed. Rationale: " + p.Rationale, nil
	}
	return "", fmt.Errorf("%w: %s on %s", ErrUnsupported, p.Action, t.Kind)
}

func (k Kube) rolloutRestart(ctx context.Context, t remediation.Target) (string, error) {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{
			restartedAtAnnotation: k.Now().UTC().Format(time.RFC3339),
		}},
	}}})
	if err != nil {
		return "", err
	}
	apps := k.Client.AppsV1()
	opts := metav1.PatchOptions{FieldManager: "remediation-executor"}
	switch t.Kind {
	case "Deployment":
		_, err = apps.Deployments(t.Namespace).Patch(ctx, t.Name, types.StrategicMergePatchType, patch, opts)
	case "StatefulSet":
		_, err = apps.StatefulSets(t.Namespace).Patch(ctx, t.Name, types.StrategicMergePatchType, patch, opts)
	case "DaemonSet":
		_, err = apps.DaemonSets(t.Namespace).Patch(ctx, t.Name, types.StrategicMergePatchType, patch, opts)
	default:
		return "", fmt.Errorf("%w: rollout-restart on %s", ErrUnsupported, t.Kind)
	}
	if err != nil {
		return "", fmt.Errorf("rollout restart %s: %w", t, err)
	}
	return "rolling restart of " + t.String() + " started", nil
}

func (k Kube) scale(ctx context.Context, t remediation.Target, replicas int32) (string, error) {
	apps := k.Client.AppsV1()
	var (
		cur *autoscalingv1.Scale
		err error
	)
	switch t.Kind {
	case "Deployment":
		cur, err = apps.Deployments(t.Namespace).GetScale(ctx, t.Name, metav1.GetOptions{})
	case "StatefulSet":
		cur, err = apps.StatefulSets(t.Namespace).GetScale(ctx, t.Name, metav1.GetOptions{})
	default:
		return "", fmt.Errorf("%w: scale on %s", ErrUnsupported, t.Kind)
	}
	if err != nil {
		return "", fmt.Errorf("get scale of %s: %w", t, err)
	}
	from := cur.Spec.Replicas
	cur.Spec.Replicas = replicas
	if t.Kind == "Deployment" {
		_, err = apps.Deployments(t.Namespace).UpdateScale(ctx, t.Name, cur, metav1.UpdateOptions{})
	} else {
		_, err = apps.StatefulSets(t.Namespace).UpdateScale(ctx, t.Name, cur, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("scale %s: %w", t, err)
	}
	return fmt.Sprintf("scaled %s from %d to %d replicas", t, from, replicas), nil
}
