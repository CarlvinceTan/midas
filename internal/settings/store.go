// Package settings owns Midas's small, native configuration surface.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	AutoModelRotation    = "autoModelRotation"
	AutoProviderRotation = "autoProviderRotation"
	TerminalTitle        = "terminalTitle"
	CompactHeader        = "compactHeader"
	TitleMaxWords        = "titleMaxWords"
	StatusMaxWords       = "statusMaxWords"
	// Padding is the space Midas keeps between its content and the terminal
	// borders, in cells (0–4).
	Padding        = "padding"
	RemotePassword = "remotePassword"
	// Currency is the cost display currency: "default" (USD), "location", or
	// an ISO 4217 code. It is shared with the pre-port Midas settings file.
	Currency = "currency"
	// Per-agent model configuration, keyed by agent name: the explicit
	// `provider/model` a user set in /agents and the model each entry agent
	// last used. Levels are keyed by agent name and by model reference.
	// Compaction is Pi's compaction configuration object: whether auto
	// compaction runs, the tokens reserved for the response, and how much recent
	// context a compaction keeps.
	Compaction          = "compaction"
	AgentModels         = "agentModels"
	AgentLastUsed       = "agentLastUsed"
	AgentThinkingLevels = "agentThinkingLevels"
	ModelThinkingLevels = "modelThinkingLevels"
	// Agents holds user-defined agents, keyed by name: description, mode,
	// model, reasoning, prompt, tools, parents, and maxDepth.
	Agents = "agents"
)

type Values struct {
	AutoModelRotation    bool
	AutoProviderRotation bool
	TerminalTitle        bool
	CompactHeader        bool
	TitleMaxWords        int
	StatusMaxWords       int
	Padding              int
	RemotePassword       string
	Currency             string
	Compaction           CompactionSettings
}

// CompactionSettings mirrors Pi's compaction settings object.
type CompactionSettings struct {
	Enabled          bool `json:"enabled"`
	ReserveTokens    int  `json:"reserveTokens"`
	KeepRecentTokens int  `json:"keepRecentTokens"`
}

// DefaultCompactionSettings matches Pi's defaults.
func DefaultCompactionSettings() CompactionSettings {
	return CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000}
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(configDir string) *Store {
	return &Store{path: filepath.Join(configDir, "settings.json")}
}

func (s *Store) Load() Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := s.read()
	return Values{
		AutoModelRotation:    boolValue(raw, AutoModelRotation, true),
		AutoProviderRotation: boolValue(raw, AutoProviderRotation, false),
		TerminalTitle:        boolValue(raw, TerminalTitle, true),
		CompactHeader:        boolValue(raw, CompactHeader, false),
		TitleMaxWords:        intValue(raw, TitleMaxWords, 8, 1, 20),
		StatusMaxWords:       intValue(raw, StatusMaxWords, 6, 1, 12),
		Padding:              intValue(raw, Padding, 1, 0, 4),
		RemotePassword:       stringValue(raw, RemotePassword),
		Currency:             normalizedCurrencyValue(raw),
		Compaction:           compactionValue(raw),
	}
}

// Object returns a stored JSON object as its raw fields, so configuration owned
// by another package (such as user-defined agents) can be decoded there without
// this package knowing its shape. A missing or unreadable value is nil.
func (s *Store) Object(key string) map[string]json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(s.read()[key], &object); err != nil {
		return nil
	}
	return object
}

func (s *Store) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(key, value)
}

// StringMap returns a stored string map such as agentModels. Blank names and
// values are dropped, so a hand-edited settings file degrades to the agent's
// default instead of breaking startup.
func (s *Store) StringMap(key string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stringMapValue(s.read(), key)
}

// SetStringMapEntry writes one field of a stored string map. An empty value
// removes the field, so clearing an /agents override restores the agent's
// default instead of pinning an empty model.
func (s *Store) SetStringMapEntry(key, field, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := stringMapValue(s.read(), key)
	field, value = strings.TrimSpace(field), strings.TrimSpace(value)
	if field != "" {
		if value == "" {
			delete(values, field)
		} else {
			values[field] = value
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return s.setLocked(key, json.RawMessage(encoded))
}

func stringMapValue(raw map[string]json.RawMessage, key string) map[string]string {
	var stored map[string]string
	if err := json.Unmarshal(raw[key], &stored); err != nil {
		stored = nil
	}
	values := make(map[string]string, len(stored))
	for name, value := range stored {
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		values[name] = value
	}
	return values
}

func (s *Store) setLocked(key string, value any) error {
	raw := s.read()
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw[key] = encoded
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, s.path)
}

func (s *Store) read() map[string]json.RawMessage {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return make(map[string]json.RawMessage)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return make(map[string]json.RawMessage)
	}
	return raw
}

func boolValue(raw map[string]json.RawMessage, key string, fallback bool) bool {
	var value bool
	if json.Unmarshal(raw[key], &value) != nil {
		return fallback
	}
	return value
}

func intValue(raw map[string]json.RawMessage, key string, fallback, minimum, maximum int) int {
	var value int
	if json.Unmarshal(raw[key], &value) != nil {
		return fallback
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func stringValue(raw map[string]json.RawMessage, key string) string {
	var value string
	if json.Unmarshal(raw[key], &value) != nil {
		return ""
	}
	return value
}

// SetCompaction writes the compaction object, keeping the enabled flag and the
// two token budgets together as one setting.
func (s *Store) SetCompaction(value CompactionSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(Compaction, value)
}

// compactionValue reads the compaction object, falling back to Pi's defaults for
// missing or out-of-range fields.
func compactionValue(raw map[string]json.RawMessage) CompactionSettings {
	value := DefaultCompactionSettings()
	if len(raw[Compaction]) == 0 {
		return value
	}
	var stored struct {
		Enabled          *bool `json:"enabled"`
		ReserveTokens    *int  `json:"reserveTokens"`
		KeepRecentTokens *int  `json:"keepRecentTokens"`
	}
	if json.Unmarshal(raw[Compaction], &stored) != nil {
		return value
	}
	if stored.Enabled != nil {
		value.Enabled = *stored.Enabled
	}
	if stored.ReserveTokens != nil && *stored.ReserveTokens >= 1024 && *stored.ReserveTokens <= 1_000_000 {
		value.ReserveTokens = *stored.ReserveTokens
	}
	if stored.KeepRecentTokens != nil && *stored.KeepRecentTokens >= 1024 && *stored.KeepRecentTokens <= 1_000_000 {
		value.KeepRecentTokens = *stored.KeepRecentTokens
	}
	return value
}

// normalizedCurrencyValue keeps the currency key to "default", "location" or a
// three-letter ISO code, matching the pre-port Midas settings contract.
func normalizedCurrencyValue(raw map[string]json.RawMessage) string {
	value := strings.TrimSpace(stringValue(raw, Currency))
	switch {
	case value == "":
		return "default"
	case value == "default" || value == "location":
		return value
	case len(value) == 3 && isAlpha(value):
		return strings.ToUpper(value)
	default:
		return "default"
	}
}

func isAlpha(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return len(value) > 0
}
