package tui

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

const (
	defaultEditorRows = 40
	pasteStart        = "\x1b[200~"
	pasteEnd          = "\x1b[201~"
)

var pasteMarkerPattern = regexp.MustCompile(`\[paste #(\d+)(?: (?:\+\d+ lines|\d+ chars))?\]`)

// EditorOptions configures the reusable multiline prompt editor. The editor
// intentionally depends on callbacks rather than a concrete renderer so it can
// be embedded in both the regular and alternate-screen TUIs.
type EditorOptions struct {
	PaddingX      int
	Rows          func() int
	BorderStyle   func(string) string
	SlashCommands []SlashCommand
	// CWD resolves pasted `~/` paths into absolute attachment chips.
	CWD           string
	OnSubmit      func(string)
	OnChange      func(string)
	OnEscape      func()
	RequestRender func()
}

// SlashCommand is one command shown by the editor's native slash menu.
type SlashCommand struct {
	Name        string
	Description string
	// Args is the usage hint of a command that accepts arguments, for example
	// "<name>" or "[on|off]". An empty hint marks a command that takes none, so
	// "/name extra" is delivered as an ordinary prompt instead of a command error.
	Args string
}

type editorSnapshot struct {
	text         string
	cursor       int
	pastes       map[int]string
	pasteCounter int
}

type editorVisualLine struct {
	text       string
	start      int
	end        int
	logicalEnd bool
}

// Editor is Midas's multiline terminal editor. Cursor offsets are UTF-8 byte
// offsets, matching Go strings; all movement and deletion remains grapheme-safe.
type Editor struct {
	text   string
	cursor int

	paddingX    int
	rows        func() int
	borderStyle func(string) string
	onSubmit    func(string)
	onChange    func(string)
	onEscape    func()
	request     func()
	focused     bool
	cursorShown bool
	disableSend bool

	slashCommands      []SlashCommand
	slashMatches       []SlashCommand
	slashSelected      int
	slashMenuVisible   bool
	slashRenderedStart int
	slashRenderedCount int

	lastWidth     int
	scroll        int
	lastLayout    []editorVisualLine
	preferredX    int
	hasPreferredX bool

	history      []string
	historyIndex int
	historyDraft string

	undo       []editorSnapshot
	killRing   []string
	lastAction string
	yankStart  int

	inPaste       bool
	pasteBuffer   string
	pastes        map[int]string
	pasteCounter  int
	jumpDirection int

	cwd       string
	chips     map[string]string
	submitted map[string]string
}

func NewEditor(options EditorOptions) *Editor {
	rows := options.Rows
	if rows == nil {
		rows = func() int { return defaultEditorRows }
	}
	style := options.BorderStyle
	if style == nil {
		style = func(value string) string { return value }
	}
	return &Editor{
		paddingX: max(0, options.PaddingX), rows: rows, borderStyle: style,
		onSubmit: options.OnSubmit, onChange: options.OnChange,
		onEscape: options.OnEscape, request: options.RequestRender,
		historyIndex: -1, pastes: make(map[int]string), cursorShown: true,
		cwd: options.CWD, chips: make(map[string]string),
		slashCommands: append([]SlashCommand(nil), options.SlashCommands...),
	}
}

func (e *Editor) IsFocused() bool       { return e.focused }
func (e *Editor) SetFocused(value bool) { e.focused = value }
func (e *Editor) SetCursorShown(value bool) {
	e.cursorShown = value
	e.changed(false)
}
func (e *Editor) Invalidate()            { e.lastLayout = nil }
func (e *Editor) Text() string           { return e.text }
func (e *Editor) Cursor() int            { return e.cursor }
func (e *Editor) SlashMenuVisible() bool { return e.slashMenuVisible }

func (e *Editor) SetBorderStyle(style func(string) string) {
	if style == nil {
		style = func(value string) string { return value }
	}
	e.borderStyle = style
	e.changed(false)
}

func (e *Editor) SetSubmitDisabled(disabled bool) { e.disableSend = disabled }

func (e *Editor) SetText(value string) {
	value = normalizeEditorText(value)
	if value != e.text {
		e.pushUndo()
	}
	e.text = value
	e.cursor = len(value)
	e.scroll = 0
	e.pastes = make(map[int]string)
	e.pasteCounter = 0
	e.chips = make(map[string]string)
	e.submitted = nil
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) InsertText(value string) {
	if value == "" {
		return
	}
	e.pushUndo()
	e.insertRaw(normalizeEditorText(value))
	e.lastAction = ""
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) AddToHistory(value string) {
	value = strings.TrimSpace(value)
	if value == "" || (len(e.history) > 0 && e.history[0] == value) {
		return
	}
	e.history = append([]string{value}, e.history...)
	if len(e.history) > 100 {
		e.history = e.history[:100]
	}
}

func (e *Editor) ExpandedText() string {
	return pasteMarkerPattern.ReplaceAllStringFunc(e.text, func(marker string) string {
		match := pasteMarkerPattern.FindStringSubmatch(marker)
		if len(match) < 2 {
			return marker
		}
		var id int
		_, _ = fmt.Sscanf(match[1], "%d", &id)
		if value, ok := e.pastes[id]; ok {
			return value
		}
		return marker
	})
}

func (e *Editor) HandleInput(data string) {
	if strings.Contains(data, pasteStart) {
		e.inPaste = true
		e.pasteBuffer = ""
		data = strings.Replace(data, pasteStart, "", 1)
	}
	if e.inPaste {
		e.pasteBuffer += data
		if end := strings.Index(e.pasteBuffer, pasteEnd); end >= 0 {
			value := e.pasteBuffer[:end]
			rest := e.pasteBuffer[end+len(pasteEnd):]
			e.pasteBuffer = ""
			e.inPaste = false
			e.handlePaste(value)
			if rest != "" {
				e.HandleInput(rest)
			}
		}
		return
	}

	bindings := GetKeybindings()
	if e.jumpDirection != 0 {
		if value, ok := printableInput(data); ok {
			e.jump(value)
		}
		e.jumpDirection = 0
		return
	}
	if e.slashMenuVisible {
		if bindings.Matches(data, "tui.select.cancel") {
			e.closeSlashMenu()
			e.repaint()
			return
		}
		if bindings.Matches(data, "tui.select.up") {
			e.moveSlashSelection(-1)
			return
		}
		if bindings.Matches(data, "tui.select.down") {
			e.moveSlashSelection(1)
			return
		}
		if bindings.Matches(data, "tui.input.tab") {
			e.acceptSlashSelection(false)
			return
		}
		if bindings.Matches(data, "tui.select.confirm") {
			e.acceptSlashSelection(true)
			return
		}
	}
	if bindings.Matches(data, "tui.input.copy") || bindings.Matches(data, "tui.select.cancel") {
		if e.onEscape != nil {
			e.onEscape()
		}
		return
	}
	if bindings.Matches(data, "tui.editor.undo") {
		e.undoLast()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteToLineEnd") {
		e.deleteToLineEnd()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteToLineStart") {
		e.deleteToLineStart()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteWordBackward") {
		e.deleteWord(-1)
		return
	}
	if bindings.Matches(data, "tui.editor.deleteWordForward") {
		e.deleteWord(1)
		return
	}
	if bindings.Matches(data, "tui.editor.deleteCharBackward") || MatchesKey(data, "shift+backspace") {
		e.backspace()
		return
	}
	if bindings.Matches(data, "tui.editor.deleteCharForward") || MatchesKey(data, "shift+delete") {
		e.deleteForward()
		return
	}
	if bindings.Matches(data, "tui.editor.yank") {
		e.yank()
		return
	}
	if bindings.Matches(data, "tui.editor.yankPop") {
		e.yankPop()
		return
	}
	if bindings.Matches(data, "tui.editor.historyPrevious") {
		e.navigateHistory(-1)
		return
	}
	if bindings.Matches(data, "tui.editor.historyNext") {
		e.navigateHistory(1)
		return
	}
	if bindings.Matches(data, "tui.editor.cursorLineStart") {
		e.cursor = e.lineStart()
		e.resetPreferred()
		return
	}
	if bindings.Matches(data, "tui.editor.cursorLineEnd") {
		e.cursor = e.lineEnd()
		e.resetPreferred()
		return
	}
	if bindings.Matches(data, "tui.editor.cursorWordLeft") {
		e.moveWord(-1)
		return
	}
	if bindings.Matches(data, "tui.editor.cursorWordRight") {
		e.moveWord(1)
		return
	}
	if bindings.Matches(data, "tui.input.newLine") || data == "\n" || data == "\x1b\r" || data == "\x1b[13;2~" {
		e.insertNewline()
		return
	}
	if bindings.Matches(data, "tui.input.submit") {
		if !e.disableSend {
			e.submit()
		}
		return
	}
	if bindings.Matches(data, "tui.editor.cursorUp") {
		e.moveVertical(-1)
		return
	}
	if bindings.Matches(data, "tui.editor.cursorDown") {
		e.moveVertical(1)
		return
	}
	if bindings.Matches(data, "tui.editor.cursorLeft") {
		e.moveHorizontal(-1)
		return
	}
	if bindings.Matches(data, "tui.editor.cursorRight") {
		e.moveHorizontal(1)
		return
	}
	if bindings.Matches(data, "tui.editor.pageUp") {
		e.moveVertical(-max(5, e.rows()*3/10))
		return
	}
	if bindings.Matches(data, "tui.editor.pageDown") {
		e.moveVertical(max(5, e.rows()*3/10))
		return
	}
	if bindings.Matches(data, "tui.editor.jumpForward") {
		e.jumpDirection = 1
		return
	}
	if bindings.Matches(data, "tui.editor.jumpBackward") {
		e.jumpDirection = -1
		return
	}
	if bindings.Matches(data, "tui.input.tab") {
		e.InsertText("    ")
		return
	}
	if value, ok := printableInput(data); ok {
		e.insertTyped(value)
	}
}

func printableInput(data string) (string, bool) {
	if value, ok := DecodeKittyPrintable(data); ok {
		return value, true
	}
	if data == "" {
		return "", false
	}
	for _, r := range data {
		if r < 32 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return "", false
		}
	}
	return data, true
}

func (e *Editor) HandleMouse(event MouseEvent) *MouseResult {
	if event.Type != MouseClick && event.Type != MousePress {
		return nil
	}
	if event.Button != MouseLeft || event.Y <= 0 {
		return nil
	}
	if e.slashMenuVisible && event.Y >= e.slashRenderedStart && event.Y < e.slashRenderedStart+e.slashRenderedCount {
		start, _ := e.slashVisibleRange()
		index := start + event.Y - e.slashRenderedStart
		if index >= 0 && index < len(e.slashMatches) {
			e.slashSelected = index
			if event.Type == MouseClick {
				e.acceptSlashSelection(false)
			} else {
				e.repaint()
			}
		}
		return &MouseResult{Handled: true, Focus: true}
	}
	layout := e.layout(max(1, event.Width-2*min(e.paddingX, max(0, (event.Width-1)/2))))
	index := e.scroll + event.Y - 1
	if index < 0 || index >= len(layout) {
		return &MouseResult{Handled: true, Focus: true}
	}
	line := layout[index]
	target := max(0, event.X-e.paddingX)
	e.cursor = byteAtColumn(e.text, line.start, line.end, target)
	e.resetPreferred()
	e.leaveHistory()
	// Selectable keeps the caret move while the viewport anchors a selection, so
	// dragging over the input highlights like dragging over the transcript.
	return &MouseResult{Handled: true, Focus: true, Selectable: true}
}

func (e *Editor) Render(width int) []string {
	padding := min(e.paddingX, max(0, (width-1)/2))
	contentWidth := max(1, width-padding*2)
	layoutWidth := max(1, contentWidth)
	if padding == 0 {
		layoutWidth = max(1, contentWidth-1)
	}
	e.lastWidth = layoutWidth
	layout := e.layout(layoutWidth)
	e.lastLayout = layout
	cursorLine := e.cursorVisualLine(layout)
	maxVisible := max(5, e.rows()*3/10)
	if cursorLine < e.scroll {
		e.scroll = cursorLine
	}
	if cursorLine >= e.scroll+maxVisible {
		e.scroll = cursorLine - maxVisible + 1
	}
	e.scroll = max(0, min(e.scroll, max(0, len(layout)-maxVisible)))
	end := min(len(layout), e.scroll+maxVisible)
	visible := layout[e.scroll:end]

	result := make([]string, 0, len(visible)+8)
	result = append(result, e.borderStyle(scrollRule(width, "↑", e.scroll)))
	left := strings.Repeat(" ", padding)
	for _, line := range visible {
		display := line.text
		lineWidth := tuitext.VisibleWidth(display)
		if e.cursorShown && e.cursor >= line.start && (e.cursor < line.end || (e.cursor == line.end && line.logicalEnd)) {
			local := e.cursor - line.start
			before := display[:min(local, len(display))]
			after := display[min(local, len(display)):]
			at := " "
			if graphemes := tuitext.Graphemes(after); len(graphemes) > 0 {
				at = graphemes[0]
				after = after[len(at):]
			} else {
				lineWidth++
			}
			marker := ""
			if e.focused {
				marker = CursorMarker
			}
			display = before + marker + "\x1b[7m" + at + "\x1b[27m" + after
		}
		display = StyleAttachmentMarkers(CurrentTheme(), display, "")
		result = append(result, left+display+strings.Repeat(" ", max(0, contentWidth-lineWidth))+left)
	}
	below := len(layout) - end
	result = append(result, e.borderStyle(scrollRule(width, "↓", below)))
	e.slashRenderedStart = len(result)
	e.slashRenderedCount = 0
	if e.slashMenuVisible {
		menu := e.renderSlashMenu(contentWidth)
		e.slashRenderedCount = len(menu)
		for _, line := range menu {
			lineWidth := tuitext.VisibleWidth(line)
			result = append(result, left+line+strings.Repeat(" ", max(0, contentWidth-lineWidth))+left)
		}
	}
	return result
}

const slashMenuMaxVisible = 5

func (e *Editor) updateSlashMenu() {
	if len(e.slashCommands) == 0 || e.cursor < 1 || strings.Contains(e.text[:e.cursor], "\n") {
		e.closeSlashMenu()
		return
	}
	prefix := e.text[:e.cursor]
	if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, " \t") {
		e.closeSlashMenu()
		return
	}
	query := strings.TrimPrefix(prefix, "/")
	matches := FuzzyFilterWithTypos(e.slashCommands, query, func(command SlashCommand) string { return command.Name })
	if len(matches) == 0 {
		e.closeSlashMenu()
		return
	}
	e.slashMatches = matches
	e.slashSelected = 0
	e.slashMenuVisible = true
}

func (e *Editor) closeSlashMenu() {
	e.slashMatches = nil
	e.slashSelected = 0
	e.slashMenuVisible = false
	e.slashRenderedStart = 0
	e.slashRenderedCount = 0
}

func (e *Editor) moveSlashSelection(delta int) {
	if len(e.slashMatches) == 0 {
		return
	}
	e.slashSelected = (e.slashSelected + delta + len(e.slashMatches)) % len(e.slashMatches)
	e.repaint()
}

func (e *Editor) acceptSlashSelection(submit bool) {
	if !e.slashMenuVisible || e.slashSelected < 0 || e.slashSelected >= len(e.slashMatches) {
		return
	}
	e.pushUndo()
	command := e.slashMatches[e.slashSelected]
	after := e.text[e.cursor:]
	e.text = "/" + command.Name + " " + after
	e.cursor = len(command.Name) + 2
	e.lastAction = ""
	e.leaveHistory()
	e.closeSlashMenu()
	e.changed(true)
	if submit && !e.disableSend {
		e.submit()
	}
}

func (e *Editor) slashVisibleRange() (int, int) {
	if len(e.slashMatches) <= slashMenuMaxVisible {
		return 0, len(e.slashMatches)
	}
	start := e.slashSelected - slashMenuMaxVisible/2
	start = max(0, min(start, len(e.slashMatches)-slashMenuMaxVisible))
	return start, start + slashMenuMaxVisible
}

func (e *Editor) renderSlashMenu(width int) []string {
	start, end := e.slashVisibleRange()
	lines := make([]string, 0, slashMenuMaxVisible+1)
	theme := CurrentTheme()
	primaryWidth := 12
	for _, command := range e.slashMatches {
		primaryWidth = max(primaryWidth, tuitext.VisibleWidth(command.Name)+2)
	}
	primaryWidth = min(primaryWidth, 32)
	for index := start; index < end; index++ {
		command := e.slashMatches[index]
		prefix := "  "
		if index == e.slashSelected {
			prefix = "→ "
		}
		line := prefix + command.Name
		if command.Description != "" && width > 40 {
			nameWidth := tuitext.VisibleWidth(command.Name)
			spacing := strings.Repeat(" ", max(1, primaryWidth-nameWidth))
			remaining := width - 2 - nameWidth - len(spacing) - 2
			if remaining > 10 {
				description := tuitext.TruncateToWidth(strings.Join(strings.Fields(command.Description), " "), remaining, "", false)
				line += spacing + description
				if index != e.slashSelected {
					line = prefix + command.Name + theme.FG("muted", spacing+description)
				}
			}
		}
		line = tuitext.TruncateToWidth(line, width, "", false)
		if index == e.slashSelected {
			line = theme.FG("accent", line)
		}
		lines = append(lines, line)
	}
	if start > 0 || end < len(e.slashMatches) {
		lines = append(lines, theme.FG("muted", fmt.Sprintf("  (%d/%d)", e.slashSelected+1, len(e.slashMatches))))
	}
	return lines
}

func scrollRule(width int, direction string, hidden int) string {
	if hidden <= 0 {
		return strings.Repeat("─", max(0, width))
	}
	label := fmt.Sprintf(" %s %d more ", direction, hidden)
	if tuitext.VisibleWidth(label)+2 > width {
		return tuitext.TruncateToWidth("───"+label, width, "", false)
	}
	left := (width - tuitext.VisibleWidth(label)) / 2
	return strings.Repeat("─", left) + label + strings.Repeat("─", width-left-tuitext.VisibleWidth(label))
}

func (e *Editor) layout(width int) []editorVisualLine {
	if e.text == "" {
		return []editorVisualLine{{logicalEnd: true}}
	}
	result := make([]editorVisualLine, 0)
	offset := 0
	parts := strings.Split(e.text, "\n")
	for index, part := range parts {
		chunks := wrapEditorLine(part, width)
		for chunkIndex, chunk := range chunks {
			result = append(result, editorVisualLine{
				text: chunk.text, start: offset + chunk.start, end: offset + chunk.end,
				logicalEnd: chunkIndex == len(chunks)-1,
			})
		}
		offset += len(part)
		if index < len(parts)-1 {
			offset++
		}
	}
	return result
}

type editorChunk struct {
	text       string
	start, end int
}

func wrapEditorLine(value string, width int) []editorChunk {
	if value == "" || width <= 0 {
		return []editorChunk{{}}
	}
	if tuitext.VisibleWidth(value) <= width {
		return []editorChunk{{text: value, end: len(value)}}
	}
	graphemes := tuitext.Graphemes(value)
	result := make([]editorChunk, 0)
	start, pos, columns := 0, 0, 0
	lastBreak, breakColumns := -1, 0
	for _, grapheme := range graphemes {
		gw := tuitext.VisibleWidth(grapheme)
		if columns+gw > width && pos > start {
			cut := pos
			if lastBreak > start && columns-breakColumns+gw <= width {
				cut = lastBreak
			}
			result = append(result, editorChunk{text: value[start:cut], start: start, end: cut})
			start = cut
			columns = tuitext.VisibleWidth(value[start:pos])
			lastBreak = -1
		}
		pos += len(grapheme)
		columns += gw
		if tuitext.IsWhitespaceChar(grapheme) {
			lastBreak, breakColumns = pos, columns
		}
	}
	result = append(result, editorChunk{text: value[start:], start: start, end: len(value)})
	return result
}

func (e *Editor) cursorVisualLine(layout []editorVisualLine) int {
	for index, line := range layout {
		if e.cursor >= line.start && (e.cursor < line.end || (e.cursor == line.end && line.logicalEnd)) {
			return index
		}
	}
	return max(0, len(layout)-1)
}

func (e *Editor) moveVertical(delta int) {
	layout := e.lastLayout
	if len(layout) == 0 {
		width := e.lastWidth
		if width <= 0 {
			width = 80
		}
		layout = e.layout(width)
	}
	current := e.cursorVisualLine(layout)
	if delta < 0 && current == 0 {
		e.navigateHistory(-1)
		return
	}
	if delta > 0 && current == len(layout)-1 && e.historyIndex >= 0 {
		e.navigateHistory(1)
		return
	}
	target := max(0, min(len(layout)-1, current+delta))
	// Clamp defensively: a layout from an earlier render could start past the
	// cursor, and slicing such a range would panic.
	column := tuitext.VisibleWidth(e.text[min(layout[current].start, e.cursor):e.cursor])
	if e.hasPreferredX {
		column = e.preferredX
	} else {
		e.preferredX, e.hasPreferredX = column, true
	}
	e.cursor = byteAtColumn(e.text, layout[target].start, layout[target].end, column)
	e.lastAction = ""
	e.leaveHistory()
}

func byteAtColumn(value string, start, end, target int) int {
	column, pos := 0, start
	for _, grapheme := range tuitext.Graphemes(value[start:end]) {
		next := column + tuitext.VisibleWidth(grapheme)
		if target < next {
			return pos
		}
		column, pos = next, pos+len(grapheme)
	}
	return end
}

func (e *Editor) moveHorizontal(direction int) {
	e.leaveHistory()
	e.lastAction = ""
	e.resetPreferred()
	if direction < 0 && e.cursor > 0 {
		if start, ok := e.chipEndingAt(e.cursor); ok {
			e.cursor = start
			return
		}
		e.cursor = previousGraphemeBoundary(e.text, e.cursor)
	}
	if direction > 0 && e.cursor < len(e.text) {
		if end, ok := e.chipStartingAt(e.cursor); ok {
			e.cursor = end
			return
		}
		e.cursor = nextGraphemeBoundary(e.text, e.cursor)
	}
}

func (e *Editor) lineStart() int {
	if at := strings.LastIndex(e.text[:e.cursor], "\n"); at >= 0 {
		return at + 1
	}
	return 0
}

func (e *Editor) lineEnd() int {
	if at := strings.Index(e.text[e.cursor:], "\n"); at >= 0 {
		return e.cursor + at
	}
	return len(e.text)
}

func (e *Editor) moveWord(direction int) {
	e.leaveHistory()
	e.lastAction = ""
	e.resetPreferred()
	if direction < 0 {
		for e.cursor > 0 && editorWordClass(graphemeBefore(e.text, e.cursor)) == 0 {
			e.cursor = previousGraphemeBoundary(e.text, e.cursor)
		}
		if e.cursor == 0 {
			return
		}
		class := editorWordClass(graphemeBefore(e.text, e.cursor))
		for e.cursor > 0 && editorWordClass(graphemeBefore(e.text, e.cursor)) == class {
			e.cursor = previousGraphemeBoundary(e.text, e.cursor)
		}
		return
	}
	for e.cursor < len(e.text) && editorWordClass(graphemeAfter(e.text, e.cursor)) == 0 {
		e.cursor = nextGraphemeBoundary(e.text, e.cursor)
	}
	if e.cursor >= len(e.text) {
		return
	}
	class := editorWordClass(graphemeAfter(e.text, e.cursor))
	for e.cursor < len(e.text) && editorWordClass(graphemeAfter(e.text, e.cursor)) == class {
		e.cursor = nextGraphemeBoundary(e.text, e.cursor)
	}
}

func editorWordClass(value string) int {
	if value == "" || tuitext.IsWhitespaceChar(value) {
		return 0
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			return 1
		}
	}
	return 2
}

func graphemeBefore(value string, cursor int) string {
	start := previousGraphemeBoundary(value, cursor)
	return value[start:cursor]
}
func graphemeAfter(value string, cursor int) string {
	end := nextGraphemeBoundary(value, cursor)
	return value[cursor:end]
}

func (e *Editor) insertTyped(value string) {
	if tuitext.IsWhitespaceChar(value) || e.lastAction != "type-word" {
		e.pushUndo()
	}
	e.lastAction = "type-word"
	if value == "!" && e.cursor == 0 && e.text == "" {
		e.insertRaw("! ")
	} else if value == "!" && e.cursor == 2 && e.text == "! " {
		e.text, e.cursor = "!! ", 3
	} else {
		e.insertRaw(value)
	}
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) insertRaw(value string) {
	e.text = e.text[:e.cursor] + value + e.text[e.cursor:]
	e.cursor += len(value)
}

func (e *Editor) insertNewline() {
	e.pushUndo()
	e.insertRaw("\n")
	e.lastAction = ""
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) backspace() {
	if e.cursor == 0 {
		return
	}
	e.pushUndo()
	start := previousGraphemeBoundary(e.text, e.cursor)
	if markerStart, ok := e.markerEndingAt(e.cursor); ok {
		start = markerStart
		e.removePasteAt(markerStart)
	} else if chipStart, ok := e.chipEndingAt(e.cursor); ok {
		start = chipStart
		e.removeChipAt(chipStart)
	}
	e.text = e.text[:start] + e.text[e.cursor:]
	e.cursor = start
	e.lastAction = ""
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) deleteForward() {
	if e.cursor >= len(e.text) {
		return
	}
	e.pushUndo()
	end := nextGraphemeBoundary(e.text, e.cursor)
	if markerEnd, ok := e.markerStartingAt(e.cursor); ok {
		end = markerEnd
		e.removePasteAt(e.cursor)
	} else if chipEnd, ok := e.chipStartingAt(e.cursor); ok {
		end = chipEnd
		e.removeChipAt(e.cursor)
	}
	e.text = e.text[:e.cursor] + e.text[end:]
	e.lastAction = ""
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) deleteWord(direction int) {
	wasKill := e.lastAction == "kill"
	original := e.cursor
	e.moveWord(direction)
	target := e.cursor
	start, end := min(original, target), max(original, target)
	if start == end {
		return
	}
	e.cursor = original
	e.pushUndo()
	deleted := e.text[start:end]
	e.pushKill(deleted, direction < 0, wasKill)
	e.text = e.text[:start] + e.text[end:]
	e.cursor = start
	e.lastAction = "kill"
	e.changed(true)
}

func (e *Editor) deleteToLineStart() { e.deleteRange(e.lineStart(), e.cursor, true) }
func (e *Editor) deleteToLineEnd()   { e.deleteRange(e.cursor, e.lineEnd(), false) }
func (e *Editor) deleteRange(start, end int, prepend bool) {
	if start == end {
		return
	}
	wasKill := e.lastAction == "kill"
	e.pushUndo()
	e.pushKill(e.text[start:end], prepend, wasKill)
	e.text = e.text[:start] + e.text[end:]
	e.cursor = start
	e.lastAction = "kill"
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) pushKill(value string, prepend, accumulate bool) {
	if value == "" {
		return
	}
	if accumulate && len(e.killRing) > 0 {
		if prepend {
			e.killRing[0] = value + e.killRing[0]
		} else {
			e.killRing[0] += value
		}
		return
	}
	e.killRing = append([]string{value}, e.killRing...)
	if len(e.killRing) > 60 {
		e.killRing = e.killRing[:60]
	}
}

func (e *Editor) yank() {
	if len(e.killRing) == 0 {
		return
	}
	e.pushUndo()
	e.yankStart = e.cursor
	e.insertRaw(e.killRing[0])
	e.lastAction = "yank"
	e.changed(true)
}

func (e *Editor) yankPop() {
	if e.lastAction != "yank" || len(e.killRing) < 2 {
		return
	}
	e.pushUndo()
	e.text = e.text[:e.yankStart] + e.text[e.cursor:]
	e.cursor = e.yankStart
	first := e.killRing[0]
	e.killRing = append(e.killRing[1:], first)
	e.insertRaw(e.killRing[0])
	e.lastAction = "yank"
	e.changed(true)
}

func (e *Editor) navigateHistory(direction int) {
	if len(e.history) == 0 {
		return
	}
	next := e.historyIndex - direction
	if next < -1 || next >= len(e.history) {
		return
	}
	if e.historyIndex == -1 && next >= 0 {
		e.historyDraft = e.text
	}
	e.historyIndex = next
	if next == -1 {
		e.text = e.historyDraft
	} else {
		e.text = e.history[next]
	}
	e.cursor = len(e.text)
	e.scroll = 0
	e.resetPreferred()
	e.changed(true)
}

func (e *Editor) leaveHistory()   { e.historyIndex = -1; e.historyDraft = "" }
func (e *Editor) resetPreferred() { e.hasPreferredX = false }

func (e *Editor) jump(value string) {
	if value == "" {
		return
	}
	if e.jumpDirection > 0 {
		if at := strings.Index(e.text[e.cursor:], value); at >= 0 {
			e.cursor += at
		}
	} else if at := strings.LastIndex(e.text[:e.cursor], value); at >= 0 {
		e.cursor = at
	}
	e.resetPreferred()
}

func (e *Editor) handlePaste(value string) {
	value = normalizeEditorText(value)
	var filtered strings.Builder
	for _, r := range value {
		if r == '\n' || r >= 32 {
			filtered.WriteRune(r)
		}
	}
	value = filtered.String()
	if value == "" {
		return
	}
	e.pushUndo()
	if marker, paths, ok := InsertAttachmentChips(value, e.cwd); ok {
		e.chips = mergeChipPaths(e.chips, paths)
		e.insertRaw(marker)
		e.lastAction = ""
		e.leaveHistory()
		e.changed(true)
		return
	}
	if strings.Count(value, "\n")+1 > 10 || len(value) > 1000 {
		e.pasteCounter++
		e.pastes[e.pasteCounter] = value
		lines := strings.Count(value, "\n") + 1
		marker := fmt.Sprintf("[paste #%d %d chars]", e.pasteCounter, len(value))
		if lines > 10 {
			marker = fmt.Sprintf("[paste #%d +%d lines]", e.pasteCounter, lines)
		}
		e.insertRaw(marker)
	} else {
		e.insertRaw(value)
	}
	e.lastAction = ""
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) markerEndingAt(end int) (int, bool) {
	for _, indexes := range pasteMarkerPattern.FindAllStringIndex(e.text, -1) {
		if indexes[1] == end {
			return indexes[0], true
		}
	}
	return 0, false
}
func (e *Editor) markerStartingAt(start int) (int, bool) {
	for _, indexes := range pasteMarkerPattern.FindAllStringIndex(e.text, -1) {
		if indexes[0] == start {
			return indexes[1], true
		}
	}
	return 0, false
}
func (e *Editor) removePasteAt(start int) {
	match := pasteMarkerPattern.FindStringSubmatch(e.text[start:])
	if len(match) < 2 {
		return
	}
	var id int
	_, _ = fmt.Sscanf(match[1], "%d", &id)
	delete(e.pastes, id)
}

func (e *Editor) submit() {
	value, ok := e.takeSubmission()
	if !ok {
		return
	}
	if e.onSubmit != nil {
		e.onSubmit(value)
	}
}

// takeSubmission commits the current editor value without routing it. Chat
// uses this after a steer has been admitted by the active agent run.
func (e *Editor) takeSubmission() (string, bool) {
	value := strings.TrimRightFunc(e.ExpandedText(), unicode.IsSpace)
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false
	}
	e.AddToHistory(trimmed)
	e.captureSubmission(value)
	e.text, e.cursor, e.scroll = "", 0, 0
	e.undo = nil
	e.pastes = make(map[int]string)
	e.pasteCounter = 0
	e.chips = make(map[string]string)
	e.lastAction = ""
	e.leaveHistory()
	e.changed(true)
	return value, true
}

func (e *Editor) pushUndo() {
	pastes := make(map[int]string, len(e.pastes))
	for key, value := range e.pastes {
		pastes[key] = value
	}
	e.undo = append(e.undo, editorSnapshot{text: e.text, cursor: e.cursor, pastes: pastes, pasteCounter: e.pasteCounter})
	if len(e.undo) > 200 {
		e.undo = e.undo[len(e.undo)-200:]
	}
}

func (e *Editor) undoLast() {
	if len(e.undo) == 0 {
		return
	}
	snapshot := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.text, e.cursor, e.pastes, e.pasteCounter = snapshot.text, snapshot.cursor, snapshot.pastes, snapshot.pasteCounter
	e.lastAction = ""
	e.leaveHistory()
	e.changed(true)
}

func (e *Editor) changed(notify bool) {
	e.resetPreferred()
	e.updateSlashMenu()
	// Any change can move text under a layout that was computed before it, and a
	// stale layout makes the cursor maths below index the wrong bytes.
	e.lastLayout = nil
	if notify && e.onChange != nil {
		e.onChange(e.text)
	}
	e.repaint()
}

func (e *Editor) repaint() {
	if e.request != nil {
		e.request()
	}
}

func normalizeEditorText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\t", "    ")
}
