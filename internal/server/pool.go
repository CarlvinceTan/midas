package server

import (
	"context"
	"sync"
	"time"

	mcpconfig "github.com/CarlvinceTan/midas/internal/mcp"
	"github.com/CarlvinceTan/midas/pkg/agent"
)

// Pool is the shared MCP layer. One connection per server, not one per agent:
// MCP servers are stateful and expensive to start, and every agent in the
// environment is served by the same set of tools.
type Pool struct {
	mu      sync.Mutex
	configs map[string]mcpconfig.Config
	manager *mcpconfig.Manager
	tools   []agent.Tool
	started bool
	// connecting is closed when an in-flight connect finishes, so concurrent
	// callers wait for one attempt instead of starting their own.
	connecting chan struct{}
	// lastUsed is when an agent last asked for tools, which is what makes stopping
	// them when idle possible without watching each connection.
	lastUsed time.Time
	// stopped counts how many times the pool has stopped idle servers, for the
	// API to report.
	stopped int
}

// NewPool creates a pool over the MCP configuration of a working directory.
func NewPool(configDir, root string) (*Pool, error) {
	configs, err := mcpconfig.LoadConfigs(configDir, root)
	if err != nil {
		return nil, err
	}
	manager, err := mcpconfig.New(configs)
	if err != nil {
		return nil, err
	}
	return &Pool{configs: configs, manager: manager}, nil
}

// Tools connects lazily on first use and returns the shared tool set. A tool set
// that is already connected is returned as is, however many agents ask for it.
func (p *Pool) Tools(ctx context.Context) ([]agent.Tool, error) {
	p.mu.Lock()
	if p.started {
		p.lastUsed = time.Now()
		tools := p.tools
		p.mu.Unlock()
		// A server that failed to connect is reported through Statuses rather than
		// by failing every caller: one bad server must not take the others down.
		return tools, nil
	}
	if p.connecting != nil {
		// Another caller is already connecting. Waiting for it avoids a second
		// round of connections to the same servers.
		done := p.connecting
		p.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		p.mu.Lock()
		tools := p.tools
		p.mu.Unlock()
		return tools, nil
	}
	done := make(chan struct{})
	p.connecting = done
	manager := p.manager
	p.mu.Unlock()

	// Connecting happens without the lock: MCP servers can take a while to start,
	// and Statuses, Names, IdleFor, and Close must not stall behind them.
	var tools []agent.Tool
	if manager != nil {
		// A failure to connect one server should not remove the others' tools: the
		// statuses show which server is down, and the tools that did connect are
		// still shared.
		_ = manager.ConnectAll(ctx)
		tools = manager.Tools()
	}

	p.mu.Lock()
	if p.manager == manager {
		p.tools, p.started = tools, true
	}
	p.connecting = nil
	p.lastUsed = time.Now()
	p.mu.Unlock()
	close(done)
	return tools, nil
}

// Reap stops the pool's servers when nothing has asked for a tool for longer than
// idle, and reports how many were disconnected. The pool stays usable: the next
// agent to ask reconnects them, which is the same lazy start as the first time.
func (p *Pool) Reap(idle time.Duration) int {
	if idle <= 0 {
		return 0
	}
	p.mu.Lock()
	if !p.started || p.manager == nil || time.Since(p.lastUsed) < idle {
		p.mu.Unlock()
		return 0
	}
	manager := p.manager
	connected := 0
	for _, status := range manager.Statuses() {
		if status.State == mcpconfig.StateConnected {
			connected++
		}
	}
	if connected == 0 {
		// Nothing is up, so there is nothing to stop; the pool still returns to its
		// unstarted state so a later ask reconnects.
		p.resetLocked()
		p.mu.Unlock()
		return 0
	}
	p.resetLocked()
	p.stopped++
	p.mu.Unlock()
	// Closing a connection can block on a server that stopped responding, so it
	// happens outside the lock.
	_ = manager.Close()
	return connected
}

// resetLocked returns the pool to its pre-connect state, ready to reconnect.
func (p *Pool) resetLocked() {
	p.started = false
	p.tools = nil
	manager, err := mcpconfig.New(p.configs)
	if err != nil {
		// Without a fresh manager the pool must not keep using the closed one.
		p.manager = nil
		return
	}
	p.manager = manager
}

// IdleFor reports how long the pool has gone unused, for the API and the reaper.
func (p *Pool) IdleFor() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastUsed.IsZero() {
		return 0
	}
	return time.Since(p.lastUsed)
}

// StoppedCount reports how many times idle servers have been stopped.
func (p *Pool) StoppedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// Names lists the MCP servers this pool knows about.
func (p *Pool) Names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.manager == nil {
		return nil
	}
	names := []string{}
	for _, status := range p.manager.Statuses() {
		names = append(names, status.Name)
	}
	return names
}

// Statuses reports each MCP server's state, for the API's view of the pool.
func (p *Pool) Statuses() []mcpconfig.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.manager == nil {
		return nil
	}
	return p.manager.Statuses()
}

// Close ends every connection.
func (p *Pool) Close() {
	p.mu.Lock()
	manager := p.manager
	p.manager = nil
	p.mu.Unlock()
	if manager != nil {
		_ = manager.Close()
	}
}
