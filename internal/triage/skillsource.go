package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"k8s-agent-sandbox/internal/skills"
)

// TriageSkillGVR is the TriageSkill custom resource (deploy/crds/triageskill.yaml).
var TriageSkillGVR = schema.GroupVersionResource{Group: "triage.k8s-agent-sandbox.io", Version: "v1alpha1", Resource: "triageskills"}

// SkillSource supplies the skills handed to each agent.
type SkillSource interface {
	Skills(ctx context.Context) ([]skills.Skill, error)
}

// CRDSkills reads TriageSkill objects from the cluster on every call, so a
// new or edited skill applies from the next run.
type CRDSkills struct {
	Client dynamic.Interface
	Log    *slog.Logger
}

// Skills implements SkillSource. Invalid objects are logged and skipped so
// one bad skill does not take the others down.
func (c CRDSkills) Skills(ctx context.Context) ([]skills.Skill, error) {
	list, err := c.Client.Resource(TriageSkillGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list triageskills: %w", err)
	}
	var out []skills.Skill
	for _, item := range list.Items {
		s, err := skillFromObject(item)
		if err == nil {
			err = s.Validate()
		}
		if err != nil {
			c.Log.Warn("skipping invalid TriageSkill", "name", item.GetName(), "err", err)
			continue
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b skills.Skill) int { return strings.Compare(a.Name, b.Name) })
	if len(out) > skills.MaxSkills {
		c.Log.Warn("too many TriageSkills; ignoring the rest", "count", len(out), "limit", skills.MaxSkills)
		out = out[:skills.MaxSkills]
	}
	return out, nil
}

func skillFromObject(u unstructured.Unstructured) (skills.Skill, error) {
	spec, ok := u.Object["spec"]
	if !ok {
		return skills.Skill{}, fmt.Errorf("%w: no spec", skills.ErrInvalid)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return skills.Skill{}, fmt.Errorf("encode spec: %w", err)
	}
	var s skills.Skill
	if err := json.Unmarshal(raw, &s); err != nil {
		return skills.Skill{}, fmt.Errorf("%w: spec: %w", skills.ErrInvalid, err)
	}
	s.Name = u.GetName()
	return s, nil
}
