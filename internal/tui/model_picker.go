package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

type ModelPickerOptions struct {
	Models     []ai.Model
	OnSelect   func(ai.Model)
	OnCancel   func()
	Borderless bool
	Theme      *Theme
}

type ModelPicker struct {
	FocusState
	models     []ai.Model
	filtered   []ai.Model
	filter     string
	selected   int
	onSelect   func(ai.Model)
	onCancel   func()
	borderless bool
	title      string
	theme      *Theme
}

func NewModelPicker(options ModelPickerOptions) *ModelPicker {
	models := append([]ai.Model(nil), options.Models...)
	return &ModelPicker{models: models, filtered: append([]ai.Model(nil), models...), onSelect: options.OnSelect, onCancel: options.OnCancel, borderless: options.Borderless, theme: options.Theme}
}

// SetTitle is how Dialog names this picker's frame; a borderless picker ignores it
// because its frame belongs to the panel around it.
func (p *ModelPicker) SetTitle(title string) {
	if strings.TrimSpace(title) == "" {
		return
	}
	p.title = title
}

func (p *ModelPicker) Invalidate()        {}
func (p *ModelPicker) Filter() string     { return p.filter }
func (p *ModelPicker) Selected() int      { return p.selected }
func (p *ModelPicker) Models() []ai.Model { return append([]ai.Model(nil), p.filtered...) }

func (p *ModelPicker) refilter() {
	p.filtered = FuzzyFilter(p.models, p.filter, func(model ai.Model) string {
		return model.Provider + " " + model.Provider + " " + model.Name + " " + model.ID
	})
	p.selected = min(p.selected, max(0, len(p.filtered)-1))
}

func (p *ModelPicker) HandleInput(data string) {
	if MatchesKey(data, "escape") || MatchesKey(data, "ctrl+c") {
		if p.onCancel != nil {
			p.onCancel()
		}
		return
	}
	if MatchesKey(data, "enter") {
		if p.selected < len(p.filtered) && p.onSelect != nil {
			p.onSelect(p.filtered[p.selected])
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

// titleOrDefault is the frame title: what Dialog set, or "Models".
func (p *ModelPicker) titleOrDefault() string {
	if strings.TrimSpace(p.title) != "" {
		return strings.TrimSpace(p.title)
	}
	return "Models"
}

func (p *ModelPicker) Render(width int) []string {
	theme := p.theme
	if theme == nil {
		theme = CurrentTheme()
	}
	framed := !p.borderless
	total := max(1, width)
	if framed {
		total = max(3, width)
	}
	inner := total
	if framed {
		inner = total - 2
	}
	contentWidth := inner
	if framed {
		contentWidth = inner - 1
	}
	border := func(value string) string { return theme.FG("borderAccent", value) }
	row := func(line string) string {
		content := tuitext.TruncateToWidth(line, contentWidth, "…", true)
		content += strings.Repeat(" ", max(0, contentWidth-tuitext.VisibleWidth(content)))
		if framed {
			return border("│") + " " + content + border("│")
		}
		return content
	}
	rows := []string{row("> " + theme.FG("text", p.filter) + theme.FG("accent", "█"))}
	const visible = 12
	start := max(0, min(p.selected-visible/2, len(p.filtered)-visible))
	for index := 0; index < visible; index++ {
		position := start + index
		if position >= len(p.filtered) {
			rows = append(rows, row(""))
			continue
		}
		model := p.filtered[position]
		selected := position == p.selected
		marker := "  "
		if selected {
			marker = theme.FG("accent", "→ ")
		}
		name, provider := ModelDisplayParts(model)
		if selected {
			name = theme.FG("accent", name)
		} else {
			name = theme.FG("text", name)
		}
		if provider != "" {
			provider = theme.FG("muted", "  "+provider)
		}
		rows = append(rows, row(marker+name+provider))
	}
	if !framed {
		return rows
	}
	title := ""
	if total >= 12 {
		title = " " + p.titleOrDefault() + " "
	}
	top := border("╭─") + theme.FG("borderAccent", title) + border(strings.Repeat("─", max(0, total-3-utf8.RuneCountInString(title)))+"╮")
	bottom := border("╰" + strings.Repeat("─", inner) + "╯")
	return append(append([]string{top}, rows...), bottom)
}

func ModelDisplayParts(model ai.Model) (name, provider string) {
	name = model.Name
	if name == "" || name == model.ID {
		name = humanizeIdentifier(model.ID)
	}
	provider = humanizeIdentifier(model.Provider)
	return name, provider
}

func humanizeIdentifier(value string) string {
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(value))
	for index, word := range words {
		lower := strings.ToLower(word)
		switch lower {
		case "openai":
			words[index] = "OpenAI"
		case "github":
			words[index] = "GitHub"
		case "gpt":
			words[index] = "GPT"
		case "ai":
			words[index] = "AI"
		default:
			if word == lower && word != "" {
				r, size := utf8.DecodeRuneInString(word)
				words[index] = string(unicode.ToUpper(r)) + word[size:]
			}
		}
	}
	return strings.Join(words, " ")
}
