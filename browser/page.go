package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/PolymuxOrg/midas/humanize"
)

var errLoadStateTimeout = errors.New("page load state timeout")

type DialogListener func(dialog *Dialog)

// MousePosListener is invoked whenever the page dispatches a CDP mouse event
// (humanized or direct). Listeners receive viewport CSS pixel coordinates and
// must be safe for concurrent invocation from any goroutine.
type MousePosListener func(x, y float64)

type Page struct {
	conn        connLike
	mainSession sessionLike
	targetID    string
	registry    *FrameRegistry
	network     *NetworkManager
	execCtx     *executionContextRegistry

	mu                    sync.RWMutex
	sessions              map[string]sessionLike
	frameOrdinals         map[string]int
	nextFrameOrdinal      int
	currentURL            string
	initScripts           []string
	extraHTTPHeaders      map[string]string
	consoleListeners      map[int]ConsoleListener
	nextConsoleListenerID int
	dialogListeners       map[int]DialogListener
	nextDialogListenerID  int
	navSeq                int64
	latestNavID           int64
	selectorHelpers       map[string]map[int64]struct{}

	humanCfg         *humanize.Config
	humanCursorX     float64
	humanCursorY     float64
	humanCursorReady bool

	mousePosListeners       map[int]MousePosListener
	nextMousePosListenerID  int
}

func newPage(conn connLike, session sessionLike, targetID string, tree frameNode) *Page {
	page := &Page{
		conn:        conn,
		mainSession: session,
		targetID:    targetID,
		registry:    NewFrameRegistry(targetID, tree.Frame.ID),
		network:     NewNetworkManager(),
		execCtx:     newExecutionContextRegistry(),
		sessions:    make(map[string]sessionLike),
		frameOrdinals: map[string]int{
			tree.Frame.ID: 0,
		},
		nextFrameOrdinal: 1,
		currentURL:       tree.Frame.URL,
		consoleListeners:  make(map[int]ConsoleListener),
		dialogListeners:   make(map[int]DialogListener),
		selectorHelpers:   make(map[string]map[int64]struct{}),
		mousePosListeners: make(map[int]MousePosListener),
	}
	if session.ID() != "" {
		page.sessions[session.ID()] = session
	}
	page.registry.SeedFromFrameTree(session.ID(), tree)
	page.network.TrackSession(session)
	page.execCtx.AttachSession(session)
	if page.currentURL == "" {
		page.currentURL = "about:blank"
	}
	page.installConsoleBridge(session)
	page.installDialogHandler(session)
	return page
}

func createPage(ctx context.Context, conn connLike, session sessionLike, targetID string) (*Page, error) {
	_ = session.Send(ctx, "Page.enable", nil, nil)
	_ = session.Send(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, nil)

	var res struct {
		FrameTree frameNode `json:"frameTree"`
	}
	if err := session.Send(ctx, "Page.getFrameTree", nil, &res); err != nil {
		return nil, err
	}
	return newPage(conn, session, targetID, res.FrameTree), nil
}

func (p *Page) TargetID() string {
	return p.targetID
}

func (p *Page) targetId() string {
	return p.targetID
}

func (p *Page) MainFrameID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.registry.MainFrameID()
}

func (p *Page) mainFrameId() string {
	return p.MainFrameID()
}

func (p *Page) URL() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.currentURL
}

func (p *Page) seedCurrentURL(url string) {
	if url == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentURL = url
}

func (p *Page) sendCDP(ctx context.Context, method string, params any, result any) error {
	return p.mainSession.Send(ctx, method, params, result)
}

func (p *Page) Goto(ctx context.Context, url string) (*Response, error) {
	return p.doGoto(ctx, url, LoadStateDOMContentLoaded, 15*time.Second)
}

func (p *Page) doGoto(ctx context.Context, url string, waitUntil LoadState, timeout time.Duration) (*Response, error) {
	navID := p.beginNavigationCommand()
	tracker := NewNavigationResponseTracker(p, p.mainSession, navID)
	watcher := NewLifecycleWatcher(p, p.mainSession, p.network, waitUntil, timeout, navID)
	defer tracker.Dispose()
	defer watcher.Dispose()

	var res pageNavigateResult
	if err := p.mainSession.Send(ctx, "Page.navigate", map[string]any{"url": url}, &res); err != nil {
		return nil, err
	}
	if res.ErrorText != "" {
		return nil, errors.New(res.ErrorText)
	}
	if res.LoaderID != "" {
		tracker.SetExpectedLoaderID(res.LoaderID)
		watcher.SetExpectedLoaderID(res.LoaderID)
	}
	p.seedCurrentURL(url)
	if waitUntil == "" {
		return tracker.NavigationCompleted(ctx), nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := watcher.Wait(waitCtx); err != nil {
		return nil, err
	}
	return tracker.NavigationCompleted(waitCtx), nil
}

func (p *Page) Reload(ctx context.Context) (*Response, error) {
	return p.reload(ctx, LoadStateDOMContentLoaded, 15*time.Second, false)
}

func (p *Page) reload(ctx context.Context, waitUntil LoadState, timeout time.Duration, ignoreCache bool) (*Response, error) {
	navID := p.beginNavigationCommand()
	tracker := NewNavigationResponseTracker(p, p.mainSession, navID)
	tracker.ExpectNavigationWithoutKnownLoader()
	var watcher *LifecycleWatcher
	if waitUntil != "" {
		watcher = NewLifecycleWatcher(p, p.mainSession, p.network, waitUntil, timeout, navID)
		defer watcher.Dispose()
	}
	defer tracker.Dispose()

	if err := p.mainSession.Send(ctx, "Page.reload", map[string]any{
		"ignoreCache": ignoreCache,
	}, nil); err != nil {
		return nil, err
	}
	if waitUntil == "" {
		return tracker.NavigationCompleted(ctx), nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := watcher.Wait(waitCtx); err != nil {
		return nil, err
	}
	return tracker.NavigationCompleted(waitCtx), nil
}

func (p *Page) Close(ctx context.Context) error {
	p.network.Dispose()
	return p.conn.Send(ctx, "Target.closeTarget", map[string]any{"targetId": p.targetID}, nil)
}

func (p *Page) asProtocolFrameTree(rootMainFrameID string) frameNode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneJSON(p.registry.AsFrameTree(rootMainFrameID))
}

func (p *Page) frameForId(frameID string) *Frame {
	return p.Frame(frameID)
}

func (p *Page) listAllFrameIds() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.registry.ListAllFrames()...)
}

func (p *Page) getSessionById(sessionID string) sessionLike {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if sessionID == "" {
		return p.mainSession
	}
	return p.sessions[sessionID]
}

func (p *Page) getSessionForFrame(frameID string) sessionLike {
	return p.sessionForFrame(frameID)
}

func (p *Page) getOrdinal(frameID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ordinal, ok := p.frameOrdinals[frameID]; ok {
		return ordinal
	}
	ordinal := p.nextFrameOrdinal
	p.nextFrameOrdinal++
	p.frameOrdinals[frameID] = ordinal
	return ordinal
}

func (p *Page) onFrameAttached(frameID, parentID string, session sessionLike) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registry.OnFrameAttached(frameID, parentID, session.ID())
	if _, ok := p.frameOrdinals[frameID]; !ok {
		p.frameOrdinals[frameID] = p.nextFrameOrdinal
		p.nextFrameOrdinal++
	}
}

func (p *Page) setFrameOwnerMetadata(frameID string, backendNodeID int, xpath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if backendNodeID > 0 {
		p.registry.SetOwnerBackendNodeID(frameID, backendNodeID)
	}
	if xpath != "" {
		p.registry.SetOwnerXPath(frameID, xpath)
	}
}

func (p *Page) onFrameDetached(frameID, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registry.OnFrameDetached(frameID, reason)
	delete(p.frameOrdinals, frameID)
}

func (p *Page) onFrameNavigated(frame cdpFrame, session sessionLike) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registry.OnFrameNavigated(frame, session.ID())
	if _, ok := p.frameOrdinals[frame.ID]; !ok {
		p.frameOrdinals[frame.ID] = p.nextFrameOrdinal
		p.nextFrameOrdinal++
	}
	if frame.ID == p.registry.MainFrameID() && frame.URL != "" {
		p.currentURL = frame.URL
	}
}

func (p *Page) onNavigatedWithinDocument(frameID, url string, session sessionLike) {
	if url == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registry.OnNavigatedWithinDocument(frameID, url, session.ID())
	if frameID == p.registry.MainFrameID() {
		p.currentURL = url
	}
}

func (p *Page) adoptOopifSession(child sessionLike, childMainFrameID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if child.ID() != "" {
		p.sessions[child.ID()] = child
	}
	p.network.TrackSession(child)
	p.execCtx.AttachSession(child)
	p.registry.AdoptChildSession(child.ID(), childMainFrameID)
	p.installConsoleBridge(child)
	p.installDialogHandler(child)
	for _, source := range p.initScripts {
		_ = child.Send(context.Background(), "Page.addScriptToEvaluateOnNewDocument", map[string]any{
			"source": source,
		}, nil)
	}
	if len(p.extraHTTPHeaders) > 0 {
		_ = child.Send(context.Background(), "Network.enable", nil, nil)
		_ = child.Send(context.Background(), "Network.setExtraHTTPHeaders", map[string]any{
			"headers": p.extraHTTPHeaders,
		}, nil)
	}
}

func (p *Page) detachOopifSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, frameID := range p.registry.FramesForSession(sessionID) {
		p.registry.OnFrameDetached(frameID, "remove")
	}
	delete(p.sessions, sessionID)
	delete(p.selectorHelpers, sessionID)
	p.network.UntrackSession(sessionID)
	p.execCtx.DetachSession(sessionID)
}

func (p *Page) beginNavigationCommand() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.navSeq++
	p.latestNavID = p.navSeq
	return p.latestNavID
}

func (p *Page) isCurrentNavigationCommand(navID int64) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.latestNavID == navID
}

func (p *Page) waitForMainLoadState(ctx context.Context, state LoadState, navigationID int64) error {
	_ = p.mainSession.Send(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, nil)

	if (state == LoadStateDOMContentLoaded || state == LoadStateLoad) && p.isMainLoadStateReady(ctx, state) {
		return nil
	}

	done := make(chan error, 1)
	var once sync.Once
	finish := func(err error) {
		once.Do(func() {
			done <- err
		})
	}

	unsubLifecycle := p.mainSession.On("Page.lifecycleEvent", func(params json.RawMessage) {
		var evt lifecycleEvent
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		if !p.isCurrentNavigationCommand(navigationID) {
			finish(errors.New("navigation superseded by a new request"))
			return
		}
		if evt.Name != lifecycleName(state) {
			return
		}
		if evt.FrameID == p.mainFrameId() {
			finish(nil)
		}
	})
	unsubDOMContentLoaded := p.mainSession.On("Page.domContentEventFired", func(_ json.RawMessage) {
		if state == LoadStateDOMContentLoaded {
			finish(nil)
		}
	})
	unsubLoad := p.mainSession.On("Page.loadEventFired", func(_ json.RawMessage) {
		if state == LoadStateLoad {
			finish(nil)
		}
	})
	defer unsubLifecycle()
	defer unsubDOMContentLoaded()
	defer unsubLoad()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w: waitForMainLoadState(%s)", errLoadStateTimeout, state)
			}
			return ctx.Err()
		case <-ticker.C:
			if !p.isCurrentNavigationCommand(navigationID) {
				return errors.New("navigation superseded by a new request")
			}
			if (state == LoadStateDOMContentLoaded || state == LoadStateLoad) && p.isMainLoadStateReady(ctx, state) {
				return nil
			}
		}
	}
}

func (p *Page) WaitForMainLoadState(ctx context.Context, state LoadState) error {
	return p.waitForMainLoadState(ctx, state, p.latestNavID)
}

func (p *Page) isMainLoadStateReady(ctx context.Context, state LoadState) bool {
	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := p.mainSession.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    "document.readyState",
		"returnByValue": true,
	}, &res); err != nil {
		return false
	}
	ready := res.Result.Value
	switch state {
	case LoadStateDOMContentLoaded:
		return ready == "interactive" || ready == "complete"
	case LoadStateLoad:
		return ready == "complete"
	default:
		return false
	}
}

func lifecycleName(state LoadState) string {
	switch state {
	case LoadStateLoad:
		return "load"
	case LoadStateDOMContentLoaded:
		return "DOMContentLoaded"
	case LoadStateNetworkIdle:
		return "networkIdle"
	default:
		return string(state)
	}
}
