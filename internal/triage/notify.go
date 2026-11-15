package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Title is the one-line heading used for a run in notifications.
func Title(run Run) string {
	a := run.Alert
	target := a.Labels["k8s.namespace.name"]
	if obj := a.Labels["k8s.object.name"]; obj != "" {
		target += "/" + obj
	}
	return fmt.Sprintf("Triage %s: %s %s", run.ID, a.Name(), target)
}

// LogNotifier writes finished runs to the structured log.
type LogNotifier struct{ Log *slog.Logger }

// Notify implements Notifier.
func (n LogNotifier) Notify(_ context.Context, run Run) error {
	if run.Report == nil {
		n.Log.Warn("triage failed", "run", run.ID, "title", Title(run), "error", run.Error)
		return nil
	}
	n.Log.Info("triage report", "run", run.ID, "title", Title(run), "report", run.Report)
	return nil
}

// SlackNotifier posts finished runs to a Slack incoming webhook.
type SlackNotifier struct {
	WebhookURL string
	Client     *http.Client
}

// Notify implements Notifier.
func (n SlackNotifier) Notify(ctx context.Context, run Run) error {
	text := Title(run) + "\nTriage failed: " + run.Error
	if run.Report != nil {
		text = run.Report.Markdown(Title(run))
	}
	return n.Post(ctx, text)
}

// Post sends one message.
func (n SlackNotifier) Post(ctx context.Context, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("encode slack message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.Client.Do(req)
	if err != nil {
		return fmt.Errorf("post to slack: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("post to slack: status %s", resp.Status)
	}
	return nil
}

// MultiNotifier calls every notifier and joins their errors.
type MultiNotifier []Notifier

// Notify implements Notifier.
func (m MultiNotifier) Notify(ctx context.Context, run Run) error {
	var errs []error
	for _, n := range m {
		errs = append(errs, n.Notify(ctx, run))
	}
	return errors.Join(errs...)
}
