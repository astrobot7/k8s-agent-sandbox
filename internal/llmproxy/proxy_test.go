package llmproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"k8s-agent-sandbox/internal/identity"
)

type upstream struct {
	mu      sync.Mutex
	headers http.Header
	body    string
	path    string
	calls   int
}

func (u *upstream) seen() (http.Header, string, string, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.headers, u.body, u.path, u.calls
}

func newProxy(t *testing.T) (*Proxy, *upstream) {
	t.Helper()
	up := &upstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		up.mu.Lock()
		up.headers, up.body, up.path = r.Header.Clone(), string(b), r.URL.Path
		up.calls++
		up.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Request-Id", "req_123")
		_, _ = io.WriteString(w, `{"id":"msg_1","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	p := New(Policy{MaxTokens: 8192, MaxBodyBytes: 1 << 20}, fakeRBAC{},
		u, "sk-real-key", http.DefaultTransport, 4, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return p, up
}

// fakeRBAC stands in for RBACAuthorizer: bearer "agent" may use claude-sonnet-5-5 only.
type fakeRBAC struct{}

func (fakeRBAC) Authorize(_ context.Context, r *http.Request, model string) (string, error) {
	tok, ok := identity.BearerToken(r)
	if !ok || tok != "agent" {
		return "", identity.ErrUnauthenticated
	}
	if model != "claude-sonnet-5-5" {
		return "agent-sandboxes/triage-abc", fmt.Errorf("%w: may not use models/%s", identity.ErrForbidden, model)
	}
	return "agent-sandboxes/triage-abc", nil
}

// post sends a request as the agent unless headers override Authorization.
func post(t *testing.T, h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer agent")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const okBody = `{"model":"claude-sonnet-5-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],
 "tools":[{"name":"pods_get","input_schema":{"type":"object"}}]}`

func TestForwardsAllowedRequestWithServerKey(t *testing.T) {
	t.Parallel()
	p, up := newProxy(t)
	rec := post(t, p.Handler(), "/v1/messages", okBody, map[string]string{
		"x-api-key":      "sandbox-placeholder",
		"anthropic-beta": "some-feature",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Request-Id") != "req_123" || !strings.Contains(rec.Body.String(), "msg_1") {
		t.Errorf("response not relayed: headers %v body %s", rec.Header(), rec.Body)
	}
	headers, body, path, _ := up.seen()
	if path != "/v1/messages" || body != okBody {
		t.Errorf("upstream got path %q body %q", path, body)
	}
	if got := headers.Get("x-api-key"); got != "sk-real-key" {
		t.Errorf("upstream x-api-key = %q, want the proxy's key", got)
	}
	// The agent's identity token must never reach Anthropic.
	for _, h := range []string{"Authorization", "anthropic-beta"} {
		if v := headers.Get(h); v != "" {
			t.Errorf("client header %s leaked upstream: %q", h, v)
		}
	}
	if headers.Get("anthropic-version") != DefaultAnthropicVersion {
		t.Errorf("anthropic-version = %q", headers.Get("anthropic-version"))
	}
}

func TestPolicyRejections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path, body, want string
	}{
		{"max_tokens missing", "/v1/messages", `{"model":"claude-sonnet-5-5"}`, "max_tokens"},
		{"max_tokens over cap", "/v1/messages", `{"model":"claude-sonnet-5-5","max_tokens":100000}`, "max_tokens"},
		{"server web fetch tool", "/v1/messages",
			`{"model":"claude-sonnet-5-5","max_tokens":10,"tools":[{"type":"web_fetch_20250910","name":"web_fetch"}]}`, "server tool"},
		{"mcp connector", "/v1/messages",
			`{"model":"claude-sonnet-5-5","max_tokens":10,"mcp_servers":[{"type":"url","url":"https://evil.example"}]}`, "mcp_servers"},
		{"code execution container", "/v1/messages",
			`{"model":"claude-sonnet-5-5","max_tokens":10,"container":"c_1"}`, "container"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, up := newProxy(t)
			rec := post(t, p.Handler(), tc.path, tc.body, nil)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("status %d body %s; want 400 mentioning %q", rec.Code, rec.Body, tc.want)
			}
			if _, _, _, calls := up.seen(); calls != 0 {
				t.Error("rejected request reached upstream")
			}
		})
	}
}

func TestRBACDecidesCallerAndModel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, auth, body string
		want             int
	}{
		{"no token", "", okBody, http.StatusUnauthorized},
		{"unknown token", "Bearer someone-else", okBody, http.StatusUnauthorized},
		{"model RBAC does not grant", "Bearer agent", `{"model":"claude-opus-5-5","max_tokens":10}`, http.StatusForbidden},
		{"policy details hidden from unauthenticated callers", "", `{`, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, up := newProxy(t)
			rec := post(t, p.Handler(), "/v1/messages", tc.body, map[string]string{"Authorization": tc.auth})
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if _, _, _, calls := up.seen(); calls != 0 {
				t.Error("denied request reached upstream")
			}
		})
	}
}

func TestCountTokensSkipsMaxTokens(t *testing.T) {
	t.Parallel()
	p, _ := newProxy(t)
	rec := post(t, p.Handler(), "/v1/messages/count_tokens", `{"model":"claude-sonnet-5-5","messages":[]}`, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("count_tokens: status %d %s", rec.Code, rec.Body)
	}
}

func TestOnlyMessagesEndpoints(t *testing.T) {
	t.Parallel()
	p, up := newProxy(t)
	for _, path := range []string{"/v1/files", "/v1/models", "/v1/messages/batches"} {
		if rec := post(t, p.Handler(), path, okBody, nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 404/405", path, rec.Code)
		}
	}
	if _, _, _, calls := up.seen(); calls != 0 {
		t.Error("blocked path reached upstream")
	}
}

func TestBodyLimit(t *testing.T) {
	t.Parallel()
	p, up := newProxy(t)
	big := `{"model":"claude-sonnet-5-5","max_tokens":10,"pad":"` + strings.Repeat("x", 2<<20) + `"}`
	if rec := post(t, p.Handler(), "/v1/messages", big, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413", rec.Code)
	}
	if _, _, _, calls := up.seen(); calls != 0 {
		t.Error("oversized request reached upstream")
	}
}
