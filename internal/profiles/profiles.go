// Package profiles defines the agent profiles Midas can run and how a
// profile's prompt, tools, and model defaults are resolved.
package profiles

import (
	"fmt"
	"slices"
	"strings"
)

// Profile is one agent: a built-in or one the user defined in settings.json.
type Profile struct {
	Name         string
	Mode         string
	Description  string
	SystemPrompt string
	ReadOnly     bool
	Reserved     bool
	// Model and Reasoning are the agent's own defaults from configuration. They
	// apply when the user has not chosen a model for it in /agents, and a pin
	// chosen there still wins.
	Model     string
	Reasoning string
	// Tools is the tool surface: full, read-only, or none.
	Tools string
	// Parents are the agents this one inherits its model from when it has none.
	Parents []string
	// MaxDepth caps how many levels of subagents may run beneath this agent.
	MaxDepth int
}

var builtInProfiles = map[string]Profile{
	"main": {
		Name: "main", Mode: "primary", Description: "Default interactive coding agent for new sessions.",
		SystemPrompt: "You are the main Midas coding agent. Work directly in the user's current directory. Understand the request, inspect the code and project instructions, make the smallest correct change, preserve unrelated work, run relevant checks, and report the outcome concisely. Use delegation only for bounded reconnaissance or difficult design decisions. Write temporary or generated scratch files (probes, captures, one-off scripts) under the system temp directory ($TMPDIR, usually /tmp), never in the working tree, and clean them up when done.",
	},
	"advisor": {
		Name: "advisor", Mode: "subagent", Description: "Advises on architecture, implementation tradeoffs, risks, and sequencing without changing files.", ReadOnly: true,
		SystemPrompt: "You are a pragmatic senior engineering advisor. Inspect relevant code and constraints, separate facts from assumptions, recommend a preferred option with alternatives and tradeoffs, and do not modify files or claim unrun checks.",
	},
	"explore": {
		Name: "explore", Mode: "subagent", Description: "Fast read-only codebase reconnaissance.", ReadOnly: true,
		SystemPrompt: "You are a fast read-only reconnaissance agent. Search and read the codebase, return paths, symbols, and concise findings, and never modify files or run state-changing commands.",
	},
	"title": {
		Name: "title", Mode: "utility", Description: "Names the session after the overarching goal of the work.", Reserved: true,
		SystemPrompt: "You name Midas sessions. In the requested word budget, name the overarching goal or outcome of the work as a title, describing the overall objective and never the current activity, tool, command, or agent. Reply with only the title, no quotes.",
	},
	"summary": {
		Name: "summary", Mode: "utility", Description: "Writes the short live summary of what the session is doing.", Reserved: true,
		SystemPrompt: "You summarize Midas sessions. In the requested word budget, summarize what is happening right now: the state of the conversation and the aim of the current step, never the tool or agent name. Reply with only the summary phrase, no quotes.",
	},
}

// Profiles lists every agent Midas knows: built-ins first, then the ones defined
// in settings.json, each alphabetically.
func Profiles() []Profile {
	names := make([]string, 0, len(builtInProfiles)+len(customProfiles))
	for name := range builtInProfiles {
		names = append(names, name)
	}
	slices.Sort(names)
	result := make([]Profile, 0, len(names)+len(customProfiles))
	for _, name := range names {
		result = append(result, builtInProfiles[name])
	}
	custom := make([]string, 0, len(customProfiles))
	for name := range customProfiles {
		custom = append(custom, name)
	}
	slices.Sort(custom)
	for _, name := range custom {
		result = append(result, customProfiles[name])
	}
	return result
}

// ResolveProfile resolves one agent by name, built-in or user-defined.
func ResolveProfile(name string) (Profile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "main"
	}
	profile, ok := lookup(name)
	if !ok {
		return Profile{}, fmt.Errorf("unknown agent %q", name)
	}
	return profile, nil
}

// ConfiguredModel is the model an agent's own definition names, if any.
func ConfiguredModel(name string) string {
	profile, ok := lookup(name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(profile.Model)
}

// ConfiguredReasoning is the reasoning level an agent's own definition names, if
// any.
func ConfiguredReasoning(name string) string {
	profile, ok := lookup(name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(profile.Reasoning)
}

// ToolsMode reports an agent's tool surface, defaulting to full for built-ins
// that do not name one.
func ToolsMode(name string) string {
	profile, ok := lookup(name)
	if !ok {
		return ToolsFull
	}
	if strings.TrimSpace(profile.Tools) != "" {
		return profile.Tools
	}
	if profile.ReadOnly {
		// The built-in reconnaissance agents are read-only without naming a tool
		// surface.
		return ToolsReadOnly
	}
	return ToolsFull
}
