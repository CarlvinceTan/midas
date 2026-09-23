package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Hub ties the pieces together: configuration, the bridge supervisor, the store,
// and in-flight logins. Everything is lazy — opening a hub starts no bridge.
type Hub struct {
	mu             sync.Mutex
	config         Config
	configLocation ConfigLocation
	stateDir       string
	supervisor     *Supervisor
	store          *Store
	logins         *loginManager

	notifications *notifySessions
	mcpOnce       sync.Once
	mcpInstance   *mcp.Server

	eventsMu    sync.Mutex
	subscribers map[string]func(Record)

	// sessionsMu guards the identity handed out to MCP sessions that do not
	// carry one of their own, which is what login ownership is keyed on.
	sessionsMu  sync.Mutex
	sessionKeys map[*mcp.ServerSession]string
	nextSession int
}

// Record is one event the hub publishes to its clients.
type Record struct {
	Type    string   `json:"type"`
	Bridge  string   `json:"bridge,omitempty"`
	Account string   `json:"account,omitempty"`
	Message *Message `json:"message,omitempty"`
	Login   *Login   `json:"login,omitempty"`
	Status  string   `json:"status,omitempty"`
}

// Open loads the hub's configuration and state. It starts nothing.
func Open(getenv func(string) string) (*Hub, error) {
	location, err := ResolveConfigLocation(getenv)
	if err != nil {
		return nil, err
	}
	config, err := location.Load()
	if err != nil {
		return nil, err
	}
	stateDir, err := StateDir(getenv)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(stateDir)
	if err != nil {
		return nil, err
	}
	hub := &Hub{
		config: config, configLocation: location, stateDir: stateDir,
		store: store, subscribers: map[string]func(Record){},
		notifications: newNotifySessions(),
		sessionKeys:   map[*mcp.ServerSession]string{},
	}
	hub.supervisor = newSupervisor(config.Bridges, hub.handleEvent)
	hub.logins = newLoginManager(func(login Login) {
		hub.publish(Record{Type: "login", Bridge: login.Bridge, Account: login.Account, Login: &login, Status: login.Status})
	})
	return hub, nil
}

// Config returns a copy of the loaded configuration.
func (h *Hub) Config() Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.config.Clone()
}

// updateConfig applies mutate and persists the result, both under the hub mutex,
// so concurrent updates cannot lose each other or marshal a map another
// goroutine is writing.
func (h *Hub) updateConfig(mutate func(*Config)) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	mutate(&h.config)
	return h.configLocation.Save(h.config.Clone())
}

// ConfigPath is where the hub reads and writes its settings: its own hub.json, or
// the agent's settings file when Midas launched it.
func (h *Hub) ConfigPath() string { return h.configLocation.ConfigPath() }

// SaveConfig persists the configuration, preserving any other section of a shared
// settings file.
func (h *Hub) SaveConfig(config Config) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.configLocation.Save(config.Clone())
}

// StateDir is where the hub keeps its database and installed bridges.
func (h *Hub) StateDir() string { return h.stateDir }

// BridgeStatuses lists every installed bridge and its supervisor state.
func (h *Hub) BridgeStatuses() []BridgeStatus {
	names := h.supervisor.Names()
	statuses := make([]BridgeStatus, 0, len(names))
	for _, name := range names {
		statuses = append(statuses, h.supervisor.Status(name))
	}
	return statuses
}

// StartBridge starts a bridge now instead of on first use.
func (h *Hub) StartBridge(ctx context.Context, name string) error {
	_, _, err := h.supervisor.acquire(ctx, name)
	return err
}

// StopBridge stops a running bridge; the next call starts it again.
func (h *Hub) StopBridge(name string) error {
	return h.supervisor.Stop(name)
}

// Accounts lists the accounts every installed bridge serves. Bridges without a
// cached answer are asked, which starts them.
func (h *Hub) Accounts(ctx context.Context) ([]Account, error) {
	names := h.supervisor.Names()
	if len(names) == 0 {
		return nil, nil
	}
	accounts := []Account{}
	failures := []string{}
	for _, name := range names {
		var reported []Account
		if err := h.supervisor.call(ctx, name, MethodAccounts, nil, &reported); err != nil {
			// A bridge that cannot be reached is reported when no bridge could be: an
			// empty account list would read as "this environment has no accounts",
			// which is the opposite of what happened.
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for index := range reported {
			reported[index].Bridge = name
		}
		h.rememberAccounts(name, reported)
		accounts = append(accounts, reported...)
	}
	if len(accounts) == 0 && len(failures) > 0 {
		return nil, errors.New("hub: " + strings.Join(failures, "; "))
	}
	slices.SortStableFunc(accounts, func(a, b Account) int {
		if a.Bridge == b.Bridge {
			return cmp.Compare(a.ID, b.ID)
		}
		return cmp.Compare(a.Bridge, b.Bridge)
	})
	return accounts, nil
}

// rememberAccounts caches the account list in configuration so a fresh process
// can answer without starting every bridge.
func (h *Hub) rememberAccounts(name string, accounts []Account) {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	slices.Sort(ids)
	_ = h.updateConfig(func(config *Config) {
		entry, ok := config.Bridges[name]
		if !ok {
			return
		}
		entry.Accounts = ids
		config.Bridges[name] = entry
	})
}

// SendRequest is one message to deliver: text, attachments, or both.
type SendRequest struct {
	Bridge  string
	Account string
	To      string
	Text    string
	// ReplyTo is the message this one answers, when it is a reply.
	ReplyTo string
	// Media are the attachments to send. Each names a path whose bytes the bridge
	// reads, or a URL a bridge that fetches remote media can use.
	Media []Media
}

// Send delivers a message through a bridge.
func (h *Hub) Send(ctx context.Context, request SendRequest) (Message, error) {
	text := strings.TrimSpace(request.Text)
	if text == "" && len(request.Media) == 0 {
		return Message{}, fmt.Errorf("hub: nothing to send")
	}
	resolved, err := h.resolveBridge(request.Bridge)
	if err != nil {
		return Message{}, err
	}
	request.Bridge = resolved
	if strings.TrimSpace(request.Account) == "" {
		return Message{}, fmt.Errorf("hub: an account is required to send")
	}
	if strings.TrimSpace(request.To) == "" {
		return Message{}, fmt.Errorf("hub: a recipient or thread is required to send")
	}
	params := map[string]any{"account": request.Account, "to": request.To, "text": text}
	if request.ReplyTo != "" {
		params["replyTo"] = request.ReplyTo
	}
	if len(request.Media) > 0 {
		params["media"] = request.Media
	}
	var sent Message
	if err := h.supervisor.call(ctx, request.Bridge, MethodSend, params, &sent); err != nil {
		return Message{}, err
	}
	if sent.Account == "" {
		sent.Account = request.Account
	}
	if sent.Thread == "" {
		sent.Thread = request.To
	}
	if sent.Timestamp == 0 {
		sent.Timestamp = time.Now().UnixMilli()
	}
	if sent.Kind == "" {
		sent.Kind = kindForMedia(request.Media, text)
	}
	if sent.Status == "" {
		sent.Status = StatusSent
	}
	if sent.ReplyTo == "" {
		sent.ReplyTo = request.ReplyTo
	}
	if err := h.store.Append(sent); err != nil {
		return Message{}, err
	}
	return sent, nil
}

// Delivery states a sent message moves through.
const (
	StatusPending   = "pending"
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusFailed    = "failed"
)

// kindForMedia names a message after its attachments, which is what a client
// renders: a voice note with a caption is still a voice note.
func kindForMedia(media []Media, text string) string {
	for _, attachment := range media {
		if strings.TrimSpace(attachment.Kind) != "" && attachment.Kind != KindText {
			return attachment.Kind
		}
	}
	if len(media) > 0 {
		return KindDocument
	}
	if text != "" {
		return KindText
	}
	return ""
}

// History returns stored messages, asking the bridge for anything older than
// what the hub already has when a thread is named.
func (h *Hub) History(ctx context.Context, bridge, account, thread string, limit int) ([]Message, error) {
	if resolved, err := h.resolveBridge(bridge); err == nil {
		bridge = resolved
	}
	if strings.TrimSpace(bridge) != "" {
		params := map[string]any{"account": account, "thread": thread, "limit": limit}
		var fetched []Message
		if err := h.supervisor.call(ctx, bridge, MethodHistory, params, &fetched); err != nil {
			// A bridge that refuses the call is reported when the hub has nothing of
			// its own to serve, so a wrong account name reads as an error instead of
			// an empty conversation. A bridge that simply cannot answer still leaves
			// whatever the hub already stored readable.
			if len(h.store.Messages(account, thread, 1)) == 0 {
				return nil, fmt.Errorf("hub: %s: %w", bridge, err)
			}
		} else {
			for _, message := range fetched {
				if message.Account == "" {
					message.Account = account
				}
				_ = h.store.Append(message)
			}
		}
	}
	return h.store.Messages(account, thread, limit), nil
}

// Threads lists conversations for an account: what the bridge knows, merged with
// what the hub has stored, with unread counts from the read cursors. A bridge that
// knows its structure reports each conversation — a channel inside a community, a
// group, a direct message — and a thread that never carried a message is still
// listed.
func (h *Hub) Threads(ctx context.Context, bridge, account string) ([]Thread, error) {
	if resolved, err := h.resolveBridge(bridge); err == nil {
		bridge = resolved
	}
	threads := h.store.Threads(account)
	seen := map[string]bool{}
	for _, thread := range threads {
		seen[readKey(thread.Account, thread.ID)] = true
	}
	if strings.TrimSpace(bridge) != "" {
		// A bridge is asked per account: an account-less request is not something a
		// service understands, and the tool lets the caller leave it out.
		accounts := []string{account}
		if strings.TrimSpace(account) == "" {
			accounts = accounts[:0]
			if reported, err := h.Accounts(ctx); err == nil {
				for _, entry := range reported {
					if entry.Bridge == bridge {
						accounts = append(accounts, entry.ID)
					}
				}
			}
		}
		for _, account := range accounts {
			if err := h.mergeReportedThreads(ctx, bridge, account); err != nil {
				// The same rule as history: a bridge that refuses the call is reported
				// when the hub has nothing of its own for that account, so a wrong
				// account name does not read as "no conversations".
				if len(h.store.Threads(account)) == 0 {
					return nil, fmt.Errorf("hub: %s: %w", bridge, err)
				}
			}
		}
		threads = h.store.Threads(account)
	}
	slices.SortStableFunc(threads, func(a, b Thread) int { return cmp.Compare(b.LastMessage, a.LastMessage) })
	return threads, nil
}

// mergeReportedThreads folds what a bridge says about one account's conversations
// into the store. A description wins over what the hub inferred, because the
// community a channel belongs to, its participants, and its muted state are only
// known to the bridge.
func (h *Hub) mergeReportedThreads(ctx context.Context, bridge, account string) error {
	if strings.TrimSpace(bridge) == "" {
		return nil
	}
	var reported []Thread
	if err := h.supervisor.call(ctx, bridge, MethodThreads, map[string]any{"account": account}, &reported); err != nil {
		return err
	}
	for _, thread := range reported {
		if thread.Account == "" {
			thread.Account = account
		}
		if strings.TrimSpace(thread.ID) == "" {
			continue
		}
		if thread.Bridge == "" {
			thread.Bridge = bridge
		}
		_ = h.store.SetThread(thread)
	}
	return nil
}

// MarkRead advances a thread's read cursor and tells the bridge, so other devices
// agree that the conversation was seen. A bridge that does not implement it is
// not an error: the local cursor is what unread counts use.
func (h *Hub) MarkRead(ctx context.Context, bridge, account, thread string, upTo int64) (Thread, error) {
	if resolved, err := h.resolveBridge(bridge); err == nil {
		bridge = resolved
	}
	if strings.TrimSpace(thread) == "" {
		return Thread{}, fmt.Errorf("hub: a thread is required to mark it read")
	}
	if err := h.store.MarkRead(account, thread, upTo); err != nil {
		return Thread{}, err
	}
	if strings.TrimSpace(bridge) != "" {
		params := map[string]any{"account": account, "thread": thread, "upTo": upTo}
		_ = h.supervisor.call(ctx, bridge, MethodMarkRead, params, nil)
	}
	for _, candidate := range h.store.Threads(account) {
		if candidate.ID == thread {
			return candidate, nil
		}
	}
	return Thread{ID: thread, Account: account}, nil
}

// LoginStart begins an account login and returns its first state.
func (h *Hub) LoginStart(ctx context.Context, session, bridge, account string) (Login, error) {
	resolved, err := h.resolveBridge(bridge)
	if err != nil {
		return Login{}, err
	}
	bridge = resolved
	return h.logins.start(ctx, session, bridge, account, func(callContext context.Context, method string, params map[string]any, result any) error {
		return h.supervisor.call(callContext, bridge, method, params, result)
	})
}

// LoginStatus reports a login, rendering the challenge for terminals when asked.
func (h *Hub) LoginStatus(id, session string, render bool) (Login, error) {
	login, ok := h.logins.status(id)
	if !ok {
		return Login{}, fmt.Errorf("hub: no such login")
	}
	if !h.logins.ownedBy(id, session) {
		return Login{}, fmt.Errorf("hub: this login belongs to another session")
	}
	if render && login.Challenge.Kind == "qr" && login.Challenge.Payload != "" {
		login.Rendered = RenderQR(login.Challenge.Payload)
	}
	return login, nil
}

// LoginSubmit answers a challenge that expects input.
func (h *Hub) LoginSubmit(ctx context.Context, id, session, response string) (Login, error) {
	login, ok := h.logins.status(id)
	if !ok {
		return Login{}, fmt.Errorf("hub: no such login")
	}
	if !h.logins.ownedBy(id, session) {
		return Login{}, fmt.Errorf("hub: this login belongs to another session")
	}
	if err := h.supervisor.call(ctx, login.Bridge, MethodAnswer, map[string]any{
		"account": login.Account, "response": response,
	}, nil); err != nil {
		return Login{}, err
	}
	return h.logins.answer(id, response)
}

// LoginCancel drops an in-flight login.
func (h *Hub) LoginCancel(ctx context.Context, id, session string) error {
	login, ok := h.logins.status(id)
	if !ok {
		return fmt.Errorf("hub: no such login")
	}
	if !h.logins.ownedBy(id, session) {
		return fmt.Errorf("hub: this login belongs to another session")
	}
	_ = h.supervisor.call(ctx, login.Bridge, MethodCancel, map[string]any{"account": login.Account}, nil)
	h.logins.cancel(id)
	return nil
}

// Logins lists in-flight logins.
func (h *Hub) Logins() []Login { return h.logins.list() }

// Subscribe registers a callback for hub events and returns a cancel function.
func (h *Hub) Subscribe(key string, callback func(Record)) func() {
	h.eventsMu.Lock()
	h.subscribers[key] = callback
	h.eventsMu.Unlock()
	return func() {
		h.eventsMu.Lock()
		delete(h.subscribers, key)
		h.eventsMu.Unlock()
	}
}

// Reap stops idle bridges. Callers run it on a ticker; the hub itself runs no
// timers when nothing is attached.
func (h *Hub) Reap() int { return h.supervisor.Reap(h.Config().IdleTTL()) }

// Close stops every bridge.
func (h *Hub) Close() { h.supervisor.StopAll() }

// Stats reports supervisor counters, which is how tests and operators see that
// nothing starts until it is used.
func (h *Hub) Stats() (calls, started, stopped int64) {
	h.supervisor.mu.Lock()
	defer h.supervisor.mu.Unlock()
	return h.supervisor.calls, h.supervisor.started, h.supervisor.stopped
}

// sessionKey is a stable identity for an MCP session. Transports do not always
// hand out an ID, so sessions without one get a hub-assigned key: ownership must
// never fall back to "any session".
func (h *Hub) sessionKey(session *mcp.ServerSession) string {
	if session == nil {
		return ""
	}
	if id := strings.TrimSpace(session.ID()); id != "" {
		return id
	}
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	if key, ok := h.sessionKeys[session]; ok {
		return key
	}
	h.nextSession++
	key := fmt.Sprintf("session_%d", h.nextSession)
	h.sessionKeys[session] = key
	return key
}

// handleEvent folds a bridge event into the store, the logins, and subscribers.
func (h *Hub) handleEvent(bridge string, event Event) {
	switch event.Event {
	case EventMessage:
		var message Message
		if json.Unmarshal(event.Data, &message) != nil {
			return
		}
		// Every account-scoped record names its account: a bridge serves several,
		// so guessing from the bridge name would invent one.
		if strings.TrimSpace(message.Account) == "" {
			return
		}
		_ = h.store.Append(message)
		h.publish(Record{Type: "message", Bridge: bridge, Account: message.Account, Message: &message})
	case EventEdit:
		var envelope struct {
			ID      string `json:"id"`
			Account string `json:"account"`
			Thread  string `json:"thread"`
			Text    string `json:"text"`
			At      int64  `json:"at"`
		}
		if json.Unmarshal(event.Data, &envelope) != nil {
			return
		}
		updated, ok := h.store.Edit(envelope.Account, envelope.ID, envelope.Text, envelope.At)
		if !ok {
			return
		}
		h.publish(Record{Type: "edit", Bridge: bridge, Account: updated.Account, Message: &updated})
	case EventDelete:
		var envelope struct {
			ID      string `json:"id"`
			Account string `json:"account"`
		}
		if json.Unmarshal(event.Data, &envelope) != nil {
			return
		}
		updated, ok := h.store.Delete(envelope.Account, envelope.ID)
		if !ok {
			return
		}
		h.publish(Record{Type: "delete", Bridge: bridge, Account: updated.Account, Message: &updated})
	case EventReaction:
		var envelope struct {
			ID      string `json:"id"`
			Account string `json:"account"`
			Emoji   string `json:"emoji"`
			Sender  string `json:"sender"`
			At      int64  `json:"at"`
			Removed bool   `json:"removed"`
		}
		if json.Unmarshal(event.Data, &envelope) != nil {
			return
		}
		updated, ok := h.store.React(envelope.Account, envelope.ID, Reaction{
			Emoji: envelope.Emoji, Sender: envelope.Sender, At: envelope.At, Removed: envelope.Removed,
		})
		if !ok {
			return
		}
		h.publish(Record{Type: "reaction", Bridge: bridge, Account: updated.Account, Message: &updated})
	case EventThread:
		var thread Thread
		if json.Unmarshal(event.Data, &thread) != nil {
			return
		}
		if thread.Bridge == "" {
			thread.Bridge = bridge
		}
		if err := h.store.SetThread(thread); err != nil {
			return
		}
		h.publish(Record{Type: "thread", Bridge: bridge, Account: thread.Account})
	case EventChallenge:
		var challenge Challenge
		if json.Unmarshal(event.Data, &challenge) != nil {
			return
		}
		var account string
		var envelope struct {
			Account string `json:"account"`
		}
		_ = json.Unmarshal(event.Data, &envelope)
		account = envelope.Account
		if _, ok := h.logins.apply(bridge, account, challenge); !ok {
			h.publish(Record{Type: "login", Bridge: bridge, Account: account, Status: LoginPending})
		}
	case EventStatus:
		var envelope struct {
			Account string `json:"account"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(event.Data, &envelope) != nil {
			return
		}
		h.logins.complete(bridge, envelope.Account, envelope.Status, envelope.Reason)
		h.publish(Record{Type: "status", Bridge: bridge, Account: envelope.Account, Status: envelope.Status})
	}
}

func (h *Hub) publish(record Record) {
	h.push(record)
	h.eventsMu.Lock()
	subscribers := make([]func(Record), 0, len(h.subscribers))
	for key, callback := range h.subscribers {
		_ = key
		subscribers = append(subscribers, callback)
	}
	h.eventsMu.Unlock()
	for _, callback := range subscribers {
		callback(record)
	}
}

// singleBridge is the bridge to use when a caller does not name one: the only
// installed one, or empty when the choice is ambiguous.
func (h *Hub) singleBridge() string {
	names := h.supervisor.Names()
	if len(names) == 1 {
		return names[0]
	}
	return ""
}

// resolveBridge turns an optional bridge name into the one to use, explaining an
// ambiguous choice instead of reporting a bridge that does not exist.
func (h *Hub) resolveBridge(bridge string) (string, error) {
	if trimmed := strings.TrimSpace(bridge); trimmed != "" {
		return trimmed, nil
	}
	names := h.supervisor.Names()
	switch len(names) {
	case 0:
		return "", errNoBridges
	case 1:
		return names[0], nil
	default:
		return "", fmt.Errorf("several bridges are installed (%s); name one", strings.Join(names, ", "))
	}
}
