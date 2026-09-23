package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The bridge protocol is intentionally tiny: newline-delimited JSON over the
// bridge's stdio. A bridge can be written in any language in about fifty lines,
// which is what keeps the hub from growing a bridge framework.
//
// Hub to bridge:
//
//	{"id":1,"method":"hello"}
//	{"id":2,"method":"accounts"}
//	{"id":3,"method":"send","params":{"account":"personal","to":"...","text":"..."}}
//	{"id":4,"method":"history","params":{"account":"personal","thread":"...","limit":20}}
//
// Bridge to hub:
//
//	{"id":1,"result":{...}}                      response to a request
//	{"id":3,"error":"no such account"}           failed request
//	{"event":"message","data":{...}}             unsolicited event
//	{"event":"challenge","data":{...}}           a login step the user must act on
const (
	MethodHello    = "hello"
	MethodAccounts = "accounts"
	MethodSend     = "send"
	MethodHistory  = "history"
	MethodThreads  = "threads"
	MethodMarkRead = "markRead"
	MethodLogin    = "login"
	MethodAnswer   = "answer"
	MethodCancel   = "cancel"

	EventMessage   = "message"
	EventChallenge = "challenge"
	EventStatus    = "status"
	// EventEdit, EventDelete, and EventReaction report a change to a message the
	// hub already has. A bridge that cannot report them simply never emits them.
	EventEdit     = "edit"
	EventDelete   = "delete"
	EventReaction = "reaction"
	// EventThread describes a conversation itself: its kind, the community it
	// belongs to, and who is in it. Bridges that know their structure emit it so a
	// client can render channels inside a space rather than a flat list.
	EventThread = "thread"
)

// Message kinds. Kind is what the message is, so a client can render it without
// inspecting the media: text is the default when a bridge says nothing.
const (
	KindText     = "text"
	KindImage    = "image"
	KindVideo    = "video"
	KindAudio    = "audio"
	KindVoice    = "voice"
	KindDocument = "document"
	KindSticker  = "sticker"
	KindLocation = "location"
	KindSystem   = "system"
)

// Conversation kinds. Kind is what the thread is, and Parent names the community
// a channel belongs to.
const (
	ThreadDirect    = "dm"
	ThreadGroup     = "group"
	ThreadChannel   = "channel"
	ThreadCommunity = "community"
)

// Media is one attachment. Bytes belong to the bridge and the service, so the hub
// keeps a reference — a path on this machine or a URL — rather than copying
// content into its own log.
type Media struct {
	// Kind is image, video, audio, voice, document, sticker, or location.
	Kind string `json:"kind,omitempty"`
	// MIME is the content type, e.g. image/png or audio/ogg.
	MIME string `json:"mime,omitempty"`
	// Filename is the name the sender gave it, when it has one.
	Filename string `json:"filename,omitempty"`
	// Size is the byte length.
	Size int64 `json:"size,omitempty"`
	// Caption is the text attached to the media itself, which some services keep
	// separate from the message text.
	Caption string `json:"caption,omitempty"`
	// DurationMS is the length of a voice note, video, or audio.
	DurationMS int64 `json:"durationMs,omitempty"`
	// Path is where the bytes are on this machine, and URL is where they can be
	// fetched when the bridge serves them instead. A bridge sets one of the two.
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	// SHA256 is the digest of the bytes when the bridge knows it.
	SHA256 string `json:"sha256,omitempty"`
	// Thumbnail is a small preview, as a path or a data URL.
	Thumbnail string `json:"thumbnail,omitempty"`
}

// Reaction is one person's reaction to a message. A reaction with Removed set
// takes that person's reaction away again.
type Reaction struct {
	Emoji   string `json:"emoji"`
	Sender  string `json:"sender,omitempty"`
	At      int64  `json:"at,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

// Request is one hub-to-bridge call.
type Request struct {
	ID     int64          `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

// Response is a bridge's reply, carrying either a result or an error.
type Response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Event is an unsolicited bridge-to-hub message.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Account is one account a bridge serves.
type Account struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	Bridge      string `json:"bridge,omitempty"`
	// Status is "connected", "logged_out", or "pending".
	Status string `json:"status,omitempty"`
}

// Thread is one conversation an account knows about.
type Thread struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	Title       string `json:"title,omitempty"`
	Bridge      string `json:"bridge,omitempty"`
	Unread      int    `json:"unread,omitempty"`
	LastMessage int64  `json:"lastMessage,omitempty"`
	// Kind is dm, group, channel, or community. Empty means the bridge did not say.
	Kind string `json:"kind,omitempty"`
	// Parent is the community or group a channel belongs to.
	Parent string `json:"parent,omitempty"`
	// Participants are the people in the conversation, when the bridge knows them.
	Participants []string `json:"participants,omitempty"`
	// Muted and Archived are the service's own view of the conversation. They are
	// pointers because a report that does not mention them (most bridges describe a
	// conversation without repeating every flag) must not clear what the service
	// said before; nil means "not reported", not "false".
	Muted    *bool `json:"muted,omitempty"`
	Archived *bool `json:"archived,omitempty"`
}

// Message is one stored or freshly received message.
type Message struct {
	ID        string `json:"id"`
	Account   string `json:"account"`
	Thread    string `json:"thread"`
	Sender    string `json:"sender,omitempty"`
	Text      string `json:"text"`
	Timestamp int64  `json:"timestamp"`
	// Incoming is false for messages this hub sent.
	Incoming bool `json:"incoming,omitempty"`
	// Kind is what the message is: text when the bridge does not say.
	Kind string `json:"kind,omitempty"`
	// Media are the attachments on this message.
	Media []Media `json:"media,omitempty"`
	// ReplyTo is the message this one answers, when it is a reply.
	ReplyTo string `json:"replyTo,omitempty"`
	// Reactions are folded into this message as the bridge reports them.
	Reactions []Reaction `json:"reactions,omitempty"`
	// Edited marks a message whose text was replaced after it was sent.
	Edited bool `json:"edited,omitempty"`
	// Deleted marks a message the sender removed. Its content is cleared, but the
	// record stays so history does not shift under a client that already has it.
	Deleted bool `json:"deleted,omitempty"`
	// Status is the delivery state of a message this hub sent: pending, sent,
	// delivered, read, or failed, with Error saying why when it failed.
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// IsEmpty reports whether a message carries nothing to show, which is what makes
// an attachment message with no caption valid and a truly empty send invalid.
func (m Message) IsEmpty() bool {
	return strings.TrimSpace(m.Text) == "" && len(m.Media) == 0
}

// Challenge is a login step the user must act on: a QR payload, a code, a link,
// or a password prompt. Bridges emit it; the hub relays it verbatim.
type Challenge struct {
	// Kind is "qr", "code", "link", "password", or "verify".
	Kind string `json:"kind"`
	// Payload is the bridge's own value: the QR string, the code, or a URL.
	Payload string `json:"payload,omitempty"`
	// Hint tells the user where to act, e.g. "WhatsApp → Linked devices".
	Hint string `json:"hint,omitempty"`
	// ExpiresAt is a unix millisecond deadline, zero when the step does not expire.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
}

// BridgeProcess is a running bridge: request/response calls plus a stream of
// events.
type BridgeProcess struct {
	name   string
	stdin  io.WriteCloser
	events chan Event
	done   chan struct{}
	closed atomic.Bool
	nextID int64
	mu     sync.Mutex
	// failure is why the bridge's stream ended, when it did not end cleanly: a read
	// error the caller has to see, rather than an exit that looks normal.
	failure error
	pending map[int64]chan Response
	// kill ends the process when its stream is unusable, so a bridge that is still
	// running cannot be left behind holding resources.
	kill func()
}

// newBridgeProcess wires a bridge's stdio into the request/response protocol. kill
// is called when the stream cannot be read any further, so the process behind it
// does not outlive its own protocol.
func newBridgeProcess(name string, stdin io.WriteCloser, stdout io.Reader, kill func()) *BridgeProcess {
	process := &BridgeProcess{
		name: name, stdin: stdin, events: make(chan Event, 128),
		done: make(chan struct{}), pending: map[int64]chan Response{}, kill: kill,
	}
	go process.read(stdout)
	return process
}

// Failure is why the bridge's stream ended when it did not end cleanly, and nil
// when the process simply exited.
func (p *BridgeProcess) Failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}

// setFailure records the first reason the stream ended, which is the one that
// explains the others.
func (p *BridgeProcess) setFailure(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	if p.failure == nil {
		p.failure = err
	}
	p.mu.Unlock()
}

func (p *BridgeProcess) read(stdout io.Reader) {
	defer close(p.done)
	defer close(p.events)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// A response carries "result", an event carries "data"; one envelope
		// shape reads both without guessing which arrived.
		var envelope struct {
			ID     *int64          `json:"id"`
			Error  string          `json:"error"`
			Event  string          `json:"event"`
			Result json.RawMessage `json:"result"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}
		if envelope.ID != nil {
			p.mu.Lock()
			waiter, ok := p.pending[*envelope.ID]
			delete(p.pending, *envelope.ID)
			p.mu.Unlock()
			if ok {
				waiter <- Response{ID: *envelope.ID, Error: envelope.Error, Result: envelope.Result}
			}
			continue
		}
		if envelope.Event != "" {
			select {
			case p.events <- Event{Event: envelope.Event, Data: envelope.Data}:
			default:
				// A slow consumer must never block the bridge's stdout.
			}
		}
	}
	// A stream that ends because of a read error, or because a line was longer than
	// the framing allows, is not a bridge that exited: the caller has to be told,
	// and the process behind the stream has to go.
	if err := scanner.Err(); err != nil {
		p.setFailure(fmt.Errorf("hub: read from bridge %s: %w", p.name, err))
		if p.kill != nil {
			p.kill()
		}
	}
}

// Events is the bridge's event stream. It closes when the process ends.
func (p *BridgeProcess) Events() <-chan Event { return p.events }

// Done closes when the bridge process ends.
func (p *BridgeProcess) Done() <-chan struct{} { return p.done }

// Call sends one request and waits for its response, or for ctx to end.
func (p *BridgeProcess) Call(ctx context.Context, method string, params map[string]any, result any) error {
	p.mu.Lock()
	if p.closed.Load() {
		p.mu.Unlock()
		return fmt.Errorf("hub: bridge %s is not running", p.name)
	}
	p.nextID++
	id := p.nextID
	waiter := make(chan Response, 1)
	p.pending[id] = waiter
	p.mu.Unlock()
	// Every return path drops the waiter: an abandoned entry would leak until the
	// process exited, and a timed-out call is the common case.
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()

	encoded, err := json.Marshal(Request{ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	if _, err := p.stdin.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("hub: write to bridge %s: %w", p.name, err)
	}

	select {
	case response := <-waiter:
		if response.Error != "" {
			return fmt.Errorf("%s: %s", p.name, response.Error)
		}
		if result == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		if failure := p.Failure(); failure != nil {
			return failure
		}
		return fmt.Errorf("hub: bridge %s exited", p.name)
	}
}

// Close ends the bridge's stdin and waits for the process to finish. The wait is
// bounded: a bridge that ignores stdin EOF must not hang its caller.
func (p *BridgeProcess) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	_ = p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
	}
	return nil
}

// ServeBridge runs the bridge side of the protocol: it reads requests from stdin,
// answers them with handle, and gives the handler an emitter so it can push
// events at any time. Bridge binaries stay tiny because this does the framing.
func ServeBridge(stdin io.Reader, stdout io.Writer, handle func(ctx context.Context, method string, params map[string]any, emit func(Event)) (any, error)) error {
	if handle == nil {
		return errors.New("hub: a bridge needs a request handler")
	}
	writer := bufio.NewWriter(stdout)
	encoder := json.NewEncoder(writer)
	var writeMu sync.Mutex
	emit := func(event Event) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = encoder.Encode(event)
		_ = writer.Flush()
	}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request Request
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			continue
		}
		result, err := handle(context.Background(), request.Method, request.Params, emit)
		response := Response{ID: request.ID}
		writeMu.Lock()
		if err != nil {
			response.Error = err.Error()
		} else if result != nil {
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				response.Error = encodeErr.Error()
			} else {
				response.Result = encoded
			}
		}
		err = encoder.Encode(response)
		if err == nil {
			err = writer.Flush()
		}
		writeMu.Unlock()
		if err != nil {
			return err
		}
	}
	return scanner.Err()
}
