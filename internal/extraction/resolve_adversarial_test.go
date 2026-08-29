// resolve_adversarial_test.go: the edges V-01..V-22 leave open -- the DIRECTION of each sort
// key, the BOUNDARY of each relation predicate, and the inputs no spec there reaches (a second
// page, a non-finite box, a subnormal span, a label that is its own value).
//
// The V-series proves the total order has enough keys; it does not prove any key points the
// right way. Every ordering spec here is built so byte order alone would invert the answer.
package extraction_test

import (
	"math"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	rvaLabelCurrency = `(?i)\bcurrency\b`
	rvaLabelSubtotal = `(?i)\bsub[\s-]*total\b`
	// A format-only sweep: the label matches the whole token, so the label IS the value.
	rvaLabelTINFormat = `\b[0-9]{8}-[0-9]{4}\b`
)

func rvaTokOn(page int, text string, x0, y0, x1, y1 float64) extraction.Token {
	return extraction.Token{Text: text, Region: extraction.Region{Page: page, X0: x0, Y0: y0, X1: x1, Y1: y1}}
}

// rvaPages numbers each page from 1 in slice order.
func rvaPages(tokens ...[]extraction.Token) []extraction.TokenPage {
	out := make([]extraction.TokenPage, len(tokens))
	for i, toks := range tokens {
		out[i] = extraction.TokenPage{Number: i + 1, WidthPt: 612, HeightPt: 792, Tokens: toks}
	}
	return out
}

// rvaFloat defeats constant folding. Go evaluates a constant expression in exact precision, so
// a boundary written as constants is not the float64 arithmetic the resolver runs.
//
//go:noinline
func rvaFloat(v float64) float64 { return v }

// rvaFields is the emitted field sequence, one entry per contiguous run.
func rvaFields(got []extraction.Candidate) []string {
	var seq []string
	for _, c := range got {
		if len(seq) == 0 || seq[len(seq)-1] != c.Field {
			seq = append(seq, c.Field)
		}
	}
	return seq
}

// --- the total order, one spec per key DIRECTION ------------------------------

// Tier is key 1 and distance key 2, so a learned candidate precedes a generic one that sits
// nearer the anchor. Subtask 08's tier-beats-distance rule rests on this.
func TestResolve_OrdersLearnedBeforeGenericEvenAtAGreaterDistance(t *testing.T) {
	anchor := rvTok("Invoice No:", 0.10, 0.10, 0.20, 0.13)
	near := rvTok("INV-200", 0.25, 0.10, 0.35, 0.13)
	far := rvTok("INV-100", 0.60, 0.10, 0.70, 0.13)

	rules := extraction.RuleSet{
		Learned: []extraction.AnchorRule{
			rvLearned(t, "learned-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.5, extraction.ShapeInvoiceNumber),
		},
		Tier1: []extraction.Tier1Rule{
			rvTier1(t, "t1.invoice_number.right", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.5, extraction.ShapeInvoiceNumber),
		},
	}

	got := extraction.Resolve(rvPage(anchor, near, far), rules)
	rvFloor(t, got, "one learned and one generic right rule over the same anchor")

	if len(got) != 4 {
		t.Fatalf("got %d candidate(s), want 4 (two rules over two values): %+v", len(got), got)
	}
	want := []struct {
		tier  extraction.Tier
		value string
	}{
		{extraction.TierLearned, "INV-200"},
		{extraction.TierLearned, "INV-100"},
		{extraction.TierGeneric, "INV-200"},
		{extraction.TierGeneric, "INV-100"},
	}
	for i, w := range want {
		if got[i].Tier != w.tier || got[i].Value != w.value {
			t.Errorf("candidate %d = (tier %v, %q), want (tier %v, %q) -- tier orders before distance, and learned before generic",
				i, got[i].Tier, got[i].Value, w.tier, w.value)
		}
	}
}

// A box outranks no box. The boxless token carries the lower value, so byte order alone would
// invert the pair.
func TestResolve_PutsABoxBeforeNoBox(t *testing.T) {
	boxed := rvTok("Invoice No: INV-002", 0.10, 0.10, 0.45, 0.13)
	boxless := extraction.Token{Text: "Invoice No: INV-001", Region: extraction.Region{Page: 1}}
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}

	got := extraction.Resolve(rvPage(boxless, boxed), rules)
	rvFloor(t, got, "one boxed and one boxless token under a same_token rule")

	if len(got) != 2 {
		t.Fatalf("got %d candidate(s), want 2: %+v", len(got), got)
	}
	if got[0].Region == nil || got[0].Value != "INV-002" {
		t.Errorf("candidate 0 = (region %v, %q), want the boxed INV-002 -- a candidate with geometry orders first", got[0].Region, got[0].Value)
	}
	if got[1].Region != nil || got[1].Value != "INV-001" {
		t.Errorf("candidate 1 = (region %+v, %q), want the boxless INV-001", got[1].Region, got[1].Value)
	}
}

// Reading order is Y before X, and both outrank Value. The upper token sits further right and
// carries the higher value, so X0 order and byte order each invert the pair.
func TestResolve_OrdersByRowBeforeColumnAndBothBeforeValue(t *testing.T) {
	upperRight := rvTok("Invoice No: INV-900", 0.50, 0.10, 0.85, 0.13)
	lowerLeft := rvTok("Invoice No: INV-100", 0.10, 0.30, 0.45, 0.33)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}

	got := extraction.Resolve(rvPage(lowerLeft, upperRight), rules)
	rvFloor(t, got, "an upper-right and a lower-left token under a same_token rule")

	if want := []string{"INV-900", "INV-100"}; !slices.Equal(rvValues(got), want) {
		t.Errorf("values = %v, want %v -- Y0 decides before X0, and both before Value", rvValues(got), want)
	}
}

// --- more than one page --------------------------------------------------------

// A candidate carries its own token's page, and page is a sort key. Page 2 holds the lower
// value, so byte order alone would invert the pair.
func TestResolve_CarriesThePageAndOrdersByIt(t *testing.T) {
	pages := rvaPages(
		[]extraction.Token{rvaTokOn(1, "Invoice No: INV-500", 0.10, 0.10, 0.45, 0.13)},
		[]extraction.Token{rvaTokOn(2, "Invoice No: INV-100", 0.10, 0.10, 0.45, 0.13)},
	)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}

	got := extraction.Resolve(pages, rules)
	rvFloor(t, got, "a same_token rule over two pages")

	if len(got) != 2 {
		t.Fatalf("got %d candidate(s), want 2 (one per page): %+v", len(got), got)
	}
	for i, want := range []struct {
		page  int
		value string
	}{{1, "INV-500"}, {2, "INV-100"}} {
		if got[i].Region == nil {
			t.Fatalf("candidate %d has no region; the page assertion below has nothing to read", i)
		}
		if got[i].Region.Page != want.page || got[i].Value != want.value {
			t.Errorf("candidate %d = (page %d, %q), want (page %d, %q) -- page orders before Value",
				i, got[i].Region.Page, got[i].Value, want.page, want.value)
		}
	}
}

// A relation is confined to one page: page co-ordinates are normalised per page, so an anchor
// on page 1 and a token on page 2 are not neighbours however their boxes compare.
func TestResolve_RelationsDoNotCrossPages(t *testing.T) {
	anchor := rvaTokOn(1, "Invoice No:", 0.10, 0.10, 0.25, 0.13)
	value := rvaTokOn(2, "INV-777", 0.30, 0.10, 0.40, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
	}}

	split := rvaPages([]extraction.Token{anchor}, []extraction.Token{value})
	if got := extraction.Resolve(split, rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) relating an anchor on page 1 to a token on page 2, want 0: %+v", len(got), got)
	}

	together := rvaPages([]extraction.Token{anchor, rvaTokOn(1, "INV-777", 0.30, 0.10, 0.40, 0.13)})
	rvControl(t, extraction.Resolve(together, rules), "the same two tokens on one page")
}

// --- the relation boundaries ---------------------------------------------------

// b.X0 >= a.X1 admits a touching pair: the gap is zero, not negative.
func TestResolve_RightAcceptsATouchingBox(t *testing.T) {
	anchor := rvTok("Invoice No:", 0.10, 0.10, 0.25, 0.13)
	touching := rvTok("INV-001", 0.25, 0.10, 0.35, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
	}}

	got := extraction.Resolve(rvPage(anchor, touching), rules)
	rvFloor(t, got, "a value box whose left edge touches the anchor's right edge")

	if len(got) != 1 {
		t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
	}
	if got[0].Distance != 0 {
		t.Errorf("Distance = %v, want exactly 0 for a touching pair", got[0].Distance)
	}
}

// max_distance 0 means touching only, taken literally: the gap must be <= 0, not < 0.
func TestResolve_RightWithZeroMaxDistanceAcceptsOnlyATouchingBox(t *testing.T) {
	anchor := rvTok("Invoice No:", 0.10, 0.10, 0.25, 0.13)
	apart := rvTok("INV-001", 0.30, 0.10, 0.40, 0.13)
	touching := rvTok("INV-001", 0.25, 0.10, 0.35, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0, extraction.ShapeInvoiceNumber),
	}}

	if got := extraction.Resolve(rvPage(anchor, apart), rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) for a 0.05 gap under max_distance 0, want 0: %+v", len(got), got)
	}
	rvControl(t, extraction.Resolve(rvPage(anchor, touching), rules), "the same rule over a touching box")
}

// The off-axis overlap test is >=, so an overlap of exactly half the shorter span qualifies.
// Every coordinate here is a binary fraction, so ov and 0.5*span are equal exactly.
func TestResolve_BelowAcceptsExactlyHalfOverlap(t *testing.T) {
	anchor := rvTok("Supplier Name:", 0.25, 0.125, 0.75, 0.25)
	half := rvTok("ACME", 0.5, 0.3125, 1.0, 0.4375)
	under := rvTok("ACME", 0.5625, 0.3125, 1.0, 0.4375)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "supplier_name", rvLabelSupplier, extraction.RelBelow, 0.25, extraction.ShapeName),
	}}

	// The arithmetic the boundary rests on, so a change of units cannot make this spec vacuous.
	// Read through rvaFloat: Go folds a constant expression in exact precision, which is not
	// the float64 arithmetic the resolver runs.
	if ov, span := rvaFloat(0.75)-rvaFloat(0.5), min(rvaFloat(0.75)-rvaFloat(0.25), rvaFloat(1.0)-rvaFloat(0.5)); ov != 0.5*span {
		t.Fatalf("the half case overlaps by %v against a half-span of %v; it is no longer the boundary", ov, 0.5*span)
	}

	got := extraction.Resolve(rvPage(anchor, half), rules)
	rvFloor(t, got, "a value overlapping the anchor by exactly half the shorter span")
	if len(got) != 1 {
		t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
	}

	if got := extraction.Resolve(rvPage(anchor, under), rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) for an overlap just under half, want 0: %+v", len(got), got)
	}
}

// The mirror of the right relation's line test: below needs horizontal overlap.
func TestResolve_BelowIgnoresATokenInAnotherColumn(t *testing.T) {
	anchor := rvTok("Supplier Name:", 0.10, 0.20, 0.30, 0.23)
	otherColumn := rvTok("ACME", 0.60, 0.25, 0.80, 0.28)
	sameColumn := rvTok("ACME", 0.10, 0.25, 0.30, 0.28)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "supplier_name", rvLabelSupplier, extraction.RelBelow, 0.06, extraction.ShapeName),
	}}

	if got := extraction.Resolve(rvPage(anchor, otherColumn), rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) for a value in another column, want 0 -- the horizontal spans do not overlap: %+v", len(got), got)
	}
	rvControl(t, extraction.Resolve(rvPage(anchor, sameColumn), rules), "the same pair in one column")
}

// A subnormal span halves to zero, so ov >= 0.5*span would admit a zero overlap. The strict
// ov > 0 conjunct is what rejects it, and this is the only input that reaches it.
func TestResolve_RejectsAZeroOverlapUnderASubnormalSpan(t *testing.T) {
	tiny := rvaFloat(math.SmallestNonzeroFloat64)

	if half := 0.5 * tiny; half != 0 {
		t.Fatalf("0.5*%v is %v, not 0; the half-span test no longer underflows and this spec reaches nothing", tiny, half)
	}
	if ov := min(tiny, rvaFloat(0.5)) - max(rvaFloat(0), tiny); ov != 0 {
		t.Fatalf("the two Y bands overlap by %v, not 0; the boundary this spec targets has moved", ov)
	}

	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
	}}

	anchor := rvaTokOn(1, "Invoice No:", 0.10, 0, 0.20, tiny)
	value := rvaTokOn(1, "INV-001", 0.30, tiny, 0.40, 0.5)
	if got := extraction.Resolve(rvaPages([]extraction.Token{anchor, value}), rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) for bands that touch at a point and overlap by nothing, want 0: %+v", len(got), got)
	}

	normal := rvPage(rvTok("Invoice No:", 0.10, 0.10, 0.20, 0.13), rvTok("INV-001", 0.30, 0.10, 0.40, 0.13))
	rvControl(t, extraction.Resolve(normal, rules), "the same pair on a normal line band")
}

// --- boxes that are not boxes ---------------------------------------------------

// A NaN or infinite coordinate is not geometry: same_token keeps the value with no region, and
// a spatial relation refuses the token outright.
func TestResolve_RejectsANonFiniteBox(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	cases := []struct {
		name   string
		region extraction.Region
	}{
		{"x0 nan", extraction.Region{Page: 1, X0: nan, Y0: 0.10, X1: 0.40, Y1: 0.13}},
		{"x1 inf", extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: inf, Y1: 0.13}},
		{"y0 negative inf", extraction.Region{Page: 1, X0: 0.10, Y0: math.Inf(-1), X1: 0.40, Y1: 0.13}},
		{"y1 nan", extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: nan}},
	}

	sameToken := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}
	right := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			anchor := extraction.Token{Text: "Invoice No: INV-001", Region: tc.region}

			got := extraction.Resolve(rvPage(anchor), sameToken)
			rvFloor(t, got, "a same_token rule over a token with a non-finite box")
			for i, c := range got {
				if c.Region != nil {
					t.Errorf("candidate %d carries Region %+v, want nil -- a non-finite coordinate is not geometry", i, *c.Region)
				}
			}

			pair := rvPage(
				extraction.Token{Text: "Invoice No:", Region: tc.region},
				rvTok("INV-001", 0.50, 0.10, 0.60, 0.13),
			)
			if got := extraction.Resolve(pair, right); len(got) != 0 {
				t.Errorf("got %d candidate(s) from a non-finite anchor box, want 0: %+v", len(got), got)
			}
		})
	}

	real := rvPage(rvTok("Invoice No:", 0.10, 0.10, 0.25, 0.13), rvTok("INV-001", 0.50, 0.10, 0.60, 0.13))
	rvControl(t, extraction.Resolve(real, right), "the same right rule over two finite boxes")
}

// A token cannot be its own value: usableBox forces X0 < X1, so no box sits to its own right.
func TestResolve_AnchorNeverRelatesToItself(t *testing.T) {
	first := rvTok("12345678-0001", 0.10, 0.10, 0.30, 0.13)
	second := rvTok("87654321-0002", 0.35, 0.10, 0.55, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-tin", "supplier_tin", rvaLabelTINFormat, extraction.RelRight, 0.5, extraction.ShapeTIN),
	}}

	if got := extraction.Resolve(rvPage(first), rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) from a lone token that matches its own rule, want 0 -- an anchor is not its own value: %+v", len(got), got)
	}

	got := extraction.Resolve(rvPage(first, second), rules)
	rvControl(t, got, "the same rule with a second token to the right")
	if len(got) != 1 {
		t.Fatalf("got %d candidate(s), want exactly 1 -- only the first token has a neighbour to its right: %+v", len(got), got)
	}
	if got[0].Value != "87654321-0002" {
		t.Errorf("Value = %q, want the token to the RIGHT of the anchor, %q", got[0].Value, "87654321-0002")
	}
}

// --- what a candidate carries ----------------------------------------------------

// A label that spans its whole token is its own value: the format-only TIN sweep depends on it.
func TestResolve_SameTokenKeepsALabelThatIsItsOwnValue(t *testing.T) {
	tok := rvTok("12345678-0001", 0.10, 0.10, 0.30, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-tin", "supplier_tin", rvaLabelTINFormat, extraction.RelSameToken, 0, extraction.ShapeTIN),
	}}

	got := extraction.Resolve(rvPage(tok), rules)
	rvFloor(t, got, "a same_token rule whose label matches the whole token")

	if len(got) != 1 {
		t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
	}
	if got[0].Value != "12345678-0001" {
		t.Errorf("Value = %q, want the whole token %q -- a match spanning the token leaves the token, not an empty remainder",
			got[0].Value, "12345678-0001")
	}
}

// The separator trim covers whitespace AND dashes, so a spaced dash between label and value is
// removed whichever comes first.
func TestResolve_SameTokenTrimsASpacedDashSeparator(t *testing.T) {
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "total", rvLabelTotal, extraction.RelSameToken, 0, extraction.ShapeAmount),
	}}

	for _, text := range []string{"Total - 1,500.00", "Total – 1,500.00", "Total — 1,500.00", "Total: 1,500.00"} {
		t.Run(text, func(t *testing.T) {
			got := extraction.Resolve(rvPage(rvTok(text, 0.10, 0.10, 0.45, 0.13)), rules)
			rvFloor(t, got, "a same_token amount rule over "+text)

			if len(got) != 1 {
				t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
			}
			if got[0].Value != "1500.00" {
				t.Errorf("Value = %q, want %q -- the separator between label and value must be trimmed", got[0].Value, "1500.00")
			}
		})
	}
}

// A shape that reads nothing produces no candidate. It must never produce an empty Value: the
// invoices column would take the blank as an answer.
func TestResolve_EmitsNothingWhenTheShapeReadsNothing(t *testing.T) {
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "total", rvLabelTotal, extraction.RelSameToken, 0, extraction.ShapeAmount),
	}}

	if got := extraction.Resolve(rvPage(rvTok("Total: not money", 0.10, 0.10, 0.45, 0.13)), rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) where the amount shape read nothing, want 0: %+v", len(got), got)
	}
	rvControl(t, extraction.Resolve(rvPage(rvTok("Total: 1,500.00", 0.10, 0.10, 0.45, 0.13)), rules), "the same rule over a readable amount")
}

func TestResolve_ValueIsNeverEmpty(t *testing.T) {
	got := extraction.Resolve(rvPage(rvMixedTokens()...), rvGeneric())
	rvFloor(t, got, "the mixed page under the generic rule set")

	for i, c := range got {
		if c.Value == "" {
			t.Errorf("candidate %d (%s, rule %s) has an empty Value; a blank is not an answer", i, c.Field, c.RuleID)
		}
	}
}

// One token can answer two fields. The candidates differ by RuleID, and law E07 is not in play:
// these are candidates, not fields.
func TestResolve_OneTokenFeedsTwoFields(t *testing.T) {
	tok := rvTok("Sub-total: 1,500.00", 0.10, 0.10, 0.45, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-sub", "subtotal", rvaLabelSubtotal, extraction.RelSameToken, 0, extraction.ShapeAmount),
		rvLearned(t, "rule-tot", "total", rvLabelTotal, extraction.RelSameToken, 0, extraction.ShapeAmount),
	}}

	got := extraction.Resolve(rvPage(tok), rules)
	rvFloor(t, got, "one token matching a subtotal rule and a total rule")

	if len(got) != 2 {
		t.Fatalf("got %d candidate(s), want 2 -- one per field: %+v", len(got), got)
	}
	for i, want := range []struct{ field, ruleID string }{{"subtotal", "rule-sub"}, {"total", "rule-tot"}} {
		if got[i].Field != want.field || got[i].RuleID != want.ruleID {
			t.Errorf("candidate %d = (%s, rule %s), want (%s, rule %s)", i, got[i].Field, got[i].RuleID, want.field, want.ruleID)
		}
		if got[i].Value != "1500.00" {
			t.Errorf("candidate %d has Value %q, want %q", i, got[i].Value, "1500.00")
		}
	}
}

// The output order is the vocabulary's, not the rule set's. The rules here run backwards
// through HeaderFields, so generation order and vocabulary order are exact opposites.
func TestResolve_KeepsVocabularyOrderWhenTheRulesRunBackwards(t *testing.T) {
	tokens := []extraction.Token{
		rvTok("Total: 1,500.00", 0.10, 0.40, 0.45, 0.43),
		rvTok("Currency: NGN", 0.10, 0.30, 0.45, 0.33),
		rvTok("Date: 2026-03-04", 0.10, 0.20, 0.45, 0.23),
		rvTok("Invoice No: INV-001", 0.10, 0.10, 0.45, 0.13),
	}
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-total", "total", rvLabelTotal, extraction.RelSameToken, 0, extraction.ShapeAmount),
		rvLearned(t, "rule-ccy", "currency", rvaLabelCurrency, extraction.RelSameToken, 0, extraction.ShapeCurrency),
		rvLearned(t, "rule-date", "issue_date", rvLabelDate, extraction.RelSameToken, 0, extraction.ShapeDate),
		rvLearned(t, "rule-inv", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}

	got := extraction.Resolve(rvPage(tokens...), rules)
	rvFloor(t, got, "four backwards-ordered rules over four tokens")

	want := []string{"invoice_number", "issue_date", "currency", "total"}
	if seq := rvaFields(got); !slices.Equal(seq, want) {
		t.Errorf("emitted field order %v, want %v -- HeaderFields decides the order, not the rule set", seq, want)
	}
	// The rule set is genuinely backwards, so the assertion above is not satisfied by both.
	if slices.IsSorted(want) {
		t.Fatal("the expected order happens to be alphabetical; pick fields whose vocabulary order is not incidental")
	}
}

func TestResolve_EmptyInputsYieldAnEmptyNonNilSlice(t *testing.T) {
	rules := rvMixedRules(t)
	pages := rvPage(rvMixedTokens()...)

	cases := []struct {
		name  string
		pages []extraction.TokenPage
		rules extraction.RuleSet
	}{
		{"no pages, no rules", nil, extraction.RuleSet{}},
		{"empty page slice", []extraction.TokenPage{}, rules},
		{"a page with no token", rvPage(), rules},
		{"tokens but no rule", pages, extraction.RuleSet{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A panic here fails the test, which is the no-panic half of the assertion.
			got := extraction.Resolve(tc.pages, tc.rules)
			if got == nil {
				t.Error("Resolve returned a nil slice; every caller would have to coerce it away from a JSON null")
			}
			if len(got) != 0 {
				t.Errorf("got %d candidate(s), want 0: %+v", len(got), got)
			}
		})
	}
	rvControl(t, extraction.Resolve(pages, rules), "the same rule set over the mixed page")
}
