package tui

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// AttachmentMarkerPattern matches the atomic prompt chips the editor inserts
// for pasted files, e.g. `[Image: screenshot.png]` / `[File: report.pdf]`.
var AttachmentMarkerPattern = regexp.MustCompile(`\[(?:Image|File): [^\]\n]*\]`)

// attachmentFileNamePattern mirrors the pre-port rule that a bare multi-word
// candidate is only a path when its basename ends in a filename extension. It
// separates `/Users/me/Screenshot 2026.png` from prose such as
// `/Users/me/report.txt please`.
var attachmentFileNamePattern = regexp.MustCompile(`\.[A-Za-z0-9]{1,10}$`)

var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".heic": true, ".heif": true, ".tif": true, ".tiff": true,
}

// StyleAttachmentMarkers paints every `[Image: …]` / `[File: …]` chip yellow,
// matching the editor's chips. Non-marker text is returned untouched so the
// caller keeps its own styling.
// `restore` names the color the surrounding text uses, so the line keeps its
// own styling after the chip; it is empty when the caller renders unstyled text.
func StyleAttachmentMarkers(theme *Theme, value, restore string) string {
	if theme == nil || !strings.Contains(value, "[") {
		return value
	}
	suffix := ""
	if restore != "" {
		suffix = theme.FGPrefix(restore)
	}
	return AttachmentMarkerPattern.ReplaceAllStringFunc(value, func(marker string) string {
		return theme.FG("toolTitle", marker) + suffix
	})
}

// AttachmentMarkers lists the chips present in text, in order.
func AttachmentMarkers(value string) []string {
	return AttachmentMarkerPattern.FindAllString(value, -1)
}

// AttachmentMarker builds the chip label for one absolute path. Image files get
// the `[Image: …]` marker, everything else `[File: …]`.
func AttachmentMarker(filePath string) (string, bool) {
	name := attachmentDisplayName(filePath)
	if name == "" {
		return "", false
	}
	image := imageExtensions[strings.ToLower(filepath.Ext(name))]
	if image {
		return "[Image: " + name + "]", true
	}
	return "[File: " + name + "]", false
}

func attachmentDisplayName(filePath string) string {
	name := filepath.Base(filePath)
	// macOS screenshot names carry a narrow no-break space before am/pm, which
	// renders at a width that disagrees with its measurement. Display it as a
	// normal space; the source path is unaffected.
	name = strings.NewReplacer(
		"\u00a0", " ", "\u1680", " ", "\u202f", " ", "\u205f", " ", "\u3000", " ",
	).Replace(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	return strings.TrimSpace(name)
}

// ParsePastedPaths normalizes a clipboard paste into absolute paths, but only
// when the whole paste is unambiguously path-only. It supports one path per
// line, quoted paths, shell backslash escapes, spaces and Unicode, plus `~/`
// and `file://` forms. Anything containing prose, a relative path or a URL is
// refused, so a pasted sentence never attaches an arbitrary mentioned path.
func ParsePastedPaths(text, cwd string) ([]string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\r", "\n"), "\n")
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		candidates := []string{line}
		if strings.Contains(line, "\\") || structuralQuotePattern.MatchString(line) {
			tokens, ok := tokenizeShellLine(line)
			if !ok {
				return nil, false
			}
			candidates = tokens
		}
		if len(candidates) == 0 {
			return nil, false
		}
		for _, candidate := range candidates {
			resolved, ok := resolvePastedToken(candidate, cwd)
			if !ok {
				return nil, false
			}
			paths = append(paths, resolved)
		}
	}
	if len(paths) == 0 {
		return nil, false
	}
	return paths, true
}

// A quote only starts a shell word at the beginning of a line or after
// whitespace, so an apostrophe inside a filename is not mistaken for quoting.
var structuralQuotePattern = regexp.MustCompile(`(^|\s)['"]`)

func tokenizeShellLine(line string) ([]string, bool) {
	tokens := make([]string, 0, 2)
	var current strings.Builder
	started := false
	var quote rune
	runes := []rune(line)
	for index := 0; index < len(runes); index++ {
		ch := runes[index]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			} else {
				current.WriteRune(ch)
			}
			started = true
		case quote == '"':
			switch ch {
			case '"':
				quote = 0
			case '\\':
				if index+1 >= len(runes) {
					return nil, false
				}
				index++
				current.WriteRune(runes[index])
			default:
				current.WriteRune(ch)
			}
			started = true
		case ch == '\\':
			if index+1 >= len(runes) {
				return nil, false
			}
			index++
			current.WriteRune(runes[index])
			started = true
		case ch == '\'' || ch == '"':
			quote = ch
			started = true
		case ch == ' ' || ch == '\t':
			if started {
				tokens = append(tokens, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(ch)
			started = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		tokens = append(tokens, current.String())
	}
	return tokens, true
}

func resolvePastedToken(token, cwd string) (string, bool) {
	if token == "" || strings.ContainsRune(token, 0) {
		return "", false
	}
	if strings.ContainsAny(token, " \t") && !attachmentFileNamePattern.MatchString(path.Base(token)) {
		return "", false
	}
	var expanded string
	switch {
	case strings.HasPrefix(token, "file://"):
		parsed, err := url.Parse(token)
		if err != nil || parsed.Path == "" {
			return "", false
		}
		expanded = parsed.Path
	case strings.HasPrefix(token, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		expanded = filepath.Join(home, token[2:])
	case strings.HasPrefix(token, "/"):
		expanded = token
	default:
		return "", false
	}
	if !filepath.IsAbs(expanded) || strings.ContainsRune(expanded, 0) {
		return "", false
	}
	// Only absolute inputs qualify, so the working directory never changes the
	// resolved path; it is kept in the signature for parity with the pre-port
	// parser.
	_ = cwd
	return filepath.Clean(expanded), true
}

// InsertAttachmentChips converts a path-only paste into chip markers and the
// absolute path each marker stands for. It returns false when the paste is not
// unambiguously a set of file paths.
func InsertAttachmentChips(text, cwd string) (marker string, paths map[string]string, ok bool) {
	values, ok := ParsePastedPaths(text, cwd)
	if !ok {
		return "", nil, false
	}
	paths = make(map[string]string, len(values))
	labels := make([]string, 0, len(values))
	for _, value := range values {
		label, _ := AttachmentMarker(value)
		if label == "" {
			return "", nil, false
		}
		if _, exists := paths[label]; exists {
			// Identical basenames would make the markers ambiguous; fall back to
			// plain text so no chip silently points at the wrong file.
			return "", nil, false
		}
		paths[label] = value
		labels = append(labels, label)
	}
	return strings.Join(labels, " ") + " ", paths, true
}

// ExpandAttachmentPaths replaces chip markers with the absolute path they were
// pasted from, so the agent can read the real file. Markers without a known
// path (typed by hand or restored from a draft) are left as-is.
func ExpandAttachmentPaths(value string, paths map[string]string) string {
	if len(paths) == 0 {
		return value
	}
	return AttachmentMarkerPattern.ReplaceAllStringFunc(value, func(marker string) string {
		if path, ok := paths[marker]; ok {
			return path
		}
		return marker
	})
}
