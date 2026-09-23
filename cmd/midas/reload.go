package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/internal/chat"
	"github.com/CarlvinceTan/midas/internal/instructions"
	"github.com/CarlvinceTan/midas/pkg/mcp"
	"github.com/CarlvinceTan/midas/internal/profiles"
	midassettings "github.com/CarlvinceTan/midas/internal/settings"
	"github.com/CarlvinceTan/midas/internal/skills"
	"github.com/CarlvinceTan/midas/internal/tui"
)

// sessionReload is what a reload produced, so the caller can update the view and
// close what it replaced.
type sessionReload struct {
	Settings     midassettings.Values
	Instructions []instructions.Entry
	Skills       []skills.Entry
	// Configs are the MCP servers read from disk. Manager is set only when they
	// differ from what the session was running with, and it is the replacement the
	// caller installs and closes the previous one for.
	Configs      map[string]mcp.Config
	Manager      *mcp.Manager
	MCPChanged   bool
	ChangedParts []string
}

// reloadSession re-reads everything the session takes from disk: settings, the
// user-defined agents, the agent model configuration, the project's instructions
// and skills, and the MCP servers. The session is updated in place, so the next
// turn runs with what is on disk now.
//
// A reload never half-applies: reading or validating a part that fails leaves the
// session exactly as it was and reports the error, because a session that silently
// lost its instructions or its tools is worse than a stale one.
func reloadSession(
	ctx context.Context,
	configDir, root, profileName string,
	settingsStore *midassettings.Store,
	agentModels *tui.AgentModelConfig,
	session *chat.Chat,
) (sessionReload, error) {
	if settingsStore == nil {
		return sessionReload{}, fmt.Errorf("this session has no settings to reload")
	}
	// Agents first: everything after this resolves against them, so an invalid
	// definition must not take effect halfway through a reload.
	if err := profiles.LoadCustom(settingsStore.Object(midassettings.Agents)); err != nil {
		return sessionReload{}, err
	}
	if agentModels != nil {
		agentModels.Reload()
	}
	result := sessionReload{Settings: settingsStore.Load()}

	instructionEntries, err := instructions.Load(root, configDir)
	if err != nil {
		return sessionReload{}, err
	}
	skillEntries := skills.Discover(root, configDir)
	profile, err := profiles.ResolveProfile(profileName)
	if err != nil {
		return sessionReload{}, err
	}
	mode, additional, err := profileRuntime(root, profile)
	if err != nil {
		return sessionReload{}, err
	}
	if err := session.SetProfile(profile.Name, composeSystemPrompt(profile.SystemPrompt, instructionEntries, skillEntries), mode, additional); err != nil {
		return sessionReload{}, err
	}
	// Subagents inherit this text, so a reload has to hand it over as well.
	session.SetSystemSuffix(composeSystemPrompt("", instructionEntries, skillEntries))
	result.Instructions, result.Skills = instructionEntries, skillEntries
	result.ChangedParts = append(result.ChangedParts, "instructions", "skills")

	configs, err := mcp.LoadConfigs(configDir, root)
	if err != nil {
		return sessionReload{}, err
	}
	result.Configs = configs
	if !mcpConfigsEqual(configs, session.MCPConfigs()) {
		// Servers are replaced only when the file names different ones: reconnecting
		// an unchanged set would drop tools mid-conversation for no reason.
		replacement, err := mcp.New(configs)
		if err != nil {
			return sessionReload{}, err
		}
		connectContext, cancel := context.WithTimeout(ctx, 15*time.Second)
		_ = replacement.ConnectAll(connectContext)
		cancel()
		if err := session.SetMCP(replacement); err != nil {
			_ = replacement.Close()
			return sessionReload{}, err
		}
		result.Manager, result.MCPChanged = replacement, true
		result.ChangedParts = append(result.ChangedParts, "MCP servers")
	}
	return result, nil
}

// mcpConfigsEqual compares two server sets by everything that decides a connection.
func mcpConfigsEqual(left, right map[string]mcp.Config) bool {
	if len(left) != len(right) {
		return false
	}
	for name, config := range left {
		other, ok := right[name]
		if !ok {
			return false
		}
		if !slices.Equal(config.Command, other.Command) || config.URL != other.URL ||
			config.Disabled != other.Disabled || !stringMapsEqual(config.Env, other.Env) ||
			!stringMapsEqual(config.Headers, other.Headers) {
			return false
		}
	}
	return true
}

func stringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// reloadNotice is what the user is told after a reload: the parts that could have
// changed, so "nothing happened" is never ambiguous.
func reloadNotice(result sessionReload) string {
	parts := []string{"settings", "agents"}
	parts = append(parts, result.ChangedParts...)
	if len(parts) == 0 {
		return "Reloaded configuration."
	}
	return "Reloaded " + strings.Join(uniqueStrings(parts), ", ") + "."
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
