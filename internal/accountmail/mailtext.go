package accountmail

import (
	"strings"
	"unicode"
)

// NameChars is the most runes of a visitor-written name a branded mail shows.
const NameChars = 60

const zws = "​"

// Unlinked puts U+200B at each link start (domain dot, address @, ://) so clients do not auto-link; stripping the marks gives back the input.
func Unlinked(s string) string {
	r := []rune(strings.ReplaceAll(s, zws, ""))
	alnum := func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }
	var b strings.Builder
	for i, c := range r {
		if c == ':' && i+2 < len(r) && r[i+1] == '/' && r[i+2] == '/' {
			b.WriteString(zws)
		}
		b.WriteRune(c)
		switch {
		case c == '.' && i > 0 && alnum(r[i-1]) && i+2 < len(r) && unicode.IsLetter(r[i+1]) && unicode.IsLetter(r[i+2]),
			c == '@' && i > 0 && i+1 < len(r) && alnum(r[i-1]) && alnum(r[i+1]):
			b.WriteString(zws)
		}
	}
	return b.String()
}

// MailName strips marks, cuts to NameChars runes, then unlinks: the form a visitor-written name takes in a mail.
func MailName(s string) string {
	return Unlinked(cutRunes(strings.ReplaceAll(s, zws, ""), NameChars))
}

// cutRunes shortens s to n runes, with … as the last one.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
