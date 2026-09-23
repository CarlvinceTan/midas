package tui

import (
	"cmp"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

// AltScreenSearchSegment maps one literal match back to terminal-cell geometry.
type AltScreenSearchSegment struct {
	Row      int `json:"row"`
	StartCol int `json:"startCol"`
	EndCol   int `json:"endCol"`
}

// AltScreenSearchMatch may contain multiple segments when whitespace crosses rows.
type AltScreenSearchMatch struct {
	Segments []AltScreenSearchSegment `json:"segments"`
}

// AltScreenSearchResult reports cached matches and whether source/query changed.
type AltScreenSearchResult struct {
	Matches []AltScreenSearchMatch `json:"matches"`
	Changed bool                   `json:"changed"`
}

type searchSourceSpan struct {
	textStart, textEnd    int
	row, startCol, endCol int
	linearColumns         bool
}

type searchCorpus struct {
	text  string
	spans []searchSourceSpan
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func isJSWhitespaceRune(char rune) bool {
	switch char {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return char >= 0x2000 && char <= 0x200a
}

func isJSWhitespace(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !isJSWhitespaceRune(char) {
			return false
		}
	}
	return true
}

func normalizeSearchQuery(query string) string {
	var result strings.Builder
	pendingSpace := false
	wrote := false
	for _, char := range query {
		if isJSWhitespaceRune(char) {
			if wrote {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			result.WriteByte(' ')
			pendingSpace = false
		}
		result.WriteRune(char)
		wrote = true
	}
	return result.String()
}

func buildSearchCorpus(lines []string) searchCorpus {
	var text strings.Builder
	spans := make([]searchSourceSpan, 0)
	textLength := 0
	pendingSeparator := false
	appendSeparator := func() {
		if pendingSeparator {
			text.WriteByte(' ')
			textLength++
			pendingSeparator = false
		}
	}
	for row, sourceLine := range lines {
		line := tuitext.StripTerminalSequences(sourceLine)
		printableASCII := true
		for _, char := range []byte(line) {
			if char < 0x20 || char > 0x7e {
				printableASCII = false
				break
			}
		}
		column := 0
		if printableASCII {
			for index := 0; index < len(line); {
				if line[index] == ' ' {
					if textLength > 0 {
						pendingSeparator = true
					}
					column++
					index++
					continue
				}
				end := index + 1
				for end < len(line) && line[end] != ' ' {
					end++
				}
				appendSeparator()
				chunk := line[index:end]
				text.WriteString(chunk)
				spans = append(spans, searchSourceSpan{
					textStart: textLength, textEnd: textLength + len(chunk), row: row,
					startCol: column, endCol: column + len(chunk), linearColumns: true,
				})
				textLength += len(chunk)
				column += len(chunk)
				index = end
			}
		} else {
			for _, grapheme := range tuitext.Graphemes(line) {
				width := tuitext.VisibleWidth(grapheme)
				if isJSWhitespace(grapheme) {
					if textLength > 0 {
						pendingSeparator = true
					}
					column += width
					continue
				}
				appendSeparator()
				length := utf16Length(grapheme)
				text.WriteString(grapheme)
				spans = append(spans, searchSourceSpan{
					textStart: textLength, textEnd: textLength + length, row: row,
					startCol: column, endCol: column + width,
				})
				textLength += length
				column += width
			}
		}
		if textLength > 0 {
			pendingSeparator = true
		}
	}
	return searchCorpus{text: text.String(), spans: spans}
}

func findSearchCorpusMatches(corpus searchCorpus, normalizedQuery string) []AltScreenSearchMatch {
	if normalizedQuery == "" {
		return []AltScreenSearchMatch{}
	}
	expression, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(normalizedQuery))
	if err != nil {
		return []AltScreenSearchMatch{}
	}
	indices := expression.FindAllStringIndex(corpus.text, -1)
	matches := make([]AltScreenSearchMatch, 0, len(indices))
	spanIndex := 0
	for _, byteRange := range indices {
		start := utf16Length(corpus.text[:byteRange[0]])
		end := start + utf16Length(corpus.text[byteRange[0]:byteRange[1]])
		for spanIndex < len(corpus.spans) && corpus.spans[spanIndex].textEnd <= start {
			spanIndex++
		}
		segments := make([]AltScreenSearchSegment, 0)
		for index := spanIndex; index < len(corpus.spans); index++ {
			span := corpus.spans[index]
			if span.textStart >= end {
				break
			}
			if span.textEnd <= start {
				continue
			}
			startCol, endCol := span.startCol, span.endCol
			if span.linearColumns {
				startCol += max(start, span.textStart) - span.textStart
				endCol = span.startCol + min(end, span.textEnd) - span.textStart
			}
			if len(segments) > 0 && segments[len(segments)-1].Row == span.row && startCol <= segments[len(segments)-1].EndCol {
				segments[len(segments)-1].EndCol = max(segments[len(segments)-1].EndCol, endCol)
			} else {
				segments = append(segments, AltScreenSearchSegment{Row: span.row, StartCol: startCol, EndCol: endCol})
			}
		}
		for spanIndex < len(corpus.spans) && corpus.spans[spanIndex].textEnd <= end {
			spanIndex++
		}
		if len(segments) > 0 {
			matches = append(matches, AltScreenSearchMatch{Segments: segments})
		}
	}
	return matches
}

// FindAltScreenSearchMatches performs literal case-insensitive transcript search.
func FindAltScreenSearchMatches(lines []string, query string) []AltScreenSearchMatch {
	normalized := normalizeSearchQuery(query)
	if normalized == "" {
		return []AltScreenSearchMatch{}
	}
	return findSearchCorpusMatches(buildSearchCorpus(lines), normalized)
}

// GetAltScreenSearchMatchKey returns the geometry-stable key used to retain selection.
func GetAltScreenSearchMatchKey(match AltScreenSearchMatch) string {
	if len(match.Segments) == 0 {
		return ""
	}
	first, last := match.Segments[0], match.Segments[len(match.Segments)-1]
	return strconv.Itoa(first.Row) + ":" + strconv.Itoa(first.StartCol) + ":" + strconv.Itoa(last.Row) + ":" + strconv.Itoa(last.EndCol)
}

// AltScreenSearchIndex caches its corpus and result while source and normalized query are stable.
type AltScreenSearchIndex struct {
	sourceLines     []string
	corpus          *searchCorpus
	normalizedQuery string
	hasQuery        bool
	matches         []AltScreenSearchMatch
}

func (i *AltScreenSearchIndex) Search(lines []string, query string) AltScreenSearchResult {
	sourceChanged := len(i.sourceLines) != len(lines)
	if !sourceChanged {
		for index := range lines {
			if i.sourceLines[index] != lines[index] {
				sourceChanged = true
				break
			}
		}
	}
	if sourceChanged || i.corpus == nil {
		i.sourceLines = append([]string(nil), lines...)
		corpus := buildSearchCorpus(lines)
		i.corpus = &corpus
	}
	normalized := normalizeSearchQuery(query)
	changed := sourceChanged || !i.hasQuery || normalized != i.normalizedQuery
	if changed {
		i.normalizedQuery, i.hasQuery = normalized, true
		i.matches = findSearchCorpusMatches(*i.corpus, normalized)
	}
	return AltScreenSearchResult{Matches: i.matches, Changed: changed}
}

// SearchNavigationButtonStyle styles one navigation label and receives hover state.
type SearchNavigationButtonStyle func(text string, hovered bool) string

// AltScreenSearchComponent is the three-row search overlay.
type AltScreenSearchComponent struct {
	input                      *Input
	onQueryChange              func(string)
	navigationButtonStyle      SearchNavigationButtonStyle
	resultCount, resultIndex   int
	previousButtonStart        int
	previousButtonEnd          int
	nextButtonStart            int
	nextButtonEnd              int
	hoveredNavigationDirection int
	hasHoveredDirection        bool
}

func NewAltScreenSearchComponent(onQueryChange func(string), style SearchNavigationButtonStyle) *AltScreenSearchComponent {
	if style == nil {
		style = func(text string, _ bool) string { return text }
	}
	return &AltScreenSearchComponent{
		input: NewInput(InputOptions{
			Prompt: " ", Placeholder: "Find in transcript",
			PlaceholderStyle: func(text string) string { return "\x1b[2m" + text + "\x1b[22m" },
		}),
		onQueryChange: onQueryChange, navigationButtonStyle: style,
		resultIndex: -1, previousButtonStart: -1, previousButtonEnd: -1, nextButtonStart: -1, nextButtonEnd: -1,
	}
}

func (c *AltScreenSearchComponent) IsFocused() bool         { return c.input.IsFocused() }
func (c *AltScreenSearchComponent) SetFocused(focused bool) { c.input.SetFocused(focused) }
func (c *AltScreenSearchComponent) Invalidate()             { c.input.Invalidate() }
func (c *AltScreenSearchComponent) Query() string           { return c.input.Value() }

func (c *AltScreenSearchComponent) SetResult(index, count int) {
	c.resultIndex, c.resultCount = index, count
}

func (c *AltScreenSearchComponent) NavigationDirectionAt(row, column int) (int, bool) {
	if row != 2 {
		return 0, false
	}
	if column >= c.previousButtonStart && column < c.previousButtonEnd {
		return -1, true
	}
	if column >= c.nextButtonStart && column < c.nextButtonEnd {
		return 1, true
	}
	return 0, false
}

func (c *AltScreenSearchComponent) SetHoveredNavigationDirection(direction int, present bool) bool {
	if c.hasHoveredDirection == present && (!present || c.hoveredNavigationDirection == direction) {
		return false
	}
	c.hoveredNavigationDirection, c.hasHoveredDirection = direction, present
	return true
}

func (c *AltScreenSearchComponent) HandleInput(data string) {
	previous := c.input.Value()
	c.input.HandleInput(data)
	query := c.input.Value()
	if query != previous && c.onQueryChange != nil {
		c.onQueryChange(query)
	}
}

func formatSearchKey(key KeyID) string {
	if key == "" {
		return "Unbound"
	}
	parts := strings.Split(string(key), "+")
	for index, part := range parts {
		if runtime.GOOS == "darwin" && strings.EqualFold(part, "alt") {
			parts[index] = "Option"
		} else if part != "" {
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "+")
}

func (c *AltScreenSearchComponent) Render(width int) []string {
	safeWidth := max(1, width)
	innerWidth := max(0, safeWidth-2)
	bindings := GetKeybindings()
	previousKeys, nextKeys := bindings.Keys("tui.altScreen.searchPrevious"), bindings.Keys("tui.altScreen.searchNext")
	var previousKey, nextKey KeyID
	if len(previousKeys) > 0 {
		previousKey = previousKeys[0]
	}
	if len(nextKeys) > 0 {
		nextKey = nextKeys[0]
	}
	result := ""
	if c.input.Value() != "" {
		if c.resultCount == 0 {
			result = "No matches"
		} else {
			result = strconv.Itoa(c.resultIndex+1) + "/" + strconv.Itoa(c.resultCount)
		}
	}
	visibleResult := tuitext.TruncateToWidth(result, max(0, innerWidth-3), "", false)
	resultText := ""
	if visibleResult != "" {
		resultText = "\x1b[2m " + visibleResult + " \x1b[22m"
	}
	inputWidth := max(0, innerWidth-tuitext.VisibleWidth(resultText))
	inputLine := tuitext.TruncateToWidth(c.input.Render(max(1, inputWidth))[0], inputWidth, "", false)
	content := inputLine + strings.Repeat(" ", max(0, inputWidth-tuitext.VisibleWidth(inputLine))) + resultText
	previousButton := "↑ " + formatSearchKey(previousKey)
	nextButton := "↓ " + formatSearchKey(nextKey)
	separator := " · "
	availableControlsWidth := max(0, innerWidth-3)
	controlsWidth := tuitext.VisibleWidth(previousButton) + tuitext.VisibleWidth(separator) + tuitext.VisibleWidth(nextButton)
	if controlsWidth > availableControlsWidth {
		previousButton, nextButton, separator = "↑", "↓", " "
		controlsWidth = 3
	}
	showButtons := controlsWidth <= availableControlsWidth
	renderedButtons := ""
	if showButtons {
		renderedButtons = c.navigationButtonStyle(previousButton, c.hasHoveredDirection && c.hoveredNavigationDirection == -1) + separator + c.navigationButtonStyle(nextButton, c.hasHoveredDirection && c.hoveredNavigationDirection == 1)
	}
	outerGapsWidth := 0
	if showButtons {
		outerGapsWidth = 2
	}
	rightRuleWidth := 0
	if renderedButtons != "" && innerWidth > controlsWidth+outerGapsWidth {
		rightRuleWidth = 1
	}
	leftRuleWidth := max(0, innerWidth-controlsWidth*boolInt(showButtons)-outerGapsWidth-rightRuleWidth)
	previousStart := 1 + leftRuleWidth + 1
	if showButtons {
		c.previousButtonStart, c.previousButtonEnd = previousStart, previousStart+tuitext.VisibleWidth(previousButton)
		c.nextButtonStart = c.previousButtonEnd + tuitext.VisibleWidth(separator)
		c.nextButtonEnd = c.nextButtonStart + tuitext.VisibleWidth(nextButton)
	} else {
		c.previousButtonStart, c.previousButtonEnd, c.nextButtonStart, c.nextButtonEnd = -1, -1, -1, -1
	}
	if safeWidth == 1 {
		return []string{"┌", "│", "└"}
	}
	gap, buttons := "", ""
	if renderedButtons != "" {
		gap, buttons = " ", renderedButtons+" "
	}
	return []string{
		"┌" + strings.Repeat("─", innerWidth) + "┐",
		"│" + content + "│",
		"└" + strings.Repeat("─", leftRuleWidth) + gap + buttons + strings.Repeat("─", rightRuleWidth) + "┘",
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type altScreenSearchSelectionMode string

const (
	searchSelectionQuery    altScreenSearchSelectionMode = "query"
	searchSelectionRetain   altScreenSearchSelectionMode = "retain"
	searchSelectionNext     altScreenSearchSelectionMode = "next"
	searchSelectionPrevious altScreenSearchSelectionMode = "previous"
)

type activeAltScreenSearch struct {
	component      *AltScreenSearchComponent
	index          AltScreenSearchIndex
	overlay        *OverlayHandle
	query          string
	matches        []AltScreenSearchMatch
	selectedIndex  int
	selectedKey    string
	hasSelectedKey bool
	anchorRow      int
	selectionMode  altScreenSearchSelectionMode
}

func (t *TuiAltScreen) ToggleSearch() {
	t.frameMu.Lock()
	if t.activeSearch != nil {
		t.frameMu.Unlock()
		t.CloseSearch()
		return
	}
	component := NewAltScreenSearchComponent(t.updateSearchQuery, t.searchNavigationButtonStyle)
	search := &activeAltScreenSearch{
		component: component, matches: []AltScreenSearchMatch{}, selectedIndex: -1,
		anchorRow: t.primaryScrollViewLocked().ScrollTop(), selectionMode: searchSelectionQuery,
	}
	t.activeSearch = search
	width := Percent("40%")
	minimum := 32
	// The overlay stack is part of the state a frame reads, so it is changed while
	// the lock is still held rather than between frames.
	search.overlay = t.ShowOverlay(component, OverlayOptions{
		Anchor: AnchorTopRight, Width: &width, MinWidth: &minimum, Margin: UniformMargin(1),
	})
	t.frameMu.Unlock()
}

func (t *TuiAltScreen) CloseSearch() {
	t.frameMu.Lock()
	search := t.activeSearch
	if search == nil {
		t.frameMu.Unlock()
		return
	}
	t.activeSearch = nil
	if search.overlay != nil {
		// Hiding the overlay removes it from the stack a frame reads, so it happens
		// under the same lock.
		search.overlay.Hide()
	}
	t.frameMu.Unlock()
	t.RequestRender(false)
}

func (t *TuiAltScreen) updateSearchQuery(query string) {
	t.frameMu.Lock()
	search := t.activeSearch
	if search == nil || query == search.query {
		t.frameMu.Unlock()
		return
	}
	anchor := t.primaryScrollViewLocked().ScrollTop()
	if search.selectedIndex >= 0 && search.selectedIndex < len(search.matches) && len(search.matches[search.selectedIndex].Segments) > 0 {
		anchor = search.matches[search.selectedIndex].Segments[0].Row
	}
	search.anchorRow, search.query, search.selectionMode = anchor, query, searchSelectionQuery
	search.component.SetResult(-1, 0)
	t.frameMu.Unlock()
	t.RequestRender(false)
}

func (t *TuiAltScreen) NavigateSearch(direction int) {
	t.frameMu.Lock()
	search := t.activeSearch
	if search == nil || search.query == "" {
		t.frameMu.Unlock()
		return
	}
	if direction < 0 {
		search.selectionMode = searchSelectionPrevious
	} else {
		search.selectionMode = searchSelectionNext
	}
	t.frameMu.Unlock()
	t.RequestRender(false)
}

func (t *TuiAltScreen) refreshSearchLocked(layout LayoutFrame) bool {
	search := t.activeSearch
	if search == nil {
		return false
	}
	scrollView := layout.PrimaryScrollView
	if scrollView == nil {
		scrollView = t.implicitScrollView
	}
	box, ok := GetScrollViewBox(layout, scrollView)
	if !ok || !box.HasScrollContent || normalizeSearchQuery(search.query) == "" {
		search.matches, search.selectedIndex, search.selectedKey, search.hasSelectedKey = []AltScreenSearchMatch{}, -1, "", false
		search.selectionMode = searchSelectionRetain
		search.component.SetResult(-1, 0)
		return false
	}
	shouldReveal := search.selectionMode != searchSelectionRetain
	result := search.index.Search(box.ScrollContent, search.query)
	search.matches = result.Matches
	if !result.Changed && search.selectionMode == searchSelectionRetain {
		return false
	}
	exactIndex := search.selectedIndex
	if result.Changed {
		exactIndex = -1
		if search.hasSelectedKey {
			for index, match := range search.matches {
				if GetAltScreenSearchMatchKey(match) == search.selectedKey {
					exactIndex = index
					break
				}
			}
		}
	}
	selected := -1
	if len(search.matches) > 0 {
		switch search.selectionMode {
		case searchSelectionQuery:
			selected = sort.Search(len(search.matches), func(index int) bool {
				return len(search.matches[index].Segments) == 0 || search.matches[index].Segments[0].Row >= search.anchorRow
			})
			if selected == len(search.matches) {
				selected = 0
			}
		case searchSelectionNext:
			base := exactIndex
			if base < 0 {
				base = min(search.selectedIndex, len(search.matches)-1)
			}
			if base < 0 {
				selected = 0
			} else {
				selected = (base + 1) % len(search.matches)
			}
		case searchSelectionPrevious:
			base := exactIndex
			if base < 0 {
				base = min(search.selectedIndex, len(search.matches)-1)
			}
			if base < 0 {
				selected = len(search.matches) - 1
			} else {
				selected = (base - 1 + len(search.matches)) % len(search.matches)
			}
		default:
			if exactIndex >= 0 {
				selected = exactIndex
			} else {
				selected = min(max(0, search.selectedIndex), len(search.matches)-1)
			}
		}
	}
	search.selectedIndex = selected
	search.hasSelectedKey = selected >= 0
	if selected >= 0 {
		search.selectedKey = GetAltScreenSearchMatchKey(search.matches[selected])
	} else {
		search.selectedKey = ""
	}
	search.selectionMode = searchSelectionRetain
	search.component.SetResult(selected, len(search.matches))
	if !shouldReveal || selected < 0 || scrollView.ViewportHeight() <= 0 {
		return false
	}
	match := search.matches[selected]
	if len(match.Segments) == 0 {
		return false
	}
	first, last := match.Segments[0], match.Segments[len(match.Segments)-1]
	before := scrollView.ScrollTop()
	target := before
	if first.Row < before || last.Row > before+scrollView.ViewportHeight()-1 {
		target = first.Row - scrollView.ViewportHeight()/3
	}
	scrollView.ScrollTo(target, ScrollToOptions{DisableFollow: true})
	return scrollView.ScrollTop() != before
}

func applySearchTextHighlight(text string, style func(string) string) string {
	var result strings.Builder
	plainStart := 0
	for index := 0; index < len(text); {
		code, length, ok := tuitext.ExtractAnsiCode(text, index)
		if !ok {
			_, size := utf8.DecodeRuneInString(text[index:])
			index += size
			continue
		}
		if index > plainStart {
			result.WriteString(style(text[plainStart:index]))
		}
		result.WriteString(code)
		index += length
		plainStart = index
	}
	if plainStart < len(text) {
		result.WriteString(style(text[plainStart:]))
	}
	return result.String()
}

type searchHighlightRange struct {
	start, end int
	current    bool
}

func (t *TuiAltScreen) applySearchHighlightsLocked(screen []string, layout LayoutFrame) []string {
	search := t.activeSearch
	if search == nil || search.selectedIndex < 0 || len(search.matches) == 0 {
		return screen
	}
	scrollView := layout.PrimaryScrollView
	if scrollView == nil {
		scrollView = t.implicitScrollView
	}
	box, ok := GetScrollViewBox(layout, scrollView)
	if !ok {
		return screen
	}
	minRow := max(0, box.Rect.Y, box.Clip.Y)
	maxRow := min(len(screen), box.Rect.Y+box.Rect.Height, box.Clip.Y+box.Clip.Height)
	minColumn := max(0, box.Rect.X, box.Clip.X)
	maxColumn := min(t.Terminal.Columns(), box.Rect.X+box.Rect.Width, box.Clip.X+box.Clip.Width)
	if scrollbar, visible := GetScrollbarGeometry(box, false); visible {
		maxColumn = min(maxColumn, scrollbar.Column)
	}
	minContentRow := scrollView.ScrollTop() + minRow - box.Rect.Y
	maxContentRow := scrollView.ScrollTop() + maxRow - box.Rect.Y - 1
	low := sort.Search(len(search.matches), func(index int) bool {
		segments := search.matches[index].Segments
		return len(segments) == 0 || segments[len(segments)-1].Row >= minContentRow
	})
	ranges := make(map[int][]searchHighlightRange)
	for matchIndex := low; matchIndex < len(search.matches); matchIndex++ {
		match := search.matches[matchIndex]
		if len(match.Segments) == 0 {
			continue
		}
		if match.Segments[0].Row > maxContentRow {
			break
		}
		for _, segment := range match.Segments {
			row := box.Rect.Y + segment.Row - scrollView.ScrollTop()
			if row < minRow || row >= maxRow {
				continue
			}
			start := max(minColumn, box.Rect.X+segment.StartCol)
			end := min(maxColumn, box.Rect.X+segment.EndCol)
			if end > start {
				ranges[row] = append(ranges[row], searchHighlightRange{start, end, matchIndex == search.selectedIndex})
			}
		}
	}
	result := append([]string(nil), screen...)
	for row, rowRanges := range ranges {
		line := result[row]
		if IsImageLine(line) {
			continue
		}
		lineWidth := tuitext.VisibleWidth(line)
		slices.SortFunc(rowRanges, func(a, b searchHighlightRange) int { return cmp.Compare(b.start, a.start) })
		for _, highlight := range rowRanges {
			start, end := min(highlight.start, lineWidth), min(highlight.end, lineWidth)
			if end <= start {
				continue
			}
			before := tuitext.SliceByColumn(line, 0, start, true)
			text := tuitext.SliceByColumn(line, start, end-start, true)
			after := tuitext.SliceByColumn(line, end, max(0, lineWidth-end), true)
			style := t.searchMatchStyle
			if highlight.current {
				style = t.searchCurrentMatchStyle
			}
			line = before + applySearchTextHighlight(text, style) + after
		}
		result[row] = line
	}
	return result
}
