// vocabulary_scope_reconcile_adversarial_test.go: what the three advisory arms do once two of
// them meet on one page, and where the arithmetic referee's tolerance boundary actually sits.
// External package: Resolve, Reconcile and Input are all exported.
package extraction_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// vsrRefereePage is the T-02.3 arrangement with the printed grand total under the caller's
// control, so the referee's tolerance can be walked from both sides of the boundary.
func vsrRefereePage(printedTotal string) []extraction.TokenPage {
	return rvPage(
		rvTok("Total", 0.62, 0.30, 0.70, 0.33),
		rvTok("1,250,000.00", 0.60, 0.37, 0.78, 0.40),
		rvTok("Taxable amount", 0.10, 0.60, 0.24, 0.63),
		rvTok("3,187,420.00", 0.36, 0.60, 0.50, 0.63),
		rvTok("VAT @ 7.5%", 0.10, 0.70, 0.24, 0.73),
		rvTok("239,056.50", 0.39, 0.70, 0.53, 0.73),
		rvTok("TOTAL DUE (NGN)", 0.10, 0.80, 0.30, 0.83),
		rvTok(printedTotal, 0.39, 0.80, 0.53, 0.83),
	)
}

func vsrReconcile(pages []extraction.TokenPage) []extraction.FieldResult {
	return extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(pages, rvGeneric())})
}

// The referee corroborates on positive evidence only, and "balances" means within
// reconcileTolerance -- one kobo, compared with GreaterThan, so a gap of exactly one kobo is
// still a balance. T-02.3 pins the exact-balance case; this walks both sides of the boundary,
// which is where a tolerance regression would hide.
func TestReconcile_TheRefereeCorroboratesToOneKoboAndRefusesBeyondIt(t *testing.T) {
	// 3,187,420.00 + 239,056.50 = 3,426,476.50 exactly.
	for _, c := range []struct {
		printed    string
		wantValue  string
		wantReason extraction.Reason
		wantAlts   int
	}{
		{"3,426,476.50", "3426476.50", extraction.ReasonNone, 0},
		{"3,426,476.51", "3426476.51", extraction.ReasonNone, 0},
		{"3,426,476.49", "3426476.49", extraction.ReasonNone, 0},
		{"3,426,476.52", "1250000.00", extraction.ReasonAmbiguous, 1},
		{"3,426,476.48", "1250000.00", extraction.ReasonAmbiguous, 1},
	} {
		out := vsrReconcile(vsrRefereePage(c.printed))

		// The referee cannot run at all without a decided subtotal and vat; assert both before
		// reading total, or a missing addend would look like a tolerance verdict.
		sub, ok := rcFind(out, "subtotal")
		if !ok || sub.Reason != extraction.ReasonNone || sub.Value == nil || *sub.Value != "3187420.00" {
			t.Fatalf("%s: subtotal = %+v (ok=%v), want ReasonNone / %q", c.printed, sub, ok, "3187420.00")
		}
		vat, ok := rcFind(out, "vat")
		if !ok || vat.Reason != extraction.ReasonNone || vat.Value == nil || *vat.Value != "239056.50" {
			t.Fatalf("%s: vat = %+v (ok=%v), want ReasonNone / %q", c.printed, vat, ok, "239056.50")
		}

		total, ok := rcFind(out, "total")
		if !ok || total.Reason != c.wantReason || total.Value == nil || *total.Value != c.wantValue {
			t.Errorf("%s: total = %+v (ok=%v), want %q / %q", c.printed, total, ok, c.wantReason, c.wantValue)
		}
		if len(total.Alternatives) != c.wantAlts {
			t.Errorf("%s: total alternatives = %v, want %d", c.printed, total.Alternatives, c.wantAlts)
		}
	}
}

// Amount payable and Total on one page are two readings of the SAME field. total IS in
// doubtfulFields, so the pair presents as ambiguous rather than deciding silently -- the
// contrast that makes the subtotal case below a defect and not a design.
func TestReconcile_AnAmountPayableCompetingWithATotalIsAmbiguous(t *testing.T) {
	out := vsrReconcile(rvPage(
		rvTok("Total", 0.10, 0.40, 0.30, 0.43),
		rvTok("980,000.00", 0.35, 0.40, 0.50, 0.43),
		rvTok("Amount payable", 0.10, 0.50, 0.30, 0.53),
		rvTok("1,053,500.00", 0.35, 0.50, 0.50, 0.53),
	))

	total, ok := rcFind(out, "total")
	if !ok || total.Reason != extraction.ReasonAmbiguous || total.Value == nil {
		t.Fatalf("total = %+v (ok=%v), want ReasonAmbiguous", total, ok)
	}
	if len(total.Alternatives) != 1 || total.Alternatives[0].Value == nil {
		t.Fatalf("total alternatives = %v, want exactly one", total.Alternatives)
	}
	got := []string{*total.Value, *total.Alternatives[0].Value}
	if got[0] != "980000.00" || got[1] != "1053500.00" {
		t.Errorf("total readings = %v, want [980000.00 1053500.00]", got)
	}
}

// Two subtotal spellings on one page are settled by distance alone: at equal standing the pair
// presents as ambiguous, and one pixel of difference decides it silently. This bounds the
// characterisation in TestResolve_ATaxableAmountColumnHeaderMintsASecondSubtotal -- what
// subtotal's absence from doubtfulFields costs is the WIDENED peer group, not the equal-standing
// one, so "a second subtotal never presents as doubtful" would be too strong a claim.
func TestReconcile_TwoSubtotalSpellingsAreDoubtfulOnlyAtEqualStanding(t *testing.T) {
	page := func(taxableValueX0, taxableValueX1 float64) []extraction.TokenPage {
		return rvPage(
			rvTok("Taxable amount", 0.10, 0.50, 0.30, 0.53),
			rvTok("900,000.00", taxableValueX0, 0.50, taxableValueX1, 0.53),
			rvTok("Sub-total", 0.10, 0.60, 0.30, 0.63),
			rvTok("1,000,000.00", 0.35, 0.60, 0.50, 0.63),
		)
	}

	// Equal standing: identical offsets from identical label boxes.
	equal := vsrReconcile(page(0.35, 0.50))
	sub, ok := rcFind(equal, "subtotal")
	if !ok || sub.Reason != extraction.ReasonAmbiguous || sub.Value == nil || *sub.Value != "900000.00" {
		t.Errorf("equal standing: subtotal = %+v (ok=%v), want ReasonAmbiguous / %q", sub, ok, "900000.00")
	}
	if len(sub.Alternatives) != 1 || sub.Alternatives[0].Value == nil || *sub.Alternatives[0].Value != "1000000.00" {
		t.Errorf("equal standing: subtotal alternatives = %v, want one at %q", sub.Alternatives, "1000000.00")
	}

	// The taxable reading moved strictly nearer: it now decides unflagged, and the other
	// reading is not even kept as an alternative.
	nearer := vsrReconcile(page(0.32, 0.47))
	sub, ok = rcFind(nearer, "subtotal")
	if !ok || sub.Reason != extraction.ReasonNone || sub.Value == nil || *sub.Value != "900000.00" {
		t.Errorf("nearer: subtotal = %+v (ok=%v), want ReasonNone / %q", sub, ok, "900000.00")
	}
	if len(sub.Alternatives) != 0 {
		t.Errorf("nearer: subtotal alternatives = %v, want none -- the competing reading is dropped, not flagged", sub.Alternatives)
	}
}

// Issued and Invoice date on one page are settled by distance alone -- there is no vocabulary
// preference between them, and issue_date is not in doubtfulFields, so whichever label sits
// nearer its date decides unflagged. Both orderings are asserted: one alone would pass against
// a lexicon that always preferred one arm.
func TestReconcile_IssuedAndInvoiceDateAreSettledByDistanceAlone(t *testing.T) {
	page := func(invoiceDateX0, invoiceDateX1, issuedX0, issuedX1 float64) []extraction.TokenPage {
		return rvPage(
			rvTok("Invoice date", 0.10, 0.20, 0.30, 0.23),
			rvTok("2026-01-02", invoiceDateX0, 0.20, invoiceDateX1, 0.23),
			rvTok("Issued", 0.10, 0.30, 0.30, 0.33),
			rvTok("2026-03-04", issuedX0, 0.30, issuedX1, 0.33),
		)
	}

	for _, c := range []struct {
		what      string
		pages     []extraction.TokenPage
		wantValue string
	}{
		{"issued nearer", page(0.36, 0.50, 0.35, 0.49), "2026-03-04"},
		{"invoice date nearer", page(0.35, 0.49, 0.36, 0.50), "2026-01-02"},
	} {
		cands := extraction.Resolve(c.pages, rvGeneric())
		dates := rvFor(cands, "issue_date")
		if len(dates) != 2 {
			t.Fatalf("%s: issue_date candidates = %+v, want exactly 2 -- both labels must anchor before the tie is read", c.what, dates)
		}

		out := extraction.Reconcile(extraction.Input{Candidates: cands})
		date, ok := rcFind(out, "issue_date")
		if !ok || date.Reason != extraction.ReasonNone || date.Value == nil || *date.Value != c.wantValue {
			t.Errorf("%s: issue_date = %+v (ok=%v), want ReasonNone / %q", c.what, date, ok, c.wantValue)
		}
		if len(date.Alternatives) != 0 {
			t.Errorf("%s: issue_date alternatives = %v, want none -- the further label's date is dropped unflagged", c.what, date.Alternatives)
		}
	}
}
