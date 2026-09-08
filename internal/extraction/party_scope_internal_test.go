// party_scope_internal_test.go: partyField's own totality, and the suppression the two new
// buyer phrases must earn. Internal package: partyField and anchorOutranked are unexported.
//
// Reads no fixture: an internal test that reads one builds the pdfium pool and fatals
// TestPDFiumPool_NotBuiltOnACancelledContext by file-name order alone.
package extraction

import (
	"slices"
	"testing"
)

// partyField is total: every Party, including one no constant names yet, reaches a shipped TIN
// field. A fourth Party added without a case here would otherwise route to whatever the last
// branch happens to be, silently.
func TestPartyField_IsTotalOverEveryParty(t *testing.T) {
	cases := []struct {
		party Party
		want  string
	}{
		{PartyUnknown, "supplier_tin"},
		{PartySupplier, "supplier_tin"},
		{PartyBuyer, "buyer_tin"},
		{Party(99), "supplier_tin"}, // no constant names this one; the fallback must still hold
	}
	if len(cases) == 0 {
		t.Fatal("no cases; the loop below would check nothing")
	}

	for _, c := range cases {
		got := partyField(c.party)
		if got != c.want {
			t.Errorf("partyField(%d) = %q, want %q", int(c.party), got, c.want)
		}
		if !slices.Contains(HeaderFields, got) {
			t.Errorf("partyField(%d) = %q, which is not in HeaderFields; a candidate on it reaches no invoices column", int(c.party), got)
		}
	}
}

// buyer_tin carries "invoice to" and "deliver to" as well as the narrow party words. The
// shipped suppression spec exercises only "Supplier TIN:" and "Buyer TIN ", so those two
// phrases have no containment assertion there: on an inline party-bearing token the wider
// entry must claim the strictly wider span and anchorOutranked must drop bare_tin.
func TestAnchorLexicon_TheWidenedBuyerPhrasesOutrankTheBareTINLabel(t *testing.T) {
	for _, text := range []string{
		"Invoice to TIN: 99999999-0402",
		"Deliver to Tax ID 99999999-0402",
	} {
		inner, outer := alSpan(text, "bare_tin"), alSpan(text, "buyer_tin")
		if inner == nil || outer == nil {
			t.Errorf("%q: bare_tin span %v, buyer_tin span %v; both must match or the containment below compares nothing", text, inner, outer)
			continue
		}
		if !(outer[0] <= inner[0] && outer[1] >= inner[1] && outer[1]-outer[0] > inner[1]-inner[0]) {
			t.Errorf("%q: bare_tin claims %v and buyer_tin %v; the widened buyer entry must claim the STRICTLY wider span", text, inner, outer)
		}
		if !anchorOutranked(text, inner) {
			t.Errorf("%q: anchorOutranked left the bare_tin span %v standing; the party-less label would anchor a rule on a token the buyer owns", text, inner)
		}
	}

	// The control: on the bare heading there is no TIN label at all, so nothing here rests on
	// bare_tin matching every token that carries the phrase.
	const heading = "Invoice to"
	if loc := alSpan(heading, "bare_tin"); loc != nil {
		t.Errorf("%q: bare_tin claims %v, want no match; the phrase carries no TIN label", heading, loc)
	}
}
