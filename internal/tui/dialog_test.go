package tui

import (
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// TestDialogNeverStacksFrames is the regression test for a frame inside a frame:
// a picker that draws its own border is given the title instead of being wrapped,
// while a frameless component is wrapped once.
func TestDialogNeverStacksFrames(t *testing.T) {
	countFrames := func(lines []string) int {
		count := 0
		for _, line := range lines {
			if strings.Contains(line, "╭") {
				count++
			}
		}
		return count
	}
	thinking := NewThinkingPicker(ThinkingPickerOptions{
		Model:    ai.Model{ID: "gpt-5.6-sol", Provider: "openai", Reasoning: true},
		Current:  ai.ThinkingHigh,
		OnSelect: func(ai.ThinkingLevel) {},
	})
	dialog := Dialog("Agents > Main > Thinking", thinking)
	lines := dialog.Render(60)
	if frames := countFrames(lines); frames != 1 {
		t.Fatalf("thinking dialog frames = %d, want 1: %#v", frames, lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Agents > Main > Thinking") {
		t.Fatalf("the dialog title is missing: %#v", lines)
	}

	model := NewModelPicker(ModelPickerOptions{Models: []ai.Model{{ID: "gpt-5.6-sol", Provider: "openai"}}})
	modelDialog := Dialog("Model", model)
	modelLines := modelDialog.Render(60)
	if frames := countFrames(modelLines); frames != 1 {
		t.Fatalf("model dialog frames = %d, want 1: %#v", frames, modelLines)
	}
	if !strings.Contains(strings.Join(modelLines, "\n"), "Model") {
		t.Fatalf("the model dialog title is missing: %#v", modelLines)
	}

	// A component without its own frame is wrapped in exactly one panel.
	frameless := Dialog("Agents > Main > Model", NewSearchOptionPicker([]Option{{Label: "Main", Value: "main"}}, func(string) {}, func() {}))
	framelessLines := frameless.Render(60)
	if frames := countFrames(framelessLines); frames != 1 {
		t.Fatalf("wrapped dialog frames = %d, want 1: %#v", frames, framelessLines)
	}
	if got := tuitext.StripTerminalSequences(framelessLines[0]); !strings.Contains(got, "Agents > Main > Model") {
		t.Fatalf("wrapped dialog title = %q", got)
	}
}
