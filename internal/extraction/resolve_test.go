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
	"strings"
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

// --- EXTR-16-02: anchor specificity (D-A) -----------------------------------

// rvSubSpanText is the fused label M1 and M3 both live on: supplier_name matches "Supplier"
// (span 0-8) while supplier_tin matches "Supplier TIN" (span 0-12). The narrower match owns
// nothing on this token, yet today it anchors all three supplier_name relations.
const rvSubSpanText = "Supplier TIN: 99999999-0101"

func rvSubSpanPage() []extraction.TokenPage {
	return rvPage(rvTok(rvSubSpanText, 0.10, 0.10, 0.45, 0.13))
}

// AC-1. The zero asserted for supplier_name carries supplier_tin as its positive control in the
// same test, per this file's second rule.
func TestResolve_ASubSpanLabelDoesNotAnchorAGenericRule(t *testing.T) {
	got := extraction.Resolve(rvSubSpanPage(), rvGeneric())

	if names := rvValues(rvFor(got, "supplier_name")); len(names) != 0 {
		t.Errorf("supplier_name candidates = %q, want none: on %q the supplier_name lexicon match (span 0-8) is a strict sub-span of the supplier_tin match (span 0-12), so it must not anchor a Tier-1 rule",
			names, rvSubSpanText)
	}

	tins := rvFor(got, "supplier_tin")
	rvControl(t, tins, "supplier_tin over the same token")
	if got, want := rvValues(tins), []string{"99999999-0101"}; !slices.Equal(got, want) {
		t.Errorf("supplier_tin candidates = %q, want %q", got, want)
	}
}

// AC-1. The guard on an over-broad D-A: suppression is one-directional, so the entry holding the
// WIDER match keeps its own rule.
func TestResolve_TheWidestLexiconMatchKeepsItsOwnRule(t *testing.T) {
	got := extraction.Resolve(rvSubSpanPage(), rvGeneric())

	tins := rvFor(got, "supplier_tin")
	rvControl(t, tins, "supplier_tin over the widest lexicon match on the token")
	if got, want := rvValues(tins), []string{"99999999-0101"}; !slices.Equal(got, want) {
		t.Errorf("supplier_tin candidates = %q, want %q: supplier_tin owns the widest match on %q and must not be suppressed by the narrower supplier_name match inside it",
			got, want, rvSubSpanText)
	}
	if got, want := tins[0].RuleID, "t1.supplier_tin.same_token"; got != want {
		t.Errorf("supplier_tin candidate RuleID = %q, want %q", got, want)
	}
}

// AC-1. Only a STRICT sub-span loses. Two halves, because the shipped lexicon offers two
// distinct non-strict shapes and neither may suppress:
//
//   - identical offsets. Measured over every label alternative, no two lexicon entries match at
//     the same [start,end) on any token, so the identical-span case that does arise is a rule
//     against its OWN entry -- on "Total" both are [0,5]. A containment test that is not strict
//     suppresses every shipped rule and Resolve returns nothing at all.
//   - overlap with no containment. On "Net Amount Due" subtotal matches [0,10] and total matches
//     [4,14]; neither span holds the other, so both anchor.
func TestResolve_AnEqualSpanMatchIsNotSuppressed(t *testing.T) {
	self := rvPage(
		rvTok("Total", 0.10, 0.30, 0.20, 0.33),
		rvTok("5375.00", 0.40, 0.30, 0.55, 0.33),
	)
	got := extraction.Resolve(self, rvGeneric())
	totals := rvFor(got, "total")
	rvControl(t, totals, `the shipped total rule over a bare "Total" label`)
	if got, want := rvValues(totals), []string{"5375.00"}; !slices.Equal(got, want) {
		t.Errorf(`total candidates over "Total" = %q, want %q: the total rule's own lexicon entry matches at the identical span, and an equal span is not a strict sub-span`, got, want)
	}

	overlap := rvPage(
		rvTok("Net Amount Due", 0.10, 0.30, 0.30, 0.33),
		rvTok("5375.00", 0.40, 0.30, 0.55, 0.33),
	)
	got = extraction.Resolve(overlap, rvGeneric())
	subs := rvFor(got, "subtotal")
	totals = rvFor(got, "total")
	rvControl(t, subs, `subtotal over "Net Amount Due"`)
	rvControl(t, totals, `total over "Net Amount Due"`)
	if got, want := rvValues(subs), []string{"5375.00"}; !slices.Equal(got, want) {
		t.Errorf(`subtotal candidates over "Net Amount Due" = %q, want %q: subtotal matches [0,10] and total [4,14], and neither span holds the other`, got, want)
	}
	if got, want := rvValues(totals), []string{"5375.00"}; !slices.Equal(got, want) {
		t.Errorf(`total candidates over "Net Amount Due" = %q, want %q: subtotal matches [0,10] and total [4,14], and neither span holds the other`, got, want)
	}
}

// AC-1, second sentence (D-5). The lexicon is Tier-1's own vocabulary and has no authority over
// a tenant's learned label, so the gate is on TierGeneric. The learned rule's label matches
// [0,8] on the fused token -- the same strict sub-span that costs the shipped rule its anchor.
func TestResolve_ALearnedRuleIsNeverSuppressedByTheLexicon(t *testing.T) {
	pages := rvPage(
		rvTok(rvSubSpanText, 0.10, 0.10, 0.45, 0.13),
		rvTok("Adeyemi Trading Limited", 0.10, 0.14, 0.40, 0.17),
	)
	rules := extraction.RuleSet{
		Learned: []extraction.AnchorRule{
			rvLearned(t, "learned-supplier", "supplier_name", rvLabelSupplier, extraction.RelBelow, 0.06, extraction.ShapeName),
		},
		Tier1: extraction.Tier1Rules,
	}

	got := extraction.Resolve(pages, rules)
	names := rvFor(got, "supplier_name")
	rvControl(t, names, "the learned supplier_name rule over the fused token")

	if len(names) != 1 {
		t.Fatalf("supplier_name candidates = %+v, want exactly 1: the learned rule survives the sub-span gate while every shipped supplier_name rule loses this anchor", names)
	}
	c := names[0]
	if c.Tier != extraction.TierLearned {
		t.Errorf("supplier_name candidate Tier = %v, want TierLearned", c.Tier)
	}
	if c.RuleID != "learned-supplier" {
		t.Errorf("supplier_name candidate RuleID = %q, want %q", c.RuleID, "learned-supplier")
	}
	if c.Value != "Adeyemi Trading Limited" {
		t.Errorf("supplier_name candidate Value = %q, want %q", c.Value, "Adeyemi Trading Limited")
	}
}

// --- the intervening-label boundary -----------------------------------------
//
// A label owns the value beyond it, so a rightward read stops at one. Every arrangement below
// sits on ONE baseline inside the right relation's 0.35 dial, because that is the only shape
// relatedTokens admits a rightward pair on.

const (
	rvbY0 = 0.70
	rvbY1 = 0.72
)

// rvbRow puts one token on that shared baseline.
func rvbRow(text string, x0, x1 float64) extraction.Token {
	return rvTok(text, x0, rvbY0, x1, rvbY1)
}

// rvbBetween is the token that sits between the anchor and the value.
func rvbBetween(text string) extraction.Token {
	return rvbRow(text, 0.30, 0.38)
}

// rvbPage is the arrangement AC-1 names: a VAT anchor ending at 0.16, one token between, and
// the amount starting at 0.45. vat reads it at 0.29, and Total -- when it is a label -- at 0.07.
func rvbPage(between extraction.Token) []extraction.TokenPage {
	return rvPage(rvbRow("VAT", 0.10, 0.16), between, rvbRow("2,687.50", 0.45, 0.58))
}

func rvbShow(cs []extraction.Candidate) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = fmt.Sprintf("%s=%q via %s tier=%d d=%.6f", c.Field, c.Value, c.RuleID, c.Tier, c.Distance)
	}
	return strings.Join(out, ", ")
}

func rvbRuleIDs(cs []extraction.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.RuleID
	}
	return out
}

// rvbOnly fails unless cs is exactly the one named candidate. Distance is compared too: a
// boundary that dropped the nearest candidate and kept a farther one satisfies a value-only
// assertion.
func rvbOnly(t *testing.T, cs []extraction.Candidate, what, value, ruleID string, distance float64) {
	t.Helper()
	if len(cs) != 1 {
		t.Fatalf("%s = [%s], want exactly one candidate %q from %s at %.6f", what, rvbShow(cs), value, ruleID, distance)
	}
	c := cs[0]
	if c.Value != value || c.RuleID != ruleID || math.Abs(c.Distance-distance) > 1e-9 {
		t.Errorf("%s = [%s], want %q from %s at %.6f", what, rvbShow(cs), value, ruleID, distance)
	}
}

// AC-1. The Total label sits between the VAT anchor and the amount and shares its band, so the
// VAT read stops there. The control is on the SAME page: the label that stopped it is an anchor
// in its own right and its read must survive, or the zero above holds equally against a Resolve
// that reads nothing off this arrangement.
func TestResolve_ARightwardReadStopsAtAnInterveningLabel(t *testing.T) {
	got := extraction.Resolve(rvbPage(rvbBetween("Total")), rvGeneric())
	rvFloor(t, got, "the [VAT | Total | 2,687.50] arrangement")

	if vats := rvFor(got, "vat"); len(vats) != 0 {
		t.Errorf("vat = [%s], want none; Total owns the amount beyond it and the VAT read stops at the label", rvbShow(vats))
	}

	totals := rvFor(got, "total")
	rvControl(t, totals, "the Total label's own rightward read on the same page")
	rvbOnly(t, totals, "total", "2687.50", "t1.total.right", 0.070000)
}

// AC-2. Nothing about the boundary is specific to vat: a Sub-total anchor reaching past a VAT
// label takes the VAT's own amount, and that read stops too. vat's read of the same amount is
// the control -- it crosses nothing.
func TestResolve_TheBoundaryHoldsForASubtotalCrossingVAT(t *testing.T) {
	got := extraction.Resolve(rvPage(
		rvbRow("Sub-total", 0.10, 0.20),
		rvbRow("2,500.00", 0.30, 0.42),
		rvbRow("VAT", 0.44, 0.48),
		rvbRow("187.50", 0.50, 0.60),
	), rvGeneric())
	rvFloor(t, got, "the [Sub-total | 2,500.00 | VAT | 187.50] arrangement")

	rvbOnly(t, rvFor(got, "subtotal"), "subtotal", "2500.00", "t1.subtotal.right", 0.100000)

	vats := rvFor(got, "vat")
	rvControl(t, vats, "the VAT label's own read of the amount beside it")
	rvbOnly(t, vats, "vat", "187.50", "t1.vat.right", 0.020000)
}

// AC-2, over the fields no amount rule reaches. Each arm pairs the blocked arrangement with the
// SAME geometry carrying a token no lexicon entry claims, so the absence is measured against a
// read that demonstrably arrives.
func TestResolve_TheBoundaryHoldsForANonAmountField(t *testing.T) {
	for _, arm := range []struct {
		name           string
		anchor, value  extraction.Token
		between, inert extraction.Token
		field, ruleID  string
		want           string
		distance       float64
	}{
		{
			name:    "an issue-date label between the invoice-number label and the number",
			anchor:  rvbRow("Invoice No", 0.10, 0.20),
			between: rvbRow("Issue Date", 0.28, 0.38),
			inert:   rvbRow("::::", 0.28, 0.38),
			value:   rvbRow("INV-2103", 0.40, 0.52),
			field:   "invoice_number", ruleID: "t1.invoice_number.right",
			want: "INV-2103", distance: 0.200000,
		},
		{
			name:    "a total label between the currency label and the code",
			anchor:  rvbRow("Currency", 0.10, 0.20),
			between: rvbRow("Total", 0.28, 0.36),
			inert:   rvbRow("::::", 0.28, 0.36),
			value:   rvbRow("NGN", 0.40, 0.48),
			field:   "currency", ruleID: "t1.currency.right",
			want: "NGN", distance: 0.200000,
		},
		{
			// Q-11's token: reg_identifier claims [6 16] and vat [0 3], so vat's narrower hit
			// survives anchorOutranked and the token is a label under either reading.
			name:    "a registration phrase between the buyer-TIN label and the TIN",
			anchor:  rvbRow("Buyer TIN", 0.10, 0.20),
			between: rvbRow("TAX / VAT REG NO", 0.28, 0.45),
			inert:   rvbRow("::::", 0.28, 0.45),
			value:   rvbRow("99999999-0101", 0.50, 0.65),
			field:   "buyer_tin", ruleID: "t1.buyer_tin.right",
			want: "99999999-0101", distance: 0.300000,
		},
	} {
		// The control first: without it the absence below is what an arrangement that reaches
		// nothing also reports.
		ctl := rvFor(extraction.Resolve(rvPage(arm.anchor, arm.inert, arm.value), rvGeneric()), arm.field)
		rvControl(t, ctl, arm.name+", with a token no lexicon entry claims in the same slot")
		var reached bool
		for _, c := range ctl {
			if c.RuleID == arm.ruleID && c.Value == arm.want && math.Abs(c.Distance-arm.distance) <= 1e-9 {
				reached = true
			}
		}
		if !reached {
			t.Fatalf("%s: the control produced [%s], want %s to read %q at %.6f; the arrangement never reached the value and the assertion below proves nothing", arm.name, rvbShow(ctl), arm.ruleID, arm.want, arm.distance)
		}

		got := rvFor(extraction.Resolve(rvPage(arm.anchor, arm.between, arm.value), rvGeneric()), arm.field)
		if slices.Contains(rvbRuleIDs(got), arm.ruleID) {
			t.Errorf("%s: %s = [%s], want no %s candidate; the label between owns the value beyond it whatever field the anchor fills", arm.name, arm.field, rvbShow(got), arm.ruleID)
		}
	}
}

// AC-1. The lexicon's own registration phrase is a label like any other. The second arm carries
// it PAST the token start: a pattern anchored at ^ claims nothing there, and every phrase spec
// on this story written before f707543a put its phrase at position 0.
func TestResolve_TheRCPhraseStopsTheVATRead(t *testing.T) {
	page := func(text string) []extraction.TokenPage {
		return rvPage(rvbRow("VAT", 0.10, 0.16), rvbRow(text, 0.30, 0.45), rvbRow("1234567", 0.50, 0.62))
	}

	for _, arm := range []struct{ name, between, inert string }{
		{"the phrase alone on the token", "RC NUMBER", "::::"},
		{"the phrase past the token start", "Ref: RC NUMBER", "Ref: ----"},
	} {
		ctl := rvFor(extraction.Resolve(page(arm.inert), rvGeneric()), "vat")
		rvControl(t, ctl, arm.name+", with a token no lexicon entry claims in the same slot")
		rvbOnly(t, ctl, arm.name+", control vat", "1234567", "t1.vat.right", 0.340000)

		if got := rvFor(extraction.Resolve(page(arm.between), rvGeneric()), "vat"); len(got) != 0 {
			t.Errorf("%s: vat = [%s], want none; a company registration number is not the VAT amount", arm.name, rvbShow(got))
		}
	}
}

// AC-3. The boundary is rightward only. The below twin -- a VAT label taking the Total's value
// one line down -- is measured and deliberately deferred to a story that asks for it, so this
// pins the DECISION: it reds if the predicate is ever given a Y-axis twin for consistency.
func TestResolve_TheBoundaryDoesNotApplyBelow(t *testing.T) {
	got := extraction.Resolve(rvPage(
		rvTok("VAT", 0.10, 0.700, 0.16, 0.720),
		rvTok("Total", 0.10, 0.730, 0.18, 0.750),
		rvTok("2,687.50", 0.10, 0.758, 0.23, 0.778),
	), rvGeneric())
	rvFloor(t, got, "the [VAT] / [Total] / [2,687.50] stack")

	rvbOnly(t, rvFor(got, "vat"), "vat, reading past the Total label one line down", "2687.50", "t1.vat.below", 0.038000)
	rvbOnly(t, rvFor(got, "total"), "total", "2687.50", "t1.total.below", 0.008000)
}

// AC-3. A learned rule is the tenant's answer to "the value is THERE" and the shipped lexicon
// has no authority over it, so the boundary is gated on TierGeneric -- the gate anchorOutranked
// already carries. Both halves are asserted on ONE page: a spec asserting only that the learned
// candidate survives passes equally against a boundary that never fires.
func TestResolve_ALearnedRuleIsNotBounded(t *testing.T) {
	rules := extraction.RuleSet{
		Learned: []extraction.AnchorRule{
			rvLearned(t, "learned-vat", "vat", `(?i)\bvat\b`, extraction.RelRight, 0.35, extraction.ShapeAmount),
		},
		Tier1: extraction.Tier1Rules,
	}

	got := extraction.Resolve(rvbPage(rvbBetween("Total")), rules)
	rvFloor(t, got, "the [VAT | Total | 2,687.50] arrangement under a learned rightward rule")

	vats := rvFor(got, "vat")
	rvControl(t, vats, "the learned vat rule reading past the Total label")
	rvbOnly(t, vats, "vat", "2687.50", "learned-vat", 0.290000)
	if vats[0].Tier != extraction.TierLearned {
		t.Errorf("the surviving vat candidate is tier %v, want TierLearned; the generic rule on this identical geometry is the one that goes", vats[0].Tier)
	}
}

// AC-1. The value token carries a label of its own and must not block its own read: the cut is
// b.X0 < value.X0, strict, so the value is never between the anchor and itself.
func TestResolve_ALabelBesideTheValueDoesNotBlockItself(t *testing.T) {
	got := extraction.Resolve(rvPage(
		rvbRow("Buyer", 0.10, 0.17),
		rvbRow("Buyer: Honeywell Group", 0.25, 0.50),
	), rvGeneric())
	rvFloor(t, got, "the [Buyer | Buyer: Honeywell Group] arrangement")

	names := rvFor(got, "buyer_name")
	rvControl(t, names, "the buyer_name rightward read of a value token that is itself a label")
	if !slices.Contains(rvbRuleIDs(names), "t1.buyer_name.right") {
		t.Fatalf("buyer_name = [%s], want a t1.buyer_name.right candidate; the value token blocked its own read", rvbShow(names))
	}
	for _, c := range names {
		if c.RuleID != "t1.buyer_name.right" {
			continue
		}
		if c.Value != "Buyer: Honeywell Group" || math.Abs(c.Distance-0.080000) > 1e-9 {
			t.Errorf("buyer_name via t1.buyer_name.right = %q at %.6f, want %q at 0.080000", c.Value, c.Distance, "Buyer: Honeywell Group")
		}
	}
}

// AC-1. A label BEYOND the value is between nothing: the cut is b.X0 < value.X0.
func TestResolve_ALabelBeyondTheValueDoesNotBlock(t *testing.T) {
	got := extraction.Resolve(rvPage(
		rvbRow("VAT", 0.10, 0.16),
		rvbRow("2,687.50", 0.25, 0.38),
		rvbRow("Total", 0.45, 0.53),
	), rvGeneric())
	rvFloor(t, got, "the [VAT | 2,687.50 | Total] arrangement")

	vats := rvFor(got, "vat")
	rvControl(t, vats, "the VAT read of an amount with the next label past it")
	rvbOnly(t, vats, "vat", "2687.50", "t1.vat.right", 0.090000)
}

// AC-1. One page, both directions. The Total label sits LEFT of the VAT anchor and blocks
// nothing for vat; the VAT label sits between Total and the amount, so total's own read stops.
// A boundary that looked left would pass a spec asserting only the first half.
func TestResolve_ALabelLeftOfTheAnchorDoesNotBlockIt(t *testing.T) {
	got := extraction.Resolve(rvPage(
		rvbRow("Total", 0.02, 0.08),
		rvbRow("VAT", 0.10, 0.16),
		rvbRow("2,687.50", 0.30, 0.43),
	), rvGeneric())
	rvFloor(t, got, "the [Total | VAT | 2,687.50] arrangement")

	vats := rvFor(got, "vat")
	rvControl(t, vats, "the VAT read with the other label to its left")
	rvbOnly(t, vats, "vat", "2687.50", "t1.vat.right", 0.140000)

	if totals := rvFor(got, "total"); len(totals) != 0 {
		t.Errorf("total = [%s], want none; the VAT label is between the Total anchor and the amount", rvbShow(totals))
	}
}

// AC-1. Only a LABEL blocks. The paired arrangement is the discriminator: the same geometry
// with a lexicon-claimed token in that slot must lose the candidate, or "a non-label does not
// block" holds equally against a boundary that never fires.
func TestResolve_ANonLabelBetweenAnchorAndValueDoesNotBlock(t *testing.T) {
	inert := rvFor(extraction.Resolve(rvbPage(rvbBetween("::::")), rvGeneric()), "vat")
	rvControl(t, inert, "the VAT read past a token no lexicon entry claims")
	rvbOnly(t, inert, "vat, with an unclaimed token between", "2687.50", "t1.vat.right", 0.290000)

	if got := rvFor(extraction.Resolve(rvbPage(rvbBetween("Total")), rvGeneric()), "vat"); len(got) != 0 {
		t.Errorf("vat with a LABEL in the same slot = [%s], want none; the two arrangements differ only in the middle token's text, so a boundary that ignores text passes the assertion above and fails here", rvbShow(got))
	}
}

// AC-1. The label must share the anchor's band. The paired arrangement moves the same token
// back onto the baseline, so this measures the band test rather than describing it.
func TestResolve_ALabelOutsideTheAnchorsBandDoesNotBlock(t *testing.T) {
	off := rvFor(extraction.Resolve(rvbPage(rvTok("Total", 0.30, 0.90, 0.38, 0.92)), rvGeneric()), "vat")
	rvControl(t, off, "the VAT read past a label two tenths of a page below the baseline")
	rvbOnly(t, off, "vat, with the label off the band", "2687.50", "t1.vat.right", 0.290000)

	if got := rvFor(extraction.Resolve(rvbPage(rvbBetween("Total")), rvGeneric()), "vat"); len(got) != 0 {
		t.Errorf("vat with the same label ON the band = [%s], want none; the two arrangements differ only in the middle token's Y, so a boundary that ignores the band passes the assertion above and fails here", rvbShow(got))
	}
}

// AC-1. Label-ness is precomputed once per page, so the boundary has to read each page's OWN
// array: page 1 carries a label in the slot where page 2 carries a token no lexicon entry
// claims. Exactly one VAT read survives and it is page 2's -- a boundary reading page 1's
// labels for page 2 loses it, and one reading page 2's for page 1 keeps two. Neither shape is
// reachable on a single page, which is why this arrangement has two.
func TestResolve_TheBoundaryReadsEachPagesOwnLabels(t *testing.T) {
	row := func(page int, text string, x0, x1 float64) extraction.Token {
		return extraction.Token{Text: text, Region: extraction.Region{Page: page, X0: x0, Y0: rvbY0, X1: x1, Y1: rvbY1}}
	}
	got := extraction.Resolve([]extraction.TokenPage{
		{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
			row(1, "VAT", 0.10, 0.16), row(1, "Total", 0.30, 0.38), row(1, "2,687.50", 0.45, 0.58),
		}},
		{Number: 2, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
			row(2, "VAT", 0.10, 0.16), row(2, "::::", 0.30, 0.38), row(2, "1,234.50", 0.45, 0.58),
		}},
	}, rvGeneric())
	rvFloor(t, got, "the two-page arrangement")

	// The control: page 1 was walked. Its Total label reads its own value, so the single VAT
	// candidate below is not what a Resolve that only ever reached page 2 also returns.
	rvbOnly(t, rvFor(got, "total"), "total, read off page 1", "2687.50", "t1.total.right", 0.070000)

	vats := rvFor(got, "vat")
	rvControl(t, vats, "the VAT read on the page whose middle token no lexicon entry claims")
	rvbOnly(t, vats, "vat", "1234.50", "t1.vat.right", 0.290000)
}

// EXTR-22. Candidate.Adjacent is a constant of the relation, and BOTH beside-the-label
// relations set it. Every doubtful cell the corpus produces heads on a below read, so a flag
// wired for RelRight alone -- the shape the boundary predicate has -- leaves the doubt with
// nothing to work on and every downstream assertion vacuously true.
func TestResolve_EveryRelationBesideTheLabelMarksItsCandidateAdjacent(t *testing.T) {
	got := extraction.Resolve(rvPage(rvMixedTokens()...), rvMixedRules(t))
	rvFloor(t, got, "the mixed-relation arrangement")

	for _, tc := range []struct {
		field    string
		relation string
		adjacent bool
	}{
		{"invoice_number", "same_token", false},
		{"issue_date", "right", true},
		{"total", "below", true},
	} {
		cands := rvFor(got, tc.field)
		if len(cands) == 0 {
			t.Errorf("no %s candidate; the %s relation is unread and its flag is unasserted", tc.field, tc.relation)
			continue
		}
		for _, c := range cands {
			if c.Adjacent != tc.adjacent {
				t.Errorf("%s = %q via the %s relation reads Adjacent = %v, want %v", tc.field, c.Value, tc.relation, c.Adjacent, tc.adjacent)
			}
		}
	}
}

// --- EXTR-25-02: fallback tier precedence -----------------------------------
//
// TierFallback exists as a declaration only -- Resolve still hardcodes TierGeneric for every
// Tier-1 rule regardless of Fallback (that wiring is EXTR-25-02's own implementation, not this
// spec pass). Below, a Fallback: true rule therefore resolves exactly like an ordinary one, so
// a candidate that should lose to a label instead competes with it on Distance or reading order
// alone -- the red this test pins.

// AC-2.1. A labelled reading must beat a fallback one regardless of distance or reading order,
// even when the fallback wins on every other axis (Distance 0 vs 0.12, Y0 0.10 vs 0.40).
func TestResolve_AFallbackNeverOutranksALabelledReading(t *testing.T) {
	t.Run("fallback closer", func(t *testing.T) {
		label := rvTier1(t, "g.currency.right", "currency", `(?i)currency:`, extraction.RelRight, 0.35, extraction.ShapeName)
		fallback := extraction.Tier1Rule{
			Key: "g.currency.sweep", Field: "currency",
			Rule:     rvRule(t, `^NGN$`, extraction.RelSameToken, 0, extraction.ShapeName),
			Fallback: true,
		}
		page := rvPage(
			rvTok("Currency:", 0.10, 0.10, 0.20, 0.13),
			rvTok("USD", 0.32, 0.10, 0.40, 0.13), // gap 0.12 from the label
			rvTok("NGN", 0.10, 0.40, 0.20, 0.43), // same_token, distance 0
		)
		rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{label, fallback}}

		got := extraction.Resolve(page, rules)
		cands := rvFor(got, "currency")
		rvFloor(t, cands, "the [Currency: USD] label beside a bare NGN token")

		field, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: got}), "currency")
		if !ok || field.Value == nil {
			t.Fatalf("currency decided nothing from %+v", cands)
		}
		if *field.Value != "USD" {
			t.Errorf("currency = %q, want %q -- the label must win even though the fallback's distance is 0 against the label's 0.12", *field.Value, "USD")
		}
		if field.Reason != extraction.ReasonNone || len(field.Alternatives) != 0 {
			t.Errorf("currency reason = %q alternatives = %q, want %q with none", field.Reason, valuesOf(field.Alternatives), extraction.ReasonNone)
		}

		// Control: the same two candidates re-tiered to one shared tier decide NGN, the closer
		// one -- proving the pass above is the tier split's own doing, not a fixture coincidence.
		retiered := make([]extraction.Candidate, len(cands))
		for i, c := range cands {
			c.Tier = extraction.TierGeneric
			retiered[i] = c
		}
		ctl, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: retiered}), "currency")
		if !ok || ctl.Value == nil || *ctl.Value != "NGN" {
			t.Fatalf("control: untiered currency = %+v, want NGN decided -- otherwise the USD pass above proves nothing", ctl)
		}
	})

	t.Run("fallback first in reading order", func(t *testing.T) {
		label := extraction.Tier1Rule{
			Key: "g.currency.same_token", Field: "currency",
			Rule: rvRule(t, `^USD$`, extraction.RelSameToken, 0, extraction.ShapeName),
		}
		fallback := extraction.Tier1Rule{
			Key: "g.currency.sweep2", Field: "currency",
			Rule:     rvRule(t, `^NGN$`, extraction.RelSameToken, 0, extraction.ShapeName),
			Fallback: true,
		}
		page := rvPage(
			rvTok("NGN", 0.10, 0.10, 0.20, 0.13), // reads first
			rvTok("USD", 0.10, 0.40, 0.20, 0.43),
		)
		rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{label, fallback}}

		got := extraction.Resolve(page, rules)
		cands := rvFor(got, "currency")
		rvFloor(t, cands, "USD and NGN as two whole same_token candidates")

		field, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: got}), "currency")
		if !ok || field.Value == nil {
			t.Fatalf("currency decided nothing from %+v", cands)
		}
		if *field.Value != "USD" {
			t.Errorf("currency = %q, want %q -- the label must win even though the fallback's token reads first", *field.Value, "USD")
		}
		if field.Reason != extraction.ReasonNone || len(field.Alternatives) != 0 {
			t.Errorf("currency reason = %q alternatives = %q, want %q with none", field.Reason, valuesOf(field.Alternatives), extraction.ReasonNone)
		}
	})
}

// AC-2.2. With no labelled candidate for the field, the fallback rule decides alone. The Tier
// assertion is the red: Resolve must tag its own candidate TierFallback, and does not yet.
func TestResolve_AFallbackDecidesWhenNoLabelResolved(t *testing.T) {
	fallback := extraction.Tier1Rule{
		Key: "g.currency.sweep", Field: "currency",
		Rule:     rvRule(t, `^NGN$`, extraction.RelSameToken, 0, extraction.ShapeName),
		Fallback: true,
	}
	page := rvPage(rvTok("NGN", 0.10, 0.10, 0.20, 0.13))
	rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{fallback}}

	got := extraction.Resolve(page, rules)
	cands := rvFor(got, "currency")
	rvFloor(t, cands, "the sole fallback rule over its own token")
	if len(cands) != 1 {
		t.Fatalf("currency candidates = %+v, want exactly one", cands)
	}
	if cands[0].Tier != extraction.TierFallback {
		t.Errorf("currency candidate Tier = %v, want TierFallback -- Resolve must tag a Fallback rule's own candidates TierFallback", cands[0].Tier)
	}

	field, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: got}), "currency")
	if !ok || field.Value == nil {
		t.Fatalf("currency decided nothing from %+v", cands)
	}
	if *field.Value != "NGN" {
		t.Errorf("currency = %q, want %q", *field.Value, "NGN")
	}
	if field.Reason != extraction.ReasonNone || len(field.Alternatives) != 0 {
		t.Errorf("currency reason = %q alternatives = %q, want %q with none", field.Reason, valuesOf(field.Alternatives), extraction.ReasonNone)
	}
}

// AC-2.7. TierFallback must sort below TierGeneric, itself below TierLearned -- asserted by
// name so a reordering of the iota block reds even though the ints still compare.
func TestResolve_TheTierOrderIsLearnedGenericFallback(t *testing.T) {
	if !(extraction.TierLearned < extraction.TierGeneric) {
		t.Errorf("TierLearned (%d) is not less than TierGeneric (%d)", extraction.TierLearned, extraction.TierGeneric)
	}
	if !(extraction.TierGeneric < extraction.TierFallback) {
		t.Errorf("TierGeneric (%d) is not less than TierFallback (%d)", extraction.TierGeneric, extraction.TierFallback)
	}
}
