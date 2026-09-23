package tui

import (
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestPaddingDefaultsToOneAndClamps(t *testing.T) {
	t.Cleanup(func() { SetPadding(1) })
	if got := Padding(); got != 1 {
		t.Fatalf("default padding = %d, want 1", got)
	}
	if SetPadding(1) {
		t.Fatal("an unchanged padding reported a change")
	}
	if !SetPadding(3) || Padding() != 3 {
		t.Fatalf("padding = %d, want 3", Padding())
	}
	if !SetPadding(maxPadding+5) || Padding() != maxPadding {
		t.Fatalf("padding = %d, want %d", Padding(), maxPadding)
	}
	if !SetPadding(-4) || Padding() != 0 {
		t.Fatalf("padding = %d, want 0", Padding())
	}
}

func TestRowGeometryFollowsPaddingAndNeverOverflows(t *testing.T) {
	t.Cleanup(func() { SetPadding(1) })
	for _, padding := range []int{0, 1, 2, 4} {
		SetPadding(padding)
		margin, inner := chatRowGeometry(40)
		if margin != padding || inner != 40-padding*2 {
			t.Fatalf("padding %d geometry = %d/%d", padding, margin, inner)
		}
		// A narrow terminal drops the gutter instead of the content.
		narrowMargin, narrowInner := chatRowGeometry(3)
		if narrowMargin+narrowInner > 3 || narrowInner < 1 {
			t.Fatalf("padding %d overflowed a narrow row: %d/%d", padding, narrowMargin, narrowInner)
		}
	}
	SetPadding(0)
	if margin, inner := chatRowGeometry(40); margin != 0 || inner != 40 {
		t.Fatalf("zero padding geometry = %d/%d", margin, inner)
	}
}

func TestViewportPaddingInsertsBlankBorderRows(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, SessionTitle: "Padded Midas", CWD: "/work/midas", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetPadding(2)
	t.Cleanup(func() { chat.SetPadding(1) })
	viewport := NewChatViewport(chat)
	frame := RenderLayoutFrame(viewport.Root, 60, 16, nil)

	for _, row := range []int{0, 1, 14, 15} {
		if got := strings.TrimSpace(stripANSI(frame.Lines[row])); got != "" {
			t.Fatalf("border row %d is not blank: %q", row, got)
		}
	}
	title := stripANSI(frame.Lines[2])
	if !strings.HasPrefix(title, "  Padded Midas") {
		t.Fatalf("title row is not inset by the padding: %q", title)
	}
	divider := stripANSI(frame.Lines[4])
	if !strings.HasPrefix(divider, "  ") || !strings.HasPrefix(strings.TrimSpace(divider), "─") {
		t.Fatalf("divider row is not inset by the padding: %q", divider)
	}
	if got, want := tuitext.VisibleWidth(strings.TrimSpace(divider)), 56; got != want {
		t.Fatalf("divider width = %d, want %d", got, want)
	}
	// The rounded editor frame takes the same gutter as every other row.
	frameLine := stripANSI(frame.Lines[len(frame.Lines)-Padding()-2])
	if !strings.HasPrefix(frameLine, strings.Repeat(" ", Padding())+"╰") {
		t.Fatalf("editor frame did not close inside the gutter: %q", frameLine)
	}
}

func TestViewportWithoutPaddingStartsAtTheFirstRow(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, SessionTitle: "Tight Midas", CWD: "/work/midas"})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetPadding(0)
	t.Cleanup(func() { chat.SetPadding(1) })
	viewport := NewChatViewport(chat)
	frame := RenderLayoutFrame(viewport.Root, 60, 16, nil)
	if got := stripANSI(frame.Lines[0]); !strings.HasPrefix(got, "Tight Midas") {
		t.Fatalf("title row with zero padding = %q", got)
	}
	if got := stripANSI(frame.Lines[len(frame.Lines)-1]); !strings.HasPrefix(got, "Not set • off") {
		t.Fatalf("footer row with zero padding = %q", got)
	}
}

func TestViewportPaddingYieldsToShortTerminals(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, SessionTitle: "Short", CWD: "/work/midas"})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetPadding(maxPadding)
	t.Cleanup(func() { chat.SetPadding(1) })
	viewport := NewChatViewport(chat)
	for _, height := range []int{1, 4, 8} {
		frame := RenderLayoutFrame(viewport.Root, 24, height, nil)
		if len(frame.Lines) != height {
			t.Fatalf("height %d produced %d lines", height, len(frame.Lines))
		}
		for _, line := range frame.Lines {
			if got := tuitext.VisibleWidth(line); got > 24 {
				t.Fatalf("height %d row overflowed: %d", height, got)
			}
		}
	}
}
