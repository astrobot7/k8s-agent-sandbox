// Package llmproxy is a policy-enforcing reverse proxy for the Anthropic
// Messages API. Sandboxed agents call it with their ServiceAccount token, not
// an API key; it authorizes the caller with RBAC, checks the request against a
// policy, adds the API key, and forwards it upstream.
package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	authzv1 "k8s.io/api/authorization/v1"

	"k8s-agent-sandbox/internal/identity"
)

// DefaultAnthropicVersion is sent when the client does not set anthropic-version.
const DefaultAnthropicVersion = "2023-06-01"

// Policy is what a request must satisfy to be forwarded. Which models a caller
// may use is not here: RBAC decides that (see RBACAuthorizer).
type Policy struct {
	MaxTokens    int64 // upper bound for max_tokens
	MaxBodyBytes int64
}

// Authorizer decides whether the request's caller may use model, and names the
// caller for logs. Errors wrapping identity.ErrUnauthenticated or
// identity.ErrForbidden become 401 and 403.
type Authorizer interface {
	Authorize(ctx context.Context, r *http.Request, model string) (caller string, err error)
}

// RBACAuthorizer authenticates the caller's ServiceAccount token and checks
// RBAC: verb "use" on resource "models" (resourceNames = model IDs) in the
// triage API group, in the caller's namespace.
type RBACAuthorizer struct{ Verifier identity.Verifier }

// Authorize implements Authorizer.
func (a RBACAuthorizer) Authorize(ctx context.Context, r *http.Request, model string) (string, error) {
	c, err := a.Verifier.Check(ctx, r, authzv1.ResourceAttributes{
		Group: identity.Group, Resource: "models", Verb: "use", Name: model,
	})
	if err != nil {
		return c.Username, err
	}
	return c.Namespace + "/" + c.PodName, nil
}

// Proxy forwards allowed requests to the Anthropic API.
type Proxy struct {
	policy    Policy
	authz     Authorizer
	upstream  *url.URL
	apiKey    string
	transport http.RoundTripper
	log       *slog.Logger
	inflight  chan struct{}
	openAI    map[string]string // non-nil: OpenAI-compatible upstream; alias -> upstream model
}

// WithOpenAIUpstream switches the proxy to an OpenAI chat-completions upstream
// (for example a local mlx_lm.server). models maps the model names agents use
// (and RBAC grants) to upstream model IDs; unmapped names pass through.
func (p *Proxy) WithOpenAIUpstream(models map[string]string) *Proxy {
	if models == nil {
		models = map[string]string{}
	}
	p.openAI = models
	return p
}

// New returns a Proxy. maxInflight bounds concurrent upstream requests.
func New(p Policy, authz Authorizer, upstream *url.URL, apiKey string, rt http.RoundTripper, maxInflight int, log *slog.Logger) *Proxy {
	return &Proxy{policy: p, authz: authz, upstream: upstream, apiKey: apiKey, transport: rt, log: log,
		inflight: make(chan struct{}, maxInflight)}
}

// Handler returns the routes. Anything other than the two Messages endpoints is 404.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", p.serve)
	mux.HandleFunc("POST /v1/messages/count_tokens", p.serve)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// errPolicy marks a request the policy rejects.
var errPolicy = errors.New("rejected by llm-proxy policy")

// request holds the fields the policy inspects.
type request struct {
	Model      string            `json:"model"`
	MaxTokens  *int64            `json:"max_tokens"`
	Tools      []json.RawMessage `json:"tools"`
	MCPServers json.RawMessage   `json:"mcp_servers"`
	Container  json.RawMessage   `json:"container"`
}

// Check validates a request body for the given path.
func (p Policy) Check(path string, body []byte) (model string, err error) {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return "", fmt.Errorf("%w: body is not valid JSON: %w", errPolicy, err)
	}
	if req.Model == "" {
		return "", fmt.Errorf("%w: model is required", errPolicy)
	}
	if path == "/v1/messages" {
		if req.MaxTokens == nil || *req.MaxTokens <= 0 || *req.MaxTokens > p.MaxTokens {
			return req.Model, fmt.Errorf("%w: max_tokens must be between 1 and %d", errPolicy, p.MaxTokens)
		}
	}
	// Server-side tools (web fetch/search, code execution, ...) and the MCP
	// connector let the model reach the network on its own: a data exfiltration
	// path that bypasses our egress controls. Only client-side tools pass.
	for i, raw := range req.Tools {
		var t struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return req.Model, fmt.Errorf("%w: tools[%d] is invalid", errPolicy, i)
		}
		if t.Type != "" && t.Type != "custom" {
			return req.Model, fmt.Errorf("%w: server tool %q is not allowed", errPolicy, t.Type)
		}
	}
	if len(req.MCPServers) > 0 && string(req.MCPServers) != "null" {
		return req.Model, fmt.Errorf("%w: mcp_servers is not allowed", errPolicy)
	}
	if len(req.Container) > 0 && string(req.Container) != "null" {
		return req.Model, fmt.Errorf("%w: container is not allowed", errPolicy)
	}
	return req.Model, nil
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.policy.MaxBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "llm-proxy: request body too large or unreadable")
		return
	}
	// Authenticate and authorize before revealing anything about the policy.
	var peek struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &peek)
	caller, err := p.authz.Authorize(r.Context(), r, peek.Model)
	if err != nil {
		code := identity.HTTPStatus(err)
		p.log.Warn("request denied", "path", r.URL.Path, "model", peek.Model, "caller", caller, "status", code, "reason", err)
		typ := map[int]string{401: "authentication_error", 403: "permission_error"}[code]
		if typ == "" {
			typ = "api_error"
		}
		writeError(w, code, typ, "llm-proxy: "+err.Error())
		return
	}

	model, err := p.policy.Check(r.URL.Path, body)
	if err != nil {
		p.log.Warn("request rejected", "path", r.URL.Path, "model", model, "caller", caller, "reason", err)
		writeError(w, http.StatusBadRequest, "invalid_request_error", "llm-proxy: "+err.Error())
		return
	}

	select {
	case p.inflight <- struct{}{}:
		defer func() { <-p.inflight }()
	case <-r.Context().Done():
		return
	}

	forward := p.forward
	if p.openAI != nil {
		forward = p.forwardOpenAI
	}
	status, err := forward(r.Context(), w, r, body)
	if err != nil {
		p.log.Error("upstream request failed", "path", r.URL.Path, "model", model, "err", err)
		if status == 0 {
			writeError(w, http.StatusBadGateway, "api_error", "llm-proxy: upstream unavailable")
		}
		return
	}
	p.log.Info("request forwarded", "path", r.URL.Path, "model", model, "caller", caller, "status", status,
		"duration_ms", time.Since(start).Milliseconds())
}

// forward sends the request upstream and streams the response back. It returns
// the upstream status, or 0 if nothing was written to the client yet.
func (p *Proxy) forward(ctx context.Context, w http.ResponseWriter, r *http.Request, body []byte) (int, error) {
	u := *p.upstream
	u.Path = r.URL.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build upstream request: %w", err)
	}
	// Only these headers go upstream. Client credentials and anthropic-beta
	// (which can switch on extra features) are dropped.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	version := r.Header.Get("anthropic-version")
	if version == "" {
		version = DefaultAnthropicVersion
	}
	req.Header.Set("anthropic-version", version)
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Request-Id", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := copyFlush(w, resp.Body); err != nil {
		return resp.StatusCode, fmt.Errorf("stream response: %w", err)
	}
	return resp.StatusCode, nil
}

// forwardOpenAI translates the request to OpenAI chat completions, calls the
// upstream, and translates the reply back. Non-streaming only.
func (p *Proxy) forwardOpenAI(ctx context.Context, w http.ResponseWriter, r *http.Request, body []byte) (int, error) {
	var meta struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &meta)
	if r.URL.Path == "/v1/messages/count_tokens" {
		// No equivalent upstream; a rough estimate keeps clients working.
		writeJSONBody(w, http.StatusOK, map[string]int{"input_tokens": len(body) / 4})
		return http.StatusOK, nil
	}
	if meta.Stream {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "llm-proxy: streaming is not supported with an OpenAI-compatible upstream")
		return http.StatusBadRequest, nil
	}
	upstreamModel := meta.Model
	if m, ok := p.openAI[meta.Model]; ok {
		upstreamModel = m
	}
	out, tools, err := toOpenAI(body, upstreamModel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "llm-proxy: "+err.Error())
		return http.StatusBadRequest, nil
	}

	u := *p.upstream
	u.Path = "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(out))
	if err != nil {
		return 0, fmt.Errorf("build upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("read upstream response: %w", err)
	}
	if resp.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "api_error", fmt.Sprintf("llm-proxy: upstream returned %s", resp.Status))
		return resp.StatusCode, nil
	}
	translated, err := fromOpenAI(respBody, meta.Model, tools)
	if err != nil {
		writeError(w, http.StatusBadGateway, "api_error", "llm-proxy: "+err.Error())
		return http.StatusBadGateway, nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(translated)
	return http.StatusOK, err
}

func writeJSONBody(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// copyFlush copies and flushes as it goes, so streamed (SSE) responses are not buffered.
func copyFlush(w http.ResponseWriter, r io.Reader) (int64, error) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	var n int64
	for {
		k, err := r.Read(buf)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil {
				return n, werr
			}
			n += int64(k)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

func writeError(w http.ResponseWriter, code int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}
