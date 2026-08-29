// fingerprint.go: the layout fingerprint -- which anchor labels appear on page 1, in what
// reading order, and in which third of the page. Never supplier identity: that is not known
// until after extraction, and one supplier commonly has several layouts.
package extraction

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TokenPage is one page's positioned text with no render attached: Fingerprint and the
// resolver never see a borrowed ImagePNG they could retain.
type TokenPage struct {
	Number   int
	WidthPt  float64
	HeightPt float64
	Tokens   []Token
}

// CollectTokens is the onPage callback that fills dst. The Tokens slice is taken as-is --
// PageReader builds a fresh one per page and does not reuse it. ImagePNG is borrowed for the
// length of the call, so nothing here copies or aliases it.
func CollectTokens(dst *[]TokenPage) func(Page) error {
	return func(p Page) error {
		*dst = append(*dst, TokenPage{
			Number:   p.Number,
			WidthPt:  p.WidthPt,
			HeightPt: p.HeightPt,
			Tokens:   p.Tokens,
		})
		return nil
	}
}

// FingerprintVersion prefixes every fingerprint. Bumping it makes every stored rule stop
// matching, which is the intended invalidation: no migration, no stale-rule window.
const FingerprintVersion = "v1"

// anchorMatcher is one anchorLexicon pattern, compiled.
type anchorMatcher struct {
	ID string
	RE *regexp.Regexp
}

// anchorLabelMatchers is anchorLexicon's compiled form, in the same order, built once so
// Fingerprint does not recompile ten patterns per call. TestAnchorLexicon_IsOrderedAndUnique
// proves every pattern compiles, so MustCompile here cannot panic.
var anchorLabelMatchers = compileAnchorLexicon()

func compileAnchorLexicon() []anchorMatcher {
	out := make([]anchorMatcher, len(anchorLexicon))
	for i, entry := range anchorLexicon {
		out[i] = anchorMatcher{ID: entry.ID, RE: regexp.MustCompile(entry.Pattern)}
	}
	return out
}

// labelObservation is one lexicon hit. The token's text is read by the pattern and then
// dropped, which is why no supplier name or TIN can reach the hash.
type labelObservation struct {
	y0, x0  float64
	labelID string
	band    int
}

// columnBand places a box in the left, middle or right third by its centre X. Ordered
// comparisons, so a coordinate outside [0,1] lands in an edge band instead of panicking.
func columnBand(r Region) int {
	cx := (r.X0 + r.X1) / 2
	switch {
	case cx < 1.0/3.0:
		return 0
	case cx < 2.0/3.0:
		return 1
	default:
		return 2
	}
}

// Fingerprint identifies a page-1 layout by which anchor labels appear, in what reading order,
// and in which third of the page. A page carrying no recognised label still fingerprints, to
// sha256(""): a real value that simply matches no stored rule.
func Fingerprint(pages []TokenPage) string {
	var obs []labelObservation
	for _, page := range pages {
		// Page 1 by Number, not by slice position: the caller's order is not the page order.
		if page.Number != 1 {
			continue
		}
		for _, tok := range page.Tokens {
			for _, m := range anchorLabelMatchers {
				if m.RE.MatchString(tok.Text) {
					obs = append(obs, labelObservation{
						y0:      tok.Region.Y0,
						x0:      tok.Region.X0,
						labelID: m.ID,
						band:    columnBand(tok.Region),
					})
				}
			}
		}
		break
	}

	// Band is the fourth sort key, not only an emitted value: two tokens can share Y0 and X0
	// yet differ in X1 and so in band, and without it that tie takes its order from the input.
	sort.Slice(obs, func(i, j int) bool {
		a, b := obs[i], obs[j]
		switch {
		case a.y0 != b.y0:
			return a.y0 < b.y0
		case a.x0 != b.x0:
			return a.x0 < b.x0
		case a.labelID != b.labelID:
			return a.labelID < b.labelID
		default:
			return a.band < b.band
		}
	})

	// A label id is [a-z_]+ and a band is one digit, so neither separator can occur inside an
	// element.
	elems := make([]string, len(obs))
	for i, o := range obs {
		elems[i] = o.labelID + ":" + strconv.Itoa(o.band)
	}
	sum := sha256.Sum256([]byte(strings.Join(elems, "|")))
	return FingerprintVersion + ":" + hex.EncodeToString(sum[:])
}
