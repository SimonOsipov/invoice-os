// learn.go: one correction becomes one rule. The geometry here is the inverse of relatedTokens
// (resolve.go), so a derived rule fires on the page it was derived from
// (TestLearnRule_R17_DerivedRuleRoundTripsThroughResolve). Pure -- no clock, no database, no
// network, no goroutine, and no map on the path (resolve_internal_test.go scans for each).
package extraction

import (
	"math"
	"regexp"
	"strings"
)

// LearnedRule is what one correction taught. Body is the exact bytes for
// extraction_anchor_rules.rule; []byte rather than json.RawMessage keeps encoding/json out of
// this file's import allowlist (TestResolve_ImportsOnlyPureStdlib).
type LearnedRule struct {
	Field  string
	Anchor AnchorObservation
	Rule   Rule
	Body   []byte
}

// candidate is one qualifying anchor with the relation it stands in to the corrected region.
type candidate struct {
	AnchorObservation
	kind RelationKind
	gap  float64
}

// LearnRule derives one rule from a pointed correction. ok is false when no anchor stands in a
// relation to region -- an honest refusal; the correction is still recorded. The field lock
// (invoice_number, supplier_tin, supplier_name) lives in the handler's refuseField, not here.
func LearnRule(field string, region Region, anchors []AnchorObservation) (LearnedRule, bool) {
	shape, ok := tier1Shape(field)
	if !ok {
		return LearnedRule{}, false
	}
	// The wire's normalisedBox admits a zero-area box and usableBox does not; this is where a
	// degenerate drag is refused, and where NaN is kept out of betterAnchor.
	if !usableBox(region) {
		return LearnedRule{}, false
	}

	var best candidate
	var bestLabel string
	found := false
	for _, o := range anchors {
		if o.Page != region.Page {
			continue
		}
		box := anchorRegion(o)
		if !usableBox(box) {
			continue
		}
		kind, gap, related := relateRegion(box, region)
		if !related || gap > capDistance(kind) {
			continue
		}
		label, ok := learnedLabel(o.Text)
		if !ok {
			continue
		}
		if _, err := regexp.Compile(label); err != nil {
			continue
		}
		c := candidate{AnchorObservation: o, kind: kind, gap: gap}
		if !found || betterAnchor(c, best) {
			best, bestLabel, found = c, label, true
		}
	}
	if !found {
		return LearnedRule{}, false
	}

	h := hundredthsAtLeast(best.gap)
	if c := capHundredths(best.kind); h > c {
		h = c
	}
	body := []byte(`{"label":` + jsonString(bestLabel) +
		`,"relation":{"kind":` + jsonString(string(best.kind)) +
		`,"max_distance":` + hundredthsJSON(h) +
		`},"shape":` + jsonString(string(shape)) + `}`)

	// A body ParseRule rejects is a defect, not a stored row -- refuse rather than persist it.
	rule, err := ParseRule(body)
	if err != nil {
		return LearnedRule{}, false
	}
	return LearnedRule{Field: field, Anchor: best.AnchorObservation, Rule: rule, Body: body}, true
}

// tier1Shape is the field gate and the shape lookup in one linear scan: tier1Specs' field set
// equals HeaderFields, so a miss here is a field no rule can fill.
func tier1Shape(field string) (Shape, bool) {
	for _, s := range tier1Specs {
		if s.field == field {
			return s.shape, true
		}
	}
	return "", false
}

func anchorRegion(o AnchorObservation) Region {
	return Region{Page: o.Page, X0: o.X0, Y0: o.Y0, X1: o.X1, Y1: o.Y1}
}

// relateRegion inverts relatedTokens (resolve.go), including its ov > 0 conjunct: without it a
// subnormal span halves to zero and admits an overlap Resolve refuses
// (TestLearnRule_RightOverlapBoundary, "subnormal span with zero overlap").
//
// same_token has no geometric inverse -- Resolve matches the label against a token's text and
// takes the remainder -- so box overlap stands in for "the value lives inside the anchor token".
func relateRegion(a, r Region) (RelationKind, float64, bool) {
	if overlap1D(a.X0, a.X1, r.X0, r.X1) > 0 && overlap1D(a.Y0, a.Y1, r.Y0, r.Y1) > 0 {
		return RelSameToken, 0, true
	}
	if r.X0 >= a.X1 {
		ov := overlap1D(a.Y0, a.Y1, r.Y0, r.Y1)
		span := min(a.Y1-a.Y0, r.Y1-r.Y0)
		if ov > 0 && ov >= 0.5*span {
			return RelRight, r.X0 - a.X1, true
		}
	}
	if r.Y0 >= a.Y1 {
		ov := overlap1D(a.X0, a.X1, r.X0, r.X1)
		span := min(a.X1-a.X0, r.X1-r.X0)
		if ov > 0 && ov >= 0.5*span {
			return RelBelow, r.Y0 - a.Y1, true
		}
	}
	return "", 0, false
}

// capDistance is the relation's Tier-1 dial. Strict, so a gap exactly at the cap survives,
// rounds to the cap, and fires (TestLearnRule_GapExactlyAtTheCap).
func capDistance(kind RelationKind) float64 {
	switch kind {
	case RelRight:
		return tier1MaxDistanceRight
	case RelBelow:
		return tier1MaxDistanceBelow
	default:
		return 0
	}
}

func capHundredths(kind RelationKind) int {
	switch kind {
	case RelRight:
		return 35
	case RelBelow:
		return 6
	default:
		return 0
	}
}

// hundredthsAtLeast is the smallest integer h >= 0 with float64(h)/100 >= g. The naive
// math.Ceil(g*100) over-rounds at exact hundredths and math.Ceil(g*100)/100 undershoots just
// above others (TestHundredthsAtLeast_TheNaiveCeilFormulaIsWrongAtTheseValues); rounding UP is
// what absorbs the difference between the user's drag box and the token box.
func hundredthsAtLeast(g float64) int {
	if g <= 0 {
		return 0
	}
	h := int(math.Ceil(g * 100))
	if h < 0 {
		h = 0
	}
	for float64(h)/100 < g {
		h++
	}
	for h > 0 && float64(h-1)/100 >= g {
		h--
	}
	return h
}

// hundredthsJSON spells h as "0.HH". h is capped at 35, so two digits always suffice and
// strconv stays outside this file's import allowlist.
func hundredthsJSON(h int) string {
	return "0." + string([]byte{byte('0' + h/10), byte('0' + h%10)})
}

// learnedLabel is "(?i)" + QuoteMeta(text), word-bounded where the outer BYTES allow: Go's \b
// is the ASCII word boundary, so a leading "É" or a trailing "." must not carry one
// (TestLearnRule_R13_TrailingDotLabelHasNoTrailingBoundary). Text is re-capped because a stored
// layout_anchors row does not round-trip through capAnchorLabelBytes. An empty label is refused:
// it compiles and then matches every token.
func learnedLabel(text string) (string, bool) {
	t := strings.TrimSpace(capAnchorLabelBytes(text))
	if t == "" {
		return "", false
	}
	label := "(?i)"
	if isWordByte(t[0]) {
		label += `\b`
	}
	label += regexp.QuoteMeta(t)
	if isWordByte(t[len(t)-1]) {
		label += `\b`
	}
	return label, true
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// betterAnchor is a strict total order over candidate: gap, relation, lexicon index, then every
// remaining field of AnchorObservation, so no two distinct candidates compare equal.
// TestBetterAnchor_IsATotalOrder is the oracle -- the permutation specs are not.
func betterAnchor(a, b candidate) bool {
	if c := compareFloat(a.gap, b.gap); c != 0 {
		return c < 0
	}
	if ra, rb := relationRank(a.kind), relationRank(b.kind); ra != rb {
		return ra < rb
	}
	if la, lb := lexiconIndex(a.Label), lexiconIndex(b.Label); la != lb {
		return la < lb
	}
	if c := strings.Compare(a.Label, b.Label); c != 0 {
		return c < 0
	}
	if a.Page != b.Page {
		return a.Page < b.Page
	}
	if c := compareFloat(a.Y0, b.Y0); c != 0 {
		return c < 0
	}
	if c := compareFloat(a.X0, b.X0); c != 0 {
		return c < 0
	}
	// Y1 and X1 too: two boxes can share a top-left corner and differ below it.
	if c := compareFloat(a.Y1, b.Y1); c != 0 {
		return c < 0
	}
	if c := compareFloat(a.X1, b.X1); c != 0 {
		return c < 0
	}
	if a.Band != b.Band {
		return a.Band < b.Band
	}
	return strings.Compare(a.Text, b.Text) < 0
}

func relationRank(kind RelationKind) int {
	switch kind {
	case RelSameToken:
		return 0
	case RelRight:
		return 1
	case RelBelow:
		return 2
	default:
		return 3
	}
}

// lexiconIndex is label's position in anchorLexicon order; a label naming no entry sorts last.
func lexiconIndex(label string) int {
	for i, m := range anchorLabelMatchers {
		if m.ID == label {
			return i
		}
	}
	return len(anchorLabelMatchers)
}
