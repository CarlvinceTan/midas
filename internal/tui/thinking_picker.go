package tui

import (
	"strings"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

type ThinkingPickerOptions struct {
	Model        ai.Model
	Current      ai.ThinkingLevel
	Default      ai.ThinkingLevel
	Title        string
	OnSelect     func(ai.ThinkingLevel)
	OnSetDefault func(ai.ThinkingLevel)
	OnCancel     func()
	Theme        *Theme
}

type thinkingPickerItem struct {
	level       ai.ThinkingLevel
	description string
}

type ThinkingPicker struct {
	FocusState
	items        []thinkingPickerItem
	filtered     []thinkingPickerItem
	filter       string
	current      ai.ThinkingLevel
	defaultLevel ai.ThinkingLevel
	title        string
	selected     int
	onSelect     func(ai.ThinkingLevel)
	onSetDefault func(ai.ThinkingLevel)
	onCancel     func()
	theme        *Theme
	rowTarget    map[int]int
}

var allThinkingLevels = []ai.ThinkingLevel{
	ai.ThinkingOff, ai.ThinkingMinimal, ai.ThinkingLow, ai.ThinkingMedium,
	ai.ThinkingHigh, ai.ThinkingXHigh, ai.ThinkingMax,
}

func ThinkingLevels(model ai.Model) []ai.ThinkingLevel {
	if !model.Reasoning {
		return []ai.ThinkingLevel{ai.ThinkingOff}
	}
	levels := make([]ai.ThinkingLevel, 0, len(allThinkingLevels))
	for _, level := range allThinkingLevels {
		mapped, exists := model.ThinkingLevelMap[level]
		if exists && mapped == nil {
			continue
		}
		levels = append(levels, level)
	}
	return levels
}

// NextThinkingLevel returns the next thinking level supported by model,
// wrapping to the first level after the last one.
func NextThinkingLevel(model ai.Model, current ai.ThinkingLevel) ai.ThinkingLevel {
	levels := ThinkingLevels(model)
	for index, level := range levels {
		if level == current {
			return levels[(index+1)%len(levels)]
		}
	}
	return levels[0]
}

func NewThinkingPicker(options ThinkingPickerOptions) *ThinkingPicker {
	descriptions := map[ai.ThinkingLevel]string{
		ai.ThinkingOff:     "No reasoning",
		ai.ThinkingMinimal: "(~1k tokens)",
		ai.ThinkingLow:     "(~2k tokens)",
		ai.ThinkingMedium:  "(~8k tokens)",
		ai.ThinkingHigh:    "(~16k tokens)",
		ai.ThinkingXHigh:   "(~32k tokens)",
		ai.ThinkingMax:     "Maximum reasoning",
	}
	levels := ThinkingLevels(options.Model)
	items := make([]thinkingPickerItem, 0, len(levels))
	selected := 0
	for index, level := range levels {
		items = append(items, thinkingPickerItem{level: level, description: descriptions[level]})
		if level == options.Current {
			selected = index
		}
	}
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = "Thinking"
	}
	defaultLevel := options.Default
	if defaultLevel == "" {
		defaultLevel = ai.ThinkingMedium
	}
	return &ThinkingPicker{
		items: items, filtered: append([]thinkingPickerItem(nil), items...), selected: selected,
		current: options.Current, defaultLevel: defaultLevel, title: title,
		onSelect: options.OnSelect, onSetDefault: options.OnSetDefault, onCancel: options.OnCancel,
		theme: options.Theme, rowTarget: make(map[int]int),
	}
}

// SetTitle is how Dialog names this picker's frame: the picker draws the frame, so
// the caller must not wrap it in a panel of its own.
func (p *ThinkingPicker) SetTitle(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Thinking"
	}
	p.title = title
}

func (p *ThinkingPicker) Invalidate()   {}
func (p *ThinkingPicker) Selected() int { return p.selected }

func (p *ThinkingPicker) refilter() {
	p.filtered = FuzzyFilter(p.items, p.filter, func(item thinkingPickerItem) string {
		return string(item.level) + " " + item.description
	})
	p.selected = min(p.selected, max(0, len(p.filtered)-1))
}

func (p *ThinkingPicker) HandleInput(data string) {
	if MatchesKey(data, "escape") || MatchesKey(data, "ctrl+c") {
		if p.onCancel != nil {
			p.onCancel()
		}
		return
	}
	if MatchesKey(data, "enter") {
		if p.selected < len(p.filtered) && p.onSelect != nil {
			p.onSelect(p.filtered[p.selected].level)
		}
		return
	}
	if MatchesKey(data, "up") {
		if len(p.filtered) > 0 {
			p.selected = (p.selected + len(p.filtered) - 1) % len(p.filtered)
		}
		return
	}
	if MatchesKey(data, "down") {
		if len(p.filtered) > 0 {
			p.selected = (p.selected + 1) % len(p.filtered)
		}
		return
	}
	if MatchesKey(data, "backspace") {
		if p.filter != "" {
			_, size := utf8.DecodeLastRuneInString(p.filter)
			p.filter = p.filter[:len(p.filter)-size]
			p.refilter()
		}
		return
	}
	if utf8.RuneCountInString(data) == 1 {
		r, _ := utf8.DecodeRuneInString(data)
		if r >= ' ' && r != 0x7f {
			p.filter += data
			p.refilter()
		}
	}
}

func (p *ThinkingPicker) HandleMouse(event MouseEvent) *MouseResult {
	if event.Type != MouseClick || event.Button != MouseLeft {
		return nil
	}
	index, ok := p.rowTarget[event.Y]
	if !ok || index >= len(p.filtered) {
		return nil
	}
	p.selected = index
	if p.onSelect != nil {
		p.onSelect(p.filtered[index].level)
	}
	render := true
	return &MouseResult{Handled: true, Render: &render}
}

func (p *ThinkingPicker) Render(width int) []string {
	total := max(3, width)
	inner := total - 2
	theme := p.theme
	if theme == nil {
		theme = CurrentTheme()
	}
	border := func(value string) string { return theme.FG("borderAccent", value) }
	titleLabel := " " + p.title + " "
	title := ""
	if total >= tuitext.VisibleWidth(titleLabel)+6 {
		title = titleLabel
	}
	top := border("╭─") + theme.FG("borderAccent", title) + border(strings.Repeat("─", max(0, total-3-tuitext.VisibleWidth(title)))+"╮")
	row := func(line string) string {
		contentWidth := max(1, inner-1)
		content := tuitext.TruncateToWidth(line, contentWidth, "…", true)
		content += strings.Repeat(" ", max(0, contentWidth-tuitext.VisibleWidth(content)))
		return border("│") + " " + content + border("│")
	}

	rows := []string{row("> " + theme.FG("text", p.filter) + theme.FG("accent", "█"))}
	const visible = 10
	count := min(visible, max(1, len(p.filtered)))
	start := 0
	if len(p.filtered) > visible {
		start = max(0, min(p.selected-visible/2, len(p.filtered)-visible))
	}
	p.rowTarget = make(map[int]int)
	for index := 0; index < count; index++ {
		position := start + index
		if position >= len(p.filtered) {
			rows = append(rows, row(""))
			continue
		}
		p.rowTarget[index+1] = position
		item := p.filtered[position]
		selected := position == p.selected
		marker := "  "
		if selected {
			marker = theme.FG("accent", "→ ")
		}
		name := string(item.level)
		if item.level == p.current {
			name = theme.FG("success", name)
		} else if selected {
			name = theme.FG("accent", name)
		} else {
			name = theme.FG("text", name)
		}
		suffix := item.description
		if item.level == p.defaultLevel {
			suffix += " · default"
		}
		rows = append(rows, row(marker+name+theme.FG("muted", "  "+suffix)))
	}
	bottom := border("╰" + strings.Repeat("─", inner) + "╯")
	return append(append([]string{top}, rows...), bottom)
}

func thinkingThemeName(level ai.ThinkingLevel) string {
	value := capitalizePanelTitle(strings.ToLower(string(level)))
	return "thinking" + value
}
