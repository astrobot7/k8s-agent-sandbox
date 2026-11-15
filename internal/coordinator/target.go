package coordinator

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s-agent-sandbox/internal/remediation"
)

// ErrNoTarget means the alert does not name an object remediation could act on.
var ErrNoTarget = errors.New("alert names no remediation target")

// Alert labels that name the object an alert is about. The warning-event rule
// sets the k8s.object.* labels; metric rules usually carry the workload labels.
var workloadLabels = []struct{ label, kind string }{
	{"k8s.deployment.name", "Deployment"},
	{"k8s.statefulset.name", "StatefulSet"},
	{"k8s.daemonset.name", "DaemonSet"},
	{"k8s.pod.name", "Pod"},
}

// ResolveTarget finds the object a remediation acts on: the workload that
// controls the alert's object. A Pod owned by a ReplicaSet of a Deployment
// resolves to the Deployment, since its pods are replaced by name on every
// restart; a bare Pod stays a Pod. Labels are untrusted, so every step is
// checked against the live objects.
func ResolveTarget(ctx context.Context, c client.Reader, labels map[string]string) (remediation.Target, error) {
	ns := labels["k8s.namespace.name"]
	kind, name := labels["k8s.object.kind"], labels["k8s.object.name"]
	if kind == "" || name == "" {
		for _, wl := range workloadLabels {
			if v := labels[wl.label]; v != "" {
				kind, name = wl.kind, v
				break
			}
		}
	}
	if ns == "" || kind == "" || name == "" {
		return remediation.Target{}, ErrNoTarget
	}
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		return verifyWorkload(ctx, c, remediation.Target{Namespace: ns, Kind: kind, Name: name})
	case "Pod":
		return podOwner(ctx, c, ns, name)
	}
	return remediation.Target{}, fmt.Errorf("%w: kind %s", ErrNoTarget, kind)
}

func podOwner(ctx context.Context, c client.Reader, ns, name string) (remediation.Target, error) {
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &pod); err != nil {
		return remediation.Target{}, fmt.Errorf("get pod %s/%s: %w", ns, name, err)
	}
	owner := metav1.GetControllerOf(&pod)
	switch {
	case owner == nil:
		return remediation.Target{Namespace: ns, Kind: "Pod", Name: name}, nil
	case owner.Kind == "StatefulSet" || owner.Kind == "DaemonSet":
		return verifyWorkload(ctx, c, remediation.Target{Namespace: ns, Kind: owner.Kind, Name: owner.Name})
	case owner.Kind == "ReplicaSet":
		var rs appsv1.ReplicaSet
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: owner.Name}, &rs); err != nil {
			return remediation.Target{}, fmt.Errorf("get replicaset %s/%s: %w", ns, owner.Name, err)
		}
		if d := metav1.GetControllerOf(&rs); d != nil && d.Kind == "Deployment" {
			return verifyWorkload(ctx, c, remediation.Target{Namespace: ns, Kind: "Deployment", Name: d.Name})
		}
	}
	return remediation.Target{}, fmt.Errorf("%w: pod %s/%s is controlled by a %s", ErrNoTarget, ns, name, owner.Kind)
}

func verifyWorkload(ctx context.Context, c client.Reader, t remediation.Target) (remediation.Target, error) {
	var obj client.Object
	switch t.Kind {
	case "Deployment":
		obj = &appsv1.Deployment{}
	case "StatefulSet":
		obj = &appsv1.StatefulSet{}
	default:
		obj = &appsv1.DaemonSet{}
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: t.Namespace, Name: t.Name}, obj); err != nil {
		return remediation.Target{}, fmt.Errorf("get %s %s: %w", t.Kind, t, err)
	}
	return t, nil
}
