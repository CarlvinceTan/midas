package tui

import (
	"strings"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

const segmentReset = "\x1b[0m\x1b]8;;\x07"

// CompositeTuiLine paints overlayLine into a fixed-width region of baseLine.
func CompositeTuiLine(baseLine, overlayLine string, startCol, overlayWidth, totalWidth int) string {
	if IsImageLine(baseLine) {
		return baseLine
	}

	afterStart := startCol + overlayWidth
	base := tuitext.ExtractSegments(baseLine, startCol, afterStart, totalWidth-afterStart, true)
	overlay := tuitext.SliceWithWidth(overlayLine, 0, overlayWidth, true)
	beforePad := max(0, startCol-base.BeforeWidth)
	overlayPad := max(0, overlayWidth-overlay.Width)
	actualBeforeWidth := max(startCol, base.BeforeWidth)
	actualOverlayWidth := max(overlayWidth, overlay.Width)
	afterTarget := max(0, totalWidth-actualBeforeWidth-actualOverlayWidth)
	afterPad := max(0, afterTarget-base.AfterWidth)

	result := base.Before +
		strings.Repeat(" ", beforePad) +
		segmentReset +
		overlay.Text +
		strings.Repeat(" ", overlayPad) +
		segmentReset +
		base.After +
		strings.Repeat(" ", afterPad)
	if tuitext.VisibleWidth(result) <= totalWidth {
		return result
	}
	return tuitext.SliceByColumn(result, 0, totalWidth, true)
}
