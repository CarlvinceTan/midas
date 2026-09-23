package tui

import (
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestEditorSubmitAndNewline(t *testing.T) {
	var submitted string
	editor := NewEditor(EditorOptions{OnSubmit: func(value string) { submitted = value }})
	editor.HandleInput("hello")
	editor.HandleInput("\x1b[13;2~")
	editor.HandleInput("world")
	if got := editor.Text(); got != "hello\nworld" {
		t.Fatalf("multiline text = %q", got)
	}
	editor.HandleInput("\r")
	if submitted != "hello\nworld" {
		t.Fatalf("submitted = %q", submitted)
	}
	if editor.Text() != "" {
		t.Fatalf("editor was not cleared: %q", editor.Text())
	}
}

func TestEditorGraphemeSafeMovementAndDeletion(t *testing.T) {
	editor := NewEditor(EditorOptions{})
	editor.SetText("a👨‍👩‍👧‍👦é")
	editor.HandleInput("\x7f")
	if got := editor.Text(); got != "a👨‍👩‍👧‍👦" {
		t.Fatalf("combining grapheme delete = %q", got)
	}
	editor.HandleInput("\x7f")
	if got := editor.Text(); got != "a" {
		t.Fatalf("emoji grapheme delete = %q", got)
	}
}

func TestEditorLargePasteIsAtomicAndExpandedOnSubmit(t *testing.T) {
	paste := strings.Repeat("line\n", 20)
	var submitted string
	editor := NewEditor(EditorOptions{OnSubmit: func(value string) { submitted = value }})
	editor.HandleInput(pasteStart + paste + pasteEnd)
	if got := editor.Text(); got != "[paste #1 +21 lines]" {
		t.Fatalf("marker = %q", got)
	}
	if got := editor.ExpandedText(); got != paste {
		t.Fatalf("expanded paste differs: %q", got)
	}
	editor.HandleInput("\x7f")
	if editor.Text() != "" {
		t.Fatalf("paste marker was not deleted atomically: %q", editor.Text())
	}

	editor.HandleInput(pasteStart + paste + pasteEnd)
	editor.HandleInput("\r")
	if submitted != strings.TrimSpace(paste) {
		t.Fatalf("submitted paste differs: %q", submitted)
	}
}

func TestEditorHistoryPreservesDraft(t *testing.T) {
	editor := NewEditor(EditorOptions{})
	editor.AddToHistory("older")
	editor.AddToHistory("newer")
	editor.SetText("draft")
	editor.HandleInput("\x1b[A")
	if got := editor.Text(); got != "newer" {
		t.Fatalf("previous history = %q", got)
	}
	editor.HandleInput("\x1b[A")
	if got := editor.Text(); got != "older" {
		t.Fatalf("older history = %q", got)
	}
	editor.HandleInput("\x1b[B")
	editor.HandleInput("\x1b[B")
	if got := editor.Text(); got != "draft" {
		t.Fatalf("restored draft = %q", got)
	}
}

func TestEditorRenderWrapsAndKeepsCursorVisible(t *testing.T) {
	editor := NewEditor(EditorOptions{Rows: func() int { return 20 }})
	editor.SetFocused(true)
	editor.SetText("one two three four five six seven eight nine ten")
	lines := editor.Render(12)
	if len(lines) < 4 {
		t.Fatalf("expected wrapped editor, got %#v", lines)
	}
	for _, line := range lines {
		if width := tuitext.VisibleWidth(line); width != 12 {
			t.Fatalf("line width = %d for %q", width, line)
		}
	}
	foundCursor := false
	for _, line := range lines {
		if strings.Contains(line, CursorMarker) {
			foundCursor = true
		}
	}
	if !foundCursor {
		t.Fatal("focused editor did not emit cursor marker")
	}
}

func TestEditorUndoCoalescesTypedWords(t *testing.T) {
	editor := NewEditor(EditorOptions{})
	editor.HandleInput("hello")
	editor.HandleInput(" ")
	editor.HandleInput("world")
	editor.HandleInput("\x1f") // ctrl+- legacy encoding
	if got := editor.Text(); got != "hello" {
		t.Fatalf("undo word = %q", got)
	}
}

func TestEditorSlashMenuMatchesLegacyKeyboardBehavior(t *testing.T) {
	var submitted string
	editor := NewEditor(EditorOptions{
		SlashCommands: []SlashCommand{
			{Name: "model", Description: "Select the active model"},
			{Name: "agents", Description: "Set the active agent profile"},
			{Name: "stats", Description: "Token usage and spend for Midas"},
		},
		OnSubmit: func(value string) { submitted = value },
	})

	editor.HandleInput("/")
	if !editor.SlashMenuVisible() {
		t.Fatal("typing / did not open the slash menu")
	}
	menu := tuitext.StripTerminalSequences(strings.Join(editor.Render(80), "\n"))
	for _, want := range []string{"→ model", "Select the active model", "agents", "stats"} {
		if !strings.Contains(menu, want) {
			t.Fatalf("slash menu missing %q:\n%s", want, menu)
		}
	}

	editor.HandleInput("\x1b[B")
	editor.HandleInput("\t")
	if got := editor.Text(); got != "/agents " {
		t.Fatalf("Tab completion = %q", got)
	}
	if editor.SlashMenuVisible() {
		t.Fatal("Tab completion left the slash menu open")
	}

	editor.SetText("")
	editor.HandleInput("/sta")
	editor.HandleInput("\r")
	if submitted != "/stats" {
		t.Fatalf("Enter submitted %q", submitted)
	}
	if editor.Text() != "" {
		t.Fatalf("editor was not cleared after command submit: %q", editor.Text())
	}
}

func TestEditorSlashMenuEscapeOnlyClosesMenu(t *testing.T) {
	editor := NewEditor(EditorOptions{SlashCommands: NativeSlashCommands})
	editor.HandleInput("/")
	editor.HandleInput("\x1b")
	if editor.SlashMenuVisible() {
		t.Fatal("Escape did not close slash menu")
	}
	if got := editor.Text(); got != "/" {
		t.Fatalf("Escape cleared editor text: %q", got)
	}
}

func TestNativeSlashCommandsExcludeHelpAndClear(t *testing.T) {
	foundRemote := false
	for _, command := range NativeSlashCommands {
		if command.Name == "help" || command.Name == "clear" {
			t.Fatalf("removed command remains in catalog: /%s", command.Name)
		}
		if command.Name == "remote" {
			foundRemote = true
		}
	}
	if !foundRemote {
		t.Fatal("/remote is missing from native slash commands")
	}
}

func TestEditorActivatesShellPrefixWhileTyping(t *testing.T) {
	editor := NewEditor(EditorOptions{})
	editor.HandleInput("!")
	if editor.Text() != "! " || editor.Cursor() != 2 {
		t.Fatalf("first bang = %q cursor=%d", editor.Text(), editor.Cursor())
	}
	editor.HandleInput("!")
	if editor.Text() != "!! " || editor.Cursor() != 3 {
		t.Fatalf("double bang = %q cursor=%d", editor.Text(), editor.Cursor())
	}
	editor.SetText("! ")
	editor.HandleInput("\x7f")
	if editor.Text() != "!" {
		t.Fatalf("deleted shell space = %q", editor.Text())
	}
}

func TestEditorSlashMenuCorrectsSingleTypingMistake(t *testing.T) {
	var submitted string
	editor := NewEditor(EditorOptions{
		SlashCommands: NativeSlashCommands,
		OnSubmit:      func(value string) { submitted = value },
	})
	editor.HandleInput("/exiit")
	if !editor.SlashMenuVisible() {
		t.Fatal("typing /exiit did not keep the slash menu open")
	}
	menu := tuitext.StripTerminalSequences(strings.Join(editor.Render(80), "\n"))
	if !strings.Contains(menu, "→ exit") {
		t.Fatalf("typo did not select /exit:\n%s", menu)
	}
	editor.HandleInput("\r")
	if submitted != "/exit" {
		t.Fatalf("Enter submitted %q", submitted)
	}
}

func TestEditorSlashMenuDoesNotRenderInlineSuggestion(t *testing.T) {
	editor := NewEditor(EditorOptions{SlashCommands: NativeSlashCommands})
	editor.SetFocused(true)
	editor.HandleInput("/m")
	rendered := strings.Join(editor.Render(80), "\n")
	plain := tuitext.StripTerminalSequences(rendered)
	if strings.Contains(strings.Split(plain, "\n")[1], "/model") {
		t.Fatalf("input line contains inline suggestion:\n%s", plain)
	}
	editor.HandleInput("\x1b[C")
	if got := editor.Text(); got != "/m" {
		t.Fatalf("right arrow accepted suggestion: %q", got)
	}
	editor.HandleInput("\t")
	if got := editor.Text(); got != "/model " {
		t.Fatalf("tab completion = %q", got)
	}
}

func TestEditorSlashMenuUsesOneDarkSelectionColors(t *testing.T) {
	editor := NewEditor(EditorOptions{SlashCommands: NativeSlashCommands})
	editor.HandleInput("/")
	rendered := strings.Join(editor.Render(80), "\n")
	accent := strings.TrimSuffix(CurrentTheme().FG("accent", "→ model"), "\x1b[39m")
	muted := CurrentTheme().FG("muted", strings.Repeat(" ", 6)+"Set the active agent profile")
	if !strings.Contains(rendered, accent) {
		t.Fatalf("selected command does not use accent color: %q", rendered)
	}
	if !strings.Contains(rendered, muted) {
		t.Fatalf("command description does not use muted color: %q", rendered)
	}
}

func TestEditorPastedImagePathBecomesAChip(t *testing.T) {
	editor := NewEditor(EditorOptions{CWD: "/work"})
	editor.HandleInput(pasteStart + "/tmp/Screenshot 2026-09-21 at 12.21.16\u202fpm.png" + pasteEnd)
	if got := editor.Text(); got != "[Image: Screenshot 2026-09-21 at 12.21.16 pm.png] " {
		t.Fatalf("chip = %q", got)
	}
	// The chip is one unit: the trailing space goes first, then the whole label
	// disappears in a single backspace.
	editor.HandleInput("\x7f")
	if got := editor.Text(); got != "[Image: Screenshot 2026-09-21 at 12.21.16 pm.png]" {
		t.Fatalf("trailing space should go first: %q", got)
	}
	editor.HandleInput("\x7f")
	if got := editor.Text(); got != "" {
		t.Fatalf("chip was not deleted atomically: %q", got)
	}
}

func TestEditorPastedFilePathsStayAtomicAndResolveOnSubmit(t *testing.T) {
	editor := NewEditor(EditorOptions{CWD: "/work"})
	editor.HandleInput(pasteStart + "/tmp/notes.md" + pasteEnd)
	editor.HandleInput(pasteStart + "/tmp/other.png" + pasteEnd)
	if got := editor.Text(); got != "[File: notes.md] [Image: other.png] " {
		t.Fatalf("chips = %q", got)
	}
	// Left then backspace removes the image chip, not a character of it.
	editor.HandleInput("\x1b[D")
	editor.HandleInput("\x7f")
	// The chip disappears whole; the spaces that separated it stay, exactly like
	// the pre-port editor.
	if got := editor.Text(); got != "[File: notes.md]  " {
		t.Fatalf("after atomic delete = %q", got)
	}
	editor.HandleInput("\r")
	paths := editor.SubmissionAttachments()
	if paths["[File: notes.md]"] != "/tmp/notes.md" {
		t.Fatalf("submitted attachment paths = %v", paths)
	}
}

func TestEditorRendersAttachmentChipsInYellow(t *testing.T) {
	editor := NewEditor(EditorOptions{CWD: "/work"})
	editor.SetText("[Image: shot.png] look")
	rendered := strings.Join(editor.Render(40), "\n")
	if !strings.Contains(rendered, CurrentTheme().FG("toolTitle", "[Image: shot.png]")) {
		t.Fatalf("chip was not painted yellow: %q", rendered)
	}
}

func TestEditorTypedBracketsAreNotChips(t *testing.T) {
	editor := NewEditor(EditorOptions{CWD: "/work"})
	editor.SetText("[Image: typed.png]")
	editor.HandleInput("\x7f")
	if got := editor.Text(); got != "[Image: typed.png" {
		t.Fatalf("typed brackets should delete character by character: %q", got)
	}
}
