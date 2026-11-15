package llmproxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestProxyOpenAIUpstreamEndToEnd(t *testing.T) {
	t.Parallel()
	var (
		mu       sync.Mutex
		gotPath  string
		gotAuth  string
		gotModel string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		gotPath, gotAuth, gotModel = r.URL.Path, r.Header.Get("Authorization"), req.Model
		mu.Unlock()
		_, _ = io.WriteString(w, `{"id":"c1","choices":[{"finish_reason":"stop","message":{"role":"assistant",
		 "content":"<tool_call>{\"name\":\"pods_get\",\"arguments\":{\"name\":\"api\"}}</tool_call>"}}],
		 "usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	t.Cleanup(upstream.Close)
	u, _ := url.Parse(upstream.URL)
	p := New(Policy{MaxTokens: 8192, MaxBodyBytes: 1 << 20}, allowLocal{}, u, "", http.DefaultTransport, 2,
		slog.New(slog.NewTextHandler(io.Discard, nil))).WithOpenAIUpstream(map[string]string{"local-qwen": "mlx-community/Qwen"})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"local-qwen","max_tokens":100,"messages":[{"role":"user","content":"alert"}],
		  "tools":[{"name":"pods_get","input_schema":{"type":"object"}}]}`))
	req.Header.Set("Authorization", "Bearer agent-identity-token")
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var r anthropicReply
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.StopReason != "tool_use" || r.Content[0].Name != "pods_get" || r.Model != "local-qwen" {
		t.Errorf("reply = %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/chat/completions" || gotModel != "mlx-community/Qwen" {
		t.Errorf("upstream got path %q model %q", gotPath, gotModel)
	}
	if gotAuth != "" {
		t.Errorf("agent identity token leaked upstream: %q", gotAuth)
	}
}

type allowLocal struct{}

func (allowLocal) Authorize(_ context.Context, _ *http.Request, _ string) (string, error) {
	return "agent-sandboxes/triage-abc", nil
}

func TestToOpenAIMapsConversation(t *testing.T) {
	t.Parallel()
	in := `{"model":"local-qwen","max_tokens":512,
	 "system":[{"type":"text","text":"You triage."}],
	 "messages":[
	  {"role":"user","content":[{"type":"text","text":"alert"}]},
	  {"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"t1","name":"pods_get","input":{"name":"api"}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"OOMKilled","is_error":false},
	                            {"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"denied"}],"is_error":true}]}],
	 "tools":[{"name":"pods_get","description":"get a pod","input_schema":{"type":"object"}}]}`

	raw, names, err := toOpenAI([]byte(in), "mlx-community/Qwen")
	if err != nil {
		t.Fatal(err)
	}
	var out openAIRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Model != "mlx-community/Qwen" || out.MaxTokens != 512 || len(names) != 1 || names[0] != "pods_get" {
		t.Fatalf("model/max_tokens/tools = %q %d %v", out.Model, out.MaxTokens, names)
	}
	roles := []string{}
	for _, m := range out.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,tool" {
		t.Fatalf("roles = %v", roles)
	}
	asst := out.Messages[2]
	if asst.Content != "checking" || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Function.Arguments != `{"name":"api"}` {
		t.Errorf("assistant message = %+v", asst)
	}
	if out.Messages[3].ToolCallID != "t1" || out.Messages[3].Content != "OOMKilled" || out.Messages[4].Content != "ERROR: denied" {
		t.Errorf("tool messages = %+v", out.Messages[3:])
	}
	if out.Tools[0].Function.Name != "pods_get" || string(out.Tools[0].Function.Parameters) != `{"type":"object"}` {
		t.Errorf("tools = %+v", out.Tools)
	}
}

type anthropicReply struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Model      string           `json:"model"`
	Usage      struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func reply(t *testing.T, body string, tools ...string) anthropicReply {
	t.Helper()
	raw, err := fromOpenAI([]byte(body), "local-qwen", tools)
	if err != nil {
		t.Fatal(err)
	}
	var r anthropicReply
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFromOpenAIToolCallInText(t *testing.T) {
	t.Parallel()
	// Exactly what mlx_lm.server returned for Qwen2.5-Coder-7B with a tool offered.
	body := `{"id":"chatcmpl-1","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant",
	 "content":"` + "```json\\n{\\n  \\\"name\\\": \\\"pods_list_in_namespace\\\",\\n  \\\"arguments\\\": {\\n    \\\"namespace\\\": \\\"workloads\\\"\\n  }\\n}\\n```" + `"}}],
	 "usage":{"prompt_tokens":179,"completion_tokens":33}}`
	r := reply(t, body, "pods_list_in_namespace")
	if r.StopReason != "tool_use" || len(r.Content) != 1 {
		t.Fatalf("reply = %+v", r)
	}
	c := r.Content[0]
	if c.Type != "tool_use" || c.Name != "pods_list_in_namespace" || c.ID == "" {
		t.Errorf("tool_use block = %+v", c)
	}
	var args map[string]string
	if err := json.Unmarshal(c.Input, &args); err != nil || args["namespace"] != "workloads" {
		t.Errorf("input = %s", c.Input)
	}
	if r.Model != "local-qwen" || r.Usage.InputTokens != 179 || r.Usage.OutputTokens != 33 {
		t.Errorf("model/usage = %s %+v", r.Model, r.Usage)
	}
}

func TestFromOpenAIVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, content, toolCalls string
		wantStop                 string
		wantTools                []string
		wantText                 string
	}{
		{name: "structured tool_calls", toolCalls: `[{"id":"c1","type":"function","function":{"name":"pods_get","arguments":"{\"name\":\"api\"}"}}]`,
			wantStop: "tool_use", wantTools: []string{"pods_get"}},
		{name: "qwen tool_call tag with text", content: "Let me check.\n<tool_call>{\"name\":\"events_list\",\"arguments\":{\"namespace\":\"w\"}}</tool_call>",
			wantStop: "tool_use", wantTools: []string{"events_list"}, wantText: "Let me check."},
		{name: "bare json object", content: `{"name":"pods_get","arguments":"{\"name\":\"api\"}"}`,
			wantStop: "tool_use", wantTools: []string{"pods_get"}},
		{name: "json naming an unoffered tool stays text", content: `{"name":"delete_cluster","arguments":{}}`,
			wantStop: "end_turn", wantText: `{"name":"delete_cluster","arguments":{}}`},
		{name: "plain answer", content: "It is OOMKilled.", wantStop: "end_turn", wantText: "It is OOMKilled."},
		{name: "openai-style function object", content: `{"type":"function","function":{"name":"events_list","arguments":{"namespace":"w"}}}`,
			wantStop: "tool_use", wantTools: []string{"events_list"}},
		{name: "array of calls", content: `[{"name":"pods_get","arguments":{"name":"a"}},{"name":"events_list","arguments":{}}]`,
			wantStop: "tool_use", wantTools: []string{"pods_get", "events_list"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := map[string]any{"role": "assistant", "content": tc.content}
			if tc.toolCalls != "" {
				msg["tool_calls"] = json.RawMessage(tc.toolCalls)
			}
			body, _ := json.Marshal(map[string]any{"id": "x", "choices": []any{map[string]any{"finish_reason": "stop", "message": msg}}})
			r := reply(t, string(body), "pods_get", "events_list")
			if r.StopReason != tc.wantStop {
				t.Errorf("stop_reason = %q, want %q", r.StopReason, tc.wantStop)
			}
			var tools []string
			var text string
			for _, b := range r.Content {
				switch b.Type {
				case "tool_use":
					tools = append(tools, b.Name)
					if !json.Valid(b.Input) {
						t.Errorf("invalid input JSON %s", b.Input)
					}
				case "text":
					text = b.Text
				}
			}
			if strings.Join(tools, ",") != strings.Join(tc.wantTools, ",") || text != tc.wantText {
				t.Errorf("tools %v text %q; want %v %q", tools, text, tc.wantTools, tc.wantText)
			}
		})
	}
}
