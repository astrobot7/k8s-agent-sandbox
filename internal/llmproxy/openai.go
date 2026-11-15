package llmproxy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// This file translates between the Anthropic Messages API (what agents speak)
// and the OpenAI chat-completions API (what local servers such as
// mlx_lm.server speak). Small local models often write tool calls as JSON in
// the reply text instead of structured tool_calls, so replies are also
// scanned for such calls.

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int64              `json:"max_tokens"`
	System    json.RawMessage    `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []block
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // tool_result: string or []block
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type openAIRequest struct {
	Model     string          `json:"model"`
	MaxTokens int64           `json:"max_tokens,omitempty"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		FinishReason string        `json:"finish_reason"`
		Message      openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// toOpenAI converts an Anthropic Messages request. upstreamModel replaces the
// model name; it also returns the offered tool names for parsing the reply.
func toOpenAI(body []byte, upstreamModel string) ([]byte, []string, error) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("decode messages request: %w", err)
	}
	out := openAIRequest{Model: upstreamModel, MaxTokens: req.MaxTokens}
	if sys := textOf(req.System); sys != "" {
		out.Messages = append(out.Messages, openAIMessage{Role: "system", Content: sys})
	}
	for _, m := range req.Messages {
		blocks, err := blocksOf(m.Content)
		if err != nil {
			return nil, nil, err
		}
		var text []string
		var calls []openAIToolCall
		for _, b := range blocks {
			switch b.Type {
			case "text":
				text = append(text, b.Text)
			case "tool_use":
				var c openAIToolCall
				c.ID, c.Type = b.ID, "function"
				c.Function.Name, c.Function.Arguments = b.Name, string(b.Input)
				calls = append(calls, c)
			case "tool_result":
				content := textOf(b.Content)
				if b.IsError {
					content = "ERROR: " + content
				}
				out.Messages = append(out.Messages, openAIMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: content})
			}
		}
		if len(text) > 0 || len(calls) > 0 {
			out.Messages = append(out.Messages, openAIMessage{Role: m.Role, Content: strings.Join(text, "\n"), ToolCalls: calls})
		}
	}
	var names []string
	for _, t := range req.Tools {
		var ot openAITool
		ot.Type = "function"
		ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.Name, t.Description, t.InputSchema
		out.Tools = append(out.Tools, ot)
		names = append(names, t.Name)
	}
	raw, err := json.Marshal(out)
	return raw, names, err
}

// fromOpenAI converts a chat-completions response into an Anthropic Messages
// response. Tool calls come from tool_calls, or failing that from JSON objects
// {"name": <offered tool>, "arguments": {...}} written in the reply text.
func fromOpenAI(body []byte, model string, toolNames []string) ([]byte, error) {
	var resp openAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode chat completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("chat completion has no choices")
	}
	msg := resp.Choices[0].Message
	var content []anthropicBlock
	calls := msg.ToolCalls
	text := msg.Content
	if len(calls) == 0 {
		calls, text = toolCallsInText(text, toolNames)
	}
	if t := strings.TrimSpace(text); t != "" {
		content = append(content, anthropicBlock{Type: "text", Text: t})
	}
	for i, c := range calls {
		input := json.RawMessage(c.Function.Arguments)
		if !json.Valid(input) || len(input) == 0 {
			input = json.RawMessage("{}")
		}
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("toolu_local_%d", i)
		}
		content = append(content, anthropicBlock{Type: "tool_use", ID: id, Name: c.Function.Name, Input: input})
	}
	stop := "end_turn"
	if len(calls) > 0 {
		stop = "tool_use"
	} else if resp.Choices[0].FinishReason == "length" {
		stop = "max_tokens"
	}
	if content == nil {
		content = []anthropicBlock{{Type: "text", Text: ""}}
	}
	return json.Marshal(map[string]any{
		"id": "msg_" + resp.ID, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]int64{"input_tokens": resp.Usage.PromptTokens, "output_tokens": resp.Usage.CompletionTokens},
	})
}

// Candidate JSON spans: fenced code blocks, Qwen-style <tool_call> tags, then
// any top-level {...} in the text.
var (
	fenceRE   = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
	toolTagRE = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)
)

// toolCallsInText finds tool calls written as JSON in text and returns them
// with the text that remains after removing them.
func toolCallsInText(text string, toolNames []string) ([]openAIToolCall, string) {
	var calls []openAIToolCall
	rest := text
	take := func(span, obj string) {
		var v struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Params    json.RawMessage `json:"parameters"`
			Function  *struct {       // OpenAI-style {"function": {"name", "arguments"}}
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		}
		if json.Unmarshal([]byte(obj), &v) != nil {
			return
		}
		if v.Function != nil && v.Name == "" {
			v.Name, v.Arguments = v.Function.Name, v.Function.Arguments
		}
		if !slices.Contains(toolNames, v.Name) {
			return
		}
		args := v.Arguments
		if len(args) == 0 {
			args = v.Params
		}
		// Some models encode arguments as a JSON string.
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = json.RawMessage(s)
		}
		var c openAIToolCall
		c.Type = "function"
		c.Function.Name, c.Function.Arguments = v.Name, string(args)
		calls = append(calls, c)
		rest = strings.Replace(rest, span, "", 1)
	}
	for _, re := range []*regexp.Regexp{toolTagRE, fenceRE} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			take(m[0], m[1])
		}
	}
	if len(calls) == 0 {
		for _, obj := range topLevelObjects(text) {
			take(obj, obj)
		}
	}
	// Calls written as a JSON array leave only "[,]" behind.
	if len(calls) > 0 && strings.Trim(rest, " \t\r\n[],") == "" {
		rest = ""
	}
	return calls, rest
}

// topLevelObjects returns balanced {...} spans, ignoring braces inside strings.
func topLevelObjects(s string) []string {
	var out []string
	depth, start, inStr, esc := 0, -1, false, false
	for i, r := range s {
		switch {
		case esc:
			esc = false
		case inStr && r == '\\':
			esc = true
		case r == '"' && depth > 0:
			inStr = !inStr
		case inStr:
		case r == '{':
			if depth == 0 {
				start = i
			}
			depth++
		case r == '}' && depth > 0:
			depth--
			if depth == 0 {
				out = append(out, s[start:i+1])
			}
		}
	}
	return out
}

func blocksOf(raw json.RawMessage) ([]anthropicBlock, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []anthropicBlock{{Type: "text", Text: s}}, nil
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("decode content blocks: %w", err)
	}
	return blocks, nil
}

// textOf flattens a string or a list of text blocks.
func textOf(raw json.RawMessage) string {
	blocks, err := blocksOf(raw)
	if err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
