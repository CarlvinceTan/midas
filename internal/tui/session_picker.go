package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/pkg/storage"
	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

type SessionPickerOptions struct {
	Sessions  []storage.Session
	CurrentID string
	OnNew     func()
	OnSelect  func(storage.Session)
	OnCancel  func()
	Now       func() time.Time
	Theme     *Theme
}

type sessionPickerEntry struct {
	new     bool
	session storage.Session
}

// SessionPicker lists only native Midas storage. It never scans another
// agent's data directory.
type SessionPicker struct {
	FocusState
	entries   []sessionPickerEntry
	selected  int
	onNew     func()
	onSelect  func(storage.Session)
	onCancel  func()
	now       func() time.Time
	theme     *Theme
	rowTarget map[int]int
}

func NewSessionPicker(options SessionPickerOptions) *SessionPicker {
	entries := []sessionPickerEntry{{new: true}}
	selected := 0
	for _, session := range options.Sessions {
		entries = append(entries, sessionPickerEntry{session: session})
		if session.ID == options.CurrentID {
			selected = len(entries) - 1
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &SessionPicker{
		entries: entries, selected: selected, onNew: options.OnNew, onSelect: options.OnSelect,
		onCancel: options.OnCancel, now: now, theme: options.Theme, rowTarget: make(map[int]int),
	}
}

func (p *SessionPicker) Invalidate()   {}
func (p *SessionPicker) Selected() int { return p.selected }

func (p *SessionPicker) HandleInput(data string) {
	if MatchesKey(data, "escape") || MatchesKey(data, "ctrl+c") {
		if p.onCancel != nil {
			p.onCancel()
		}
		return
	}
	if MatchesKey(data, "up") {
		p.selected = (p.selected + len(p.entries) - 1) % len(p.entries)
		return
	}
	if MatchesKey(data, "down") {
		p.selected = (p.selected + 1) % len(p.entries)
		return
	}
	if MatchesKey(data, "enter") {
		p.activate()
	}
}

func (p *SessionPicker) HandleMouse(event MouseEvent) *MouseResult {
	if event.Type != MouseClick || event.Button != MouseLeft {
		return nil
	}
	index, ok := p.rowTarget[event.Y]
	if !ok {
		return nil
	}
	p.selected = index
	p.activate()
	render := true
	return &MouseResult{Handled: true, Render: &render}
}

func (p *SessionPicker) activate() {
	entry := p.entries[p.selected]
	if entry.new {
		if p.onNew != nil {
			p.onNew()
		}
		return
	}
	if p.onSelect != nil {
		p.onSelect(entry.session)
	}
}

func (p *SessionPicker) Render(width int) []string {
	width = max(1, width)
	theme := p.theme
	if theme == nil {
		theme = CurrentTheme()
	}
	const visible = 10
	start := max(0, min(p.selected-visible/2, len(p.entries)-visible))
	lines := make([]string, 0, visible+1)
	p.rowTarget = make(map[int]int)
	for row := 0; row < visible; row++ {
		index := start + row
		if index >= len(p.entries) {
			lines = append(lines, "")
			continue
		}
		p.rowTarget[row] = index
		entry := p.entries[index]
		selected := index == p.selected
		marker := "  "
		if selected {
			marker = theme.FG("accent", "→ ")
		}
		label, metadata := "New session", "Midas"
		if !entry.new {
			label = strings.TrimSpace(entry.session.Title)
			if label == "" {
				label = "New session"
			}
			parts := []string{projectLabel(entry.session.CWD)}
			if ago := relativeTime(p.now(), entry.session.UpdatedAt); ago != "" {
				parts = append(parts, ago)
			}
			metadata = strings.Join(parts, " · ")
		}
		if selected {
			label = theme.FG("accent", label)
		} else {
			label = theme.FG("text", label)
		}
		tail := theme.FG("muted", "  "+metadata)
		available := max(1, width-2)
		headWidth := max(0, available-tuitext.VisibleWidth(tail))
		head := ""
		if headWidth > 0 {
			head = tuitext.TruncateToWidth(label, headWidth, "…", false)
		}
		lines = append(lines, marker+head+tuitext.TruncateToWidth(tail, available, "…", false))
	}
	if len(p.entries) > visible {
		lines = append(lines, "  "+theme.FG("dim", fmt.Sprintf("%d/%d", p.selected+1, len(p.entries))))
	} else {
		lines = append(lines, "")
	}
	return lines
}

func projectLabel(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == string(filepath.Separator) {
		return "Midas"
	}
	label := filepath.Base(path)
	if label == "" || label == "." {
		return "Midas"
	}
	return capitalizePanelTitle(label)
}

func relativeTime(now time.Time, timestamp int64) string {
	if timestamp <= 0 {
		return ""
	}
	delta := now.Sub(time.UnixMilli(timestamp))
	if delta < 0 {
		delta = 0
	}
	seconds := int(delta.Round(time.Second) / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds ago", seconds)
	}
	minutes := int((delta + 30*time.Second) / time.Minute)
	if minutes < 60 {
		return fmt.Sprintf("%dm ago", minutes)
	}
	hours := int((delta + 30*time.Minute) / time.Hour)
	if hours < 24 {
		return fmt.Sprintf("%dh ago", hours)
	}
	days := int((delta + 12*time.Hour) / (24 * time.Hour))
	return fmt.Sprintf("%dd ago", days)
}
