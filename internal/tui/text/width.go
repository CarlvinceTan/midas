// Package text implements terminal cell width, ANSI sequence extraction and
// output normalisation for Midas.
//
// # Index semantics
//
// The reference renderer indexes strings by UTF-16 code unit. Every function
// here indexes by byte instead. That is equivalent for the operations ported so
// far because each one either (a) fast-paths on printable ASCII, or (b) walks
// the string one unit at a time looking for ANSI introducers, which are all
// single-unit ASCII, and copies every other unit through unchanged. Where an
// offset can land mid-character — the editor cursor, paste markers, column
// slicing — the caller needs real UTF-16 semantics and this package must not be
// used as the index model. Those call sites are ported separately.
//
// # Character classification
//
// Width and printing decisions are driven by Unicode property tables in
// unicode_tables.go. Grapheme cluster segmentation uses github.com/rivo/uniseg,
// which implements UAX #29; agreement is enforced by differential tests.
package text

import (
	"slices"
	"strings"
	"unicode/utf16"
)

// IsPrintableASCII reports whether s is non-empty-free of anything outside
// 0x20..0x7E. The reference returns true for the empty string as well.
func IsPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// inRanges reports whether cp falls inside one of the inclusive [lo, hi] pairs
// stored back to back in ranges.
func inRanges(ranges []rune, cp rune) bool {
	lo, hi := 0, len(ranges)/2-1
	for lo <= hi {
		mid := int(uint(lo+hi) >> 1)
		start, end := ranges[mid*2], ranges[mid*2+1]
		switch {
		case cp < start:
			hi = mid - 1
		case cp > end:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

// isSurrogate reports whether cp is a UTF-16 surrogate code point. Valid UTF-8
// never produces one, but the reference classifies surrogate code units as
// zero-width, so the check is kept for fidelity.
func isSurrogate(cp rune) bool { return cp >= 0xd800 && cp <= 0xdfff }

// EastAsianWidth mirrors get-east-asian-width's eastAsianWidth(cp): 2 for
// fullwidth and wide, 1 otherwise, with ambiguous treated as narrow.
func EastAsianWidth(cp rune) int {
	if inRanges(eastAsianWideRanges, cp) {
		return 2
	}
	return 1
}

// terminalSpacingMark reports membership in the terminal-spacing mark set: the
// Spacing_Mark category minus three exceptions, plus an explicit legacy list.
func terminalSpacingMark(cp rune) bool {
	return inRanges(spacingMarkRanges, cp) || inRanges(terminalSpacingExtraRanges, cp)
}

// isTerminalSpacingMarkCluster mirrors /^(?:[...]|[...])+$/v: every code point
// in the cluster belongs to the terminal-spacing set.
func isTerminalSpacingMarkCluster(cluster []rune) bool {
	if len(cluster) == 0 {
		return false
	}
	for _, cp := range cluster {
		if !terminalSpacingMark(cp) {
			return false
		}
	}
	return true
}

// isZeroWidthCluster mirrors
// /^(?:\p{Default_Ignorable_Code_Point}|\p{Control}|\p{Mark}|\p{Surrogate})+$/v.
func isZeroWidthCluster(cluster []rune) bool {
	if len(cluster) == 0 {
		return false
	}
	for _, cp := range cluster {
		if !(inRanges(defaultIgnorableRanges, cp) || inRanges(controlRanges, cp) || inRanges(markRanges, cp) || isSurrogate(cp)) {
			return false
		}
	}
	return true
}

// isMarkChar mirrors /^\p{Mark}$/v.
func isMarkChar(cp rune) bool { return inRanges(markRanges, cp) }

// isNonPrintingChar mirrors
// /^(?:\p{Default_Ignorable_Code_Point}|\p{Control}|\p{Format}|\p{Mark}|\p{Surrogate})$/v.
func isNonPrintingChar(cp rune) bool {
	return inRanges(defaultIgnorableRanges, cp) || inRanges(controlRanges, cp) ||
		inRanges(formatRanges, cp) || inRanges(markRanges, cp) || isSurrogate(cp)
}

// stripLeadingNonPrinting mirrors the leading-only variant of the same set.
func stripLeadingNonPrinting(cluster []rune) []rune {
	for i, cp := range cluster {
		if !isNonPrintingChar(cp) {
			return cluster[i:]
		}
	}
	return nil
}

// couldBeEmoji is the reference's cheap pre-filter in front of the RGI_Emoji
// membership test. The final length check is a UTF-16 length, matching
// JavaScript's String#length.
func couldBeEmoji(cluster []rune) bool {
	if len(cluster) == 0 {
		return false
	}
	cp := cluster[0]
	if (cp >= 0x1f000 && cp <= 0x1fbff) || // emoji and pictographs
		(cp >= 0x2300 && cp <= 0x23ff) || // misc technical
		(cp >= 0x2600 && cp <= 0x27bf) || // misc symbols, dingbats
		(cp >= 0x2b50 && cp <= 0x2b55) { // specific stars and circles
		return true
	}
	for _, c := range cluster {
		if c == 0xfe0f { // VS16
			return true
		}
	}
	return len(utf16.Encode(cluster)) > 2
}

// IsRGIEmoji reports whether the cluster is a single RGI emoji presentation,
// either a one-code-point emoji or one of the enumerated RGI sequences.
func IsRGIEmoji(segment string) bool {
	cluster := []rune(segment)
	if len(cluster) == 1 {
		return inRanges(rgiEmojiSingleRanges, cluster[0])
	}
	// rgiEmojiSequences is emitted in code point order, which is Go's byte order
	// for UTF-8, so a binary search compares consistently.
	i, found := slices.BinarySearch(rgiEmojiSequences, segment)
	return found && rgiEmojiSequences[i] == segment
}

// GraphemeWidth returns the terminal cell width of a single grapheme cluster.
func GraphemeWidth(segment string) int {
	if segment == "\t" {
		return 3
	}

	cluster := []rune(segment)
	if len(cluster) == 0 {
		return 0
	}

	// Some marks occupy cells even without a base character.
	if isTerminalSpacingMarkCluster(cluster) {
		return len(cluster)
	}

	// Zero-width clusters.
	if isZeroWidthCluster(cluster) {
		return 0
	}

	// Emoji, behind the cheap pre-filter.
	if couldBeEmoji(cluster) && IsRGIEmoji(segment) {
		return 2
	}

	base := stripLeadingNonPrinting(cluster)
	if len(base) == 0 {
		return 0
	}
	cp := base[0]

	// Regional indicators are usually rendered full-width even when isolated
	// during streaming; staying at 2 avoids terminal auto-wrap drift.
	if cp >= 0x1f1e6 && cp <= 0x1f1ff {
		return 2
	}

	width := EastAsianWidth(cp)

	// Intl.Segmenter can group several terminal-spacing code points into one
	// cluster. Count trailing code points terminals may allocate cells for.
	followsMark := false
	for _, char := range base[1:] {
		switch {
		case terminalSpacingMark(char):
			width++
			followsMark = false
		case isMarkChar(char):
			followsMark = true
		case !isNonPrintingChar(char):
			if followsMark || (char >= 0xff00 && char <= 0xffef) {
				width += EastAsianWidth(char)
			} else if char == 0x0e33 || char == 0x0eb3 {
				width++
			}
			followsMark = false
		}
	}

	return width
}

// ExtractAnsiCode extracts an ANSI escape sequence starting at byte position
// pos. It recognises CSI sequences terminated by one of m, G, K, H, J, and
// OSC/APC sequences terminated by BEL or ST — exactly the set the reference
// recognises, quirks included.
func ExtractAnsiCode(s string, pos int) (code string, length int, ok bool) {
	if pos >= len(s) || s[pos] != 0x1b || pos+1 >= len(s) {
		return "", 0, false
	}

	switch s[pos+1] {
	case '[':
		j := pos + 2
		for j < len(s) && !isCSITerminator(s[j]) {
			j++
		}
		if j < len(s) {
			return s[pos : j+1], j + 1 - pos, true
		}
		return "", 0, false
	case ']', '_':
		for j := pos + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return s[pos : j+1], j + 1 - pos, true
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return s[pos : j+2], j + 2 - pos, true
			}
		}
		return "", 0, false
	default:
		return "", 0, false
	}
}

func isCSITerminator(c byte) bool {
	switch c {
	case 'm', 'G', 'K', 'H', 'J':
		return true
	default:
		return false
	}
}

// StripTerminalSequences removes ANSI, OSC and APC sequences, keeping visible
// text.
func StripTerminalSequences(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if _, n, ok := ExtractAnsiCode(s, i); ok {
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// NormalizeTerminalOutput replaces precomposed Thai/Lao AM vowels with their
// compatibility decomposition and expands tabs to three spaces, leaving tabs
// inside terminal sequences untouched.
func NormalizeTerminalOutput(s string) string {
	normalized := s
	if strings.ContainsRune(normalized, 0x0e33) || strings.ContainsRune(normalized, 0x0eb3) {
		normalized = strings.NewReplacer(
			"\u0e33", "\u0e4d\u0e32",
			"\u0eb3", "\u0ecd\u0eb2",
		).Replace(normalized)
	}
	if !strings.Contains(normalized, "\t") {
		return normalized
	}

	var b strings.Builder
	b.Grow(len(normalized))
	for i := 0; i < len(normalized); {
		if code, n, ok := ExtractAnsiCode(normalized, i); ok {
			b.WriteString(code)
			i += n
			continue
		}
		if normalized[i] == '\t' {
			b.WriteString("   ")
		} else {
			b.WriteByte(normalized[i])
		}
		i++
	}
	return b.String()
}

// VisibleWidth returns the terminal column width of a string, ignoring ANSI
// sequences and counting tabs as three cells.
func VisibleWidth(s string) int {
	if len(s) == 0 {
		return 0
	}
	if IsPrintableASCII(s) {
		return len(s)
	}

	clean := s
	if strings.Contains(clean, "\t") {
		clean = strings.ReplaceAll(clean, "\t", "   ")
	}
	if strings.Contains(clean, "\x1b") {
		clean = StripTerminalSequences(clean)
	}

	width := 0
	for _, cluster := range Graphemes(clean) {
		width += GraphemeWidth(cluster)
	}
	return width
}
