package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultIdleWait = 500 * time.Millisecond
	rootSessionKey  = "__main__"
)

var ignoredResourceTypes = map[string]struct{}{
	"Image":      {},
	"Media":      {},
	"Font":       {},
	"Stylesheet": {},
	"Other":      {},
}

type NetworkManager struct {
	mu sync.Mutex

	sessions map[string]struct {
		session sessionLike
		detach  func()
	}
	observers map[int]networkObserver
	nextObsID int

	requests                map[string]networkRequestInfo
	documentRequestsByFrame map[string]string
}

func NewNetworkManager() *NetworkManager {
	return &NetworkManager{
		sessions: make(map[string]struct {
			session sessionLike
			detach  func()
		}),
		observers:               make(map[int]networkObserver),
		requests:                make(map[string]networkRequestInfo),
		documentRequestsByFrame: make(map[string]string),
	}
}

func (m *NetworkManager) TrackSession(session sessionLike) {
	sid := m.sessionKey(session)

	m.mu.Lock()
	if _, ok := m.sessions[sid]; ok {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	onRequest := func(params json.RawMessage) {
		var evt struct {
			RequestID string `json:"requestId"`
			FrameID   string `json:"frameId"`
			LoaderID  string `json:"loaderId"`
			Type      string `json:"type"`
			Request   struct {
				URL string `json:"url"`
			} `json:"request"`
		}
		if json.Unmarshal(params, &evt) != nil || evt.RequestID == "" {
			return
		}
		info := networkRequestInfo{
			sessionID:       sid,
			requestID:       evt.RequestID,
			requestKey:      m.requestKey(sid, evt.RequestID),
			frameID:         evt.FrameID,
			loaderID:        evt.LoaderID,
			url:             evt.Request.URL,
			timestamp:       time.Now(),
			resourceType:    evt.Type,
			documentRequest: evt.Type == "Document",
		}

		m.mu.Lock()
		m.requests[info.requestKey] = info
		if info.documentRequest && info.frameID != "" {
			m.documentRequestsByFrame[info.frameID] = info.requestKey
		}
		observers := m.snapshotObserversLocked()
		m.mu.Unlock()

		for _, obs := range observers {
			if obs.onRequestStarted != nil {
				obs.onRequestStarted(info)
			}
		}
	}

	finishRequest := func(reqID string, failed bool) {
		if reqID == "" {
			return
		}
		key := m.requestKey(sid, reqID)

		m.mu.Lock()
		info, ok := m.requests[key]
		if ok {
			delete(m.requests, key)
			if info.frameID != "" {
				delete(m.documentRequestsByFrame, info.frameID)
			}
		} else {
			info = networkRequestInfo{
				sessionID:  sid,
				requestID:  reqID,
				requestKey: key,
				timestamp:  time.Now(),
			}
		}
		observers := m.snapshotObserversLocked()
		m.mu.Unlock()

		for _, obs := range observers {
			if failed {
				if obs.onRequestFailed != nil {
					obs.onRequestFailed(info)
				}
			} else if obs.onRequestFinished != nil {
				obs.onRequestFinished(info)
			}
		}
	}

	onFinished := func(params json.RawMessage) {
		var evt struct {
			RequestID string `json:"requestId"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		finishRequest(evt.RequestID, false)
	}

	onFailed := func(params json.RawMessage) {
		var evt struct {
			RequestID string `json:"requestId"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		finishRequest(evt.RequestID, true)
	}

	onResponse := func(params json.RawMessage) {
		var evt struct {
			RequestID string `json:"requestId"`
			FrameID   string `json:"frameId"`
			LoaderID  string `json:"loaderId"`
			Response  struct {
				URL               string         `json:"url"`
				Status            int            `json:"status"`
				StatusText        string         `json:"statusText"`
				Headers           map[string]any `json:"headers"`
				HeadersText       string         `json:"headersText"`
				MimeType          string         `json:"mimeType"`
				RemoteIPAddress   string         `json:"remoteIPAddress"`
				RemotePort        int            `json:"remotePort"`
				FromServiceWorker bool           `json:"fromServiceWorker"`
				SecurityDetails   map[string]any `json:"securityDetails"`
			} `json:"response"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		m.mu.Lock()
		key := m.requestKey(sid, evt.RequestID)
		info := m.requests[key]
		info.frameID = defaultString(info.frameID, evt.FrameID)
		info.loaderID = defaultString(info.loaderID, evt.LoaderID)
		info.response = &networkResponseDetails{
			URL:               evt.Response.URL,
			Status:            evt.Response.Status,
			StatusText:        evt.Response.StatusText,
			Headers:           stringifyMap(evt.Response.Headers),
			HeadersText:       evt.Response.HeadersText,
			MimeType:          evt.Response.MimeType,
			RemoteIPAddress:   evt.Response.RemoteIPAddress,
			RemotePort:        evt.Response.RemotePort,
			FromServiceWorker: evt.Response.FromServiceWorker,
			SecurityDetails:   cloneJSON(evt.Response.SecurityDetails),
		}
		m.requests[key] = info
		m.mu.Unlock()
		if len(evt.Response.URL) >= 5 && evt.Response.URL[:5] == "data:" {
			finishRequest(evt.RequestID, false)
		}
	}

	onResponseExtraInfo := func(params json.RawMessage) {
		var evt struct {
			RequestID   string         `json:"requestId"`
			Headers     map[string]any `json:"headers"`
			HeadersText string         `json:"headersText"`
		}
		if json.Unmarshal(params, &evt) != nil || evt.RequestID == "" {
			return
		}
		key := m.requestKey(sid, evt.RequestID)
		m.mu.Lock()
		info := m.requests[key]
		if info.response == nil {
			info.response = &networkResponseDetails{}
		}
		info.response.ExtraHeaders = stringifyMap(evt.Headers)
		info.response.ExtraHeadersText = evt.HeadersText
		m.requests[key] = info
		m.mu.Unlock()
	}

	onFrameStopped := func(params json.RawMessage) {
		var evt struct {
			FrameID string `json:"frameId"`
		}
		if json.Unmarshal(params, &evt) != nil || evt.FrameID == "" {
			return
		}
		m.mu.Lock()
		key := m.documentRequestsByFrame[evt.FrameID]
		info, ok := m.requests[key]
		if ok {
			delete(m.requests, key)
		}
		delete(m.documentRequestsByFrame, evt.FrameID)
		observers := m.snapshotObserversLocked()
		m.mu.Unlock()
		if ok {
			info.timestamp = time.Now()
			for _, obs := range observers {
				if obs.onRequestFinished != nil {
					obs.onRequestFinished(info)
				}
			}
		}
	}

	unsubscribers := []func(){
		session.On("Network.requestWillBeSent", onRequest),
		session.On("Network.loadingFinished", onFinished),
		session.On("Network.loadingFailed", onFailed),
		session.On("Network.requestServedFromCache", onFinished),
		session.On("Network.responseReceived", onResponse),
		session.On("Network.responseReceivedExtraInfo", onResponseExtraInfo),
		session.On("Page.frameStoppedLoading", onFrameStopped),
	}

	_ = session.Send(context.Background(), "Network.enable", nil, nil)
	_ = session.Send(context.Background(), "Page.enable", nil, nil)

	m.mu.Lock()
	m.sessions[sid] = struct {
		session sessionLike
		detach  func()
	}{
		session: session,
		detach: func() {
			for _, unsub := range unsubscribers {
				unsub()
			}
		},
	}
	m.mu.Unlock()
}

func (m *NetworkManager) RequestInfo(sessionID, requestID string) (networkRequestInfo, bool) {
	key := m.requestKey(sessionID, requestID)
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.requests[key]
	return info, ok
}

func stringifyMap(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = fmt.Sprint(value)
	}
	return out
}

func (m *NetworkManager) UntrackSession(rawSessionID string) {
	sid := rawSessionID
	if sid == "" {
		sid = rootSessionKey
	}

	m.mu.Lock()
	entry, ok := m.sessions[sid]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, sid)
	for key := range m.requests {
		if len(key) >= len(sid)+1 && key[:len(sid)+1] == sid+":" {
			delete(m.requests, key)
		}
	}
	for frameID, key := range m.documentRequestsByFrame {
		if len(key) >= len(sid)+1 && key[:len(sid)+1] == sid+":" {
			delete(m.documentRequestsByFrame, frameID)
		}
	}
	m.mu.Unlock()

	entry.detach()
}

func (m *NetworkManager) AddObserver(observer networkObserver) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextObsID++
	id := m.nextObsID
	m.observers[id] = observer
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.observers, id)
	}
}

func (m *NetworkManager) WaitForIdle(opts waitForIdleOptions) waitForIdleHandle {
	startTime := opts.startTime
	if startTime.IsZero() {
		startTime = time.Now()
	}
	idleTime := opts.idleTime
	if idleTime <= 0 {
		idleTime = defaultIdleWait
	}
	filter := opts.filter
	if filter == nil {
		filter = func(info networkRequestInfo) bool {
			_, ignored := ignoredResourceTypes[info.resourceType]
			return !ignored
		}
	}

	tracked := make(map[string]struct{})
	promise := make(chan error, 1)
	var mu sync.Mutex
	var idleTimer *time.Timer
	var timeoutTimer *time.Timer
	settled := false
	removeObserver := func() {}

	cleanup := func(err error) {
		mu.Lock()
		if settled {
			mu.Unlock()
			return
		}
		settled = true
		if idleTimer != nil {
			idleTimer.Stop()
		}
		if timeoutTimer != nil {
			timeoutTimer.Stop()
		}
		mu.Unlock()
		removeObserver()
		if err != nil {
			promise <- err
		} else {
			promise <- nil
		}
	}

	maybeIdle := func() {
		mu.Lock()
		defer mu.Unlock()
		if settled {
			return
		}
		if len(tracked) == 0 {
			if idleTimer == nil {
				idleTimer = time.AfterFunc(idleTime, func() {
					cleanup(nil)
				})
			}
		} else if idleTimer != nil {
			idleTimer.Stop()
			idleTimer = nil
		}
	}

	observer := networkObserver{
		onRequestStarted: func(info networkRequestInfo) {
			if info.timestamp.Before(startTime) || !filter(info) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if settled {
				return
			}
			tracked[info.requestKey] = struct{}{}
			if idleTimer != nil {
				idleTimer.Stop()
				idleTimer = nil
			}
		},
		onRequestFinished: func(info networkRequestInfo) {
			mu.Lock()
			if settled {
				mu.Unlock()
				return
			}
			delete(tracked, info.requestKey)
			mu.Unlock()
			maybeIdle()
		},
		onRequestFailed: func(info networkRequestInfo) {
			mu.Lock()
			if settled {
				mu.Unlock()
				return
			}
			delete(tracked, info.requestKey)
			mu.Unlock()
			maybeIdle()
		},
	}

	removeObserver = m.AddObserver(observer)
	maybeIdle()

	if opts.timeout > 0 {
		totalBudget := opts.totalBudget
		if totalBudget <= 0 {
			totalBudget = opts.timeout
		}
		timeoutTimer = time.AfterFunc(opts.timeout, func() {
			cleanup(fmt.Errorf("networkidle timed out after %dms", totalBudget.Milliseconds()))
		})
	}

	return waitForIdleHandle{
		promise: promise,
		dispose: func() {
			cleanup(errors.New("waitForIdle disposed"))
		},
	}
}

func (m *NetworkManager) Dispose() {
	m.mu.Lock()
	entries := make([]struct {
		session sessionLike
		detach  func()
	}, 0, len(m.sessions))
	for _, entry := range m.sessions {
		entries = append(entries, entry)
	}
	m.sessions = make(map[string]struct {
		session sessionLike
		detach  func()
	})
	m.observers = make(map[int]networkObserver)
	m.requests = make(map[string]networkRequestInfo)
	m.documentRequestsByFrame = make(map[string]string)
	m.mu.Unlock()
	for _, entry := range entries {
		entry.detach()
	}
}

func (m *NetworkManager) snapshotObserversLocked() []networkObserver {
	out := make([]networkObserver, 0, len(m.observers))
	for _, obs := range m.observers {
		out = append(out, obs)
	}
	return out
}

func (m *NetworkManager) sessionKey(session sessionLike) string {
	if session.ID() == "" {
		return rootSessionKey
	}
	return session.ID()
}

func (m *NetworkManager) requestKey(sessionID, requestID string) string {
	return sessionID + ":" + requestID
}
