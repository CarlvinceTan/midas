package tui

import (
	"regexp"
	"strconv"
	"sync"
	"time"
)

const minimumRenderInterval = 16 * time.Millisecond

// maxPendingBackgroundQueries bounds the OSC 11 reply queue. Entries past it are
// timed-out placeholders that no reply is ever expected for.
const maxPendingBackgroundQueries = 128

// InputListenerResult can consume or transform terminal input. Data nil means
// unchanged; a pointer to an empty string suppresses final delivery.
type InputListenerResult struct {
	Consume bool
	Data    *string
}

// InputListener observes terminal input before the focused component.
type InputListener func(data string) InputListenerResult

// TuiHooks connect the renderer-independent base to a concrete renderer.
type TuiHooks struct {
	Render              func()
	ResetRenderState    func()
	BeforeTerminalStart func()
	AfterTerminalStart  func()
	BeforeTerminalStop  func(StopOptions)
	AfterTerminalStop   func(StopOptions)
	MountedRoots        func() []Component
}

// BaseOptions configure TuiBase.
type BaseOptions struct {
	ShowHardwareCursor bool
	Scheduler          Scheduler
	Hooks              TuiHooks
}

type inputListenerEntry struct {
	id       uint64
	listener InputListener
}

type colorListenerEntry struct {
	id       uint64
	listener func(TerminalColorScheme)
}

// ColorQueryResult is produced by asynchronous terminal color queries.
type ColorQueryResult struct {
	Color RGBColor
	OK    bool
}

// ColorSchemeQueryResult is produced by asynchronous color-scheme queries.
type ColorSchemeQueryResult struct {
	Scheme TerminalColorScheme
	OK     bool
}

type pendingBackgroundQuery struct {
	settled bool
	result  chan ColorQueryResult
	cancel  CancelFunc
}

// TuiBase owns focus, overlays, input routing, lifecycle, and render scheduling.
// Concrete regular/fullscreen renderers provide hooks for actual terminal
// painting.
type TuiBase struct {
	Container

	Mode      TuiMode
	Terminal  Terminal
	hooks     TuiHooks
	scheduler Scheduler

	focusedComponent Component
	inputListeners   []inputListenerEntry
	colorListeners   []colorListenerEntry
	nextListenerID   uint64
	listenerMu       sync.Mutex

	OnDebug func()

	// frameMu serialises component state with rendering. Terminal input arrives on
	// the terminal reader goroutine while frames are painted on scheduler
	// goroutines, so without it an edit and a render read and write the same
	// component fields at once. The concrete screens render under this lock.
	frameMu sync.Mutex

	renderMu                 sync.Mutex
	renderRequested          bool
	immediateRenderScheduled bool
	renderTimer              CancelFunc
	lastRenderAt             time.Duration
	showHardwareCursor       bool
	clearOnShrink            bool
	fullRedrawCount          int
	stopped                  bool

	pendingOSC11Replies int
	pendingOSC11Queries []*pendingBackgroundQuery
	colorNotifications  bool
	queryMu             sync.Mutex

	focusOrderCounter uint64
	overlayStack      []*overlayEntry
	renderedOverlays  []renderedOverlayLayout
	overlayRestore    overlayFocusRestore
}

// NewTuiBase constructs the renderer-independent TUI base.
func NewTuiBase(terminal Terminal, mode TuiMode, options BaseOptions) *TuiBase {
	scheduler := options.Scheduler
	if scheduler == nil {
		scheduler = newLoopScheduler()
	}
	base := &TuiBase{
		Container:          Container{Children: []Component{}},
		Mode:               mode,
		Terminal:           terminal,
		hooks:              options.Hooks,
		scheduler:          scheduler,
		showHardwareCursor: options.ShowHardwareCursor,
		overlayStack:       []*overlayEntry{},
		renderedOverlays:   []renderedOverlayLayout{},
		overlayRestore:     overlayFocusRestore{status: overlayRestoreInactive},
	}
	return base
}

// FullRedraws returns the concrete renderer's full-redraw count.
func (t *TuiBase) FullRedraws() int { return t.fullRedrawCount }

// IncrementFullRedraws records a full renderer repaint.
func (t *TuiBase) IncrementFullRedraws() { t.fullRedrawCount++ }

func (t *TuiBase) isStopped() bool {
	t.renderMu.Lock()
	defer t.renderMu.Unlock()
	return t.stopped
}

// ShowHardwareCursor reports the configured hardware cursor mode.
func (t *TuiBase) ShowHardwareCursor() bool { return t.showHardwareCursor }

// SetShowHardwareCursor changes cursor mode and requests a render.
func (t *TuiBase) SetShowHardwareCursor(enabled bool) {
	if t.showHardwareCursor == enabled {
		return
	}
	t.showHardwareCursor = enabled
	if !enabled {
		t.Terminal.HideCursor()
	}
	t.RequestRender(false)
}

// ClearOnShrink reports whether concrete renderers should clear stale rows.
func (t *TuiBase) ClearOnShrink() bool { return t.clearOnShrink }

// SetClearOnShrink changes stale-row clearing behavior.
func (t *TuiBase) SetClearOnShrink(enabled bool) { t.clearOnShrink = enabled }

// FocusedComponent returns the current keyboard focus owner.
func (t *TuiBase) FocusedComponent() Component { return t.focusedComponent }

// Invalidate invalidates mounted roots and every overlay.
func (t *TuiBase) Invalidate() {
	for _, root := range t.mountedRoots() {
		root.Invalidate()
	}
	for _, overlay := range t.overlayStack {
		overlay.component.Invalidate()
	}
}

func (t *TuiBase) mountedRoots() []Component {
	if t.hooks.MountedRoots != nil {
		return t.hooks.MountedRoots()
	}
	return t.Children
}

// Start initializes terminal input and schedules the first frame.
func (t *TuiBase) Start() {
	t.renderMu.Lock()
	t.stopped = false
	t.renderMu.Unlock()
	if t.hooks.BeforeTerminalStart != nil {
		t.hooks.BeforeTerminalStart()
	}
	// On a single-goroutine scheduler input is posted to the loop rather than
	// handled on the terminal reader, which is what keeps component state
	// single-threaded. Test schedulers deliver synchronously.
	t.Terminal.Start(func(data string) {
		if poster, ok := t.scheduler.(inputPoster); ok {
			poster.PostInput(func() { t.handleTerminalInput(data) })
			return
		}
		t.handleTerminalInput(data)
	}, func() { t.RequestRender(false) })
	SetKittyProtocolActive(t.Terminal.KittyProtocolActive())
	if t.hooks.AfterTerminalStart != nil {
		t.hooks.AfterTerminalStart()
	}
	t.Terminal.HideCursor()
	if t.colorNotifications {
		t.Terminal.Write("\x1b[?2031h")
	}
	t.queryCellSize()
	t.RequestRender(false)
}

// Stop restores terminal state.
func (t *TuiBase) Stop(options StopOptions) {
	t.renderMu.Lock()
	t.stopped = true
	t.cancelRenderTimerLocked()
	t.renderMu.Unlock()
	// Queued input and renders are dropped and the loop is idle before the final
	// frame is written, so the teardown cannot race a pending callback.
	stopLoop(t.scheduler)
	if t.colorNotifications {
		t.Terminal.Write("\x1b[?2031l")
	}
	if t.hooks.BeforeTerminalStop != nil {
		t.hooks.BeforeTerminalStop(options)
	}
	t.Terminal.ShowCursor()
	t.Terminal.Stop()
	if t.hooks.AfterTerminalStop != nil {
		t.hooks.AfterTerminalStop(options)
	}
}

// RenderNow performs a synchronous render, optionally resetting renderer state.
func (t *TuiBase) RenderNow(force bool) {
	if force && t.hooks.ResetRenderState != nil {
		t.hooks.ResetRenderState()
	}
	t.renderMu.Lock()
	t.renderRequested = false
	t.cancelRenderTimerLocked()
	t.lastRenderAt = t.scheduler.Now()
	t.renderMu.Unlock()
	t.doRender()
}

// RequestRender coalesces ordinary renders and allows forced/input frames to
// preempt the 16 ms throttle.
func (t *TuiBase) RequestRender(force bool) {
	if force {
		if t.hooks.ResetRenderState != nil {
			t.hooks.ResetRenderState()
		}
		t.requestImmediateRender()
		return
	}
	t.renderMu.Lock()
	if t.renderRequested {
		t.renderMu.Unlock()
		return
	}
	t.renderRequested = true
	t.renderMu.Unlock()
	t.scheduler.Defer(t.scheduleRender)
}

func (t *TuiBase) requestImmediateRender() {
	t.renderMu.Lock()
	t.cancelRenderTimerLocked()
	t.renderRequested = true
	if t.immediateRenderScheduled {
		t.renderMu.Unlock()
		return
	}
	t.immediateRenderScheduled = true
	t.renderMu.Unlock()
	t.scheduler.Defer(func() {
		t.renderMu.Lock()
		t.immediateRenderScheduled = false
		if t.stopped || !t.renderRequested {
			t.renderMu.Unlock()
			return
		}
		t.cancelRenderTimerLocked()
		t.renderRequested = false
		t.lastRenderAt = t.scheduler.Now()
		t.renderMu.Unlock()
		t.doRender()
	})
}

func (t *TuiBase) cancelRenderTimerLocked() {
	if t.renderTimer == nil {
		return
	}
	t.renderTimer()
	t.renderTimer = nil
}

func (t *TuiBase) scheduleRender() {
	t.renderMu.Lock()
	if t.stopped || t.renderTimer != nil || !t.renderRequested {
		t.renderMu.Unlock()
		return
	}
	delay := max(time.Duration(0), minimumRenderInterval-(t.scheduler.Now()-t.lastRenderAt))
	t.renderTimer = t.scheduler.AfterFunc(delay, func() {
		t.renderMu.Lock()
		t.renderTimer = nil
		if t.stopped || !t.renderRequested {
			t.renderMu.Unlock()
			return
		}
		t.renderRequested = false
		t.lastRenderAt = t.scheduler.Now()
		t.renderMu.Unlock()
		t.doRender()

		t.renderMu.Lock()
		requested := t.renderRequested
		t.renderMu.Unlock()
		if requested {
			t.scheduleRender()
		}
	})
	t.renderMu.Unlock()
}

func (t *TuiBase) doRender() {
	if t.hooks.Render != nil {
		t.hooks.Render()
	}
}

// AddInputListener adds an insertion-ordered input transform and returns an
// idempotent unsubscribe function.
func (t *TuiBase) AddInputListener(listener InputListener) func() {
	t.listenerMu.Lock()
	t.nextListenerID++
	entry := inputListenerEntry{id: t.nextListenerID, listener: listener}
	t.inputListeners = append(t.inputListeners, entry)
	t.listenerMu.Unlock()
	return func() {
		t.listenerMu.Lock()
		defer t.listenerMu.Unlock()
		for index, candidate := range t.inputListeners {
			if candidate.id == entry.id {
				t.inputListeners = append(t.inputListeners[:index], t.inputListeners[index+1:]...)
				return
			}
		}
	}
}

// OnTerminalColorSchemeChange subscribes to complete DSR color reports.
func (t *TuiBase) OnTerminalColorSchemeChange(listener func(TerminalColorScheme)) func() {
	t.listenerMu.Lock()
	t.nextListenerID++
	entry := colorListenerEntry{id: t.nextListenerID, listener: listener}
	t.colorListeners = append(t.colorListeners, entry)
	t.listenerMu.Unlock()
	return func() {
		t.listenerMu.Lock()
		defer t.listenerMu.Unlock()
		for index, candidate := range t.colorListeners {
			if candidate.id == entry.id {
				t.colorListeners = append(t.colorListeners[:index], t.colorListeners[index+1:]...)
				return
			}
		}
	}
}

// SetTerminalColorSchemeNotifications toggles terminal palette notifications.
func (t *TuiBase) SetTerminalColorSchemeNotifications(enabled bool) {
	if t.colorNotifications == enabled {
		return
	}
	t.colorNotifications = enabled
	t.renderMu.Lock()
	stopped := t.stopped
	t.renderMu.Unlock()
	if !stopped {
		if enabled {
			t.Terminal.Write("\x1b[?2031h")
		} else {
			t.Terminal.Write("\x1b[?2031l")
		}
	}
}

func (t *TuiBase) queryCellSize() {
	if GetCapabilities().Images == ImageNone {
		return
	}
	t.Terminal.Write("\x1b[16t")
}

var cellSizeResponsePattern = regexp.MustCompile(`^\x1b\[6;(\d+);(\d+)t$`)

func (t *TuiBase) handleTerminalInput(data string) {
	SetKittyProtocolActive(t.Terminal.KittyProtocolActive())
	if t.consumeOSC11BackgroundResponse(data) || t.consumeTerminalColorSchemeReport(data) {
		return
	}

	current := data
	t.listenerMu.Lock()
	listeners := append([]inputListenerEntry(nil), t.inputListeners...)
	t.listenerMu.Unlock()
	for _, entry := range listeners {
		result := entry.listener(current)
		if result.Consume {
			return
		}
		if result.Data != nil {
			current = *result.Data
		}
	}
	if current == "" {
		return
	}
	data = current

	if t.consumeCellSizeResponse(data) {
		return
	}
	if MatchesDebugKey(data) && t.OnDebug != nil {
		t.OnDebug()
		return
	}

	t.repairOverlayFocusForInput()
	if handler, ok := t.focusedComponent.(InputHandler); ok {
		if IsKeyRelease(data) {
			requester, wants := t.focusedComponent.(KeyReleaseRequester)
			if !wants || !requester.WantsKeyRelease() {
				return
			}
		}
		// Frames and input run on the same goroutine because input is posted to the
		// scheduler loop, so no lock is needed here: component state stays
		// single-threaded.
		handler.HandleInput(data)
		t.requestImmediateRender()
	}
}

func (t *TuiBase) consumeOSC11BackgroundResponse(data string) bool {
	if !IsOSC11BackgroundColorResponse(data) {
		return false
	}
	color, ok := ParseOSC11BackgroundColor(data)
	t.queryMu.Lock()
	if t.pendingOSC11Replies <= 0 {
		t.queryMu.Unlock()
		return false
	}
	t.pendingOSC11Replies--
	var query *pendingBackgroundQuery
	if len(t.pendingOSC11Queries) > 0 {
		query = t.pendingOSC11Queries[0]
		t.pendingOSC11Queries = t.pendingOSC11Queries[1:]
	}
	if query != nil && !query.settled {
		query.settled = true
		cancel := query.cancel
		query.cancel = nil
		t.queryMu.Unlock()
		if cancel != nil {
			cancel()
		}
		query.result <- ColorQueryResult{Color: color, OK: ok}
		close(query.result)
		return true
	}
	t.queryMu.Unlock()
	return true
}

// prunePendingOSC11QueriesLocked drops the oldest settled placeholders while the
// reply queue is over its bound. The caller holds queryMu.
func (t *TuiBase) prunePendingOSC11QueriesLocked() {
	for len(t.pendingOSC11Queries) > maxPendingBackgroundQueries {
		if !t.pendingOSC11Queries[0].settled {
			return
		}
		t.pendingOSC11Queries = t.pendingOSC11Queries[1:]
		// A dropped placeholder can no longer consume the reply it was waiting for,
		// so the outstanding count falls with it.
		t.pendingOSC11Replies--
	}
}

func (t *TuiBase) consumeTerminalColorSchemeReport(data string) bool {
	scheme, ok := ParseTerminalColorSchemeReport(data)
	if !ok {
		return false
	}
	t.listenerMu.Lock()
	listeners := append([]colorListenerEntry(nil), t.colorListeners...)
	t.listenerMu.Unlock()
	for _, entry := range listeners {
		entry.listener(scheme)
	}
	return true
}

func (t *TuiBase) consumeCellSizeResponse(data string) bool {
	match := cellSizeResponsePattern.FindStringSubmatch(data)
	if len(match) != 3 {
		return false
	}
	height, heightErr := strconv.Atoi(match[1])
	width, widthErr := strconv.Atoi(match[2])
	if heightErr != nil || widthErr != nil || height <= 0 || width <= 0 {
		return true
	}
	SetCellDimensions(CellDimensions{WidthPx: width, HeightPx: height})
	t.Invalidate()
	t.RequestRender(false)
	return true
}

// QueryTerminalBackgroundColor sends OSC 11 and resolves on reply or timeout.
func (t *TuiBase) QueryTerminalBackgroundColor(timeout time.Duration) <-chan ColorQueryResult {
	result := make(chan ColorQueryResult, 1)
	query := &pendingBackgroundQuery{result: result}
	t.queryMu.Lock()
	t.pendingOSC11Queries = append(t.pendingOSC11Queries, query)
	t.pendingOSC11Replies++
	t.queryMu.Unlock()
	t.Terminal.Write("\x1b]11;?\x07")
	cancel := t.scheduler.AfterFunc(timeout, func() {
		t.queryMu.Lock()
		if query.settled {
			t.queryMu.Unlock()
			return
		}
		query.settled = true
		query.cancel = nil
		// The settled query stays in the queue as a placeholder: a reply carries no
		// ID, so its position is what matches a late answer to the query that asked
		// for it. Placeholders are pruned so a terminal that never answers cannot
		// grow the queue without bound.
		t.prunePendingOSC11QueriesLocked()
		t.queryMu.Unlock()
		result <- ColorQueryResult{}
		close(result)
	})
	t.queryMu.Lock()
	if query.settled {
		t.queryMu.Unlock()
		cancel()
	} else {
		query.cancel = cancel
		t.queryMu.Unlock()
	}
	return result
}

// QueryTerminalColorScheme sends the DSR query and resolves on report or timeout.
func (t *TuiBase) QueryTerminalColorScheme(timeout time.Duration) <-chan ColorSchemeQueryResult {
	result := make(chan ColorSchemeQueryResult, 1)
	settled := false
	var settleMu sync.Mutex
	var cancelTimer CancelFunc
	var unsubscribe func()
	settle := func(scheme TerminalColorScheme, ok bool) {
		settleMu.Lock()
		if settled {
			settleMu.Unlock()
			return
		}
		settled = true
		cancel := cancelTimer
		cancelTimer = nil
		removeListener := unsubscribe
		settleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if removeListener != nil {
			removeListener()
		}
		result <- ColorSchemeQueryResult{Scheme: scheme, OK: ok}
		close(result)
	}
	unsubscribe = t.OnTerminalColorSchemeChange(func(scheme TerminalColorScheme) { settle(scheme, true) })
	t.Terminal.Write("\x1b[?996n")
	cancel := t.scheduler.AfterFunc(timeout, func() { settle("", false) })
	settleMu.Lock()
	if settled {
		settleMu.Unlock()
		cancel()
	} else {
		cancelTimer = cancel
		settleMu.Unlock()
	}
	return result
}
