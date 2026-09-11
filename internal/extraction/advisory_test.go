// Oracles over the advisory fixtures. The lexicon they pin shipped before them, so every test is
// green on arrival and reds only under a mutant of the code it names.
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

// advObserved reports whether page 1 carries an AnchorObservation for label; AnchorObservations
// reads no other page.
func advObserved(t *testing.T, fixture, label string) bool {
	t.Helper()
	for _, o := range extraction.AnchorObservations(rvCorpusPages(t, fixture)) {
		if o.Label == label {
			return true
		}
	}
	return false
}

// advDistance returns Resolve's Distance for field/value read from a neighbouring token, or fatals
// unless every such candidate carries the same one (the dense line amount is read twice).
func advDistance(t *testing.T, cands []extraction.Candidate, field, value string) float64 {
	t.Helper()
	var ds []float64
	for _, c := range cands {
		if c.Field == field && c.Value == value && c.Adjacent {
			ds = append(ds, c.Distance)
		}
	}
	slices.Sort(ds)
	ds = slices.Compact(ds)
	if len(ds) != 1 {
		t.Fatalf("adjacent %s candidates valued %q carry distances %v, want exactly one", field, value, ds)
	}
	return ds[0]
}

// advToken returns the one token, on any page, whose trimmed text equals want -- pdfium pads a
// split label with a trailing space.
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

// advRewriteToken copies pages with one token's text replaced and its box kept, so only the word
// differs -- a second PDF would move the label's box too.
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

// T-06.1: the faithful register resolves neither filing blocker, so both are pinned as known gaps
// by name and never counted as passes.
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

	// known gap: value beyond tier1MaxDistanceRight -- the label matches, only the value is too far.
	total, ok := rcFind(out, "total")
	if !ok || total.Reason != extraction.ReasonMissing {
		t.Errorf("R0 total = %+v (ok=%v), want Reason %q -- known gap: value beyond tier1MaxDistanceRight", total, ok, extraction.ReasonMissing)
	}
	if !advObserved(t, fxAdvisoryRegister, "total") {
		t.Errorf("R0 carries no total AnchorObservation; \"Amount payable\" should still match the label")
	}

	// R1 unspaces only the header labels, so issue_date resolves and the totals gap stays.
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

// T-06.2: Core AC-3 -- once "Taxable amount" decides subtotal, the referee picks the printed total
// over the nearer line amount.
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

// T-06.3: inject a competing subtotal rather than drop one -- a missing subtotal fails parseMoney
// with or without decidedMoney's ReasonNone guard, so only an ambiguous one exercises it.
func TestAdvisory_TheRefereeIsWhatMovedIt(t *testing.T) {
	cands := advResolve(t, fxAdvisoryDense)

	subs := rvFor(cands, "subtotal")
	if len(subs) != 1 {
		t.Fatalf("subtotal candidates = %+v, want exactly 1 to copy", subs)
	}
	// 9999999.99 + 239056.50 matches no total, so the injected value corroborates nothing itself.
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

// T-06.4: characterisation only. The register's withholding value sits outside the right dial, so
// vat never reaches it; TestResolve_AWithholdingLineIsNotTheVAT is the owning phrase's oracle.
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

// advArms retypes the seven label arms this story added or widened, so T-06.5 does not grade
// anchorLexicon with itself.
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

// advDeclared is field x fixture -> matched. An absent cell is declared false: that covers
// buyer_tin, which neither source prints, and the letter-spaced "I S S U E D" on R0.
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

// T-06.5: matched and resolved are graded separately, and matched is compared against advDeclared
// in both directions, so an unresolved label can never be silently absent.
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

	// buyer_name: known gap, ambiguous against the line under the name.
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

	// withholding_tax: n/a -- an owning phrase fills no field, so it is neither gap nor pass.
	if _, ok := rcFind(r0, "withholding_tax"); ok {
		t.Errorf("R0 produced a withholding_tax field result; the owning phrase fills no field")
	}
	if _, ok := rcFind(r1, "withholding_tax"); ok {
		t.Errorf("R1 produced a withholding_tax field result; the owning phrase fills no field")
	}
}

// --- T-06.6 -------------------------------------------------------------------

// T-06.6: each gap is measured on the built fixture, to 4dp, and asserted on its side of the dial.
// Row names carry the real-document gap (local docling) as provenance, never asserted.
func TestAdvisory_TheLabelValueGapsAreRecorded(t *testing.T) {
	const tol = 5e-5
	dense := advResolve(t, fxAdvisoryDense)

	inDial := func(t *testing.T, got, want, dial float64) {
		t.Helper()
		if math.Abs(got-want) > tol {
			t.Errorf("gap = %v, want %v", got, want)
		}
		if got > dial {
			t.Errorf("gap %v exceeds its dial %v", got, dial)
		}
	}
	// outOfDial reads a pair Resolve never relates, so it measures with RightGapForTest; the weld
	// subtest below holds that helper to Resolve's own formula.
	outOfDial := func(t *testing.T, labelText, valueText string, want float64) {
		t.Helper()
		for _, fixture := range []string{fxAdvisoryRegister, fxAdvisoryRegisterUnspaced} {
			pages := rvCorpusPages(t, fixture)
			got := extraction.RightGapForTest(advToken(t, pages, labelText).Region, advToken(t, pages, valueText).Region)
			if math.Abs(got-want) > tol {
				t.Errorf("%s: gap = %v, want %v", fixture, got, want)
			}
			if got <= extraction.Tier1MaxDistanceRightForTest {
				t.Errorf("%s: gap %v is inside tier1MaxDistanceRight %v; the out-of-dial record no longer holds", fixture, got, extraction.Tier1MaxDistanceRightForTest)
			}
		}
	}

	t.Run("RightGapForTest equals Resolve's Distance on D0 Taxable amount", func(t *testing.T) {
		pages := rvCorpusPages(t, fxAdvisoryDense)
		helper := extraction.RightGapForTest(advToken(t, pages, "Taxable amount").Region, advToken(t, pages, "3,187,420.00").Region)
		if resolved := advDistance(t, dense, "subtotal", "3187420.00"); helper != resolved {
			t.Errorf("RightGapForTest = %v, Resolve's Distance = %v; the helper no longer mirrors relatedTokens", helper, resolved)
		}
	})

	t.Run("supplier_name FROM to name, R0, below, real doc 0.0145", func(t *testing.T) {
		got := advDistance(t, advResolve(t, fxAdvisoryRegister), "supplier_name", "Okonkwo Advisory Partners")
		inDial(t, got, 0.0142, extraction.Tier1MaxDistanceBelowForTest)
	})

	t.Run("buyer_name BILLED TO to name, R0, below, real doc 0.0145", func(t *testing.T) {
		got := advDistance(t, advResolve(t, fxAdvisoryRegister), "buyer_name", "Honeywell Group Nigeria Plc")
		inDial(t, got, 0.0142, extraction.Tier1MaxDistanceBelowForTest)
	})

	t.Run("issue_date Issued to date, R1 (tamed label, R0 geometry), below, real doc 0.0145", func(t *testing.T) {
		got := advDistance(t, advResolve(t, fxAdvisoryRegisterUnspaced), "issue_date", "2026-09-01")
		inDial(t, got, 0.0146, extraction.Tier1MaxDistanceBelowForTest)
	})

	t.Run("subtotal Taxable amount to value, D0, right, real doc 0.1581", func(t *testing.T) {
		inDial(t, advDistance(t, dense, "subtotal", "3187420.00"), 0.1802, extraction.Tier1MaxDistanceRightForTest)
	})

	t.Run("vat VAT @ 7.5% to value, D0, right, real doc 0.1878", func(t *testing.T) {
		inDial(t, advDistance(t, dense, "vat", "239056.50"), 0.2053, extraction.Tier1MaxDistanceRightForTest)
	})

	t.Run("total TOTAL DUE (NGN) to value, D0, right, real doc 0.0940", func(t *testing.T) {
		inDial(t, advDistance(t, dense, "total", "3426476.50"), 0.1344, extraction.Tier1MaxDistanceRightForTest)
	})

	t.Run("total Total payable to line amount, D0, below, real doc 0.0537", func(t *testing.T) {
		inDial(t, advDistance(t, dense, "total", "1250000.00"), 0.0524, extraction.Tier1MaxDistanceBelowForTest)
	})

	t.Run("total Amount payable to value, R0+R1, right, KNOWN GAP, real doc 0.3932", func(t *testing.T) {
		outOfDial(t, "Amount payable", "₦14,430,000.00", 0.4604)
	})

	t.Run("withholding_tax Withholding tax 10% to value, R0+R1, right, outside the dial, real doc 0.4838", func(t *testing.T) {
		outOfDial(t, "Withholding tax 10%", "-₦1,480,000.00", 0.5388)
	})
}

// --- T-06.7 -------------------------------------------------------------------

// T-06.7: AC-7 -- "BILL TO" decides like "BILLED TO" across all ten header fields.
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

// T-06.9: the contest T-06.2 rests on exists -- the line amount is the nearer total, so ordering
// alone cannot pick the printed one. Value sets, not counts: the line amount is read twice.
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
		t.Errorf("nearest vat candidate Adjacent = true, want false (the SUMMARY box's same-token read)")
	}
}

// --- T-06.10 ------------------------------------------------------------------

// T-06.10: the summary prints "Total payable ₦3,426,476.50" as one token. The label matches, but
// sameTokenValue's residue is no amount, so no total may be read from that token's box.
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

	totals := rvFor(extraction.Resolve(pages, rvGeneric()), "total")
	rvFloor(t, totals, "total on the dense invoice")
	for _, c := range totals {
		if c.Region != nil && *c.Region == tok.Region {
			t.Errorf("a total candidate was read from the joined token: %+v", c)
		}
	}
}

// --- T-06.11 ------------------------------------------------------------------

// T-06.11: each newly matched label's declared tokenisation holds on the built PDF. A split row
// needs label and value as two exact tokens; a joined row needs one token carrying both.
func TestAdvisory_TheFixturesTokeniseTheWayTheyDeclare(t *testing.T) {
	register := []string{fxAdvisoryRegister, fxAdvisoryRegisterUnspaced}
	type decl struct {
		fixtures           []string
		what, label, value string // value == "" means label IS the whole (joined) token
	}
	rows := []decl{
		{register, "FROM + name, split, evidenced (local docling)", "FROM", "Okonkwo Advisory Partners"},
		{register, "BILLED TO + name, split, evidenced (local docling)", "BILLED TO", "Honeywell Group Nigeria Plc"},
		{register, "Withholding tax 10% + value, split, evidenced (local docling)", "Withholding tax 10%", "-₦1,480,000.00"},
		{register, "Amount payable + value, split, evidenced (local docling)", "Amount payable", "₦14,430,000.00"},
		{[]string{fxAdvisoryRegister}, "I S S U E D + date, split, evidenced (local docling)", "I S S U E D", "2026-09-01"},
		{[]string{fxAdvisoryRegisterUnspaced}, "Issued + date, split, tamed (R0 unspaced)", "Issued", "2026-09-01"},
		{[]string{fxAdvisoryDense}, "Taxable amount + value, split, evidenced (local docling)", "Taxable amount", "3,187,420.00"},
		{[]string{fxAdvisoryDense}, "VAT @ 7.5% + value, split, evidenced (local docling)", "VAT @ 7.5%", "239,056.50"},
		{[]string{fxAdvisoryDense}, "TOTAL DUE (NGN) + value, split, evidenced (local docling)", "TOTAL DUE (NGN)", "3,426,476.50"},
		{[]string{fxAdvisoryDense}, "Total payable + value, JOINED, evidenced (local docling)", "Total payable ₦3,426,476.50", ""},
	}
	for _, row := range rows {
		for _, fixture := range row.fixtures {
			t.Run(fixture+"/"+row.what, func(t *testing.T) {
				pages := rvCorpusPages(t, fixture)
				advToken(t, pages, row.label) // fatals unless exactly 1
				if row.value != "" {
					advToken(t, pages, row.value) // a separate token: two, not one
				}
			})
		}
	}
}
