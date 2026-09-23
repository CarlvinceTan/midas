package server

// Reload applies the environment's configuration file to the agents that are
// running. It is what makes an edit to server.json take effect without a restart:
// new agents start, a changed model or prompt applies to the agent's next message,
// and a removed agent stops. An unreadable or invalid file changes nothing and is
// reported, so a bad edit cannot take a live environment down.

import (
	"context"
	"fmt"
	"strings"
)

// ReloadReport says what one reload did, and what it refused to do.
type ReloadReport struct {
	Added   []string `json:"added,omitempty"`
	Updated []string `json:"updated,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Failed lists the agents whose new configuration could not be built. Those
	// agents keep running exactly as they were.
	Failed []ReloadFailure `json:"failed,omitempty"`
}

// ReloadFailure is one agent a reload could not apply.
type ReloadFailure struct {
	Address string `json:"address"`
	Error   string `json:"error"`
}

// Empty reports whether the reload had nothing to do.
func (r ReloadReport) Empty() bool {
	return len(r.Added)+len(r.Updated)+len(r.Removed)+len(r.Failed) == 0
}

// Summary is a one-line description for a log or a notice.
func (r ReloadReport) Summary() string {
	if r.Empty() {
		return "no changes"
	}
	parts := make([]string, 0, 4)
	if len(r.Added) > 0 {
		parts = append(parts, "added "+strings.Join(r.Added, ", "))
	}
	if len(r.Updated) > 0 {
		parts = append(parts, "updated "+strings.Join(r.Updated, ", "))
	}
	if len(r.Removed) > 0 {
		parts = append(parts, "removed "+strings.Join(r.Removed, ", "))
	}
	if len(r.Failed) > 0 {
		failed := make([]string, 0, len(r.Failed))
		for _, failure := range r.Failed {
			failed = append(failed, failure.Address+" ("+failure.Error+")")
		}
		parts = append(parts, "failed "+strings.Join(failed, ", "))
	}
	return strings.Join(parts, "; ")
}

// Reload re-reads the configuration and applies it to the running agents.
func (e *Environment) Reload(ctx context.Context) (ReloadReport, error) {
	config, err := Load(e.ConfigDir)
	if err != nil {
		return ReloadReport{}, err
	}
	if err := validateAgentEntries(config.Agents); err != nil {
		return ReloadReport{}, err
	}
	report := ReloadReport{}
	// The file is the operator's intent; the running agents are what it is applied
	// to. What was last applied is remembered per address so an unchanged entry is
	// left alone rather than having its brain rebuilt for nothing.
	e.mu.Lock()
	e.Config = config
	if config.VaultMode != "" && config.VaultMode != e.VaultMode {
		e.VaultMode = config.VaultMode
		if config.VaultMode == VaultUserPermission {
			// Switching back to user-permission locks the vault again, exactly as the
			// mode endpoint does.
			e.vaultKey = ""
		}
	}
	applied := make(map[string]AgentConfig, len(e.appliedAgents))
	for address, entry := range e.appliedAgents {
		applied[address] = entry
	}
	e.mu.Unlock()

	wanted := make(map[string]AgentConfig, len(config.Agents))
	for _, entry := range config.Agents {
		wanted[entry.Address] = entry
	}
	for _, entry := range config.Agents {
		_, live := e.Registry.Get(entry.Address)
		if live {
			if previous, known := applied[entry.Address]; known && previous == entry {
				continue
			}
		}
		if _, err := e.applyAgent(entry); err != nil {
			report.Failed = append(report.Failed, ReloadFailure{Address: entry.Address, Error: err.Error()})
			continue
		}
		if live {
			report.Updated = append(report.Updated, entry.Address)
		} else {
			report.Added = append(report.Added, entry.Address)
		}
	}
	for address := range applied {
		if _, keep := wanted[address]; keep {
			continue
		}
		if live, ok := e.Registry.Get(address); ok {
			// Removal stops the agent: an operator asking for it gone wants it gone,
			// so the turn in flight is cancelled rather than finished.
			live.Stop()
			e.Registry.Remove(address)
		}
		e.forgetAppliedAgent(address)
		report.Removed = append(report.Removed, address)
	}
	if !report.Empty() {
		e.publish(Event{Kind: EventAgent, Action: "reload", Detail: report.Summary()})
	}
	return report, nil
}

// applyAgent makes the running environment match one configured agent: it starts an
// agent that is not running, and swaps the brain of one whose model, prompt, or role
// changed. The swap takes effect on that agent's next message.
func (e *Environment) applyAgent(entry AgentConfig) (AgentStatus, error) {
	brain, err := e.brainFor(entry, e.options, e.getenv)
	if err != nil {
		return AgentStatus{}, err
	}
	if creator, ok := brain.(interface{ SetCreator(AgentCreator) }); ok {
		creator.SetCreator(e)
	}
	if live, ok := e.Registry.Get(entry.Address); ok {
		live.SetBrain(brain)
		live.SetRole(entry.Role)
		e.rememberAppliedAgent(entry)
		return live.Status(), nil
	}
	agent, err := NewAgent(e.Broker, entry.Address, entry.Role, brain)
	if err != nil {
		return AgentStatus{}, err
	}
	e.Registry.Add(agent)
	go agent.Run(e.ctx)
	e.rememberAppliedAgent(entry)
	return agent.Status(), nil
}

// rememberAppliedAgent records the configuration a running agent was built from.
func (e *Environment) rememberAppliedAgent(entry AgentConfig) {
	e.mu.Lock()
	if e.appliedAgents == nil {
		e.appliedAgents = map[string]AgentConfig{}
	}
	e.appliedAgents[entry.Address] = entry
	e.mu.Unlock()
}

func (e *Environment) forgetAppliedAgent(address string) {
	e.mu.Lock()
	delete(e.appliedAgents, address)
	e.mu.Unlock()
}

// validateAgentEntries keeps a file that cannot be applied from being applied
// halfway: every entry has to be usable before any of them is.
func validateAgentEntries(entries []AgentConfig) error {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		address := strings.ToLower(strings.TrimSpace(entry.Address))
		if err := validateAgentAddress(address); err != nil {
			return err
		}
		if _, duplicate := seen[address]; duplicate {
			return fmt.Errorf("server: %q appears twice in the agent list", address)
		}
		seen[address] = struct{}{}
	}
	return nil
}

var _ = context.Background
