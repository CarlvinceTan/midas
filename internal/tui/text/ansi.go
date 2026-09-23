package text

import (
	"strconv"
	"strings"
)

// ActiveHyperlink is an open OSC 8 hyperlink. The terminator is preserved
// because some terminals only make BEL-terminated links clickable, so a
// wrapped line must be reopened with the same terminator it started with.
type ActiveHyperlink struct {
	Params     string
	URL        string
	Terminator string
}

// ParseOsc8Hyperlink classifies an ANSI sequence as an OSC 8 hyperlink.
//
// It mirrors the reference's three-way return: `isOsc8` false means the
// sequence is not an OSC 8 code at all (the reference's `undefined`), while
// `isOsc8` true with a nil link means an empty URL, i.e. a close (the
// reference's `null`).
func ParseOsc8Hyperlink(ansiCode string) (link *ActiveHyperlink, isOsc8 bool) {
	const prefix = "\x1b]8;"
	if !strings.HasPrefix(ansiCode, prefix) {
		return nil, false
	}

	terminator := "\x1b\\"
	if strings.HasSuffix(ansiCode, "\x07") {
		terminator = "\x07"
	}
	body, ok := strings.CutSuffix(ansiCode[len(prefix):], terminator)
	if !ok {
		// An unterminated sequence cannot be trusted to name a link.
		return nil, false
	}

	params, url, _ := strings.Cut(body, ";")
	if url == "" {
		return nil, true
	}
	return &ActiveHyperlink{Params: params, URL: url, Terminator: terminator}, true
}

func formatOsc8Hyperlink(link *ActiveHyperlink) string {
	return "\x1b]8;" + link.Params + ";" + link.URL + link.Terminator
}

func formatOsc8Close(terminator string) string {
	return "\x1b]8;;" + terminator
}

// getActiveOsc8Close returns the closing sequence for the last hyperlink opened
// in prefix, or "" when prefix never opens one.
func getActiveOsc8Close(prefix string) string {
	if !strings.Contains(prefix, "\x1b]8;") {
		return ""
	}

	var active *ActiveHyperlink
	for i := 0; i < len(prefix); {
		if code, n, ok := ExtractAnsiCode(prefix, i); ok {
			if link, isOsc8 := ParseOsc8Hyperlink(code); isOsc8 {
				active = link
			}
			i += n
		} else {
			i++
		}
	}
	if active == nil {
		return ""
	}
	return formatOsc8Close(active.Terminator)
}

// AnsiCodeTracker tracks active SGR attributes so styling can be replayed
// across line breaks.
type AnsiCodeTracker struct {
	bold          bool
	dim           bool
	italic        bool
	underline     bool
	blink         bool
	inverse       bool
	hidden        bool
	strikethrough bool
	fgColor       string // full code, e.g. "31" or "38;5;240"; empty means unset
	bgColor       string
	hyperlink     *ActiveHyperlink
}

// sgrParams extracts the parameters of a CSI ... m sequence, mirroring the
// reference's unanchored /\x1b\[([\d;]*)m/ search. Because the parameter class
// cannot contain 'm', backtracking can only ever succeed on the maximal digit
// run, so a match exists exactly when the run is immediately followed by 'm'.
func sgrParams(ansiCode string) (string, bool) {
	for searchFrom := 0; searchFrom < len(ansiCode); {
		start := strings.Index(ansiCode[searchFrom:], "\x1b[")
		if start == -1 {
			return "", false
		}
		start += searchFrom
		j := start + 2
		for j < len(ansiCode) && isDigitOrSemicolon(ansiCode[j]) {
			j++
		}
		if j < len(ansiCode) && ansiCode[j] == 'm' {
			return ansiCode[start+2 : j], true
		}
		searchFrom = start + 1
	}
	return "", false
}

func isDigitOrSemicolon(c byte) bool {
	return (c >= '0' && c <= '9') || c == ';'
}

// Process applies one ANSI sequence to the tracked state.
func (t *AnsiCodeTracker) Process(ansiCode string) {
	if link, isOsc8 := ParseOsc8Hyperlink(ansiCode); isOsc8 {
		t.hyperlink = link
		return
	}

	if !strings.HasSuffix(ansiCode, "m") {
		return
	}

	params, ok := sgrParams(ansiCode)
	if !ok {
		return
	}
	if params == "" || params == "0" {
		t.Reset()
		return
	}

	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); {
		// An unparseable parameter behaves like JavaScript's NaN: it matches no
		// case and changes nothing.
		code, err := strconv.Atoi(parts[i])
		if err != nil {
			code = -1
		}

		// 256-colour and RGB codes consume several parameters.
		if code == 38 || code == 48 {
			if i+2 < len(parts) && parts[i+1] == "5" {
				colorCode := parts[i] + ";" + parts[i+1] + ";" + parts[i+2]
				if code == 38 {
					t.fgColor = colorCode
				} else {
					t.bgColor = colorCode
				}
				i += 3
				continue
			}
			if i+4 < len(parts) && parts[i+1] == "2" {
				colorCode := parts[i] + ";" + parts[i+1] + ";" + parts[i+2] + ";" + parts[i+3] + ";" + parts[i+4]
				if code == 38 {
					t.fgColor = colorCode
				} else {
					t.bgColor = colorCode
				}
				i += 5
				continue
			}
		}

		switch code {
		case 0:
			t.Reset()
		case 1:
			t.bold = true
		case 2:
			t.dim = true
		case 3:
			t.italic = true
		case 4:
			t.underline = true
		case 5:
			t.blink = true
		case 7:
			t.inverse = true
		case 8:
			t.hidden = true
		case 9:
			t.strikethrough = true
		case 21:
			t.bold = false
		case 22:
			t.bold = false
			t.dim = false
		case 23:
			t.italic = false
		case 24:
			t.underline = false
		case 25:
			t.blink = false
		case 27:
			t.inverse = false
		case 28:
			t.hidden = false
		case 29:
			t.strikethrough = false
		case 39:
			t.fgColor = ""
		case 49:
			t.bgColor = ""
		default:
			if (code >= 30 && code <= 37) || (code >= 90 && code <= 97) {
				t.fgColor = strconv.Itoa(code)
			} else if (code >= 40 && code <= 47) || (code >= 100 && code <= 107) {
				t.bgColor = strconv.Itoa(code)
			}
		}
		i++
	}
}

// Reset clears every SGR attribute. An SGR reset does not affect OSC 8 state.
func (t *AnsiCodeTracker) Reset() {
	t.bold = false
	t.dim = false
	t.italic = false
	t.underline = false
	t.blink = false
	t.inverse = false
	t.hidden = false
	t.strikethrough = false
	t.fgColor = ""
	t.bgColor = ""
}

// ActiveCodes returns the sequence that restores the tracked state.
func (t *AnsiCodeTracker) ActiveCodes() string {
	var codes []string
	if t.bold {
		codes = append(codes, "1")
	}
	if t.dim {
		codes = append(codes, "2")
	}
	if t.italic {
		codes = append(codes, "3")
	}
	if t.underline {
		codes = append(codes, "4")
	}
	if t.blink {
		codes = append(codes, "5")
	}
	if t.inverse {
		codes = append(codes, "7")
	}
	if t.hidden {
		codes = append(codes, "8")
	}
	if t.strikethrough {
		codes = append(codes, "9")
	}
	if t.fgColor != "" {
		codes = append(codes, t.fgColor)
	}
	if t.bgColor != "" {
		codes = append(codes, t.bgColor)
	}

	result := ""
	if len(codes) > 0 {
		result = "\x1b[" + strings.Join(codes, ";") + "m"
	}
	if t.hyperlink != nil {
		result += formatOsc8Hyperlink(t.hyperlink)
	}
	return result
}

// ActiveBackgroundCode returns only the background colour sequence.
func (t *AnsiCodeTracker) ActiveBackgroundCode() string {
	if t.bgColor == "" {
		return ""
	}
	return "\x1b[" + t.bgColor + "m"
}

// HasActiveCodes reports whether any attribute or hyperlink is active.
func (t *AnsiCodeTracker) HasActiveCodes() bool {
	return t.bold || t.dim || t.italic || t.underline || t.blink || t.inverse ||
		t.hidden || t.strikethrough || t.fgColor != "" || t.bgColor != "" || t.hyperlink != nil
}

// LineEndReset closes attributes that must not bleed into padding. Underline is
// closed, and an open hyperlink is closed here and reopened at the next line
// start via ActiveCodes.
func (t *AnsiCodeTracker) LineEndReset() string {
	result := ""
	if t.underline {
		result += "\x1b[24m"
	}
	if t.hyperlink != nil {
		result += formatOsc8Close(t.hyperlink.Terminator)
	}
	return result
}

func updateTrackerFromText(s string, tracker *AnsiCodeTracker) {
	for i := 0; i < len(s); {
		if code, n, ok := ExtractAnsiCode(s, i); ok {
			tracker.Process(code)
			i += n
		} else {
			i++
		}
	}
}

// GetActiveBackgroundAnsi returns only the background colour active at the end
// of an ANSI-styled string.
func GetActiveBackgroundAnsi(s string) string {
	var tracker AnsiCodeTracker
	updateTrackerFromText(s, &tracker)
	return tracker.ActiveBackgroundCode()
}

func getActiveForegroundAnsi(s string) string {
	var tracker AnsiCodeTracker
	updateTrackerFromText(s, &tracker)
	if tracker.fgColor == "" {
		return ""
	}
	return "\x1b[" + tracker.fgColor + "m"
}
