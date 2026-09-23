package server

import (
	"sync"
	"time"
)

// Event kinds on the change feed. A client subscribes once and renders the
// environment from these rather than polling five endpoints.
const (
	EventMessage = "message"
	EventTeam    = "team"
	EventGroup   = "group"
	EventHost    = "host"
	EventAgent   = "agent"
	EventVault   = "vault"
	EventMCP     = "mcp"
)

// Event is one change. Exactly one of the payload fields is set, according to
// Kind, so a client can switch on it without guessing.
type Event struct {
	Kind    string       `json:"kind"`
	Action  string       `json:"action"`
	At      int64        `json:"at"`
	Message *Envelope    `json:"message,omitempty"`
	Team    *Team        `json:"team,omitempty"`
	Group   *Group       `json:"group,omitempty"`
	Host    *Host        `json:"host,omitempty"`
	Agent   *AgentStatus `json:"agent,omitempty"`
	Detail  string       `json:"detail,omitempty"`
}

// Feed fans changes out to subscribers. Each subscriber has a bounded queue: a
// slow client misses events rather than stalling the environment, which is the
// same rule the bus uses for a slow agent.
type Feed struct {
	mu          sync.Mutex
	subscribers map[int]chan Event
	next        int
	history     []Event
	historyCap  int
	now         func() time.Time
}

// NewFeed creates an empty feed, remembering a bounded tail so a client that
// connects mid-flight can catch up.
func NewFeed() *Feed {
	return &Feed{subscribers: map[int]chan Event{}, historyCap: 256, now: time.Now}
}

// Subscribe returns a channel of changes and a function that stops it.
func (f *Feed) Subscribe() (<-chan Event, func()) {
	channel := make(chan Event, 128)
	f.mu.Lock()
	f.next++
	id := f.next
	f.subscribers[id] = channel
	f.mu.Unlock()
	return channel, func() {
		f.mu.Lock()
		if existing, ok := f.subscribers[id]; ok {
			delete(f.subscribers, id)
			close(existing)
		}
		f.mu.Unlock()
	}
}

// Publish records a change and wakes every subscriber.
func (f *Feed) Publish(event Event) {
	// The sends happen under the lock because unsubscribe and Close close
	// subscriber channels while holding it; sending after a snapshot would race
	// with those closes. Every send is non-blocking, so the lock is not held for
	// more than a channel operation.
	f.mu.Lock()
	defer f.mu.Unlock()
	if event.At == 0 {
		event.At = f.now().UnixMilli()
	}
	f.history = append(f.history, event)
	if len(f.history) > f.historyCap {
		f.history = f.history[len(f.history)-f.historyCap:]
	}
	for _, channel := range f.subscribers {
		select {
		case channel <- event:
		default:
		}
	}
}

// Since returns the changes after a timestamp, which is how a reconnecting client
// catches up without a full reload.
func (f *Feed) Since(at int64) []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	if at <= 0 {
		return nil
	}
	events := []Event{}
	for _, event := range f.history {
		if event.At > at {
			events = append(events, event)
		}
	}
	return events
}

// Close stops every subscriber.
func (f *Feed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, channel := range f.subscribers {
		delete(f.subscribers, id)
		close(channel)
	}
}
