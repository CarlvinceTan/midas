package text

import (
	"strings"
	"unicode/utf8"
)

// JavaScript's whitespace set for String#trim and the /\s/ class: WhiteSpace
// plus LineTerminator. Go's unicode.IsSpace differs on two code points —
// it excludes U+FEFF, which JavaScript trims, and includes U+0085, which
// JavaScript does not — so trimming is implemented explicitly here.
func isJSWhitespace(r rune) bool {
	switch r {
	case 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// jsTrim removes leading and trailing JavaScript whitespace.
func jsTrim(s string) string {
	return jsTrimEnd(jsTrimStart(s))
}

func jsTrimStart(s string) string {
	for i, r := range s {
		if !isJSWhitespace(r) {
			return s[i:]
		}
	}
	return ""
}

// jsTrimEnd removes trailing JavaScript whitespace without copying the prefix.
func jsTrimEnd(s string) string {
	end := len(s)
	for end > 0 {
		r, size := lastRune(s[:end])
		if !isJSWhitespace(r) {
			break
		}
		end -= size
	}
	return s[:end]
}

// lastRune decodes the final rune of s, reporting its encoded size.
func lastRune(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}
	r, size := utf8.DecodeLastRuneInString(s)
	return r, size
}

// hasJSWhitespace reports whether s contains any JavaScript whitespace rune,
// mirroring the unanchored /\s/ test the reference uses per character.
func hasJSWhitespace(s string) bool {
	for _, r := range s {
		if isJSWhitespace(r) {
			return true
		}
	}
	return false
}

// IsWhitespaceChar reports whether s contains a whitespace character.
func IsWhitespaceChar(s string) bool { return hasJSWhitespace(s) }

// PUNCTUATION_REGEX in the reference is a single unanchored character class, so
// any matching rune makes the whole string "punctuation".
const punctuationChars = "(){}[]<>.,;:'\"!?+-=*/\\|&%^$#@~`"

// IsPunctuationChar reports whether s contains a punctuation character.
func IsPunctuationChar(s string) bool {
	return strings.ContainsAny(s, punctuationChars)
}

// isCJK mirrors the unanchored cjkBreakRegex: true when any rune of the cluster
// belongs to one of the CJK script extensions.
func isCJK(s string) bool {
	for _, r := range s {
		if inRanges(cjkRanges, r) {
			return true
		}
	}
	return false
}

// splitIntoTokensWithAnsi splits text into whitespace and word tokens, keeping
// ANSI codes attached to the following visible content and emitting CJK clusters
// as their own tokens.
func splitIntoTokensWithAnsi(s string) []string {
	tokens := []string{}
	current := ""
	pendingAnsi := ""
	currentKind := "" // "", "space" or "word"

	flushCurrent := func() {
		if current == "" {
			return
		}
		tokens = append(tokens, current)
		current = ""
		currentKind = ""
	}

	for i := 0; i < len(s); {
		if code, n, ok := ExtractAnsiCode(s, i); ok {
			pendingAnsi += code
			i += n
			continue
		}

		end := i
		for end < len(s) {
			if _, _, ok := ExtractAnsiCode(s, end); ok {
				break
			}
			end++
		}

		for _, segment := range Graphemes(s[i:end]) {
			segmentIsSpace := segment == " "
			if !segmentIsSpace && isCJK(segment) {
				flushCurrent()
				tokens = append(tokens, pendingAnsi+segment)
				pendingAnsi = ""
				continue
			}

			segmentKind := "word"
			if segmentIsSpace {
				segmentKind = "space"
			}
			if current != "" && currentKind != segmentKind {
				flushCurrent()
			}

			if pendingAnsi != "" {
				current += pendingAnsi
				pendingAnsi = ""
			}
			currentKind = segmentKind
			current += segment
		}

		i = end
	}

	if pendingAnsi != "" {
		switch {
		case current != "":
			current += pendingAnsi
		case len(tokens) > 0:
			tokens[len(tokens)-1] += pendingAnsi
		default:
			current = pendingAnsi
		}
	}
	if current != "" {
		tokens = append(tokens, current)
	}

	return tokens
}

// WrapTextWithAnsi word-wraps text to width, preserving ANSI styling across
// line breaks. Every returned line is at most width visible columns, and no
// padding or background is applied.
func WrapTextWithAnsi(s string, width int) []string {
	if s == "" {
		return []string{""}
	}

	// Split manually rather than with strings.FieldsFunc, which drops empty
	// fields and would lose blank lines.
	inputLines := splitLinesJS(s)

	result := []string{}
	var tracker AnsiCodeTracker

	for _, inputLine := range inputLines {
		prefix := ""
		if len(result) > 0 {
			prefix = tracker.ActiveCodes()
		}
		result = append(result, wrapSingleLine(prefix+inputLine, width)...)
		updateTrackerFromText(inputLine, &tracker)
	}

	if len(result) > 0 {
		return result
	}
	return []string{""}
}

// splitLinesJS splits on \r\n, \r or \n, keeping empty lines.
func splitLinesJS(s string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r':
			lines = append(lines, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		case '\n':
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}

func wrapSingleLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}

	if VisibleWidth(line) <= width {
		return []string{line}
	}

	wrapped := []string{}
	var tracker AnsiCodeTracker
	tokens := splitIntoTokensWithAnsi(line)

	currentLine := ""
	currentVisibleLength := 0

	for _, token := range tokens {
		tokenVisibleLength := VisibleWidth(token)
		isWhitespace := jsTrim(token) == ""

		if tokenVisibleLength > width && !isWhitespace {
			if currentLine != "" {
				if reset := tracker.LineEndReset(); reset != "" {
					currentLine += reset
				}
				wrapped = append(wrapped, currentLine)
				currentLine = ""
				currentVisibleLength = 0
			}

			broken := breakLongWord(token, width, &tracker)
			wrapped = append(wrapped, broken[:len(broken)-1]...)
			currentLine = broken[len(broken)-1]
			currentVisibleLength = VisibleWidth(currentLine)
			continue
		}

		totalNeeded := currentVisibleLength + tokenVisibleLength
		if totalNeeded > width && currentVisibleLength > 0 {
			lineToWrap := jsTrimEnd(currentLine)
			if reset := tracker.LineEndReset(); reset != "" {
				lineToWrap += reset
			}
			wrapped = append(wrapped, lineToWrap)
			if isWhitespace {
				currentLine = tracker.ActiveCodes()
				currentVisibleLength = 0
			} else {
				currentLine = tracker.ActiveCodes() + token
				currentVisibleLength = tokenVisibleLength
			}
		} else {
			currentLine += token
			currentVisibleLength += tokenVisibleLength
		}

		updateTrackerFromText(token, &tracker)
	}

	if currentLine != "" {
		wrapped = append(wrapped, currentLine)
	}

	if len(wrapped) == 0 {
		return []string{""}
	}
	for i := range wrapped {
		wrapped[i] = jsTrimEnd(wrapped[i])
	}
	return wrapped
}

// breakLongWord splits a single over-wide token into lines, replaying active
// styling at the start of each continuation line.
func breakLongWord(word string, width int, tracker *AnsiCodeTracker) []string {
	lines := []string{}
	currentLine := tracker.ActiveCodes()
	currentWidth := 0

	type segment struct {
		isAnsi bool
		value  string
	}
	var segments []segment

	for i := 0; i < len(word); {
		if code, n, ok := ExtractAnsiCode(word, i); ok {
			segments = append(segments, segment{true, code})
			i += n
			continue
		}

		end := i
		for end < len(word) {
			if _, _, ok := ExtractAnsiCode(word, end); ok {
				break
			}
			end++
		}
		for _, grapheme := range Graphemes(word[i:end]) {
			segments = append(segments, segment{false, grapheme})
		}
		i = end
	}

	for _, seg := range segments {
		if seg.isAnsi {
			currentLine += seg.value
			tracker.Process(seg.value)
			continue
		}

		grapheme := seg.value
		if grapheme == "" {
			continue
		}

		graphemeCellWidth := VisibleWidth(grapheme)
		if currentWidth+graphemeCellWidth > width {
			if reset := tracker.LineEndReset(); reset != "" {
				currentLine += reset
			}
			lines = append(lines, currentLine)
			currentLine = tracker.ActiveCodes()
			currentWidth = 0
		}

		currentLine += grapheme
		currentWidth += graphemeCellWidth
	}

	if currentLine != "" {
		lines = append(lines, currentLine)
	}
	if len(lines) > 0 {
		return lines
	}
	return []string{""}
}
