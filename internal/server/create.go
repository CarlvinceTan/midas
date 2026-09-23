package server

// Agents in a server environment do not delegate: there is no task tool that runs
// a subagent inside the caller's own run. An agent that needs help either asks an
// existing agent over the bus (ask_agent, tell_agent) or creates one
// (create_agent), which is what makes the team visible to the operator, persisted
// across restarts, and reachable by everyone.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// maxAgents bounds an environment. Agents can create agents, so without a cap a
// single confused run could fill the machine with them.
const maxAgents = 32

var agentAddressPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// CreateAgent adds an agent to the running environment and persists it, so the
// next turn can reach it and a restart brings it back. It is the server's
// alternative to delegation: an agent grows the team instead of spawning work
// inside its own run.
func (e *Environment) CreateAgent(ctx context.Context, entry AgentConfig) (AgentStatus, error) {
	entry.Address = strings.ToLower(strings.TrimSpace(entry.Address))
	entry.Role = strings.TrimSpace(entry.Role)
	entry.Model = strings.TrimSpace(entry.Model)
	if err := validateAgentAddress(entry.Address); err != nil {
		return AgentStatus{}, err
	}
	if _, exists := e.Registry.Get(entry.Address); exists {
		return AgentStatus{}, fmt.Errorf("server: an agent is already at %q", entry.Address)
	}
	if statuses := e.Registry.Statuses(); len(statuses) >= maxAgents {
		return AgentStatus{}, fmt.Errorf("server: this environment already runs %d agents, its limit", len(statuses))
	}
	// The brain is built before anything is written, so an agent that cannot run
	// never reaches the configuration file.
	if _, err := e.brainFor(entry, e.options, e.getenv); err != nil {
		return AgentStatus{}, err
	}
	if err := e.updateConfig(func(config *Config) {
		config.Agents = append(config.Agents, entry)
	}); err != nil {
		// The agent is not registered without a saved configuration: a restart would
		// otherwise forget it while this process kept answering for it.
		return AgentStatus{}, err
	}
	status, err := e.applyAgent(entry)
	if err != nil {
		return AgentStatus{}, err
	}
	e.publish(Event{Kind: EventAgent, Action: "created", Detail: entry.Address})
	return status, nil
}

// validateAgentAddress rejects anything that is not usable as an address, a file
// name, and a path segment in one.
func validateAgentAddress(address string) error {
	if strings.TrimSpace(address) == "" {
		return errors.New("server: an agent needs an address")
	}
	if !agentAddressPattern.MatchString(address) {
		return fmt.Errorf("server: %q is not a valid agent address; use lower-case letters, digits, dots, dashes, or underscores", address)
	}
	return nil
}

// AgentCreator creates an agent in a running environment. A brain is given one so
// the agent it runs for can grow the team.
type AgentCreator interface {
	CreateAgent(ctx context.Context, entry AgentConfig) (AgentStatus, error)
}
