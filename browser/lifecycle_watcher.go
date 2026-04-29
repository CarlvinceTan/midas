package browser

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type LifecycleWatcher struct {
	page                *Page
	mainSession         sessionLike
	networkManager      *NetworkManager
	waitUntil           LoadState
	timeout             time.Duration
	navigationCommandID int64
	startTime           time.Time
	idleStartTime       time.Time

	expectedLoaderID string
	currentLoaderID  string
	initialLoaderID  string

	abortErr        error
	disposed        bool
	listeners       []func()
	idleHandle      *waitForIdleHandle
	pendingFollowup bool
}

func NewLifecycleWatcher(page *Page, mainSession sessionLike, networkManager *NetworkManager, waitUntil LoadState, timeout time.Duration, navigationCommandID int64) *LifecycleWatcher {
	w := &LifecycleWatcher{
		page:                page,
		mainSession:         mainSession,
		networkManager:      networkManager,
		waitUntil:           waitUntil,
		timeout:             timeout,
		navigationCommandID: navigationCommandID,
		startTime:           time.Now(),
		idleStartTime:       time.Now(),
	}
	w.installSessionListeners()
	return w
}

func (w *LifecycleWatcher) SetExpectedLoaderID(loaderID string) {
	if loaderID == "" {
		return
	}
	w.expectedLoaderID = loaderID
	w.initialLoaderID = loaderID
	w.currentLoaderID = loaderID
	w.idleStartTime = time.Now()
}

func (w *LifecycleWatcher) Wait(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	defer w.Dispose()

	if w.waitUntil == LoadStateDOMContentLoaded {
		if err := w.page.waitForMainLoadState(ctx, LoadStateDOMContentLoaded, w.navigationCommandID); err != nil {
			return w.abortOr(err)
		}
		return w.abortOr(nil)
	}

	for {
		if err := w.page.waitForMainLoadState(ctx, LoadStateLoad, w.navigationCommandID); err != nil {
			return w.abortOr(err)
		}
		if w.waitUntil != LoadStateNetworkIdle {
			return w.abortOr(nil)
		}

		if err := w.waitForNetworkIdle(ctx); err != nil {
			if w.pendingFollowup && err.Error() == "waitForIdle disposed" {
				w.pendingFollowup = false
				continue
			}
			return w.abortOr(err)
		}
		return w.abortOr(nil)
	}
}

func (w *LifecycleWatcher) Dispose() {
	if w.disposed {
		return
	}
	w.disposed = true
	if w.idleHandle != nil {
		w.idleHandle.dispose()
		w.idleHandle = nil
	}
	for _, unsub := range w.listeners {
		unsub()
	}
	w.listeners = nil
}

func (w *LifecycleWatcher) installSessionListeners() {
	w.listeners = append(w.listeners,
		w.mainSession.On("Page.frameNavigated", func(params json.RawMessage) {
			var evt frameNavigatedEvent
			if json.Unmarshal(params, &evt) != nil || evt.Frame.ID == "" {
				return
			}
			if evt.Frame.ID != w.page.mainFrameId() {
				return
			}
			loaderID := evt.Frame.LoaderID
			if loaderID == "" {
				return
			}
			if w.initialLoaderID == "" {
				w.initialLoaderID = loaderID
				w.currentLoaderID = loaderID
				w.idleStartTime = time.Now()
			}
			if w.expectedLoaderID == "" {
				w.expectedLoaderID = loaderID
				w.currentLoaderID = loaderID
				w.idleStartTime = time.Now()
				return
			}
			if loaderID != w.expectedLoaderID {
				if !w.page.isCurrentNavigationCommand(w.navigationCommandID) {
					w.abortErr = errors.New("navigation was superseded by a new request")
					return
				}
				w.adoptNewMainLoader(loaderID)
			}
		}),
		w.mainSession.On("Page.frameDetached", func(params json.RawMessage) {
			var evt frameDetachedEvent
			if json.Unmarshal(params, &evt) != nil || evt.FrameID == "" {
				return
			}
			if evt.FrameID != w.page.mainFrameId() || evt.Reason == "swap" {
				return
			}
			w.abortErr = errors.New("main frame was detached")
		}),
	)
}

func (w *LifecycleWatcher) waitForNetworkIdle(ctx context.Context) error {
	w.pendingFollowup = false
	remaining := time.Until(w.startTime.Add(w.timeout))
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	idleWindow := defaultIdleWait
	if remaining < idleWindow {
		idleWindow = remaining
	}
	handle := w.networkManager.WaitForIdle(waitForIdleOptions{
		startTime:   w.idleStartTime,
		timeout:     remaining,
		totalBudget: w.timeout,
		idleTime:    idleWindow,
		filter:      w.buildIdleFilter(),
	})
	w.idleHandle = &handle
	select {
	case err := <-handle.promise:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *LifecycleWatcher) adoptNewMainLoader(loaderID string) {
	w.expectedLoaderID = loaderID
	w.currentLoaderID = loaderID
	w.idleStartTime = time.Now()
	if w.waitUntil != LoadStateNetworkIdle {
		return
	}
	w.pendingFollowup = true
	if w.idleHandle != nil {
		w.idleHandle.dispose()
		w.idleHandle = nil
	}
}

func (w *LifecycleWatcher) buildIdleFilter() func(info networkRequestInfo) bool {
	loaderID := w.currentLoaderID
	mainFrameID := w.page.mainFrameId()
	return func(info networkRequestInfo) bool {
		if _, ignored := ignoredResourceTypes[info.resourceType]; ignored {
			return false
		}
		if loaderID != "" && info.loaderID != "" {
			return info.loaderID == loaderID
		}
		if info.loaderID == "" && info.frameID != "" {
			return info.frameID == mainFrameID
		}
		return true
	}
}

func (w *LifecycleWatcher) abortOr(err error) error {
	if w.abortErr != nil {
		return w.abortErr
	}
	return err
}
