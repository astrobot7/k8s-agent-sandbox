// Package sandbox launches agent sandboxes (agent-sandbox Sandbox objects
// under gVisor) for triage and remediation steps, and authenticates the
// agents inside them.
package sandbox

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"k8s-agent-sandbox/api/v1alpha1"
)

// GVR is the agent-sandbox Sandbox resource.
var GVR = schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes"}

// Where the agent finds its identity token inside the sandbox.
const (
	tokenDir  = "/var/run/secrets/triage"
	tokenFile = tokenDir + "/token"
)

// Labels on each sandbox pod. PodAuth maps a caller to its step with them.
const (
	// RunLabel is the name of the step resource (TriageRun or Remediation).
	RunLabel = "triage.agent-sandbox/run"
	// StepLabel is "triage" or "remediate". Pods without it are triage pods.
	StepLabel = "triage.agent-sandbox/step"
)

// Steps a sandbox can serve.
const (
	StepTriage    = "triage"
	StepRemediate = "remediate"
)

// Config describes the sandbox each run gets.
type Config struct {
	Namespace     string
	TokenAudience string // audience of the projected identity token
	Image         string
	RuntimeClass  string // "gvisor"
	ControllerURL string // how the agent reaches this controller
	MCPEndpoints  []string
	LLMBaseURL    string // llm-proxy; the sandbox never holds an API key
	AgentTools    string // optional comma-separated MCP tool allowlist (small models)
}

// The agent stops itself this long before the run's deadline, so it can still
// submit what it has; the sandbox is deleted this long after it.
const deadlineMargin = time.Minute

// Spec is one step's sandbox.
type Spec struct {
	Step string // StepTriage or StepRemediate; also the sandbox name prefix
	ID   string // the step resource's name
	// Owner is the step resource; deleting it deletes the sandbox.
	OwnerKind      string
	OwnerUID       types.UID
	ServiceAccount string
	Model          string
	Deadline       time.Time
	Env            map[string]string // extra environment, e.g. AGENT_MODE
}

// Name is the Sandbox object's name.
func (s Spec) Name() string { return s.Step + "-" + s.ID }

// Launcher creates one agent-sandbox Sandbox per step.
type Launcher struct {
	Client dynamic.Interface
	Config Config
	Now    func() time.Time
}

// Launch creates the sandbox and returns its name.
func (l *Launcher) Launch(ctx context.Context, spec Spec) (string, error) {
	obj := l.Config.Object(spec, l.Now())
	if _, err := l.Client.Resource(GVR).Namespace(l.Config.Namespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create sandbox %s: %w", obj.GetName(), err)
	}
	return obj.GetName(), nil
}

// Delete removes a sandbox. A sandbox that is already gone is not an error.
func (l *Launcher) Delete(ctx context.Context, name string) error {
	err := l.Client.Resource(GVR).Namespace(l.Config.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete sandbox %s: %w", name, err)
	}
	return nil
}

// Object builds the Sandbox for a step: a gVisor pod with a read-only root
// filesystem, a hard shutdown time, and no Kubernetes API credentials. It runs
// as the step's own ServiceAccount, which the coordinator created for this
// step alone. Its only credential is a projected token for TokenAudience,
// which the controller and llm-proxy verify; the API server rejects that
// audience. The Sandbox is owned by its step resource.
func (c Config) Object(spec Spec, now time.Time) *unstructured.Unstructured {
	labels := map[string]any{
		// Network policies select agents by this label, whatever their step.
		"app.kubernetes.io/name":    "triage-agent",
		"app.kubernetes.io/part-of": "alert-triage",
		RunLabel:                    spec.ID,
		StepLabel:                   spec.Step,
	}
	env := []any{
		envVar("RUN_ID", spec.ID),
		envVar("TOKEN_FILE", tokenFile),
		envVar("CONTROLLER_URL", c.ControllerURL),
		envVar("MCP_ENDPOINTS", strings.Join(c.MCPEndpoints, ",")),
		envVar("ANTHROPIC_MODEL", spec.Model),
		envVar("ANTHROPIC_BASE_URL", c.LLMBaseURL),
		envVar("AGENT_TIMEOUT", max(spec.Deadline.Sub(now)-deadlineMargin, deadlineMargin).String()),
	}
	if c.AgentTools != "" {
		env = append(env, envVar("AGENT_TOOLS", c.AgentTools))
	}
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		env = append(env, envVar(k, spec.Env[k]))
	}
	metadata := map[string]any{
		"name":      spec.Name(),
		"namespace": c.Namespace,
		"labels":    maps.Clone(labels),
	}
	if spec.OwnerUID != "" {
		metadata["ownerReferences"] = []any{map[string]any{
			"apiVersion": v1alpha1.GroupVersion.String(),
			"kind":       spec.OwnerKind,
			"name":       spec.ID,
			"uid":        string(spec.OwnerUID),
			"controller": true,
		}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agents.x-k8s.io/v1beta1",
		"kind":       "Sandbox",
		"metadata":   metadata,
		"spec": map[string]any{
			"shutdownTime":   spec.Deadline.Add(deadlineMargin).UTC().Format(time.RFC3339),
			"shutdownPolicy": "Delete",
			"podTemplate": map[string]any{
				"metadata": map[string]any{"labels": maps.Clone(labels)},
				"spec": map[string]any{
					"runtimeClassName":             c.RuntimeClass,
					"restartPolicy":                "Never",
					"serviceAccountName":           spec.ServiceAccount,
					"automountServiceAccountToken": false, // no Kubernetes API token

					"enableServiceLinks": false,
					"securityContext": map[string]any{
						"runAsNonRoot":   true,
						"runAsUser":      int64(65532),
						"runAsGroup":     int64(65532),
						"seccompProfile": map[string]any{"type": "RuntimeDefault"},
					},
					"containers": []any{map[string]any{
						"name":            "agent",
						"image":           c.Image,
						"imagePullPolicy": "IfNotPresent",
						"env":             env,
						"securityContext": map[string]any{
							"allowPrivilegeEscalation": false,
							"readOnlyRootFilesystem":   true,
							"capabilities":             map[string]any{"drop": []any{"ALL"}},
						},
						"resources": map[string]any{
							"requests": map[string]any{"cpu": "50m", "memory": "64Mi"},
							"limits":   map[string]any{"cpu": "500m", "memory": "256Mi"},
						},
						"volumeMounts": []any{
							map[string]any{"name": "scratch", "mountPath": "/tmp"},
							map[string]any{"name": "identity", "mountPath": tokenDir, "readOnly": true},
						},
					}},
					"volumes": []any{
						map[string]any{
							"name":     "scratch",
							"emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "64Mi"},
						},
						map[string]any{
							"name": "identity",
							"projected": map[string]any{"sources": []any{map[string]any{
								"serviceAccountToken": map[string]any{
									"audience":          c.TokenAudience,
									"expirationSeconds": int64(3600),
									"path":              "token",
								},
							}}},
						},
					},
				},
			},
		},
	}}
}

func envVar(name, value string) map[string]any {
	return map[string]any{"name": name, "value": value}
}
