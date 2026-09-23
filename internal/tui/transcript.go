package tui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

const transcriptPreviewLines = 8

type transcriptTargetKind uint8

const (
	transcriptRunTarget transcriptTargetKind = iota
	transcriptChainTarget
	transcriptDetailTarget
	// transcriptLiveTarget is the in-flight action row toggling its detail.
	transcriptLiveTarget
)

type transcriptTarget struct {
	kind       transcriptTargetKind
	start, end int
	entry      int
	key        string
}

type transcriptActivityItem struct {
	segment chatSegment
	tool    *chatTool
	index   int
}

type transcriptSegment struct {
	kind  chatSegmentKind
	text  string
	items []transcriptActivityItem
	start int
}

// transcriptRenderer owns the immutable render snapshot and accumulates mouse
// targets for one frame. Keeping frame state together avoids plumbing the same
// width, expansion maps, MCP names, and target slice through every nested row.
type transcriptRenderer struct {
	width          int
	expandAll      bool
	runExpanded    map[int]bool
	chainExpanded  map[string]bool
	detailExpanded map[string]bool
	mcpNames       []string
	targets        []transcriptTarget
	live           liveSnapshot
}

func (entry *chatEntry) appendDelta(kind chatSegmentKind, delta string, now int64) {
	if delta == "" {
		return
	}
	if entry.startedAt == 0 {
		entry.startedAt = now
	}
	if len(entry.segments) > 0 {
		last := &entry.segments[len(entry.segments)-1]
		if last.kind == kind && last.toolID == "" {
			last.text += delta
			return
		}
		if last.endedAt == 0 {
			last.endedAt = now
		}
	}
	entry.segments = append(entry.segments, chatSegment{kind: kind, text: delta, startedAt: now})
}

func (entry *chatEntry) hasTool(id string) bool {
	for index := range entry.tools {
		if entry.tools[index].id == id {
			return true
		}
	}
	return false
}

func (entry *chatEntry) hasSegmentKind(kind chatSegmentKind) bool {
	for _, segment := range entry.segments {
		if segment.kind == kind {
			return true
		}
	}
	return false
}

func (entry *chatEntry) startToolSegment(id string, now int64) {
	if len(entry.segments) > 0 {
		last := &entry.segments[len(entry.segments)-1]
		if last.kind == chatSegmentTool && last.toolID == id {
			return
		}
		if last.endedAt == 0 {
			last.endedAt = now
		}
	}
	entry.segments = append(entry.segments, chatSegment{kind: chatSegmentTool, toolID: id, startedAt: now})
}

func (entry *chatEntry) finishToolSegment(id string, now int64) {
	for index := range slices.Backward(entry.segments) {
		if entry.segments[index].kind == chatSegmentTool && entry.segments[index].toolID == id {
			entry.segments[index].endedAt = now
			return
		}
	}
}

func (entry *chatEntry) finishOpenSegments(now int64) {
	for index := range entry.segments {
		if entry.segments[index].endedAt == 0 {
			entry.segments[index].endedAt = now
		}
	}
}

func (entry *chatEntry) appendAssistantContent(message ai.AssistantMessage) {
	now := message.Timestamp
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	if entry.startedAt == 0 {
		entry.startedAt = now
	}
	for _, block := range message.Content {
		switch value := block.(type) {
		case ai.TextContent:
			entry.text += value.Text
			entry.appendDelta(chatSegmentText, value.Text, now)
		case *ai.TextContent:
			if value != nil {
				entry.text += value.Text
				entry.appendDelta(chatSegmentText, value.Text, now)
			}
		case ai.ThinkingContent:
			entry.thinking += value.Thinking
			entry.appendDelta(chatSegmentThinking, value.Thinking, now)
		case *ai.ThinkingContent:
			if value != nil {
				entry.thinking += value.Thinking
				entry.appendDelta(chatSegmentThinking, value.Thinking, now)
			}
		case ai.ToolCall:
			entry.appendToolCall(value, now)
		case *ai.ToolCall:
			if value != nil {
				entry.appendToolCall(*value, now)
			}
		}
	}
	if message.Timestamp > entry.endedAt {
		entry.endedAt = message.Timestamp
	}
}

func (entry *chatEntry) appendToolCall(call ai.ToolCall, now int64) {
	if call.ID == "" || entry.hasTool(call.ID) {
		return
	}
	entry.tools = append(entry.tools, chatTool{
		id: call.ID, name: call.Name, args: cloneArguments(call.Arguments), status: "running",
	})
	entry.startToolSegment(call.ID, now)
}

func buildTranscriptEntries(messages []ai.Message) []chatEntry {
	builder := transcriptBuilder{
		entries:         make([]chatEntry, 0, len(messages)),
		activeAssistant: -1,
	}
	for _, message := range messages {
		switch value := message.(type) {
		case ai.UserMessage:
			builder.addUser(value)
		case *ai.UserMessage:
			if value != nil {
				builder.addUser(*value)
			}
		case ai.AssistantMessage:
			builder.addAssistant(value)
		case *ai.AssistantMessage:
			if value != nil {
				builder.addAssistant(*value)
			}
		case ai.ToolResultMessage:
			builder.addToolResult(value)
		case *ai.ToolResultMessage:
			if value != nil {
				builder.addToolResult(*value)
			}
		}
	}
	return builder.entries
}

type transcriptBuilder struct {
	entries         []chatEntry
	activeAssistant int
	runStarted      int64
}

func (b *transcriptBuilder) addUser(message ai.UserMessage) {
	text := userMessageText(message)
	if text == "" {
		return
	}
	if message.Synthetic {
		// A checkpoint is context Midas wrote for the model, so the transcript
		// shows one muted marker instead of a prompt card.
		b.entries = append(b.entries, chatEntry{
			kind:      chatNotice,
			text:      "Compacted earlier context into a summary",
			startedAt: message.Timestamp,
		})
		b.activeAssistant = -1
		b.runStarted = message.Timestamp
		return
	}
	b.entries = append(b.entries, chatEntry{
		kind:        chatUser,
		text:        text,
		borderStyle: storedPromptBorderStyle(message),
		startedAt:   message.Timestamp,
	})
	b.activeAssistant = -1
	b.runStarted = message.Timestamp
}

func (b *transcriptBuilder) assistant(at int64) *chatEntry {
	if b.activeAssistant < 0 || b.activeAssistant >= len(b.entries) || b.entries[b.activeAssistant].kind != chatAssistant {
		b.entries = append(b.entries, chatEntry{kind: chatAssistant, startedAt: cmp.Or(b.runStarted, at)})
		b.activeAssistant = len(b.entries) - 1
	}
	return &b.entries[b.activeAssistant]
}

func (b *transcriptBuilder) addAssistant(message ai.AssistantMessage) {
	b.assistant(message.Timestamp).appendAssistantContent(message)
}

func (b *transcriptBuilder) addToolResult(result ai.ToolResultMessage) {
	entry := b.assistant(result.Timestamp)
	found := false
	for index := range entry.tools {
		if entry.tools[index].id != result.ToolCallID {
			continue
		}
		entry.tools[index].status = "done"
		entry.tools[index].result = storedToolResultText(result)
		entry.tools[index].isError = result.IsError
		found = true
		break
	}
	if !found {
		entry.tools = append(entry.tools, chatTool{
			id: result.ToolCallID, name: result.ToolName, result: storedToolResultText(result), status: "done", isError: result.IsError,
		})
		entry.startToolSegment(result.ToolCallID, cmp.Or(result.Timestamp, time.Now().UnixMilli()))
	}
	entry.finishToolSegment(result.ToolCallID, result.Timestamp)
	if result.Timestamp > entry.endedAt {
		entry.endedAt = result.Timestamp
	}
}

func storedToolResultText(result ai.ToolResultMessage) string {
	var output strings.Builder
	for _, block := range result.Content {
		switch value := block.(type) {
		case ai.TextContent:
			output.WriteString(value.Text)
		case *ai.TextContent:
			if value != nil {
				output.WriteString(value.Text)
			}
		}
	}
	return output.String()
}

func cloneTranscriptEntries(entries []chatEntry) []chatEntry {
	cloned := slices.Clone(entries)
	for index := range entries {
		cloned[index].segments = slices.Clone(entries[index].segments)
		cloned[index].tools = slices.Clone(entries[index].tools)
		for toolIndex := range entries[index].tools {
			cloned[index].tools[toolIndex].args = cloneArguments(entries[index].tools[toolIndex].args)
		}
		if entries[index].shell != nil {
			copy := *entries[index].shell
			cloned[index].shell = &copy
		}
	}
	return cloned
}

func (c *Chat) clearExpansionForEntryLocked(entry int) {
	if entry < 0 {
		return
	}
	delete(c.runExpanded, entry)
	prefix := fmt.Sprintf("%d:", entry)
	for key := range c.chainExpanded {
		if strings.HasPrefix(key, prefix) {
			delete(c.chainExpanded, key)
		}
	}
	for key := range c.detailExpanded {
		if strings.HasPrefix(key, prefix) {
			delete(c.detailExpanded, key)
		}
	}
}

func (c *Chat) renderTranscriptRuns(width int) []string {
	width = max(1, width)
	c.mu.Lock()
	entries := cloneTranscriptEntries(c.entries)
	active := c.active
	editorStyle := c.editorBorderStyle
	thinking := c.thinking
	renderer := transcriptRenderer{
		width:          width,
		expandAll:      c.expandedTools,
		runExpanded:    maps.Clone(c.runExpanded),
		chainExpanded:  maps.Clone(c.chainExpanded),
		detailExpanded: maps.Clone(c.detailExpanded),
		mcpNames:       slices.Clone(c.mcpNames),
		live:           c.liveSnapshotLocked(width, time.Now()),
	}
	c.mu.Unlock()

	lines := make([]string, 0)
	for entryIndex, entry := range entries {
		start := len(lines)
		switch entry.kind {
		case chatUser:
			style := entry.borderStyle
			if style == nil {
				style = editorStyle
				if style == nil {
					style = func(value string) string { return CurrentTheme().Thinking(thinking, value) }
				}
			}
			text := entry.text
			if entry.display != "" {
				text = entry.display
			}
			margin, available := chatRowGeometry(width)
			lines = append(lines, insetChatRows(renderPromptCard(text, available, style), margin)...)
		case chatAssistant:
			rendered := renderer.renderAssistant(entry, entryIndex, entryIndex == active, len(lines))
			lines = append(lines, markTranscriptContentLines(rendered)...)
			// The live action row sits right after the in-flight run, so a queued
			// or steered prompt below it stays in submission order.
			if entryIndex == active {
				lines = append(lines, renderer.renderLiveStatus(renderer.live, len(lines))...)
			}
		case chatShell:
			if entry.shell != nil {
				margin, available := chatRowGeometry(width)
				lines = append(lines, insetChatRows(renderShellCard(*entry.shell, available), margin)...)
			}
		case chatNotice:
			margin, available := chatRowGeometry(width)
			label := CurrentTheme().FG("muted", "✻ "+entry.text)
			lines = append(lines, strings.Repeat(" ", margin)+tuitext.TruncateToWidth(label, available, "…", false))
		}
		if len(lines) > start {
			lines = append(lines, "")
		}
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	c.mu.Lock()
	c.transcriptTargets = renderer.targets
	c.mu.Unlock()
	return lines
}

// insetChatRows indents a rounded card by the row gutter, so cards sit inside the
// same padding as every other Midas row instead of touching the terminal border.
func insetChatRows(lines []string, margin int) []string {
	if margin <= 0 {
		return lines
	}
	inset := strings.Repeat(" ", margin)
	insetLines := make([]string, len(lines))
	for index, line := range lines {
		insetLines[index] = inset + line
	}
	return insetLines
}

// markTranscriptContentLines adds zero-width selection bounds around the
// visible content of unframed transcript rows. Leading indentation and trailing
// layout fill remain outside the bounds, matching the previous Midas renderer.
func markTranscriptContentLines(lines []string) []string {
	for index := range lines {
		lines[index] = markTranscriptContent(lines[index])
	}
	return lines
}

func markTranscriptContent(line string) string {
	if line == "" || strings.Contains(line, ContentStartMarker) || strings.Contains(line, tuitext.DecorationMarker) {
		return line
	}
	first, last := -1, -1
	for index := 0; index < len(line); {
		if _, length, ok := tuitext.ExtractAnsiCode(line, index); ok {
			index += length
			continue
		}
		if line[index] != ' ' {
			if first < 0 {
				first = index
			}
			last = index
		}
		index++
	}
	if first < 0 {
		return line
	}
	return line[:first] + ContentStartMarker + line[first:last+1] + ContentEndMarker + line[last+1:]
}

func normalizedTranscriptSegments(entry chatEntry) []transcriptSegment {
	segments := entry.segments
	if len(segments) == 0 {
		if entry.thinking != "" {
			segments = append(segments, chatSegment{kind: chatSegmentThinking, text: entry.thinking})
		}
		for _, tool := range entry.tools {
			segments = append(segments, chatSegment{kind: chatSegmentTool, toolID: tool.id})
		}
		if entry.text != "" {
			segments = append(segments, chatSegment{kind: chatSegmentText, text: entry.text})
		}
	}
	tools := make(map[string]*chatTool, len(entry.tools))
	for index := range entry.tools {
		tools[entry.tools[index].id] = &entry.tools[index]
	}
	result := make([]transcriptSegment, 0, len(segments))
	for index, segment := range segments {
		if segment.kind == chatSegmentText {
			if strings.TrimSpace(segment.text) != "" {
				result = append(result, transcriptSegment{kind: chatSegmentText, text: segment.text, start: index})
			}
			continue
		}
		item := transcriptActivityItem{segment: segment, index: index}
		if segment.kind == chatSegmentTool {
			item.tool = tools[segment.toolID]
			if item.tool == nil {
				continue
			}
		}
		if len(result) > 0 && result[len(result)-1].kind != chatSegmentText {
			result[len(result)-1].items = append(result[len(result)-1].items, item)
			continue
		}
		result = append(result, transcriptSegment{kind: chatSegmentThinking, items: []transcriptActivityItem{item}, start: index})
	}
	return result
}

func (r *transcriptRenderer) renderAssistant(entry chatEntry, entryIndex int, active bool, base int) []string {
	segments := normalizedTranscriptSegments(entry)
	if len(segments) == 0 {
		return nil
	}
	hasActivity := false
	finalText := -1
	failures := 0
	for index, segment := range segments {
		if segment.kind == chatSegmentText {
			finalText = index
			continue
		}
		hasActivity = true
		for _, item := range segment.items {
			if item.tool != nil && item.tool.isError {
				failures++
			}
		}
	}
	lines := make([]string, 0)
	outerPad, _ := chatRowGeometry(r.width)
	render := func(segment transcriptSegment, pad int) {
		if segment.kind == chatSegmentText {
			lines = append(lines, renderTranscriptText(segment.text, r.width, pad)...)
			return
		}
		chainKey := fmt.Sprintf("%d:%d", entryIndex, segment.start)
		lines = append(lines, r.renderActivityChain(segment.items, pad, entryIndex, chainKey, base+len(lines))...)
	}

	if active {
		last := len(segments) - 1
		rendered := false
		for index, segment := range segments {
			if index == last && segment.kind != chatSegmentText {
				continue
			}
			// Keep the live tool lines apart from the output text that follows
			// them, matching the spacing of an expanded run.
			if rendered && segment.kind == chatSegmentText {
				lines = append(lines, "")
			}
			render(segment, outerPad)
			rendered = true
		}
		return lines
	}
	if !hasActivity {
		for _, segment := range segments {
			render(segment, outerPad)
		}
		return lines
	}

	expanded := r.expandAll || r.runExpanded[entryIndex]
	marker := "+"
	if expanded {
		marker = "-"
	}
	title := "Worked"
	if entry.startedAt > 0 && entry.endedAt > entry.startedAt {
		title += " for " + formatTranscriptDuration(entry.endedAt-entry.startedAt)
	}
	theme := CurrentTheme()
	header := strings.Repeat(" ", outerPad) + theme.FG("accent", marker) + " " + theme.FG("muted", title)
	if failures > 0 {
		header += theme.FG("muted", " · ") + theme.FG("error", fmt.Sprintf("%d failed", failures))
	}
	header = tuitext.TruncateToWidth(header, max(1, r.width-outerPad), "…", true)
	lines = append(lines, header)
	r.targets = append(r.targets, transcriptTarget{kind: transcriptRunTarget, start: base, end: base + 1, entry: entryIndex})
	if expanded {
		bodyStarted := false
		for index, segment := range segments {
			if index == finalText {
				continue
			}
			if !bodyStarted || segment.kind == chatSegmentText {
				lines = append(lines, "")
			}
			render(segment, outerPad+2)
			bodyStarted = true
		}
	}
	if finalText >= 0 {
		lines = append(lines, "")
		render(segments[finalText], outerPad)
	}
	return lines
}

func renderTranscriptText(value string, width, pad int) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	rightPad, _ := chatRowGeometry(width)
	available := max(1, width-pad-rightPad)
	lines := RenderMarkdown(value, available, CurrentTheme())
	prefix := strings.Repeat(" ", pad)
	for index := range lines {
		lines[index] = prefix + lines[index]
	}
	return lines
}

func (r *transcriptRenderer) renderActivityChain(items []transcriptActivityItem, pad, entry int, key string, base int) []string {
	if len(items) == 0 {
		return nil
	}
	if len(items) == 1 {
		return r.renderActivityItem(items[0], pad, entry, key, base)
	}
	theme := CurrentTheme()
	expanded := r.expandAll || r.chainExpanded[key]
	marker := "+"
	if expanded {
		marker = "-"
	}
	failures := 0
	for _, item := range items {
		if item.tool != nil && item.tool.isError {
			failures++
		}
	}
	header := strings.Repeat(" ", pad) + theme.FG("accent", marker) + " " + theme.FG("muted", summarizeTranscriptChain(items, r.mcpNames))
	if failures > 0 {
		header += theme.FG("muted", " · ") + theme.FG("error", fmt.Sprintf("%d failed", failures))
	}
	rightPad, _ := chatRowGeometry(r.width)
	header = tuitext.TruncateToWidth(header, max(1, r.width-rightPad), "…", true)
	lines := []string{header}
	r.targets = append(r.targets, transcriptTarget{kind: transcriptChainTarget, start: base, end: base + 1, entry: entry, key: key})
	if !expanded {
		return lines
	}
	for _, item := range items {
		itemLines := r.renderActivityItem(item, pad+2, entry, key, base+len(lines))
		lines = append(lines, itemLines...)
	}
	return lines
}

func (r *transcriptRenderer) renderActivityItem(item transcriptActivityItem, pad, entry int, chainKey string, base int) []string {
	detailKey := fmt.Sprintf("%s:%d", chainKey, item.index)
	expanded := r.expandAll || r.detailExpanded[detailKey]
	var lines []string
	rightPad, _ := chatRowGeometry(r.width)
	contentWidth := max(1, r.width-pad-rightPad)
	if item.tool != nil {
		lines = renderTranscriptTool(*item.tool, contentWidth, expanded, r.mcpNames)
	} else {
		lines = renderTranscriptThought(item.segment, contentWidth, expanded)
	}
	if len(lines) == 0 {
		return nil
	}
	prefix := strings.Repeat(" ", pad)
	for index := range lines {
		if index == 0 {
			lines[index] = prefix + tuitext.TruncateToWidth(lines[index], contentWidth, "…", true)
		} else {
			bodyPad := prefix + "  "
			lines[index] = bodyPad + tuitext.TruncateToWidth(lines[index], max(1, contentWidth-2), "…", true)
		}
	}
	r.targets = append(r.targets, transcriptTarget{kind: transcriptDetailTarget, start: base, end: base + len(lines), entry: entry, key: detailKey})
	return lines
}

func renderTranscriptThought(segment chatSegment, width int, expanded bool) []string {
	theme := CurrentTheme()
	label := "Thought"
	if segment.startedAt > 0 && segment.endedAt > segment.startedAt {
		label += " for " + formatTranscriptDuration(segment.endedAt-segment.startedAt)
	}
	marker := ""
	if strings.TrimSpace(segment.text) != "" {
		marker = "+ "
		if expanded {
			marker = "- "
		}
	}
	lines := []string{theme.FG("mdHeading", marker+label)}
	if !expanded || strings.TrimSpace(segment.text) == "" {
		return lines
	}
	body := strings.TrimSpace(segment.text)
	return append(lines, tuitext.WrapTextWithAnsi(theme.FG("thinkingText", body), max(1, width-2))...)
}

func renderTranscriptTool(tool chatTool, width int, expanded bool, mcpNames []string) []string {
	if strings.EqualFold(strings.TrimSpace(tool.name), "task") {
		return renderTranscriptTaskTool(tool, width, expanded)
	}
	theme := CurrentTheme()
	glyph := theme.FG("accent", "●")
	message := toolActionText(tool, false, mcpNames)
	if tool.status == "done" {
		glyph = theme.FG("success", "✓")
		message = toolActionText(tool, true, mcpNames)
	}
	if tool.isError {
		glyph = theme.FG("error", "✗")
	}
	lines := []string{glyph + " " + styleTranscriptVerb(message) + transcriptEditStats(tool)}
	if !expanded {
		return lines
	}
	return appendTranscriptPreview(lines, tool.result, tool.isError, width)
}

// transcriptEditStats renders the `+N -N` summary Midas shows for an edit: the
// added count in success green and the removed count in error red, omitting a
// zero count entirely. Edits that failed report no summary.
func transcriptEditStats(tool chatTool) string {
	if tool.isError || tool.status != "done" {
		return ""
	}
	added, removed, ok := editChangeStats(tool)
	if !ok || added == 0 && removed == 0 {
		return ""
	}
	theme := CurrentTheme()
	parts := make([]string, 0, 2)
	if added > 0 {
		parts = append(parts, theme.FG("success", fmt.Sprintf("+%d", added)))
	}
	if removed > 0 {
		parts = append(parts, theme.FG("error", fmt.Sprintf("-%d", removed)))
	}
	return " " + strings.Join(parts, " ")
}

// editChangeStats counts the lines an edit tool call added and removed. The
// counts come from the replacement arguments, because Midas's edit tool applies
// exact replacements and reports only how many blocks changed.
func editChangeStats(tool chatTool) (added, removed int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(tool.name)) {
	case "edit", "multiedit":
	default:
		return 0, 0, false
	}
	pairs := editArgumentPairs(tool.args)
	if len(pairs) == 0 {
		return 0, 0, false
	}
	for _, pair := range pairs {
		plus, minus := countLineChanges(pair[0], pair[1])
		added, removed = added+plus, removed+minus
	}
	return added, removed, true
}

// editArgumentPairs reads the old/new text pairs an edit call carries: the
// `edits` array the tool documents, or a single top-level old/new pair. Values
// arrive either decoded or as the JSON string the provider streamed.
func editArgumentPairs(args map[string]any) [][2]string {
	pairs := make([][2]string, 0, 1)
	value := args["edits"]
	if encoded, ok := value.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(encoded), &decoded); err == nil {
			value = decoded
		}
	}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			object, ok := item.(map[string]any)
			if !ok {
				continue
			}
			oldText, _ := object["oldText"].(string)
			newText, _ := object["newText"].(string)
			if oldText == "" && newText == "" {
				continue
			}
			pairs = append(pairs, [2]string{oldText, newText})
		}
	}
	if len(pairs) == 0 {
		oldText, _ := args["oldText"].(string)
		newText, _ := args["newText"].(string)
		if oldText != "" || newText != "" {
			pairs = append(pairs, [2]string{oldText, newText})
		}
	}
	return pairs
}

// maxDiffLines bounds the line-level comparison used for edit summaries; larger
// replacements fall back to trimming shared edges instead.
const maxDiffLines = 400

// countLineChanges counts the lines a replacement really changed, the way a diff
// hunk does: shared lines are context, everything else is an addition or a
// removal.
func countLineChanges(oldText, newText string) (added, removed int) {
	oldLines := transcriptTextLines(oldText)
	newLines := transcriptTextLines(newText)
	common := 0
	if len(oldLines) <= maxDiffLines && len(newLines) <= maxDiffLines {
		common = commonLineCount(oldLines, newLines)
	} else {
		common = commonEdgeLines(oldLines, newLines)
	}
	return len(newLines) - common, len(oldLines) - common
}

// commonLineCount is the longest common subsequence of two line lists.
func commonLineCount(left, right []string) int {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for i := 1; i <= len(left); i++ {
		for j := 1; j <= len(right); j++ {
			if left[i-1] == right[j-1] {
				current[j] = previous[j-1] + 1
				continue
			}
			current[j] = max(previous[j], current[j-1])
		}
		previous, current = current, previous
		for index := range current {
			current[index] = 0
		}
	}
	return previous[len(right)]
}

// commonEdgeLines counts the identical leading and trailing lines of a large
// replacement, which is enough to keep its summary honest without diffing it.
func commonEdgeLines(oldLines, newLines []string) int {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	return prefix + suffix
}

func transcriptTextLines(value string) []string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	// A trailing newline terminates the last line instead of adding one.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func renderTranscriptTaskTool(tool chatTool, width int, expanded bool) []string {
	theme := CurrentTheme()
	glyph := theme.FG("accent", "●")
	active := tool.status != "done"
	if !active {
		glyph = theme.FG("success", "✓")
	}
	if tool.isError {
		glyph = theme.FG("error", "✗")
	}
	agent := titleWords(defaultString(argumentString(tool.args, "subagent_type", "agent", "agent_type"), "subagent"))
	description := argumentString(tool.args, "description", "title")
	prompt := argumentString(tool.args, "prompt")
	if description == "" {
		description, _, _ = strings.Cut(prompt, "\n")
	}
	if description == "" {
		description = "Delegated task"
	}
	if !expanded {
		title := "Subagents (1 task):"
		if active {
			title = "Running " + title
		}
		return []string{
			tuitext.TruncateToWidth(glyph+" "+theme.FG("toolTitle", title), width, "…", true),
			tuitext.TruncateToWidth(glyph+" "+theme.FG("accent", agent+":")+" "+theme.FG("dim", description), width, "…", true),
		}
	}
	lines := []string{glyph + " " + theme.FG("toolTitle", agent)}
	if prompt != "" {
		lines = append(lines, "", theme.FG("muted", "─── Task ───"))
		lines = appendTranscriptPreview(lines, prompt, false, width)
	}
	output := unwrapTaskResult(tool.result)
	if strings.TrimSpace(output) != "" {
		lines = append(lines, "", theme.FG("muted", "─── Output ───"))
		lines = appendTranscriptPreview(lines, output, tool.isError, width)
	}
	return lines
}

func appendTranscriptPreview(lines []string, value string, isError bool, width int) []string {
	theme := CurrentTheme()
	preview := strings.TrimSpace(tuitext.StripTerminalSequences(value))
	if preview == "" {
		return lines
	}
	logical := strings.Split(strings.ReplaceAll(preview, "\r\n", "\n"), "\n")
	if qr, ok := renderQRBlock(logical, width); ok {
		// A QR code is meant to be scanned, so it is shown whole, bounded, and
		// centred rather than truncated like ordinary output.
		return append(lines, qr...)
	}
	hidden := max(0, len(logical)-transcriptPreviewLines)
	logical = logical[:min(len(logical), transcriptPreviewLines)]
	for _, line := range logical {
		name := "dim"
		if isError {
			name = "error"
		}
		lines = append(lines, theme.FG(name, line))
	}
	if hidden > 0 {
		lines = append(lines, theme.FG("muted", fmt.Sprintf("... %d more lines", hidden)))
	}
	return lines
}

// QR display bounds: a payload renders as an exact matrix, and a terminal only
// has so much room for it. An oversized code is replaced by a note, because half
// a QR code cannot be scanned and clipping it would only look like a bug.
const (
	qrBlockMaxRows = 30
)

// renderQRBlock centres a block of QR rows inside the transcript if the preview
// is one. Detection is deliberately strict: every line must consist of half
// blocks and spaces, with both present, which ordinary command output never is.
func renderQRBlock(lines []string, width int) ([]string, bool) {
	rows := make([]string, 0, len(lines))
	widest := 0
	hasBlock, hasSpace := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, character := range trimmed {
			switch character {
			case '█', '▀', '▄':
				hasBlock = true
			case ' ':
				hasSpace = true
			default:
				return nil, false
			}
		}
		rows = append(rows, trimmed)
		widest = max(widest, len([]rune(trimmed)))
	}
	if !hasBlock || !hasSpace || len(rows) < 8 || widest < 8 {
		return nil, false
	}
	if len(rows) > qrBlockMaxRows || width > 0 && qrWidth(rows) > qrAvailableWidth(width) {
		return []string{CurrentTheme().FG("muted", fmt.Sprintf(
			"QR code not shown: %d rows × %d columns exceeds this terminal's budget", len(rows), qrWidth(rows)))}, true
	}
	left := 0
	if available := qrAvailableWidth(width); available > 0 {
		left = max(0, (available-qrWidth(rows))/2)
	}
	rendered := make([]string, 0, len(rows))
	for _, row := range rows {
		rendered = append(rendered, strings.Repeat(" ", left)+CurrentTheme().FG("text", row))
	}
	return rendered, true
}

// qrWidth is the visual width of the widest row.
func qrWidth(rows []string) int {
	width := 0
	for _, row := range rows {
		width = max(width, len([]rune(row)))
	}
	return width
}

// qrAvailableWidth is the room a QR block has on one transcript row.
func qrAvailableWidth(width int) int {
	if width <= 0 {
		return 0
	}
	margin, available := chatRowGeometry(width)
	_ = margin
	return available
}

func unwrapTaskResult(value string) string {
	start := strings.Index(value, "<task_result>")
	end := strings.LastIndex(value, "</task_result>")
	if start < 0 || end <= start {
		return value
	}
	start += len("<task_result>")
	return strings.TrimSpace(value[start:end])
}

// intArgument reads a numeric tool argument, which decoded JSON delivers as a
// float64.
func intArgument(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	}
	return 0
}

func styleTranscriptVerb(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if space := strings.IndexByte(message, ' '); space >= 0 {
		return CurrentTheme().FG("toolTitle", message[:space]) + CurrentTheme().FG("muted", message[space:])
	}
	return CurrentTheme().FG("toolTitle", message)
}

func toolActionText(tool chatTool, done bool, mcpNames []string) string {
	name := strings.ToLower(strings.TrimSpace(tool.name))
	if title := mcpToolTitle(tool.name, mcpNames); title != "" {
		return title
	}
	path := argumentString(tool.args, "filePath", "file_path", "path")
	command := argumentString(tool.args, "command")
	query := argumentString(tool.args, "query", "pattern")
	switch name {
	case "read":
		if path == "" {
			path = "file"
		}
		if done {
			return "Read " + path
		}
		return "Reading " + path
	case "bash", "shell":
		if command == "" {
			command = "command"
		}
		if done {
			return "Ran `" + strings.Join(strings.Fields(command), " ") + "`"
		}
		return "Running `" + strings.Join(strings.Fields(command), " ") + "`"
	case "edit", "multiedit":
		if path == "" {
			path = "file"
		}
		if done {
			return "Edited " + path
		}
		return "Editing " + path
	case "compact":
		if tool.isError {
			if reason := argumentString(tool.args, "error"); reason != "" {
				return "Compaction Failed: " + reason
			}
			return "Compaction Failed"
		}
		before := intArgument(tool.args, "before")
		after := intArgument(tool.args, "after")
		if !done {
			return "Compacting…"
		}
		if before <= 0 {
			return "Compaction Successful"
		}
		saved := float64(before-after) / float64(before) * 100
		return fmt.Sprintf("Compaction Successful %d tokens -> %d tokens (%.0f%%)", before, after, saved)
	case "write":
		if path == "" {
			path = "file"
		}
		if done {
			return "Wrote " + path
		}
		return "Writing " + path
	case "grep":
		if done {
			return "Searched " + defaultString(query, "pattern")
		}
		return "Searching " + defaultString(query, "pattern")
	case "glob", "find", "list", "ls":
		if done {
			return "Explored " + defaultString(path, ".")
		}
		return "Exploring " + defaultString(path, ".")
	case "webfetch", "fetch":
		url := argumentString(tool.args, "url")
		if done {
			return "Fetched " + defaultString(url, "page")
		}
		return "Fetching " + defaultString(url, "page")
	case "skill":
		skill := titleWords(argumentString(tool.args, "name", "skill", "skill_name"))
		if done {
			return "Used " + defaultString(skill, "skill") + " skill"
		}
		return "Using " + defaultString(skill, "skill") + " skill"
	case "task":
		if done {
			return "Subagents (1 task)"
		}
		return "Running Subagents (1 task)"
	default:
		label := titleWords(tool.name)
		summary := toolArgumentSummary(tool.args)
		if done {
			if summary != "" {
				return "Ran " + label + ": " + summary
			}
			return "Ran " + label
		}
		if summary != "" {
			return "Running " + label + ": " + summary
		}
		return "Running " + label
	}
}

func argumentString(arguments map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := arguments[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func titleWords(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Tool"
	}
	var result strings.Builder
	previousSpace := true
	for index, char := range value {
		if char == '_' || char == '-' || unicode.IsSpace(char) {
			if !previousSpace {
				result.WriteByte(' ')
			}
			previousSpace = true
			continue
		}
		if index > 0 && unicode.IsUpper(char) && !previousSpace {
			result.WriteByte(' ')
		}
		if previousSpace {
			char = unicode.ToUpper(char)
		} else {
			char = unicode.ToLower(char)
		}
		result.WriteRune(char)
		previousSpace = false
	}
	return strings.TrimSpace(result.String())
}

func summarizeTranscriptChain(items []transcriptActivityItem, mcpNames []string) string {
	type group struct {
		verb, noun string
		count      int
		names      []string
	}
	groups := make(map[string]*group)
	order := make([]string, 0)
	add := func(key, verb, noun, name string) {
		value := groups[key]
		if value == nil {
			value = &group{verb: verb, noun: noun}
			groups[key] = value
			order = append(order, key)
		}
		value.count++
		if name != "" {
			if slices.Contains(value.names, name) {
				return
			}
			value.names = append(value.names, name)
		}
	}
	reasoning := 0
	for _, item := range items {
		if item.tool == nil {
			reasoning++
			continue
		}
		name := strings.ToLower(item.tool.name)
		if server := matchingMCPServer(item.tool.name, mcpNames); server != "" {
			add("mcp", "Used", "MCP", titleWords(server))
			continue
		}
		switch name {
		case "read":
			add("read", "Read", "file", "")
		case "bash", "shell":
			add("bash", "Ran", "command", "")
		case "edit", "multiedit", "write":
			add("edit", "Edited", "file", "")
		case "grep":
			add("grep", "Searched", "pattern", "")
		case "glob", "find", "list", "ls":
			add("find", "Explored", "path", "")
		case "webfetch", "fetch":
			add("fetch", "Fetched", "page", "")
		case "skill":
			add("skill", "Used", "skill", titleWords(argumentString(item.tool.args, "name", "skill", "skill_name")))
		default:
			add("tool", "Used", "tool", "")
		}
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		value := groups[key]
		label := ""
		if len(value.names) > 0 {
			label = value.verb + " " + joinNames(value.names) + " " + value.noun
			if len(value.names) > 1 {
				label += "s"
			}
		} else {
			label = fmt.Sprintf("%s %d %s", value.verb, value.count, value.noun)
			if value.count != 1 {
				label += "s"
			}
		}
		if len(parts) > 0 && label != "" {
			runes := []rune(label)
			runes[0] = unicode.ToLower(runes[0])
			label = string(runes)
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		if reasoning > 0 {
			return "Thought through the task"
		}
		return "Worked"
	}
	return strings.Join(parts[:min(3, len(parts))], ", ")
}

func matchingMCPServer(tool string, servers []string) string {
	best := ""
	for _, server := range servers {
		if len(server) <= len(best) {
			continue
		}
		if tool == server || strings.HasPrefix(tool, server+"_") {
			best = server
		}
	}
	return best
}

func mcpToolTitle(tool string, servers []string) string {
	server := matchingMCPServer(tool, servers)
	if server == "" {
		return ""
	}
	suffix := strings.TrimPrefix(strings.TrimPrefix(tool, server), "_")
	title := titleWords(server) + " MCP"
	if suffix != "" {
		title += ": " + titleWords(suffix)
	}
	return title
}

func joinNames(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func formatTranscriptDuration(milliseconds int64) string {
	if milliseconds < 1000 {
		return fmt.Sprintf("%dms", max(int64(0), milliseconds))
	}
	seconds := (milliseconds + 500) / 1000
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds %= 60
	if minutes < 60 {
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes %= 60
	if minutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, minutes)
}

func (c *Chat) handleTranscriptMouse(event MouseEvent) *MouseResult {
	if event.Type != MouseClick || event.Button != MouseLeft {
		return nil
	}
	y := event.Y - len(c.renderStartup(event.Width))
	if y < 0 {
		return nil
	}
	c.mu.Lock()
	for _, target := range c.transcriptTargets {
		if y < target.start || y >= target.end {
			continue
		}
		switch target.kind {
		case transcriptRunTarget:
			c.runExpanded[target.entry] = !c.runExpanded[target.entry]
		case transcriptChainTarget:
			c.chainExpanded[target.key] = !c.chainExpanded[target.key]
		case transcriptDetailTarget:
			c.detailExpanded[target.key] = !c.detailExpanded[target.key]
		case transcriptLiveTarget:
			c.liveExpanded = !c.liveExpanded
		}
		c.mu.Unlock()
		c.repaint()
		return &MouseResult{Handled: true}
	}
	c.mu.Unlock()
	return nil
}
