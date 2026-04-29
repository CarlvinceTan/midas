package browser

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

type Frame struct {
	page    *Page
	frameID string
}

func (f *Frame) ID() string {
	return f.frameID
}

func (f *Frame) URL() string {
	f.page.mu.RLock()
	defer f.page.mu.RUnlock()
	return f.page.registry.Frame(f.frameID).URL
}

func (f *Frame) Name() string {
	f.page.mu.RLock()
	defer f.page.mu.RUnlock()
	return f.page.registry.Frame(f.frameID).Name
}

func (f *Frame) Parent() *Frame {
	f.page.mu.RLock()
	defer f.page.mu.RUnlock()
	parentID := f.page.registry.GetParent(f.frameID)
	if parentID == "" {
		return nil
	}
	return f.page.Frame(parentID)
}

func (f *Frame) ParentFrame() *Frame {
	return f.Parent()
}

func (f *Frame) ChildFrames() []*Frame {
	f.page.mu.RLock()
	ids := f.page.registry.ChildFrames(f.frameID)
	f.page.mu.RUnlock()
	out := make([]*Frame, 0, len(ids))
	for _, id := range ids {
		out = append(out, f.page.Frame(id))
	}
	return out
}

func (f *Frame) IsDetached() bool {
	f.page.mu.RLock()
	defer f.page.mu.RUnlock()
	for _, id := range f.page.registry.ListAllFrames() {
		if id == f.frameID {
			return false
		}
	}
	return true
}

func (f *Frame) Evaluate(ctx context.Context, expression string, result any) error {
	return f.page.evaluateInFrame(ctx, f.frameID, expression, result)
}

func (f *Frame) GetNodeAtLocation(ctx context.Context, x, y int) (map[string]any, error) {
	session := f.page.sessionForFrame(f.frameID)
	_ = session.Send(ctx, "DOM.enable", nil, nil)

	var nodeLoc struct {
		BackendNodeID int `json:"backendNodeId"`
	}
	if err := session.Send(ctx, "DOM.getNodeForLocation", map[string]any{
		"x":                         x,
		"y":                         y,
		"includeUserAgentShadowDOM": true,
		"ignorePointerEventsNone":   false,
	}, &nodeLoc); err != nil {
		return nil, err
	}

	var describe struct {
		Node map[string]any `json:"node"`
	}
	if err := session.Send(ctx, "DOM.describeNode", map[string]any{
		"backendNodeId": nodeLoc.BackendNodeID,
	}, &describe); err != nil {
		return nil, err
	}
	return describe.Node, nil
}

func (f *Frame) GetLocationForSelector(ctx context.Context, selector string) (*ScreenshotClip, error) {
	session := f.page.sessionForFrame(f.frameID)
	_ = session.Send(ctx, "DOM.enable", nil, nil)

	var doc struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := session.Send(ctx, "DOM.getDocument", nil, &doc); err != nil {
		return nil, err
	}

	var query struct {
		NodeID int `json:"nodeId"`
	}
	if err := session.Send(ctx, "DOM.querySelector", map[string]any{
		"nodeId":   doc.Root.NodeID,
		"selector": selector,
	}, &query); err != nil {
		return nil, err
	}

	var box struct {
		Model struct {
			Content []float64 `json:"content"`
			Width   float64   `json:"width"`
			Height  float64   `json:"height"`
		} `json:"model"`
	}
	if err := session.Send(ctx, "DOM.getBoxModel", map[string]any{
		"nodeId": query.NodeID,
	}, &box); err != nil {
		return nil, err
	}
	return &ScreenshotClip{
		X:      box.Model.Content[0],
		Y:      box.Model.Content[1],
		Width:  box.Model.Width,
		Height: box.Model.Height,
		Scale:  1,
	}, nil
}

func (f *Frame) GetAccessibilityTree(ctx context.Context, withFrames bool) ([]map[string]any, error) {
	session := f.page.sessionForFrame(f.frameID)
	_ = session.Send(ctx, "Accessibility.enable", nil, nil)

	var res struct {
		Nodes []map[string]any `json:"nodes"`
	}
	params := map[string]any{"frameId": f.frameID}
	if err := session.Send(ctx, "Accessibility.getFullAXTree", params, &res); err != nil {
		if err := session.Send(ctx, "Accessibility.getFullAXTree", nil, &res); err != nil {
			return nil, err
		}
	}
	if !withFrames {
		return res.Nodes, nil
	}
	nodes := append([]map[string]any(nil), res.Nodes...)
	for _, child := range f.ChildFrames() {
		childNodes, err := child.GetAccessibilityTree(ctx, false)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, childNodes...)
	}
	return nodes, nil
}

func (f *Frame) WaitForLoadState(ctx context.Context, state LoadState, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if f.frameID == f.page.MainFrameID() {
		return f.page.WaitForMainLoadState(waitCtx, state)
	}
	return f.Evaluate(waitCtx, fmt.Sprintf(`(() => document.readyState === %q || document.readyState === "complete")()`, lifecycleReadyState(state)), nil)
}

func (f *Frame) Locator(selector string) *Locator {
	return &Locator{frame: f, selector: selector, pierceShadow: true}
}

func (f *Frame) Tap(ctx context.Context, x, y float64) error {
	return f.page.Tap(ctx, x, y)
}

func (f *Frame) DeepLocator(selector string) *DeepLocator {
	return &DeepLocator{Locator: *f.Locator(selector)}
}

func (f *Frame) FrameLocator(selector string) *FrameLocator {
	return &FrameLocator{page: f.page, root: f, selector: selector}
}

func (f *Frame) Screenshot(ctx context.Context, opts ScreenshotOptions) ([]byte, error) {
	frames := []*Frame{f}
	cleanups := make([]screenshotCleanup, 0, 5)
	defer runScreenshotCleanups(cleanups)

	animMode := opts.Animations
	if animMode == "" {
		animMode = ScreenshotAnimationsDisabled
	}
	if animMode == ScreenshotAnimationsDisabled {
		cleanups = append(cleanups, disableAnimationsForFrames(ctx, frames))
	}

	caretMode := opts.Caret
	if caretMode == "" || caretMode == ScreenshotCaretHide {
		cleanups = append(cleanups, hideCaretForFrames(ctx, frames))
	}

	if opts.Style != "" {
		cleanups = append(cleanups, applyStyleToFrames(ctx, frames, opts.Style, "custom"))
	}

	if len(opts.Mask) > 0 {
		maskColor := opts.MaskColor
		if maskColor == "" {
			maskColor = "#FF0000"
		}
		cleanups = append(cleanups, applyMaskOverlays(ctx, opts.Mask, maskColor))
	}

	if opts.WaitBeforeCapture > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.WaitBeforeCapture):
		}
	}

	clip, err := normalizeScreenshotClip(opts.Clip)
	if err != nil {
		return nil, err
	}
	params := map[string]any{}
	if opts.Format != "" {
		params["format"] = opts.Format
	}
	if opts.Quality > 0 {
		params["quality"] = opts.Quality
	}
	if clip != nil {
		clipScale := clip.Scale
		if clipScale <= 0 {
			clipScale = 1
		}
		params["clip"] = map[string]any{
			"x":      clip.X,
			"y":      clip.Y,
			"width":  clip.Width,
			"height": clip.Height,
			"scale":  clipScale,
		}
	}
	if opts.FullPage {
		params["captureBeyondViewport"] = true
	}
	session := f.page.sessionForFrame(f.frameID)
	if opts.OmitBackground {
		params["fromSurface"] = true
		cleanups = append(cleanups, setTransparentBackground(ctx, session))
	}

	var res struct {
		Data string `json:"data"`
	}
	if err := session.Send(ctx, "Page.captureScreenshot", params, &res); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(res.Data)
}

func (f *Frame) resolveChildFrame(ctx context.Context, selector string, pierceShadow bool) (*Frame, error) {
	node, release, err := f.resolveSelector(ctx, selector, selectorResolveOptions{pierceShadow: pierceShadow})
	if err != nil {
		return nil, err
	}
	defer release()

	if child := f.page.childFrameForOwnerNode(f.frameID, node.backendNodeID); child != nil {
		return child, nil
	}

	var match struct {
		Name string `json:"name"`
		Src  string `json:"src"`
	}
	if err := node.callFunction(ctx, `
		return {
			name: this.getAttribute("name") || "",
			src: this.getAttribute("src") || ""
		};
	`, &match); err != nil {
		return nil, err
	}

	children := f.ChildFrames()
	if len(children) == 0 {
		return nil, fmt.Errorf("no child frames found for %q", selector)
	}
	for _, child := range children {
		if match.Name != "" && child.Name() == match.Name {
			return child, nil
		}
		if match.Src != "" && strings.Contains(child.URL(), match.Src) {
			return child, nil
		}
	}
	return children[0], nil
}

func lifecycleReadyState(state LoadState) string {
	switch state {
	case LoadStateLoad:
		return "complete"
	default:
		return "interactive"
	}
}
