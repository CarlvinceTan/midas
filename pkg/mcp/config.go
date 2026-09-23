package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/mcpconfig"
)

const (
	// ConfigFile is the MCP configuration file shared with other agents. It is
	// read as a fallback so a setup made before the merge keeps working.
	ConfigFile = "mcp.json"
	// SettingsFile is the agent's own configuration file, which holds the launch
	// list under "mcpServers" and each server's settings under "mcp".
	SettingsFile = mcpconfig.SettingsFile
	// settingsEnv tells a server Midas launched where the agent's settings file is.
	// Servers that do not know it ignore it.
	settingsEnv = mcpconfig.OverrideEnv
)

type configEnvelope struct {
	MCPServers map[string]Config `json:"mcpServers"`
	Servers    map[string]Config `json:"servers"`
}

// UnmarshalJSON accepts both Midas' compact command array and the common MCP
// command-plus-args representation used by other clients.
func (c *Config) UnmarshalJSON(data []byte) error {
	var wire struct {
		Command  json.RawMessage   `json:"command"`
		Args     []string          `json:"args"`
		URL      string            `json:"url"`
		Env      map[string]string `json:"env"`
		Headers  map[string]string `json:"headers"`
		Disabled bool              `json:"disabled"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var command []string
	rawCommand := strings.TrimSpace(string(wire.Command))
	switch {
	case rawCommand == "" || rawCommand == "null":
		if len(wire.Args) > 0 {
			return errors.New("mcp: args require a command")
		}
	case strings.HasPrefix(rawCommand, `"`):
		var executable string
		if err := json.Unmarshal(wire.Command, &executable); err != nil {
			return fmt.Errorf("mcp: decode command: %w", err)
		}
		command = append([]string{executable}, wire.Args...)
	case strings.HasPrefix(rawCommand, "["):
		if err := json.Unmarshal(wire.Command, &command); err != nil {
			return fmt.Errorf("mcp: decode command: %w", err)
		}
		command = append(command, wire.Args...)
	default:
		return errors.New("mcp: command must be a string or string array")
	}
	*c = Config{Command: command, URL: wire.URL, Env: wire.Env, Headers: wire.Headers, Disabled: wire.Disabled}
	return nil
}

// LoadConfigs reads only Midas-owned MCP configuration. The user's settings file
// names the servers first, project scopes override it, and the legacy mcp.json
// files fill in any server the settings files do not name.
func LoadConfigs(configDir, projectRoot string) (map[string]Config, error) {
	result := make(map[string]Config)
	// Scopes are consulted most specific first, and the settings file beats the
	// legacy mcp.json, so nothing that was configured before the merge changes
	// meaning.
	paths := append(scopedPaths(configDir, projectRoot, SettingsFile), scopedPaths(configDir, projectRoot, ConfigFile)...)
	for _, path := range paths {
		var (
			configs map[string]Config
			err     error
		)
		if filepath.Base(path) == SettingsFile {
			configs, err = loadSettingsFile(path)
		} else {
			configs, err = loadConfigFile(path)
		}
		if err != nil {
			return nil, err
		}
		for name, config := range configs {
			if _, exists := result[name]; exists {
				continue
			}
			result[name] = config
		}
	}
	announceSettings(result, configDir)
	return result, nil
}

// scopedPaths lists one file name in every scope Midas reads, most specific first,
// so the first scope to name a server wins.
func scopedPaths(configDir, projectRoot, name string) []string {
	paths := []string{}
	if projectRoot != "" {
		paths = append(paths, filepath.Join(projectRoot, ".agents", name), filepath.Join(projectRoot, ".midas", name))
	}
	if configDir != "" {
		paths = append(paths, filepath.Join(configDir, name))
	}
	seen := make(map[string]struct{}, len(paths))
	unique := paths[:0]
	for _, path := range paths {
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		unique = append(unique, path)
	}
	return unique
}

// announceSettings tells the servers Midas launches where the agent's settings
// file is, so the servers it ships keep their own settings in the same file as
// everything else. A server that does not know the variable ignores it, and an
// entry that sets it itself is left alone.
func announceSettings(configs map[string]Config, configDir string) {
	if strings.TrimSpace(configDir) == "" {
		return
	}
	settings := filepath.Join(configDir, SettingsFile)
	for name, config := range configs {
		if len(config.Command) == 0 {
			continue
		}
		if _, set := config.Env[settingsEnv]; set {
			continue
		}
		env := make(map[string]string, len(config.Env)+1)
		for key, value := range config.Env {
			env[key] = value
		}
		env[settingsEnv] = settings
		config.Env = env
		configs[name] = config
	}
}

// loadSettingsFile reads the "mcpServers" object of an agent settings file. Other
// keys in that file belong to the agent and are ignored here.
func loadSettingsFile(path string) (map[string]Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mcp: read %s: %w", path, err)
	}
	var envelope struct {
		MCPServers map[string]Config `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("mcp: decode %s: %w", path, err)
	}
	return envelope.MCPServers, nil
}

func loadConfigFile(path string) (map[string]Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mcp: read %s: %w", path, err)
	}
	var envelope configEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("mcp: decode %s: %w", path, err)
	}
	if envelope.MCPServers == nil && envelope.Servers == nil {
		return nil, fmt.Errorf("mcp: %s must contain an mcpServers or servers object", path)
	}
	result := make(map[string]Config, len(envelope.Servers)+len(envelope.MCPServers))
	for name, config := range envelope.Servers {
		result[name] = config
	}
	for name, config := range envelope.MCPServers {
		result[name] = config
	}
	return result, nil
}
