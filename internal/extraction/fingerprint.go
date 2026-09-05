// fingerprint.go: the layout fingerprint -- which anchor labels appear on page 1, in what
// reading order, and in which third of the page. Never supplier identity: that is not known
// until after extraction, and one supplier commonly has several layouts. The same page-1
// observations are also kept whole (AnchorObservations) and stored as
// extraction_jobs.layout_anchors, so a later correction can name the anchor it sat under.
package extraction

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
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

// maxAnchorLabelBytes caps a stored anchor label's text in BYTES, matching maxRuleLabelBytes.
// EXTR-14's learned label is "(?i)" + optional \b + QuoteMeta(text) + optional \b, and
// QuoteMeta at worst doubles, so 4+2+2*128+2 = 264 stays under ParseRule's 512-byte cap.
const maxAnchorLabelBytes = 128

// AnchorObservation is one anchor-lexicon hit on page 1: which label, the substring the
// pattern itself matched, where, and in which third. Text is a label word ("Buyer",
// "Invoice No"), never the token's value half, and is not hashed.
type AnchorObservation struct {
	Label string  `json:"label"`
	Text  string  `json:"text"`
	Page  int     `json:"page"`
	Band  int     `json:"band"`
	X0    float64 `json:"x0"`
	Y0    float64 `json:"y0"`
	X1    float64 `json:"x1"`
	Y1    float64 `json:"y1"`
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

// AnchorObservations returns page 1's lexicon hits in the exact order Fingerprint hashes
// them. Never nil: layout_anchors takes a JSON array, never null.
func AnchorObservations(pages []TokenPage) []AnchorObservation {
	out := make([]AnchorObservation, 0, 8)
	for _, page := range pages {
		// Page 1 by Number, not by slice position: the caller's order is not the page order.
		if page.Number != 1 {
			continue
		}
		for _, tok := range page.Tokens {
			for _, m := range anchorLabelMatchers {
				if !m.RE.MatchString(tok.Text) {
					continue
				}
				out = append(out, AnchorObservation{
					Label: m.ID,
					Text:  capAnchorLabelBytes(m.RE.FindString(tok.Text)),
					Page:  page.Number,
					Band:  columnBand(tok.Region),
					X0:    tok.Region.X0,
					Y0:    tok.Region.Y0,
					X1:    tok.Region.X1,
					Y1:    tok.Region.Y1,
				})
			}
		}
		break
	}

	// Band is the fourth sort key, not only an emitted value: two tokens can share Y0 and X0
	// yet differ in X1 and so in band, and without it that tie takes its order from the input.
	// Stable, so comparator-equal hits keep collection order -- the hash cannot see the
	// difference (equal Label and Band), but a stored observation list can.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Y0 != b.Y0:
			return a.Y0 < b.Y0
		case a.X0 != b.X0:
			return a.X0 < b.X0
		case a.Label != b.Label:
			return a.Label < b.Label
		default:
			return a.Band < b.Band
		}
	})
	return out
}

// Fingerprint identifies a page-1 layout by which anchor labels appear, in what reading order,
// and in which third of the page -- AnchorObservations' (label, band) projection, so the
// hashed set and the stored list can never drift apart. A page carrying no recognised label
// still fingerprints, to sha256(""): a real value that simply matches no stored rule.
// TestFingerprint_IsUnchangedByAnchorSpecificity is the oracle that says the value moved.
func Fingerprint(pages []TokenPage) string {
	obs := AnchorObservations(pages)

	// A label id is [a-z_]+ and a band is one digit, so neither separator can occur inside an
	// element.
	elems := make([]string, len(obs))
	for i, o := range obs {
		elems[i] = o.Label + ":" + strconv.Itoa(o.Band)
	}
	sum := sha256.Sum256([]byte(strings.Join(elems, "|")))
	return FingerprintVersion + ":" + hex.EncodeToString(sum[:])
}

// BoxlessFingerprintVersion prefixes every boxless fingerprint. Disjoint from
// FingerprintVersion on byte 0, which is what makes a "b1:" value unable to collide with any
// "v1:" value in the shared layout_fingerprint column.
const BoxlessFingerprintVersion = "b1"

// labelPlacement says where a lexicon match sits inside its own token: "w" whole, "l" leading,
// "i" inline. Mirrors sameTokenValue (resolve.go:158-163), whose split is w versus l+i.
//
// STUB -- EXTR-19-02 Stage 2.5. The body is Stage 3's.
func labelPlacement(text string, loc []int) string { return "" }

// BoxlessFingerprint identifies a page-1 layout with no usable geometry -- every DOCX token
// carries the zero box -- by which anchor labels appear, in document order, and how each
// label sits inside its own token. Nothing sorts: the order IS the signal, which is what
// separates it from Fingerprint. A page carrying no recognised label still fingerprints, to
// sha256(""), matching Fingerprint's own documented behaviour.
//
// STUB -- EXTR-19-02 Stage 2.5. The body is Stage 3's.
func BoxlessFingerprint(pages []TokenPage) string { return "" }

// AnchorLabelText is the lexicon pattern's own matched substring for o.Label against tok,
// capped at maxAnchorLabelBytes on a rune boundary. "" when o.Label names no lexicon entry or
// its pattern does not match tok.Text.
func AnchorLabelText(o AnchorObservation, tok Token) string {
	for _, m := range anchorLabelMatchers {
		if m.ID == o.Label {
			return capAnchorLabelBytes(m.RE.FindString(tok.Text))
		}
	}
	return ""
}

// capAnchorLabelBytes truncates to maxAnchorLabelBytes without splitting a rune. issue_date's
// \s* is unbounded and RE2 folds U+017F to "s", so a real match can run past the cap in
// multi-byte runes.
func capAnchorLabelBytes(s string) string {
	if len(s) <= maxAnchorLabelBytes {
		return s
	}
	cut := maxAnchorLabelBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// normalisedAnchorBox mirrors normalisedBox (handlers_correction.go) so a worker cannot store
// a box the wire would refuse. TestNormalisedBoxAgreesWithTheAnchorCodecPredicate is the
// oracle for the two staying in agreement. A degenerate zero-area box is admitted.
func normalisedAnchorBox(x0, y0, x1, y1 float64) bool {
	return x0 >= 0 && x0 <= x1 && x1 <= 1 && y0 >= 0 && y0 <= y1 && y1 <= 1
}

func checkAnchorObservation(i int, o AnchorObservation) error {
	if o.Page < 1 {
		return fmt.Errorf("layout_anchors[%d]: page %d is below 1", i, o.Page)
	}
	if !normalisedAnchorBox(o.X0, o.Y0, o.X1, o.Y1) {
		return fmt.Errorf("layout_anchors[%d]: (%v,%v)-(%v,%v) is not a normalised box: 0 <= x0 <= x1 <= 1 and 0 <= y0 <= y1 <= 1",
			i, o.X0, o.Y0, o.X1, o.Y1)
	}
	return nil
}

// MarshalAnchorObservations encodes the list for extraction_jobs.layout_anchors. A nil or
// empty list marshals to "[]", never "null": the column's CHECK takes an array. Writes are
// validated too, so no path can store a row the reader would refuse.
func MarshalAnchorObservations(obs []AnchorObservation) ([]byte, error) {
	for i, o := range obs {
		if err := checkAnchorObservation(i, o); err != nil {
			return nil, err
		}
	}
	if obs == nil {
		obs = []AnchorObservation{}
	}
	return json.Marshal(obs)
}

// UnmarshalAnchorObservations reads the column back. An unknown key is ignored, as ParseRule
// ignores one; an element outside the normalised box or below page 1 is an error.
func UnmarshalAnchorObservations(raw []byte) ([]AnchorObservation, error) {
	if t := bytes.TrimLeft(raw, " \t\r\n"); len(t) == 0 || t[0] != '[' {
		return nil, fmt.Errorf("layout_anchors: want a JSON array")
	}

	var obs []AnchorObservation
	if err := json.Unmarshal(raw, &obs); err != nil {
		return nil, fmt.Errorf("layout_anchors: %w", err)
	}
	for i, o := range obs {
		if err := checkAnchorObservation(i, o); err != nil {
			return nil, err
		}
	}
	if obs == nil {
		obs = []AnchorObservation{}
	}
	return obs, nil
}
