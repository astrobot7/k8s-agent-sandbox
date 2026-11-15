package identity

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const agentUser = "system:serviceaccount:agent-sandboxes:triage-agent"

func podBoundUser() authnv1.UserInfo {
	return authnv1.UserInfo{
		Username: agentUser,
		UID:      "sa-uid",
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:agent-sandboxes"},
		Extra: map[string]authnv1.ExtraValue{
			extraPodName: {"triage-abc"},
			extraPodUID:  {"pod-uid-1"},
		},
	}
}

// newVerifier returns a Verifier whose TokenReviews return status and whose
// SubjectAccessReviews allow only what allow says. It records the last SAR.
func newVerifier(status authnv1.TokenReviewStatus, allow func(authzv1.ResourceAttributes) bool) (Verifier, *authzv1.SubjectAccessReviewSpec) {
	client := fake.NewClientset()
	var lastSAR authzv1.SubjectAccessReviewSpec
	client.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview)
		out := tr.DeepCopy()
		out.Status = status
		return true, out, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authzv1.SubjectAccessReview)
		lastSAR = sar.Spec
		out := sar.DeepCopy()
		out.Status.Allowed = allow(*sar.Spec.ResourceAttributes)
		return true, out, nil
	})
	return Verifier{Client: client, Audience: Audience}, &lastSAR
}

func request(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestCheckAllowsPodBoundServiceAccount(t *testing.T) {
	t.Parallel()
	v, lastSAR := newVerifier(
		authnv1.TokenReviewStatus{Authenticated: true, Audiences: []string{Audience}, User: podBoundUser()},
		func(a authzv1.ResourceAttributes) bool {
			return a.Resource == "models" && a.Name == "claude-sonnet-5-5"
		},
	)
	c, err := v.Check(t.Context(), request("tok"), authzv1.ResourceAttributes{
		Group: Group, Resource: "models", Verb: "use", Name: "claude-sonnet-5-5",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if c.Namespace != "agent-sandboxes" || c.ServiceAccount != "triage-agent" || c.PodName != "triage-abc" || c.PodUID != "pod-uid-1" {
		t.Errorf("caller = %+v", c)
	}
	if lastSAR.User != agentUser || lastSAR.ResourceAttributes.Namespace != "agent-sandboxes" ||
		lastSAR.ResourceAttributes.Group != Group || len(lastSAR.Extra[extraPodName]) != 1 {
		t.Errorf("SubjectAccessReview spec = %+v", lastSAR)
	}
}

func TestCheckRejections(t *testing.T) {
	t.Parallel()
	allowAll := func(authzv1.ResourceAttributes) bool { return true }
	denyAll := func(authzv1.ResourceAttributes) bool { return false }
	notPodBound := podBoundUser()
	notPodBound.Extra = nil
	user := authnv1.UserInfo{Username: "alice", Extra: podBoundUser().Extra}

	tests := []struct {
		name   string
		token  string
		status authnv1.TokenReviewStatus
		allow  func(authzv1.ResourceAttributes) bool
		want   error
	}{
		{"no token", "", authnv1.TokenReviewStatus{}, allowAll, ErrUnauthenticated},
		{"invalid token", "x", authnv1.TokenReviewStatus{Authenticated: false, Error: "expired"}, allowAll, ErrUnauthenticated},
		{"wrong audience", "x", authnv1.TokenReviewStatus{Authenticated: true, Audiences: []string{"https://kubernetes.default.svc"}, User: podBoundUser()}, allowAll, ErrUnauthenticated},
		{"not a service account", "x", authnv1.TokenReviewStatus{Authenticated: true, Audiences: []string{Audience}, User: user}, allowAll, ErrUnauthenticated},
		{"not bound to a pod", "x", authnv1.TokenReviewStatus{Authenticated: true, Audiences: []string{Audience}, User: notPodBound}, allowAll, ErrUnauthenticated},
		{"rbac denies", "x", authnv1.TokenReviewStatus{Authenticated: true, Audiences: []string{Audience}, User: podBoundUser()}, denyAll, ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, _ := newVerifier(tc.status, tc.allow)
			_, err := v.Check(t.Context(), request(tc.token), authzv1.ResourceAttributes{Group: Group, Resource: "triageruns", Verb: "get"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Check error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestHTTPStatus(t *testing.T) {
	t.Parallel()
	if HTTPStatus(ErrUnauthenticated) != 401 || HTTPStatus(ErrForbidden) != 403 || HTTPStatus(errors.New("x")) != 500 {
		t.Error("unexpected status mapping")
	}
}

func TestTokenFileTransportRereadsToken(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	var (
		mu  sync.Mutex
		got []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r.Header.Get("Authorization"))
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: TokenFileTransport{Path: path}}

	for _, tok := range []string{"first\n", "rotated"} {
		if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "Bearer first" || got[1] != "Bearer rotated" {
		t.Errorf("Authorization headers = %q", got)
	}
}
