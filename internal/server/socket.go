package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// socketRequest is what a client may send over the socket. Sends are accepted
// here so a desktop client holds one connection rather than one for events and
// another for writes.
type socketRequest struct {
	Action string `json:"action"`
	Group  string `json:"group,omitempty"`
	To     string `json:"to,omitempty"`
	Text   string `json:"text,omitempty"`
	From   string `json:"from,omitempty"`
}

// socketReply answers a request that needs one.
type socketReply struct {
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Message *Envelope `json:"message,omitempty"`
}

// handleSocket carries the change feed over a WebSocket and accepts sends on the
// same connection. Authentication is the same bearer token; a browser client that
// cannot set headers may pass ?token= instead, which is the usual trade for a
// browser transport.
func (e *Environment) handleSocket(writer http.ResponseWriter, request *http.Request) {
	if !e.socketAuthorized(request) {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "a bearer token is required")
		return
	}
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
		// A desktop client and the server are the same origin in every deployment
		// we ship; the token is what authorises, so origin checks would only break
		// clients.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	events, unsubscribe := e.Feed.Subscribe()
	defer unsubscribe()

	// One goroutine reads requests, this one writes events: a slow reader must not
	// stall the feed, and a slow feed must not stall reads.
	go func() {
		defer cancel()
		for {
			_, data, err := connection.Read(ctx)
			if err != nil {
				return
			}
			var message socketRequest
			if err := json.Unmarshal(data, &message); err != nil {
				_ = writeSocket(ctx, connection, socketReply{OK: false, Error: "invalid JSON"})
				continue
			}
			reply := e.handleSocketRequest(message)
			if err := writeSocket(ctx, connection, reply); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			if err := writeSocket(ctx, connection, event); err != nil {
				return
			}
		}
	}
}

// socketAuthorized accepts the bearer header, or a token query parameter for
// browser clients. An environment without a configured token authorises nobody,
// otherwise a request that omits the token would match the empty value.
func (e *Environment) socketAuthorized(request *http.Request) bool {
	token := e.token()
	if token == "" {
		return false
	}
	header := strings.TrimSpace(request.Header.Get("Authorization"))
	if subtle.ConstantTimeCompare([]byte(header), []byte("Bearer "+token)) == 1 {
		return true
	}
	query := strings.TrimSpace(request.URL.Query().Get("token"))
	return subtle.ConstantTimeCompare([]byte(query), []byte(token)) == 1
}

// handleSocketRequest performs one request from a socket client.
func (e *Environment) handleSocketRequest(message socketRequest) socketReply {
	switch strings.ToLower(strings.TrimSpace(message.Action)) {
	case "send":
		text := strings.TrimSpace(message.Text)
		if text == "" {
			return socketReply{OK: false, Error: "a message needs text"}
		}
		to := strings.TrimSpace(message.To)
		if to == "" {
			to = e.defaultAgent()
		}
		from := strings.TrimSpace(message.From)
		if from == "" {
			from = UserAddress
		}
		envelope, err := e.Broker.Send(from, to, message.Group, text)
		if err != nil {
			return socketReply{OK: false, Error: err.Error()}
		}
		return socketReply{OK: true, Message: &envelope}
	case "ping":
		return socketReply{OK: true}
	default:
		return socketReply{OK: false, Error: "unknown action " + message.Action}
	}
}

func writeSocket(ctx context.Context, connection *websocket.Conn, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	writeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return connection.Write(writeContext, websocket.MessageText, encoded)
}
