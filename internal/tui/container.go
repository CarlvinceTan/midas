package tui

type mouseChildLayout struct {
	component Component
	height    int
}

type containerMouseLayout struct {
	width    int
	children []mouseChildLayout
}

// Container renders children vertically in insertion order.
type Container struct {
	Children []Component

	mouseLayout     *containerMouseLayout
	mouseFocusOwner Component
}

// ChildComponents exposes descendants to focus/mount traversal. Box
// intentionally does not implement this contract, matching the reference's
// instanceof Container check.
func (c *Container) ChildComponents() []Component { return c.Children }

// AddChild appends a child.
func (c *Container) AddChild(component Component) {
	c.Children = append(c.Children, component)
}

// RemoveChild removes the first identity match.
func (c *Container) RemoveChild(component Component) {
	for index, child := range c.Children {
		if sameComponent(child, component) {
			c.Children = append(c.Children[:index], c.Children[index+1:]...)
			return
		}
	}
}

// Clear removes all children.
func (c *Container) Clear() { c.Children = []Component{} }

// SetMouseFocusOwner configures a containing component that handles keyboard
// input. When a descendant requests focus, that owner becomes the keyboard
// focus target, matching the reference container's input behavior.
func (c *Container) SetMouseFocusOwner(owner Component) { c.mouseFocusOwner = owner }

// Invalidate invalidates every child.
func (c *Container) Invalidate() {
	for _, child := range c.Children {
		child.Invalidate()
	}
}

// HandleMouse forwards an event to the child occupying its Y coordinate.
func (c *Container) HandleMouse(event MouseEvent) *MouseResult {
	if event.Y < 0 || event.Y >= event.Height {
		return nil
	}

	children := c.mouseChildren(event.Width)
	childY := 0
	for _, entry := range children {
		if event.Y >= childY && event.Y < childY+entry.height {
			childEvent := event
			childEvent.Y = event.Y - childY
			childEvent.Height = entry.height
			result := DispatchMouseEvent(entry.component, childEvent)
			if result != nil && result.Focus && c.mouseFocusOwner != nil {
				if _, handlesInput := c.mouseFocusOwner.(InputHandler); handlesInput {
					copy := *result
					copy.FocusTarget = c.mouseFocusOwner
					return &copy
				}
			}
			return result
		}
		childY += entry.height
	}
	return nil
}

func (c *Container) mouseChildren(width int) []mouseChildLayout {
	if c.mouseLayout != nil && c.mouseLayout.width == width {
		return c.mouseLayout.children
	}
	children := make([]mouseChildLayout, 0, len(c.Children))
	for _, component := range c.Children {
		children = append(children, mouseChildLayout{component: component, height: len(component.Render(width))})
	}
	return children
}

// Render concatenates child rows and caches their heights for mouse dispatch.
func (c *Container) Render(width int) []string {
	lines := make([]string, 0)
	children := make([]mouseChildLayout, 0, len(c.Children))
	for _, child := range c.Children {
		childLines := child.Render(width)
		children = append(children, mouseChildLayout{component: child, height: len(childLines)})
		lines = append(lines, childLines...)
	}
	c.mouseLayout = &containerMouseLayout{width: width, children: children}
	return lines
}
