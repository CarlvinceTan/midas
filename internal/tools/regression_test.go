package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// TestReadRefusesNonRegularFiles covers the hang a FIFO would otherwise cause:
// reading one blocks forever because nothing ever writes to it.
func TestReadRefusesNonRegularFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a FIFO")
	}
	kit := newTestToolkit(t)
	fifo := filepath.Join(kit.Root(), "pipe")
	if err := makeFIFO(fifo); err != nil {
		t.Skipf("cannot create a FIFO: %v", err)
	}
	_, err := findTool(t, kit, "read").Execute(context.Background(), ai.NewToolCall("1", "read", map[string]any{"path": "pipe"}), nil)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("reading a FIFO = %v", err)
	}
}

// TestReadRefusesFilesLargerThanTheReadLimit covers the out-of-memory path: the
// tool must refuse with advice instead of loading a huge file.
func TestReadRefusesFilesLargerThanTheReadLimit(t *testing.T) {
	kit := newTestToolkit(t)
	path := filepath.Join(kit.Root(), "huge.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file is enough: only the size matters.
	if err := file.Truncate(maxTextReadBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = findTool(t, kit, "read").Execute(context.Background(), ai.NewToolCall("1", "read", map[string]any{"path": "huge.log"}), nil)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("reading an oversized file = %v", err)
	}
}

// TestReadRejectsOutOfRangeOffset covers the numeric argument boundary: a float
// that converts unpredictably to int must be refused rather than reaching a slice.
func TestReadRejectsOutOfRangeOffset(t *testing.T) {
	kit := newTestToolkit(t)
	if err := os.WriteFile(filepath.Join(kit.Root(), "small.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []float64{1 << 63, 1e19, -0} {
		_, err := findTool(t, kit, "read").Execute(context.Background(), ai.NewToolCall("1", "read", map[string]any{
			"path": "small.txt", "offset": offset,
		}), nil)
		if offset > 1 {
			if err == nil || !strings.Contains(err.Error(), "positive integer") {
				t.Fatalf("offset %v = %v", offset, err)
			}
		}
	}
}

// TestGrepReportsTruncationAtTheMatchLimit is a regression test for a silent cap:
// the tool used to stop at 200 matches without saying so.
func TestGrepReportsTruncationAtTheMatchLimit(t *testing.T) {
	kit := newTestToolkit(t)
	var content strings.Builder
	for index := 0; index < maxGrepMatches+20; index++ {
		content.WriteString("needle\n")
	}
	if err := os.WriteFile(filepath.Join(kit.Root(), "matches.txt"), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := findTool(t, kit, "grep").Execute(context.Background(), ai.NewToolCall("1", "grep", map[string]any{
		"pattern": "needle", "path": ".",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "[Showing the first 200 matches.]") {
		t.Fatalf("missing truncation notice: %q", text[len(text)-80:])
	}
}

// TestGrepTruncatesLongLinesOnRuneBoundaries keeps invalid UTF-8 out of results.
func TestGrepTruncatesLongLinesOnRuneBoundaries(t *testing.T) {
	kit := newTestToolkit(t)
	line := "needle " + strings.Repeat("é", maxGrepLine)
	if err := os.WriteFile(filepath.Join(kit.Root(), "long.txt"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := findTool(t, kit, "grep").Execute(context.Background(), ai.NewToolCall("1", "grep", map[string]any{
		"pattern": "needle", "path": "long.txt",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.HasSuffix(text, "…") {
		t.Fatalf("long line was not truncated: %q", text[len(text)-20:])
	}
	if !isValidUTF8(text) {
		t.Fatal("truncation split a rune")
	}
}

// TestEditRefusesMixedLineEndings: rewriting such a file would silently convert
// every line ending in it, so the edit is refused instead.
func TestEditRefusesMixedLineEndings(t *testing.T) {
	kit := newTestToolkit(t)
	path := filepath.Join(kit.Root(), "mixed.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := findTool(t, kit, "edit").Execute(context.Background(), ai.NewToolCall("1", "edit", map[string]any{
		"path": "mixed.txt", "edits": []any{map[string]any{"oldText": "beta", "newText": "BETA"}},
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "mixes LF and CRLF") {
		t.Fatalf("editing a mixed-ending file = %v", err)
	}
	// A file with one consistent ending still edits.
	if err := os.WriteFile(filepath.Join(kit.Root(), "crlf.txt"), []byte("alpha\r\nbeta\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := findTool(t, kit, "edit").Execute(context.Background(), ai.NewToolCall("2", "edit", map[string]any{
		"path": "crlf.txt", "edits": []any{map[string]any{"oldText": "beta", "newText": "BETA"}},
	}), nil); err != nil {
		t.Fatalf("editing a CRLF file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(kit.Root(), "crlf.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "alpha\r\nBETA\r\n" {
		t.Fatalf("edited CRLF file = %q", got)
	}
}

// TestScratchNameKeepsProjectFiles: a prefix match alone used to refuse ordinary
// names like template.go.
func TestScratchNameKeepsProjectFiles(t *testing.T) {
	scratch := []string{"tmp", "tmp_file.json", "temp-notes.md", "scratch.py", "probe.go", "throwaway.txt", "capture.tmp"}
	for _, name := range scratch {
		if !scratchName(name) {
			t.Errorf("%q should look like scratch", name)
		}
	}
	project := []string{"template.go", "templating.go", "temporary.txt", "prober.go", "main.go", "notes.md"}
	for _, name := range project {
		if scratchName(name) {
			t.Errorf("%q is a project file", name)
		}
	}
}

func isValidUTF8(value string) bool {
	for _, runeValue := range value {
		if runeValue == '\uFFFD' {
			return false
		}
	}
	return true
}

// TestListAndGrepFollowSymlinkedFilesInsideTheWorkspace: repositories commonly
// symlink generated sources, and both tools used to skip them silently.
func TestListAndGrepFollowSymlinkedFilesInsideTheWorkspace(t *testing.T) {
	kit := newTestToolkit(t)
	realDir := filepath.Join(kit.Root(), "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "linked.txt"), []byte("needle in a linked file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(realDir, "linked.txt"), filepath.Join(kit.Root(), "alias.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(realDir, filepath.Join(kit.Root(), "aliasdir")); err != nil {
		t.Fatal(err)
	}

	grepResult, err := findTool(t, kit, "grep").Execute(context.Background(), ai.NewToolCall("1", "grep", map[string]any{
		"pattern": "needle", "path": ".",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := grepResult.Content[0].(ai.TextContent).Text; !strings.Contains(text, "alias.txt") {
		t.Fatalf("grep skipped a symlinked file: %q", text)
	}

	listResult, err := findTool(t, kit, "list").Execute(context.Background(), ai.NewToolCall("2", "list", map[string]any{"path": "."}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := listResult.Content[0].(ai.TextContent).Text; !strings.Contains(text, "aliasdir/") {
		t.Fatalf("list hid a symlinked directory: %q", text)
	}

	// A link that leaves the workspace stays invisible to both tools.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(kit.Root(), "escape.txt")); err != nil {
		t.Fatal(err)
	}
	grepResult, err = findTool(t, kit, "grep").Execute(context.Background(), ai.NewToolCall("3", "grep", map[string]any{
		"pattern": "needle", "path": ".",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := grepResult.Content[0].(ai.TextContent).Text; strings.Contains(text, "escape.txt") {
		t.Fatalf("grep read outside the workspace through a link: %q", text)
	}
}
