// ubl_content_test.go: BUG-04-01 (task-397) Stage-4 QA. Closes the mapping holes the Test
// Specs table left open -- cac:PartyTaxScheme's scheme id, a POSITIVE cbc:CompanyID, and
// cbc:InvoicedQuantity / cac:Price / cac:Item, none of which any Mode-A spec asserted.
//
// The oracle here is walkDocument: a Token() walk that keys every element by its
// NAMESPACE-RESOLVED path. That is strictly stronger than a substring match -- a cbc:/cac:
// prefix swap changes the path and fails, where bytes.Contains would not notice. It is not an
// unmarshal into anything, so AC #10's prohibition stands untouched.
package ubl_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// xmlNode is one element of the rendered document, keyed by its resolved path.
type xmlNode struct {
	path     string // e.g. "Invoice/cac:AccountingSupplierParty/cac:Party/cbc:Name"
	text     string
	attrs    map[string]string
	children int
	order    int // start-tag position; closing order differs across nesting levels
}

func nsPrefix(space string) string {
	switch space {
	case wantNSInvoice:
		return ""
	case wantNSCAC:
		return "cac:"
	case wantNSCBC:
		return "cbc:"
	default:
		return "{" + space + "}"
	}
}

// walkDocument returns every element in closing order, which for siblings is document order.
func walkDocument(t *testing.T, out []byte) []xmlNode {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(out))
	var stack []*xmlNode
	var nodes []xmlNode
	var opened int
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("walk: %v\n---\n%s", err, out)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			path := nsPrefix(e.Name.Space) + e.Name.Local
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children++
				path = parent.path + "/" + path
			}
			opened++
			n := &xmlNode{path: path, attrs: map[string]string{}, order: opened}
			for _, a := range e.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			stack = append(stack, n)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(e)
			}
		case xml.EndElement:
			if len(stack) == 0 {
				t.Fatalf("walk: end element with no open element\n---\n%s", out)
			}
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			nodes = append(nodes, *n)
		}
	}
	if len(stack) != 0 {
		t.Fatalf("walk: %d unclosed elements\n---\n%s", len(stack), out)
	}
	if len(nodes) == 0 {
		t.Fatalf("walk found no elements -- every assertion built on it would be vacuous\n---\n%s", out)
	}
	return nodes
}

func nodesAt(nodes []xmlNode, path string) []xmlNode {
	var got []xmlNode
	for _, n := range nodes {
		if n.path == path {
			got = append(got, n)
		}
	}
	return got
}

func textsAt(nodes []xmlNode, path string) []string {
	var got []string
	for _, n := range nodesAt(nodes, path) {
		got = append(got, n.text)
	}
	return got
}

// oneAt fails unless exactly one element sits at path, so an assertion can never pass because
// the element is absent.
func oneAt(t *testing.T, nodes []xmlNode, path string) xmlNode {
	t.Helper()
	got := nodesAt(nodes, path)
	if len(got) != 1 {
		t.Fatalf("found %d elements at %s, want exactly 1", len(got), path)
	}
	return got[0]
}

func wantTextAt(t *testing.T, nodes []xmlNode, path, want, why string) {
	t.Helper()
	if got := oneAt(t, nodes, path).text; got != want {
		t.Errorf("%s = %q, want %q -- %s", path, got, want, why)
	}
}

func wantTextsAt(t *testing.T, nodes []xmlNode, path string, want []string, why string) {
	t.Helper()
	got := textsAt(nodes, path)
	if len(got) == 0 {
		t.Fatalf("no elements at %s -- the comparison below would be vacuous", path)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v -- %s", path, got, want, why)
	}
}

// wantChildOrder asserts the immediate children of parentPath appear in exactly this order.
// parentPath must identify a single element, or two instances' children would interleave.
func wantChildOrder(t *testing.T, nodes []xmlNode, parentPath string, want []string) {
	t.Helper()
	if n := len(nodesAt(nodes, parentPath)); n != 1 {
		t.Fatalf("found %d elements at %s, want exactly 1 -- child order would be ambiguous", n, parentPath)
	}

	kids := make([]xmlNode, 0, len(want))
	for _, n := range nodes {
		rest, ok := strings.CutPrefix(n.path, parentPath+"/")
		if ok && !strings.Contains(rest, "/") {
			kids = append(kids, n)
		}
	}
	sort.Slice(kids, func(i, j int) bool { return kids[i].order < kids[j].order })

	got := make([]string, 0, len(kids))
	for _, k := range kids {
		got = append(got, k.path[len(parentPath)+1:])
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("children of %s = %v, want %v -- UBL is order-sensitive at every level", parentPath, got, want)
	}
}

const (
	supplierPath      = "Invoice/cac:AccountingSupplierParty/cac:Party"
	buyerPath         = "Invoice/cac:AccountingCustomerParty/cac:Party"
	linePath          = "Invoice/cac:InvoiceLine"
	monetaryTotalPath = "Invoice/cac:LegalMonetaryTotal"
)

// --- AC #4: order below the top level ------------------------------------------------------------

// TestRender_NestedElementOrderFollowsTheUBLSequence: TestRender_ElementOrderFollowsTheUBLSequence
// pins only the eleven top-level elements, so swapping cac:Item with cac:Price -- or cac:PartyName
// with cac:PartyTaxScheme -- shipped the whole suite green. Verified by mutation. Struct field
// order is the only thing pinning these, and UBL is order-sensitive at every level.
func TestRender_NestedElementOrderFollowsTheUBLSequence(t *testing.T) {
	c := completeCanonical(t)
	c.Lines = c.Lines[:1] // one line: two would interleave their children
	nodes := walkDocument(t, mustRender(t, c))

	for _, party := range []string{supplierPath, buyerPath} {
		wantChildOrder(t, nodes, party, []string{"cac:PartyName", "cac:PartyTaxScheme"})
		wantChildOrder(t, nodes, party+"/cac:PartyTaxScheme", []string{"cbc:CompanyID", "cac:TaxScheme"})
	}
	wantChildOrder(t, nodes, monetaryTotalPath,
		[]string{"cbc:LineExtensionAmount", "cbc:TaxExclusiveAmount", "cbc:PayableAmount"})
	wantChildOrder(t, nodes, linePath, []string{
		"cbc:ID", "cbc:InvoicedQuantity", "cbc:LineExtensionAmount", "cac:TaxTotal", "cac:Item", "cac:Price",
	})
}

// --- AC #2/#4: the TIN scheme identifier ------------------------------------------------------

// TestRender_PartyTaxSchemeIdentifiesTheTINScheme: the scheme id is what says WHICH identifier
// system cbc:CompanyID is drawn from. A typo there ships a document nobody can resolve, and
// until this test nothing read the value at all.
func TestRender_PartyTaxSchemeIdentifiesTheTINScheme(t *testing.T) {
	nodes := walkDocument(t, mustRender(t, completeCanonical(t)))

	for _, party := range []string{supplierPath, buyerPath} {
		wantTextAt(t, nodes, party+"/cac:PartyTaxScheme/cac:TaxScheme/cbc:ID", "TIN",
			"cbc:CompanyID is a Nigerian TIN and the scheme id must say so")
	}
}

// --- AC #6: the positive half of the omission rule ----------------------------------------------

// TestRender_EmitsCompanyIDForEachPresentTIN is the positive TestRender_OmitsAbsentOptionalElements
// could not be: that test only asserts cbc:CompanyID ABSENT, so a renderer that never emitted it
// at all passed. Asserting each party's own TIN also catches a supplier/buyer swap.
func TestRender_EmitsCompanyIDForEachPresentTIN(t *testing.T) {
	c := completeCanonical(t)
	nodes := walkDocument(t, mustRender(t, c))

	wantTextAt(t, nodes, supplierPath+"/cac:PartyTaxScheme/cbc:CompanyID", *c.Supplier.TIN,
		"a present supplier TIN must reach the document")
	wantTextAt(t, nodes, buyerPath+"/cac:PartyTaxScheme/cbc:CompanyID", *c.Buyer.TIN,
		"a present buyer TIN must reach the document, and must not be the supplier's")
	wantTextAt(t, nodes, supplierPath+"/cac:PartyName/cbc:Name", *c.Supplier.Name,
		"the party name and the party TIN sit in different elements")
	wantTextAt(t, nodes, buyerPath+"/cac:PartyName/cbc:Name", *c.Buyer.Name,
		"the party name and the party TIN sit in different elements")
}

// --- AC #4/#7: line-level field mapping ----------------------------------------------------------

// TestRender_MapsEveryLineFieldToItsUBLElement reads each line field back by element. Nothing
// asserted cbc:InvoicedQuantity, cac:Price/cbc:PriceAmount or cac:Item before this, so a
// UnitPrice/LineTotal swap -- or a quantity written into the wrong element -- shipped green.
func TestRender_MapsEveryLineFieldToItsUBLElement(t *testing.T) {
	c := completeCanonical(t)
	nodes := walkDocument(t, mustRender(t, c))

	wantTextsAt(t, nodes, linePath+"/cbc:ID", []string{"1", "2"}, "cbc:ID carries LineNo")
	wantTextsAt(t, nodes, linePath+"/cbc:InvoicedQuantity", []string{"2", "1"}, "Quantity")
	wantTextsAt(t, nodes, linePath+"/cbc:LineExtensionAmount", []string{"800.00", "200.00"}, "LineTotal")
	wantTextsAt(t, nodes, linePath+"/cac:TaxTotal/cbc:TaxAmount", []string{"60.00", "15.00"}, "LineTax")
	wantTextsAt(t, nodes, linePath+"/cac:Item/cbc:Name", []string{"Widget", "Gadget"}, "Description")
	wantTextsAt(t, nodes, linePath+"/cac:Price/cbc:PriceAmount", []string{"400.00", "200.00"}, "UnitPrice")
}

// TestRender_InvoicedQuantityCarriesNoUnitCode pins the knowing structural gap: Canonical stores
// no unit, and a fabricated unitCode is the same class of invention as a derived cbc:Percent
// [ubl-conformance-is-structural-not-certified].
func TestRender_InvoicedQuantityCarriesNoUnitCode(t *testing.T) {
	nodes := walkDocument(t, mustRender(t, completeCanonical(t)))

	got := nodesAt(nodes, linePath+"/cbc:InvoicedQuantity")
	if len(got) == 0 {
		t.Fatal("no cbc:InvoicedQuantity elements -- the assertion below would be vacuous")
	}
	for i, n := range got {
		if _, ok := n.attrs["unitCode"]; ok {
			t.Errorf("line %d cbc:InvoicedQuantity carries unitCode=%q; the unit is not stored and not ours to invent",
				i, n.attrs["unitCode"])
		}
	}
}

// --- AC #7: currency on amounts -------------------------------------------------------------------

// TestRender_EveryAmountCarriesTheDocumentCurrency: Canonical has no per-line currency, so a
// line amount can never disagree with cbc:DocumentCurrencyCode. This pins that -- and that no
// amount ships without a currencyID at all.
func TestRender_EveryAmountCarriesTheDocumentCurrency(t *testing.T) {
	c := completeCanonical(t)
	nodes := walkDocument(t, mustRender(t, c))

	wantTextAt(t, nodes, "Invoice/cbc:DocumentCurrencyCode", *c.Currency, "the document currency")

	var checked int
	for _, n := range nodes {
		leaf := n.path[strings.LastIndex(n.path, "/")+1:]
		if !strings.HasSuffix(leaf, "Amount") || n.children > 0 {
			continue
		}
		checked++
		if got := n.attrs["currencyID"]; got != *c.Currency {
			t.Errorf("%s currencyID = %q, want %q -- every amount is in the document currency",
				n.path, got, *c.Currency)
		}
	}
	// completeCanonical has a document TaxTotal, three monetary totals and two lines each with a
	// LineExtensionAmount, a TaxAmount and a PriceAmount.
	if want := 1 + 3 + 2*3; checked != want {
		t.Errorf("checked %d amount elements, want %d -- the loop above is not seeing the document", checked, want)
	}
}
