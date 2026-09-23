package tui

import (
	"encoding/base64"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

const (
	altContentStartMarker = "\x1b]777;midas-content-start\x07"
	altContentEndMarker   = "\x1b]777;midas-content-end\x07"
	copyOKStyle           = "\x1b[42m\x1b[30m"
	copyFailStyle         = "\x1b[41m\x1b[97m"
)

type altSelectionGranularity string

const (
	selectionCharacter altSelectionGranularity = "character"
	selectionWord      altSelectionGranularity = "word"
	selectionLine      altSelectionGranularity = "line"
)

type altSelectionPoint struct {
	row        int
	col        int
	scrollView *ScrollView
	boundary   bool
}

type altSelectionRange struct {
	start altSelectionPoint
	end   altSelectionPoint
}

type selectionClickState struct {
	timestamp          time.Time
	count, row         int
	scrollView         *ScrollView
	wordStart, wordEnd int
}

func cloneSelectionPoint(point altSelectionPoint) *altSelectionPoint {
	copy := point
	return &copy
}

func (t *TuiAltScreen) getScrollSelectionPoint(scrollView *ScrollView, x, y int) (altSelectionPoint, bool) {
	if t.currentLayout == nil {
		return altSelectionPoint{}, false
	}
	box, ok := GetScrollViewBox(*t.currentLayout, scrollView)
	if !ok || box.Rect.Height <= 0 || box.Clip.Height <= 0 {
		return altSelectionPoint{}, false
	}
	visibleTop := max(0, box.Rect.Y, box.Clip.Y)
	visibleBottom := min(t.Terminal.Rows()-1, box.Rect.Y+box.Rect.Height-1, box.Clip.Y+box.Clip.Height-1)
	if visibleBottom < visibleTop {
		return altSelectionPoint{}, false
	}
	pointerRow := max(visibleTop, min(visibleBottom, y))
	maxContentRow := max(0, len(box.ScrollContent)-1)
	return altSelectionPoint{
		row: max(0, min(maxContentRow, scrollView.ScrollTop()+pointerRow-box.Rect.Y)),
		col: max(0, min(box.Rect.Width-1, x-box.Rect.X)), scrollView: scrollView,
	}, true
}

func (t *TuiAltScreen) getSelectionPoint(event rawAltMouseEvent, scrollView *ScrollView) altSelectionPoint {
	if scrollView != nil {
		if point, ok := t.getScrollSelectionPoint(scrollView, event.x, event.y); ok {
			return point
		}
	}
	return altSelectionPoint{
		row: max(0, min(t.Terminal.Rows()-1, event.y)),
		col: max(0, min(t.Terminal.Columns()-1, event.x)),
	}
}

func (t *TuiAltScreen) getSelectionSourceLine(point altSelectionPoint) string {
	if point.scrollView != nil && t.currentLayout != nil {
		if box, ok := GetScrollViewBox(*t.currentLayout, point.scrollView); ok && point.row >= 0 && point.row < len(box.ScrollContent) {
			return box.ScrollContent[point.row]
		}
	}
	if point.row >= 0 && point.row < len(t.selectionScreen) {
		return t.selectionScreen[point.row]
	}
	return ""
}

type wordSelectionSegment struct {
	start, end int
	selectable bool
	joiner     bool
}

func selectionWordClass(grapheme string) int {
	if isJSWhitespace(grapheme) {
		return 0
	}
	if grapheme == "/" || grapheme == "-" {
		return 2
	}
	for _, char := range grapheme {
		if unicode.IsLetter(char) || unicode.IsNumber(char) || unicode.IsMark(char) || char == '_' {
			return 1
		}
	}
	return 3
}

func selectionWordSegments(line string) []wordSelectionSegment {
	segments := make([]wordSelectionSegment, 0)
	column := 0
	for _, grapheme := range tuitext.Graphemes(line) {
		width := tuitext.VisibleWidth(grapheme)
		class := selectionWordClass(grapheme)
		merge := len(segments) > 0 && (class == 0 || class == 1) &&
			selectionWordClass(tuitext.SliceByColumn(line, segments[len(segments)-1].start, segments[len(segments)-1].end-segments[len(segments)-1].start, true)) == class
		if merge {
			segments[len(segments)-1].end += width
		} else {
			segments = append(segments, wordSelectionSegment{start: column, end: column + width, selectable: class == 1 || class == 2, joiner: class == 2})
		}
		column += width
	}
	return segments
}

func (t *TuiAltScreen) getWordSelection(point altSelectionPoint) (altSelectionRange, bool) {
	line := tuitext.StripTerminalSequences(t.getSelectionSourceLine(point))
	segments := selectionWordSegments(line)
	clicked := -1
	for index, segment := range segments {
		if point.col >= segment.start && point.col < segment.end {
			clicked = index
			break
		}
	}
	if clicked < 0 {
		return altSelectionRange{}, false
	}
	start, end := segments[clicked].start, segments[clicked].end
	canJoin := func(left, right wordSelectionSegment) bool {
		return left.selectable && right.selectable && (left.joiner || right.joiner)
	}
	for index := clicked; index > 0 && canJoin(segments[index-1], segments[index]); index-- {
		start = segments[index-1].start
	}
	for index := clicked; index < len(segments)-1 && canJoin(segments[index], segments[index+1]); index++ {
		end = segments[index+1].end
	}
	startPoint, endPoint := point, point
	startPoint.col = start
	endPoint.col, endPoint.boundary = end, true
	return altSelectionRange{start: startPoint, end: endPoint}, true
}

func (t *TuiAltScreen) getLineSelection(point altSelectionPoint) altSelectionRange {
	start, end := point, point
	start.col = 0
	end.col, end.boundary = tuitext.VisibleWidth(t.getSelectionSourceLine(point)), true
	return altSelectionRange{start: start, end: end}
}

func pointBefore(left, right altSelectionPoint) bool {
	return left.row < right.row || (left.row == right.row && left.col < right.col)
}

func (t *TuiAltScreen) updateSelectionFocus(point altSelectionPoint) {
	if t.selectionGranularity == selectionCharacter || t.selectionInitialRange == nil {
		t.selectionFocus = cloneSelectionPoint(point)
		return
	}
	rangeValue := t.getLineSelection(point)
	if t.selectionGranularity == selectionWord {
		var ok bool
		rangeValue, ok = t.getWordSelection(point)
		if !ok {
			return
		}
	}
	initial := t.selectionInitialRange
	if pointBefore(rangeValue.start, initial.start) {
		t.selectionAnchor = cloneSelectionPoint(initial.end)
		t.selectionFocus = cloneSelectionPoint(rangeValue.start)
	} else {
		t.selectionAnchor = cloneSelectionPoint(initial.start)
		t.selectionFocus = cloneSelectionPoint(rangeValue.end)
	}
}

func (t *TuiAltScreen) selectionClickCount(point altSelectionPoint, word altSelectionRange, hasWord bool) int {
	now := time.Now()
	count := 1
	previous := t.lastSelectionClick
	if hasWord && previous != nil && now.Sub(previous.timestamp) <= doubleClickInterval && previous.row == point.row &&
		previous.scrollView == point.scrollView && previous.wordStart == word.start.col && previous.wordEnd == word.end.col {
		count = previous.count%3 + 1
	}
	if hasWord {
		t.lastSelectionClick = &selectionClickState{
			timestamp: now, count: count, row: point.row, scrollView: point.scrollView,
			wordStart: word.start.col, wordEnd: word.end.col,
		}
	} else {
		t.lastSelectionClick = nil
	}
	return count
}

func (t *TuiAltScreen) stopSelectionAutoScroll() {
	t.selectionTimerMu.Lock()
	t.selectionAutoScrollGeneration++
	if t.selectionAutoScrollTimer != nil {
		t.selectionAutoScrollTimer.Stop()
		t.selectionAutoScrollTimer = nil
	}
	t.selectionAutoScrollDirection = 0
	t.selectionDragPointer = nil
	t.selectionTimerMu.Unlock()
}

func (t *TuiAltScreen) updateSelectionAutoScroll(event rawAltMouseEvent) {
	anchor := t.selectionAnchor
	if anchor == nil || anchor.scrollView == nil || t.currentLayout == nil {
		t.stopSelectionAutoScroll()
		return
	}
	box, ok := GetScrollViewBox(*t.currentLayout, anchor.scrollView)
	if !ok || box.Rect.Height <= 0 || box.Clip.Height <= 0 {
		t.stopSelectionAutoScroll()
		return
	}
	visibleTop := max(0, box.Rect.Y, box.Clip.Y)
	visibleBottom := min(t.Terminal.Rows()-1, box.Rect.Y+box.Rect.Height-1, box.Clip.Y+box.Clip.Height-1)
	direction := 0
	if event.y <= visibleTop {
		direction = -1
	} else if event.y >= visibleBottom {
		direction = 1
	}
	if direction == 0 {
		t.stopSelectionAutoScroll()
		return
	}
	t.selectionTimerMu.Lock()
	t.selectionDragPointer = &mousePoint{x: event.x, y: event.y}
	t.selectionAutoScrollDirection = direction
	if t.selectionAutoScrollTimer == nil {
		t.selectionAutoScrollGeneration++
		generation := t.selectionAutoScrollGeneration
		t.selectionAutoScrollTimer = time.AfterFunc(50*time.Millisecond, func() { t.autoScrollSelection(generation) })
	}
	t.selectionTimerMu.Unlock()
}

func (t *TuiAltScreen) autoScrollSelection(generation uint64) {
	t.selectionTimerMu.Lock()
	if generation != t.selectionAutoScrollGeneration || t.selectionAnchor == nil || t.selectionAnchor.scrollView == nil || t.selectionDragPointer == nil {
		t.selectionAutoScrollTimer = nil
		t.selectionTimerMu.Unlock()
		return
	}
	direction := t.selectionAutoScrollDirection
	pointer := *t.selectionDragPointer
	scrollView := t.selectionAnchor.scrollView
	t.selectionTimerMu.Unlock()

	t.frameMu.Lock()
	remaining := scrollView.ScrollBy(direction)
	if remaining != direction {
		if point, ok := t.getScrollSelectionPoint(scrollView, pointer.x, pointer.y); ok {
			t.updateSelectionFocus(point)
		}
	}
	t.frameMu.Unlock()
	if remaining == direction {
		t.stopSelectionAutoScroll()
		return
	}
	t.RequestRender(false)
	t.selectionTimerMu.Lock()
	if generation == t.selectionAutoScrollGeneration {
		t.selectionAutoScrollTimer = time.AfterFunc(50*time.Millisecond, func() { t.autoScrollSelection(generation) })
	}
	t.selectionTimerMu.Unlock()
}

func (t *TuiAltScreen) clearTextSelection() {
	t.stopSelectionAutoScroll()
	t.selectionPressActive = false
	t.selectionAnchor, t.selectionFocus = nil, nil
	t.selectionGranularity = selectionCharacter
	t.selectionInitialRange = nil
	t.pressedURL, t.hasPressedURL = "", false
	t.selectionDragged = false
}

func (t *TuiAltScreen) handleSelectionMouseEvent(event rawAltMouseEvent) {
	button := event.button & 3
	if button != 0 && !(event.release && button == 3) {
		return
	}
	var anchorScrollView *ScrollView
	if t.selectionAnchor != nil {
		anchorScrollView = t.selectionAnchor.scrollView
	}
	point := t.getSelectionPoint(event, anchorScrollView)
	if event.release {
		if !t.selectionPressActive {
			return
		}
		t.selectionPressActive = false
		t.stopSelectionAutoScroll()
		if t.selectionAnchor == nil {
			return
		}
		t.updateSelectionFocus(point)
		isClick := !t.selectionDragged && t.selectionAnchor.scrollView == point.scrollView &&
			t.selectionAnchor.row == point.row && t.selectionAnchor.col == point.col
		clickedURL, hasURL := t.pressedURL, isClick && t.hasPressedURL
		t.pressedURL, t.hasPressedURL = "", false
		if hasURL && t.openURL != nil {
			t.selectionAnchor, t.selectionFocus = nil, nil
			func() { defer func() { _ = recover() }(); t.openURL(clickedURL) }()
			t.RequestRender(false)
			return
		}
		if isClick {
			count := 1
			if t.lastSelectionClick != nil {
				count = t.lastSelectionClick.count
			}
			click := t.createAltMouseEvent(MouseClick, event.button, event.x, event.y, nil, &count)
			overlay := t.DispatchMouseToOverlay(click)
			result := overlay.Result
			if result == nil && !overlay.Hit {
				result = t.dispatchMouseToLayout(click)
			}
			if result != nil {
				render := t.applyMouseDispatchResult(click, result)
				t.clearTextSelection()
				if render {
					t.RequestRender(false)
				}
				return
			}
		}
		if t.copyOnSelect {
			t.copySelectionToClipboard()
		}
		// The highlight is a drag affordance: once the selection has been
		// delivered the viewport paints plain text again, exactly like every
		// other surface.
		t.clearTextSelection()
		t.RequestRender(false)
		return
	}
	if event.button&32 != 0 {
		if !t.selectionPressActive || t.selectionAnchor == nil {
			return
		}
		t.selectionDragged = true
		t.lastSelectionClick = nil
		t.pressedURL, t.hasPressedURL = "", false
		t.updateSelectionFocus(point)
		t.updateSelectionAutoScroll(event)
		t.RequestRender(false)
		return
	}
	t.anchorSelection(event)
}

// anchorSelection starts a selection at a press point, applying the click-count
// granularity (word on a double click, line on a triple click). Presses on a
// component that reports itself selectable reuse it, so highlighting works over
// the transcript and the input box alike.
func (t *TuiAltScreen) anchorSelection(event rawAltMouseEvent) {
	t.stopSelectionAutoScroll()
	t.selectionPressActive = true
	var scrollView *ScrollView
	if !t.HasOverlay() && t.currentLayout != nil {
		views := GetScrollViewsAt(*t.currentLayout, event.x, event.y)
		if len(views) > 0 {
			scrollView = views[0]
		}
	}
	anchor := t.getSelectionPoint(event, scrollView)
	word, hasWord := t.getWordSelection(anchor)
	clickCount := t.selectionClickCount(anchor, word, hasWord)
	var selectionRange *altSelectionRange
	if clickCount == 2 && hasWord {
		selectionRange = &word
	}
	if clickCount == 3 {
		value := t.getLineSelection(anchor)
		selectionRange = &value
	}
	if selectionRange != nil {
		if clickCount == 2 {
			t.selectionGranularity = selectionWord
		} else {
			t.selectionGranularity = selectionLine
		}
		copy := *selectionRange
		t.selectionInitialRange = &copy
		t.selectionAnchor, t.selectionFocus = cloneSelectionPoint(selectionRange.start), cloneSelectionPoint(selectionRange.end)
	} else {
		t.selectionGranularity = selectionCharacter
		t.selectionInitialRange = nil
		t.selectionAnchor, t.selectionFocus = cloneSelectionPoint(anchor), cloneSelectionPoint(anchor)
	}
	t.selectionDragged = false
	if selectionRange == nil {
		row := max(0, min(t.Terminal.Rows()-1, event.y))
		column := max(0, min(t.Terminal.Columns()-1, event.x))
		if row < len(t.previousScreen) {
			t.pressedURL, t.hasPressedURL = tuitext.GetOsc8LinkAtColumn(t.previousScreen[row], column)
		}
	} else {
		t.pressedURL, t.hasPressedURL = "", false
	}
	t.RequestRender(false)
}

// extendSelection follows a drag that started on a selectable component, so the
// selection tracks the pointer past the component's own bounds.
func (t *TuiAltScreen) extendSelection(event rawAltMouseEvent) {
	if !t.selectionPressActive || t.selectionAnchor == nil {
		return
	}
	t.selectionDragged = true
	t.lastSelectionClick = nil
	t.pressedURL, t.hasPressedURL = "", false
	point := t.getSelectionPoint(event, t.selectionAnchor.scrollView)
	t.updateSelectionFocus(point)
	t.updateSelectionAutoScroll(event)
	t.RequestRender(false)
}

// finishSelection delivers a completed drag: the selection is copied (when
// copy-on-select is on) and the highlight is dropped, so no stale highlight is
// left behind on any surface.
func (t *TuiAltScreen) finishSelection() {
	if !t.selectionPressActive {
		return
	}
	t.selectionPressActive = false
	if t.copyOnSelect {
		// A zero-width selection (a plain click) has nothing to copy, so this is a
		// no-op there and a copy for a drag or a word/line selection.
		t.copySelectionToClipboard()
	}
	t.clearTextSelection()
	t.RequestRender(false)
}

func (t *TuiAltScreen) selectionBounds() (altSelectionRange, bool) {
	if t.selectionAnchor == nil || t.selectionFocus == nil || t.selectionAnchor.scrollView != t.selectionFocus.scrollView {
		return altSelectionRange{}, false
	}
	if t.selectionAnchor.row == t.selectionFocus.row && t.selectionAnchor.col == t.selectionFocus.col {
		return altSelectionRange{}, false
	}
	if pointBefore(*t.selectionAnchor, *t.selectionFocus) {
		return altSelectionRange{start: *t.selectionAnchor, end: *t.selectionFocus}, true
	}
	return altSelectionRange{start: *t.selectionFocus, end: *t.selectionAnchor}, true
}

func trimJSEnd(value string) string {
	return strings.TrimRightFunc(value, isJSWhitespaceRune)
}

func selectionColumns(line string, row int, selection altSelectionRange, minColumn, maxColumn int) (int, int) {
	lineWidth := tuitext.VisibleWidth(line)
	start, end := max(0, minColumn), min(lineWidth, maxColumn)
	if row == selection.start.row {
		if cell, ok := tuitext.GetGraphemeCellRange(line, selection.start.col); ok {
			start = cell.Start
		} else {
			start = min(selection.start.col, lineWidth)
		}
	}
	if row == selection.end.row {
		if selection.end.boundary {
			end = min(selection.end.col, lineWidth)
		} else if cell, ok := tuitext.GetGraphemeCellRange(line, selection.end.col); ok {
			end = cell.End
		} else {
			end = min(selection.end.col+1, lineWidth)
		}
	}
	contentStart := minColumn
	contentEnd := min(maxColumn, tuitext.VisibleWidth(trimJSEnd(tuitext.StripTerminalSequences(line))))
	for offset := 0; offset < len(line); {
		startIndex := strings.Index(line[offset:], altContentStartMarker)
		endIndex := strings.Index(line[offset:], altContentEndMarker)
		if startIndex < 0 && endIndex < 0 {
			break
		}
		isStart := startIndex >= 0 && (endIndex < 0 || startIndex < endIndex)
		index := endIndex
		markerLength := len(altContentEndMarker)
		if isStart {
			index, markerLength = startIndex, len(altContentStartMarker)
		}
		index += offset
		column := tuitext.VisibleWidth(line[:index])
		if isStart {
			contentStart = max(contentStart, column)
		} else {
			contentEnd = min(contentEnd, column)
		}
		offset = index + markerLength
	}
	return max(minColumn, start, contentStart), min(maxColumn, end, contentEnd)
}

// GetActiveSelectionText returns marker-filtered selected text.
func (t *TuiAltScreen) GetActiveSelectionText() (string, bool) {
	selection, ok := t.selectionBounds()
	if !ok {
		return "", false
	}
	source := t.selectionScreen
	if selection.start.scrollView != nil {
		if t.currentLayout == nil {
			return "", false
		}
		box, found := GetScrollViewBox(*t.currentLayout, selection.start.scrollView)
		if !found || !box.HasScrollContent {
			return "", false
		}
		source = box.ScrollContent
	}
	lines := make([]string, 0, selection.end.row-selection.start.row+1)
	for row := selection.start.row; row <= selection.end.row; row++ {
		line := ""
		if row >= 0 && row < len(source) {
			line = source[row]
		}
		if strings.Contains(line, tuitext.DecorationMarker) {
			continue
		}
		start, end := selectionColumns(line, row, selection, 0, tuitext.VisibleWidth(line))
		selected := tuitext.SliceByColumn(line, start, max(0, end-start), true)
		lines = append(lines, trimJSEnd(tuitext.StripTerminalSequences(selected)))
	}
	text := strings.Join(lines, "\n")
	return text, text != ""
}

func (t *TuiAltScreen) HasActiveSelection() bool     { _, ok := t.GetActiveSelectionText(); return ok }
func (t *TuiAltScreen) GetCopyOnSelect() bool        { return t.copyOnSelect }
func (t *TuiAltScreen) SetCopyOnSelect(enabled bool) { t.copyOnSelect = enabled }

func (t *TuiAltScreen) CopyActiveSelectionToClipboard() bool {
	text, ok := t.GetActiveSelectionText()
	if !ok {
		return false
	}
	return t.copyTextToClipboard(text)
}

func (t *TuiAltScreen) copySelectionToClipboard() bool { return t.CopyActiveSelectionToClipboard() }

func (t *TuiAltScreen) copyTextToClipboard(text string) bool {
	if t.copySelection != nil {
		ok := false
		func() { defer func() { _ = recover() }(); ok = t.copySelection(text) }()
		if ok {
			t.flashes.Flash("Copied!", defaultAltScreenFlashDuration, copyOKStyle)
		} else {
			t.flashes.Flash("Copy failed", defaultAltScreenFlashDuration, copyFailStyle)
		}
		return ok
	}
	t.Terminal.Write("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07")
	t.flashes.Flash("Copied!", defaultAltScreenFlashDuration, copyOKStyle)
	return true
}

func applySelectionHighlight(text string) string {
	var result strings.Builder
	result.WriteString("\x1b[7m")
	for index := 0; index < len(text); {
		if code, length, ok := tuitext.ExtractAnsiCode(text, index); ok {
			result.WriteString(code)
			if strings.HasSuffix(code, "m") {
				result.WriteString("\x1b[7m")
			}
			index += length
			continue
		}
		_, size := utf8.DecodeRuneInString(text[index:])
		result.WriteString(text[index : index+size])
		index += size
	}
	result.WriteString("\x1b[27m")
	return result.String()
}

func (t *TuiAltScreen) applySelectionLocked(screen []string, layout LayoutFrame) []string {
	selection, ok := t.selectionBounds()
	if !ok {
		return screen
	}
	screenSelection := selection
	minRow, maxRow := 0, len(screen)-1
	minColumn, maxColumn := 0, t.Terminal.Columns()
	if selection.start.scrollView != nil {
		box, found := GetScrollViewBox(layout, selection.start.scrollView)
		if !found {
			return screen
		}
		minRow = max(0, box.Rect.Y, box.Clip.Y)
		maxRow = min(len(screen)-1, box.Rect.Y+box.Rect.Height-1, box.Clip.Y+box.Clip.Height-1)
		minColumn = max(0, box.Rect.X, box.Clip.X)
		maxColumn = min(t.Terminal.Columns(), box.Rect.X+box.Rect.Width, box.Clip.X+box.Clip.Width)
		screenSelection.start.row = box.Rect.Y + selection.start.row - selection.start.scrollView.ScrollTop()
		screenSelection.start.col = box.Rect.X + selection.start.col
		screenSelection.end.row = box.Rect.Y + selection.end.row - selection.start.scrollView.ScrollTop()
		screenSelection.end.col = box.Rect.X + selection.end.col
	}
	result := append([]string(nil), screen...)
	for row, line := range result {
		if row < minRow || row > maxRow || row < screenSelection.start.row || row > screenSelection.end.row || IsImageLine(line) {
			continue
		}
		lineWidth := tuitext.VisibleWidth(line)
		start, end := selectionColumns(line, row, screenSelection, minColumn, maxColumn)
		if end <= start {
			continue
		}
		before := tuitext.SliceByColumn(line, 0, start, true)
		selected := tuitext.SliceByColumn(line, start, end-start, true)
		after := tuitext.SliceByColumn(line, end, max(0, lineWidth-end), true)
		result[row] = before + applySelectionHighlight(selected) + after
	}
	return result
}

func (t *TuiAltScreen) handleRightClickPaste(event rawAltMouseEvent) bool {
	if t.onRightClickPaste == nil || runtime.GOOS != "windows" || strings.EqualFold(os.Getenv("TERM_PROGRAM"), "vscode") || event.release || event.button != 2 {
		return false
	}
	func() { defer func() { _ = recover() }(); t.onRightClickPaste() }()
	return true
}
