package tui

import (
	"sync"
	"time"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

const defaultAltScreenFlashDuration = time.Second

type altScreenFlashEntry struct {
	id      uint64
	message string
	style   string
	timer   *time.Timer
}

// AltScreenFlashContainer stores transient messages composited at the top right.
type AltScreenFlashContainer struct {
	mu            sync.Mutex
	entries       []altScreenFlashEntry
	nextID        uint64
	requestRender func()
}

func NewAltScreenFlashContainer(requestRender func()) *AltScreenFlashContainer {
	if requestRender == nil {
		requestRender = func() {}
	}
	return &AltScreenFlashContainer{entries: []altScreenFlashEntry{}, requestRender: requestRender}
}

func (c *AltScreenFlashContainer) Flash(message string, duration time.Duration, style string) {
	if style == "" {
		style = "\x1b[7m"
	}
	if duration < 0 {
		duration = 0
	}
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	entry := altScreenFlashEntry{id: id, message: message, style: style}
	entry.timer = time.AfterFunc(duration, func() {
		c.mu.Lock()
		for index := range c.entries {
			if c.entries[index].id == id {
				c.entries = append(c.entries[:index], c.entries[index+1:]...)
				c.mu.Unlock()
				c.requestRender()
				return
			}
		}
		c.mu.Unlock()
	})
	c.entries = append(c.entries, entry)
	c.mu.Unlock()
	c.requestRender()
}

func (c *AltScreenFlashContainer) Dispose() {
	c.mu.Lock()
	for _, entry := range c.entries {
		entry.timer.Stop()
	}
	c.entries = []altScreenFlashEntry{}
	c.mu.Unlock()
}

func (c *AltScreenFlashContainer) Invalidate() {}

func (c *AltScreenFlashContainer) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	lines := make([]string, len(c.entries))
	for index, entry := range c.entries {
		message := tuitext.TruncateToWidth(" "+entry.message+" ", width, "", false)
		lines[index] = entry.style + message + "\x1b[0m"
	}
	return lines
}

// Flash shows a reverse-video transient message for one second unless duration is supplied.
func (t *TuiAltScreen) Flash(message string, duration ...time.Duration) {
	delay := defaultAltScreenFlashDuration
	if len(duration) > 0 {
		delay = duration[0]
	}
	t.flashes.Flash(message, delay, "\x1b[7m")
}

// FlashStyled shows a transient message with a caller-supplied SGR prefix.
func (t *TuiAltScreen) FlashStyled(message string, duration time.Duration, style string) {
	t.flashes.Flash(message, duration, style)
}

func (t *TuiAltScreen) compositeFlashesLocked(screen []string, width, height int) []string {
	flashLines := t.flashes.Render(width)
	if len(flashLines) > height {
		flashLines = flashLines[len(flashLines)-height:]
	}
	if len(flashLines) == 0 {
		return screen
	}
	result := append([]string(nil), screen...)
	for len(result) < height {
		result = append(result, "")
	}
	for row, line := range flashLines {
		flashWidth := tuitext.VisibleWidth(line)
		if flashWidth > 0 {
			result[row] = CompositeTuiLine(result[row], line, width-flashWidth, flashWidth, width)
		}
	}
	return result
}
