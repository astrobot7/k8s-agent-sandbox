package actions

import (
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"k8s-agent-sandbox/internal/remediation"
)

var now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func TestRolloutRestartAnnotatesTheTemplate(t *testing.T) {
	t.Parallel()
	c := fake.NewClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: "workloads"}})
	k := Kube{Client: c, Now: func() time.Time { return now }}
	p := remediation.Proposal{Action: remediation.ActionRolloutRestart,
		Target: remediation.Target{Namespace: "workloads", Kind: "Deployment", Name: "payments-api"}}
	if _, err := k.Do(t.Context(), p); err != nil {
		t.Fatalf("Do: %v", err)
	}
	d, err := c.AppsV1().Deployments("workloads").Get(t.Context(), "payments-api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Spec.Template.Annotations[restartedAtAnnotation]; got != "2026-11-15T12:00:00Z" {
		t.Errorf("restartedAt = %q", got)
	}
}

func TestRestartPodDeletesIt(t *testing.T) {
	t.Parallel()
	c := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "batch-1", Namespace: "workloads"}})
	k := Kube{Client: c, Now: time.Now}
	p := remediation.Proposal{Action: remediation.ActionRestartPod,
		Target: remediation.Target{Namespace: "workloads", Kind: "Pod", Name: "batch-1"}}
	if _, err := k.Do(t.Context(), p); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if _, err := c.CoreV1().Pods("workloads").Get(t.Context(), "batch-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("pod still exists: %v", err)
	}
}

func TestDriverRefusesMismatchedKinds(t *testing.T) {
	t.Parallel()
	k := Kube{Client: fake.NewClientset(), Now: time.Now}
	for _, p := range []remediation.Proposal{
		{Action: remediation.ActionRestartPod, Target: remediation.Target{Namespace: "w", Kind: "Deployment", Name: "x"}},
		{Action: remediation.ActionScale, Target: remediation.Target{Namespace: "w", Kind: "DaemonSet", Name: "x"}, Params: remediation.Params{Replicas: ptr.To(int32(2))}},
		{Action: remediation.ActionRolloutRestart, Target: remediation.Target{Namespace: "w", Kind: "Pod", Name: "x"}},
	} {
		if _, err := k.Do(t.Context(), p); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Do(%s on %s) = %v, want ErrUnsupported", p.Action, p.Target.Kind, err)
		}
	}
}

func TestOpenPRChangesNothing(t *testing.T) {
	t.Parallel()
	k := Kube{Client: fake.NewClientset(), Now: time.Now}
	msg, err := k.Do(t.Context(), remediation.Proposal{Action: remediation.ActionOpenPR, Rationale: "bump the limit",
		Target: remediation.Target{Namespace: "w", Kind: "Deployment", Name: "x"}})
	if err != nil || !strings.Contains(msg, "not configured") {
		t.Errorf("Do = %q, %v", msg, err)
	}
}
