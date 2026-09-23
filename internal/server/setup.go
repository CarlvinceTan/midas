package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigFile is the environment's own file, beside the agent's config.
const ConfigFile = "server.json"

// AgentConfig describes one agent in the environment.
type AgentConfig struct {
	// Address is where the agent receives, and how others reach it.
	Address string `json:"address"`
	// Role is a label for the registry, e.g. "orchestrator" or "worker".
	Role string `json:"role,omitempty"`
	// Model is provider/model. Empty uses the environment default.
	Model string `json:"model,omitempty"`
	// Prompt is the system prompt. Empty uses the environment default.
	Prompt string `json:"prompt,omitempty"`
}

// BrowserConfig describes the browser every agent shares. The binary is expected
// to be in the image; the profile lives on persistent storage so logins survive a
// restart.
type BrowserConfig struct {
	// Binary is the browser executable. Empty means the environment has none and
	// agents get no browser tool.
	Binary string `json:"binary,omitempty"`
	// ProfileDir is where its user data lives.
	ProfileDir string `json:"profileDir,omitempty"`
	// Port is the debugging port to run it with. Zero lets the browser choose,
	// which is the safer default on a server: the endpoint is discovered either
	// way, so there is nothing to collide with.
	Port int `json:"port,omitempty"`
}

// HostConfig is a device the environment may reach.
type HostConfig struct {
	Name string `json:"name"`
	// Local marks the machine the environment runs on.
	Local bool `json:"local,omitempty"`
	// Default marks the host used when a task does not name one.
	Default bool `json:"default,omitempty"`
}

// Config is the whole environment.
type Config struct {
	// Token authorises the API and the WebSocket surface.
	Token string `json:"token,omitempty"`
	// Model is the default provider/model for agents that do not name one.
	Model string `json:"model,omitempty"`
	// VaultMode is "autonomous" (unlocked by default, no user present) or
	// "user-permission" (locked until the master password is supplied).
	VaultMode string `json:"vaultMode,omitempty"`
	// Agents are the long-lived workers this environment hosts.
	Agents []AgentConfig `json:"agents,omitempty"`
	// Browser is the shared browser.
	Browser BrowserConfig `json:"browser,omitempty"`
	// Hosts are the devices the environment knows about.
	Hosts []HostConfig `json:"hosts,omitempty"`
}

// Vault modes.
const (
	// VaultAutonomous keeps the vault unlocked: no user is present to approve
	// anything, which is the right default on a server.
	VaultAutonomous = "autonomous"
	// VaultUserPermission keeps it locked until the master password arrives
	// through the API, for environments where a person is accountable.
	VaultUserPermission = "user-permission"
)

// Path returns the environment's config file inside a config directory.
func Path(configDir string) string {
	if strings.TrimSpace(configDir) == "" {
		configDir = "."
	}
	return filepath.Join(configDir, ConfigFile)
}

// Load reads the configuration, returning the default when no file exists yet.
func Load(configDir string) (Config, error) {
	path := Path(configDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("server: read %s: %w", path, err)
	}
	config := Config{}
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("server: parse %s: %w", path, err)
	}
	return withDefaults(config), nil
}

// Save writes the configuration with owner-only permissions, since it holds a
// token.
func Save(configDir string, config Config) error {
	path := Path(configDir)
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".server-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// Setup prepares the environment: it writes a config with a generated token when
// there is none, creates the state directory, and prepares the browser profile.
// It is idempotent, so a restart is a no-op rather than a reset.
func Setup(configDir, stateDir string) (Config, error) {
	config, err := Load(configDir)
	if err != nil {
		return Config{}, err
	}
	changed := false
	if strings.TrimSpace(config.Token) == "" {
		random := make([]byte, 24)
		if _, err := rand.Read(random); err != nil {
			return Config{}, err
		}
		config.Token = hex.EncodeToString(random)
		changed = true
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return Config{}, err
	}
	if profile := strings.TrimSpace(config.Browser.ProfileDir); profile != "" {
		// The profile is prepared here rather than by a client: a browser an agent
		// can use out of the box is the whole point.
		if err := os.MkdirAll(profile, 0o700); err != nil {
			return Config{}, fmt.Errorf("server: prepare browser profile: %w", err)
		}
	}
	if _, err := os.Stat(Path(configDir)); os.IsNotExist(err) || changed {
		if err := Save(configDir, config); err != nil {
			return Config{}, err
		}
	}
	return config, nil
}

// defaultConfig is what a fresh environment looks like: one orchestrator that can
// delegate, and no browser until one is named.
func defaultConfig() Config {
	return Config{
		VaultMode: VaultAutonomous,
		Agents: []AgentConfig{
			{Address: "orchestrator", Role: "orchestrator"},
			{Address: "worker", Role: "worker"},
		},
		Hosts: []HostConfig{{Name: "local", Local: true, Default: true}},
	}
}

// withDefaults fills the fields a hand-written config can reasonably omit.
func withDefaults(config Config) Config {
	if strings.TrimSpace(config.VaultMode) == "" {
		config.VaultMode = VaultAutonomous
	}
	if len(config.Agents) == 0 {
		config.Agents = defaultConfig().Agents
	}
	if len(config.Hosts) == 0 {
		config.Hosts = defaultConfig().Hosts
	}
	return config
}
