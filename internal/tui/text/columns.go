package text

import "strings"

// GraphemeCellRange is the terminal-cell span occupied by one grapheme.
type GraphemeCellRange struct {
	Start int
	End   int
}

// GetGraphemeCellRange returns the cell range of the grapheme at a visible
// column, or false when the column is past the end of the line.
func GetGraphemeCellRange(line string, column int) (GraphemeCellRange, bool) {
	currentCol := 0
	for i := 0; i < len(line); {
		if _, n, ok := ExtractAnsiCode(line, i); ok {
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
			width := GraphemeWidth(segment)
			if width > 0 && column >= currentCol && column < currentCol+width {
				return GraphemeCellRange{Start: currentCol, End: currentCol + width}, true
			}
			currentCol += width
		}
		i = textEnd
	}
	return GraphemeCellRange{}, false
}

// GetOsc8LinkAtColumn returns the URL of the OSC 8 hyperlink covering a visible
// column, or false when no hyperlink is open there.
func GetOsc8LinkAtColumn(line string, column int) (string, bool) {
	activeURL := ""
	hasActive := false
	currentCol := 0

	for i := 0; i < len(line); {
		if ansi, n, ok := ExtractAnsiCode(line, i); ok {
			if _, url, matched := parseOsc8ForColumn(ansi); matched {
				// An empty URL closes the link, mirroring `hyperlink[1] || undefined`.
				if url == "" {
					activeURL = ""
					hasActive = false
				} else {
					activeURL = url
					hasActive = true
				}
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
			width := GraphemeWidth(segment)
			if segment == "\t" {
				width = 3
			}
			if column >= currentCol && column < currentCol+width {
				return activeURL, hasActive
			}
			currentCol += width
		}
		i = textEnd
	}
	return "", false
}

// parseOsc8ForColumn mirrors /^\x1b\]8;[^;]*;([^\x07\x1b]*)(?:\x07|\x1b\\)$/,
// returning the captured URL.
func parseOsc8ForColumn(ansiCode string) (params, url string, matched bool) {
	if !strings.HasPrefix(ansiCode, "\x1b]8;") {
		return "", "", false
	}
	body := ansiCode[4:]
	switch {
	case strings.HasSuffix(body, "\x07"):
		body = body[:len(body)-1]
	case strings.HasSuffix(body, "\x1b\\"):
		body = body[:len(body)-2]
	default:
		return "", "", false
	}
	separator := strings.Index(body, ";")
	if separator == -1 {
		return "", "", false
	}
	params = body[:separator]
	// [^;]* forbids semicolons in the parameter field.
	if strings.Contains(params, ";") {
		return "", "", false
	}
	url = body[separator+1:]
	// The capture class excludes BEL and ESC.
	if strings.ContainsAny(url, "\x07\x1b") {
		return "", "", false
	}
	return params, url, true
}

// FrameEdge identifies which row of a rounded frame is being rendered.
type FrameEdge string

const (
	FrameTop    FrameEdge = "top"
	FrameBottom FrameEdge = "bottom"
	FrameBody   FrameEdge = "body"
)

// Decoration markers understood by the alternate-screen renderer. They are a
// local addition to the vendored build, not upstream: the selection and copy
// code uses them to bound content and to skip frame decoration.
const (
	decorationMarker = "\x1b]777;midas-decoration\x07"
	contentStartMark = "\x1b]777;midas-content-start\x07"
	contentEndMark   = "\x1b]777;midas-content-end\x07"
)

// DecorationMarker is the marker that flags a rendered row as frame decoration.
const DecorationMarker = decorationMarker

// RoundedFrameRow renders one row of a rounded frame. The middle segment is
// already rendered at the inner width. From the vendored build rather than
// upstream; see tools/parity/dist-patches.json.
func RoundedFrameRow(middle string, edge FrameEdge, color func(string) string) string {
	if edge == FrameBody {
		return color("\u2502") + middle + "\x1b[0m" + color("\u2502")
	}
	corners := [2]string{"\u256d", "\u256e"}
	if edge == FrameBottom {
		corners = [2]string{"\u2570", "\u256f"}
	}
	return decorationMarker + contentStartMark + contentEndMark +
		color(corners[0]) + middle + color(corners[1])
}
