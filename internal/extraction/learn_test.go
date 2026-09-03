// learn_test.go: O-06, O-07, O-09 -- the layout_anchors codec's happy path and AnchorLabelText
// against the real corpus. External package: every spec reaches only exported symbols.
package extraction_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// O-06: marshal -> unmarshal of an observation list is the identity, across bands 0/1/2, a
// zero-area box and an empty Text; a nil or empty list marshals to "[]", never "null" -- the
// layout_anchors column's CHECK takes an array.
func TestAnchorObservationsCodec_RoundTripsTheIdentity(t *testing.T) {
	table := []extraction.AnchorObservation{
		{Label: "invoice_no", Text: "Invoice No", Page: 1, Band: 0, X0: 0.1194, Y0: 0.1179, X1: 0.3034, Y1: 0.1291},
		{Label: "buyer_name", Text: "Buyer", Page: 1, Band: 2, X0: 0.6550, Y0: 0.1937, X1: 0.7048, Y1: 0.2078},
		{Label: "supplier_tin", Text: "", Page: 1, Band: 1, X0: 0.5, Y0: 0.5, X1: 0.5, Y1: 0.5}, // zero-area box, empty Text
	}

	raw, err := extraction.MarshalAnchorObservations(table)
	if err != nil {
		t.Fatalf("MarshalAnchorObservations(table) error = %v, want nil", err)
	}

	got, err := extraction.UnmarshalAnchorObservations(raw)
	if err != nil {
		t.Fatalf("UnmarshalAnchorObservations(marshalled table) error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, table) {
		t.Errorf("round-tripped observations = %+v, want %+v", got, table)
	}

	for _, empty := range [][]extraction.AnchorObservation{nil, {}} {
		raw, err := extraction.MarshalAnchorObservations(empty)
		if err != nil {
			t.Fatalf("MarshalAnchorObservations(%#v) error = %v, want nil", empty, err)
		}
		if string(raw) != "[]" {
			t.Errorf("MarshalAnchorObservations(%#v) = %q, want \"[]\": the column's CHECK takes an array, never null", empty, string(raw))
		}
	}
}

// O-07: an array element carrying an unknown key decodes with no error, the unknown key
// ignored, and every known field intact -- forward compatibility, the same discipline
// ParseRule already follows (TestParseRule_IgnoresUnknownKeys).
func TestAnchorObservationsCodec_IgnoresAnUnknownKey(t *testing.T) {
	raw := []byte(`[{"label":"total","text":"Total","page":1,"band":0,"x0":0,"y0":0,"x1":0.1,"y1":0.1,"confidence":0.9}]`)

	got, err := extraction.UnmarshalAnchorObservations(raw)
	if err != nil {
		t.Fatalf("UnmarshalAnchorObservations(unknown key) error = %v, want nil", err)
	}
	want := []extraction.AnchorObservation{{Label: "total", Text: "Total", Page: 1, Band: 0, X0: 0, Y0: 0, X1: 0.1, Y1: 0.1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnmarshalAnchorObservations(unknown key) = %+v, want %+v -- the known fields must still decode, not merely error-free", got, want)
	}
}

// O-09: AnchorLabelText returns the lexicon pattern's own matched substring, measured against
// the real reader on two different corpus layouts.
func TestAnchorLabelText_MatchesTheMeasuredCorpusOracles(t *testing.T) {
	for _, c := range []struct {
		file  string
		label string
		want  string
	}{
		{"corpus_two_column.pdf", "buyer_name", "Buyer"},
		{"corpus_split_labels.pdf", "invoice_no", "Invoice No"},
	} {
		t.Run(c.file+"/"+c.label, func(t *testing.T) {
			pages := rvCorpusPages(t, c.file)
			obs := extraction.AnchorObservations(pages)

			var found extraction.AnchorObservation
			var srcTok extraction.Token
			haveFound := false
			for _, page := range pages {
				if page.Number != 1 {
					continue
				}
				for _, o := range obs {
					if o.Label != c.label {
						continue
					}
					for _, candidate := range page.Tokens {
						if candidate.Region.X0 == o.X0 && candidate.Region.Y0 == o.Y0 &&
							candidate.Region.X1 == o.X1 && candidate.Region.Y1 == o.Y1 {
							found, srcTok, haveFound = o, candidate, true
						}
					}
				}
			}
			if !haveFound {
				t.Fatalf("%s: no observation/token pair found for label %q", c.file, c.label)
			}

			if got := extraction.AnchorLabelText(found, srcTok); got != c.want {
				t.Errorf("AnchorLabelText(%s observation, its own token) = %q, want %q", c.label, got, c.want)
			}
		})
	}
}

// --- EXTR-14-04: LearnRule, the derivation -----------------------------------
//
// R-01 is corrected during this RED pass: the story's fixture (buyer TIN via below/0.03) is
// unreachable under LearnRule's own step-5 ordering (same_token always wins at gap 0), and its
// expected rule extracts nothing from the page it names -- ShapeTIN's anchored pattern rejects
// "TIN: 99999999-0402" whole. Measured with the real reader instead: same_token, gap 0, label
// "(?i)\bTIN\b" (see .ralph/subtasks/extr-14-04-arch.md S:0). AC-3's below coverage moves to a
// second fixture on the same page (TestLearnRule_R01Below_BuyerNameBelowBuyerLabel).

// rvAnchor builds a synthetic AnchorObservation on page 1, band 0. LearnRule reads o.Text
// directly and does not require it to come from the real anchor lexicon.
func rvAnchor(label, text string, x0, y0, x1, y1 float64) extraction.AnchorObservation {
	return extraction.AnchorObservation{Label: label, Text: text, Page: 1, Band: 0, X0: x0, Y0: y0, X1: x1, Y1: y1}
}

// rvTokenByText finds the one page-1 token whose text is an exact match. A count other than 1
// fails loud: a silent duplicate would make "the token's own box" ambiguous.
func rvTokenByText(t *testing.T, pages []extraction.TokenPage, text string) extraction.Token {
	t.Helper()

	var found extraction.Token
	n := 0
	for _, page := range pages {
		if page.Number != 1 {
			continue
		}
		for _, tok := range page.Tokens {
			if tok.Text == text {
				found = tok
				n++
			}
		}
	}
	if n != 1 {
		t.Fatalf("page 1 carries %d token(s) with text %q, want exactly 1", n, text)
	}
	return found
}

// rvObservationFor finds the AnchorObservation AnchorObservations derived for tok specifically
// -- not merely "an observation with this label", since two labels can share a box (R-12).
func rvObservationFor(t *testing.T, obs []extraction.AnchorObservation, label string, tok extraction.Token) extraction.AnchorObservation {
	t.Helper()

	for _, o := range obs {
		if o.Label == label && o.X0 == tok.Region.X0 && o.Y0 == tok.Region.Y0 && o.X1 == tok.Region.X1 && o.Y1 == tok.Region.Y1 {
			return o
		}
	}
	t.Fatalf("no %q observation over the box of %+v", label, tok)
	return extraction.AnchorObservation{}
}

// R-01' (corrected): pointing at the buyer TIN token derives same_token at gap 0 for buyer_tin
// -- even though the matching AnchorObservation's own Label is "supplier_tin": the lexicon's
// supplier_tin pattern has an optional prefix and matches a bare "TIN", while buyer_tin's
// pattern needs the word "buyer" inside the SAME token, which this one does not carry (it sits
// in the adjacent "Buyer" token instead).
func TestLearnRule_R01_SameTokenOnTheSharedBuyerTINBox(t *testing.T) {
	pages := rvCorpusPages(t, "corpus_two_column.pdf")
	obs := extraction.AnchorObservations(pages)

	tok := rvTokenByText(t, pages, "TIN: 99999999-0402")
	anchor := rvObservationFor(t, obs, "supplier_tin", tok)

	lr, ok := extraction.LearnRule("buyer_tin", tok.Region, obs)
	if !ok {
		t.Fatalf("LearnRule(buyer_tin, the buyer TIN token's own box) ok = false, want true")
	}
	if lr.Anchor != anchor {
		t.Errorf("LearnRule anchor = %+v, want the supplier_tin observation over the same box: %+v", lr.Anchor, anchor)
	}

	const want = `{"label":"(?i)\\bTIN\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	if string(lr.Body) != want {
		t.Errorf("LearnRule(buyer_tin) body = %s, want %s", lr.Body, want)
	}
	if lr.Rule.Relation.MaxDistance != 0 {
		t.Errorf("LearnRule(buyer_tin) max_distance = %v, want 0 -- AC-1's own assertion, not just the relation kind", lr.Rule.Relation.MaxDistance)
	}
}

// R-01's below coverage: pointing at "Honeywell Group" derives below for buyer_name, at the
// real gap between the "Buyer" row and the name row.
func TestLearnRule_R01Below_BuyerNameBelowBuyerLabel(t *testing.T) {
	pages := rvCorpusPages(t, "corpus_two_column.pdf")
	obs := extraction.AnchorObservations(pages)

	tok := rvTokenByText(t, pages, "Honeywell Group")
	anchor := rvObservationFor(t, obs, "buyer_name", rvTokenByText(t, pages, "Buyer"))

	lr, ok := extraction.LearnRule("buyer_name", tok.Region, obs)
	if !ok {
		t.Fatalf("LearnRule(buyer_name, %q's box) ok = false, want true", "Honeywell Group")
	}
	if lr.Anchor != anchor {
		t.Errorf("LearnRule anchor = %+v, want the buyer_name observation %+v", lr.Anchor, anchor)
	}

	const want = `{"label":"(?i)\\bBuyer\\b","relation":{"kind":"below","max_distance":0.01},"shape":"name"}`
	if string(lr.Body) != want {
		t.Errorf("LearnRule(buyer_name) body = %s, want %s", lr.Body, want)
	}
}

// R-02: pointing at the value token on corpus_split_labels.pdf derives right for
// invoice_number, at the token's own real gap -- and "Invoice Date", on the row above, whose
// raw X gap is actually smaller, is correctly excluded: its row does not vertically overlap the
// value's row at all.
func TestLearnRule_R02_RightWithVerticalOverlap(t *testing.T) {
	pages := rvCorpusPages(t, "corpus_split_labels.pdf")
	obs := extraction.AnchorObservations(pages)

	value := rvTokenByText(t, pages, "INV-1002")
	invoiceNoAnchor := rvObservationFor(t, obs, "invoice_no", rvTokenByText(t, pages, "Invoice No ")) // pdfium's own token carries the trailing space; AnchorObservations' matched substring does not

	lr, ok := extraction.LearnRule("invoice_number", value.Region, obs)
	if !ok {
		t.Fatalf("LearnRule(invoice_number, INV-1002's box) ok = false, want true")
	}
	if lr.Anchor != invoiceNoAnchor {
		t.Errorf("LearnRule anchor = %+v, want the invoice_no observation %+v -- Invoice Date must not win despite a smaller raw X gap", lr.Anchor, invoiceNoAnchor)
	}

	const want = `{"label":"(?i)\\bInvoice No\\b","relation":{"kind":"right","max_distance":0.16},"shape":"invoice_number"}`
	if string(lr.Body) != want {
		t.Errorf("LearnRule(invoice_number) body = %s, want %s", lr.Body, want)
	}
}

// R-03: pointing at the whole "Invoice No: INV-1001" token overlaps its own anchor box, so it
// derives same_token at gap 0 -- not right or below.
func TestLearnRule_R03_SameTokenOverlapOnInlineLabel(t *testing.T) {
	pages := rvCorpusPages(t, "corpus_inline_labels.pdf")
	obs := extraction.AnchorObservations(pages)

	tok := rvTokenByText(t, pages, "Invoice No: INV-1001")
	anchor := rvObservationFor(t, obs, "invoice_no", tok)

	lr, ok := extraction.LearnRule("invoice_number", tok.Region, obs)
	if !ok {
		t.Fatalf("LearnRule(invoice_number, the token's own box) ok = false, want true")
	}
	if lr.Anchor != anchor {
		t.Errorf("LearnRule anchor = %+v, want %+v", lr.Anchor, anchor)
	}

	const want = `{"label":"(?i)\\bInvoice No\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"invoice_number"}`
	if string(lr.Body) != want {
		t.Errorf("LearnRule(invoice_number) body = %s, want %s", lr.Body, want)
	}
}

// R-12: the "Sub-total" token on corpus_totals_block.pdf is matched by two lexicon entries --
// subtotal and total, sharing the token's own box. Both are same_token candidates at gap 0, so
// the tie goes to lexicon order: subtotal (index 7) beats total (index 9). QuoteMeta does not
// escape "-", so a label of "(?i)\bSub\-total\b" would be wrong.
func TestLearnRule_R12_LexiconTieBreakPicksSubtotalOverTotal(t *testing.T) {
	pages := rvCorpusPages(t, "corpus_totals_block.pdf")
	obs := extraction.AnchorObservations(pages)

	tok := rvTokenByText(t, pages, "Sub-total ") // pdfium's own token carries the trailing space
	subtotalAnchor := rvObservationFor(t, obs, "subtotal", tok)
	totalAnchor := rvObservationFor(t, obs, "total", tok)
	if subtotalAnchor.X0 != totalAnchor.X0 || subtotalAnchor.Y0 != totalAnchor.Y0 {
		t.Fatalf("subtotal and total observations do not share a box (%+v vs %+v); the tie-break below would be vacuous", subtotalAnchor, totalAnchor)
	}

	lr, ok := extraction.LearnRule("subtotal", tok.Region, obs)
	if !ok {
		t.Fatalf("LearnRule(subtotal, the shared box) ok = false, want true")
	}
	if lr.Anchor != subtotalAnchor {
		t.Errorf("LearnRule anchor = %+v, want the subtotal observation %+v -- lexicon order must pick subtotal over total", lr.Anchor, subtotalAnchor)
	}

	const want = `{"label":"(?i)\\bSub-total\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"amount"}`
	if string(lr.Body) != want {
		t.Errorf("LearnRule(subtotal) body = %s, want %s", lr.Body, want)
	}
}

// R-14: LearnRule returns each of the seven correctable HeaderFields' own tier1Specs shape.
// invoice_number, supplier_tin and supplier_name are locked at the correction HANDLER
// (refuseField), not inside LearnRule, which applies no field lock of its own -- so this table
// only needs the seven fields a correction can actually name.
func TestLearnRule_R14_ShapeMatchesTier1SpecsAcrossCorrectableFields(t *testing.T) {
	anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.30, 0.13)
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}

	for _, c := range []struct {
		field string
		shape extraction.Shape
	}{
		{"issue_date", extraction.ShapeDate},
		{"buyer_tin", extraction.ShapeTIN},
		{"buyer_name", extraction.ShapeName},
		{"currency", extraction.ShapeCurrency},
		{"subtotal", extraction.ShapeAmount},
		{"vat", extraction.ShapeAmount},
		{"total", extraction.ShapeAmount},
	} {
		t.Run(c.field, func(t *testing.T) {
			lr, ok := extraction.LearnRule(c.field, region, []extraction.AnchorObservation{anchor})
			if !ok {
				t.Fatalf("LearnRule(%s) ok = false, want true", c.field)
			}
			if lr.Rule.Shape != c.shape {
				t.Errorf("LearnRule(%s) shape = %q, want %q", c.field, lr.Rule.Shape, c.shape)
			}
		})
	}
}

// R-11: every derived body round-trips through ParseRule, across a table spanning all three
// relations, a label carrying regexp metacharacters, a non-ASCII leading rune, and the byte cap.
func TestLearnRule_R11_EveryDerivedBodyParses(t *testing.T) {
	table := []struct {
		name   string
		field  string
		anchor extraction.AnchorObservation
		region extraction.Region
	}{
		{"same_token ascii", "invoice_number", rvAnchor("a", "Invoice No", 0.10, 0.10, 0.30, 0.13), extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}},
		{"right", "issue_date", rvAnchor("a", "Date", 0.10, 0.20, 0.20, 0.23), extraction.Region{Page: 1, X0: 0.25, Y0: 0.20, X1: 0.35, Y1: 0.23}},
		{"below", "buyer_name", rvAnchor("a", "Buyer", 0.10, 0.30, 0.30, 0.33), extraction.Region{Page: 1, X0: 0.10, Y0: 0.35, X1: 0.30, Y1: 0.37}},
		{"same_token dot metachar", "invoice_number", rvAnchor("a", "Inv. No.", 0.10, 0.40, 0.25, 0.43), extraction.Region{Page: 1, X0: 0.10, Y0: 0.40, X1: 0.25, Y1: 0.43}},
		{"same_token parens metachar", "total", rvAnchor("a", "Total (NGN)", 0.10, 0.50, 0.30, 0.53), extraction.Region{Page: 1, X0: 0.10, Y0: 0.50, X1: 0.30, Y1: 0.53}},
		{"same_token asterisk metachar", "vat", rvAnchor("a", "VAT*", 0.10, 0.60, 0.20, 0.63), extraction.Region{Page: 1, X0: 0.10, Y0: 0.60, X1: 0.20, Y1: 0.63}},
		{"same_token non-ascii leading rune", "currency", rvAnchor("a", "État", 0.10, 0.70, 0.20, 0.73), extraction.Region{Page: 1, X0: 0.10, Y0: 0.70, X1: 0.20, Y1: 0.73}},
		{"same_token byte cap", "vat", rvAnchor("a", strings.Repeat("$", 512), 0.10, 0.80, 0.90, 0.83), extraction.Region{Page: 1, X0: 0.10, Y0: 0.80, X1: 0.90, Y1: 0.83}},
		{"right at the cap", "subtotal", rvAnchor("a", "Sub", 0.05, 0.05, 0.12, 0.08), extraction.Region{Page: 1, X0: 0.47, Y0: 0.05, X1: 0.57, Y1: 0.08}},
		{"below at the cap", "vat", rvAnchor("a", "VAT", 0.05, 0.04, 0.15, 0.10), extraction.Region{Page: 1, X0: 0.05, Y0: 0.16, X1: 0.15, Y1: 0.18}},
		{"same_token colon", "currency", rvAnchor("a", "Currency:", 0.60, 0.10, 0.70, 0.13), extraction.Region{Page: 1, X0: 0.60, Y0: 0.10, X1: 0.70, Y1: 0.13}},
		{"right backslash metachar", "total", rvAnchor("a", `Amount\Total`, 0.60, 0.20, 0.70, 0.23), extraction.Region{Page: 1, X0: 0.72, Y0: 0.20, X1: 0.82, Y1: 0.23}},
	}
	if len(table) == 0 {
		t.Fatal("the fixture table is empty; every assertion below would be vacuous")
	}

	for _, c := range table {
		t.Run(c.name, func(t *testing.T) {
			lr, ok := extraction.LearnRule(c.field, c.region, []extraction.AnchorObservation{c.anchor})
			if !ok {
				t.Fatalf("LearnRule(%s) ok = false, want true", c.field)
			}
			if _, err := extraction.ParseRule(lr.Body); err != nil {
				t.Errorf("ParseRule(%s) error = %v, want nil", lr.Body, err)
			}
		})
	}
}

// R-17: a derived rule, applied by Resolve to the very page it was derived from, returns the
// corrected value as a TierLearned candidate -- the pin that the learner and the resolver share
// one geometry.
func TestLearnRule_R17_DerivedRuleRoundTripsThroughResolve(t *testing.T) {
	for _, c := range []struct {
		name      string
		file      string
		field     string
		clickText string // the token whose box is the correction's region
		wantValue string
	}{
		{"buyer_tin same_token", "corpus_two_column.pdf", "buyer_tin", "TIN: 99999999-0402", "99999999-0402"},
		{"buyer_name below", "corpus_two_column.pdf", "buyer_name", "Honeywell Group", "Honeywell Group"},
		{"invoice_number right", "corpus_split_labels.pdf", "invoice_number", "INV-1002", "INV-1002"},
		{"invoice_number same_token", "corpus_inline_labels.pdf", "invoice_number", "Invoice No: INV-1001", "INV-1001"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pages := rvCorpusPages(t, c.file)
			obs := extraction.AnchorObservations(pages)
			tok := rvTokenByText(t, pages, c.clickText)

			lr, ok := extraction.LearnRule(c.field, tok.Region, obs)
			if !ok {
				t.Fatalf("LearnRule(%s) ok = false, want true", c.field)
			}

			rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
				{ID: "learned-test", Field: c.field, Rule: lr.Rule},
			}}
			got := extraction.Resolve(pages, rules)

			found := false
			for _, cand := range got {
				if cand.Field == c.field && cand.Value == c.wantValue && cand.Tier == extraction.TierLearned {
					found = true
				}
			}
			if !found {
				t.Errorf("Resolve with the learned rule for %s = %+v, want a TierLearned candidate with value %q", c.field, got, c.wantValue)
			}
		})
	}
}
