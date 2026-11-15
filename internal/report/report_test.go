package report

import (
	"errors"
	"strings"
	"testing"
)

func valid() Report {
	return Report{
		Summary:     "checkout-api is crashlooping",
		LikelyCause: "OOMKilled after the memory limit was lowered to 64Mi",
		Confidence:  ConfidenceHigh,
		Evidence:    []Evidence{{Source: "pods_get workloads/api", Detail: "lastState.terminated.reason=OOMKilled", Link: "http://localhost:8080/logs"}},
		NextSteps:   []string{"Raise the memory limit back to 256Mi"},
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*Report)
		wantErr string
	}{
		{name: "valid report passes", mutate: func(*Report) {}},
		{name: "missing summary", mutate: func(r *Report) { r.Summary = " " }, wantErr: "summary is required"},
		{name: "unknown confidence", mutate: func(r *Report) { r.Confidence = "certain" }, wantErr: "confidence must be"},
		{name: "no evidence", mutate: func(r *Report) { r.Evidence = nil }, wantErr: "at least one evidence"},
		{name: "evidence link must be http", mutate: func(r *Report) { r.Evidence[0].Link = "javascript:alert(1)" }, wantErr: "http(s) URL"},
		{name: "oversized summary", mutate: func(r *Report) { r.Summary = strings.Repeat("x", maxSummary+1) }, wantErr: "summary longer"},
		{name: "too many steps", mutate: func(r *Report) { r.NextSteps = make([]string, maxSteps+1) }, wantErr: "more than"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := valid()
			tc.mutate(&r)
			err := r.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want ErrInvalid containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestMarkdownIncludesKeyParts(t *testing.T) {
	t.Parallel()
	md := valid().Markdown("K8sWarningEvents workloads/api")
	for _, want := range []string{"K8sWarningEvents", "OOMKilled", "high confidence", "1. Raise"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown() missing %q:\n%s", want, md)
		}
	}
}
