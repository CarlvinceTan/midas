package browser

import (
	"context"
	"encoding/json"
	"sync"
)

type NavigationResponseTracker struct {
	page                *Page
	session             sessionLike
	navigationCommandID int64

	expectedLoaderID          string
	selectedRequestID         string
	selectedResponse          *Response
	acceptNextWithoutLoaderID bool

	responseResolved bool
	responseCh       chan *Response
	listeners        []func()

	mu               sync.Mutex
	pendingExtraInfo map[string]*extraInfoPayload
}

type extraInfoPayload struct {
	headers     map[string]string
	headersText string
}

func NewNavigationResponseTracker(page *Page, session sessionLike, navigationCommandID int64) *NavigationResponseTracker {
	t := &NavigationResponseTracker{
		page:                page,
		session:             session,
		navigationCommandID: navigationCommandID,
		responseCh:          make(chan *Response, 1),
		pendingExtraInfo:    make(map[string]*extraInfoPayload),
	}
	t.installListeners()
	return t
}

func (t *NavigationResponseTracker) Dispose() {
	for _, unsub := range t.listeners {
		unsub()
	}
	t.listeners = nil
}

func (t *NavigationResponseTracker) SetExpectedLoaderID(loaderID string) {
	if loaderID != "" {
		t.expectedLoaderID = loaderID
	}
}

func (t *NavigationResponseTracker) ExpectNavigationWithoutKnownLoader() {
	t.acceptNextWithoutLoaderID = true
}

func (t *NavigationResponseTracker) NavigationCompleted(ctx context.Context) *Response {
	select {
	case resp := <-t.responseCh:
		return resp
	case <-ctx.Done():
		if !t.responseResolved {
			t.resolveResponse(nil)
		}
		return <-t.responseCh
	}
}

func (t *NavigationResponseTracker) installListeners() {
	t.listeners = append(t.listeners,
		t.session.On("Network.responseReceived", func(params json.RawMessage) {
			var evt struct {
				RequestID string `json:"requestId"`
				FrameID   string `json:"frameId"`
				LoaderID  string `json:"loaderId"`
				Type      string `json:"type"`
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
			if !t.page.isCurrentNavigationCommand(t.navigationCommandID) {
				return
			}
			if evt.Type != "Document" || evt.FrameID != t.page.mainFrameId() {
				return
			}

			details := &networkResponseDetails{
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

			t.mu.Lock()
			pending := t.pendingExtraInfo[evt.RequestID]
			delete(t.pendingExtraInfo, evt.RequestID)
			t.mu.Unlock()

			if pending != nil {
				details.ExtraHeaders = pending.headers
				details.ExtraHeadersText = pending.headersText
			}

			if t.acceptNextWithoutLoaderID {
				t.acceptNextWithoutLoaderID = false
				t.selectResponse(evt.RequestID, evt.FrameID, evt.LoaderID, details)
				return
			}
			if t.expectedLoaderID != "" && evt.LoaderID != "" && evt.LoaderID != t.expectedLoaderID {
				return
			}
			t.selectResponse(evt.RequestID, evt.FrameID, evt.LoaderID, details)
		}),
		t.session.On("Network.responseReceivedExtraInfo", func(params json.RawMessage) {
			var evt struct {
				RequestID   string         `json:"requestId"`
				Headers     map[string]any `json:"headers"`
				HeadersText string         `json:"headersText"`
			}
			if json.Unmarshal(params, &evt) != nil || evt.RequestID == "" {
				return
			}

			// Once a response is selected it owns its own per-request listeners
			// (see Response.installLifetimeListeners) and routes extra-info
			// applies itself — those listeners outlive this tracker so headers
			// arriving after Dispose still reach the caller. Tracker only needs
			// to buffer events that arrive before responseReceived has selected
			// a response, so they can be folded into newResponse via details.
			t.mu.Lock()
			if t.selectedResponse != nil && t.selectedRequestID == evt.RequestID {
				t.mu.Unlock()
				return
			}
			t.pendingExtraInfo[evt.RequestID] = &extraInfoPayload{
				headers:     stringifyMap(evt.Headers),
				headersText: evt.HeadersText,
			}
			t.mu.Unlock()
		}),
		// Network.loadingFinished and Network.loadingFailed are intentionally
		// NOT handled here. The Response installs its own listeners for those
		// events keyed on its requestID, so markFinished still fires (and
		// finishWait still closes) when they arrive after the navigation call
		// has returned and disposed this tracker.
	)
}

func (t *NavigationResponseTracker) selectResponse(requestID, frameID, loaderID string, details *networkResponseDetails) {
	if t.responseResolved || t.selectedResponse != nil {
		return
	}
	if details == nil || details.URL == "" || hasDisallowedNavigationURL(details.URL) {
		t.resolveResponse(nil)
		return
	}
	resp := newResponse(t.page, t.session, requestID, frameID, loaderID, details)
	t.mu.Lock()
	t.selectedRequestID = requestID
	t.selectedResponse = resp
	t.mu.Unlock()
	t.resolveResponse(resp)
}

func (t *NavigationResponseTracker) resolveResponse(resp *Response) {
	if t.responseResolved {
		return
	}
	t.responseResolved = true
	t.responseCh <- resp
}

func hasDisallowedNavigationURL(url string) bool {
	return len(url) >= 5 && url[:5] == "data:" || len(url) >= 6 && url[:6] == "about:"
}

func defaultString(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
