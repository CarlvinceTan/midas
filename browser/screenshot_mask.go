package browser

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"github.com/PolymuxOrg/midas/browser/dom"
	"time"
)

type maskRect struct {
	X         float64
	Y         float64
	Width     float64
	Height    float64
	RootToken string
}

type maskFrameEntry struct {
	Rects      []maskRect
	RootTokens []string
}

func applyMaskOverlays(ctx context.Context, locators []*Locator, color string) screenshotCleanup {
	if len(locators) == 0 {
		return func() {}
	}
	if color == "" {
		color = "#FF0000"
	}

	token := fmt.Sprintf("__polymux_mask_%d_%s", time.Now().UnixNano(), randomString(8))
	rectsByFrame := make(map[*Frame]*maskFrameEntry)

	for _, locator := range locators {
		info, err := resolveMaskRects(ctx, locator, token)
		if err != nil {
			log.Printf("screenshot mask: failed to resolve locator: %v", err)
			continue
		}
		if info == nil {
			continue
		}
		entry := rectsByFrame[info.Frame]
		if entry == nil {
			entry = &maskFrameEntry{
				Rects:      make([]maskRect, 0),
				RootTokens: make([]string, 0),
			}
			rectsByFrame[info.Frame] = entry
		}
		entry.Rects = append(entry.Rects, info.Rects...)
		for _, rect := range info.Rects {
			if rect.RootToken != "" {
				entry.RootTokens = append(entry.RootTokens, rect.RootToken)
			}
		}
	}

	if len(rectsByFrame) == 0 {
		return func() {}
	}

	for frame, entry := range rectsByFrame {
		_ = frame.Evaluate(ctx, fmt.Sprintf(`(() => {
			try {
				const doc = document;
				if (!doc) return;
				for (const rect of %s) {
					const defaultRoot = doc.documentElement || doc.body;
					if (!defaultRoot) return;
					let root = defaultRoot;
					if (rect.rootToken) {
						const found = doc.querySelector('[data-polymux-mask-root="' + rect.rootToken + '"]');
						if (found) root = found;
					}
					const el = doc.createElement('div');
					el.setAttribute('data-polymux-mask', %q);
					el.style.position = 'absolute';
					el.style.left = rect.x + 'px';
					el.style.top = rect.y + 'px';
					el.style.width = rect.width + 'px';
					el.style.height = rect.height + 'px';
					el.style.backgroundColor = %q;
					el.style.pointerEvents = 'none';
					el.style.zIndex = '2147483647';
					el.style.opacity = '1';
					el.style.mixBlendMode = 'normal';
					if (rect.rootToken) {
						try {
							const style = window.getComputedStyle(root);
							if (style && style.position === 'static') {
								if (!root.hasAttribute('data-polymux-mask-root-pos')) {
									root.setAttribute('data-polymux-mask-root-pos', root.style.position || '');
								}
								root.style.position = 'relative';
							}
						} catch {}
					}
					root.appendChild(el);
				}
			} catch {}
		})()`, formatMaskRectsJSON(entry.Rects), token, color), nil)
	}

	return func() {
		for frame, entry := range rectsByFrame {
			_ = frame.Evaluate(context.Background(), fmt.Sprintf(`(() => {
				try {
					const doc = document;
					if (!doc) return;
					const nodes = doc.querySelectorAll('[data-polymux-mask="%s"]');
					nodes.forEach(node => node.remove());
					for (const rootToken of %s) {
						const root = doc.querySelector('[data-polymux-mask-root="' + rootToken + '"]');
						if (!root) continue;
						const prev = root.getAttribute('data-polymux-mask-root-pos');
						if (prev !== null) {
							root.style.position = prev;
							root.removeAttribute('data-polymux-mask-root-pos');
						}
						root.removeAttribute('data-polymux-mask-root');
					}
				} catch {}
			})()`, token, formatStringsJSON(entry.RootTokens)), nil)
		}
	}
}

type maskRectsResult struct {
	Frame *Frame
	Rects []maskRect
}

func resolveMaskRects(ctx context.Context, locator *Locator, maskToken string) (*maskRectsResult, error) {
	frame := locator.frame
	session := frame.page.sessionForFrame(frame.frameID)

	resolved, err := locator.resolveNodesForMask(ctx)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return nil, nil
	}

	rects := make([]maskRect, 0, len(resolved))
	for _, node := range resolved {
		rect, err := resolveMaskRectForObject(ctx, session, node.objectID, maskToken)
		if err != nil {
			log.Printf("screenshot mask: failed to resolve rect for object: %v", err)
		}
		_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
			"objectId": node.objectID,
		}, nil)
		if rect != nil {
			rects = append(rects, *rect)
		}
	}

	if len(rects) == 0 {
		return nil, nil
	}

	return &maskRectsResult{
		Frame: frame,
		Rects: rects,
	}, nil
}

func resolveMaskRectForObject(ctx context.Context, session sessionLike, objectID, maskToken string) (*maskRect, error) {
	var res struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}

	err := session.Send(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":            objectID,
		"functionDeclaration": resolveMaskRectScript,
		"arguments":           []any{map[string]any{"value": maskToken}},
		"returnByValue":       true,
		"awaitPromise":        true,
	}, &res)
	if err != nil {
		return nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, fmt.Errorf("mask rect script error: %s", defaultString(res.ExceptionDetails.Exception.Description, res.ExceptionDetails.Text))
	}

	rect, err := parseMaskRectResult(res.Result.Value)
	if err != nil {
		return nil, err
	}
	return rect, nil
}

func parseMaskRectResult(value any) (*maskRect, error) {
	m, ok := value.(map[string]any)
	if !ok || m == nil {
		return nil, nil
	}

	x, _ := m["x"].(float64)
	y, _ := m["y"].(float64)
	w, _ := m["width"].(float64)
	h, _ := m["height"].(float64)
	rootToken, _ := m["rootToken"].(string)

	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) || math.IsNaN(w) || math.IsInf(w, 0) || math.IsNaN(h) || math.IsInf(h, 0) {
		return nil, nil
	}
	if w <= 0 || h <= 0 {
		return nil, nil
	}

	return &maskRect{
		X:         x,
		Y:         y,
		Width:     w,
		Height:    h,
		RootToken: rootToken,
	}, nil
}

func (l *Locator) resolveNodesForMask(ctx context.Context) ([]*resolvedNode, error) {
	targetFrame, finalSelector, err := l.frame.page.resolveSelectorTarget(ctx, l.frame, l.selector, l.pierceShadow)
	if err != nil {
		return nil, err
	}

	session := targetFrame.page.sessionForFrame(targetFrame.frameID)
	ctxID, err := targetFrame.page.ensureSelectorWorld(ctx, session, targetFrame.frameID)
	if err != nil {
		return nil, err
	}

	var indices []int
	if l.index == 0 {
		evalRes := struct {
			Result struct {
				Value int `json:"value"`
			} `json:"result"`
		}{}
		if err := session.Send(ctx, "Runtime.evaluate", map[string]any{
			"expression":    dom.BuildContextInvocation(dom.HelperCount, finalSelector, l.pierceShadow),
			"returnByValue": true,
			"awaitPromise":  true,
			"contextId":     ctxID,
		}, &evalRes); err != nil {
			return nil, err
		}
		count := evalRes.Result.Value
		indices = make([]int, count)
		for i := 0; i < count; i++ {
			indices[i] = i
		}
	} else {
		indices = []int{l.index}
	}

	var results []*resolvedNode
	var toRelease []string

	defer func() {
		if len(results) == 0 {
			for _, objectID := range toRelease {
				_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
					"objectId": objectID,
				}, nil)
			}
		}
	}()

	for _, idx := range indices {
		var res struct {
			Result struct {
				ObjectID string `json:"objectId"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if err := session.Send(ctx, "Runtime.evaluate", map[string]any{
			"expression":    dom.BuildContextInvocation(dom.HelperResolve, finalSelector, l.pierceShadow, idx),
			"returnByValue": false,
			"awaitPromise":  true,
			"contextId":     ctxID,
		}, &res); err != nil {
			continue
		}
		if res.ExceptionDetails != nil || res.Result.ObjectID == "" {
			continue
		}

		var describe struct {
			Node struct {
				NodeID        int `json:"nodeId"`
				BackendNodeID int `json:"backendNodeId"`
			} `json:"node"`
		}
		if err := session.Send(ctx, "DOM.describeNode", map[string]any{
			"objectId": res.Result.ObjectID,
		}, &describe); err != nil {
			_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
				"objectId": res.Result.ObjectID,
			}, nil)
			continue
		}

		results = append(results, &resolvedNode{
			frame:         targetFrame,
			session:       session,
			frameID:       targetFrame.frameID,
			sessionID:     session.ID(),
			objectID:      res.Result.ObjectID,
			nodeID:        describe.Node.NodeID,
			backendNodeID: describe.Node.BackendNodeID,
		})
		toRelease = append(toRelease, res.Result.ObjectID)
	}

	return results, nil
}

func formatMaskRectsJSON(rects []maskRect) string {
	s := "["
	for i, rect := range rects {
		if i > 0 {
			s += ","
		}
		rootToken := "null"
		if rect.RootToken != "" {
			rootToken = fmt.Sprintf("%q", rect.RootToken)
		}
		s += fmt.Sprintf(`{x:%g,y:%g,width:%g,height:%g,rootToken:%s}`, rect.X, rect.Y, rect.Width, rect.Height, rootToken)
	}
	s += "]"
	return s
}

func formatStringsJSON(strs []string) string {
	s := "["
	for i, str := range strs {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%q", str)
	}
	s += "]"
	return s
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
