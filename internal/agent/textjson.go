package agent

import (
	"encoding/json"

	"k8s-agent-sandbox/internal/report"
)

// reportInText recovers a report a model wrote as text instead of calling
// submit_report. Small models often do this: they print the report JSON, or a
// {"name": "submit_report", "arguments": {...}} object. Only a report that
// passes validation is accepted.
func reportInText(text string) (report.Report, bool) {
	for _, obj := range jsonObjects(text) {
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Params    json.RawMessage `json:"parameters"`
		}
		candidate := json.RawMessage(obj)
		if json.Unmarshal(candidate, &call) == nil && call.Name == SubmitReportTool {
			candidate = call.Arguments
			if len(candidate) == 0 {
				candidate = call.Params
			}
		}
		if r, err := parseReport(candidate); err == nil {
			return r, true
		}
	}
	return report.Report{}, false
}

// jsonObjects returns the balanced top-level {...} spans in s, ignoring braces
// inside JSON strings.
func jsonObjects(s string) []string {
	var out []string
	depth, start, inStr, esc := 0, -1, false, false
	for i, r := range s {
		switch {
		case esc:
			esc = false
		case inStr && r == '\\':
			esc = true
		case r == '"' && depth > 0:
			inStr = !inStr
		case inStr:
		case r == '{':
			if depth == 0 {
				start = i
			}
			depth++
		case r == '}' && depth > 0:
			depth--
			if depth == 0 {
				out = append(out, s[start:i+1])
			}
		}
	}
	return out
}
