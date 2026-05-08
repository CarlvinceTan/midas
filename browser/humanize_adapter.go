package browser

import (
	"context"
	"errors"

	"github.com/PolymuxOrg/midas/humanize"
)

// EnableHumanize turns on humanized input for this Page. Once enabled,
// Locator-driven Click, Type, and Hover dispatch via the humanize package:
// Bezier mouse trajectories with wobble and overshoot, per-character keyboard
// timing, and aim/hold cadence on each click. Pass nil to disable.
//
// The humanize cursor state is lazily seeded inside the address-bar region
// on the first humanized action, so callers don't need a "warm up" step.
func (p *Page) EnableHumanize(cfg *humanize.Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cfg == nil {
		p.humanCfg = nil
		p.humanCursorReady = false
		return
	}
	cloned := *cfg
	p.humanCfg = &cloned
	p.humanCursorReady = false
}

// HumanizeEnabled reports whether humanized input is active for this page.
func (p *Page) HumanizeEnabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.humanCfg != nil
}

// HumanizeConfig returns a copy of the active config, or nil if humanize is
// disabled. The copy isolates callers from concurrent EnableHumanize calls.
func (p *Page) HumanizeConfig() *humanize.Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.humanCfg == nil {
		return nil
	}
	cloned := *p.humanCfg
	return &cloned
}

// HumanCursor returns the humanize cursor position and whether it has been
// initialized. Useful for tests and direct humanize.Move calls.
func (p *Page) HumanCursor() (x, y float64, ready bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.humanCursorX, p.humanCursorY, p.humanCursorReady
}

func (p *Page) setHumanCursor(x, y float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.humanCursorX = x
	p.humanCursorY = y
	p.humanCursorReady = true
}

// ensureHumanCursor seeds the cursor at a randomized point inside the
// configured InitialCursorX/Y region on first use. Subsequent calls are no-ops.
func (p *Page) ensureHumanCursor(ctx context.Context, raw humanize.RawMouse, cfg humanize.Config) error {
	if _, _, ok := p.HumanCursor(); ok {
		return nil
	}
	x := humanize.RandRange(cfg.InitialCursorX)
	y := humanize.RandRange(cfg.InitialCursorY)
	if err := raw.Move(ctx, x, y); err != nil {
		return err
	}
	p.setHumanCursor(x, y)
	return nil
}

// HumanizeRawMouse returns a humanize.RawMouse adapter bound to this page.
// Exposed so callers can drive bare humanize.Move/Idle/Scroll directly.
func (p *Page) HumanizeRawMouse() humanize.RawMouse {
	return &pageRawMouse{page: p}
}

// HumanizeRawKeyboard returns a humanize.RawKeyboard adapter bound to this page.
func (p *Page) HumanizeRawKeyboard() humanize.RawKeyboard {
	return &pageRawKeyboard{page: p}
}

// pageRawMouse adapts *Page to humanize.RawMouse via raw CDP Input commands.
// Down/Up/Wheel use the most recently recorded humanize cursor position
// rather than carrying coordinates of their own — humanize.Move is expected
// to have already moved the cursor into place.
type pageRawMouse struct {
	page *Page
}

func (m *pageRawMouse) Move(ctx context.Context, x, y float64) error {
	return m.page.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseMoved",
		"x":      x,
		"y":      y,
		"button": "none",
	}, nil)
}

func (m *pageRawMouse) Down(ctx context.Context) error {
	x, y, _ := m.page.HumanCursor()
	return m.page.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mousePressed",
		"x":          x,
		"y":          y,
		"button":     "left",
		"clickCount": 1,
	}, nil)
}

func (m *pageRawMouse) Up(ctx context.Context) error {
	x, y, _ := m.page.HumanCursor()
	return m.page.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mouseReleased",
		"x":          x,
		"y":          y,
		"button":     "left",
		"clickCount": 1,
	}, nil)
}

func (m *pageRawMouse) Wheel(ctx context.Context, deltaX, deltaY float64) error {
	x, y, _ := m.page.HumanCursor()
	return m.page.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseWheel",
		"x":      x,
		"y":      y,
		"button": "none",
		"deltaX": deltaX,
		"deltaY": deltaY,
	}, nil)
}

// pageRawKeyboard adapts *Page to humanize.RawKeyboard via raw CDP Input
// commands. Reuses namedKeyCDP from page_api.go for non-character keys.
type pageRawKeyboard struct {
	page *Page
}

func (k *pageRawKeyboard) Down(ctx context.Context, key string) error {
	return k.page.mainSession.Send(ctx, "Input.dispatchKeyEvent", k.dispatchPayload("keyDown", key), nil)
}

func (k *pageRawKeyboard) Up(ctx context.Context, key string) error {
	return k.page.mainSession.Send(ctx, "Input.dispatchKeyEvent", k.dispatchPayload("keyUp", key), nil)
}

func (k *pageRawKeyboard) InsertText(ctx context.Context, text string) error {
	return k.page.mainSession.Send(ctx, "Input.insertText", map[string]any{
		"text": text,
	}, nil)
}

func (k *pageRawKeyboard) dispatchPayload(eventType, key string) map[string]any {
	payload := map[string]any{"type": eventType, "key": key}
	if eventType == "keyDown" && len([]rune(key)) == 1 {
		payload["text"] = key
		payload["unmodifiedText"] = key
	}
	if d, ok := namedKeyCDP[key]; ok {
		payload["code"] = d.Code
		payload["windowsVirtualKeyCode"] = d.VK
		payload["nativeVirtualKeyCode"] = d.VK
	}
	return payload
}

var errHumanizeDisabled = errors.New("humanize: not enabled on this page")

// performHumanizedClick is the entry point used by Locator.clickAtGeometry
// when humanize is on. It picks a randomized target inside box, traces a
// Bezier path from the current cursor, and issues an aim → press → hold →
// release sequence at the new position.
func (p *Page) performHumanizedClick(ctx context.Context, box humanize.Box, isInput bool) error {
	cfg := p.HumanizeConfig()
	if cfg == nil {
		return errHumanizeDisabled
	}
	raw := p.HumanizeRawMouse()
	if err := p.ensureHumanCursor(ctx, raw, *cfg); err != nil {
		return err
	}
	target := humanize.ClickTarget(box, isInput, *cfg)
	sx, sy, _ := p.HumanCursor()
	if err := humanize.Move(ctx, raw, sx, sy, target.X, target.Y, *cfg); err != nil {
		return err
	}
	p.setHumanCursor(target.X, target.Y)
	return humanize.Click(ctx, raw, isInput, *cfg)
}

// performHumanizedHover moves the cursor to (x, y) along a Bezier path
// without issuing a press/release.
func (p *Page) performHumanizedHover(ctx context.Context, x, y float64) error {
	cfg := p.HumanizeConfig()
	if cfg == nil {
		return errHumanizeDisabled
	}
	raw := p.HumanizeRawMouse()
	if err := p.ensureHumanCursor(ctx, raw, *cfg); err != nil {
		return err
	}
	sx, sy, _ := p.HumanCursor()
	if err := humanize.Move(ctx, raw, sx, sy, x, y, *cfg); err != nil {
		return err
	}
	p.setHumanCursor(x, y)
	return nil
}

// performHumanizedType issues humanized keystrokes for text. The caller is
// responsible for focusing the target field first (typically by clicking it,
// which Locator.Type does on the humanize path).
func (p *Page) performHumanizedType(ctx context.Context, text string) error {
	cfg := p.HumanizeConfig()
	if cfg == nil {
		return errHumanizeDisabled
	}
	return humanize.Type(ctx, p, p.HumanizeRawKeyboard(), text, *cfg)
}

// HumanizeScroll dispatches a humanized vertical wheel sequence for a target
// delta. Positive scrolls down. Returns the signed total scrolled.
//
// Provided so callers can issue ad-hoc scrolls without going through a
// locator. For element-aware scrolling (scroll-into-zone + recheck loop),
// build on top of this and BoundingBox.
func (p *Page) HumanizeScroll(ctx context.Context, deltaY float64) (float64, error) {
	cfg := p.HumanizeConfig()
	if cfg == nil {
		return 0, errHumanizeDisabled
	}
	raw := p.HumanizeRawMouse()
	if err := p.ensureHumanCursor(ctx, raw, *cfg); err != nil {
		return 0, err
	}
	return humanize.Scroll(ctx, raw, deltaY, *cfg)
}
