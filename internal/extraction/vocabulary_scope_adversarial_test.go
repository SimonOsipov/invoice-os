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

// --- the from arm ------------------------------------------------------------------------

// vsaFromArm is the alternation branch supplier_name gained, derived from the shipped entry so
// the before-pattern below cannot go stale, and vsaFromOnly is that branch alone.
const (
	vsaFromArm  = `|^\s*from\b\s*(?:[:.\-–—]|$)`
	vsaFromOnly = `(?i)^\s*from\b\s*(?:[:.\-–—]|$)`
)

// vsaSupplierBefore is supplier_name's pattern with the from arm removed.
func vsaSupplierBefore(t *testing.T) string {
	t.Helper()
	for _, e := range anchorLexicon {
		if e.ID != "supplier_name" {
			continue
		}
		if n := strings.Count(e.Pattern, vsaFromArm); n != 1 {
			t.Fatalf("the from arm %q appears %d time(s) in %q, want exactly 1; the before-pattern cannot be derived", vsaFromArm, n, e.Pattern)
		}
		return strings.Replace(e.Pattern, vsaFromArm, "", 1)
	}
	t.Fatal("anchorLexicon carries no supplier_name entry")
	return ""
}

// vsaSameSpan reports whether two FindStringIndex results agree, nil included.
func vsaSameSpan(a, b []int) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || (a[0] == b[0] && a[1] == b[1])
}

// vsaStrings is a generated probe set: three vocabulary slots joined by one separator, plus a
// two-slot set behind five leading prefixes. Wide enough that a silent change to the
// supplier/seller/vendor arm cannot hide in it.
func vsaStrings() []string {
	words := []string{
		"supplier", "Seller", "VENDOR", "vendors", "subsupplier",
		"from", "From", "FROM", "freight", "fromage",
		"buyer", "Lagos", "invoice", "", "-", ":", "—",
	}
	seps := []string{"", " ", "  ", ":", ".", "\t", "\n", "-"}
	out := make([]string, 0, 45000)
	for _, a := range words {
		for _, b := range words {
			for _, c := range words {
				for _, s := range seps {
					out = append(out, a+s+b+" "+c)
				}
			}
		}
	}
	for _, pre := range []string{"", " ", "  ", "\t", "\n"} {
		for _, a := range words {
			for _, b := range words {
				out = append(out, pre+a+" "+b, pre+a+b)
			}
		}
	}
	return out
}

// The alternation was restructured -- the outer \b pair moved inside the first arm -- so the
// supplier/seller/vendor reading could have shifted without any from spec noticing, since every
// one of them is about from. Each difference from the pre-arm spelling has to be the from arm's,
// and the entry minus its arm has to read exactly as the un-parenthesised original did.
func TestAnchorLexicon_TheFromArmIsTheOnlyThingSupplierNameGained(t *testing.T) {
	const original = `(?i)\b(supplier|seller|vendor)\b`

	before := regexp.MustCompile(vsaSupplierBefore(t))
	fromArm := regexp.MustCompile(vsaFromOnly)
	orig := regexp.MustCompile(original)

	set := vsaStrings()
	if len(set) == 0 {
		t.Fatal("the probe set is empty; every count below would be zero for the wrong reason")
	}

	var differ, added, lost, shifted, attributed, reparenthesised, beforeMatched int
	for _, s := range set {
		b, n := before.FindStringIndex(s), alSpan(s, "supplier_name")
		if b != nil {
			beforeMatched++
		}
		// The parenthesisation itself must be inert: dropping the from arm has to leave the
		// original spelling's exact span.
		if o := orig.FindStringIndex(s); !vsaSameSpan(o, b) {
			reparenthesised++
			if reparenthesised <= 3 {
				t.Errorf("%q: %q spans %v, the shipped entry minus its from arm spans %v; the rewrite moved the supplier/seller/vendor reading", s, original, o, b)
			}
		}
		if vsaSameSpan(b, n) {
			continue
		}
		differ++
		switch {
		case b == nil:
			added++
		case n == nil:
			lost++
			if lost <= 3 {
				t.Errorf("%q: matched %v before the from arm and nothing after it; the from arm may only add", s, b)
			}
		default:
			shifted++
		}
		if f := fromArm.FindStringIndex(s); vsaSameSpan(f, n) {
			attributed++
		} else if differ-attributed <= 3 {
			t.Errorf("%q: span moved from %v to %v and the from arm spans %v; the difference is not the from arm's", s, b, n, f)
		}
	}

	// Non-vacuity on both sides: an all-nil column, or a set no from ever leads, satisfies the
	// equalities above without exercising either pattern.
	if beforeMatched == 0 {
		t.Errorf("the pre-arm pattern matched none of the %d probe(s); the equality above proved nothing", len(set))
	}
	if added == 0 {
		t.Errorf("the from arm added no match over %d probe(s); the attribution above proved nothing", len(set))
	}
	if differ != attributed {
		t.Errorf("%d probe(s) differ and %d are the from arm's; %d are unaccounted for", differ, attributed, differ-attributed)
	}
	t.Logf("probes=%d differ=%d added=%d lost=%d shifted=%d attributed=%d", len(set), differ, added, lost, shifted, attributed)
}

// The ^ carries the guard on its own. Nothing pinned it: every prose row the story listed puts a
// bare word after its from, which the terminator refuses anyway, so the anchor could be deleted
// and the suite stay green. These rows put a real terminator behind a mid-sentence from, where
// only the ^ refuses them.
func TestAnchorLexicon_AMidSentenceFromWithATerminatorNamesNobody(t *testing.T) {
	for _, text := range []string{
		"Period from:",
		"Valid from:",
		"Amount received from:",
		"Balance carried forward from.",
		"Invoice period from —",
		"Goods shipped from-",
		"Discount applied from",
	} {
		if loc := alSpan(text, "supplier_name"); loc != nil {
			t.Errorf("%q: supplier_name span = %v, want no match; only the leading anchor refuses this row", text, loc)
		}
	}

	// The paired positive: the same terminators at the start of a token are admitted, so the
	// refusals above are the anchor's and not the terminator set's.
	for _, text := range []string{"From:", "From.", "From —", "From-"} {
		if loc := alSpan(text, "supplier_name"); loc == nil || loc[0] != 0 {
			t.Errorf("%q: supplier_name span = %v, want a match starting at 0", text, loc)
		}
	}
}

// The terminator set, pinned in both directions. Its members are five printable separators plus
// end-of-token, and ASCII whitespace may precede them. Everything else refuses -- including a
// non-breaking space, which Go's \s does not cover, so a PDF that prints "From<NBSP>:" reads no
// supplier at all.
func TestAnchorLexicon_TheFromArmsTerminatorSet(t *testing.T) {
	admit := []string{
		"From", "From:", "From.", "From-", "From–", "From—",
		"From ", "From\t", "From\n", "From\r\n", "From  :", "From :",
		"From:Kaduna", "From—Kaduna", "From. Kaduna Advisory Partners",
		"  From:", "\nFrom:", "  \n From:",
	}
	refuse := []string{
		"From;", "From,", "From =", "From_", "From/", "From|", "From*", "From#",
		"From)", "From]", "From'", `From"`, "From·", "From ", "From :",
		"FromKaduna", "Fromm:", "From Kaduna Advisory Partners", "From to",
	}
	if len(admit) == 0 || len(refuse) == 0 {
		t.Fatal("a side of the table is empty; the assertions below would range over nothing")
	}
	for _, text := range admit {
		if loc := alSpan(text, "supplier_name"); loc == nil || loc[0] != 0 {
			t.Errorf("%q: supplier_name span = %v, want a match starting at 0", text, loc)
		}
	}
	for _, text := range refuse {
		if loc := alSpan(text, "supplier_name"); loc != nil {
			t.Errorf("%q: supplier_name span = %v, want no match", text, loc)
		}
	}

	// $ is end-of-TEXT, not end-of-line: a token holding two lines refuses, so a stacked
	// "From\nKaduna" block cannot be read as a bare From heading.
	if loc := alSpan("From\nKaduna Advisory Partners", "supplier_name"); loc != nil {
		t.Errorf("a two-line token spans %v, want no match; the arm carries no (?m) flag", loc)
	}
}

// vsaPage stacks texts one under another on a single page.
func vsaPage(texts ...string) TokenPage {
	page := TokenPage{Number: 1, WidthPt: 612, HeightPt: 792}
	for i, s := range texts {
		y := 0.10 + float64(i)*0.05
		page.Tokens = append(page.Tokens, Token{Text: s, Region: Region{Page: 1, X0: 0.10, Y0: y, X1: 0.45, Y1: y + 0.02}})
	}
	return page
}

// vsaOrder is partyOrder with its per-token count checked.
func vsaOrder(t *testing.T, page TokenPage) []Party {
	t.Helper()
	order := partyOrder(page)
	if len(order) != len(page.Tokens) {
		t.Fatalf("partyOrder returned %d part(ies) for %d token(s)", len(order), len(page.Tokens))
	}
	return order
}

// The terminator's cost in the other direction, characterised rather than left silent: a date
// range printed "From: <date>" or "From – To" is a leading, terminated from, so it opens a
// supplier block and names a date as the supplier. The uncolonned refusal
// (TestAnchorLexicon_AnUncolonnedFromNamesNoSupplier) is the same trade seen from the other end.
func TestParty_ATerminatedFromOnADateRangeNamesTheDate(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"From: 01 Aug 2026", "01 Aug 2026"},
		{"From – To", "To"},
	} {
		page := vsaPage(c.text)
		if order := vsaOrder(t, page); order[0] != PartySupplier {
			t.Errorf("%q party = %s, want PartySupplier", c.text, ptName(order[0]))
		}
		cands := Resolve([]TokenPage{page}, RuleSet{Tier1: Tier1Rules})
		if len(cands) == 0 {
			t.Fatalf("%q: Resolve returned no candidate; the assertion below would range over nothing", c.text)
		}
		out := Reconcile(Input{Candidates: cands})
		name := riFieldResult(t, out, "supplier_name")
		if name.Value == nil || *name.Value != c.want {
			t.Errorf("%q: supplier_name = %+v, want %q -- the residual surface the terminator admits", c.text, name, c.want)
		}
	}
}

// A From heading behind the buyer's block opens a supplier block mid-page and strands nothing:
// both names and both TINs still route. The mirror of
// TestParty_ALeadingFromOpensASupplierBlock, whose From leads the page.
func TestParty_AFromHeadingBehindTheBuyerBlockOpensASupplierBlock(t *testing.T) {
	page := vsaPage(
		"Billed to", "Enugu Ceramics Limited", "TIN 99999999-1302",
		"From", "Kaduna Advisory Partners", "TIN 99999999-1301",
	)
	order := vsaOrder(t, page)
	for i, want := range []Party{PartyBuyer, PartyBuyer, PartyBuyer, PartySupplier, PartySupplier, PartySupplier} {
		if order[i] != want {
			t.Errorf("token[%d] (%q) party = %s, want %s", i, page.Tokens[i].Text, ptName(order[i]), ptName(want))
		}
	}

	cands := Resolve([]TokenPage{page}, RuleSet{Tier1: Tier1Rules})
	if len(cands) == 0 {
		t.Fatal("Resolve returned no candidate; every assertion below would range over nothing")
	}
	out := Reconcile(Input{Candidates: cands})
	for _, c := range []struct{ field, want string }{
		{"buyer_name", "Enugu Ceramics Limited"},
		{"buyer_tin", "99999999-1302"},
		{"supplier_name", "Kaduna Advisory Partners"},
		{"supplier_tin", "99999999-1301"},
	} {
		got := riFieldResult(t, out, c.field)
		if got.Reason != ReasonNone || got.Value == nil || *got.Value != c.want {
			t.Errorf("%s = %+v, want ReasonNone / %q", c.field, got, c.want)
		}
	}
}

// Two real From headings: the nearer one takes the block and the first block's name is dropped
// without a trace -- no alternative, no doubt. Characterised, not endorsed; partyOrder keeps
// only the nearest heading by construction.
func TestParty_ASecondFromHeadingTakesTheBlockFromTheFirst(t *testing.T) {
	page := vsaPage("From", "Alpha Ltd", "From:", "Beta Ltd", "TIN 99999999-1301")
	for i, p := range vsaOrder(t, page) {
		if p != PartySupplier {
			t.Errorf("token[%d] (%q) party = %s, want PartySupplier", i, page.Tokens[i].Text, ptName(p))
		}
	}

	cands := Resolve([]TokenPage{page}, RuleSet{Tier1: Tier1Rules})
	if len(cands) == 0 {
		t.Fatal("Resolve returned no candidate; every assertion below would range over nothing")
	}
	out := Reconcile(Input{Candidates: cands})
	name := riFieldResult(t, out, "supplier_name")
	if name.Reason != ReasonNone || name.Value == nil || *name.Value != "Beta Ltd" {
		t.Errorf("supplier_name = %+v, want ReasonNone / %q", name, "Beta Ltd")
	}
	if len(name.Alternatives) != 0 {
		t.Errorf("supplier_name alternatives = %+v, want none; the first block's name is dropped silently", name.Alternatives)
	}
	tin := riFieldResult(t, out, "supplier_tin")
	if tin.Reason != ReasonNone || tin.Value == nil || *tin.Value != "99999999-1301" {
		t.Errorf("supplier_tin = %+v, want ReasonNone / %q", tin, "99999999-1301")
	}
}

// A flattened two-column header names both parties in one token. Before the from arm such a
// token headed the buyer; now both vocabularies match it, so partyHeading's "names both parties,
// names neither" rule takes over and no block opens at all.
func TestParty_AFromTokenThatAlsoNamesTheBuyerHeadsNeither(t *testing.T) {
	for _, text := range []string{"From: Billed to", "From — Invoice to", "From. Deliver to"} {
		if alSpan(text, "supplier_name") == nil || alSpan(text, "buyer_name") == nil {
			t.Fatalf("%q does not match both party vocabularies; the rule under test is not reached", text)
		}
		if p := partyHeading(text); p != PartyUnknown {
			t.Errorf("%q heads %s, want PartyUnknown -- a heading naming both parties names neither", text, ptName(p))
		}
	}

	// The control: with the from arm out of reach the same trailing phrase still heads the
	// buyer, so the refusals above are the collision's and not the phrase's.
	if p := partyHeading("Invoice to: From"); p != PartyBuyer {
		t.Errorf("%q heads %s, want PartyBuyer", "Invoice to: From", ptName(p))
	}
}

// A From heading whose only neighbour is another label names nobody: crossesALabel refuses the
// pair. Without it a bare heading column would read the next label as the supplier's name.
func TestParty_AFromHeadingWhoseValueIsALabelNamesNoSupplier(t *testing.T) {
	page := vsaPage("From:", "Invoice No")
	if p := partyHeading(page.Tokens[0].Text); p != PartySupplier {
		t.Fatalf("%q heads %s, want PartySupplier; the refusal below would not be crossesALabel's", page.Tokens[0].Text, ptName(p))
	}
	for _, c := range Resolve([]TokenPage{page}, RuleSet{Tier1: Tier1Rules}) {
		if c.Field == "supplier_name" {
			t.Errorf("supplier_name candidate = %+v, want none", c)
		}
	}

	// The control: the same heading over an ordinary name does read it, so the refusal above is
	// the label's and not the layout's.
	found := false
	for _, c := range Resolve([]TokenPage{vsaPage("From:", "Kaduna Advisory Partners")}, RuleSet{Tier1: Tier1Rules}) {
		if c.Field == "supplier_name" && c.Value == "Kaduna Advisory Partners" {
			found = true
		}
	}
	if !found {
		t.Error("the control page minted no supplier_name candidate; the refusal above proves nothing")
	}
}

// A From alone on a page opens a block with nothing in it: the boundary is drawn and every
// party field stays missing.
func TestParty_AFromAloneOnAPageNamesNobody(t *testing.T) {
	page := vsaPage("From")
	if order := vsaOrder(t, page); order[0] != PartySupplier {
		t.Errorf("token[0] party = %s, want PartySupplier", ptName(order[0]))
	}
	out := Reconcile(Input{Candidates: Resolve([]TokenPage{page}, RuleSet{Tier1: Tier1Rules})})
	if len(out) == 0 {
		t.Fatal("Reconcile returned no field; the assertions below would range over nothing")
	}
	for _, field := range []string{"supplier_name", "supplier_tin", "buyer_name", "buyer_tin"} {
		if got := riFieldResult(t, out, field); got.Value != nil {
			t.Errorf("%s = %q, want nil; the page carries nothing but the heading", field, *got.Value)
		}
	}
}

// The from arm reaches no token on any committed arrangement: every one claims the span it
// claimed before the arm. That is what proves wild_two_party_bare_tin's PartyUnknown -> supplier
// fallback undisturbed by measurement rather than by inspection.
func TestAnchorLexicon_TheFromArmAddsNoAnchorOnTheCommittedArrangements(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*.docling.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < len(vsaScoredArrangements) {
		t.Fatalf("%d docling fixture(s) found, want at least the %d scored arrangements", len(paths), len(vsaScoredArrangements))
	}

	before := regexp.MustCompile(vsaSupplierBefore(t))
	var texts []string
	for _, path := range paths {
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
		t.Fatal("the committed arrangements carry no token text; every assertion below would range over nothing")
	}

	matched := 0
	for _, text := range texts {
		got, want := alSpan(text, "supplier_name"), before.FindStringIndex(text)
		if !vsaSameSpan(got, want) {
			t.Errorf("supplier_name on %q: span %v, pre-arm span %v; the from arm changed a committed arrangement's anchors", text, got, want)
		}
		if want != nil {
			matched++
		}
	}
	// Non-vacuity: an all-nil column satisfies the equality without exercising either pattern.
	if matched == 0 {
		t.Errorf("supplier_name matched none of the %d committed token text(s); the equality above proved nothing", len(texts))
	}
}

// The ceiling above supplier_name's entry, proved rather than asserted. A From token becomes an
// AnchorObservation carrying the matched text, and learnedLabel rebuilds it word-bounded with no
// leading anchor -- so the learned rule reaches a mid-sentence from the entry itself refuses.
func TestLearn_ALearnedFromLabelLosesTheLeadingGuard(t *testing.T) {
	obs := AnchorObservations([]TokenPage{vsaPage("From")})
	if len(obs) == 0 {
		t.Fatal("AnchorObservations returned nothing for a From token; every assertion below would range over nothing")
	}
	var text string
	for _, o := range obs {
		if o.Label == "supplier_name" {
			text = o.Text
		}
	}
	if text != "From" {
		t.Fatalf("the supplier_name observation carries %q, want %q", text, "From")
	}

	label, ok := learnedLabel(text)
	if !ok {
		t.Fatal("learnedLabel refused the observed text")
	}
	if label != `(?i)\bFrom\b` {
		t.Errorf("learnedLabel(%q) = %q, want %q", text, label, `(?i)\bFrom\b`)
	}
	re, err := regexp.Compile(label)
	if err != nil {
		t.Fatalf("compile %q: %v", label, err)
	}
	const prose = "Balance carried forward from previous invoice"
	if !re.MatchString(prose) {
		t.Errorf("the learned label %q refuses %q; the ceiling names a reach that is not there", label, prose)
	}
	// The control: the entry itself still refuses the same prose, so the reach above is the
	// learned rule's alone.
	if loc := alSpan(prose, "supplier_name"); loc != nil {
		t.Errorf("supplier_name spans %v on %q; the guard the ceiling says is lost was never there", loc, prose)
	}
}
