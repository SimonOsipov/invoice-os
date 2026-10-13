package ubl_test

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func nrsCanonical(t *testing.T) submission.Canonical {
	t.Helper()
	c := completeCanonical(t)
	due := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	point := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	c.IssueTime, c.DueDate, c.TaxPointDate = ublStr("14:30:00"), &due, &point
	c.TaxCurrencyCode = ublStr("USD")
	c.InvoiceKind, c.PaymentStatus = ublStr("B2B"), ublStr("PAID")
	full := func(p submission.Party, tag string) submission.Party {
		p.Email, p.Telephone = ublStr(tag+"@x.ng"), ublStr("+234"+tag)
		p.Street, p.City, p.PostalZone = ublStr("1 Main St "+tag), ublStr("Lagos"), ublStr("100001")
		p.Country, p.State, p.LGA = ublStr("NG"), ublStr("NG-LA"), ublStr("NG-LA-001")
		return p
	}
	c.Supplier, c.Buyer = full(c.Supplier, "s"), full(c.Buyer, "b")
	c.TaxSubtotals = []submission.TaxSubtotal{
		{Category: "STANDARD_VAT", Percent: ublStr("7.50"), TaxableAmount: ublStr("800.00"), TaxAmount: ublStr("60.00")},
		{Category: "ZERO_VAT", TaxableAmount: ublStr("200.00"), TaxAmount: ublStr("15.00")},
	}
	l := &c.Lines[0]
	l.TaxCategory, l.TaxPercent = ublStr("STANDARD_VAT"), ublStr("7.50")
	l.HSNCode, l.ProductCategory = ublStr("1234.56"), ublStr("Hardware")
	l.SellersItemIdentification, l.PriceUnit, l.BaseQuantity = ublStr("SKU-1"), ublStr("EA"), ublStr("1.000")
	return c
}

func TestRender_NRSHeaderElementsInSequence(t *testing.T) {
	nodes := walkDocument(t, mustRender(t, nrsCanonical(t)))
	wantChildOrder(t, nodes, "Invoice", []string{
		"cbc:CustomizationID", "cbc:ProfileID", "cbc:ID", "cbc:IssueDate", "cbc:IssueTime", "cbc:DueDate",
		"cbc:InvoiceTypeCode", "cbc:TaxPointDate", "cbc:DocumentCurrencyCode", "cbc:TaxCurrencyCode",
		"cac:AccountingSupplierParty", "cac:AccountingCustomerParty", "cac:TaxTotal", "cac:LegalMonetaryTotal",
		"cac:InvoiceLine", "cac:InvoiceLine",
	})
	wantTextAt(t, nodes, "Invoice/cbc:IssueTime", "14:30:00", "IssueTime")
	wantTextAt(t, nodes, "Invoice/cbc:DueDate", "2026-09-05", "DueDate")
	wantTextAt(t, nodes, "Invoice/cbc:TaxPointDate", "2026-08-07", "TaxPointDate")
	wantTextAt(t, nodes, "Invoice/cbc:TaxCurrencyCode", "USD", "TaxCurrencyCode")

	c := completeCanonical(t)
	nodes = walkDocument(t, mustRender(t, c))
	for _, e := range []string{"IssueTime", "DueDate", "TaxPointDate", "TaxCurrencyCode"} {
		if n := nodesAt(nodes, "Invoice/cbc:"+e); len(n) != 0 {
			t.Errorf("cbc:%s rendered with nothing stored", e)
		}
	}
}

func TestRender_PartyPostalAddressAndContact(t *testing.T) {
	nodes := walkDocument(t, mustRender(t, nrsCanonical(t)))
	for _, tc := range []struct{ path, tag string }{{supplierPath, "s"}, {buyerPath, "b"}} {
		wantChildOrder(t, nodes, tc.path, []string{"cac:PartyName", "cac:PostalAddress", "cac:PartyTaxScheme", "cac:Contact"})
		wantChildOrder(t, nodes, tc.path+"/cac:PostalAddress", []string{
			"cbc:StreetName", "cbc:CityName", "cbc:PostalZone", "cbc:CountrySubentityCode", "cbc:District", "cac:Country"})
		a := tc.path + "/cac:PostalAddress/"
		wantTextAt(t, nodes, a+"cbc:StreetName", "1 Main St "+tc.tag, "street")
		wantTextAt(t, nodes, a+"cbc:CityName", "Lagos", "city")
		wantTextAt(t, nodes, a+"cbc:PostalZone", "100001", "postal zone")
		wantTextAt(t, nodes, a+"cbc:CountrySubentityCode", "NG-LA", "state")
		wantTextAt(t, nodes, a+"cbc:District", "NG-LA-001", "LGA")
		wantTextAt(t, nodes, a+"cac:Country/cbc:IdentificationCode", "NG", "country")
		wantChildOrder(t, nodes, tc.path+"/cac:Contact", []string{"cbc:Telephone", "cbc:ElectronicMail"})
		wantTextAt(t, nodes, tc.path+"/cac:Contact/cbc:Telephone", "+234"+tc.tag, "telephone")
		wantTextAt(t, nodes, tc.path+"/cac:Contact/cbc:ElectronicMail", tc.tag+"@x.ng", "email")
	}
}

func TestRender_PartialAddressOmitsTheRest(t *testing.T) {
	c := completeCanonical(t)
	c.Buyer.State = ublStr("NG-LA")
	nodes := walkDocument(t, mustRender(t, c))
	wantChildOrder(t, nodes, buyerPath+"/cac:PostalAddress", []string{"cbc:CountrySubentityCode"})
	if n := nodesAt(nodes, buyerPath+"/cac:Contact"); len(n) != 0 {
		t.Error("cac:Contact rendered with no contact stored")
	}
	if n := nodesAt(nodes, supplierPath+"/cac:PostalAddress"); len(n) != 0 {
		t.Error("supplier cac:PostalAddress rendered with no address stored")
	}
}

func TestRender_TaxSubtotalPerCategory(t *testing.T) {
	nodes := walkDocument(t, mustRender(t, nrsCanonical(t)))
	p := "Invoice/cac:TaxTotal/cac:TaxSubtotal"
	if n := len(nodesAt(nodes, p)); n != 2 {
		t.Fatalf("%d TaxSubtotal, want 2", n)
	}
	wantChildOrder(t, nodes, "Invoice/cac:TaxTotal", []string{
		"cbc:TaxAmount", "cac:TaxSubtotal", "cac:TaxSubtotal"})
	wantTextsAt(t, nodes, p+"/cac:TaxCategory/cbc:ID", []string{"STANDARD_VAT", "ZERO_VAT"}, "category code")
	wantTextsAt(t, nodes, p+"/cac:TaxCategory/cbc:Percent", []string{"7.50"}, "percent only when stored")
	wantTextsAt(t, nodes, p+"/cac:TaxCategory/cac:TaxScheme/cbc:ID", []string{"VAT", "VAT"}, "scheme")
	wantTextsAt(t, nodes, p+"/cbc:TaxableAmount", []string{"800.00", "200.00"}, "taxable")
	wantTextsAt(t, nodes, p+"/cbc:TaxAmount", []string{"60.00", "15.00"}, "tax")
	for _, e := range []string{"cbc:TaxableAmount", "cbc:TaxAmount"} {
		for _, n := range nodesAt(nodes, p+"/"+e) {
			if n.attrs["currencyID"] != "NGN" {
				t.Errorf("%s currencyID = %q, want NGN", e, n.attrs["currencyID"])
			}
		}
	}
}

func TestRender_NoTaxTotalWhenVATIsAbsentEvenWithSubtotals(t *testing.T) {
	c := nrsCanonical(t)
	c.VAT = nil
	nodes := walkDocument(t, mustRender(t, c))
	if n := nodesAt(nodes, "Invoice/cac:TaxTotal"); len(n) != 0 {
		t.Error("document cac:TaxTotal rendered with VAT absent")
	}
}

func TestRender_SubtotalWithoutTaxAmountIsNotRendered(t *testing.T) {
	c := nrsCanonical(t)
	c.TaxSubtotals[0].TaxAmount = nil
	c.TaxSubtotals[1].TaxableAmount = nil
	nodes := walkDocument(t, mustRender(t, c))
	p := "Invoice/cac:TaxTotal/cac:TaxSubtotal"
	if n := len(nodesAt(nodes, p)); n != 1 {
		t.Fatalf("%d TaxSubtotal, want 1", n)
	}
	wantTextsAt(t, nodes, p+"/cac:TaxCategory/cbc:ID", []string{"ZERO_VAT"}, "only the complete subtotal")
	if n := nodesAt(nodes, p+"/cbc:TaxableAmount"); len(n) != 0 {
		t.Error("TaxableAmount rendered although nil")
	}
}

func TestRender_LineNRSElements(t *testing.T) {
	c := nrsCanonical(t)
	l := &c.Lines[0]
	l.ISICCode, l.ServiceCategory = ublStr("6201"), ublStr("Software")
	nodes := walkDocument(t, mustRender(t, c))
	l1 := linePath + "/cac:Item/"
	wantChildOrder(t, nodes, "Invoice/cac:InvoiceLine/cac:Item/cac:ClassifiedTaxCategory", []string{
		"cbc:ID", "cbc:Percent", "cac:TaxScheme"})
	q := nodesAt(nodes, linePath+"/cbc:InvoicedQuantity")
	if q[0].attrs["unitCode"] != "EA" {
		t.Errorf("InvoicedQuantity unitCode = %q, want EA", q[0].attrs["unitCode"])
	}
	if _, ok := q[1].attrs["unitCode"]; ok {
		t.Error("line 2 InvoicedQuantity carries a unitCode with no price_unit")
	}
	bq := oneAt(t, nodes, linePath+"/cac:Price/cbc:BaseQuantity")
	if bq.text != "1.000" || bq.attrs["unitCode"] != "EA" {
		t.Errorf("BaseQuantity = %q unitCode %q", bq.text, bq.attrs["unitCode"])
	}
	wantTextAt(t, nodes, l1+"cac:SellersItemIdentification/cbc:ID", "SKU-1", "seller item id")
	codes := nodesAt(nodes, l1+"cac:CommodityClassification/cbc:ItemClassificationCode")
	if len(codes) != 2 || codes[0].text != "1234.56" || codes[0].attrs["listID"] != "HS" ||
		codes[1].text != "6201" || codes[1].attrs["listID"] != "ISIC" {
		t.Errorf("classification codes = %+v", codes)
	}
	wantTextAt(t, nodes, l1+"cac:ClassifiedTaxCategory/cbc:ID", "STANDARD_VAT", "line category")
	wantTextAt(t, nodes, l1+"cac:ClassifiedTaxCategory/cbc:Percent", "7.50", "line percent")
	wantTextAt(t, nodes, l1+"cac:ClassifiedTaxCategory/cac:TaxScheme/cbc:ID", "VAT", "line scheme")
	wantTextsAt(t, nodes, l1+"cac:AdditionalItemProperty/cbc:Name", []string{"product_category", "service_category"}, "names")
	wantTextsAt(t, nodes, l1+"cac:AdditionalItemProperty/cbc:Value", []string{"Hardware", "Software"}, "values")

	one := c
	one.Lines = c.Lines[:1]
	nodes = walkDocument(t, mustRender(t, one))
	wantChildOrder(t, nodes, linePath+"/cac:Item", []string{
		"cbc:Name", "cac:SellersItemIdentification", "cac:CommodityClassification", "cac:CommodityClassification",
		"cac:ClassifiedTaxCategory", "cac:AdditionalItemProperty", "cac:AdditionalItemProperty"})
	wantChildOrder(t, nodes, linePath, []string{
		"cbc:ID", "cbc:InvoicedQuantity", "cbc:LineExtensionAmount", "cac:TaxTotal", "cac:Item", "cac:Price"})
}

func TestRender_ItemWithoutDescriptionHasNoName(t *testing.T) {
	c := completeCanonical(t)
	c.Lines[0].Description = nil
	c.Lines[0].SellersItemIdentification = ublStr("SKU-9")
	nodes := walkDocument(t, mustRender(t, c))
	if n := len(nodesAt(nodes, linePath+"/cac:Item")); n != 2 {
		t.Fatalf("%d cac:Item, want 2 (line 1 holds the seller id)", n)
	}
	wantTextsAt(t, nodes, linePath+"/cac:Item/cbc:Name", []string{"Gadget"}, "line 1 has no name")
	wantTextAt(t, nodes, linePath+"/cac:Item/cac:SellersItemIdentification/cbc:ID", "SKU-9", "seller id")
}

func TestRender_BaseQuantityWithoutUnitPriceIsNotRendered(t *testing.T) {
	c := completeCanonical(t)
	c.Lines[0].UnitPrice = nil
	c.Lines[0].BaseQuantity = ublStr("1.000")
	nodes := walkDocument(t, mustRender(t, c))
	if n := len(nodesAt(nodes, linePath+"/cac:Price")); n != 1 {
		t.Errorf("%d cac:Price, want 1 (line 2 only)", n)
	}
	if n := nodesAt(nodes, linePath+"/cac:Price/cbc:BaseQuantity"); len(n) != 0 {
		t.Error("BaseQuantity rendered without a unit price")
	}
}

func TestRender_PercentWithoutCategoryRendersNoClassifiedTaxCategory(t *testing.T) {
	c := completeCanonical(t)
	c.Lines[0].TaxPercent = ublStr("7.50")
	out := mustRender(t, c)
	nodes := walkDocument(t, out)
	if n := nodesAt(nodes, linePath+"/cac:Item/cac:ClassifiedTaxCategory"); len(n) != 0 {
		t.Error("ClassifiedTaxCategory rendered without a category")
	}
	if bytes.Contains(out, []byte("7.50")) {
		t.Error("the orphan percent rendered somewhere")
	}
}

func TestRender_KindAndPaymentStatusAreNotRendered(t *testing.T) {
	c := completeCanonical(t)
	c.InvoiceKind, c.PaymentStatus = ublStr("B2B"), ublStr("PAID")
	out := string(mustRender(t, c))
	for _, s := range []string{"B2B", "PAID"} {
		if strings.Contains(out, s) {
			t.Errorf("output contains %q", s)
		}
	}
}

func TestRender_LegacyCanonicalIsByteIdenticalToHead(t *testing.T) {
	want, err := os.ReadFile("testdata/legacy_complete.xml")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRender(t, completeCanonical(t)); !bytes.Equal(got, want) {
		t.Errorf("legacy render differs from the 10b2e701 golden:\n%s", got)
	}
}

func TestRender_NRSFixtureIsWellFormedAndDeterministic(t *testing.T) {
	c := nrsCanonical(t)
	a := mustRender(t, c)
	if err := wellFormed(a); err != nil {
		t.Fatalf("not well-formed: %v", err)
	}
	if !bytes.Equal(a, mustRender(t, c)) {
		t.Error("two renders differ")
	}
}
