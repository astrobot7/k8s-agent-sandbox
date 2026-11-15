package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"k8s-agent-sandbox/internal/skills"
)

func TestTriageUsesSkills(t *testing.T) {
	t.Parallel()
	lib, err := skills.New(
		skills.Skill{Name: "k8s-warning-events", Description: "entry point", Instructions: "ENTRY STEPS",
			Match: map[string][]string{"alertname": {"K8s warning events"}}},
		skills.Skill{Name: "oomkilled", Description: "memory limit kills", Instructions: "OOM STEPS"},
	)
	if err != nil {
		t.Fatal(err)
	}
	m := &scriptedModel{replies: []Reply{
		{Calls: []ToolCall{call("1", ReadSkillTool, `{"name":"oomkilled"}`), call("2", ReadSkillTool, `{"name":"nope"}`)}},
		{Calls: []ToolCall{call("3", SubmitReportTool, goodReport)}},
	}}
	tb := &fakeToolbox{}
	a := newAgent(m, tb, Budget{MaxTurns: 5, MaxToolCalls: 1, MaxToolOutputBytes: 1000})
	a.Skills = lib

	if _, err := a.Triage(t.Context(), Task{Prompt: "alert", Labels: map[string]string{"alertname": "K8s warning events"}}); err != nil {
		t.Fatalf("Triage: %v", err)
	}
	// Matched skill inlined, unmatched skill only indexed.
	if !strings.Contains(m.system, "ENTRY STEPS") || !strings.Contains(m.system, "- oomkilled: memory limit kills") {
		t.Errorf("system prompt lacks skills:\n%s", m.system)
	}
	if strings.Contains(m.system, "OOM STEPS") {
		t.Error("unmatched skill instructions were inlined")
	}
	if got := m.sent[0][0]; got.IsError || !strings.Contains(got.Content, "OOM STEPS") {
		t.Errorf("read_skill(oomkilled) = %+v", got)
	}
	if got := m.sent[0][1]; !got.IsError || !strings.Contains(got.Content, "available: k8s-warning-events, oomkilled") {
		t.Errorf("read_skill(nope) = %+v", got)
	}
	if len(tb.calls) != 0 {
		t.Errorf("read_skill went to the toolbox: %v", tb.calls)
	}
	var offered bool
	for _, tool := range m.tools {
		offered = offered || tool.Name == ReadSkillTool
	}
	if !offered {
		t.Error("read_skill not offered")
	}
}

func TestTriageWithoutSkillsOffersNoReadSkill(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{{Calls: []ToolCall{call("1", SubmitReportTool, goodReport)}}}}
	if _, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range m.tools {
		if tool.Name == ReadSkillTool {
			t.Error("read_skill offered without skills")
		}
	}
	if strings.Contains(m.system, "Triage skills") {
		t.Error("skills section present without skills")
	}
}

// scriptedModel replays canned replies and records what the agent sent back.
type scriptedModel struct {
	replies []Reply
	sent    [][]ToolResult
	notes   []string
	tools   []Tool
	system  string
}

func (m *scriptedModel) Start(_ context.Context, system, _ string, tools []Tool) (Reply, error) {
	m.system, m.tools = system, tools
	return m.next()
}

func (m *scriptedModel) Continue(_ context.Context, results []ToolResult, note string) (Reply, error) {
	m.sent = append(m.sent, results)
	m.notes = append(m.notes, note)
	return m.next()
}

func (m *scriptedModel) next() (Reply, error) {
	if len(m.replies) == 0 {
		return Reply{}, errors.New("script exhausted")
	}
	r := m.replies[0]
	m.replies = m.replies[1:]
	return r, nil
}

type fakeToolbox struct {
	outputs map[string]string
	errs    map[string]error
	calls   []string
}

func (f *fakeToolbox) Tools() []Tool {
	return []Tool{{Name: "pods_get", InputSchema: map[string]any{"type": "object"}}}
}

func (f *fakeToolbox) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	f.calls = append(f.calls, name)
	if err := f.errs[name]; err != nil {
		return "", err
	}
	return f.outputs[name], nil
}

const goodReport = `{"summary":"api crashloops","likely_cause":"OOMKilled","confidence":"high",
 "evidence":[{"source":"alert labels","detail":"reason=OOMKilled"}],"next_steps":["raise memory limit"]}`

func call(id, name, input string) ToolCall {
	return ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

func newAgent(m Model, tb Toolbox, b Budget) *Agent {
	return &Agent{Model: m, Toolbox: tb, Budget: b, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestTriageHappyPath(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{
		{Calls: []ToolCall{call("1", "pods_get", `{"name":"api"}`)}},
		{Calls: []ToolCall{call("2", SubmitReportTool, goodReport)}},
	}}
	tb := &fakeToolbox{outputs: map[string]string{"pods_get": "status: OOMKilled"}}

	r, err := newAgent(m, tb, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"})
	if err != nil {
		t.Fatalf("Triage: %v", err)
	}
	if r.LikelyCause != "OOMKilled" {
		t.Errorf("LikelyCause = %q", r.LikelyCause)
	}
	if got := m.sent[0][0]; got.CallID != "1" || got.Content != "status: OOMKilled" || got.IsError {
		t.Errorf("tool result sent to model = %+v", got)
	}
	if last := m.tools[len(m.tools)-1]; last.Name != SubmitReportTool {
		t.Errorf("submit_report not offered as a tool; last tool = %q", last.Name)
	}
}

func TestTriageInvalidReportIsSentBack(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{
		{Calls: []ToolCall{call("1", SubmitReportTool, `{"summary":"x","confidence":"certain"}`)}},
		{Calls: []ToolCall{call("2", SubmitReportTool, goodReport)}},
	}}
	if _, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"}); err != nil {
		t.Fatalf("Triage: %v", err)
	}
	got := m.sent[0][0]
	if !got.IsError || !strings.Contains(got.Content, "confidence must be") {
		t.Errorf("invalid report feedback = %+v", got)
	}
}

func TestTriageRecoversReportWrittenAsText(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, text string }{
		{"bare report json", "Here is my report:\n```json\n" + goodReport + "\n```"},
		{"submit_report written as text", `{"name": "submit_report", "arguments": ` + goodReport + `}`},
		{"submit_report with parameters", `I am done. {"name": "submit_report", "parameters": ` + goodReport + `}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &scriptedModel{replies: []Reply{{Text: tc.text}}}
			r, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"})
			if err != nil || r.LikelyCause != "OOMKilled" {
				t.Fatalf("Triage = %+v, %v", r, err)
			}
		})
	}
}

func TestTriageIgnoresInvalidReportText(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{
		{Text: `{"summary": "x", "confidence": "certain"}`}, // invalid: nudged, not accepted
		{Calls: []ToolCall{call("1", SubmitReportTool, goodReport)}},
	}}
	if _, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"}); err != nil {
		t.Fatal(err)
	}
	if len(m.notes) != 1 || !strings.Contains(m.notes[0], "submit_report") {
		t.Errorf("invalid report text should be nudged; notes = %q", m.notes)
	}
}

func TestTriageNudgesWhenModelStopsWithoutReport(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{
		{Text: "I think it is OOM."},
		{Calls: []ToolCall{call("1", SubmitReportTool, goodReport)}},
	}}
	if _, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"}); err != nil {
		t.Fatalf("Triage: %v", err)
	}
	if !strings.Contains(m.notes[0], "submit_report") {
		t.Errorf("note = %q, want a nudge to call submit_report", m.notes[0])
	}
}

func TestTriageEnforcesBudgets(t *testing.T) {
	t.Parallel()
	loop := Reply{Calls: []ToolCall{call("x", "pods_get", `{}`)}}
	m := &scriptedModel{replies: []Reply{loop, loop, loop, loop}}
	tb := &fakeToolbox{outputs: map[string]string{"pods_get": "ok"}}

	_, err := newAgent(m, tb, Budget{MaxTurns: 4, MaxToolCalls: 2, MaxToolOutputBytes: 100}).Triage(t.Context(), Task{Prompt: "alert"})
	if !errors.Is(err, ErrNoReport) {
		t.Fatalf("Triage error = %v, want ErrNoReport", err)
	}
	if len(tb.calls) != 2 {
		t.Errorf("toolbox called %d times, want 2 (MaxToolCalls)", len(tb.calls))
	}
	if third := m.sent[2][0]; !third.IsError || !strings.Contains(third.Content, "budget exhausted") {
		t.Errorf("call over budget got %+v", third)
	}
	if !strings.Contains(m.notes[2], "last turn") {
		t.Errorf("no last-turn warning; notes = %q", m.notes)
	}
}

func TestTriageToolErrorsAndTruncation(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{
		{Calls: []ToolCall{call("1", "pods_get", `{}`), call("2", "pods_log", `{}`)}},
		{Calls: []ToolCall{call("3", SubmitReportTool, goodReport)}},
	}}
	tb := &fakeToolbox{
		outputs: map[string]string{"pods_get": strings.Repeat("a", 50)},
		errs:    map[string]error{"pods_log": errors.New("forbidden")},
	}
	if _, err := newAgent(m, tb, Budget{MaxTurns: 5, MaxToolCalls: 5, MaxToolOutputBytes: 10}).Triage(t.Context(), Task{Prompt: "alert"}); err != nil {
		t.Fatalf("Triage: %v", err)
	}
	got := m.sent[0]
	if !strings.HasPrefix(got[0].Content, strings.Repeat("a", 10)+"\n[output truncated") {
		t.Errorf("long output not truncated: %q", got[0].Content)
	}
	if !got[1].IsError || got[1].Content != "forbidden" {
		t.Errorf("tool error result = %+v", got[1])
	}
}
