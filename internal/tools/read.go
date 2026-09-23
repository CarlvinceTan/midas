package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

var readSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"Path to the file, relative to the workspace or absolute within it"},
    "offset":{"type":"integer","minimum":1,"description":"First line to read, starting at 1"},
    "limit":{"type":"integer","minimum":1,"description":"Maximum number of lines to read"}
  },
  "required":["path"],
  "additionalProperties":false
}`)

type readTool struct{ kit *Toolkit }

func (t readTool) Definition() ai.Tool {
	return ai.Tool{
		Name:        "read",
		Description: "Read a text file or supported image. Text is limited to 2000 lines or 50KB; use offset and limit to continue.",
		Parameters:  readSchema,
	}
}

func (t readTool) Execute(ctx context.Context, call ai.ToolCall, _ agent.ToolUpdate) (agent.ToolResult, error) {
	name, err := stringArgument(call.Arguments, "path")
	if err != nil {
		return agent.ToolResult{}, err
	}
	offset, present, err := optionalPositiveInt(call.Arguments, "offset")
	if err != nil {
		return agent.ToolResult{}, err
	}
	if !present {
		offset = 1
	}
	limit, limited, err := optionalPositiveInt(call.Arguments, "limit")
	if err != nil {
		return agent.ToolResult{}, err
	}
	path, err := t.kit.resolveExisting(name)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("read %s: %w", name, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("read %s: %w", name, err)
	}
	// A FIFO, socket, or device would block or stream forever, and a directory
	// has no contents to read; only regular files are readable.
	if !info.Mode().IsRegular() {
		return agent.ToolResult{}, fmt.Errorf("read %s: not a regular file", name)
	}
	mime := imageMIME(path)
	// The read is bounded so a huge file fails with advice instead of exhausting
	// memory. Images are held whole for base64 and are capped much lower.
	readLimit := int64(maxTextReadBytes)
	if mime != "" {
		readLimit = maxImageReadBytes
	}
	if info.Size() > readLimit {
		return agent.ToolResult{}, fmt.Errorf(
			"read %s: file is %s, larger than the %s limit; use bash to inspect a bounded range",
			name, formatBytes(int(info.Size())), formatBytes(int(readLimit)))
	}
	file, err := os.Open(path)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("read %s: %w", name, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, readLimit+1))
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > readLimit {
		return agent.ToolResult{}, fmt.Errorf(
			"read %s: file grew past the %s limit while reading; use bash to inspect a bounded range",
			name, formatBytes(int(readLimit)))
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if mime != "" {
		return agent.ToolResult{Content: []ai.Content{
			ai.NewText("Read image file [" + mime + "]"),
			ai.NewImage(base64.StdEncoding.EncodeToString(data), mime),
		}}, nil
	}

	lines := strings.Split(string(data), "\n")
	if offset > len(lines) {
		return agent.ToolResult{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", offset, len(lines))
	}
	selected := lines[offset-1:]
	userEnd := len(lines)
	if limited && limit < len(selected) {
		selected = selected[:limit]
		userEnd = offset - 1 + limit
	}
	truncated := truncateHead(selected, maxOutputLines, maxOutputBytes)
	if truncated.firstLineTooLarge {
		return agent.ToolResult{Content: []ai.Content{ai.NewText(fmt.Sprintf(
			"[Line %d is %s, exceeds %s limit. Use bash to inspect a bounded slice.]",
			offset, formatBytes(len([]byte(selected[0]))), formatBytes(maxOutputBytes),
		))}}, nil
	}
	text := truncated.content
	shownEnd := offset + truncated.lines - 1
	details := map[string]any(nil)
	if truncated.truncated {
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", offset, shownEnd, len(lines), shownEnd+1)
		details = map[string]any{"truncated": true, "reason": truncated.reason, "totalLines": len(lines)}
	} else if limited && userEnd < len(lines) {
		text += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", len(lines)-userEnd, userEnd+1)
	}
	return agent.ToolResult{Content: []ai.Content{ai.NewText(text)}, Details: details}, nil
}

func imageMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return ""
	}
}

type headTruncation struct {
	content           string
	lines             int
	truncated         bool
	reason            string
	firstLineTooLarge bool
}

func truncateHead(lines []string, maxLines, maxBytes int) headTruncation {
	if len(lines) > 0 && len([]byte(lines[0])) > maxBytes {
		return headTruncation{truncated: true, reason: "bytes", firstLineTooLarge: true}
	}
	var output strings.Builder
	for index, line := range lines {
		if index >= maxLines {
			return headTruncation{content: output.String(), lines: index, truncated: true, reason: "lines"}
		}
		separator := 0
		if index > 0 {
			separator = 1
		}
		if output.Len()+separator+len([]byte(line)) > maxBytes {
			return headTruncation{content: output.String(), lines: index, truncated: true, reason: "bytes"}
		}
		if separator != 0 {
			output.WriteByte('\n')
		}
		output.WriteString(line)
	}
	return headTruncation{content: output.String(), lines: len(lines)}
}

func formatBytes(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	return fmt.Sprintf("%.1fKB", float64(size)/1024)
}

func validUTF8Tail(data []byte) []byte {
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[1:]
	}
	return data
}
