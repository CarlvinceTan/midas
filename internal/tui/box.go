package tui

import (
	"strings"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

const (
	// ContentStartMarker and ContentEndMarker are local vendored additions used
	// by alternate-screen copy selection to exclude Box padding.
	ContentStartMarker = "\x1b]777;midas-content-start\x07"
	ContentEndMarker   = "\x1b]777;midas-content-end\x07"
)

type boxRenderCache struct {
	childLines []string
	width      int
	bgSample   *string
	lines      []string
}

// Box applies padding and an optional background to vertically stacked children.
type Box struct {
	Children []Component

	paddingX int
	paddingY int
	bgFn     func(string) string
	cache    *boxRenderCache

	mouseLayout *containerMouseLayout
}

// NewBox constructs a Box with explicit padding and background function. Pass a
// nil background function for no styling.
func NewBox(paddingX, paddingY int, bgFn func(string) string) *Box {
	return &Box{Children: []Component{}, paddingX: paddingX, paddingY: paddingY, bgFn: bgFn}
}

// NewDefaultBox constructs the reference default: one cell of padding on every
// side and no background function.
func NewDefaultBox() *Box { return NewBox(1, 1, nil) }

// AddChild appends a child and invalidates the render cache.
func (b *Box) AddChild(component Component) {
	b.Children = append(b.Children, component)
	b.invalidateCache()
}

// RemoveChild removes the first identity match.
func (b *Box) RemoveChild(component Component) {
	for index, child := range b.Children {
		if sameComponent(child, component) {
			b.Children = append(b.Children[:index], b.Children[index+1:]...)
			b.invalidateCache()
			return
		}
	}
}

// Clear removes all children and invalidates the render cache.
func (b *Box) Clear() {
	b.Children = []Component{}
	b.invalidateCache()
}

// SetBackground changes the background function. The cache intentionally is
// not invalidated here; Render detects changes by sampling fn("test").
func (b *Box) SetBackground(fn func(string) string) { b.bgFn = fn }

func (b *Box) invalidateCache() { b.cache = nil }

// Invalidate clears the cache and invalidates every child.
func (b *Box) Invalidate() {
	b.invalidateCache()
	for _, child := range b.Children {
		child.Invalidate()
	}
}

// HandleMouse excludes Box padding and dispatches within the content area.
func (b *Box) HandleMouse(event MouseEvent) *MouseResult {
	contentWidth := max(1, event.Width-b.paddingX*2)
	contentY := event.Y - b.paddingY
	contentX := event.X - b.paddingX
	if contentY < 0 || contentX < 0 || contentX >= contentWidth {
		return nil
	}

	children := b.mouseChildren(contentWidth)
	childY := 0
	for _, entry := range children {
		if contentY >= childY && contentY < childY+entry.height {
			childEvent := event
			childEvent.X = contentX
			childEvent.Y = contentY - childY
			childEvent.Width = contentWidth
			childEvent.Height = entry.height
			return DispatchMouseEvent(entry.component, childEvent)
		}
		childY += entry.height
	}
	return nil
}

func (b *Box) mouseChildren(width int) []mouseChildLayout {
	if b.mouseLayout != nil && b.mouseLayout.width == width {
		return b.mouseLayout.children
	}
	children := make([]mouseChildLayout, 0, len(b.Children))
	for _, component := range b.Children {
		children = append(children, mouseChildLayout{component: component, height: len(component.Render(width))})
	}
	return children
}

// Render renders the authoritative vendored Box behavior, including the OSC
// 777 content markers absent from the recovered upstream source.
func (b *Box) Render(width int) []string {
	if len(b.Children) == 0 {
		return []string{}
	}

	contentWidth := max(1, width-b.paddingX*2)
	leftPad := strings.Repeat(" ", b.paddingX)
	childLines := make([]string, 0)
	mouseChildren := make([]mouseChildLayout, 0, len(b.Children))
	for _, child := range b.Children {
		lines := child.Render(contentWidth)
		mouseChildren = append(mouseChildren, mouseChildLayout{component: child, height: len(lines)})
		for _, line := range lines {
			childLines = append(childLines, leftPad+ContentStartMarker+line+ContentEndMarker)
		}
	}
	b.mouseLayout = &containerMouseLayout{width: contentWidth, children: mouseChildren}
	if len(childLines) == 0 {
		return []string{}
	}

	var bgSample *string
	if b.bgFn != nil {
		sample := b.bgFn("test")
		bgSample = &sample
	}
	if b.matchesCache(width, childLines, bgSample) {
		return b.cache.lines
	}

	result := make([]string, 0, len(childLines)+b.paddingY*2)
	for index := 0; index < b.paddingY; index++ {
		result = append(result, b.applyBackground("", width))
	}
	for _, line := range childLines {
		result = append(result, b.applyBackground(line, width))
	}
	for index := 0; index < b.paddingY; index++ {
		result = append(result, b.applyBackground("", width))
	}

	b.cache = &boxRenderCache{childLines: childLines, width: width, bgSample: bgSample, lines: result}
	return result
}

func (b *Box) matchesCache(width int, childLines []string, bgSample *string) bool {
	cache := b.cache
	if cache == nil || cache.width != width || !sameOptionalString(cache.bgSample, bgSample) || len(cache.childLines) != len(childLines) {
		return false
	}
	for index := range childLines {
		if cache.childLines[index] != childLines[index] {
			return false
		}
	}
	return true
}

func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (b *Box) applyBackground(line string, width int) string {
	padding := max(0, width-tuitext.VisibleWidth(line))
	padded := line + strings.Repeat(" ", padding)
	if b.bgFn != nil {
		return tuitext.ApplyBackgroundToLine(padded, width, b.bgFn)
	}
	return padded
}
