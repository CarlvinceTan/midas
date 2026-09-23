package tui

import (
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestThinkingPickerRespectsModelLevels(t *testing.T) {
	unsupported := (*string)(nil)
	model := ai.Model{Reasoning: true, ThinkingLevelMap: map[ai.ThinkingLevel]*string{ai.ThinkingMinimal: unsupported}}
	levels := ThinkingLevels(model)
	for _, level := range levels {
		if level == ai.ThinkingMinimal {
			t.Fatal("unsupported level was included")
		}
	}
	if got := ThinkingLevels(ai.Model{}); len(got) != 1 || got[0] != ai.ThinkingOff {
		t.Fatalf("non-reasoning levels = %#v", got)
	}
}

func TestNextThinkingLevelCyclesSupportedModelLevels(t *testing.T) {
	model := ai.Model{
		Reasoning: true,
		ThinkingLevelMap: map[ai.ThinkingLevel]*string{
			ai.ThinkingMinimal: nil,
			ai.ThinkingHigh:    nil,
		},
	}

	if got := NextThinkingLevel(model, ai.ThinkingOff); got != ai.ThinkingLow {
		t.Fatalf("next after off = %q, want low", got)
	}
	if got := NextThinkingLevel(model, ai.ThinkingMax); got != ai.ThinkingOff {
		t.Fatalf("next after max = %q, want off", got)
	}
	if got := NextThinkingLevel(model, ai.ThinkingMinimal); got != ai.ThinkingOff {
		t.Fatalf("next after unsupported current = %q, want off", got)
	}
	if got := NextThinkingLevel(ai.Model{}, ai.ThinkingMedium); got != ai.ThinkingOff {
		t.Fatalf("non-reasoning next = %q, want off", got)
	}
}

func TestThinkingPickerNavigatesAndRenders(t *testing.T) {
	var selected ai.ThinkingLevel
	picker := NewThinkingPicker(ThinkingPickerOptions{
		Model: ai.Model{Reasoning: true}, Current: ai.ThinkingMedium, Theme: NewOneDarkTheme(ColorTrue),
		OnSelect: func(level ai.ThinkingLevel) { selected = level },
	})
	picker.HandleInput("\x1b[B")
	picker.HandleInput("\r")
	if selected != ai.ThinkingHigh {
		t.Fatalf("selected = %q", selected)
	}
	lines := picker.Render(42)
	if len(lines) != 10 {
		t.Fatalf("height = %d", len(lines))
	}
	plain := tuitext.StripTerminalSequences(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "╭─ Thinking ") || !strings.Contains(plain, "> █") || !strings.Contains(plain, "→ high") || !strings.Contains(plain, "(~16k tokens)") {
		t.Fatalf("render:\n%s", plain)
	}
	for index, line := range lines {
		if width := tuitext.VisibleWidth(line); width != 42 {
			t.Fatalf("line %d width = %d: %q", index, width, line)
		}
	}
}
