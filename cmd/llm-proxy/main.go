// Command llm-proxy holds the Anthropic API key and forwards policy-checked
// Messages API requests from sandboxed agents, which authenticate with their
// ServiceAccount token. RBAC decides which models each caller may use.
// Outbound traffic goes through the egress gateway (HTTPS_PROXY).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"k8s-agent-sandbox/internal/cli"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/llmproxy"
)

func main() { cli.Main(newRootCmd()) }

type options struct {
	upstreamAPI   string
	upstreamURL   string
	maxTokens     int
	maxInflight   int
	tokenAudience string
	modelMap      string
	listenAddr    string
}

func newRootCmd() *cobra.Command {
	var o options
	cmd := cli.Command("llm-proxy", "Forward policy-checked Messages API requests from sandboxed agents",
		func(cmd *cobra.Command, log *slog.Logger) error { return run(cmd.Context(), log, o) })
	f := cmd.Flags()
	f.StringVar(&o.upstreamAPI, "upstream-api", "anthropic", `upstream protocol: "anthropic" or "openai" (an OpenAI-compatible server such as mlx_lm.server)`)
	f.StringVar(&o.upstreamURL, "upstream-url", "https://api.anthropic.com", "upstream base URL")
	f.IntVar(&o.maxTokens, "max-tokens", 8192, "cap on max_tokens per request")
	f.IntVar(&o.maxInflight, "max-inflight", 8, "concurrent upstream requests")
	f.StringVar(&o.tokenAudience, "token-audience", identity.Audience, "audience of agent identity tokens")
	f.StringVar(&o.modelMap, "model-map", "", `openai upstream only: "alias=upstream-model,..." so RBAC can grant a simple alias`)
	f.StringVar(&o.listenAddr, "listen-addr", ":8080", "HTTP listen address")
	return cmd
}

// run starts the proxy. The API key is read from ANTHROPIC_API_KEY only, never
// a flag, so it does not show up in process listings.
func run(ctx context.Context, log *slog.Logger, o options) error {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if o.upstreamAPI == "anthropic" && key == "" {
		return errors.New("ANTHROPIC_API_KEY is not set")
	}
	if o.upstreamAPI != "anthropic" && o.upstreamAPI != "openai" {
		return fmt.Errorf("--upstream-api must be anthropic or openai, got %q", o.upstreamAPI)
	}
	upstream, err := url.Parse(o.upstreamURL)
	if err != nil {
		return fmt.Errorf("--upstream-url: %w", err)
	}

	// Callers and their allowed models are checked with TokenReview and
	// SubjectAccessReview, so the proxy needs its own in-cluster credentials.
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster config: %w", err)
	}
	kube, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}
	authz := llmproxy.RBACAuthorizer{Verifier: identity.Verifier{Client: kube, Audience: o.tokenAudience}}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment // HTTPS_PROXY / HTTP_PROXY -> egress gateway
	transport.ResponseHeaderTimeout = 5 * time.Minute

	p := llmproxy.New(llmproxy.Policy{
		MaxTokens:    int64(o.maxTokens),
		MaxBodyBytes: 4 << 20,
	}, authz, upstream, key, transport, o.maxInflight, log)
	if o.upstreamAPI == "openai" {
		models := map[string]string{}
		for _, pair := range strings.Split(o.modelMap, ",") {
			if alias, model, ok := strings.Cut(strings.TrimSpace(pair), "="); ok {
				models[alias] = model
			}
		}
		p.WithOpenAIUpstream(models)
		log.Info("using OpenAI-compatible upstream", "models", models)
	}

	srv := &http.Server{
		Addr:              o.listenAddr,
		Handler:           p.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout: model responses can stream for minutes.
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr, "upstream", upstream.String(), "max_tokens", o.maxTokens)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
