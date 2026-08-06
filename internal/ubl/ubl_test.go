// ubl_test.go: BUG-04-01 (task-397) RED specs (QA Mode A) for the UBL 2.1 renderer -- the
// Test Specs table transcribed before ubl.go has a body.
//
// PACKAGE ubl_test (external), per [test-package-follows-the-symbol]: Render, Missing and
// ErrIncomplete are the whole surface, so the suite needs no unexported access. The namespace
// URIs and profile constants below are declared HERE as literals rather than read from the
// package, so an implementation that ships a wrong constant fails instead of agreeing with
// itself.
//
// THE TRAP THIS FILE EXISTS TO AVOID: xml.Unmarshal into a struct shaped like the renderer's
// own document returns err == nil with EVERY FIELD EMPTY -- Go resolves <cbc:ID> to
// Name{Space: nsCBC, Local: "ID"} once xmlns:cbc is in scope, and a literal `xml:"cbc:ID"`
// tag never matches that. Measured. So well-formedness is checked ONLY by wellFormed's
// Token() loop, and TestRender_WellFormednessCheckIsNotVacuous pins that the loop can fail.
// The one legitimate Unmarshal in the suite is into the namespace-URI-tagged mirror in
// TestRender_EscapesFreeText.
//
// No testify, no t.Skip (internal/tools/rlsgate fails CI on any test-level skip); these are
// pure Go with no DB, no network and no clock.
package ubl_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/ubl"
)

// The three namespace URIs and the three declared profile constants, quoted from the standard
// rather than from the package under test.
const (
	wantNSInvoice = "urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
	wantNSCAC     = "urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
	wantNSCBC     = "urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2"

	wantCustomizationID = "urn:cen.eu:en16931:2017#compliant#urn:fdc:peppol.eu:2017:poacc:billing:3.0"
	wantProfileID       = "urn:fdc:peppol.eu:2017:poacc:billing:01:1.0"
	wantInvoiceTypeCode = "380"

	wantXMLDeclaration = `<?xml version="1.0" encoding="UTF-8"?>`
	issueDateLayout    = "2006-01-02"
)

// documentSequence is AC #4: the eleven top-level elements, in the order UBL requires.
var documentSequence = []string{
	"<cbc:CustomizationID>",
	"<cbc:ProfileID>",
	"<cbc:ID>",
	"<cbc:IssueDate>",
	"<cbc:InvoiceTypeCode>",
	"<cbc:DocumentCurrencyCode>",
	"<cac:AccountingSupplierParty>",
	"<cac:AccountingCustomerParty>",
	"<cac:TaxTotal>",
	"<cac:LegalMonetaryTotal>",
	"<cac:InvoiceLine>",
}

func ublStr(s string) *string { return &s }

// completeCanonical satisfies all six Missing inputs. VAT is non-nil ON PURPOSE: with a nil
// VAT the document-level <cac:TaxTotal> is absent and bytes.Index finds the LINE-level one
// nested in <cac:InvoiceLine>, so TestRender_ElementOrderFollowsTheUBLSequence would fail on
// a correct document. It also contains no literal "..." -- the AC #1 elision guard scans the
// rendered output, which is built from these values.
func completeCanonical(t *testing.T) submission.Canonical {
	t.Helper()
	issued := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	return submission.Canonical{
		InvoiceID:     "11111111-1111-1111-1111-111111111111",
		InvoiceNumber: "INV-2026-0001",
		IssueDate:     &issued,
		Supplier:      submission.Party{TIN: ublStr("12345678-0001"), Name: ublStr("Acme Supplies Ltd")},
		Buyer:         submission.Party{TIN: ublStr("87654321-0001"), Name: ublStr("Beta Trading Ltd")},
		Currency:      ublStr("NGN"),
		Subtotal:      ublStr("1000.00"),
		VAT:           ublStr("75.00"),
		Total:         ublStr("1075.00"),
		Lines: []submission.CanonicalLine{
			{
				LineID: "aaaaaaaa-0000-0000-0000-000000000001", LineNo: 1,
				Description: ublStr("Widget"), Quantity: ublStr("2"),
				UnitPrice: ublStr("400.00"), LineTotal: ublStr("800.00"), LineTax: ublStr("60.00"),
			},
			{
				LineID: "aaaaaaaa-0000-0000-0000-000000000002", LineNo: 2,
				Description: ublStr("Gadget"), Quantity: ublStr("1"),
				UnitPrice: ublStr("200.00"), LineTotal: ublStr("200.00"), LineTax: ublStr("15.00"),
			},
		},
	}
}

// wellFormed is the suite's ONLY well-formedness oracle: a Token() loop to io.EOF.
// TestRender_WellFormednessCheckIsNotVacuous proves it can fail.
func wellFormed(b []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// mustRender fails the test on any Render error. Every Render assertion below goes through it,
// so a negative assertion ("output does not contain X") can never pass on nil bytes.
func mustRender(t *testing.T, c submission.Canonical) []byte {
	t.Helper()
	out, err := ubl.Render(c)
	if err != nil {
		t.Fatalf("Render: unexpected error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("Render returned no bytes")
	}
	return out
}

func wantContains(t *testing.T, out []byte, want, why string) {
	t.Helper()
	if !bytes.Contains(out, []byte(want)) {
		t.Errorf("output does not contain %q -- %s\n---\n%s", want, why, out)
	}
}

func wantAbsent(t *testing.T, out []byte, unwanted, why string) {
	t.Helper()
	if bytes.Contains(out, []byte(unwanted)) {
		t.Errorf("output contains %q -- %s\n---\n%s", unwanted, why, out)
	}
}

// invoiceLineIDs reads the cbc:ID of each cac:InvoiceLine, in document order.
func invoiceLineIDs(t *testing.T, out []byte) []string {
	t.Helper()
	var ids []string
	for _, chunk := range bytes.Split(out, []byte("<cac:InvoiceLine>"))[1:] {
		start := bytes.Index(chunk, []byte("<cbc:ID>"))
		end := bytes.Index(chunk, []byte("</cbc:ID>"))
		if start < 0 || end < start {
			t.Fatalf("invoice line carries no <cbc:ID>:\n%s", chunk)
		}
		ids = append(ids, string(chunk[start+len("<cbc:ID>"):end]))
	}
	return ids
}

// --- AC #1: declaration, root, namespaces ------------------------------------------------

func TestRender_DeclaresTheThreeUBLNamespacesInFull(t *testing.T) {
	out := mustRender(t, completeCanonical(t))

	for _, ns := range []string{wantNSInvoice, wantNSCAC, wantNSCBC} {
		wantContains(t, out, ns, "each of the three namespace URIs must appear in full")
	}
	wantAbsent(t, out, "...", "a namespace URI (or anything else) must never be elided in the shipped document")
}

func TestRender_XMLDeclarationAndRoot(t *testing.T) {
	out := mustRender(t, completeCanonical(t))

	if !bytes.HasPrefix(out, []byte(wantXMLDeclaration)) {
		t.Errorf("output does not open with %q; first 64 bytes: %q", wantXMLDeclaration, out[:min(64, len(out))])
	}

	dec := xml.NewDecoder(bytes.NewReader(out))
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("no start element before %v", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "Invoice" || se.Name.Space != wantNSInvoice {
			t.Errorf("first start element = %q in %q, want %q in %q",
				se.Name.Local, se.Name.Space, "Invoice", wantNSInvoice)
		}
		return
	}
}

// --- AC #5: well-formedness ---------------------------------------------------------------

func TestRender_IsWellFormedXML(t *testing.T) {
	out := mustRender(t, completeCanonical(t))

	if err := wellFormed(out); err != nil {
		t.Errorf("rendered document is not well-formed XML: %v\n---\n%s", err, out)
	}
}

// TestRender_WellFormednessCheckIsNotVacuous pins that wellFormed CAN fail. Without it, a
// broken oracle would silently green every well-formedness assertion in the suite. It never
// calls Render, so it is green from the moment it is written -- a guard, not a red-first spec.
func TestRender_WellFormednessCheckIsNotVacuous(t *testing.T) {
	malformed := []byte(`<Invoice><cbc:ID>x</Invoice>`)

	err := wellFormed(malformed)
	if err == nil {
		t.Fatal("wellFormed accepted a malformed document -- the oracle cannot fail and proves nothing")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("wellFormed returned io.EOF for a malformed document, want a syntax error; got %v", err)
	}
	if !strings.Contains(err.Error(), "XML syntax error") {
		t.Errorf("wellFormed error = %v, want an XML syntax error", err)
	}
}

// --- AC #2: declared profile constants -----------------------------------------------------

// TestRender_DeclaresTheBISProfileConstants checks the three values the document DECLARES.
// Declaring a profile is not a validator verdict [ubl-conformance-is-structural-not-certified].
func TestRender_DeclaresTheBISProfileConstants(t *testing.T) {
	out := mustRender(t, completeCanonical(t))

	wantContains(t, out, "<cbc:CustomizationID>"+wantCustomizationID+"</cbc:CustomizationID>",
		"the EN 16931 / BIS 3.0 customization the document declares")
	wantContains(t, out, "<cbc:ProfileID>"+wantProfileID+"</cbc:ProfileID>",
		"the PEPPOL billing profile the document declares")
	wantContains(t, out, "<cbc:InvoiceTypeCode>"+wantInvoiceTypeCode+"</cbc:InvoiceTypeCode>",
		"380 = commercial invoice")
}

// --- AC #3: xsd:date ------------------------------------------------------------------------

func TestRender_IssueDateIsDateOnly(t *testing.T) {
	c := completeCanonical(t)
	issued := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	c.IssueDate = &issued

	out := mustRender(t, c)

	wantContains(t, out, "<cbc:IssueDate>2026-08-06</cbc:IssueDate>",
		"cbc:IssueDate is an xsd:date, not a timestamp")
	wantAbsent(t, out, "T00:00:00",
		"a time.Time field marshals RFC3339; IssueDate must be a pre-formatted date string")
}

// TestRender_IssueDateIsNotTimezoneShifted mirrors mock_wire_test.go:888-890: the two zones
// must disagree on the CALENDAR DAY or the pin proves nothing.
func TestRender_IssueDateIsNotTimezoneShifted(t *testing.T) {
	local := time.Date(2026, 8, 6, 0, 30, 0, 0, time.FixedZone("WAT", 3600))
	if local.UTC().Format(issueDateLayout) == local.Format(issueDateLayout) {
		t.Fatalf("test precondition broken: local and UTC must land on different days, both are %s",
			local.Format(issueDateLayout))
	}

	c := completeCanonical(t)
	c.IssueDate = &local

	out := mustRender(t, c)

	wantContains(t, out, "<cbc:IssueDate>2026-08-06</cbc:IssueDate>",
		"IssueDate formats in the time's OWN location")
	wantAbsent(t, out, "2026-08-05",
		"a .UTC() normalisation before formatting would silently shift the invoice a day earlier")
}

// --- AC #5: escaping ------------------------------------------------------------------------

// escapeMirror recovers the supplier name. Its tags are in NAMESPACE-URI form -- the literal
// `xml:"cbc:Name"` form the renderer marshals with never matches on the way back in.
type escapeMirror struct {
	XMLName  xml.Name `xml:"urn:oasis:names:specification:ubl:schema:xsd:Invoice-2 Invoice"`
	Supplier struct {
		Party struct {
			PartyName struct {
				Name string `xml:"urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2 Name"`
			} `xml:"urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2 PartyName"`
		} `xml:"urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2 Party"`
	} `xml:"urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2 AccountingSupplierParty"`
}

func TestRender_EscapesFreeText(t *testing.T) {
	const raw = `A & B <Ltd> "x"`
	c := completeCanonical(t)
	c.Supplier.Name = ublStr(raw)

	out := mustRender(t, c)

	// Go emits &#34; for a double quote, not &quot;. Measured.
	wantContains(t, out, `A &amp; B &lt;Ltd&gt; &#34;x&#34;`, "free text must be XML-escaped")
	wantAbsent(t, out, "<Ltd>", "an unescaped angle bracket would break the document")
	if err := wellFormed(out); err != nil {
		t.Fatalf("document with hostile free text is not well-formed: %v", err)
	}

	var got escapeMirror
	if err := xml.Unmarshal(out, &got); err != nil {
		t.Fatalf("mirror unmarshal: %v\n---\n%s", err, out)
	}
	if got.Supplier.Party.PartyName.Name != raw {
		t.Errorf("recovered supplier name = %q, want %q -- escaping must be lossless",
			got.Supplier.Party.PartyName.Name, raw)
	}
}

// --- AC #4: element order and lines ----------------------------------------------------------

func TestRender_ElementOrderFollowsTheUBLSequence(t *testing.T) {
	out := mustRender(t, completeCanonical(t))

	prev := -1
	prevName := "start of document"
	for _, elem := range documentSequence {
		at := bytes.Index(out, []byte(elem))
		if at < 0 {
			t.Fatalf("%s is absent from the document\n---\n%s", elem, out)
		}
		if at <= prev {
			t.Errorf("%s appears at %d, at or before %s at %d -- UBL is order-sensitive",
				elem, at, prevName, prev)
		}
		prev, prevName = at, elem
	}
}

func TestRender_EmitsOneInvoiceLinePerCanonicalLineInLineNoOrder(t *testing.T) {
	tests := []struct {
		name    string
		lineNos []int
		want    []string
	}{
		{name: "ascending", lineNos: []int{1, 2, 3}, want: []string{"1", "2", "3"}},
		// The renderer emits in SLICE order and does not sort; with an ascending fixture alone
		// a sorting implementation would pass.
		{name: "slice order is not lineNo order", lineNos: []int{3, 1, 2}, want: []string{"3", "1", "2"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := completeCanonical(t)
			c.Lines = nil
			for _, n := range tc.lineNos {
				c.Lines = append(c.Lines, submission.CanonicalLine{
					LineNo: n, Description: ublStr("Item"), Quantity: ublStr("1"),
					UnitPrice: ublStr("10.00"), LineTotal: ublStr("10.00"),
				})
			}

			out := mustRender(t, c)

			if n := bytes.Count(out, []byte("<cac:InvoiceLine>")); n != len(tc.lineNos) {
				t.Errorf("got %d <cac:InvoiceLine> elements, want %d\n---\n%s", n, len(tc.lineNos), out)
			}
			if got := invoiceLineIDs(t, out); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("line cbc:ID values in document order = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- AC #7: money ------------------------------------------------------------------------------

// TestRender_MoneyPassesThroughVerbatim asserts against >value< rather than the bare value:
// "1000.000" CONTAINS "1000.00", so a bare-substring negative would fire on a correct document.
func TestRender_MoneyPassesThroughVerbatim(t *testing.T) {
	c := completeCanonical(t)
	c.Subtotal = ublStr("1000.000")
	c.VAT = ublStr("75.5")
	c.Total = ublStr("1075.500")
	for i := range c.Lines {
		c.Lines[i].Quantity = ublStr("1")
		c.Lines[i].UnitPrice = ublStr("111.11")
		c.Lines[i].LineTotal = ublStr("111.11")
		c.Lines[i].LineTax = ublStr("2.22")
	}

	out := mustRender(t, c)

	for _, want := range []string{">1000.000<", ">75.5<", ">1075.500<"} {
		wantContains(t, out, want, "decimal strings reach the document byte-for-byte, never through a float")
	}
	for _, unwanted := range []string{">1000.00<", ">75.50<", ">1075.50<"} {
		wantAbsent(t, out, unwanted, "no rounding, no precision normalisation, no float round-trip")
	}
}

func TestRender_NoDerivedTaxPercent(t *testing.T) {
	c := completeCanonical(t)
	c.Subtotal = ublStr("1000.00")
	c.VAT = ublStr("75.00")

	out := mustRender(t, c)

	wantContains(t, out, ">75.00<", "the stored VAT amount is emitted as stored")
	wantAbsent(t, out, "cbc:Percent",
		"the server has no authority to invent a tax rate from subtotal and VAT")
}

// --- AC #6: omission ----------------------------------------------------------------------------

func TestRender_OmitsAbsentOptionalElements(t *testing.T) {
	c := completeCanonical(t)
	c.Supplier.TIN = nil
	c.Buyer.TIN = nil

	out := mustRender(t, c)

	wantContains(t, out, "<cbc:Name>Acme Supplies Ltd</cbc:Name>", "the supplier still renders")
	wantContains(t, out, "<cbc:Name>Beta Trading Ltd</cbc:Name>", "the buyer still renders")
	wantAbsent(t, out, "cbc:CompanyID", "an absent TIN omits its element entirely")
	wantAbsent(t, out, "<cbc:CompanyID></cbc:CompanyID>",
		"absence is never an empty element -- that would make absent and present-but-blank indistinguishable")
}

// --- AC #8: the gate -----------------------------------------------------------------------------

func TestMissing_ReportsEveryGapInFixedOrder(t *testing.T) {
	want := []string{
		"an invoice number",
		"an issue date",
		"a currency",
		"a supplier name",
		"a buyer name",
		"at least one line item",
	}

	got := ubl.Missing(submission.Canonical{})

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Missing(zero) = %#v, want %#v", got, want)
	}
}

func TestMissing_IsEmptyForACompleteCanonical(t *testing.T) {
	c := completeCanonical(t)

	if got := ubl.Missing(c); len(got) != 0 {
		t.Errorf("Missing(complete) = %#v, want empty", got)
	}
	// Paired positive: "nothing is missing" only means something if the document renders.
	if _, err := ubl.Render(c); err != nil {
		t.Errorf("Render(complete) = error %v, want a document", err)
	}
}

func TestMissing_TreatsBlankStringsAsAbsent(t *testing.T) {
	c := completeCanonical(t)
	c.Currency = ublStr("")
	c.Supplier.Name = ublStr("   ")

	want := []string{"a currency", "a supplier name"}
	if got := ubl.Missing(c); !reflect.DeepEqual(got, want) {
		t.Errorf("Missing(blank currency and supplier name) = %#v, want %#v", got, want)
	}
}

// TestMissing_DoesNotRequireATIN: an absent TIN still yields a constructible document. TIN
// presence is the MBS rule pack's job, not this gate's [ubl-gate-is-content-not-status].
func TestMissing_DoesNotRequireATIN(t *testing.T) {
	c := completeCanonical(t)
	c.Supplier.TIN = nil
	c.Buyer.TIN = nil

	if got := ubl.Missing(c); len(got) != 0 {
		t.Errorf("Missing(no TINs) = %#v, want empty -- a TIN is not required to construct the document", got)
	}
	if _, err := ubl.Render(c); err != nil {
		t.Errorf("Render(no TINs) = error %v, want a document", err)
	}
}

func TestRender_RefusesAnIncompleteCanonical(t *testing.T) {
	c := completeCanonical(t)
	c.Lines = nil

	out, err := ubl.Render(c)

	if !errors.Is(err, ubl.ErrIncomplete) {
		t.Errorf("Render(no lines) error = %v, want it to wrap ErrIncomplete", err)
	}
	if out != nil {
		t.Errorf("Render(no lines) returned %d bytes, want nil -- never a partial document", len(out))
	}
	if err != nil && !strings.Contains(err.Error(), "at least one line item") {
		t.Errorf("Render(no lines) error = %q, want it to name the missing content", err)
	}
}
