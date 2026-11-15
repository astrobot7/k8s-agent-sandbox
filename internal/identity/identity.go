// Package identity authenticates triage agents by their Kubernetes
// ServiceAccount and authorizes them with RBAC.
//
// Each sandbox pod gets a projected ServiceAccount token whose audience is
// Audience, not the Kubernetes API. Services the agent calls verify that token
// with a TokenReview (who is calling, from which pod) and check permissions
// with a SubjectAccessReview against RBAC rules on resources in Group, for
// example "triageruns" (verbs get, update) or "models" (verb use).
package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Audience is the audience of agent tokens. The Kubernetes API server rejects
// tokens with this audience, so they cannot be used against the cluster.
const Audience = "triage.k8s-agent-sandbox.io"

// Group is the API group RBAC rules use for triage permissions.
const Group = "triage.k8s-agent-sandbox.io"

// TokenReview extra keys for pod-bound ServiceAccount tokens.
const (
	extraPodName = "authentication.kubernetes.io/pod-name"
	extraPodUID  = "authentication.kubernetes.io/pod-uid"
)

// Errors map to HTTP 401 and 403.
var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
)

// Caller is an authenticated ServiceAccount, bound to one pod.
type Caller struct {
	Username       string
	UID            string
	Groups         []string
	Extra          map[string][]string
	Namespace      string
	ServiceAccount string
	PodName        string
	PodUID         string
}

// Verifier checks tokens and permissions against the Kubernetes API.
type Verifier struct {
	Client   kubernetes.Interface
	Audience string
}

// BearerToken extracts the token from an Authorization header.
func BearerToken(r *http.Request) (string, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok = strings.TrimSpace(tok)
	return tok, ok && tok != ""
}

// Authenticate validates a token with a TokenReview. Only pod-bound
// ServiceAccount tokens for the Verifier's audience are accepted.
func (v Verifier) Authenticate(ctx context.Context, token string) (Caller, error) {
	tr, err := v.Client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: token, Audiences: []string{v.Audience}},
	}, metav1.CreateOptions{})
	if err != nil {
		return Caller{}, fmt.Errorf("token review: %w", err)
	}
	st := tr.Status
	if !st.Authenticated {
		return Caller{}, fmt.Errorf("%w: %s", ErrUnauthenticated, st.Error)
	}
	if !slices.Contains(st.Audiences, v.Audience) {
		return Caller{}, fmt.Errorf("%w: token audience is not %q", ErrUnauthenticated, v.Audience)
	}
	parts := strings.Split(st.User.Username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" {
		return Caller{}, fmt.Errorf("%w: %q is not a ServiceAccount", ErrUnauthenticated, st.User.Username)
	}
	c := Caller{
		Username:       st.User.Username,
		UID:            st.User.UID,
		Groups:         st.User.Groups,
		Extra:          map[string][]string{},
		Namespace:      parts[2],
		ServiceAccount: parts[3],
	}
	for k, v := range st.User.Extra {
		c.Extra[k] = []string(v)
	}
	if v := c.Extra[extraPodName]; len(v) == 1 {
		c.PodName = v[0]
	}
	if v := c.Extra[extraPodUID]; len(v) == 1 {
		c.PodUID = v[0]
	}
	if c.PodName == "" || c.PodUID == "" {
		return Caller{}, fmt.Errorf("%w: token is not bound to a pod", ErrUnauthenticated)
	}
	return c, nil
}

// Authorize asks RBAC whether the caller may perform attrs, with a
// SubjectAccessReview. An empty attrs.Namespace means the caller's namespace.
func (v Verifier) Authorize(ctx context.Context, c Caller, attrs authzv1.ResourceAttributes) error {
	if attrs.Namespace == "" {
		attrs.Namespace = c.Namespace
	}
	extra := make(map[string]authzv1.ExtraValue, len(c.Extra))
	for k, val := range c.Extra {
		extra[k] = authzv1.ExtraValue(val)
	}
	sar, err := v.Client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{
		Spec: authzv1.SubjectAccessReviewSpec{
			User: c.Username, UID: c.UID, Groups: c.Groups, Extra: extra,
			ResourceAttributes: &attrs,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("subject access review: %w", err)
	}
	if !sar.Status.Allowed {
		target := attrs.Resource
		if attrs.Name != "" {
			target += "/" + attrs.Name
		}
		return fmt.Errorf("%w: %s may not %s %s in %s", ErrForbidden, c.Username, attrs.Verb, target, attrs.Namespace)
	}
	return nil
}

// Check authenticates the request's bearer token and authorizes attrs.
func (v Verifier) Check(ctx context.Context, r *http.Request, attrs authzv1.ResourceAttributes) (Caller, error) {
	tok, ok := BearerToken(r)
	if !ok {
		return Caller{}, fmt.Errorf("%w: missing bearer token", ErrUnauthenticated)
	}
	c, err := v.Authenticate(ctx, tok)
	if err != nil {
		return Caller{}, err
	}
	return c, v.Authorize(ctx, c, attrs)
}

// HTTPStatus maps an identity error to an HTTP status code.
func HTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// TokenFileTransport adds the projected token at Path as a bearer token to
// every request. It rereads the file each time because the kubelet rotates it.
type TokenFileTransport struct {
	Path string
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t TokenFileTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := os.ReadFile(t.Path)
	if err != nil {
		return nil, fmt.Errorf("read identity token: %w", err)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
