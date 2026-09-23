package tui

import (
	"cmp"
	"math"
	"slices"
	"strings"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

// LayoutRect is a terminal-cell rectangle.
type LayoutRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// LayoutBox is one node in a rendered layout tree.
type LayoutBox struct {
	Component Component
	Rect      LayoutRect
	Clip      LayoutRect
	Children  []*LayoutBox
	Parent    *LayoutBox

	Lines      []string
	HasLines   bool
	LineOffset int

	ScrollView       *ScrollView
	ScrollContent    []string
	HasScrollContent bool
	Layer            int
}

// LayoutFrame contains the box tree and painted terminal rows for one frame.
type LayoutFrame struct {
	Root              *LayoutBox
	Width             int
	Height            int
	Lines             []string
	PrimaryScrollView *ScrollView
}

// ScrollbarGeometry is the resolved scrollbar track and thumb geometry.
type ScrollbarGeometry struct {
	Column       int `json:"column"`
	TrackTop     int `json:"trackTop"`
	TrackHeight  int `json:"trackHeight"`
	ThumbTop     int `json:"thumbTop"`
	ThumbHeight  int `json:"thumbHeight"`
	MaxScrollTop int `json:"maxScrollTop"`
}

type renderCacheEntry struct {
	component Component
	widths    map[int][]string
}

type layoutContext struct {
	viewport          LayoutViewport
	renderCache       []renderCacheEntry
	requestRender     func()
	primaryScrollView *ScrollView
}

func intersectLayoutRect(a, b LayoutRect) LayoutRect {
	x := max(a.X, b.X)
	y := max(a.Y, b.Y)
	right := min(a.X+a.Width, b.X+b.Width)
	bottom := min(a.Y+a.Height, b.Y+b.Height)
	return LayoutRect{X: x, Y: y, Width: max(0, right-x), Height: max(0, bottom-y)}
}

func (c *layoutContext) renderCached(component Component, width int) []string {
	safeWidth := max(1, width)
	for index := range c.renderCache {
		entry := &c.renderCache[index]
		if !sameComponent(entry.component, component) {
			continue
		}
		if lines, ok := entry.widths[safeWidth]; ok {
			return lines
		}
		lines := component.Render(safeWidth)
		entry.widths[safeWidth] = lines
		return lines
	}
	lines := component.Render(safeWidth)
	c.renderCache = append(c.renderCache, renderCacheEntry{
		component: component,
		widths:    map[int][]string{safeWidth: lines},
	})
	return lines
}

func (c *layoutContext) measureHeight(component Component, width int) int {
	return len(c.renderCached(component, width))
}

func (c *layoutContext) measureWidth(component Component, width int) int {
	measured := 0
	for _, line := range c.renderCached(component, width) {
		measured = max(measured, tuitext.VisibleWidth(line))
	}
	return measured
}

func withLayoutParent(box, parent *LayoutBox) *LayoutBox {
	box.Parent = parent
	return box
}

func translateLayoutBox(box *LayoutBox, deltaY int) {
	box.Rect.Y += deltaY
	for _, child := range box.Children {
		translateLayoutBox(child, deltaY)
	}
}

func updateLayoutClips(box *LayoutBox, parentClip LayoutRect) {
	box.Clip = intersectLayoutRect(parentClip, box.Rect)
	for _, child := range box.Children {
		updateLayoutClips(child, box.Clip)
	}
}

func layoutComponent(
	context *layoutContext,
	component Component,
	x, y, width int,
	height *int,
	clip LayoutRect,
) *LayoutBox {
	safeWidth := max(1, width)
	node, hasNode := GetLayoutNode(component)
	if !hasNode {
		lines := context.renderCached(component, safeWidth)
		allocatedHeight := len(lines)
		if height != nil {
			allocatedHeight = max(0, *height)
		}
		lineOffset := 0
		if len(lines) > allocatedHeight && allocatedHeight > 0 {
			cursorLine := -1
			for index, line := range lines {
				if strings.Contains(line, CursorMarker) {
					cursorLine = index
					break
				}
			}
			if cursorLine >= allocatedHeight {
				lineOffset = cursorLine - allocatedHeight + 1
			}
		}
		rect := LayoutRect{X: x, Y: y, Width: safeWidth, Height: allocatedHeight}
		return &LayoutBox{
			Component:  component,
			Rect:       rect,
			Clip:       intersectLayoutRect(clip, rect),
			Children:   []*LayoutBox{},
			Lines:      lines,
			HasLines:   true,
			LineOffset: lineOffset,
		}
	}

	if node.Type == LayoutScroll {
		scroll := node.Scroll
		previousScrollTop := scroll.ScrollTop()
		contentWidth := scroll.ContentWidth(safeWidth)
		childBox := layoutComponent(context, node.Component, x, y-previousScrollTop, contentWidth, nil, clip)
		contentHeight := childBox.Rect.Height
		viewportHeight := contentHeight
		if height != nil {
			viewportHeight = max(0, *height)
		}
		scroll.UpdateLayout(contentHeight, viewportHeight, context.requestRender)
		translateLayoutBox(childBox, previousScrollTop-scroll.ScrollTop())
		if scroll.IsPrimary() || context.primaryScrollView == nil {
			context.primaryScrollView = scroll
		}
		rect := LayoutRect{X: x, Y: y, Width: safeWidth, Height: viewportHeight}
		childClip := intersectLayoutRect(clip, rect)
		box := &LayoutBox{
			Component:        component,
			Rect:             rect,
			Clip:             childClip,
			Children:         []*LayoutBox{childBox},
			ScrollView:       scroll,
			ScrollContent:    context.renderCached(node.Component, contentWidth),
			HasScrollContent: true,
		}
		childBox.Parent = box
		updateLayoutClips(childBox, childClip)
		return box
	}

	entries := VisibleStackEntries(node.Entries, context.viewport)
	gapTotal := max(0, len(entries)-1) * node.Gap
	if node.Type == LayoutVStack {
		intrinsicHeights := make([]int, len(entries))
		for index, entry := range entries {
			if entry.Basis != nil {
				intrinsicHeights[index] = *entry.Basis
			} else {
				intrinsicHeights[index] = context.measureHeight(entry.Component, safeWidth)
			}
		}
		sizes := AllocateStackSizes(entries, intrinsicHeights, height, node.Gap)
		naturalHeight := gapTotal
		for _, size := range sizes {
			naturalHeight += size
		}
		allocatedHeight := naturalHeight
		if height != nil {
			allocatedHeight = max(0, *height)
		}
		rect := LayoutRect{X: x, Y: y, Width: safeWidth, Height: allocatedHeight}
		box := &LayoutBox{
			Component: component,
			Rect:      rect,
			Clip:      intersectLayoutRect(clip, rect),
			Children:  []*LayoutBox{},
		}
		childY := y
		for index, entry := range entries {
			childHeight := sizes[index]
			box.Children = append(box.Children, withLayoutParent(
				layoutComponent(context, entry.Component, x, childY, safeWidth, &childHeight, box.Clip),
				box,
			))
			childY += childHeight + node.Gap
		}
		return box
	}

	intrinsicWidths := make([]int, len(entries))
	for index, entry := range entries {
		if entry.Basis != nil {
			intrinsicWidths[index] = *entry.Basis
		} else {
			intrinsicWidths[index] = context.measureWidth(entry.Component, safeWidth)
		}
	}
	widths := AllocateStackSizes(entries, intrinsicWidths, &safeWidth, node.Gap)
	intrinsicHeights := make([]int, len(entries))
	for index, entry := range entries {
		intrinsicHeights[index] = context.measureHeight(entry.Component, max(1, widths[index]))
	}
	allocatedHeight := 0
	if height == nil {
		for _, childHeight := range intrinsicHeights {
			allocatedHeight = max(allocatedHeight, childHeight)
		}
	} else {
		allocatedHeight = max(0, *height)
	}
	rect := LayoutRect{X: x, Y: y, Width: safeWidth, Height: allocatedHeight}
	box := &LayoutBox{
		Component: component,
		Rect:      rect,
		Clip:      intersectLayoutRect(clip, rect),
		Children:  []*LayoutBox{},
	}
	childX := x
	for index, entry := range entries {
		naturalChildHeight := intrinsicHeights[index]
		childHeight := min(allocatedHeight, naturalChildHeight)
		if node.Align == AlignStretch {
			childHeight = allocatedHeight
		}
		childY := y
		if node.Align == AlignCenter {
			childY += (allocatedHeight - childHeight) / 2
		} else if node.Align == AlignEnd {
			childY += allocatedHeight - childHeight
		}
		childWidth := widths[index]
		if childWidth == 0 {
			box.Children = append(box.Children, &LayoutBox{
				Component: entry.Component,
				Rect:      LayoutRect{X: childX, Y: childY, Width: 0, Height: childHeight},
				Clip:      LayoutRect{X: childX, Y: childY},
				Children:  []*LayoutBox{},
				Parent:    box,
			})
		} else {
			box.Children = append(box.Children, withLayoutParent(
				layoutComponent(context, entry.Component, childX, childY, childWidth, &childHeight, box.Clip),
				box,
			))
		}
		childX += childWidth + node.Gap
	}
	return box
}

func replaceScrollbarCell(line string, column, totalWidth int, replacement string, preserveTargetBackground bool) string {
	if IsImageLine(line) {
		return line
	}
	rangeAtColumn, found := tuitext.GetGraphemeCellRange(line, column)
	start, end := column, column+1
	if found {
		start, end = rangeAtColumn.Start, rangeAtColumn.End
	}
	before := tuitext.SliceByColumn(line, 0, start, true)
	target := tuitext.SliceByColumn(line, start, end-start, true)
	after := tuitext.SliceByColumn(line, end, max(0, totalWidth-end), true)

	targetPrefix := ""
	for targetIndex := 0; targetIndex < len(target); {
		ansi, length, ok := tuitext.ExtractAnsiCode(target, targetIndex)
		if !ok {
			break
		}
		targetPrefix += ansi
		targetIndex += length
	}
	beforePadding := strings.Repeat(" ", max(0, start-tuitext.VisibleWidth(before)))
	cellPaddingBefore := strings.Repeat(" ", max(0, column-start))
	cellPaddingAfter := strings.Repeat(" ", max(0, end-column-1))
	targetStyle := segmentReset
	if preserveTargetBackground {
		targetStyle += tuitext.GetActiveBackgroundAnsi(targetPrefix)
	}
	return before + beforePadding + targetStyle + cellPaddingBefore + replacement + cellPaddingAfter + after
}

// GetScrollbarGeometry resolves scrollbar geometry for a laid-out ScrollView.
func GetScrollbarGeometry(box *LayoutBox, includeHiddenAuto bool) (ScrollbarGeometry, bool) {
	if box.ScrollView == nil || box.Rect.Width <= 0 || box.Rect.Height <= 0 {
		return ScrollbarGeometry{}, false
	}
	contentHeight := 0
	if len(box.Children) > 0 {
		contentHeight = box.Children[0].Rect.Height
	} else if box.HasScrollContent {
		contentHeight = len(box.ScrollContent)
	}
	trackHeight := box.Rect.Height
	canRevealHiddenAuto := includeHiddenAuto && box.ScrollView.Scrollbar() == ScrollbarAuto && contentHeight > trackHeight
	if !box.ScrollView.IsScrollbarVisible() && !canRevealHiddenAuto {
		return ScrollbarGeometry{}, false
	}

	minThumbHeight := min(2, trackHeight)
	thumbHeight := trackHeight
	if contentHeight > 0 {
		thumbHeight = max(minThumbHeight, min(trackHeight, jsRound(float64(trackHeight*trackHeight)/float64(contentHeight))))
	}
	maxScrollTop := max(0, contentHeight-trackHeight)
	maxThumbTop := trackHeight - thumbHeight
	thumbOffset := 0
	if maxScrollTop != 0 {
		thumbOffset = jsRound(float64(box.ScrollView.ScrollTop()) / float64(maxScrollTop) * float64(maxThumbTop))
	}
	column := box.Rect.X + box.Rect.Width - 1
	if column < box.Clip.X || column >= box.Clip.X+box.Clip.Width {
		return ScrollbarGeometry{}, false
	}
	return ScrollbarGeometry{
		Column:       column,
		TrackTop:     box.Rect.Y,
		TrackHeight:  trackHeight,
		ThumbTop:     box.Rect.Y + thumbOffset,
		ThumbHeight:  thumbHeight,
		MaxScrollTop: maxScrollTop,
	}, true
}

func jsRound(value float64) int { return int(math.Floor(value + 0.5)) }

func paintScrollbar(box *LayoutBox, screen []string, totalWidth int) {
	geometry, ok := GetScrollbarGeometry(box, false)
	if !ok || box.ScrollView == nil {
		return
	}
	for offset := 0; offset < geometry.TrackHeight; offset++ {
		row := geometry.TrackTop + offset
		if row < box.Clip.Y || row >= box.Clip.Y+box.Clip.Height || row < 0 || row >= len(screen) {
			continue
		}
		isThumb := row >= geometry.ThumbTop && row < geometry.ThumbTop+geometry.ThumbHeight
		replacement := box.ScrollView.ScrollbarTrackStyle("│")
		if isThumb {
			glyph := "┃"
			if box.ScrollView.IsScrollbarActive() {
				glyph = "█"
			}
			replacement = box.ScrollView.ScrollbarThumbStyle(glyph)
		}
		screen[row] = replaceScrollbarCell(
			screen[row], geometry.Column, totalWidth, replacement,
			box.ScrollView.Scrollbar() != ScrollbarAlways,
		)
	}
}

func paintLayoutBox(box *LayoutBox, screen []string, totalWidth int) {
	if box.HasLines {
		firstRow := max(box.Rect.Y, box.Clip.Y, 0)
		lastRow := min(box.Rect.Y+box.Rect.Height, box.Clip.Y+box.Clip.Height, len(screen))
		for row := firstRow; row < lastRow; row++ {
			lineIndex := box.LineOffset + row - box.Rect.Y
			if lineIndex < 0 || lineIndex >= len(box.Lines) {
				continue
			}
			line := stripLeadingOSC133Zones(box.Lines[lineIndex])
			if metadata, ok := GetKittyImageMetadata(line); ok {
				clipBottom := min(len(screen), box.Clip.Y+box.Clip.Height)
				visibleRows := min(metadata.Rows, clipBottom-row)
				if visibleRows < metadata.Rows {
					line = CropKittyImageLine(line, 0, visibleRows)
				}
			}
			if box.Rect.X == 0 && box.Rect.Width >= totalWidth && (IsImageLine(line) || screen[row] == "") {
				screen[row] = line
			} else {
				screen[row] = CompositeTuiLine(screen[row], line, box.Rect.X, box.Rect.Width, totalWidth)
			}
		}
	}
	for _, child := range box.Children {
		paintLayoutBox(child, screen, totalWidth)
	}

	if box.ScrollView != nil && box.HasScrollContent && box.ScrollView.ScrollTop() > 0 && box.Rect.Height > 0 {
		for imageRow := box.ScrollView.ScrollTop() - 1; imageRow >= 0; imageRow-- {
			imageLine := ""
			if imageRow < len(box.ScrollContent) {
				imageLine = box.ScrollContent[imageRow]
			}
			if metadata, ok := GetKittyImageMetadata(imageLine); ok {
				hiddenRows := box.ScrollView.ScrollTop() - imageRow
				if hiddenRows < metadata.Rows {
					visibleRows := min(box.Rect.Height, metadata.Rows-hiddenRows)
					cropped := CropKittyImageLine(imageLine, hiddenRows, visibleRows)
					if box.Rect.X == 0 && box.Rect.Width >= totalWidth && box.Rect.Y >= 0 && box.Rect.Y < len(screen) {
						screen[box.Rect.Y] = cropped
					}
				}
				break
			}
			if imageLine != "" {
				break
			}
		}
	}
	paintScrollbar(box, screen, totalWidth)
}

func stripLeadingOSC133Zones(line string) string {
	for strings.HasPrefix(line, "\x1b]133;") && len(line) > 7 {
		zone := line[6]
		if zone != 'A' && zone != 'B' && zone != 'C' {
			break
		}
		switch {
		case line[7] == '\x07':
			line = line[8:]
		case len(line) > 8 && line[7] == '\x1b' && line[8] == '\\':
			line = line[9:]
		default:
			return line
		}
	}
	return line
}

// RenderLayoutFrame lays out and paints one fixed terminal viewport.
func RenderLayoutFrame(root Component, width, height int, requestRender func()) LayoutFrame {
	safeWidth := max(1, width)
	safeHeight := max(1, height)
	if requestRender == nil {
		requestRender = func() {}
	}
	context := &layoutContext{
		viewport:      LayoutViewport{Width: safeWidth, Height: safeHeight},
		renderCache:   []renderCacheEntry{},
		requestRender: requestRender,
	}
	rootHeight := safeHeight
	rootBox := layoutComponent(context, root, 0, 0, safeWidth, &rootHeight, LayoutRect{
		Width: safeWidth, Height: safeHeight,
	})
	lines := make([]string, safeHeight)
	paintLayoutBox(rootBox, lines, safeWidth)
	return LayoutFrame{
		Root:              rootBox,
		Width:             safeWidth,
		Height:            safeHeight,
		Lines:             lines,
		PrimaryScrollView: context.primaryScrollView,
	}
}

func containsLayoutPoint(rect LayoutRect, x, y int) bool {
	return x >= rect.X && x < rect.X+rect.Width && y >= rect.Y && y < rect.Y+rect.Height
}

// GetLayoutBoxesAt returns the visual hit path deepest/highest-layer first.
func GetLayoutBoxesAt(frame LayoutFrame, x, y int) []*LayoutBox {
	type match struct {
		box   *LayoutBox
		depth int
	}
	matches := make([]match, 0)
	var visit func(*LayoutBox, int)
	visit = func(box *LayoutBox, depth int) {
		if !containsLayoutPoint(box.Clip, x, y) {
			return
		}
		matches = append(matches, match{box: box, depth: depth})
		for _, child := range box.Children {
			visit(child, depth+1)
		}
	}
	visit(frame.Root, 0)
	slices.SortStableFunc(matches, func(a, b match) int {
		if a.box.Layer != b.box.Layer {
			return cmp.Compare(b.box.Layer, a.box.Layer)
		}
		return cmp.Compare(b.depth, a.depth)
	})
	result := make([]*LayoutBox, len(matches))
	for index, match := range matches {
		result[index] = match.box
	}
	return result
}

// GetScrollViewBox finds the box for one ScrollView.
func GetScrollViewBox(frame LayoutFrame, scrollView *ScrollView) (*LayoutBox, bool) {
	var visit func(*LayoutBox) *LayoutBox
	visit = func(box *LayoutBox) *LayoutBox {
		if box.ScrollView == scrollView {
			return box
		}
		for _, child := range box.Children {
			if match := visit(child); match != nil {
				return match
			}
		}
		return nil
	}
	box := visit(frame.Root)
	return box, box != nil
}

// GetScrollViewsAt returns nested scroll views deepest first.
func GetScrollViewsAt(frame LayoutFrame, x, y int) []*ScrollView {
	type match struct {
		scroll *ScrollView
		depth  int
	}
	matches := make([]match, 0)
	var visit func(*LayoutBox, int)
	visit = func(box *LayoutBox, depth int) {
		if !containsLayoutPoint(box.Clip, x, y) {
			return
		}
		if box.ScrollView != nil && containsLayoutPoint(box.Rect, x, y) {
			matches = append(matches, match{scroll: box.ScrollView, depth: depth})
		}
		for _, child := range box.Children {
			visit(child, depth+1)
		}
	}
	visit(frame.Root, 0)
	slices.SortStableFunc(matches, func(a, b match) int { return cmp.Compare(b.depth, a.depth) })
	result := make([]*ScrollView, len(matches))
	for index, match := range matches {
		result[index] = match.scroll
	}
	return result
}
