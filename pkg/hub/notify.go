package hub

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// notifySessions tracks the MCP sessions a hub server is serving, so a hub that
// nothing has connected to sends nothing.
type notifySessions struct {
	mu       sync.Mutex
	sessions map[string]*mcp.ServerSession
}

func newNotifySessions() *notifySessions {
	return &notifySessions{sessions: map[string]*mcp.ServerSession{}}
}

func (n *notifySessions) drop(key string) {
	n.mu.Lock()
	delete(n.sessions, key)
	n.mu.Unlock()
}

// attachSession records a session so it can be dropped when it closes. Push
// itself rides resource subscriptions, which the SDK routes per session, so this
// only needs to know when a session goes away.
func (h *Hub) attachSession(session *mcp.ServerSession) {
	if session == nil {
		return
	}
	key := h.sessionKey(session)
	if key == "" {
		return
	}
	h.notifications.mu.Lock()
	_, attached := h.notifications.sessions[key]
	h.notifications.sessions[key] = session
	h.notifications.mu.Unlock()
	if attached {
		return
	}
	go func() {
		_ = session.Wait()
		h.notifications.drop(key)
	}()
}

// push tells subscribed clients that a record changed the resource it belongs to.
// The MCP protocol has no generic server notification, and logging notifications
// are deprecated, so resource updates are the channel: a client that wants push
// subscribes to hub://threads/{account}/{thread} or hub://logins/{loginID} and
// hears about every change. A client that does not subscribe loses nothing: the
// same data is readable through the tools.
func (h *Hub) push(record Record) {
	server := h.mcpInstance
	if server == nil {
		return
	}
	for _, uri := range recordURIs(record) {
		_ = server.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: uri})
	}
}

// recordURIs are the resources a record belongs to.
func recordURIs(record Record) []string {
	uris := []string{}
	if record.Message != nil {
		uris = append(uris, threadURI(record.Message.Account, record.Message.Thread))
	}
	if record.Login != nil {
		uris = append(uris, loginURI(record.Login.ID))
	}
	return uris
}

// threadURI and loginURI escape their segments: a thread id may contain the
// characters a URI reserves, such as the colon in "dm:alice" or the slash in
// "space:acme/ops", and the resource template only matches unreserved characters
// or percent-escapes.
func threadURI(account, thread string) string {
	return "hub://threads/" + escapeSegment(account) + "/" + escapeSegment(thread)
}

func loginURI(id string) string { return "hub://logins/" + escapeSegment(id) }

// escapeSegment percent-encodes everything a URI template does not accept
// unescaped. It works byte by byte, so a multi-byte rune is escaped correctly too.
func escapeSegment(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9',
			char == '-', char == '.', char == '_', char == '~':
			builder.WriteByte(char)
		default:
			fmt.Fprintf(&builder, "%%%02X", char)
		}
	}
	return builder.String()
}
