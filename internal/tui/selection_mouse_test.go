package tui

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// mountedChatAltScreen mounts the real chat viewport on a fake terminal so
// mouse tests exercise the same dispatch path the running TUI uses.
type mountedChatAltScreen struct {
	chat     *Chat
	screen   *TuiAltScreen
	terminal *fakeTerminal
}

func mountChatAltScreen(t *testing.T, chat *Chat, columns, rows int) *mountedChatAltScreen {
	t.Helper()
	terminal := newFakeTerminal(columns, rows)
	screen := NewTuiAltScreen(terminal, TuiAltScreenOptions{
		Mouse: boolPointer(true), Scheduler: &fakeScheduler{}, ShowHardwareCursor: true,
		IsMultiplexer: func() bool { return false },
	})
	screen.SetLayoutRoot(NewChatViewport(chat).Root)
	screen.SetFocus(chat)
	screen.Start()
	screen.RenderNow(false)
	return &mountedChatAltScreen{chat: chat, screen: screen, terminal: terminal}
}

func (m *mountedChatAltScreen) box(kind string) *LayoutBox {
	var found *LayoutBox
	var visit func(*LayoutBox)
	visit = func(box *LayoutBox) {
		if part, ok := box.Component.(*chatPart); ok && part.kind == kind {
			found = box
		}
		for _, child := range box.Children {
			visit(child)
		}
	}
	if m.screen.currentLayout != nil {
		visit(m.screen.currentLayout.Root)
	}
	return found
}

// press, drag and release use one-based terminal coordinates, like the decoder.
func (m *mountedChatAltScreen) press(x, y int) {
	m.terminal.onInput(fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1))
}

func (m *mountedChatAltScreen) drag(x, y int) {
	m.terminal.onInput(fmt.Sprintf("\x1b[<32;%d;%dM", x+1, y+1))
}

func (m *mountedChatAltScreen) release(x, y int) {
	m.terminal.onInput(fmt.Sprintf("\x1b[<0;%d;%dm", x+1, y+1))
}

func (m *mountedChatAltScreen) copiedText(t *testing.T) string {
	t.Helper()
	for _, entry := range m.terminal.log {
		index := strings.Index(entry, "\x1b]52;c;")
		if index < 0 {
			continue
		}
		payload := entry[index+len("\x1b]52;c;"):]
		payload = strings.TrimSuffix(payload, "\x07")
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			t.Fatalf("clipboard payload = %q: %v", payload, err)
		}
		return string(decoded)
	}
	return ""
}

func editorChat(t *testing.T, text string) *Chat {
	t.Helper()
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, Model: "m", Provider: "p", CWD: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	chat.editor.SetText(text)
	return chat
}

func TestClickingTheInputBoxMovesTheCaretToTheClick(t *testing.T) {
	chat := editorChat(t, "hello brave new world")
	mounted := mountChatAltScreen(t, chat, 80, 20)
	defer mounted.screen.Stop(StopOptions{PreserveScreen: true})
	editor := mounted.box("editor")
	if editor == nil {
		t.Fatal("editor box was not laid out")
	}
	y := editor.Rect.Y + 1
	content := editor.Rect.X + Padding() + 2
	for _, offset := range []int{0, 8, 18} {
		mounted.press(content+offset, y)
		mounted.release(content+offset, y)
	}
	// The last press landed on the 19th cell of the content row.
	if got := chat.editor.Cursor(); got != 18 {
		t.Fatalf("caret after clicks = %d, want 18", got)
	}
	if mounted.screen.HasActiveSelection() {
		t.Fatal("a plain click left a highlight behind")
	}
	if copied := mounted.copiedText(t); copied != "" {
		t.Fatalf("a plain click copied %q", copied)
	}
}

func TestDraggingTheInputBoxHighlightsAndClearsOnRelease(t *testing.T) {
	chat := editorChat(t, "hello brave new world")
	mounted := mountChatAltScreen(t, chat, 80, 20)
	defer mounted.screen.Stop(StopOptions{PreserveScreen: true})
	editor := mounted.box("editor")
	if editor == nil {
		t.Fatal("editor box was not laid out")
	}
	y := editor.Rect.Y + 1
	content := editor.Rect.X + Padding() + 2
	mounted.press(content, y)
	mounted.drag(content+10, y)
	selected, ok := mounted.screen.GetActiveSelectionText()
	if !ok || !strings.HasPrefix(selected, "hello brav") {
		t.Fatalf("mid-drag selection = %q, ok=%v", selected, ok)
	}
	mounted.release(content+10, y)
	if copied := mounted.copiedText(t); copied != selected {
		t.Fatalf("copied %q, want %q", copied, selected)
	}
	if mounted.screen.HasActiveSelection() {
		t.Fatal("the highlight outlived the drag")
	}
}

func TestDraggingTheTranscriptHighlightsAndClearsOnRelease(t *testing.T) {
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Model: "m", Provider: "p",
		InitialMessages: []ai.Message{
			ai.NewUserMessage("transcript line to select", time.UnixMilli(1_000)),
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("selectable answer")}, Timestamp: 1_100},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mounted := mountChatAltScreen(t, chat, 80, 24)
	defer mounted.screen.Stop(StopOptions{PreserveScreen: true})
	document := mounted.box("document")
	if document == nil {
		t.Fatal("document box was not laid out")
	}
	// Find the transcript row holding the answer text.
	row := -1
	for offset := 0; offset < document.Clip.Height; offset++ {
		y := document.Clip.Y + offset
		line := strings.Join(strings.Fields(stripANSI(mounted.screen.previousScreen[y])), " ")
		if strings.Contains(line, "selectable answer") {
			row = y
			break
		}
	}
	if row < 0 {
		t.Fatal("transcript row was not rendered")
	}
	mounted.press(document.Rect.X+2, row)
	mounted.drag(document.Rect.X+10, row)
	if !mounted.screen.HasActiveSelection() {
		t.Fatal("dragging the transcript selected nothing")
	}
	mounted.release(document.Rect.X+10, row)
	if mounted.screen.HasActiveSelection() {
		t.Fatal("the transcript highlight outlived the drag")
	}
}

// menuRowPanel is the smallest panel body with row actions: it renders a couple
// of rows and records the row a click landed on, the way a real list panel does.
type menuRowPanel struct {
	rows  []string
	menu  []string
	picks []int
}

func (p *menuRowPanel) Render(int) []string { return append([]string(nil), p.rows...) }
func (p *menuRowPanel) Invalidate()         {}

func (p *menuRowPanel) HandleMouse(event MouseEvent) *MouseResult {
	if event.Type == MousePress {
		if event.Y >= 0 && event.Y < len(p.rows) {
			p.menu = append(p.menu, p.rows[event.Y])
			p.picks = append(p.picks, event.Y)
			return &MouseResult{Handled: true, Capture: true, Selectable: true}
		}
	}
	return &MouseResult{Handled: true, Capture: true, Selectable: true}
}

func TestDraggingAPanelSelectsItsTextAndStillActs(t *testing.T) {
	// Panel bodies are text surfaces: a press anchors a selection and the panel
	// keeps its own press handling, so dragging highlights while a click still
	// opens the row's action menu.
	view := &menuRowPanel{rows: []string{"T1  First task", "T2  Second task"}}
	panel := NewPanel("Rows", view)
	rows := plainLines(panel.Render(60))
	row := -1
	for index, line := range rows {
		if strings.Contains(line, "First task") {
			row = index
		}
	}
	if row < 0 {
		t.Fatalf("row was not rendered: %#v", rows)
	}
	terminal := newFakeTerminal(60, 12)
	screen := NewTuiAltScreen(terminal, TuiAltScreenOptions{
		Mouse: boolPointer(true), Scheduler: &fakeScheduler{}, IsMultiplexer: func() bool { return false },
	})
	screen.SetLayoutRoot(panel)
	screen.Start()
	screen.RenderNow(false)

	// Drag from the row's id across its title: the text highlights.
	terminal.onInput(fmt.Sprintf("\x1b[<0;4;%dM", row+1))
	terminal.onInput(fmt.Sprintf("\x1b[<32;20;%dM", row+1))
	selected, ok := screen.GetActiveSelectionText()
	if !ok || !strings.Contains(selected, "First tas") {
		t.Fatalf("panel drag selection = %q, ok=%v", selected, ok)
	}
	terminal.onInput(fmt.Sprintf("\x1b[<0;20;%dm", row+1))
	if screen.HasActiveSelection() {
		t.Fatal("the panel highlight outlived the drag")
	}

	// A plain click on the same row still reaches the panel.
	terminal.onInput(fmt.Sprintf("\x1b[<0;4;%dM", row+1))
	terminal.onInput(fmt.Sprintf("\x1b[<0;4;%dm", row+1))
	if len(view.menu) == 0 {
		t.Fatal("the row click was swallowed by the selection gesture")
	}
	if screen.HasActiveSelection() {
		t.Fatal("a click left a highlight behind")
	}
	screen.Stop(StopOptions{PreserveScreen: true})
}

func TestClickingAWrappedInputRowPositionsTheCaret(t *testing.T) {
	chat := editorChat(t, strings.Repeat("word ", 20))
	mounted := mountChatAltScreen(t, chat, 40, 14)
	defer mounted.screen.Stop(StopOptions{PreserveScreen: true})
	editor := mounted.box("editor")
	if editor == nil {
		t.Fatal("editor box was not laid out")
	}
	if editor.Rect.Height < 4 {
		t.Fatalf("input did not wrap: %+v", editor.Rect)
	}
	// Second display row, first content column.
	content := editor.Rect.X + Padding() + 2
	mounted.press(content, editor.Rect.Y+2)
	mounted.release(content, editor.Rect.Y+2)
	before := chat.editor.text[:chat.editor.Cursor()]
	if before == "" || len(before) >= len(chat.editor.text) {
		t.Fatalf("wrapped click caret = %d (%q)", chat.editor.Cursor(), before)
	}
	if !strings.HasSuffix(before, " ") {
		t.Fatalf("wrapped click did not land on a word boundary: %q", before)
	}
}

func TestDoubleClickingTheInputBoxSelectsAndCopiesTheWord(t *testing.T) {
	chat := editorChat(t, "alpha bravo charlie")
	mounted := mountChatAltScreen(t, chat, 60, 12)
	defer mounted.screen.Stop(StopOptions{PreserveScreen: true})
	editor := mounted.box("editor")
	if editor == nil {
		t.Fatal("editor box was not laid out")
	}
	y := editor.Rect.Y + 1
	// Two clicks inside the double-click interval select the word under the pointer.
	content := editor.Rect.X + Padding() + 2
	mounted.press(content+6, y)
	mounted.release(content+6, y)
	mounted.press(content+6, y)
	selected, ok := mounted.screen.GetActiveSelectionText()
	if !ok || selected != "bravo" {
		t.Fatalf("double-click selection = %q, ok=%v", selected, ok)
	}
	mounted.release(content+6, y)
	if copied := mounted.copiedText(t); copied != "bravo" {
		t.Fatalf("double-click copied %q", copied)
	}
	if mounted.screen.HasActiveSelection() {
		t.Fatal("the word highlight outlived the double click")
	}
}
