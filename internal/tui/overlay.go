package tui

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

// OverlayAnchor identifies an overlay anchor point.
type OverlayAnchor string

const (
	AnchorCenter       OverlayAnchor = "center"
	AnchorTopLeft      OverlayAnchor = "top-left"
	AnchorTopRight     OverlayAnchor = "top-right"
	AnchorBottomLeft   OverlayAnchor = "bottom-left"
	AnchorBottomRight  OverlayAnchor = "bottom-right"
	AnchorTopCenter    OverlayAnchor = "top-center"
	AnchorBottomCenter OverlayAnchor = "bottom-center"
	AnchorLeftCenter   OverlayAnchor = "left-center"
	AnchorRightCenter  OverlayAnchor = "right-center"
)

// SizeValue is either an absolute cell count or a percentage expression.
type SizeValue struct {
	absolute   *int
	percentage string
}

// Cells constructs an absolute overlay size or position.
func Cells(value int) SizeValue { return SizeValue{absolute: &value} }

// Percent constructs a percentage expression. Malformed values are retained so
// row/column resolution can follow the reference's center fallback.
func Percent(value string) SizeValue { return SizeValue{percentage: value} }

// OverlayMargin configures distances from terminal edges.
type OverlayMargin struct {
	Top    int
	Right  int
	Bottom int
	Left   int
}

// UniformMargin constructs equal margins on all sides.
func UniformMargin(value int) *OverlayMargin {
	return &OverlayMargin{Top: value, Right: value, Bottom: value, Left: value}
}

// OverlayOptions configure overlay sizing, placement, and focus capture.
type OverlayOptions struct {
	Width        *SizeValue
	MinWidth     *int
	MaxHeight    *SizeValue
	Anchor       OverlayAnchor
	OffsetX      *int
	OffsetY      *int
	Row          *SizeValue
	Col          *SizeValue
	Margin       *OverlayMargin
	Visible      func(termWidth, termHeight int) bool
	NonCapturing bool
}

// OverlayBounds is the most recent terminal-relative overlay rectangle.
type OverlayBounds struct {
	Row    int `json:"row"`
	Col    int `json:"col"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type overlayEntry struct {
	component  Component
	options    OverlayOptions
	preFocus   Component
	hidden     bool
	focusOrder uint64
	bounds     *OverlayBounds
}

type renderedOverlayLayout struct {
	entry  *overlayEntry
	row    int
	col    int
	width  int
	height int
}

type overlayRestoreStatus uint8

const (
	overlayRestoreInactive overlayRestoreStatus = iota
	overlayRestoreEligible
	overlayRestoreBlocked
)

type overlayFocusRestore struct {
	status         overlayRestoreStatus
	overlay        *overlayEntry
	blockedBy      Component
	restoreOverlay bool
	resumeTarget   Component
}

// OverlayHandle controls one overlay entry.
type OverlayHandle struct {
	base  *TuiBase
	entry *overlayEntry
}

// SetFocus changes keyboard focus and clears pending overlay restoration when
// appropriate.
func (t *TuiBase) SetFocus(component Component) {
	t.setFocusInternal(component, false)
}

func (t *TuiBase) setFocusInternal(component Component, preserveRestore bool) {
	previousFocus := t.focusedComponent
	nextFocus := component
	previousOverlay := t.findVisibleOverlayByComponent(previousFocus)
	nextIsOverlay := t.findOverlayByComponent(nextFocus) != nil
	restore := t.visibleOverlayRestore()

	if nextFocus != nil && !nextIsOverlay {
		if restore.status == overlayRestoreBlocked && sameComponent(restore.blockedBy, previousFocus) {
			if !restore.restoreOverlay || !t.isComponentMounted(restore.blockedBy) {
				nextFocus = t.resolveBlockedOverlayResume(restore)
			} else {
				t.overlayRestore = overlayFocusRestore{
					status: overlayRestoreBlocked, overlay: restore.overlay,
					blockedBy: nextFocus, restoreOverlay: restore.restoreOverlay, resumeTarget: restore.resumeTarget,
				}
			}
		} else if previousOverlay != nil && restore.status != overlayRestoreInactive &&
			restore.overlay == previousOverlay && !t.isOverlayFocusAncestor(previousOverlay, nextFocus) {
			t.overlayRestore = overlayFocusRestore{
				status: overlayRestoreBlocked, overlay: previousOverlay,
				blockedBy: nextFocus, restoreOverlay: true,
			}
		}
	} else if nextFocus == nil {
		if restore.status == overlayRestoreBlocked && sameComponent(restore.blockedBy, previousFocus) {
			nextFocus = t.resolveBlockedOverlayResume(restore)
		} else if !preserveRestore {
			t.clearOverlayRestore()
		}
	}

	if focusable, ok := t.focusedComponent.(Focusable); ok {
		focusable.SetFocused(false)
	}
	t.focusedComponent = nextFocus
	if focusable, ok := nextFocus.(Focusable); ok {
		focusable.SetFocused(true)
	}
	if focusedOverlay := t.findVisibleOverlayByComponent(nextFocus); focusedOverlay != nil {
		t.overlayRestore = overlayFocusRestore{status: overlayRestoreEligible, overlay: focusedOverlay}
	}
}

func (t *TuiBase) clearOverlayRestore() {
	t.overlayRestore = overlayFocusRestore{status: overlayRestoreInactive}
}

func (t *TuiBase) clearOverlayRestoreFor(overlay *overlayEntry) {
	if t.overlayRestore.status != overlayRestoreInactive && t.overlayRestore.overlay == overlay {
		t.clearOverlayRestore()
	}
}

func (t *TuiBase) resolveBlockedOverlayResume(restore overlayFocusRestore) Component {
	if restore.restoreOverlay {
		return restore.overlay.component
	}
	t.clearOverlayRestore()
	return restore.resumeTarget
}

func (t *TuiBase) visibleOverlayRestore() overlayFocusRestore {
	restore := t.overlayRestore
	if restore.status == overlayRestoreInactive {
		return restore
	}
	if !t.hasOverlayEntry(restore.overlay) || !t.isOverlayVisible(restore.overlay) {
		return overlayFocusRestore{status: overlayRestoreInactive}
	}
	return restore
}

func (t *TuiBase) isOverlayFocusAncestor(entry *overlayEntry, component Component) bool {
	visited := map[*overlayEntry]bool{}
	current := entry.preFocus
	for current != nil {
		if sameComponent(current, component) {
			return true
		}
		overlay := t.findOverlayByComponent(current)
		if overlay == nil || visited[overlay] {
			return false
		}
		visited[overlay] = true
		current = overlay.preFocus
	}
	return false
}

func (t *TuiBase) retargetOverlayPreFocus(removed *overlayEntry) {
	for _, overlay := range t.overlayStack {
		if overlay != removed && sameComponent(overlay.preFocus, removed.component) {
			overlay.preFocus = removed.preFocus
		}
	}
}

type childComponentProvider interface {
	ChildComponents() []Component
}

func (t *TuiBase) isComponentMounted(component Component) bool {
	for _, root := range t.mountedRoots() {
		if containsComponent(root, component) {
			return true
		}
	}
	return false
}

func containsComponent(root, target Component) bool {
	if sameComponent(root, target) {
		return true
	}
	container, ok := root.(childComponentProvider)
	if !ok {
		return false
	}
	for _, child := range container.ChildComponents() {
		if containsComponent(child, target) {
			return true
		}
	}
	return false
}

// ShowOverlay adds an overlay and returns its control handle.
func (t *TuiBase) ShowOverlay(component Component, options OverlayOptions) *OverlayHandle {
	t.focusOrderCounter++
	entry := &overlayEntry{
		component: component, options: options, preFocus: t.focusedComponent,
		focusOrder: t.focusOrderCounter,
	}
	t.overlayStack = append(t.overlayStack, entry)
	if !options.NonCapturing && t.isOverlayVisible(entry) {
		t.SetFocus(component)
	}
	t.Terminal.HideCursor()
	t.RequestRender(false)
	return &OverlayHandle{base: t, entry: entry}
}

// Hide permanently removes the overlay.
func (h *OverlayHandle) Hide() {
	base, entry := h.base, h.entry
	index := base.overlayIndex(entry)
	if index == -1 {
		return
	}
	base.clearOverlayRestoreFor(entry)
	base.retargetOverlayPreFocus(entry)
	base.overlayStack = append(base.overlayStack[:index], base.overlayStack[index+1:]...)
	if sameComponent(base.focusedComponent, entry.component) {
		top := base.topmostVisibleOverlay()
		if top != nil {
			base.SetFocus(top.component)
		} else {
			base.SetFocus(entry.preFocus)
		}
	}
	if len(base.overlayStack) == 0 {
		base.Terminal.HideCursor()
	}
	base.RequestRender(false)
}

// SetHidden temporarily hides or shows the overlay.
func (h *OverlayHandle) SetHidden(hidden bool) {
	entry, base := h.entry, h.base
	if entry.hidden == hidden || !base.hasOverlayEntry(entry) {
		return
	}
	entry.hidden = hidden
	if hidden {
		base.clearOverlayRestoreFor(entry)
		if sameComponent(base.focusedComponent, entry.component) {
			top := base.topmostVisibleOverlay()
			if top != nil {
				base.SetFocus(top.component)
			} else {
				base.SetFocus(entry.preFocus)
			}
		}
	} else if !entry.options.NonCapturing && base.isOverlayVisible(entry) {
		base.focusOrderCounter++
		entry.focusOrder = base.focusOrderCounter
		base.SetFocus(entry.component)
	}
	base.RequestRender(false)
}

// IsHidden reports temporary visibility state.
func (h *OverlayHandle) IsHidden() bool { return h.entry.hidden }

// Focus focuses and visually raises a visible overlay.
func (h *OverlayHandle) Focus() {
	base, entry := h.base, h.entry
	if !base.hasOverlayEntry(entry) || !base.isOverlayVisible(entry) {
		return
	}
	base.focusOrderCounter++
	entry.focusOrder = base.focusOrderCounter
	base.SetFocus(entry.component)
	base.RequestRender(false)
}

// Unfocus releases focus. With no argument it follows the overlay fallback;
// one argument is an explicit target and may itself be nil.
func (h *OverlayHandle) Unfocus(target ...Component) {
	base, entry := h.base, h.entry
	isFocused := sameComponent(base.focusedComponent, entry.component)
	restore := base.overlayRestore
	hasPending := restore.status != overlayRestoreInactive && restore.overlay == entry
	if !isFocused && !hasPending {
		return
	}
	explicit := len(target) > 0
	var explicitTarget Component
	if explicit {
		explicitTarget = target[0]
	}
	if restore.status == overlayRestoreBlocked && restore.overlay == entry && sameComponent(base.focusedComponent, restore.blockedBy) {
		if explicit {
			base.overlayRestore = overlayFocusRestore{
				status: overlayRestoreBlocked, overlay: entry, blockedBy: restore.blockedBy,
				resumeTarget: explicitTarget,
			}
		} else {
			base.clearOverlayRestore()
		}
		base.RequestRender(false)
		return
	}
	base.clearOverlayRestoreFor(entry)
	if isFocused || explicit {
		top := base.topmostVisibleOverlay()
		fallback := entry.preFocus
		if top != nil && top != entry {
			fallback = top.component
		}
		if explicit {
			base.SetFocus(explicitTarget)
		} else {
			base.SetFocus(fallback)
		}
	}
	base.RequestRender(false)
}

// IsFocused reports whether this overlay owns keyboard focus.
func (h *OverlayHandle) IsFocused() bool {
	return sameComponent(h.base.focusedComponent, h.entry.component)
}

// Bounds returns the most recent visible rendered bounds.
func (h *OverlayHandle) Bounds() (OverlayBounds, bool) {
	if !h.base.hasOverlayEntry(h.entry) || !h.base.isOverlayVisible(h.entry) || h.entry.bounds == nil {
		return OverlayBounds{}, false
	}
	return *h.entry.bounds, true
}

// HideOverlay removes the last inserted overlay, not necessarily the visually
// frontmost overlay.
func (t *TuiBase) HideOverlay() {
	if len(t.overlayStack) == 0 {
		return
	}
	overlay := t.overlayStack[len(t.overlayStack)-1]
	t.clearOverlayRestoreFor(overlay)
	t.retargetOverlayPreFocus(overlay)
	t.overlayStack = t.overlayStack[:len(t.overlayStack)-1]
	if sameComponent(t.focusedComponent, overlay.component) {
		top := t.topmostVisibleOverlay()
		if top != nil {
			t.SetFocus(top.component)
		} else {
			t.SetFocus(overlay.preFocus)
		}
	}
	if len(t.overlayStack) == 0 {
		t.Terminal.HideCursor()
	}
	t.RequestRender(false)
}

// HasOverlay reports whether at least one overlay is currently visible.
func (t *TuiBase) HasOverlay() bool {
	for _, overlay := range t.overlayStack {
		if t.isOverlayVisible(overlay) {
			return true
		}
	}
	return false
}

// HasOverlayEntries reports whether hidden entries still exist.
func (t *TuiBase) HasOverlayEntries() bool { return len(t.overlayStack) > 0 }

// IsOverlayFocused reports whether current focus is a visible overlay.
func (t *TuiBase) IsOverlayFocused() bool {
	return t.findVisibleOverlayByComponent(t.focusedComponent) != nil
}

// ResolveMouseFocusTarget keeps an overlay container as focus owner when a
// nested descendant was the concrete click target.
func (t *TuiBase) ResolveMouseFocusTarget(component Component) Component {
	for index := len(t.overlayStack) - 1; index >= 0; index-- {
		overlay := t.overlayStack[index]
		if t.isOverlayVisible(overlay) && containsComponent(overlay.component, component) {
			return overlay.component
		}
	}
	return component
}

// OverlayMouseDispatch reports whether an overlay rectangle was hit.
type OverlayMouseDispatch struct {
	Hit    bool
	Result *MouseResult
}

// DispatchMouseToOverlay dispatches to the visually topmost overlay under the
// absolute pointer coordinate.
func (t *TuiBase) DispatchMouseToOverlay(event MouseEvent) OverlayMouseDispatch {
	for index := len(t.renderedOverlays) - 1; index >= 0; index-- {
		layout := t.renderedOverlays[index]
		if event.ScreenX < layout.col || event.ScreenX >= layout.col+layout.width ||
			event.ScreenY < layout.row || event.ScreenY >= layout.row+layout.height {
			continue
		}
		local := event
		local.X = event.ScreenX - layout.col
		local.Y = event.ScreenY - layout.row
		local.Width = layout.width
		local.Height = layout.height
		result := DispatchMouseEvent(layout.entry.component, local)
		if result != nil && result.Focus {
			copy := *result
			copy.FocusTarget = layout.entry.component
			result = &copy
		}
		return OverlayMouseDispatch{Hit: true, Result: result}
	}
	return OverlayMouseDispatch{}
}

func (t *TuiBase) isOverlayVisible(entry *overlayEntry) bool {
	if entry.hidden {
		return false
	}
	return entry.options.Visible == nil || entry.options.Visible(t.Terminal.Columns(), t.Terminal.Rows())
}

func (t *TuiBase) topmostVisibleOverlay() *overlayEntry {
	var top *overlayEntry
	for _, overlay := range t.overlayStack {
		if overlay.options.NonCapturing || !t.isOverlayVisible(overlay) {
			continue
		}
		if top == nil || overlay.focusOrder > top.focusOrder {
			top = overlay
		}
	}
	return top
}

func (t *TuiBase) overlayIndex(entry *overlayEntry) int {
	for index, candidate := range t.overlayStack {
		if candidate == entry {
			return index
		}
	}
	return -1
}

func (t *TuiBase) hasOverlayEntry(entry *overlayEntry) bool { return t.overlayIndex(entry) != -1 }

func (t *TuiBase) findOverlayByComponent(component Component) *overlayEntry {
	if component == nil {
		return nil
	}
	for _, overlay := range t.overlayStack {
		if sameComponent(overlay.component, component) {
			return overlay
		}
	}
	return nil
}

func (t *TuiBase) findVisibleOverlayByComponent(component Component) *overlayEntry {
	overlay := t.findOverlayByComponent(component)
	if overlay != nil && t.isOverlayVisible(overlay) {
		return overlay
	}
	return nil
}

func (t *TuiBase) repairOverlayFocusForInput() {
	if focused := t.findOverlayByComponent(t.focusedComponent); focused != nil && !t.isOverlayVisible(focused) {
		top := t.topmostVisibleOverlay()
		if top != nil {
			t.SetFocus(top.component)
		} else {
			t.setFocusInternal(focused.preFocus, true)
		}
	}
	if t.findOverlayByComponent(t.focusedComponent) == nil {
		restore := t.visibleOverlayRestore()
		if restore.status == overlayRestoreEligible {
			t.SetFocus(restore.overlay.component)
		} else if restore.status == overlayRestoreBlocked && !sameComponent(restore.blockedBy, t.focusedComponent) {
			if restore.restoreOverlay {
				t.SetFocus(restore.overlay.component)
			} else {
				t.clearOverlayRestore()
				t.SetFocus(restore.resumeTarget)
			}
		}
	}
}

var percentagePattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)%$`)

func parseSizeValue(value *SizeValue, reference int) (int, bool) {
	if value == nil {
		return 0, false
	}
	if value.absolute != nil {
		return *value.absolute, true
	}
	match := percentagePattern.FindStringSubmatch(value.percentage)
	if len(match) != 2 {
		return 0, false
	}
	percentage, _ := strconv.ParseFloat(match[1], 64)
	return int(math.Floor(float64(reference) * percentage / 100)), true
}

type resolvedOverlayLayout struct {
	width     int
	row       int
	col       int
	maxHeight *int
}

func (t *TuiBase) resolveOverlayLayout(options OverlayOptions, overlayHeight, termWidth, termHeight int) resolvedOverlayLayout {
	margin := OverlayMargin{}
	if options.Margin != nil {
		margin = *options.Margin
	}
	margin.Top = max(0, margin.Top)
	margin.Right = max(0, margin.Right)
	margin.Bottom = max(0, margin.Bottom)
	margin.Left = max(0, margin.Left)
	availableWidth := max(1, termWidth-margin.Left-margin.Right)
	availableHeight := max(1, termHeight-margin.Top-margin.Bottom)

	width, ok := parseSizeValue(options.Width, termWidth)
	if !ok {
		width = min(80, availableWidth)
	}
	if options.MinWidth != nil {
		width = max(width, *options.MinWidth)
	}
	width = max(1, min(width, availableWidth))

	var maxHeight *int
	if parsed, ok := parseSizeValue(options.MaxHeight, termHeight); ok {
		parsed = max(1, min(parsed, availableHeight))
		maxHeight = &parsed
	}
	effectiveHeight := overlayHeight
	if maxHeight != nil {
		effectiveHeight = min(effectiveHeight, *maxHeight)
	}

	anchor := options.Anchor
	if anchor == "" {
		anchor = AnchorCenter
	}
	row := 0
	if options.Row != nil {
		if options.Row.absolute != nil {
			row = *options.Row.absolute
		} else if match := percentagePattern.FindStringSubmatch(options.Row.percentage); len(match) == 2 {
			percentage, _ := strconv.ParseFloat(match[1], 64)
			maxRow := max(0, availableHeight-effectiveHeight)
			row = margin.Top + int(math.Floor(float64(maxRow)*percentage/100))
		} else {
			row = resolveAnchorRow(AnchorCenter, effectiveHeight, availableHeight, margin.Top)
		}
	} else {
		row = resolveAnchorRow(anchor, effectiveHeight, availableHeight, margin.Top)
	}

	col := 0
	if options.Col != nil {
		if options.Col.absolute != nil {
			col = *options.Col.absolute
		} else if match := percentagePattern.FindStringSubmatch(options.Col.percentage); len(match) == 2 {
			percentage, _ := strconv.ParseFloat(match[1], 64)
			maxCol := max(0, availableWidth-width)
			col = margin.Left + int(math.Floor(float64(maxCol)*percentage/100))
		} else {
			col = resolveAnchorCol(AnchorCenter, width, availableWidth, margin.Left)
		}
	} else {
		col = resolveAnchorCol(anchor, width, availableWidth, margin.Left)
	}
	if options.OffsetY != nil {
		row += *options.OffsetY
	}
	if options.OffsetX != nil {
		col += *options.OffsetX
	}
	row = max(margin.Top, min(row, termHeight-margin.Bottom-effectiveHeight))
	col = max(margin.Left, min(col, termWidth-margin.Right-width))
	return resolvedOverlayLayout{width: width, row: row, col: col, maxHeight: maxHeight}
}

func resolveAnchorRow(anchor OverlayAnchor, height, availableHeight, marginTop int) int {
	switch anchor {
	case AnchorTopLeft, AnchorTopCenter, AnchorTopRight:
		return marginTop
	case AnchorBottomLeft, AnchorBottomCenter, AnchorBottomRight:
		return marginTop + availableHeight - height
	default:
		return marginTop + (availableHeight-height)/2
	}
}

func resolveAnchorCol(anchor OverlayAnchor, width, availableWidth, marginLeft int) int {
	switch anchor {
	case AnchorTopLeft, AnchorLeftCenter, AnchorBottomLeft:
		return marginLeft
	case AnchorTopRight, AnchorRightCenter, AnchorBottomRight:
		return marginLeft + availableWidth - width
	default:
		return marginLeft + (availableWidth-width)/2
	}
}

// CompositeOverlays renders all visible overlays in focus order.
func (t *TuiBase) CompositeOverlays(lines []string, termWidth, termHeight int) []string {
	if len(t.overlayStack) == 0 {
		t.renderedOverlays = []renderedOverlayLayout{}
		return lines
	}
	result := append([]string(nil), lines...)
	for _, entry := range t.overlayStack {
		entry.bounds = nil
	}
	visible := make([]*overlayEntry, 0, len(t.overlayStack))
	for _, entry := range t.overlayStack {
		if t.isOverlayVisible(entry) {
			visible = append(visible, entry)
		}
	}
	slices.SortStableFunc(visible, func(a, b *overlayEntry) int { return cmp.Compare(a.focusOrder, b.focusOrder) })

	type rendered struct {
		entry           *overlayEntry
		lines           []string
		row, col, width int
	}
	renderedEntries := make([]rendered, 0, len(visible))
	minimumLines := len(result)
	for _, entry := range visible {
		initial := t.resolveOverlayLayout(entry.options, 0, termWidth, termHeight)
		overlayLines := entry.component.Render(initial.width)
		if initial.maxHeight != nil && len(overlayLines) > *initial.maxHeight {
			overlayLines = overlayLines[:*initial.maxHeight]
		}
		final := t.resolveOverlayLayout(entry.options, len(overlayLines), termWidth, termHeight)
		bounds := OverlayBounds{Row: final.row, Col: final.col, Width: final.width, Height: len(overlayLines)}
		entry.bounds = &bounds
		renderedEntries = append(renderedEntries, rendered{
			entry: entry, lines: overlayLines, row: final.row, col: final.col, width: final.width,
		})
		minimumLines = max(minimumLines, final.row+len(overlayLines))
	}
	t.renderedOverlays = make([]renderedOverlayLayout, len(renderedEntries))
	for index, entry := range renderedEntries {
		t.renderedOverlays[index] = renderedOverlayLayout{
			entry: entry.entry, row: entry.row, col: entry.col, width: entry.width, height: len(entry.lines),
		}
	}
	workingHeight := max(len(result), termHeight, minimumLines)
	for len(result) < workingHeight {
		result = append(result, "")
	}
	viewportStart := max(0, workingHeight-termHeight)
	for _, entry := range renderedEntries {
		for index, line := range entry.lines {
			row := viewportStart + entry.row + index
			if row < 0 || row >= len(result) {
				continue
			}
			if tuitext.VisibleWidth(line) > entry.width {
				line = tuitext.SliceByColumn(line, 0, entry.width, true)
			}
			result[row] = CompositeTuiLine(result[row], line, entry.col, entry.width, termWidth)
		}
	}
	return result
}

// ApplyLineResets normalizes text rows and appends the exact segment reset.
func (t *TuiBase) ApplyLineResets(lines []string) []string {
	for index, line := range lines {
		if !IsImageLine(line) {
			lines[index] = tuitext.NormalizeTerminalOutput(line) + segmentReset
		}
	}
	return lines
}

// CursorPosition is an absolute row and visible column.
type CursorPosition struct {
	Row int
	Col int
}

// ExtractCursorPosition scans the bottom visible viewport from bottom upward,
// strips the chosen marker, and returns its absolute row.
func (t *TuiBase) ExtractCursorPosition(lines []string, height int) (CursorPosition, bool) {
	viewportTop := max(0, len(lines)-height)
	for row := len(lines) - 1; row >= viewportTop; row-- {
		index := strings.Index(lines[row], CursorMarker)
		if index == -1 {
			continue
		}
		column := tuitext.VisibleWidth(lines[row][:index])
		lines[row] = lines[row][:index] + lines[row][index+len(CursorMarker):]
		return CursorPosition{Row: row, Col: column}, true
	}
	return CursorPosition{}, false
}
