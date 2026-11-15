package agent

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"k8s-agent-sandbox/internal/report"
)

// MaxReportRejections bounds how often a report is sent back for unfinished
// investigation, so a model that cannot comply still ends with a report.
const MaxReportRejections = 2

var (
	investigativeVerbs = []string{"check", "verify", "inspect", "review", "look at", "look into",
		"investigate", "examine", "confirm", "query", "search", "describe", "get the", "list the"}
	investigableObjects = []string{"log", "event", "image", "status", "restart", "exit code", "pod", "deployment",
		"replicaset", "statefulset", "daemonset", "service", "endpoint", "node", "metric", "trace", "span",
		"probe", "resource limit", "memory", "cpu", "spec", "manifest", "alert history"}
)

// uninvestigatedSteps returns next steps the agent could carry out itself with
// its read-only tools: they name one of its tools, or ask to check/verify/
// inspect something its tools can see (logs, events, images, status, metrics,
// traces, resources). Steps only a human can take (change code or config,
// roll back, scale, contact a team) are not returned.
func uninvestigatedSteps(steps []string, toolNames []string) []string {
	var out []string
	for _, s := range steps {
		lower := strings.ToLower(s)
		if containsAny(lower, toolNames) || (containsAny(lower, investigativeVerbs) && containsAny(lower, investigableObjects)) {
			out = append(out, s)
		}
	}
	return out
}

var urlRE = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)

// reviewState tracks what the agent actually did, to check a report against.
type reviewState struct {
	toolNames  []string        // MCP tools offered
	called     map[string]bool // tools that returned a result this run
	urls       map[string]bool // links that appeared in tool output
	rejections int
}

func newReviewState(toolNames []string) *reviewState {
	return &reviewState{toolNames: toolNames, called: map[string]bool{}, urls: map[string]bool{}}
}

// record notes a successful tool call and the links in its output.
func (s *reviewState) record(tool, output string) {
	s.called[tool] = true
	for _, u := range urlRE.FindAllString(output, -1) {
		s.urls[u] = true
	}
}

// evidenceProblems lists evidence citing a tool not called this run, or a
// link no tool returned. Such evidence was made up, not gathered.
func (s *reviewState) evidenceProblems(r report.Report) []string {
	var out []string
	for i, e := range r.Evidence {
		fields := strings.FieldsFunc(strings.ToLower(e.Source), func(c rune) bool {
			return c == ' ' || c == ':' || c == '(' || c == '/' || c == ','
		})
		if len(fields) > 0 && slices.Contains(s.toolNames, fields[0]) && !s.called[fields[0]] {
			out = append(out, fmt.Sprintf("evidence[%d] cites %s, which you have not called in this run", i, fields[0]))
		}
		if e.Link != "" && !s.urls[e.Link] {
			out = append(out, fmt.Sprintf("evidence[%d].link %s did not come from any tool output; use only links tools returned, or none", i, e.Link))
		}
	}
	return out
}

// review decides whether to accept r. It returns feedback to send back, or ""
// to accept. At the rejection limit it accepts anyway, removes made-up links,
// and marks the report incomplete so on-call is not misled.
func (s *reviewState) review(r *report.Report) string {
	steps := uninvestigatedSteps(r.NextSteps, s.toolNames)
	problems := s.evidenceProblems(*r)
	if len(steps) == 0 && len(problems) == 0 {
		return ""
	}
	if s.rejections < MaxReportRejections {
		s.rejections++
		return reviewFeedback(steps, problems)
	}
	for i := range r.Evidence {
		if r.Evidence[i].Link != "" && !s.urls[r.Evidence[i].Link] {
			r.Evidence[i].Link = ""
		}
	}
	var missing []string
	if len(steps) > 0 {
		missing = append(missing, fmt.Sprintf("%d next step(s) the agent could have checked itself", len(steps)))
	}
	if len(problems) > 0 {
		missing = append(missing, fmt.Sprintf("%d evidence item(s) not backed by a tool call", len(problems)))
	}
	r.Incomplete = "accepted after " + fmt.Sprint(MaxReportRejections) + " reviews with " + strings.Join(missing, " and ")
	return ""
}

// reviewFeedback is sent back to the model with a rejected report.
func reviewFeedback(steps, problems []string) string {
	var b strings.Builder
	b.WriteString("Report not accepted yet.\n")
	if len(steps) > 0 {
		b.WriteString("These next_steps are investigations you can do now with your tools. Do them, add what you find to evidence ")
		b.WriteString("(quote the exact lines and values), and keep next_steps for actions only a human can take ")
		b.WriteString("(change code or config, roll back, scale, redeploy):\n")
		for i, st := range steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, st)
		}
	}
	if len(problems) > 0 {
		b.WriteString("Evidence must come from tool calls you made in this run:\n")
		for _, p := range problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	b.WriteString("Then call submit_report again.")
	return b.String()
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(s, strings.ToLower(n)) {
			return true
		}
	}
	return false
}
