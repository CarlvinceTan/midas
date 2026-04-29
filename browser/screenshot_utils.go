package browser

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"
)

type screenshotCleanup func()

func collectFramesForScreenshot(page *Page) []*Frame {
	seen := make(map[string]bool)
	var frames []*Frame
	main := page.MainFrame()
	if !seen[main.ID()] {
		seen[main.ID()] = true
		frames = append(frames, main)
	}
	for _, f := range page.Frames() {
		if !seen[f.ID()] {
			seen[f.ID()] = true
			frames = append(frames, f)
		}
	}
	return frames
}

func normalizeScreenshotClip(clip *ScreenshotClip) (*ScreenshotClip, error) {
	if clip == nil {
		return nil, nil
	}
	out := *clip
	for _, value := range []float64{out.X, out.Y, out.Width, out.Height} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("screenshot clip values must be finite")
		}
	}
	if out.Width <= 0 || out.Height <= 0 {
		return nil, fmt.Errorf("screenshot clip width/height must be positive")
	}
	if out.Scale <= 0 {
		out.Scale = 1
	}
	return &out, nil
}

func computeScreenshotScale(ctx context.Context, page *Page, mode ScreenshotScaleMode) float64 {
	if mode != ScreenshotScaleCSS {
		return 0
	}
	var dpr float64
	if err := page.Evaluate(ctx, `(() => {
		const r = Number(window.devicePixelRatio) || 1;
		return Number.isFinite(r) && r > 0 ? r : 1;
	})()`, &dpr); err != nil {
		return 1
	}
	if math.IsNaN(dpr) || math.IsInf(dpr, 0) || dpr <= 0 {
		return 1
	}
	scale := 1 / dpr
	if scale > 2 {
		scale = 2
	}
	if scale < 0.1 {
		scale = 0.1
	}
	return scale
}

func applyStyleToFrames(ctx context.Context, frames []*Frame, css, label string) screenshotCleanup {
	trimmed := css
	if trimmed == "" {
		return func() {}
	}
	token := fmt.Sprintf("__polymux_style_%s_%d_%s", label, time.Now().UnixNano(), randomString(8))
	for _, frame := range frames {
		err := frame.Evaluate(ctx, fmt.Sprintf(`(() => {
			try {
				const doc = document;
				if (!doc) return;
				const style = doc.createElement('style');
				style.setAttribute('data-polymux-style', %q);
				style.textContent = %q;
				const parent = doc.head || doc.documentElement || doc.body;
				if (parent) parent.appendChild(style);
			} catch {}
		})()`, token, trimmed), nil)
		if err != nil {
			log.Printf("screenshot style apply warning: %v", err)
		}
	}
	return func() {
		for _, frame := range frames {
			_ = frame.Evaluate(context.Background(), fmt.Sprintf(`(() => {
				try {
					const doc = document;
					if (!doc) return;
					const nodes = doc.querySelectorAll('[data-polymux-style="%s"]');
					nodes.forEach(node => node.remove());
				} catch {}
			})()`, token), nil)
		}
	}
}

func disableAnimationsForFrames(ctx context.Context, frames []*Frame) screenshotCleanup {
	cleanup := applyStyleToFrames(ctx, frames, `
*,*::before,*::after {
  animation-delay: 0s !important;
  animation-duration: 0s !important;
  animation-iteration-count: 1 !important;
  animation-play-state: paused !important;
  transition-property: none !important;
  transition-duration: 0s !important;
  transition-delay: 0s !important;
}`, "animations")
	for _, frame := range frames {
		_ = frame.Evaluate(ctx, `(() => {
			if (typeof document.getAnimations !== 'function') return true;
			for (const animation of document.getAnimations()) {
				try {
					const timing = animation.effect && animation.effect.getComputedTiming ? animation.effect.getComputedTiming() : null;
					if (timing && timing.iterations !== Infinity && animation.finish) {
						animation.finish();
					} else if (animation.cancel) {
						animation.cancel();
					}
				} catch {
					if (animation.cancel) animation.cancel();
				}
			}
			return true;
		})()`, nil)
	}
	return cleanup
}

func hideCaretForFrames(ctx context.Context, frames []*Frame) screenshotCleanup {
	return applyStyleToFrames(ctx, frames, `
input,textarea,[contenteditable],[contenteditable=""],[contenteditable="true"],[contenteditable="plaintext-only"],*:focus {
  caret-color: transparent !important;
}`, "caret")
}

func setTransparentBackground(ctx context.Context, session sessionLike) screenshotCleanup {
	_ = session.Send(ctx, "Emulation.setDefaultBackgroundColorOverride", map[string]any{
		"color": map[string]any{"r": 0, "g": 0, "b": 0, "a": 0},
	}, nil)
	return func() {
		_ = session.Send(context.Background(), "Emulation.setDefaultBackgroundColorOverride", map[string]any{}, nil)
	}
}

func runScreenshotCleanups(cleanups []screenshotCleanup) {
	for i := len(cleanups) - 1; i >= 0; i-- {
		if cleanups[i] != nil {
			cleanups[i]()
		}
	}
}
