package render

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"bmp cyrillic", "привет", 6},
		{"ellipsis", ellipsis, 1},
		{"astral emoji counts two", "😀", 2},
		{"mixed", "a😀b", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16Len(tc.in)
			if got != tc.want {
				t.Fatalf("UTF16Len(%q) = %d, want %d", tc.in, got, tc.want)
			}
			if want := len(utf16.Encode([]rune(tc.in))); got != want {
				t.Fatalf("UTF16Len(%q) = %d, utf16.Encode says %d", tc.in, got, want)
			}
			if n := utf16Len(tc.in); n != tc.want {
				t.Fatalf("utf16Len(%q) = %d, want %d", tc.in, n, tc.want)
			}
		})
	}
}

func TestTruncateWords(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"short enough", "hello", 10, "hello"},
		{"exact fit", "hello", 5, "hello"},
		{"zero budget", "hello", 0, ""},
		{"negative budget", "hello", -3, ""},
		{"word boundary", "alpha beta gamma", 12, "alpha beta" + ellipsis},
		{"hard cut when boundary too costly", "alpha betagammadelta", 12, "alpha betag" + ellipsis},
		{"trailing space removed", "alpha      beta", 8, "alpha" + ellipsis},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateWords(tc.in, tc.maxRunes)
			if got != tc.want {
				t.Fatalf("truncateWords(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
			}
			if n := utf8.RuneCountInString(got); tc.maxRunes > 0 && n > tc.maxRunes {
				t.Fatalf("result %q is %d runes, over budget %d", got, n, tc.maxRunes)
			}
		})
	}
}

// Edge 32: astral-plane runes near the cut are never split mid-surrogate-pair,
// and combining marks are never separated from their base character.
func TestTruncateWords_Edge32_NoSplitAstralOrCluster(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"cut lands on emoji", "aa 😀😀😀😀", 5, "aa 😀" + ellipsis},
		{"emoji run hard cut", "😀😀😀😀😀😀", 3, "😀😀" + ellipsis},
		{"emoji after word boundary", "hi 😀😀😀😀😀", 6, "hi 😀😀" + ellipsis},
		{"combining mark base dropped", "abcdéfg", 6, "abcd" + ellipsis},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateWords(tc.in, tc.maxRunes)
			if got != tc.want {
				t.Fatalf("truncateWords(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
			}
			assertNoSplitRune(t, got)
			if strings.Count(got, "😀") != strings.Count(tc.want, "😀") {
				t.Fatalf("emoji count changed: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestPrefixOf(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"whole string", "hello", 5, "hello"},
		{"beyond length", "hello", 99, "hello"},
		{"zero", "hello", 0, ""},
		{"negative", "hello", -1, ""},
		{"shortened gets ellipsis", "hello world", 5, "hello" + ellipsis},
		{"trailing space trimmed", "hello world", 6, "hello" + ellipsis},
		{"astral not split", "a😀b", 2, "a😀" + ellipsis},
		{"combining mark base dropped", "aéb", 2, "a" + ellipsis},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixOf([]rune(tc.in), tc.n)
			if got != tc.want {
				t.Fatalf("prefixOf(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			assertNoSplitRune(t, got)
		})
	}
}

// assertNoSplitRune fails if s contains a replacement rune or does not survive
// a UTF-16 round trip — either would mean a code point was cut in half.
func assertNoSplitRune(t *testing.T, s string) {
	t.Helper()
	if !utf8.ValidString(s) || strings.ContainsRune(s, utf8.RuneError) {
		t.Fatalf("invalid or replacement rune in %q", s)
	}
	if back := string(utf16.Decode(utf16.Encode([]rune(s)))); back != s {
		t.Fatalf("UTF-16 round trip changed %q into %q", s, back)
	}
}
