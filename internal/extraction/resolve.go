// resolve.go: rule application. Every candidate every rule produced, grouped in HeaderFields
// order and ordered within a field, never a nil slice. Pure -- no database, no clock, no
// network, no goroutine, and no map on the path (resolve_internal_test.go scans for each).
package extraction

import (
	"math"
	"slices"
	"strings"
	"unicode"
)

// Tier is which rule produced a candidate. An ordering input, never a displayed confidence.
type Tier int

const (
	TierLearned Tier = iota // a stored rule for this layout fingerprint
	TierGeneric             // a shipped Tier-1 rule
)

// Candidate is one possible value for one field. Not a Field: law E07 makes Field.Name unique
// within a result, and a field may have more than one answer.
type Candidate struct {
	Field    string  // a HeaderFields member
	Value    string  // non-empty, already normalised by its Shape
	Region   *Region // nil when the source token carried no usable box
	Reason   Reason  // always ReasonNone here; the doubt pass fills the slot
	RuleID   string  // AnchorRule.ID or Tier1Rule.Key
	Tier     Tier
	Distance float64 // gap along the relation's axis, normalised; 0 for same_token
}

// RuleSet is what Resolve reads. Neither slice is ever a map: iteration order is output order.
type RuleSet struct {
	Learned []AnchorRule
	Tier1   []Tier1Rule
}

// maxCandidatesPerField bounds a pathological document. It truncates AFTER ordering, so it is
// a deterministic cut and never a pick between two plausible values.
const maxCandidatesPerField = 8

// Resolve returns every candidate every rule produced. It makes no decision: a field with two
// plausible values keeps both, and a field with none is simply absent.
//
// A field outside HeaderFields falls out of the loop: it has no invoices column and no place in
// the output order. Nothing sorts the pages first -- the total order alone makes the output
// permutation-invariant, and a second ordering mechanism would mask a gap in the comparator.
func Resolve(pages []TokenPage, rules RuleSet) []Candidate {
	var all []Candidate
	for _, r := range rules.Learned {
		all = appendRuleCandidates(all, pages, r.Rule, r.Field, r.ID, TierLearned)
	}
	for _, r := range rules.Tier1 {
		all = appendRuleCandidates(all, pages, r.Rule, r.Field, r.Key, TierGeneric)
	}

	out := make([]Candidate, 0, len(HeaderFields))
	var per []Candidate
	for _, field := range HeaderFields {
		per = per[:0]
		for _, c := range all {
			if c.Field == field {
				per = append(per, c)
			}
		}
		slices.SortFunc(per, compareCandidates)
		out = append(out, per[:min(len(per), maxCandidatesPerField)]...)
	}
	return out
}

// appendRuleCandidates applies one rule to every page in slice order and every token in reader
// order. A Rule built as a composite literal has no compiled matcher and yields nothing rather
// than panicking; ParseRule is the only constructor that sets one.
func appendRuleCandidates(dst []Candidate, pages []TokenPage, rule Rule, field, ruleID string, tier Tier) []Candidate {
	if rule.re == nil {
		return dst
	}
	for _, page := range pages {
		for _, tok := range page.Tokens {
			loc := rule.re.FindStringIndex(tok.Text)
			if loc == nil {
				continue
			}
			switch rule.Relation.Kind {
			case RelSameToken:
				dst = appendReadings(dst, rule.Shape, sameTokenValue(tok.Text, loc),
					usableRegion(tok.Region), field, ruleID, tier, 0)
			case RelRight, RelBelow:
				for _, rel := range relatedTokens(page, tok.Region, rule.Relation) {
					value := page.Tokens[rel.index]
					dst = appendReadings(dst, rule.Shape, value.Text,
						usableRegion(value.Region), field, ruleID, tier, rel.distance)
				}
			}
		}
	}
	return dst
}

// appendReadings emits one candidate per reading the shape accepts, so an ambiguous numeric
// date keeps both readings and the Value key separates them.
func appendReadings(dst []Candidate, shape Shape, raw string, region *Region, field, ruleID string, tier Tier, distance float64) []Candidate {
	for _, v := range shape.Normalize(raw) {
		dst = append(dst, Candidate{
			Field:    field,
			Value:    v,
			Region:   region,
			Reason:   ReasonNone,
			RuleID:   ruleID,
			Tier:     tier,
			Distance: distance,
		})
	}
	return dst
}

// sameTokenValue is what a same_token match leaves behind. A match spanning the whole token is
// a label that IS its own value -- a bare TIN swept by format alone -- so the token stands.
// Otherwise the remainder follows the match, past ":", "-", an en or em dash and whitespace.
func sameTokenValue(text string, loc []int) string {
	if loc[0] == 0 && loc[1] == len(text) {
		return text
	}
	return strings.TrimLeftFunc(text[loc[1]:], isLabelSep)
}

func isLabelSep(r rune) bool {
	return r == ':' || r == '-' || r == '–' || r == '—' || unicode.IsSpace(r)
}

// relatedToken is one token standing in a relation to an anchor: where it sits on the page and
// the edge gap that ranks it.
type relatedToken struct {
	index    int
	distance float64
}

// relatedTokens returns every token on page standing in rel's relation to anchor, in reader
// order. A token needs overlap along the off-axis of at least half the SHORTER of the two spans
// -- against the anchor's own span a wide label over a narrow value drops the right answer
// (TestResolve_BelowFindsTheStackedValue, "wide label over a narrow value").
//
// An unusable box on either side relates to nothing: a zero box sits at the page corner and
// would be falsely adjacent to everything. That predicate also excludes the anchor from its own
// result, since usableBox forces X0 < X1 and Y0 < Y1.
func relatedTokens(page TokenPage, anchor Region, rel Relation) []relatedToken {
	if !usableBox(anchor) {
		return nil
	}

	var out []relatedToken
	for i, tok := range page.Tokens {
		b := tok.Region
		if !usableBox(b) {
			continue
		}

		var gap, ov, span float64
		switch rel.Kind {
		case RelRight:
			if b.X0 < anchor.X1 {
				continue
			}
			gap = b.X0 - anchor.X1
			ov = overlap1D(anchor.Y0, anchor.Y1, b.Y0, b.Y1)
			span = min(anchor.Y1-anchor.Y0, b.Y1-b.Y0)
		case RelBelow:
			if b.Y0 < anchor.Y1 {
				continue
			}
			gap = b.Y0 - anchor.Y1
			ov = overlap1D(anchor.X0, anchor.X1, b.X0, b.X1)
			span = min(anchor.X1-anchor.X0, b.X1-b.X0)
		default:
			return nil
		}

		// A subnormal span halves to zero, so ov >= 0.5*span would admit a zero overlap. The
		// strict conjunct is what rejects one:
		// TestResolve_RejectsAZeroOverlapUnderASubnormalSpan.
		if gap > rel.MaxDistance || ov <= 0 || ov < 0.5*span {
			continue
		}
		out = append(out, relatedToken{index: i, distance: gap})
	}
	return out
}

// overlap1D is the length [a0,a1] and [b0,b1] share, negative when they are disjoint.
func overlap1D(a0, a1, b0, b1 float64) float64 {
	return min(a1, b1) - max(a0, b0)
}

// usableRegion is the box a candidate may carry: a copy, or nil when the source token had no
// geometry worth pointing at.
func usableRegion(r Region) *Region {
	if !usableBox(r) {
		return nil
	}
	box := r
	return &box
}

// usableBox is the single box predicate: a real page, finite coordinates and a positive area
// inside the normalised page. Token.Region is a value, not a pointer, so nil-ness cannot tell a
// boxless DOCX token from a real box -- only this can. Stricter than the DB check on purpose:
// it makes extraction_field_results_bbox_normalised true by construction, and it keeps a NaN
// (which makes every float comparison false) out of the comparator.
func usableBox(r Region) bool {
	if r.Page < 1 {
		return false
	}
	if !finite(r.X0) || !finite(r.Y0) || !finite(r.X1) || !finite(r.Y1) {
		return false
	}
	return r.X0 >= 0 && r.X0 < r.X1 && r.X1 <= 1 &&
		r.Y0 >= 0 && r.Y0 < r.Y1 && r.Y1 <= 1
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// compareCandidates is the total order within one field: tier, distance, region, value, rule id.
// Total because Field groups and Reason never varies, so two candidates comparing equal are
// equal in every field. TestResolve_ComparatorIsTotal is the oracle -- the permutation specs
// are not, since slices.SortFunc insertion-sorts below n=12 and leaves equals in place.
func compareCandidates(a, b Candidate) int {
	if a.Tier != b.Tier {
		if a.Tier < b.Tier {
			return -1
		}
		return 1
	}
	if c := compareFloat(a.Distance, b.Distance); c != 0 {
		return c
	}
	if c := compareRegions(a.Region, b.Region); c != 0 {
		return c
	}
	if c := strings.Compare(a.Value, b.Value); c != 0 {
		return c
	}
	return strings.Compare(a.RuleID, b.RuleID)
}

// compareRegions puts a box before no box, then orders by reading order. Y1 and X1 are compared
// as well as Y0 and X0: two boxes can share a top-left corner and differ below it.
func compareRegions(a, b *Region) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	if a.Page != b.Page {
		if a.Page < b.Page {
			return -1
		}
		return 1
	}
	if c := compareFloat(a.Y0, b.Y0); c != 0 {
		return c
	}
	if c := compareFloat(a.X0, b.X0); c != 0 {
		return c
	}
	if c := compareFloat(a.Y1, b.Y1); c != 0 {
		return c
	}
	return compareFloat(a.X1, b.X1)
}

func compareFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
