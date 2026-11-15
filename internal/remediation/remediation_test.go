package remediation

import (
	"errors"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func crashloopSkill() Skill {
	return Skill{
		Name:         "crashloop-restart",
		Description:  "restart a crashlooping workload",
		Instructions: "restart it",
		AllowedActions: []AllowedAction{
			{Type: ActionRolloutRestart, Constraints: Constraints{Namespaces: []string{"workloads", "shop-*"}}},
			{Type: ActionScale, Constraints: Constraints{Namespaces: []string{"workloads"}, MaxReplicas: 5}},
		},
	}
}

var alertTarget = Target{Namespace: "workloads", Kind: "Deployment", Name: "payments-api"}

func TestCheckProposal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		p       Proposal
		wantErr error
		wantMsg string
	}{
		{"allowed rollout restart on the alert's target",
			Proposal{Action: ActionRolloutRestart, Target: alertTarget, Rationale: "transient"}, nil, ""},
		{"action the skill does not allow",
			Proposal{Action: ActionRestartPod, Target: alertTarget, Rationale: "x"}, ErrNotAllowed, "not allowed by skill"},
		{"target other than the alert's",
			Proposal{Action: ActionRolloutRestart, Target: Target{Namespace: "workloads", Kind: "Deployment", Name: "other"}, Rationale: "x"}, ErrNotAllowed, "alert's target"},
		{"scale within limit",
			Proposal{Action: ActionScale, Target: alertTarget, Params: Params{Replicas: ptr(int32(3))}, Rationale: "x"}, nil, ""},
		{"scale above limit",
			Proposal{Action: ActionScale, Target: alertTarget, Params: Params{Replicas: ptr(int32(9))}, Rationale: "x"}, ErrNotAllowed, "maxReplicas"},
		{"scale without replicas",
			Proposal{Action: ActionScale, Target: alertTarget, Rationale: "x"}, ErrInvalid, "replicas"},
		{"missing rationale",
			Proposal{Action: ActionRolloutRestart, Target: alertTarget}, ErrInvalid, "rationale"},
		{"unknown action",
			Proposal{Action: "delete-namespace", Target: alertTarget, Rationale: "x"}, ErrInvalid, "unknown action"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The alert's target is the only object this run may act on.
			err := crashloopSkill().Check(tc.p, alertTarget)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Check() = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Check() = %v, want %v mentioning %q", err, tc.wantErr, tc.wantMsg)
			}
		})
	}
}

func TestNamespaceOutsideSkillConstraints(t *testing.T) {
	t.Parallel()
	// The alert is about billing/payments-api, but the skill only covers workloads and shop-*.
	billing := Target{Namespace: "billing", Kind: "Deployment", Name: "payments-api"}
	err := crashloopSkill().Check(Proposal{Action: ActionRolloutRestart, Target: billing, Rationale: "x"}, billing)
	if !errors.Is(err, ErrNotAllowed) || !strings.Contains(err.Error(), "outside skill") {
		t.Fatalf("Check() = %v, want namespace-outside-skill denial", err)
	}
	shop := Target{Namespace: "shop-eu", Kind: "Deployment", Name: "cart"}
	if err := crashloopSkill().Check(Proposal{Action: ActionRolloutRestart, Target: shop, Rationale: "x"}, shop); err != nil {
		t.Errorf("glob namespace shop-* should allow shop-eu: %v", err)
	}
}

func TestProtectedNamespacesAlwaysDenied(t *testing.T) {
	t.Parallel()
	s := crashloopSkill()
	s.AllowedActions[0].Constraints.Namespaces = []string{"*"} // even a skill that allows everything
	for _, ns := range []string{"kube-system", "alert-triage", "agent-sandboxes", "llm-proxy", "egress", "mcp-gateway"} {
		target := Target{Namespace: ns, Kind: "Deployment", Name: "x"}
		err := s.Check(Proposal{Action: ActionRolloutRestart, Target: target, Rationale: "x"}, target)
		if !errors.Is(err, ErrNotAllowed) || !strings.Contains(err.Error(), "protected") {
			t.Errorf("namespace %s: Check() = %v, want protected-namespace denial", ns, err)
		}
	}
}

func TestApprovalDefaultsAndOverride(t *testing.T) {
	t.Parallel()
	s := crashloopSkill()
	tests := []struct {
		action   ActionType
		override *ApprovalMode
		want     ApprovalMode
	}{
		{ActionRestartPod, nil, ApprovalAuto},
		{ActionRolloutRestart, nil, ApprovalAuto},
		{ActionScale, nil, ApprovalRequired},
		{ActionOpenPR, nil, ApprovalRequired},
		{ActionRolloutRestart, ptr(ApprovalRequired), ApprovalRequired},
		{ActionScale, ptr(ApprovalAuto), ApprovalAuto},
	}
	for _, tc := range tests {
		s.Approval = tc.override
		if got := s.ApprovalFor(tc.action); got != tc.want {
			t.Errorf("ApprovalFor(%s) with override %v = %s, want %s", tc.action, tc.override, got, tc.want)
		}
	}
}

func TestSkillValidate(t *testing.T) {
	t.Parallel()
	ok := crashloopSkill()
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid skill: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Skill)
		want   string
	}{
		{"no actions", func(s *Skill) { s.AllowedActions = nil }, "allowedActions"},
		{"unknown action type", func(s *Skill) { s.AllowedActions[0].Type = "exec" }, "unknown action"},
		{"scale without maxReplicas", func(s *Skill) { s.AllowedActions[1].Constraints.MaxReplicas = 0 }, "maxReplicas"},
		{"no namespaces", func(s *Skill) { s.AllowedActions[0].Constraints.Namespaces = nil }, "namespaces"},
		{"bad approval", func(s *Skill) { s.Approval = ptr(ApprovalMode("sometimes")) }, "approval"},
		{"bad name", func(s *Skill) { s.Name = "Bad.Name" }, "name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := crashloopSkill()
			tc.mutate(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want ErrInvalid mentioning %q", err, tc.want)
			}
		})
	}
}
