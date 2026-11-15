// Command triage-controller receives SigNoz alert webhooks as Incidents, runs
// the sandboxed TriageRuns the coordinator starts for them, and publishes the
// reports. With --remediation-enabled it also runs Remediations: the
// remediation agent's sandbox, its decision, and the executor.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/cli"
	"k8s-agent-sandbox/internal/identity"
	"k8s-agent-sandbox/internal/remediation/actions"
	"k8s-agent-sandbox/internal/remediation/executor"
	"k8s-agent-sandbox/internal/sandbox"
	"k8s-agent-sandbox/internal/triage"
)

func main() { cli.Main(newRootCmd()) }

type options struct {
	dedup         time.Duration
	incidentNS    string
	sandboxNS     string
	audience      string
	listenAddr    string
	remediationOn bool
	mode          string
	exec          executor.Config
	sandbox       sandbox.Config
}

func newRootCmd() *cobra.Command {
	var o options
	cmd := cli.Command("triage-controller", "Receive alert webhooks, run triage and remediation steps in sandboxes",
		func(cmd *cobra.Command, log *slog.Logger) error { return run(cmd.Context(), log, o) })
	f := cmd.Flags()
	f.DurationVar(&o.dedup, "dedup-window", 30*time.Minute, "ignore the same alert again within this window after its incident finished")
	f.StringVar(&o.incidentNS, "incident-namespace", "alert-triage", "namespace of Incidents")
	f.StringVar(&o.sandboxNS, "sandbox-namespace", "agent-sandboxes", "namespace of steps and sandboxes")
	f.StringVar(&o.audience, "token-audience", identity.Audience, "audience of agent identity tokens")
	f.StringVar(&o.listenAddr, "listen-addr", ":8080", "HTTP listen address (webhook and agent API)")
	f.StringVar(&o.sandbox.Image, "agent-image", "triage-agent:dev", "agent image for sandboxes")
	f.StringVar(&o.sandbox.RuntimeClass, "runtime-class", "gvisor", "RuntimeClass of sandbox pods")
	f.StringVar(&o.sandbox.ControllerURL, "controller-url", "http://triage-controller.alert-triage.svc.cluster.local:8080", "how agents reach this controller")
	f.StringSliceVar(&o.sandbox.MCPEndpoints, "mcp-endpoints", nil, "MCP endpoints handed to agents, comma-separated")
	f.StringVar(&o.sandbox.LLMBaseURL, "llm-base-url", "http://llm-proxy.llm-proxy.svc.cluster.local:8080", "llm-proxy URL handed to agents")
	f.StringVar(&o.sandbox.AgentTools, "agent-tools", "", "comma-separated MCP tool allowlist handed to agents (small models)")
	f.BoolVar(&o.remediationOn, "remediation-enabled", false, "run Remediations: the remediation agent and the executor")
	f.StringVar(&o.mode, "remediation-mode", string(executor.ModeLive), "executor kill switch: live, dry-run, or off")
	f.DurationVar(&o.exec.ApprovalTimeout, "approval-timeout", time.Hour, "reject a proposal nobody approved within this")
	f.DurationVar(&o.exec.Cooldown, "remediation-cooldown", 30*time.Minute, "at most one action per target within this")
	return cmd
}

// run starts the controller. The Slack webhook URL is a credential, so it is
// read from SLACK_WEBHOOK_URL only, never a flag.
func run(ctx context.Context, log *slog.Logger, o options) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))

	dedup, incidentNS, sandboxNS, audience, remediationOn := o.dedup, o.incidentNS, o.sandboxNS, o.audience, o.remediationOn
	execCfg := o.exec
	execCfg.Namespace, execCfg.Mode = sandboxNS, executor.Mode(o.mode)
	switch execCfg.Mode {
	case executor.ModeLive, executor.ModeDryRun, executor.ModeOff:
	default:
		return fmt.Errorf("--remediation-mode must be live, dry-run, or off, not %q", execCfg.Mode)
	}

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kubernetes config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	kube, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register triage types: %w", err)
	}
	// TriageRuns (and Remediations, when enabled) are watched. Incidents are
	// created and listed uncached (dedup must see an incident created a moment ago).
	watched := map[client.Object]cache.ByObject{
		&v1alpha1.TriageRun{}: {Namespaces: map[string]cache.Config{sandboxNS: {}}},
	}
	if remediationOn {
		watched[&v1alpha1.Remediation{}] = cache.ByObject{Namespaces: map[string]cache.Config{sandboxNS: {}}}
	}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache:   cache.Options{ByObject: watched},
		Client:  client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&v1alpha1.Incident{}}}},
	})
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}

	launcher := &sandbox.Launcher{
		Client: dyn,
		Now:    time.Now,
		Config: func() sandbox.Config {
			c := o.sandbox
			c.Namespace, c.TokenAudience = sandboxNS, audience
			return c
		}(),
	}
	notifiers := triage.MultiNotifier{triage.LogNotifier{Log: log}}
	if url := os.Getenv("SLACK_WEBHOOK_URL"); url != "" {
		notifiers = append(notifiers, triage.SlackNotifier{WebhookURL: url, Client: &http.Client{Timeout: 15 * time.Second}})
		log.Info("slack notifications enabled")
	}
	runs := &triage.RunReconciler{Client: mgr.GetClient(), Launcher: launcher, Notifier: notifiers, Log: log, Now: time.Now}
	if err := runs.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up run reconciler: %w", err)
	}

	verifier := identity.Verifier{Client: kube, Audience: audience}
	auth := sandbox.PodAuth{
		Verifier: verifier, Pods: kube, Namespace: sandboxNS,
		Step: sandbox.StepTriage, Resource: "triageruns", Owner: triage.RunOwner(mgr.GetClient(), sandboxNS),
	}
	srv := triage.NewServer(triage.Config{
		IncidentNamespace: incidentNS,
		SandboxNamespace:  sandboxNS,
		DedupWindow:       dedup,
	}, mgr.GetClient(), mgr.GetAPIReader(), auth, log).WithSkills(triage.CRDSkills{Client: dyn, Log: log})

	if remediationOn {
		var notify executor.Notifier = func(_ context.Context, text string) error {
			log.Info("remediation notice", "text", text)
			return nil
		}
		if url := os.Getenv("SLACK_WEBHOOK_URL"); url != "" {
			slack := triage.SlackNotifier{WebhookURL: url, Client: &http.Client{Timeout: 15 * time.Second}}
			notify = slack.Post
		}
		rec := &executor.Reconciler{
			Client: mgr.GetClient(), Launcher: launcher, Access: verifier,
			// The only workload write client in the project. Its Role exists only
			// in namespaces open to remediation (deploy/remediation).
			Driver: actions.Kube{Client: kube, Now: time.Now},
			Notify: notify, Config: execCfg, Log: log, Now: time.Now,
		}
		if err := rec.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("set up remediation reconciler: %w", err)
		}
		remSrv := &executor.Server{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Access: verifier, NS: sandboxNS, Log: log, Now: time.Now,
			Auth: sandbox.PodAuth{
				Verifier: verifier, Pods: kube, Namespace: sandboxNS,
				Step: sandbox.StepRemediate, Resource: "remediations", Owner: executor.Owner(mgr.GetClient(), sandboxNS),
			},
		}
		srv.WithRemediation(remSrv.Handler())
		log.Info("remediation enabled", "mode", execCfg.Mode, "approval_timeout", execCfg.ApprovalTimeout, "cooldown", execCfg.Cooldown)
	}

	httpSrv := &http.Server{
		Addr:              o.listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return mgr.Start(ctx) })
	g.Go(func() error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return errors.New("triage run cache did not sync")
		}
		log.Info("listening", "addr", httpSrv.Addr, "incident_namespace", incidentNS, "sandbox_namespace", sandboxNS)
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	})
	return g.Wait()
}
