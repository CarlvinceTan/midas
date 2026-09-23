package tui

import (
	"context"
	"errors"
	"os/exec"
	"sync"
)

// ShellRunner executes one local shell command and streams combined output.
// cancelled distinguishes an explicit context cancellation from a command error.
type ShellRunner func(ctx context.Context, cwd, command string, output func(string)) (exitCode int, cancelled bool, err error)

type shellOutputWriter struct {
	mu     sync.Mutex
	output func(string)
}

func (w *shellOutputWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(data) > 0 && w.output != nil {
		w.output(string(data))
	}
	return len(data), nil
}

func runLocalShell(ctx context.Context, cwd, command string, output func(string)) (int, bool, error) {
	process := exec.CommandContext(ctx, "bash", "-lc", command)
	process.Dir = cwd
	writer := &shellOutputWriter{output: output}
	process.Stdout, process.Stderr = writer, writer
	err := process.Run()
	if err == nil {
		return 0, false, nil
	}
	if ctx.Err() != nil {
		return -1, true, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), false, err
	}
	return -1, false, err
}

func parseShellCommand(value string) (command string, exclude bool, ok bool) {
	if len(value) >= 3 && value[:3] == "!! " {
		return trimShellCommand(value[3:]), true, true
	}
	if len(value) >= 2 && value[:2] == "! " {
		return trimShellCommand(value[2:]), false, true
	}
	return "", false, false
}

func trimShellCommand(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\n' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\n' || value[end-1] == '\r') {
		end--
	}
	return value[start:end]
}
