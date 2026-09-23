package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CarlvinceTan/midas/internal/chat"
	"github.com/CarlvinceTan/midas/pkg/mcp"
	"github.com/CarlvinceTan/midas/internal/profiles"
	midassettings "github.com/CarlvinceTan/midas/internal/settings"
	"github.com/CarlvinceTan/midas/internal/tui"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// reloadTestSession builds the smallest session a reload can act on: a settings
// store, the agent model configuration, and a chat with no provider.
func reloadTestSession(t *testing.T) (string, *midassettings.Store, *tui.AgentModelConfig, *chat.Chat) {
	t.Helper()
	configDir := t.TempDir()
	root := t.TempDir()
	store := midassettings.New(configDir)
	loaded := tui.LoadAgentModelConfig(store)
	agentModels := &loaded
	session, err := chat.New(chat.Options{
		Root: root, SessionID: "ses_reload", Agent: "main",
		Provider: (&scriptedStreamer{}).provider(), Model: ai.Model{ID: "test", Provider: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return configDir, store, agentModels, session
}

// scriptedStreamer is a provider that answers nothing, which is all a reload test
// needs.
type scriptedStreamer struct{}

func (s *scriptedStreamer) provider() ai.Provider { return s }
func (s *scriptedStreamer) ID() string            { return "test" }
func (s *scriptedStreamer) Models(context.Context) ([]ai.Model, error) {
	return nil, nil
}
func (s *scriptedStreamer) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (*ai.AssistantStream, error) {
	return ai.NewAssistantStream(), nil
}

// writeSettings replaces the settings file, the way an edit outside Midas would.
func writeSettings(t *testing.T, configDir string, values map[string]any) {
	t.Helper()
	data, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReloadPicksUpEditsMadeOutsideMidas is what /reload is for: settings,
// custom agents, agent models, and MCP servers read from disk again without a
// restart.
func TestReloadPicksUpEditsMadeOutsideMidas(t *testing.T) {
	t.Cleanup(func() { _ = profiles.LoadCustom(nil) })
	configDir, store, agentModels, session := reloadTestSession(t)
	writeSettings(t, configDir, map[string]any{
		"padding":     1,
		"agentModels": map[string]string{"main": "openai/gpt-5.6-sol"},
	})

	// Nothing new on disk yet: the reload still reports what it re-read.
	first, err := reloadSession(context.Background(), configDir, session.Root(), "main", store, agentModels, session)
	if err != nil {
		t.Fatal(err)
	}
	if first.MCPChanged {
		t.Fatal("an unchanged MCP configuration was replaced")
	}
	if got := agentModels.Ref("main", ""); got != "openai/gpt-5.6-sol" {
		t.Fatalf("agent model = %q", got)
	}

	// The user edits settings.json and adds a skill and an MCP server on disk.
	writeSettings(t, configDir, map[string]any{
		"padding":     3,
		"agentModels": map[string]string{"main": "anthropic/claude-opus"},
		"agents": map[string]any{
			"reviewer": map[string]any{"mode": "subagent", "description": "Reviews diffs."},
		},
		"mcpServers": map[string]any{
			"local": map[string]any{"command": []string{"/nonexistent/mcp-server"}},
		},
	})
	if err := os.MkdirAll(filepath.Join(configDir, "skills", "on-disk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "skills", "on-disk", "SKILL.md"),
		[]byte("---\nname: on-disk\ndescription: Loaded from disk.\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := reloadSession(context.Background(), configDir, session.Root(), "main", store, agentModels, session)
	if err != nil {
		t.Fatal(err)
	}
	if result.Settings.Padding != 3 {
		t.Fatalf("reloaded padding = %d", result.Settings.Padding)
	}
	if got := agentModels.Ref("main", ""); got != "anthropic/claude-opus" {
		t.Fatalf("reloaded agent model = %q", got)
	}
	if _, err := profiles.ResolveProfile("reviewer"); err != nil {
		t.Fatalf("a custom agent added on disk was not reloaded: %v", err)
	}
	if !result.MCPChanged || result.Manager == nil {
		t.Fatal("a server added on disk did not replace the MCP set")
	}
	if _, ok := session.MCPConfigs()["local"]; !ok {
		t.Fatalf("the session did not take the new MCP server: %#v", session.MCPConfigs())
	}
	found := false
	for _, entry := range result.Skills {
		if entry.Name == "on-disk" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a skill added on disk was not reloaded: %#v", result.Skills)
	}
}

// TestReloadLeavesTheSessionAloneWhenTheFileIsInvalid: a reload never half-applies,
// so a broken definition changes nothing.
func TestReloadLeavesTheSessionAloneWhenTheFileIsInvalid(t *testing.T) {
	t.Cleanup(func() { _ = profiles.LoadCustom(nil) })
	configDir, store, agentModels, session := reloadTestSession(t)
	writeSettings(t, configDir, map[string]any{
		"padding":     2,
		"agentModels": map[string]string{"main": "openai/gpt-5.6-sol"},
		"agents":      map[string]any{"good": map[string]any{"mode": "subagent"}},
	})
	if _, err := reloadSession(context.Background(), configDir, session.Root(), "main", store, agentModels, session); err != nil {
		t.Fatal(err)
	}
	// Now break the agents section and change the model: neither must take.
	writeSettings(t, configDir, map[string]any{
		"padding":     4,
		"agentModels": map[string]string{"main": "anthropic/claude-opus"},
		"agents":      map[string]any{"bad": map[string]any{"mode": "not-a-mode"}},
	})
	if _, err := reloadSession(context.Background(), configDir, session.Root(), "main", store, agentModels, session); err == nil {
		t.Fatal("an invalid agent definition was accepted")
	}
	if _, err := profiles.ResolveProfile("good"); err != nil {
		t.Fatalf("a valid agent was lost by a failed reload: %v", err)
	}
	if _, err := profiles.ResolveProfile("bad"); err == nil {
		t.Fatal("the invalid agent was registered")
	}
	if got := agentModels.Ref("main", ""); got != "openai/gpt-5.6-sol" {
		t.Fatalf("a failed reload changed the agent model to %q", got)
	}
}

// TestMCPConfigsEqualComparesWhatDecidesAConnection.
func TestMCPConfigsEqualComparesWhatDecidesAConnection(t *testing.T) {
	base := map[string]mcp.Config{"a": {Command: []string{"server", "--flag"}, Env: map[string]string{"K": "v"}}}
	same := map[string]mcp.Config{"a": {Command: []string{"server", "--flag"}, Env: map[string]string{"K": "v"}}}
	if !mcpConfigsEqual(base, same) {
		t.Fatal("identical configurations differed")
	}
	changed := map[string]mcp.Config{"a": {Command: []string{"server", "--other"}, Env: map[string]string{"K": "v"}}}
	if mcpConfigsEqual(base, changed) {
		t.Fatal("a changed command was treated as the same server")
	}
	if mcpConfigsEqual(base, map[string]mcp.Config{"b": base["a"]}) {
		t.Fatal("a renamed server was treated as unchanged")
	}
	if mcpConfigsEqual(base, map[string]mcp.Config{}) {
		t.Fatal("a removed server was treated as unchanged")
	}
}
