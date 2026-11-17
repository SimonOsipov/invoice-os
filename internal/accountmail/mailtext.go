package accountmail

import (
	"strings"
	"unicode"
)

// NameChars is the most runes of a visitor-written name a branded mail shows.
const NameChars = 60

// MailNamesMetadataKey is the GoTrue user_metadata key the confirmation mail reads.
const MailNamesMetadataKey = "mail_names"

const zws = "​"

// Unlinked puts U+200B at each link start (domain dot, address @, ://) so clients do not auto-link; stripping the marks gives back the input.
func Unlinked(s string) string {
	r := []rune(strings.ReplaceAll(s, zws, ""))
	// Cf runes (soft hyphen, ZWJ, word joiner) are invisible, so neighbours are read past them; a combining mark counts as a letter.
	letter := func(c rune) bool { return unicode.IsLetter(c) || unicode.Is(unicode.M, c) }
	alnum := func(c rune) bool { return letter(c) || unicode.IsDigit(c) }
	// near returns the n-th visible rune from i in direction d (+1 or -1), skipping Cf runes; -1 when none.
	near := func(i, d, n int) rune {
		for j := i + d; j >= 0 && j < len(r); j += d {
			if unicode.Is(unicode.Cf, r[j]) {
				continue
			}
			if n--; n == 0 {
				return r[j]
			}
		}
		return -1
	}
	var b strings.Builder
	for i, c := range r {
		if c == ':' && near(i, 1, 1) == '/' && near(i, 1, 2) == '/' {
			b.WriteString(zws)
		}
		b.WriteRune(c)
		switch {
		case c == '.' && alnum(near(i, -1, 1)) && letter(near(i, 1, 1)) && letter(near(i, 1, 2)),
			c == '@' && alnum(near(i, -1, 1)) && alnum(near(i, 1, 1)):
			b.WriteString(zws)
		}
	}
	return b.String()
}

// MailName strips marks, cuts to NameChars runes, then unlinks: the form a visitor-written name takes in a mail.
func MailName(s string) string {
	return Unlinked(cutRunes(strings.ReplaceAll(s, zws, ""), NameChars))
}

// MailNames is the user_metadata.mail_names value: both names in the form the confirmation mail shows.
func MailNames(workspace, display string) map[string]string {
	return map[string]string{"workspace_name": MailName(workspace), "display_name": MailName(display)}
}

// cutRunes shortens s to n runes, with … as the last one.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
