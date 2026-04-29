package cdp

import (
	"context"
	"encoding/json"
)

type EventHandler func(params json.RawMessage)

type MatchFunc func(params json.RawMessage) bool

type Unsubscribe func()

type Session interface {
	ID() string
	Send(ctx context.Context, method string, params any, result any) error
	On(event string, handler EventHandler) Unsubscribe
	Close(ctx context.Context) error
}

type DialOptions struct {
	Headers   map[string]string
	UserAgent string
}

type TargetInfo struct {
	TargetID         string `json:"targetId"`
	Type             string `json:"type,omitempty"`
	Title            string `json:"title,omitempty"`
	URL              string `json:"url,omitempty"`
	Attached         bool   `json:"attached,omitempty"`
	BrowserContextID string `json:"browserContextId,omitempty"`
	OpenerID         string `json:"openerId,omitempty"`
	Subtype          string `json:"subtype,omitempty"`
}

type requestEnvelope struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type responseEnvelope struct {
	ID        int64           `json:"id"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type eventEnvelope struct {
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type rawMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type cdpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type attachToTargetResult struct {
	SessionID string `json:"sessionId"`
}

type getTargetsResult struct {
	TargetInfos []TargetInfo `json:"targetInfos"`
}

type attachedToTargetEvent struct {
	SessionID  string     `json:"sessionId"`
	TargetInfo TargetInfo `json:"targetInfo"`
}

type detachedFromTargetEvent struct {
	SessionID string `json:"sessionId"`
	TargetID  string `json:"targetId"`
}

type targetDestroyedEvent struct {
	TargetID string `json:"targetId"`
}
