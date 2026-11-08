package platform

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanUserAgent_CutsAtTheBoundary(t *testing.T) {
	cases := []struct {
		name string
		ua   string
		want int
	}{
		{"511 runes kept", strings.Repeat("é", 511), 511},
		{"512 runes kept", strings.Repeat("é", 512), 512},
		{"513 runes cut", strings.Repeat("é", 513), 512},
		{"NUL dropped", "a\x00b", 2},
		{"empty", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanUserAgent(c.ua); utf8.RuneCountInString(got) != c.want || !utf8.ValidString(got) || strings.ContainsRune(got, 0) {
				t.Errorf("CleanUserAgent = %d runes %q, want %d valid runes with no NUL", utf8.RuneCountInString(got), got, c.want)
			}
		})
	}
}
