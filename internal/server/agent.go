package server

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
)

// Brain answers one message. A real agent uses the Midas loop with the shared
// tool pool; tests use a scripted one, which is why this is an interface rather
// than a concrete agent.
type Brain interface {
	Respond(ctx context.Context, request Request) (string, error)
}

// Request is what a brain is given.
type Request struct {
	// Agent is the address the message arrived at.
	Agent string
	// From is who sent it: another agent, or "user" for the user link.
	From string
	// Group is the conversation it belongs to, when it has one.
	Group string
	Text  string
	Kind  MessageKind
	// Tools are the shared tool definitions this agent may use.
	Tools []agent.Tool
	// Ask sends a message to another address and waits for the answer, which is
	// how one agent delegates to another.
	Ask func(ctx context.Context, to, text string) (string, error)
	// Tell sends without waiting.
	Tell func(to, text string) error
}

// AgentStatus is one agent's state, for the registry the user link reads.
type AgentStatus struct {
	Address string `json:"address"`
	Role    string `json:"role,omitempty"`
	Busy    bool   `json:"busy"`
	// Queued is how much work is waiting, which is how a caller sees backpressure.
	Queued int `json:"queued"`
	// Handled counts the messages this agent has answered since it started.
	Handled int64 `json:"handled"`
}

// Agent is a long-lived worker parked on its inbox. Between messages it holds a
// goroutine and nothing else: no timer, no poll, no model call.
type Agent struct {
	address string
	role    string
	broker  *Broker
	brain   Brain

	mu      sync.Mutex
	busy    bool
	handled int64
	cancel  context.CancelFunc
	// stopping remembers that Stop was called, so a stop that beats the loop's
	// registration of its cancel is not lost.
	stopping bool
	done     chan struct{}
}

// NewAgent registers an address and returns the agent, which starts working when
// Run is called.
func NewAgent(broker *Broker, address, role string, brain Brain) (*Agent, error) {
	if err := broker.Register(address); err != nil {
		return nil, err
	}
	return &Agent{address: address, role: role, broker: broker, brain: brain, done: make(chan struct{})}, nil
}

// Address is where this agent receives.
func (a *Agent) Address() string { return a.address }

// Run parks on the inbox until the context ends. It is meant to be started once.
func (a *Agent) Run(ctx context.Context) {
	inner, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	// A stop that arrived before the loop registered its cancel is honoured here:
	// without this, stopping an agent immediately after starting it is lost and the
	// loop runs until the process context ends.
	stopping := a.stopping
	a.mu.Unlock()
	if stopping {
		cancel()
	}
	defer close(a.done)
	for {
		select {
		case <-inner.Done():
			return
		default:
		}
		delivery, err := a.broker.take(inner, a.address)
		if err != nil {
			return
		}
		a.handle(inner, delivery)
	}
}

// Wait blocks until the agent's loop has exited, which is what a shutdown or a
// test needs instead of assuming the goroutine finished.
func (a *Agent) Wait() { <-a.done }

// SetBrain replaces the brain this agent uses for its next message. A turn that is
// already running keeps the brain it started with, so a reload changes the model
// between turns rather than under a half-finished answer.
func (a *Agent) SetBrain(brain Brain) {
	a.mu.Lock()
	a.brain = brain
	a.mu.Unlock()
}

// CurrentBrain is the brain a message would run with, which is what a test or a
// reload check reads.
func (a *Agent) CurrentBrain() Brain {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.brain
}

// SetRole changes the label the registry reports for this agent.
func (a *Agent) SetRole(role string) {
	a.mu.Lock()
	a.role = role
	a.mu.Unlock()
}

// Stop ends the agent's loop. It may be called before the loop starts, which is
// what a shutdown racing a start does.
func (a *Agent) Stop() {
	a.mu.Lock()
	a.stopping = true
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Status reports the agent's state.
func (a *Agent) Status() AgentStatus {
	a.mu.Lock()
	busy, handled := a.busy, a.handled
	a.mu.Unlock()
	queued := 0
	a.broker.mu.Lock()
	if box, ok := a.broker.boxByID[a.address]; ok {
		box.mu.Lock()
		queued = len(box.queue)
		box.mu.Unlock()
	}
	a.broker.mu.Unlock()
	return AgentStatus{Address: a.address, Role: a.role, Busy: busy, Queued: queued, Handled: handled}
}

// handle runs one message through the brain and answers an ask.
func (a *Agent) handle(ctx context.Context, delivery Delivery) {
	a.mu.Lock()
	a.busy = true
	brain := a.brain
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.handled++
		a.mu.Unlock()
	}()

	request := Request{
		Agent: a.address, From: delivery.From, Group: delivery.Group,
		Text: delivery.Text, Kind: delivery.Kind,
		Ask: func(callCtx context.Context, to, text string) (string, error) {
			return a.broker.Ask(callCtx, a.address, strings.TrimSpace(to), delivery.Group, text)
		},
		Tell: func(to, text string) error {
			_, err := a.broker.Send(a.address, strings.TrimSpace(to), delivery.Group, text)
			return err
		},
	}
	answer, err := brain.Respond(ctx, request)
	if err != nil {
		answer = "error: " + err.Error()
	}
	if delivery.Kind == KindAsk {
		_ = delivery.Reply(answer)
		return
	}
	// A tell gets its answer as a normal message back to the sender, so the user
	// link sees the whole exchange rather than only the half that was asked for.
	if strings.TrimSpace(delivery.From) != "" && strings.TrimSpace(answer) != "" && delivery.From != a.address {
		_, _ = a.broker.Send(a.address, delivery.From, delivery.Group, answer)
	}
}

// Registry is the set of agents in the environment.
type Registry struct {
	mu     sync.Mutex
	agents map[string]*Agent
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{agents: map[string]*Agent{}} }

// Add registers an agent.
func (r *Registry) Add(agent *Agent) {
	r.mu.Lock()
	r.agents[agent.Address()] = agent
	r.mu.Unlock()
}

// Get returns an agent by address.
func (r *Registry) Get(address string) (*Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	found, ok := r.agents[strings.TrimSpace(address)]
	return found, ok
}

// Statuses lists every agent, ordered by address.
func (r *Registry) Statuses() []AgentStatus {
	r.mu.Lock()
	agents := make([]*Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	r.mu.Unlock()
	statuses := make([]AgentStatus, 0, len(agents))
	for _, agent := range agents {
		statuses = append(statuses, agent.Status())
	}
	slices.SortStableFunc(statuses, func(a, b AgentStatus) int { return cmp.Compare(a.Address, b.Address) })
	return statuses
}

// Remove forgets an agent. Its inbox stays on disk, so re-adding the address picks
// up whatever was still queued for it.
func (r *Registry) Remove(address string) bool {
	address = strings.TrimSpace(address)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[address]; !ok {
		return false
	}
	delete(r.agents, address)
	return true
}

// StopAll stops every agent.
func (r *Registry) StopAll() {
	r.mu.Lock()
	agents := make([]*Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	r.mu.Unlock()
	for _, agent := range agents {
		agent.Stop()
	}
}

// WaitForIdle blocks until no agent is busy, which is what tests and shutdown
// sequences need instead of sleeping.
func (r *Registry) WaitForIdle(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		busy := false
		for _, status := range r.Statuses() {
			if status.Busy || status.Queued > 0 {
				busy = true
			}
		}
		if !busy {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	busy := []string{}
	for _, status := range r.Statuses() {
		if status.Busy || status.Queued > 0 {
			busy = append(busy, fmt.Sprintf("%s (busy=%v queued=%d)", status.Address, status.Busy, status.Queued))
		}
	}
	return fmt.Errorf("server: still busy after %s: %s", timeout, strings.Join(busy, ", "))
}
