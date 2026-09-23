// Package server is the multi-agent environment: one process hosting many
// long-lived Midas agents that share MCP connections and a browser, and talk to
// each other and to the user over one protocol.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// MessageKind separates a request that expects an answer from a note that does
// not, which is the whole vocabulary agents need to coordinate.
type MessageKind string

const (
	KindTell  MessageKind = "tell"
	KindAsk   MessageKind = "ask"
	KindReply MessageKind = "reply"
	// KindUser is a message between the user and an agent. It travels the same
	// bus as everything else, so there is one delivery path, not two.
	KindUser MessageKind = "user"
)

// UserAddress is where the user link receives. Every broker registers it, because
// the user is always one of the parties in the environment.
const UserAddress = "user"

// Envelope is one message on the bus.
type Envelope struct {
	ID      string      `json:"id"`
	Kind    MessageKind `json:"kind"`
	From    string      `json:"from"`
	To      string      `json:"to"`
	Group   string      `json:"group,omitempty"`
	Text    string      `json:"text"`
	ReplyTo string      `json:"replyTo,omitempty"`
	At      int64       `json:"at"`
}

// Delivery is a message handed to a recipient, with the channel to answer on.
type Delivery struct {
	Envelope
	// reply accepts an answer for an ask, and is nil for a tell.
	reply func(string) error
}

// Reply answers an ask. It is safe to call once; a second call is ignored.
func (d Delivery) Reply(text string) error {
	if d.reply == nil {
		return errors.New("server: this message does not expect a reply")
	}
	return d.reply(text)
}

// inbox is one address's queue. It is persistent because a long-lived agent
// should not lose work when the process restarts, and bounded because an agent
// that cannot keep up must not consume the machine.
type inbox struct {
	mu      sync.Mutex
	address string
	path    string
	queue   []Envelope
	// waiters are wake-up channels: the queue carries the message, these only
	// say that something arrived.
	waiters []chan struct{}
	closed  bool
	pending map[string]chan string
}

const inboxCapacity = 256

// Broker delivers messages between addresses. Delivery is event-driven: a parked
// agent is woken by a channel send, so an idle environment costs nothing.
type Broker struct {
	mu      sync.Mutex
	boxByID map[string]*inbox
	dir     string
	seq     int64
	now     func() time.Time
	// listeners observe every message, which is how the user link and the change
	// feed see traffic without polling.
	listeners    map[string]func(Envelope)
	nextListener int
}

// NewBroker creates a broker whose inboxes persist under dir.
func NewBroker(dir string) (*Broker, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	broker := &Broker{boxByID: map[string]*inbox{}, dir: dir, now: time.Now, listeners: map[string]func(Envelope){}}
	if err := broker.Register(UserAddress); err != nil {
		return nil, err
	}
	return broker, nil
}

// Register declares an address and loads whatever is still queued for it.
func (b *Broker) Register(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return errors.New("server: an address is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.boxByID[address]; ok {
		return nil
	}
	box := &inbox{address: address, path: filepath.Join(b.dir, address+".jsonl"), pending: map[string]chan string{}}
	queue, err := loadInbox(box.path)
	if err != nil {
		return err
	}
	box.queue = queue
	b.boxByID[address] = box
	return nil
}

// Addresses lists every registered address, in a stable order.
func (b *Broker) Addresses() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	addresses := make([]string, 0, len(b.boxByID))
	for address := range b.boxByID {
		addresses = append(addresses, address)
	}
	slices.Sort(addresses)
	return addresses
}

// Subscribe observes every envelope. The returned function stops observing.
func (b *Broker) Subscribe(listener func(Envelope)) func() {
	b.mu.Lock()
	b.nextListener++
	key := fmt.Sprintf("listener_%d", b.nextListener)
	b.listeners[key] = listener
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.listeners, key)
		b.mu.Unlock()
	}
}

// Send delivers a message without waiting for an answer.
func (b *Broker) Send(from, to, group, text string) (Envelope, error) {
	return b.deliver(KindTell, "", from, to, group, text)
}

// Ask delivers a message and waits for the recipient's answer. The reply slot is
// registered before delivery, because a recipient can answer the moment it
// receives the ask.
func (b *Broker) Ask(ctx context.Context, from, to, group, text string) (string, error) {
	b.mu.Lock()
	asker, known := b.boxByID[from]
	if !known {
		b.mu.Unlock()
		return "", fmt.Errorf("server: no such address %q", from)
	}
	b.seq++
	id := fmt.Sprintf("msg_%d", b.seq)
	answer := make(chan string, 1)
	asker.mu.Lock()
	asker.pending[id] = answer
	asker.mu.Unlock()
	b.mu.Unlock()

	envelope, err := b.deliver(KindAsk, id, from, to, group, text)
	if err != nil {
		b.forgetPending(from, id)
		return "", err
	}
	return b.awaitReply(ctx, envelope, answer)
}

// Broadcast sends to every registered address except the sender.
func (b *Broker) Broadcast(from, group, text string) ([]Envelope, error) {
	delivered := []Envelope{}
	for _, address := range b.Addresses() {
		if address == from {
			continue
		}
		envelope, err := b.Send(from, address, group, text)
		if err != nil {
			return delivered, err
		}
		delivered = append(delivered, envelope)
	}
	return delivered, nil
}

// deliver queues one message and wakes a parked recipient. An empty id is
// generated here, which is what Send needs.
func (b *Broker) deliver(kind MessageKind, id, from, to, group, text string) (Envelope, error) {
	if strings.TrimSpace(to) == "" {
		return Envelope{}, errors.New("server: a recipient is required")
	}
	b.mu.Lock()
	box, ok := b.boxByID[to]
	if !ok {
		b.mu.Unlock()
		return Envelope{}, fmt.Errorf("server: no such address %q", to)
	}
	if id == "" {
		b.seq++
		id = fmt.Sprintf("msg_%d", b.seq)
	}
	envelope := Envelope{
		ID: id, Kind: kind, From: from, To: to,
		Group: group, Text: text, At: b.now().UnixMilli(),
	}
	b.mu.Unlock()

	box.mu.Lock()
	if box.closed {
		box.mu.Unlock()
		return Envelope{}, fmt.Errorf("server: %q is no longer receiving", to)
	}
	if len(box.queue) >= inboxCapacity {
		// A recipient that cannot keep up must not grow the process without bound.
		// The oldest is dropped rather than the newest, so the freshest instruction
		// survives, and the drop is visible in the log rather than silent.
		box.queue = box.queue[1:]
	}
	box.queue = append(box.queue, envelope)
	if err := appendInbox(box.path, envelope); err != nil {
		box.mu.Unlock()
		return Envelope{}, err
	}
	waiters := box.waiters
	box.waiters = nil
	box.mu.Unlock()

	if err := b.recordGroup(envelope); err != nil {
		return Envelope{}, err
	}
	// Waiters are woken, not handed the message: the queue is the single source of
	// truth, so nothing can be delivered twice or dropped between the two.
	for _, waiter := range waiters {
		select {
		case waiter <- struct{}{}:
		default:
		}
	}
	b.announce(envelope)
	return envelope, nil
}

// take returns the next message for an address, parking until one arrives or the
// caller's context ends. The context matters: a long-lived agent that is stopped
// must leave its parking spot, and an environment shutdown must not wait for the
// next message to notice.
func (b *Broker) take(ctx context.Context, address string) (Delivery, error) {
	b.mu.Lock()
	box, ok := b.boxByID[address]
	b.mu.Unlock()
	if !ok {
		return Delivery{}, fmt.Errorf("server: no such address %q", address)
	}
	for {
		box.mu.Lock()
		if len(box.queue) > 0 {
			envelope := box.queue[0]
			box.queue = box.queue[1:]
			remaining := append([]Envelope(nil), box.queue...)
			// The file tracks the queue, so a restart replays only unhandled work.
			// The rewrite happens under the inbox lock: two consumers of the same
			// inbox would otherwise write one temporary file at the same time and
			// could leave a mixture of both behind.
			err := rewriteInbox(box.path, remaining)
			box.mu.Unlock()
			if err != nil {
				return Delivery{}, err
			}
			return b.delivery(envelope), nil
		}
		if box.closed {
			box.mu.Unlock()
			return Delivery{}, fmt.Errorf("server: %q is no longer receiving", address)
		}
		// A one-slot wake-up channel: a send that arrives between the check above
		// and parking here is still seen, because the queue is re-checked after
		// every wake.
		wake := make(chan struct{}, 1)
		box.waiters = append(box.waiters, wake)
		box.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return Delivery{}, ctx.Err()
		}
	}
}

func (box *inbox) snapshot() []Envelope {
	box.mu.Lock()
	defer box.mu.Unlock()
	return append([]Envelope(nil), box.queue...)
}

// delivery finds the reply channel an ask registered when it was sent, so the
// answer lands in the slot the asker is waiting on and nowhere else.
func (b *Broker) delivery(envelope Envelope) Delivery {
	if envelope.Kind != KindAsk {
		return Delivery{Envelope: envelope}
	}
	b.mu.Lock()
	asker, known := b.boxByID[envelope.From]
	var answer chan string
	if known {
		asker.mu.Lock()
		answer = asker.pending[envelope.ID]
		asker.mu.Unlock()
	}
	b.mu.Unlock()
	if answer == nil {
		return Delivery{Envelope: envelope}
	}
	settled := false
	return Delivery{Envelope: envelope, reply: func(text string) error {
		if settled {
			return errors.New("server: this ask was already answered")
		}
		settled = true
		select {
		case answer <- text:
			b.forgetPending(envelope.From, envelope.ID)
			// The answer goes straight back to the asker, and is recorded on the
			// bus as well: a group transcript has to show both halves of the
			// exchange, or the user link would only see questions.
			reply := Envelope{
				ID: b.nextID(), Kind: KindReply, From: envelope.To, To: envelope.From,
				Group: envelope.Group, Text: text, ReplyTo: envelope.ID, At: b.now().UnixMilli(),
			}
			if err := b.recordGroup(reply); err == nil {
				b.announce(reply)
			}
			return nil
		default:
			return errors.New("server: this ask was already answered")
		}
	}}
}

// nextID hands out message ids.
func (b *Broker) nextID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return fmt.Sprintf("msg_%d", b.seq)
}

// forgetPending drops a reply slot, so an unanswered or timed-out ask does not
// leave a channel behind for the life of the process.
func (b *Broker) forgetPending(address, id string) {
	b.mu.Lock()
	asker, known := b.boxByID[address]
	b.mu.Unlock()
	if !known {
		return
	}
	asker.mu.Lock()
	delete(asker.pending, id)
	asker.mu.Unlock()
}

// awaitReply waits for an answer to an ask, and gives up with the caller's
// context rather than hanging when no answer comes.
func (b *Broker) awaitReply(ctx context.Context, envelope Envelope, answer chan string) (string, error) {
	select {
	case text := <-answer:
		return text, nil
	case <-ctx.Done():
		b.forgetPending(envelope.From, envelope.ID)
		return "", ctx.Err()
	}
}

// History returns the messages exchanged in a group, newest last, bounded. Group
// traffic is appended to its own file, which is what lets the user link page back
// without keeping every conversation in memory.
func (b *Broker) History(group string, limit int) ([]Envelope, error) {
	if strings.TrimSpace(group) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	file, err := os.Open(b.groupPath(group))
	if err != nil {
		return nil, nil
	}
	defer file.Close()
	// Only the last limit messages are kept, so a long transcript costs the
	// scanner's buffer rather than memory proportional to the file.
	queue := make([]Envelope, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope Envelope
		if json.Unmarshal([]byte(line), &envelope) != nil {
			continue
		}
		queue = append(queue, envelope)
		if len(queue) > limit {
			queue = queue[len(queue)-limit:]
		}
	}
	// A group file that ends because of a read error, or because a line is longer
	// than the framing allows, would otherwise return a short history that reads as
	// a complete one.
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("broker: read %s: %w", b.groupPath(group), err)
	}
	return queue, nil
}

// groupPath is where one group's traffic lives. A group id becomes a single file
// name: separators of any kind are folded so an id can never name a path outside
// the groups directory.
func (b *Broker) groupPath(group string) string {
	safe := strings.Map(func(char rune) rune {
		if char == '/' || char == '\\' || char == 0 {
			return '_'
		}
		return char
	}, group)
	if safe == "" || safe == "." || safe == ".." {
		safe = "_"
	}
	return filepath.Join(b.dir, "groups", safe+".jsonl")
}

// recordGroup appends a grouped message to its transcript.
func (b *Broker) recordGroup(envelope Envelope) error {
	if strings.TrimSpace(envelope.Group) == "" {
		return nil
	}
	path := b.groupPath(envelope.Group)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return appendInbox(path, envelope)
}

func (b *Broker) announce(envelope Envelope) {
	b.mu.Lock()
	listeners := make([]func(Envelope), 0, len(b.listeners))
	for _, listener := range b.listeners {
		listeners = append(listeners, listener)
	}
	b.mu.Unlock()
	for _, listener := range listeners {
		listener(envelope)
	}
}

// Close stops every address from receiving more work.
func (b *Broker) Close() {
	b.mu.Lock()
	boxes := make([]*inbox, 0, len(b.boxByID))
	for _, box := range b.boxByID {
		boxes = append(boxes, box)
	}
	b.mu.Unlock()
	for _, box := range boxes {
		box.mu.Lock()
		box.closed = true
		for _, waiter := range box.waiters {
			close(waiter)
		}
		box.waiters = nil
		box.mu.Unlock()
	}
}

func loadInbox(path string) ([]Envelope, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	queue := []Envelope{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope Envelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}
		queue = append(queue, envelope)
	}
	return queue, scanner.Err()
}

func appendInbox(path string, envelope Envelope) error {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(encoded, '\n'))
	return err
}

func rewriteInbox(path string, queue []Envelope) error {
	// A unique temporary name means a rewrite can never interleave with another
	// writer of the same inbox, even if the caller's locking is ever relaxed.
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	for _, envelope := range queue {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			file.Close()
			os.Remove(temporary)
			return err
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			file.Close()
			os.Remove(temporary)
			return err
		}
	}
	if err := file.Close(); err != nil {
		os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, path)
}
