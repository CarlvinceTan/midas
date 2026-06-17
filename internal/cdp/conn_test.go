package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestHandleMessageResponseResolvesInflight(t *testing.T) {
	conn := newTestConn()

	rec := &inflight{
		method: "Browser.getVersion",
		done:   make(chan struct{}),
	}
	conn.inflight[1] = rec

	err := conn.handleMessage([]byte(`{"id":1,"result":{"product":"Chrome/1.0"}}`))
	if err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	select {
	case <-rec.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inflight completion")
	}

	if rec.err != nil {
		t.Fatalf("unexpected inflight error: %v", rec.err)
	}

	var result struct {
		Product string `json:"product"`
	}
	if err := json.Unmarshal(rec.result, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if result.Product != "Chrome/1.0" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestHandleAttachedToTargetCreatesSession(t *testing.T) {
	conn := newTestConn()

	err := conn.handleMessage([]byte(`{
		"method":"Target.attachedToTarget",
		"params":{
			"sessionId":"session-1",
			"targetInfo":{"targetId":"target-1","type":"page"}
		}
	}`))
	if err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	session, ok := conn.GetSession("session-1")
	if !ok {
		t.Fatal("expected session to be created")
	}
	if session.ID() != "session-1" {
		t.Fatalf("unexpected session id: %s", session.ID())
	}
	if got := conn.sessionToTarget["session-1"]; got != "target-1" {
		t.Fatalf("unexpected target mapping: %q", got)
	}
}

func TestDispatchEventToSessionAndRootTargetListeners(t *testing.T) {
	conn := newTestConn()
	conn.sessions["session-1"] = &SessionConn{root: conn, id: "session-1"}

	sessionEvents := make(chan string, 1)
	rootEvents := make(chan string, 1)

	sessionUnsub := conn.onSessionEvent("session-1", "Runtime.executionContextCreated", func(params json.RawMessage) {
		sessionEvents <- string(params)
	})
	defer sessionUnsub()

	rootUnsub := conn.On("Target.detachedFromTarget", func(params json.RawMessage) {
		rootEvents <- string(params)
	})
	defer rootUnsub()

	conn.dispatchEvent("session-1", "Runtime.executionContextCreated", json.RawMessage(`{"context":{"id":7}}`))
	conn.dispatchEvent("session-1", "Target.detachedFromTarget", json.RawMessage(`{"sessionId":"session-1","targetId":"target-1"}`))

	select {
	case payload := <-sessionEvents:
		if payload != `{"context":{"id":7}}` {
			t.Fatalf("unexpected session payload: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session event")
	}

	select {
	case payload := <-rootEvents:
		if payload != `{"sessionId":"session-1","targetId":"target-1"}` {
			t.Fatalf("unexpected root payload: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for root event")
	}
}

func TestDetachedSessionFailsInflightAndWaiters(t *testing.T) {
	conn := newTestConn()
	conn.sessions["session-1"] = &SessionConn{root: conn, id: "session-1"}
	conn.sessionToTarget["session-1"] = "target-1"

	rec := &inflight{
		sessionID: "session-1",
		method:    "Runtime.evaluate",
		done:      make(chan struct{}),
	}
	conn.inflight[99] = rec

	waiter := &dispatchWaiter{
		sessionID: "session-1",
		method:    "Runtime.evaluate",
		done:      make(chan error, 1),
	}
	conn.dispatchWaiters[1] = waiter

	err := conn.handleMessage([]byte(`{
		"method":"Target.detachedFromTarget",
		"params":{"sessionId":"session-1","targetId":"target-1"}
	}`))
	if err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	select {
	case <-rec.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inflight completion")
	}

	var detached *SessionDetachedError
	if !errors.As(rec.err, &detached) {
		t.Fatalf("expected SessionDetachedError, got %T: %v", rec.err, rec.err)
	}

	select {
	case err := <-waiter.done:
		if !errors.As(err, &detached) {
			t.Fatalf("expected SessionDetachedError from waiter, got %T: %v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for dispatch waiter")
	}
}

func TestResolveDispatchWaitersMatchesMethodAndParams(t *testing.T) {
	conn := newTestConn()

	waiter := &dispatchWaiter{
		sessionID: "session-1",
		method:    "Runtime.evaluate",
		match: func(params json.RawMessage) bool {
			return string(params) == `{"expression":"1+1"}`
		},
		done: make(chan error, 1),
	}
	conn.dispatchWaiters[1] = waiter

	conn.resolveDispatchWaitersLocked("session-1", "Runtime.evaluate", map[string]any{
		"expression": "1+1",
	})

	select {
	case err := <-waiter.done:
		if err != nil {
			t.Fatalf("unexpected waiter error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for waiter resolution")
	}
}

func TestCloseWithReasonRejectsInflightAndFutureWaiters(t *testing.T) {
	conn := newTestConn()

	rec := &inflight{
		method: "Browser.getVersion",
		done:   make(chan struct{}),
	}
	conn.inflight[1] = rec

	waiter := &dispatchWaiter{
		sessionID: "session-1",
		method:    "Runtime.evaluate",
		done:      make(chan error, 1),
	}
	conn.dispatchWaiters[1] = waiter

	conn.closeWithReason("test close")

	select {
	case <-rec.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inflight close")
	}

	var closed *ConnectionClosedError
	if !errors.As(rec.err, &closed) {
		t.Fatalf("expected ConnectionClosedError, got %T: %v", rec.err, rec.err)
	}

	select {
	case err := <-waiter.done:
		if !errors.As(err, &closed) {
			t.Fatalf("expected ConnectionClosedError, got %T: %v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for waiter close")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := conn.WaitForSessionDispatch(ctx, "session-1", "Runtime.evaluate", nil)
	if !errors.As(err, &closed) {
		t.Fatalf("expected closed connection error, got %T: %v", err, err)
	}
}

func newTestConn() *Conn {
	c := &Conn{
		inflight:               make(map[int64]*inflight),
		sessions:               make(map[string]*SessionConn),
		sessionToTarget:        make(map[string]string),
		rootHandlers:           make(map[string]map[uint64]EventHandler),
		sessionHandlers:        make(map[string]map[string]map[uint64]EventHandler),
		dispatchWaiters:        make(map[uint64]*dispatchWaiter),
		transportCloseHandlers: make(map[uint64]func(string)),
	}
	c.eventCond = sync.NewCond(&c.eventMu)
	go c.eventLoop()
	return c
}
