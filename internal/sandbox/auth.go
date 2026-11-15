package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"k8s-agent-sandbox/internal/identity"
)

// ErrNoStep is returned by an OwnerLookup when the step resource does not exist.
var ErrNoStep = errors.New("no such step")

// OwnerLookup returns the ServiceAccount the coordinator assigned to the step
// resource named name, or ErrNoStep.
type OwnerLookup func(ctx context.Context, name string) (serviceAccount string, err error)

// PodAuth authenticates agents of one step kind by their pod-bound
// ServiceAccount token. It maps the calling pod to its step resource through
// RunLabel, requires the pod's StepLabel to be Step, requires the token's
// ServiceAccount to be the one the coordinator created for that resource, and
// asks RBAC whether it may perform verb on that resource by name.
type PodAuth struct {
	Verifier  identity.Verifier
	Pods      kubernetes.Interface
	Namespace string
	Step      string      // StepTriage or StepRemediate
	Resource  string      // "triageruns" or "remediations"
	Owner     OwnerLookup // finds the step resource's ServiceAccount
}

// RunID returns the name of the step resource the caller belongs to.
func (a PodAuth) RunID(ctx context.Context, r *http.Request, verb string) (string, error) {
	tok, ok := identity.BearerToken(r)
	if !ok {
		return "", fmt.Errorf("%w: missing bearer token", identity.ErrUnauthenticated)
	}
	c, err := a.Verifier.Authenticate(ctx, tok)
	if err != nil {
		return "", err
	}
	if c.Namespace != a.Namespace {
		return "", fmt.Errorf("%w: agents must run in %s, not %s", identity.ErrForbidden, a.Namespace, c.Namespace)
	}
	pod, err := a.Pods.CoreV1().Pods(a.Namespace).Get(ctx, c.PodName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Errorf("%w: pod %s no longer exists", identity.ErrUnauthenticated, c.PodName)
	}
	if err != nil {
		return "", fmt.Errorf("get pod %s: %w", c.PodName, err)
	}
	// A token outlives its pod name: a new pod with the same name has a new UID.
	if string(pod.UID) != c.PodUID {
		return "", fmt.Errorf("%w: token belongs to an earlier pod named %s", identity.ErrUnauthenticated, c.PodName)
	}
	id := pod.Labels[RunLabel]
	if id == "" {
		return "", fmt.Errorf("%w: pod %s is not an agent sandbox", identity.ErrForbidden, c.PodName)
	}
	step := pod.Labels[StepLabel]
	if step == "" {
		step = StepTriage
	}
	if step != a.Step {
		return "", fmt.Errorf("%w: pod %s serves a %s step, not %s", identity.ErrForbidden, c.PodName, step, a.Step)
	}
	sa, err := a.Owner(ctx, id)
	if errors.Is(err, ErrNoStep) {
		return "", fmt.Errorf("%w: no %s %s", identity.ErrForbidden, a.Resource, id)
	}
	if err != nil {
		return "", fmt.Errorf("get %s %s: %w", a.Resource, id, err)
	}
	if c.ServiceAccount != sa {
		return "", fmt.Errorf("%w: %s %s belongs to service account %s, not %s",
			identity.ErrForbidden, a.Resource, id, sa, c.ServiceAccount)
	}
	err = a.Verifier.Authorize(ctx, c, authzv1.ResourceAttributes{
		Namespace: a.Namespace, Group: identity.Group, Resource: a.Resource, Name: id, Verb: verb,
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
