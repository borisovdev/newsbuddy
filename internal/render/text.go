package render

import "unicode"

// ellipsis is one rune and one UTF-16 code unit.
const ellipsis = "…" // U+2026

// utf16Len counts UTF-16 code units, matching Telegram's own counting
// : 1 per rune below U+10000, 2 for astral-plane runes.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// UTF16Len reports the length of s in UTF-16 code units, the unit Telegram
// counts message and entity lengths in.
func UTF16Len(s string) int {
	return utf16Len(s)
}

// truncateWords cuts s to at most maxRunes runes at a word boundary and
// appends "…". Operating on []rune makes splitting a surrogate pair
// structurally impossible.
func truncateWords(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	// Leave room for the ellipsis.
	cut := maxRunes - 1
	// Prefer the last word boundary, unless that discards more than 30% of
	// the budget.
	if i := lastSpace(runes[:cut]); i >= 0 && cut-i <= maxRunes*3/10 {
		cut = i
	}
	cut = alignCut(runes, cut)
	if cut == 0 {
		return ellipsis
	}
	return string(runes[:cut]) + ellipsis
}

// lastSpace returns the index of the last space rune in runes, or -1.
func lastSpace(runes []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if unicode.IsSpace(runes[i]) {
			return i
		}
	}
	return -1
}

// alignCut moves cut backwards off a grapheme cluster: if the first dropped
// rune is a combining mark, its base character is dropped too, then any
// trailing whitespace is removed.
func alignCut(runes []rune, cut int) int {
	for cut > 0 && cut < len(runes) && unicode.Is(unicode.Mn, runes[cut]) {
		cut--
	}
	for cut > 0 && unicode.IsSpace(runes[cut-1]) {
		cut--
	}
	return cut
}

// prefixOf returns the first n runes of runes, appending "…" when the value
// was actually shortened. It never cuts inside a grapheme cluster.
func prefixOf(runes []rune, n int) string {
	if n >= len(runes) {
		return string(runes)
	}
	if n <= 0 {
		return ""
	}
	n = alignCut(runes, n)
	if n == 0 {
		return ""
	}
	return string(runes[:n]) + ellipsis
}
