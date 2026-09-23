// Package mcp connects configured Model Context Protocol servers and exposes
// their tools through Midas' provider-neutral agent interface.
package mcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Config struct {
	Command  []string          `json:"command,omitempty"`
	URL      string            `json:"url,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

func (c Config) Validate() error {
	hasCommand := len(c.Command) > 0
	hasURL := strings.TrimSpace(c.URL) != ""
	if hasCommand == hasURL {
		return errors.New("mcp: configure exactly one of command or url")
	}
	if hasCommand && strings.TrimSpace(c.Command[0]) == "" {
		return errors.New("mcp: command executable must not be empty")
	}
	return nil
}

type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateFailed       State = "failed"
	StateDisabled     State = "disabled"
)

type Status struct {
	Name  string
	State State
	Error string
	Tools int
}

type TransportFactory func(Config) (sdkmcp.Transport, error)

type server struct {
	config  Config
	state   State
	err     string
	session *sdkmcp.ClientSession
	tools   []*sdkmcp.Tool
}

type Manager struct {
	mu      sync.RWMutex
	servers map[string]*server
	client  *sdkmcp.Client
	factory TransportFactory
	// configs is what this manager was built from, so a reload can tell whether
	// the file now names different servers.
	configs map[string]Config
}

func New(configs map[string]Config) (*Manager, error) {
	return newManager(configs, defaultTransport)
}

// Configs returns a copy of the configuration this manager was built from.
func (m *Manager) Configs() map[string]Config {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	copied := make(map[string]Config, len(m.configs))
	for name, config := range m.configs {
		copied[name] = config
	}
	return copied
}

func newManager(configs map[string]Config, factory TransportFactory) (*Manager, error) {
	servers := make(map[string]*server, len(configs))
	for name, config := range configs {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("mcp: server name must not be empty")
		}
		if !config.Disabled {
			if err := config.Validate(); err != nil {
				return nil, fmt.Errorf("mcp server %s: %w", name, err)
			}
		}
		state := StateDisconnected
		if config.Disabled {
			state = StateDisabled
		}
		servers[name] = &server{config: config, state: state}
	}
	stored := make(map[string]Config, len(configs))
	for name, config := range configs {
		stored[strings.TrimSpace(name)] = config
	}
	return &Manager{
		servers: servers, configs: stored,
		client:  sdkmcp.NewClient(&sdkmcp.Implementation{Name: "midas", Version: "dev"}, nil),
		factory: factory,
	}, nil
}

func defaultTransport(config Config) (sdkmcp.Transport, error) {
	if len(config.Command) > 0 {
		command := exec.Command(config.Command[0], config.Command[1:]...)
		command.Env = append([]string(nil), os.Environ()...)
		keys := make([]string, 0, len(config.Env))
		for key := range config.Env {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			command.Env = append(command.Env, key+"="+config.Env[key])
		}
		return &sdkmcp.CommandTransport{Command: command}, nil
	}
	client := http.DefaultClient
	if len(config.Headers) > 0 {
		base := http.DefaultTransport
		client = &http.Client{Transport: headerTransport{base: base, headers: cloneStrings(config.Headers)}}
	}
	return &sdkmcp.StreamableClientTransport{Endpoint: config.URL, HTTPClient: client}, nil
}

func (m *Manager) Connect(ctx context.Context, name string) error {
	m.mu.Lock()
	entry, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp: unknown server %q", name)
	}
	if entry.state == StateDisabled {
		m.mu.Unlock()
		return fmt.Errorf("mcp: server %q is disabled", name)
	}
	if entry.state == StateConnected {
		m.mu.Unlock()
		return nil
	}
	if entry.state == StateConnecting {
		m.mu.Unlock()
		return fmt.Errorf("mcp: server %q is already connecting", name)
	}
	entry.state, entry.err = StateConnecting, ""
	config := entry.config
	m.mu.Unlock()

	transport, err := m.factory(config)
	if err == nil {
		var session *sdkmcp.ClientSession
		session, err = m.client.Connect(ctx, transport, nil)
		if err == nil {
			var listed []*sdkmcp.Tool
			for tool, listErr := range session.Tools(ctx, nil) {
				if listErr != nil {
					err = listErr
					break
				}
				listed = append(listed, tool)
			}
			if err == nil {
				slices.SortStableFunc(listed, func(a, b *sdkmcp.Tool) int { return cmp.Compare(a.Name, b.Name) })
				m.mu.Lock()
				entry.session, entry.tools, entry.state, entry.err = session, listed, StateConnected, ""
				m.mu.Unlock()
				return nil
			}
			_ = session.Close()
		}
	}
	m.mu.Lock()
	entry.state, entry.err, entry.session, entry.tools = StateFailed, err.Error(), nil, nil
	m.mu.Unlock()
	return fmt.Errorf("mcp: connect %s: %w", name, err)
}

func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	entry, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp: unknown server %q", name)
	}
	session := entry.session
	entry.session, entry.tools, entry.err = nil, nil, ""
	if entry.config.Disabled {
		entry.state = StateDisabled
	} else {
		entry.state = StateDisconnected
	}
	m.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	return nil
}

func (m *Manager) Close() error {
	m.mu.RLock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	m.mu.RUnlock()
	slices.Sort(names)
	var result error
	for _, name := range names {
		result = errors.Join(result, m.Disconnect(name))
	}
	return result
}

func (m *Manager) ConnectAll(ctx context.Context) error {
	m.mu.RLock()
	names := make([]string, 0, len(m.servers))
	for name, entry := range m.servers {
		if entry.state != StateDisabled {
			names = append(names, name)
		}
	}
	m.mu.RUnlock()
	slices.Sort(names)
	var result error
	for _, name := range names {
		if err := m.Connect(ctx, name); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	statuses := make([]Status, 0, len(m.servers))
	for name, entry := range m.servers {
		statuses = append(statuses, Status{Name: name, State: entry.state, Error: entry.err, Tools: len(entry.tools)})
	}
	slices.SortFunc(statuses, func(a, b Status) int { return cmp.Compare(a.Name, b.Name) })
	return statuses
}

func (m *Manager) snapshot() map[string]server {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]server, len(m.servers))
	for name, entry := range m.servers {
		copy := *entry
		copy.tools = append([]*sdkmcp.Tool(nil), entry.tools...)
		result[name] = copy
	}
	return result
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for key, value := range t.headers {
		clone.Header.Set(key, value)
	}
	return t.base.RoundTrip(clone)
}

func cloneStrings(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
