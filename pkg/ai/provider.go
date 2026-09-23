package ai

import (
	"context"
	"net/http"
	"time"
)

type CacheRetention string

const (
	CacheNone  CacheRetention = "none"
	CacheShort CacheRetention = "short"
	CacheLong  CacheRetention = "long"
)

type Transport string

const (
	TransportAuto            Transport = "auto"
	TransportSSE             Transport = "sse"
	TransportWebSocket       Transport = "websocket"
	TransportWebSocketCached Transport = "websocket-cached"
)

type StreamOptions struct {
	APIKey                  string
	Temperature             *float64
	MaxTokens               int
	Reasoning               ThinkingLevel
	Transport               Transport
	CacheRetention          CacheRetention
	SessionID               string
	Metadata                map[string]any
	Headers                 map[string]string
	SamplingParams          map[string]any
	HTTPClient              *http.Client
	Timeout                 time.Duration
	MaxRetries              int
	MaxRetryDelay           time.Duration
	WebSocketConnectTimeout time.Duration
}

// Streamer is the narrow contract consumed by the agent loop.
type Streamer interface {
	Stream(context.Context, Model, Context, StreamOptions) (*AssistantStream, error)
}

// Provider adds identity and model discovery to a Streamer.
type Provider interface {
	Streamer
	ID() string
	Models(context.Context) ([]Model, error)
}
