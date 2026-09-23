package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"

	"github.com/CarlvinceTan/midas/internal/profiles"
	providerpkg "github.com/CarlvinceTan/midas/internal/provider"
	midassettings "github.com/CarlvinceTan/midas/internal/settings"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// AgentModelConfig is Midas' per-agent model configuration. Entry agents
// (main) remembers the model and reasoning level it last used;
// every other agent inherits the model of the agent that invokes it unless the
// user pins one in /agents.
type AgentModelConfig struct {
	store       *midassettings.Store
	models      map[string]string
	lastUsed    map[string]string
	levels      map[string]string
	modelLevels map[string]string
}

func LoadAgentModelConfig(store *midassettings.Store) AgentModelConfig {
	config := AgentModelConfig{
		store: store, models: map[string]string{}, lastUsed: map[string]string{},
		levels: map[string]string{}, modelLevels: map[string]string{},
	}
	if store == nil {
		return config
	}
	config.models = store.StringMap(midassettings.AgentModels)
	config.lastUsed = store.StringMap(midassettings.AgentLastUsed)
	config.levels = store.StringMap(midassettings.AgentThinkingLevels)
	config.modelLevels = store.StringMap(midassettings.ModelThinkingLevels)
	return config
}

// Reload re-reads the stored configuration, so a /reload picks up edits made to
// settings.json outside Midas without restarting it.
func (c *AgentModelConfig) Reload() {
	if c == nil || c.store == nil {
		return
	}
	c.models = c.store.StringMap(midassettings.AgentModels)
	c.lastUsed = c.store.StringMap(midassettings.AgentLastUsed)
	c.levels = c.store.StringMap(midassettings.AgentThinkingLevels)
	c.modelLevels = c.store.StringMap(midassettings.ModelThinkingLevels)
}

// Ref resolves the `provider/model` an agent runs with. A model pinned in
// /agents wins; entry agents fall back to the model they last used; every other
// agent inherits the model of its caller, which the caller passes as inherited.
func (c *AgentModelConfig) Ref(name, inherited string) string {
	name = strings.TrimSpace(name)
	if pinned := strings.TrimSpace(c.models[name]); pinned != "" {
		return pinned
	}
	// An agent defined in settings.json may name its own model; a choice made in
	// /agents still wins over it.
	if configured := profiles.ConfiguredModel(name); configured != "" {
		return configured
	}
	if profiles.IsEntryAgent(name) {
		return strings.TrimSpace(c.lastUsed[name])
	}
	return strings.TrimSpace(inherited)
}

// Pinned reports whether the user chose a model for this agent in /agents.
func (c *AgentModelConfig) Pinned(name string) bool {
	return strings.TrimSpace(c.models[strings.TrimSpace(name)]) != ""
}

// Level resolves an agent's reasoning level: its own /agents choice, then the
// level recorded for the model it runs with, then the caller's current level.
func (c *AgentModelConfig) Level(name, ref string, fallback ai.ThinkingLevel) ai.ThinkingLevel {
	if level := parseThinkingLevel(c.levels[strings.TrimSpace(name)]); level != "" {
		return level
	}
	if level := parseThinkingLevel(profiles.ConfiguredReasoning(name)); level != "" {
		return level
	}
	if level := parseThinkingLevel(c.modelLevels[strings.TrimSpace(ref)]); level != "" {
		return level
	}
	return fallback
}

// ParentAgent returns the agent whose model a subagent inherits. The read-only
// and utility helpers inherit from main, the only entry agent.
func ParentAgent(name, entryAgent string) string {
	parents := profiles.ParentAgents(name)
	if len(parents) == 0 {
		return ""
	}
	if len(parents) == 1 {
		return parents[0]
	}
	return strings.TrimSpace(entryAgent)
}

// InheritedRef resolves the model an agent inherits when it has no pinned
// model: the model of its parent, which is the model the session already runs
// when the parent has no override or Last Used model of its own.
func (c *AgentModelConfig) InheritedRef(name, entryAgent, current string) string {
	parent := ParentAgent(name, entryAgent)
	if parent == "" {
		return strings.TrimSpace(current)
	}
	if ref := c.Ref(parent, current); ref != "" {
		return ref
	}
	return strings.TrimSpace(current)
}

// Display is how /agents shows an agent's model: the model it resolves to with its
// reasoning level, or the label describing where that model comes from.
func (c *AgentModelConfig) Display(name string, catalog []ai.Model) string {
	name = strings.TrimSpace(name)
	if ref := c.Ref(name, ""); ref != "" {
		return ModelRefDisplay(catalog, ref) + " · " + string(c.Level(name, ref, ai.ThinkingMedium))
	}
	return profiles.DefaultModelLabel(name)
}

// defaultDescription explains what an agent's default resolves to.
func defaultDescription(name string) string {
	if profiles.IsEntryAgent(name) {
		return "remembers the model this agent last used"
	}
	parents := profiles.ParentAgents(name)
	if len(parents) == 0 {
		return "inherits the model of the agent that runs it"
	}
	return "inherits from " + strings.Join(parents, ", ")
}

// ModelRefDisplay renders a stored `provider/model` reference with the catalog
// name when the catalog knows it.
func ModelRefDisplay(catalog []ai.Model, ref string) string {
	name, _ := ModelDisplayParts(ModelForRef(ref, catalog))
	return name
}

// SplitModelReference splits "provider/model" into its parts, keeping the
// fallback provider for a bare model ID.
func SplitModelReference(provider, model string) (string, string) {
	return ai.SplitModelReference(provider, model)
}

// ModelForRef resolves a stored model reference through the provider layer.
func ModelForRef(ref string, catalog []ai.Model) ai.Model {
	return providerpkg.ModelForRef(ref, catalog)
}

// AgentPanelOptions are the /agents rows: every agent, the model it runs with,
// and whether it is a main, subagent, or utility agent. The name and model columns
// are padded so the rows line up, and the job title column says what each agent
// is for without repeating its description.
func AgentPanelOptions(list []profiles.Profile, current string, config AgentModelConfig, catalog []ai.Model) []Option {
	// Main agents first, then subagents, then the utility helpers, so the list reads
	// in the same order as the startup summary. The incoming order (alphabetical)
	// is kept inside each group.
	rank := func(mode string) int {
		switch mode {
		case profiles.ModePrimary:
			return 0
		case profiles.ModeSubagent:
			return 1
		default:
			return 2
		}
	}
	ordered := append([]profiles.Profile(nil), list...)
	slices.SortStableFunc(ordered, func(a, b profiles.Profile) int { return cmp.Compare(rank(a.Mode), rank(b.Mode)) })
	list = ordered
	models := make([]string, len(list))
	modes := make([]string, len(list))
	nameWidth, modelWidth := 0, 0
	for index, profile := range list {
		models[index] = config.Display(profile.Name, catalog)
		modes[index] = CapitalizeAgentName(profile.Mode)
		nameWidth = max(nameWidth, tuitext.VisibleWidth(CapitalizeAgentName(profile.Name)))
		modelWidth = max(modelWidth, tuitext.VisibleWidth(models[index]))
	}
	options := make([]Option, 0, len(list))
	for index, profile := range list {
		name := CapitalizeAgentName(profile.Name)
		options = append(options, Option{
			// The picker puts two spaces between the label and the description, so
			// padding the label aligns the model column across every row.
			Label:       name + strings.Repeat(" ", max(0, nameWidth-tuitext.VisibleWidth(name))),
			Description: models[index] + strings.Repeat(" ", max(0, modelWidth-tuitext.VisibleWidth(models[index]))) + "  " + modes[index],
			Value:       profile.Name,
		})
	}
	return options
}

// ModelChoiceOptions are the /agents choices for one agent: the agent's default
// first, then every model the session knows about.
func ModelChoiceOptions(name string, catalog []ai.Model) []Option {
	options := []Option{{
		Label:       profiles.DefaultModelLabel(name),
		Description: defaultDescription(name),
		Value:       "",
	}}
	for _, model := range catalog {
		modelName, providerName := ModelDisplayParts(model)
		options = append(options, Option{
			Label: modelName, Description: providerName, Value: model.Provider + "/" + model.ID,
		})
	}
	return options
}

// SetModel pins a model for an agent. An empty reference clears the pin and the
// agent-specific reasoning level, returning the agent to its default.
func (c *AgentModelConfig) SetModel(name, ref string) error {
	name, ref = strings.TrimSpace(name), strings.TrimSpace(ref)
	if err := c.persist(midassettings.AgentModels, name, ref); err != nil {
		return err
	}
	c.models[name] = ref
	if ref != "" {
		return nil
	}
	delete(c.models, name)
	if err := c.persist(midassettings.AgentThinkingLevels, name, ""); err != nil {
		return err
	}
	delete(c.levels, name)
	return nil
}

// SetLevel records an agent's own reasoning level for the model it runs with.
func (c *AgentModelConfig) SetLevel(name string, level ai.ThinkingLevel) error {
	name = strings.TrimSpace(name)
	if err := c.persist(midassettings.AgentThinkingLevels, name, string(level)); err != nil {
		return err
	}
	c.levels[name] = string(level)
	return nil
}

// SetModelLevel records the level used with a model, matching how /model and
// /thinking remember the reasoning level of the active model.
func (c *AgentModelConfig) SetModelLevel(ref string, level ai.ThinkingLevel) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if err := c.persist(midassettings.ModelThinkingLevels, ref, string(level)); err != nil {
		return err
	}
	c.modelLevels[ref] = string(level)
	return nil
}

// SetLastUsed records the model an entry agent just ran with.
func (c *AgentModelConfig) SetLastUsed(name, ref string) error {
	name, ref = strings.TrimSpace(name), strings.TrimSpace(ref)
	if name == "" || ref == "" || !profiles.IsEntryAgent(name) {
		return nil
	}
	if err := c.persist(midassettings.AgentLastUsed, name, ref); err != nil {
		return err
	}
	c.lastUsed[name] = ref
	return nil
}

func (c *AgentModelConfig) persist(key, field, value string) error {
	if c.store == nil {
		return nil
	}
	if err := c.store.SetStringMapEntry(key, field, value); err != nil {
		return fmt.Errorf("could not save the agent model: %w", err)
	}
	return nil
}

// CapitalizeAgentName renders an agent name as /agents shows it.
func CapitalizeAgentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// parseThinkingLevel accepts only levels Midas can send to a provider, so a
// stale settings value never reaches the agent loop.
func parseThinkingLevel(value string) ai.ThinkingLevel {
	level := ai.ThinkingLevel(strings.TrimSpace(value))
	for _, candidate := range ThinkingLevels(ai.Model{Reasoning: true}) {
		if candidate == level {
			return level
		}
	}
	return ""
}
