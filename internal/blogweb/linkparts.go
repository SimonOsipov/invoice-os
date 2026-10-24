package blogweb

import (
	"regexp"
	"strings"
)

// Part is a run of post text; Href is set for an http(s) address.
type Part struct{ Text, Href string }

var address = regexp.MustCompile(`(?i)https?://[^\s<>"]+`)

// Mirrors packages/api-client/src/linkParts.ts.
func linkParts(s string) []Part {
	var out []Part
	at := 0
	for _, m := range address.FindAllStringIndex(s, -1) {
		a := trimAddress(s[m[0]:m[1]])
		if len(a) <= strings.Index(a, "//")+2 {
			continue
		}
		if m[0] > at {
			out = append(out, Part{Text: s[at:m[0]]})
		}
		out = append(out, Part{Text: a, Href: a})
		at = m[0] + len(a)
	}
	if at < len(s) {
		out = append(out, Part{Text: s[at:]})
	}
	return out
}

// Trailing sentence punctuation and an unmatched ")" stay outside the link.
func trimAddress(a string) string {
	for a != "" {
		last := a[len(a)-1]
		if !strings.ContainsRune(".,;:!?", rune(last)) && (last != ')' || strings.Count(a, ")") <= strings.Count(a, "(")) {
			break
		}
		a = a[:len(a)-1]
	}
	return a
}
