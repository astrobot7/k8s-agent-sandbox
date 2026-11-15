package sandbox

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSandboxObjectIsLockedDown(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Namespace: "agent-sandboxes", TokenAudience: "triage.k8s-agent-sandbox.io",
		Image: "triage-agent:dev", RuntimeClass: "gvisor",
		ControllerURL: "http://ctrl:8080", LLMBaseURL: "http://llm-proxy:8080",
		MCPEndpoints: []string{"http://a/mcp", "http://b/mcp"},
	}
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	spec := Spec{Step: StepTriage, ID: "abc", OwnerKind: "TriageRun", OwnerUID: "uid-abc", ServiceAccount: "triage-abc",
		Model: "claude-sonnet-5-5", Deadline: now.Add(15 * time.Minute), Env: map[string]string{"AGENT_MODE": "triage"}}
	obj := cfg.Object(spec, now).Object

	str := func(path ...string) string {
		v, _, _ := unstructured.NestedString(obj, path...)
		return v
	}
	pod := []string{"spec", "podTemplate", "spec"}

	if got := str("metadata", "name"); got != "triage-abc" {
		t.Errorf("name = %q", got)
	}
	owners, _, _ := unstructured.NestedSlice(obj, "metadata", "ownerReferences")
	if len(owners) != 1 || owners[0].(map[string]any)["kind"] != "TriageRun" || owners[0].(map[string]any)["uid"] != "uid-abc" {
		t.Errorf("ownerReferences = %v; the sandbox must go when its run is deleted", owners)
	}
	if got := str("spec", "shutdownTime"); got != "2026-11-15T04:16:00Z" {
		t.Errorf("shutdownTime = %q", got)
	}
	if got := str("spec", "shutdownPolicy"); got != "Delete" {
		t.Errorf("shutdownPolicy = %q", got)
	}
	if got := str(append(pod, "runtimeClassName")...); got != "gvisor" {
		t.Errorf("runtimeClassName = %q", got)
	}
	if automount, _, _ := unstructured.NestedBool(obj, append(pod, "automountServiceAccountToken")...); automount {
		t.Error("the Kubernetes API token is mounted")
	}
	if got := str(append(pod, "serviceAccountName")...); got != "triage-abc" {
		t.Errorf("serviceAccountName = %q", got)
	}
	if got := str("spec", "podTemplate", "metadata", "labels", RunLabel); got != "abc" {
		t.Errorf("pod run label = %q", got)
	}
	if got := str("spec", "podTemplate", "metadata", "labels", StepLabel); got != StepTriage {
		t.Errorf("pod step label = %q", got)
	}
	volumes, _, _ := unstructured.NestedSlice(obj, append(pod, "volumes")...)
	var audience string
	for _, v := range volumes {
		if v.(map[string]any)["name"] == "identity" {
			sources, _, _ := unstructured.NestedSlice(v.(map[string]any), "projected", "sources")
			audience, _, _ = unstructured.NestedString(sources[0].(map[string]any), "serviceAccountToken", "audience")
		}
	}
	if audience != "triage.k8s-agent-sandbox.io" {
		t.Errorf("projected identity token audience = %q", audience)
	}

	containers, _, _ := unstructured.NestedSlice(obj, append(pod, "containers")...)
	c := containers[0].(map[string]any)
	if ro, _, _ := unstructured.NestedBool(c, "securityContext", "readOnlyRootFilesystem"); !ro {
		t.Error("root filesystem is writable")
	}
	env := map[string]map[string]any{}
	for _, e := range c["env"].([]any) {
		m := e.(map[string]any)
		env[m["name"].(string)] = m
	}
	if env["MCP_ENDPOINTS"]["value"] != "http://a/mcp,http://b/mcp" || env["ANTHROPIC_BASE_URL"]["value"] != "http://llm-proxy:8080" ||
		env["ANTHROPIC_MODEL"]["value"] != "claude-sonnet-5-5" || env["AGENT_TIMEOUT"]["value"] != "14m0s" {
		t.Errorf("env = %v", env)
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Error("the sandbox must not get an API key; the llm-proxy holds it")
	}
	if _, ok := env["RUN_TOKEN"]; ok {
		t.Error("RUN_TOKEN is gone: the agent authenticates with its ServiceAccount token")
	}
	if env["AGENT_MODE"]["value"] != "triage" {
		t.Errorf("AGENT_MODE = %v; extra env from the spec is missing", env["AGENT_MODE"])
	}
	if env["TOKEN_FILE"]["value"] != tokenFile {
		t.Errorf("TOKEN_FILE = %v", env["TOKEN_FILE"])
	}
}
