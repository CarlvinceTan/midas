package tui

import "sync"

// KeybindingID names one configurable action.
type KeybindingID string

// KeybindingDefinition defines an action's default keys and description.
type KeybindingDefinition struct {
	ID          KeybindingID
	DefaultKeys []KeyID
	Description string
}

// KeybindingsConfig replaces defaults for keys present in the map. A present
// empty slice explicitly disables that action.
type KeybindingsConfig map[KeybindingID][]KeyID

// KeybindingConflict reports a key claimed by multiple user overrides.
type KeybindingConflict struct {
	Key         KeyID          `json:"key"`
	Keybindings []KeybindingID `json:"keybindings"`
}

func keybinding(id, description string, keys ...KeyID) KeybindingDefinition {
	return KeybindingDefinition{ID: KeybindingID(id), DefaultKeys: keys, Description: description}
}

// TUIKeybindings are Midas's built-in definitions in declaration order.
var TUIKeybindings = []KeybindingDefinition{
	keybinding("tui.editor.cursorUp", "Move cursor up", "up"),
	keybinding("tui.editor.cursorDown", "Move cursor down", "down"),
	keybinding("tui.editor.historyPrevious", "Select previous prompt history entry"),
	keybinding("tui.editor.historyNext", "Select next prompt history entry"),
	keybinding("tui.editor.cursorLeft", "Move cursor left", "left", "ctrl+b"),
	keybinding("tui.editor.cursorRight", "Move cursor right", "right", "ctrl+f"),
	keybinding("tui.editor.cursorWordLeft", "Move cursor word left", "alt+left", "ctrl+left", "alt+b"),
	keybinding("tui.editor.cursorWordRight", "Move cursor word right", "alt+right", "ctrl+right", "alt+f"),
	keybinding("tui.editor.cursorLineStart", "Move to line start", "home", "ctrl+home", "ctrl+a"),
	keybinding("tui.editor.cursorLineEnd", "Move to line end", "end", "ctrl+end", "ctrl+e"),
	keybinding("tui.editor.jumpForward", "Jump forward to character", "ctrl+]"),
	keybinding("tui.editor.jumpBackward", "Jump backward to character", "ctrl+alt+]"),
	keybinding("tui.editor.pageUp", "Page up", "pageUp", "ctrl+pageUp"),
	keybinding("tui.editor.pageDown", "Page down", "pageDown", "ctrl+pageDown"),
	keybinding("tui.editor.deleteCharBackward", "Delete character backward", "backspace"),
	keybinding("tui.editor.deleteCharForward", "Delete character forward", "delete", "ctrl+d"),
	keybinding("tui.editor.deleteWordBackward", "Delete word backward", "ctrl+w", "alt+backspace"),
	keybinding("tui.editor.deleteWordForward", "Delete word forward", "alt+d", "alt+delete"),
	keybinding("tui.editor.deleteToLineStart", "Delete to line start", "ctrl+u"),
	keybinding("tui.editor.deleteToLineEnd", "Delete to line end", "ctrl+k"),
	keybinding("tui.editor.yank", "Yank", "ctrl+y"),
	keybinding("tui.editor.yankPop", "Yank pop", "alt+y"),
	keybinding("tui.editor.undo", "Undo", "ctrl+-"),
	keybinding("tui.input.newLine", "Insert newline", "shift+enter", "ctrl+j"),
	keybinding("tui.input.submit", "Submit input", "enter"),
	keybinding("tui.input.steer", "Steer active run or dequeue next message", "super+enter", "ctrl+enter"),
	keybinding("tui.input.tab", "Tab / autocomplete", "tab"),
	keybinding("tui.thinking.cycle", "Cycle thinking level", "ctrl+t"),
	keybinding("tui.transcript.expandTools", "Expand or collapse transcript tools", "ctrl+o"),
	keybinding("tui.input.copy", "Copy selection", "ctrl+c"),
	keybinding("tui.select.up", "Move selection up", "up"),
	keybinding("tui.select.down", "Move selection down", "down"),
	keybinding("tui.select.pageUp", "Selection page up", "pageUp"),
	keybinding("tui.select.pageDown", "Selection page down", "pageDown"),
	keybinding("tui.select.confirm", "Confirm selection", "enter"),
	keybinding("tui.select.cancel", "Cancel selection", "escape", "ctrl+c"),
	keybinding("tui.altScreen.pageUp", "Scroll viewport up one page", "pageUp"),
	keybinding("tui.altScreen.pageDown", "Scroll viewport down one page", "pageDown"),
	keybinding("tui.altScreen.halfPageUp", "Scroll viewport up half a page"),
	keybinding("tui.altScreen.halfPageDown", "Scroll viewport down half a page"),
	keybinding("tui.altScreen.lineUp", "Scroll viewport up one line"),
	keybinding("tui.altScreen.lineDown", "Scroll viewport down one line"),
	keybinding("tui.altScreen.previousPrompt", "Jump to previous semantic prompt", "ctrl+shift+up", "ctrl+up"),
	keybinding("tui.altScreen.nextPrompt", "Jump to next semantic prompt", "ctrl+shift+down", "ctrl+down"),
	keybinding("tui.altScreen.search", "Search the primary scroll view", "ctrl+shift+f"),
	keybinding("tui.altScreen.searchNext", "Select the next search match", "enter", "ctrl+g"),
	keybinding("tui.altScreen.searchPrevious", "Select the previous search match", "shift+enter", "ctrl+shift+g"),
	keybinding("tui.altScreen.searchClose", "Close transcript search", "escape"),
	keybinding("tui.altScreen.top", "Scroll viewport to top", "home"),
	keybinding("tui.altScreen.bottom", "Scroll viewport to bottom", "end"),
}

// KeybindingsManager resolves built-in definitions against user overrides.
type KeybindingsManager struct {
	mu          sync.RWMutex
	definitions []KeybindingDefinition
	definition  map[KeybindingID]KeybindingDefinition
	user        KeybindingsConfig
	resolved    map[KeybindingID][]KeyID
	conflicts   []KeybindingConflict
}

// NewKeybindingsManager constructs a registry. Definitions and overrides are copied.
func NewKeybindingsManager(definitions []KeybindingDefinition, userBindings ...KeybindingsConfig) *KeybindingsManager {
	manager := &KeybindingsManager{
		definitions: append([]KeybindingDefinition(nil), definitions...),
		definition:  make(map[KeybindingID]KeybindingDefinition),
		user:        KeybindingsConfig{},
		resolved:    make(map[KeybindingID][]KeyID),
		conflicts:   []KeybindingConflict{},
	}
	for index := range manager.definitions {
		definition := manager.definitions[index]
		definition.DefaultKeys = copyKeyIDs(definition.DefaultKeys)
		manager.definitions[index] = definition
		manager.definition[definition.ID] = definition
	}
	if len(userBindings) > 0 {
		manager.user = copyKeybindingsConfig(userBindings[0])
	}
	manager.rebuildLocked()
	return manager
}

func normalizeKeyIDs(keys []KeyID) []KeyID {
	seen := make(map[KeyID]struct{}, len(keys))
	result := make([]KeyID, 0, len(keys))
	for _, key := range keys {
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func copyKeyIDs(keys []KeyID) []KeyID {
	result := make([]KeyID, len(keys))
	copy(result, keys)
	return result
}

func copyKeybindingsConfig(config KeybindingsConfig) KeybindingsConfig {
	copy := make(KeybindingsConfig, len(config))
	for id, keys := range config {
		copy[id] = copyKeyIDs(keys)
	}
	return copy
}

func (m *KeybindingsManager) rebuildLocked() {
	m.resolved = make(map[KeybindingID][]KeyID, len(m.definitions))
	m.conflicts = []KeybindingConflict{}

	claims := make(map[KeyID][]KeybindingID)
	claimOrder := make([]KeyID, 0)
	for _, definition := range m.definitions {
		userKeys, overridden := m.user[definition.ID]
		if overridden {
			for _, key := range normalizeKeyIDs(userKeys) {
				if len(claims[key]) == 0 {
					claimOrder = append(claimOrder, key)
				}
				claims[key] = append(claims[key], definition.ID)
			}
		}
		keys := definition.DefaultKeys
		if overridden {
			keys = userKeys
		}
		m.resolved[definition.ID] = normalizeKeyIDs(keys)
	}
	for _, key := range claimOrder {
		if len(claims[key]) > 1 {
			m.conflicts = append(m.conflicts, KeybindingConflict{
				Key: key, Keybindings: append([]KeybindingID(nil), claims[key]...),
			})
		}
	}
}

// Matches reports whether data matches any resolved key for an action.
func (m *KeybindingsManager) Matches(data string, id KeybindingID) bool {
	m.mu.RLock()
	keys := copyKeyIDs(m.resolved[id])
	m.mu.RUnlock()
	for _, key := range keys {
		if MatchesKey(data, key) {
			return true
		}
	}
	return false
}

// Keys returns a copy of the resolved keys for an action.
func (m *KeybindingsManager) Keys(id KeybindingID) []KeyID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return copyKeyIDs(m.resolved[id])
}

// Definition returns a copied action definition.
func (m *KeybindingsManager) Definition(id KeybindingID) (KeybindingDefinition, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	definition, ok := m.definition[id]
	definition.DefaultKeys = copyKeyIDs(definition.DefaultKeys)
	return definition, ok
}

// Conflicts returns copied user-binding conflicts.
func (m *KeybindingsManager) Conflicts() []KeybindingConflict {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]KeybindingConflict, len(m.conflicts))
	for index, conflict := range m.conflicts {
		result[index] = KeybindingConflict{Key: conflict.Key, Keybindings: append([]KeybindingID(nil), conflict.Keybindings...)}
	}
	return result
}

// SetUserBindings replaces all overrides and rebuilds resolved bindings.
func (m *KeybindingsManager) SetUserBindings(config KeybindingsConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.user = copyKeybindingsConfig(config)
	m.rebuildLocked()
}

// UserBindings returns a deep copy of configured overrides.
func (m *KeybindingsManager) UserBindings() KeybindingsConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return copyKeybindingsConfig(m.user)
}

// ResolvedBindings returns a deep copy of all resolved actions.
func (m *KeybindingsManager) ResolvedBindings() KeybindingsConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(KeybindingsConfig, len(m.definitions))
	for _, definition := range m.definitions {
		result[definition.ID] = copyKeyIDs(m.resolved[definition.ID])
	}
	return result
}

var (
	globalKeybindingsMu sync.Mutex
	globalKeybindings   *KeybindingsManager
)

// SetKeybindings replaces the process-global keybinding registry.
func SetKeybindings(keybindings *KeybindingsManager) {
	globalKeybindingsMu.Lock()
	globalKeybindings = keybindings
	globalKeybindingsMu.Unlock()
}

// GetKeybindings returns the process-global registry, lazily creating defaults.
func GetKeybindings() *KeybindingsManager {
	globalKeybindingsMu.Lock()
	defer globalKeybindingsMu.Unlock()
	if globalKeybindings == nil {
		globalKeybindings = NewKeybindingsManager(TUIKeybindings)
	}
	return globalKeybindings
}
