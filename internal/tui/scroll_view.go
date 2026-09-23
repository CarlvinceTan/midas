package tui

import (
	"strings"
	"sync"
	"time"
)

// ScrollbarMode controls vertical scrollbar visibility.
type ScrollbarMode string

const (
	ScrollbarHidden ScrollbarMode = "hidden"
	ScrollbarAuto   ScrollbarMode = "auto"
	ScrollbarAlways ScrollbarMode = "always"
)

// OverscrollBehavior controls whether unconsumed wheel movement may chain.
type OverscrollBehavior string

const (
	OverscrollChain   OverscrollBehavior = "chain"
	OverscrollContain OverscrollBehavior = "contain"
)

// ScrollViewOptions configure a vertical ScrollView.
type ScrollViewOptions struct {
	FollowEnd           bool
	Primary             bool
	Overscroll          OverscrollBehavior
	Scrollbar           ScrollbarMode
	ScrollbarTrackStyle func(string) string
	ScrollbarThumbStyle func(string) string
	// ScrollbarHideDelay distinguishes an omitted value (one second) from an
	// explicit pointer to zero (hide on the next timer turn).
	ScrollbarHideDelay *time.Duration
}

// ScrollToOptions alter follow-end behavior for one explicit scroll.
type ScrollToOptions struct {
	DisableFollow bool
}

type scrollTimer interface {
	Stop() bool
}

// ScrollView owns exactly one child and exposes a vertically clipped viewport
// to the layout engine.
type ScrollView struct {
	Container

	child Component

	followEnd  bool
	primary    bool
	overscroll OverscrollBehavior

	ScrollbarTrackStyle func(string) string
	ScrollbarThumbStyle func(string) string

	currentScrollbar   ScrollbarMode
	scrollbarHideDelay time.Duration
	currentScrollTop   int
	contentHeight      int
	viewportHeight     int
	followingEnd       bool
	followSuppressed   bool
	requestRender      func()
	transientVisible   bool
	scrollbarActive    bool
	scrollbarHideTimer scrollTimer
	afterFunc          func(time.Duration, func()) scrollTimer
	scrollbarMu        sync.Mutex
	timerGeneration    uint64
}

// NewScrollView constructs a vertical ScrollView with exactly one child.
func NewScrollView(component Component, options ScrollViewOptions) *ScrollView {
	overscroll := options.Overscroll
	if overscroll == "" {
		overscroll = OverscrollChain
	}
	scrollbar := options.Scrollbar
	if scrollbar == "" {
		scrollbar = ScrollbarHidden
	}
	trackStyle := options.ScrollbarTrackStyle
	if trackStyle == nil {
		trackStyle = func(value string) string { return "\x1b[90m" + value + "\x1b[39m" }
	}
	thumbStyle := options.ScrollbarThumbStyle
	if thumbStyle == nil {
		thumbStyle = func(value string) string { return "\x1b[37m" + value + "\x1b[39m" }
	}
	delay := time.Second
	if options.ScrollbarHideDelay != nil {
		delay = *options.ScrollbarHideDelay
	}
	if delay < 0 {
		delay = 0
	}

	scroll := &ScrollView{
		Container:           Container{Children: []Component{component}},
		child:               component,
		followEnd:           options.FollowEnd,
		primary:             options.Primary,
		overscroll:          overscroll,
		ScrollbarTrackStyle: trackStyle,
		ScrollbarThumbStyle: thumbStyle,
		currentScrollbar:    scrollbar,
		scrollbarHideDelay:  delay,
		followingEnd:        options.FollowEnd,
	}
	scroll.afterFunc = func(delay time.Duration, callback func()) scrollTimer {
		return time.AfterFunc(delay, callback)
	}
	return scroll
}

// ScrollTop returns the current top content row.
func (s *ScrollView) ScrollTop() int { return s.currentScrollTop }

// IsFollowingEnd reports whether content growth keeps the viewport at the end.
func (s *ScrollView) IsFollowingEnd() bool { return s.followingEnd }

// ViewportHeight returns the most recently laid-out viewport height.
func (s *ScrollView) ViewportHeight() int { return s.viewportHeight }

// IsPrimary reports whether this ScrollView explicitly owns primary scrolling.
func (s *ScrollView) IsPrimary() bool { return s.primary }

// Overscroll returns the configured overscroll behavior.
func (s *ScrollView) Overscroll() OverscrollBehavior { return s.overscroll }

// Scrollbar returns the configured scrollbar mode.
func (s *ScrollView) Scrollbar() ScrollbarMode { return s.currentScrollbar }

// IsScrollbarVisible reports whether the scrollbar should currently be painted.
func (s *ScrollView) IsScrollbarVisible() bool {
	s.scrollbarMu.Lock()
	defer s.scrollbarMu.Unlock()
	if s.currentScrollbar == ScrollbarAlways {
		return s.viewportHeight > 0
	}
	return s.currentScrollbar == ScrollbarAuto &&
		s.contentHeight > s.viewportHeight && s.transientVisible
}

// IsScrollbarActive controls the active thumb glyph.
func (s *ScrollView) IsScrollbarActive() bool { return s.scrollbarActive }

// SetScrollbar changes visibility mode and requests a render.
func (s *ScrollView) SetScrollbar(scrollbar ScrollbarMode) {
	if scrollbar == s.currentScrollbar {
		return
	}
	s.currentScrollbar = scrollbar
	if scrollbar != ScrollbarAuto {
		s.hideTransientScrollbar()
	} else if s.scrollbarActive {
		s.markScrollbarActivity()
	}
	s.requestRenderNow()
}

// ContentWidth reserves a column only for an always-visible scrollbar.
func (s *ScrollView) ContentWidth(width int) int {
	if s.currentScrollbar == ScrollbarAlways && width > 1 {
		return width - 1
	}
	return width
}

func (s *ScrollView) markScrollbarActivity() {
	s.scrollbarMu.Lock()
	if s.currentScrollbar != ScrollbarAuto || s.contentHeight <= s.viewportHeight {
		s.scrollbarMu.Unlock()
		return
	}
	s.transientVisible = true
	s.timerGeneration++
	generation := s.timerGeneration
	if s.scrollbarHideTimer != nil {
		s.scrollbarHideTimer.Stop()
		s.scrollbarHideTimer = nil
	}
	if s.scrollbarActive {
		s.scrollbarMu.Unlock()
		return
	}
	s.scrollbarHideTimer = s.afterFunc(s.scrollbarHideDelay, func() {
		s.scrollbarMu.Lock()
		if generation != s.timerGeneration {
			s.scrollbarMu.Unlock()
			return
		}
		s.scrollbarHideTimer = nil
		s.transientVisible = false
		requestRender := s.requestRender
		s.scrollbarMu.Unlock()
		if requestRender != nil {
			requestRender()
		}
	})
	s.scrollbarMu.Unlock()
}

func (s *ScrollView) hideTransientScrollbar() {
	s.scrollbarMu.Lock()
	defer s.scrollbarMu.Unlock()
	s.transientVisible = false
	s.timerGeneration++
	if s.scrollbarHideTimer != nil {
		s.scrollbarHideTimer.Stop()
		s.scrollbarHideTimer = nil
	}
}

// SetScrollbarActive marks the scrollbar as directly manipulated.
func (s *ScrollView) SetScrollbarActive(active bool) {
	if active == s.scrollbarActive {
		return
	}
	s.scrollbarActive = active
	s.markScrollbarActivity()
	s.requestRenderNow()
}

// ScrollTo moves to an absolute content row.
func (s *ScrollView) ScrollTo(scrollTop int, options ...ScrollToOptions) {
	var opts ScrollToOptions
	if len(options) > 0 {
		opts = options[0]
	}
	maxScrollTop := max(0, s.contentHeight-s.viewportHeight)
	next := max(0, min(maxScrollTop, scrollTop))
	nextSuppressed := opts.DisableFollow && next == maxScrollTop
	nextFollowing := !nextSuppressed && s.followEnd && next == maxScrollTop
	if next == s.currentScrollTop && nextFollowing == s.followingEnd && nextSuppressed == s.followSuppressed {
		return
	}
	moved := next != s.currentScrollTop
	s.currentScrollTop = next
	s.followingEnd = nextFollowing
	s.followSuppressed = nextSuppressed
	if moved {
		s.markScrollbarActivity()
	}
	s.requestRenderNow()
}

// ScrollBy moves by logical lines and returns the unconsumed delta.
func (s *ScrollView) ScrollBy(lines int) int {
	if lines == 0 {
		return 0
	}
	maxScrollTop := max(0, s.contentHeight-s.viewportHeight)
	start := s.currentScrollTop
	if s.followingEnd {
		start = maxScrollTop
	}
	next := max(0, min(maxScrollTop, start+lines))
	moved := next - start
	wasFollowing := s.followingEnd
	s.currentScrollTop = next
	s.followingEnd = s.followEnd && next == maxScrollTop
	s.followSuppressed = false
	if moved != 0 {
		s.markScrollbarActivity()
	}
	if moved != 0 || s.followingEnd != wasFollowing {
		s.requestRenderNow()
	}
	return lines - moved
}

// ScrollToStart moves to the first content row.
func (s *ScrollView) ScrollToStart() {
	changed := s.currentScrollTop != 0 || s.followingEnd != (s.followEnd && s.contentHeight <= s.viewportHeight)
	s.currentScrollTop = 0
	s.followingEnd = s.followEnd && s.contentHeight <= s.viewportHeight
	s.followSuppressed = false
	if changed {
		s.markScrollbarActivity()
		s.requestRenderNow()
	}
}

// ScrollToEnd moves to the last viewport and restores follow-end.
func (s *ScrollView) ScrollToEnd() {
	next := max(0, s.contentHeight-s.viewportHeight)
	changed := s.currentScrollTop != next || s.followingEnd != s.followEnd
	s.currentScrollTop = next
	s.followingEnd = s.followEnd
	s.followSuppressed = false
	if changed {
		s.markScrollbarActivity()
		s.requestRenderNow()
	}
}

// UpdateLayout records content and viewport dimensions and clamps scroll state.
func (s *ScrollView) UpdateLayout(contentHeight, viewportHeight int, requestRender func()) {
	previousContent, previousViewport := s.contentHeight, s.viewportHeight
	s.contentHeight = max(0, contentHeight)
	s.viewportHeight = max(0, viewportHeight)
	s.scrollbarMu.Lock()
	s.requestRender = requestRender
	s.scrollbarMu.Unlock()
	maxScrollTop := max(0, s.contentHeight-s.viewportHeight)
	switch {
	case s.followingEnd && s.viewportHeight < previousViewport && s.contentHeight <= previousContent:
		// The viewport shrank without new content, which is what opening the slash
		// menu or another dock panel does. Keep the window where it is instead of
		// pulling the transcript up, so what the user is reading does not move.
		// Following resumes when the viewport grows back, or as soon as new content
		// arrives.
		s.currentScrollTop = max(0, min(s.currentScrollTop, maxScrollTop))
	case s.followingEnd:
		s.currentScrollTop = maxScrollTop
	default:
		s.currentScrollTop = max(0, min(s.currentScrollTop, maxScrollTop))
	}
	if s.currentScrollTop < maxScrollTop {
		s.followSuppressed = false
	}
	if s.followEnd && s.currentScrollTop == maxScrollTop && !s.followSuppressed {
		s.followingEnd = true
	}
	if s.contentHeight <= s.viewportHeight {
		s.hideTransientScrollbar()
	}
}

func (s *ScrollView) requestRenderNow() {
	s.scrollbarMu.Lock()
	requestRender := s.requestRender
	s.scrollbarMu.Unlock()
	if requestRender != nil {
		requestRender()
	}
}

// AddChild panics because a ScrollView has exactly one immutable child.
func (s *ScrollView) AddChild(Component) { panic("ScrollView has exactly one child") }

// RemoveChild panics because a ScrollView's child is immutable.
func (s *ScrollView) RemoveChild(Component) { panic("ScrollView child cannot be removed") }

// Clear panics because a ScrollView's child is immutable.
func (s *ScrollView) Clear() { panic("ScrollView child cannot be cleared") }

// Render delegates to the child, reserving an always-visible scrollbar column.
func (s *ScrollView) Render(width int) []string {
	contentWidth := s.ContentWidth(width)
	lines := s.child.Render(contentWidth)
	if contentWidth == width {
		return lines
	}
	result := make([]string, len(lines))
	for index, line := range lines {
		result[index] = line + strings.Repeat(" ", width-contentWidth)
	}
	return result
}

// LayoutNode exposes scroll state to the layout engine.
func (s *ScrollView) LayoutNode() LayoutNode {
	return LayoutNode{Type: LayoutScroll, Component: s.child, Scroll: s}
}
