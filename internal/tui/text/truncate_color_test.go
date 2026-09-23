package text

import (
	"strings"
	"testing"
)

func TestTruncationEllipsisInheritsActiveForeground(t *testing.T) {
	tests := []struct {
		name, input, colour string
		width               int
	}{
		{name: "basic", input: "\x1b[31mred text here\x1b[0m", colour: "\x1b[31m", width: 5},
		{name: "256 colour", input: "\x1b[1;38;5;208mcolour 256 value\x1b[22m", colour: "\x1b[38;5;208m", width: 6},
		{name: "true colour", input: "\x1b[38;2;12;34;56mcustom colour value\x1b[39m", colour: "\x1b[38;2;12;34;56m", width: 7},
		{name: "latest colour", input: "\x1b[31mred \x1b[34mblue tail\x1b[0m", colour: "\x1b[34m", width: 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := TruncateToWidth(test.input, test.width, "…", false)
			if !strings.Contains(got, test.colour+"…\x1b[0m") {
				t.Fatalf("ellipsis did not inherit %q: %q", test.colour, got)
			}
			if VisibleWidth(got) != test.width {
				t.Fatalf("visible width = %d, want %d", VisibleWidth(got), test.width)
			}
		})
	}
}

func TestEllipsisOnlyResultKeepsSourceForeground(t *testing.T) {
	got := TruncateToWidth("\x1b[35mlong value\x1b[39m", 1, "…", false)
	if !strings.Contains(got, "\x1b[35m…\x1b[0m") {
		t.Fatalf("ellipsis-only result lost source colour: %q", got)
	}
}

func TestTruncationEllipsisDoesNotExtendHyperlinkOrStylePadding(t *testing.T) {
	input := "\x1b[36m\x1b]8;;https://example.com\x07linked content is long\x1b]8;;\x07\x1b[39m"
	got := TruncateToWidth(input, 10, "…", true)
	close := strings.Index(got, "\x1b]8;;\x07")
	ellipsis := strings.Index(got, "\x1b[36m…\x1b[0m")
	if close < 0 || ellipsis <= close {
		t.Fatalf("hyperlink was not closed before coloured ellipsis: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("truncated value did not reset after ellipsis: %q", got)
	}

	padded := TruncateToWidth("\x1b[32m界界界\x1b[39m", 4, "…", true)
	if !strings.HasSuffix(padded, "\x1b[0m ") || VisibleWidth(padded) != 4 {
		t.Fatalf("padding inherited foreground or changed width: %q", padded)
	}
}

func TestTruncationEllipsisUsesDefaultAfterForegroundCloses(t *testing.T) {
	got := TruncateToWidth("\x1b[31mred\x1b[39m plain tail", 10, "…", false)
	if strings.Contains(got, "\x1b[31m…") {
		t.Fatalf("ellipsis reused a closed colour: %q", got)
	}
}
