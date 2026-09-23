package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

var editSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"File to edit within the workspace"},
    "edits":{"type":"array","minItems":1,"items":{"type":"object","properties":{"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["oldText","newText"],"additionalProperties":false}}
  },
  "required":["path","edits"],
  "additionalProperties":false
}`)

type editTool struct{ kit *Toolkit }

type replacement struct {
	oldText string
	newText string
	index   int
}

func (t editTool) Definition() ai.Tool {
	return ai.Tool{Name: "edit", Description: "Apply one or more exact, unique, non-overlapping text replacements to one file. All edits match the original content.", Parameters: editSchema}
}

func (editTool) Sequential() bool { return true }

func (t editTool) Execute(ctx context.Context, call ai.ToolCall, _ agent.ToolUpdate) (agent.ToolResult, error) {
	name, err := stringArgument(call.Arguments, "path")
	if err != nil {
		return agent.ToolResult{}, err
	}
	edits, err := editArguments(call.Arguments)
	if err != nil {
		return agent.ToolResult{}, err
	}
	path, err := t.kit.resolveExisting(name)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("edit %s: %w", name, err)
	}
	lock := t.kit.mutationLock(path)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("edit %s: %w", name, err)
	}
	bom := ""
	content := string(raw)
	if strings.HasPrefix(content, "\ufeff") {
		bom, content = "\ufeff", strings.TrimPrefix(content, "\ufeff")
	}
	crlf := strings.Contains(content, "\r\n")
	// A file that mixes endings would be silently rewritten throughout by the
	// round trip below, so mixed endings are refused instead of normalised.
	if crlf && hasMixedLineEndings(content) {
		return agent.ToolResult{}, fmt.Errorf("edit %s: file mixes LF and CRLF line endings; normalise it first", name)
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	for index := range edits {
		edits[index].oldText = normalizeNewlines(edits[index].oldText)
		edits[index].newText = normalizeNewlines(edits[index].newText)
		if edits[index].oldText == "" {
			return agent.ToolResult{}, fmt.Errorf("edits[%d].oldText must not be empty", index)
		}
		count := strings.Count(normalized, edits[index].oldText)
		if count == 0 {
			return agent.ToolResult{}, fmt.Errorf("could not find edits[%d] in %s; oldText must match exactly", index, name)
		}
		if count > 1 {
			return agent.ToolResult{}, fmt.Errorf("found %d occurrences of edits[%d] in %s; oldText must be unique", count, index, name)
		}
		edits[index].index = strings.Index(normalized, edits[index].oldText)
	}
	slices.SortStableFunc(edits, func(a, b replacement) int { return cmp.Compare(a.index, b.index) })
	for index := 1; index < len(edits); index++ {
		previous := edits[index-1]
		if previous.index+len(previous.oldText) > edits[index].index {
			return agent.ToolResult{}, fmt.Errorf("edits overlap in %s", name)
		}
	}
	updated := normalized
	for index := len(edits) - 1; index >= 0; index-- {
		edit := edits[index]
		updated = updated[:edit.index] + edit.newText + updated[edit.index+len(edit.oldText):]
	}
	if updated == normalized {
		return agent.ToolResult{}, fmt.Errorf("no changes made to %s", name)
	}
	if crlf {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := os.WriteFile(path, []byte(bom+updated), 0o644); err != nil {
		return agent.ToolResult{}, fmt.Errorf("edit %s: %w", name, err)
	}
	return agent.ToolResult{
		Content: []ai.Content{ai.NewText(fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(edits), name))},
		Details: map[string]any{"replacements": len(edits)},
	}, nil
}

// hasMixedLineEndings reports whether content contains both CRLF and a bare LF.
func hasMixedLineEndings(content string) bool {
	crlf := strings.Count(content, "\r\n")
	bare := strings.Count(content, "\n") - crlf
	return crlf > 0 && bare > 0
}

func editArguments(arguments map[string]any) ([]replacement, error) {
	value, ok := arguments["edits"]
	if !ok {
		if oldText, oldOK := arguments["oldText"].(string); oldOK {
			if newText, newOK := arguments["newText"].(string); newOK {
				return []replacement{{oldText: oldText, newText: newText}}, nil
			}
		}
		return nil, fmt.Errorf("edits must contain at least one replacement")
	}
	if encoded, ok := value.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			return nil, fmt.Errorf("edits must be an array: %w", err)
		}
		value = decoded
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("edits must contain at least one replacement")
	}
	edits := make([]replacement, 0, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edits[%d] must be an object", index)
		}
		oldText, ok := object["oldText"].(string)
		if !ok {
			return nil, fmt.Errorf("edits[%d].oldText must be a string", index)
		}
		newText, ok := object["newText"].(string)
		if !ok {
			return nil, fmt.Errorf("edits[%d].newText must be a string", index)
		}
		edits = append(edits, replacement{oldText: oldText, newText: newText})
	}
	return edits, nil
}

func normalizeNewlines(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}
