// Command mock-bridge is the reference bridge: it speaks the hub's bridge
// protocol over stdio and pretends to be a chat service, so it is both the
// starting point for a real bridge and the fixture the hub's end-to-end tests
// drive.
//
// It models the shapes a real service has: several accounts in one process,
// direct messages, groups, and a community ("space") with channels inside it,
// attachments of every kind (image, voice note, document, sticker) with real
// files on disk, replies, edits, reactions, deletions, and a device-link login
// that only accepts the right token.
//
// Everything is deterministic on purpose: events are emitted in response to a
// call rather than on a timer, so a test never has to sleep to see them.
//
//	HUB_MOCK_MEDIA_DIR       where the scripted attachment files are written
//	HUB_MOCK_INJECT          delay, e.g. 50ms, before one incoming message is pushed
//	HUB_MOCK_INJECT_TEXT     what that incoming message says
//	HUB_MOCK_LOGIN_TTL       how long a login challenge stays valid, default 2m
//	HUB_MOCK_NO_ACCOUNTS     serve no accounts at all, like a service nobody has
//	                         signed in to yet
//	HUB_MOCK_CRASH_ON_SEND   exit instead of answering the nth send, e.g. 1
//	HUB_MOCK_FLOOD           answer hello with one line of this many bytes, too long
//	                         for the hub's framing, which is how a runaway bridge looks
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/pkg/hub"
)

func main() {
	bridge := newMockBridge()
	if err := hub.ServeBridge(os.Stdin, os.Stdout, bridge.handle); err != nil {
		fmt.Fprintln(os.Stderr, "mock-bridge:", err)
		os.Exit(1)
	}
}

// mockBridge is one process serving several accounts, which is the shape the hub
// expects: accounts are addressed by ID through every method.
type mockBridge struct {
	accounts []hub.Account
	started  time.Time
	mediaDir string
	// threads are the conversations the service knows about, including channels
	// that have never carried a message.
	threads map[string]hub.Thread

	mu           sync.Mutex
	sent         map[string][]hub.Message // account ID to its messages
	historyCalls map[string]int           // account/thread to how often it was read
	sends        int
	logins       map[string]loginState
}

// loginState is one in-flight device link.
type loginState struct {
	token     string
	expiresAt time.Time
	cancelled bool
}

func newMockBridge() *mockBridge {
	mediaDir := strings.TrimSpace(os.Getenv("HUB_MOCK_MEDIA_DIR"))
	if mediaDir == "" {
		mediaDir = filepath.Join(os.TempDir(), "midas-mock-bridge-media")
	}
	_ = os.MkdirAll(mediaDir, 0o755)
	bridge := &mockBridge{
		accounts: []hub.Account{
			{ID: "personal", DisplayName: "Mock Personal", Status: "connected"},
			{ID: "work", DisplayName: "Mock Work", Status: "connected"},
		},
		started: time.Now(), mediaDir: mediaDir,
		sent: map[string][]hub.Message{}, historyCalls: map[string]int{}, logins: map[string]loginState{},
	}
	bridge.threads = scriptedThreads()
	return bridge
}

// scriptedThreads is the structure the mock reports: a community with two
// channels, a group, and a direct message. The general channel deliberately has no
// messages, so a client has to list a conversation it has never read.
func scriptedThreads() map[string]hub.Thread {
	return map[string]hub.Thread{
		"space:acme": {
			ID: "space:acme", Account: "personal", Title: "Acme", Kind: hub.ThreadCommunity,
			Participants: []string{"me", "alice", "bob"},
		},
		"space:acme/general": {
			ID: "space:acme/general", Account: "personal", Title: "#general", Kind: hub.ThreadChannel,
			Parent: "space:acme", Participants: []string{"alice", "bob", "carol"},
		},
		"space:acme/ops": {
			ID: "space:acme/ops", Account: "personal", Title: "#ops", Kind: hub.ThreadChannel,
			Parent: "space:acme", Participants: []string{"alice", "bob"},
			// A service reports how much is waiting before a client reads anything.
			Unread: 3,
		},
		"dm:alice": {
			ID: "dm:alice", Account: "personal", Title: "Alice", Kind: hub.ThreadDirect,
			Participants: []string{"alice"},
		},
		"group:team": {
			ID: "group:team", Account: "personal", Title: "Team", Kind: hub.ThreadGroup,
			Participants: []string{"alice", "bob", "carol"},
		},
		"dm:bob": {
			ID: "dm:bob", Account: "work", Title: "Bob", Kind: hub.ThreadDirect,
			Participants: []string{"bob"},
		},
	}
}

func (b *mockBridge) handle(_ context.Context, method string, params map[string]any, emit func(hub.Event)) (any, error) {
	switch method {
	case hub.MethodHello:
		if os.Getenv("HUB_MOCK_CRASH_AFTER_HELLO") != "" {
			go func() {
				time.Sleep(10 * time.Millisecond)
				os.Exit(1)
			}()
		}
		// HUB_MOCK_INJECT makes the bridge push one incoming message after the
		// given delay, which is how a demo or a test exercises event delivery.
		if delay, err := time.ParseDuration(os.Getenv("HUB_MOCK_INJECT")); err == nil && delay > 0 {
			account := b.accounts[0].ID
			if requested := stringParam(params, "account"); requested != "" {
				account = requested
			}
			text := strings.TrimSpace(os.Getenv("HUB_MOCK_INJECT_TEXT"))
			if text == "" {
				text = "hello from the mock"
			}
			go func() {
				time.Sleep(delay)
				message := hub.Message{
					ID:      fmt.Sprintf("mock-in-%d", time.Now().UnixNano()),
					Account: account, Thread: "dm:alice", Sender: "alice", Text: text,
					Timestamp: time.Now().UnixMilli(), Incoming: true, Kind: hub.KindText,
				}
				b.record(message)
				data, _ := json.Marshal(message)
				emit(hub.Event{Event: hub.EventMessage, Data: data})
			}()
		}
		// HUB_MOCK_FLOOD makes the bridge answer hello with a single line too long
		// for the hub's framing, which is what a runaway bridge looks like.
		if flood, err := strconv.Atoi(strings.TrimSpace(os.Getenv("HUB_MOCK_FLOOD"))); err == nil && flood > 0 {
			return map[string]any{"name": "mock", "flood": strings.Repeat("x", flood)}, nil
		}
		return map[string]any{"name": "mock", "version": "0.1.0", "accounts": len(b.accounts)}, nil
	case hub.MethodAccounts:
		// HUB_MOCK_NO_ACCOUNTS is a service that is signed in to nothing yet, which
		// a client has to render as an empty list rather than a failure.
		if os.Getenv("HUB_MOCK_NO_ACCOUNTS") != "" {
			return []hub.Account{}, nil
		}
		return b.accounts, nil
	case hub.MethodSend:
		account := stringParam(params, "account")
		if err := b.requireAccount(account); err != nil {
			return nil, err
		}
		b.mu.Lock()
		b.sends++
		sends := b.sends
		b.mu.Unlock()
		if crash, err := strconv.Atoi(strings.TrimSpace(os.Getenv("HUB_MOCK_CRASH_ON_SEND"))); err == nil && crash == sends {
			// A bridge that dies mid-conversation: the hub has to notice rather than
			// hang on a request nobody will answer.
			os.Exit(1)
		}
		to := stringParam(params, "to")
		text := stringParam(params, "text")
		media := mediaParam(params)
		if strings.TrimSpace(text) == "" && len(media) == 0 {
			return nil, fmt.Errorf("nothing to send")
		}
		sent := hub.Message{
			ID:      fmt.Sprintf("mock-%d", time.Now().UnixNano()),
			Account: account, Thread: to, Text: text, Media: media,
			ReplyTo: stringParam(params, "replyTo"), Kind: kindFor(media, text),
			Timestamp: time.Now().UnixMilli(), Status: hub.StatusSent,
		}
		b.record(sent)
		// Sending to the echo thread brings a reply back, which is how a test sees
		// an incoming event without a timer.
		if to == "echo" {
			reply := hub.Message{
				ID: fmt.Sprintf("mock-echo-%d", time.Now().UnixNano()), Account: account,
				Thread: "echo", Sender: "echo-bot", Text: "you said: " + text, Media: media,
				ReplyTo: sent.ID, Kind: kindFor(media, text),
				Timestamp: time.Now().UnixMilli(), Incoming: true,
			}
			b.record(reply)
			data, _ := json.Marshal(reply)
			emit(hub.Event{Event: hub.EventMessage, Data: data})
		}
		return sent, nil
	case hub.MethodHistory:
		account := stringParam(params, "account")
		if err := b.requireAccount(account); err != nil {
			return nil, err
		}
		thread := stringParam(params, "thread")
		b.mu.Lock()
		key := account + "/" + thread
		b.historyCalls[key]++
		calls := b.historyCalls[key]
		b.mu.Unlock()
		if thread == "updates" && calls >= 2 {
			// The second read of this thread reports what a service reports when
			// someone reacts, when the sender corrects a message, and when a message
			// is deleted. It is emitted before the response, so the hub has folded it
			// in by the time the caller sees the history.
			b.emitUpdates(account, emit)
		}
		return b.history(account, thread), nil
	case hub.MethodThreads:
		account := stringParam(params, "account")
		if err := b.requireAccount(account); err != nil {
			return nil, err
		}
		return b.threadList(account), nil
	case hub.MethodMarkRead:
		account := stringParam(params, "account")
		if err := b.requireAccount(account); err != nil {
			return nil, err
		}
		// A service clears the unread count it reports once the client reads the
		// thread, which is what keeps a client from being told about messages it has
		// already seen.
		b.mu.Lock()
		thread := b.threads[stringParam(params, "thread")]
		thread.Unread = 0
		b.threads[thread.ID] = thread
		b.mu.Unlock()
		return map[string]any{"ok": true}, nil
	case hub.MethodLogin:
		account := stringParam(params, "account")
		if err := b.requireAccount(account); err != nil {
			return nil, err
		}
		// A real bridge starts a device link here; the mock hands out a QR token that
		// only the matching answer accepts, so a test can prove the round trip and
		// the refusals rather than "any answer works".
		ttl := 2 * time.Minute
		if configured, err := time.ParseDuration(strings.TrimSpace(os.Getenv("HUB_MOCK_LOGIN_TTL"))); err == nil && configured != 0 {
			ttl = configured
		}
		expires := time.Now().Add(ttl)
		token := "mock-login-" + account + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		b.mu.Lock()
		b.logins[account] = loginState{token: token, expiresAt: expires}
		b.mu.Unlock()
		return hub.Challenge{
			Kind: "qr", Payload: token,
			Hint:      "Scan with the mock app for " + account,
			ExpiresAt: expires.UnixMilli(),
		}, nil
	case hub.MethodAnswer:
		account := stringParam(params, "account")
		if err := b.requireAccount(account); err != nil {
			return nil, err
		}
		response := stringParam(params, "response")
		b.mu.Lock()
		state, ok := b.logins[account]
		b.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("no login is waiting for %s", account)
		}
		if state.cancelled {
			return nil, fmt.Errorf("the login for %s was cancelled", account)
		}
		if !time.Now().Before(state.expiresAt) {
			return nil, fmt.Errorf("the code for %s expired", account)
		}
		if strings.TrimSpace(response) != "scan-"+account {
			return nil, fmt.Errorf("%q is not the code shown for %s", response, account)
		}
		b.mu.Lock()
		b.accounts = setAccountStatus(b.accounts, account, "connected")
		delete(b.logins, account)
		b.mu.Unlock()
		status := map[string]any{"account": account, "status": "connected"}
		data, _ := json.Marshal(status)
		emit(hub.Event{Event: hub.EventStatus, Data: data})
		return status, nil
	case hub.MethodCancel:
		account := stringParam(params, "account")
		if err := b.requireAccount(account); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if state, ok := b.logins[account]; ok {
			state.cancelled = true
			b.logins[account] = state
		}
		b.mu.Unlock()
		return map[string]any{"status": "cancelled"}, nil
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

// emitUpdates reports a reaction, an edit, a deletion, and a thread description,
// which is what a real service sends for those changes.
func (b *mockBridge) emitUpdates(account string, emit func(hub.Event)) {
	events := []hub.Event{
		{Event: hub.EventThread, Data: mustJSON(hub.Thread{
			ID: "updates", Account: account, Title: "Updates", Kind: hub.ThreadChannel,
			Parent: "space:acme", Participants: []string{"alice", "bob"}, Muted: boolPointer(true),
		})},
		{Event: hub.EventReaction, Data: mustJSON(map[string]any{
			"id": "upd-1", "account": account, "emoji": "👍", "sender": "alice", "at": time.Now().UnixMilli(),
		})},
		{Event: hub.EventEdit, Data: mustJSON(map[string]any{
			"id": "upd-1", "account": account, "text": "deploy is green (edited)", "at": time.Now().UnixMilli(),
		})},
		{Event: hub.EventDelete, Data: mustJSON(map[string]any{"id": "upd-2", "account": account})},
	}
	for _, event := range events {
		emit(event)
	}
}

// history is the scripted conversation for a thread plus anything the hub sent.
func (b *mockBridge) history(account, thread string) []hub.Message {
	messages := []hub.Message{}
	for _, message := range b.scripted(account) {
		if thread != "" && message.Thread != thread {
			continue
		}
		messages = append(messages, message)
	}
	for _, message := range b.messages(account) {
		if thread != "" && message.Thread != thread {
			continue
		}
		messages = append(messages, message)
	}
	return messages
}

// scripted is the conversation a service would already have when a client links
// it: a community channel with every attachment kind, an update thread, a group,
// and a direct message.
func (b *mockBridge) scripted(account string) []hub.Message {
	if account != "personal" {
		if account == "work" {
			return []hub.Message{
				{ID: "work-1", Account: "work", Thread: "dm:bob", Sender: "bob", Text: "standup at ten",
					Timestamp: 1_700_000_000_000, Incoming: true, Kind: hub.KindText},
			}
		}
		return nil
	}
	base := int64(1_700_000_000_000)
	media := b.media()
	return []hub.Message{
		{ID: "ops-1", Account: "personal", Thread: "space:acme/ops", Sender: "alice", Text: "deploy is green",
			Timestamp: base, Incoming: true, Kind: hub.KindText},
		{ID: "ops-2", Account: "personal", Thread: "space:acme/ops", Sender: "alice", Text: "the dashboard",
			Timestamp: base + 1, Incoming: true, Kind: hub.KindImage, ReplyTo: "ops-1", Media: []hub.Media{media["image"]}},
		{ID: "ops-3", Account: "personal", Thread: "space:acme/ops", Sender: "bob", Text: "",
			Timestamp: base + 2, Incoming: true, Kind: hub.KindVoice, Media: []hub.Media{media["voice"]}},
		{ID: "ops-4", Account: "personal", Thread: "space:acme/ops", Sender: "bob", Text: "the report",
			Timestamp: base + 3, Incoming: true, Kind: hub.KindDocument, Media: []hub.Media{media["document"]}},
		{ID: "ops-5", Account: "personal", Thread: "space:acme/ops", Sender: "carol", Text: "",
			Timestamp: base + 4, Incoming: true, Kind: hub.KindSticker, Media: []hub.Media{media["sticker"]}},
		{ID: "ops-6", Account: "personal", Thread: "space:acme/ops", Sender: "system", Text: "carol joined the channel",
			Timestamp: base + 5, Incoming: true, Kind: hub.KindSystem},
		{ID: "upd-1", Account: "personal", Thread: "updates", Sender: "alice", Text: "deploy is green",
			Timestamp: base + 10, Incoming: true, Kind: hub.KindText},
		{ID: "upd-2", Account: "personal", Thread: "updates", Sender: "bob", Text: "rolling back",
			Timestamp: base + 11, Incoming: true, Kind: hub.KindText},
		{ID: "team-1", Account: "personal", Thread: "group:team", Sender: "bob", Text: "planning at three",
			Timestamp: base + 20, Incoming: true, Kind: hub.KindText},
		{ID: "alice-1", Account: "personal", Thread: "dm:alice", Sender: "alice", Text: "sent you the file",
			Timestamp: base + 30, Incoming: true, Kind: hub.KindText},
	}
}

// media writes the scripted attachments once and describes them, so the bytes the
// hub passes around are real: a test can open the file, hash it, and check its size.
func (b *mockBridge) media() map[string]hub.Media {
	// A 1×1 transparent PNG and a small WAV header are enough to be real files of
	// the right type; the point is the plumbing, not the pixels.
	files := map[string][]byte{
		"photo.png":   {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0},
		"voice.wav":   append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 16)...),
		"report.pdf":  []byte("%PDF-1.4\n% mock report\n"),
		"sticker.png": {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3},
	}
	kinds := map[string]hub.Media{
		"image":    {Kind: hub.KindImage, MIME: "image/png", Filename: "photo.png"},
		"voice":    {Kind: hub.KindVoice, MIME: "audio/wav", Filename: "voice.wav", DurationMS: 1200},
		"document": {Kind: hub.KindDocument, MIME: "application/pdf", Filename: "report.pdf"},
		"sticker":  {Kind: hub.KindSticker, MIME: "image/png", Filename: "sticker.png"},
	}
	filenames := map[string]string{
		"image": "photo.png", "voice": "voice.wav", "document": "report.pdf", "sticker": "sticker.png",
	}
	for kind, name := range filenames {
		path := filepath.Join(b.mediaDir, name)
		if err := os.WriteFile(path, files[name], 0o644); err != nil {
			continue
		}
		entry := kinds[kind]
		entry.Path = path
		entry.Size = int64(len(files[name]))
		entry.SHA256 = digest(files[name])
		kinds[kind] = entry
	}
	return kinds
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// threadList is what the service knows about the account's conversations, merged
// with anything the hub sent.
func (b *mockBridge) threadList(account string) []hub.Thread {
	threads := map[string]hub.Thread{}
	for id, thread := range b.threads {
		if thread.Account == account {
			threads[id] = thread
		}
	}
	for _, message := range b.history(account, "") {
		thread, ok := threads[message.Thread]
		if !ok {
			thread = hub.Thread{ID: message.Thread, Account: account}
		}
		if message.Timestamp > thread.LastMessage {
			thread.LastMessage = message.Timestamp
		}
		threads[message.Thread] = thread
	}
	ids := make([]string, 0, len(threads))
	for id := range threads {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	list := make([]hub.Thread, 0, len(ids))
	for _, id := range ids {
		list = append(list, threads[id])
	}
	return list
}

// requireAccount rejects a call that names an account this bridge does not serve,
// which is what keeps a multi-account client honest.
func (b *mockBridge) requireAccount(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("an account is required")
	}
	for _, account := range b.accounts {
		if account.ID == id {
			return nil
		}
	}
	return fmt.Errorf("no such account %q; this bridge serves %s", id, b.accountIDs())
}

func (b *mockBridge) accountIDs() string {
	ids := make([]string, 0, len(b.accounts))
	for _, account := range b.accounts {
		ids = append(ids, account.ID)
	}
	return strings.Join(ids, ", ")
}

func (b *mockBridge) record(message hub.Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sent == nil {
		b.sent = map[string][]hub.Message{}
	}
	b.sent[message.Account] = append(b.sent[message.Account], message)
}

func (b *mockBridge) messages(account string) []hub.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]hub.Message(nil), b.sent[account]...)
}

func setAccountStatus(accounts []hub.Account, id, status string) []hub.Account {
	updated := make([]hub.Account, 0, len(accounts))
	for _, account := range accounts {
		if account.ID == id {
			account.Status = status
		}
		updated = append(updated, account)
	}
	return updated
}

func kindFor(media []hub.Media, text string) string {
	for _, attachment := range media {
		if attachment.Kind != "" {
			return attachment.Kind
		}
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return hub.KindText
}

// boolPointer is how a bridge reports a flag it actually knows: an absent field
// means it said nothing about it.
func boolPointer(value bool) *bool { return &value }

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// mediaParam reads the attachments of a send call.
func mediaParam(params map[string]any) []hub.Media {
	raw, ok := params["media"]
	if !ok {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var media []hub.Media
	if err := json.Unmarshal(data, &media); err != nil {
		return nil
	}
	return media
}

func stringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return value
}
