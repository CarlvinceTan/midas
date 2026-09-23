package text

import "testing"

// TestParseOsc8HyperlinkRejectsMalformedInput: the exported parser is documented
// for arbitrary sequences, and slicing an unterminated one used to panic.
func TestParseOsc8HyperlinkRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{"\x1b]8;", "\x1b]8;;", "\x1b]8;;http://example.com", "\x1b]8", "\x1b", ""} {
		link, isOsc8 := ParseOsc8Hyperlink(input)
		if isOsc8 && link != nil && link.URL == "" {
			t.Fatalf("input %q produced an empty link", input)
		}
	}
	// A well-formed sequence still parses, with both terminators.
	link, isOsc8 := ParseOsc8Hyperlink("\x1b]8;;https://example.com\x07")
	if !isOsc8 || link == nil || link.URL != "https://example.com" || link.Terminator != "\x07" {
		t.Fatalf("BEL-terminated link = %#v, %v", link, isOsc8)
	}
	link, isOsc8 = ParseOsc8Hyperlink("\x1b]8;id=1;https://example.com\x1b\\")
	if !isOsc8 || link == nil || link.URL != "https://example.com" || link.Params != "id=1" {
		t.Fatalf("ST-terminated link = %#v, %v", link, isOsc8)
	}
	if link, isOsc8 := ParseOsc8Hyperlink("\x1b]8;;\x07"); !isOsc8 || link != nil {
		t.Fatalf("close sequence = %#v, %v", link, isOsc8)
	}
}
