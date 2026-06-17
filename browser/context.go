package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PolymuxOrg/midas/internal/cdp"
	"github.com/PolymuxOrg/midas/internal/debug"
)

const defaultFirstTopLevelPageTimeout = 5 * time.Second

type Context struct {
	conn connLike

	mu                   sync.RWMutex
	sessionInit          map[string]struct{}
	targetSessionWired   map[string]struct{}
	pagesByTarget        map[string]*Page
	mainFrameToTarget    map[string]string
	sessionOwnerPage     map[string]*Page
	frameOwnerPage       map[string]*Page
	pendingOOPIFByFrame  map[string]string
	createdAtByTarget    map[string]time.Time
	typeByTarget         map[string]string
	pageOrder            []string
	pendingCreatedTarget map[string]string
	lastPopupSignalAt    time.Time
	initScripts          []string
	extraHTTPHeaders     map[string]string
}

func Connect(ctx context.Context, wsURL string, opts ConnectOptions) (*Context, error) {
	debug.Printf("cdp dialing %s", debug.WSSummary(wsURL))
	conn, err := cdp.Dial(ctx, wsURL, cdp.DialOptions{
		Headers:   opts.Headers,
		UserAgent: opts.UserAgent,
	})
	if err != nil {
		debug.Printf("cdp dial failed: %v", err)
		return nil, err
	}
	debug.Printf("cdp connected")

	bctx := newContext(&cdpRootConn{conn: conn})
	if err := bctx.bootstrap(ctx); err != nil {
		debug.Printf("bootstrap failed: %v", err)
		_ = conn.Close()
		return nil, err
	}
	debug.Printf("browser bootstrap complete")

	ensure := true
	if !opts.EnsureFirstTopLevelPage && opts.FirstTopLevelPageTimeoutMs == 0 {
		ensure = false
	}
	if opts.EnsureFirstTopLevelPage {
		ensure = true
	}
	if ensure {
		timeout := defaultFirstTopLevelPageTimeout
		if opts.FirstTopLevelPageTimeoutMs > 0 {
			timeout = time.Duration(opts.FirstTopLevelPageTimeoutMs) * time.Millisecond
		}
		debug.Printf("ensuring first top-level page (timeout %s)", timeout)
		if err := bctx.ensureFirstTopLevelPage(ctx, timeout); err != nil {
			debug.Printf("ensure first top-level page failed: %v", err)
			_ = conn.Close()
			return nil, err
		}
		debug.Printf("first top-level page ready")
	}

	return bctx, nil
}

func newContext(conn connLike) *Context {
	return &Context{
		conn:                 conn,
		sessionInit:          make(map[string]struct{}),
		targetSessionWired:   make(map[string]struct{}),
		pagesByTarget:        make(map[string]*Page),
		mainFrameToTarget:    make(map[string]string),
		sessionOwnerPage:     make(map[string]*Page),
		frameOwnerPage:       make(map[string]*Page),
		pendingOOPIFByFrame:  make(map[string]string),
		createdAtByTarget:    make(map[string]time.Time),
		typeByTarget:         make(map[string]string),
		pendingCreatedTarget: make(map[string]string),
	}
}

func (c *Context) bootstrap(ctx context.Context) error {
	c.conn.On("Target.attachedToTarget", func(params json.RawMessage) {
		var evt struct {
			SessionID  string         `json:"sessionId"`
			TargetInfo cdp.TargetInfo `json:"targetInfo"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		// Must run async: synchronous CDP calls here deadlock the read loop.
		info, sid := evt.TargetInfo, evt.SessionID
		go func() {
			_ = c.onAttachedToTarget(context.Background(), info, sid)
		}()
	})
	c.conn.On("Target.detachedFromTarget", func(params json.RawMessage) {
		var evt struct {
			SessionID string `json:"sessionId"`
			TargetID  string `json:"targetId"`
		}
		if json.Unmarshal(params, &evt) == nil {
			c.onDetachedFromTarget(evt.SessionID, evt.TargetID)
		}
	})
	c.conn.On("Target.targetDestroyed", func(params json.RawMessage) {
		var evt struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(params, &evt) == nil {
			c.cleanupByTarget(evt.TargetID)
		}
	})
	c.conn.On("Target.targetCreated", func(params json.RawMessage) {
		var evt struct {
			TargetInfo struct {
				Type     string `json:"type"`
				OpenerID string `json:"openerId"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(params, &evt) == nil && evt.TargetInfo.Type == "page" && evt.TargetInfo.OpenerID != "" {
			c.notePopupSignal()
		}
	})

	if err := c.conn.EnableAutoAttach(ctx); err != nil {
		return err
	}

	targets, err := c.conn.GetTargets(ctx)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.Attached {
			continue
		}
		_, _ = c.conn.AttachToTarget(ctx, target.TargetID)
	}

	// Only wait for targets onAttachedToTarget will actually build into pages.
	// A non-injectable top-level page (the browser's initial chrome://newtab)
	// is counted by isTopLevelPage but skipped by isNonWebTarget in the attach
	// handler, so including it here makes bootstrap wait out the full timeout
	// for a page that never registers.
	var topLevel []string
	for _, target := range targets {
		if isTopLevelPage(target) && !isNonWebTarget(target) {
			topLevel = append(topLevel, target.TargetID)
		}
	}
	return c.waitForInitialTopLevelTargets(ctx, topLevel, 3*time.Second)
}

func (c *Context) ensureFirstTopLevelPage(ctx context.Context, timeout time.Duration) error {
	if c.hasTopLevelPage() {
		return nil
	}

	// Decide whether to wait for a page to appear or create one immediately.
	// bootstrap has already awaited any adoptable (web) top-level pages, so if
	// none registered, check the live target list: when the browser is parked
	// on a non-adoptable page (e.g. its initial chrome://newtab) and no web
	// page is present, no page will ever register — create about:blank now
	// instead of polling the full timeout. Only wait when there is genuinely no
	// top-level page yet (the connect/launch race) or a web page is still
	// registering.
	parkedOnNonWeb := false
	if targets, err := c.conn.GetTargets(ctx); err == nil {
		hasWeb, hasNonWeb := false, false
		for _, t := range targets {
			if !isTopLevelPage(t) {
				continue
			}
			if isNonWebTarget(t) {
				hasNonWeb = true
			} else {
				hasWeb = true
			}
		}
		parkedOnNonWeb = hasNonWeb && !hasWeb
	}

	if !parkedOnNonWeb {
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := c.waitForFirstTopLevelPage(waitCtx); err == nil {
			return nil
		} else if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}

	_, err := c.NewPage(ctx, "about:blank")
	return err
}

func (c *Context) hasTopLevelPage() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for targetID, targetType := range c.typeByTarget {
		if targetType == "page" && c.pagesByTarget[targetID] != nil {
			return true
		}
	}
	return false
}

func (c *Context) waitForFirstTopLevelPage(ctx context.Context) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c.hasTopLevelPage() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Context) waitForInitialTopLevelTargets(ctx context.Context, targetIDs []string, timeout time.Duration) error {
	if len(targetIDs) == 0 {
		return nil
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pending := make(map[string]struct{}, len(targetIDs))
	for _, id := range targetIDs {
		pending[id] = struct{}{}
	}
	total := len(pending)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.RLock()
		for id := range pending {
			if c.pagesByTarget[id] != nil {
				delete(pending, id)
			}
		}
		c.mu.RUnlock()
		// Return once every target registered, OR once at least one has — a
		// usable top-level page is all bootstrap needs, and waiting for ALL of
		// them lets a single stuck/internal page (e.g. a leftover
		// chrome://newtab whose createPage never completes) stall the whole
		// connect for the full timeout. Late tabs still appear via Pages(),
		// which re-queries the live registry.
		if len(pending) == 0 || len(pending) < total {
			return nil
		}
		select {
		case <-deadlineCtx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (c *Context) onAttachedToTarget(ctx context.Context, info cdp.TargetInfo, sessionID string) error {
	if isNonWebTarget(info) {
		if session, ok := c.conn.GetSession(sessionID); ok {
			_ = session.Send(ctx, "Runtime.runIfWaitingForDebugger", nil, nil)
		}
		return nil
	}
	session, ok := c.conn.GetSession(sessionID)
	if !ok {
		return nil
	}

	c.mu.Lock()
	if _, exists := c.sessionInit[sessionID]; exists {
		c.mu.Unlock()
		return nil
	}
	c.sessionInit[sessionID] = struct{}{}
	c.mu.Unlock()

	c.installTargetSessionListeners(session)

	_ = session.Send(ctx, "Page.enable", nil, nil)
	_ = session.Send(ctx, "Runtime.enable", nil, nil)
	_ = session.Send(ctx, "Target.setAutoAttach", map[string]any{
		"autoAttach":             true,
		"waitForDebuggerOnStart": true,
		"flatten":                true,
	}, nil)
	_ = session.Send(ctx, "Network.enable", nil, nil)

	_ = InstallPiercer(ctx, session)

	c.mu.RLock()
	initScripts := append([]string(nil), c.initScripts...)
	headers := cloneStringMap(c.extraHTTPHeaders)
	c.mu.RUnlock()
	for _, source := range initScripts {
		_ = session.Send(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
			"source": source,
		}, nil)
	}
	if len(headers) > 0 {
		_ = session.Send(ctx, "Network.setExtraHTTPHeaders", map[string]any{
			"headers": headers,
		}, nil)
	}

	defer func() {
		_ = session.Send(context.Background(), "Runtime.runIfWaitingForDebugger", nil, nil)
	}()

	if isTopLevelPage(info) {
		page, err := createPage(ctx, c.conn, session, info.TargetID)
		if err != nil {
			return err
		}

		c.mu.Lock()
		c.pagesByTarget[info.TargetID] = page
		c.mainFrameToTarget[page.mainFrameId()] = info.TargetID
		c.sessionOwnerPage[sessionID] = page
		c.frameOwnerPage[page.mainFrameId()] = page
		c.typeByTarget[info.TargetID] = "page"
		if _, ok := c.createdAtByTarget[info.TargetID]; !ok {
			c.createdAtByTarget[info.TargetID] = time.Now()
		}
		pendingURL := c.pendingCreatedTarget[info.TargetID]
		delete(c.pendingCreatedTarget, info.TargetID)
		c.removeFromOrderLocked(info.TargetID)
		c.pageOrder = append(c.pageOrder, info.TargetID)
		c.mu.Unlock()

		if pendingURL != "" {
			page.seedCurrentURL(pendingURL)
		} else if info.URL != "" {
			page.seedCurrentURL(info.URL)
		}
		page.seedDefaults(initScripts, headers)

		c.installFrameEventBridges(sessionID, page)
		return nil
	}

	var treeRes struct {
		FrameTree frameNode `json:"frameTree"`
	}
	if err := session.Send(ctx, "Page.getFrameTree", nil, &treeRes); err != nil {
		return nil
	}
	childMainID := treeRes.FrameTree.Frame.ID
	owner := c.resolveOwnerPage(childMainID)
	if owner != nil {
		owner.adoptOopifSession(session, childMainID)
		c.mu.Lock()
		c.sessionOwnerPage[sessionID] = owner
		c.mu.Unlock()
		c.installFrameEventBridges(sessionID, owner)
		return nil
	}

	c.mu.Lock()
	c.pendingOOPIFByFrame[childMainID] = sessionID
	c.mu.Unlock()
	return nil
}

func (c *Context) installTargetSessionListeners(session sessionLike) {
	sessionID := session.ID()
	c.mu.Lock()
	if _, ok := c.targetSessionWired[sessionID]; ok {
		c.mu.Unlock()
		return
	}
	c.targetSessionWired[sessionID] = struct{}{}
	c.mu.Unlock()

	session.On("Target.attachedToTarget", func(params json.RawMessage) {
		var evt struct {
			SessionID  string         `json:"sessionId"`
			TargetInfo cdp.TargetInfo `json:"targetInfo"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		info, sid := evt.TargetInfo, evt.SessionID
		go func() {
			_ = c.onAttachedToTarget(context.Background(), info, sid)
		}()
	})
	session.On("Target.detachedFromTarget", func(params json.RawMessage) {
		var evt struct {
			SessionID string `json:"sessionId"`
			TargetID  string `json:"targetId"`
		}
		if json.Unmarshal(params, &evt) == nil {
			c.onDetachedFromTarget(evt.SessionID, evt.TargetID)
		}
	})
	session.On("Target.targetDestroyed", func(params json.RawMessage) {
		var evt struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(params, &evt) == nil {
			c.cleanupByTarget(evt.TargetID)
		}
	})
}

func (c *Context) installFrameEventBridges(sessionID string, owner *Page) {
	session, ok := c.conn.GetSession(sessionID)
	if !ok {
		return
	}
	session.On("Page.frameAttached", func(params json.RawMessage) {
		var evt frameAttachedEvent
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		owner.onFrameAttached(evt.FrameID, evt.ParentFrameID, session)
		c.refreshFrameOwnerMetadata(session, owner, evt.FrameID)

		c.mu.Lock()
		pendingSessionID := c.pendingOOPIFByFrame[evt.FrameID]
		if pendingSessionID != "" {
			delete(c.pendingOOPIFByFrame, evt.FrameID)
		}
		c.frameOwnerPage[evt.FrameID] = owner
		c.mu.Unlock()

		if pendingSessionID != "" {
			if child, ok := c.conn.GetSession(pendingSessionID); ok {
				owner.adoptOopifSession(child, evt.FrameID)
				c.mu.Lock()
				c.sessionOwnerPage[pendingSessionID] = owner
				c.mu.Unlock()
				c.installFrameEventBridges(pendingSessionID, owner)
			}
		}

		if evt.ParentFrameID == "" {
			c.mu.Lock()
			if targetID := c.findTargetIDByPage(owner); targetID != "" {
				c.mainFrameToTarget[owner.mainFrameId()] = targetID
			}
			c.frameOwnerPage[owner.mainFrameId()] = owner
			c.mu.Unlock()
		}
	})
	session.On("Page.frameDetached", func(params json.RawMessage) {
		var evt frameDetachedEvent
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		owner.onFrameDetached(evt.FrameID, defaultDetachReason(evt.Reason))
		if evt.Reason != "swap" {
			c.mu.Lock()
			delete(c.frameOwnerPage, evt.FrameID)
			c.mu.Unlock()
		}
	})
	session.On("Page.frameNavigated", func(params json.RawMessage) {
		var evt frameNavigatedEvent
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		owner.onFrameNavigated(evt.Frame, session)
		if evt.Frame.ParentID != "" {
			c.refreshFrameOwnerMetadata(session, owner, evt.Frame.ID)
		}
	})
	session.On("Page.navigatedWithinDocument", func(params json.RawMessage) {
		var evt navigatedWithinDocumentEvent
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		owner.onNavigatedWithinDocument(evt.FrameID, evt.URL, session)
	})
}

func (c *Context) refreshFrameOwnerMetadata(session sessionLike, owner *Page, frameID string) {
	if session == nil || owner == nil || frameID == "" {
		return
	}

	var ownerRes struct {
		BackendNodeID int `json:"backendNodeId"`
	}
	if err := session.Send(context.Background(), "DOM.getFrameOwner", map[string]any{
		"frameId": frameID,
	}, &ownerRes); err != nil || ownerRes.BackendNodeID == 0 {
		return
	}

	var resolveRes struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := session.Send(context.Background(), "DOM.resolveNode", map[string]any{
		"backendNodeId": ownerRes.BackendNodeID,
	}, &resolveRes); err != nil || resolveRes.Object.ObjectID == "" {
		owner.setFrameOwnerMetadata(frameID, ownerRes.BackendNodeID, "")
		return
	}
	defer func() {
		_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
			"objectId": resolveRes.Object.ObjectID,
		}, nil)
	}()

	var xpathRes struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := session.Send(context.Background(), "Runtime.callFunctionOn", map[string]any{
		"objectId":      resolveRes.Object.ObjectID,
		"returnByValue": true,
		"functionDeclaration": `function() {
			const sameTagIndex = (el) => {
				let index = 1;
				for (let sib = el.previousElementSibling; sib; sib = sib.previousElementSibling) {
					if (sib.tagName === el.tagName) index++;
				}
				return index;
			};
			const segments = [];
			let node = this;
			while (node && node.nodeType === Node.ELEMENT_NODE) {
				segments.unshift("/" + node.tagName.toLowerCase() + "[" + sameTagIndex(node) + "]");
				node = node.parentElement;
			}
			return segments.join("") || "";
		}`,
	}, &xpathRes); err != nil {
		owner.setFrameOwnerMetadata(frameID, ownerRes.BackendNodeID, "")
		return
	}

	owner.setFrameOwnerMetadata(frameID, ownerRes.BackendNodeID, xpathRes.Result.Value)
}

func (c *Context) onDetachedFromTarget(sessionID, targetID string) {
	c.mu.RLock()
	owner := c.sessionOwnerPage[sessionID]
	c.mu.RUnlock()
	if owner != nil {
		owner.detachOopifSession(sessionID)
		c.mu.Lock()
		delete(c.sessionOwnerPage, sessionID)
		c.mu.Unlock()
	}
	if targetID != "" {
		c.cleanupByTarget(targetID)
	}
	c.mu.Lock()
	for frameID, sid := range c.pendingOOPIFByFrame {
		if sid == sessionID {
			delete(c.pendingOOPIFByFrame, frameID)
		}
	}
	delete(c.targetSessionWired, sessionID)
	delete(c.sessionInit, sessionID)
	c.mu.Unlock()
}

func (c *Context) cleanupByTarget(targetID string) {
	c.mu.Lock()
	page := c.pagesByTarget[targetID]
	if page == nil {
		c.mu.Unlock()
		return
	}
	mainID := page.mainFrameId()
	delete(c.mainFrameToTarget, mainID)
	delete(c.frameOwnerPage, mainID)
	for sid, p := range c.sessionOwnerPage {
		if p == page {
			delete(c.sessionOwnerPage, sid)
		}
	}
	for fid := range c.pendingOOPIFByFrame {
		owner := c.frameOwnerPage[fid]
		if owner == nil || owner == page {
			delete(c.pendingOOPIFByFrame, fid)
		}
	}
	c.removeFromOrderLocked(targetID)
	delete(c.pagesByTarget, targetID)
	delete(c.createdAtByTarget, targetID)
	delete(c.typeByTarget, targetID)
	delete(c.pendingCreatedTarget, targetID)
	c.mu.Unlock()
}

func (c *Context) ActivePage() *Page {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.pageOrder) - 1; i >= 0; i-- {
		targetID := c.pageOrder[i]
		if page := c.pagesByTarget[targetID]; page != nil {
			return page
		}
		c.pageOrder = append(c.pageOrder[:i], c.pageOrder[i+1:]...)
	}
	var newest *Page
	var newestTime time.Time
	for targetID, page := range c.pagesByTarget {
		if createdAt := c.createdAtByTarget[targetID]; newest == nil || createdAt.After(newestTime) {
			newest = page
			newestTime = createdAt
		}
	}
	return newest
}

func (c *Context) Pages() []*Page {
	c.mu.RLock()
	rows := make([]struct {
		page    *Page
		created time.Time
	}, 0, len(c.pagesByTarget))
	for targetID, page := range c.pagesByTarget {
		if c.typeByTarget[targetID] == "page" {
			rows = append(rows, struct {
				page    *Page
				created time.Time
			}{page: page, created: c.createdAtByTarget[targetID]})
		}
	}
	c.mu.RUnlock()
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].created.Before(rows[i].created) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	out := make([]*Page, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.page)
	}
	return out
}

func (c *Context) NewPage(ctx context.Context, url string) (*Page, error) {
	targetURL := url
	if targetURL == "" {
		targetURL = "about:blank"
	}
	var res struct {
		TargetID string `json:"targetId"`
	}
	if err := c.conn.Send(ctx, "Target.createTarget", map[string]any{
		"url": "about:blank",
	}, &res); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.pendingCreatedTarget[res.TargetID] = "about:blank"
	c.mu.Unlock()
	_ = c.conn.Send(ctx, "Target.activateTarget", map[string]any{"targetId": res.TargetID}, nil)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.RLock()
		page := c.pagesByTarget[res.TargetID]
		c.mu.RUnlock()
		if page != nil {
			if targetURL != "about:blank" {
				page.seedCurrentURL(targetURL)
				_ = page.sendCDP(ctx, "Page.navigate", map[string]any{"url": targetURL}, nil)
			}
			return page, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("newPage: target not attached (%s)", res.TargetID)
}

func (c *Context) AwaitActivePage(ctx context.Context, timeout time.Duration) (*Page, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	immediate := c.ActivePage()
	c.mu.RLock()
	hasRecentPopup := time.Since(c.lastPopupSignalAt) <= 300*time.Millisecond
	c.mu.RUnlock()
	if !hasRecentPopup && immediate != nil {
		return immediate, nil
	}
	for time.Now().Before(deadline) {
		if page := c.ActivePage(); page != nil {
			return page, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	if immediate != nil {
		return immediate, nil
	}
	return nil, errors.New("awaitActivePage: no page available")
}

func (c *Context) Close() error {
	c.mu.Lock()
	c.pagesByTarget = make(map[string]*Page)
	c.mainFrameToTarget = make(map[string]string)
	c.sessionOwnerPage = make(map[string]*Page)
	c.frameOwnerPage = make(map[string]*Page)
	c.pendingOOPIFByFrame = make(map[string]string)
	c.createdAtByTarget = make(map[string]time.Time)
	c.typeByTarget = make(map[string]string)
	c.pendingCreatedTarget = make(map[string]string)
	c.pageOrder = nil
	c.mu.Unlock()
	return c.conn.Close()
}

func (c *Context) pushActive(targetID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeFromOrderLocked(targetID)
	c.pageOrder = append(c.pageOrder, targetID)
}

func (c *Context) removeFromOrderLocked(targetID string) {
	for i, id := range c.pageOrder {
		if id == targetID {
			c.pageOrder = append(c.pageOrder[:i], c.pageOrder[i+1:]...)
			return
		}
	}
}

func (c *Context) notePopupSignal() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastPopupSignalAt = time.Now()
}

func (c *Context) resolveOwnerPage(frameID string) *Page {
	c.mu.RLock()
	if page := c.frameOwnerPage[frameID]; page != nil {
		c.mu.RUnlock()
		return page
	}
	pages := make([]*Page, 0, len(c.pagesByTarget))
	for _, page := range c.pagesByTarget {
		pages = append(pages, page)
	}
	c.mu.RUnlock()
	for _, page := range pages {
		if hasFrame(page.asProtocolFrameTree(page.mainFrameId()), frameID) {
			return page
		}
	}
	return nil
}

func (c *Context) findTargetIDByPage(page *Page) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for targetID, existing := range c.pagesByTarget {
		if existing == page {
			return targetID
		}
	}
	return ""
}

func hasFrame(tree frameNode, frameID string) bool {
	if tree.Frame.ID == frameID {
		return true
	}
	for _, child := range tree.ChildFrames {
		if hasFrame(child, frameID) {
			return true
		}
	}
	return false
}

func hasInjectableDOM(url string) bool {
	if url == "" {
		return true
	}
	if url == "about:blank" || url == "about:srcdoc" || strings.HasPrefix(url, "about:blank#") {
		return true
	}
	for _, prefix := range []string{"http://", "https://", "data:", "blob:", "file://", "filesystem:"} {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

func isNonWebTarget(info cdp.TargetInfo) bool {
	return (info.Type != "page" && info.Type != "iframe") || !hasInjectableDOM(info.URL)
}

func isTopLevelPage(info cdp.TargetInfo) bool {
	return info.Type == "page" && info.Subtype != "iframe"
}

func defaultDetachReason(reason string) string {
	if reason == "" {
		return "remove"
	}
	return reason
}
