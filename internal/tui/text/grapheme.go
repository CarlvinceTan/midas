package text

import "github.com/rivo/uniseg"

// Grapheme segmentation.
//
// The reference delegates to V8's Intl.Segmenter, which implements UAX #29 at
// the Unicode version bundled with the runtime. uniseg implements the same
// algorithm but predates rule GB9c, so it breaks Indic conjuncts apart:
//
//	ICU:   ["न" "म" "स्ते"]
//	uniseg:["न" "म" "स्" "ते"]
//
// The fix is a merge pass that suppresses exactly the boundaries GB9c forbids,
// driven by Indic_Conjunct_Break tables generated from the Unicode character
// database. Agreement is asserted by differential tests, not assumed.

func isIndicLinker(cp rune) bool    { return inRanges(indicLinkerRanges, cp) }
func isIndicConsonant(cp rune) bool { return inRanges(indicConsonantRanges, cp) }
func isIndicExtend(cp rune) bool    { return inRanges(indicExtendRanges, cp) }

func isIndicExtendOrLinker(cp rune) bool { return isIndicExtend(cp) || isIndicLinker(cp) }

// unisegClusters returns the raw uniseg segmentation of s.
func unisegClusters(s string) []string {
	g := uniseg.NewGraphemes(s)
	var out []string
	for g.Next() {
		out = append(out, g.Str())
	}
	return out
}

// Graphemes returns the grapheme clusters of s, matching the reference's
// Intl.Segmenter output.
func Graphemes(s string) []string {
	raw := unisegClusters(s)
	if len(raw) < 2 {
		return raw
	}

	// Flatten to runes, remembering where each raw cluster starts.
	runes := make([]rune, 0, len(s))
	starts := make([]int, 0, len(raw)+1)
	for _, cluster := range raw {
		starts = append(starts, len(runes))
		runes = append(runes, []rune(cluster)...)
	}
	starts = append(starts, len(runes))

	merged := make([]string, 0, len(raw))
	currentStart := 0
	for k := 1; k < len(raw); k++ {
		i := starts[k]
		// Keep the break unless GB9c forbids it.
		if i > 0 && i < len(runes) && isIndicConsonant(runes[i]) && gb9cLeftOK(runes, i) {
			continue
		}
		merged = append(merged, string(runes[starts[currentStart]:i]))
		currentStart = k
	}
	merged = append(merged, string(runes[starts[currentStart]:]))
	return merged
}

// gb9cLeftOK reports whether the runes immediately before i end with
// Consonant [Extend Linker]* Linker [Extend Linker]* — the left-hand side of
// rule GB9c.
func gb9cLeftOK(runes []rune, i int) bool {
	j := i - 1
	hasLinker := false
	for j >= 0 && isIndicExtendOrLinker(runes[j]) {
		if isIndicLinker(runes[j]) {
			hasLinker = true
		}
		j--
	}
	return hasLinker && j >= 0 && isIndicConsonant(runes[j])
}
