package cdp

import "context"

type SessionConn struct {
	root *Conn
	id   string
}

func (s *SessionConn) ID() string {
	return s.id
}

func (s *SessionConn) Send(ctx context.Context, method string, params any, result any) error {
	return s.root.send(ctx, s.id, method, params, result)
}

func (s *SessionConn) On(event string, handler EventHandler) Unsubscribe {
	return s.root.onSessionEvent(s.id, event, handler)
}

func (s *SessionConn) Close(ctx context.Context) error {
	return s.root.Send(ctx, "Target.detachFromTarget", map[string]any{
		"sessionId": s.id,
	}, nil)
}
