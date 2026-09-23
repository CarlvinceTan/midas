package tui

import (
	"fmt"
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestOptionPickerMatchesLegacySpacing(t *testing.T) {
	picker := NewOptionPicker([]Option{{Label: "API key", Description: "paste a provider key", Value: "api"}, {Label: "OAuth", Value: "oauth"}}, nil, nil)
	plain := tuitext.StripTerminalSequences(strings.Join(picker.Render(60), "\n"))
	if !strings.HasPrefix(plain, "→ API key  paste a provider key\n  OAuth") {
		t.Fatalf("render = %q", plain)
	}
}

func TestOptionPickerAlignsPositionWithOptionLabels(t *testing.T) {
	options := make([]Option, 13)
	for index := range options {
		options[index] = Option{Label: fmt.Sprintf("Option %d", index+1)}
	}
	picker := NewOptionPicker(options, nil, nil)
	plain := tuitext.StripTerminalSequences(strings.Join(picker.Render(60), "\n"))
	if !strings.HasSuffix(plain, "\n  1/13") {
		t.Fatalf("position is not aligned with option labels: %q", plain)
	}
}

func TestSearchOptionPickerFiltersWithSearchRow(t *testing.T) {
	var selected string
	picker := NewSearchOptionPicker([]Option{
		{Label: "Ant Ling", Description: "OpenAI-compatible API", Value: "ant-ling"},
		{Label: "Anthropic", Description: "Claude API", Value: "anthropic"},
		{Label: "DeepSeek", Description: "DeepSeek API", Value: "deepseek"},
	}, func(value string) { selected = value }, func() {})
	rendered := stripANSI(strings.Join(picker.Render(60), "\n"))
	if !strings.Contains(rendered, "> ") {
		t.Fatalf("search row missing:\n%s", rendered)
	}
	picker.HandleInput("deep")
	rendered = stripANSI(strings.Join(picker.Render(60), "\n"))
	if strings.Contains(rendered, "Anthropic") || !strings.Contains(rendered, "DeepSeek") {
		t.Fatalf("filter did not narrow the list:\n%s", rendered)
	}
	if !strings.Contains(rendered, "> deep") {
		t.Fatalf("query not shown in the search row:\n%s", rendered)
	}
	picker.HandleInput("\r")
	if selected != "deepseek" {
		t.Fatalf("selected = %q, want deepseek", selected)
	}
}

func TestSearchOptionPickerBackspaceRestoresOptionsAndEscapeCancels(t *testing.T) {
	cancelled := false
	picker := NewSearchOptionPicker([]Option{
		{Label: "Alpha", Value: "alpha"},
		{Label: "Beta", Value: "beta"},
	}, func(string) {}, func() { cancelled = true })
	picker.HandleInput("zz")
	if len(picker.Options()) != 0 {
		t.Fatalf("expected no matches, got %v", picker.Options())
	}
	picker.HandleInput("\x7f")
	picker.HandleInput("\x7f")
	if len(picker.Options()) != 2 {
		t.Fatalf("options after clearing the query = %v", picker.Options())
	}
	picker.HandleInput("\x1b")
	if !cancelled {
		t.Fatal("escape should cancel the picker")
	}
}

func TestOptionPickerWithoutSearchIgnoresTypedText(t *testing.T) {
	picker := NewOptionPicker([]Option{{Label: "Alpha", Value: "alpha"}}, func(string) {}, func() {})
	picker.HandleInput("b")
	if len(picker.Options()) != 1 {
		t.Fatalf("plain pickers should not filter: %v", picker.Options())
	}
}
