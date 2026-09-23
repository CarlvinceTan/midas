package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarlvinceTan/midas/pkg/mcpconfig"
)

// OverrideEnvForTest is the variable Midas sets for the servers it launches.
const OverrideEnvForTest = mcpconfig.OverrideEnv

func TestLoadConfigsUsesOnlyMidasScopesAndProjectOverrides(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "user")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, ".midas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, ConfigFile), []byte(`{"mcpServers":{"shared":{"url":"https://user.invalid"},"user":{"command":["user-server"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".midas", ConfigFile), []byte(`{"mcpServers":{"shared":{"url":"https://project.invalid"},"project":{"command":["project-server"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadConfigs(configDir, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 3 {
		t.Fatalf("configs = %#v", configs)
	}
	if configs["shared"].URL != "https://project.invalid" {
		t.Fatalf("project did not override user: %#v", configs["shared"])
	}
	if configs["user"].Command[0] != "user-server" || configs["project"].Command[0] != "project-server" {
		t.Fatalf("scope merge = %#v", configs)
	}
}

func TestLoadConfigsReadsAgentsScopeAfterMidasScope(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "user")
	project := filepath.Join(root, "project")
	for _, dir := range []string{configDir, filepath.Join(project, ".midas"), filepath.Join(project, ".agents")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(configDir, ConfigFile), []byte(`{"mcpServers":{"user":{"command":["user-server"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".midas", ConfigFile), []byte(`{"mcpServers":{"shared":{"url":"https://midas.invalid"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".agents", ConfigFile), []byte(`{"mcpServers":{"shared":{"url":"https://agents.invalid"},"agents":{"command":["agents-server"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadConfigs(configDir, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 3 {
		t.Fatalf("configs = %#v", configs)
	}
	if configs["shared"].URL != "https://agents.invalid" {
		t.Fatalf(".agents did not override .midas: %#v", configs["shared"])
	}
	if configs["agents"].Command[0] != "agents-server" || configs["user"].Command[0] != "user-server" {
		t.Fatalf("scope merge = %#v", configs)
	}
}

func TestLoadConfigsRejectsNonEnvelopeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(`{"server":{"url":"https://example.invalid"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfigs(dir, ""); err == nil {
		t.Fatal("expected schema error")
	}
}

func TestLoadConfigsAcceptsCommonCommandArgsAndServersAlias(t *testing.T) {
	dir := t.TempDir()
	data := `{"servers":{"filesystem":{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/tmp"],"env":{"MODE":"test"}}}}`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	configs, err := LoadConfigs(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	config := configs["filesystem"]
	want := []string{"npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp"}
	if len(config.Command) != len(want) {
		t.Fatalf("command = %#v", config.Command)
	}
	for index := range want {
		if config.Command[index] != want[index] {
			t.Fatalf("command = %#v", config.Command)
		}
	}
	if config.Env["MODE"] != "test" {
		t.Fatalf("env = %#v", config.Env)
	}
}

// TestLoadConfigsReadsSettingsFileAndAnnouncesIt: Midas' own configuration holds
// the launch list, and the servers it launches are told where that file is so
// their own settings land beside it.
func TestLoadConfigsReadsSettingsFileAndAnnouncesIt(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "user")
	project := filepath.Join(root, "project")
	for _, dir := range []string{configDir, filepath.Join(project, ".midas")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	settings := `{
	  "currency": "AUD",
	  "agentModels": {"main": "openai/gpt-5.6-sol"},
	  "mcpServers": {"shared": {"url": "https://user.invalid"}, "user": {"command": ["user-server"]}}
	}`
	if err := os.WriteFile(filepath.Join(configDir, SettingsFile), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".midas", SettingsFile), []byte(`{"mcpServers":{"shared":{"url":"https://project.invalid"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A legacy file naming a server the settings files do not mention is kept, and
	// one they do mention is not allowed to override them.
	if err := os.WriteFile(filepath.Join(configDir, ConfigFile), []byte(`{"mcpServers":{"legacy":{"command":["legacy-server"]},"shared":{"url":"https://legacy.invalid"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadConfigs(configDir, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 3 {
		t.Fatalf("configs = %#v", configs)
	}
	if configs["shared"].URL != "https://project.invalid" {
		t.Fatalf("the settings file did not win: %#v", configs["shared"])
	}
	if configs["legacy"].Command[0] != "legacy-server" || configs["user"].Command[0] != "user-server" {
		t.Fatalf("merge = %#v", configs)
	}
	// Every stdio server is told where the agent's settings are, so the servers
	// Midas ships read their section from it; an explicit value is respected.
	announced := filepath.Join(configDir, SettingsFile)
	if got := configs["user"].Env[OverrideEnvForTest]; got != announced {
		t.Fatalf("announced path = %q, want %q", got, announced)
	}
	if got := configs["legacy"].Env[OverrideEnvForTest]; got != announced {
		t.Fatalf("legacy server announced path = %q", got)
	}
	if got := configs["shared"].Env[OverrideEnvForTest]; got != "" {
		t.Fatalf("a URL server was given a settings path: %q", got)
	}
	if err := os.WriteFile(filepath.Join(configDir, SettingsFile), []byte(`{"mcpServers":{"own":{"command":["own"],"env":{"`+OverrideEnvForTest+`":"/custom"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configs, err = LoadConfigs(configDir, project)
	if err != nil {
		t.Fatal(err)
	}
	if got := configs["own"].Env[OverrideEnvForTest]; got != "/custom" {
		t.Fatalf("an explicit settings path was replaced: %q", got)
	}
}
