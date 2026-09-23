package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools returns the currently connected MCP tools as executable agent tools.
// Names are namespaced so servers cannot collide with built-in tools or each
// other.
func (m *Manager) Tools() []agent.Tool {
	servers := m.snapshot()
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	used := map[string]bool{}
	var result []agent.Tool
	for _, serverName := range names {
		entry := servers[serverName]
		if entry.state != StateConnected || entry.session == nil {
			continue
		}
		for _, definition := range entry.tools {
			if definition == nil {
				continue
			}
			schema, err := json.Marshal(definition.InputSchema)
			if err != nil {
				continue
			}
			result = append(result, remoteTool{
				server: serverName, name: definition.Name, session: entry.session,
				definition: ai.Tool{
					Name:        uniqueToolName(used, serverName, definition.Name),
					Description: definition.Description,
					Parameters:  schema,
				},
			})
		}
	}
	return result
}

// uniqueToolName namespaces a tool so it cannot collide with a built-in or
// another server's tool. Two distinct MCP names can sanitise to the same string
// ("a b" and "a_b"), so a repeat is disambiguated with a short hash of the
// original pair instead of failing the whole tool list later.
func uniqueToolName(used map[string]bool, serverName, toolName string) string {
	candidate := "mcp__" + sanitize(serverName) + "__" + sanitize(toolName)
	if !used[candidate] {
		used[candidate] = true
		return candidate
	}
	sum := sha256.Sum256([]byte(serverName + "\x00" + toolName))
	suffixed := fmt.Sprintf("%s_%x", candidate, sum[:4])
	for index := 1; used[suffixed]; index++ {
		suffixed = fmt.Sprintf("%s_%x_%d", candidate, sum[:4], index)
	}
	used[suffixed] = true
	return suffixed
}

type remoteTool struct {
	server     string
	name       string
	session    *sdkmcp.ClientSession
	definition ai.Tool
}

func (t remoteTool) Definition() ai.Tool { return t.definition }

func (t remoteTool) Execute(ctx context.Context, call ai.ToolCall, _ agent.ToolUpdate) (agent.ToolResult, error) {
	result, err := t.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: t.name, Arguments: call.Arguments})
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("MCP %s/%s: %w", t.server, t.name, err)
	}
	content := make([]ai.Content, 0, len(result.Content))
	for _, part := range result.Content {
		switch part := part.(type) {
		case *sdkmcp.TextContent:
			content = append(content, ai.NewText(part.Text))
		case *sdkmcp.ImageContent:
			content = append(content, ai.NewImage(base64.StdEncoding.EncodeToString(part.Data), part.MIMEType))
		default:
			encoded, marshalErr := json.Marshal(part)
			if marshalErr == nil {
				content = append(content, ai.NewText(string(encoded)))
			}
		}
	}
	if result.StructuredContent != nil {
		if encoded, marshalErr := json.Marshal(result.StructuredContent); marshalErr == nil {
			content = append(content, ai.NewText(string(encoded)))
		}
	}
	if len(content) == 0 {
		content = append(content, ai.NewText("(no output)"))
	}
	details := map[string]any{"server": t.server, "tool": t.name}
	if result.IsError {
		return agent.ToolResult{Content: content, Details: details}, errorsFromContent(content)
	}
	return agent.ToolResult{Content: content, Details: details}, nil
}

func errorsFromContent(content []ai.Content) error {
	var parts []string
	for _, item := range content {
		if text, ok := item.(ai.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	if len(parts) == 0 {
		return errors.New("MCP tool returned an error")
	}
	return errors.New(strings.Join(parts, "\n"))
}

func sanitize(value string) string {
	var output strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			output.WriteRune(char)
		} else {
			output.WriteByte('_')
		}
	}
	return output.String()
}
