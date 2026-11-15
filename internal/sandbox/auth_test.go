package sandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"k8s-agent-sandbox/internal/identity"
)

func pod(name, uid string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "agent-sandboxes", UID: types.UID(uid), Labels: labels,
	}}
}

// podAuth returns a PodAuth whose token always identifies the given pod in
// namespace ns, and whose RBAC allows only allowedVerb on triageruns.
// The token's ServiceAccount is triage-abc, the one run abc was assigned.
func podAuth(ns, podName, podUID, allowedVerb string, pods ...runtime.Object) PodAuth {
	return podAuthAs(ns, "triage-abc", podName, podUID, allowedVerb, pods...)
}

func podAuthAs(ns, sa, podName, podUID, allowedVerb string, pods ...runtime.Object) PodAuth {
	client := fake.NewClientset(pods...)
	client.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		tr.Status = authnv1.TokenReviewStatus{
			Authenticated: true,
			Audiences:     []string{identity.Audience},
			User: authnv1.UserInfo{
				Username: "system:serviceaccount:" + ns + ":" + sa,
				Extra: map[string]authnv1.ExtraValue{
					"authentication.kubernetes.io/pod-name": {podName},
					"authentication.kubernetes.io/pod-uid":  {podUID},
				},
			},
		}
		return true, tr, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authzv1.SubjectAccessReview).DeepCopy()
		ra := sar.Spec.ResourceAttributes
		// RBAC as the coordinator grants it: only the run's own name.
		sar.Status.Allowed = ra.Group == identity.Group && ra.Resource == "triageruns" &&
			ra.Namespace == "agent-sandboxes" && ra.Name == "abc" && ra.Verb == allowedVerb
		return true, sar, nil
	})
	runs := map[string]string{"abc": "triage-abc", "other": "triage-other"} // run -> its ServiceAccount
	owner := func(_ context.Context, name string) (string, error) {
		if sa, ok := runs[name]; ok {
			return sa, nil
		}
		return "", ErrNoStep
	}
	return PodAuth{Verifier: identity.Verifier{Client: client, Audience: identity.Audience}, Pods: client,
		Namespace: "agent-sandboxes", Step: StepTriage, Resource: "triageruns", Owner: owner}
}

func bearer() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/assignment", nil)
	r.Header.Set("Authorization", "Bearer projected-token")
	return r
}

func TestPodAuthResolvesRunFromPodLabel(t *testing.T) {
	t.Parallel()
	a := podAuth("agent-sandboxes", "triage-abc", "uid-1", "get", pod("triage-abc", "uid-1", map[string]string{RunLabel: "abc"}))
	id, err := a.RunID(t.Context(), bearer(), "get")
	if err != nil || id != "abc" {
		t.Fatalf("RunID = %q, %v", id, err)
	}
}

func TestPodAuthRejections(t *testing.T) {
	t.Parallel()
	runPod := pod("triage-abc", "uid-1", map[string]string{RunLabel: "abc"})
	tests := []struct {
		name string
		auth PodAuth
		verb string
		want error
	}{
		{"rbac does not grant the verb", podAuth("agent-sandboxes", "triage-abc", "uid-1", "get", runPod), "update", identity.ErrForbidden},
		{"token from an earlier pod with the same name", podAuth("agent-sandboxes", "triage-abc", "old-uid", "get", runPod), "get", identity.ErrUnauthenticated},
		{"pod is gone", podAuth("agent-sandboxes", "triage-gone", "uid-9", "get", runPod), "get", identity.ErrUnauthenticated},
		{"pod is not a triage run", podAuth("agent-sandboxes", "debug", "uid-2", "get", pod("debug", "uid-2", nil)), "get", identity.ErrForbidden},
		{"service account from another namespace", podAuth("default", "triage-abc", "uid-1", "get", runPod), "get", identity.ErrForbidden},
		{"service account of another run", podAuthAs("agent-sandboxes", "triage-other", "triage-abc", "uid-1", "get", runPod), "get", identity.ErrForbidden},
		{"pod labelled with a run that does not exist", podAuth("agent-sandboxes", "triage-x", "uid-3", "get", pod("triage-x", "uid-3", map[string]string{RunLabel: "nope"})), "get", identity.ErrForbidden},
		{"remediation pod calling the triage API", podAuth("agent-sandboxes", "remediate-abc", "uid-5", "get",
			pod("remediate-abc", "uid-5", map[string]string{RunLabel: "abc", StepLabel: StepRemediate})), "get", identity.ErrForbidden},
		{"rbac pinned to another run", podAuthAs("agent-sandboxes", "triage-other", "triage-o", "uid-4", "get", pod("triage-o", "uid-4", map[string]string{RunLabel: "other"})), "get", identity.ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tc.auth.RunID(t.Context(), bearer(), tc.verb); !errors.Is(err, tc.want) {
				t.Fatalf("RunID error = %v, want %v", err, tc.want)
			}
		})
	}
}
