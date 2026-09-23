package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

const maxListEntries = 1000

var listSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"Directory to list, relative to the workspace"},
    "depth":{"type":"integer","minimum":1,"maximum":20,"description":"Maximum recursive depth; defaults to 4"}
  },
  "additionalProperties":false
}`)

type listTool struct{ kit *Toolkit }

func (t listTool) Definition() ai.Tool {
	return ai.Tool{Name: "list", Description: "List workspace files and directories recursively without modifying them.", Parameters: listSchema}
}

func (listTool) Sequential() bool { return false }

func (t listTool) Execute(ctx context.Context, call ai.ToolCall, _ agent.ToolUpdate) (agent.ToolResult, error) {
	name := "."
	if value, ok := call.Arguments["path"]; ok {
		text, valid := value.(string)
		if !valid {
			return agent.ToolResult{}, fmt.Errorf("argument %q must be a string", "path")
		}
		if strings.TrimSpace(text) != "" {
			name = text
		}
	}
	depth, present, err := optionalPositiveInt(call.Arguments, "depth")
	if err != nil {
		return agent.ToolResult{}, err
	}
	if !present {
		depth = 4
	}
	if depth > 20 {
		return agent.ToolResult{}, errorsForMaximum("depth", 20)
	}
	root, err := t.kit.resolveExisting(name)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("list %s: %w", name, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if !info.IsDir() {
		return agent.ToolResult{}, fmt.Errorf("list %s: not a directory", name)
	}
	baseDepth := pathDepth(root)
	entries := make([]string, 0)
	truncated := false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, relErr := filepath.Rel(t.kit.root, path)
		if relErr != nil {
			return relErr
		}
		isDir := entry.IsDir()
		if !isDir && entry.Type()&fs.ModeSymlink != 0 {
			// A link to a directory is listed as a directory so it is not invisible,
			// but it is not walked: following links would need cycle detection.
			if target, statErr := os.Stat(path); statErr == nil && target.IsDir() {
				isDir = true
			}
		}
		if isDir && (entry.Name() == ".git" || entry.Name() == ".midas") {
			return filepath.SkipDir
		}
		if pathDepth(path)-baseDepth > depth {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		value := filepath.ToSlash(relative)
		if isDir {
			value += "/"
		}
		entries = append(entries, value)
		if len(entries) >= maxListEntries {
			truncated = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	slices.Sort(entries)
	text := strings.Join(entries, "\n")
	if text == "" {
		text = "(empty directory)"
	}
	if truncated {
		text += fmt.Sprintf("\n\n[Showing the first %d entries.]", maxListEntries)
	}
	return agent.ToolResult{Content: []ai.Content{ai.NewText(text)}}, nil
}

func pathDepth(path string) int {
	cleaned := filepath.Clean(path)
	if cleaned == string(filepath.Separator) {
		return 0
	}
	return len(strings.Split(cleaned, string(filepath.Separator)))
}

func errorsForMaximum(name string, maximum int) error {
	return fmt.Errorf("argument %q must be at most %d", name, maximum)
}
