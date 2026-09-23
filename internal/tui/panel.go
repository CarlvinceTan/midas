package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

// Titled is a component that draws its own frame and can be told the path it sits
// on. Dialog hands such a component the title instead of wrapping it, which is what
// keeps a dialog from ever being a frame inside a frame.
type Titled interface {
	SetTitle(title string)
}

// Dialog presents a component as a dialog titled with the path the user took, for
// example "Agents > Main > Thinking". A component that draws its own frame keeps
// it and is given the title; anything else is wrapped in a panel. Callers use this
// instead of NewPanel so nesting cannot happen by construction.
func Dialog(title string, child Component) Component {
	if titled, ok := child.(Titled); ok {
		titled.SetTitle(title)
		return child
	}
	return NewPanel(title, child)
}

// Panel draws the same titled rounded frame used by Midas command overlays.
type Panel struct {
	FocusState
	Title func() string
	Child Component
	Theme *Theme
}

func NewPanel(title string, child Component) *Panel {
	return &Panel{Title: func() string { return title }, Child: child}
}

func (p *Panel) Invalidate() {
	if p.Child != nil {
		p.Child.Invalidate()
	}
}

func (p *Panel) SetFocused(focused bool) {
	p.FocusState.SetFocused(focused)
	if child, ok := p.Child.(Focusable); ok {
		child.SetFocused(focused)
	}
}

func (p *Panel) HandleInput(data string) {
	if child, ok := p.Child.(InputHandler); ok {
		child.HandleInput(data)
	}
}

func (p *Panel) HandleMouse(event MouseEvent) *MouseResult {
	if p.Child == nil || event.X < 1 || event.Y < 1 {
		return nil
	}
	event.X--
	event.Y--
	event.Width = max(1, event.Width-2)
	event.Height = max(1, event.Height-2)
	result := DispatchMouseEvent(p.Child, event)
	if result == nil {
		return nil
	}
	// A panel body is content: it keeps its own press handling (a row click still
	// acts) and the viewport may anchor a text selection over it, so highlighting
	// works inside dock overlays too.
	result.Selectable = true
	return result
}

func (p *Panel) ChildComponents() []Component {
	if p.Child == nil {
		return nil
	}
	return []Component{p.Child}
}

func (p *Panel) Render(width int) []string {
	total := max(3, width)
	inner := total - 2
	theme := p.Theme
	if theme == nil {
		theme = CurrentTheme()
	}
	raw := ""
	if p.Title != nil {
		raw = capitalizePanelTitle(p.Title())
	}
	title := ""
	if total >= 12 {
		title = " " + raw + " "
	}
	border := func(value string) string { return theme.FG("borderAccent", value) }
	edge := func(value string) string {
		return tuitext.DecorationMarker + ContentStartMarker + ContentEndMarker + value
	}
	top := edge(border("╭─") + theme.FG("borderAccent", title) + border(strings.Repeat("─", max(0, total-3-utf8.RuneCountInString(title)))+"╮"))
	pad := 0
	if total >= 9 {
		pad = 1
	}
	contentWidth := max(1, inner-pad*2)
	gutter := strings.Repeat(" ", pad)
	rows := []string{}
	if p.Child != nil {
		for _, line := range p.Child.Render(contentWidth) {
			fitted := line
			if tuitext.VisibleWidth(fitted) > contentWidth {
				fitted = tuitext.TruncateToWidth(fitted, contentWidth, "…", true)
			}
			fill := strings.Repeat(" ", max(0, contentWidth-tuitext.VisibleWidth(fitted)))
			rows = append(rows, border("│")+gutter+ContentStartMarker+fitted+ContentEndMarker+fill+gutter+border("│"))
		}
	}
	bottom := edge(border("╰" + strings.Repeat("─", inner) + "╯"))
	return append(append([]string{top}, rows...), bottom)
}

func capitalizePanelTitle(value string) string {
	first, size := utf8.DecodeRuneInString(value)
	if first == utf8.RuneError && size == 0 {
		return value
	}
	return string(unicode.ToUpper(first)) + value[size:]
}
