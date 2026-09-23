package text

import "strings"

// ApplyBackgroundToLine pads a line to width and wraps the result in bgFn.
func ApplyBackgroundToLine(line string, width int, bgFn func(string) string) string {
	padding := width - VisibleWidth(line)
	if padding < 0 {
		padding = 0
	}
	return bgFn(line + strings.Repeat(" ", padding))
}

// truncateFragmentToWidth keeps whole grapheme clusters up to maxWidth,
// reporting the width actually kept.
func truncateFragmentToWidth(s string, maxWidth int) (string, int) {
	if maxWidth <= 0 || s == "" {
		return "", 0
	}

	if IsPrintableASCII(s) {
		clipped := s
		if len(clipped) > maxWidth {
			clipped = clipped[:maxWidth]
		}
		return clipped, len(clipped)
	}

	hasAnsi := strings.Contains(s, "\x1b")
	hasTabs := strings.Contains(s, "\t")
	if !hasAnsi && !hasTabs {
		var result strings.Builder
		width := 0
		for _, segment := range Graphemes(s) {
			w := GraphemeWidth(segment)
			if width+w > maxWidth {
				break
			}
			result.WriteString(segment)
			width += w
		}
		return result.String(), width
	}

	var result strings.Builder
	width := 0
	pendingAnsi := ""
	for i := 0; i < len(s); {
		if code, n, ok := ExtractAnsiCode(s, i); ok {
			pendingAnsi += code
			i += n
			continue
		}

		if s[i] == '\t' {
			if width+3 > maxWidth {
				break
			}
			if pendingAnsi != "" {
				result.WriteString(pendingAnsi)
				pendingAnsi = ""
			}
			result.WriteByte('\t')
			width += 3
			i++
			continue
		}

		end := i
		for end < len(s) && s[end] != '\t' {
			if _, _, ok := ExtractAnsiCode(s, end); ok {
				break
			}
			end++
		}

		for _, segment := range Graphemes(s[i:end]) {
			w := GraphemeWidth(segment)
			if width+w > maxWidth {
				return result.String(), width
			}
			if pendingAnsi != "" {
				result.WriteString(pendingAnsi)
				pendingAnsi = ""
			}
			result.WriteString(segment)
			width += w
		}
		i = end
	}

	return result.String(), width
}

// finalizeTruncatedResult appends the hyperlink close, reset and ellipsis to a
// truncated prefix, optionally padding to maxWidth. When the kept prefix has an
// active foreground colour, that colour is replayed for the ellipsis after the
// reset. Other attributes and padding remain reset.
func finalizeTruncatedResult(prefix string, prefixWidth int, ellipsis string, ellipsisWidth, maxWidth int, pad bool, foregroundHint string) string {
	const reset = "\x1b[0m"
	hyperlinkClose := getActiveOsc8Close(prefix)
	foreground := getActiveForegroundAnsi(prefix)
	if foreground == "" {
		foreground = foregroundHint
	}
	visible := prefixWidth + ellipsisWidth

	var result string
	if ellipsis != "" {
		result = prefix + hyperlinkClose + reset + foreground + ellipsis + reset
	} else {
		result = prefix + hyperlinkClose + reset
	}

	if !pad {
		return result
	}
	return result + strings.Repeat(" ", maxInt(0, maxWidth-visible))
}

// leadingTerminalCodes returns the zero-width styling prefix before the first
// visible character. It lets an ellipsis-only result retain the source colour
// when maxWidth leaves no room for any source text.
func leadingTerminalCodes(value string) string {
	var result strings.Builder
	for index := 0; index < len(value); {
		code, length, ok := ExtractAnsiCode(value, index)
		if !ok {
			break
		}
		result.WriteString(code)
		index += length
	}
	return result.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TruncateToWidth truncates text to a maximum visible width, appending an
// ellipsis when it has to cut, and optionally padding to exactly maxWidth.
func TruncateToWidth(s string, maxWidth int, ellipsis string, pad bool) string {
	if maxWidth <= 0 {
		return ""
	}
	if s == "" {
		if pad {
			return strings.Repeat(" ", maxWidth)
		}
		return ""
	}

	ellipsisWidth := VisibleWidth(ellipsis)
	if ellipsisWidth >= maxWidth {
		textWidth := VisibleWidth(s)
		if textWidth <= maxWidth {
			if pad {
				return s + strings.Repeat(" ", maxWidth-textWidth)
			}
			return s
		}

		clipped, clippedWidth := truncateFragmentToWidth(ellipsis, maxWidth)
		if clippedWidth == 0 {
			if pad {
				return strings.Repeat(" ", maxWidth)
			}
			return ""
		}
		foreground := getActiveForegroundAnsi(leadingTerminalCodes(s))
		return finalizeTruncatedResult("", 0, clipped, clippedWidth, maxWidth, pad, foreground)
	}

	if IsPrintableASCII(s) {
		if len(s) <= maxWidth {
			if pad {
				return s + strings.Repeat(" ", maxWidth-len(s))
			}
			return s
		}
		targetWidth := maxWidth - ellipsisWidth
		return finalizeTruncatedResult(s[:targetWidth], targetWidth, ellipsis, ellipsisWidth, maxWidth, pad, "")
	}

	targetWidth := maxWidth - ellipsisWidth
	var result strings.Builder
	pendingAnsi := ""
	visibleSoFar := 0
	keptWidth := 0
	keepContiguousPrefix := true
	overflowed := false
	exhaustedInput := false
	hasAnsi := strings.Contains(s, "\x1b")
	hasTabs := strings.Contains(s, "\t")

	if !hasAnsi && !hasTabs {
		for _, segment := range Graphemes(s) {
			width := GraphemeWidth(segment)
			if keepContiguousPrefix && keptWidth+width <= targetWidth {
				result.WriteString(segment)
				keptWidth += width
			} else {
				keepContiguousPrefix = false
			}
			visibleSoFar += width
			if visibleSoFar > maxWidth {
				overflowed = true
				break
			}
		}
		exhaustedInput = !overflowed
	} else {
		i := 0
		for i < len(s) {
			if code, n, ok := ExtractAnsiCode(s, i); ok {
				pendingAnsi += code
				i += n
				continue
			}

			if s[i] == '\t' {
				if keepContiguousPrefix && keptWidth+3 <= targetWidth {
					if pendingAnsi != "" {
						result.WriteString(pendingAnsi)
						pendingAnsi = ""
					}
					result.WriteByte('\t')
					keptWidth += 3
				} else {
					keepContiguousPrefix = false
					pendingAnsi = ""
				}
				visibleSoFar += 3
				if visibleSoFar > maxWidth {
					overflowed = true
					break
				}
				i++
				continue
			}

			end := i
			for end < len(s) && s[end] != '\t' {
				if _, _, ok := ExtractAnsiCode(s, end); ok {
					break
				}
				end++
			}

			for _, segment := range Graphemes(s[i:end]) {
				width := GraphemeWidth(segment)
				if keepContiguousPrefix && keptWidth+width <= targetWidth {
					if pendingAnsi != "" {
						result.WriteString(pendingAnsi)
						pendingAnsi = ""
					}
					result.WriteString(segment)
					keptWidth += width
				} else {
					keepContiguousPrefix = false
					pendingAnsi = ""
				}

				visibleSoFar += width
				if visibleSoFar > maxWidth {
					overflowed = true
					break
				}
			}
			if overflowed {
				break
			}
			i = end
		}
		exhaustedInput = i >= len(s)
	}

	if !overflowed && exhaustedInput {
		if pad {
			return s + strings.Repeat(" ", maxInt(0, maxWidth-visibleSoFar))
		}
		return s
	}

	return finalizeTruncatedResult(result.String(), keptWidth, ellipsis, ellipsisWidth, maxWidth, pad, "")
}
