package tui

import (
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestModelPickerFiltersNavigatesAndSelects(t *testing.T) {
	models := []ai.Model{
		{Provider: "openai", ID: "gpt-5", Name: "GPT 5"},
		{Provider: "anthropic", ID: "claude-sonnet", Name: "Claude Sonnet"},
		{Provider: "google", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
	}
	var selected ai.Model
	cancelled := false
	picker := NewModelPicker(ModelPickerOptions{
		Models: models, Theme: NewOneDarkTheme(ColorTrue),
		OnSelect: func(model ai.Model) { selected = model },
		OnCancel: func() { cancelled = true },
	})
	picker.HandleInput("g")
	picker.HandleInput("e")
	picker.HandleInput("m")
	if got := picker.Models(); len(got) != 1 || got[0].Provider != "google" {
		t.Fatalf("filtered = %#v", got)
	}
	picker.HandleInput("\r")
	if selected.Provider != "google" {
		t.Fatalf("selected = %#v", selected)
	}
	picker.HandleInput("\x1b")
	if !cancelled {
		t.Fatal("escape did not cancel")
	}
}

func TestModelPickerRenderingKeepsStableHeightAndWidth(t *testing.T) {
	picker := NewModelPicker(ModelPickerOptions{
		Models: []ai.Model{{Provider: "openai", ID: "gpt-5.1-codex", Name: "GPT 5.1 Codex"}},
		Theme:  NewOneDarkTheme(ColorTrue),
	})
	lines := picker.Render(42)
	if len(lines) != 15 {
		t.Fatalf("height = %d", len(lines))
	}
	for index, line := range lines {
		if width := tuitext.VisibleWidth(line); width != 42 {
			t.Fatalf("line %d width = %d: %q", index, width, line)
		}
	}
	plain := tuitext.StripTerminalSequences(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "Models") || !strings.Contains(plain, "→ GPT 5.1 Codex  OpenAI") {
		t.Fatalf("render = %s", plain)
	}
}

func TestModelDisplayPartsHumanizesFallbackIdentifiers(t *testing.T) {
	name, provider := ModelDisplayParts(ai.Model{Provider: "openai", ID: "gpt-5_1-codex"})
	if name != "GPT 5 1 Codex" || provider != "OpenAI" {
		t.Fatalf("display = %q / %q", name, provider)
	}
}
