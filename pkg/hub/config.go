// Package hub is a lean local homeserver for agents: it connects messaging
// bridges on demand, keeps their history in one place, and exposes all of it
// over MCP. It is deliberately independent of any agent that talks to it — the
// only thing it shares with Midas is where its own config file lives.
//
// Nothing is attached by default. With an empty configuration the hub starts no
// bridge processes, opens no connections, and runs no timers: bridges exist only
// after someone installs and starts one.
package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/pkg/mcpconfig"
)

// ConfigFile is the hub's own configuration file inside the agent config
// directory. The hub reads and writes this file and nothing else there.
const ConfigFile = "hub.json"

// DefaultIdleTimeout is how long a bridge may sit unused before the supervisor
// stops it.
const DefaultIdleTimeout = 10 * time.Minute

// Config is the whole hub configuration.
type Config struct {
	// Listen is the HTTP address to serve MCP on. Empty means stdio only.
	Listen string `json:"listen,omitempty"`
	// Token authorises HTTP clients. The hub generates one the first time it
	// listens and rewrites the file with owner-only permissions.
	Token string `json:"token,omitempty"`
	// IdleTimeout stops a bridge that has not been used for this long.
	IdleTimeout string `json:"idleTimeout,omitempty"`
	// Registry names an optional catalog of installable bridges: a local path or a
	// URL. Empty means every install must name an explicit source, so an
	// unconfigured hub never uses the network on its own.
	Registry string `json:"registry,omitempty"`
	// RegistryTTL is how long a fetched registry stays fresh. Zero uses the
	// default of one hour.
	RegistryTTLSeconds int `json:"registryTTLSeconds,omitempty"`
	// Bridges are the installed bridges, keyed by name.
	Bridges map[string]BridgeConfig `json:"bridges"`
}

// BridgeConfig describes one installed bridge.
type BridgeConfig struct {
	// Source is where the bridge came from: a local path or a URL.
	Source string `json:"source,omitempty"`
	// Checksum is the expected sha256 of the bridge binary, when known.
	Checksum string `json:"checksum,omitempty"`
	// Command runs the bridge. The hub appends nothing to it.
	Command []string `json:"command"`
	// Env is extra environment for the bridge process.
	Env map[string]string `json:"env,omitempty"`
	// Accounts are the account IDs this bridge serves, discovered at runtime and
	// cached here so a fresh process can answer without waiting.
	Accounts []string `json:"accounts,omitempty"`
}

// Clone returns a copy that shares no mutable state with the receiver, so a
// caller cannot reach into the stored configuration through a returned value.
func (c Config) Clone() Config {
	clone := c
	clone.Bridges = make(map[string]BridgeConfig, len(c.Bridges))
	for name, bridge := range c.Bridges {
		bridge.Command = append([]string(nil), bridge.Command...)
		bridge.Accounts = append([]string(nil), bridge.Accounts...)
		if bridge.Env != nil {
			bridge.Env = maps.Clone(bridge.Env)
		}
		clone.Bridges[name] = bridge
	}
	return clone
}

// RegistryTTL is how long a fetched registry is reused before it is read again.
func (c Config) RegistryTTL() time.Duration {
	if c.RegistryTTLSeconds <= 0 {
		return time.Hour
	}
	return time.Duration(c.RegistryTTLSeconds) * time.Second
}

// IdleTTL returns the configured idle timeout, or the default.
func (c Config) IdleTTL() time.Duration {
	if strings.TrimSpace(c.IdleTimeout) == "" {
		return DefaultIdleTimeout
	}
	parsed, err := time.ParseDuration(c.IdleTimeout)
	if err != nil || parsed <= 0 {
		return DefaultIdleTimeout
	}
	return parsed
}

// ConfigLocation is where the hub's configuration lives: its own hub.json beside
// the agent's config, or the mcp.hub section of the agent's settings file when
// Midas launched it, so everything the user configured for Midas is in one place.
type ConfigLocation struct {
	Path string
	// Section addresses the hub's object inside a shared file. It is nil when the
	// hub owns the whole file, which is how it runs under any other agent.
	Section []string
}

// ResolveConfigLocation returns where this process reads and writes hub settings.
func ResolveConfigLocation(getenv func(string) string) (ConfigLocation, error) {
	path, section := mcpconfig.Location(getenv, "hub")
	if path == "" {
		return ConfigLocation{}, errors.New("hub: could not resolve a configuration path")
	}
	return ConfigLocation{Path: path, Section: section}, nil
}

// OwnConfigPath is the hub's own file inside the agent config directory. It
// honours MIDAS_CONFIG_DIR because that is where the user already keeps hub.json,
// not because the hub reads anything else from there.
func OwnConfigPath(getenv func(string) string) (string, error) {
	return mcpconfig.LegacyPath(getenv, "hub"), nil
}

// ConfigPath returns the path of the file this configuration is read from and
// written to.
func (f ConfigLocation) ConfigPath() string { return f.Path }

// Load reads the hub's configuration. When the shared settings file has no hub
// section yet, the hub's own file is used, so a configuration made before the
// merge keeps working until the next write.
func (f ConfigLocation) Load() (Config, error) {
	stored, err := mcpconfig.Read(f.Path, f.Section)
	if err != nil {
		return Config{}, fmt.Errorf("hub: %w", err)
	}
	if stored == nil && f.Section != nil {
		stored, err = mcpconfig.Read(f.legacyPath(), nil)
		if err != nil {
			return Config{}, fmt.Errorf("hub: %w", err)
		}
	}
	if stored == nil {
		return Config{Bridges: map[string]BridgeConfig{}}, nil
	}
	config := Config{}
	if err := json.Unmarshal(stored, &config); err != nil {
		return Config{}, fmt.Errorf("hub: parse %s: %w", f.Path, err)
	}
	if config.Bridges == nil {
		config.Bridges = map[string]BridgeConfig{}
	}
	return config, nil
}

// Save writes the configuration atomically, preserving every other section of a
// shared file.
func (f ConfigLocation) Save(config Config) error {
	if config.Bridges == nil {
		config.Bridges = map[string]BridgeConfig{}
	}
	if err := mcpconfig.Update(f.Path, f.Section, func(json.RawMessage) (any, error) { return config, nil }); err != nil {
		return fmt.Errorf("hub: %w", err)
	}
	return nil
}

// legacyPath is the hub's own file, which held the configuration before it moved
// into the agent's settings file.
func (f ConfigLocation) legacyPath() string {
	return filepath.Join(filepath.Dir(f.Path), ConfigFile)
}

// StateDir is where the hub keeps its database and installed bridges, beside its
// configuration. HUB_STATE_DIR overrides it for tests and CI.
func StateDir(getenv func(string) string) (string, error) {
	if explicit := strings.TrimSpace(getenv("HUB_STATE_DIR")); explicit != "" {
		return explicit, nil
	}
	path, err := OwnConfigPath(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "hub"), nil
}

// LoadConfig reads the configuration and reports where it lives, returning the
// empty default when nothing has been configured yet.
func LoadConfig(getenv func(string) string) (Config, string, error) {
	file, err := ResolveConfigLocation(getenv)
	if err != nil {
		return Config{}, "", err
	}
	config, err := file.Load()
	return config, file.Path, err
}

// ReadConfig reads one configuration file that the hub owns outright.
func ReadConfig(path string) (Config, error) {
	return ConfigLocation{Path: path}.Load()
}

// WriteConfig persists the configuration with owner-only permissions, since it
// can hold an HTTP token, and without disturbing any other key in the file.
func WriteConfig(path string, config Config) error {
	return ConfigLocation{Path: path}.Save(config)
}
