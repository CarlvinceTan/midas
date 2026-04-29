package snapshot

import "context"

func ActiveElementXPath(ctx context.Context, page Page) string {
	frameCtx := buildFrameContext(page)
	focusedFrameID := ""
	for _, frameID := range frameCtx.Frames {
		session := page.SessionForFrame(frameID)
		var res struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		if err := session.Send(ctx, "Runtime.evaluate", map[string]any{
			"expression":    documentHasFocusStrictJS(),
			"returnByValue": true,
		}, &res); err == nil && res.Result.Value {
			focusedFrameID = frameID
			break
		}
	}
	if focusedFrameID == "" {
		focusedFrameID = page.MainFrameID()
	}

	focusedSession := page.SessionForFrame(focusedFrameID)
	var active struct {
		Result struct {
			ObjectID string `json:"objectId"`
		} `json:"result"`
	}
	if err := focusedSession.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    resolveDeepActiveElementJS(),
		"returnByValue": false,
	}, &active); err != nil || active.Result.ObjectID == "" {
		return ""
	}
	defer func() {
		_ = focusedSession.Send(context.Background(), "Runtime.releaseObject", map[string]any{"objectId": active.Result.ObjectID}, nil)
	}()
	var leaf struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := focusedSession.Send(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":            active.Result.ObjectID,
		"functionDeclaration": nodeToAbsoluteXPathJS(),
		"returnByValue":       true,
	}, &leaf); err != nil || leaf.Result.Value == "" {
		return ""
	}
	prefix := ""
	for cur := focusedFrameID; cur != ""; cur = frameCtx.ParentByFrame[cur] {
		parent := frameCtx.ParentByFrame[cur]
		if parent == "" {
			break
		}
		parentSession := page.SessionForFrame(parent)
		var owner struct {
			BackendNodeID int `json:"backendNodeId"`
		}
		if err := parentSession.Send(ctx, "DOM.getFrameOwner", map[string]any{"frameId": cur}, &owner); err == nil && owner.BackendNodeID > 0 {
			xp := absoluteXPathForBackendNode(ctx, parentSession, owner.BackendNodeID)
			if xp != "" {
				if prefix == "" {
					prefix = normalizeXPath(xp)
				} else {
					prefix = prefixXPath(prefix, xp)
				}
			}
		}
	}
	if prefix == "" {
		return normalizeXPath(leaf.Result.Value)
	}
	return prefixXPath(prefix, leaf.Result.Value)
}

func ResolveXPathForLocation(ctx context.Context, page Page, x, y int) *ResolvedLocation {
	frameCtx := buildFrameContext(page)
	curFrameID := page.MainFrameID()
	curSession := page.SessionForFrame(curFrameID)
	curX := x
	curY := y
	chain := make([]iframeChainStep, 0, 4)

	for depth := 0; depth < 8; depth++ {
		_ = curSession.Send(ctx, "DOM.enable", nil, nil)
		sx, sy := 0, 0
		var scroll struct {
			Result struct {
				Value struct {
					SX int `json:"sx"`
					SY int `json:"sy"`
				} `json:"value"`
			} `json:"result"`
		}
		if err := curSession.Send(ctx, "Runtime.evaluate", map[string]any{
			"expression":    getScrollOffsetsJS(),
			"returnByValue": true,
		}, &scroll); err == nil {
			sx = scroll.Result.Value.SX
			sy = scroll.Result.Value.SY
		}

		var nodeLoc struct {
			BackendNodeID int    `json:"backendNodeId"`
			FrameID       string `json:"frameId"`
		}
		if err := curSession.Send(ctx, "DOM.getNodeForLocation", map[string]any{
			"x":                         curX + sx,
			"y":                         curY + sy,
			"includeUserAgentShadowDOM": false,
			"ignorePointerEventsNone":   false,
		}, &nodeLoc); err != nil || nodeLoc.BackendNodeID <= 0 {
			return nil
		}
		if nodeLoc.FrameID != "" && nodeLoc.FrameID != curFrameID {
			abs := buildAbsoluteXPathFromChain(ctx, chain, curSession, nodeLoc.BackendNodeID)
			if abs == "" {
				return nil
			}
			return &ResolvedLocation{
				FrameID:       nodeLoc.FrameID,
				BackendNodeID: nodeLoc.BackendNodeID,
				AbsoluteXPath: abs,
			}
		}

		var matchedChild string
		for _, fid := range listChildrenOf(frameCtx.ParentByFrame, curFrameID) {
			var owner struct {
				BackendNodeID int `json:"backendNodeId"`
			}
			if err := curSession.Send(ctx, "DOM.getFrameOwner", map[string]any{"frameId": fid}, &owner); err == nil && owner.BackendNodeID == nodeLoc.BackendNodeID {
				matchedChild = fid
				break
			}
		}
		if matchedChild == "" {
			abs := buildAbsoluteXPathFromChain(ctx, chain, curSession, nodeLoc.BackendNodeID)
			if abs == "" {
				return nil
			}
			return &ResolvedLocation{
				FrameID:       curFrameID,
				BackendNodeID: nodeLoc.BackendNodeID,
				AbsoluteXPath: abs,
			}
		}
		chain = append(chain, iframeChainStep{
			ParentSession:       curSession,
			IFrameBackendNodeID: nodeLoc.BackendNodeID,
		})

		var resolved struct {
			Object struct {
				ObjectID string `json:"objectId"`
			} `json:"object"`
		}
		left, top := 0, 0
		if err := curSession.Send(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": nodeLoc.BackendNodeID}, &resolved); err == nil && resolved.Object.ObjectID != "" {
			var rect struct {
				Result struct {
					Value struct {
						Left int `json:"left"`
						Top  int `json:"top"`
					} `json:"value"`
				} `json:"result"`
			}
			if err := curSession.Send(ctx, "Runtime.callFunctionOn", map[string]any{
				"objectId":            resolved.Object.ObjectID,
				"functionDeclaration": getBoundingRectLiteJS(),
				"returnByValue":       true,
			}, &rect); err == nil {
				left = rect.Result.Value.Left
				top = rect.Result.Value.Top
			}
			_ = curSession.Send(context.Background(), "Runtime.releaseObject", map[string]any{"objectId": resolved.Object.ObjectID}, nil)
		}
		curX -= left
		curY -= top
		if curX < 0 {
			curX = 0
		}
		if curY < 0 {
			curY = 0
		}
		curFrameID = matchedChild
		curSession = page.SessionForFrame(curFrameID)
	}
	return nil
}

func documentHasFocusStrictJS() string {
	return `(() => !!document.hasFocus())()`
}

func resolveDeepActiveElementJS() string {
	return `(() => {
		let current = document.activeElement;
		while (current && current.shadowRoot && current.shadowRoot.activeElement) {
			current = current.shadowRoot.activeElement;
		}
		return current || null;
	})()`
}

func getScrollOffsetsJS() string {
	return `(() => ({ sx: Math.round(window.scrollX || 0), sy: Math.round(window.scrollY || 0) }))()`
}

func getBoundingRectLiteJS() string {
	return `function() {
		const rect = this.getBoundingClientRect();
		return { left: Math.round(rect.left), top: Math.round(rect.top) };
	}`
}
