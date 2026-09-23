package tui

// LayoutNodeType identifies a renderer-aware layout component.
type LayoutNodeType string

const (
	LayoutVStack LayoutNodeType = "vstack"
	LayoutHStack LayoutNodeType = "hstack"
	LayoutScroll LayoutNodeType = "scroll"
)

// LayoutNode describes either a stack or a vertical scroll viewport.
type LayoutNode struct {
	Type LayoutNodeType

	Entries []StackLayoutEntry
	Gap     int
	Align   StackAlignment

	Component Component
	Scroll    *ScrollView
}

// LayoutComponent exposes structural layout metadata to RenderLayoutFrame.
type LayoutComponent interface {
	Component
	LayoutNode() LayoutNode
}

// GetLayoutNode returns structural metadata for a layout-aware component.
func GetLayoutNode(component Component) (LayoutNode, bool) {
	candidate, ok := component.(LayoutComponent)
	if !ok {
		return LayoutNode{}, false
	}
	return candidate.LayoutNode(), true
}
