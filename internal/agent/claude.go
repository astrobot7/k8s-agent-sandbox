package agent

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Claude implements Model with the Anthropic Messages API. It keeps the
// conversation history; use one Claude per run.
type Claude struct {
	client    anthropic.Client
	model     string
	maxTokens int64
	system    string
	tools     []anthropic.ToolUnionParam
	messages  []anthropic.MessageParam
}

// NewClaude returns a Model for the given Anthropic model ID.
func NewClaude(apiKey, model string, opts ...option.RequestOption) *Claude {
	opts = append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	return &Claude{client: anthropic.NewClient(opts...), model: model, maxTokens: 4096}
}

// Start implements Model.
func (c *Claude) Start(ctx context.Context, system, prompt string, tools []Tool) (Reply, error) {
	c.system = system
	c.tools = make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		c.tools = append(c.tools, toolParam(t))
	}
	c.messages = []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))}
	return c.send(ctx)
}

// Continue implements Model.
func (c *Claude) Continue(ctx context.Context, results []ToolResult, note string) (Reply, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(results)+1)
	for _, r := range results {
		blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
	}
	if note != "" {
		blocks = append(blocks, anthropic.NewTextBlock(note))
	}
	if len(blocks) == 0 {
		blocks = append(blocks, anthropic.NewTextBlock("Continue."))
	}
	c.messages = append(c.messages, anthropic.NewUserMessage(blocks...))
	return c.send(ctx)
}

func (c *Claude) send(ctx context.Context) (Reply, error) {
	msg, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: c.maxTokens,
		System:    []anthropic.TextBlockParam{{Text: c.system}},
		Messages:  c.messages,
		Tools:     c.tools,
	})
	if err != nil {
		return Reply{}, fmt.Errorf("anthropic messages: %w", err)
	}
	c.messages = append(c.messages, msg.ToParam())

	var reply Reply
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			reply.Text += b.Text
		case anthropic.ToolUseBlock:
			reply.Calls = append(reply.Calls, ToolCall{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	return reply, nil
}

func toolParam(t Tool) anthropic.ToolUnionParam {
	schema := anthropic.ToolInputSchemaParam{Properties: t.InputSchema["properties"]}
	if req, ok := t.InputSchema["required"]; ok {
		schema.Required = toStrings(req)
	}
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:        t.Name,
		Description: anthropic.String(t.Description),
		InputSchema: schema,
	}}
}

func toStrings(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
