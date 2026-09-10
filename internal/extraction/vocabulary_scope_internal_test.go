// vocabulary_scope_internal_test.go: EXTR-26-01, T-01.1/T-01.2/T-01.3/T-01.5/T-01.6. Package
// extraction: every spec reads a span through alSpan (anchor_internal_test.go) or calls
// anchorOutranked, both unexported.
package extraction

import "testing"

// T-01.1: alSuffix lets a two-word party label take one of four enumerated tails between its
// words. RED today: `bill\s*to` needs "bill" then whitespace then "to", and in "Billed to" the
// next char is "e" -- no match at all.
func TestAnchorLexicon_ATwoWordPartyLabelTakesAnEnumeratedSuffix(t *testing.T) {
	for _, c := range []struct {
		text string
		loc  []int
	}{
		{"Billed to", []int{0, 9}},
		{"Billed To:", []int{0, 9}},
		{"Billing to", []int{0, 10}},
		{"Bills to", []int{0, 8}},
		{"Delivered to", []int{0, 12}},
		{"Invoiced to", []int{0, 11}},
	} {
		loc := alSpan(c.text, "buyer_name")
		if loc == nil {
			t.Errorf("%q: buyer_name span = nil, want %v", c.text, c.loc)
			continue
		}
		if loc[0] != c.loc[0] || loc[1] != c.loc[1] {
			t.Errorf("%q: buyer_name span = %v, want %v", c.text, loc, c.loc)
		}
	}
}

// T-01.2: the suffix must not reach the four uninflected two-word entries it was never meant
// for. Passes today -- 0 of 16 spellings match at offset 0 -- and it is the fence that reds on
// all 16 the moment someone generalises alSuffix into one of these patterns.
//
// Not `loc == nil`: total's bare "total" arm and issue_date's bare "date" arm already match the
// trailing noun of half these spellings at a non-zero offset, for a reason unrelated to
// alSuffix (.ralph/arch-26-01.md). Only "no match starts at offset 0" isolates the two-word arm.
func TestAnchorLexicon_TheSuffixDoesNotReachTheUninflectedEntries(t *testing.T) {
	tails := []string{"ed", "ing", "s", "d"}
	cases := []struct{ id, first, second string }{
		{"subtotal", "sub", "total"},
		{"total", "grand", "total"},
		{"total", "amount", "due"},
		{"issue_date", "invoice", "date"},
	}

	n := 0
	for _, c := range cases {
		for _, tail := range tails {
			n++
			text := c.first + tail + " " + c.second
			if loc := alSpan(text, c.id); loc != nil && loc[0] == 0 {
				t.Errorf("%s: %q matches at %v, want no match starting at offset 0 -- the two-word arm fired on a spelling alSuffix must not reach", c.id, text, loc)
			}
		}
	}
	if n != 16 {
		t.Fatalf("checked %d spelling(s), want 16", n)
	}
}

// T-01.3: the paired positive for T-01.1 -- without it, T-01.1 would pass against a pattern
// loosened to match everything.
func TestAnchorLexicon_TheOriginalPartySpellingsStillMatch(t *testing.T) {
	for _, text := range []string{"Bill To", "Sold to", "Invoice to", "Deliver to", "Buyer"} {
		if alSpan(text, "buyer_name") == nil {
			t.Errorf("%q: buyer_name span = nil, want a match", text)
		}
	}
	if loc := alSpan("Supplier", "buyer_name"); loc != nil {
		t.Errorf("%q: buyer_name span = %v, want no match", "Supplier", loc)
	}
}

// T-01.5: the loosened buyer_name/buyer_tin must still outrank the party-less bare_tin. RED
// today: buyer_tin does not match this token at all, leaving bare_tin unopposed.
func TestAnchorLexicon_TheLoosenedBuyerLabelStillOutranksTheBareTIN(t *testing.T) {
	const text = "Billed to TIN: 99999999-1302"

	// bare_tin's own \s* prefix consumes nothing at this offset, so the span keeps its leading
	// space -- width 4. "TIN" alone would be width 3 and is the wrong oracle here.
	bare := alSpan(text, "bare_tin")
	if want := []int{9, 13}; bare == nil || bare[0] != want[0] || bare[1] != want[1] {
		t.Fatalf("bare_tin span = %v, want %v", bare, want)
	}
	if !anchorOutranked(text, bare) {
		t.Errorf("anchorOutranked(%q, %v) = false, want true: buyer_tin must claim a strictly wider span", text, bare)
	}

	tin := alSpan(text, "buyer_tin")
	if want := []int{0, 13}; tin == nil || tin[0] != want[0] || tin[1] != want[1] {
		t.Fatalf("buyer_tin span = %v, want %v", tin, want)
	}
	if tinWidth, bareWidth := tin[1]-tin[0], bare[1]-bare[0]; tinWidth <= bareWidth {
		t.Errorf("buyer_tin width = %d, bare_tin width = %d, want buyer_tin strictly wider", tinWidth, bareWidth)
	}
}

// T-01.6: a characterisation pin, not red-first -- passes today by design. Non-vacuity is
// structural rather than a length check: every row asserts a non-nil span, so the table reds if
// the matcher goes silent; "₦Total" starts at offset 3, so it reds under a `^` anchor; the nine
// trailing-decoration rows end before len(text), so they red under a `$` anchor; and slicing the
// raw text by loc reds if any normalisation pass runs ahead of FindStringIndex.
func TestAnchorLexicon_ADecoratedLabelStillNamesItsOwnField(t *testing.T) {
	for _, c := range []struct{ text, owner, want string }{
		{"Sub-total:", "subtotal", "Sub-total"},
		{"SUBTOTAL ₦", "subtotal", "SUBTOTAL"},
		{"Subtotal (NGN)", "subtotal", "Subtotal"},
		{"Subtotal %", "subtotal", "Subtotal"},
		{"CURRENCY (NGN)", "currency", "CURRENCY"},
		{"TIN #", "bare_tin", "TIN"},
		{"VAT @ 7.5%", "vat", "VAT"},
		{"TOTAL DUE (NGN)", "total", "TOTAL"},
		{"₦Total", "total", "Total"},
		{"GRAND TOTAL —", "total", "GRAND TOTAL"},
	} {
		loc := alSpan(c.text, c.owner)
		if loc == nil {
			t.Errorf("%q: %s span = nil, want a match", c.text, c.owner)
			continue
		}
		if got := c.text[loc[0]:loc[1]]; got != c.want {
			t.Errorf("%q: %s matched %q, want %q", c.text, c.owner, got, c.want)
		}
	}
}
