package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRemediateRetriesARefusedProposal(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{
		{Calls: []ToolCall{call("1", "pods_get", `{"name":"payments-api"}`)}},
		{Calls: []ToolCall{call("2", ProposeActionTool, `{"action":"scale","replicas":9,"rationale":"load"}`)}},
		{Calls: []ToolCall{call("3", ProposeActionTool, `{"action":"rollout-restart","rationale":"pool exhausted: 'too many connections'"}`)}},
	}}
	var submitted []RemediationDecision
	submit := func(_ context.Context, d RemediationDecision) error {
		submitted = append(submitted, d)
		if d.Action == "scale" {
			return errors.New("replicas 9 exceeds maxReplicas 4")
		}
		return nil
	}
	a := newAgent(m, &fakeToolbox{}, DefaultBudget)
	d, err := a.Remediate(t.Context(), RemediationTask{Prompt: "remediate", Actions: []string{"rollout-restart", "scale"}, MaxReplicas: 4}, submit)
	if err != nil {
		t.Fatalf("Remediate: %v", err)
	}
	if d.Action != "rollout-restart" || len(submitted) != 2 {
		t.Errorf("decision = %+v, submitted = %+v", d, submitted)
	}
	if got := m.sent[1][0]; !got.IsError || !strings.Contains(got.Content, "maxReplicas") {
		t.Errorf("the refusal was not shown to the model: %+v", got)
	}
	if m.system != RemediationPrompt {
		t.Error("remediation prompt not used")
	}
}

func TestRemediateNoAction(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{{Calls: []ToolCall{call("1", NoActionTool, `{"reason":"DB_HOST is wrong; a restart will not help"}`)}}}}
	d, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Remediate(t.Context(),
		RemediationTask{Prompt: "remediate", Actions: []string{"rollout-restart"}}, func(context.Context, RemediationDecision) error { return nil })
	if err != nil || !strings.Contains(d.NoAction, "DB_HOST") {
		t.Fatalf("Remediate = %+v, %v", d, err)
	}
	for _, tool := range m.tools {
		if tool.Name == ProposeActionTool {
			if _, ok := tool.InputSchema["properties"].(map[string]any)["replicas"]; ok {
				t.Error("replicas offered although scale is not allowed")
			}
		}
	}
}

func TestRemediateStopsAtTheTurnBudget(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{{Text: "thinking"}, {Text: "still thinking"}}}
	_, err := newAgent(m, &fakeToolbox{}, Budget{MaxTurns: 2, MaxToolCalls: 5, MaxToolOutputBytes: 100}).Remediate(t.Context(),
		RemediationTask{Prompt: "remediate", Actions: []string{"rollout-restart"}}, func(context.Context, RemediationDecision) error { return nil })
	if !errors.Is(err, ErrNoDecision) {
		t.Errorf("err = %v, want ErrNoDecision", err)
	}
}
