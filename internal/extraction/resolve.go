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
	TierGeneric             // a LABELLED shipped Tier-1 rule
	// TierFallback: a shipped Tier-1 rule that recognises a value by its shape alone, with no
	// label to corroborate it. Also shipped, so "shipped rule" alone no longer names TierGeneric.
	TierFallback
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
	// Adjacent marks a value taken from a token beside its label, not from inside the label's
	// own token. Not Distance != 0: a rightward token flush against its anchor reads gap 0.
	Adjacent bool
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
	// One partition per page, read by every party-scoped rule below and computed once.
	parties := make([][]Party, len(pages))
	labels := make([][]bool, len(pages))
	for i, p := range pages {
		parties[i] = partyOrder(p)
		labels[i] = labelTokens(p)
	}
	// Learned arrives seq DESC, so the first rule that produces anything for a field is the newest
	// one reaching this page and supersedes the rest. A rule producing nothing claims nothing --
	// resolve_converge_test.go and TestResolve_ANewerRuleThatProducesNothingDoesNotSuppressTheOlderOne.
	var claimed []string
	for _, r := range rules.Learned {
		if slices.Contains(claimed, r.Field) {
			continue
		}
		before := len(all)
		// A learned rule is never party-scoped: re-routing one by heading would overrule the
		// reviewer who pointed at the field.
		// ceiling: learned rules carry no drop band and LearnRule cannot relate a dropped
		// value; revisit when a correction on an offset stack must learn
		all = appendRuleCandidates(all, pages, parties, labels, r.Rule, BandAnywhere, false, r.Field, r.ID, TierLearned, 0, false)
		if len(all) > before {
			claimed = append(claimed, r.Field)
		}
	}
	for _, r := range rules.Tier1 {
		tier := TierGeneric
		if r.Fallback {
			tier = TierFallback
		}
		all = appendRuleCandidates(all, pages, parties, labels, r.Rule, r.Band, r.PartyScoped, r.Field, r.Key, tier, r.Drop, r.RowReach)
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
// order, skipping any anchor outside band. A Rule built as a composite literal has no compiled
// matcher and yields nothing rather than panicking; ParseRule is the only constructor that sets
// one.
//
// scoped routes the candidate to the field the ANCHOR's party owns rather than to field. The
// anchor, never the value: on a below relation the value can sit past a block boundary, and
// reading its party there would let it steal the other party's field.
//
// drop and rowReach are the Tier-1 right-relation dials; a learned call passes 0 and false.
func appendRuleCandidates(dst []Candidate, pages []TokenPage, parties [][]Party, labels [][]bool, rule Rule, band PageBand, scoped bool, field, ruleID string, tier Tier, drop float64, rowReach bool) []Candidate {
	if rule.re == nil {
		return dst
	}
	for pi, page := range pages {
		for ti, tok := range page.Tokens {
			// loc indexes the view, so every read of loc takes text (TestResolve_ASpacedLabelReadsAsItsWord).
			text := labelView(tok.Text)
			loc := rule.re.FindStringIndex(text)
			if loc == nil {
				continue
			}
			if tier != TierLearned && anchorOutranked(text, loc) {
				continue
			}
			if !inBand(band, page.Number, tok.Region) {
				continue
			}
			outField := field
			if scoped {
				outField = partyField(parties[pi][ti])
			}
			switch rule.Relation.Kind {
			case RelSameToken:
				dst = appendReadings(dst, rule.Shape, sameTokenValue(text, loc),
					usableRegion(tok.Region), outField, ruleID, tier, 0, false)
			case RelRight, RelBelow:
				bounded := tier != TierLearned && rule.Relation.Kind == RelRight
				for _, rel := range relatedTokens(page, tok.Region, rule.Relation, drop) {
					value := page.Tokens[rel.index]
					if bounded && crossesALabel(page, labels[pi], tok.Region, value.Region) {
						continue
					}
					// A dropped pair also checks the value's own band: a label sitting there
					// owns the value just as one sitting on the anchor's band would.
					if bounded && rel.dropped && crossesALabel(page, labels[pi],
						Region{Page: tok.Region.Page, X0: tok.Region.X0, X1: tok.Region.X1, Y0: value.Region.Y0, Y1: value.Region.Y1},
						value.Region) {
						continue
					}
					// Adjacent is a constant of this branch, both relations: every value here
					// came from a token beside the anchor, never from inside it.
					// ceiling: a dropped value ranks by horizontal gap alone; revisit if a
					// same-line value loses rank 0 to one
					dst = appendReadings(dst, rule.Shape, value.Text,
						usableRegion(value.Region), outField, ruleID, tier, rel.distance, true)
				}
				// ceiling: a label carrying other letters, e.g. "Total (NGN)", gets no row reach; revisit when a far-right total with a suffixed label misses
				// ceiling: a dropped far value gets no row reach; revisit when an offset far-right total misses
				if rowReach && bounded && bareLabel(text, loc) {
					if vi, gap, ok := rowReachToken(page, tok.Region, rule.Relation); ok && !ownedBelow(page, labels[pi], page.Tokens[vi].Region) {
						value := page.Tokens[vi]
						dst = appendReadings(dst, rule.Shape, value.Text,
							usableRegion(value.Region), outField, ruleID, tier, gap, true)
					}
				}
			}
		}
	}
	return dst
}

// anchorOutranked reports whether another anchor-lexicon entry claims a strictly wider span of
// text that contains loc. A narrower label owns nothing on the token -- "Supplier" inside
// "Supplier TIN: 99999999-0101", "total" inside "Sub-total" -- so it must not anchor its rule.
// Strict containment only: an equal span is a rule meeting its own entry, and a non-strict test
// would suppress every shipped rule.
func anchorOutranked(text string, loc []int) bool {
	for _, m := range anchorLabelMatchers {
		other := m.RE.FindStringIndex(text)
		if other == nil {
			continue
		}
		if other[0] <= loc[0] && other[1] >= loc[1] && other[1]-other[0] > loc[1]-loc[0] {
			return true
		}
	}
	return false
}

// labelView is text as a label reads it: a token made only of single letters spaced apart
// ("I N V O I C E") reads as those letters joined. Every other token reads as printed.
// ceiling: word gaps are dropped too, so a spaced label whose pattern needs a word boundary ("T O T A L   D U E") misses; revisit when one does
func labelView(text string) string {
	units := strings.Fields(text)
	if len(units) < 2 {
		return text
	}
	for _, u := range units {
		if r := []rune(u); len(r) != 1 || !unicode.IsLetter(r[0]) {
			return text
		}
	}
	return strings.Join(units, "")
}

// labelTokens is one bool per token in the page's own order: does this token carry any
// anchor-lexicon label. Computed once per page, like partyOrder: crossesALabel runs per candidate
// pair, so reading the lexicon inside it costs orders of magnitude more than one whole Resolve on
// a dense row. The ratio moves with label density and no benchmark is committed, so the shape is
// held by TestResolve_TheBoundaryPredicateCallsNothingThatReadsTheLexicon, not by a figure here.
//
// No not-outranked qualifier: a token's WIDEST lexicon hit can never be strictly contained in a
// wider one, so "carries a hit not itself outranked" and "carries a hit" are the same predicate
// (TestAnchorLexicon_OutrankingNeverEmptiesATokensLabelSet).
func labelTokens(page TokenPage) []bool {
	out := make([]bool, len(page.Tokens))
	for i, tok := range page.Tokens {
		text := labelView(tok.Text)
		for _, m := range anchorLabelMatchers {
			if m.RE.MatchString(text) {
				out[i] = true
				break
			}
		}
	}
	return out
}

// crossesALabel reports whether a labelled token sits between anchor and value on the rightward
// path, inside the anchor's own band. A label owns what follows it, so the read stops there.
//
// The value token cannot block itself: the cut at its own left edge excludes it, which is why
// that test is >= and not >.
func crossesALabel(page TokenPage, labels []bool, anchor, value Region) bool {
	for i, tok := range page.Tokens {
		if !labels[i] {
			continue
		}
		b := tok.Region
		if !usableBox(b) {
			continue
		}
		if b.X0 < anchor.X1 || b.X0 >= value.X0 {
			continue
		}
		ov := overlap1D(anchor.Y0, anchor.Y1, b.Y0, b.Y1)
		span := min(anchor.Y1-anchor.Y0, b.Y1-b.Y0)
		if ov <= 0 || ov < 0.5*span {
			continue
		}
		return true
	}
	return false
}

// bareLabel reports whether text carries no letter outside its label match.
func bareLabel(text string, loc []int) bool {
	return !strings.ContainsFunc(text[:loc[0]], unicode.IsLetter) &&
		!strings.ContainsFunc(text[loc[1]:], unicode.IsLetter)
}

// rowReachToken is the first token on anchor's line to its right, when it sits past rel's dial.
func rowReachToken(page TokenPage, anchor Region, rel Relation) (index int, gap float64, ok bool) {
	if !usableBox(anchor) {
		return 0, 0, false
	}
	line := Relation{Kind: RelRight, MaxDistance: math.Inf(1)}
	index = -1
	for i, tok := range page.Tokens {
		if !usableBox(tok.Region) {
			continue
		}
		// ceiling: only a bare "₦" is passed over; a bare "NGN" or "N" before the amount still ends the reach
		if strings.TrimSpace(tok.Text) == "₦" {
			continue
		}
		g, _, order, _, overlap := relationClauses(anchor, tok.Region, line, 0)
		if order || overlap {
			continue
		}
		if index < 0 || tok.Region.X0 < page.Tokens[index].Region.X0 {
			index, gap = i, g
		}
	}
	if index < 0 || gap <= rel.MaxDistance {
		return 0, 0, false
	}
	return index, gap, true
}

// A value stacked under another label is that label's (TestResolve_TheRowReachStopsAtAnotherColumnsValue).
// ceiling: any label stacked over the value refuses the row reach, even one whose own rule rejects it; revisit when a far-right amount under a non-amount column header misses
func ownedBelow(page TokenPage, labels []bool, value Region) bool {
	below := Relation{Kind: RelBelow, MaxDistance: tier1MaxDistanceBelow}
	for i, tok := range page.Tokens {
		if !labels[i] || !usableBox(tok.Region) {
			continue
		}
		if _, _, order, distance, overlap := relationClauses(tok.Region, value, below, 0); !order && !distance && !overlap {
			return true
		}
	}
	return false
}

// appendReadings emits one candidate per reading the shape accepts, so an ambiguous numeric
// date keeps both readings and the Value key separates them.
func appendReadings(dst []Candidate, shape Shape, raw string, region *Region, field, ruleID string, tier Tier, distance float64, adjacent bool) []Candidate {
	for _, v := range shape.Normalize(raw) {
		dst = append(dst, Candidate{
			Field:    field,
			Value:    v,
			Region:   region,
			Reason:   ReasonNone,
			RuleID:   ruleID,
			Tier:     tier,
			Distance: distance,
			Adjacent: adjacent,
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
	// dropped marks a pair the drop band admitted where the line band alone would have refused.
	dropped bool
}

// relatedTokens returns every token on page standing in rel's relation to anchor, in reader
// order. A token needs overlap along the off-axis of at least half the SHORTER of the two spans
// -- against the anchor's own span a wide label over a narrow value drops the right answer
// (TestResolve_BelowFindsTheStackedValue, "wide label over a narrow value").
//
// An unusable box on either side relates to nothing: a zero box sits at the page corner and
// would be falsely adjacent to everything. That predicate also excludes the anchor from its own
// result, since usableBox forces X0 < X1 and Y0 < Y1.
func relatedTokens(page TokenPage, anchor Region, rel Relation, drop float64) []relatedToken {
	if !usableBox(anchor) {
		return nil
	}
	if rel.Kind != RelRight && rel.Kind != RelBelow {
		return nil
	}

	var out []relatedToken
	for i, tok := range page.Tokens {
		b := tok.Region
		if !usableBox(b) {
			continue
		}
		gap, dropped, order, distance, overlap := relationClauses(anchor, b, rel, drop)
		if order || distance || overlap {
			continue
		}
		out = append(out, relatedToken{index: i, distance: gap, dropped: dropped})
	}
	return out
}

// relationClauses reports every conjunct that rejects value for anchor under rel, not just the
// first: TestOffsetStack_PdfiumTwinFailsOnlyTheOverlapClauseOnRight. gap is the relation's axis
// gap and dropped marks a right pair the drop band admitted where the line band alone refused;
// below ignores drop and never sets dropped.
func relationClauses(anchor, value Region, rel Relation, drop float64) (gap float64, dropped, order, distance, overlap bool) {
	var ov, span float64
	switch rel.Kind {
	case RelRight:
		order = value.X0 < anchor.X1
		gap = value.X0 - anchor.X1
		ov = overlap1D(anchor.Y0, anchor.Y1, value.Y0, value.Y1)
		span = min(anchor.Y1-anchor.Y0, value.Y1-value.Y0)
		// A subnormal span halves to zero, so ov >= 0.5*span would admit a zero overlap. The
		// strict conjunct is what rejects one: TestResolve_RejectsAZeroOverlapUnderASubnormalSpan.
		lineFails := ov <= 0 || ov < 0.5*span
		// ceiling: ratio against the label box, whose height pdfium varies with glyphs;
		// revisit when a label with no descenders misses a dropped value
		dropped = lineFails && value.Y0 > anchor.Y0 && value.Y0-anchor.Y0 < drop*(anchor.Y1-anchor.Y0)
		overlap = lineFails && !dropped
	case RelBelow:
		order = value.Y0 < anchor.Y1
		gap = value.Y0 - anchor.Y1
		ov = overlap1D(anchor.X0, anchor.X1, value.X0, value.X1)
		span = min(anchor.X1-anchor.X0, value.X1-value.X0)
		overlap = ov <= 0 || ov < 0.5*span
	default:
		return 0, false, true, true, true
	}

	distance = gap > rel.MaxDistance
	return gap, dropped, order, distance, overlap
}

// overlap1D is the length [a0,a1] and [b0,b1] share, negative when they are disjoint.
func overlap1D(a0, a1, b0, b1 float64) float64 {
	return min(a1, b1) - max(a0, b0)
}

// PageBand scopes a Tier-1 rule to part of a page. An enum, not a coordinate pair: the zero
// value has to mean "every page", or a rule that set no band would go silent.
type PageBand int

const (
	BandAnywhere    PageBand = iota // every page, every row -- the zero value
	BandPage1Top                    // page 1, anchor box wholly above the half-way line
	BandPage1Bottom                 // page 1, anchor box wholly below it
)

// pageBandSplit is the half-way line the two bounded bands divide page 1 on.
const pageBandSplit = 0.5

// inBand reports whether an anchor at r on page satisfies band. An unrecognised band, a boxless
// token and a box straddling the split all match nothing: a bounded band over an unknown or
// ambiguous position has to fail closed, or the sweep it scopes goes page-wide
// (TestResolve_ABandedRuleIgnoresAnAnchorOutsideItsBand).
func inBand(band PageBand, page int, r Region) bool {
	switch band {
	case BandAnywhere:
		return true
	case BandPage1Top:
		return page == 1 && usableBox(r) && r.Y1 <= pageBandSplit
	case BandPage1Bottom:
		return page == 1 && usableBox(r) && r.Y0 >= pageBandSplit
	default:
		return false
	}
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
// Total because Field groups, Reason never varies, and Adjacent follows RuleID -- one rule has
// one relation kind (TestTier1_EveryKeyNamesItsOwnRelation) -- so two candidates comparing equal
// are equal in every field. TestResolve_ComparatorIsTotal is the oracle -- the permutation specs
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
