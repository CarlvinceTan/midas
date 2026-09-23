package tui

import (
	"strings"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

// RenderMarkdown renders the deliberately small Markdown surface needed by
// coding-agent replies without introducing a browser or a large parser.
func RenderMarkdown(source string, width int, theme *Theme) []string {
	width = max(1, width)
	if theme == nil {
		theme = CurrentTheme()
	}
	lines := make([]string, 0)
	inCode := false
	for _, raw := range strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			language := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			if !inCode {
				title := ""
				if language != "" {
					title = " " + language
				}
				lines = append(lines, theme.FG("mdCodeBlockBorder", "┌─")+theme.FG("dim", title))
				inCode = true
			} else {
				lines = append(lines, theme.FG("mdCodeBlockBorder", "└─"))
				inCode = false
			}
			continue
		}
		if inCode {
			lines = append(lines, prefixedWrapped(theme.FG("mdCodeBlockBorder", "│ "), theme.FG("mdCodeBlock", raw), width)...)
			continue
		}
		if trimmed == "" {
			lines = append(lines, "")
			continue
		}
		if level, text := markdownHeading(trimmed); level > 0 {
			styled := theme.FG("mdHeading", bold(styleMarkdownInline(text, theme, theme.FGPrefix("mdHeading"))))
			lines = append(lines, tuitext.WrapTextWithAnsi(styled, width)...)
			continue
		}
		if markdownRule(trimmed) {
			lines = append(lines, theme.FG("mdHr", strings.Repeat("─", width)))
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			lines = append(lines, prefixedWrapped(theme.FG("mdQuoteBorder", "│ "), theme.FG("mdQuote", styleMarkdownInline(text, theme, theme.FGPrefix("mdQuote"))), width)...)
			continue
		}
		if prefix, text, ok := markdownListItem(trimmed); ok {
			lines = append(lines, prefixedWrapped(theme.FG("mdListBullet", prefix), styleMarkdownInline(text, theme, ""), width)...)
			continue
		}
		lines = append(lines, tuitext.WrapTextWithAnsi(styleMarkdownInline(raw, theme, ""), width)...)
	}
	if inCode {
		lines = append(lines, theme.FG("mdCodeBlockBorder", "└─"))
	}
	return lines
}

func markdownHeading(line string) (int, string) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, line
	}
	return level, strings.TrimSpace(line[level:])
}

func markdownRule(line string) bool {
	compact := strings.ReplaceAll(line, " ", "")
	if len(compact) < 3 {
		return false
	}
	return strings.Trim(compact, "-") == "" || strings.Trim(compact, "*") == "" || strings.Trim(compact, "_") == ""
}

func markdownListItem(line string) (string, string, bool) {
	if len(line) >= 2 && strings.ContainsRune("-*+", rune(line[0])) && line[1] == ' ' {
		return "• ", strings.TrimSpace(line[2:]), true
	}
	index := 0
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	if index > 0 && index+1 < len(line) && line[index] == '.' && line[index+1] == ' ' {
		return line[:index+1] + " ", strings.TrimSpace(line[index+2:]), true
	}
	return "", line, false
}

func prefixedWrapped(prefix, content string, width int) []string {
	prefixWidth := tuitext.VisibleWidth(prefix)
	wrapped := tuitext.WrapTextWithAnsi(content, max(1, width-prefixWidth))
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	indent := strings.Repeat(" ", prefixWidth)
	for index := range wrapped {
		if index == 0 {
			wrapped[index] = prefix + wrapped[index]
		} else {
			wrapped[index] = indent + wrapped[index]
		}
	}
	return wrapped
}

// styleMarkdownInline styles inline Markdown spans. restore is the ANSI prefix
// of the enclosing block style (for example a heading color), re-emitted after
// every span so an inline color reset cannot drop the surrounding color.
func styleMarkdownInline(value string, theme *Theme, restore string) string {
	var result strings.Builder
	for value != "" {
		switch {
		case strings.HasPrefix(value, "**"):
			if end := strings.Index(value[2:], "**"); end >= 0 {
				result.WriteString(theme.FG("mdBold", bold(value[2:2+end])))
				result.WriteString(restore)
				value = value[2+end+2:]
				continue
			}
		case strings.HasPrefix(value, "__"):
			if end := strings.Index(value[2:], "__"); end >= 0 {
				result.WriteString(theme.FG("mdBold", bold(value[2:2+end])))
				result.WriteString(restore)
				value = value[2+end+2:]
				continue
			}
		case value[0] == '`':
			if end := strings.IndexByte(value[1:], '`'); end >= 0 {
				result.WriteString(theme.FG("mdCode", value[1:1+end]))
				result.WriteString(restore)
				value = value[1+end+1:]
				continue
			}
		case value[0] == '[':
			closeLabel := strings.IndexByte(value, ']')
			if closeLabel > 0 && closeLabel+1 < len(value) && value[closeLabel+1] == '(' {
				if closeURL := strings.IndexByte(value[closeLabel+2:], ')'); closeURL >= 0 {
					urlEnd := closeLabel + 2 + closeURL
					result.WriteString(theme.FG("mdLink", value[1:closeLabel]))
					result.WriteString(restore)
					result.WriteString(theme.FG("mdLinkUrl", " ("+value[closeLabel+2:urlEnd]+")"))
					result.WriteString(restore)
					value = value[urlEnd+1:]
					continue
				}
			}
		}
		_, size := firstMarkdownRune(value)
		result.WriteString(value[:size])
		value = value[size:]
	}
	return result.String()
}

func firstMarkdownRune(value string) (rune, int) {
	// DecodeRuneInString reports size 1 for an invalid byte, so callers can
	// always slice value[:size] even when a provider delta split a rune.
	return utf8.DecodeRuneInString(value)
}
