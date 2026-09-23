package tui

import "testing"

func TestOneDarkPaletteMatchesPrePortMidas(t *testing.T) {
	want := map[string]string{
		"accent":          "#61afef",
		"borderAccent":    "#61afef",
		"borderMuted":     "#2c313c",
		"muted":           "#7f848e",
		"dim":             "#5c6370",
		"text":            "#abb2bf",
		"userMessageText": "#abb2bf",
		"startupHeading":  "#c678dd",
	}
	for name, expected := range want {
		if got := oneDarkColors[name]; got != expected {
			t.Errorf("%s = %q, want %q", name, got, expected)
		}
	}
}
