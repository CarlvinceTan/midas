package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

var writeSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"Path to create or overwrite within the workspace"},
    "content":{"type":"string","description":"Complete file content"}
  },
  "required":["path","content"],
  "additionalProperties":false
}`)

type writeTool struct{ kit *Toolkit }

func (t writeTool) Definition() ai.Tool {
	return ai.Tool{Name: "write", Description: "Create or completely overwrite a file, creating parent directories as needed. Write temporary or generated scratch files to the system temp directory instead of the workspace.", Parameters: writeSchema}
}

func (writeTool) Sequential() bool { return true }

func (t writeTool) Execute(ctx context.Context, call ai.ToolCall, _ agent.ToolUpdate) (agent.ToolResult, error) {
	name, err := stringArgument(call.Arguments, "path")
	if err != nil {
		return agent.ToolResult{}, err
	}
	content, err := stringArgument(call.Arguments, "content")
	if err != nil {
		return agent.ToolResult{}, err
	}
	if scratchName(name) {
		return agent.ToolResult{}, fmt.Errorf("refusing to write %s inside the workspace: temporary and generated files belong in %s", name, scratchDir())
	}
	path, err := t.kit.resolveWritable(name)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("write %s: %w", name, err)
	}
	lock := t.kit.mutationLock(path)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return agent.ToolResult{}, fmt.Errorf("write %s: %w", name, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return agent.ToolResult{}, fmt.Errorf("write %s: %w", name, err)
	}
	return agent.ToolResult{Content: []ai.Content{ai.NewText("Successfully wrote to " + name)}}, nil
}
