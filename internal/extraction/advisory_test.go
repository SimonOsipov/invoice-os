// advisory_test.go: T-06.1..T-06.11 (T-06.8, the fixture-generator tests, already exist in
// fixtures_test.go). Pass-on-arrival oracles over the three faithful advisory fixtures
// (arch-26-06 Appendix C, amendments A1-A14) -- EXTR-26-01..05 already shipped the lexicon
// these pin against, so nothing here is red at HEAD without a named mutant.
package extraction_test

import (
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- shared reading helpers ---------------------------------------------------

func advResolve(t *testing.T, fixture string) []extraction.Candidate {
	t.Helper()
	got := extraction.Resolve(rvCorpusPages(t, fixture), rvGeneric())
	rvFloor(t, got, fixture)
	return got
}

func advReconcile(t *testing.T, fixture string) []extraction.FieldResult {
	t.Helper()
	return extraction.Reconcile(extraction.Input{Candidates: advResolve(t, fixture)})
}

// advObserved reports whether page 1 carries an AnchorObservation for label.
func advObserved(t *testing.T, fixture, label string) bool {
	t.Helper()
	for _, o := range extraction.AnchorObservations(rvCorpusPages(t, fixture)) {
		if o.Label == label {
			return true
		}
	}
	return false
}

// advDistance returns the Distance of the field/value candidate, or fatals -- every T-06.6 row
// reads Resolve's own computed gap rather than a hand-typed one.
func advDistance(t *testing.T, cands []extraction.Candidate, field, value string) float64 {
	t.Helper()
	for _, c := range cands {
		if c.Field == field && c.Value == value {
			return c.Distance
		}
	}
	t.Fatalf("no %s candidate valued %q among %d candidate(s)", field, value, len(cands))
	return 0
}

// advToken returns the one token, on any page, whose trimmed text equals want -- pdfium pads a
// split label with a trailing space (docs/extraction-corpus.md: "compare trimmed").
func advToken(t *testing.T, pages []extraction.TokenPage, want string) extraction.Token {
	t.Helper()
	var hits []extraction.Token
	for _, p := range pages {
		for _, tok := range p.Tokens {
			if strings.TrimSpace(tok.Text) == want {
				hits = append(hits, tok)
			}
		}
	}
	if len(hits) != 1 {
		t.Fatalf("%d token(s) trimmed-equal to %q, want exactly 1: %+v", len(hits), want, hits)
	}
	return hits[0]
}

// advRewriteToken deep-copies pages and replaces the text of the token trimmed-equal to from
// with to, box unchanged -- the token-rewrite mechanism (arch-26-06 S8), isolating the WORD from
// the label's own box the way a second PDF cannot.
func advRewriteToken(t *testing.T, pages []extraction.TokenPage, from, to string) []extraction.TokenPage {
	t.Helper()
	out := make([]extraction.TokenPage, len(pages))
	hits := 0
	for i, p := range pages {
		toks := make([]extraction.Token, len(p.Tokens))
		copy(toks, p.Tokens)
		for j, tok := range toks {
			if strings.TrimSpace(tok.Text) == from {
				toks[j].Text = to
				hits++
			}
		}
		out[i] = extraction.TokenPage{Number: p.Number, WidthPt: p.WidthPt, HeightPt: p.HeightPt, Tokens: toks}
	}
	if hits != 1 {
		t.Fatalf("rewrote %d token(s) trimmed-equal to %q, want exactly 1", hits, from)
	}
	return out
}

func advStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func advSameValue(a, b *string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

// --- T-06.1 -------------------------------------------------------------------

// T-06.1: Core AC-5 is NOT delivered on the register (D-26-06, arch-26-06 S11) -- both filing
// blockers are pinned as KNOWN GAPS BY NAME, never silently passed. Renamed from the story's
// "clears both filing blockers": the faithful register clears neither.
// Controls: M1 (no "issued") reds the R1 half; M2 (no "payable") reds the label-matches half.
func TestAdvisory_TheRegisterFilingBlockersAreKnownGapsByName(t *testing.T) {
	out := advReconcile(t, fxAdvisoryRegister)

	// known gap: letter-spaced label -- "I S S U E D" matches no anchor-lexicon entry.
	issueDate, ok := rcFind(out, "issue_date")
	if !ok || issueDate.Reason != extraction.ReasonMissing {
		t.Errorf("R0 issue_date = %+v (ok=%v), want Reason %q -- known gap: letter-spaced label", issueDate, ok, extraction.ReasonMissing)
	}
	if advObserved(t, fxAdvisoryRegister, "issue_date") {
		t.Errorf("R0 carries an issue_date AnchorObservation; the letter-spaced-label gap claims it has none")
	}

	// known gap: value beyond tier1MaxDistanceRight -- the label MATCHES ("Amount payable");
	// only the value sits past the dial. This label-matches half is what M2 reds.
	total, ok := rcFind(out, "total")
	if !ok || total.Reason != extraction.ReasonMissing {
		t.Errorf("R0 total = %+v (ok=%v), want Reason %q -- known gap: value beyond tier1MaxDistanceRight", total, ok, extraction.ReasonMissing)
	}
	if !advObserved(t, fxAdvisoryRegister, "total") {
		t.Errorf("R0 carries no total AnchorObservation; \"Amount payable\" should still match the label")
	}

	// R1 unspaces only the two header labels: issue_date resolves, total does not -- the
	// totals column gap is untouched by that one transformation.
	out1 := advReconcile(t, fxAdvisoryRegisterUnspaced)
	issueDate1, ok := rcFind(out1, "issue_date")
	if !ok || issueDate1.Reason != extraction.ReasonNone || !advSameValue(issueDate1.Value, rcStr("2026-09-01")) {
		t.Errorf("R1 issue_date = %+v (ok=%v), want ReasonNone / %q", issueDate1, ok, "2026-09-01")
	}
	total1, ok := rcFind(out1, "total")
	if !ok || total1.Reason != extraction.ReasonMissing {
		t.Errorf("R1 total = %+v (ok=%v), want Reason %q -- unspacing the header labels does not touch the totals column", total1, ok, extraction.ReasonMissing)
	}
}

// --- T-06.2 -------------------------------------------------------------------

// T-06.2: Core AC-3, the referee's first decision on a real document's vocabulary.
// Controls: M3 (no "taxable amount"); M0 (the whole pre-story lexicon, which reproduces the
// 2026-09-09 production observation on the real document: subtotal missing, total 1250000.00
// ambiguous alts=[3426476.50]).
func TestAdvisory_TheDenseInvoiceLetsTheRefereeDecide(t *testing.T) {
	out := advReconcile(t, fxAdvisoryDense)

	sub, ok := rcFind(out, "subtotal")
	if !ok || sub.Reason != extraction.ReasonNone || !advSameValue(sub.Value, rcStr("3187420.00")) {
		t.Errorf("subtotal = %+v (ok=%v), want ReasonNone / %q", sub, ok, "3187420.00")
	}

	total, ok := rcFind(out, "total")
	if !ok || total.Reason != extraction.ReasonNone || !advSameValue(total.Value, rcStr("3426476.50")) {
		t.Errorf("total = %+v (ok=%v), want ReasonNone / %q", total, ok, "3426476.50")
	}
	if len(total.Alternatives) != 0 {
		t.Errorf("total alternatives = %v, want none", total.Alternatives)
	}
}

// --- T-06.3 -------------------------------------------------------------------

// T-06.3: the referee's non-vacuity guard (A5) -- INJECT an equal-standing subtotal candidate,
// never DROP (dropping the subtotal candidates does not discriminate reconcile.go's
// decidedMoney guard at all: total stays ambiguous either way).
// Control: G1 removes the `if cell.Reason != ReasonNone { return ... false }` guard.
func TestAdvisory_TheRefereeIsWhatMovedIt(t *testing.T) {
	cands := advResolve(t, fxAdvisoryDense)

	subs := rvFor(cands, "subtotal")
	if len(subs) != 1 {
		t.Fatalf("subtotal candidates = %+v, want exactly 1 to copy", subs)
	}
	// 9999999.99 + 239056.50 = 10239056.49, not a candidate -- the injected value must not
	// accidentally corroborate anything of its own.
	injected := subs[0]
	injected.Value = "9999999.99"
	cands = append(cands, injected)

	out := extraction.Reconcile(extraction.Input{Candidates: cands})

	sub, ok := rcFind(out, "subtotal")
	if !ok || sub.Reason != extraction.ReasonAmbiguous || !advSameValue(sub.Value, rcStr("3187420.00")) {
		t.Errorf("subtotal = %+v (ok=%v), want ReasonAmbiguous / %q -- the real reading must still stand as head", sub, ok, "3187420.00")
	}

	total, ok := rcFind(out, "total")
	if !ok || total.Reason != extraction.ReasonAmbiguous || !advSameValue(total.Value, rcStr("1250000.00")) {
		t.Errorf("total = %+v (ok=%v), want ReasonAmbiguous / %q -- the referee must stand down once subtotal is no longer decided", total, ok, "1250000.00")
	}
	found := false
	for _, a := range total.Alternatives {
		if advSameValue(a.Value, rcStr("3426476.50")) {
			found = true
		}
	}
	if !found {
		t.Errorf("total alternatives = %v, want one at %q", total.Alternatives, "3426476.50")
	}
}

// --- T-06.4 -------------------------------------------------------------------

// T-06.4: characterisation only (A13), not a discriminating oracle -- on the real register the
// withholding value sits outside the right dial (0.4838), so the owning phrase is not
// load-bearing for vat on this document. Core AC-4's discriminating oracle is EXTR-26-04's
// T-04.1. M4 does NOT move this row: a DECLARED SURVIVOR, not a bug. Do not add a tamed layout
// to manufacture a discriminator here.
func TestAdvisory_TheWithholdingLineIsNotTheVAT(t *testing.T) {
	for _, fixture := range []string{fxAdvisoryRegister, fxAdvisoryRegisterUnspaced} {
		out := advReconcile(t, fixture)
		vat, ok := rcFind(out, "vat")
		if !ok || vat.Reason != extraction.ReasonMissing {
			t.Errorf("%s: vat = %+v (ok=%v), want Reason %q", fixture, vat, ok, extraction.ReasonMissing)
		}
		for _, c := range advResolve(t, fixture) {
			if c.Field == "vat" && strings.Contains(c.Value, "1480000") {
				t.Errorf("%s: a vat candidate carries the withholding amount: %+v", fixture, c)
			}
		}
	}
}

// --- T-06.5 -------------------------------------------------------------------

// advArms is the seven label-vocabulary entries EXTR-26-01..05 added or widened (arch-26-06
// S5a), independently retyped so this test does not read anchorLexicon to grade itself.
var advArms = map[string]*regexp.Regexp{
	"buyer_tin":       regexp.MustCompile(`(?i)\bbill(?:ed|ing|s|d)?\s*to\b.*\btin\b`),
	"buyer_name":      regexp.MustCompile(`(?i)\bbill(?:ed|ing|s|d)?\s*to\b`),
	"issue_date":      regexp.MustCompile(`(?i)\bissued\b`),
	"subtotal":        regexp.MustCompile(`(?i)\btaxable\s*amount\b`),
	"total":           regexp.MustCompile(`(?i)\bamount\s*payable\b`),
	"supplier_name":   regexp.MustCompile(`(?i)^\s*from\b`),
	"withholding_tax": regexp.MustCompile(`(?i)\bwith[\s-]*hold(?:ing)?\s*tax\b`),
}

var advNewFields = []string{"buyer_tin", "buyer_name", "issue_date", "subtotal", "total", "supplier_name", "withholding_tax"}

// advDeclared is field x fixture -> matched, arch-26-06 A6. issue_date/R0 and every
// buyer_tin cell are absent (declared false) on purpose.
var advDeclared = map[[2]string]bool{
	{"buyer_name", fxAdvisoryRegister}:              true,
	{"buyer_name", fxAdvisoryRegisterUnspaced}:      true,
	{"issue_date", fxAdvisoryRegisterUnspaced}:      true,
	{"subtotal", fxAdvisoryDense}:                   true,
	{"total", fxAdvisoryRegister}:                   true,
	{"total", fxAdvisoryRegisterUnspaced}:           true,
	{"supplier_name", fxAdvisoryRegister}:           true,
	{"supplier_name", fxAdvisoryRegisterUnspaced}:   true,
	{"withholding_tax", fxAdvisoryRegister}:         true,
	{"withholding_tax", fxAdvisoryRegisterUnspaced}: true,
}

// T-06.5: one row per newly matched label, matched and resolved graded separately, compared
// against the declared set in BOTH directions -- a declared row not measured reds, and a
// measured hit on any of the seven arms not declared also reds (A6).
// Controls: M1, M2, M3, M4, M6 each flip one row's matched column; M5 flips row 2 (buyer_name).
func TestAdvisory_EveryNewlyMatchedLabelReportsWhetherItResolved(t *testing.T) {
	for _, fixture := range []string{fxAdvisoryRegister, fxAdvisoryRegisterUnspaced, fxAdvisoryDense} {
		obs := extraction.AnchorObservations(rvCorpusPages(t, fixture))
		for _, field := range advNewFields {
			measured := false
			for _, o := range obs {
				if o.Label == field && advArms[field].MatchString(o.Text) {
					measured = true
				}
			}
			want := advDeclared[[2]string{field, fixture}]
			if measured != want {
				t.Errorf("%s / %s: matched = %v, want %v (both-directions compare against the declared table)", field, fixture, measured, want)
			}
		}
	}

	r0 := advReconcile(t, fxAdvisoryRegister)
	r1 := advReconcile(t, fxAdvisoryRegisterUnspaced)
	d0 := advReconcile(t, fxAdvisoryDense)
	named := []struct {
		name string
		out  []extraction.FieldResult
	}{{"R0", r0}, {"R1", r1}}

	// buyer_name: known gap -- ambiguous, alt is "Finance Department" (the line under the
	// name), not "the TIN line" (D-A8's description is wrong for this document).
	for _, o := range named {
		bn, ok := rcFind(o.out, "buyer_name")
		if !ok || bn.Reason != extraction.ReasonAmbiguous || !advSameValue(bn.Value, rcStr("Honeywell Group Nigeria Plc")) {
			t.Errorf("%s buyer_name = %+v (ok=%v), want ReasonAmbiguous / %q", o.name, bn, ok, "Honeywell Group Nigeria Plc")
		}
		if len(bn.Alternatives) != 1 || !advSameValue(bn.Alternatives[0].Value, rcStr("Finance Department")) {
			t.Errorf("%s buyer_name alternatives = %v, want one at %q", o.name, bn.Alternatives, "Finance Department")
		}
	}

	// issue_date: R0 known gap (missing), R1 resolved.
	if id, ok := rcFind(r0, "issue_date"); !ok || id.Reason != extraction.ReasonMissing {
		t.Errorf("R0 issue_date = %+v (ok=%v), want Reason %q", id, ok, extraction.ReasonMissing)
	}
	if id, ok := rcFind(r1, "issue_date"); !ok || id.Reason != extraction.ReasonNone || !advSameValue(id.Value, rcStr("2026-09-01")) {
		t.Errorf("R1 issue_date = %+v (ok=%v), want ReasonNone / %q", id, ok, "2026-09-01")
	}

	// subtotal: D0 resolved.
	if s, ok := rcFind(d0, "subtotal"); !ok || s.Reason != extraction.ReasonNone || !advSameValue(s.Value, rcStr("3187420.00")) {
		t.Errorf("D0 subtotal = %+v (ok=%v), want ReasonNone / %q", s, ok, "3187420.00")
	}

	// total: R0/R1 known gap -- value beyond tier1MaxDistanceRight.
	for _, o := range named {
		tot, ok := rcFind(o.out, "total")
		if !ok || tot.Reason != extraction.ReasonMissing {
			t.Errorf("%s total = %+v (ok=%v), want Reason %q", o.name, tot, ok, extraction.ReasonMissing)
		}
	}

	// supplier_name: R0/R1 resolved.
	for _, o := range named {
		sn, ok := rcFind(o.out, "supplier_name")
		if !ok || sn.Reason != extraction.ReasonNone || !advSameValue(sn.Value, rcStr("Okonkwo Advisory Partners")) {
			t.Errorf("%s supplier_name = %+v (ok=%v), want ReasonNone / %q", o.name, sn, ok, "Okonkwo Advisory Partners")
		}
	}

	// withholding_tax: n/a (owning phrase) -- it owns no HeaderFields member, so Reconcile
	// produces no cell for it at all. A third state, never scored gap or pass.
	if _, ok := rcFind(r0, "withholding_tax"); ok {
		t.Errorf("R0 produced a withholding_tax field result; the owning phrase fills no field")
	}
	if _, ok := rcFind(r1, "withholding_tax"); ok {
		t.Errorf("R1 produced a withholding_tax field result; the owning phrase fills no field")
	}

	// buyer_tin: not printed by either source -- the matched-grid loop above already confirmed
	// zero hits on all three fixtures; named here so the both-directions compare is explicit.
}

// --- T-06.6 -------------------------------------------------------------------

// T-06.6: one row per newly matched label, the fixture gap asserted to 4dp against Resolve's
// own Distance, or (for the one out-of-dial row) the SAME gap formula relatedTokens uses
// (RightGapForTest), never a hand-typed one. The real-document gap (arch-26-06 S6, local
// docling 1.10.0) is cited as provenance only, never asserted.
// Control: move one value fxLine >= 5pt in the builder, regenerate -- reds (run manually; the
// committed PDFs are byte-pinned, not reproduced in-process here).
func TestAdvisory_TheLabelValueGapsAreRecorded(t *testing.T) {
	const tol = 5e-5

	t.Run("supplier_name FROM to name, R0, below, real doc 0.0145", func(t *testing.T) {
		got := advDistance(t, advResolve(t, fxAdvisoryRegister), "supplier_name", "Okonkwo Advisory Partners")
		if math.Abs(got-0.0142) > tol {
			t.Errorf("gap = %v, want 0.0142", got)
		}
		if got > extraction.Tier1MaxDistanceBelowForTest {
			t.Errorf("gap %v exceeds tier1MaxDistanceBelow %v", got, extraction.Tier1MaxDistanceBelowForTest)
		}
	})

	t.Run("buyer_name BILLED TO to name, R0, below, real doc 0.0145", func(t *testing.T) {
		got := advDistance(t, advResolve(t, fxAdvisoryRegister), "buyer_name", "Honeywell Group Nigeria Plc")
		if math.Abs(got-0.0142) > tol {
			t.Errorf("gap = %v, want 0.0142", got)
		}
		if got > extraction.Tier1MaxDistanceBelowForTest {
			t.Errorf("gap %v exceeds tier1MaxDistanceBelow %v", got, extraction.Tier1MaxDistanceBelowForTest)
		}
	})

	t.Run("issue_date Issued to date, R1, below, real doc 0.0145", func(t *testing.T) {
		got := advDistance(t, advResolve(t, fxAdvisoryRegisterUnspaced), "issue_date", "2026-09-01")
		if math.Abs(got-0.0146) > tol {
			t.Errorf("gap = %v, want 0.0146", got)
		}
		if got > extraction.Tier1MaxDistanceBelowForTest {
			t.Errorf("gap %v exceeds tier1MaxDistanceBelow %v", got, extraction.Tier1MaxDistanceBelowForTest)
		}
	})

	t.Run("subtotal Taxable amount to value, D0, right, real doc 0.1581", func(t *testing.T) {
		got := advDistance(t, advResolve(t, fxAdvisoryDense), "subtotal", "3187420.00")
		if math.Abs(got-0.1802) > tol {
			t.Errorf("gap = %v, want 0.1802", got)
		}
		if got > extraction.Tier1MaxDistanceRightForTest {
			t.Errorf("gap %v exceeds tier1MaxDistanceRight %v", got, extraction.Tier1MaxDistanceRightForTest)
		}
	})

	t.Run("total TOTAL DUE (NGN) to value, D0, right, real doc 0.0940", func(t *testing.T) {
		got := advDistance(t, advResolve(t, fxAdvisoryDense), "total", "3426476.50")
		if math.Abs(got-0.1344) > tol {
			t.Errorf("gap = %v, want 0.1344", got)
		}
		if got > extraction.Tier1MaxDistanceRightForTest {
			t.Errorf("gap %v exceeds tier1MaxDistanceRight %v", got, extraction.Tier1MaxDistanceRightForTest)
		}
	})

	t.Run("total Amount payable to value, R0+R1, right, KNOWN GAP, real doc 0.3932", func(t *testing.T) {
		for _, fixture := range []string{fxAdvisoryRegister, fxAdvisoryRegisterUnspaced} {
			pages := rvCorpusPages(t, fixture)
			label := advToken(t, pages, "Amount payable")
			value := advToken(t, pages, "₦14,430,000.00")
			got := extraction.RightGapForTest(label.Region, value.Region)
			if math.Abs(got-0.4604) > tol {
				t.Errorf("%s: gap = %v, want 0.4604", fixture, got)
			}
			if got <= extraction.Tier1MaxDistanceRightForTest {
				t.Errorf("%s: gap %v is inside tier1MaxDistanceRight %v; the known gap no longer holds", fixture, got, extraction.Tier1MaxDistanceRightForTest)
			}
		}
	})
}

// --- T-06.7 -------------------------------------------------------------------

// T-06.7: AC-7, the fixture-level companion to T-01.4. Token-rewrite mechanism (arch-26-06 S8,
// preferred over a second PDF, which also moves the label's own box). Compares Value/Reason/
// Alternatives over all ten HeaderFields.
// Control: M5 (empty alSuffix) gives 7/10 -- three fields move, not one; the story's "fails
// today because buyer_name" is false at HEAD.
func TestAdvisory_TheLoosenedRegisterDecidesLikeTheExactOne(t *testing.T) {
	exactPages := rvCorpusPages(t, fxAdvisoryRegister)
	loosenedPages := advRewriteToken(t, rvCorpusPages(t, fxAdvisoryRegister), "BILLED TO", "BILL TO")

	exact := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(exactPages, rvGeneric())})
	loosened := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(loosenedPages, rvGeneric())})

	for _, field := range extraction.HeaderFields {
		want, wok := rcFind(exact, field)
		got, gok := rcFind(loosened, field)
		if wok != gok {
			t.Errorf("%s: present exact=%v loosened=%v", field, wok, gok)
			continue
		}
		if !wok {
			continue
		}
		if !advSameValue(want.Value, got.Value) {
			t.Errorf("%s: Value exact=%s loosened=%s", field, advStr(want.Value), advStr(got.Value))
		}
		if want.Reason != got.Reason {
			t.Errorf("%s: Reason exact=%q loosened=%q", field, want.Reason, got.Reason)
		}
		if !reflect.DeepEqual(want.Alternatives, got.Alternatives) {
			t.Errorf("%s: Alternatives exact=%v loosened=%v", field, want.Alternatives, got.Alternatives)
		}
	}
}

// --- T-06.9 -------------------------------------------------------------------

// T-06.9: Resolve only (arch-26-06 S2d) -- the non-vacuity guard for T-06.2 and the
// fixture-level twin of T-02.7. Asserts value sets and the nearest value, not candidate counts
// (A4: the faithful layout mints THREE total candidates, not two). Measured, not predicted: S2d
// guessed the nearest vat candidate would be Adjacent; the real reading is same_token.
// Control: M3 removes the only subtotal candidate.
func TestAdvisory_TheDenseInvoiceStagesTheContest(t *testing.T) {
	got := advResolve(t, fxAdvisoryDense)

	totals := rvFor(got, "total")
	vals := rvValues(totals)
	if !slices.Contains(vals, "1250000.00") || !slices.Contains(vals, "3426476.50") {
		t.Fatalf("total values = %v, want both 1250000.00 and 3426476.50", vals)
	}
	minLine, minPrinted := math.Inf(1), math.Inf(1)
	for _, c := range totals {
		switch c.Value {
		case "1250000.00":
			minLine = min(minLine, c.Distance)
		case "3426476.50":
			minPrinted = min(minPrinted, c.Distance)
		}
	}
	if !(minLine < minPrinted) {
		t.Errorf("min distance of 1250000.00 candidates = %v, want strictly less than 3426476.50's %v", minLine, minPrinted)
	}

	subs := rvFor(got, "subtotal")
	subVals := rvValues(subs)
	if len(subVals) != 1 || subVals[0] != "3187420.00" {
		t.Errorf("subtotal values = %v, want exactly {3187420.00}", subVals)
	}

	vats := rvFor(got, "vat")
	rvFloor(t, vats, "vat on the dense invoice")
	nearest := vats[0]
	for _, c := range vats[1:] {
		if c.Distance < nearest.Distance {
			nearest = c
		}
	}
	if nearest.Value != "239056.50" {
		t.Errorf("nearest vat candidate = %+v, want Value 239056.50", nearest)
	}
	if nearest.Adjacent {
		t.Errorf("nearest vat candidate Adjacent = true, want false (same_token) -- measured, not S2d's predicted true")
	}
}

// --- T-06.10 ------------------------------------------------------------------

// T-06.10: P-10 as an oracle, not prose in ## Decisions -- pins the tokenisation gap AC-5's
// second half declines to fix (sameTokenValue is not touched by this story). Asserting the
// label MATCHES and separately that the value does NOT resolve is what tells a tokenisation gap
// from a vocabulary miss; without the first half this would pass on a mislabelled token.
// Controls:
//   - matches half: the total arm that matches bare "Total" is removed with a unique needle.
//   - resolves-nothing half: sameTokenValue is mutated to also strip a leading WORD (not just
//     separators) before handing the remainder to ShapeAmount -- the minimal shape a real fix
//     to AC-5's second half would take.
func TestAdvisory_AJoinedLabelAndValueResolvesNothing(t *testing.T) {
	const joined = "Total payable ₦3,426,476.50"
	pages := rvCorpusPages(t, fxAdvisoryDense)
	tok := advToken(t, pages, joined) // fatals unless exactly one token carries both

	matched := false
	for _, o := range extraction.AnchorObservations(pages) {
		if o.Label == "total" && o.X0 == tok.Region.X0 && o.Y0 == tok.Region.Y0 && o.X1 == tok.Region.X1 && o.Y1 == tok.Region.Y1 {
			matched = true
		}
	}
	if !matched {
		t.Errorf("no total AnchorObservation on the joined token %q -- the label half must still match", joined)
	}

	for _, c := range rvFor(extraction.Resolve(pages, rvGeneric()), "total") {
		if c.Region != nil && *c.Region == tok.Region && c.Distance == 0 {
			t.Errorf("a same-token total candidate was minted from the joined token: %+v", c)
		}
	}
}

// --- T-06.11 ------------------------------------------------------------------

// T-06.11: the design section's tokenisation table (S7, A7) as a committed literal, compared
// against measured token counts in both directions -- a declared "two" that measures as one
// token (or vice versa) reds either way, since advToken fatals unless the count is exactly 1.
// Control: join a declared split pair onto one fxLine in the builder, regenerate -- reds.
func TestAdvisory_TheFixturesTokeniseTheWayTheyDeclare(t *testing.T) {
	type decl struct {
		fixture, what, label, value string // value == "" means label IS the whole (joined) token
	}
	rows := []decl{
		{fxAdvisoryRegister, "Amount payable + value, split, evidenced (local docling 1.10.0)", "Amount payable", "₦14,430,000.00"},
		{fxAdvisoryRegister, "I S S U E D + date, split", "I S S U E D", "2026-09-01"},
		{fxAdvisoryRegisterUnspaced, "Issued + date, split", "Issued", "2026-09-01"},
		{fxAdvisoryDense, "Taxable amount + value, split", "Taxable amount", "3,187,420.00"},
		{fxAdvisoryDense, "VAT @ 7.5% + value, split", "VAT @ 7.5%", "239,056.50"},
		{fxAdvisoryDense, "TOTAL DUE (NGN) + value, split", "TOTAL DUE (NGN)", "3,426,476.50"},
		{fxAdvisoryDense, "Total payable + value, JOINED", "Total payable ₦3,426,476.50", ""},
	}
	for _, row := range rows {
		t.Run(row.what, func(t *testing.T) {
			pages := rvCorpusPages(t, row.fixture)
			advToken(t, pages, row.label) // fatals unless exactly 1 -- present regardless of split/joined
			if row.value != "" {
				advToken(t, pages, row.value) // a SEPARATE token: proves "two", not "one"
			}
		})
	}
}
