// Package report defines the triage report an agent returns, its validation,
// and its JSON schema (used as the agent's submit_report tool input).
package report

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Confidence is how sure the agent is about the likely cause.
type Confidence string

// Confidence levels.
const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Limits keep reports readable and bound what a manipulated agent can push out.
const (
	maxSummary     = 2000
	maxCause       = 4000
	maxEvidence    = 20
	maxEvidenceLen = 2000
	maxSteps       = 10
	maxStepLen     = 1000
)

// Evidence is one observation that supports the conclusion.
type Evidence struct {
	Source string `json:"source"`
	Detail string `json:"detail"`
	Link   string `json:"link,omitempty"`
}

// Report is the result of one triage run.
type Report struct {
	Summary     string     `json:"summary"`
	LikelyCause string     `json:"likely_cause"`
	Confidence  Confidence `json:"confidence"`
	Evidence    []Evidence `json:"evidence"`
	NextSteps   []string   `json:"next_steps"`
	// Incomplete is set by the agent, never by the model (it is not in Schema):
	// the report was accepted only because the agent's review limit was reached.
	Incomplete string `json:"incomplete,omitempty"`
}

// ErrInvalid is wrapped by every validation error.
var ErrInvalid = errors.New("invalid report")

// Validate checks required fields, allowed values, and size limits.
func (r Report) Validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(strings.TrimSpace(r.Summary) != "", "summary is required")
	check(len(r.Summary) <= maxSummary, "summary longer than %d bytes", maxSummary)
	check(strings.TrimSpace(r.LikelyCause) != "", "likely_cause is required")
	check(len(r.LikelyCause) <= maxCause, "likely_cause longer than %d bytes", maxCause)
	switch r.Confidence {
	case ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
	default:
		errs = append(errs, fmt.Errorf("confidence must be low, medium, or high, got %q", r.Confidence))
	}
	check(len(r.Evidence) > 0, "at least one evidence entry is required")
	check(len(r.Evidence) <= maxEvidence, "more than %d evidence entries", maxEvidence)
	for i, e := range r.Evidence {
		check(e.Source != "" && e.Detail != "", "evidence[%d] needs source and detail", i)
		check(len(e.Detail) <= maxEvidenceLen, "evidence[%d].detail longer than %d bytes", i, maxEvidenceLen)
		if e.Link != "" {
			u, err := url.Parse(e.Link)
			check(err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "",
				"evidence[%d].link must be an http(s) URL", i)
		}
	}
	check(len(r.NextSteps) <= maxSteps, "more than %d next_steps", maxSteps)
	for i, s := range r.NextSteps {
		check(len(s) <= maxStepLen, "next_steps[%d] longer than %d bytes", i, maxStepLen)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return nil
}

// Schema returns the JSON schema of a Report, for the submit_report tool.
func Schema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":      str("One or two sentences: what is wrong and what it affects."),
			"likely_cause": str("The most likely root cause, and why. Say so plainly if evidence is insufficient."),
			"confidence": map[string]any{
				"type": "string", "enum": []string{"low", "medium", "high"},
				"description": "How well the evidence supports likely_cause.",
			},
			"evidence": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"source": str("Where this came from, e.g. pods_log checkout/api-7d9f or signoz_search_logs."),
						"detail": str("The concrete observation: quoted error line, status, timestamp, value."),
						"link":   str("Optional SigNoz webUrl for this evidence."),
					},
					"required": []string{"source", "detail"},
				},
			},
			"next_steps": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Concrete actions for the on-call engineer, most useful first.",
			},
		},
		"required": []string{"summary", "likely_cause", "confidence", "evidence", "next_steps"},
	}
}

// Markdown renders the report for chat and logs.
func (r Report) Markdown(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%s*\n", title)
	if r.Incomplete != "" {
		fmt.Fprintf(&b, ":warning: *Incomplete investigation:* %s\n", r.Incomplete)
	}
	fmt.Fprintf(&b, "%s\n\n*Likely cause* (%s confidence): %s\n", r.Summary, r.Confidence, r.LikelyCause)
	if len(r.Evidence) > 0 {
		b.WriteString("\n*Evidence*\n")
		for _, e := range r.Evidence {
			fmt.Fprintf(&b, "• [%s] %s", e.Source, e.Detail)
			if e.Link != "" {
				fmt.Fprintf(&b, " (%s)", e.Link)
			}
			b.WriteString("\n")
		}
	}
	if len(r.NextSteps) > 0 {
		b.WriteString("\n*Next steps*\n")
		for i, s := range r.NextSteps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, s)
		}
	}
	return b.String()
}
