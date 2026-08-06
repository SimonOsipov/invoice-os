// ubl_edge_test.go: BUG-04-01 (task-397) Stage-4 QA. Input shapes the Test Specs table did not
// reach: no money at all, a zero-value line, duplicate line numbers, credit-note-shaped
// negatives, non-ASCII and very long free text, and determinism.
//
// Several tests here PIN current behaviour rather than require it -- each says so, and each is
// raised with the story owner. A pinning test still earns its place: it turns a silent change
// into a failing build.
package ubl_test

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/ubl"
)

// --- empty containers ------------------------------------------------------------------------

// TestRender_OmitsLegalMonetaryTotalWhenEveryAmountIsAbsent pins omission (AC #6): with no
// stored money the container is dropped entirely, never emitted empty. Its positive counterpart
// below asserts the same path present, so neither can pass on a typo.
func TestRender_OmitsLegalMonetaryTotalWhenEveryAmountIsAbsent(t *testing.T) {
	c := completeCanonical(t)
	c.Subtotal, c.VAT, c.Total = nil, nil, nil

	out := mustRender(t, c)
	nodes := walkDocument(t, out)

	if got := nodesAt(nodes, monetaryTotalPath); len(got) != 0 {
		t.Errorf("cac:LegalMonetaryTotal rendered with no stored money: %#v", got)
	}
	if got := nodesAt(nodes, "Invoice/cac:TaxTotal"); len(got) != 0 {
		t.Errorf("document cac:TaxTotal present with a nil VAT: %#v", got)
	}
	if err := wellFormed(out); err != nil {
		t.Errorf("moneyless document is not well-formed: %v", err)
	}
}

// TestRender_EmitsLegalMonetaryTotalWithOnlyThePresentAmounts: one stored amount is enough to
// emit the container, and it carries exactly the members that are present.
func TestRender_EmitsLegalMonetaryTotalWithOnlyThePresentAmounts(t *testing.T) {
	tests := []struct {
		name     string
		subtotal *string
		total    *string
		want     [][2]string // child element, text -- in document order
	}{
		{"total only", nil, ublStr("1075.00"), [][2]string{{"cbc:PayableAmount", "1075.00"}}},
		{"subtotal only", ublStr("1000.00"), nil, [][2]string{
			{"cbc:LineExtensionAmount", "1000.00"}, {"cbc:TaxExclusiveAmount", "1000.00"},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := completeCanonical(t)
			c.Subtotal, c.Total = tc.subtotal, tc.total

			nodes := walkDocument(t, mustRender(t, c))

			var wantKids []string
			for _, kv := range tc.want {
				wantKids = append(wantKids, kv[0])
			}
			wantChildOrder(t, nodes, monetaryTotalPath, wantKids)
			for _, kv := range tc.want {
				wantTextAt(t, nodes, monetaryTotalPath+"/"+kv[0], kv[1],
					"a present amount reaches the document verbatim")
			}
		})
	}
}

// barestCanonical carries exactly what Missing accepts and not one optional more.
func barestCanonical() submission.Canonical {
	issued := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	return submission.Canonical{
		InvoiceNumber: "INV-2026-0002",
		IssueDate:     &issued,
		Supplier:      submission.Party{Name: ublStr("Acme Supplies Ltd")},
		Buyer:         submission.Party{Name: ublStr("Beta Trading Ltd")},
		Currency:      ublStr("NGN"),
		Lines:         []submission.CanonicalLine{{LineNo: 1}},
	}
}

// TestRender_EmitsNoEmptyContainer generalises the rule: a cac: element exists to hold children,
// so one with none is an absent container emitted anyway. Swept over each optional set ALONE --
// a childless container can appear at a partial fill, not only at the barest one.
func TestRender_EmitsNoEmptyContainer(t *testing.T) {
	tests := []struct {
		name string
		set  func(*submission.Canonical)
	}{
		{"no optional at all", func(*submission.Canonical) {}},
		{"subtotal alone", func(c *submission.Canonical) { c.Subtotal = ublStr("1000.00") }},
		{"vat alone", func(c *submission.Canonical) { c.VAT = ublStr("75.00") }},
		{"total alone", func(c *submission.Canonical) { c.Total = ublStr("1075.00") }},
		{"supplier tin alone", func(c *submission.Canonical) { c.Supplier.TIN = ublStr("12345678-0001") }},
		{"buyer tin alone", func(c *submission.Canonical) { c.Buyer.TIN = ublStr("87654321-0001") }},
		{"description alone", func(c *submission.Canonical) { c.Lines[0].Description = ublStr("Widget") }},
		{"blank description alone", func(c *submission.Canonical) { c.Lines[0].Description = ublStr("") }},
		{"quantity alone", func(c *submission.Canonical) { c.Lines[0].Quantity = ublStr("2") }},
		{"unit price alone", func(c *submission.Canonical) { c.Lines[0].UnitPrice = ublStr("400.00") }},
		{"line total alone", func(c *submission.Canonical) { c.Lines[0].LineTotal = ublStr("800.00") }},
		{"line tax alone", func(c *submission.Canonical) { c.Lines[0].LineTax = ublStr("60.00") }},
		{"every optional", func(c *submission.Canonical) {
			full := completeCanonical(t)
			c.Subtotal, c.VAT, c.Total = full.Subtotal, full.VAT, full.Total
			c.Supplier.TIN, c.Buyer.TIN = full.Supplier.TIN, full.Buyer.TIN
			c.Lines = full.Lines
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := barestCanonical()
			tc.set(&c)

			nodes := walkDocument(t, mustRender(t, c))

			var containers int
			for _, n := range nodes {
				if !strings.HasPrefix(n.path[strings.LastIndex(n.path, "/")+1:], "cac:") {
					continue
				}
				containers++
				if n.children == 0 {
					t.Errorf("%s rendered with no children -- an absent container is omitted, not emitted empty", n.path)
				}
			}
			if containers == 0 {
				t.Fatal("no cac: elements found -- the loop above is not seeing the document")
			}
		})
	}
}

// TestRender_EmitsAnAlmostEmptyLineForAZeroValueCanonicalLine PINS: Missing counts lines, it
// does not inspect them, so a zero-value CanonicalLine renders as an InvoiceLine carrying only
// cbc:ID -- and cbc:ID reads "0" although LineNo is documented 1..N. Owner decision.
func TestRender_EmitsAnAlmostEmptyLineForAZeroValueCanonicalLine(t *testing.T) {
	c := completeCanonical(t)
	c.Lines = []submission.CanonicalLine{{}}

	out := mustRender(t, c)
	nodes := walkDocument(t, out)

	line := oneAt(t, nodes, linePath)
	if line.children != 1 {
		t.Errorf("zero-value line rendered %d children, PINNED behaviour is 1 (cbc:ID alone)", line.children)
	}
	wantTextAt(t, nodes, linePath+"/cbc:ID", "0", "PINNED: LineNo 0 is emitted, not rejected or renumbered")
	if err := wellFormed(out); err != nil {
		t.Errorf("document with a zero-value line is not well-formed: %v", err)
	}
}

// --- line numbering --------------------------------------------------------------------------

// TestRender_DoesNotDeduplicateOrRenumberLineNumbers PINS: cbc:ID mirrors LineNo verbatim, so
// duplicates and negatives reach the document. The renderer is not the place to repair stored
// data, but a duplicated cbc:ID is not addressable by a receiver. Owner decision.
func TestRender_DoesNotDeduplicateOrRenumberLineNumbers(t *testing.T) {
	c := completeCanonical(t)
	c.Lines = []submission.CanonicalLine{
		{LineNo: 7, Description: ublStr("First")},
		{LineNo: 7, Description: ublStr("Second")},
		{LineNo: -3, Description: ublStr("Third")},
	}

	nodes := walkDocument(t, mustRender(t, c))

	wantTextsAt(t, nodes, linePath+"/cbc:ID", []string{"7", "7", "-3"},
		"PINNED: LineNo passes through as stored -- no dedupe, no renumber, no sort")
	wantTextsAt(t, nodes, linePath+"/cac:Item/cbc:Name", []string{"First", "Second", "Third"},
		"the lines stay in slice order and keep their own descriptions")
}

// --- credit-note-shaped input -------------------------------------------------------------------

// TestRender_PassesNegativeAndZeroAmountsThroughUnchanged PINS: money is copied verbatim, so a
// fully negative invoice renders -- but cbc:InvoiceTypeCode stays 380 (commercial invoice)
// because Canonical carries no document type. A credit note is 381. Owner decision.
func TestRender_PassesNegativeAndZeroAmountsThroughUnchanged(t *testing.T) {
	c := completeCanonical(t)
	c.Subtotal, c.VAT, c.Total = ublStr("-1000.00"), ublStr("0"), ublStr("-1000.00")
	c.Lines = []submission.CanonicalLine{{
		LineNo: 1, Description: ublStr("Returned widget"), Quantity: ublStr("-2"),
		UnitPrice: ublStr("500.00"), LineTotal: ublStr("-1000.00"), LineTax: ublStr("0"),
	}}

	nodes := walkDocument(t, mustRender(t, c))

	wantTextAt(t, nodes, "Invoice/cac:LegalMonetaryTotal/cbc:PayableAmount", "-1000.00",
		"a negative total is neither clamped nor sign-flipped")
	wantTextAt(t, nodes, "Invoice/cac:TaxTotal/cbc:TaxAmount", "0",
		"a zero tax amount is emitted, not treated as absent")
	wantTextsAt(t, nodes, linePath+"/cbc:InvoicedQuantity", []string{"-2"}, "a negative quantity passes through")
	wantTextAt(t, nodes, "Invoice/cbc:InvoiceTypeCode", "380",
		"PINNED: the type code is unconditional; Canonical carries no document type, so a credit note still declares 380")
}

// --- free text ---------------------------------------------------------------------------------

// TestRender_PreservesFreeTextLosslessly walks the text back out of the document: what a reader
// recovers must equal what was stored, byte for byte.
func TestRender_PreservesFreeTextLosslessly(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"nigerian name and naira", "Adébáyọ̀ Okonkwo & Sons ₦"},
		{"greek and cjk", "Ωμέγα 株式会社"},
		{"leading and trailing whitespace", "  spaced  "},
		{"whitespace only", "   "},
		{"newline and tab", "line one\nline two\tcol"},
		{"very long", strings.Repeat("ünïcödé ", 8192)},
		{"markup lookalike", `</cbc:Name><cbc:Name>injected`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := completeCanonical(t)
			c.Lines = c.Lines[:1]
			c.Lines[0].Description = ublStr(tc.text)

			out := mustRender(t, c)

			if err := wellFormed(out); err != nil {
				t.Fatalf("document is not well-formed: %v", err)
			}
			if !utf8.Valid(out) {
				t.Error("output is not valid UTF-8, but the declaration says encoding=\"UTF-8\"")
			}
			nodes := walkDocument(t, out)
			wantTextsAt(t, nodes, linePath+"/cac:Item/cbc:Name", []string{tc.text},
				"free text must survive the round trip unchanged")
			// One line in, one line out: injected markup must not have forged a second element.
			if got := len(nodesAt(nodes, linePath)); got != 1 {
				t.Errorf("got %d cac:InvoiceLine elements, want 1 -- free text escaped its element", got)
			}
		})
	}
}

// TestRender_KeepsBlankFreeTextDistinctFromAbsentFreeText: the amount rule -- absent omits,
// blank renders empty -- applies to cac:Item too.
func TestRender_KeepsBlankFreeTextDistinctFromAbsentFreeText(t *testing.T) {
	c := completeCanonical(t)
	c.Lines = c.Lines[:1]
	c.Lines[0].Description = ublStr("")

	blank := walkDocument(t, mustRender(t, c))
	wantTextsAt(t, blank, linePath+"/cac:Item/cbc:Name", []string{""},
		"a stored empty description renders an empty element, not an absent one")

	c.Lines[0].Description = nil
	absent := walkDocument(t, mustRender(t, c))
	if got := nodesAt(absent, linePath+"/cac:Item"); len(got) != 0 {
		t.Errorf("nil description still rendered cac:Item: %#v", got)
	}
}

// --- determinism ----------------------------------------------------------------------------------

// TestRender_IsDeterministic: the document is content-addressable evidence downstream, so equal
// input must give byte-identical output -- no map iteration, no clock, no randomness.
func TestRender_IsDeterministic(t *testing.T) {
	first := mustRender(t, completeCanonical(t))
	second := mustRender(t, completeCanonical(t))

	if !bytes.Equal(first, second) {
		t.Errorf("two renders of an equal Canonical differ\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	// Same instance twice: proves the first call left no state behind in it either.
	c := completeCanonical(t)
	again, err := ubl.Render(c)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Equal(first, again) {
		t.Error("rendering the same Canonical instance a second time produced different bytes")
	}
}
