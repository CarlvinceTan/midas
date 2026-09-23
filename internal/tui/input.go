package tui

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

// InputOptions configures a single-line editable text input.
type InputOptions struct {
	Prompt           string
	Placeholder      string
	Secret           bool
	PlaceholderStyle func(string) string
	OnSubmit         func(string)
	OnEscape         func()
}

type inputSnapshot struct {
	value  string
	cursor int
}

// Input is the reference-compatible single-line editor used by search and prompts.
type Input struct {
	value            string
	cursor           int
	prompt           string
	placeholder      string
	placeholderStyle func(string) string
	secret           bool
	renderedStartCol int
	onSubmit         func(string)
	onEscape         func()
	focused          bool
	pasteBuffer      string
	inPaste          bool
	killRing         []string
	undo             []inputSnapshot
	lastAction       string
}

func NewInput(options InputOptions) *Input {
	style := options.PlaceholderStyle
	if style == nil {
		style = func(text string) string { return text }
	}
	return &Input{
		prompt: options.Prompt, placeholder: options.Placeholder, placeholderStyle: style, secret: options.Secret,
		onSubmit: options.OnSubmit, onEscape: options.OnEscape,
	}
}

func (i *Input) Value() string { return i.value }

func (i *Input) SetValue(value string) {
	i.value = value
	if i.cursor > len(value) {
		i.cursor = len(value)
	}
}

func (i *Input) IsFocused() bool { return i.focused }

func (i *Input) SetFocused(focused bool) { i.focused = focused }

func (i *Input) Invalidate() {}

func (i *Input) HandleInput(data string) {
	if strings.Contains(data, "\x1b[200~") {
		i.inPaste = true
		i.pasteBuffer = ""
		data = strings.Replace(data, "\x1b[200~", "", 1)
	}
	if i.inPaste {
		i.pasteBuffer += data
		if end := strings.Index(i.pasteBuffer, "\x1b[201~"); end >= 0 {
			content := i.pasteBuffer[:end]
			remaining := i.pasteBuffer[end+len("\x1b[201~"):]
			i.pasteBuffer = ""
			i.inPaste = false
			i.handlePaste(content)
			if remaining != "" {
				i.HandleInput(remaining)
			}
		}
		return
	}

	bindings := GetKeybindings()
	if bindings.Matches(data, "tui.select.cancel") {
		if i.onEscape != nil {
			i.onEscape()
		}
		return
	}
	if bindings.Matches(data, "tui.editor.undo") {
		i.undoLast()
		return
	}
	if bindings.Matches(data, "tui.input.submit") || data == "\n" {
		if i.onSubmit != nil {
			i.onSubmit(i.value)
		}
		return
	}
	if bindings.Matches(data, "tui.editor.deleteCharBackward") {
		i.backspace()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteCharForward") {
		i.deleteForward()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteWordBackward") {
		i.deleteWordBackward()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteWordForward") {
		i.deleteWordForward()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteToLineStart") {
		i.deleteToStart()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteToLineEnd") {
		i.deleteToEnd()
		return
	}
	if bindings.Matches(data, "tui.editor.yank") {
		i.yank()
		return
	}
	if bindings.Matches(data, "tui.editor.yankPop") {
		i.yankPop()
		return
	}
	if bindings.Matches(data, "tui.editor.cursorLeft") {
		i.lastAction = ""
		i.cursor = previousGraphemeBoundary(i.value, i.cursor)
		return
	}
	if bindings.Matches(data, "tui.editor.cursorRight") {
		i.lastAction = ""
		i.cursor = nextGraphemeBoundary(i.value, i.cursor)
		return
	}
	if bindings.Matches(data, "tui.editor.cursorLineStart") {
		i.lastAction = ""
		i.cursor = 0
		return
	}
	if bindings.Matches(data, "tui.editor.cursorLineEnd") {
		i.lastAction = ""
		i.cursor = len(i.value)
		return
	}
	if bindings.Matches(data, "tui.editor.cursorWordLeft") {
		i.moveWordBackward()
		return
	}
	if bindings.Matches(data, "tui.editor.cursorWordRight") {
		i.moveWordForward()
		return
	}
	if printable, ok := DecodeKittyPrintable(data); ok {
		i.insert(printable)
		return
	}
	for _, char := range data {
		if char < 32 || char == 0x7f || (char >= 0x80 && char <= 0x9f) {
			return
		}
	}
	i.insert(data)
}

func (i *Input) HandleMouse(event MouseEvent) *MouseResult {
	if event.Type != MousePress || event.Button != MouseLeft || event.Y != 0 {
		return nil
	}
	targetColumn := i.renderedStartCol + max(0, event.X-2)
	currentColumn := 0
	byteIndex := 0
	i.cursor = len(i.value)
	for _, grapheme := range tuitext.Graphemes(i.value) {
		nextColumn := currentColumn + tuitext.VisibleWidth(grapheme)
		if targetColumn < nextColumn {
			i.cursor = byteIndex
			break
		}
		byteIndex += len(grapheme)
		currentColumn = nextColumn
	}
	i.lastAction = ""
	return &MouseResult{Handled: true, Focus: true}
}

func (i *Input) insert(text string) {
	if isJSWhitespace(text) || i.lastAction != "type-word" {
		i.pushUndo()
	}
	i.lastAction = "type-word"
	i.value = i.value[:i.cursor] + text + i.value[i.cursor:]
	i.cursor += len(text)
}

func (i *Input) backspace() {
	i.lastAction = ""
	if i.cursor <= 0 {
		return
	}
	i.pushUndo()
	start := previousGraphemeBoundary(i.value, i.cursor)
	i.value = i.value[:start] + i.value[i.cursor:]
	i.cursor = start
}

func (i *Input) deleteForward() {
	i.lastAction = ""
	if i.cursor >= len(i.value) {
		return
	}
	i.pushUndo()
	end := nextGraphemeBoundary(i.value, i.cursor)
	i.value = i.value[:i.cursor] + i.value[end:]
}

func (i *Input) deleteToStart() {
	if i.cursor == 0 {
		return
	}
	i.pushUndo()
	deleted := i.value[:i.cursor]
	i.pushKill(deleted, true, i.lastAction == "kill")
	i.lastAction = "kill"
	i.value = i.value[i.cursor:]
	i.cursor = 0
}

func (i *Input) deleteToEnd() {
	if i.cursor >= len(i.value) {
		return
	}
	i.pushUndo()
	deleted := i.value[i.cursor:]
	i.pushKill(deleted, false, i.lastAction == "kill")
	i.lastAction = "kill"
	i.value = i.value[:i.cursor]
}

func (i *Input) deleteWordBackward() {
	if i.cursor == 0 {
		return
	}
	wasKill := i.lastAction == "kill"
	i.pushUndo()
	old := i.cursor
	i.moveWordBackward()
	start := i.cursor
	i.pushKill(i.value[start:old], true, wasKill)
	i.lastAction = "kill"
	i.value = i.value[:start] + i.value[old:]
}

func (i *Input) deleteWordForward() {
	if i.cursor >= len(i.value) {
		return
	}
	wasKill := i.lastAction == "kill"
	i.pushUndo()
	old := i.cursor
	i.moveWordForward()
	end := i.cursor
	i.cursor = old
	i.pushKill(i.value[old:end], false, wasKill)
	i.lastAction = "kill"
	i.value = i.value[:old] + i.value[end:]
}

func (i *Input) pushKill(text string, prepend, accumulate bool) {
	if text == "" {
		return
	}
	if accumulate && len(i.killRing) > 0 {
		last := len(i.killRing) - 1
		if prepend {
			i.killRing[last] = text + i.killRing[last]
		} else {
			i.killRing[last] += text
		}
		return
	}
	i.killRing = append(i.killRing, text)
}

func (i *Input) yank() {
	if len(i.killRing) == 0 || i.killRing[len(i.killRing)-1] == "" {
		return
	}
	i.pushUndo()
	text := i.killRing[len(i.killRing)-1]
	i.value = i.value[:i.cursor] + text + i.value[i.cursor:]
	i.cursor += len(text)
	i.lastAction = "yank"
}

func (i *Input) yankPop() {
	if i.lastAction != "yank" || len(i.killRing) <= 1 {
		return
	}
	i.pushUndo()
	previous := i.killRing[len(i.killRing)-1]
	i.value = i.value[:i.cursor-len(previous)] + i.value[i.cursor:]
	i.cursor -= len(previous)
	i.killRing = append([]string{i.killRing[len(i.killRing)-1]}, i.killRing[:len(i.killRing)-1]...)
	text := i.killRing[len(i.killRing)-1]
	i.value = i.value[:i.cursor] + text + i.value[i.cursor:]
	i.cursor += len(text)
	i.lastAction = "yank"
}

func (i *Input) pushUndo() {
	i.undo = append(i.undo, inputSnapshot{value: i.value, cursor: i.cursor})
}

func (i *Input) undoLast() {
	if len(i.undo) == 0 {
		return
	}
	snapshot := i.undo[len(i.undo)-1]
	i.undo = i.undo[:len(i.undo)-1]
	i.value, i.cursor, i.lastAction = snapshot.value, snapshot.cursor, ""
}

func inputGraphemes(value string) ([]string, []int) {
	graphemes := tuitext.Graphemes(value)
	starts := make([]int, len(graphemes)+1)
	for index, grapheme := range graphemes {
		starts[index+1] = starts[index] + len(grapheme)
	}
	return graphemes, starts
}

func previousGraphemeBoundary(value string, cursor int) int {
	_, starts := inputGraphemes(value[:max(0, min(cursor, len(value)))])
	if len(starts) < 2 {
		return 0
	}
	return starts[len(starts)-2]
}

func nextGraphemeBoundary(value string, cursor int) int {
	cursor = max(0, min(cursor, len(value)))
	graphemes := tuitext.Graphemes(value[cursor:])
	if len(graphemes) == 0 {
		return len(value)
	}
	return cursor + len(graphemes[0])
}

func inputWordClass(grapheme string) int {
	if isJSWhitespace(grapheme) {
		return 0
	}
	for _, char := range grapheme {
		if unicode.IsLetter(char) || unicode.IsNumber(char) || char == '_' {
			return 1
		}
	}
	return 2
}

func (i *Input) moveWordBackward() {
	if i.cursor == 0 {
		return
	}
	i.lastAction = ""
	graphemes, starts := inputGraphemes(i.value[:i.cursor])
	index := len(graphemes) - 1
	for index >= 0 && inputWordClass(graphemes[index]) == 0 {
		index--
	}
	if index < 0 {
		i.cursor = 0
		return
	}
	class := inputWordClass(graphemes[index])
	for index >= 0 && inputWordClass(graphemes[index]) == class {
		index--
	}
	i.cursor = starts[index+1]
}

func (i *Input) moveWordForward() {
	if i.cursor >= len(i.value) {
		return
	}
	i.lastAction = ""
	graphemes, starts := inputGraphemes(i.value[i.cursor:])
	index := 0
	for index < len(graphemes) && inputWordClass(graphemes[index]) == 0 {
		index++
	}
	if index >= len(graphemes) {
		i.cursor = len(i.value)
		return
	}
	class := inputWordClass(graphemes[index])
	for index < len(graphemes) && inputWordClass(graphemes[index]) == class {
		index++
	}
	i.cursor += starts[index]
}

func (i *Input) handlePaste(text string) {
	i.lastAction = ""
	i.pushUndo()
	clean := strings.ReplaceAll(text, "\r\n", "")
	clean = strings.NewReplacer("\r", "", "\n", "", "\t", "    ").Replace(clean)
	i.value = i.value[:i.cursor] + clean + i.value[i.cursor:]
	i.cursor += len(clean)
}

func (i *Input) Render(width int) []string {
	available := width - tuitext.VisibleWidth(i.prompt)
	if available <= 0 {
		return []string{tuitext.TruncateToWidth(i.prompt, width, "", false)}
	}
	if i.value == "" && i.placeholder != "" {
		placeholder := tuitext.TruncateToWidth(i.placeholder, available, "", false)
		graphemes := tuitext.Graphemes(placeholder)
		atCursor := " "
		if len(graphemes) > 0 {
			atCursor = graphemes[0]
		}
		after := placeholder[len(atCursor):]
		marker := ""
		if i.focused {
			marker = CursorMarker
		}
		text := marker + "\x1b[7m" + i.placeholderStyle(atCursor) + "\x1b[27m" + i.placeholderStyle(after)
		return []string{i.prompt + text + strings.Repeat(" ", max(0, available-tuitext.VisibleWidth(text)))}
	}

	displayValue, displayCursor := i.value, i.cursor
	if i.secret {
		graphemes, starts := inputGraphemes(i.value)
		cursorGrapheme := 0
		for index, start := range starts {
			if start <= i.cursor {
				cursorGrapheme = index
			}
		}
		displayValue = strings.Repeat("•", len(graphemes))
		displayCursor = len("•") * cursorGrapheme
	}
	visibleText := ""
	cursorDisplay := displayCursor
	i.renderedStartCol = 0
	totalWidth := tuitext.VisibleWidth(displayValue)
	if totalWidth < available {
		visibleText = displayValue
	} else {
		scrollWidth := available
		if displayCursor == len(displayValue) {
			scrollWidth--
		}
		cursorColumn := tuitext.VisibleWidth(displayValue[:displayCursor])
		if scrollWidth > 0 {
			half := scrollWidth / 2
			start := 0
			if cursorColumn >= half {
				if cursorColumn > totalWidth-half {
					start = max(0, totalWidth-scrollWidth)
				} else {
					start = max(0, cursorColumn-half)
				}
			}
			i.renderedStartCol = start
			visibleText = tuitext.SliceByColumn(displayValue, start, scrollWidth, true)
			before := tuitext.SliceByColumn(displayValue, start, max(0, cursorColumn-start), true)
			cursorDisplay = len(before)
		} else {
			cursorDisplay = 0
		}
	}
	remaining := visibleText[cursorDisplay:]
	graphemes := tuitext.Graphemes(remaining)
	atCursor := " "
	if len(graphemes) > 0 {
		atCursor = graphemes[0]
	}
	before := visibleText[:cursorDisplay]
	after := visibleText[cursorDisplay+min(len(atCursor), len(remaining)):]
	marker := ""
	if i.focused {
		marker = CursorMarker
	}
	text := before + marker + "\x1b[7m" + atCursor + "\x1b[27m" + after
	return []string{i.prompt + text + strings.Repeat(" ", max(0, available-tuitext.VisibleWidth(text)))}
}

// DecodeKittyPrintable decodes plain or Shift-only Kitty CSI-u text input.
func DecodeKittyPrintable(data string) (string, bool) {
	match := kittyCSIUPattern.FindStringSubmatch(data)
	if len(match) != 6 {
		return "", false
	}
	parsed, ok := parseKittySequence(data)
	if !ok || parsed.modifier&^(modifierShift|modifierLocks) != 0 || parsed.modifier&(modifierAlt|modifierCtrl) != 0 {
		return "", false
	}
	codepoint := parsed.codepoint
	if parsed.modifier&modifierShift != 0 && match[2] != "" {
		if shifted, err := strconv.Atoi(match[2]); err == nil {
			codepoint = shifted
		}
	}
	codepoint = normalizeKittyFunctionalCodepoint(codepoint)
	if codepoint < 32 || codepoint > utf8.MaxRune || (codepoint >= 0xd800 && codepoint <= 0xdfff) {
		return "", false
	}
	return string(rune(codepoint)), true
}
