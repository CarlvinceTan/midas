package tui

import (
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestRenderMarkdownCoreBlocksAndInlineStyles(t *testing.T) {
	source := "# Heading\n\n- item with `code` and **bold**\n> quoted\n---\n```go\nfmt.Println(\"hi\")\n```\n[OpenAI](https://openai.com)"
	lines := RenderMarkdown(source, 48, NewOneDarkTheme(ColorTrue))
	plain := tuitext.StripTerminalSequences(strings.Join(lines, "\n"))
	for _, want := range []string{"Heading", "• item with code and bold", "│ quoted", "────", "┌─ go", "│ fmt.Println", "└─", "OpenAI (https://openai.com)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("markdown missing %q:\n%s", want, plain)
		}
	}
}

func TestRenderMarkdownStylesBoldYellowAndLinksBlue(t *testing.T) {
	theme := NewOneDarkTheme(ColorTrue)
	line := RenderMarkdown("plain **bold** and [label](https://example.com)", 80, theme)[0]
	boldSpan := theme.FG("mdBold", bold("bold"))
	if !strings.Contains(line, boldSpan) {
		t.Fatalf("bold span = %q, want it to contain %q", line, boldSpan)
	}
	if !strings.Contains(line, theme.FG("mdLink", "label")) {
		t.Fatalf("link label = %q, want blue label", line)
	}
	if got := tuitext.StripTerminalSequences(line); got != "plain bold and label (https://example.com)" {
		t.Fatalf("plain text = %q", got)
	}
}

func TestRenderMarkdownRestoresBlockColorAfterInlineSpans(t *testing.T) {
	theme := NewOneDarkTheme(ColorTrue)
	lines := RenderMarkdown("# heading `code` tail\n> quoted `code` tail", 80, theme)
	if len(lines) != 2 {
		t.Fatalf("lines = %#v", lines)
	}
	code := theme.FG("mdCode", "code")
	for index, prefix := range []string{theme.FGPrefix("mdHeading"), theme.FGPrefix("mdQuote")} {
		if !strings.Contains(lines[index], code+prefix+" tail") {
			t.Fatalf("block color lost after inline span: %q", lines[index])
		}
	}
}

func TestRenderMarkdownUsesHangingIndentAndClosesStreamingFence(t *testing.T) {
	lines := RenderMarkdown("1. a long list item that wraps onto another line\n```\ncode", 20, NewOneDarkTheme(ColorTrue))
	plain := strings.Split(tuitext.StripTerminalSequences(strings.Join(lines, "\n")), "\n")
	if len(plain) < 5 || !strings.HasPrefix(plain[0], "1. ") || !strings.HasPrefix(plain[1], "   ") || plain[len(plain)-1] != "└─" {
		t.Fatalf("markdown lines = %#v", plain)
	}
}
