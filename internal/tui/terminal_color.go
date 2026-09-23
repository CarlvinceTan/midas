package tui

import (
	"regexp"
	"strconv"
	"strings"
)

// RGBColor is an eight-bit terminal color.
type RGBColor struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// TerminalColorScheme is the terminal's reported dark/light preference.
type TerminalColorScheme string

const (
	ColorSchemeDark  TerminalColorScheme = "dark"
	ColorSchemeLight TerminalColorScheme = "light"
)

var (
	osc11Pattern       = regexp.MustCompile(`(?i)^\x1b\]11;([^\x07\x1b]*)(?:\x07|\x1b\\)$`)
	colorSchemePattern = regexp.MustCompile(`^(?:\x1b\[\?997;(1|2)n)+$`)
	hex6Pattern        = regexp.MustCompile(`(?i)^[0-9a-f]{6}$`)
	hex12Pattern       = regexp.MustCompile(`(?i)^[0-9a-f]{12}$`)
	hexChannelPattern  = regexp.MustCompile(`(?i)^[0-9a-f]+$`)
)

// IsOSC11BackgroundColorResponse recognizes a complete OSC 11 reply.
func IsOSC11BackgroundColorResponse(data string) bool { return osc11Pattern.MatchString(data) }

// ParseOSC11BackgroundColor parses #RRGGBB, #RRRRGGGGBBBB, rgb:, and rgba:
// replies. The bool is false for a malformed but structurally complete value.
func ParseOSC11BackgroundColor(data string) (RGBColor, bool) {
	match := osc11Pattern.FindStringSubmatch(data)
	if len(match) != 2 {
		return RGBColor{}, false
	}
	value := strings.TrimSpace(match[1])
	if strings.HasPrefix(value, "#") {
		hex := value[1:]
		if hex6Pattern.MatchString(hex) {
			return RGBColor{
				R: parseHex(hex[0:2]),
				G: parseHex(hex[2:4]),
				B: parseHex(hex[4:6]),
			}, true
		}
		if hex12Pattern.MatchString(hex) {
			r, okR := parseOSCChannel(hex[0:4])
			g, okG := parseOSCChannel(hex[4:8])
			b, okB := parseOSCChannel(hex[8:12])
			if okR && okG && okB {
				return RGBColor{R: r, G: g, B: b}, true
			}
		}
		return RGBColor{}, false
	}

	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "rgba:") {
		value = value[5:]
	} else if strings.HasPrefix(lower, "rgb:") {
		value = value[4:]
	}
	channels := strings.Split(value, "/")
	if len(channels) < 3 {
		return RGBColor{}, false
	}
	r, okR := parseOSCChannel(channels[0])
	g, okG := parseOSCChannel(channels[1])
	b, okB := parseOSCChannel(channels[2])
	if !okR || !okG || !okB {
		return RGBColor{}, false
	}
	return RGBColor{R: r, G: g, B: b}, true
}

func parseHex(value string) int {
	parsed, _ := strconv.ParseInt(value, 16, 64)
	return int(parsed)
}

func parseOSCChannel(channel string) (int, bool) {
	if !hexChannelPattern.MatchString(channel) {
		return 0, false
	}
	parsed, err := strconv.ParseUint(channel, 16, 64)
	if err != nil {
		return 0, false
	}
	maximum := uint64(1)
	for range len(channel) {
		maximum *= 16
	}
	maximum--
	if maximum == 0 {
		return 0, false
	}
	return jsRound(float64(parsed) / float64(maximum) * 255), true
}

// ParseTerminalColorSchemeReport recognizes one or more complete DSR reports.
func ParseTerminalColorSchemeReport(data string) (TerminalColorScheme, bool) {
	match := colorSchemePattern.FindStringSubmatch(data)
	if len(match) != 2 {
		return "", false
	}
	if match[1] == "2" {
		return ColorSchemeLight, true
	}
	return ColorSchemeDark, true
}
