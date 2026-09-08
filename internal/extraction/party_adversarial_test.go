// party_adversarial_test.go: the edges party_internal_test.go's eight AC specs leave open -- a
// both-parties token INSIDE a block, a heading that carries its own value, punctuation and case,
// a repeated heading, a long run, and whether partyOrder touches its input or remembers a call.
package extraction

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A both-parties token heads neither party, and heading neither is not the same as ending the
// block: partyHeading's PartyUnknown means "this token heads nothing", which is also what every
// ordinary token returns. If it reset `current`, every ordinary token would have to reset it too.
func TestPartyOrder_ABothPartiesTokenDoesNotEndTheBlock(t *testing.T) {
	const both = "Supplier / Buyer"

	// The floor: the token must genuinely match BOTH vocabularies, or this is only the
	// already-covered claim that a plain token does not end a block.
	if len(partyHeadings) != 2 {
		t.Fatalf("partyHeadings holds %d entry/entries, want 2; the both-match floor below would not cover both parties", len(partyHeadings))
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
		t.Fatalf("partyHeading(%q) = %s, want PartyUnknown; the premise of every case below is that this token heads neither party", both, ptName(got))
	}

	cases := []struct {
		name  string
		texts []string
		want  []Party
	}{
		{
			"mid-block: the buyer block survives it, and so does the TIN after it",
			[]string{"Buyer", "Honeywell Group", both, "99999999-0102"},
			[]Party{PartyBuyer, PartyBuyer, PartyBuyer, PartyBuyer},
		},
		{
			"before any heading: it opens no block, so the prefix stays unknown",
			[]string{both, "99999999-0101", "Buyer", "99999999-0102"},
			[]Party{PartyUnknown, PartyUnknown, PartyBuyer, PartyBuyer},
		},
		{
			// The control: a REAL heading does end the block, so the case above is not passing
			// on a partyOrder whose block never ends.
			"the control: a real heading after it still opens its own block",
			[]string{"Buyer", "Honeywell Group", both, "99999999-0102", "Supplier", "99999999-0101"},
			[]Party{PartyBuyer, PartyBuyer, PartyBuyer, PartyBuyer, PartySupplier, PartySupplier},
		},
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
			if !slices.Equal(got, c.want) {
				t.Errorf("partyOrder(%v) = %v, want %v", c.texts, ptNames(got), ptNames(c.want))
			}
		})
	}
}

// A heading belongs to itself even when its own text carries the value: the party word and the
// TIN share one token on every inline layout.
func TestPartyOrder_AHeadingCarryingItsOwnValueStillHeadsItsBlock(t *testing.T) {
	texts := []string{"Supplier TIN: 99999999-0101", "Acme Ltd", "Buyer TIN 99999999-0702", "Zeta Plc"}
	want := []Party{PartySupplier, PartySupplier, PartyBuyer, PartyBuyer}

	// The floor: both headings must carry a TIN, or this is the bare-heading case again.
	for _, i := range []int{0, 2} {
		if !strings.Contains(texts[i], "99999999-") {
			t.Fatalf("fixture token %d (%q) carries no TIN; the case under test is a heading that also carries a value", i, texts[i])
		}
	}
	if len(texts) != len(want) {
		t.Fatalf("the fixture carries %d token(s) against %d expected part(ies)", len(texts), len(want))
	}

	got := partyOrder(ptPage(1, texts...))
	if len(got) != len(texts) {
		t.Fatalf("partyOrder returned %d entry/entries over %d token(s), want one per token", len(got), len(texts))
	}
	if !slices.Equal(got, want) {
		t.Errorf("partyOrder = %v, want %v; the party word heads the block whatever else the token carries", ptNames(got), ptNames(want))
	}
}

// A second heading of the SAME party opens a block of that party, which is indistinguishable
// from continuing the first. This pins that it is not read as a reset or as a toggle.
func TestPartyOrder_ARepeatedHeadingOfOnePartyDoesNotDisturbTheBlock(t *testing.T) {
	texts := []string{"Supplier", "Acme Ltd", "Supplier", "TIN: 99999999-0101", "Buyer", "Zeta Plc"}
	want := []Party{PartySupplier, PartySupplier, PartySupplier, PartySupplier, PartyBuyer, PartyBuyer}

	if len(texts) != len(want) {
		t.Fatalf("the fixture carries %d token(s) against %d expected part(ies)", len(texts), len(want))
	}
	got := partyOrder(ptPage(1, texts...))
	if len(got) != len(texts) {
		t.Fatalf("partyOrder returned %d entry/entries over %d token(s), want one per token", len(got), len(texts))
	}
	if !slices.Equal(got, want) {
		t.Errorf("partyOrder = %v, want %v; a repeat opens a block of the same party, it does not reset or toggle one", ptNames(got), ptNames(want))
	}
}

// The heading vocabulary is the lexicon's, so punctuation, case and inner spacing are the
// lexicon's business. "Vendor / Seller" is the load-bearing row: two alternatives of ONE matcher
// fire, and partyHeading counts MATCHERS, so it stays a supplier heading.
func TestPartyHeading_ReadsThroughPunctuationCaseAndSpacing(t *testing.T) {
	cases := []struct {
		text string
		want Party
	}{
		{"Bill To:", PartyBuyer},
		{"  BILL  TO  ", PartyBuyer},
		{"(Buyer)", PartyBuyer},
		{"buyer:", PartyBuyer},
		{"Sold  To", PartyBuyer},
		// The delivery/billing phrases head the buyer's block too, and no other spec fails here
		// if one of them stops doing so.
		{"Invoice to", PartyBuyer},
		{"Deliver to", PartyBuyer},
		{"SUPPLIER", PartySupplier},
		{"Vendor / Seller", PartySupplier},
		{"\tSupplier\n", PartySupplier},
		// Negatives: \b is a word boundary, so a party word inside a longer word is not a heading.
		{"Suppliers", PartyUnknown},
		{"clientele", PartyUnknown},
		{"Acme Ltd", PartyUnknown},
		{"99999999-0101", PartyUnknown},
	}
	if len(cases) == 0 {
		t.Fatal("no cases; the loop below would check nothing")
	}

	var sawParty, sawUnknown bool
	for _, c := range cases {
		got := partyHeading(c.text)
		if got != c.want {
			t.Errorf("partyHeading(%q) = %s, want %s", c.text, ptName(got), ptName(c.want))
		}
		if c.want == PartyUnknown {
			sawUnknown = true
		} else {
			sawParty = true
		}
	}
	if !sawParty || !sawUnknown {
		t.Error("the table carries only one side; a table of all-positives or all-negatives passes for a partyHeading that answers one value to everything")
	}
}

// partyOrderLongRun is long enough that a per-token recompile or a quadratic rescan would show,
// and long enough that "the whole page is one value" is not the shape of a two-token fixture.
const partyOrderLongRun = 5000

func TestPartyOrder_ALongRunInheritsTheHeadingBeforeIt(t *testing.T) {
	texts := make([]string, 0, partyOrderLongRun+2)
	texts = append(texts, "Buyer")
	for i := range partyOrderLongRun {
		texts = append(texts, "99999999-"+strconv.Itoa(1000+i%9000))
	}
	const supplierAt = partyOrderLongRun/2 + 1
	texts = slices.Insert(texts, supplierAt, "Supplier")

	if len(texts) != partyOrderLongRun+2 {
		t.Fatalf("the fixture carries %d token(s), want %d", len(texts), partyOrderLongRun+2)
	}
	if texts[supplierAt] != "Supplier" {
		t.Fatalf("the fixture's supplier heading sits at index %d as %q, want %q", supplierAt, texts[supplierAt], "Supplier")
	}

	got := partyOrder(ptPage(1, texts...))
	if len(got) != len(texts) {
		t.Fatalf("partyOrder returned %d entry/entries over %d token(s), want one per token", len(got), len(texts))
	}
	for i, p := range got {
		want := PartyBuyer
		if i >= supplierAt {
			want = PartySupplier
		}
		if p != want {
			t.Fatalf("partyOrder()[%d] (%q) = %s, want %s; the two headings are the only boundaries on a %d-token page", i, texts[i], ptName(p), ptName(want), len(texts))
		}
	}
}

// partyOrder is a read. Its caller hands it the same TokenPage the resolver is still walking,
// so it must neither edit the page nor carry anything into the next call.
func TestPartyOrder_TouchesNoInputAndRemembersNoCall(t *testing.T) {
	headed := ptPage(1, "Buyer", "Zeta Plc", "99999999-0702")
	headed.Tokens[1].Region = Region{Page: 1, X0: 0.1, Y0: 0.2, X1: 0.4, Y1: 0.3}
	before := slices.Clone(headed.Tokens)
	if len(before) == 0 {
		t.Fatal("the fixture page carries no token; every assertion below would compare nothing")
	}

	first := partyOrder(headed)
	if !slices.Equal(first, []Party{PartyBuyer, PartyBuyer, PartyBuyer}) {
		t.Fatalf("partyOrder(headed) = %v, want all PartyBuyer; the carry-over check below needs a call that ends inside a block", ptNames(first))
	}
	if !slices.Equal(headed.Tokens, before) {
		t.Errorf("partyOrder edited its input page: %+v, want %+v", headed.Tokens, before)
	}

	// The next call must not inherit the buyer block the call above ended inside.
	bare := []string{"TIN:", "99999999-0101"}
	second := partyOrder(ptPage(2, bare...))
	if len(second) != len(bare) {
		t.Fatalf("partyOrder returned %d entry/entries over %d token(s), want one per token", len(second), len(bare))
	}
	for i, p := range second {
		if p != PartyUnknown {
			t.Errorf("partyOrder()[%d] (%q) = %s after a call that ended in a buyer block, want PartyUnknown; nothing crosses a call", i, bare[i], ptName(p))
		}
	}

	// And the first page still answers the same, so the interleaving above changed nothing.
	if again := partyOrder(headed); !slices.Equal(again, first) {
		t.Errorf("partyOrder(headed) = %v on the repeat, %v the first time; the call is not a pure function of its page", ptNames(again), ptNames(first))
	}
}
