package agent

import (
	"strings"
	"testing"
)

// The next steps from a real report produced by the local model.
var localModelSteps = []string{
	"Check the logs of the failing containers using the 'pods_log' function to see if there are any error messages that can provide more information about the issue.",
	"Verify that the container image is correct and available in the image registry.",
	"Check the application code for any issues that may be causing the application to crash.",
	"If the issue is related to the application code, update the code and redeploy the pods.",
}

func TestUninvestigatedSteps(t *testing.T) {
	t.Parallel()
	got := uninvestigatedSteps(localModelSteps, []string{"pods_log", "pods_get", "events_list"})
	if len(got) != 2 || got[0] != localModelSteps[0] || got[1] != localModelSteps[1] {
		t.Errorf("flagged %q; want the logs step (names pods_log) and the image step (verify + image)", got)
	}
	humanOnly := []string{
		"Create the payments-db Service or fix DB_HOST in the Deployment.",
		"Roll back with kubectl rollout undo deployment/payments-api.",
		"Raise the memory limit to 512Mi.",
	}
	if got := uninvestigatedSteps(humanOnly, []string{"pods_log"}); len(got) != 0 {
		t.Errorf("human-only steps flagged: %q", got)
	}
}

const reportWithPendingSteps = `{"summary":"api crashloops","likely_cause":"unknown","confidence":"low",
 "evidence":[{"source":"events_list","detail":"Back-off restarting failed container"}],
 "next_steps":["Check the logs of the failing containers using pods_log"]}`

func TestReportWithInvestigableStepsIsSentBack(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{
		{Calls: []ToolCall{call("1", SubmitReportTool, reportWithPendingSteps)}},
		{Calls: []ToolCall{call("2", "pods_log", `{"name":"api","previous":true}`)}},
		{Calls: []ToolCall{call("3", SubmitReportTool, goodReport)}},
	}}
	tb := &fakeToolbox{outputs: map[string]string{"pods_log": "lookup payments-db: no such host"}}
	r, err := newAgent(m, tb, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"})
	if err != nil || r.LikelyCause != "OOMKilled" {
		t.Fatalf("Triage = %+v, %v", r, err)
	}
	first := m.sent[0][0]
	if !first.IsError || !strings.Contains(first.Content, "investigations you can do now") || !strings.Contains(first.Content, "pods_log") {
		t.Errorf("feedback for the first report = %+v", first)
	}
	if len(tb.calls) != 1 || tb.calls[0] != "pods_log" {
		t.Errorf("agent did not investigate after feedback: %v", tb.calls)
	}
}

func TestReportAcceptedAfterMaxRejections(t *testing.T) {
	t.Parallel()
	pending := Reply{Calls: []ToolCall{call("x", SubmitReportTool, reportWithPendingSteps)}}
	m := &scriptedModel{replies: []Reply{pending, pending, pending}}
	r, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"})
	if err != nil {
		t.Fatalf("Triage: %v", err)
	}
	if r.LikelyCause != "unknown" || len(m.sent) != MaxReportRejections {
		t.Errorf("want the report accepted on attempt %d; got %+v after %d rejections", MaxReportRejections+1, r, len(m.sent))
	}
}

// Evidence the model made up (a tool it never called, a link no tool returned)
// is sent back; at the limit the report is accepted, the link removed, and the
// report marked incomplete.
func TestMadeUpEvidenceIsSentBackThenMarkedIncomplete(t *testing.T) {
	t.Parallel()
	madeUp := `{"summary":"api crashloops","likely_cause":"bad image","confidence":"low",
	 "evidence":[{"source":"pods_get","detail":"No specific details provided","link":"http://localhost:8080/invented"}],
	 "next_steps":["Raise the memory limit."]}`
	m := &scriptedModel{replies: []Reply{
		{Calls: []ToolCall{call("1", SubmitReportTool, madeUp)}},
		{Calls: []ToolCall{call("2", SubmitReportTool, madeUp)}},
		{Calls: []ToolCall{call("3", SubmitReportTool, madeUp)}},
	}}
	r, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"})
	if err != nil {
		t.Fatalf("Triage: %v", err)
	}
	if fb := m.sent[0][0].Content; !strings.Contains(fb, "cites pods_get, which you have not called") || !strings.Contains(fb, "did not come from any tool output") {
		t.Errorf("feedback = %q", fb)
	}
	if r.Evidence[0].Link != "" || !strings.Contains(r.Incomplete, "not backed by a tool call") {
		t.Errorf("accepted report = %+v; want link removed and incomplete set", r)
	}
}

func TestEvidenceFromToolsIsAccepted(t *testing.T) {
	t.Parallel()
	backed := `{"summary":"api crashloops","likely_cause":"db host does not resolve","confidence":"high",
	 "evidence":[{"source":"pods_log workloads/api","detail":"lookup payments-db: no such host","link":"https://signoz.local/logs/1"}],
	 "next_steps":["Create the payments-db Service."]}`
	m := &scriptedModel{replies: []Reply{
		{Calls: []ToolCall{call("1", "pods_log", `{}`)}},
		{Calls: []ToolCall{call("2", SubmitReportTool, backed)}},
	}}
	tb := &fakeToolbox{outputs: map[string]string{"pods_log": "lookup payments-db: no such host (see https://signoz.local/logs/1)"}}
	r, err := newAgent(m, tb, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"})
	if err != nil || r.Incomplete != "" || r.Evidence[0].Link != "https://signoz.local/logs/1" {
		t.Fatalf("Triage = %+v, %v; want accepted on first submit with the link kept", r, err)
	}
}

func TestTextReportWithInvestigableStepsIsSentBack(t *testing.T) {
	t.Parallel()
	m := &scriptedModel{replies: []Reply{
		{Text: "Report: " + reportWithPendingSteps},
		{Calls: []ToolCall{call("1", SubmitReportTool, goodReport)}},
	}}
	if _, err := newAgent(m, &fakeToolbox{}, DefaultBudget).Triage(t.Context(), Task{Prompt: "alert"}); err != nil {
		t.Fatal(err)
	}
	if len(m.notes) != 1 || !strings.Contains(m.notes[0], "investigations you can do now") {
		t.Errorf("text report with pending steps was not sent back; notes = %q", m.notes)
	}
}
