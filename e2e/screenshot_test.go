package e2e

import (
	"bytes"
	"testing"

	"github.com/PolymuxOrg/midas/browser"
)

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestPageScreenshotPNG(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	data, err := page.Screenshot(testCtx(t), browser.ScreenshotOptions{})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("screenshot returned no bytes")
	}
	if !bytes.HasPrefix(data, pngMagic) {
		t.Errorf("screenshot is not a PNG (first bytes: % x)", data[:min(8, len(data))])
	}
}

func TestPageScreenshotFullPage(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/scrollable.html")

	full, err := page.Screenshot(testCtx(t), browser.ScreenshotOptions{FullPage: true})
	if err != nil {
		t.Fatalf("full-page screenshot: %v", err)
	}
	if !bytes.HasPrefix(full, pngMagic) {
		t.Error("full-page screenshot is not a PNG")
	}
	// A 3000px-tall page should produce a larger capture than the viewport.
	viewport, err := page.Screenshot(testCtx(t), browser.ScreenshotOptions{})
	if err != nil {
		t.Fatalf("viewport screenshot: %v", err)
	}
	if len(full) <= len(viewport) {
		t.Errorf("full-page (%d bytes) should be larger than viewport (%d bytes)", len(full), len(viewport))
	}
}

func TestLocatorScreenshotClipsToElement(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	data, err := page.Locator("button").Screenshot(testCtx(t), browser.ScreenshotOptions{})
	if err != nil {
		t.Fatalf("element screenshot: %v", err)
	}
	if !bytes.HasPrefix(data, pngMagic) {
		t.Error("element screenshot is not a PNG")
	}
	// An element shot is the button only — must be smaller than the full page.
	page2, err := page.Screenshot(testCtx(t), browser.ScreenshotOptions{FullPage: true})
	if err != nil {
		t.Fatalf("page screenshot: %v", err)
	}
	if len(data) >= len(page2) {
		t.Errorf("element screenshot (%d) should be smaller than full page (%d)", len(data), len(page2))
	}
}

func TestPageScreenshotClipRegion(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	data, err := page.Screenshot(testCtx(t), browser.ScreenshotOptions{
		Clip: &browser.ScreenshotClip{X: 0, Y: 0, Width: 50, Height: 50},
	})
	if err != nil {
		t.Fatalf("clipped screenshot: %v", err)
	}
	if !bytes.HasPrefix(data, pngMagic) {
		t.Error("clipped screenshot is not a PNG")
	}
}
