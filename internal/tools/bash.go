package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

var bashSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "command":{"type":"string","description":"Bash command to execute in the workspace"},
    "timeout":{"type":"integer","minimum":1,"description":"Optional timeout in seconds"}
  },
  "required":["command"],
  "additionalProperties":false
}`)

type bashTool struct{ kit *Toolkit }

func (t bashTool) Definition() ai.Tool {
	return ai.Tool{Name: "bash", Description: "Execute a Bash command in the workspace. Returns the last 2000 lines or 50KB of combined output. Write temporary or generated files to the system temp directory, not the workspace.", Parameters: bashSchema}
}

func (bashTool) Sequential() bool { return true }

func (t bashTool) Execute(ctx context.Context, call ai.ToolCall, update agent.ToolUpdate) (agent.ToolResult, error) {
	command, err := stringArgument(call.Arguments, "command")
	if err != nil {
		return agent.ToolResult{}, err
	}
	timeout, timed, err := optionalPositiveInt(call.Arguments, "timeout")
	if err != nil {
		return agent.ToolResult{}, err
	}
	runContext := ctx
	cancel := func() {}
	if timed {
		runContext, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runContext, "/bin/bash", "-lc", command)
	cmd.Dir = t.kit.root
	// Anything the command shells out to that honours TMPDIR writes its scratch
	// files there instead of the workspace.
	cmd.Env = append(os.Environ(), "TMPDIR="+scratchDir(), "TMP="+scratchDir(), "TEMP="+scratchDir())
	applyProcessGroup(cmd)
	// Cancellation kills the whole process group, not just the shell, so children
	// started by the command die with it. WaitDelay then bounds the wait for a
	// descendant that keeps the output pipes open after the shell exits. Signalling
	// happens inside Wait rather than after it, so a reaped pid can never be
	// signalled by mistake.
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	output := newTailOutput(maxOutputBytes)
	cmd.Stdout = output
	cmd.Stderr = output
	if update != nil {
		update(agent.ToolResult{})
		output.onUpdate = func(text string) {
			update(agent.ToolResult{Content: []ai.Content{ai.NewText(text)}})
		}
	}
	if err := cmd.Start(); err != nil {
		return agent.ToolResult{}, fmt.Errorf("start command: %w", err)
	}
	err = cmd.Wait()
	snapshot, details := output.snapshot()
	if snapshot == "" {
		snapshot = "(no output)"
	}
	if runContext.Err() != nil {
		status := "Command aborted"
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			status = fmt.Sprintf("Command timed out after %d seconds", timeout)
		}
		return agent.ToolResult{}, fmt.Errorf("%s\n\n%s: %w", emptyAsNothing(snapshot), status, runContext.Err())
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return agent.ToolResult{}, fmt.Errorf("%s\n\nCommand exited with code %d", emptyAsNothing(snapshot), exitError.ExitCode())
		}
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: []ai.Content{ai.NewText(snapshot)}, Details: details}, nil
}

func emptyAsNothing(text string) string {
	if text == "(no output)" {
		return ""
	}
	return text
}

type tailOutput struct {
	mu       sync.Mutex
	data     []byte
	total    int64
	newlines int64
	lastByte byte
	onUpdate func(string)
	lastEmit time.Time
}

func newTailOutput(capacity int) *tailOutput {
	return &tailOutput{data: make([]byte, 0, capacity)}
}

func (o *tailOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	originalLength := len(data)
	o.total += int64(originalLength)
	o.newlines += int64(bytes.Count(data, []byte("\n")))
	if originalLength > 0 {
		o.lastByte = data[originalLength-1]
	}
	o.data = append(o.data, data...)
	if len(o.data) > maxOutputBytes {
		o.data = append(o.data[:0], o.data[len(o.data)-maxOutputBytes:]...)
	}
	now := time.Now()
	callback := o.onUpdate
	shouldEmit := callback != nil && now.Sub(o.lastEmit) >= 100*time.Millisecond
	if shouldEmit {
		o.lastEmit = now
		text := string(validUTF8Tail(append([]byte(nil), o.data...)))
		o.mu.Unlock()
		callback(text)
		return originalLength, nil
	}
	o.mu.Unlock()
	return originalLength, nil
}

func (o *tailOutput) snapshot() (string, map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	data := validUTF8Tail(append([]byte(nil), o.data...))
	lines := strings.Split(string(data), "\n")
	if len(lines) > maxOutputLines {
		lines = lines[len(lines)-maxOutputLines:]
	}
	text := strings.Join(lines, "\n")
	totalLines := o.newlines
	if o.total > 0 && o.lastByte != '\n' {
		totalLines++
	}
	truncated := o.total > int64(maxOutputBytes) || totalLines > int64(maxOutputLines)
	if !truncated {
		return text, nil
	}
	details := map[string]any{"truncated": true, "totalBytes": o.total, "totalLines": totalLines}
	text += fmt.Sprintf("\n\n[Showing the last %d lines / %s of command output.]", len(lines), formatBytes(len([]byte(text))))
	return text, details
}
