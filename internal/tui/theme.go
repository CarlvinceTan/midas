package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// ColorMode selects either 24-bit RGB or the nearest xterm-256 color.
type ColorMode string

const (
	ColorTrue ColorMode = "truecolor"
	Color256  ColorMode = "256color"
)

// Theme is Midas's terminal color palette. The default values intentionally
// match the existing One Dark interface so the Go UI can be compared directly.
type Theme struct {
	mode ColorMode
	fg   map[string]string
	bg   map[string]string
}

var oneDarkColors = map[string]string{
	"accent": "#61afef", "border": "#3e4452", "borderAccent": "#61afef", "borderMuted": "#2c313c",
	"success": "#98c379", "error": "#e06c75", "warning": "#e06c75", "muted": "#7f848e", "dim": "#5c6370",
	"text": "#abb2bf", "thinkingText": "#7f848e", "selectedBg": "#3e4451", "scrollbarTrack": "#21252b",
	"scrollbarThumb": "#3e4452", "searchMatchBg": "#d19a66", "searchMatchText": "#282c34",
	"userMessageBg": "#2c313c", "userMessageText": "#abb2bf", "customMessageBg": "#21252b",
	"customMessageText": "#abb2bf", "customMessageLabel": "#c678dd", "toolPendingBg": "#2c313c",
	"toolSuccessBg": "#2f3a2f", "toolErrorBg": "#3b2b2d", "toolTitle": "#e5c07b", "toolOutput": "#abb2bf",
	"mdHeading": "#d19a66", "mdBold": "#e5c07b", "mdLink": "#61afef", "mdLinkUrl": "#5c6370", "mdCode": "#98c379",
	"mdCodeBlock": "#abb2bf", "mdCodeBlockBorder": "#5c6370", "mdQuote": "#5c6370", "mdQuoteBorder": "#5c6370",
	"mdHr": "#3e4452", "mdListBullet": "#e5c07b", "toolDiffAdded": "#8ca485", "toolDiffRemoved": "#b87882",
	"toolDiffContext": "#7f848e", "syntaxComment": "#5c6370", "syntaxKeyword": "#c678dd",
	"syntaxFunction": "#61afef", "syntaxVariable": "#e06c75", "syntaxString": "#98c379",
	"syntaxNumber": "#d19a66", "syntaxType": "#e5c07b", "syntaxOperator": "#56b6c2", "syntaxPunctuation": "#abb2bf",
	"thinkingOff": "#c678dd", "thinkingMinimal": "#c678dd", "thinkingLow": "#c678dd", "thinkingMedium": "#c678dd",
	"thinkingHigh": "#c678dd", "thinkingXhigh": "#c678dd", "thinkingMax": "#c678dd", "bashMode": "#61afef",
	"startupHeading": "#c678dd",
}

var backgroundNames = map[string]bool{
	"selectedBg": true, "searchMatchBg": true, "userMessageBg": true, "customMessageBg": true,
	"toolPendingBg": true, "toolSuccessBg": true, "toolErrorBg": true,
}

func NewOneDarkTheme(mode ColorMode) *Theme {
	if mode == "" {
		mode = Color256
	}
	theme := &Theme{mode: mode, fg: make(map[string]string), bg: make(map[string]string)}
	for name, value := range oneDarkColors {
		if backgroundNames[name] {
			theme.bg[name] = colorANSI(value, mode, true)
		} else {
			theme.fg[name] = colorANSI(value, mode, false)
		}
	}
	return theme
}

var (
	defaultThemeMu sync.Mutex
	defaultTheme   *Theme
	defaultMode    ColorMode
)

func CurrentTheme() *Theme {
	mode := Color256
	if GetCapabilities().TrueColor {
		mode = ColorTrue
	}
	defaultThemeMu.Lock()
	defer defaultThemeMu.Unlock()
	if defaultTheme == nil || defaultMode != mode {
		defaultTheme, defaultMode = NewOneDarkTheme(mode), mode
	}
	return defaultTheme
}

// FGPrefix returns just the opening color code for a name, so a caller can
// restore that color after an inline style (for example an attachment chip)
// without emitting a reset of its own.
func (t *Theme) FGPrefix(name string) string { return t.fg[name] }

func (t *Theme) FG(name, value string) string {
	ansi := t.fg[name]
	if ansi == "" {
		return value
	}
	return ansi + value + "\x1b[39m"
}

func (t *Theme) BG(name, value string) string {
	ansi := t.bg[name]
	if ansi == "" {
		return value
	}
	return ansi + value + "\x1b[49m"
}

func (t *Theme) Thinking(level ai.ThinkingLevel, value string) string {
	return t.FG(thinkingThemeName(level), value)
}

func colorANSI(hex string, mode ColorMode, background bool) string {
	value := strings.TrimPrefix(hex, "#")
	if len(value) != 6 {
		return ""
	}
	raw, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return ""
	}
	r, g, b := int(raw>>16), int(raw>>8&0xff), int(raw&0xff)
	if mode == ColorTrue {
		if background {
			return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
		}
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	}
	code := rgbTo256(r, g, b)
	if background {
		return fmt.Sprintf("\x1b[48;5;%dm", code)
	}
	return fmt.Sprintf("\x1b[38;5;%dm", code)
}

func rgbTo256(r, g, b int) int {
	cube := []int{0, 95, 135, 175, 215, 255}
	gray := make([]int, 24)
	for index := range gray {
		gray[index] = 8 + index*10
	}
	nearest := func(value int, values []int) int {
		best, bestDistance := 0, math.MaxFloat64
		for index, candidate := range values {
			distance := math.Abs(float64(value - candidate))
			if distance < bestDistance {
				best, bestDistance = index, distance
			}
		}
		return best
	}
	distance := func(r1, g1, b1, r2, g2, b2 int) float64 {
		dr, dg, db := float64(r1-r2), float64(g1-g2), float64(b1-b2)
		return dr*dr*.299 + dg*dg*.587 + db*db*.114
	}
	ri, gi, bi := nearest(r, cube), nearest(g, cube), nearest(b, cube)
	cubeIndex := 16 + 36*ri + 6*gi + bi
	cubeDistance := distance(r, g, b, cube[ri], cube[gi], cube[bi])
	grayValue := int(math.Floor(float64(299*r+587*g+114*b)/1000 + .5))
	grayIndex := nearest(grayValue, gray)
	grayDistance := distance(r, g, b, gray[grayIndex], gray[grayIndex], gray[grayIndex])
	spread := max(r, g, b) - min(r, g, b)
	if spread < 10 && grayDistance < cubeDistance {
		return 232 + grayIndex
	}
	return cubeIndex
}
