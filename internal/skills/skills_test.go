package skills

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func skill(name string, match map[string][]string) Skill {
	return Skill{Name: name, Description: "covers " + name, Match: match, Instructions: "do the " + name + " steps"}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		s       Skill
		wantErr string
	}{
		{"valid", skill("crashloop", nil), ""},
		{"dot in name", skill("crash.loop", nil), "name"},
		{"upper case name", skill("CrashLoop", nil), "name"},
		{"empty description", Skill{Name: "x", Instructions: "y"}, "description"},
		{"empty instructions", Skill{Name: "x", Description: "y"}, "instructions"},
		{"oversized instructions", Skill{Name: "x", Description: "y", Instructions: strings.Repeat("a", MaxInstructions+1)}, "limit"},
		{"empty match values", skill("x", map[string][]string{"alertname": {}}), "no values"},
		{"bad glob", skill("x", map[string][]string{"alertname": {"[a"}}), "bad pattern"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.s.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want ErrInvalid mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestMatches(t *testing.T) {
	t.Parallel()
	s := skill("x", map[string][]string{"alertname": {"K8s warning events"}, "k8s.object.kind": {"Pod", "Deploy*"}})
	tests := []struct {
		labels map[string]string
		want   bool
	}{
		{map[string]string{"alertname": "K8s warning events", "k8s.object.kind": "Pod"}, true},
		{map[string]string{"alertname": "K8s warning events", "k8s.object.kind": "Deployment"}, true},
		{map[string]string{"alertname": "K8s warning events", "k8s.object.kind": "Node"}, false},
		{map[string]string{"alertname": "K8s warning events"}, false},
	}
	for _, tc := range tests {
		if got := s.Matches(tc.labels); got != tc.want {
			t.Errorf("Matches(%v) = %v, want %v", tc.labels, got, tc.want)
		}
	}
	if skill("y", nil).Matches(map[string]string{"alertname": "anything"}) {
		t.Error("a skill without match must never match")
	}
}

func TestNewRejectsDuplicates(t *testing.T) {
	t.Parallel()
	if _, err := New(skill("a", nil), skill("a", nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("New with duplicate names = %v", err)
	}
}

func TestPromptSection(t *testing.T) {
	t.Parallel()
	lib, err := New(skill("zeta", nil), skill("alpha", map[string][]string{"alertname": {"A"}}))
	if err != nil {
		t.Fatal(err)
	}
	got := lib.PromptSection(map[string]string{"alertname": "A"})
	for _, want := range []string{"- alpha: covers alpha", "- zeta: covers zeta", "### Skill alpha (matches this alert)", "do the alpha steps", "read_skill"} {
		if !strings.Contains(got, want) {
			t.Errorf("PromptSection missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "do the zeta steps") {
		t.Error("an unmatched skill's instructions were inlined")
	}
	if strings.Index(got, "- alpha") > strings.Index(got, "- zeta") {
		t.Error("index is not sorted by name")
	}
	var empty *Library
	if empty.PromptSection(nil) != "" || empty.Len() != 0 {
		t.Error("nil library should produce no prompt section")
	}
}

// triageSkill mirrors the TriageSkill custom resource (deploy/crds/triageskill.yaml).
type triageSkill struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Description  string              `json:"description"`
		Match        map[string][]string `json:"match"`
		Instructions string              `json:"instructions"`
	} `json:"spec"`
}

// TestRepoSkills checks every skill shipped in skills/: valid, unique, and
// every skill another skill tells the agent to read actually exists.
func TestRepoSkills(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("../../skills/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no skills found: %v", err)
	}
	var all []Skill
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var cr triageSkill
		if err := yaml.UnmarshalStrict(data, &cr); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if cr.APIVersion != "triage.k8s-agent-sandbox.io/v1alpha1" || cr.Kind != "TriageSkill" {
			t.Errorf("%s: apiVersion/kind = %s/%s", f, cr.APIVersion, cr.Kind)
		}
		if base := strings.TrimSuffix(filepath.Base(f), ".yaml"); base != cr.Metadata.Name {
			t.Errorf("%s: file name should match metadata.name %q", f, cr.Metadata.Name)
		}
		all = append(all, Skill{Name: cr.Metadata.Name, Description: cr.Spec.Description,
			Match: cr.Spec.Match, Instructions: cr.Spec.Instructions})
	}
	lib, err := New(all...)
	if err != nil {
		t.Fatalf("shipped skills are invalid: %v", err)
	}
	refRE := regexp.MustCompile("read (?:the )?`([a-z0-9-]+)`")
	for _, s := range all {
		for _, m := range refRE.FindAllStringSubmatch(s.Instructions, -1) {
			if _, ok := lib.Get(m[1]); !ok {
				t.Errorf("skill %s refers to missing skill %q", s.Name, m[1])
			}
		}
	}
}
