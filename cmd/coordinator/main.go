// Command coordinator moves each Incident through its steps and gives every
// step a short-lived ServiceAccount, Role, and RoleBinding of its own. It is
// the only component allowed to create RBAC objects, and it runs apart from
// the webhook-facing triage controller so that permission stays off the
// network edge.
package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"k8s-agent-sandbox/api/v1alpha1"
	"k8s-agent-sandbox/internal/cli"
	"k8s-agent-sandbox/internal/coordinator"
)

func main() { cli.Main(newRootCmd()) }

func newRootCmd() *cobra.Command {
	var cfg coordinator.Config
	cmd := cli.Command("coordinator", "Move Incidents through their steps with per-step identities",
		func(_ *cobra.Command, log *slog.Logger) error { return run(log, cfg) })
	f := cmd.Flags()
	f.StringVar(&cfg.IncidentNamespace, "incident-namespace", "alert-triage", "namespace of Incidents")
	f.StringVar(&cfg.SandboxNamespace, "sandbox-namespace", "agent-sandboxes", "namespace of steps, their identities, and sandboxes")
	f.StringVar(&cfg.Model, "agent-model", "claude-sonnet-5-5", "the only model a step's Role allows")
	f.DurationVar(&cfg.RunTimeout, "run-timeout", 15*time.Minute, "deadline of each TriageRun")
	f.DurationVar(&cfg.Grace, "run-grace", 2*time.Minute, "how long past a deadline before the coordinator gives up on a step")
	f.DurationVar(&cfg.Retention, "incident-retention", 24*time.Hour, "how long finished incidents and their steps are kept")
	f.IntVar(&cfg.MaxConcurrent, "max-concurrent-runs", 2, "active steps with a sandbox; further incidents wait as Pending")
	f.BoolVar(&cfg.Remediation, "remediation-enabled", false, "start a Remediation after a successful triage when a RemediationSkill matches")
	f.DurationVar(&cfg.RemediationTimeout, "remediation-timeout", 10*time.Minute, "the remediation agent's time to decide")
	return cmd
}

func run(log *slog.Logger, cfg coordinator.Config) error {
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register core types: %w", err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register triage types: %w", err)
	}

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kubernetes config: %w", err)
	}
	watched := map[client.Object]cache.ByObject{
		&v1alpha1.Incident{}:  {Namespaces: map[string]cache.Config{cfg.IncidentNamespace: {}}},
		&v1alpha1.TriageRun{}: {Namespaces: map[string]cache.Config{cfg.SandboxNamespace: {}}},
	}
	if cfg.Remediation {
		watched[&v1alpha1.Remediation{}] = cache.ByObject{Namespaces: map[string]cache.Config{cfg.SandboxNamespace: {}}}
		watched[&v1alpha1.RemediationSkill{}] = cache.ByObject{}
	}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: ":8081",
		Cache:                  cache.Options{ByObject: watched},
		// Identities are only created and deleted, never watched: the
		// coordinator has no list or watch permission on them. Pods and
		// workloads are read once per remediation to resolve its target.
		Client: client.Options{Cache: &client.CacheOptions{
			DisableFor: []client.Object{&corev1.ServiceAccount{}, &rbacv1.Role{}, &rbacv1.RoleBinding{},
				&corev1.Pod{}, &appsv1.ReplicaSet{}, &appsv1.Deployment{}, &appsv1.StatefulSet{}, &appsv1.DaemonSet{}},
		}},
	})
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	r := &coordinator.Reconciler{Client: mgr.GetClient(), TargetReader: mgr.GetAPIReader(), Config: cfg, Now: time.Now}
	if err := r.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up coordinator: %w", err)
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	log.Info("coordinator starting", "incidents", cfg.IncidentNamespace, "sandboxes", cfg.SandboxNamespace,
		"model", cfg.Model, "max_concurrent", cfg.MaxConcurrent, "run_timeout", cfg.RunTimeout,
		"remediation", cfg.Remediation, "remediation_timeout", cfg.RemediationTimeout)
	return mgr.Start(ctrl.SetupSignalHandler())
}
