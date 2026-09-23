package tui

import (
	"fmt"
	"strings"
	"time"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

// The live action row is the pre-port Midas `WorkingIndicator`: a spinner plus
// the action currently in progress, rendered inside the transcript right after
// the in-flight run. The header keeps the coarse status; this row is the detail
// ("Running `go test ./...`", "Thinking for 12s", "Writing 3 lines").
const liveSpinnerInterval = 80 * time.Millisecond

const (
	liveToneThinking = "thinking"
	liveToneRunning  = "running"
	// liveToneWriting is the model composing a call, which the pre-port Midas
	// showed in plain white rather than as a running action.
	liveToneWriting = "writing"
)

type liveAction struct {
	id     string
	label  string
	tone   string
	detail string
}

// liveSnapshot is the locked view of the live row one frame renders.
type liveSnapshot struct {
	active   bool
	action   liveAction
	spinner  string
	expanded bool
}

// spinnerFrames is the Braille spinner the live action row advances while a
// run is in flight.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Advance drives the UI tick: it eases the t/s reading toward the measured rate
// and advances the activity spinner. It reports whether a repaint is needed.
func (c *Chat) Advance(now time.Time) bool {
	c.mu.Lock()
	target, has := c.generation.Rate()
	changed := c.rateDisplay.Step(target, has)
	busy := c.busy
	if busy && (c.lastSpinnerAt.IsZero() || now.Sub(c.lastSpinnerAt) >= liveSpinnerInterval) {
		c.spinnerFrame = (c.spinnerFrame + 1) % len(spinnerFrames)
		c.lastSpinnerAt = now
		changed = true
	}
	c.mu.Unlock()
	return changed
}

// SetModelReasoning records whether the active model hides reasoning tokens.
// Only models that do not hide them may calibrate the chars/token ratio.
func (c *Chat) SetModelReasoning(value bool) {
	c.mu.Lock()
	c.modelReasoning = value
	c.mu.Unlock()
}

// liveSnapshotLocked reports the action the transcript indicator should show.
// The active entry's newest in-flight step wins; a finished step only supplies
// the timer for the "Working for …" fallback.
func (c *Chat) liveSnapshotLocked(width int, now time.Time) liveSnapshot {
	snapshot := liveSnapshot{spinner: spinnerFrames[c.spinnerFrame%len(spinnerFrames)], expanded: c.liveExpanded}
	if !c.busy || c.active < 0 || c.active >= len(c.entries) {
		return snapshot
	}
	entry := &c.entries[c.active]
	snapshot.active = true
	var fallbackID string
	var fallbackSince int64
	consider := func(id string, since int64) {
		if fallbackID == "" {
			fallbackID, fallbackSince = id, since
		}
	}
	for index := len(entry.segments) - 1; index >= 0; index-- {
		segment := entry.segments[index]
		switch segment.kind {
		case chatSegmentTool:
			tool := entry.toolByID(segment.toolID)
			if tool == nil {
				continue
			}
			if tool.status != "done" {
				snapshot.action = liveAction{id: tool.id, label: toolActionText(*tool, false, c.mcpNames), tone: liveToneRunning}
				return snapshot
			}
			consider(tool.id, segment.endedAt)
		case chatSegmentThinking:
			if segment.endedAt == 0 {
				since := segment.startedAt
				if since == 0 {
					since = entry.startedAt
				}
				snapshot.action = liveAction{
					id:     fmt.Sprintf("thinking:%d", index),
					label:  "Thinking for " + formatLiveSeconds(now.UnixMilli()-since),
					tone:   liveToneThinking,
					detail: segment.text,
				}
				return snapshot
			}
			// Only a finished tool keeps its own timing; other settled steps start
			// the "Working for …" clock when the action is gone, like the port's
			// predecessor did.
			consider(fmt.Sprintf("thinking:%d", index), 0)
		case chatSegmentText:
			if strings.TrimSpace(segment.text) == "" {
				continue
			}
			lines := estimateWrappedLines(segment.text, width)
			snapshot.action = liveAction{
				id:    fmt.Sprintf("text:%d", index),
				label: fmt.Sprintf("Writing %d line%s", lines, pluralSuffix(lines)),
				tone:  liveToneThinking,
			}
			return snapshot
		}
	}
	if label := strings.TrimSpace(c.writingLabel); label != "" {
		snapshot.action = liveAction{id: "writing", label: label, tone: liveToneWriting}
		return snapshot
	}
	if fallbackID == "" {
		fallbackID, fallbackSince = c.liveSinceID, c.liveSince.UnixMilli()
	}
	if fallbackID == "" {
		fallbackID = "working"
	}
	if fallbackSince == 0 {
		fallbackSince = now.UnixMilli()
	}
	snapshot.action = liveAction{id: fallbackID, label: "Working for " + formatLiveSeconds(now.UnixMilli()-fallbackSince), tone: liveToneThinking}
	return snapshot
}

func (entry *chatEntry) toolByID(id string) *chatTool {
	for index := range entry.tools {
		if entry.tools[index].id == id {
			return &entry.tools[index]
		}
	}
	return nil
}

// estimateWrappedLines counts the visible lines streaming prose occupies, so
// "Writing 3 lines" does not change with every token.
func estimateWrappedLines(value string, width int) int {
	available := max(8, width-4)
	lines := 0
	for _, paragraph := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		lines += max(1, (tuitext.VisibleWidth(paragraph)+available-1)/available)
	}
	return max(1, lines)
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// formatLiveSeconds uses whole seconds, like the pre-port indicator: the row
// reports "Thinking for 4s", never milliseconds.
func formatLiveSeconds(milliseconds int64) string {
	seconds := max(int64(0), milliseconds) / 1000
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds %= 60
	if minutes < 60 {
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes %= 60
	if minutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, minutes)
}

// renderLiveStatus renders the indicator row (and its expanded detail) and
// records the click target that toggles that detail.
func (r *transcriptRenderer) renderLiveStatus(snapshot liveSnapshot, base int) []string {
	if !snapshot.active {
		return nil
	}
	theme := CurrentTheme()
	margin, _ := chatRowGeometry(r.width)
	contentWidth := max(1, r.width-margin*2)
	glyph := theme.FG("accent", snapshot.spinner)
	label := theme.FG("mdHeading", snapshot.action.label)
	switch snapshot.action.tone {
	case liveToneRunning:
		label = styleTranscriptVerb(snapshot.action.label)
	case liveToneWriting:
		// Composing a call is plain text, not a styled verb.
		label = theme.FG("text", snapshot.action.label)
	}
	line := strings.Repeat(" ", margin) + markTranscriptContent(tuitext.TruncateToWidth(glyph+" "+label, contentWidth, "…", true))
	lines := []string{line}
	r.targets = append(r.targets, transcriptTarget{kind: transcriptLiveTarget, start: base, end: base + 1})
	if !snapshot.expanded || strings.TrimSpace(snapshot.action.detail) == "" {
		return lines
	}
	detailIndent := margin + 2
	detailWidth := max(1, r.width-detailIndent-margin)
	detail := strings.ReplaceAll(snapshot.action.detail, "\r\n", "\n")
	rows := make([]string, 0, 12)
	for _, row := range strings.Split(detail, "\n") {
		if strings.TrimSpace(row) == "" {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) > 12 {
		rows = rows[len(rows)-12:]
	}
	for _, row := range rows {
		lines = append(lines, strings.Repeat(" ", detailIndent)+markTranscriptContent(CurrentTheme().FG("thinkingText", tuitext.TruncateToWidth(row, detailWidth, "…", true))))
	}
	r.targets = append(r.targets, transcriptTarget{kind: transcriptLiveTarget, start: base + 1, end: base + len(lines)})
	return lines
}
