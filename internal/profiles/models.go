package profiles

import "strings"

// builtInParents lists, for every agent that inherits a model, the agents it
// can be invoked by. Entry agents are absent: they remember the model they last
// used rather than inheriting one.
var builtInParents = map[string][]string{
	"advisor": {"main"},
	"explore": {"main"},
	// The helper agents run for whichever entry agent is working, so they inherit
	// the active session model unless the user pins one in /agents.
	"title":   {"main"},
	"summary": {"main"},
}

// IsEntryAgent reports whether an agent is a user-facing entry point. Entry
// agents default to the model and reasoning level they last used.
func IsEntryAgent(name string) bool {
	profile, ok := lookup(name)
	return ok && profile.Mode == ModePrimary && !profile.Reserved
}

// IsUtilityAgent reports whether an agent is an internal helper Midas runs on
// its own behalf. Utility agents are never entry points, never delegation
// targets, and are listed apart from the subagents in the startup summary.
func IsUtilityAgent(name string) bool {
	profile, ok := lookup(name)
	return ok && profile.Mode == ModeUtility
}

// ParentAgents returns the agents whose model this agent inherits when it has no
// model of its own. A user-defined agent names its parents; the built-in
// subagents inherit from main; entry agents inherit from nobody.
func ParentAgents(name string) []string {
	name = strings.TrimSpace(name)
	if profile, ok := customProfiles[name]; ok {
		return append([]string(nil), profile.Parents...)
	}
	return append([]string(nil), builtInParents[name]...)
}

// EntryAgents returns the user-facing entry points, in profile order.
func EntryAgents() []string {
	result := make([]string, 0, len(builtInProfiles))
	for _, profile := range Profiles() {
		if IsEntryAgent(profile.Name) {
			result = append(result, profile.Name)
		}
	}
	return result
}

// DefaultModelLabel is how an agent's model reads when the user has not chosen
// one in /agents: entry agents remember the model they last used, and every other
// agent inherits the model of the agent that invokes it.
func DefaultModelLabel(name string) string {
	if IsEntryAgent(name) {
		return "Default (Last used)"
	}
	if len(ParentAgents(name)) == 0 {
		return "Default"
	}
	return "Default (Inherit)"
}
