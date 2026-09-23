package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"
)

// bridge is one installed bridge and its lazily started process.
type bridge struct {
	name    string
	config  BridgeConfig
	mu      sync.Mutex
	process *BridgeProcess
	started time.Time
	used    time.Time

	// lastError records why the last start failed, for hub_bridges list.
	lastError string
}

// Supervisor starts bridges on demand and stops them when they go idle. Nothing
// is started at boot: the first call that needs a bridge starts it.
type Supervisor struct {
	mu      sync.Mutex
	bridges map[string]*bridge
	onEvent func(bridge string, event Event)

	calls   int64
	started int64
	stopped int64
}

// newSupervisor builds a supervisor over the configured bridges.
func newSupervisor(configs map[string]BridgeConfig, onEvent func(string, Event)) *Supervisor {
	supervisor := &Supervisor{bridges: map[string]*bridge{}, onEvent: onEvent}
	for name, config := range configs {
		supervisor.bridges[name] = &bridge{name: name, config: config}
	}
	return supervisor
}

// Names lists installed bridges in a stable order.
func (s *Supervisor) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.bridges))
	for name := range s.bridges {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Stop ends one bridge's process. The bridge stays installed and starts again on
// the next call.
func (s *Supervisor) Stop(name string) error {
	s.mu.Lock()
	entry, ok := s.bridges[name]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("hub: no bridge named %q is installed", name)
	}
	return entry.stop()
}

// add registers a newly installed bridge.
func (s *Supervisor) add(name string, config BridgeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bridges[name] = &bridge{name: name, config: config}
}

// remove stops and forgets a bridge.
func (s *Supervisor) remove(name string) {
	s.mu.Lock()
	entry := s.bridges[name]
	delete(s.bridges, name)
	s.mu.Unlock()
	if entry != nil {
		_ = entry.stop()
	}
}

// call runs one request against a bridge, starting it if it is not running.
func (s *Supervisor) call(ctx context.Context, name, method string, params map[string]any, result any) error {
	_, process, err := s.acquire(ctx, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	// The process reference is taken while the bridge's lock is held, so a
	// concurrent stop cannot nil it out between the lookup and the call.
	return process.Call(ctx, method, params, result)
}

// acquire returns a running bridge and its process, starting it if needed.
func (s *Supervisor) acquire(ctx context.Context, name string) (*bridge, *BridgeProcess, error) {
	s.mu.Lock()
	entry, ok := s.bridges[name]
	s.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("hub: no bridge named %q is installed", name)
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.process != nil {
		entry.used = time.Now()
		return entry, entry.process, nil
	}
	if len(entry.config.Command) == 0 {
		return nil, nil, fmt.Errorf("hub: bridge %q has no command configured", name)
	}
	process, err := startBridge(ctx, entry.name, entry.config)
	if err != nil {
		entry.lastError = err.Error()
		return nil, nil, err
	}
	entry.process = process
	entry.started = time.Now()
	entry.used = time.Now()
	entry.lastError = ""
	s.mu.Lock()
	s.started++
	s.mu.Unlock()
	go s.watch(entry)
	return entry, process, nil
}

// watch forwards a bridge's events until its process ends.
func (s *Supervisor) watch(entry *bridge) {
	// The process is read under the bridge's lock: a stop can land between the
	// goroutine starting and this line, and an unsynchronised read here is a race
	// the race detector reports and a shutdown can turn into a nil dereference.
	entry.mu.Lock()
	process := entry.process
	entry.mu.Unlock()
	if process == nil {
		return
	}
	for {
		select {
		case event, ok := <-process.Events():
			if !ok {
				// The event stream ends when the process does, which can happen before
				// Done is observed: forgetting the process here is what keeps the
				// status from reporting a bridge that has already exited as running.
				entry.clearProcess(process)
				return
			}
			if s.onEvent != nil {
				s.onEvent(entry.name, event)
			}
		case <-process.Done():
			entry.clearProcess(process)
			return
		}
	}
}

// clearProcess forgets a process if it is still the one this bridge is running, and
// keeps the reason it ended so hub_bridges list says what happened.
func (b *bridge) clearProcess(process *BridgeProcess) {
	b.mu.Lock()
	if b.process == process {
		b.process = nil
		if failure := process.Failure(); failure != nil {
			b.lastError = failure.Error()
		}
	}
	b.mu.Unlock()
}

// Reap stops bridges that have been idle for longer than ttl.
func (s *Supervisor) Reap(ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	cutoff := time.Now().Add(-ttl)
	s.mu.Lock()
	entries := make([]*bridge, 0, len(s.bridges))
	for _, entry := range s.bridges {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	stopped := 0
	for _, entry := range entries {
		entry.mu.Lock()
		idle := entry.process != nil && entry.used.Before(cutoff)
		entry.mu.Unlock()
		if !idle {
			continue
		}
		if err := entry.stop(); err == nil {
			stopped++
			s.mu.Lock()
			s.stopped++
			s.mu.Unlock()
		}
	}
	return stopped
}

// stop ends a bridge's process.
func (b *bridge) stop() error {
	b.mu.Lock()
	process := b.process
	b.process = nil
	b.mu.Unlock()
	if process == nil {
		return nil
	}
	return process.Close()
}

// StopAll ends every running bridge.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	entries := make([]*bridge, 0, len(s.bridges))
	for _, entry := range s.bridges {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	for _, entry := range entries {
		_ = entry.stop()
	}
}

// Status reports one bridge's state for hub_bridges list.
func (s *Supervisor) Status(name string) BridgeStatus {
	s.mu.Lock()
	entry := s.bridges[name]
	s.mu.Unlock()
	if entry == nil {
		return BridgeStatus{Name: name, State: "missing"}
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	status := BridgeStatus{Name: name, Accounts: append([]string(nil), entry.config.Accounts...), Error: entry.lastError}
	switch {
	case entry.process != nil:
		status.State = "running"
		status.Since = entry.started.UTC().Format(time.RFC3339)
	case entry.lastError != "":
		status.State = "failed"
	default:
		status.State = "stopped"
	}
	return status
}

// BridgeStatus is one bridge's supervisor state.
type BridgeStatus struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Accounts []string `json:"accounts,omitempty"`
	Error    string   `json:"error,omitempty"`
	Since    string   `json:"since,omitempty"`
}

// startBridge spawns a bridge process and wires its stdio.
func startBridge(ctx context.Context, name string, config BridgeConfig) (*BridgeProcess, error) {
	command := exec.Command(config.Command[0], config.Command[1:]...)
	command.Env = os.Environ()
	for key, value := range config.Env {
		command.Env = append(command.Env, key+"="+value)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("hub: start bridge %s: %w", name, err)
	}
	process := newBridgeProcess(name, stdin, stdout, func() {
		// The stream is unreadable, so the bridge cannot answer again: ending it is
		// better than leaving a process whose protocol nobody is reading.
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	go func() { _ = command.Wait() }()
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := process.Call(ready, MethodHello, nil, nil); err != nil {
		// Kill first: a bridge that ignores stdin EOF would otherwise hold Close
		// until its timeout, and a failed hello must not wedge the supervisor.
		_ = command.Process.Kill()
		_ = process.Close()
		return nil, fmt.Errorf("hub: bridge %s did not answer hello: %w", name, err)
	}
	return process, nil
}

// errNoBridges is returned when a call needs a bridge and none is installed.
var errNoBridges = errors.New("no bridges are installed; use hub_bridges install")
