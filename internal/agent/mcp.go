package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPToolbox exposes the tools of several MCP servers as one Toolbox.
type MCPToolbox struct {
	sessions []*mcp.ClientSession
	byName   map[string]*mcp.ClientSession
	tools    []Tool
}

// ConnectMCP connects to each Streamable HTTP endpoint and collects its tools.
// Tool names must be unique across servers.
func ConnectMCP(ctx context.Context, httpClient *http.Client, endpoints []string) (*MCPToolbox, error) {
	tb := &MCPToolbox{byName: map[string]*mcp.ClientSession{}}
	client := mcp.NewClient(&mcp.Implementation{Name: "triage-agent", Version: "0.1.0"}, nil)
	for _, ep := range endpoints {
		cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ep, HTTPClient: httpClient}, nil)
		if err != nil {
			tb.Close()
			return nil, fmt.Errorf("connect MCP %s: %w", ep, err)
		}
		tb.sessions = append(tb.sessions, cs)
		if err := tb.addTools(ctx, ep, cs); err != nil {
			tb.Close()
			return nil, err
		}
	}
	return tb, nil
}

func (tb *MCPToolbox) addTools(ctx context.Context, ep string, cs *mcp.ClientSession) error {
	cursor := ""
	for {
		res, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("list tools at %s: %w", ep, err)
		}
		for _, t := range res.Tools {
			if _, dup := tb.byName[t.Name]; dup || t.Name == SubmitReportTool || t.Name == ReadSkillTool {
				return fmt.Errorf("tool name %q from %s collides with another tool", t.Name, ep)
			}
			schema, err := schemaMap(t.InputSchema)
			if err != nil {
				return fmt.Errorf("tool %q at %s: %w", t.Name, ep, err)
			}
			tb.byName[t.Name] = cs
			tb.tools = append(tb.tools, Tool{Name: t.Name, Description: t.Description, InputSchema: schema})
		}
		if res.NextCursor == "" {
			return nil
		}
		cursor = res.NextCursor
	}
}

// Tools implements Toolbox.
func (tb *MCPToolbox) Tools() []Tool { return tb.tools }

// Restrict keeps only the named tools, so small models get a shorter prompt.
// Names no server offers are ignored; an empty list keeps everything.
func (tb *MCPToolbox) Restrict(names []string) {
	if len(names) == 0 {
		return
	}
	keep := map[string]bool{}
	for _, n := range names {
		keep[strings.TrimSpace(n)] = true
	}
	tools := tb.tools[:0]
	for _, t := range tb.tools {
		if keep[t.Name] {
			tools = append(tools, t)
		} else {
			delete(tb.byName, t.Name)
		}
	}
	tb.tools = tools
}

// Call implements Toolbox.
func (tb *MCPToolbox) Call(ctx context.Context, name string, input json.RawMessage) (string, error) {
	cs, ok := tb.byName[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		return "", fmt.Errorf("call %s: %w", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
			b.WriteString("\n")
		} else {
			b.WriteString("[non-text content omitted]\n")
		}
	}
	if res.IsError {
		return "", errors.New(strings.TrimSpace(b.String()))
	}
	return b.String(), nil
}

// Close closes every session.
func (tb *MCPToolbox) Close() {
	for _, cs := range tb.sessions {
		_ = cs.Close()
	}
}

func schemaMap(s any) (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encode input schema: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode input schema: %w", err)
	}
	if m == nil {
		m = map[string]any{"type": "object"}
	}
	return m, nil
}
