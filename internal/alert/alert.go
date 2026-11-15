// Package alert parses the Alertmanager-format webhooks that SigNoz sends to
// webhook notification channels.
package alert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// StatusFiring is the status of an alert that is currently firing.
const StatusFiring = "firing"

// Webhook is the body of an Alertmanager webhook notification.
type Webhook struct {
	Version     string  `json:"version"`
	GroupKey    string  `json:"groupKey"`
	Status      string  `json:"status"`
	Receiver    string  `json:"receiver"`
	ExternalURL string  `json:"externalURL"`
	Alerts      []Alert `json:"alerts"`
}

// Alert is one alert inside a webhook notification.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Parse decodes a webhook body. Callers should limit the reader's size.
func Parse(r io.Reader) (Webhook, error) {
	var w Webhook
	if err := json.NewDecoder(r).Decode(&w); err != nil {
		return Webhook{}, fmt.Errorf("decode alert webhook: %w", err)
	}
	return w, nil
}

// Firing returns the alerts in the webhook that are firing.
func (w Webhook) Firing() []Alert {
	var out []Alert
	for _, a := range w.Alerts {
		if a.Status == StatusFiring {
			out = append(out, a)
		}
	}
	return out
}

// Name returns the alert rule name.
func (a Alert) Name() string { return a.Labels["alertname"] }

// Key identifies the alert across notifications: the Alertmanager fingerprint
// when present, otherwise a hash of the sorted labels.
func (a Alert) Key() string {
	if a.Fingerprint != "" {
		return a.Fingerprint
	}
	keys := make([]string, 0, len(a.Labels))
	for k := range a.Labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\x00", k, a.Labels[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
