// Package tui provides the renderer-independent core of the Midas terminal UI.
package tui

import "reflect"

// Component is the common rendering contract implemented by every TUI node.
//
// Components should be pointer-backed. The reference implementation relies on
// object identity for removal, focus, layout caches, and mouse targets.
type Component interface {
	Render(width int) []string
	Invalidate()
}

// InputHandler is implemented by components that accept keyboard input.
type InputHandler interface {
	HandleInput(data string)
}

// KeyReleaseRequester opts a focused component into Kitty key-release events.
type KeyReleaseRequester interface {
	WantsKeyRelease() bool
}

// Focusable is implemented by components that expose focus state.
type Focusable interface {
	IsFocused() bool
	SetFocused(focused bool)
}

// FocusState can be embedded by components that need focus state.
type FocusState struct {
	focused bool
}

// IsFocused reports whether the component currently owns keyboard focus.
func (f *FocusState) IsFocused() bool { return f.focused }

// SetFocused updates the component's keyboard-focus state.
func (f *FocusState) SetFocused(focused bool) { f.focused = focused }

// CursorMarker is a zero-width APC sequence emitted at the logical cursor.
const CursorMarker = "\x1b_midas:c\x07"

// MouseEventType identifies a normalized mouse action.
type MouseEventType string

const (
	MousePress   MouseEventType = "press"
	MouseRelease MouseEventType = "release"
	MouseMove    MouseEventType = "move"
	MouseDrag    MouseEventType = "drag"
	MouseClick   MouseEventType = "click"
	MouseWheel   MouseEventType = "wheel"
)

// MouseButton identifies the normalized mouse button.
type MouseButton string

const (
	MouseLeft   MouseButton = "left"
	MouseMiddle MouseButton = "middle"
	MouseRight  MouseButton = "right"
	MouseNone   MouseButton = "none"
)

// MouseEvent is a cell-based mouse event. X and Y are local to the receiving
// component; ScreenX and ScreenY are absolute terminal coordinates.
type MouseEvent struct {
	Type       MouseEventType
	Button     MouseButton
	X          int
	Y          int
	ScreenX    int
	ScreenY    int
	Width      int
	Height     int
	Shift      bool
	Alt        bool
	Ctrl       bool
	WheelDelta *int
	ClickCount *int
}

// MouseDispatchTarget records the concrete target and its coordinate transform.
type MouseDispatchTarget struct {
	Component Component
	OriginX   int
	OriginY   int
	Width     int
	Height    int
}

// MouseResult is returned by mouse handlers. Render is a pointer because an
// omitted value and an explicit false have different renderer-level defaults.
// Target and FocusTarget are filled by DispatchMouseEvent.
// Selectable marks a text surface. The fullscreen viewport anchors a text
// selection on such a press while the component keeps its own handling, so the
// editor moves its caret and still highlights under a drag. Components that act
// on a press (pickers, list panels) leave it false and keep the reference
// behaviour of owning the gesture.
type MouseResult struct {
	Handled     bool
	Capture     bool
	Focus       bool
	Render      *bool
	Selectable  bool
	Target      *MouseDispatchTarget
	FocusTarget Component
}

// MouseHandler is implemented by components that accept normalized mouse input.
type MouseHandler interface {
	HandleMouse(event MouseEvent) *MouseResult
}

// DispatchMouseEvent dispatches to a component and records its exact target and
// coordinate transform. A nested dispatch result retains its original target.
func DispatchMouseEvent(component Component, event MouseEvent) *MouseResult {
	handler, ok := component.(MouseHandler)
	if !ok {
		return nil
	}
	result := handler.HandleMouse(event)
	if result == nil {
		return nil
	}
	if result.Target != nil {
		return result
	}
	if !result.Handled && !result.Capture && !result.Focus {
		return nil
	}

	dispatched := *result
	dispatched.Handled = true
	if dispatched.Focus {
		dispatched.FocusTarget = component
	}
	dispatched.Target = &MouseDispatchTarget{
		Component: component,
		OriginX:   event.ScreenX - event.X,
		OriginY:   event.ScreenY - event.Y,
		Width:     event.Width,
		Height:    event.Height,
	}
	return &dispatched
}

// RetargetMouseEvent recreates local coordinates for a previous dispatch target.
func RetargetMouseEvent(event MouseEvent, target MouseDispatchTarget) MouseEvent {
	event.X = event.ScreenX - target.OriginX
	event.Y = event.ScreenY - target.OriginY
	event.Width = target.Width
	event.Height = target.Height
	return event
}

// sameComponent implements the reference renderer's object-identity semantics
// without panicking on an accidentally non-comparable Go value.
func sameComponent(a, b Component) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta.Comparable() {
		return a == b
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	switch va.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return va.Pointer() == vb.Pointer()
	default:
		return false
	}
}
