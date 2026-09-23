package tui

import (
	"strings"

	"github.com/CarlvinceTan/midas/internal/profiles"
	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

type AgentPickerOptions struct {
	Profiles []profiles.Profile
	Current  string
	OnSelect func(profiles.Profile)
	OnCancel func()
	Theme    *Theme
}

// AgentPicker switches between Midas's built-in capability profiles. Reserved
// worker profiles are omitted by the caller and cannot be selected here.
type AgentPicker struct {
	FocusState
	profiles  []profiles.Profile
	selected  int
	onSelect  func(profiles.Profile)
	onCancel  func()
	theme     *Theme
	rowTarget map[int]int
}

func NewAgentPicker(options AgentPickerOptions) *AgentPicker {
	profiles := append([]profiles.Profile(nil), options.Profiles...)
	selected := 0
	for index, profile := range profiles {
		if profile.Name == options.Current {
			selected = index
			break
		}
	}
	return &AgentPicker{profiles: profiles, selected: selected, onSelect: options.OnSelect, onCancel: options.OnCancel, theme: options.Theme, rowTarget: make(map[int]int)}
}

func (p *AgentPicker) Invalidate()   {}
func (p *AgentPicker) Selected() int { return p.selected }

func (p *AgentPicker) HandleInput(data string) {
	if MatchesKey(data, "escape") || MatchesKey(data, "ctrl+c") {
		if p.onCancel != nil {
			p.onCancel()
		}
		return
	}
	if len(p.profiles) == 0 {
		return
	}
	if MatchesKey(data, "up") {
		p.selected = (p.selected + len(p.profiles) - 1) % len(p.profiles)
		return
	}
	if MatchesKey(data, "down") {
		p.selected = (p.selected + 1) % len(p.profiles)
		return
	}
	if MatchesKey(data, "enter") && p.onSelect != nil {
		p.onSelect(p.profiles[p.selected])
	}
}

func (p *AgentPicker) HandleMouse(event MouseEvent) *MouseResult {
	if event.Type != MouseClick || event.Button != MouseLeft {
		return nil
	}
	index, ok := p.rowTarget[event.Y]
	if !ok {
		return nil
	}
	p.selected = index
	if p.onSelect != nil {
		p.onSelect(p.profiles[index])
	}
	render := true
	return &MouseResult{Handled: true, Render: &render}
}

func (p *AgentPicker) Render(width int) []string {
	width = max(1, width)
	theme := p.theme
	if theme == nil {
		theme = CurrentTheme()
	}
	const visible = 8
	start := max(0, min(p.selected-visible/2, len(p.profiles)-visible))
	lines := make([]string, 0, visible)
	p.rowTarget = make(map[int]int)
	for row := 0; row < visible; row++ {
		index := start + row
		if index >= len(p.profiles) {
			lines = append(lines, "")
			continue
		}
		p.rowTarget[row] = index
		profile := p.profiles[index]
		selected := index == p.selected
		marker := "  "
		name := capitalizePanelTitle(profile.Name)
		if selected {
			marker = theme.FG("accent", "→ ")
			name = theme.FG("accent", name)
		} else {
			name = theme.FG("text", name)
		}
		available := max(1, width-2)
		nameWidth := min(14, available)
		head := tuitext.TruncateToWidth(name, nameWidth, "…", true)
		descriptionWidth := max(0, available-nameWidth)
		description := ""
		if descriptionWidth > 0 {
			description = theme.FG("muted", tuitext.TruncateToWidth("  "+strings.TrimSpace(profile.Description), descriptionWidth, "…", false))
		}
		lines = append(lines, marker+head+description)
	}
	return lines
}
