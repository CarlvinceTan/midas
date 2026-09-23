package tui

import (
	"math"
	"regexp"
	"strconv"
	"time"
)

const (
	altWheelScrollMultiplier = 5
	doubleClickInterval      = 500 * time.Millisecond
)

var sgrMousePattern = regexp.MustCompile(`^\x1b\[<(\d+);(\d+);(\d+)([Mm])$`)

func (t *TuiAltScreen) shouldDeferViewportInputToOverlay() bool {
	if !t.IsOverlayFocused() {
		return false
	}
	search := t.activeSearch
	return search == nil || search.overlay == nil || !search.overlay.IsFocused()
}

func (t *TuiAltScreen) handleViewportInput(data string) InputListenerResult {
	if data == "\x1b[O" {
		hadActiveSelection := t.selectionPressActive
		hadNonEmptySelection := hadActiveSelection && t.HasActiveSelection()
		t.selectionPressActive = false
		t.stopSelectionAutoScroll()
		t.stopScrollbarHover()
		t.scrollbarDrag = nil
		t.clearComponentMouseGesture()
		t.lastComponentClick = nil
		if search := t.activeSearch; search != nil && search.component.SetHoveredNavigationDirection(0, false) {
			t.RequestRender(false)
		}
		if hadActiveSelection {
			t.selectionAnchor, t.selectionFocus = nil, nil
			t.selectionGranularity = selectionCharacter
			t.selectionInitialRange = nil
			if hadNonEmptySelection {
				t.RequestRender(false)
			}
		}
		t.lastSelectionClick = nil
		return InputListenerResult{Consume: true}
	}
	if data == "\x1b[I" {
		return InputListenerResult{Consume: true}
	}
	if wheel, ok := parseAltWheelEvent(data); ok {
		delta := wheel.direction * t.getWheelScrollLines(wheel.button)
		event := t.createAltMouseEvent(MouseWheel, wheel.button, wheel.x, wheel.y, &delta, nil)
		overlay := t.DispatchMouseToOverlay(event)
		result := overlay.Result
		if result == nil && !overlay.Hit {
			result = t.dispatchMouseToLayout(event)
		}
		if result != nil {
			if t.applyMouseDispatchResult(event, result) {
				t.RequestRender(false)
			}
			return InputListenerResult{Consume: true}
		}
		if t.shouldDeferViewportInputToOverlay() {
			return InputListenerResult{}
		}
		t.routeWheel(wheel)
		return InputListenerResult{Consume: true}
	}
	if raw, ok := parseAltSgrMouseEvent(data); ok {
		t.handleAltMouseEvent(raw)
		return InputListenerResult{Consume: true}
	}
	if isAltMouseSequence(data) {
		return InputListenerResult{Consume: true}
	}
	return t.handleViewportKeyboardInput(data)
}

type rawAltMouseEvent struct {
	button  int
	x, y    int
	release bool
}

type altWheelEvent struct {
	direction int
	x, y      int
	button    int
}

type mousePoint struct{ x, y int }

type componentClickState struct {
	timestamp time.Time
	count     int
	component Component
	x, y      int
}

type scrollbarDragState struct {
	scrollView *ScrollView
	grabOffset int
}

func decodeAltMouseButton(button int) MouseButton {
	switch button & 3 {
	case 0:
		return MouseLeft
	case 1:
		return MouseMiddle
	case 2:
		return MouseRight
	default:
		return MouseNone
	}
}

func (t *TuiAltScreen) createAltMouseEvent(eventType MouseEventType, button, x, y int, wheelDelta, clickCount *int) MouseEvent {
	mouseButton := decodeAltMouseButton(button)
	if eventType == MouseWheel {
		mouseButton = MouseNone
	}
	return MouseEvent{
		Type: eventType, Button: mouseButton, X: x, Y: y, ScreenX: x, ScreenY: y,
		Width: max(1, t.Terminal.Columns()), Height: max(1, t.Terminal.Rows()),
		Shift: button&4 != 0, Alt: button&8 != 0, Ctrl: button&16 != 0,
		WheelDelta: wheelDelta, ClickCount: clickCount,
	}
}

func parseAltWheelEvent(data string) (altWheelEvent, bool) {
	if match := sgrMousePattern.FindStringSubmatch(data); len(match) == 5 {
		button, _ := strconv.Atoi(match[1])
		if button&64 == 0 || button&3 > 1 {
			return altWheelEvent{}, false
		}
		x, _ := strconv.Atoi(match[2])
		y, _ := strconv.Atoi(match[3])
		direction := -1
		if button&3 == 1 {
			direction = 1
		}
		return altWheelEvent{direction: direction, x: x - 1, y: y - 1, button: button}, true
	}
	if len(data) == 6 && data[:3] == "\x1b[M" {
		button := int(data[3]) - 32
		if button&64 == 0 || button&3 > 1 {
			return altWheelEvent{}, false
		}
		direction := -1
		if button&3 == 1 {
			direction = 1
		}
		return altWheelEvent{direction: direction, x: int(data[4]) - 33, y: int(data[5]) - 33, button: button}, true
	}
	return altWheelEvent{}, false
}

func parseAltSgrMouseEvent(data string) (rawAltMouseEvent, bool) {
	match := sgrMousePattern.FindStringSubmatch(data)
	if len(match) != 5 {
		return rawAltMouseEvent{}, false
	}
	button, _ := strconv.Atoi(match[1])
	x, _ := strconv.Atoi(match[2])
	y, _ := strconv.Atoi(match[3])
	return rawAltMouseEvent{button: button, x: x - 1, y: y - 1, release: match[4] == "m"}, true
}

func isAltMouseSequence(data string) bool {
	if sgrMousePattern.MatchString(data) {
		return true
	}
	return len(data) == 6 && data[:3] == "\x1b[M"
}

func (t *TuiAltScreen) getWheelScrollLines(button int) int {
	if button&8 != 0 {
		return t.wheelScrollLines * altWheelScrollMultiplier
	}
	return t.wheelScrollLines
}

func (t *TuiAltScreen) dispatchMouseToLayout(event MouseEvent) *MouseResult {
	if t.currentLayout == nil {
		return nil
	}
	visited := make([]Component, 0)
	for _, box := range GetLayoutBoxesAt(*t.currentLayout, event.ScreenX, event.ScreenY) {
		alreadyVisited := false
		for _, component := range visited {
			if sameComponent(component, box.Component) {
				alreadyVisited = true
				break
			}
		}
		if alreadyVisited {
			continue
		}
		visited = append(visited, box.Component)
		if _, structural := GetLayoutNode(box.Component); structural {
			continue
		}
		local := event
		local.X, local.Y = event.ScreenX-box.Rect.X, event.ScreenY-box.Rect.Y
		local.Width, local.Height = box.Rect.Width, box.Rect.Height
		if result := DispatchMouseEvent(box.Component, local); result != nil {
			return result
		}
	}
	return nil
}

func (t *TuiAltScreen) applyMouseDispatchResult(event MouseEvent, result *MouseResult) bool {
	if result == nil || result.Target == nil {
		return false
	}
	focusTarget := result.FocusTarget
	if focusTarget == nil {
		focusTarget = result.Target.Component
	}
	focusTarget = t.ResolveMouseFocusTarget(focusTarget)
	focusChanged := result.Focus && !sameComponent(t.FocusedComponent(), focusTarget)
	if result.Focus {
		t.SetFocus(focusTarget)
	}
	if result.Capture {
		target := *result.Target
		t.mouseCapture = &target
	}
	if result.Render != nil {
		return *result.Render
	}
	return focusChanged || event.Type == MousePress || event.Type == MouseClick || event.Type == MouseDrag || event.Type == MouseWheel
}

func (t *TuiAltScreen) dispatchMouseToTarget(event MouseEvent, target *MouseDispatchTarget) *MouseResult {
	if target == nil {
		return nil
	}
	return DispatchMouseEvent(target.Component, RetargetMouseEvent(event, *target))
}

func (t *TuiAltScreen) componentClickCount(target *MouseDispatchTarget, x, y int) int {
	now := time.Now()
	previous := t.lastComponentClick
	count := 1
	if previous != nil && now.Sub(previous.timestamp) <= doubleClickInterval &&
		sameComponent(previous.component, target.Component) && previous.x == x && previous.y == y {
		count = previous.count%3 + 1
	}
	t.lastComponentClick = &componentClickState{timestamp: now, count: count, component: target.Component, x: x, y: y}
	return count
}

func (t *TuiAltScreen) clearComponentMouseGesture() {
	t.mouseCapture = nil
	t.mousePressTarget = nil
	t.mousePressPoint = nil
	t.mousePressMoved = false
}

func (t *TuiAltScreen) handleSearchMouseEvent(raw rawAltMouseEvent) bool {
	search := t.activeSearch
	if search == nil || search.overlay == nil {
		return false
	}
	bounds, ok := search.overlay.Bounds()
	if !ok || raw.x < bounds.Col || raw.x >= bounds.Col+bounds.Width || raw.y < bounds.Row || raw.y >= bounds.Row+bounds.Height {
		if search.component.SetHoveredNavigationDirection(0, false) {
			t.RequestRender(false)
		}
		return false
	}
	direction, present := search.component.NavigationDirectionAt(raw.y-bounds.Row, raw.x-bounds.Col)
	if search.component.SetHoveredNavigationDirection(direction, present) {
		t.RequestRender(false)
	}
	if !present || raw.release || raw.button&32 != 0 || raw.button&3 != 0 {
		return false
	}
	t.NavigateSearch(direction)
	return true
}

func (t *TuiAltScreen) handleAltMouseEvent(raw rawAltMouseEvent) {
	isMotion := raw.button&32 != 0
	eventType := MousePress
	if raw.release {
		eventType = MouseRelease
	} else if isMotion {
		if decodeAltMouseButton(raw.button) == MouseNone {
			eventType = MouseMove
		} else {
			eventType = MouseDrag
		}
	}
	event := t.createAltMouseEvent(eventType, raw.button, raw.x, raw.y, nil, nil)
	if t.mouseCapture != nil || t.mousePressTarget != nil {
		target := t.mouseCapture
		if target == nil {
			target = t.mousePressTarget
		}
		if t.mousePressPoint != nil && (raw.x != t.mousePressPoint.x || raw.y != t.mousePressPoint.y) {
			t.mousePressMoved = true
			t.lastComponentClick = nil
		}
		render := t.applyMouseDispatchResult(event, t.dispatchMouseToTarget(event, target))
		if raw.release {
			if !t.mousePressMoved && t.mousePressPoint != nil && t.mousePressPoint.x == raw.x && t.mousePressPoint.y == raw.y {
				count := t.componentClickCount(target, raw.x, raw.y)
				click := t.createAltMouseEvent(MouseClick, raw.button, raw.x, raw.y, nil, &count)
				render = t.applyMouseDispatchResult(click, t.dispatchMouseToTarget(click, target)) || render
			}
			if t.selectionPressActive {
				// A drag over a selectable component ends like any other drag.
				t.finishSelection()
				render = true
			}
			t.clearComponentMouseGesture()
		} else if t.selectionPressActive {
			t.extendSelection(raw)
		}
		if render {
			t.RequestRender(false)
		}
		return
	}
	if t.handleSearchMouseEvent(raw) {
		return
	}
	overlay := t.DispatchMouseToOverlay(event)
	if !overlay.Hit {
		scrollbarHandled := t.handleScrollbarMouseEvent(raw)
		if t.scrollbarDrag == nil {
			t.updateScrollbarHover(raw.x, raw.y)
		}
		if scrollbarHandled {
			return
		}
	} else {
		t.stopScrollbarHover()
	}
	result := overlay.Result
	if result == nil && !overlay.Hit {
		result = t.dispatchMouseToLayout(event)
	}
	if result == nil {
		if t.handleRightClickPaste(raw) {
			return
		}
		t.handleSelectionMouseEvent(raw)
		return
	}
	render := t.applyMouseDispatchResult(event, result)
	if eventType == MousePress && result.Target != nil {
		if result.Selectable {
			// A text surface: the component keeps its press handling (the editor
			// moves the caret to the press point) and the viewport anchors a
			// selection, so a drag over it highlights. A plain click clears it.
			t.anchorSelection(raw)
		} else {
			t.clearTextSelection()
		}
		target := *result.Target
		t.mousePressTarget = &target
		t.mousePressPoint = &mousePoint{x: raw.x, y: raw.y}
		t.mousePressMoved = false
	}
	if render {
		t.RequestRender(false)
	}
}

func (t *TuiAltScreen) routeWheel(event altWheelEvent) {
	remaining := event.direction * t.getWheelScrollLines(event.button)
	seen := make(map[*ScrollView]bool)
	if t.currentLayout != nil {
		for _, scrollView := range GetScrollViewsAt(*t.currentLayout, event.x, event.y) {
			seen[scrollView] = true
			remaining = scrollView.ScrollBy(remaining)
			if remaining == 0 || scrollView.Overscroll() == OverscrollContain {
				break
			}
		}
	}
	primary := t.primaryScrollViewLocked()
	if remaining != 0 && !seen[primary] {
		primary.ScrollBy(remaining)
	}
	t.updateScrollbarHover(event.x, event.y)
	t.RequestRender(false)
}

type scrollbarTarget struct {
	scrollView *ScrollView
	geometry   ScrollbarGeometry
}

func (t *TuiAltScreen) scrollbarTargetAt(x, y int, includeHiddenAuto bool) (scrollbarTarget, bool) {
	if t.HasOverlay() || t.currentLayout == nil {
		return scrollbarTarget{}, false
	}
	for _, scrollView := range GetScrollViewsAt(*t.currentLayout, x, y) {
		box, ok := GetScrollViewBox(*t.currentLayout, scrollView)
		if !ok {
			continue
		}
		geometry, ok := GetScrollbarGeometry(box, includeHiddenAuto)
		if ok && x == geometry.Column && y >= geometry.TrackTop && y < geometry.TrackTop+geometry.TrackHeight {
			return scrollbarTarget{scrollView: scrollView, geometry: geometry}, true
		}
	}
	return scrollbarTarget{}, false
}

func (t *TuiAltScreen) setScrollbarHover(scrollView *ScrollView) {
	if t.scrollbarHover == scrollView {
		return
	}
	if t.scrollbarHover != nil {
		t.scrollbarHover.SetScrollbarActive(false)
	}
	t.scrollbarHover = scrollView
	if scrollView != nil {
		scrollView.SetScrollbarActive(true)
	}
}

func (t *TuiAltScreen) updateScrollbarHover(x, y int) {
	target, ok := t.scrollbarTargetAt(x, y, true)
	if !ok {
		t.setScrollbarHover(nil)
		return
	}
	t.setScrollbarHover(target.scrollView)
}

func (t *TuiAltScreen) stopScrollbarHover() { t.setScrollbarHover(nil) }

func scrollScrollbarToPointer(scrollView *ScrollView, geometry ScrollbarGeometry, pointerY, grabOffset int) {
	maxThumbOffset := geometry.TrackHeight - geometry.ThumbHeight
	thumbOffset := max(0, min(maxThumbOffset, pointerY-geometry.TrackTop-grabOffset))
	scrollTop := 0
	if maxThumbOffset != 0 {
		scrollTop = int(math.Floor(float64(thumbOffset)/float64(maxThumbOffset)*float64(geometry.MaxScrollTop) + 0.5))
	}
	scrollView.ScrollTo(scrollTop)
}

func (t *TuiAltScreen) handleScrollbarMouseEvent(event rawAltMouseEvent) bool {
	if t.scrollbarDrag != nil {
		if event.release {
			t.scrollbarDrag = nil
			return true
		}
		if t.currentLayout != nil {
			if box, ok := GetScrollViewBox(*t.currentLayout, t.scrollbarDrag.scrollView); ok {
				if geometry, ok := GetScrollbarGeometry(box, false); ok {
					scrollScrollbarToPointer(t.scrollbarDrag.scrollView, geometry, event.y, t.scrollbarDrag.grabOffset)
				}
			}
		}
		return true
	}
	if event.release || event.button&32 != 0 || event.button&3 != 0 {
		return false
	}
	target, ok := t.scrollbarTargetAt(event.x, event.y, false)
	if !ok {
		return false
	}
	t.clearTextSelection()
	t.lastSelectionClick = nil
	t.setScrollbarHover(target.scrollView)
	onThumb := event.y >= target.geometry.ThumbTop && event.y < target.geometry.ThumbTop+target.geometry.ThumbHeight
	grabOffset := target.geometry.ThumbHeight / 2
	if onThumb {
		grabOffset = event.y - target.geometry.ThumbTop
	} else {
		scrollScrollbarToPointer(target.scrollView, target.geometry, event.y, grabOffset)
	}
	t.scrollbarDrag = &scrollbarDragState{scrollView: target.scrollView, grabOffset: grabOffset}
	return true
}
