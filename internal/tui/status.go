package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const statusRefreshInterval = 6 * time.Second

// cleanStatusPhrase enforces the header contract even when a helper model
// ignores its prompt: words only, no punctuation, and a strict word cap.
func cleanStatusPhrase(value string, maximum int) string {
	if maximum <= 0 {
		maximum = 6
	}
	var clean strings.Builder
	space := true
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) {
			clean.WriteRune(r)
			space = false
			continue
		}
		if !space {
			clean.WriteByte(' ')
			space = true
		}
	}
	words := strings.Fields(clean.String())
	if len(words) > maximum {
		words = words[:maximum]
	}
	return strings.Join(words, " ")
}

func (c *Chat) statusContextLocked() string {
	var prompt string
	for index := len(c.entries) - 1; index >= 0; index-- {
		if c.entries[index].kind == chatUser {
			prompt = c.entries[index].text
			break
		}
	}
	lines := make([]string, 0, 4)
	if prompt != "" {
		lines = append(lines, "Goal: "+singleLine(prompt, 500))
	}
	if c.active >= 0 && c.active < len(c.entries) {
		active := c.entries[c.active]
		if active.text != "" {
			lines = append(lines, "Assistant progress: "+singleLine(active.text, 500))
		}
		if active.thinking != "" {
			lines = append(lines, "Reasoning progress: "+singleLine(active.thinking, 300))
		}
		if len(active.tools) > 0 {
			running, complete, failed := 0, 0, 0
			for _, tool := range active.tools {
				if tool.status == "running" {
					running++
				} else if tool.isError {
					failed++
				} else {
					complete++
				}
			}
			lines = append(lines, fmt.Sprintf("Work steps: %d running %d complete %d failed", running, complete, failed))
		}
	}
	return strings.Join(lines, "\n")
}

func (c *Chat) maybeGenerateStatus() {
	c.mu.Lock()
	if !c.busy || c.compactHeader || c.statusGenerator == nil || c.statusGenerating || time.Since(c.lastStatusAt) < statusRefreshInterval {
		c.mu.Unlock()
		return
	}
	source := c.statusContextLocked()
	if strings.TrimSpace(source) == "" {
		c.mu.Unlock()
		return
	}
	c.statusGenerating = true
	c.statusGeneration++
	generation := c.statusGeneration
	c.lastStatusAt = time.Now()
	maximum := c.statusMaxWords
	generator := c.statusGenerator
	ctx := c.ctx
	c.mu.Unlock()

	go func() {
		requestContext, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		value, err := generator(requestContext, source, maximum)
		value = cleanStatusPhrase(value, maximum)
		c.mu.Lock()
		if c.statusGeneration == generation {
			c.statusGenerating = false
			if err == nil && c.busy && !c.compactHeader && value != "" && !strings.EqualFold(value, c.title) {
				c.statusText = value
			}
		}
		c.mu.Unlock()
		c.repaint()
	}()
}
