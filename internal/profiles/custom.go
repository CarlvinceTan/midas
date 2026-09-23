package profiles

// This file owns the agents a user defines in settings.json. A definition is the
// same shape an opencode user already knows: description, mode, model, reasoning,
// prompt, tools, and the agents it inherits its model from. Midas adds maxDepth,
// the cap on how many levels of subagents may run beneath an agent.

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Modes an agent can have. Primary agents are user-facing entry points,
// subagents are invoked by other agents, and utility agents are the helpers
// Midas runs on its own behalf.
const (
	ModePrimary  = "primary"
	ModeSubagent = "subagent"
	ModeUtility  = "utility"

	// Tool surfaces an agent can be given.
	ToolsFull     = "full"
	ToolsReadOnly = "read-only"
	ToolsNone     = "none"

	// DefaultMaxDepth is how many levels of subagents may run beneath an agent
	// that does not set maxDepth: the agent itself plus one level of children.
	DefaultMaxDepth = 2
)

// Definition is one user-defined agent as it appears in settings.json:
//
//	"agents": {
//	  "reviewer": {
//	    "description": "Reviews diffs for correctness.",
//	    "mode": "subagent",
//	    "model": "openai/gpt-5.6-sol",
//	    "reasoning": "high",
//	    "prompt": "You review diffs...",
//	    "tools": "read-only",
//	    "parents": ["main"],
//	    "maxDepth": 2
//	  }
//	}
type Definition struct {
	Description string   `json:"description"`
	Mode        string   `json:"mode"`
	Model       string   `json:"model"`
	Reasoning   string   `json:"reasoning"`
	Prompt      string   `json:"prompt"`
	Tools       string   `json:"tools"`
	Parents     []string `json:"parents"`
	MaxDepth    *int     `json:"maxDepth"`
}

// customProfiles holds the user-defined agents, keyed by name. It is package
// state because agents are resolved from several layers (chat, the TUI, and the
// command) that do not share a configuration object.
var customProfiles = map[string]Profile{}

// LoadCustom replaces the user-defined agents. Every definition is validated
// here, so a mistake in settings.json fails at startup with the agent's name
// instead of surfacing later as a missing agent or an empty prompt.
func LoadCustom(entries map[string]json.RawMessage) error {
	loaded := make(map[string]Profile, len(entries))
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("agents: an agent needs a name")
		}
		if _, builtIn := builtInProfiles[name]; builtIn {
			return fmt.Errorf("agents: %q is a built-in agent; rename yours to add it", name)
		}
		if !isAgentName(name) {
			return fmt.Errorf("agents: %q is not a valid agent name; use letters, digits, and dashes", name)
		}
		var definition Definition
		if err := json.Unmarshal(entries[name], &definition); err != nil {
			return fmt.Errorf("agents: %s: %w", name, err)
		}
		profile, err := definition.profile(name)
		if err != nil {
			return err
		}
		loaded[name] = profile
	}
	for _, profile := range loaded {
		for _, parent := range profile.Parents {
			if _, ok := loaded[parent]; ok {
				continue
			}
			if _, ok := builtInProfiles[parent]; !ok {
				return fmt.Errorf("agents: %s inherits from %q, which is not an agent", profile.Name, parent)
			}
		}
	}
	customProfiles = loaded
	return nil
}

// profile turns one definition into the agent Midas runs, filling in what the
// definition leaves out.
func (d Definition) profile(name string) (Profile, error) {
	mode := strings.ToLower(strings.TrimSpace(d.Mode))
	if mode == "" {
		mode = ModeSubagent
	}
	switch mode {
	case ModePrimary, ModeSubagent, ModeUtility:
	default:
		return Profile{}, fmt.Errorf("agents: %s: mode must be primary, subagent, or utility, not %q", name, d.Mode)
	}
	tools := strings.ToLower(strings.TrimSpace(d.Tools))
	switch tools {
	case "":
		tools = ToolsFull
	case ToolsFull, ToolsReadOnly, ToolsNone:
	default:
		return Profile{}, fmt.Errorf("agents: %s: tools must be full, read-only, or none, not %q", name, d.Tools)
	}
	reasoning := strings.ToLower(strings.TrimSpace(d.Reasoning))
	if reasoning != "" && !validReasoningLevel(reasoning) {
		return Profile{}, fmt.Errorf("agents: %s: unknown reasoning level %q", name, d.Reasoning)
	}
	maxDepth := DefaultMaxDepth
	if d.MaxDepth != nil {
		if *d.MaxDepth < 0 {
			return Profile{}, fmt.Errorf("agents: %s: maxDepth must not be negative", name)
		}
		maxDepth = *d.MaxDepth
	}
	parents := make([]string, 0, len(d.Parents))
	for _, parent := range d.Parents {
		parent = strings.TrimSpace(parent)
		if parent == "" || slices.Contains(parents, parent) {
			continue
		}
		parents = append(parents, parent)
	}
	if len(parents) == 0 && mode != ModePrimary {
		// A subagent that names no parent inherits from main, like the built-ins.
		parents = []string{"main"}
	}
	description := strings.TrimSpace(d.Description)
	if description == "" {
		description = "User-defined " + mode + " agent."
	}
	prompt := strings.TrimSpace(d.Prompt)
	if prompt == "" {
		prompt = defaultCustomPrompt(name, description)
	}
	return Profile{
		Name: name, Mode: mode, Description: description, SystemPrompt: prompt,
		ReadOnly: tools == ToolsReadOnly, Tools: tools,
		Model: strings.TrimSpace(d.Model), Reasoning: reasoning,
		Parents: parents, MaxDepth: maxDepth,
	}, nil
}

func defaultCustomPrompt(name, description string) string {
	return "You are the " + name + " agent. " + description +
		" Work directly in the user's current directory, inspect the code and project instructions, " +
		"preserve unrelated work, run the checks that apply, and report the outcome concisely."
}

// isAgentName keeps names usable as a key, a flag value, and a settings entry.
func isAgentName(name string) bool {
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9',
			char == '-', char == '_':
		default:
			return false
		}
	}
	return true
}

func validReasoningLevel(value string) bool {
	switch value {
	case "off", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// lookup resolves one agent: a built-in first, then a user-defined one.
func lookup(name string) (Profile, bool) {
	name = strings.TrimSpace(name)
	if profile, ok := builtInProfiles[name]; ok {
		return profile, true
	}
	profile, ok := customProfiles[name]
	return profile, ok
}

// MaxDepth is the cap on how many levels of subagents may run beneath an agent:
// its own maxDepth when it sets one, and DefaultMaxDepth otherwise.
func MaxDepth(name string) int {
	profile, ok := lookup(name)
	if !ok || profile.MaxDepth <= 0 {
		return DefaultMaxDepth
	}
	return profile.MaxDepth
}

// CanEnter reports whether a session may start as this agent. Only primary
// agents are entry points: a subagent runs when another agent invokes it, and a
// utility agent runs only inside Midas.
func CanEnter(name string) bool {
	profile, ok := lookup(name)
	return ok && profile.Mode == ModePrimary && !profile.Reserved
}

// CanDelegate reports whether an agent may spawn subagents. Utility agents never
// delegate, and neither do agents that cannot act (read-only or tool-less), so
// reconnaissance never grows into a tree of its own.
func CanDelegate(name string) bool {
	profile, ok := lookup(name)
	if !ok || profile.Mode == ModeUtility || profile.Reserved {
		return false
	}
	return ToolsMode(name) == ToolsFull
}

// CanDelegateTo reports whether an agent may be spawned as a subagent.
func CanDelegateTo(name string) bool {
	profile, ok := lookup(name)
	return ok && profile.Mode == ModeSubagent
}
