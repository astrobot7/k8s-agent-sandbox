package alert

import (
	"strings"
	"testing"
)

const sample = `{
  "version": "4",
  "groupKey": "{}:{alertname=\"K8sWarningEvents\"}",
  "status": "firing",
  "receiver": "triage-webhook",
  "alerts": [
    {"status": "firing", "fingerprint": "abc123",
     "labels": {"alertname": "K8sWarningEvents", "k8s.namespace.name": "workloads", "k8s.object.name": "api-7d9f"},
     "annotations": {"summary": "BackOff"},
     "startsAt": "2026-11-15T03:55:25Z"},
    {"status": "resolved", "fingerprint": "def456", "labels": {"alertname": "K8sWarningEvents"}}
  ]
}`

func TestParseAndFiring(t *testing.T) {
	t.Parallel()
	w, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	firing := w.Firing()
	if len(firing) != 1 {
		t.Fatalf("Firing() returned %d alerts, want 1", len(firing))
	}
	a := firing[0]
	if a.Name() != "K8sWarningEvents" || a.Key() != "abc123" {
		t.Errorf("got name %q key %q", a.Name(), a.Key())
	}
	if a.StartsAt.IsZero() {
		t.Error("StartsAt not parsed")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := Parse(strings.NewReader("{not json")); err == nil {
		t.Fatal("Parse accepted invalid JSON")
	}
}

func TestKeyWithoutFingerprint(t *testing.T) {
	t.Parallel()
	a := Alert{Labels: map[string]string{"b": "2", "a": "1"}}
	b := Alert{Labels: map[string]string{"a": "1", "b": "2"}}
	c := Alert{Labels: map[string]string{"a": "1", "b": "3"}}

	if a.Key() != b.Key() {
		t.Errorf("same labels in different order gave different keys: %q vs %q", a.Key(), b.Key())
	}
	if a.Key() == c.Key() {
		t.Error("different labels gave the same key")
	}
}
