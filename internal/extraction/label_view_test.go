// label_view_test.go: 02-T2..02-T9, 02-T13. External package: every spec reaches only exported
// symbols, plus LabelViewForTest and AnchorLabelIDsForTest.
package extraction_test

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// lsLabelPage is a one-token label above a one-token value, the geometry 02-T2..02-T9 share.
func lsLabelPage(label, value string) []extraction.TokenPage {
	return rvPage(rvTok(label, 0.10, 0.200, 0.25, 0.210), rvTok(value, 0.10, 0.222, 0.22, 0.232))
}

// lsRows renders one comparable row per candidate. slices.Equal on []Candidate itself compares
// Region by pointer (resolve.go:29) and fails even on equal values; this compares by content.
func lsRows(cs []extraction.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = fmt.Sprintf("field=%q value=%q rule=%q tier=%d d=%.6f adj=%v box=%v",
			c.Field, c.Value, c.RuleID, c.Tier, c.Distance, c.Adjacent, c.Region)
	}
	return out
}

// --- 02-T2 ---------------------------------------------------------------------------------

// A spaced label reads and resolves exactly as its unspaced word: same candidates, same
// observation label/band (with Text joined), same Fingerprint.
func TestResolve_ASpacedLabelReadsAsItsWord(t *testing.T) {
	check := func(t *testing.T, field, labelID, value, unspacedLabel string, spacedForms ...string) {
		t.Helper()

		unspacedPages := lsLabelPage(unspacedLabel, value)
		unspacedCands := rvFor(extraction.Resolve(unspacedPages, extraction.RuleSet{Tier1: extraction.Tier1Rules}), field)
		rvFloor(t, unspacedCands, field+" on the unspaced page")
		wantRows := lsRows(unspacedCands)
		wantFP := extraction.Fingerprint(unspacedPages)

		var wantBand int
		var wantFound bool
		for _, o := range extraction.AnchorObservations(unspacedPages) {
			if o.Label == labelID {
				wantBand, wantFound = o.Band, true
			}
		}
		if !wantFound {
			t.Fatalf("the unspaced page carries no %s observation; nothing below can be compared against it", labelID)
		}

		for _, label := range spacedForms {
			pages := lsLabelPage(label, value)
			got := rvFor(extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules}), field)
			if rows := lsRows(got); !slices.Equal(rows, wantRows) {
				t.Errorf("%q resolves %d %s candidate(s), the unspaced page %d: %v vs %v", label, len(got), field, len(unspacedCands), rows, wantRows)
			}

			var band int
			var obsText string
			var found bool
			for _, o := range extraction.AnchorObservations(pages) {
				if o.Label == labelID {
					band, obsText, found = o.Band, o.Text, true
				}
			}
			if !found || band != wantBand {
				t.Errorf("%q AnchorObservations %s: found=%v band=%d, want found band %d", label, labelID, found, band, wantBand)
			}
			if want := strings.Join(strings.Fields(label), ""); found && obsText != want {
				t.Errorf("%q observation Text = %q, want %q", label, obsText, want)
			}

			if fp := extraction.Fingerprint(pages); fp != wantFP {
				t.Errorf("%q Fingerprint = %q, want %q (the unspaced page's)", label, fp, wantFP)
			}
		}
	}

	check(t, "invoice_number", "invoice_no", "OAP/2026/0088", "INVOICE NUMBER",
		"I N V O I C E N U M B E R", "I N V O I C E   N U M B E R")
	check(t, "issue_date", "issue_date", "2026-09-01", "ISSUED",
		"I S S U E D")
}

// --- 02-T3 ---------------------------------------------------------------------------------

// Letter-spaced text that is no label anchors nothing: neither a candidate nor an observation.
func TestResolve_SpacedTextThatIsNoLabelAnchorsNothing(t *testing.T) {
	const value = "OAP/2026/0088"
	const control = "I N V O I C E N O"

	// control, fatal: the positive control must anchor before the negatives below mean anything.
	ctl := rvFor(extraction.Resolve(lsLabelPage(control, value), extraction.RuleSet{Tier1: extraction.Tier1Rules}), "invoice_number")
	if len(ctl) == 0 || ctl[0].Value != value {
		t.Fatalf("%q anchored nothing (got %v); the negative rows below would hold against a Resolve that never returns anything", control, ctl)
	}

	for _, label := range []string{
		"C O M M E R C I A L   I N V O I C E",
		"Q X Z W",
		"I N V O I C E N O T E S",
	} {
		pages := lsLabelPage(label, value)
		got := rvFor(extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules}), "invoice_number")
		if len(got) != 0 {
			t.Errorf("%q resolves invoice_number %v, want none", label, rvValues(got))
		}
		if obs := extraction.AnchorObservations(pages); len(obs) != 0 {
			t.Errorf("%q carries AnchorObservation(s) %+v, want none", label, obs)
		}
	}
}

// --- 02-T4 ---------------------------------------------------------------------------------

// A spaced label on the value's own band blocks a dropped read exactly as its unspaced word
// does. Copies TestResolve_ALabelOnTheValuesBandBlocksADroppedRead (resolve_test.go:1673).
func TestResolve_ASpacedLabelBlocksLikeItsWord(t *testing.T) {
	label := rvBox(0.10, 0.500, 0.18, 0.511)
	labelTok := extraction.Token{Text: "Sub total", Region: label}
	valueTok := extraction.Token{Text: "1,500.00", Region: rvBox(0.40, 0.510, 0.47, 0.523)}
	competitor := extraction.Token{Text: "112.50", Region: rvBox(0.36, 0.510, 0.42, 0.521)}

	rule := rvTier1(t, "t1.subtotal.right", "subtotal", rvaLabelSubtotal, extraction.RelRight, 0.35, extraction.ShapeAmount)
	rule.Drop = 0.97
	rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{rule}}

	// control, fatal: a non-label word in the between slot must reach the competitor first.
	memo := extraction.Token{Text: "Memo", Region: rvBox(0.30, 0.510, 0.34, 0.521)}
	ctl := rvFor(extraction.Resolve(rvPage(labelTok, valueTok, memo, competitor), rules), "subtotal")
	rvControl(t, ctl, "the control page with Memo in the between slot")
	var reached bool
	for _, c := range ctl {
		if c.Value == "112.50" && math.Abs(c.Distance-0.18) <= 1e-9 {
			reached = true
		}
	}
	if !reached {
		t.Fatalf("the control produced %+v, want a subtotal candidate 112.50 at distance 0.18; the rows below would prove nothing", ctl)
	}

	// parity: the unspaced VAT label blocks exactly as the original spec pins.
	unspaced := extraction.Token{Text: "VAT", Region: rvBox(0.30, 0.510, 0.34, 0.521)}
	gotUnspaced := rvFor(extraction.Resolve(rvPage(labelTok, valueTok, unspaced, competitor), rules), "subtotal")
	for _, c := range gotUnspaced {
		if c.Value == "112.50" {
			t.Errorf("unspaced VAT: subtotal candidates = %v, want none naming 112.50", rvValues(gotUnspaced))
		}
	}

	// the spaced form must block the same way.
	spaced := extraction.Token{Text: "V A T", Region: rvBox(0.30, 0.510, 0.34, 0.521)}
	gotSpaced := rvFor(extraction.Resolve(rvPage(labelTok, valueTok, spaced, competitor), rules), "subtotal")
	for _, c := range gotSpaced {
		if c.Value == "112.50" {
			t.Errorf("subtotal candidates name 112.50 past a spaced VAT: %v", rvValues(gotSpaced))
		}
	}
}

// --- 02-T5 ---------------------------------------------------------------------------------

// A spaced party heading owns the bare TIN label after it, exactly as its unspaced word does.
// Copies TestTier1_ABareTINLabelBindsToTheHeadingBeforeIt (tier1_adversarial_test.go:317).
func TestTier1_ASpacedPartyHeadingOwnsTheTINAfterIt(t *testing.T) {
	t1Floor(t)

	const supplierTIN, buyerTIN = "99999999-0801", "99999999-0802"
	build := func(heading string) []extraction.TokenPage {
		return []extraction.TokenPage{t1aPage(1,
			rvTok("TIN:", 0.10, 0.10, 0.20, 0.13),
			rvTok(supplierTIN, 0.25, 0.10, 0.45, 0.13),
			t1aHeading(1, heading, 0.20, 0.23),
			rvTok("TIN:", 0.10, 0.25, 0.20, 0.28),
			rvTok(buyerTIN, 0.25, 0.25, 0.45, 0.28),
		)}
	}

	// premise, fatal: the unspaced heading must split the two TINs before the spaced form is
	// judged. Contains, not Equal: t1.tin.sweep (shape-only) and t1.tin.right (label-based) both
	// fire on the same bare "TIN:" label, so each party legitimately carries the value twice.
	unspaced := extraction.Resolve(build("BILLED TO"), extraction.RuleSet{Tier1: extraction.Tier1Rules})
	unspacedBuyer := rvValues(rvFor(unspaced, "buyer_tin"))
	if !slices.Contains(unspacedBuyer, buyerTIN) || slices.Contains(unspacedBuyer, supplierTIN) {
		t.Fatalf("BILLED TO: buyer_tin = %v, want it to hold %s and not %s; the premise must hold before the spaced heading is judged", unspacedBuyer, buyerTIN, supplierTIN)
	}

	got := extraction.Resolve(build("B I L L E D   T O"), extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, got, "two bare TIN labels either side of a spaced buyer heading")

	supplier := rvValues(rvFor(got, "supplier_tin"))
	buyer := rvValues(rvFor(got, "buyer_tin"))
	if !slices.Contains(buyer, buyerTIN) || slices.Contains(buyer, supplierTIN) {
		t.Errorf("buyer_tin = %v, want it to hold %s and not %s", buyer, buyerTIN, supplierTIN)
	}
	if !slices.Contains(supplier, supplierTIN) || slices.Contains(supplier, buyerTIN) {
		t.Errorf("supplier_tin = %v, want it to hold %s and not %s", supplier, supplierTIN, buyerTIN)
	}

	// parity: the spaced heading's buyer_tin count matches BILLED TO's exactly (same two rules,
	// same party, once the heading is recognised).
	if len(buyer) != len(unspacedBuyer) {
		t.Errorf("B I L L E D   T O buyer_tin = %v (%d row(s)), want the same count as BILLED TO's %v (%d row(s))", buyer, len(buyer), unspacedBuyer, len(unspacedBuyer))
	}
}

// --- 02-T6 ---------------------------------------------------------------------------------

// ShapeName refuses a spaced label exactly as it refuses the unspaced word, and keeps a spaced
// personal name unjoined: the view decides labels, never values.
func TestShapeName_RefusesASpacedLabelAndKeepsASpacedName(t *testing.T) {
	if got := extraction.ShapeName.Normalize("INVOICE NUMBER"); got != nil {
		t.Fatalf("ShapeName.Normalize(%q) = %v, want nil; the premise must hold before the spaced form is judged", "INVOICE NUMBER", got)
	}
	if got := extraction.ShapeName.Normalize("BILLED TO"); got != nil {
		t.Fatalf("ShapeName.Normalize(%q) = %v, want nil; the premise must hold before the spaced form is judged", "BILLED TO", got)
	}

	if got := extraction.ShapeName.Normalize("I N V O I C E   N U M B E R"); got != nil {
		t.Errorf("ShapeName.Normalize(%q) = %v, want nil", "I N V O I C E   N U M B E R", got)
	}
	if got := extraction.ShapeName.Normalize("B I L L E D   T O"); got != nil {
		t.Errorf("ShapeName.Normalize(%q) = %v, want nil", "B I L L E D   T O", got)
	}

	// control: a spaced personal name is never joined -- the view only decides labels.
	if got, want := extraction.ShapeName.Normalize("A D E Y E M I"), []string{"A D E Y E M I"}; !slices.Equal(got, want) {
		t.Errorf("ShapeName.Normalize(%q) = %v, want %v -- a spaced value reads as printed, never joined", "A D E Y E M I", got, want)
	}
}

// --- 02-T7 ---------------------------------------------------------------------------------

// A spaced label is the same identity input as its unspaced word: equal BoxlessFingerprint, and
// AnchorLabelText reads the same joined text the observation itself carries.
func TestFingerprint_ASpacedLabelIsTheSameIdentityInput(t *testing.T) {
	spacedPages := lsLabelPage("I N V O I C E N U M B E R", "OAP/2026/0088")
	unspacedPages := lsLabelPage("INVOICE NUMBER", "OAP/2026/0088")

	if got, want := extraction.BoxlessFingerprint(spacedPages), extraction.BoxlessFingerprint(unspacedPages); got != want {
		t.Errorf("BoxlessFingerprint differs: spaced %q, unspaced %q, want equal", got, want)
	}

	var obs extraction.AnchorObservation
	var found bool
	for _, o := range extraction.AnchorObservations(spacedPages) {
		if o.Label == "invoice_no" {
			obs, found = o, true
		}
	}
	if !found {
		t.Fatalf("the spaced page carries no invoice_no AnchorObservation; the text-identity assertion below needs one")
	}

	tok0 := spacedPages[0].Tokens[0]
	if got := extraction.AnchorLabelText(obs, tok0); got != "INVOICENUMBER" || got != obs.Text {
		t.Errorf("AnchorLabelText = %q, observation Text = %q, want both %q", got, obs.Text, "INVOICENUMBER")
	}
}

// --- 02-T8 ---------------------------------------------------------------------------------

// lsCorpusReads walks every committed fixture: every testdata/*.pdf through pdfium, every
// testdata/*.docling.json through its golden.
func lsCorpusReads(t *testing.T) map[string][]extraction.TokenPage {
	t.Helper()
	reads := map[string][]extraction.TokenPage{}

	pdfs, err := filepath.Glob(filepath.Join("testdata", "*.pdf"))
	if err != nil || len(pdfs) == 0 {
		t.Fatalf("glob testdata/*.pdf: %v (%d matches)", err, len(pdfs))
	}
	for _, p := range pdfs {
		name := filepath.Base(p)
		reads["pdfium:"+name] = rvCorpusPages(t, name)
	}

	goldens, err := filepath.Glob(filepath.Join("testdata", "*.docling.json"))
	if err != nil || len(goldens) == 0 {
		t.Fatalf("glob testdata/*.docling.json: %v (%d matches)", err, len(goldens))
	}
	for _, g := range goldens {
		name := filepath.Base(g)
		_, pages, _ := dcServeGolden(t, dcReadNamedGolden(t, name))
		reads["golden:"+name] = pages
	}
	return reads
}

// lsImageOnly is every read with no text layer at all -- the only reads 02-T8's token-count
// floor exempts.
var lsImageOnly = map[string]bool{
	"pdfium:dense_invoice.pdf":            true,
	"pdfium:scanned_invoice.pdf":          true,
	"pdfium:wild_scanned_no_number.pdf":   true,
	"golden:scanned_invoice.docling.json": true,
}

// lsQualifying is every read's letter-spaced tokens, the ones LabelViewForTest actually moves.
var lsQualifying = map[string][]string{
	"pdfium:advisory_register.pdf":                          {"I N V O I C E N U M B E R", "I S S U E D"},
	"pdfium:wild_stacked_borderless_asprinted.pdf":          {"I N V O I C E N U M B E R", "I S S U E D "},
	"golden:wild_stacked_borderless_asprinted.docling.json": {"I N V O I C E N U M B E R", "I S S U E D"},
}

type lsAdd struct {
	field, value, ruleID string
	distance             float64
}

// lsWantAdditions is the corpus differential's only predicted change: three reads gain the two
// header fields a letter-spaced label was blocking. Everything else is unchanged (P18).
var lsWantAdditions = map[string][]lsAdd{
	"pdfium:advisory_register.pdf": {
		{"invoice_number", "OAP/2026/0088", "t1.invoice_number.below", 0.014216},
		{"issue_date", "2026-09-01", "t1.issue_date.below", 0.014602},
	},
	"pdfium:wild_stacked_borderless_asprinted.pdf": {
		{"invoice_number", "INV-2204", "t1.invoice_number.below", 0.009050},
		{"issue_date", "2026-07-30", "t1.issue_date.right", 0.273745},
	},
	"golden:wild_stacked_borderless_asprinted.docling.json": {
		{"invoice_number", "INV-2204", "t1.invoice_number.below", 0.006187},
		{"issue_date", "2026-07-30", "t1.issue_date.right", 0.272294},
	},
}

type lsKey struct{ field, value, ruleID string }

func lsMultiset(cs []extraction.Candidate) map[lsKey]int {
	m := map[lsKey]int{}
	for _, c := range cs {
		m[lsKey{c.Field, c.Value, c.RuleID}]++
	}
	return m
}

// The label view reads only the six letter-spaced tokens the corpus actually prints, and moves
// candidates on exactly the three reads that print them -- no removal anywhere else.
func TestResolve_TheLetterSpacedViewReadsOnlyTheSpacedLabels(t *testing.T) {
	reads := lsCorpusReads(t)
	if len(reads) < 47 {
		t.Fatalf("walked %d read(s), want at least 47", len(reads))
	}

	qualifying := map[string][]string{}
	for name, pages := range reads {
		ntok := 0
		for _, p := range pages {
			ntok += len(p.Tokens)
		}
		if ntok == 0 && !lsImageOnly[name] {
			t.Errorf("%s reads 0 token(s) and is not a pinned image-only read", name)
		}
		if ntok > 0 && lsImageOnly[name] {
			t.Errorf("%s reads %d token(s), want 0 -- it is pinned as image-only", name, ntok)
		}

		for _, p := range pages {
			for _, tok := range p.Tokens {
				if extraction.LabelViewForTest(tok.Text) == tok.Text {
					continue
				}
				qualifying[name] = append(qualifying[name], tok.Text)
				if ids := extraction.AnchorLabelIDsForTest(tok.Text); len(ids) != 0 {
					t.Errorf("%s %q matches %v as printed; the census's premise is that it matches nothing", name, tok.Text, ids)
				}
			}
		}
	}

	total := 0
	for name, texts := range qualifying {
		sort.Strings(texts)
		want := append([]string(nil), lsQualifying[name]...)
		sort.Strings(want)
		if !slices.Equal(texts, want) {
			t.Errorf("%s qualifying tokens = %q, want %q", name, texts, want)
		}
		total += len(texts)
	}
	for name, want := range lsQualifying {
		if _, ok := qualifying[name]; !ok {
			t.Errorf("%s carries no qualifying token, want %v", name, want)
		}
	}
	if total != 6 {
		t.Errorf("census carries %d qualifying token(s) across the corpus, want exactly 6", total)
	}

	// Differential: compare each read against a copy where every qualifying token's text has its
	// spaces replaced by underscores. That rewrite is immune to the view either way it runs --
	// strings.Fields sees one already-solid unit, so labelView leaves it as printed -- so any
	// difference from the unmodified read is the view's own doing, not an artefact of the rewrite.
	for name, pages := range reads {
		orig := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})

		modPages := make([]extraction.TokenPage, len(pages))
		for i, p := range pages {
			toks := make([]extraction.Token, len(p.Tokens))
			copy(toks, p.Tokens)
			for j, tok := range toks {
				if extraction.LabelViewForTest(tok.Text) != tok.Text {
					toks[j].Text = strings.ReplaceAll(tok.Text, " ", "_")
				}
			}
			modPages[i] = extraction.TokenPage{Number: p.Number, WidthPt: p.WidthPt, HeightPt: p.HeightPt, Tokens: toks}
		}
		mod := extraction.Resolve(modPages, extraction.RuleSet{Tier1: extraction.Tier1Rules})

		om, mm := lsMultiset(orig), lsMultiset(mod)
		seen := map[lsKey]bool{}
		for k := range om {
			seen[k] = true
		}
		for k := range mm {
			seen[k] = true
		}
		var gained, lost []lsKey
		for k := range seen {
			d := om[k] - mm[k]
			for i := 0; i < d; i++ {
				gained = append(gained, k)
			}
			for i := 0; i < -d; i++ {
				lost = append(lost, k)
			}
		}

		want := lsWantAdditions[name]
		if len(gained) != len(want) {
			t.Errorf("%s gained %d row(s), want %d: got %+v, want %+v", name, len(gained), len(want), gained, want)
		} else {
			for _, w := range want {
				var found bool
				for _, c := range orig {
					if c.Field == w.field && c.Value == w.value && c.RuleID == w.ruleID {
						found = true
						if math.Abs(c.Distance-w.distance) > 5e-6 {
							t.Errorf("%s %s=%q distance = %.6f, want within 5e-6 of %.6f", name, w.field, w.value, c.Distance, w.distance)
						}
					}
				}
				if !found {
					t.Errorf("%s gained rows %+v, missing %+v", name, gained, w)
				}
			}
		}
		if len(lost) != 0 {
			t.Errorf("%s lost %+v, want none", name, lost)
		}
	}
}

// --- 02-T9 ---------------------------------------------------------------------------------

// A rule learned on the one-space form reads a three-space page: the label view is the identity
// both the learner and the reader see, whatever the reader collapsed the gap to.
func TestLearnRule_ARuleLearnedOnOneSpaceReadsThreeSpaces(t *testing.T) {
	onePages := lsLabelPage("I N V O I C E N U M B E R", "OAP/2026/0088")
	lr, ok := extraction.LearnRule("invoice_number", rvBox(0.10, 0.222, 0.22, 0.232), extraction.AnchorObservations(onePages))
	if !ok {
		t.Fatalf("LearnRule(invoice_number) refused: the one-space page carries no invoice_no observation")
	}

	threePages := lsLabelPage("I N V O I C E   N U M B E R", "OAP/2026/0088")
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{{ID: "ls", Field: "invoice_number", Rule: lr.Rule}}}
	got := rvFor(extraction.Resolve(threePages, rules), "invoice_number")
	if len(got) != 1 || got[0].Value != "OAP/2026/0088" || got[0].RuleID != "ls" {
		t.Errorf("Resolve(three-space page) invoice_number = %+v, want exactly OAP/2026/0088 from rule ls", got)
	}
}

// --- 02-T13 --------------------------------------------------------------------------------

// A rule learned on the unspaced twin R1 reads the spaced register R0, because R0 shares R1's
// layout identity once the view is wired -- and only where the learned label itself is a single
// word: the multi-word invoice-number rule reaches nothing on R0 ([r0-shares-r1-identity]).
func TestLearnRule_ARuleLearnedOnTheUnspacedTwinReadsTheSpacedRegister(t *testing.T) {
	r1 := rvCorpusPages(t, fxAdvisoryRegisterUnspaced)
	r0 := rvCorpusPages(t, fxAdvisoryRegister)
	obs1 := extraction.AnchorObservations(r1)

	dateRegion := advToken(t, r1, "2026-09-01").Region
	numRegion := advToken(t, r1, "OAP/2026/0088").Region

	lrDate, ok := extraction.LearnRule("issue_date", dateRegion, obs1)
	if !ok {
		t.Fatalf("LearnRule(issue_date, R1) refused; the rest of the test needs its rule")
	}
	if lrDate.Rule.Label != `(?i)\bIssued\b` || lrDate.Rule.Relation.Kind != extraction.RelBelow {
		t.Errorf("issue_date rule = %q / %s, want %q / below", lrDate.Rule.Label, lrDate.Rule.Relation.Kind, `(?i)\bIssued\b`)
	}

	lrNum, ok := extraction.LearnRule("invoice_number", numRegion, obs1)
	if !ok {
		t.Fatalf("LearnRule(invoice_number, R1) refused; the rest of the test needs its rule")
	}
	if lrNum.Rule.Label != `(?i)\bInvoice number\b` || lrNum.Rule.Relation.Kind != extraction.RelBelow {
		t.Errorf("invoice_number rule = %q / %s, want %q / below", lrNum.Rule.Label, lrNum.Rule.Relation.Kind, `(?i)\bInvoice number\b`)
	}

	// precondition, fatal: R0 and R1 must share a layout identity before a rule learned on R1
	// could ever key to R0 -- Store.AnchorRulesFor looks rules up by this value (anchor_store.go:29).
	if fp0, fp1 := extraction.Fingerprint(r0), extraction.Fingerprint(r1); fp0 != fp1 {
		t.Fatalf("R0 does not share R1's fingerprint (%q vs %q)", fp0, fp1)
	}
	if bx0, bx1 := extraction.BoxlessFingerprint(r0), extraction.BoxlessFingerprint(r1); bx0 != bx1 {
		t.Fatalf("R0 does not share R1's boxless fingerprint (%q vs %q)", bx0, bx1)
	}

	// control, fatal: the learned rules must read their own source page first.
	ctlDate := rvFor(extraction.Resolve(r1, extraction.RuleSet{Learned: []extraction.AnchorRule{{ID: "r1-issue_date", Field: "issue_date", Rule: lrDate.Rule}}}), "issue_date")
	if len(ctlDate) != 1 || ctlDate[0].Value != "2026-09-01" {
		t.Fatalf("R1 issue_date via its own learned rule = %+v, want exactly [2026-09-01]", ctlDate)
	}
	ctlNum := rvFor(extraction.Resolve(r1, extraction.RuleSet{Learned: []extraction.AnchorRule{{ID: "r1-invoice_number", Field: "invoice_number", Rule: lrNum.Rule}}}), "invoice_number")
	if len(ctlNum) != 1 || ctlNum[0].Value != "OAP/2026/0088" {
		t.Fatalf("R1 invoice_number via its own learned rule = %+v, want exactly [OAP/2026/0088]", ctlNum)
	}

	gotDate := rvFor(extraction.Resolve(r0, extraction.RuleSet{Learned: []extraction.AnchorRule{{ID: "r1-issue_date", Field: "issue_date", Rule: lrDate.Rule}}}), "issue_date")
	if v := rvValues(gotDate); !slices.Equal(v, []string{"2026-09-01"}) {
		t.Errorf("R0 issue_date via R1's learned rule = %v, want exactly [2026-09-01]", v)
	}

	gotNum := rvFor(extraction.Resolve(r0, extraction.RuleSet{Learned: []extraction.AnchorRule{{ID: "r1-invoice_number", Field: "invoice_number", Rule: lrNum.Rule}}}), "invoice_number")
	if len(gotNum) != 0 {
		t.Errorf("R0 invoice_number via R1's learned rule = %v, want none -- a multi-word label reads nothing on the spaced layout", rvValues(gotNum))
	}
}
