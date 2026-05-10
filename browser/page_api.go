package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	snappkg "github.com/PolymuxOrg/midas/browser/snapshot"
	"github.com/PolymuxOrg/midas/browser/dom"
	"strings"
	"time"
	"unicode/utf8"
)

func (p *Page) seedDefaults(initScripts []string, headers map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, source := range initScripts {
		if !containsString(p.initScripts, source) {
			p.initScripts = append(p.initScripts, source)
		}
	}
	if len(headers) > 0 {
		p.extraHTTPHeaders = cloneStringMap(headers)
	}
}

func (p *Page) sessionForFrame(frameID string) sessionLike {
	p.mu.RLock()
	defer p.mu.RUnlock()
	sessionID := p.registry.GetOwnerSessionID(frameID)
	if sessionID == "" {
		return p.mainSession
	}
	if session := p.sessions[sessionID]; session != nil {
		return session
	}
	return p.mainSession
}

func (p *Page) Frame(frameID string) *Frame {
	if frameID == "" {
		frameID = p.MainFrameID()
	}
	return &Frame{page: p, frameID: frameID}
}

func (p *Page) MainFrame() *Frame {
	return p.Frame(p.MainFrameID())
}

func (p *Page) Frames() []*Frame {
	p.mu.RLock()
	ids := p.registry.ListAllFrames()
	p.mu.RUnlock()
	out := make([]*Frame, 0, len(ids))
	for _, id := range ids {
		out = append(out, p.Frame(id))
	}
	return out
}

func (p *Page) Title(ctx context.Context) (string, error) {
	var title string
	err := p.Evaluate(ctx, "document.title", &title)
	return title, err
}

func (p *Page) Evaluate(ctx context.Context, expression string, result any) error {
	return p.evaluateInFrame(ctx, p.MainFrameID(), expression, result)
}

func (p *Page) evaluateInFrame(ctx context.Context, frameID, expression string, result any) error {
	session := p.sessionForFrame(frameID)
	ctxID := p.execCtx.MainWorldID(session.ID(), frameID)

	params := map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}
	if ctxID != 0 {
		params["contextId"] = ctxID
	}

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
	if err := session.Send(ctx, "Runtime.evaluate", params, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		if res.ExceptionDetails.Exception.Description != "" {
			return errors.New(res.ExceptionDetails.Exception.Description)
		}
		if res.ExceptionDetails.Text != "" {
			return errors.New(res.ExceptionDetails.Text)
		}
		return errors.New("page evaluation failed")
	}
	if result == nil {
		return nil
	}
	buf, err := json.Marshal(res.Result.Value)
	if err != nil {
		return err
	}
	return json.Unmarshal(buf, result)
}

func (p *Page) WaitForTimeout(_ context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	<-timer.C
	return nil
}

func (p *Page) WaitForSelector(ctx context.Context, selector string, opts WaitForSelectorOptions) (bool, error) {
	if opts.State == "" {
		opts.State = SelectorStateVisible
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	frame, finalSelector, err := p.resolveSelectorTarget(ctx, p.MainFrame(), selector, opts.PierceShadow)
	if err != nil {
		return false, err
	}
	session := frame.page.sessionForFrame(frame.frameID)
	ctxID, err := frame.page.ensureSelectorWorld(ctx, session, frame.frameID)
	if err != nil {
		return false, err
	}

	deadline := time.Now().Add(opts.Timeout)
	for {
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
		err := session.Send(ctx, "Runtime.evaluate", map[string]any{
			"expression":    dom.BuildContextInvocation(dom.HelperMatchesState, finalSelector, string(opts.State), opts.PierceShadow),
			"returnByValue": true,
			"awaitPromise":  true,
			"contextId":     ctxID,
		}, &res)
		if err == nil && res.ExceptionDetails == nil {
			var matched bool
			buf, marshalErr := json.Marshal(res.Result.Value)
			if marshalErr == nil && json.Unmarshal(buf, &matched) == nil && matched {
				return true, nil
			}
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("waitForSelector timeout for %q", selector)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (p *Page) SetViewportSize(ctx context.Context, width, height int, deviceScaleFactor float64) error {
	if deviceScaleFactor <= 0 {
		deviceScaleFactor = 1
	}
	return p.mainSession.Send(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             width,
		"height":            height,
		"deviceScaleFactor": deviceScaleFactor,
		"mobile":            false,
		"screenWidth":       width,
		"screenHeight":      height,
	}, nil)
}

func (p *Page) Click(ctx context.Context, x, y float64, clickCount int) error {
	if clickCount <= 0 {
		clickCount = 1
	}
	if err := p.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseMoved",
		"x":      x,
		"y":      y,
		"button": "none",
	}, nil); err != nil {
		return err
	}
	p.notifyMousePos(x, y)
	for i := 1; i <= clickCount; i++ {
		if err := p.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":       "mousePressed",
			"x":          x,
			"y":          y,
			"button":     "left",
			"clickCount": i,
		}, nil); err != nil {
			return err
		}
		if err := p.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":       "mouseReleased",
			"x":          x,
			"y":          y,
			"button":     "left",
			"clickCount": i,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *Page) Hover(ctx context.Context, x, y float64) error {
	if err := p.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseMoved",
		"x":      x,
		"y":      y,
		"button": "none",
	}, nil); err != nil {
		return err
	}
	p.notifyMousePos(x, y)
	return nil
}

func (p *Page) Scroll(ctx context.Context, x, y, deltaX, deltaY float64) error {
	if err := p.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseWheel",
		"x":      x,
		"y":      y,
		"button": "none",
		"deltaX": deltaX,
		"deltaY": deltaY,
	}, nil); err != nil {
		return err
	}
	p.notifyMousePos(x, y)
	return nil
}

func (p *Page) DragAndDrop(ctx context.Context, fromX, fromY, toX, toY float64, steps int) error {
	if steps <= 0 {
		steps = 1
	}
	if err := p.Click(ctx, fromX, fromY, 1); err != nil {
		return err
	}
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := fromX + (toX-fromX)*t
		y := fromY + (toY-fromY)*t
		if err := p.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":    "mouseMoved",
			"x":       x,
			"y":       y,
			"button":  "left",
			"buttons": 1,
		}, nil); err != nil {
			return err
		}
		p.notifyMousePos(x, y)
	}
	if err := p.mainSession.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mouseReleased",
		"x":          toX,
		"y":          toY,
		"button":     "left",
		"buttons":    1,
		"clickCount": 1,
	}, nil); err != nil {
		return err
	}
	p.notifyMousePos(toX, toY)
	return nil
}

func (p *Page) Type(ctx context.Context, text string, delay time.Duration) error {
	for _, r := range text {
		ch := string(r)
		if err := p.mainSession.Send(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":           "keyDown",
			"text":           ch,
			"unmodifiedText": ch,
			"key":            ch,
		}, nil); err != nil {
			return err
		}
		if err := p.mainSession.Send(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type": "keyUp",
			"key":  ch,
		}, nil); err != nil {
			return err
		}
		if delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil
}

// namedKeyCDP maps key names to CDP code/virtualKeyCode pairs (use rawKeyDown for non-character keys).
var namedKeyCDP = map[string]struct {
	Code string
	VK   int
}{
	"Escape":     {"Escape", 27},
	"Tab":        {"Tab", 9},
	"Backspace":  {"Backspace", 8},
	"Delete":     {"Delete", 46},
	"ArrowUp":    {"ArrowUp", 38},
	"ArrowDown":  {"ArrowDown", 40},
	"ArrowLeft":  {"ArrowLeft", 37},
	"ArrowRight": {"ArrowRight", 39},
	"Home":       {"Home", 36},
	"End":        {"End", 35},
	"PageUp":     {"PageUp", 33},
	"PageDown":   {"PageDown", 34},
}

func (p *Page) KeyPress(ctx context.Context, key string) error {
	// Enter requires rawKeyDown → char(\r) → keyUp for form submission (devtools-protocol #45).
	if key == "Enter" {
		const vk = 13
		if err := p.mainSession.Send(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "rawKeyDown",
			"key":                   "Enter",
			"code":                  "Enter",
			"windowsVirtualKeyCode": vk,
			"nativeVirtualKeyCode":  vk,
			"text":                  "\r",
			"unmodifiedText":        "\r",
		}, nil); err != nil {
			return err
		}
		if err := p.mainSession.Send(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "char",
			"key":                   "Enter",
			"code":                  "Enter",
			"windowsVirtualKeyCode": vk,
			"nativeVirtualKeyCode":  vk,
			"text":                  "\r",
			"unmodifiedText":        "\r",
		}, nil); err != nil {
			return err
		}
		return p.mainSession.Send(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "keyUp",
			"key":                   "Enter",
			"code":                  "Enter",
			"windowsVirtualKeyCode": vk,
			"nativeVirtualKeyCode":  vk,
		}, nil)
	}

	downType := "keyDown"
	if utf8.RuneCountInString(key) != 1 {
		downType = "rawKeyDown"
	}
	down := map[string]any{"type": downType, "key": key}
	up := map[string]any{"type": "keyUp", "key": key}
	if d, ok := namedKeyCDP[key]; ok {
		down["code"] = d.Code
		down["windowsVirtualKeyCode"] = d.VK
		down["nativeVirtualKeyCode"] = d.VK
		up["code"] = d.Code
		up["windowsVirtualKeyCode"] = d.VK
		up["nativeVirtualKeyCode"] = d.VK
	}
	if err := p.mainSession.Send(ctx, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return p.mainSession.Send(ctx, "Input.dispatchKeyEvent", up, nil)
}

func (p *Page) GoBack(ctx context.Context, waitUntil LoadState, timeout time.Duration) (*Response, error) {
	return p.traverseHistory(ctx, -1, waitUntil, timeout)
}

func (p *Page) GoForward(ctx context.Context, waitUntil LoadState, timeout time.Duration) (*Response, error) {
	return p.traverseHistory(ctx, 1, waitUntil, timeout)
}

func (p *Page) traverseHistory(ctx context.Context, delta int, waitUntil LoadState, timeout time.Duration) (*Response, error) {
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := p.mainSession.Send(ctx, "Page.getNavigationHistory", nil, &history); err != nil {
		return nil, err
	}
	nextIndex := history.CurrentIndex + delta
	if nextIndex < 0 || nextIndex >= len(history.Entries) {
		return nil, nil
	}
	navID := p.beginNavigationCommand()
	tracker := NewNavigationResponseTracker(p, p.mainSession, navID)
	defer tracker.Dispose()
	var watcher *LifecycleWatcher
	if waitUntil != "" {
		watcher = NewLifecycleWatcher(p, p.mainSession, p.network, waitUntil, timeout, navID)
		defer watcher.Dispose()
	}
	if err := p.mainSession.Send(ctx, "Page.navigateToHistoryEntry", map[string]any{
		"entryId": history.Entries[nextIndex].ID,
	}, nil); err != nil {
		return nil, err
	}
	if history.Entries[nextIndex].URL != "" {
		p.seedCurrentURL(history.Entries[nextIndex].URL)
	}
	if waitUntil == "" {
		return nil, nil
	}
	if watcher != nil {
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := watcher.Wait(waitCtx); err != nil {
			return nil, err
		}
		return tracker.NavigationCompleted(waitCtx), nil
	}
	return tracker.NavigationCompleted(ctx), nil
}

func (p *Page) AddInitScript(ctx context.Context, source string) error {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	p.mu.Lock()
	if containsString(p.initScripts, source) {
		p.mu.Unlock()
		return nil
	}
	p.initScripts = append(p.initScripts, source)
	sessions := p.snapshotSessionsLocked()
	p.mu.Unlock()
	for _, session := range sessions {
		if err := session.Send(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
			"source": source,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *Page) SetExtraHTTPHeaders(ctx context.Context, headers map[string]string) error {
	headers = cloneStringMap(headers)
	p.mu.Lock()
	p.extraHTTPHeaders = headers
	sessions := p.snapshotSessionsLocked()
	p.mu.Unlock()
	for _, session := range sessions {
		if err := session.Send(ctx, "Network.enable", nil, nil); err != nil {
			return err
		}
		if err := session.Send(ctx, "Network.setExtraHTTPHeaders", map[string]any{
			"headers": headers,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *Page) AddConsoleListener(listener ConsoleListener) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextConsoleListenerID++
	id := p.nextConsoleListenerID
	p.consoleListeners[id] = listener
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.consoleListeners, id)
	}
}

func (p *Page) installConsoleBridge(session sessionLike) {
	session.On("Runtime.consoleAPICalled", func(params json.RawMessage) {
		var evt struct {
			Type string `json:"type"`
			Args []struct {
				Type        string `json:"type"`
				Value       any    `json:"value"`
				Description string `json:"description"`
			} `json:"args"`
			ExecutionContextID int64   `json:"executionContextId"`
			Timestamp          float64 `json:"timestamp"`
			StackTrace         struct {
				CallFrames []struct {
					URL          string `json:"url"`
					LineNumber   int    `json:"lineNumber"`
					ColumnNumber int    `json:"columnNumber"`
				} `json:"callFrames"`
			} `json:"stackTrace"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		msg := ConsoleMessage{
			Type:       evt.Type,
			Timestamp:  evt.Timestamp,
			PageTarget: p.targetID,
		}
		for _, arg := range evt.Args {
			msg.Args = append(msg.Args, map[string]any{
				"type":        arg.Type,
				"value":       arg.Value,
				"description": arg.Description,
			})
			if msg.Text != "" {
				msg.Text += " "
			}
			switch {
			case arg.Value != nil:
				msg.Text += fmt.Sprint(arg.Value)
			case arg.Description != "":
				msg.Text += arg.Description
			default:
				msg.Text += arg.Type
			}
		}
		if len(evt.StackTrace.CallFrames) > 0 {
			msg.Location = map[string]any{
				"url":          evt.StackTrace.CallFrames[0].URL,
				"lineNumber":   evt.StackTrace.CallFrames[0].LineNumber,
				"columnNumber": evt.StackTrace.CallFrames[0].ColumnNumber,
			}
		}
		p.mu.RLock()
		listeners := make([]ConsoleListener, 0, len(p.consoleListeners))
		for _, listener := range p.consoleListeners {
			listeners = append(listeners, listener)
		}
		p.mu.RUnlock()
		for _, listener := range listeners {
			listener(msg)
		}
	})
}

// AddMousePosListener registers fn to be called whenever the page records a
// new cursor position (any humanized or direct CDP mouse event). Returns an
// unsubscribe function.
func (p *Page) AddMousePosListener(listener MousePosListener) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextMousePosListenerID++
	id := p.nextMousePosListenerID
	p.mousePosListeners[id] = listener
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.mousePosListeners, id)
	}
}

// notifyMousePos snapshots the listener map under the read lock and invokes
// each one outside the lock so a slow listener can't block CDP dispatch.
func (p *Page) notifyMousePos(x, y float64) {
	p.mu.RLock()
	listeners := make([]MousePosListener, 0, len(p.mousePosListeners))
	for _, l := range p.mousePosListeners {
		listeners = append(listeners, l)
	}
	p.mu.RUnlock()
	for _, l := range listeners {
		l(x, y)
	}
}

func (p *Page) AddDialogListener(listener DialogListener) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextDialogListenerID++
	id := p.nextDialogListenerID
	p.dialogListeners[id] = listener
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.dialogListeners, id)
	}
}

func (p *Page) installDialogHandler(session sessionLike) {
	session.On("Page.javascriptDialogOpening", func(params json.RawMessage) {
		var evt struct {
			URL               string     `json:"url"`
			FrameID           string     `json:"frameId"`
			Type              DialogType `json:"type"`
			Message           string     `json:"message"`
			HasBrowserHandler bool       `json:"hasBrowserHandler"`
			DefaultPrompt     string     `json:"defaultPrompt"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		dialog := newDialog(p, evt.FrameID, evt.URL, evt.Type, evt.Message, evt.DefaultPrompt)
		p.mu.RLock()
		listeners := make([]DialogListener, 0, len(p.dialogListeners))
		for _, listener := range p.dialogListeners {
			listeners = append(listeners, listener)
		}
		p.mu.RUnlock()
		for _, listener := range listeners {
			listener(dialog)
		}
	})
}

func (p *Page) handleDialog(ctx context.Context, accept bool, promptText string) error {
	return p.mainSession.Send(ctx, "Page.handleJavaScriptDialog", map[string]any{
		"accept":     accept,
		"promptText": promptText,
	}, nil)
}

func (p *Page) Screenshot(ctx context.Context, opts ScreenshotOptions) ([]byte, error) {
	frames := collectFramesForScreenshot(p)
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
		cleanup := applyMaskOverlays(ctx, opts.Mask, maskColor)
		cleanups = append(cleanups, cleanup)
	}

	if opts.WaitBeforeCapture > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.WaitBeforeCapture):
		}
	}

	params := map[string]any{}
	if opts.Format != "" {
		params["format"] = opts.Format
	}
	if opts.Quality > 0 {
		params["quality"] = opts.Quality
	}
	clip, err := normalizeScreenshotClip(opts.Clip)
	if err != nil {
		return nil, err
	}
	if clip != nil {
		clipScale := clip.Scale
		if clipScale <= 0 && opts.Scale == ScreenshotScaleCSS {
			clipScale = computeScreenshotScale(ctx, p, opts.Scale)
		}
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
	if opts.OmitBackground {
		params["fromSurface"] = true
		cleanups = append(cleanups, setTransparentBackground(ctx, p.mainSession))
	}

	var res struct {
		Data string `json:"data"`
	}
	if err := p.mainSession.Send(ctx, "Page.captureScreenshot", params, &res); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(res.Data)
}

func (p *Page) Snapshot(ctx context.Context) (*SnapshotResult, error) {
	return snappkg.Capture(ctx, newSnapshotPageAdapter(p), SnapshotOptions{})
}

func (p *Page) SnapshotWithOptions(ctx context.Context, opts SnapshotOptions) (*SnapshotResult, error) {
	return snappkg.Capture(ctx, newSnapshotPageAdapter(p), opts)
}

func (p *Page) ActiveElementXPath(ctx context.Context) (string, error) {
	return snappkg.ActiveElementXPath(ctx, newSnapshotPageAdapter(p)), nil
}

func (p *Page) ResolveXPathForLocation(ctx context.Context, x, y int) (*ResolvedLocation, error) {
	return snappkg.ResolveXPathForLocation(ctx, newSnapshotPageAdapter(p), x, y), nil
}

func (p *Page) DiffSnapshotTrees(previous, next string) string {
	return snappkg.DiffTrees(previous, next)
}

func (p *Page) Locator(selector string) *Locator {
	return p.MainFrame().Locator(selector)
}

func (p *Page) DeepLocator(selector string) *DeepLocator {
	return p.MainFrame().DeepLocator(selector)
}

func (p *Page) FrameLocator(selector string) *FrameLocator {
	return &FrameLocator{page: p, root: p.MainFrame(), selector: selector}
}

func (p *Page) snapshotSessionsLocked() []sessionLike {
	sessions := make([]sessionLike, 0, len(p.sessions)+1)
	sessions = append(sessions, p.mainSession)
	for _, session := range p.sessions {
		if session == p.mainSession {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions
}

func buildSelectorInvocation(selector string, state SelectorState, pierceShadow bool) string {
	return fmt.Sprintf(`(() => {
		%s
		const elements = queryAll(document, %q, %t);
		const visible = (el) => {
			if (!el) return false;
			const style = getComputedStyle(el);
			if (style.visibility === 'hidden' || style.display === 'none') return false;
			const rect = el.getBoundingClientRect();
			return rect.width > 0 && rect.height > 0;
		};
		switch (%q) {
		case "attached":
			return elements.length > 0;
		case "detached":
			return elements.length === 0;
		case "hidden":
			return elements.length === 0 || !visible(elements[0]);
		default:
			return elements.length > 0 && visible(elements[0]);
		}
	})()`, selectorQueryPrelude(), selector, pierceShadow, string(state))
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func (p *Page) SendCDP(ctx context.Context, method string, params any, result any) error {
	return p.mainSession.Send(ctx, method, params, result)
}

func (p *Page) SendCDPToFrame(ctx context.Context, frameID, method string, params any, result any) error {
	session := p.sessionForFrame(frameID)
	return session.Send(ctx, method, params, result)
}

func (p *Page) Tap(ctx context.Context, x, y float64) error {
	return newTouch(p).Tap(ctx, x, y)
}

func (p *Page) Touch() *Touch {
	return newTouch(p)
}
