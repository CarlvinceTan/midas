package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type Option struct {
	Label       string
	Description string
	Value       string
}

// OptionPicker is the compact content-only list used inside a Panel. With
// search enabled it grows a `> ` query row at the top that filters the list —
// the same affordance as the model picker, so long provider catalogs stay
// navigable.
type OptionPicker struct {
	FocusState
	options   []Option
	filtered  []int
	selected  int
	search    bool
	filter    string
	onSelect  func(string)
	onCancel  func()
	rowTarget map[int]int
}

func NewOptionPicker(options []Option, onSelect func(string), onCancel func()) *OptionPicker {
	return newOptionPicker(options, onSelect, onCancel, false)
}

// NewSearchOptionPicker builds a filterable option list (search + Up/Down +
// Enter), matching the model picker's interaction.
func NewSearchOptionPicker(options []Option, onSelect func(string), onCancel func()) *OptionPicker {
	return newOptionPicker(options, onSelect, onCancel, true)
}

func newOptionPicker(options []Option, onSelect func(string), onCancel func(), search bool) *OptionPicker {
	picker := &OptionPicker{
		options: append([]Option(nil), options...), onSelect: onSelect, onCancel: onCancel,
		search: search, rowTarget: map[int]int{},
	}
	picker.refilter()
	return picker
}

func (p *OptionPicker) Invalidate() {}

// Filter returns the current search query.
func (p *OptionPicker) Filter() string { return p.filter }

// Options returns the options still visible after filtering.
func (p *OptionPicker) Options() []Option {
	result := make([]Option, 0, len(p.filtered))
	for _, index := range p.filtered {
		result = append(result, p.options[index])
	}
	return result
}

func (p *OptionPicker) refilter() {
	if !p.search || strings.TrimSpace(p.filter) == "" {
		p.filtered = make([]int, len(p.options))
		for index := range p.options {
			p.filtered[index] = index
		}
	} else {
		indices := make([]int, len(p.options))
		for index := range p.options {
			indices[index] = index
		}
		p.filtered = FuzzyFilter(indices, p.filter, func(index int) string {
			option := p.options[index]
			return option.Label + " " + option.Description + " " + option.Value
		})
		if p.filtered == nil {
			p.filtered = []int{}
		}
	}
	p.selected = min(p.selected, max(0, len(p.filtered)-1))
}

// printableSearchInput keeps the search query to visible characters, so key
// escape sequences and control keys never leak into the filter.
func printableSearchInput(data string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, data)
}

func (p *OptionPicker) HandleInput(data string) {
	if MatchesKey(data, "escape") || MatchesKey(data, "ctrl+c") {
		if p.onCancel != nil {
			p.onCancel()
		}
		return
	}
	if p.search {
		if MatchesKey(data, "backspace") {
			if p.filter != "" {
				_, size := utf8.DecodeLastRuneInString(p.filter)
				p.filter = p.filter[:len(p.filter)-size]
				p.refilter()
			}
			return
		}
		if text := printableSearchInput(data); text != "" {
			p.filter += text
			p.refilter()
			return
		}
	}
	if len(p.filtered) == 0 {
		return
	}
	if MatchesKey(data, "up") {
		p.selected = (p.selected + len(p.filtered) - 1) % len(p.filtered)
		return
	}
	if MatchesKey(data, "down") {
		p.selected = (p.selected + 1) % len(p.filtered)
		return
	}
	if MatchesKey(data, "enter") && p.onSelect != nil {
		p.onSelect(p.options[p.filtered[p.selected]].Value)
	}
}

func (p *OptionPicker) HandleMouse(event MouseEvent) *MouseResult {
	if event.Type != MouseClick || event.Button != MouseLeft {
		return nil
	}
	index, ok := p.rowTarget[event.Y]
	if !ok || index >= len(p.filtered) {
		return nil
	}
	p.selected = index
	if p.onSelect != nil {
		p.onSelect(p.options[p.filtered[index]].Value)
	}
	render := true
	return &MouseResult{Handled: true, Render: &render}
}

func (p *OptionPicker) Render(_ int) []string {
	theme := CurrentTheme()
	const visible = 12
	start := max(0, min(p.selected-visible/2, len(p.filtered)-visible))
	end := min(len(p.filtered), start+visible)
	lines := make([]string, 0, end-start+2)
	p.rowTarget = map[int]int{}
	if p.search {
		// No "Search:" label: ">" mirrors the selection arrow so the query text
		// lines up in the same column as the option names below it.
		lines = append(lines, "> "+theme.FG("text", p.filter)+theme.FG("accent", "█"))
	}
	if len(p.filtered) == 0 {
		lines = append(lines, theme.FG("muted", "  no matches"))
		return lines
	}
	for index := start; index < end; index++ {
		p.rowTarget[len(lines)] = index
		option := p.options[p.filtered[index]]
		selected := index == p.selected
		marker := "  "
		label := theme.FG("text", option.Label)
		if selected {
			marker = theme.FG("accent", "→ ")
			label = theme.FG("accent", option.Label)
		}
		description := ""
		if strings.TrimSpace(option.Description) != "" {
			description = theme.FG("muted", "  "+option.Description)
		}
		lines = append(lines, marker+label+description)
	}
	if len(p.filtered) > visible {
		lines = append(lines, theme.FG("dim", fmt.Sprintf("  %d/%d", p.selected+1, len(p.filtered))))
	}
	return lines
}
