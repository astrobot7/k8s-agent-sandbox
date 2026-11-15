// Package slacksink is a stand-in for Slack incoming webhooks, for local
// testing. It accepts the same POSTs Slack does ({"text": "..."} to
// /services/...), keeps the most recent messages in memory, and shows them on
// a small web page.
package slacksink

import (
	"encoding/json"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Message is one received webhook post.
type Message struct {
	Time    time.Time `json:"time"`
	Channel string    `json:"channel"` // the webhook path, e.g. /services/T000/B000/XXXX
	Text    string    `json:"text"`
}

// Sink stores messages and serves them.
type Sink struct {
	max int
	now func() time.Time

	mu       sync.Mutex
	messages []Message // oldest first
}

// New returns a Sink that keeps the last max messages.
func New(max int) *Sink { return &Sink{max: max, now: time.Now} }

// Handler returns the routes: POST /services/... (Slack-compatible),
// GET /api/messages (JSON, newest first), GET / (web page), GET /healthz.
func (s *Sink) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /services/", s.receive)
	mux.HandleFunc("GET /api/messages", s.list)
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func (s *Sink) receive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		// Slack answers invalid payloads with 400 "invalid_payload" / "no_text".
		http.Error(w, "no_text", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.messages = append(s.messages, Message{Time: s.now(), Channel: r.URL.Path, Text: body.Text})
	if len(s.messages) > s.max {
		s.messages = slices.Delete(s.messages, 0, len(s.messages)-s.max)
	}
	s.mu.Unlock()
	_, _ = w.Write([]byte("ok")) // Slack's success body
}

// Messages returns a copy, newest first.
func (s *Sink) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.messages)
	slices.Reverse(out)
	return out
}

func (s *Sink) list(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Messages())
}

func (s *Sink) page(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Report text comes from the model; html/template escapes it.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_ = pageTmpl.Execute(w, s.Messages())
}

var pageTmpl = template.Must(template.New("page").Funcs(template.FuncMap{
	"ts": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5">
<title>Triage messages</title>
<style>
 :root { color-scheme: light dark; --bg:#f6f6f4; --card:#fff; --fg:#1d1d1f; --muted:#6b6b70; --line:#e2e2df; }
 @media (prefers-color-scheme: dark) { :root { --bg:#16171a; --card:#1f2024; --fg:#ececee; --muted:#9a9aa1; --line:#2e2f34; } }
 body { margin:0; background:var(--bg); color:var(--fg); font:15px/1.5 -apple-system, system-ui, sans-serif; }
 main { max-width:860px; margin:0 auto; padding:24px 16px; }
 h1 { font-size:20px; margin:0 0 4px; } p.sub { color:var(--muted); margin:0 0 20px; font-size:13px; }
 article { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:14px 16px; margin-bottom:12px; }
 .meta { color:var(--muted); font-size:12px; margin-bottom:6px; }
 pre { margin:0; white-space:pre-wrap; word-break:break-word; font:13px/1.5 ui-monospace, Menlo, monospace; }
 .empty { color:var(--muted); }
</style></head>
<body><main>
<h1>Triage messages</h1>
<p class="sub">Slack stand-in &middot; newest first &middot; refreshes every 5 seconds</p>
{{range .}}<article><div class="meta">{{ts .Time}} &middot; {{.Channel}}</div><pre>{{.Text}}</pre></article>
{{else}}<p class="empty">No messages yet. Triage reports appear here when a run finishes.</p>{{end}}
</main></body></html>`))
