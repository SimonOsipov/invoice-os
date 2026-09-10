// sweep_qa_test.go: what EXTR-25-03's own specs do not reach. AC-3.1..AC-3.6 all put the sweep
// beside money on a page that means naira. Here it meets page furniture, a token that is not
// money at all, a second currency's amount, a learned reading, a second page, and the anchor
// outranking gate -- the surfaces a shape-only rule with BandAnywhere actually spans.
//
// Several of these pin a reading that is WRONG on the page. That is deliberate: the cost of a
// label-free rule belongs in a test that names it, not in a defect found on a tenant's invoice.
package extraction_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// sqSweeps is every t1.currency.sweep candidate the SHIPPED set produces over pages.
func sqSweeps(cands []extraction.Candidate) []extraction.Candidate {
	var out []extraction.Candidate
	for _, c := range cands {
		if c.Field == "currency" && c.RuleID == "t1.currency.sweep" {
			out = append(out, c)
		}
	}
	return out
}

// sqDecide resolves pages against the shipped set and returns the currency decision plus every
// sweep candidate behind it. Fatal when the sweep produced nothing: every assertion below is
// about what the sweep did, so an empty candidate set must never read as agreement.
func sqDecide(t *testing.T, pages []extraction.TokenPage) (extraction.FieldResult, []extraction.Candidate) {
	t.Helper()

	cands := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	sweeps := sqSweeps(cands)
	if len(sweeps) == 0 {
		t.Fatalf("no t1.currency.sweep candidate over %d page(s); the decision below would say nothing about the sweep", len(pages))
	}
	got, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: cands}), "currency")
	if !ok {
		t.Fatalf("Reconcile returned no currency result at all")
	}
	return got, sweeps
}

// sqAssert pins a whole decision: value, reason and alternative count together. A value-only
// assertion is dead against the fallback tier -- dropping Fallback leaves the value alone and
// moves only Reason on half the arrangements (measured, EXTR-25-03 QA).
func sqAssert(t *testing.T, got extraction.FieldResult, want string) {
	t.Helper()

	if got.Value == nil {
		t.Fatalf("currency decided nothing, want %q", want)
	}
	if *got.Value != want {
		t.Errorf("currency = %q, want %q", *got.Value, want)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("currency reason = %q, want %q", got.Reason, extraction.ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("currency alternatives = %q, want none", valuesOf(got.Alternatives))
	}
}

// The sweep reads page furniture. BandAnywhere spans the whole page, and no relation ties the
// symbol to an amount, so a footer line naming the currency resolves the field on a page that
// prints no money at all. Correct here, and the same mechanism as "Amount ₦" on
// wild_ruled_lines_totals.pdf -- the two cases are indistinguishable to the rule.
func TestResolve_TheSweepReadsACurrencyOffPageFurniture(t *testing.T) {
	footer := rvTok("All prices in ₦", 0.10, 0.95, 0.35, 0.98)
	got, sweeps := sqDecide(t, rvPage(
		rvTok("ACME LTD", 0.10, 0.05, 0.30, 0.08),
		footer,
	))

	if len(sweeps) != 1 {
		t.Fatalf("got %d sweep candidate(s), want exactly 1 -- only the footer carries a ₦", len(sweeps))
	}
	if sweeps[0].Region == nil || *sweeps[0].Region != footer.Region {
		t.Errorf("the sweep candidate sits at %+v, want the footer token's %+v", sweeps[0].Region, footer.Region)
	}
	sqAssert(t, got, "NGN")
}

// The sweep reads a ₦ out of a token that is not money. The rule is shape-only by design, so a
// street name or a reference code that happens to carry the glyph resolves the field with no
// doubt attached. This is the cost EXTR-25 accepted; a narrowing that ties the symbol to an
// amount must red here on purpose.
func TestResolve_TheSweepReadsACurrencyOutOfANonMoneyToken(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"a street address", "12 ₦kpor Close, Lagos"},
		{"a reference code", "REF/₦/2026-0042"},
		{"a phone number", "+234 (0) 803 ₦ 555 0142"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, sweeps := sqDecide(t, rvPage(rvTok(tc.text, 0.10, 0.20, 0.50, 0.23)))
			if len(sweeps) != 1 {
				t.Fatalf("got %d sweep candidate(s) over one token, want exactly 1", len(sweeps))
			}
			sqAssert(t, got, "NGN")
		})
	}
}

// A second currency's amount does not contest the sweep, because nothing shipped reads "$" or a
// bare three-letter token without a label. An unlabelled dollar invoice carrying one ₦ therefore
// files as NGN with ReasonNone and ZERO alternatives -- confidently wrong, and currency-allowed
// admits it, since it compares for equality against NGN. Only a LABEL rescues the page, which is
// the control below.
func TestResolve_ASecondCurrencysAmountDoesNotContestTheNairaSweep(t *testing.T) {
	dollars := rvTok("Total: $3,000.00", 0.10, 0.30, 0.35, 0.33)
	naira := rvTok("₦2,500.00", 0.10, 0.40, 0.25, 0.43)

	t.Run("no currency label", func(t *testing.T) {
		got, sweeps := sqDecide(t, rvPage(dollars, naira))
		if len(sweeps) != 1 {
			t.Fatalf("got %d sweep candidate(s), want exactly 1 -- only one token carries a ₦", len(sweeps))
		}
		sqAssert(t, got, "NGN")
	})

	// Control: the same two amounts, one label added. The page is not simply unreadable -- what
	// the arm above measures is the absence of a competing CANDIDATE, not a dead fixture.
	t.Run("with a currency label the page reads USD", func(t *testing.T) {
		got, _ := sqDecide(t, rvPage(rvTok("Currency: USD", 0.10, 0.20, 0.35, 0.23), dollars, naira))
		sqAssert(t, got, "USD")
	})
}

// A learned reading is the tenant's own answer for this layout and must outrank the sweep, which
// has no label at all. TierLearned < TierFallback carries this, but only if Resolve really tags
// the sweep TierFallback -- so the sweep candidate is asserted present and NGN first.
func TestResolve_ALearnedCurrencyRuleStillBeatsTheNairaSweep(t *testing.T) {
	rules := extraction.RuleSet{
		Learned: []extraction.AnchorRule{
			rvLearned(t, "L-1", "currency", `(?i)\bcurrency\b`, extraction.RelRight, 0.21, extraction.ShapeCurrency),
		},
		Tier1: extraction.Tier1Rules,
	}
	pages := rvPage(
		rvTok("Currency", 0.10, 0.30, 0.20, 0.33),
		rvTok("USD", 0.22, 0.30, 0.30, 0.33),
		rvTok("₦2,500.00", 0.10, 0.50, 0.25, 0.53),
	)

	cands := extraction.Resolve(pages, rules)
	sweeps := sqSweeps(cands)
	if len(sweeps) != 1 {
		t.Fatalf("got %d sweep candidate(s), want exactly 1; the precedence below would hold over nothing", len(sweeps))
	}
	if sweeps[0].Value != "NGN" || sweeps[0].Tier != extraction.TierFallback {
		t.Fatalf("the sweep candidate reads %q at tier %d, want %q at TierFallback (%d)", sweeps[0].Value, sweeps[0].Tier, "NGN", extraction.TierFallback)
	}
	learned := 0
	for _, c := range rvFor(cands, "currency") {
		if c.Tier == extraction.TierLearned {
			learned++
		}
	}
	if learned == 0 {
		t.Fatalf("no learned currency candidate; the arrangement never exercised the tenant's own rule")
	}

	got, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: cands}), "currency")
	if !ok {
		t.Fatalf("Reconcile returned no currency result")
	}
	sqAssert(t, got, "USD")
}

// BandAnywhere is a whole-DOCUMENT reach, not a first-page one: a ₦ that appears only on page two
// still resolves the field. The control is the same two pages with the glyph removed, which must
// decide nothing at all -- otherwise the arm above would pass on a page-1 reading.
func TestResolve_TheSweepReachesAPageOtherThanTheFirst(t *testing.T) {
	twoPages := func(second string) []extraction.TokenPage {
		return []extraction.TokenPage{
			{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
				{Text: "ACME LTD", Region: extraction.Region{Page: 1, X0: 0.10, Y0: 0.05, X1: 0.30, Y1: 0.08}},
			}},
			{Number: 2, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
				{Text: second, Region: extraction.Region{Page: 2, X0: 0.10, Y0: 0.90, X1: 0.25, Y1: 0.93}},
			}},
		}
	}

	got, sweeps := sqDecide(t, twoPages("₦2,500.00"))
	if len(sweeps) != 1 {
		t.Fatalf("got %d sweep candidate(s), want exactly 1", len(sweeps))
	}
	if sweeps[0].Region == nil || sweeps[0].Region.Page != 2 {
		t.Errorf("the sweep candidate sits on page %+v, want page 2", sweeps[0].Region)
	}
	sqAssert(t, got, "NGN")

	control := extraction.Resolve(twoPages("2,500.00"), extraction.RuleSet{Tier1: extraction.Tier1Rules})
	if n := len(sqSweeps(control)); n != 0 {
		t.Errorf("the ₦-free control produced %d sweep candidate(s), want 0 -- the arm above would then be measuring the walk, not the glyph", n)
	}
}

// The outranking gate now applies to TierFallback too (resolve.go: tier != TierLearned), so it is
// worth pinning which way it can cut. It cannot cut either way here: the sweep's label is
// end-anchored, so its match spans the token and no lexicon hit is ever strictly wider; and the
// sweep is not an anchorLexicon entry (AC-3.8), so it never widens anyone else's span either.
func TestResolve_TheSweepIsNeitherOutrankedNorOutranking(t *testing.T) {
	// The token carries an anchor-lexicon label AND a ₦, so both directions are live.
	got, sweeps := sqDecide(t, rvPage(rvTok("Invoice No. ₦", 0.10, 0.20, 0.35, 0.23)))
	if len(sweeps) != 1 {
		t.Fatalf("got %d sweep candidate(s) on a token that carries an anchor label, want exactly 1 -- anchorOutranked must not suppress the sweep", len(sweeps))
	}
	sqAssert(t, got, "NGN")

	// The other direction: a clean invoice-number token beside a ₦ token still reads its number.
	// Without this, "the sweep outranks nobody" would rest on the arm above alone, where the
	// shape rejects the remainder for its own reasons.
	cands := extraction.Resolve(rvPage(
		rvTok("Invoice No: INV-9", 0.10, 0.20, 0.40, 0.23),
		rvTok("₦2,500.00", 0.10, 0.40, 0.25, 0.43),
	), extraction.RuleSet{Tier1: extraction.Tier1Rules})
	if n := len(sqSweeps(cands)); n != 1 {
		t.Fatalf("got %d sweep candidate(s), want exactly 1; the invoice-number assertion below would not be about a page the sweep touched", n)
	}
	num, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: cands}), "invoice_number")
	if !ok || num.Value == nil {
		t.Fatalf("invoice_number decided nothing beside a ₦ token; the sweep must not suppress another field's anchor")
	}
	if *num.Value != "INV-9" {
		t.Errorf("invoice_number = %q, want %q", *num.Value, "INV-9")
	}
}
