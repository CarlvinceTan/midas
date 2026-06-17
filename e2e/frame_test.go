package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

// Iframe interaction via FrameLocator and the `>>` chained-selector path.

func TestFrameLocatorReadsAcrossIframe(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/frames/one-frame.html")
	if _, err := page.WaitForSelector(testCtx(t), "iframe", browser.WaitForSelectorOptions{
		State: browser.SelectorStateAttached,
	}); err != nil {
		t.Fatalf("wait for iframe: %v", err)
	}

	fl := page.FrameLocator("iframe")
	var text string
	// The frame loads async; retry the cross-frame read briefly.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		text, _ = fl.Locator("#frame-text").TextContent(testCtx(t))
		if strings.TrimSpace(text) == "leaf frame content" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if strings.TrimSpace(text) != "leaf frame content" {
		t.Errorf("cross-frame TextContent = %q, want 'leaf frame content'", text)
	}
}

func TestFrameLocatorClicksInsideIframe(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/frames/one-frame.html")
	if _, err := page.WaitForSelector(testCtx(t), "iframe", browser.WaitForSelectorOptions{
		State: browser.SelectorStateAttached,
	}); err != nil {
		t.Fatalf("wait for iframe: %v", err)
	}

	fl := page.FrameLocator("iframe")
	// Wait until the frame button is resolvable before acting.
	deadline := time.Now().Add(5 * time.Second)
	var clickErr error
	for time.Now().Before(deadline) {
		clickErr = fl.Locator("#frame-button").Click(testCtx(t))
		if clickErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if clickErr != nil {
		t.Fatalf("click inside iframe: %v", clickErr)
	}
	result, err := fl.Locator("#frame-result").TextContent(testCtx(t))
	if err != nil {
		t.Fatalf("read frame result: %v", err)
	}
	if strings.TrimSpace(result) != "Frame clicked" {
		t.Errorf("frame result = %q, want 'Frame clicked'", result)
	}
}

func TestFrameLocatorFillsInsideIframe(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/frames/one-frame.html")
	if _, err := page.WaitForSelector(testCtx(t), "iframe", browser.WaitForSelectorOptions{
		State: browser.SelectorStateAttached,
	}); err != nil {
		t.Fatalf("wait for iframe: %v", err)
	}

	fl := page.FrameLocator("iframe")
	deadline := time.Now().Add(5 * time.Second)
	var fillErr error
	for time.Now().Before(deadline) {
		fillErr = fl.Locator("#frame-input").Fill(testCtx(t), "typed in frame")
		if fillErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if fillErr != nil {
		t.Fatalf("fill inside iframe: %v", fillErr)
	}
	val, err := fl.Locator("#frame-input").InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("read frame input value: %v", err)
	}
	if val != "typed in frame" {
		t.Errorf("frame input value = %q, want 'typed in frame'", val)
	}
}

func TestPageFramesIncludesChild(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/frames/one-frame.html")

	// Give the child frame time to attach.
	deadline := time.Now().Add(5 * time.Second)
	var childCount int
	for time.Now().Before(deadline) {
		frames := page.Frames()
		childCount = len(frames) - 1 // minus main frame
		if childCount >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if childCount < 1 {
		t.Errorf("expected at least one child frame, page.Frames() returned %d total", childCount+1)
	}

	main := page.MainFrame()
	if main == nil {
		t.Fatal("MainFrame() returned nil")
	}
	if len(main.ChildFrames()) < 1 {
		t.Errorf("main frame reports %d child frames, want >= 1", len(main.ChildFrames()))
	}
}
