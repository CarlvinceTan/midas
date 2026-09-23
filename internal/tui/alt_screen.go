package tui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

const (
	enterAltScreen          = "\x1b[?1049h"
	exitAltScreen           = "\x1b[?1049l"
	disableAutowrap         = "\x1b[?7l"
	enableAutowrap          = "\x1b[?7h"
	enableButtonMotionMouse = "\x1b[?1000h\x1b[?1002h\x1b[?1004h\x1b[?1006h"
	enableAllMotionMouse    = "\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1004h\x1b[?1006h"
	disableMouse            = "\x1b[?1006l\x1b[?1004l\x1b[?1003l\x1b[?1002l\x1b[?1000l"

	maxCachedOffscreenKittyImages            = 16
	maxCachedOffscreenKittyTransmissionBytes = int64(32 * 1024 * 1024)
	maxCachedOffscreenKittyDecodedBytes      = int64(64 * 1024 * 1024)
	pageScrollOverlap                        = 4
)

// TuiAltScreenOptions configures alternate-screen ownership and painting.
// Mouse defaults to enabled when nil.
type TuiAltScreenOptions struct {
	ShowHardwareCursor          bool
	Scheduler                   Scheduler
	Mouse                       *bool
	IsMultiplexer               func() bool
	SearchMatchStyle            func(string) string
	SearchCurrentMatchStyle     func(string) string
	SearchNavigationButtonStyle SearchNavigationButtonStyle
	WheelScrollLines            float64
	OpenURL                     func(string)
	OnRightClickPaste           func()
	CopyOnSelect                *bool
	CopySelection               func(string) bool
}

type cachedKittyImage struct {
	imageID                int
	transmissionGeneration uint64
	transmissionBytes      int64
	estimatedDecodedBytes  int64
}

// TuiAltScreen renders and owns a fixed fullscreen terminal viewport.
type TuiAltScreen struct {
	*TuiBase

	previousScreen                []string
	selectionScreen               []string
	lastDocument                  []string
	previousScreenWidth           int
	previousScreenHeight          int
	layoutRoot                    Component
	currentLayout                 *LayoutFrame
	implicitDocument              *altImplicitDocument
	implicitScrollView            *ScrollView
	altScreenActive               bool
	imageProtocol                 ImageProtocol
	savedCapabilities             *TerminalCapabilities
	uploadedKittyImages           []cachedKittyImage
	mouseEnabled                  bool
	isMultiplexer                 func() bool
	activeSearch                  *activeAltScreenSearch
	searchMatchStyle              func(string) string
	searchCurrentMatchStyle       func(string) string
	searchNavigationButtonStyle   SearchNavigationButtonStyle
	wheelScrollLines              int
	mouseCapture                  *MouseDispatchTarget
	mousePressTarget              *MouseDispatchTarget
	mousePressPoint               *mousePoint
	mousePressMoved               bool
	lastComponentClick            *componentClickState
	scrollbarDrag                 *scrollbarDragState
	scrollbarHover                *ScrollView
	selectionAnchor               *altSelectionPoint
	selectionFocus                *altSelectionPoint
	selectionGranularity          altSelectionGranularity
	selectionInitialRange         *altSelectionRange
	lastSelectionClick            *selectionClickState
	selectionPressActive          bool
	selectionDragged              bool
	selectionDragPointer          *mousePoint
	selectionAutoScrollDirection  int
	selectionAutoScrollTimer      *time.Timer
	selectionAutoScrollGeneration uint64
	selectionTimerMu              sync.Mutex
	pressedURL                    string
	hasPressedURL                 bool
	openURL                       func(string)
	onRightClickPaste             func()
	copyOnSelect                  bool
	copySelection                 func(string) bool
	flashes                       *AltScreenFlashContainer
}

type altImplicitDocument struct {
	base *TuiBase
}

func (d *altImplicitDocument) Render(width int) []string {
	return d.base.Container.Render(width)
}

func (d *altImplicitDocument) Invalidate() { d.base.Container.Invalidate() }

func (d *altImplicitDocument) HandleMouse(event MouseEvent) *MouseResult {
	return d.base.Container.HandleMouse(event)
}

// NewTuiAltScreen constructs a fullscreen viewport renderer.
func NewTuiAltScreen(terminal Terminal, options TuiAltScreenOptions) *TuiAltScreen {
	mouseEnabled := true
	if options.Mouse != nil {
		mouseEnabled = *options.Mouse
	}
	tui := &TuiAltScreen{
		previousScreen:              []string{},
		selectionScreen:             []string{},
		lastDocument:                []string{},
		uploadedKittyImages:         []cachedKittyImage{},
		mouseEnabled:                mouseEnabled,
		isMultiplexer:               options.IsMultiplexer,
		searchMatchStyle:            options.SearchMatchStyle,
		searchCurrentMatchStyle:     options.SearchCurrentMatchStyle,
		searchNavigationButtonStyle: options.SearchNavigationButtonStyle,
		wheelScrollLines:            max(1, int(math.Floor(options.WheelScrollLines))),
		selectionGranularity:        selectionCharacter,
		openURL:                     options.OpenURL,
		onRightClickPaste:           options.OnRightClickPaste,
		copyOnSelect:                true,
		copySelection:               options.CopySelection,
	}
	if options.CopyOnSelect != nil {
		tui.copyOnSelect = *options.CopyOnSelect
	}
	if tui.searchMatchStyle == nil {
		tui.searchMatchStyle = func(text string) string { return "\x1b[4m" + text + "\x1b[24m" }
	}
	if tui.searchCurrentMatchStyle == nil {
		tui.searchCurrentMatchStyle = func(text string) string { return "\x1b[1;7m" + text + "\x1b[22;27m" }
	}
	if tui.searchNavigationButtonStyle == nil {
		tui.searchNavigationButtonStyle = func(text string, _ bool) string { return text }
	}
	if tui.isMultiplexer == nil {
		tui.isMultiplexer = isMultiplexerEnvironment
	}
	tui.TuiBase = NewTuiBase(terminal, ModeFullscreen, BaseOptions{
		ShowHardwareCursor: options.ShowHardwareCursor,
		Scheduler:          options.Scheduler,
		Hooks: TuiHooks{
			Render:              tui.renderAltScreen,
			ResetRenderState:    tui.resetAltRenderState,
			BeforeTerminalStart: tui.beforeAltTerminalStart,
			BeforeTerminalStop:  tui.beforeAltTerminalStop,
			AfterTerminalStop:   tui.afterAltTerminalStop,
			MountedRoots:        tui.mountedAltRoots,
		},
	})
	tui.flashes = NewAltScreenFlashContainer(func() { tui.RequestRender(false) })
	tui.implicitDocument = &altImplicitDocument{base: tui.TuiBase}
	tui.implicitScrollView = NewScrollView(tui.implicitDocument, ScrollViewOptions{
		FollowEnd: true,
		Primary:   true,
	})
	tui.AddInputListener(tui.handleViewportInput)
	return tui
}

func isMultiplexerEnvironment() bool {
	if _, ok := os.LookupEnv("TMUX"); ok {
		return true
	}
	if _, ok := os.LookupEnv("ZELLIJ"); ok {
		return true
	}
	if _, ok := os.LookupEnv("STY"); ok {
		return true
	}
	term := strings.ToLower(os.Getenv("TERM"))
	return strings.HasPrefix(term, "tmux") || strings.HasPrefix(term, "screen")
}

func (t *TuiAltScreen) mountedAltRoots() []Component {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	if t.layoutRoot != nil {
		return []Component{t.layoutRoot}
	}
	return append([]Component(nil), t.Children...)
}

// Render renders the explicit layout root when set, otherwise the ordinary
// document children. This unbounded form is used when restoring the document
// to the main screen.
func (t *TuiAltScreen) Render(width int) []string {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	return t.renderDocumentLocked(width)
}

func (t *TuiAltScreen) renderDocumentLocked(width int) []string {
	if t.layoutRoot != nil {
		return t.layoutRoot.Render(width)
	}
	return t.TuiBase.Container.Render(width)
}

// SetLayoutRoot switches between implicit document scrolling and an explicit
// fixed-layout root.
func (t *TuiAltScreen) SetLayoutRoot(component Component) {
	t.frameMu.Lock()
	if sameComponent(t.layoutRoot, component) {
		t.frameMu.Unlock()
		return
	}
	t.layoutRoot = component
	t.currentLayout = nil
	t.frameMu.Unlock()
	t.RequestRender(false)
}

// ViewportTop returns the primary scroll view's current top row.
func (t *TuiAltScreen) ViewportTop() int {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	return t.primaryScrollViewLocked().ScrollTop()
}

// IsFollowingOutput reports whether the primary viewport follows document growth.
func (t *TuiAltScreen) IsFollowingOutput() bool {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	return t.primaryScrollViewLocked().IsFollowingEnd()
}

func (t *TuiAltScreen) primaryScrollViewLocked() *ScrollView {
	if t.currentLayout != nil && t.currentLayout.PrimaryScrollView != nil {
		return t.currentLayout.PrimaryScrollView
	}
	return t.implicitScrollView
}

// ScrollBy moves the primary viewport by logical rows.
func (t *TuiAltScreen) ScrollBy(lines int) {
	t.frameMu.Lock()
	t.primaryScrollViewLocked().ScrollBy(lines)
	t.frameMu.Unlock()
	t.RequestRender(false)
}

// ScrollToTop moves the primary viewport to its first row.
func (t *TuiAltScreen) ScrollToTop() {
	t.frameMu.Lock()
	t.primaryScrollViewLocked().ScrollToStart()
	t.frameMu.Unlock()
	t.RequestRender(false)
}

// ScrollToBottom moves the primary viewport to its last row.
func (t *TuiAltScreen) ScrollToBottom() {
	t.frameMu.Lock()
	t.primaryScrollViewLocked().ScrollToEnd()
	t.frameMu.Unlock()
	t.RequestRender(false)
}

func (t *TuiAltScreen) handleViewportKeyboardInput(data string) InputListenerResult {
	keybindings := GetKeybindings()
	isRelease := IsKeyRelease(data)
	consume := func(action KeybindingID, callback func()) (InputListenerResult, bool) {
		if !keybindings.Matches(data, action) {
			return InputListenerResult{}, false
		}
		if !isRelease {
			callback()
		}
		return InputListenerResult{Consume: true}, true
	}
	if result, matched := consume("tui.altScreen.search", t.ToggleSearch); matched {
		return result
	}
	t.frameMu.Lock()
	search := t.activeSearch
	searchFocused := search != nil && search.overlay != nil && search.overlay.IsFocused()
	t.frameMu.Unlock()
	if searchFocused {
		if result, matched := consume("tui.altScreen.searchNext", func() { t.NavigateSearch(1) }); matched {
			return result
		}
		if result, matched := consume("tui.altScreen.searchPrevious", func() { t.NavigateSearch(-1) }); matched {
			return result
		}
		if result, matched := consume("tui.altScreen.searchClose", t.CloseSearch); matched {
			return result
		}
	}
	if t.IsOverlayFocused() && !searchFocused {
		return InputListenerResult{}
	}
	if result, matched := consume("tui.altScreen.pageUp", func() {
		t.ScrollBy(-max(1, t.primaryViewportHeight()-pageScrollOverlap))
	}); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.pageDown", func() {
		t.ScrollBy(max(1, t.primaryViewportHeight()-pageScrollOverlap))
	}); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.halfPageUp", func() {
		t.ScrollBy(-max(1, t.primaryViewportHeight()/2))
	}); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.halfPageDown", func() {
		t.ScrollBy(max(1, t.primaryViewportHeight()/2))
	}); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.lineUp", func() { t.ScrollBy(-1) }); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.lineDown", func() { t.ScrollBy(1) }); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.previousPrompt", func() { t.scrollToPrompt(-1) }); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.nextPrompt", func() { t.scrollToPrompt(1) }); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.top", t.ScrollToTop); matched {
		return result
	}
	if result, matched := consume("tui.altScreen.bottom", t.ScrollToBottom); matched {
		return result
	}
	return InputListenerResult{}
}

func (t *TuiAltScreen) primaryViewportHeight() int {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	return t.primaryScrollViewLocked().ViewportHeight()
}

func (t *TuiAltScreen) scrollToPrompt(direction int) {
	t.frameMu.Lock()
	if t.currentLayout == nil {
		t.frameMu.Unlock()
		return
	}
	scrollView := t.primaryScrollViewLocked()
	box, ok := GetScrollViewBox(*t.currentLayout, scrollView)
	if !ok || !box.HasScrollContent {
		t.frameMu.Unlock()
		return
	}
	start := scrollView.ScrollTop() + direction
	found := false
	for row := start; row >= 0 && row < len(box.ScrollContent); row += direction {
		line := box.ScrollContent[row]
		if strings.HasPrefix(line, "\x1b]133;A\x07") || strings.HasPrefix(line, "\x1b]133;A\x1b\\") {
			scrollView.ScrollTo(row)
			found = true
			break
		}
	}
	t.frameMu.Unlock()
	if found {
		t.RequestRender(false)
	}
}

func (t *TuiAltScreen) beforeAltTerminalStart() {
	t.stopSelectionAutoScroll()
	t.stopScrollbarHover()
	t.scrollbarDrag = nil
	t.clearComponentMouseGesture()
	t.lastComponentClick = nil
	t.clearTextSelection()
	t.lastSelectionClick = nil
	t.flashes.Dispose()
	capabilities := GetCapabilities()
	t.frameMu.Lock()
	t.altScreenActive = true
	t.imageProtocol = capabilities.Images
	t.uploadedKittyImages = []cachedKittyImage{}
	needsInvalidate := capabilities.Images == ImageITerm2
	if needsInvalidate {
		saved := capabilities
		t.savedCapabilities = &saved
		capabilities.Images = ImageNone
		SetCapabilities(capabilities)
	}
	t.frameMu.Unlock()

	if needsInvalidate {
		t.TuiBase.Invalidate()
	}

	t.frameMu.Lock()
	t.lastDocument = []string{}
	t.resetAltRenderStateLocked()
	mouseSequence := ""
	if t.mouseEnabled {
		mouseSequence = enableAllMotionMouse
		if t.isMultiplexer() {
			mouseSequence = enableButtonMotionMouse
		}
	}
	t.Terminal.Write(enterAltScreen + disableAutowrap + mouseSequence + "\x1b[2J\x1b[H\x1b[?25l")
	t.frameMu.Unlock()
}

func (t *TuiAltScreen) beforeAltTerminalStop(_ StopOptions) {
	t.CloseSearch()
	t.stopSelectionAutoScroll()
	t.stopScrollbarHover()
	t.scrollbarDrag = nil
	t.clearComponentMouseGesture()
	t.flashes.Dispose()
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	if !t.altScreenActive {
		return
	}
	mouseSequence := ""
	if t.mouseEnabled {
		mouseSequence = disableMouse
	}
	t.Terminal.Write(synchronizedOutputBegin + t.deleteAltKittyImagesLocked() + mouseSequence + enableAutowrap + synchronizedOutputEnd)
	t.uploadedKittyImages = []cachedKittyImage{}
}

func (t *TuiAltScreen) afterAltTerminalStop(options StopOptions) {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	if !t.altScreenActive {
		return
	}
	t.altScreenActive = false
	if options.PreserveScreen {
		t.Terminal.Write(synchronizedOutputBegin + exitAltScreen + "\x1b[?25h" + synchronizedOutputEnd)
	} else {
		width := max(1, t.Terminal.Columns())
		documentLines := t.renderDocumentLocked(width)
		for index, line := range documentLines {
			documentLines[index] = stripLeadingOSC133Zones(line)
			documentLines[index] = strings.ReplaceAll(documentLines[index], CursorMarker, "")
		}
		t.lastDocument = t.ApplyLineResets(documentLines)
		for index, line := range t.lastDocument {
			if !IsImageLine(line) && tuitext.VisibleWidth(line) > width {
				t.lastDocument[index] = tuitext.SliceByColumn(line, 0, width, true)
			}
		}
		var buffer strings.Builder
		buffer.WriteString(synchronizedOutputBegin)
		buffer.WriteString(exitAltScreen)
		buffer.WriteString(disableAutowrap)
		for row, line := range t.lastDocument {
			if row > 0 {
				buffer.WriteString("\r\n")
			}
			buffer.WriteString("\r\x1b[2K")
			buffer.WriteString(line)
		}
		buffer.WriteString("\x1b[0m")
		buffer.WriteString(enableAutowrap)
		buffer.WriteString("\r\n\x1b[?25h")
		buffer.WriteString(synchronizedOutputEnd)
		t.Terminal.Write(buffer.String())
	}
	if t.savedCapabilities != nil {
		SetCapabilities(*t.savedCapabilities)
		t.savedCapabilities = nil
	}
}

func (t *TuiAltScreen) deleteAltKittyImagesLocked() string {
	if t.imageProtocol == ImageKitty {
		return DeleteAllKittyImages()
	}
	return ""
}

func (t *TuiAltScreen) resetAltRenderState() {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	t.resetAltRenderStateLocked()
}

func (t *TuiAltScreen) resetAltRenderStateLocked() {
	t.previousScreen = []string{}
	t.previousScreenWidth = 0
	t.previousScreenHeight = 0
	t.currentLayout = nil
}

func (t *TuiAltScreen) prepareKittyScreen(screen []string) ([]string, string) {
	visibleImageIDs := make(map[int]struct{})
	lines := append([]string(nil), screen...)
	for index, line := range screen {
		placement, ok := GetKittyImagePlacement(line)
		if !ok {
			continue
		}
		visibleImageIDs[placement.ImageID] = struct{}{}
		cacheIndex := t.cachedKittyImageIndex(placement.ImageID)
		var previous *cachedKittyImage
		if cacheIndex != -1 {
			cached := t.uploadedKittyImages[cacheIndex]
			previous = &cached
			t.uploadedKittyImages = append(t.uploadedKittyImages[:cacheIndex], t.uploadedKittyImages[cacheIndex+1:]...)
		}
		t.uploadedKittyImages = append(t.uploadedKittyImages, cachedKittyImage{
			imageID:                placement.ImageID,
			transmissionGeneration: placement.TransmissionGeneration,
			transmissionBytes:      int64(placement.TransmissionBytes),
			estimatedDecodedBytes:  placement.EstimatedDecodedBytes,
		})
		if previous != nil && previous.transmissionGeneration == placement.TransmissionGeneration {
			lines[index] = placement.ReplacementLine
		}
	}

	offscreenCount := 0
	var offscreenTransmissionBytes int64
	var offscreenDecodedBytes int64
	for _, cached := range t.uploadedKittyImages {
		if _, visible := visibleImageIDs[cached.imageID]; visible {
			continue
		}
		offscreenCount++
		offscreenTransmissionBytes += cached.transmissionBytes
		offscreenDecodedBytes += cached.estimatedDecodedBytes
	}

	var evicted strings.Builder
	for offscreenCount > maxCachedOffscreenKittyImages ||
		offscreenTransmissionBytes > maxCachedOffscreenKittyTransmissionBytes ||
		offscreenDecodedBytes > maxCachedOffscreenKittyDecodedBytes {
		evictIndex := -1
		for index, cached := range t.uploadedKittyImages {
			if _, visible := visibleImageIDs[cached.imageID]; !visible {
				evictIndex = index
				break
			}
		}
		if evictIndex == -1 {
			break
		}
		cached := t.uploadedKittyImages[evictIndex]
		evicted.WriteString(DeleteKittyImage(uint32(cached.imageID)))
		t.uploadedKittyImages = append(t.uploadedKittyImages[:evictIndex], t.uploadedKittyImages[evictIndex+1:]...)
		offscreenCount--
		offscreenTransmissionBytes -= cached.transmissionBytes
		offscreenDecodedBytes -= cached.estimatedDecodedBytes
	}
	return lines, evicted.String()
}

func (t *TuiAltScreen) cachedKittyImageIndex(imageID int) int {
	for index, cached := range t.uploadedKittyImages {
		if cached.imageID == imageID {
			return index
		}
	}
	return -1
}

func (t *TuiAltScreen) renderAltScreen() {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	if t.isStopped() || !t.altScreenActive {
		return
	}
	width := max(1, t.Terminal.Columns())
	height := max(1, t.Terminal.Rows())
	root := Component(t.implicitScrollView)
	if t.layoutRoot != nil {
		root = t.layoutRoot
	}
	nextLayout := RenderLayoutFrame(root, width, height, func() { t.RequestRender(false) })
	if t.refreshSearchLocked(nextLayout) {
		nextLayout = RenderLayoutFrame(root, width, height, func() { t.RequestRender(false) })
	}
	screen := make([]string, len(nextLayout.Lines))
	for index, line := range nextLayout.Lines {
		screen[index] = stripLeadingOSC133Zones(line)
	}
	screen = t.applySearchHighlightsLocked(screen, nextLayout)
	if t.HasOverlayEntries() {
		screen = t.CompositeOverlays(screen, width, height)
	}
	if len(screen) > height {
		screen = append([]string(nil), screen[len(screen)-height:]...)
	}
	t.selectionScreen = append([]string(nil), screen...)
	screen = t.applySelectionLocked(screen, nextLayout)
	screen = t.compositeFlashesLocked(screen, width, height)
	cursorPos, hasCursor := t.ExtractCursorPosition(screen, height)
	screen = t.ApplyLineResets(screen)
	for index, line := range screen {
		if !IsImageLine(line) && tuitext.VisibleWidth(line) > width {
			screen[index] = tuitext.SliceByColumn(line, 0, width, true)
		}
	}

	fullRedraw := len(t.previousScreen) == 0 || t.previousScreenWidth != width || t.previousScreenHeight != height
	imagesNeedRedraw := false
	for row, line := range screen {
		previous := ""
		if row < len(t.previousScreen) {
			previous = t.previousScreen[row]
		}
		if line != previous && (IsImageLine(line) || IsImageLine(previous)) {
			imagesNeedRedraw = true
			break
		}
	}
	redrawImages := fullRedraw || imagesNeedRedraw
	hadUploadedKittyImages := len(t.uploadedKittyImages) > 0
	preparedLines := screen
	evictedImageDeletion := ""
	if redrawImages && t.imageProtocol == ImageKitty {
		preparedLines, evictedImageDeletion = t.prepareKittyScreen(screen)
	}

	var buffer strings.Builder
	buffer.WriteString(synchronizedOutputBegin)
	if fullRedraw {
		t.IncrementFullRedraws()
		if t.imageProtocol == ImageKitty && hadUploadedKittyImages {
			buffer.WriteString(DeleteAllKittyPlacements())
		} else {
			buffer.WriteString(t.deleteAltKittyImagesLocked())
		}
		buffer.WriteString("\x1b[2J")
	} else if imagesNeedRedraw {
		if t.imageProtocol == ImageITerm2 {
			buffer.WriteString("\x1b[2J")
		} else if t.imageProtocol == ImageKitty {
			buffer.WriteString(DeleteAllKittyPlacements())
		}
	}
	buffer.WriteString(evictedImageDeletion)
	for row := 0; row < height; row++ {
		line := ""
		if row < len(screen) {
			line = screen[row]
		}
		if !fullRedraw && !imagesNeedRedraw && row < len(t.previousScreen) && line == t.previousScreen[row] {
			continue
		}
		prepared := ""
		if row < len(preparedLines) {
			prepared = preparedLines[row]
		}
		fmt.Fprintf(&buffer, "\x1b[%d;1H\x1b[2K%s", row+1, prepared)
	}
	if hasCursor {
		fmt.Fprintf(&buffer, "\x1b[%d;%dH", cursorPos.Row+1, min(width, cursorPos.Col)+1)
		if t.ShowHardwareCursor() {
			buffer.WriteString("\x1b[?25h")
		} else {
			buffer.WriteString("\x1b[?25l")
		}
	} else {
		buffer.WriteString("\x1b[?25l")
	}
	buffer.WriteString(synchronizedOutputEnd)
	t.Terminal.Write(buffer.String())

	t.previousScreen = screen
	t.previousScreenWidth = width
	t.previousScreenHeight = height
	t.currentLayout = &nextLayout
}
