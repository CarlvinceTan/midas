package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/storage"
	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestSessionPickerNavigatesSelectsAndCreates(t *testing.T) {
	now := time.UnixMilli(1_700_000_120_000)
	items := []storage.Session{
		{ID: "one", CWD: "/code/midas", Title: "First", UpdatedAt: now.Add(-2 * time.Minute).UnixMilli()},
		{ID: "two", CWD: "/code/midas", Title: "Second", UpdatedAt: now.Add(-time.Hour).UnixMilli()},
	}
	var selected string
	created, cancelled := 0, 0
	picker := NewSessionPicker(SessionPickerOptions{
		Sessions: items, CurrentID: "one", Now: func() time.Time { return now }, Theme: NewOneDarkTheme(ColorTrue),
		OnNew: func() { created++ }, OnSelect: func(session storage.Session) { selected = session.ID }, OnCancel: func() { cancelled++ },
	})
	if picker.Selected() != 1 {
		t.Fatalf("current selection = %d", picker.Selected())
	}
	picker.HandleInput("\x1b[B")
	picker.HandleInput("\r")
	if selected != "two" {
		t.Fatalf("selected = %q", selected)
	}
	picker.HandleInput("\x1b[B")
	picker.HandleInput("\r")
	if created != 1 {
		t.Fatalf("created = %d", created)
	}
	picker.HandleInput("\x1b")
	if cancelled != 1 {
		t.Fatalf("cancelled = %d", cancelled)
	}
}

func TestSessionPickerRendersStableMidasOnlyList(t *testing.T) {
	now := time.UnixMilli(1_700_000_120_000)
	picker := NewSessionPicker(SessionPickerOptions{
		Sessions: []storage.Session{{ID: "one", CWD: "/code/midas", Title: strings.Repeat("Long", 30), UpdatedAt: now.Add(-2 * time.Minute).UnixMilli()}},
		Now:      func() time.Time { return now }, Theme: NewOneDarkTheme(ColorTrue),
	})
	lines := picker.Render(38)
	if len(lines) != 11 {
		t.Fatalf("height = %d", len(lines))
	}
	plain := tuitext.StripTerminalSequences(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "New session") || !strings.Contains(plain, "Midas · 2m ago") || !strings.Contains(plain, "…") {
		t.Fatalf("render:\n%s", plain)
	}
}
