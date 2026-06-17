package browser

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ActionabilityOptions controls the wait-until-actionable preflight that
// Locator runs before mouse interactions. Defaults match Playwright's
// behavior closely: 30s timeout, 80ms stability window, both occlusion and
// stability checks on. Set Disabled to skip the preflight entirely (useful
// when callers know the element is ready and want to avoid the ~80ms
// stability sleep).
type ActionabilityOptions struct {
	// Timeout is the total budget for the actionability loop. The loop polls
	// at PollInterval until a check passes or the deadline elapses.
	Timeout time.Duration
	// PollInterval is the sleep between failed actionability iterations.
	// Lower = faster recovery, more CDP traffic.
	PollInterval time.Duration
	// StabilityWindow is how long the bounding box must stay still for the
	// element to count as stable. A non-zero value adds this much latency
	// to every actionable click.
	StabilityWindow time.Duration
	// SkipStability turns off the layout-stability check.
	SkipStability bool
	// SkipOcclusion turns off the document.elementFromPoint occlusion check.
	SkipOcclusion bool
	// RequireEnabled makes the loop wait until the element is enabled (not
	// `disabled` and not inside a disabled fieldset). Set by click/dblclick;
	// matches Playwright's actionability matrix.
	RequireEnabled bool
	// RequireEditable makes the loop wait until the element is editable (a
	// non-readonly, non-disabled input/textarea, or a contenteditable). Set by
	// fill. Editable implies enabled, so callers need not set both.
	RequireEditable bool
	// Disabled bypasses the entire actionability flow — only the existing
	// resolve + IsVisible check runs.
	Disabled bool
}

// DefaultActionabilityOptions returns the recommended profile. Used when a
// caller passes an empty/zero ActionabilityOptions.
func DefaultActionabilityOptions() ActionabilityOptions {
	return ActionabilityOptions{
		Timeout:         30 * time.Second,
		PollInterval:    50 * time.Millisecond,
		StabilityWindow: 80 * time.Millisecond,
	}
}

// awaitActionable resolves the locator's element and waits until it passes
// actionability checks: visible, geometry > 0, layout-stable across
// StabilityWindow, and not covered at the centroid by an unrelated element.
//
// On success, returns a freshly-resolved element + its release function. The
// caller MUST call release. On timeout, returns the most recent failure
// observed during polling, wrapped with the selector for context.
func (l *Locator) awaitActionable(ctx context.Context, opts ActionabilityOptions) (*resolvedElement, func(), error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 50 * time.Millisecond
	}
	if opts.StabilityWindow <= 0 {
		opts.StabilityWindow = 80 * time.Millisecond
	}

	deadline := time.Now().Add(opts.Timeout)
	var lastErr error

	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		elem, release, err := l.resolveElement(ctx)
		switch {
		case err != nil:
			lastErr = err
		case !elem.IsVisible():
			release()
			lastErr = notVisibleError(l.selector, l.index, l.frame.frameID)
		case opts.RequireEditable && !elem.IsEditable():
			release()
			lastErr = notEditableError(l.selector, l.index, l.frame.frameID)
		case opts.RequireEnabled && !elem.IsEnabled():
			release()
			lastErr = notEnabledError(l.selector, l.index, l.frame.frameID)
		case opts.Disabled:
			return elem, release, nil
		default:
			geom := elem.Geometry()
			if geom == nil || geom.Width <= 0 || geom.Height <= 0 {
				release()
				lastErr = fmt.Errorf("element %q has zero-sized geometry", l.selector)
			} else {
				fresh, checkErr := elem.node.checkStableAndUnoccluded(ctx, opts)
				if checkErr == nil {
					// Refresh elem.geometry with the post-stability rect so
					// downstream Centroid()/Geometry() callers click exactly
					// where we just verified, not at a stale centroid.
					elem.geometry = &ElementGeometry{
						X:      fresh.X,
						Y:      fresh.Y,
						Width:  fresh.Width,
						Height: fresh.Height,
						Scale:  fresh.Scale,
					}
					return elem, release, nil
				}
				release()
				lastErr = checkErr
			}
		}

		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = fmt.Errorf("actionability timed out for %q", l.selector)
			}
			return nil, nil, fmt.Errorf("waiting for %q to be actionable: %w", l.selector, lastErr)
		}

		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}
}

// checkStableAndUnoccluded sleeps for opts.StabilityWindow inside the page,
// re-reads the bounding box, and verifies (a) it has not shifted by more than
// 1px in any dimension and (b) document.elementFromPoint at the centroid
// returns the target element or one of its descendants. Returns the
// post-stability geometry on success.
//
// The check runs as a single CDP round-trip — the setTimeout + Promise are
// resolved in-page, awaited via Runtime.callFunctionOn's awaitPromise.
func (n *resolvedNode) checkStableAndUnoccluded(ctx context.Context, opts ActionabilityOptions) (*geometryResult, error) {
	body := fmt.Sprintf(`
const r1 = this.getBoundingClientRect();
if (r1.width <= 0 || r1.height <= 0) {
    return { ok: false, reason: "zero_size" };
}
const skipStability = %v;
const skipOcclusion = %v;
const stabilityMs = %d;

const self = this;
// deepHit descends through shadow roots (open, and closed via the piercer's
// host->root registry) so the hit-target check resolves to the real element
// under the point rather than the shadow host returned by elementFromPoint.
const deepHit = (x, y) => {
    let el = document.elementFromPoint(x, y);
    let guard = 0;
    while (el && guard++ < 64) {
        let root = el.shadowRoot;
        if (!root && window.__stagehandV3__ && typeof window.__stagehandV3__.getClosedRoot === "function") {
            try { root = window.__stagehandV3__.getClosedRoot(el); } catch (e) {}
        }
        if (!root || typeof root.elementFromPoint !== "function") break;
        const inner = root.elementFromPoint(x, y);
        if (!inner || inner === el) break;
        el = inner;
    }
    return el;
};
const finishCheck = (rect, resolve) => {
    if (!skipOcclusion) {
        const cx = rect.left + rect.width / 2;
        const cy = rect.top + rect.height / 2;
        const hit = deepHit(cx, cy);
        if (!hit) {
            resolve({ ok: false, reason: "no_hit" });
            return;
        }
        if (hit !== self && !self.contains(hit)) {
            let desc = hit.tagName ? hit.tagName.toLowerCase() : "?";
            if (hit.id) desc += "#" + hit.id;
            else if (typeof hit.className === "string" && hit.className) {
                const cls = hit.className.split(" ")[0];
                if (cls) desc += "." + cls;
            }
            resolve({ ok: false, reason: "occluded", occluder: desc });
            return;
        }
    }
    resolve({ ok: true, x: rect.x, y: rect.y, width: rect.width, height: rect.height });
};

return new Promise((resolve) => {
    if (skipStability) {
        finishCheck(r1, resolve);
        return;
    }
    setTimeout(() => {
        const r2 = self.getBoundingClientRect();
        const stable =
            Math.abs(r1.x - r2.x) < 1 &&
            Math.abs(r1.y - r2.y) < 1 &&
            Math.abs(r1.width - r2.width) < 1 &&
            Math.abs(r1.height - r2.height) < 1;
        if (!stable) {
            resolve({ ok: false, reason: "unstable" });
            return;
        }
        finishCheck(r2, resolve);
    }, stabilityMs);
});
`, opts.SkipStability, opts.SkipOcclusion, opts.StabilityWindow.Milliseconds())

	var res struct {
		OK       bool    `json:"ok"`
		Reason   string  `json:"reason"`
		Occluder string  `json:"occluder,omitempty"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		Width    float64 `json:"width"`
		Height   float64 `json:"height"`
	}
	if err := n.callFunction(ctx, body, &res); err != nil {
		return nil, err
	}
	if !res.OK {
		switch res.Reason {
		case "occluded":
			return nil, fmt.Errorf("click point covered by <%s>", res.Occluder)
		case "unstable":
			return nil, errors.New("element layout still moving")
		case "no_hit":
			return nil, errors.New("no element at click point")
		case "zero_size":
			return nil, errors.New("element has zero-sized geometry")
		default:
			return nil, fmt.Errorf("not actionable: %s", res.Reason)
		}
	}
	return &geometryResult{X: res.X, Y: res.Y, Width: res.Width, Height: res.Height, Scale: 1}, nil
}
