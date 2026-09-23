package text

// SliceResult is the visible text of a column range plus its actual width.
type SliceResult struct {
	Text  string
	Width int
}

// SliceByColumn extracts a range of visible columns from a line, handling ANSI
// codes and wide characters.
func SliceByColumn(line string, startCol, length int, strict bool) string {
	return SliceWithWidth(line, startCol, length, strict).Text
}

// SliceWithWidth extracts a range of visible columns and reports the width of
// what it kept.
//
// This follows the vendored build rather than upstream: an ANSI code that lands
// inside the range flushes any pending styling first, so earlier styles are
// replayed before boundary resets rather than after them. See
// tools/parity/dist-patches.json.
func SliceWithWidth(line string, startCol, length int, strict bool) SliceResult {
	if length <= 0 {
		return SliceResult{}
	}
	endCol := startCol + length
	result := ""
	resultWidth := 0
	currentCol := 0
	pendingAnsi := ""

	for i := 0; i < len(line); {
		if ansi, n, ok := ExtractAnsiCode(line, i); ok {
			if currentCol >= startCol && currentCol < endCol {
				// Replay earlier styles before boundary resets, never after them.
				if pendingAnsi != "" {
					result += pendingAnsi
					pendingAnsi = ""
				}
				result += ansi
			} else if currentCol < startCol {
				pendingAnsi += ansi
			}
			i += n
			continue
		}

		textEnd := i
		for textEnd < len(line) {
			if _, _, ok := ExtractAnsiCode(line, textEnd); ok {
				break
			}
			textEnd++
		}

		for _, segment := range Graphemes(line[i:textEnd]) {
			w := GraphemeWidth(segment)
			inRange := currentCol >= startCol && currentCol < endCol
			fits := !strict || currentCol+w <= endCol
			if inRange && fits {
				if pendingAnsi != "" {
					result += pendingAnsi
					pendingAnsi = ""
				}
				result += segment
				resultWidth += w
			}
			currentCol += w
			if currentCol >= endCol {
				break
			}
		}

		i = textEnd
		if currentCol >= endCol {
			break
		}
	}

	return SliceResult{Text: result, Width: resultWidth}
}

// SegmentResult holds the parts of a line either side of an overlay region.
type SegmentResult struct {
	Before      string
	BeforeWidth int
	After       string
	AfterWidth  int
}

// ExtractSegments splits a line into the region before an overlay and the
// region after it in one pass, so content after the overlay inherits the
// styling that was active before it.
func ExtractSegments(line string, beforeEnd, afterStart, afterLen int, strictAfter bool) SegmentResult {
	before := ""
	beforeWidth := 0
	after := ""
	afterWidth := 0
	currentCol := 0
	pendingAnsiBefore := ""
	afterStarted := false
	afterEnd := afterStart + afterLen

	var styleTracker AnsiCodeTracker

	for i := 0; i < len(line); {
		if ansi, n, ok := ExtractAnsiCode(line, i); ok {
			styleTracker.Process(ansi)
			if currentCol < beforeEnd {
				pendingAnsiBefore += ansi
			} else if currentCol >= afterStart && currentCol < afterEnd && afterStarted {
				after += ansi
			}
			i += n
			continue
		}

		textEnd := i
		for textEnd < len(line) {
			if _, _, ok := ExtractAnsiCode(line, textEnd); ok {
				break
			}
			textEnd++
		}

		for _, segment := range Graphemes(line[i:textEnd]) {
			w := GraphemeWidth(segment)

			if currentCol < beforeEnd && currentCol+w <= beforeEnd {
				if pendingAnsiBefore != "" {
					before += pendingAnsiBefore
					pendingAnsiBefore = ""
				}
				before += segment
				beforeWidth += w
			} else if currentCol >= afterStart && currentCol < afterEnd {
				fits := !strictAfter || currentCol+w <= afterEnd
				if fits {
					if !afterStarted {
						after += styleTracker.ActiveCodes()
						afterStarted = true
					}
					after += segment
					afterWidth += w
				}
			}

			currentCol += w
			if done(beforeEnd, afterLen, afterEnd, currentCol) {
				break
			}
		}

		i = textEnd
		if done(beforeEnd, afterLen, afterEnd, currentCol) {
			break
		}
	}

	return SegmentResult{Before: before, BeforeWidth: beforeWidth, After: after, AfterWidth: afterWidth}
}

// done mirrors the reference's early-exit test: when there is no "after"
// region, only the "before" region matters.
func done(beforeEnd, afterLen, afterEnd, currentCol int) bool {
	if afterLen <= 0 {
		return currentCol >= beforeEnd
	}
	return currentCol >= afterEnd
}
