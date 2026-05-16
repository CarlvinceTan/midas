package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type inflight struct {
	sessionID string
	method    string
	done      chan struct{}
	result    json.RawMessage
	err       error
}

type dispatchWaiter struct {
	sessionID string
	method    string
	match     MatchFunc
	done      chan error
}

type Conn struct {
	ws      *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	writeMu sync.Mutex

	mu            sync.RWMutex
	closed        bool
	closeErr      error
	nextHandlerID uint64

	nextID int64

	inflight        map[int64]*inflight
	sessions        map[string]*SessionConn
	sessionToTarget map[string]string

	rootHandlers    map[string]map[uint64]EventHandler
	sessionHandlers map[string]map[string]map[uint64]EventHandler

	dispatchWaiters map[uint64]*dispatchWaiter

	transportCloseHandlers map[uint64]func(string)
	closeOnce              sync.Once
}

func Dial(ctx context.Context, wsURL string, opts DialOptions) (*Conn, error) {
	header := http.Header{}
	for key, values := range opts.Headers {
		header.Set(key, values)
	}
	if opts.UserAgent != "" {
		header.Set("User-Agent", opts.UserAgent)
	}

	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	// CDP responses can be huge (Network.responseBody on big pages, large
	// attachedToTarget bursts during navigation). Disable coder's 32 KiB
	// read cap; the protocol itself bounds message size.
	conn.SetReadLimit(-1)

	// Connection-scoped context outlives the Dial call's ctx so reads/writes
	// after Dial returns aren't tied to the dial timeout.
	connCtx, cancel := context.WithCancel(context.Background())

	c := &Conn{
		ws:                     conn,
		ctx:                    connCtx,
		cancel:                 cancel,
		inflight:               make(map[int64]*inflight),
		sessions:               make(map[string]*SessionConn),
		sessionToTarget:        make(map[string]string),
		rootHandlers:           make(map[string]map[uint64]EventHandler),
		sessionHandlers:        make(map[string]map[string]map[uint64]EventHandler),
		dispatchWaiters:        make(map[uint64]*dispatchWaiter),
		transportCloseHandlers: make(map[uint64]func(string)),
	}

	go c.readLoop()

	return c, nil
}

func (c *Conn) Send(ctx context.Context, method string, params any, result any) error {
	return c.send(ctx, "", method, params, result)
}

func (c *Conn) On(event string, handler EventHandler) Unsubscribe {
	id := c.nextSubscriptionID()

	c.mu.Lock()
	defer c.mu.Unlock()

	handlers := c.rootHandlers[event]
	if handlers == nil {
		handlers = make(map[uint64]EventHandler)
		c.rootHandlers[event] = handlers
	}
	handlers[id] = handler

	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if handlers := c.rootHandlers[event]; handlers != nil {
			delete(handlers, id)
			if len(handlers) == 0 {
				delete(c.rootHandlers, event)
			}
		}
	}
}

func (c *Conn) Close() error {
	c.closeWithReason("client close")
	return nil
}

func (c *Conn) EnableAutoAttach(ctx context.Context) error {
	if err := c.Send(ctx, "Target.setAutoAttach", map[string]any{
		"autoAttach":             true,
		"flatten":                true,
		"waitForDebuggerOnStart": true,
	}, nil); err != nil {
		return err
	}
	return c.Send(ctx, "Target.setDiscoverTargets", map[string]any{
		"discover": true,
	}, nil)
}

func (c *Conn) AttachToTarget(ctx context.Context, targetID string) (*SessionConn, error) {
	var res attachToTargetResult
	if err := c.Send(ctx, "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true,
	}, &res); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	session := c.sessions[res.SessionID]
	if session == nil {
		session = &SessionConn{root: c, id: res.SessionID}
		c.sessions[res.SessionID] = session
	}
	c.sessionToTarget[res.SessionID] = targetID

	return session, nil
}

func (c *Conn) GetSession(sessionID string) (*SessionConn, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	session, ok := c.sessions[sessionID]
	return session, ok
}

func (c *Conn) GetTargets(ctx context.Context) ([]TargetInfo, error) {
	var res getTargetsResult
	if err := c.Send(ctx, "Target.getTargets", nil, &res); err != nil {
		return nil, err
	}
	return res.TargetInfos, nil
}

func (c *Conn) WaitForSessionDispatch(ctx context.Context, sessionID, method string, match MatchFunc) error {
	id := c.nextSubscriptionID()
	waiter := &dispatchWaiter{
		sessionID: sessionID,
		method:    method,
		match:     match,
		done:      make(chan error, 1),
	}

	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = &ConnectionClosedError{}
		}
		return err
	}
	c.dispatchWaiters[id] = waiter
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.dispatchWaiters, id)
		c.mu.Unlock()
	}()

	select {
	case err := <-waiter.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Conn) OnTransportClosed(handler func(string)) Unsubscribe {
	id := c.nextSubscriptionID()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.transportCloseHandlers[id] = handler

	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.transportCloseHandlers, id)
	}
}

func (c *Conn) send(ctx context.Context, sessionID, method string, params any, result any) error {
	reqID := atomic.AddInt64(&c.nextID, 1)
	rec := &inflight{
		sessionID: sessionID,
		method:    method,
		done:      make(chan struct{}),
	}

	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = &ConnectionClosedError{}
		}
		return err
	}
	c.inflight[reqID] = rec
	c.resolveDispatchWaitersLocked(sessionID, method, params)
	c.mu.Unlock()

	payload := requestEnvelope{
		ID:        reqID,
		Method:    method,
		Params:    params,
		SessionID: sessionID,
	}

	if err := c.writeJSON(payload); err != nil {
		c.finishInflight(reqID, nil, err)
		c.closeWithReason("write error: " + err.Error())
		return err
	}

	select {
	case <-ctx.Done():
		c.cancelInflight(reqID, ctx.Err())
		return ctx.Err()
	case <-rec.done:
		if rec.err != nil {
			return rec.err
		}
		if result == nil || len(rec.result) == 0 {
			return nil
		}
		return json.Unmarshal(rec.result, result)
	}
}

func (c *Conn) readLoop() {
	for {
		_, payload, err := c.ws.Read(c.ctx)
		if err != nil {
			c.closeWithReason(readErrorReason(err))
			return
		}
		if err := c.handleMessage(payload); err != nil {
			c.closeWithReason("read error: " + err.Error())
			return
		}
	}
}

func (c *Conn) handleMessage(payload []byte) error {
	var msg rawMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}

	if msg.ID != 0 {
		c.finishInflight(msg.ID, msg.Result, cdpResponseError(msg.Error))
		return nil
	}

	if msg.Method == "" {
		return nil
	}

	switch msg.Method {
	case "Target.attachedToTarget":
		var evt attachedToTargetEvent
		if err := json.Unmarshal(msg.Params, &evt); err != nil {
			return err
		}
		c.handleAttachedToTarget(evt)
	case "Target.detachedFromTarget":
		var evt detachedFromTargetEvent
		if err := json.Unmarshal(msg.Params, &evt); err != nil {
			return err
		}
		c.handleDetachedFromTarget(evt)
	case "Target.targetDestroyed":
		var evt targetDestroyedEvent
		if err := json.Unmarshal(msg.Params, &evt); err != nil {
			return err
		}
		c.handleTargetDestroyed(evt)
	}

	c.dispatchEvent(msg.SessionID, msg.Method, msg.Params)
	return nil
}

func (c *Conn) handleAttachedToTarget(evt attachedToTargetEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.sessions[evt.SessionID]; !ok {
		c.sessions[evt.SessionID] = &SessionConn{root: c, id: evt.SessionID}
	}
	if evt.TargetInfo.TargetID != "" {
		c.sessionToTarget[evt.SessionID] = evt.TargetInfo.TargetID
	}
}

func (c *Conn) handleDetachedFromTarget(evt detachedFromTargetEvent) {
	detachErr := &SessionDetachedError{
		SessionID: evt.SessionID,
		TargetID:  evt.TargetID,
	}

	c.mu.Lock()
	var inflightIDs []int64
	for id, rec := range c.inflight {
		if rec.sessionID == evt.SessionID {
			inflightIDs = append(inflightIDs, id)
		}
	}

	var waiterIDs []uint64
	var waiters []*dispatchWaiter
	for id, waiter := range c.dispatchWaiters {
		if waiter.sessionID == evt.SessionID {
			waiterIDs = append(waiterIDs, id)
			waiters = append(waiters, waiter)
		}
	}

	delete(c.sessions, evt.SessionID)
	delete(c.sessionToTarget, evt.SessionID)
	delete(c.sessionHandlers, evt.SessionID)
	c.mu.Unlock()

	for _, id := range inflightIDs {
		c.finishInflight(id, nil, detachErr)
	}
	for i, waiter := range waiters {
		c.mu.Lock()
		delete(c.dispatchWaiters, waiterIDs[i])
		c.mu.Unlock()
		select {
		case waiter.done <- detachErr:
		default:
		}
	}
}

func (c *Conn) handleTargetDestroyed(evt targetDestroyedEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for sessionID, targetID := range c.sessionToTarget {
		if targetID == evt.TargetID {
			delete(c.sessionToTarget, sessionID)
		}
	}
}

func (c *Conn) dispatchEvent(sessionID, event string, params json.RawMessage) {
	if sessionID != "" {
		c.mu.RLock()
		sessionHandlers := cloneHandlers(c.sessionHandlers[sessionID][event])
		rootHandlers := make(map[uint64]EventHandler)
		if strings.HasPrefix(event, "Target.") {
			rootHandlers = cloneHandlers(c.rootHandlers[event])
		}
		c.mu.RUnlock()

		for _, handler := range sessionHandlers {
			handler(params)
		}
		for _, handler := range rootHandlers {
			handler(params)
		}
		return
	}

	c.mu.RLock()
	handlers := cloneHandlers(c.rootHandlers[event])
	c.mu.RUnlock()

	for _, handler := range handlers {
		handler(params)
	}
}

func (c *Conn) nextSubscriptionID() uint64 {
	return atomic.AddUint64(&c.nextHandlerID, 1)
}

func (c *Conn) writeJSON(payload requestEnvelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(c.ctx, c.ws, payload)
}

func (c *Conn) finishInflight(id int64, result json.RawMessage, err error) {
	c.mu.Lock()
	rec, ok := c.inflight[id]
	if ok {
		delete(c.inflight, id)
		rec.result = result
		rec.err = err
	}
	c.mu.Unlock()

	if ok {
		close(rec.done)
	}
}

func (c *Conn) cancelInflight(id int64, err error) {
	c.finishInflight(id, nil, err)
}

func (c *Conn) resolveDispatchWaitersLocked(sessionID, method string, params any) {
	if sessionID == "" || len(c.dispatchWaiters) == 0 {
		return
	}

	var raw json.RawMessage
	if params != nil {
		if encoded, err := json.Marshal(params); err == nil {
			raw = encoded
		}
	}

	for id, waiter := range c.dispatchWaiters {
		if waiter.sessionID != sessionID || waiter.method != method {
			continue
		}
		if waiter.match != nil && !waiter.match(raw) {
			continue
		}
		delete(c.dispatchWaiters, id)
		waiter.done <- nil
		return
	}
}

func (c *Conn) closeWithReason(reason string) {
	c.closeOnce.Do(func() {
		closeErr := &ConnectionClosedError{Why: reason}

		c.mu.Lock()
		c.closed = true
		c.closeErr = closeErr

		pendingInflight := make([]*inflight, 0, len(c.inflight))
		for _, rec := range c.inflight {
			pendingInflight = append(pendingInflight, rec)
		}
		c.inflight = make(map[int64]*inflight)

		waiters := make([]*dispatchWaiter, 0, len(c.dispatchWaiters))
		for _, waiter := range c.dispatchWaiters {
			waiters = append(waiters, waiter)
		}
		c.dispatchWaiters = make(map[uint64]*dispatchWaiter)

		handlers := make([]func(string), 0, len(c.transportCloseHandlers))
		for _, handler := range c.transportCloseHandlers {
			handlers = append(handlers, handler)
		}
		c.mu.Unlock()

		for _, rec := range pendingInflight {
			rec.err = closeErr
			close(rec.done)
		}
		for _, waiter := range waiters {
			select {
			case waiter.done <- closeErr:
			default:
			}
		}
		for _, handler := range handlers {
			handler(reason)
		}

		if c.cancel != nil {
			c.cancel()
		}
		if c.ws != nil {
			_ = c.ws.Close(websocket.StatusNormalClosure, "")
		}
	})
}

func (c *Conn) onSessionEvent(sessionID, event string, handler EventHandler) Unsubscribe {
	id := c.nextSubscriptionID()

	c.mu.Lock()
	defer c.mu.Unlock()

	sessionEvents := c.sessionHandlers[sessionID]
	if sessionEvents == nil {
		sessionEvents = make(map[string]map[uint64]EventHandler)
		c.sessionHandlers[sessionID] = sessionEvents
	}
	handlers := sessionEvents[event]
	if handlers == nil {
		handlers = make(map[uint64]EventHandler)
		sessionEvents[event] = handlers
	}
	handlers[id] = handler

	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if sessionEvents := c.sessionHandlers[sessionID]; sessionEvents != nil {
			if handlers := sessionEvents[event]; handlers != nil {
				delete(handlers, id)
				if len(handlers) == 0 {
					delete(sessionEvents, event)
				}
			}
			if len(sessionEvents) == 0 {
				delete(c.sessionHandlers, sessionID)
			}
		}
	}
}

func cloneHandlers(src map[uint64]EventHandler) map[uint64]EventHandler {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[uint64]EventHandler, len(src))
	for id, handler := range src {
		dst[id] = handler
	}
	return dst
}

func cdpResponseError(respErr *cdpError) error {
	if respErr == nil {
		return nil
	}
	return errors.New(respErr.Message)
}

func readErrorReason(err error) string {
	// coder/websocket exposes the peer's close status via CloseStatus; a
	// non-CloseError (TCP reset, ctx cancel, EOF) returns -1.
	status := websocket.CloseStatus(err)
	switch {
	case status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway:
		return err.Error()
	case status != -1:
		return "unexpected close: " + err.Error()
	default:
		return err.Error()
	}
}
