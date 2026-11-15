package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s-agent-sandbox/internal/alert"
)

// Labels set on the resources of one incident.
const (
	// FingerprintLabel holds the alert key, so a repeat notification finds its incident.
	FingerprintLabel = "triage.k8s-agent-sandbox.io/fingerprint"
	// IncidentLabel names the incident a step resource belongs to.
	IncidentLabel = "triage.k8s-agent-sandbox.io/incident"
	// StepLabel is the step ("triage", later "remediate") a resource serves.
	StepLabel = "triage.k8s-agent-sandbox.io/step"
)

// Alert is the SigNoz alert that started an incident.
type Alert struct {
	// Fingerprint identifies the alert across notifications.
	Fingerprint string `json:"fingerprint"`
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +optional
	StartsAt metav1.Time `json:"startsAt,omitzero"`
	// +optional
	GeneratorURL string `json:"generatorURL,omitempty"`
}

// AlertFrom converts a webhook alert. The fingerprint is a.Key(), which falls
// back to a label hash when SigNoz sends none.
func AlertFrom(a alert.Alert) Alert {
	return Alert{
		Fingerprint:  a.Key(),
		Labels:       a.Labels,
		Annotations:  a.Annotations,
		StartsAt:     metav1.NewTime(a.StartsAt),
		GeneratorURL: a.GeneratorURL,
	}
}

// Webhook converts back to the webhook form the agent and notifiers use.
func (a Alert) Webhook() alert.Alert {
	return alert.Alert{
		Status:       alert.StatusFiring,
		Labels:       a.Labels,
		Annotations:  a.Annotations,
		StartsAt:     a.StartsAt.Time,
		GeneratorURL: a.GeneratorURL,
		Fingerprint:  a.Fingerprint,
	}
}
