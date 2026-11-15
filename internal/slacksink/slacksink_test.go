package slacksink

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestReceiveLikeSlack(t *testing.T) {
	t.Parallel()
	s := New(10)
	h := s.Handler()
	if rec := post(t, h, "/services/T0/B0/X", `{"text":"report 1"}`); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("valid post: %d %q", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{}`, `{"text":"  "}`, `not json`} {
		if rec := post(t, h, "/services/T0/B0/X", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("payload %q: status %d, want 400", bad, rec.Code)
		}
	}
	if got := s.Messages(); len(got) != 1 || got[0].Text != "report 1" || got[0].Channel != "/services/T0/B0/X" {
		t.Errorf("messages = %+v", got)
	}
}

func TestNewestFirstAndCapped(t *testing.T) {
	t.Parallel()
	s := New(2)
	h := s.Handler()
	for _, txt := range []string{"a", "b", "c"} {
		post(t, h, "/services/x", `{"text":"`+txt+`"}`)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/messages", nil))
	var got []Message
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "c" || got[1].Text != "b" {
		t.Errorf("messages = %+v, want c then b (a dropped)", got)
	}
}

func TestPageEscapesReportText(t *testing.T) {
	t.Parallel()
	s := New(10)
	h := s.Handler()
	post(t, h, "/services/x", `{"text":"<script>alert(1)</script> *Likely cause*"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("page renders report text as HTML")
	}
	if !strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, "*Likely cause*") {
		t.Errorf("page missing escaped message:\n%s", body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
}
