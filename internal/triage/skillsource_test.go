package triage

import (
	"io"
	"log/slog"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func triageSkillObject(name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "triage.k8s-agent-sandbox.io/v1alpha1",
		"kind":       "TriageSkill",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
}

func TestCRDSkillsListsValidSkillsAndSkipsInvalid(t *testing.T) {
	t.Parallel()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{TriageSkillGVR: "TriageSkillList"},
		triageSkillObject("oomkilled", map[string]any{
			"description":  "OOM",
			"instructions": "check limits",
		}),
		triageSkillObject("crashloop", map[string]any{
			"description":  "crashes",
			"match":        map[string]any{"alertname": []any{"K8s warning events"}},
			"instructions": "read previous logs",
		}),
		triageSkillObject("broken", map[string]any{"description": "no instructions"}),
	)
	src := CRDSkills{Client: client, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	got, err := src.Skills(t.Context())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(got) != 2 || got[0].Name != "crashloop" || got[1].Name != "oomkilled" {
		t.Fatalf("skills = %+v, want crashloop and oomkilled sorted, broken skipped", got)
	}
	if got[0].Match["alertname"][0] != "K8s warning events" || got[0].Instructions != "read previous logs" {
		t.Errorf("crashloop skill = %+v", got[0])
	}
}
