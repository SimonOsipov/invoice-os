// vocabulary_scope_test.go: what the widened label vocabulary decides once Resolve and
// Reconcile have run over it. External package: Resolve, Reconcile, Tier1Rules and HeaderFields
// are all exported, so no internal hook is needed.
package extraction_test

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// vsAdvisoryPage is one synthetic header page carrying all ten HeaderFields, with buyerLabel as
// the only thing that ever varies between two calls. The buyer name sits BELOW its label, the
// arrangement T-01.4 exists to prove the loosened rule resolves exactly like the exact one.
func vsAdvisoryPage(buyerLabel string) []extraction.TokenPage {
	return rvPage(
		rvTok("Invoice No: INV-2601", 0.10, 0.04, 0.40, 0.06),
		rvTok("Issue Date: 2026-09-10", 0.10, 0.14, 0.40, 0.16),
		rvTok("Supplier: Lagos Advisory Partners", 0.10, 0.24, 0.40, 0.26),
		rvTok("Supplier TIN: 99999999-1300", 0.10, 0.34, 0.40, 0.36),
		rvTok(buyerLabel, 0.10, 0.44, 0.20, 0.46),
		rvTok("Enugu Ceramics Limited", 0.10, 0.47, 0.32, 0.49),
		rvTok("Buyer TIN: 99999999-1302", 0.10, 0.57, 0.40, 0.59),
		rvTok("Currency: NGN", 0.10, 0.67, 0.40, 0.69),
		rvTok("Subtotal: 3,187,420.00", 0.10, 0.77, 0.40, 0.79),
		rvTok("VAT: 239,056.50", 0.10, 0.87, 0.40, 0.89),
		rvTok("Total: 3,426,476.50", 0.10, 0.97, 0.40, 0.99),
	)
}

func vsDeref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// T-01.4: two pages identical except "Bill to" vs "Billed to" must decide every header field
// the same way. Without alSuffix the loosened page resolves buyer_name missing, so the two
// sides differ.
func TestReconcile_ALoosenedPartyLabelDecidesExactlyAsTheExactOneDoes(t *testing.T) {
	rules := rvGeneric()

	exact := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(vsAdvisoryPage("Bill to"), rules)})
	loosened := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(vsAdvisoryPage("Billed to"), rules)})

	// Non-vacuity: both sides must actually have produced the ten header fields, or the
	// per-field loop below would range over nothing and pass trivially.
	if len(exact) < len(extraction.HeaderFields) || len(loosened) < len(extraction.HeaderFields) {
		t.Fatalf("Reconcile returned %d (exact) and %d (loosened) result(s), want at least %d each", len(exact), len(loosened), len(extraction.HeaderFields))
	}

	// Positive control: the exact spelling must itself resolve buyer_name to the printed name,
	// or an accidental equality below (both sides missing) would prove nothing.
	var control *extraction.FieldResult
	for i := range exact[:len(extraction.HeaderFields)] {
		if exact[i].Name == "buyer_name" {
			control = &exact[i]
		}
	}
	const wantName = "Enugu Ceramics Limited"
	if control == nil || control.Reason != extraction.ReasonNone || control.Value == nil || *control.Value != wantName {
		t.Fatalf("the exact-spelling page's buyer_name = %+v, want ReasonNone / %q -- the fixture must resolve on its own before the loosened page can be compared against it", control, wantName)
	}

	for i, field := range extraction.HeaderFields {
		a, b := exact[i], loosened[i]
		if a.Name != field || b.Name != field {
			t.Fatalf("result[%d].Name = %q / %q, want %q in both -- Reconcile did not return HeaderFields order", i, a.Name, b.Name, field)
		}
		if a.Reason != b.Reason {
			t.Errorf("%s: Reason = %q (exact) vs %q (loosened), want equal", field, a.Reason, b.Reason)
		}
		if (a.Value == nil) != (b.Value == nil) || (a.Value != nil && *a.Value != *b.Value) {
			t.Errorf("%s: Value = %s (exact) vs %s (loosened), want equal", field, vsDeref(a.Value), vsDeref(b.Value))
		}
		if len(a.Alternatives) != len(b.Alternatives) {
			t.Errorf("%s: %d alternative(s) (exact) vs %d (loosened), want equal", field, len(a.Alternatives), len(b.Alternatives))
		}
	}
}

// vsRefereeCore is the arithmetic-referee page: a line-item "Total" column with its own
// below-reading, plus a footer carrying subtotalLabel, VAT and the printed grand total.
// taxableColumn adds a second line-item column headed "Taxable amount" -- the shape T-02.8
// characterises.
func vsRefereeCore(subtotalLabel string, taxableColumn bool) []extraction.TokenPage {
	toks := []extraction.Token{
		rvTok("Total", 0.62, 0.30, 0.70, 0.33),
		rvTok("1,250,000.00", 0.60, 0.37, 0.78, 0.40),
		rvTok(subtotalLabel, 0.10, 0.60, 0.24, 0.63),
		rvTok("3,187,420.00", 0.36, 0.60, 0.50, 0.63),
		rvTok("VAT @ 7.5%", 0.10, 0.70, 0.24, 0.73),
		rvTok("239,056.50", 0.39, 0.70, 0.53, 0.73),
		rvTok("TOTAL DUE (NGN)", 0.10, 0.80, 0.30, 0.83),
		rvTok("3,426,476.50", 0.39, 0.80, 0.53, 0.83),
	}
	if taxableColumn {
		toks = append(toks,
			rvTok("Taxable amount", 0.40, 0.30, 0.54, 0.33),
			rvTok("1,162,790.70", 0.38, 0.37, 0.56, 0.40),
		)
	}
	return rvPage(toks...)
}

// T-02.7: the non-vacuity guard for T-02.3, written first. If the printed grand total ever
// measured nearer its anchor than the line total, corroborateTotal would never run and T-02.3
// would pass proving nothing -- this pins the ranking directly.
func TestResolve_TheAdvisoryPageStagesTheContestTheRefereeMustSettle(t *testing.T) {
	got := extraction.Resolve(vsRefereeCore("Taxable amount", false), rvGeneric())
	rvFloor(t, got, "the referee page")

	totals := rvFor(got, "total")
	if len(totals) != 2 {
		t.Fatalf("total: %d candidate(s), want exactly 2: %+v", len(totals), totals)
	}
	vals := rvValues(totals)
	if !slices.Contains(vals, "1250000.00") || !slices.Contains(vals, "3426476.50") {
		t.Fatalf("total values = %v, want {1250000.00, 3426476.50}", vals)
	}
	// Resolve sorts a field's own candidates by distance ascending (compareCandidates); pin
	// that ordering here rather than trust it silently.
	if totals[0].Distance > totals[1].Distance {
		t.Fatalf("total candidates arrived out of distance order: %+v", totals)
	}
	if totals[0].Value != "1250000.00" {
		t.Errorf("nearer total = %q (dist %v), want %q -- the line reading must be the one the referee has to beat", totals[0].Value, totals[0].Distance, "1250000.00")
	}

	subs := rvFor(got, "subtotal")
	if len(subs) != 1 || subs[0].Value != "3187420.00" {
		t.Errorf("subtotal candidates = %+v, want exactly one at %q", subs, "3187420.00")
	}
	vats := rvFor(got, "vat")
	if len(vats) != 1 || vats[0].Value != "239056.50" {
		t.Errorf("vat candidates = %+v, want exactly one at %q", vats, "239056.50")
	}
}

// T-02.3: without the taxable-amount arm subtotal reads missing, corroborateTotal returns res
// untouched at its haveSub gate, and total keeps the wrong line amount. T-02.7 above is this
// row's non-vacuity guard: with the contest unstaged this test passes proving nothing.
func TestReconcile_TheRefereeDecidesTheAdvisoryTotal(t *testing.T) {
	out := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(vsRefereeCore("Taxable amount", false), rvGeneric())})

	total, ok := rcFind(out, "total")
	if !ok || total.Reason != extraction.ReasonNone || total.Value == nil || *total.Value != "3426476.50" {
		t.Errorf("total = %+v (ok=%v), want ReasonNone / %q", total, ok, "3426476.50")
	}
	if len(total.Alternatives) != 0 {
		t.Errorf("total alternatives = %v, want none", total.Alternatives)
	}

	sub, ok := rcFind(out, "subtotal")
	if !ok || sub.Reason != extraction.ReasonNone || sub.Value == nil || *sub.Value != "3187420.00" {
		t.Errorf("subtotal = %+v (ok=%v), want ReasonNone / %q", sub, ok, "3187420.00")
	}
}

// T-02.4: the control for T-02.3 -- proves the SUBTOTAL VOCABULARY moved total, not page order.
// It does not discriminate corroborateTotal's haveSub gate: strip that gate and this still
// passes, because a zero subtotal balances neither reading and the referee returns res anyway.
func TestReconcile_TheRefereeIsStillSilentWithoutASubtotal(t *testing.T) {
	out := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(vsRefereeCore("Assessable consideration", false), rvGeneric())})

	sub, ok := rcFind(out, "subtotal")
	if !ok || sub.Reason != extraction.ReasonMissing {
		t.Errorf("subtotal = %+v (ok=%v), want Reason %q", sub, ok, extraction.ReasonMissing)
	}

	total, ok := rcFind(out, "total")
	if !ok || total.Reason != extraction.ReasonAmbiguous || total.Value == nil || *total.Value != "1250000.00" {
		t.Errorf("total = %+v (ok=%v), want ReasonAmbiguous / %q", total, ok, "1250000.00")
	}
	if len(total.Alternatives) != 1 || total.Alternatives[0].Value == nil || *total.Alternatives[0].Value != "3426476.50" {
		t.Errorf("total alternatives = %v, want one at %q", total.Alternatives, "3426476.50")
	}
}

// firsFooterPage is the two-line footer dense_invoice.pdf prints: "ISSUED UNDER THE FIRS" over
// secondLine.
func firsFooterPage(secondLine string) []extraction.TokenPage {
	return rvPage(
		rvTok("ISSUED UNDER THE FIRS", 0.10, 0.90, 0.50, 0.93),
		rvTok(secondLine, 0.10, 0.95, 0.55, 0.98),
	)
}

// T-02.5: the negative half alone is vacuous -- it passes whether "issued" is absent from the
// lexicon or present and shape-refused, for two different reasons. The paired control proves
// the zero is ShapeDate refusing the neighbour, not the vocabulary missing the anchor. The
// control is what carries the meaning: strip the issued arm and the control reds while the
// negative half stays silent.
func TestResolve_TheFIRSFooterIsNotAnIssueDate(t *testing.T) {
	rules := rvGeneric()

	zero := extraction.Resolve(firsFooterPage("E-INVOICING REGULATIONS 2026"), rules)
	if len(zero) != 0 {
		t.Errorf("candidates = %+v, want none", zero)
	}

	control := extraction.Resolve(firsFooterPage("2026-09-10"), rules)
	rvControl(t, control, "the FIRS footer with a real date below it")

	dates := rvFor(control, "issue_date")
	if len(dates) != 1 || dates[0].Value != "2026-09-10" {
		t.Fatalf("issue_date candidates = %+v, want exactly one at %q -- otherwise the zero above cannot be told apart from a vocabulary miss", dates, "2026-09-10")
	}
	// The measured distance: the anchor is the footer's first line, one line above. A candidate
	// bound from anywhere else would carry a different one.
	if got := dates[0].Distance; got < 0.0199 || got > 0.0201 {
		t.Errorf("issue_date distance = %v, want 0.0200 -- the control must bind the token directly below ISSUED UNDER THE FIRS", got)
	}
}

// T-02.8: CHARACTERISATION, not a fix. Exposure: any advisory invoice printing a per-line
// "Taxable amount" column. subtotal is absent from doubtfulFields (reconcile.go:64), so the
// column reading is never weighed against the footer's -- being strictly nearer, it decides
// unflagged and the referee is then fed the wrong addend. Only the WIDENED peer group is lost:
// at equal standing the pair still flags, per
// TestReconcile_TwoSubtotalSpellingsAreDoubtfulOnlyAtEqualStanding. Repairing needs
// column-header awareness, which is [vocabulary-fence]: line-item column vocabulary is
// EXTR-24's, not this entry's.
func TestResolve_ATaxableAmountColumnHeaderMintsASecondSubtotal(t *testing.T) {
	cands := extraction.Resolve(vsRefereeCore("Taxable amount", true), rvGeneric())

	subs := rvFor(cands, "subtotal")
	if len(subs) != 2 {
		t.Fatalf("subtotal: %d candidate(s), want exactly 2 (the footer reading and the column header's): %+v", len(subs), subs)
	}
	vals := rvValues(subs)
	if !slices.Contains(vals, "3187420.00") || !slices.Contains(vals, "1162790.70") {
		t.Fatalf("subtotal values = %v, want {3187420.00, 1162790.70}", vals)
	}

	out := extraction.Reconcile(extraction.Input{Candidates: cands})

	sub, ok := rcFind(out, "subtotal")
	if !ok || sub.Reason != extraction.ReasonNone || sub.Value == nil || *sub.Value != "1162790.70" {
		t.Errorf("subtotal = %+v (ok=%v), want ReasonNone / %q -- the nearer column-header reading wins unflagged", sub, ok, "1162790.70")
	}
	if len(sub.Alternatives) != 0 {
		t.Errorf("subtotal alternatives = %v, want none", sub.Alternatives)
	}

	total, ok := rcFind(out, "total")
	if !ok || total.Reason != extraction.ReasonAmbiguous || total.Value == nil || *total.Value != "1250000.00" {
		t.Errorf("total = %+v (ok=%v), want ReasonAmbiguous / %q -- corroborateTotal's subtotal is now wrong (1162790.70 + 239056.50 balances neither total reading), so it defers", total, ok, "1250000.00")
	}
}

// T-03.3: a mid-sentence from never opens a supplier block. Measured to red under a bare
// (unterminated) `from`, which files the TIN as supplier_tin and fabricates
// supplier_name = "01 Aug to 31 Aug 2026" -- this row is the shipped guard's whole
// justification.
func TestParty_AMidSentenceFromDoesNotOpenASupplierBlock(t *testing.T) {
	page := rvPage(
		rvTok("Billed to", 0.10, 0.10, 0.20, 0.12),
		rvTok("Enugu Ceramics Limited", 0.10, 0.13, 0.32, 0.15),
		rvTok("Services rendered from 01 Aug to 31 Aug 2026", 0.10, 0.20, 0.55, 0.22),
		rvTok("TIN 99999999-1302", 0.10, 0.30, 0.30, 0.32),
	)
	cands := extraction.Resolve(page, rvGeneric())
	rvFloor(t, cands, "the mid-sentence-from page")

	if got := rvFor(cands, "supplier_name"); len(got) != 0 {
		t.Errorf("supplier_name candidates = %+v, want none", got)
	}

	out := extraction.Reconcile(extraction.Input{Candidates: cands})
	tin, ok := rcFind(out, "buyer_tin")
	if !ok || tin.Reason != extraction.ReasonNone || tin.Value == nil || *tin.Value != "99999999-1302" {
		t.Errorf("buyer_tin = %+v (ok=%v), want ReasonNone / %q", tin, ok, "99999999-1302")
	}
	name, ok := rcFind(out, "buyer_name")
	if !ok || name.Reason != extraction.ReasonNone || name.Value == nil || *name.Value != "Enugu Ceramics Limited" {
		t.Errorf("buyer_name = %+v (ok=%v), want ReasonNone / %q", name, ok, "Enugu Ceramics Limited")
	}
}
