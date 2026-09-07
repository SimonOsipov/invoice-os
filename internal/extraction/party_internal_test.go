// party_internal_test.go: the party partition. Package extraction, not extraction_test: these
// call the unexported partyHeading/partyOrder, read partyHeadings and anchorLabelMatchers, and
// scan party.go's own source.
package extraction

import (
	"fmt"
	"go/ast"
	"slices"
	"testing"
)

// ptPage builds a boxless page. Zero texts leaves Tokens nil, which is the empty-page case.
func ptPage(number int, texts ...string) TokenPage {
	var toks []Token
	for _, s := range texts {
		toks = append(toks, Token{Text: s})
	}
	return TokenPage{Number: number, WidthPt: 595, HeightPt: 842, Tokens: toks}
}

// ptName names a Party for a failure message; Party is an int with no String method.
func ptName(p Party) string {
	switch p {
	case PartyUnknown:
		return "PartyUnknown"
	case PartySupplier:
		return "PartySupplier"
	case PartyBuyer:
		return "PartyBuyer"
	}
	return fmt.Sprintf("Party(%d)", int(p))
}

func ptNames(ps []Party) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = ptName(p)
	}
	return out
}

// ptMatcher is the compiled matcher anchorLabelMatchers holds for id. By value: nothing here
// may alias the shipped slice.
func ptMatcher(id string) (anchorMatcher, bool) {
	for _, m := range anchorLabelMatchers {
		if m.ID == id {
			return m, true
		}
	}
	return anchorMatcher{}, false
}

func TestPartyOrder_AssignsEveryTokenToTheHeadingBeforeIt(t *testing.T) {
	page := ptPage(1, "Supplier", "Acme Ltd", "TIN: 99999999-0101", "Buyer", "Zeta Plc", "TIN: 99999999-0702")
	want := []Party{PartySupplier, PartySupplier, PartySupplier, PartyBuyer, PartyBuyer, PartyBuyer}

	if len(page.Tokens) != len(want) {
		t.Fatalf("the fixture carries %d token(s) against %d expected part(ies); the assertion below would compare nothing", len(page.Tokens), len(want))
	}

	got := partyOrder(page)
	if len(got) != len(page.Tokens) {
		t.Fatalf("partyOrder returned %d entry/entries over %d token(s), want one per token", len(got), len(page.Tokens))
	}
	if !slices.Equal(got, want) {
		t.Errorf("partyOrder = %v, want %v; a heading belongs to itself and owns every token until the next heading", ptNames(got), ptNames(want))
	}
}

func TestPartyOrder_TokensBeforeTheFirstHeadingAreUnknown(t *testing.T) {
	// "Bill To", not "Invoice to": buyer_name does not match "Invoice to" until EXTR-22-02.
	const headingIdx = 6
	page := ptPage(1,
		"INVOICE", "No. INV-001", "Date: 2026-01-05",
		"Acme Ltd", "TIN:", "99999999-0101",
		"Bill To",
		"Zeta Plc", "TIN:", "99999999-0702",
	)

	if len(page.Tokens) <= headingIdx {
		t.Fatalf("the fixture carries %d token(s), want more than %d; the heading is not on the page", len(page.Tokens), headingIdx)
	}
	if got := partyHeading(page.Tokens[headingIdx].Text); got != PartyBuyer {
		t.Fatalf("partyHeading(%q) = %s, want PartyBuyer; the fixture's only heading heads nothing, so every index below would be PartyUnknown for the wrong reason",
			page.Tokens[headingIdx].Text, ptName(got))
	}

	got := partyOrder(page)
	if len(got) != len(page.Tokens) {
		t.Fatalf("partyOrder returned %d entry/entries over %d token(s), want one per token", len(got), len(page.Tokens))
	}
	for i, p := range got {
		want := PartyUnknown
		if i >= headingIdx {
			want = PartyBuyer
		}
		if p != want {
			t.Errorf("partyOrder()[%d] (%q) = %s, want %s", i, page.Tokens[i].Text, ptName(p), ptName(want))
		}
	}
}

func TestPartyOrder_APageWithNoHeadingIsAllUnknown(t *testing.T) {
	bare := []string{"TIN:", "99999999-0101", "TIN:", "99999999-0702"}

	page := ptPage(1, bare...)
	got := partyOrder(page)
	if len(got) != len(bare) {
		t.Fatalf("partyOrder returned %d entry/entries over %d token(s), want one per token", len(got), len(bare))
	}
	for i, p := range got {
		if p != PartyUnknown {
			t.Errorf("partyOrder()[%d] (%q) = %s, want PartyUnknown; no heading appears on this page", i, bare[i], ptName(p))
		}
	}

	// The control: all-PartyUnknown is also what a stub returns, so the same page under one
	// heading must come back all-PartySupplier or the assertion above proves nothing.
	control := partyOrder(ptPage(1, append([]string{"Supplier"}, bare...)...))
	if len(control) != len(bare)+1 {
		t.Fatalf("the control returned %d entry/entries over %d token(s), want one per token", len(control), len(bare)+1)
	}
	for i, p := range control {
		if p != PartySupplier {
			t.Errorf("the control's partyOrder()[%d] = %s, want PartySupplier; a page led by one heading is wholly that party", i, ptName(p))
		}
	}
}

// The page bound is structural -- partyOrder takes one TokenPage, so a heading cannot reach the
// next page. This pins that no state crosses a call, and that the same tokens on a shared page
// answer differently.
func TestPartyOrder_OwnershipNeverCrossesAPage(t *testing.T) {
	texts1 := []string{"Invoice", "Supplier", "Acme Ltd", "Buyer"}
	texts2 := []string{"99999999-0702"}

	page1 := ptPage(1, texts1...)
	page2 := ptPage(2, texts2...)
	if page1.Number != 1 || page2.Number != 2 {
		t.Fatalf("the fixture pages are numbered %d and %d, want 1 and 2; the boundary under test is not the one written down", page1.Number, page2.Number)
	}

	want1 := []Party{PartyUnknown, PartySupplier, PartySupplier, PartyBuyer}
	got1 := partyOrder(page1)
	if len(got1) != len(texts1) {
		t.Fatalf("partyOrder(page 1) returned %d entry/entries over %d token(s), want one per token", len(got1), len(texts1))
	}
	if !slices.Equal(got1, want1) {
		t.Errorf("partyOrder(page 1) = %v, want %v", ptNames(got1), ptNames(want1))
	}

	got2 := partyOrder(page2)
	if len(got2) != len(texts2) {
		t.Fatalf("partyOrder(page 2) returned %d entry/entries over %d token(s), want one per token", len(got2), len(texts2))
	}
	if got2[0] != PartyUnknown {
		t.Errorf("partyOrder(page 2)[0] (%q) = %s, want PartyUnknown; page 1's trailing Buyer heading owns nothing on page 2", texts2[0], ptName(got2[0]))
	}

	// The control: the same texts as ONE page. Without it the assertion above holds for a
	// partyOrder that never partitions at all.
	joined := ptPage(1, append(slices.Clone(texts1), texts2...)...)
	gotJoined := partyOrder(joined)
	if len(gotJoined) != len(texts1)+len(texts2) {
		t.Fatalf("the joined-page control returned %d entry/entries over %d token(s), want one per token", len(gotJoined), len(texts1)+len(texts2))
	}
	if last := gotJoined[len(gotJoined)-1]; last != PartyBuyer {
		t.Errorf("the joined-page control's last entry (%q) = %s, want PartyBuyer; on one page the heading does reach it, so the page boundary is the only difference", texts2[0], ptName(last))
	}
}

func TestPartyOrder_ABoxlessPagePartitionsTheSame(t *testing.T) {
	texts := []string{"Invoice", "Supplier", "Acme Ltd", "TIN: 99999999-0101", "Buyer", "Zeta Plc"}
	// Buyer's box sits ABOVE Supplier's while token order keeps Supplier first, so a
	// nearest-heading-by-box implementation partitions the boxed page differently.
	boxes := []Region{
		{Page: 1, X0: 0.10, Y0: 0.04, X1: 0.40, Y1: 0.08},
		{Page: 1, X0: 0.10, Y0: 0.60, X1: 0.40, Y1: 0.64},
		{Page: 1, X0: 0.10, Y0: 0.65, X1: 0.40, Y1: 0.69},
		{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.40, Y1: 0.24},
		{Page: 1, X0: 0.10, Y0: 0.14, X1: 0.40, Y1: 0.18},
		{Page: 1, X0: 0.10, Y0: 0.70, X1: 0.40, Y1: 0.74},
	}

	if len(texts) < 4 || len(boxes) != len(texts) {
		t.Fatalf("the fixture carries %d text(s) and %d box(es), want at least 4 of each and equal counts", len(texts), len(boxes))
	}

	boxed := ptPage(1, texts...)
	for i := range boxed.Tokens {
		boxed.Tokens[i].Region = boxes[i]
		if !usableBox(boxed.Tokens[i].Region) {
			t.Fatalf("boxed token %d (%q) carries an unusable box %+v; the boxed variant is not boxed", i, texts[i], boxes[i])
		}
	}

	boxless := ptPage(1, texts...)
	for i, tok := range boxless.Tokens {
		if usableBox(tok.Region) {
			t.Fatalf("boxless token %d (%q) carries a usable box; the boxless variant is not boxless", i, texts[i])
		}
	}

	withBoxes := partyOrder(boxed)
	if len(withBoxes) != len(texts) {
		t.Fatalf("partyOrder(boxed) returned %d entry/entries over %d token(s), want one per token", len(withBoxes), len(texts))
	}

	// Non-trivial before equal: two all-PartyUnknown slices compare equal.
	for _, want := range []Party{PartyUnknown, PartySupplier, PartyBuyer} {
		if !slices.Contains(withBoxes, want) {
			t.Fatalf("partyOrder(boxed) = %v, which holds no %s; the equality below would compare a degenerate partition", ptNames(withBoxes), ptName(want))
		}
	}

	gotBoxless := partyOrder(boxless)
	if !slices.Equal(withBoxes, gotBoxless) {
		t.Errorf("partyOrder(boxed) = %v, partyOrder(boxless) = %v, want equal; partyOrder reads no box, so the zero box changes nothing",
			ptNames(withBoxes), ptNames(gotBoxless))
	}
}

func TestPartyHeading_ATokenNamingBothPartiesHeadsNeither(t *testing.T) {
	const both = "Supplier / Buyer"

	// The input must genuinely match both vocabularies, or PartyUnknown below would only mean
	// "matched nothing".
	if len(partyHeadings) == 0 {
		t.Fatal("partyHeadings is empty; the loop below would check nothing")
	}
	for _, h := range partyHeadings {
		m, ok := ptMatcher(h.labelID)
		if !ok {
			t.Fatalf("anchorLabelMatchers holds no entry %q", h.labelID)
		}
		if !m.RE.MatchString(both) {
			t.Fatalf("%q does not match the %q pattern; the fixture names one party, not both", both, h.labelID)
		}
	}

	if got := partyHeading(both); got != PartyUnknown {
		t.Errorf("partyHeading(%q) = %s, want PartyUnknown; a heading naming both parties names neither", both, ptName(got))
	}
	if got := partyHeading("Supplier"); got != PartySupplier {
		t.Errorf("partyHeading(%q) = %s, want PartySupplier; the control shows the one-party case still heads its party", "Supplier", ptName(got))
	}
}

func TestPartyHeading_ReadsTheLexiconAndNotACopy(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty; every assertion below would run over nothing")
	}
	if len(partyHeadings) != 2 {
		t.Fatalf("partyHeadings holds %d entry/entries, want 2", len(partyHeadings))
	}

	var ids []string
	var parties []Party
	for _, h := range partyHeadings {
		if slices.Contains(ids, h.labelID) {
			t.Errorf("partyHeadings names lexicon id %q twice", h.labelID)
		}
		if slices.Contains(parties, h.party) {
			t.Errorf("partyHeadings names %s twice", ptName(h.party))
		}
		ids = append(ids, h.labelID)
		parties = append(parties, h.party)

		// A labelID the lexicon lacks leaves partyHeading silent on every token, which reads
		// exactly like a page with no headings.
		if _, ok := ptMatcher(h.labelID); !ok {
			t.Fatalf("anchorLabelMatchers holds no entry %q; partyHeading would match nothing at all", h.labelID)
		}
		if anchorPattern(h.labelID) == "" {
			t.Errorf("anchorPattern(%q) is empty; an empty label compiles and then matches every token", h.labelID)
		}
	}
	if !slices.Contains(parties, PartySupplier) || !slices.Contains(parties, PartyBuyer) {
		t.Errorf("partyHeadings names %v, want one PartySupplier and one PartyBuyer", ptNames(parties))
	}

	// The lexicon's non-obvious alternatives. A partyHeading that re-spelled the patterns as
	// \bsupplier\b / \bbuyer\b answers PartyUnknown to all four.
	probes := []struct {
		text string
		want Party
	}{
		{"Vendor", PartySupplier},
		{"Seller", PartySupplier},
		{"Sold To", PartyBuyer},
		{"Client", PartyBuyer},
	}
	if len(probes) == 0 {
		t.Fatal("no probes; the loop below would check nothing")
	}
	for _, p := range probes {
		if got := partyHeading(p.text); got != p.want {
			t.Errorf("partyHeading(%q) = %s, want %s; the pattern is read from the lexicon, not re-spelled here", p.text, ptName(got), ptName(p.want))
		}
	}
}

// partyBannedSelectors is geometry entering party.go. Region itself is banned, not only the four
// coordinates: passing a Region onward is geometry too.
var partyBannedSelectors = []string{"Region", "X0", "Y0", "X1", "Y1"}

// ptGeometrySelectors names every banned selector written in f.
func ptGeometrySelectors(f *ast.File) []string {
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && slices.Contains(partyBannedSelectors, sel.Sel.Name) {
			hits = append(hits, sel.Sel.Name)
		}
		return true
	})
	return hits
}

func TestParty_UsesNoMapAndNoGeometry(t *testing.T) {
	// The per-file floor must itself discriminate: an aggregate decls==0 check reads the same
	// over an empty file as over a clean one.
	if got := len(rvParse(t, "emptyDecls.go", "package p\n").Decls); got != 0 {
		t.Fatalf("the empty-file fixture has %d declaration(s); the per-file floor below would prove nothing", got)
	}
	if got := len(rvParse(t, "oneDecl.go", "package p\nvar x = 1\n").Decls); got == 0 {
		t.Fatal("the one-declaration fixture parses to zero declarations; the per-file floor below would prove nothing")
	}

	f := rvParse(t, "party.go", nil)
	if len(f.Decls) == 0 {
		t.Fatal("party.go parses to zero declarations; a file with nothing in it is not a clean one")
	}
	declaresPartyOrder := false
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "partyOrder" {
			declaresPartyOrder = true
		}
	}
	if !declaresPartyOrder {
		t.Fatal("party.go declares no func partyOrder; the scans below would report clean over the wrong file")
	}

	if rvHasMapType(f) {
		t.Error("party.go writes a map type; map iteration order is not deterministic and resolve.go's posture forbids one on this path")
	}
	if hits := ptGeometrySelectors(f); len(hits) > 0 {
		t.Errorf("party.go reads geometry %v; the partition is token order, and a boxless read carries arrival order and nothing else", hits)
	}

	// The needles are source strings, never committed files: proving the map scan works must
	// not commit a map.
	const mapNeedle = `package p

func f() {
	var m map[string]int
	_ = m
}
`
	const geomNeedle = `package p

func f(t struct{ Region struct{ X0 float64 } }) float64 {
	return t.Region.X0
}
`
	const sliceControl = `package p

func f() {
	var s []int
	for i := range s {
		_ = i
	}
}
`
	const textControl = `package p

func f(t struct{ Text string }) string {
	return t.Text
}
`
	if !rvHasMapType(rvParse(t, "mapNeedle.go", mapNeedle)) {
		t.Error("the needle source writes a map and the scan did not report it; the all-clear on party.go proves nothing")
	}
	if rvHasMapType(rvParse(t, "sliceControl.go", sliceControl)) {
		t.Error("the control source ranges a slice and the scan called it a map; the scan is not specific")
	}
	needled := ptGeometrySelectors(rvParse(t, "geomNeedle.go", geomNeedle))
	for _, want := range []string{"Region", "X0"} {
		if !slices.Contains(needled, want) {
			t.Errorf("the needle source reads .%s and the scan reported %v; the all-clear on party.go proves nothing", want, needled)
		}
	}
	if hits := ptGeometrySelectors(rvParse(t, "textControl.go", textControl)); len(hits) > 0 {
		t.Errorf("the control source reads .Text and the scan reported %v; the scan is not specific", hits)
	}
}

// The four pages with no interior: nothing, one token that is itself a heading, a heading last,
// and a token no pattern can match.
func TestPartyOrder_HoldsAtTheEdgesOfAPage(t *testing.T) {
	cases := []struct {
		name  string
		texts []string
		want  []Party
	}{
		{"an empty page", nil, []Party{}},
		{"one token, itself a heading", []string{"Buyer"}, []Party{PartyBuyer}},
		{"a heading as the very last token", []string{"Supplier", "Acme Ltd", "Buyer"}, []Party{PartySupplier, PartySupplier, PartyBuyer}},
		{"a token with empty text", []string{"", "Supplier", ""}, []Party{PartyUnknown, PartySupplier, PartySupplier}},
	}
	if len(cases) == 0 {
		t.Fatal("no cases; the loop below would check nothing")
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := partyOrder(ptPage(1, c.texts...))
			if len(got) != len(c.texts) {
				t.Fatalf("partyOrder returned %d entry/entries over %d token(s), want one per token", len(got), len(c.texts))
			}
			if got == nil {
				t.Error("partyOrder returned a nil slice; resolve.go's posture on this path is a non-nil empty one")
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("partyOrder = %v, want %v", ptNames(got), ptNames(c.want))
			}
		})
	}
}
