package tui

import tuitext "github.com/CarlvinceTan/midas/internal/tui/text"

// HStack lays children out horizontally and composites their rendered rows.
type HStack struct {
	Stack
}

// NewHStack constructs an HStack with default options and unconfigured children.
func NewHStack(children ...Component) *HStack {
	configured := make([]StackChild, 0, len(children))
	for _, child := range children {
		configured = append(configured, StackChild{Component: child})
	}
	return NewHStackWithOptions(StackOptions{}, configured...)
}

// NewHStackWithOptions constructs a configured HStack.
func NewHStackWithOptions(options StackOptions, children ...StackChild) *HStack {
	return &HStack{Stack: newStack(options, children)}
}

// LayoutNode exposes the horizontal stack to the layout engine.
func (h *HStack) LayoutNode() LayoutNode {
	return LayoutNode{Type: LayoutHStack, Entries: h.Entries(), Gap: h.gap, Align: h.align}
}

// Render measures children at the full parent width, allocates widths, then
// renders each non-zero child again at its allocated width.
func (h *HStack) Render(width int) []string {
	safeWidth := max(1, width)
	viewport := LayoutViewport{Width: safeWidth, Height: maxSafeInteger}
	entries := VisibleStackEntries(h.entries, viewport)
	if len(entries) == 0 {
		return []string{}
	}

	intrinsicWidths := make([]int, len(entries))
	for index, entry := range entries {
		for _, line := range entry.Component.Render(safeWidth) {
			intrinsicWidths[index] = max(intrinsicWidths[index], tuitext.VisibleWidth(line))
		}
	}
	widths := AllocateStackSizes(entries, intrinsicWidths, &safeWidth, h.gap)
	rendered := make([][]string, len(entries))
	height := 0
	for index, entry := range entries {
		if widths[index] == 0 {
			rendered[index] = []string{}
			continue
		}
		rendered[index] = entry.Component.Render(widths[index])
		height = max(height, len(rendered[index]))
	}

	result := make([]string, height)
	x := 0
	for index, lines := range rendered {
		childWidth := widths[index]
		offset := 0
		if h.align == AlignCenter {
			offset = (height - len(lines)) / 2
		} else if h.align == AlignEnd {
			offset = height - len(lines)
		}
		for row, line := range lines {
			target := row + offset
			if target < 0 || target >= len(result) {
				continue
			}
			result[target] = CompositeTuiLine(result[target], line, x, childWidth, safeWidth)
		}
		x += childWidth + h.gap
	}
	return result
}
