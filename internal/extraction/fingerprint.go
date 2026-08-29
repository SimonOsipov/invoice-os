// fingerprint.go: the layout fingerprint -- which anchor labels appear on page 1, in what
// reading order, and in which third of the page. Never supplier identity: that is not known
// until after extraction, and one supplier commonly has several layouts.
package extraction

import "regexp"

// TokenPage is one page's positioned text with no render attached: Fingerprint and the
// resolver never see a borrowed ImagePNG they could retain.
type TokenPage struct {
	Number   int
	WidthPt  float64
	HeightPt float64
	Tokens   []Token
}

// CollectTokens is the onPage callback that fills dst. The Tokens slice is taken as-is --
// PageReader builds a fresh one per page and does not reuse it. Unimplemented here; EXTR-04-03
// Stage 3 fills it in.
func CollectTokens(dst *[]TokenPage) func(Page) error {
	return func(Page) error { return nil }
}

// FingerprintVersion prefixes every fingerprint. Bumping it makes every stored rule stop
// matching, which is the intended invalidation: no migration, no stale-rule window.
const FingerprintVersion = "v1"

// lexiconEntry is one anchorLexicon pattern, compiled.
type lexiconEntry struct {
	ID string
	Re *regexp.Regexp
}

// fingerprintLexicon is anchorLexicon's compiled form, built once so Fingerprint does not
// recompile ten patterns on every call. TestAnchorLexicon_IsOrderedAndUnique (A-08) proves
// every pattern compiles, so MustCompile here cannot panic.
var fingerprintLexicon = compileFingerprintLexicon()

func compileFingerprintLexicon() []lexiconEntry {
	out := make([]lexiconEntry, len(anchorLexicon))
	for i, entry := range anchorLexicon {
		out[i] = lexiconEntry{ID: entry.ID, Re: regexp.MustCompile(entry.Pattern)}
	}
	return out
}

// Fingerprint identifies a page-1 layout by which anchor labels appear, in what reading order,
// and in which third of the page -- never by supplier identity. Unimplemented here; EXTR-04-03
// Stage 3 fills it in.
func Fingerprint(pages []TokenPage) string {
	return ""
}
