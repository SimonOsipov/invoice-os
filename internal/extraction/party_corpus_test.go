// party_corpus_test.go: the party partition over the committed wild layouts. External package,
// not extraction: reading a fixture builds the pdfium pool, and
// TestPDFiumPool_NotBuiltOnACancelledContext fatals if an internal test builds it first.
//
// Helpers use a pc* prefix; the pages come from resolve_test.go's rvCorpusPages.
package extraction_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	pcTwoParty = "wild_two_party_bare_tin.pdf"
	pcRuled    = "wild_ruled_lines_totals.pdf"
)

// pcName names a Party for a failure message; Party is an int with no String method.
func pcName(p extraction.Party) string {
	switch p {
	case extraction.PartyUnknown:
		return "PartyUnknown"
	case extraction.PartySupplier:
		return "PartySupplier"
	case extraction.PartyBuyer:
		return "PartyBuyer"
	}
	return fmt.Sprintf("Party(%d)", int(p))
}

func pcNames(ps []extraction.Party) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = pcName(p)
	}
	return out
}

// pcPageOne is one layout's first page, with its partition.
func pcPageOne(t *testing.T, layout string) (extraction.TokenPage, []extraction.Party) {
	t.Helper()

	page := rvCorpusPages(t, layout)[0]
	if page.Number != 1 {
		t.Fatalf("%s's first page is numbered %d, want 1; the indices below name another page", layout, page.Number)
	}
	got := extraction.PartyOrderForTest(page)
	if len(got) != len(page.Tokens) {
		t.Fatalf("%s: partyOrder returned %d entry/entries over %d token(s), want one per token", layout, len(got), len(page.Tokens))
	}
	return page, got
}

// "Invoice to" is this layout's only party heading and sits at token index 6. Index 7 is the
// label fragment "Customer No.", which buyer_name matches too -- so index 6 alone discriminates
// the heading from the fragment behind it.
//
// The layout carries NO supplier heading, so its shipped supplier_tin reading rests entirely on
// the PartyUnknown fallback. Pinned here so a later change cannot drop the fallback while its
// only real user is still shipping.
func TestPartyOrder_InvoiceToHeadsTheBuyerBlock(t *testing.T) {
	const headingIdx = 6

	page, got := pcPageOne(t, pcTwoParty)
	if len(page.Tokens) <= headingIdx {
		t.Fatalf("%s carries %d token(s), want more than %d; the index below names no token", pcTwoParty, len(page.Tokens), headingIdx)
	}
	if text := page.Tokens[headingIdx].Text; !strings.EqualFold(strings.TrimSpace(text), "Invoice to") {
		t.Fatalf("token %d of %s reads %q, want the \"Invoice to\" heading; the index below names another token", headingIdx, pcTwoParty, text)
	}

	for i, p := range got {
		want := extraction.PartyUnknown
		if i >= headingIdx {
			want = extraction.PartyBuyer
		}
		if p != want {
			t.Errorf("partyOrder()[%d] (%q) = %s, want %s", i, page.Tokens[i].Text, pcName(p), pcName(want))
		}
	}
	if slices.Contains(got, extraction.PartySupplier) {
		t.Errorf("partyOrder = %v; %s has no supplier heading at all, and its supplier_tin reading is the PartyUnknown fallback's only shipped user", pcNames(got), pcTwoParty)
	}
}

// The last block on a page runs to the end of it and owns the totals. Harmless only while no
// amount field is party-scoped: the day one is, that rule inherits a partition in which the
// buyer owns Sub-total, VAT and Total.
func TestPartyOrder_TheLastBlockOnAPageOwnsTheTotals(t *testing.T) {
	const (
		buyerFrom  = 5
		buyerCount = 29
	)

	page, got := pcPageOne(t, pcRuled)
	if len(page.Tokens) != buyerFrom+buyerCount {
		t.Fatalf("%s carries %d token(s), want %d; the block bounds below name another page", pcRuled, len(page.Tokens), buyerFrom+buyerCount)
	}
	// Non-degenerate first: an all-PartyBuyer page satisfies the run below without the
	// partition having drawn any boundary at all.
	if !slices.Contains(got, extraction.PartySupplier) {
		t.Fatalf("partyOrder = %v, which holds no PartySupplier; the run asserted below would be the whole page", pcNames(got))
	}

	for i := buyerFrom; i < len(got); i++ {
		if got[i] != extraction.PartyBuyer {
			t.Errorf("partyOrder()[%d] (%q) = %s, want PartyBuyer; the last block runs to the end of the page", i, page.Tokens[i].Text, pcName(got[i]))
		}
	}
	for _, want := range []string{"Sub-total", "VAT", "Total"} {
		found := false
		for i := buyerFrom; i < len(got); i++ {
			if strings.TrimSpace(page.Tokens[i].Text) == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no token in the last block reads %q; the block this spec calls totals-owning owns no total", want)
		}
	}
}
