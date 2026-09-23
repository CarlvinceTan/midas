package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

const (
	maxGrepMatches = 200
	maxGrepFile    = 2 * 1024 * 1024
	maxGrepLine    = 500
)

var grepSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "pattern":{"type":"string","description":"Go regular expression to search for"},
    "path":{"type":"string","description":"File or directory to search, relative to the workspace"}
  },
  "required":["pattern"],
  "additionalProperties":false
}`)

type grepTool struct{ kit *Toolkit }

func (t grepTool) Definition() ai.Tool {
	return ai.Tool{Name: "grep", Description: "Search workspace text files with a regular expression without modifying them.", Parameters: grepSchema}
}

func (grepTool) Sequential() bool { return false }

func (t grepTool) Execute(ctx context.Context, call ai.ToolCall, _ agent.ToolUpdate) (agent.ToolResult, error) {
	pattern, err := stringArgument(call.Arguments, "pattern")
	if err != nil {
		return agent.ToolResult{}, err
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("invalid pattern: %w", err)
	}
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
	root, err := t.kit.resolveExisting(name)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("grep %s: %w", name, err)
	}
	matches := make([]string, 0)
	truncated := false
	search := func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(matches) >= maxGrepMatches {
			truncated = true
			return fs.SkipAll
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == ".midas") {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		isFile := info.Mode().IsRegular()
		if !isFile && info.Mode()&fs.ModeSymlink != 0 {
			// A file reached through a link is searched, so a symlinked source in the
			// workspace is not invisible. A linked directory is not walked, which is
			// what keeps a cycle from hanging the search.
			target, statErr := os.Stat(path)
			if statErr != nil {
				return nil
			}
			if !target.Mode().IsRegular() {
				return nil
			}
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil || !within(t.kit.root, resolved) {
				return nil
			}
			info, isFile = target, true
		}
		if !isFile || info.Size() > maxGrepFile {
			return nil
		}
		hitLimit, grepErr := t.grepFile(ctx, path, expression, &matches)
		if grepErr != nil {
			return grepErr
		}
		if hitLimit {
			truncated = true
			return fs.SkipAll
		}
		return nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if info.IsDir() {
		err = filepath.WalkDir(root, search)
	} else {
		entry := fs.FileInfoToDirEntry(info)
		err = search(root, entry, nil)
	}
	if err != nil && err != fs.SkipAll {
		return agent.ToolResult{}, err
	}
	text := strings.Join(matches, "\n")
	if text == "" {
		text = "No matches."
	}
	if truncated {
		text += fmt.Sprintf("\n\n[Showing the first %d matches.]", maxGrepMatches)
	}
	return agent.ToolResult{Content: []ai.Content{ai.NewText(text)}}, nil
}

// grepFile appends at most maxGrepMatches matches and reports whether it
// stopped at that cap, so the caller can say the result was truncated.
func (t grepTool) grepFile(ctx context.Context, path string, expression *regexp.Regexp, matches *[]string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	probe := make([]byte, 512)
	count, readErr := file.Read(probe)
	if readErr != nil && readErr != io.EOF {
		return false, readErr
	}
	if bytes.IndexByte(probe[:count], 0) >= 0 {
		return false, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	relative, err := filepath.Rel(t.kit.root, path)
	if err != nil {
		return false, err
	}
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, maxGrepFile)
	for line := 1; scanner.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		value := scanner.Text()
		if !expression.MatchString(value) {
			continue
		}
		if len(value) > maxGrepLine {
			value = truncateRunes(value, maxGrepLine) + "…"
		}
		*matches = append(*matches, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(relative), line, value))
		if len(*matches) >= maxGrepMatches {
			return true, nil
		}
	}
	return false, scanner.Err()
}

// truncateRunes cuts a string to at most maxBytes without splitting a rune.
func truncateRunes(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
