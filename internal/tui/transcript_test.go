package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestTranscriptTaskAndMCPRowsKeepLegacyPresentation(t *testing.T) {
	task := chatTool{
		id: "task-1", name: "task", status: "done",
		args: map[string]any{
			"subagent_type": "code-reviewer",
			"description":   "Review changes",
			"prompt":        "Review the current diff.",
		},
		result: `<task id="child"><task_result>Looks good.</task_result></task>`,
	}
	collapsed := plainLines(renderTranscriptTaskTool(task, 80, false))
	for index := range collapsed {
		collapsed[index] = strings.TrimRight(collapsed[index], " ")
	}
	if len(collapsed) != 2 || collapsed[0] != "✓ Subagents (1 task):" || collapsed[1] != "✓ Code Reviewer: Review changes" {
		t.Fatalf("collapsed task rows = %#v", collapsed)
	}
	expanded := strings.Join(plainLines(renderTranscriptTaskTool(task, 80, true)), "\n")
	for _, want := range []string{"✓ Code Reviewer", "─── Task ───", "Review the current diff.", "─── Output ───", "Looks good."} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded task missing %q:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "<task_result>") {
		t.Fatalf("task transport wrapper leaked into transcript:\n%s", expanded)
	}

	mcp := chatTool{id: "mcp-1", name: "filesystem_read_file", status: "done"}
	row := stripANSI(renderTranscriptTool(mcp, 80, false, []string{"filesystem"})[0])
	if row != "✓ Filesystem MCP: Read File" {
		t.Fatalf("MCP row = %q", row)
	}
	overlapping := chatTool{id: "mcp-2", name: "Supabase_Midas_execute_sql", status: "done"}
	row = stripANSI(renderTranscriptTool(overlapping, 80, false, []string{"Supabase", "Supabase_Midas"})[0])
	if row != "✓ Supabase Midas MCP: Execute Sql" {
		t.Fatalf("longest MCP server match = %q", row)
	}
}

func TestLiveToolResultsKeepMultilineDetailForExpansion(t *testing.T) {
	result := &agent.ToolResult{Content: []ai.Content{ai.NewText("one\ntwo\nthree")}}
	if got := toolResultText(result); got != "one\ntwo\nthree" {
		t.Fatalf("tool result was flattened before expansion: %q", got)
	}
	tool := chatTool{id: "read-1", name: "read", status: "done", result: toolResultText(result)}
	plain := strings.Join(plainLines(renderTranscriptTool(tool, 60, true, nil)), "\n")
	for _, want := range []string{"one", "two", "three"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expanded result missing %q:\n%s", want, plain)
		}
	}
}

func TestLiveToolRowsAreSeparatedFromStreamingOutput(t *testing.T) {
	for _, test := range []struct {
		name     string
		segments []chatSegment
		tools    []chatTool
		leading  string
	}{
		{
			name: "multi tool chain",
			tools: []chatTool{
				{id: "t1", name: "read", status: "done", args: map[string]any{"path": "a.go"}},
				{id: "t2", name: "bash", status: "done", args: map[string]any{"command": "go test ./..."}},
			},
			segments: []chatSegment{
				{kind: chatSegmentThinking, text: "Looking at the renderer", startedAt: 1, endedAt: 2},
				{kind: chatSegmentTool, toolID: "t1", startedAt: 2, endedAt: 3},
				{kind: chatSegmentTool, toolID: "t2", startedAt: 3, endedAt: 4},
				{kind: chatSegmentText, text: "The renderer keeps the rows apart.", startedAt: 4},
			},
			leading: "Read 1 file, ran 1 command",
		},
		{
			name:  "single tool row",
			tools: []chatTool{{id: "t1", name: "read", status: "done", args: map[string]any{"path": "a.go"}}},
			segments: []chatSegment{
				{kind: chatSegmentTool, toolID: "t1", startedAt: 2, endedAt: 3},
				{kind: chatSegmentText, text: "The renderer keeps the rows apart.", startedAt: 4},
			},
			leading: "✓ Read a.go",
		},
	} {
		chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}})
		if err != nil {
			t.Fatal(err)
		}
		chat.mu.Lock()
		chat.entries = []chatEntry{
			{kind: chatUser, text: "fix the spacing"},
			{kind: chatAssistant, text: "The renderer keeps the rows apart.", tools: test.tools, segments: test.segments, startedAt: 1},
		}
		chat.active = 1
		chat.mu.Unlock()

		lines := plainLines(chat.renderTranscript(70))
		leading := -1
		for index, line := range lines {
			if strings.Contains(line, test.leading) {
				leading = index
				break
			}
		}
		if leading < 0 {
			t.Fatalf("%s: live activity row %q missing:\n%s", test.name, test.leading, strings.Join(lines, "\n"))
		}
		if leading+2 >= len(lines) || strings.TrimSpace(lines[leading+1]) != "" {
			t.Fatalf("%s: live output does not start after one blank row:\n%s", test.name, strings.Join(lines, "\n"))
		}
		if !strings.Contains(lines[leading+2], "The renderer keeps the rows apart.") {
			t.Fatalf("%s: output text did not follow the activity row:\n%s", test.name, strings.Join(lines, "\n"))
		}
	}
}

func TestEditRowsReportAddedAndRemovedLines(t *testing.T) {
	theme := CurrentTheme()
	tool := chatTool{
		id: "edit-1", name: "edit", status: "done",
		args: map[string]any{
			"path": "tui/transcript.go",
			"edits": []any{
				// Context is trimmed away, so only the changed line counts.
				map[string]any{"oldText": "a\nb\nc\nd", "newText": "a\nB\nc\nd"},
				// A replacement that only removes lines reports no added count.
				map[string]any{"oldText": "keep\nremove me\nkeep2", "newText": "keep\nkeep2"},
			},
		},
	}
	row := renderTranscriptTool(tool, 90, false, nil)[0]
	if !strings.Contains(row, theme.FG("success", "+1")) || !strings.Contains(row, theme.FG("error", "-2")) {
		t.Fatalf("edit row did not style the counts: %q", row)
	}
	if got, want := stripANSI(row), "✓ Edited tui/transcript.go +1 -2"; got != want {
		t.Fatalf("edit row = %q, want %q", got, want)
	}

	added := chatTool{id: "edit-2", name: "edit", status: "done", args: map[string]any{
		"path": "a.go", "oldText": "one", "newText": "one\ntwo\nthree",
	}}
	row = renderTranscriptTool(added, 90, false, nil)[0]
	if got, want := stripANSI(row), "✓ Edited a.go +2"; got != want {
		t.Fatalf("added-only row = %q, want %q", got, want)
	}
	if strings.Contains(row, theme.FG("error", "-")) {
		t.Fatalf("added-only row styled a removal count: %q", row)
	}
}

func TestEditRowsWithoutRealChangesOrFailuresShowNoCounts(t *testing.T) {
	unchanged := chatTool{id: "edit-1", name: "edit", status: "done", args: map[string]any{
		"path": "a.go", "oldText": "same\ntext", "newText": "same\ntext\n",
	}}
	row := renderTranscriptTool(unchanged, 90, false, nil)[0]
	if got, want := stripANSI(row), "✓ Edited a.go"; got != want {
		t.Fatalf("unchanged edit row = %q, want %q", got, want)
	}

	running := chatTool{id: "edit-2", name: "edit", status: "running", args: map[string]any{
		"path": "a.go", "oldText": "one", "newText": "one\ntwo",
	}}
	if got := stripANSI(renderTranscriptTool(running, 90, false, nil)[0]); strings.Contains(got, "+1") {
		t.Fatalf("running edit row reported counts: %q", got)
	}

	failed := chatTool{id: "edit-3", name: "edit", status: "done", isError: true, args: map[string]any{
		"path": "a.go", "oldText": "one", "newText": "one\ntwo",
	}}
	if got := stripANSI(renderTranscriptTool(failed, 90, false, nil)[0]); strings.Contains(got, "+1") {
		t.Fatalf("failed edit row reported counts: %q", got)
	}

	read := chatTool{id: "read-1", name: "read", status: "done", args: map[string]any{"path": "a.go"}}
	if got := stripANSI(renderTranscriptTool(read, 90, false, nil)[0]); got != "✓ Read a.go" {
		t.Fatalf("read row = %q", got)
	}
}

func TestEditLineCountsMatchDiffHunks(t *testing.T) {
	for _, test := range []struct {
		name             string
		oldText, newText string
		added, removed   int
	}{
		{name: "single replacement", oldText: "a\nb\nc\nd", newText: "a\nB\nc\nd", added: 1, removed: 1},
		{name: "removal only", oldText: "keep\nremove me\nkeep2", newText: "keep\nkeep2", added: 0, removed: 1},
		{name: "insertion only", oldText: "start\nend", newText: "start\nnew\nend", added: 1, removed: 0},
		{name: "whole block replaced", oldText: "one\ntwo", newText: "three\nfour", added: 2, removed: 2},
		{name: "trailing newline is a terminator", oldText: "a\n", newText: "a\nb\n", added: 1, removed: 0},
		{name: "crlf matches lf", oldText: "a\r\nb", newText: "a\nc", added: 1, removed: 1},
		{name: "changed around shared context", oldText: "a\nkeep\nb", newText: "A\nkeep\nB", added: 2, removed: 2},
		{name: "reordered context", oldText: "keep\ndrop\nkeep2", newText: "keep\nkeep2\nadded", added: 1, removed: 1},
	} {
		added, removed := countLineChanges(test.oldText, test.newText)
		if added != test.added || removed != test.removed {
			t.Fatalf("%s: counted +%d -%d, want +%d -%d", test.name, added, removed, test.added, test.removed)
		}
	}
}

func TestEditRowsCountStreamedArgumentJSON(t *testing.T) {
	tool := chatTool{
		id: "edit-1", name: "edit", status: "done",
		args: map[string]any{
			"path":  "a.go",
			"edits": `[{"oldText":"keep\ndrop\nkeep2","newText":"keep\nkeep2\nadded"}]`,
		},
	}
	if got, want := stripANSI(renderTranscriptTool(tool, 90, false, nil)[0]), "✓ Edited a.go +1 -1"; got != want {
		t.Fatalf("streamed-argument edit row = %q, want %q", got, want)
	}
	if got := stripANSI(renderTranscriptTool(chatTool{id: "e", name: "edit", status: "done", args: map[string]any{"path": "a.go"}}, 90, false, nil)[0]); got != "✓ Edited a.go" {
		t.Fatalf("edit row without arguments = %q", got)
	}
}

func TestTranscriptRendersCompactionCheckpointsAsNoticeRows(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, InitialMessages: []ai.Message{
		ai.NewUserMessage("old question", time.Unix(1, 0)),
		ai.NewSyntheticMessage("Compaction summary of earlier conversation:\n\n## Goal\nold work", time.Unix(2, 0)),
		ai.NewUserMessage("recent question", time.Unix(3, 0)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(strings.Join(chat.renderTranscript(80), "\n"))
	if !strings.Contains(plain, "Compacted earlier context into a summary") {
		t.Fatalf("checkpoint row missing:\n%s", plain)
	}
	if strings.Contains(plain, "## Goal") {
		t.Fatalf("checkpoint body leaked into the transcript:\n%s", plain)
	}
	if !strings.Contains(plain, "recent question") {
		t.Fatalf("kept prompt missing:\n%s", plain)
	}
}

func TestCompactionRendersAsAToolRowWithItsSizes(t *testing.T) {
	tool := chatTool{
		id: "compact:1", name: "compact", status: "done",
		args: map[string]any{"before": float64(120000), "after": float64(45000)},
	}
	if got, want := toolActionText(tool, true, nil), "Compaction Successful 120000 tokens -> 45000 tokens (62%)"; got != want {
		t.Fatalf("compaction row = %q, want %q", got, want)
	}
	// A failure keeps the same shape, with the reason and the error glyph.
	failed := tool
	failed.isError = true
	failed.args = map[string]any{"before": float64(120000), "after": float64(120000), "error": "the summarizer returned no text"}
	if got, want := toolActionText(failed, true, nil), "Compaction Failed: the summarizer returned no text"; got != want {
		t.Fatalf("failed compaction row = %q, want %q", got, want)
	}
	bare := failed
	bare.args = map[string]any{"before": float64(1), "after": float64(1)}
	if got := toolActionText(bare, true, nil); got != "Compaction Failed" {
		t.Fatalf("failed compaction row without a reason = %q", got)
	}
	failedLines := renderTranscriptTool(failed, 80, false, nil)
	if !strings.Contains(stripANSI(failedLines[0]), "✗") || !strings.Contains(stripANSI(failedLines[0]), "Compaction Failed") {
		t.Fatalf("failed compaction row rendered = %#v", failedLines)
	}
	running := tool
	running.status = "running"
	if got := toolActionText(running, false, nil); got != "Compacting…" {
		t.Fatalf("in-flight compaction row = %q", got)
	}
	// It renders through the same path as any other tool, with the done glyph.
	lines := renderTranscriptTool(tool, 80, false, nil)
	if len(lines) == 0 || !strings.Contains(stripANSI(lines[0]), "Compaction Successful") || !strings.Contains(stripANSI(lines[0]), "✓") {
		t.Fatalf("rendered lines = %#v", lines)
	}
}

func TestWritingCommandLabelIsWhiteAndUsesThreeDots(t *testing.T) {
	partial := ai.AssistantMessage{Content: []ai.Content{ai.NewToolCall("call_1", "bash", map[string]any{"command": "go test ./..."})}}
	label := writingToolLabel(&partial)
	if label != "Writing Command…" {
		t.Fatalf("label = %q", label)
	}
	if strings.Count(label, ".") != 0 || strings.Count(label, "…") != 1 {
		t.Fatalf("label must use a single three-dot ellipsis, not periods: %q", label)
	}
	// A write tool does not claim the command label.
	other := ai.AssistantMessage{Content: []ai.Content{ai.NewToolCall("call_2", "write", map[string]any{"path": "main.go"})}}
	if got := writingToolLabel(&other); got != "" {
		t.Fatalf("write tool label = %q", got)
	}
	if got := writingToolLabel(nil); got != "" {
		t.Fatalf("nil partial label = %q", got)
	}

	// The live row renders it in plain white, not as a styled verb.
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.writingLabel = label
	chat.busy = true
	chat.active = 0
	chat.entries = []chatEntry{{kind: chatAssistant, startedAt: 1}}
	snapshot := chat.liveSnapshotLocked(80, time.Now())
	if snapshot.action.label != label || snapshot.action.tone != liveToneWriting {
		t.Fatalf("live action = %#v", snapshot.action)
	}
	// The row carries content markers, so compare after removing them.
	rendered := stripContentMarkers(strings.Join(rendererForTest(chat).renderLiveStatus(snapshot, 0), "\n"))
	if !strings.Contains(rendered, CurrentTheme().FG("text", label)) {
		t.Fatalf("live row is not white:\n%q", rendered)
	}
	// It must not be styled as a running action (verb colouring) instead.
	if strings.Contains(rendered, CurrentTheme().FG("toolTitle", "Writing")) {
		t.Fatalf("live row used the running style:\n%q", rendered)
	}
}

// stripContentMarkers removes the renderer's content markers so a test can
// compare styled text directly.
func stripContentMarkers(value string) string {
	for {
		start := strings.Index(value, "\x1b]777;midas-content-start\a")
		if start < 0 {
			return strings.ReplaceAll(value, "\x1b]777;midas-content-end\a", "")
		}
		value = value[:start] + value[start+len("\x1b]777;midas-content-start\a"):]
	}
}

// rendererForTest builds a transcript renderer at a fixed width.
func rendererForTest(*Chat) *transcriptRenderer {
	return &transcriptRenderer{width: 80}
}

// TestQRBlocksAreCentredAndBounded covers how a rendered QR code reaches the
// terminal: whole, centred, and never larger than the row budget.
func TestQRBlocksAreCentredAndBounded(t *testing.T) {
	// A small code, as the hub renders it: half blocks and spaces only.
	rows := []string{}
	for index := 0; index < 12; index++ {
		rows = append(rows, "  ██▀▀██  ██  ▄▄██  ▀▀  ██  ")
	}
	rendered, ok := renderQRBlock(rows, 80)
	if !ok {
		t.Fatal("a QR block was not recognised")
	}
	if len(rendered) != len(rows) {
		t.Fatalf("rendered %d lines for %d rows", len(rendered), len(rows))
	}
	first := stripANSI(rendered[0])
	leading := len(first) - len(strings.TrimLeft(first, " "))
	// The renderer trims each row, so the visible block is the trimmed width.
	if want := (qrAvailableWidth(80) - len([]rune(strings.TrimSpace(rows[0])))) / 2; leading != want {
		t.Fatalf("leading gap = %d, want %d", leading, want)
	}
	if leading <= 0 {
		t.Fatalf("QR block is not indented: %q", first)
	}
	if strings.Trim(strings.TrimSpace(first), " █▀▄") != "" {
		t.Fatalf("QR row carries non-block content: %q", first)
	}
	// Ordinary output is never mistaken for a code.
	if _, ok := renderQRBlock([]string{"$ go test ./...", "ok  github.com/x/y"}, 80); ok {
		t.Fatal("command output was rendered as a QR block")
	}

	// An oversized code is replaced by a note rather than clipped into nonsense.
	tall := make([]string, 0, qrBlockMaxRows+2)
	for index := 0; index < qrBlockMaxRows+2; index++ {
		tall = append(tall, rows[0])
	}
	lines := appendTranscriptPreview(nil, strings.Join(tall, "\n"), false, 80)
	if len(lines) != 1 || !strings.Contains(stripANSI(lines[0]), "QR code not shown") {
		t.Fatalf("oversized QR = %#v", lines)
	}
	lines = appendTranscriptPreview(nil, "hello\nworld", false, 80)
	if len(lines) != 2 || !strings.Contains(stripANSI(lines[0]), "hello") {
		t.Fatalf("ordinary output = %#v", lines)
	}
}
