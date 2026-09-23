package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestToolkitContainsOnlyMinimalCodingTools(t *testing.T) {
	kit := newTestToolkit(t)
	var names []string
	for _, tool := range kit.All() {
		names = append(names, tool.Definition().Name)
	}
	if want := []string{"read", "write", "edit", "bash"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("tool names = %v, want %v", names, want)
	}
}

func TestReadOnlyToolkitCanDiscoverWithoutMutationTools(t *testing.T) {
	kit := newTestToolkit(t)
	if err := os.WriteFile(filepath.Join(kit.Root(), "example.go"), []byte("package example\n\nfunc Target() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range kit.ReadOnly() {
		names = append(names, tool.Definition().Name)
	}
	if want := []string{"read", "list", "grep"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("read-only tool names = %v, want %v", names, want)
	}
	listed, err := findTool(t, kit, "list").Execute(context.Background(), ai.NewToolCall("list", "list", map[string]any{"path": "."}), nil)
	if err != nil || !strings.Contains(listed.Content[0].(ai.TextContent).Text, "example.go") {
		t.Fatalf("list result = %#v, %v", listed, err)
	}
	found, err := findTool(t, kit, "grep").Execute(context.Background(), ai.NewToolCall("grep", "grep", map[string]any{"pattern": "Target", "path": "."}), nil)
	if err != nil || !strings.Contains(found.Content[0].(ai.TextContent).Text, "example.go:3:func Target") {
		t.Fatalf("grep result = %#v, %v", found, err)
	}
}

func TestWriteReadAndEditRoundTrip(t *testing.T) {
	kit := newTestToolkit(t)
	write := findTool(t, kit, "write")
	read := findTool(t, kit, "read")
	edit := findTool(t, kit, "edit")

	_, err := write.Execute(context.Background(), ai.NewToolCall("1", "write", map[string]any{
		"path": "nested/example.txt", "content": "alpha\r\nbeta\r\ngamma",
	}), nil)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := read.Execute(context.Background(), ai.NewToolCall("2", "read", map[string]any{
		"path": "nested/example.txt", "offset": float64(2), "limit": float64(1),
	}), nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.HasPrefix(text, "beta\r") || !strings.Contains(text, "1 more lines") {
		t.Fatalf("paginated read = %q", text)
	}

	_, err = edit.Execute(context.Background(), ai.NewToolCall("3", "edit", map[string]any{
		"path": "nested/example.txt",
		"edits": []any{
			map[string]any{"oldText": "alpha", "newText": "one"},
			map[string]any{"oldText": "gamma", "newText": "three"},
		},
	}), nil)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(kit.Root(), "nested/example.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "one\r\nbeta\r\nthree"; got != want {
		t.Fatalf("edited content = %q, want %q", got, want)
	}
}

func TestReadTruncatesWithContinuation(t *testing.T) {
	kit := newTestToolkit(t)
	lines := make([]string, maxOutputLines+2)
	for index := range lines {
		lines[index] = "line"
	}
	path := filepath.Join(kit.Root(), "large.txt")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := findTool(t, kit, "read").Execute(context.Background(), ai.NewToolCall("1", "read", map[string]any{"path": "large.txt"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "Showing lines 1-2000") || !strings.Contains(text, "offset=2001") {
		t.Fatalf("missing continuation notice: %q", text[len(text)-120:])
	}
	if result.Details["reason"] != "lines" {
		t.Fatalf("truncation details = %#v", result.Details)
	}
}

func TestFileToolsRejectWorkspaceEscape(t *testing.T) {
	kit := newTestToolkit(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(kit.Root(), "outside")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		tool string
		args map[string]any
	}{
		{"read", map[string]any{"path": "../secret.txt"}},
		{"read", map[string]any{"path": "outside/secret.txt"}},
		{"write", map[string]any{"path": "outside/new.txt", "content": "bad"}},
		{"edit", map[string]any{"path": "outside/secret.txt", "edits": []any{map[string]any{"oldText": "secret", "newText": "bad"}}}},
	}
	for _, test := range cases {
		t.Run(test.tool+test.args["path"].(string), func(t *testing.T) {
			_, err := findTool(t, kit, test.tool).Execute(context.Background(), ai.NewToolCall("1", test.tool, test.args), nil)
			if err == nil {
				t.Fatal("expected workspace escape error")
			}
		})
	}
}

func TestEditRequiresUniqueNonOverlappingText(t *testing.T) {
	kit := newTestToolkit(t)
	path := filepath.Join(kit.Root(), "example.txt")
	if err := os.WriteFile(path, []byte("same same"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := findTool(t, kit, "edit")
	_, err := tool.Execute(context.Background(), ai.NewToolCall("1", "edit", map[string]any{
		"path": "example.txt", "edits": []any{map[string]any{"oldText": "same", "newText": "new"}},
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "2 occurrences") {
		t.Fatalf("duplicate error = %v", err)
	}

	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), ai.NewToolCall("2", "edit", map[string]any{
		"path": "example.txt", "edits": []any{
			map[string]any{"oldText": "abcd", "newText": "x"},
			map[string]any{"oldText": "cdef", "newText": "y"},
		},
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap error = %v", err)
	}
}

func TestBashStreamsAndReturnsCombinedOutput(t *testing.T) {
	kit := newTestToolkit(t)
	tool := findTool(t, kit, "bash")
	var updates []string
	result, err := tool.Execute(context.Background(), ai.NewToolCall("1", "bash", map[string]any{
		"command": "printf stdout; printf stderr >&2",
	}), func(result agent.ToolResult) {
		if len(result.Content) > 0 {
			updates = append(updates, result.Content[0].(ai.TextContent).Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Content[0].(ai.TextContent).Text; got != "stdoutstderr" {
		t.Fatalf("output = %q", got)
	}
	if len(updates) == 0 {
		t.Fatal("expected at least one streaming update")
	}
}

func TestBashExitAndTimeoutErrorsRetainStatus(t *testing.T) {
	kit := newTestToolkit(t)
	tool := findTool(t, kit, "bash")
	_, err := tool.Execute(context.Background(), ai.NewToolCall("1", "bash", map[string]any{
		"command": "printf failed; exit 7",
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "code 7") {
		t.Fatalf("exit error = %v", err)
	}

	started := time.Now()
	_, err = tool.Execute(context.Background(), ai.NewToolCall("2", "bash", map[string]any{
		"command": "sleep 5", "timeout": float64(1),
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "timed out after 1 seconds") {
		t.Fatalf("timeout error = %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("timeout took %s", time.Since(started))
	}
}

func TestCanceledFileOperationDoesNotMutate(t *testing.T) {
	kit := newTestToolkit(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := findTool(t, kit, "write").Execute(ctx, ai.NewToolCall("1", "write", map[string]any{
		"path": "canceled.txt", "content": "no",
	}), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if _, statErr := os.Stat(filepath.Join(kit.Root(), "canceled.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("canceled write created file: %v", statErr)
	}
}

func newTestToolkit(t *testing.T) *Toolkit {
	t.Helper()
	kit, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return kit
}

func findTool(t *testing.T, kit *Toolkit, name string) agent.Tool {
	t.Helper()
	for _, tool := range append(kit.All(), kit.ReadOnly()...) {
		if tool.Definition().Name == name {
			return tool
		}
	}
	t.Fatalf("missing tool %q", name)
	return nil
}

func TestWriteToolKeepsScratchFilesOutOfTheWorkspace(t *testing.T) {
	root := t.TempDir()
	kit, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	write := writeTool{kit: kit}
	for _, name := range []string{"probe.go", "scratch.py", "tmp_capture.json", "notes.tmp"} {
		_, err := write.Execute(context.Background(), ai.ToolCall{
			Name: "write", Arguments: map[string]any{"path": name, "content": "x"},
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "temporary and generated files belong") {
			t.Fatalf("%s was written into the workspace: %v", name, err)
		}
		if _, statErr := os.Stat(filepath.Join(root, name)); statErr == nil {
			t.Fatalf("%s exists in the workspace", name)
		}
	}
	// Real project files are still written normally.
	if _, err := write.Execute(context.Background(), ai.ToolCall{
		Name: "write", Arguments: map[string]any{"path": "main.go", "content": "package main"},
	}, nil); err != nil {
		t.Fatalf("project file rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "main.go")); err != nil {
		t.Fatalf("project file missing: %v", err)
	}
	// The temp directory is a real, existing place to put scratch work.
	if _, err := os.Stat(scratchDir()); err != nil {
		t.Fatalf("scratch dir unusable: %v", err)
	}
}

func TestBashToolPointsChildProcessesAtTheTempDirectory(t *testing.T) {
	root := t.TempDir()
	kit, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := bashTool{kit: kit}.Execute(context.Background(), ai.ToolCall{
		Name: "bash", Arguments: map[string]any{"command": "printf %s \"$TMPDIR\""},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(ai.TextContent).Text
	if strings.TrimSpace(text) != scratchDir() {
		t.Fatalf("child TMPDIR = %q, want %q", text, scratchDir())
	}
}
