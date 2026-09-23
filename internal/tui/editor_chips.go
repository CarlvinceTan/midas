package tui

import "strings"

// Attachment chips are atomic: the cursor steps over them and one backspace
// removes the whole `[Image: …]` / `[File: …]` label. Only markers this editor
// created from a paste are registered, so hand-typed brackets stay ordinary
// text.

func mergeChipPaths(existing, added map[string]string) map[string]string {
	if existing == nil {
		existing = make(map[string]string, len(added))
	}
	for marker, filePath := range added {
		existing[marker] = filePath
	}
	return existing
}

// chipIndexes lists the byte ranges of registered chips, in text order.
func (e *Editor) chipIndexes() [][2]int {
	if len(e.chips) == 0 || e.text == "" {
		return nil
	}
	var ranges [][2]int
	for _, indexes := range AttachmentMarkerPattern.FindAllStringIndex(e.text, -1) {
		if _, known := e.chips[e.text[indexes[0]:indexes[1]]]; !known {
			continue
		}
		ranges = append(ranges, [2]int{indexes[0], indexes[1]})
	}
	return ranges
}

func (e *Editor) chipEndingAt(end int) (int, bool) {
	for _, indexes := range e.chipIndexes() {
		if indexes[1] == end {
			return indexes[0], true
		}
	}
	return 0, false
}

func (e *Editor) chipStartingAt(start int) (int, bool) {
	for _, indexes := range e.chipIndexes() {
		if indexes[0] == start {
			return indexes[1], true
		}
	}
	return 0, false
}

func (e *Editor) removeChipAt(start int) {
	end, ok := e.chipStartingAt(start)
	if !ok {
		return
	}
	delete(e.chips, e.text[start:end])
}

// SubmissionAttachments returns the chips captured by the last submit and
// clears them. Chat expands the visible markers into the absolute paths the
// agent can read.
func (e *Editor) SubmissionAttachments() map[string]string {
	if len(e.submitted) == 0 {
		return nil
	}
	result := e.submitted
	e.submitted = nil
	return result
}

// captureSubmission snapshots the chips that survive into the submitted text so
// the send path can still resolve them after the editor clears.
func (e *Editor) captureSubmission(value string) {
	if len(e.chips) == 0 {
		e.submitted = nil
		return
	}
	captured := make(map[string]string)
	for marker, filePath := range e.chips {
		if strings.Contains(value, marker) {
			captured[marker] = filePath
		}
	}
	if len(captured) == 0 {
		e.submitted = nil
		return
	}
	e.submitted = captured
}
