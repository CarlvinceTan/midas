package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// ChatViewport is Midas's fullscreen composition: a pinned session header,
// scrollable startup/transcript document, and a fixed queue/editor/footer dock.
type ChatViewport struct {
	Root       *VStack
	Transcript *ScrollView
}

type chatPart struct {
	chat *Chat
	kind string
}

// chatRowGeometry returns the symmetric outer gutter used by unframed Midas
// rows: the Padding setting on each side. Rounded cards and dock panels
// intentionally remain full width, matching the legacy UI. Very narrow
// terminals drop the gutter before dropping content.
func chatRowGeometry(width int) (margin, inner int) {
	width = max(1, width)
	margin = min(Padding(), max(0, (width-1)/2))
	return margin, max(1, width-margin*2)
}

func (p *chatPart) Invalidate() {
	if p.kind == "editor" {
		p.chat.editor.Invalidate()
	}
}

// ChildComponents keeps Chat itself mounted for focus restoration after an
// overlay closes even though the visual surface is split into layout regions.
func (p *chatPart) ChildComponents() []Component {
	if p.kind == "editor" {
		children := []Component{p.chat}
		if dock := p.chat.DockComponent(); dock != nil {
			children = append(children, dock)
		}
		return children
	}
	return nil
}

func (p *chatPart) Render(width int) []string {
	switch p.kind {
	case "header":
		return p.chat.renderHeader(width)
	case "document":
		return p.chat.renderDocument(width)
	case "pending":
		return p.chat.renderPending(width)
	case "editor":
		return p.chat.renderDock(width)
	case "footer":
		return p.chat.renderFooter(width)
	case "gap":
		// The viewport's border padding: one blank row per Padding cell.
		return make([]string, Padding())
	case "blank":
		return []string{""}
	default:
		return nil
	}
}

func (p *chatPart) HandleMouse(event MouseEvent) *MouseResult {
	if p.kind == "document" {
		return p.chat.handleTranscriptMouse(event)
	}
	if p.kind == "pending" {
		return p.chat.handlePendingMouse(event)
	}
	if p.kind == "footer" {
		return p.chat.handleFooterMouse(event)
	}
	if p.kind != "editor" {
		return nil
	}
	if dock := p.chat.DockComponent(); dock != nil {
		return DispatchMouseEvent(dock, event)
	}
	margin, frameWidth := chatRowGeometry(event.Width)
	left := margin + 2
	if event.X < left || event.X >= left+max(1, frameWidth-4) {
		return nil
	}
	inner := event
	inner.X -= left
	inner.Width = max(1, frameWidth-4)
	result := p.chat.editor.HandleMouse(inner)
	if result == nil {
		return nil
	}
	result.FocusTarget = p.chat
	result.Target = &MouseDispatchTarget{
		Component: p.chat.editor,
		OriginX:   event.ScreenX - event.X + left,
		OriginY:   event.ScreenY - event.Y,
		Width:     inner.Width,
		Height:    event.Height,
	}
	return result
}

// NewChatViewport builds the same fixed-shell layout as the original Midas UI.
func NewChatViewport(chat *Chat) ChatViewport {
	header := &chatPart{chat: chat, kind: "header"}
	document := &chatPart{chat: chat, kind: "document"}
	pending := &chatPart{chat: chat, kind: "pending"}
	editor := &chatPart{chat: chat, kind: "editor"}
	footer := &chatPart{chat: chat, kind: "footer"}
	// The padding above and below the transcript chrome: the viewport reserves
	// Padding blank rows at the top and bottom borders.
	topGap := &chatPart{chat: chat, kind: "gap"}
	bottomGap := &chatPart{chat: chat, kind: "gap"}
	blank := &chatPart{chat: chat, kind: "blank"}

	transcript := NewScrollView(document, ScrollViewOptions{
		FollowEnd:  true,
		Primary:    true,
		Overscroll: OverscrollChain,
		// Midas keeps the transcript free of an in-app scrollbar: the transient
		// auto scrollbar covered the rightmost column of the newest lines.
		Scrollbar: ScrollbarHidden,
	})
	dock := NewVStackWithOptions(StackOptions{},
		NewStackChild(blank, StackEntryOptions{Grow: new(0), Shrink: new(0), MinSize: new(1)}),
		NewStackChild(pending, StackEntryOptions{Shrink: new(1), MinSize: new(0)}),
		NewStackChild(editor, StackEntryOptions{Shrink: new(1), MinSize: new(3)}),
		NewStackChild(footer, StackEntryOptions{Shrink: new(1), MinSize: new(1)}),
	)
	root := NewVStackWithOptions(StackOptions{},
		NewStackChild(topGap, StackEntryOptions{Grow: new(0), Shrink: new(1), MinSize: new(0)}),
		NewStackChild(header, StackEntryOptions{Grow: new(0), Shrink: new(0), MinSize: new(1)}),
		NewStackChild(transcript, StackEntryOptions{Basis: new(0), Grow: new(1), Shrink: new(1), MinSize: new(1)}),
		NewStackChild(dock, StackEntryOptions{Grow: new(0), Shrink: new(1), MinSize: new(1)}),
		NewStackChild(bottomGap, StackEntryOptions{Grow: new(0), Shrink: new(1), MinSize: new(0)}),
	)
	return ChatViewport{Root: root, Transcript: transcript}
}

func (c *Chat) renderHeader(width int) []string {
	c.mu.Lock()
	title, cwd, branch := c.title, c.cwd, c.branch
	busy := c.busy
	compact := c.compactHeader
	statusText, statusFallback, statusMaxWords := c.statusText, c.statusFallback, c.statusMaxWords
	toastText, toastLevel := c.toastText, c.toastLevel
	skillCount, mcpCount := 0, len(c.mcpNames)
	for _, group := range c.skillGroups {
		skillCount += len(group)
	}
	c.mu.Unlock()
	if strings.TrimSpace(title) == "" {
		title = "New Session"
	}
	location := compactPath(cwd)
	if branch != "" && !compact {
		location += " (" + branch + ")"
	}
	resources := fmt.Sprintf("%d skills • %d mcps", skillCount, mcpCount)
	margin, inner := chatRowGeometry(width)
	inset := strings.Repeat(" ", margin)
	theme := CurrentTheme()
	titleLine := inset + alignChatSides(bold(theme.FG("text", title)), dim(location), inner)
	if compact {
		// The divider closes the header even when the status and resource rows
		// are dropped, so the transcript stays visually separated from Midas's
		// session header.
		lines := []string{
			titleLine,
			inset + theme.FG("borderMuted", strings.Repeat("─", inner)),
		}
		if toastText != "" && inner > 0 {
			widthOfToast := toastWidth(toastText, inner)
			lines[0] = CompositeTuiLine(lines[0], renderToast(toastText, toastLevel, widthOfToast), margin+inner-widthOfToast, widthOfToast, width)
		}
		return lines
	}
	status := "Idle"
	if busy {
		status = statusText
		if status == "" {
			status = statusFallback
		}
		if status == "" {
			status = "Working"
		}
	}
	status = cleanStatusPhrase(status, statusMaxWords)
	lines := []string{
		titleLine,
		inset + alignChatSides(theme.FG("muted", status), dim(resources), inner),
		inset + theme.FG("borderMuted", strings.Repeat("─", inner)),
	}
	if toastText != "" && inner > 0 {
		widthOfToast := toastWidth(toastText, inner)
		lines[1] = CompositeTuiLine(lines[1], renderToast(toastText, toastLevel, widthOfToast), margin+inner-widthOfToast, widthOfToast, width)
	}
	return lines
}

func (c *Chat) renderDocument(width int) []string {
	lines := c.renderStartup(width)
	if transcript := c.renderTranscript(width); len(transcript) > 0 {
		lines = append(lines, transcript...)
	}
	return lines
}

func (c *Chat) renderStartup(width int) []string {
	c.mu.Lock()
	contexts := append([]string(nil), c.contextPaths...)
	agents := flattenGroups(c.agentGroups)
	skills := flattenGroups(c.skillGroups)
	mcps := append([]string(nil), c.mcpNames...)
	c.mu.Unlock()
	type column struct {
		title  string
		values []string
	}
	columns := []column{
		{"[Context]", defaultItems(contexts)},
		{"[Agents]", defaultItems(agents)},
		{"[Skills]", defaultItems(skills)},
		{"[MCPs]", defaultItems(mcps)},
	}
	margin, inner := chatRowGeometry(width)
	inset, gap := strings.Repeat(" ", margin), 2
	available := max(len(columns), inner-gap*(len(columns)-1))
	base, remainder := available/len(columns), available%len(columns)
	widths := make([]int, len(columns))
	for index := range widths {
		widths[index] = base
		if index < remainder {
			widths[index]++
		}
	}
	cell := func(value string, columnWidth int, heading bool) string {
		if value == "" {
			return strings.Repeat(" ", max(1, columnWidth))
		}
		if heading {
			value = CurrentTheme().FG("startupHeading", value)
		} else {
			value = dim(value)
		}
		value = tuitext.TruncateToWidth(value, max(1, columnWidth), "…", false)
		return value + strings.Repeat(" ", max(0, columnWidth-tuitext.VisibleWidth(value)))
	}
	rows := 1
	for _, column := range columns {
		rows = max(rows, len(column.values))
	}
	result := make([]string, 0, rows+1)
	for row := -1; row < rows; row++ {
		parts := make([]string, len(columns))
		for index, column := range columns {
			value, heading := column.title, row < 0
			if row >= 0 {
				value = ""
				if row < len(column.values) {
					value = column.values[row]
				}
			}
			parts[index] = cell(value, widths[index], heading)
		}
		line := strings.TrimRight(strings.Join(parts, strings.Repeat(" ", gap)), " ")
		result = append(result, inset+tuitext.TruncateToWidth(line, inner, "", false))
	}
	return result
}

func flattenGroups(groups [][]string) []string {
	result := make([]string, 0)
	for index, group := range groups {
		if index > 0 && len(result) > 0 {
			result = append(result, "")
		}
		result = append(result, group...)
	}
	return result
}

func defaultItems(values []string) []string {
	if len(values) == 0 {
		return []string{"None"}
	}
	return values
}

func (c *Chat) renderEditorDock(width int) []string {
	width = max(1, width)
	// The input frame takes the same outer gutter as every other Midas row, so
	// the padding setting insets the box the user types in too. Narrow terminals
	// drop the frame rather than the content.
	margin, frameWidth := chatRowGeometry(width)
	if frameWidth < 5 {
		return c.editor.Render(width)
	}
	inset := strings.Repeat(" ", margin)
	// RoundedDialogFrame in the legacy UI contributed one border column and
	// one content gutter on each side. The editor itself had zero padding.
	innerWidth := max(1, frameWidth-4)
	c.mu.Lock()
	voiceActive := c.voiceActive
	voiceStopping := c.voiceStopping
	voiceReady := c.voiceReady
	voiceEditor := c.voiceEditor
	c.mu.Unlock()
	inner := c.editor.Render(innerWidth)
	if voiceActive && voiceEditor != nil {
		inner = voiceEditor.Render(innerWidth)
	}
	c.lastEditorHeight = len(inner)
	style := c.editorStyleFor(c.editor.Text())
	if style == nil {
		c.mu.Lock()
		level := c.thinking
		c.mu.Unlock()
		style = func(value string) string { return CurrentTheme().Thinking(level, value) }
	}
	bottom := len(inner) - 1
	for index := 1; index < len(inner); index++ {
		plain := strings.TrimSpace(tuitext.StripTerminalSequences(inner[index]))
		if plain == "" {
			continue
		}
		if strings.Trim(plain, "─") == "" || (strings.Contains(plain, "↓") && strings.Contains(plain, "more")) {
			bottom = index
			break
		}
	}
	lines := make([]string, len(inner))
	for index, line := range inner {
		if index == 0 {
			if voiceActive && !voiceStopping {
				// The frame names the dictation state: the model loads on the first
				// /voice, so it reports Loading until the recognizer is ready.
				label := " Voice: Loading "
				if voiceReady {
					label = " Voice: Listening "
				}
				lines[index] = inset + style("╭─"+label+strings.Repeat("─", max(0, frameWidth-3-tuitext.VisibleWidth(label)))+"╮")
			} else {
				lines[index] = inset + style("╭"+strings.Repeat("─", frameWidth-2)+"╮")
			}
			continue
		}
		if index == bottom {
			lines[index] = inset + style("╰"+strings.Repeat("─", frameWidth-2)+"╯")
			continue
		}
		if index > bottom {
			// The command menu sits under the box, inside the same gutter.
			content := tuitext.TruncateToWidth(line, frameWidth, "", false)
			lines[index] = inset + content
			continue
		}
		content := tuitext.TruncateToWidth(line, innerWidth, "", false)
		content += strings.Repeat(" ", max(0, innerWidth-tuitext.VisibleWidth(content)))
		lines[index] = inset + style("│") + " " + content + " " + style("│")
	}
	return lines
}

func (c *Chat) renderDock(width int) []string {
	if dock := c.DockComponent(); dock != nil {
		lines := dock.Render(max(1, width))
		c.lastEditorHeight = len(lines)
		return lines
	}
	return c.renderEditorDock(width)
}

func (c *Chat) renderFooter(width int) []string {
	c.mu.Lock()
	model, modelName, thinking := c.model, c.modelName, c.thinking
	usage, contextWindow := c.usage, c.modelContext
	contextTokens, hasContext := c.contextReading(time.Now())
	remoteActive := c.remoteURL != ""
	rate, hasRate := c.rateDisplay.Value()
	costFormatter := c.costFormatter
	c.mu.Unlock()
	displayModel := "Not set"
	if model != "" {
		displayModel = formatModelName(model)
		if modelName != "" {
			displayModel = formatModelName(modelName)
		}
	}
	left := displayModel + " • " + string(thinking)
	if thinking == "" {
		left = displayModel + " • off"
	}
	if remoteActive {
		left += " • Remote active"
	}
	rateText := "--"
	if hasRate && rate > 0 {
		if rate >= 100 {
			rateText = fmt.Sprintf("%.0f", math.Floor(rate+0.5))
		} else {
			rateText = fmt.Sprintf("%.1f", math.Floor(rate*10+0.5)/10)
		}
	}
	contextText := formatContextText(contextTokens, contextWindow, hasContext)
	right := fmt.Sprintf("%4s t/s • %s • %s", rateText, contextText, formatCost(usage.Cost.Total, costFormatter))
	theme := CurrentTheme()
	margin, inner := chatRowGeometry(width)
	inset := strings.Repeat(" ", margin)
	line := alignChatSides(theme.FG("dim", left), theme.FG("dim", right), inner)
	return []string{inset + line + inset}
}

func (c *Chat) handleFooterMouse(event MouseEvent) *MouseResult {
	if event.Type != MousePress || event.Button != MouseLeft || event.Y != 0 {
		return nil
	}
	c.mu.Lock()
	model, modelName, thinking := c.model, c.modelName, c.thinking
	remoteURL, copyRemote := c.remoteURL, c.onCopyRemote
	usage, contextWindow := c.usage, c.modelContext
	contextTokens, hasContext := c.contextReading(time.Now())
	rate, hasRate := c.rateDisplay.Value()
	costFormatter := c.costFormatter
	c.mu.Unlock()
	if remoteURL == "" || copyRemote == nil {
		return nil
	}
	displayModel := "Not set"
	if model != "" {
		displayModel = formatModelName(model)
		if modelName != "" {
			displayModel = formatModelName(modelName)
		}
	}
	if thinking == "" {
		thinking = ai.ThinkingOff
	}
	base := displayModel + " • " + string(thinking)
	start := tuitext.VisibleWidth(base) + tuitext.VisibleWidth(" • ")
	end := start + tuitext.VisibleWidth("Remote active")
	margin, inner := chatRowGeometry(event.Width)
	rateText := "--"
	if hasRate && rate > 0 {
		if rate >= 100 {
			rateText = fmt.Sprintf("%.0f", math.Floor(rate+0.5))
		} else {
			rateText = fmt.Sprintf("%.1f", math.Floor(rate*10+0.5)/10)
		}
	}
	contextText := formatContextText(contextTokens, contextWindow, hasContext)
	right := fmt.Sprintf("%4s t/s • %s • %s", rateText, contextText, formatCost(usage.Cost.Total, costFormatter))
	leftLimit := inner - tuitext.VisibleWidth(right) - 1
	if end > leftLimit || event.X < margin+start || event.X >= margin+end {
		return nil
	}
	copyRemote()
	return &MouseResult{Handled: true}
}

func alignChatSides(left, right string, width int) string {
	leftWidth, rightWidth := tuitext.VisibleWidth(left), tuitext.VisibleWidth(right)
	if rightWidth >= width {
		return tuitext.TruncateToWidth(right, width, "", false)
	}
	if leftWidth+rightWidth+1 <= width {
		return left + strings.Repeat(" ", width-leftWidth-rightWidth) + right
	}
	left = tuitext.TruncateToWidth(left, max(0, width-rightWidth-1), "…", false)
	return left + strings.Repeat(" ", max(1, width-tuitext.VisibleWidth(left)-rightWidth)) + right
}

func formatModelName(value string) string {
	if slash := strings.LastIndex(value, "/"); slash >= 0 && slash < len(value)-1 {
		value = value[slash+1:]
	}
	value = strings.ReplaceAll(value, "-", " ")
	words := strings.Fields(value)
	for index, word := range words {
		if len(word) > 0 {
			words[index] = strings.ToUpper(word[:1]) + word[1:]
		}
		if strings.EqualFold(word, "deepseek") {
			words[index] = "DeepSeek"
		}
	}
	return strings.Join(words, " ")
}

func formatTokens(value int) string {
	if value < 1000 {
		return fmt.Sprintf("%d", value)
	}
	if value < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(value)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
}

var _ Component = (*chatPart)(nil)
var _ MouseHandler = (*chatPart)(nil)

// formatCost renders the session cost with the configured display currency,
// falling back to plain USD when the application has not supplied a formatter.
// formatContextText renders the footer's ctx pair: the context size against the
// model's window. Unknown readings keep the placeholder.
func formatContextText(tokens, contextWindow int, known bool) string {
	if !known || tokens <= 0 || contextWindow <= 0 {
		return "-- (--%)"
	}
	return fmt.Sprintf("%s (%d%%)", formatTokens(tokens), (tokens*100+contextWindow/2)/contextWindow)
}

func formatCost(total float64, formatter func(float64) string) string {
	if formatter != nil {
		return formatter(total)
	}
	return fmt.Sprintf("$%.2f", total)
}
