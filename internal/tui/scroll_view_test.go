package tui

import "testing"

// TestFollowEndKeepsItsWindowWhenTheViewportShrinks pins the transcript's
// behavior when the dock grows: opening the slash menu must not scroll the
// transcript, while new content still follows the end.
func TestFollowEndKeepsItsWindowWhenTheViewportShrinks(t *testing.T) {
	view := NewScrollView(&mutableAltComponent{lines: []string{"content"}}, ScrollViewOptions{FollowEnd: true})
	render := func() {}

	// Content taller than the viewport: following the end scrolls to the last page.
	view.UpdateLayout(30, 20, render)
	if view.currentScrollTop != 10 || !view.followingEnd {
		t.Fatalf("initial layout = %d, following=%v", view.currentScrollTop, view.followingEnd)
	}

	// The menu opens: the viewport shrinks and no content arrived. The window stays
	// put instead of jumping to the end.
	view.UpdateLayout(30, 14, render)
	if view.currentScrollTop != 10 {
		t.Fatalf("shrink scrolled the transcript to %d, want 10", view.currentScrollTop)
	}
	if !view.followingEnd {
		t.Fatal("following the end was dropped by a shrink")
	}

	// New content while the menu is still open keeps following the end.
	view.UpdateLayout(34, 14, render)
	if view.currentScrollTop != 20 {
		t.Fatalf("new content did not follow the end: %d, want 20", view.currentScrollTop)
	}

	// The menu closes: the viewport grows back and following returns to the end.
	view.UpdateLayout(34, 20, render)
	if view.currentScrollTop != 14 {
		t.Fatalf("growing the viewport did not resume following: %d, want 14", view.currentScrollTop)
	}
}

// TestScrolledUpTranscriptStaysPutWhenTheViewportShrinks covers the other half:
// a reader who scrolled away keeps their position.
func TestScrolledUpTranscriptStaysPutWhenTheViewportShrinks(t *testing.T) {
	view := NewScrollView(&mutableAltComponent{lines: []string{"content"}}, ScrollViewOptions{FollowEnd: true})
	render := func() {}
	view.UpdateLayout(30, 20, render)
	view.ScrollBy(-5)
	if view.currentScrollTop != 5 || view.followingEnd {
		t.Fatalf("after scrolling up = %d, following=%v", view.currentScrollTop, view.followingEnd)
	}
	view.UpdateLayout(30, 14, render)
	if view.currentScrollTop != 5 {
		t.Fatalf("shrink moved a scrolled-up transcript to %d, want 5", view.currentScrollTop)
	}
	// Clamping still applies when the content no longer reaches the position.
	view.UpdateLayout(6, 2, render)
	if view.currentScrollTop != 4 {
		t.Fatalf("clamp after content shrank = %d, want 4", view.currentScrollTop)
	}
}
