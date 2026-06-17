package browser

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/PolymuxOrg/midas/internal/cdp"
)

type fakeSession struct {
	id         string
	mu         sync.Mutex
	handlers   map[string]map[int]cdp.EventHandler
	nextID     int
	responders map[string]func(params any, result any) error
	calls      []string
	callParams []methodCall
}

type methodCall struct {
	Method string
	Params map[string]any
}

func newFakeSession(id string) *fakeSession {
	return &fakeSession{
		id:         id,
		handlers:   make(map[string]map[int]cdp.EventHandler),
		responders: make(map[string]func(params any, result any) error),
	}
}

func (s *fakeSession) ID() string { return s.id }

func (s *fakeSession) Send(_ context.Context, method string, params any, result any) error {
	s.mu.Lock()
	s.calls = append(s.calls, method)
	var paramsMap map[string]any
	if params != nil {
		if m, ok := params.(map[string]any); ok {
			paramsMap = m
		}
	}
	s.callParams = append(s.callParams, methodCall{Method: method, Params: paramsMap})
	responder := s.responders[method]
	s.mu.Unlock()
	if responder != nil {
		return responder(params, result)
	}
	return nil
}

func (s *fakeSession) On(event string, handler cdp.EventHandler) cdp.Unsubscribe {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	if s.handlers[event] == nil {
		s.handlers[event] = make(map[int]cdp.EventHandler)
	}
	id := s.nextID
	s.handlers[event][id] = handler
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.handlers[event], id)
	}
}

func (s *fakeSession) Close(context.Context) error { return nil }

func (s *fakeSession) respond(method string, fn func(params any, result any) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responders[method] = fn
}

func (s *fakeSession) dispatch(event string, payload any) {
	buf, _ := json.Marshal(payload)
	s.mu.Lock()
	handlers := make([]cdp.EventHandler, 0, len(s.handlers[event]))
	for _, handler := range s.handlers[event] {
		handlers = append(handlers, handler)
	}
	s.mu.Unlock()
	for _, handler := range handlers {
		handler(buf)
	}
}

func (s *fakeSession) lastMethod() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return ""
	}
	return s.calls[len(s.calls)-1]
}

func (s *fakeSession) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *fakeSession) methodParams() []methodCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]methodCall, len(s.callParams))
	copy(out, s.callParams)
	return out
}

type fakeConn struct {
	mu       sync.Mutex
	handlers map[string]map[int]cdp.EventHandler
	nextID   int
	targets  []cdp.TargetInfo
	sessions map[string]sessionLike
	attach   func(targetID string) (sessionLike, error)
	send     func(method string, params any, result any) error
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		handlers: make(map[string]map[int]cdp.EventHandler),
		sessions: make(map[string]sessionLike),
	}
}

func (c *fakeConn) Send(_ context.Context, method string, params any, result any) error {
	if c.send != nil {
		return c.send(method, params, result)
	}
	return nil
}

func (c *fakeConn) On(event string, handler cdp.EventHandler) cdp.Unsubscribe {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	if c.handlers[event] == nil {
		c.handlers[event] = make(map[int]cdp.EventHandler)
	}
	id := c.nextID
	c.handlers[event][id] = handler
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.handlers[event], id)
	}
}

func (c *fakeConn) Close() error                           { return nil }
func (c *fakeConn) EnableAutoAttach(context.Context) error { return nil }
func (c *fakeConn) AttachToTarget(_ context.Context, targetID string) (sessionLike, error) {
	if c.attach != nil {
		return c.attach(targetID)
	}
	return nil, nil
}
func (c *fakeConn) GetSession(sessionID string) (sessionLike, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[sessionID]
	return session, ok
}

// addSession is a test helper for inserting into the sessions map under the
// same mutex GetSession uses. Tests that write directly via
// `conn.sessions[id] = sess` race with goroutines spawned by dispatch handlers,
// which call GetSession concurrently — see test_helpers_test.go GetSession.
func (c *fakeConn) addSession(id string, session sessionLike) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[id] = session
}
func (c *fakeConn) GetTargets(context.Context) ([]cdp.TargetInfo, error) {
	return append([]cdp.TargetInfo(nil), c.targets...), nil
}

func (c *fakeConn) dispatch(event string, payload any) {
	buf, _ := json.Marshal(payload)
	c.mu.Lock()
	handlers := make([]cdp.EventHandler, 0, len(c.handlers[event]))
	for _, handler := range c.handlers[event] {
		handlers = append(handlers, handler)
	}
	c.mu.Unlock()
	for _, handler := range handlers {
		handler(buf)
	}
}

func setReadyStateResult(result any, state string) {
	if result == nil {
		return
	}
	if dst, ok := result.(*struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}); ok {
		dst.Result.Value = state
	}
}

func populateJSONResult(result any, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(buf, result)
}
