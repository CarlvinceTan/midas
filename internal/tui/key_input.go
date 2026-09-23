package tui

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

// KeyID is a key name with optional order-independent modifiers, such as
// "pageUp", "ctrl+c", or "shift+ctrl+d".
type KeyID string

const (
	modifierShift = 1
	modifierAlt   = 2
	modifierCtrl  = 4
	modifierSuper = 8
	modifierLocks = 64 + 128
)

const (
	codepointEscape    = 27
	codepointTab       = 9
	codepointEnter     = 13
	codepointSpace     = 32
	codepointBackspace = 127
	codepointKPEnter   = 57414

	codepointUp       = -1
	codepointDown     = -2
	codepointRight    = -3
	codepointLeft     = -4
	codepointDelete   = -10
	codepointInsert   = -11
	codepointPageUp   = -12
	codepointPageDown = -13
	codepointHome     = -14
	codepointEnd      = -15
)

var kittyProtocolActive atomic.Bool

// SetKittyProtocolActive records whether ambiguous legacy key encodings should
// be interpreted under Kitty keyboard-protocol rules.
func SetKittyProtocolActive(active bool) { kittyProtocolActive.Store(active) }

// IsKittyProtocolActive reports the current keyboard-protocol mode.
func IsKittyProtocolActive() bool { return kittyProtocolActive.Load() }

var (
	kittyCSIUPattern       = regexp.MustCompile(`^\x1b\[(\d+)(?::(\d*))?(?::(\d+))?(?:;(\d+))?(?::(\d+))?u$`)
	kittyArrowPattern      = regexp.MustCompile(`^\x1b\[1;(\d+)(?::(\d+))?([ABCD])$`)
	kittyFunctionalPattern = regexp.MustCompile(`^\x1b\[(\d+)(?:;(\d+))?(?::(\d+))?~$`)
	kittyHomeEndPattern    = regexp.MustCompile(`^\x1b\[1;(\d+)(?::(\d+))?([HF])$`)
	modifyOtherKeysPattern = regexp.MustCompile(`^\x1b\[27;(\d+);(\d+)~$`)
)

var symbolKeys = map[byte]struct{}{}

func init() {
	for index := 0; index < len("`-=[]\\;',./!@#$%^&*()_+|~{}:<>?"); index++ {
		symbolKeys["`-=[]\\;',./!@#$%^&*()_+|~{}:<>?"[index]] = struct{}{}
	}
}

var kittyFunctionalKeyEquivalents = map[int]int{
	57399: '0', 57400: '1', 57401: '2', 57402: '3', 57403: '4',
	57404: '5', 57405: '6', 57406: '7', 57407: '8', 57408: '9',
	57409: '.', 57410: '/', 57411: '*', 57412: '-', 57413: '+',
	57415: '=', 57416: ',', 57417: codepointLeft, 57418: codepointRight,
	57419: codepointUp, 57420: codepointDown, 57421: codepointPageUp,
	57422: codepointPageDown, 57423: codepointHome, 57424: codepointEnd,
	57425: codepointInsert, 57426: codepointDelete,
}

var legacyKeySequences = map[string][]string{
	"up":       {"\x1b[A", "\x1bOA"},
	"down":     {"\x1b[B", "\x1bOB"},
	"right":    {"\x1b[C", "\x1bOC"},
	"left":     {"\x1b[D", "\x1bOD"},
	"home":     {"\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~"},
	"end":      {"\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~"},
	"insert":   {"\x1b[2~"},
	"delete":   {"\x1b[3~"},
	"pageup":   {"\x1b[5~", "\x1b[[5~"},
	"pagedown": {"\x1b[6~", "\x1b[[6~"},
	"clear":    {"\x1b[E", "\x1bOE"},
	"f1":       {"\x1bOP", "\x1b[11~", "\x1b[[A"},
	"f2":       {"\x1bOQ", "\x1b[12~", "\x1b[[B"},
	"f3":       {"\x1bOR", "\x1b[13~", "\x1b[[C"},
	"f4":       {"\x1bOS", "\x1b[14~", "\x1b[[D"},
	"f5":       {"\x1b[15~", "\x1b[[E"},
	"f6":       {"\x1b[17~"},
	"f7":       {"\x1b[18~"},
	"f8":       {"\x1b[19~"},
	"f9":       {"\x1b[20~"},
	"f10":      {"\x1b[21~"},
	"f11":      {"\x1b[23~"},
	"f12":      {"\x1b[24~"},
}

var legacyShiftSequences = map[string][]string{
	"up": {"\x1b[a"}, "down": {"\x1b[b"}, "right": {"\x1b[c"}, "left": {"\x1b[d"},
	"clear": {"\x1b[e"}, "insert": {"\x1b[2$"}, "delete": {"\x1b[3$"},
	"pageup": {"\x1b[5$"}, "pagedown": {"\x1b[6$"}, "home": {"\x1b[7$"}, "end": {"\x1b[8$"},
}

var legacyCtrlSequences = map[string][]string{
	"up": {"\x1bOa"}, "down": {"\x1bOb"}, "right": {"\x1bOc"}, "left": {"\x1bOd"},
	"clear": {"\x1bOe"}, "insert": {"\x1b[2^"}, "delete": {"\x1b[3^"},
	"pageup": {"\x1b[5^"}, "pagedown": {"\x1b[6^"}, "home": {"\x1b[7^"}, "end": {"\x1b[8^"},
}

type parsedKittySequence struct {
	codepoint     int
	baseLayoutKey int
	hasBaseLayout bool
	modifier      int
}

func parseKittySequence(data string) (parsedKittySequence, bool) {
	if match := kittyCSIUPattern.FindStringSubmatch(data); len(match) == 6 {
		codepoint, _ := strconv.Atoi(match[1])
		modifierValue := 1
		if match[4] != "" {
			modifierValue, _ = strconv.Atoi(match[4])
		}
		parsed := parsedKittySequence{codepoint: codepoint, modifier: modifierValue - 1}
		if match[3] != "" {
			parsed.baseLayoutKey, _ = strconv.Atoi(match[3])
			parsed.hasBaseLayout = true
		}
		return parsed, true
	}
	if match := kittyArrowPattern.FindStringSubmatch(data); len(match) == 4 {
		modifierValue, _ := strconv.Atoi(match[1])
		codes := map[string]int{"A": codepointUp, "B": codepointDown, "C": codepointRight, "D": codepointLeft}
		return parsedKittySequence{codepoint: codes[match[3]], modifier: modifierValue - 1}, true
	}
	if match := kittyFunctionalPattern.FindStringSubmatch(data); len(match) == 4 {
		keyNumber, _ := strconv.Atoi(match[1])
		codes := map[int]int{2: codepointInsert, 3: codepointDelete, 5: codepointPageUp, 6: codepointPageDown, 7: codepointHome, 8: codepointEnd}
		codepoint, ok := codes[keyNumber]
		if !ok {
			return parsedKittySequence{}, false
		}
		modifierValue := 1
		if match[2] != "" {
			modifierValue, _ = strconv.Atoi(match[2])
		}
		return parsedKittySequence{codepoint: codepoint, modifier: modifierValue - 1}, true
	}
	if match := kittyHomeEndPattern.FindStringSubmatch(data); len(match) == 4 {
		modifierValue, _ := strconv.Atoi(match[1])
		codepoint := codepointEnd
		if match[3] == "H" {
			codepoint = codepointHome
		}
		return parsedKittySequence{codepoint: codepoint, modifier: modifierValue - 1}, true
	}
	return parsedKittySequence{}, false
}

func normalizeKittyFunctionalCodepoint(codepoint int) int {
	if equivalent, ok := kittyFunctionalKeyEquivalents[codepoint]; ok {
		return equivalent
	}
	return codepoint
}

func normalizeShiftedLetterCodepoint(codepoint, modifier int) int {
	if modifier&^modifierLocks&modifierShift != 0 && codepoint >= 'A' && codepoint <= 'Z' {
		return codepoint + ('a' - 'A')
	}
	return codepoint
}

func matchesKittySequence(data string, expectedCodepoint, expectedModifier int) bool {
	parsed, ok := parseKittySequence(data)
	if !ok || parsed.modifier&^modifierLocks != expectedModifier&^modifierLocks {
		return false
	}
	normalized := normalizeShiftedLetterCodepoint(normalizeKittyFunctionalCodepoint(parsed.codepoint), parsed.modifier)
	expected := normalizeShiftedLetterCodepoint(normalizeKittyFunctionalCodepoint(expectedCodepoint), expectedModifier)
	if normalized == expected {
		return true
	}
	if parsed.hasBaseLayout && parsed.baseLayoutKey == expectedCodepoint {
		_, symbol := symbolKeys[byte(normalized)]
		latin := normalized >= 'a' && normalized <= 'z'
		return !latin && !symbol
	}
	return false
}

func parseModifyOtherKeys(data string) (codepoint, modifier int, ok bool) {
	match := modifyOtherKeysPattern.FindStringSubmatch(data)
	if len(match) != 3 {
		return 0, 0, false
	}
	modifierValue, _ := strconv.Atoi(match[1])
	codepoint, _ = strconv.Atoi(match[2])
	return codepoint, modifierValue - 1, true
}

func matchesModifyOtherKeys(data string, expectedCodepoint, expectedModifier int) bool {
	codepoint, modifier, ok := parseModifyOtherKeys(data)
	return ok && codepoint == expectedCodepoint && modifier == expectedModifier
}

func matchesPrintableModifyOtherKeys(data string, expectedCodepoint, expectedModifier int) bool {
	if expectedModifier == 0 {
		return false
	}
	codepoint, modifier, ok := parseModifyOtherKeys(data)
	if !ok || modifier != expectedModifier {
		return false
	}
	return normalizeShiftedLetterCodepoint(codepoint, modifier) == normalizeShiftedLetterCodepoint(expectedCodepoint, expectedModifier)
}

func matchesLegacySequence(data string, sequences []string) bool {
	for _, sequence := range sequences {
		if data == sequence {
			return true
		}
	}
	return false
}

func matchesLegacyModifierSequence(data, key string, modifier int) bool {
	if modifier == modifierShift {
		return matchesLegacySequence(data, legacyShiftSequences[key])
	}
	if modifier == modifierCtrl {
		return matchesLegacySequence(data, legacyCtrlSequences[key])
	}
	return false
}

func rawCtrlCharacter(key byte) (string, bool) {
	char := key
	if char >= 'A' && char <= 'Z' {
		char += 'a' - 'A'
	}
	if (char >= 'a' && char <= 'z') || char == '[' || char == '\\' || char == ']' || char == '_' {
		return string([]byte{char & 0x1f}), true
	}
	if char == '-' {
		return "\x1f", true
	}
	return "", false
}

type parsedKeyID struct {
	key                     string
	ctrl, shift, alt, super bool
}

func parseKeyID(keyID KeyID) (parsedKeyID, bool) {
	parts := strings.Split(strings.ToLower(string(keyID)), "+")
	key := parts[len(parts)-1]
	if key == "" {
		return parsedKeyID{}, false
	}
	parsed := parsedKeyID{key: key}
	for _, part := range parts {
		switch part {
		case "ctrl":
			parsed.ctrl = true
		case "shift":
			parsed.shift = true
		case "alt":
			parsed.alt = true
		case "super":
			parsed.super = true
		}
	}
	return parsed, true
}

func isWindowsTerminalSession() bool {
	return os.Getenv("WT_SESSION") != "" && os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_CLIENT") == "" && os.Getenv("SSH_TTY") == ""
}

func matchesRawBackspace(data string, expectedModifier int) bool {
	if data == "\x7f" {
		return expectedModifier == 0
	}
	if data != "\x08" {
		return false
	}
	if isWindowsTerminalSession() {
		return expectedModifier == modifierCtrl
	}
	return expectedModifier == 0
}

// MatchesKey matches one terminal input event against a key identifier.
func MatchesKey(data string, keyID KeyID) bool {
	parsed, ok := parseKeyID(keyID)
	if !ok {
		return false
	}
	key := parsed.key
	modifier := 0
	if parsed.shift {
		modifier |= modifierShift
	}
	if parsed.alt {
		modifier |= modifierAlt
	}
	if parsed.ctrl {
		modifier |= modifierCtrl
	}
	if parsed.super {
		modifier |= modifierSuper
	}

	switch key {
	case "escape", "esc":
		return modifier == 0 && (data == "\x1b" || matchesKittySequence(data, codepointEscape, 0) || matchesModifyOtherKeys(data, codepointEscape, 0))
	case "space":
		if !IsKittyProtocolActive() {
			if modifier == modifierCtrl && data == "\x00" {
				return true
			}
			if modifier == modifierAlt && data == "\x1b " {
				return true
			}
		}
		if modifier == 0 {
			return data == " " || matchesKittySequence(data, codepointSpace, 0) || matchesModifyOtherKeys(data, codepointSpace, 0)
		}
		return matchesKittySequence(data, codepointSpace, modifier) || matchesModifyOtherKeys(data, codepointSpace, modifier)
	case "tab":
		if modifier == modifierShift {
			return data == "\x1b[Z" || matchesKittySequence(data, codepointTab, modifierShift) || matchesModifyOtherKeys(data, codepointTab, modifierShift)
		}
		if modifier == 0 {
			return data == "\t" || matchesKittySequence(data, codepointTab, 0)
		}
		return matchesKittySequence(data, codepointTab, modifier) || matchesModifyOtherKeys(data, codepointTab, modifier)
	case "enter", "return":
		if modifier == modifierShift {
			if matchesKittySequence(data, codepointEnter, modifierShift) || matchesKittySequence(data, codepointKPEnter, modifierShift) || matchesModifyOtherKeys(data, codepointEnter, modifierShift) {
				return true
			}
			return IsKittyProtocolActive() && (data == "\x1b\r" || data == "\n")
		}
		if modifier == modifierAlt {
			if matchesKittySequence(data, codepointEnter, modifierAlt) || matchesKittySequence(data, codepointKPEnter, modifierAlt) || matchesModifyOtherKeys(data, codepointEnter, modifierAlt) {
				return true
			}
			return !IsKittyProtocolActive() && data == "\x1b\r"
		}
		if modifier == 0 {
			return data == "\r" || (!IsKittyProtocolActive() && data == "\n") || data == "\x1bOM" || matchesKittySequence(data, codepointEnter, 0) || matchesKittySequence(data, codepointKPEnter, 0)
		}
		return matchesKittySequence(data, codepointEnter, modifier) || matchesKittySequence(data, codepointKPEnter, modifier) || matchesModifyOtherKeys(data, codepointEnter, modifier)
	case "backspace":
		if modifier == modifierAlt {
			if data == "\x1b\x7f" || data == "\x1b\x08" {
				return true
			}
			return matchesKittySequence(data, codepointBackspace, modifierAlt) || matchesModifyOtherKeys(data, codepointBackspace, modifierAlt)
		}
		if modifier == modifierCtrl {
			return matchesRawBackspace(data, modifierCtrl) || matchesKittySequence(data, codepointBackspace, modifierCtrl) || matchesModifyOtherKeys(data, codepointBackspace, modifierCtrl)
		}
		if modifier == 0 {
			return matchesRawBackspace(data, 0) || matchesKittySequence(data, codepointBackspace, 0) || matchesModifyOtherKeys(data, codepointBackspace, 0)
		}
		return matchesKittySequence(data, codepointBackspace, modifier) || matchesModifyOtherKeys(data, codepointBackspace, modifier)
	case "insert", "delete", "home", "end", "pageup", "pagedown":
		codepoints := map[string]int{"insert": codepointInsert, "delete": codepointDelete, "home": codepointHome, "end": codepointEnd, "pageup": codepointPageUp, "pagedown": codepointPageDown}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences[key]) || matchesKittySequence(data, codepoints[key], 0)
		}
		return matchesLegacyModifierSequence(data, key, modifier) || matchesKittySequence(data, codepoints[key], modifier)
	case "clear":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences[key])
		}
		return matchesLegacyModifierSequence(data, key, modifier)
	case "up", "down", "left", "right":
		codepoints := map[string]int{"up": codepointUp, "down": codepointDown, "left": codepointLeft, "right": codepointRight}
		if modifier == modifierAlt {
			legacyAlt := map[string][]string{
				"up": {"\x1bp"}, "down": {"\x1bn"},
				"left": {"\x1b[1;3D", "\x1bb"}, "right": {"\x1b[1;3C", "\x1bf"},
			}
			if !IsKittyProtocolActive() {
				if key == "left" {
					legacyAlt[key] = append(legacyAlt[key], "\x1bB")
				} else if key == "right" {
					legacyAlt[key] = append(legacyAlt[key], "\x1bF")
				}
			}
			return matchesLegacySequence(data, legacyAlt[key]) || matchesKittySequence(data, codepoints[key], modifierAlt)
		}
		if modifier == modifierCtrl && (key == "left" || key == "right") {
			explicit := "\x1b[1;5D"
			if key == "right" {
				explicit = "\x1b[1;5C"
			}
			return data == explicit || matchesLegacyModifierSequence(data, key, modifierCtrl) || matchesKittySequence(data, codepoints[key], modifierCtrl)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences[key]) || matchesKittySequence(data, codepoints[key], 0)
		}
		return matchesLegacyModifierSequence(data, key, modifier) || matchesKittySequence(data, codepoints[key], modifier)
	case "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		return modifier == 0 && matchesLegacySequence(data, legacyKeySequences[key])
	}

	if len(key) != 1 {
		return false
	}
	character := key[0]
	_, symbol := symbolKeys[character]
	isLetter := character >= 'a' && character <= 'z'
	isDigit := character >= '0' && character <= '9'
	if !isLetter && !isDigit && !symbol {
		return false
	}
	rawCtrl, hasRawCtrl := rawCtrlCharacter(character)
	if modifier == modifierCtrl+modifierAlt && !IsKittyProtocolActive() && hasRawCtrl && data == "\x1b"+rawCtrl {
		return true
	}
	if modifier == modifierAlt && !IsKittyProtocolActive() && data == "\x1b"+key {
		return true
	}
	if modifier == modifierCtrl {
		if hasRawCtrl && data == rawCtrl {
			return true
		}
		return matchesKittySequence(data, int(character), modifierCtrl) || matchesPrintableModifyOtherKeys(data, int(character), modifierCtrl)
	}
	if modifier == modifierShift+modifierCtrl {
		return matchesKittySequence(data, int(character), modifier) || matchesPrintableModifyOtherKeys(data, int(character), modifier)
	}
	if modifier == modifierShift {
		if isLetter && data == strings.ToUpper(key) {
			return true
		}
		return matchesKittySequence(data, int(character), modifierShift) || matchesPrintableModifyOtherKeys(data, int(character), modifierShift)
	}
	if modifier != 0 {
		return matchesKittySequence(data, int(character), modifier) || matchesPrintableModifyOtherKeys(data, int(character), modifier)
	}
	return data == key || matchesKittySequence(data, int(character), 0)
}

// IsKeyRelease recognizes Kitty event-type 3 without misclassifying bracketed
// paste contents.
func IsKeyRelease(data string) bool {
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	for _, suffix := range []string{":3u", ":3~", ":3A", ":3B", ":3C", ":3D", ":3H", ":3F"} {
		if strings.Contains(data, suffix) {
			return true
		}
	}
	return false
}

// IsKeyRepeat recognizes Kitty event-type 2. Destructive one-shot actions such
// as explicit queue dequeue should ignore repeats from a held key.
func IsKeyRepeat(data string) bool {
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	for _, suffix := range []string{":2u", ":2~", ":2A", ":2B", ":2C", ":2D", ":2H", ":2F"} {
		if strings.Contains(data, suffix) {
			return true
		}
	}
	return false
}

// MatchesDebugKey matches the reference's global shift+ctrl+d binding.
func MatchesDebugKey(data string) bool { return MatchesKey(data, KeyID("shift+ctrl+d")) }
