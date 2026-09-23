package tui

// VStack lays children out vertically.
type VStack struct {
	Stack
}

// NewVStack constructs a VStack with default options and unconfigured children.
func NewVStack(children ...Component) *VStack {
	configured := make([]StackChild, 0, len(children))
	for _, child := range children {
		configured = append(configured, StackChild{Component: child})
	}
	return NewVStackWithOptions(StackOptions{}, configured...)
}

// NewVStackWithOptions constructs a configured VStack.
func NewVStackWithOptions(options StackOptions, children ...StackChild) *VStack {
	return &VStack{Stack: newStack(options, children)}
}

// LayoutNode exposes the vertical stack to the layout engine.
func (v *VStack) LayoutNode() LayoutNode {
	return LayoutNode{Type: LayoutVStack, Entries: v.Entries(), Gap: v.gap, Align: v.align}
}

// Render allocates each child at its intrinsic height, then crops or pads to its
// basis while inserting configured gaps.
func (v *VStack) Render(width int) []string {
	viewport := LayoutViewport{Width: max(1, width), Height: maxSafeInteger}
	entries := VisibleStackEntries(v.entries, viewport)
	rendered := make([][]string, len(entries))
	intrinsic := make([]int, len(entries))
	for index, entry := range entries {
		rendered[index] = entry.Component.Render(viewport.Width)
		intrinsic[index] = len(rendered[index])
	}
	sizes := AllocateStackSizes(entries, intrinsic, nil, v.gap)
	lines := make([]string, 0)
	for index := range entries {
		if index > 0 {
			for gap := 0; gap < v.gap; gap++ {
				lines = append(lines, "")
			}
		}
		childLines := rendered[index]
		if len(childLines) > sizes[index] {
			childLines = childLines[:sizes[index]]
		}
		lines = append(lines, childLines...)
		for padding := len(childLines); padding < sizes[index]; padding++ {
			lines = append(lines, "")
		}
	}
	return lines
}
