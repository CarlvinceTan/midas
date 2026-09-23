package tui

import (
	"context"
	"strings"
	"time"
	"unicode"
)

const (
	// defaultTitleMaxWords caps a generated session title when the host does not
	// configure a limit in /settings.
	defaultTitleMaxWords = 8
	// maxTitleRunes keeps a title short enough for the header and the session
	// picker even when a helper model ignores its word budget.
	maxTitleRunes = 80
)

// TitleGenerator produces a session title from the request that opened the
// session. It is best-effort: an error leaves the previous title in place.
type TitleGenerator func(ctx context.Context, source string, maximum int) (string, error)

// SetTitleMaxWords changes the generated title's word budget.
func (c *Chat) SetTitleMaxWords(maximum int) {
	maximum = max(1, min(20, maximum))
	c.mu.Lock()
	c.titleMaxWords = maximum
	c.mu.Unlock()
	c.repaint()
}

// heuristicTitle is the immediate placeholder shown while the model works: the
// first meaningful line of the request, capitalized and capped to the budget.
func heuristicTitle(prompt string, maximum int) string {
	if maximum <= 0 {
		maximum = defaultTitleMaxWords
	}
	line := ""
	for _, candidate := range strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n") {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			line = trimmed
			break
		}
	}
	line = strings.TrimSpace(strings.NewReplacer("`", "", "*", "", "_", "", "#", "", ">", "").Replace(line))
	words := strings.Fields(line)
	if len(words) > maximum {
		words = words[:maximum]
	}
	value := strings.Join(words, " ")
	if value == "" {
		return ""
	}
	first := []rune(value)
	return strings.ToUpper(string(first[0])) + string(first[1:])
}

// cleanGeneratedTitle enforces the header contract even when a helper model
// ignores its prompt: one line, no surrounding quotes or punctuation runs, and
// a strict word and rune cap.
func cleanGeneratedTitle(value string, maximum int) string {
	value = strings.TrimSpace(strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")[0])
	value = strings.Trim(value, " \t\"'`*_#.")
	if maximum <= 0 {
		maximum = defaultTitleMaxWords
	}
	var clean strings.Builder
	space := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '\'' || r == '-' || r == '/' || r == '.':
			clean.WriteRune(r)
			space = false
		case unicode.IsSpace(r) || r == ',':
			if !space {
				clean.WriteByte(' ')
				space = true
			}
		default:
			// Other punctuation is dropped, matching the pre-port titles.
		}
	}
	words := strings.Fields(clean.String())
	if len(words) > maximum {
		words = words[:maximum]
	}
	cleaned := strings.Join(words, " ")
	if runes := []rune(cleaned); len(runes) > maxTitleRunes {
		cleaned = string(runes[:maxTitleRunes-1]) + "…"
	}
	return cleaned
}

// maybeGenerateTitle asks the helper model for a session title. The first
// request names the session; later requests refine it as the session's goal
// becomes clearer. Generation is best-effort and serialized per session.
func (c *Chat) maybeGenerateTitle(prompt string) {
	c.mu.Lock()
	if c.titleGenerator == nil || c.titleGenerating {
		c.mu.Unlock()
		return
	}
	if strings.TrimSpace(prompt) == "" {
		c.mu.Unlock()
		return
	}
	maximum := c.titleMaxWords
	c.titleGenerating = true
	c.titleGeneration++
	generation := c.titleGeneration
	c.mu.Unlock()

	go func() {
		generateContext, cancel := context.WithTimeout(c.ctx, 20*time.Second)
		defer cancel()
		value, err := c.titleGenerator(generateContext, titleSource(prompt), maximum)
		value = cleanGeneratedTitle(value, maximum)
		c.mu.Lock()
		if c.titleGeneration != generation {
			c.mu.Unlock()
			return
		}
		c.titleGenerating = false
		applied := ""
		if err == nil && value != "" && value != c.title {
			c.title = value
			applied = value
		}
		onTitle := c.onTitle
		c.mu.Unlock()
		if applied != "" && onTitle != nil {
			onTitle(applied)
		}
		c.repaint()
	}()
}

// titleSource frames the request for the helper: a session title names the
// overarching goal, so the opening request is the strongest signal available.
func titleSource(prompt string) string {
	return "Original request: " + singleLine(strings.TrimSpace(prompt), 1000)
}

// applyHeuristicTitle shows a placeholder title as soon as a request is sent, so
// the header never sits on "New Session" while the helper model works.
func (c *Chat) applyHeuristicTitle(prompt string) {
	c.mu.Lock()
	maximum := c.titleMaxWords
	title := ""
	if strings.TrimSpace(c.title) == "" {
		title = heuristicTitle(prompt, maximum)
		c.title = title
	}
	c.mu.Unlock()
	if title != "" {
		c.repaint()
	}
}
