package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestTranscriptScrollsWithoutPaintingScrollbar(t *testing.T) {
	messages := make([]ai.Message, 0, 40)
	for index := range 20 {
		messages = append(messages,
			ai.NewUserMessage(fmt.Sprintf("question %d with enough text to occupy a row", index), time.UnixMilli(int64(index*10+1))),
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(fmt.Sprintf("answer %d", index))}, Timestamp: int64(index*10 + 2)},
		)
	}
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, InitialMessages: messages})
	if err != nil {
		t.Fatal(err)
	}
	viewport := NewChatViewport(chat)
	frame := RenderLayoutFrame(viewport.Root, 80, 18, nil)
	if viewport.Transcript.Scrollbar() != ScrollbarHidden {
		t.Fatalf("transcript scrollbar mode = %q", viewport.Transcript.Scrollbar())
	}
	viewport.Transcript.ScrollBy(-6)
	frame = RenderLayoutFrame(viewport.Root, 80, 18, nil)
	if viewport.Transcript.IsScrollbarVisible() {
		t.Fatal("transcript painted a scrollbar while scrolled")
	}
	var transcriptBox *LayoutBox
	var visit func(*LayoutBox)
	visit = func(box *LayoutBox) {
		if box.ScrollView == viewport.Transcript {
			transcriptBox = box
		}
		for _, child := range box.Children {
			visit(child)
		}
	}
	visit(frame.Root)
	if transcriptBox == nil {
		t.Fatal("transcript layout box was not found")
	}
	if geometry, ok := GetScrollbarGeometry(transcriptBox, true); ok {
		t.Fatalf("transcript reserved scrollbar geometry: %#v", geometry)
	}
	for _, line := range frame.Lines {
		if strings.ContainsAny(stripANSI(line), "┃█") {
			t.Fatalf("frame painted a scrollbar glyph: %q", stripANSI(line))
		}
	}
}

func TestChatViewportPinsMidasChromeAroundScrollableTranscript(t *testing.T) {
	messages := make([]ai.Message, 0, 24)
	for index := range 12 {
		messages = append(messages,
			ai.NewUserMessage("question with enough text to occupy the transcript", time.Unix(int64(index+1), 0)),
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("answer")}},
		)
	}
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Model: "model-id", ModelName: "Midas Model", Thinking: ai.ThinkingHigh,
		SessionTitle: "Port Midas", CWD: "/work/midas", Branch: "main",
		AgentGroups: [][]string{{"main", "orchestrator"}, {"advisor", "explore"}},
		SkillGroups: [][]string{{"research", "notion"}, {"project-build"}},
		MCPNames:    []string{"filesystem"}, InitialMessages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	viewport := NewChatViewport(chat)
	frame := RenderLayoutFrame(viewport.Root, 80, 18, nil)
	plain := unpaddedFrame(frame)
	if !strings.Contains(plain[0], "Port Midas") || !strings.Contains(plain[0], "/work/midas (main)") {
		t.Fatalf("pinned title row = %q", plain[0])
	}
	if !strings.Contains(plain[1], "3 skills • 1 mcps") {
		t.Fatalf("pinned resource row = %q", plain[1])
	}
	joinedDock := strings.Join(plain[len(plain)-5:], "\n")
	if !strings.Contains(joinedDock, "╭") || !strings.Contains(joinedDock, "╯") || !strings.Contains(joinedDock, "Midas Model • high") {
		t.Fatalf("fixed editor/footer dock:\n%s", joinedDock)
	}
	if frame.PrimaryScrollView != viewport.Transcript || viewport.Transcript.ViewportHeight() < 1 {
		t.Fatalf("primary transcript viewport was not laid out: %#v", frame.PrimaryScrollView)
	}
}

func TestScrolledTranscriptDispatchesClicksToWorkedRows(t *testing.T) {
	messages := make([]ai.Message, 0, 28)
	for index := range 10 {
		messages = append(messages,
			ai.NewUserMessage("earlier question", time.UnixMilli(int64(index*100+1))),
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("earlier answer")}, Timestamp: int64(index*100 + 2)},
		)
	}
	messages = append(messages,
		ai.NewUserMessage("inspect the end", time.UnixMilli(2_000)),
		ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{
			ai.NewToolCall("read-last", "read", map[string]any{"path": "README.md"}),
		}, Timestamp: 2_100},
		ai.ToolResultMessage{Role: ai.RoleToolResult, ToolCallID: "read-last", ToolName: "read", Content: []ai.Content{ai.NewText("details")}, Timestamp: 2_200},
		ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("final answer")}, Timestamp: 2_300},
	)
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, InitialMessages: messages})
	if err != nil {
		t.Fatal(err)
	}
	viewport := NewChatViewport(chat)
	frame := RenderLayoutFrame(viewport.Root, 80, 22, nil)
	if viewport.Transcript.ScrollTop() == 0 {
		t.Fatal("fixture did not produce a scrolled transcript")
	}

	chat.mu.Lock()
	var target transcriptTarget
	foundTarget := false
	for _, candidate := range chat.transcriptTargets {
		if candidate.kind == transcriptRunTarget && (!foundTarget || candidate.start > target.start) {
			target, foundTarget = candidate, true
		}
	}
	chat.mu.Unlock()
	if !foundTarget {
		t.Fatal("missing Worked-row target")
	}

	var documentBox *LayoutBox
	var visit func(*LayoutBox)
	visit = func(box *LayoutBox) {
		if part, ok := box.Component.(*chatPart); ok && part.kind == "document" {
			documentBox = box
		}
		for _, child := range box.Children {
			visit(child)
		}
	}
	visit(frame.Root)
	if documentBox == nil {
		t.Fatal("document layout box was not found")
	}
	screenY := documentBox.Rect.Y + len(chat.renderStartup(documentBox.Rect.Width)) + target.start
	if screenY < documentBox.Clip.Y || screenY >= documentBox.Clip.Y+documentBox.Clip.Height {
		t.Fatalf("Worked row is not visible: screenY=%d clip=%#v", screenY, documentBox.Clip)
	}

	var result *MouseResult
	for _, box := range GetLayoutBoxesAt(frame, 1, screenY) {
		if _, structural := GetLayoutNode(box.Component); structural {
			continue
		}
		local := MouseEvent{
			Type: MouseClick, Button: MouseLeft, ScreenX: 1, ScreenY: screenY,
			X: 1 - box.Rect.X, Y: screenY - box.Rect.Y, Width: box.Rect.Width, Height: box.Rect.Height,
		}
		if result = DispatchMouseEvent(box.Component, local); result != nil {
			break
		}
	}
	if result == nil || !result.Handled {
		t.Fatal("scrolled Worked-row click was not dispatched")
	}
	plain := stripANSI(strings.Join(chat.renderTranscript(80), "\n"))
	if !strings.Contains(plain, "- Worked") || !strings.Contains(plain, "✓ Read README.md") {
		t.Fatalf("scrolled click did not expand the run:\n%s", plain)
	}
}

func TestCompactHeaderKeepsTitlePathAndDivider(t *testing.T) {
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, SessionTitle: "Compact Midas", CWD: "/work/midas", Branch: "feature",
		CompactHeader: true, SkillGroups: [][]string{{"research", "notion"}}, MCPNames: []string{"filesystem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	header := plainLines(chat.renderHeader(64))
	if len(header) != 2 {
		t.Fatalf("compact header rows = %d, want 2: %#v", len(header), header)
	}
	if got, want := header[0], " Compact Midas                                      /work/midas"; got != want {
		t.Fatalf("compact header = %q, want %q", got, want)
	}
	if got, want := header[1], " "+strings.Repeat("─", 62); got != want {
		t.Fatalf("compact header divider = %q, want %q", got, want)
	}
	if strings.Contains(header[0], "Idle") || strings.Contains(header[0], "skills") || strings.Contains(header[0], "feature") {
		t.Fatalf("compact header leaked normal metadata: %q", header[0])
	}

	viewport := NewChatViewport(chat)
	frame := RenderLayoutFrame(viewport.Root, 64, 14, nil)
	rows := unpaddedFrame(frame)
	for row := range header {
		if got := rows[row]; got != header[row] {
			t.Fatalf("compact header row %d = %q, want %q", row, got, header[row])
		}
	}

	chat.Notice("Settings changed.")
	toasted := plainLines(chat.renderHeader(64))
	if len(toasted) != 2 || !strings.Contains(toasted[0], "Settings changed") {
		t.Fatalf("compact header toast = %#v", toasted)
	}
	if toasted[1] != header[1] {
		t.Fatalf("compact header toast replaced the divider: %#v", toasted)
	}
}

func TestChatViewportShowsNoticesAsToastsOnly(t *testing.T) {
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, ContextPaths: []string{"./.midas/AGENTS.md"},
		AgentGroups: [][]string{{"main"}}, SkillGroups: [][]string{{"research", "notion"}}, MCPNames: []string{"github"},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.Notice("Settings changed.")
	if transcript := stripANSI(strings.Join(chat.renderTranscript(72), "\n")); transcript != "" {
		t.Fatalf("UI notice leaked into transcript: %q", transcript)
	}
	startup := stripANSI(strings.Join(chat.renderStartup(72), "\n"))
	for _, want := range []string{"[Context]", "[Agents]", "[Skills]", "[MCPs]", "research", "notion"} {
		if !strings.Contains(startup, want) {
			t.Fatalf("startup summary missing %q:\n%s", want, startup)
		}
	}
	header := stripANSI(strings.Join(chat.renderHeader(72), "\n"))
	if !strings.Contains(header, "Settings changed") {
		t.Fatalf("header toast missing notice: %q", header)
	}
	rendered := stripANSI(strings.Join(chat.Render(72), "\n"))
	if strings.Count(rendered, "Settings changed") != 1 {
		t.Fatalf("notice rendered outside the toast: %q", rendered)
	}
}

func TestChatViewportRendersSlashMenuBelowRoundedEditor(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetFocused(true)
	chat.HandleInput("/")
	lines := chat.renderEditorDock(80)
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = tuitext.StripTerminalSequences(line)
	}
	if len(plain) < 8 {
		t.Fatalf("slash menu was not rendered: %#v", plain)
	}
	// The frame and its menu sit inside the row gutter (padding defaults to one).
	if !strings.HasPrefix(plain[0], " ╭") || !strings.HasPrefix(plain[1], " │ /") || !strings.HasPrefix(plain[2], " ╰") {
		t.Fatalf("editor frame geometry differs from legacy Midas: %#v", plain[:3])
	}
	if !strings.HasPrefix(plain[3], " → model") || strings.Contains(plain[3], "│") {
		t.Fatalf("slash menu should sit below, not inside, the editor frame: %q", plain[3])
	}
}

func TestChatViewportKeepsBlankEditorBodyInsideFrame(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetFocused(true)
	lines := chat.renderEditorDock(80)
	if len(lines) != 3 {
		t.Fatalf("blank editor height = %d, want 3: %#v", len(lines), lines)
	}
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = tuitext.StripTerminalSequences(strings.ReplaceAll(line, CursorMarker, ""))
	}
	if !strings.HasPrefix(plain[0], " ╭") || !strings.HasPrefix(plain[1], " │") || !strings.HasSuffix(strings.TrimRight(plain[1], " "), "│") || !strings.HasPrefix(plain[2], " ╰") {
		t.Fatalf("blank editor frame is malformed: %#v", plain)
	}
	if strings.HasPrefix(plain[2], "─") {
		t.Fatalf("unframed editor rule leaked below the box: %#v", plain)
	}
}

func TestFormatModelNameUsesModelPartAndCanonicalDeepSeekCasing(t *testing.T) {
	if got := formatModelName("deepseek/deepseek-v4.1-flash"); got != "DeepSeek V4.1 Flash" {
		t.Fatalf("formatted model = %q", got)
	}
}

func TestChatChromeMatchesLegacyMidasCellGeometry(t *testing.T) {
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Model: "deepseek/deepseek-v4.1-flash", ModelName: "DeepSeek V4.1 Flash",
		ModelContext: 151071, Thinking: ai.ThinkingHigh, SessionTitle: "Port Midas", CWD: "/work/midas", Branch: "main",
		ContextPaths: []string{"~/.midas/AGENTS.md"},
		AgentGroups:  [][]string{{"main", "orchestrator"}, {"advisor", "explore"}},
		SkillGroups:  [][]string{{"research", "notion", "project-build"}}, MCPNames: []string{"filesystem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.mu.Lock()
	chat.busy = true
	chat.rateDisplay.value, chat.rateDisplay.has = 83.25, true
	// The ctx pair is the context the last turn left behind; cost stays cumulative.
	chat.contextTokens, chat.contextKnown = 42300, true
	chat.usage = ai.Usage{Cost: ai.UsageCost{Total: 0.045}}
	chat.mu.Unlock()

	header := plainLines(chat.renderHeader(80))
	wantHeader := []string{
		" Port Midas                                                  /work/midas (main)",
		" Working                                                      3 skills • 1 mcps",
		" ──────────────────────────────────────────────────────────────────────────────",
	}
	if strings.Join(header, "\n") != strings.Join(wantHeader, "\n") {
		t.Fatalf("header geometry:\n%q\nwant:\n%q", header, wantHeader)
	}

	startup := plainLines(chat.renderStartup(80))
	wantStartup := []string{
		" [Context]           [Agents]            [Skills]            [MCPs]",
		" ~/.midas/AGENTS.md  main                research            filesystem",
		"                     orchestrator        notion",
		"                                         project-build",
		"                     advisor",
		"                     explore",
	}
	if strings.Join(startup, "\n") != strings.Join(wantStartup, "\n") {
		t.Fatalf("startup geometry:\n%q\nwant:\n%q", startup, wantStartup)
	}

	footer := plainLines(chat.renderFooter(80))
	wantFooter := []string{" DeepSeek V4.1 Flash • high                      83.3 t/s • 42.3k (28%) • $0.04 "}
	if strings.Join(footer, "\n") != strings.Join(wantFooter, "\n") {
		t.Fatalf("footer geometry: %q, want %q", footer, wantFooter)
	}
}

func TestPromptCardMatchesLegacyMidasCellGeometry(t *testing.T) {
	got := plainLines(renderPromptCard("first line\nsecond line", 80, func(value string) string { return value }))
	want := []string{
		"╭──────────────────────────────────────────────────────────────────────────────╮",
		"│ first line                                                                   │",
		"│ second line                                                                  │",
		"╰──────────────────────────────────────────────────────────────────────────────╯",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("prompt card geometry:\n%q\nwant:\n%q", got, want)
	}
}

func TestRemoteFooterIndicatorCopiesOnlyWhenClicked(t *testing.T) {
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Model: "deepseek-v4", ModelName: "DeepSeek V4", Thinking: ai.ThinkingHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	copied := 0
	chat.SetRemote("https://midas.trycloudflare.com", func() { copied++ })
	const width = 110
	footer := stripANSI(chat.renderFooter(width)[0])
	start := strings.Index(footer, "Remote active")
	if start < 0 || !strings.Contains(footer, "DeepSeek V4 • high • Remote active") {
		t.Fatalf("remote footer = %q", footer)
	}
	part := &chatPart{chat: chat, kind: "footer"}
	result := part.HandleMouse(MouseEvent{Type: MousePress, Button: MouseLeft, X: start + 2, Y: 0, Width: width, Height: 1})
	if result == nil || !result.Handled || copied != 1 {
		t.Fatalf("remote click result=%#v copied=%d", result, copied)
	}
	if result := part.HandleMouse(MouseEvent{Type: MousePress, Button: MouseLeft, X: 1, Y: 0, Width: width, Height: 1}); result != nil || copied != 1 {
		t.Fatalf("unrelated footer click result=%#v copied=%d", result, copied)
	}
	chat.SetRemote("", nil)
	if footer := stripANSI(chat.renderFooter(width)[0]); strings.Contains(footer, "Remote active") {
		t.Fatalf("remote footer remained after stop: %q", footer)
	}
}

func TestUnframedChatRowsKeepSymmetricOuterGutter(t *testing.T) {
	const width = 32
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, SessionTitle: strings.Repeat("title", 12), CWD: "/a/very/long/path/that/must/fit",
		ContextPaths: []string{strings.Repeat("context", 8)}, InitialQueue: []string{strings.Repeat("queued message ", 8)},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.mu.Lock()
	chat.entries = []chatEntry{{kind: chatAssistant, text: strings.Repeat("assistant ", 12)}}
	chat.mu.Unlock()
	tool := chatTool{
		name: "read", status: "done",
		args: map[string]any{"path": "/" + strings.Repeat("very-long-directory/", 6) + "file.go"},
	}
	activity := (&transcriptRenderer{width: width}).renderActivityItem(
		transcriptActivityItem{tool: &tool}, Padding(), 0, "activity", 0,
	)
	chat.SetFocused(true)
	chat.HandleInput("/")
	commandRows := chat.renderEditorDock(width)
	commandRows = commandRows[min(3, len(commandRows)):]

	groups := map[string][]string{
		"header":     chat.renderHeader(width),
		"startup":    chat.renderStartup(width),
		"transcript": chat.renderTranscript(width),
		"activity":   activity,
		"queue":      chat.renderPending(width),
		"commands":   commandRows,
		"footer":     chat.renderFooter(width),
	}
	for group, lines := range groups {
		for index, line := range plainLines(lines) {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if !strings.HasPrefix(line, " ") {
				t.Fatalf("%s row %d has no left gutter: %q", group, index, line)
			}
			if got := tuitext.VisibleWidth(line); got > width {
				t.Fatalf("%s row %d is %d cells wide, terminal is %d: %q", group, index, got, width, line)
			}
			if got := tuitext.VisibleWidth(strings.TrimRight(line, " ")); got > width-Padding() {
				t.Fatalf("%s row %d writes into the right gutter: %q", group, index, line)
			}
		}
	}
	if row := chat.renderPending(width)[1]; !strings.Contains(row, CurrentTheme().fg["dim"]+"…") {
		t.Fatalf("queue ellipsis did not retain the row colour: %q", row)
	}
	if row := chat.renderStartup(width)[0]; !strings.Contains(row, CurrentTheme().fg["startupHeading"]+"…") {
		t.Fatalf("startup ellipsis did not retain the heading colour: %q", row)
	}
}

// TestRoundedChatFramesTakeTheRowGutter: the padding setting insets every
// surface, frames included, so nothing touches the terminal border unless the
// user asks for padding 0.
func TestRoundedChatFramesTakeTheRowGutter(t *testing.T) {
	const width = 32
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, InitialMessages: []ai.Message{
		ai.NewUserMessage("hello there", time.Now()),
	}})
	if err != nil {
		t.Fatal(err)
	}
	plain := plainLines(chat.renderTranscript(width))
	margin, available := chatRowGeometry(width)
	if margin != Padding() {
		t.Fatalf("margin = %d, want the padding", margin)
	}
	frame := ""
	for _, line := range plain {
		if strings.Contains(line, "╭") {
			frame = line
			break
		}
	}
	if frame == "" {
		t.Fatalf("no prompt card rendered: %#v", plain)
	}
	gutter := strings.Repeat(" ", margin)
	if !strings.HasPrefix(frame, gutter+"╭") {
		t.Fatalf("card does not start at the gutter: %q", frame)
	}
	if got := tuitext.VisibleWidth(frame) - margin; got != available {
		t.Fatalf("card frame width = %d, want %d: %q", got, available, frame)
	}
	editor := plainLines(chat.renderEditorDock(width))
	if !strings.HasPrefix(editor[0], gutter+"╭") {
		t.Fatalf("editor frame does not start at the gutter: %q", editor[0])
	}
	if got := tuitext.VisibleWidth(editor[0]) - margin; got != available {
		t.Fatalf("editor frame width = %d, want %d: %q", got, available, editor[0])
	}
}

func TestNarrowChatRowsNeverOverflowWhileReservingGutterWhenPossible(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, InitialQueue: []string{"queued"}})
	if err != nil {
		t.Fatal(err)
	}
	for width := 1; width <= 8; width++ {
		groups := [][]string{chat.renderHeader(width), chat.renderStartup(width), chat.renderPending(width), chat.renderFooter(width)}
		for _, lines := range groups {
			for _, line := range lines {
				if got := tuitext.VisibleWidth(line); got > width {
					t.Fatalf("row overflows at width %d: got %d for %q", width, got, stripANSI(line))
				}
			}
		}
	}
}

func TestPromptCardSelectionExcludesFrameAndPadding(t *testing.T) {
	lines := renderPromptCard("copy this\nnot the padding", 40, func(value string) string { return value })
	if !strings.Contains(lines[0], tuitext.DecorationMarker) || !strings.Contains(lines[len(lines)-1], tuitext.DecorationMarker) {
		t.Fatal("prompt frame rows are not marked as decoration")
	}
	if !strings.Contains(lines[1], ContentStartMarker) || !strings.Contains(lines[1], ContentEndMarker) {
		t.Fatal("prompt body is missing content selection bounds")
	}

	selection := altSelectionRange{
		start: altSelectionPoint{row: 0, col: 0},
		end:   altSelectionPoint{row: len(lines) - 1, col: 40, boundary: true},
	}
	start, end := selectionColumns(lines[1], 1, selection, 0, 40)
	if start != 2 || end != 11 {
		t.Fatalf("highlight columns = %d..%d, want 2..11", start, end)
	}

	tui := &TuiAltScreen{
		selectionScreen: lines,
		selectionAnchor: &altSelectionPoint{row: 0, col: 0},
		selectionFocus:  &altSelectionPoint{row: len(lines) - 1, col: 40, boundary: true},
	}
	if copied, ok := tui.GetActiveSelectionText(); !ok || copied != "copy this\nnot the padding" {
		t.Fatalf("copied prompt = %q, ok=%v", copied, ok)
	}
}

func TestAssistantSelectionExcludesTranscriptIndentation(t *testing.T) {
	lines := markTranscriptContentLines(renderTranscriptText("first line\nsecond line", 40, 3))
	tui := &TuiAltScreen{
		selectionScreen: lines,
		selectionAnchor: &altSelectionPoint{row: 0, col: 0},
		selectionFocus:  &altSelectionPoint{row: len(lines) - 1, col: 40, boundary: true},
	}
	if copied, ok := tui.GetActiveSelectionText(); !ok || copied != "first line\nsecond line" {
		t.Fatalf("copied assistant text = %q, ok=%v", copied, ok)
	}
}

func TestShellCardSelectionExcludesFrameAndPadding(t *testing.T) {
	exitCode := 0
	lines := renderShellCard(ShellEntry{Command: "printf hi", Output: "hi\n", Status: "complete", ExitCode: &exitCode}, 40)
	tui := &TuiAltScreen{
		selectionScreen: lines,
		selectionAnchor: &altSelectionPoint{row: 0, col: 0},
		selectionFocus:  &altSelectionPoint{row: len(lines) - 1, col: 40, boundary: true},
	}
	if copied, ok := tui.GetActiveSelectionText(); !ok || copied != "$ printf hi\n\nhi" {
		t.Fatalf("copied shell card = %q, ok=%v", copied, ok)
	}
}

func TestDockCommandViewsUseLegacyFullWidthPlacement(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}})
	if err != nil {
		t.Fatal(err)
	}
	panel := NewPanel("Skills", &TextView{Text: "9 available skills"})
	chat.SetDockComponent(panel)
	lines := plainLines(chat.renderDock(80))
	if len(lines) != 3 || tuitext.VisibleWidth(lines[0]) != 80 || !strings.HasPrefix(lines[0], "╭─ Skills ") || !strings.HasPrefix(lines[1], "│ 9 available skills") {
		t.Fatalf("dock panel is not the legacy full-width component: %#v", lines)
	}
	if !chat.ClearDockComponent(panel) || chat.DockComponent() != nil {
		t.Fatal("dock panel did not restore the editor")
	}
}

// unpaddedFrame strips the viewport's border padding rows so layout assertions
// can address Midas chrome from its own top row.
func unpaddedFrame(frame LayoutFrame) []string {
	padding := min(Padding(), len(frame.Lines)/2)
	lines := make([]string, 0, len(frame.Lines)-padding*2)
	for _, line := range frame.Lines[padding : len(frame.Lines)-padding] {
		lines = append(lines, stripANSI(line))
	}
	return lines
}

func plainLines(lines []string) []string {
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = tuitext.StripTerminalSequences(strings.ReplaceAll(line, CursorMarker, ""))
	}
	return plain
}

func TestViewportRoutesQueuedRowClicksToTheEditor(t *testing.T) {
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Provider: "fake", Model: "test",
		InitialQueue: []string{"first queued", "second queued"}, QueueHeld: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	viewport := NewChatViewport(chat)
	frame := RenderLayoutFrame(viewport.Root, 80, 24, nil)

	chat.renderPending(80)
	chat.mu.Lock()
	targets := append([]pendingTarget(nil), chat.pendingTargets...)
	chat.mu.Unlock()
	if len(targets) != 2 {
		t.Fatalf("pending targets = %#v", targets)
	}

	var pendingBox *LayoutBox
	var visit func(*LayoutBox)
	visit = func(box *LayoutBox) {
		if part, ok := box.Component.(*chatPart); ok && part.kind == "pending" {
			pendingBox = box
		}
		for _, child := range box.Children {
			visit(child)
		}
	}
	visit(frame.Root)
	if pendingBox == nil {
		t.Fatal("pending layout box was not found")
	}
	screenY := pendingBox.Rect.Y + targets[0].start
	var result *MouseResult
	for _, box := range GetLayoutBoxesAt(frame, 1, screenY) {
		if _, structural := GetLayoutNode(box.Component); structural {
			continue
		}
		local := MouseEvent{
			Type: MouseClick, Button: MouseLeft, ScreenX: 1, ScreenY: screenY,
			X: 1 - box.Rect.X, Y: screenY - box.Rect.Y, Width: box.Rect.Width, Height: box.Rect.Height,
		}
		if result = DispatchMouseEvent(box.Component, local); result != nil {
			break
		}
	}
	if result == nil || !result.Handled {
		t.Fatal("queued row click was not dispatched")
	}
	if got := chat.Editor().Text(); got != "first queued" {
		t.Fatalf("editor = %q", got)
	}
	chat.mu.Lock()
	queue, edit := append([]string(nil), chat.queue...), chat.queueEdit
	chat.mu.Unlock()
	if edit != 0 || len(queue) != 1 || queue[0] != "second queued" {
		t.Fatalf("queue = %#v edit = %d", queue, edit)
	}
}

// TestOpeningTheSlashMenuDoesNotScrollTheTranscript: the menu takes rows from the
// dock, so the transcript viewport shrinks. Its visible content must stay where it
// was rather than scrolling up, and closing the menu must restore the same view.
func TestOpeningTheSlashMenuDoesNotScrollTheTranscript(t *testing.T) {
	messages := make([]ai.Message, 0, 40)
	for index := 0; index < 20; index++ {
		messages = append(messages,
			ai.NewUserMessage(fmt.Sprintf("question %d", index), time.UnixMilli(int64(index*2))),
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(fmt.Sprintf("answer %d", index))}, StopReason: ai.StopComplete},
		)
	}
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, InitialMessages: messages})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetFocused(true)
	viewport := NewChatViewport(chat)
	const width, height = 60, 20
	before := RenderLayoutFrame(viewport.Root, width, height, nil).Lines
	beforeScroll := viewport.Transcript.currentScrollTop

	chat.HandleInput("/")
	after := RenderLayoutFrame(viewport.Root, width, height, nil).Lines

	if viewport.Transcript.currentScrollTop != beforeScroll {
		t.Fatalf("opening the menu scrolled the transcript: %d -> %d", beforeScroll, viewport.Transcript.currentScrollTop)
	}
	// The transcript region is the rows above the editor frame; the first of them
	// must be the same row the user was looking at.
	firstTranscriptRow := func(lines []string) string {
		for _, line := range lines {
			if strings.Contains(line, "question") || strings.Contains(line, "answer") {
				return stripANSI(line)
			}
		}
		return ""
	}
	if beforeRow, afterRow := firstTranscriptRow(before), firstTranscriptRow(after); beforeRow != afterRow {
		t.Fatalf("transcript moved:\n before %q\n after  %q", beforeRow, afterRow)
	}
	// Closing the menu and clearing the input restores the original frame exactly:
	// the rows the menu borrowed come back with the same content.
	chat.HandleInput("\x1b")
	chat.HandleInput("\x7f")
	restored := RenderLayoutFrame(viewport.Root, width, height, nil).Lines
	if !slices.Equal(before, restored) {
		for index := range min(len(before), len(restored)) {
			if before[index] != restored[index] {
				t.Fatalf("closing the menu did not restore the frame at row %d:\n before %q\n after  %q",
					index, stripANSI(before[index]), stripANSI(restored[index]))
			}
		}
		t.Fatalf("closing the menu changed the frame height: %d -> %d", len(before), len(restored))
	}
}
