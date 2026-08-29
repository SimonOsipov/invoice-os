// resolve_test.go: V-01..V-15, V-17, V-21, V-22. External package: every spec reaches only
// exported symbols.
//
// Two rules bind every spec here. An assertion that quantifies over Resolve's output carries
// rvFloor first, because a loop over an empty slice reports a pass. An assertion that the
// output is EMPTY carries a positive control in the same test, because zero candidates is also
// what a Resolve that never returns anything produces.
package extraction_test

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- labels -----------------------------------------------------------------

const (
	rvLabelInvoiceNo = `(?i)\binvoice\s*no\.?`
	rvLabelDate      = `(?i)\bdate\b`
	rvLabelTotal     = `(?i)\btotal\b`
	rvLabelSupplier  = `(?i)\bsupplier\b`
)

// --- harness ----------------------------------------------------------------

// rvRule builds a rule through ParseRule, the only constructor that compiles Label. V-22 is the
// one spec that reaches past it, on purpose.
func rvRule(t *testing.T, label string, kind extraction.RelationKind, maxDist float64, shape extraction.Shape) extraction.Rule {
	t.Helper()

	body := fmt.Sprintf(`{"label":%q,"relation":{"kind":%q,"max_distance":%v},"shape":%q}`,
		label, string(kind), maxDist, string(shape))
	r, err := extraction.ParseRule([]byte(body))
	if err != nil {
		t.Fatalf("ParseRule(%s): %v", body, err)
	}
	return r
}

func rvLearned(t *testing.T, id, field, label string, kind extraction.RelationKind, maxDist float64, shape extraction.Shape) extraction.AnchorRule {
	t.Helper()
	return extraction.AnchorRule{ID: id, Field: field, Rule: rvRule(t, label, kind, maxDist, shape)}
}

func rvTier1(t *testing.T, key, field, label string, kind extraction.RelationKind, maxDist float64, shape extraction.Shape) extraction.Tier1Rule {
	t.Helper()
	return extraction.Tier1Rule{Key: key, Field: field, Rule: rvRule(t, label, kind, maxDist, shape)}
}

func rvBox(x0, y0, x1, y1 float64) extraction.Region {
	return extraction.Region{Page: 1, X0: x0, Y0: y0, X1: x1, Y1: y1}
}

func rvTok(text string, x0, y0, x1, y1 float64) extraction.Token {
	return extraction.Token{Text: text, Region: rvBox(x0, y0, x1, y1)}
}

// rvBoxless is the DOCX shape: a valid page number and four zero coordinates. A real box, not
// an absent one -- Token.Region is a value, so nil-ness cannot tell the two apart.
func rvBoxless(text string) extraction.Token {
	return extraction.Token{Text: text, Region: extraction.Region{Page: 1}}
}

func rvPage(tokens ...extraction.Token) []extraction.TokenPage {
	return []extraction.TokenPage{{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: tokens}}
}

// rvFloor fails when got is empty. Every assertion quantifying over Resolve's output needs it.
func rvFloor(t *testing.T, got []extraction.Candidate, what string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("Resolve produced no candidate for %s; every assertion below would pass vacuously", what)
	}
}

// rvControl fails when a positive control is empty, which is what makes a paired zero-result
// assertion mean anything.
func rvControl(t *testing.T, got []extraction.Candidate, what string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("the positive control (%s) produced no candidate; the zero asserted above holds equally against a Resolve that never returns anything", what)
	}
}

func rvFor(got []extraction.Candidate, field string) []extraction.Candidate {
	var out []extraction.Candidate
	for _, c := range got {
		if c.Field == field {
			out = append(out, c)
		}
	}
	return out
}

func rvValues(cs []extraction.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Value
	}
	return out
}

// rvMixedTokens is one page exercising all three relations across three fields, so a spec over
// it is not a single-rule spec in disguise.
func rvMixedTokens() []extraction.Token {
	return []extraction.Token{
		rvTok("Invoice No: INV-001", 0.10, 0.05, 0.45, 0.08),
		rvTok("Date:", 0.10, 0.12, 0.20, 0.15),
		rvTok("2026-03-04", 0.25, 0.12, 0.40, 0.15),
		rvTok("Total", 0.10, 0.30, 0.20, 0.33),
		rvTok("NGN 1,500.00", 0.10, 0.35, 0.30, 0.38),
		rvTok("Invoice No: INV-002", 0.55, 0.05, 0.90, 0.08),
	}
}

func rvMixedRules(t *testing.T) extraction.RuleSet {
	t.Helper()
	return extraction.RuleSet{
		Learned: []extraction.AnchorRule{
			rvLearned(t, "learned-inv", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
		},
		Tier1: []extraction.Tier1Rule{
			rvTier1(t, "g.issue_date.right", "issue_date", rvLabelDate, extraction.RelRight, 0.35, extraction.ShapeDate),
			rvTier1(t, "g.total.below", "total", rvLabelTotal, extraction.RelBelow, 0.06, extraction.ShapeAmount),
		},
	}
}

// rvGeneric is the shipped Tier-1 set, read from the package and never re-typed here: a
// test-local fork of the ten lexicon patterns drifts from the shipped ones silently
// (TestTier1_ReusesTheAnchorLexiconPatterns).
func rvGeneric() extraction.RuleSet {
	return extraction.RuleSet{Tier1: extraction.Tier1Rules}
}

// rvCorpusPages reads a committed corpus fixture through the real reader and CollectTokens, so
// the geometry under test is pdfium's and not a hand-typed approximation of it.
func rvCorpusPages(t *testing.T, name string) []extraction.TokenPage {
	t.Helper()

	var pages []extraction.TokenPage
	doc := extraction.Document{Bytes: fxRead(t, name), ContentType: "application/pdf"}
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), doc, extraction.CollectTokens(&pages)); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if len(pages) == 0 {
		t.Fatalf("%s yielded no page; every assertion over it would report nothing", name)
	}
	return pages
}

const rvCorpusInline = "corpus_inline_labels.pdf"

// --- the specs --------------------------------------------------------------

// V-01
func TestResolve_SameTokenCapturesTheRemainder(t *testing.T) {
	tok := rvTok("Invoice No: INV-001", 0.10, 0.10, 0.45, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}

	got := extraction.Resolve(rvPage(tok), rules)
	rvFloor(t, got, "a same_token rule over \"Invoice No: INV-001\"")

	if len(got) != 1 {
		t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Field != "invoice_number" {
		t.Errorf("Field = %q, want %q", c.Field, "invoice_number")
	}
	if c.Value != "INV-001" {
		t.Errorf("Value = %q, want %q -- the label and its separator must be trimmed off", c.Value, "INV-001")
	}
	if c.Region == nil {
		t.Errorf("Region is nil; same_token carries the anchor token's own box")
	} else if *c.Region != tok.Region {
		t.Errorf("Region = %+v, want the anchor token's own box %+v", *c.Region, tok.Region)
	}
	if c.Distance != 0 {
		t.Errorf("Distance = %v, want 0 -- same_token has no gap", c.Distance)
	}
	if c.Reason != extraction.ReasonNone {
		t.Errorf("Reason = %q, want ReasonNone", c.Reason)
	}
	if c.Tier != extraction.TierLearned {
		t.Errorf("Tier = %v, want TierLearned", c.Tier)
	}
	if c.RuleID != "rule-1" {
		t.Errorf("RuleID = %q, want the stored row's id %q", c.RuleID, "rule-1")
	}
}

// V-02
func TestResolve_RightFindsTheNeighbouringToken(t *testing.T) {
	anchor := rvTok("Invoice No:", 0.10, 0.10, 0.25, 0.13)
	value := rvTok("INV-001", 0.30, 0.10, 0.40, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
	}}

	got := extraction.Resolve(rvPage(anchor, value), rules)
	rvFloor(t, got, "a right rule over a label and its neighbour")

	if len(got) != 1 {
		t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Value != "INV-001" {
		t.Errorf("Value = %q, want %q", c.Value, "INV-001")
	}
	if want := 0.05; math.Abs(c.Distance-want) > 1e-9 {
		t.Errorf("Distance = %v, want %v (value.X0 - anchor.X1)", c.Distance, want)
	}
	if c.Region == nil {
		t.Errorf("Region is nil; a right relation carries the VALUE token's box")
	} else if *c.Region != value.Region {
		t.Errorf("Region = %+v, want the value token's box %+v", *c.Region, value.Region)
	}
}

// V-03
func TestResolve_RightIgnoresATokenOutsideMaxDistance(t *testing.T) {
	anchor := rvTok("Invoice No:", 0.10, 0.10, 0.25, 0.13)
	far := rvTok("INV-001", 0.90, 0.10, 0.98, 0.13)
	near := rvTok("INV-001", 0.30, 0.10, 0.40, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
	}}

	if got := extraction.Resolve(rvPage(anchor, far), rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) for a value 0.65 to the right of a 0.35 max_distance, want 0: %+v", len(got), got)
	}
	rvControl(t, extraction.Resolve(rvPage(anchor, near), rules), "the same page with the value inside max_distance")
}

// V-04
func TestResolve_RightIgnoresATokenOnAnotherLine(t *testing.T) {
	anchor := rvTok("Invoice No:", 0.10, 0.10, 0.25, 0.13)
	nextLine := rvTok("INV-001", 0.30, 0.16, 0.40, 0.19)
	sameLine := rvTok("INV-001", 0.30, 0.10, 0.40, 0.13)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
	}}

	if got := extraction.Resolve(rvPage(anchor, nextLine), rules); len(got) != 0 {
		t.Errorf("got %d candidate(s) for a value one line below the anchor, want 0 -- the vertical spans do not overlap: %+v", len(got), got)
	}
	rvControl(t, extraction.Resolve(rvPage(anchor, sameLine), rules), "the same pair on one line")
}

// V-05
func TestResolve_BelowFindsTheStackedValue(t *testing.T) {
	t.Run("stacked total", func(t *testing.T) {
		anchor := rvTok("Total", 0.10, 0.50, 0.20, 0.53)
		value := rvTok("NGN 1,500.00", 0.10, 0.55, 0.25, 0.58)
		rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
			rvLearned(t, "rule-1", "total", rvLabelTotal, extraction.RelBelow, 0.06, extraction.ShapeAmount),
		}}

		got := extraction.Resolve(rvPage(anchor, value), rules)
		rvFloor(t, got, "a below rule over a stacked total")

		if len(got) != 1 {
			t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
		}
		if got[0].Value != "1500.00" {
			t.Errorf("Value = %q, want %q", got[0].Value, "1500.00")
		}
	})

	// The overlap denominator is min(anchor span, value span). Under the anchor's span alone a
	// wide label over a narrow value overlaps by 20% and the correct candidate is dropped.
	t.Run("wide label over a narrow value", func(t *testing.T) {
		anchor := rvTok("Supplier Name:", 0.10, 0.20, 0.40, 0.23)
		value := rvTok("ACME", 0.10, 0.25, 0.16, 0.28)
		rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
			rvLearned(t, "rule-1", "supplier_name", rvLabelSupplier, extraction.RelBelow, 0.06, extraction.ShapeName),
		}}

		got := extraction.Resolve(rvPage(anchor, value), rules)
		rvFloor(t, got, "a wide label stacked over a narrow value")

		if len(got) != 1 {
			t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
		}
		if got[0].Value != "ACME" {
			t.Errorf("Value = %q, want %q", got[0].Value, "ACME")
		}
	})
}

// V-06
func TestResolve_KeepsBothPlausibleCandidates(t *testing.T) {
	t.Run("two tokens, one field", func(t *testing.T) {
		top := rvTok("Invoice No: INV-002", 0.10, 0.10, 0.45, 0.13)
		bottom := rvTok("Invoice No: INV-001", 0.10, 0.20, 0.45, 0.23)
		rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
			rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
		}}

		got := extraction.Resolve(rvPage(top, bottom), rules)
		rvFloor(t, got, "two tokens matching one invoice_number rule")

		nums := rvFor(got, "invoice_number")
		if len(nums) != 2 {
			t.Fatalf("got %d invoice_number candidate(s), want 2 -- neither plausible value may be dropped: %+v", len(nums), nums)
		}
		// Same tier and distance, so the region keys decide: reading order, top box first.
		if want := []string{"INV-002", "INV-001"}; !reflect.DeepEqual(rvValues(nums), want) {
			t.Errorf("values = %v, want %v (ascending Region.Y0)", rvValues(nums), want)
		}
	})

	// One token, two readings: both survive and only the Value key separates them.
	t.Run("two readings, one token", func(t *testing.T) {
		tok := rvTok("Date: 12/03/2026", 0.10, 0.10, 0.35, 0.13)
		rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
			rvLearned(t, "rule-1", "issue_date", rvLabelDate, extraction.RelSameToken, 0, extraction.ShapeDate),
		}}

		got := extraction.Resolve(rvPage(tok), rules)
		rvFloor(t, got, "an ambiguous numeric date")

		dates := rvFor(got, "issue_date")
		if len(dates) != 2 {
			t.Fatalf("got %d issue_date candidate(s), want 2 -- 12/03/2026 reads two ways: %+v", len(dates), dates)
		}
		if want := []string{"2026-03-12", "2026-12-03"}; !reflect.DeepEqual(rvValues(dates), want) {
			t.Errorf("values = %v, want %v (byte order; every earlier sort key ties)", rvValues(dates), want)
		}
	})
}

// V-07
func TestResolve_IsDeterministicUnderInputPermutation(t *testing.T) {
	base := rvMixedTokens()
	rules := rvMixedRules(t)

	orders := [][]int{
		{0, 1, 2, 3, 4, 5},
		{5, 4, 3, 2, 1, 0},
		{2, 0, 4, 1, 5, 3},
		{3, 5, 1, 4, 0, 2},
		{1, 3, 0, 5, 2, 4},
	}

	results := make([][]extraction.Candidate, len(orders))
	for i, order := range orders {
		if len(order) != len(base) {
			t.Fatalf("permutation %d covers %d of %d tokens", i, len(order), len(base))
		}
		toks := make([]extraction.Token, len(order))
		for j, idx := range order {
			toks[j] = base[idx]
		}
		results[i] = extraction.Resolve(rvPage(toks...), rules)
	}

	rvFloor(t, results[0], "the mixed page in reader order")

	for i := 1; i < len(results); i++ {
		if !reflect.DeepEqual(results[0], results[i]) {
			t.Errorf("permutation %d produced a different result:\n got %+v\nwant %+v", i, results[i], results[0])
		}
	}
}

// V-08
func TestResolve_IsDeterministicAcrossRepeatedCalls(t *testing.T) {
	pages := rvPage(rvMixedTokens()...)
	rules := rvMixedRules(t)

	first := extraction.Resolve(pages, rules)
	rvFloor(t, first, "the mixed page on the first call")

	for i := 1; i < 100; i++ {
		if got := extraction.Resolve(pages, rules); !reflect.DeepEqual(first, got) {
			t.Fatalf("call %d differed from the first:\n got %+v\nwant %+v", i, got, first)
		}
	}
}

// V-09
func TestResolve_ReasonIsAlwaysNone(t *testing.T) {
	got := extraction.Resolve(rvCorpusPages(t, rvCorpusInline), rvGeneric())
	rvFloor(t, got, rvCorpusInline+" under the generic rule set")

	for i, c := range got {
		if c.Reason != extraction.ReasonNone {
			t.Errorf("candidate %d (%s = %q) has Reason %q, want ReasonNone -- the doubt pass owns that slot", i, c.Field, c.Value, c.Reason)
		}
	}
}

// V-10
func TestResolve_DegenerateBoxYieldsANilRegion(t *testing.T) {
	cases := []struct {
		name   string
		region extraction.Region
	}{
		{"docx zero box", extraction.Region{Page: 1}},
		{"zero width", extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.10, Y1: 0.13}},
		{"zero height", extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.10}},
		{"out of range", extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 1.40, Y1: 0.13}},
		{"page below one", extraction.Region{Page: 0, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.13}},
	}

	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := extraction.Token{Text: "Invoice No: INV-001", Region: tc.region}

			got := extraction.Resolve(rvPage(tok), rules)
			rvFloor(t, got, "a same_token rule over a token with an unusable box")

			for i, c := range got {
				if c.Region != nil {
					t.Errorf("candidate %d carries Region %+v, want nil -- an unusable box must not become a box at the page corner", i, *c.Region)
				}
			}
		})
	}
}

// V-11
func TestResolve_SpatialRelationsSkipDegenerateBoxes(t *testing.T) {
	boxless := rvPage(rvBoxless("Invoice No:"), rvBoxless("INV-001"))

	t.Run("right", func(t *testing.T) {
		rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
			rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
		}}

		if got := extraction.Resolve(boxless, rules); len(got) != 0 {
			t.Errorf("got %d candidate(s) from zero boxes, want 0 -- a zero box makes every token falsely adjacent: %+v", len(got), got)
		}
		real := rvPage(rvTok("Invoice No:", 0.10, 0.10, 0.25, 0.13), rvTok("INV-001", 0.30, 0.10, 0.40, 0.13))
		rvControl(t, extraction.Resolve(real, rules), "the same two tokens with real boxes, side by side")
	})

	t.Run("below", func(t *testing.T) {
		rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
			rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelBelow, 0.06, extraction.ShapeInvoiceNumber),
		}}

		if got := extraction.Resolve(boxless, rules); len(got) != 0 {
			t.Errorf("got %d candidate(s) from zero boxes, want 0: %+v", len(got), got)
		}
		real := rvPage(rvTok("Invoice No:", 0.10, 0.10, 0.25, 0.13), rvTok("INV-001", 0.10, 0.15, 0.25, 0.18))
		rvControl(t, extraction.Resolve(real, rules), "the same two tokens with real boxes, stacked")
	})
}

// V-12
func TestResolve_EveryRegionSatisfiesTheColumnCheck(t *testing.T) {
	got := extraction.Resolve(rvCorpusPages(t, rvCorpusInline), rvGeneric())
	rvFloor(t, got, rvCorpusInline+" under the generic rule set")

	withRegion := 0
	for i, c := range got {
		if c.Region == nil {
			continue
		}
		withRegion++
		r := *c.Region
		if r.Page < 1 {
			t.Errorf("candidate %d (%s) has Page %d, want >= 1", i, c.Field, r.Page)
		}
		if !(r.X0 >= 0 && r.X0 <= r.X1 && r.X1 <= 1) {
			t.Errorf("candidate %d (%s) has X0=%v X1=%v, want 0 <= X0 <= X1 <= 1", i, c.Field, r.X0, r.X1)
		}
		if !(r.Y0 >= 0 && r.Y0 <= r.Y1 && r.Y1 <= 1) {
			t.Errorf("candidate %d (%s) has Y0=%v Y1=%v, want 0 <= Y0 <= Y1 <= 1", i, c.Field, r.Y0, r.Y1)
		}
	}
	if withRegion == 0 {
		t.Fatal("no candidate carried a non-nil Region; the bbox assertions above ran over nothing")
	}
}

// V-13
func TestResolve_CapsCandidatesPerField(t *testing.T) {
	const matching = 40
	if extraction.MaxCandidatesPerFieldForTest >= matching {
		t.Fatalf("the cap is %d and the page carries %d matching tokens; the truncation would not be exercised",
			extraction.MaxCandidatesPerFieldForTest, matching)
	}

	const anchorX1 = 0.06
	anchor := rvTok("Invoice No:", 0.02, 0.10, anchorX1, 0.13)

	toks := []extraction.Token{anchor}
	var wantDistances []float64
	for i := range matching {
		x0 := 0.07 + 0.005*float64(i)
		toks = append(toks, rvTok(fmt.Sprintf("INV-%04d", i+1), x0, 0.10, x0+0.004, 0.13))
		if i < extraction.MaxCandidatesPerFieldForTest {
			wantDistances = append(wantDistances, x0-anchorX1)
		}
	}

	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelRight, 0.35, extraction.ShapeInvoiceNumber),
	}}

	got := extraction.Resolve(rvPage(toks...), rules)
	rvFloor(t, got, "40 tokens matching one invoice_number rule")

	nums := rvFor(got, "invoice_number")
	if len(nums) != extraction.MaxCandidatesPerFieldForTest {
		t.Fatalf("got %d invoice_number candidate(s), want exactly the cap %d", len(nums), extraction.MaxCandidatesPerFieldForTest)
	}

	gotDistances := make([]float64, len(nums))
	for i, c := range nums {
		gotDistances[i] = c.Distance
	}
	slices.Sort(gotDistances)
	slices.Sort(wantDistances)
	for i := range wantDistances {
		if math.Abs(gotDistances[i]-wantDistances[i]) > 1e-9 {
			t.Fatalf("kept distances %v, want the %d smallest %v -- the cap truncates AFTER ordering",
				gotDistances, extraction.MaxCandidatesPerFieldForTest, wantDistances)
		}
	}
}

// V-14
func TestResolve_ReturnsFieldsInVocabularyOrder(t *testing.T) {
	got := extraction.Resolve(rvPage(rvMixedTokens()...), rvMixedRules(t))
	rvFloor(t, got, "the mixed page")

	var seq []string
	for _, c := range got {
		if len(seq) == 0 || seq[len(seq)-1] != c.Field {
			seq = append(seq, c.Field)
		}
	}
	if len(seq) < 3 {
		t.Fatalf("only %d distinct field run(s) (%v); with fewer than 3 the order below is satisfied trivially", len(seq), seq)
	}

	// A field's candidates must be contiguous, else the run list repeats a name.
	for i, f := range seq {
		if slices.Index(seq, f) != i {
			t.Errorf("field %q appears in two separate runs (%v); its candidates are not grouped", f, seq)
		}
	}

	vocab := extraction.HeaderFields
	at := 0
	for _, f := range seq {
		next := slices.Index(vocab[at:], f)
		if next < 0 {
			t.Fatalf("emitted field order %v is not a subsequence of HeaderFields %v -- %q is out of place", seq, vocab, f)
		}
		at += next + 1
	}
}

// V-15
func TestResolve_NeverReturnsNil(t *testing.T) {
	rules := rvMixedRules(t)

	got := extraction.Resolve(rvPage(rvTok("Lorem ipsum dolor", 0.10, 0.10, 0.40, 0.13)), rules)
	if got == nil {
		t.Error("Resolve returned a nil slice; every caller would have to coerce it away from a JSON null")
	}
	if len(got) != 0 {
		t.Errorf("got %d candidate(s) from a page no rule matches, want 0: %+v", len(got), got)
	}
	rvControl(t, extraction.Resolve(rvPage(rvMixedTokens()...), rules), "the same rule set over the mixed page")
}

// V-17
func TestResolve_HoldsNoStateAcrossCalls(t *testing.T) {
	rules := rvMixedRules(t)
	pagesA := rvPage(rvMixedTokens()...)
	pagesB := rvPage(rvTok("Invoice No: INV-999", 0.10, 0.10, 0.45, 0.13))

	firstA := extraction.Resolve(pagesA, rules)
	rvFloor(t, firstA, "page A on the first call")

	gotB := extraction.Resolve(pagesB, rules)
	rvFloor(t, gotB, "page B")

	if reflect.DeepEqual(firstA, gotB) {
		t.Fatalf("page A and page B resolved identically; the interleaved call exercises nothing:\n%+v", gotB)
	}

	if secondA := extraction.Resolve(pagesA, rules); !reflect.DeepEqual(firstA, secondA) {
		t.Errorf("page A resolved differently after page B:\n got %+v\nwant %+v", secondA, firstA)
	}
}

// V-21
func TestResolve_DropsAnOutOfVocabularyField(t *testing.T) {
	tok := rvTok("Invoice No: INV-001", 0.10, 0.10, 0.45, 0.13)

	unknown := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "nonsense", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}
	if got := extraction.Resolve(rvPage(tok), unknown); len(got) != 0 {
		t.Errorf("got %d candidate(s) for field %q, want 0 -- the field is outside HeaderFields and has no invoices column: %+v", len(got), "nonsense", got)
	}

	known := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}
	rvControl(t, extraction.Resolve(rvPage(tok), known), "the same rule naming invoice_number")
}

// V-22
func TestResolve_IgnoresAnUncompiledRule(t *testing.T) {
	tok := rvTok("Invoice No: INV-001", 0.10, 0.10, 0.45, 0.13)

	// A composite literal cannot set the unexported compiled matcher, so re is nil. The
	// empty-kind case is necessarily uncompiled too: ParseRule rejects an empty kind outright.
	cases := []struct {
		name string
		rule extraction.Rule
	}{
		{"nil matcher", extraction.Rule{
			Label:    rvLabelInvoiceNo,
			Relation: extraction.Relation{Kind: extraction.RelSameToken},
			Shape:    extraction.ShapeInvoiceNumber,
		}},
		{"empty relation kind", extraction.Rule{
			Label: rvLabelInvoiceNo,
			Shape: extraction.ShapeInvoiceNumber,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{
				{Key: "t1.raw", Field: "invoice_number", Rule: tc.rule},
			}}
			// A panic here fails the test, which is the no-panic half of the assertion.
			if got := extraction.Resolve(rvPage(tok), rules); len(got) != 0 {
				t.Errorf("got %d candidate(s) from an unusable rule, want 0: %+v", len(got), got)
			}
		})
	}

	control := extraction.RuleSet{Tier1: []extraction.Tier1Rule{
		rvTier1(t, "t1.parsed", "invoice_number", rvLabelInvoiceNo, extraction.RelSameToken, 0, extraction.ShapeInvoiceNumber),
	}}
	rvControl(t, extraction.Resolve(rvPage(tok), control), "the same label built through ParseRule")
}
