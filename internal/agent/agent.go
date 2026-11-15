// Package agent runs the triage loop: an LLM investigates an alert by calling
// read-only tools and finishes by calling submit_report.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"k8s-agent-sandbox/internal/report"
	"k8s-agent-sandbox/internal/skills"
)

// Built-in tools handled by the agent itself, not by an MCP server.
const (
	SubmitReportTool = "submit_report" // finishes the run
	ReadSkillTool    = "read_skill"    // returns a triage skill's instructions
)

// Task is one investigation: the assignment prompt and the alert's labels,
// which select the skills that apply automatically.
type Task struct {
	Prompt string
	Labels map[string]string
}

// Tool describes a tool offered to the model.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Reply is one model turn.
type Reply struct {
	Text  string
	Calls []ToolCall
}

// Model is a conversation with an LLM. Start begins it; Continue sends the
// results for every call in the previous reply plus an optional note.
type Model interface {
	Start(ctx context.Context, system, prompt string, tools []Tool) (Reply, error)
	Continue(ctx context.Context, results []ToolResult, note string) (Reply, error)
}

// Toolbox runs the investigation tools. A returned error is shown to the model
// as a failed tool call.
type Toolbox interface {
	Tools() []Tool
	Call(ctx context.Context, name string, input json.RawMessage) (string, error)
}

// Budget bounds a run.
type Budget struct {
	MaxTurns           int
	MaxToolCalls       int
	MaxToolOutputBytes int
}

// DefaultBudget is sized for a focused investigation.
var DefaultBudget = Budget{MaxTurns: 25, MaxToolCalls: 40, MaxToolOutputBytes: 16 << 10}

// ErrNoReport means the model used its whole budget without a valid report.
var ErrNoReport = errors.New("agent finished without a valid report")

// Agent wires a model to a toolbox and the team's triage skills.
type Agent struct {
	Model   Model
	Toolbox Toolbox
	Skills  *skills.Library // optional
	Budget  Budget
	Logger  *slog.Logger
}

// Triage investigates the task and returns the validated report.
func (a *Agent) Triage(ctx context.Context, task Task) (report.Report, error) {
	tools := a.Toolbox.Tools()
	if a.Skills.Len() > 0 {
		tools = append(tools, Tool{
			Name:        ReadSkillTool,
			Description: "Return the full instructions of a triage skill. Call it when the evidence points to a problem a listed skill covers.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "enum": a.Skills.Names(), "description": "Skill name from the list in the system prompt."},
				},
				"required": []string{"name"},
			},
		})
	}
	tools = append(tools, Tool{
		Name:        SubmitReportTool,
		Description: "Submit the final triage report. Call exactly once, when the investigation is done.",
		InputSchema: report.Schema(),
	})

	system := SystemPrompt
	if section := a.Skills.PromptSection(task.Labels); section != "" {
		system += "\n\n" + section
	}
	if matched := a.Skills.Matching(task.Labels); len(matched) > 0 {
		names := make([]string, 0, len(matched))
		for _, s := range matched {
			names = append(names, s.Name)
		}
		a.Logger.Info("skills matched alert", "skills", names)
	}

	reply, err := a.Model.Start(ctx, system, task.Prompt, tools)
	if err != nil {
		return report.Report{}, fmt.Errorf("start conversation: %w", err)
	}

	toolCalls := 0
	var toolNames []string // MCP tools only; a next step naming one is work the agent can do
	for _, t := range a.Toolbox.Tools() {
		toolNames = append(toolNames, t.Name)
	}
	rv := newReviewState(toolNames)
	for turn := 1; ; turn++ {
		a.logReply(turn, reply)
		reviewNote := ""
		if len(reply.Calls) == 0 {
			// Small models sometimes write the report as text instead of calling the tool.
			if r, ok := reportInText(reply.Text); ok {
				if feedback := rv.review(&r); feedback != "" {
					a.Logger.Info("report sent back", "turn", turn, "rejections", rv.rejections)
					reviewNote = feedback
				} else {
					a.Logger.Info("report recovered from reply text", "turn", turn, "tool_calls", toolCalls, "incomplete", r.Incomplete)
					return r, nil
				}
			}
		}
		var results []ToolResult
		for _, call := range reply.Calls {
			if call.Name == SubmitReportTool {
				r, err := parseReport(call.Input)
				if err == nil {
					if feedback := rv.review(&r); feedback != "" {
						a.Logger.Info("report sent back", "turn", turn, "rejections", rv.rejections)
						results = append(results, ToolResult{CallID: call.ID, Content: feedback, IsError: true})
						continue
					}
					a.Logger.Info("report submitted", "turn", turn, "tool_calls", toolCalls, "incomplete", r.Incomplete)
					return r, nil
				}
				results = append(results, ToolResult{CallID: call.ID, Content: err.Error() + ". Fix it and call submit_report again.", IsError: true})
				continue
			}
			if call.Name == ReadSkillTool && a.Skills.Len() > 0 {
				results = append(results, a.readSkill(call))
				continue
			}
			res := a.runTool(ctx, call, &toolCalls)
			if !res.IsError {
				rv.record(call.Name, res.Content)
			}
			results = append(results, res)
		}

		if turn >= a.Budget.MaxTurns {
			return report.Report{}, fmt.Errorf("%w: turn budget of %d used", ErrNoReport, a.Budget.MaxTurns)
		}

		note := ""
		switch {
		case reviewNote != "":
			note = reviewNote
		case len(reply.Calls) == 0:
			note = "You must finish by calling submit_report."
		case turn == a.Budget.MaxTurns-1:
			note = "This is your last turn. Call submit_report now with what you have."
		}
		reply, err = a.Model.Continue(ctx, results, note)
		if err != nil {
			return report.Report{}, fmt.Errorf("continue conversation (turn %d): %w", turn, err)
		}
	}
}

func (a *Agent) runTool(ctx context.Context, call ToolCall, used *int) ToolResult {
	if *used >= a.Budget.MaxToolCalls {
		return ToolResult{CallID: call.ID, IsError: true,
			Content: "Tool call budget exhausted. Call submit_report now with the evidence you have."}
	}
	*used++
	a.Logger.Info("tool call", "tool", call.Name, "n", *used)
	out, err := a.Toolbox.Call(ctx, call.Name, call.Input)
	if err != nil {
		// Logged so a run can be diagnosed later: a failed call does not count as
		// evidence in the report review.
		msg := err.Error()
		a.Logger.Warn("tool call failed", "tool", call.Name, "n", *used, "input", truncate(string(call.Input), 300), "error", truncate(msg, 300))
		return ToolResult{CallID: call.ID, Content: truncate(msg, a.Budget.MaxToolOutputBytes), IsError: true}
	}
	a.Logger.Info("tool result", "tool", call.Name, "n", *used, "bytes", len(out))
	return ToolResult{CallID: call.ID, Content: truncate(out, a.Budget.MaxToolOutputBytes)}
}

// logReply records what the model did each turn (short text excerpt and tool
// names), so a run can be diagnosed from the sandbox logs in SigNoz after the
// sandbox is gone.
func (a *Agent) logReply(turn int, r Reply) {
	names := make([]string, 0, len(r.Calls))
	for _, c := range r.Calls {
		names = append(names, c.Name)
	}
	text := strings.TrimSpace(r.Text)
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	a.Logger.Info("model reply", "turn", turn, "calls", names, "text", text)
}

// readSkill answers read_skill from the skills in the assignment. It does not
// count against the tool budget: it reads local data only.
func (a *Agent) readSkill(call ToolCall) ToolResult {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return ToolResult{CallID: call.ID, IsError: true, Content: "read_skill needs {\"name\": \"<skill>\"}"}
	}
	s, ok := a.Skills.Get(in.Name)
	if !ok {
		return ToolResult{CallID: call.ID, IsError: true,
			Content: fmt.Sprintf("no skill named %q; available: %s", in.Name, strings.Join(a.Skills.Names(), ", "))}
	}
	a.Logger.Info("skill read", "skill", s.Name)
	return ToolResult{CallID: call.ID, Content: "# Skill " + s.Name + "\n\n" + s.Instructions}
}

func parseReport(input json.RawMessage) (report.Report, error) {
	var r report.Report
	if err := json.Unmarshal(input, &r); err != nil {
		return report.Report{}, fmt.Errorf("%w: %w", report.ErrInvalid, err)
	}
	if err := r.Validate(); err != nil {
		return report.Report{}, err
	}
	return r, nil
}

func truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return fmt.Sprintf("%s\n[output truncated: %d of %d bytes shown; narrow the query for more]", s[:limit], limit, len(s))
}
