// vocabulary_scope_adversarial_test.go: the edge, negative and boundary coverage the
// vocabulary-widening specs did not carry -- the party suffix and the three advisory arms.
// Package extraction: alSpan, alSuffix, anchorLexicon and partyOrder are all unexported.
package extraction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// vsaVerbs is the four stems alSuffix was spliced behind.
var vsaVerbs = []string{"bill", "sold", "invoice", "deliver"}

// vsaTails is the enumerated set, plus the empty tail the trailing `?` admits.
var vsaTails = []string{"", "ed", "ing", "s", "d"}

// The set is closed at four tails and reaches exactly two entries. T-01.2 fences the suffix off
// four named entries only; nothing pinned the tail set itself, so a fifth tail or a splice into
// a fifth entry landed green.
func TestAnchorLexicon_TheSuffixIsClosedAtFourTailsOverTwoEntries(t *testing.T) {
	admitted := 0
	for _, verb := range vsaVerbs {
		for _, tail := range vsaTails {
			text := verb + tail + " to"
			admitted++
			if alSpan(text, "buyer_name") == nil {
				t.Errorf("%q: buyer_name span = nil, want a match; %q is in the enumerated set", text, tail)
			}
		}
	}
	if want := len(vsaVerbs) * len(vsaTails); admitted != want {
		t.Fatalf("checked %d admitted spelling(s), want %d", admitted, want)
	}

	// Everything outside the set, including a doubled tail. Without these the "closed" in the
	// const's own comment is an unpinned claim.
	rejected := 0
	for _, verb := range vsaVerbs {
		for _, tail := range []string{"es", "er", "ment", "ings", "eds", "ss", "ded", "eding"} {
			text := verb + tail + " to"
			rejected++
			if loc := alSpan(text, "buyer_name"); loc != nil && loc[0] == 0 {
				t.Errorf("%q: buyer_name matches at %v, want no match starting at offset 0; %q is outside the closed set", text, loc, tail)
			}
		}
	}
	if want := len(vsaVerbs) * 8; rejected != want {
		t.Fatalf("checked %d rejected spelling(s), want %d", rejected, want)
	}

	// AC-4 structurally: no third entry may carry the suffix.
	if len(anchorLexicon) == 0 {
		t.Fatal("anchorLexicon is empty; the scan below would range over nothing")
	}
	var carrying []string
	for _, e := range anchorLexicon {
		if strings.Contains(e.Pattern, alSuffix) {
			carrying = append(carrying, e.ID)
		}
	}
	if len(carrying) != 2 || carrying[0] != "buyer_tin" || carrying[1] != "buyer_name" {
		t.Errorf("alSuffix appears in %v, want exactly [buyer_tin buyer_name] in lexicon order", carrying)
	}
}

// The trailing \b still fires after "to" for every arm: "Billed total" is not a buyer label.
// The suffix moved where the match STARTS, so the far boundary needed re-proving.
func TestAnchorLexicon_TheTrailingBoundaryStillFiresAfterTo(t *testing.T) {
	checked := 0
	for _, verb := range vsaVerbs {
		for _, tail := range vsaTails {
			for _, suffix := range []string{"total", "tomorrow", "token", "top"} {
				text := verb + tail + " " + suffix
				checked++
				if loc := alSpan(text, "buyer_name"); loc != nil && loc[0] == 0 {
					t.Errorf("%q: buyer_name matches at %v, want no match starting at offset 0; the word is %q, not \"to\"", text, loc, suffix)
				}
			}
			// The paired positive: without it the negatives above would hold against a pattern
			// that had stopped matching anything.
			for _, ok := range []string{verb + tail + " to", verb + tail + " to:", verb + tail + " TO "} {
				if alSpan(ok, "buyer_name") == nil {
					t.Errorf("%q: buyer_name span = nil, want a match", ok)
				}
			}
		}
	}
	if want := len(vsaVerbs) * len(vsaTails) * 4; checked != want {
		t.Fatalf("checked %d negative(s), want %d", checked, want)
	}
}

// vsaExact is the shipped pattern for id with alSuffix removed -- the pre-widening spelling,
// derived from the shipped string rather than re-typed, so it cannot drift from it.
func vsaExact(t *testing.T, id string) string {
	t.Helper()
	for _, e := range anchorLexicon {
		if e.ID == id {
			out := strings.ReplaceAll(e.Pattern, alSuffix, "")
			if out == e.Pattern {
				t.Fatalf("%s: the shipped pattern does not contain alSuffix, so the comparison would be a tautology", id)
			}
			return out
		}
	}
	t.Fatalf("%s is not in anchorLexicon", id)
	return ""
}

// The widening adds no anchor to any committed fixture: on every token text in every docling
// golden, the widened buyer entries claim the same span the exact spelling did. Measured, not
// assumed -- the eleven arrangements are what every downstream accuracy number is scored on.
func TestAnchorLexicon_TheSuffixAddsNoAnchorOnTheCommittedFixtures(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("testdata", "*.docling.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(goldens) == 0 {
		t.Fatal("no docling golden found; every assertion below would range over nothing")
	}

	var texts []string
	for _, g := range goldens {
		raw, err := os.ReadFile(g)
		if err != nil {
			t.Fatalf("%s: %v", g, err)
		}
		var doc struct {
			Pages []struct {
				Tokens []struct{ Text string } `json:"tokens"`
			} `json:"pages"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", g, err)
		}
		for _, p := range doc.Pages {
			for _, tok := range p.Tokens {
				texts = append(texts, tok.Text)
			}
		}
	}
	if len(texts) == 0 {
		t.Fatal("the goldens carry no token text; every assertion below would range over nothing")
	}

	for _, id := range []string{"buyer_name", "buyer_tin"} {
		exact := regexp.MustCompile(vsaExact(t, id))
		matched := 0
		for _, text := range texts {
			got, want := alSpan(text, id), exact.FindStringIndex(text)
			if (got == nil) != (want == nil) || (got != nil && (got[0] != want[0] || got[1] != want[1])) {
				t.Errorf("%s on %q: widened span %v, exact span %v; the suffix changed a committed fixture's anchors", id, text, got, want)
			}
			if want != nil {
				matched++
			}
		}
		// Non-vacuity: an all-nil column would satisfy the equality above without exercising
		// either pattern.
		if matched == 0 {
			t.Errorf("%s matched none of the %d committed token text(s); the equality above proved nothing", id, len(texts))
		}
	}
}

// A suffixed heading opens a buyer block, so a bare TIN after it becomes the BUYER's. The
// control heading is one letter outside the closed set, which is what makes the move
// attributable to the widening rather than to the fixture.
func TestPartyOrder_ASuffixedBuyerHeadingMovesTheBareTIN(t *testing.T) {
	const supplierTIN, buyerTIN = "TIN: 99999999-2601", "TIN: 99999999-2602"

	for _, c := range []struct {
		heading string
		want    Party
	}{
		{"Bill to", PartyBuyer},
		{"Billed to", PartyBuyer},
		{"Billing to", PartyBuyer},
		{"Delivered to", PartyBuyer},
		// Outside the closed set: the block must NOT open, or the negatives above are luck.
		{"Billeding to", PartySupplier},
		{"Biller to", PartySupplier},
	} {
		page := ptPage(1, "Supplier", "Adeyemi Trading Limited", supplierTIN, c.heading, "Enugu Ceramics Limited", buyerTIN)
		order := partyOrder(page)
		if len(order) != len(page.Tokens) {
			t.Fatalf("%q: partyOrder returned %d part(ies) for %d token(s)", c.heading, len(order), len(page.Tokens))
		}
		// Index 5 is the second TIN, the one the heading is supposed to move.
		if got := order[5]; got != c.want {
			t.Errorf("%q: the bare TIN after the heading is %s, want %s", c.heading, ptName(got), ptName(c.want))
		}
		if got := partyField(order[5]); got != partyField(c.want) {
			t.Errorf("%q: the bare TIN routes to %q, want %q", c.heading, got, partyField(c.want))
		}
		// The supplier side never moves, whichever way the heading is read.
		if got := order[2]; got != PartySupplier {
			t.Errorf("%q: the supplier's own TIN is %s, want PartySupplier", c.heading, ptName(got))
		}
	}
}

// The routing above, end to end through Resolve: the buyer's TIN reaches buyer_tin only when
// the heading is admitted.
func TestResolve_ASuffixedBuyerHeadingRoutesTheBareTINToTheBuyer(t *testing.T) {
	page := func(heading string) []TokenPage {
		texts := []string{"Supplier: Adeyemi Trading Limited", "TIN: 99999999-2601", heading, "Enugu Ceramics Limited", "TIN: 99999999-2602"}
		toks := make([]Token, len(texts))
		for i, s := range texts {
			y := 0.10 + float64(i)*0.15
			toks[i] = Token{Text: s, Region: Region{Page: 1, X0: 0.10, Y0: y, X1: 0.45, Y1: y + 0.02}}
		}
		return []TokenPage{{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: toks}}
	}
	held := func(cands []Candidate, field, value string) bool {
		for _, c := range cands {
			if c.Field == field && c.Value == value {
				return true
			}
		}
		return false
	}

	const buyerTIN = "99999999-2602"
	rules := RuleSet{Tier1: Tier1Rules}

	admitted := Resolve(page("Billed to"), rules)
	if len(admitted) == 0 {
		t.Fatal("Resolve returned no candidate on the admitted page; every assertion below would range over nothing")
	}
	if !held(admitted, "buyer_tin", buyerTIN) {
		t.Errorf("the admitted heading left %s off buyer_tin; a suffixed heading must open the buyer block", buyerTIN)
	}

	// The control: one letter outside the closed set and the same TIN stays with the supplier.
	refused := Resolve(page("Billeding to"), rules)
	if held(refused, "buyer_tin", buyerTIN) {
		t.Errorf("the refused heading still routed %s to buyer_tin; the move is not attributable to the widening", buyerTIN)
	}
	if !held(refused, "supplier_tin", buyerTIN) {
		t.Errorf("the refused heading routed %s to neither party; PartyUnknown must fall back to supplier", buyerTIN)
	}
}

// A known gap, pinned so a future edit to alSuffix is a decision and not a surprise: the tail is
// concatenated onto the stem, so it admits "Invoiceing to" and misses the real English
// inflection "Invoicing to". Flip this test, do not delete it, if the set ever learns to elide.
func TestAnchorLexicon_TheClosedSetMissesTheElidingInflection(t *testing.T) {
	if loc := alSpan("Invoicing to", "buyer_name"); loc != nil {
		t.Errorf("%q: buyer_name matches at %v; alSuffix now elides the stem's final e -- update this pin and docs/extraction-corpus.md", "Invoicing to", loc)
	}
	if alSpan("Invoiceing to", "buyer_name") == nil {
		t.Error("\"Invoiceing to\": buyer_name span = nil, want a match; the tail is a blind concatenation onto the stem")
	}
}

// vsaAdvisoryArms is the three arms this story spliced in, each with the shipped pattern's
// pre-widening spelling. Derived from the shipped string, never re-typed, so a later edit to an
// entry cannot leave the "before" side stale.
var vsaAdvisoryArms = []struct{ id, arm, without string }{
	{"subtotal", `|taxable\s*amount`, ``},
	{"total", `amount\s*(due|payable)`, `amount\s*(due)`},
	{"issue_date", `issued|`, ``},
}

// vsaBefore is the entry's pattern with its advisory arm removed.
func vsaBefore(t *testing.T, id string) string {
	t.Helper()
	for _, a := range vsaAdvisoryArms {
		if a.id != id {
			continue
		}
		for _, e := range anchorLexicon {
			if e.ID != id {
				continue
			}
			if n := strings.Count(e.Pattern, a.arm); n != 1 {
				t.Fatalf("%s: the advisory arm %q appears %d time(s) in the shipped pattern %q, want exactly 1 -- the before-pattern below cannot be derived", id, a.arm, n, e.Pattern)
			}
			return strings.Replace(e.Pattern, a.arm, a.without, 1)
		}
	}
	t.Fatalf("%s is not an advisory-arm entry", id)
	return ""
}

// The three advisory arms are case- and whitespace-insensitive between their two words, and none
// reaches a one-word near miss. T-02.1/T-02.2 pin one canonical spelling apiece; a real page
// prints the label in whatever case its template chose.
func TestAnchorLexicon_TheAdvisoryArmsSurviveCasingAndSpacing(t *testing.T) {
	for _, c := range []struct{ text, owner string }{
		{"TAXABLE AMOUNT", "subtotal"},
		{"taxable amount", "subtotal"},
		{"Taxable  Amount", "subtotal"},
		{"Taxable\tamount", "subtotal"},
		{"Taxable amount (NGN)", "subtotal"},
		{"AMOUNT PAYABLE", "total"},
		{"amount payable", "total"},
		{"Amount  Payable", "total"},
		{"ISSUED", "issue_date"},
		{"issued", "issue_date"},
		{"Issued on", "issue_date"},
		{"Issued by", "issue_date"},
	} {
		loc := alSpan(c.text, c.owner)
		if loc == nil {
			t.Errorf("%q: %s span = nil, want a match", c.text, c.owner)
			continue
		}
		if loc[0] != 0 {
			t.Errorf("%q: %s matches at %v, want the label to start at offset 0", c.text, c.owner, loc)
		}
	}

	// One-word near misses. The arms are two words wide (or, for issued, one whole word), so a
	// stem or a fragment must claim nothing.
	for _, c := range []struct{ text, id string }{
		{"Taxable", "subtotal"},
		{"Taxation", "subtotal"},
		{"Amounts payable", "subtotal"},
		{"Payable", "total"},
		{"Balance payable", "total"},
		{"Amounts payable", "total"},
		{"Reissued", "issue_date"},
		{"Issuer", "issue_date"},
		{"Unissued", "issue_date"},
	} {
		if loc := alSpan(c.text, c.id); loc != nil {
			t.Errorf("%q: %s span = %v, want no match", c.text, c.id, loc)
		}
	}
}

// vsaScoredArrangements is the eleven layouts endtoend scores (endtoend/score_test.go's
// expectByLayout). Named rather than globbed: the question this test answers is about the
// scored corpus, and a glob would silently answer it about whatever else lands in testdata.
var vsaScoredArrangements = []string{
	"corpus_inline_labels", "corpus_split_labels", "corpus_stacked_labels",
	"corpus_two_column", "corpus_ambiguous_date", "corpus_totals_block",
	"wild_two_party_bare_tin", "wild_ruled_lines_totals", "wild_rc_due_naira",
	"wild_stacked_borderless", "wild_scanned_no_number",
}

// The three advisory arms reach no token on any scored arrangement: every widened entry claims
// the span its pre-widening spelling claimed. That is what makes the 58/88 and 44/44 floors
// evidence rather than coincidence, and it bounds the column-header defect
// TestResolve_ATaxableAmountColumnHeaderMintsASecondSubtotal characterises to a hand-built page.
func TestAnchorLexicon_TheAdvisoryArmsAddNoAnchorOnTheScoredArrangements(t *testing.T) {
	if len(vsaScoredArrangements) != 11 {
		t.Fatalf("%d arrangement(s) named, want the 11 endtoend scores", len(vsaScoredArrangements))
	}

	var texts []string
	for _, name := range vsaScoredArrangements {
		path := filepath.Join("testdata", name+".docling.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var doc struct {
			Pages []struct {
				Tokens []struct{ Text string } `json:"tokens"`
			} `json:"pages"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, p := range doc.Pages {
			for _, tok := range p.Tokens {
				texts = append(texts, tok.Text)
			}
		}
	}
	if len(texts) == 0 {
		t.Fatal("the scored arrangements carry no token text; every assertion below would range over nothing")
	}

	for _, a := range vsaAdvisoryArms {
		before := regexp.MustCompile(vsaBefore(t, a.id))
		matched := 0
		for _, text := range texts {
			got, want := alSpan(text, a.id), before.FindStringIndex(text)
			if (got == nil) != (want == nil) || (got != nil && (got[0] != want[0] || got[1] != want[1])) {
				t.Errorf("%s on %q: widened span %v, pre-widening span %v; the advisory arm changed a scored arrangement's anchors", a.id, text, got, want)
			}
			if want != nil {
				matched++
			}
		}
		// Non-vacuity: an all-nil column would satisfy the equality without exercising either
		// pattern.
		if matched == 0 {
			t.Errorf("%s matched none of the %d scored token text(s); the equality above proved nothing", a.id, len(texts))
		}
	}
}

// T-03.4: a leading From opens the supplier block. supplier_tin alone is not evidence of that
// -- it already reads 99999999-1301 today via partyField's PartyUnknown -> supplier fallback,
// with no heading recognised at all. Only partyOrder tells the two apart: 0 0 0 (the fallback)
// must move to 1 1 1 (a genuinely opened block).
func TestParty_ALeadingFromOpensASupplierBlock(t *testing.T) {
	page := TokenPage{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []Token{
		{Text: "From", Region: Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.12}},
		{Text: "Kaduna Advisory Partners", Region: Region{Page: 1, X0: 0.10, Y0: 0.13, X1: 0.40, Y1: 0.15}},
		{Text: "TIN 99999999-1301", Region: Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}},
	}}

	order := partyOrder(page)
	if len(order) != len(page.Tokens) {
		t.Fatalf("partyOrder returned %d part(ies) for %d token(s)", len(order), len(page.Tokens))
	}
	for i, p := range order {
		if p != PartySupplier {
			t.Errorf("token[%d] party = %s, want PartySupplier", i, ptName(p))
		}
	}

	cands := Resolve([]TokenPage{page}, RuleSet{Tier1: Tier1Rules})
	if len(cands) == 0 {
		t.Fatal("Resolve returned no candidate; every assertion below would range over nothing")
	}
	out := Reconcile(Input{Candidates: cands})

	name := riFieldResult(t, out, "supplier_name")
	if name.Reason != ReasonNone || name.Value == nil || *name.Value != "Kaduna Advisory Partners" {
		t.Errorf("supplier_name = %+v, want ReasonNone / %q", name, "Kaduna Advisory Partners")
	}
	tin := riFieldResult(t, out, "supplier_tin")
	if tin.Reason != ReasonNone || tin.Value == nil || *tin.Value != "99999999-1301" {
		t.Errorf("supplier_tin = %+v, want ReasonNone / %q", tin, "99999999-1301")
	}
}

// T-03.6: the end-to-end proof of the residual leading-from surface, characterised as a
// regression guard. Under a bare (unterminated) `^\s*from\b` this exact page yields
// supplier_name = "Lagos to Abuja" and loses buyer_tin entirely -- measured. The shipped
// (terminated) arm must never reach this token, so this stays green through the change.
func TestParty_ALeadingProseFromLeavesTheBuyerTINAlone(t *testing.T) {
	page := TokenPage{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []Token{
		{Text: "Billed to", Region: Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.12}},
		{Text: "Enugu Ceramics Limited", Region: Region{Page: 1, X0: 0.10, Y0: 0.13, X1: 0.32, Y1: 0.15}},
		{Text: "From Lagos to Abuja", Region: Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.35, Y1: 0.22}},
		{Text: "TIN 99999999-1302", Region: Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.30, Y1: 0.32}},
	}}

	order := partyOrder(page)
	if len(order) != len(page.Tokens) {
		t.Fatalf("partyOrder returned %d part(ies) for %d token(s)", len(order), len(page.Tokens))
	}
	if order[2] != PartyBuyer {
		t.Errorf("token[2] (%q) party = %s, want PartyBuyer -- no supplier block may open here", page.Tokens[2].Text, ptName(order[2]))
	}

	cands := Resolve([]TokenPage{page}, RuleSet{Tier1: Tier1Rules})
	if len(cands) == 0 {
		t.Fatal("Resolve returned no candidate; every assertion below would range over nothing")
	}
	for _, c := range cands {
		if c.Field == "supplier_name" {
			t.Errorf("supplier_name candidate = %+v, want none", c)
		}
	}

	out := Reconcile(Input{Candidates: cands})
	tin := riFieldResult(t, out, "buyer_tin")
	if tin.Reason != ReasonNone || tin.Value == nil || *tin.Value != "99999999-1302" {
		t.Errorf("buyer_tin = %+v, want ReasonNone / %q", tin, "99999999-1302")
	}
}
