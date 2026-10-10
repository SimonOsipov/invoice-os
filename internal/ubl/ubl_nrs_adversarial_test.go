package ubl_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/ubl"
)

func lineWith(f func(*submission.CanonicalLine)) submission.Canonical {
	c := submission.Canonical{
		InvoiceNumber: "N-1", IssueDate: ptrTime(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)),
		Currency: ublStr("NGN"),
		Supplier: submission.Party{Name: ublStr("S")}, Buyer: submission.Party{Name: ublStr("B")},
		Lines: []submission.CanonicalLine{{LineNo: 1}},
	}
	f(&c.Lines[0])
	return c
}

func ptrTime(t time.Time) *time.Time { return &t }

func childNames(nodes []xmlNode, parent string) []string {
	var out []string
	for _, n := range nodes {
		if rest, ok := strings.CutPrefix(n.path, parent+"/"); ok && !strings.Contains(rest, "/") {
			out = append(out, rest)
		}
	}
	return out
}

func TestRender_HeaderNRSElementsEachAloneSitInTheirSlot(t *testing.T) {
	due := time.Date(2026, 9, 5, 0, 30, 0, 0, time.FixedZone("WAT", 3600))
	point := time.Date(2026, 8, 7, 23, 30, 0, 0, time.FixedZone("EST", -5*3600))
	cases := []struct {
		name string
		set  func(*submission.Canonical)
		want []string
		text string
	}{
		{"issue_time", func(c *submission.Canonical) { c.IssueTime = ublStr("14:30:00.123456") },
			[]string{"cbc:ID", "cbc:IssueDate", "cbc:IssueTime", "cbc:InvoiceTypeCode"}, "14:30:00.123456"},
		{"due_date", func(c *submission.Canonical) { c.DueDate = &due },
			[]string{"cbc:ID", "cbc:IssueDate", "cbc:DueDate", "cbc:InvoiceTypeCode"}, "2026-09-05"},
		{"tax_point_date", func(c *submission.Canonical) { c.TaxPointDate = &point },
			[]string{"cbc:ID", "cbc:IssueDate", "cbc:InvoiceTypeCode", "cbc:TaxPointDate", "cbc:DocumentCurrencyCode"}, "2026-08-07"},
		{"tax_currency_code", func(c *submission.Canonical) { c.TaxCurrencyCode = ublStr("USD") },
			[]string{"cbc:ID", "cbc:InvoiceTypeCode", "cbc:DocumentCurrencyCode", "cbc:TaxCurrencyCode", "cac:AccountingSupplierParty"}, "USD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := completeCanonical(t)
			tc.set(&c)
			nodes := walkDocument(t, mustRender(t, c))
			got := childNames(nodes, "Invoice")
			var kept []string
			for _, g := range got {
				for _, w := range tc.want {
					if g == w {
						kept = append(kept, g)
					}
				}
			}
			if strings.Join(kept, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("root children %v, want the subsequence %v", got, tc.want)
			}
			slot := map[string]string{"issue_time": "IssueTime", "due_date": "DueDate", "tax_point_date": "TaxPointDate", "tax_currency_code": "TaxCurrencyCode"}[tc.name]
			wantTextAt(t, nodes, "Invoice/cbc:"+slot, tc.text, "stored value, own-location date")
			for _, other := range []string{"IssueTime", "DueDate", "TaxPointDate", "TaxCurrencyCode"} {
				if other != slot && len(nodesAt(nodes, "Invoice/cbc:"+other)) != 0 {
					t.Errorf("cbc:%s rendered although only %s is stored", other, slot)
				}
			}
		})
	}
}

func TestRender_EachPartyNRSFieldAloneRendersItsOwnElementOnTheRightParty(t *testing.T) {
	cases := []struct {
		name      string
		set       func(*submission.Party)
		container string
		path      string
	}{
		{"street", func(p *submission.Party) { p.Street = ublStr("V") }, "cac:PostalAddress", "cbc:StreetName"},
		{"city", func(p *submission.Party) { p.City = ublStr("V") }, "cac:PostalAddress", "cbc:CityName"},
		{"postal_zone", func(p *submission.Party) { p.PostalZone = ublStr("V") }, "cac:PostalAddress", "cbc:PostalZone"},
		{"state", func(p *submission.Party) { p.State = ublStr("V") }, "cac:PostalAddress", "cbc:CountrySubentityCode"},
		{"lga", func(p *submission.Party) { p.LGA = ublStr("V") }, "cac:PostalAddress", "cbc:District"},
		{"country", func(p *submission.Party) { p.Country = ublStr("V") }, "cac:PostalAddress", "cac:Country/cbc:IdentificationCode"},
		{"telephone", func(p *submission.Party) { p.Telephone = ublStr("V") }, "cac:Contact", "cbc:Telephone"},
		{"email", func(p *submission.Party) { p.Email = ublStr("V") }, "cac:Contact", "cbc:ElectronicMail"},
	}
	for _, tc := range cases {
		for _, side := range []string{"supplier", "buyer"} {
			t.Run(tc.name+"/"+side, func(t *testing.T) {
				c := completeCanonical(t)
				self, other := supplierPath, buyerPath
				p := &c.Supplier
				if side == "buyer" {
					self, other, p = buyerPath, supplierPath, &c.Buyer
				}
				tc.set(p)
				nodes := walkDocument(t, mustRender(t, c))
				wantTextAt(t, nodes, self+"/"+tc.container+"/"+tc.path, "V", "field -> element")
				if kids := childNames(nodes, self+"/"+tc.container); len(kids) != 1 {
					t.Errorf("%s children = %v, want only the stored one", tc.container, kids)
				}
				for _, ct := range []string{"cac:PostalAddress", "cac:Contact"} {
					if ct != tc.container && len(nodesAt(nodes, self+"/"+ct)) != 0 {
						t.Errorf("%s rendered on the %s with only %s stored", ct, side, tc.name)
					}
					if len(nodesAt(nodes, other+"/"+ct)) != 0 {
						t.Errorf("the other party rendered %s", ct)
					}
				}
			})
		}
	}
}

func TestRender_NRSInnerElementOrder(t *testing.T) {
	// one subtotal and one line keep each parent path unambiguous for wantChildOrder
	c := nrsCanonical(t)
	c.TaxSubtotals, c.Lines = c.TaxSubtotals[:1], c.Lines[:1]
	nodes := walkDocument(t, mustRender(t, c))
	sub := "Invoice/cac:TaxTotal/cac:TaxSubtotal"
	wantChildOrder(t, nodes, sub, []string{"cbc:TaxableAmount", "cbc:TaxAmount", "cac:TaxCategory"})
	wantChildOrder(t, nodes, sub+"/cac:TaxCategory", []string{"cbc:ID", "cbc:Percent", "cac:TaxScheme"})
	wantChildOrder(t, nodes, linePath+"/cac:Price", []string{"cbc:PriceAmount", "cbc:BaseQuantity"})
	wantChildOrder(t, nodes, linePath+"/cac:Item/cac:AdditionalItemProperty", []string{"cbc:Name", "cbc:Value"})
	wantChildOrder(t, nodes, linePath+"/cac:Item/cac:SellersItemIdentification", []string{"cbc:ID"})
}

func TestRender_ItemRendersForAnySingleMemberAndNeverForNonItemFields(t *testing.T) {
	item := []struct {
		name  string
		set   func(*submission.CanonicalLine)
		child string
	}{
		{"description", func(l *submission.CanonicalLine) { l.Description = ublStr("V") }, "cbc:Name"},
		{"sellers_id", func(l *submission.CanonicalLine) { l.SellersItemIdentification = ublStr("V") }, "cac:SellersItemIdentification"},
		{"hsn", func(l *submission.CanonicalLine) { l.HSNCode = ublStr("V") }, "cac:CommodityClassification"},
		{"isic", func(l *submission.CanonicalLine) { l.ISICCode = ublStr("V") }, "cac:CommodityClassification"},
		{"tax_category", func(l *submission.CanonicalLine) { l.TaxCategory = ublStr("V") }, "cac:ClassifiedTaxCategory"},
		{"product_category", func(l *submission.CanonicalLine) { l.ProductCategory = ublStr("V") }, "cac:AdditionalItemProperty"},
		{"service_category", func(l *submission.CanonicalLine) { l.ServiceCategory = ublStr("V") }, "cac:AdditionalItemProperty"},
	}
	for _, tc := range item {
		t.Run(tc.name, func(t *testing.T) {
			nodes := walkDocument(t, mustRender(t, lineWith(tc.set)))
			if kids := childNames(nodes, linePath+"/cac:Item"); len(kids) != 1 || kids[0] != tc.child {
				t.Errorf("cac:Item children = %v, want only [%s]", kids, tc.child)
			}
		})
	}
	for _, tc := range []struct {
		name string
		set  func(*submission.CanonicalLine)
	}{
		{"tax_percent_only", func(l *submission.CanonicalLine) { l.TaxPercent = ublStr("7.50") }},
		{"base_quantity_only", func(l *submission.CanonicalLine) { l.BaseQuantity = ublStr("1.000") }},
		{"price_unit_only", func(l *submission.CanonicalLine) { l.PriceUnit = ublStr("EA") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRender(t, lineWith(tc.set))
			nodes := walkDocument(t, out)
			if kids := childNames(nodes, linePath); len(kids) != 1 || kids[0] != "cbc:ID" {
				t.Errorf("line children = %v, want only [cbc:ID]", kids)
			}
			for _, s := range []string{"7.50", "1.000", "EA"} {
				if bytes.Contains(out, []byte(s)) {
					t.Errorf("orphan %q rendered", s)
				}
			}
		})
	}
}

func TestRender_UnitCodeAndBaseQuantityRules(t *testing.T) {
	type want struct {
		qtyUnit    string
		hasQty     bool
		hasBase    bool
		baseUnit   string
		hasPrice   bool
		unitAttrOK bool
	}
	cases := []struct {
		name string
		set  func(*submission.CanonicalLine)
		want want
	}{
		{"unit_with_qty_and_price_and_base", func(l *submission.CanonicalLine) {
			l.PriceUnit, l.Quantity, l.UnitPrice, l.BaseQuantity = ublStr("KGM"), ublStr("2"), ublStr("5.00"), ublStr("1.000")
		}, want{"KGM", true, true, "KGM", true, true}},
		{"unit_without_qty", func(l *submission.CanonicalLine) {
			l.PriceUnit, l.UnitPrice, l.BaseQuantity = ublStr("KGM"), ublStr("5.00"), ublStr("1.000")
		}, want{"", false, true, "KGM", true, true}},
		{"unit_without_base", func(l *submission.CanonicalLine) {
			l.PriceUnit, l.Quantity, l.UnitPrice = ublStr("KGM"), ublStr("2"), ublStr("5.00")
		}, want{"KGM", true, false, "", true, true}},
		{"base_without_unit", func(l *submission.CanonicalLine) {
			l.Quantity, l.UnitPrice, l.BaseQuantity = ublStr("2"), ublStr("5.00"), ublStr("1.000")
		}, want{"", true, true, "", true, true}},
		{"empty_unit_has_no_attr", func(l *submission.CanonicalLine) {
			l.PriceUnit, l.Quantity, l.UnitPrice, l.BaseQuantity = ublStr(""), ublStr("2"), ublStr("5.00"), ublStr("1.000")
		}, want{"", true, true, "", true, true}},
		{"unit_with_no_price", func(l *submission.CanonicalLine) {
			l.PriceUnit, l.Quantity, l.BaseQuantity = ublStr("KGM"), ublStr("2"), ublStr("1.000")
		}, want{"KGM", true, false, "", false, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes := walkDocument(t, mustRender(t, lineWith(tc.set)))
			q := nodesAt(nodes, linePath+"/cbc:InvoicedQuantity")
			if (len(q) == 1) != tc.want.hasQty {
				t.Fatalf("%d InvoicedQuantity, want present=%v", len(q), tc.want.hasQty)
			}
			if tc.want.hasQty {
				if got, ok := q[0].attrs["unitCode"]; got != tc.want.qtyUnit || ok != (tc.want.qtyUnit != "") {
					t.Errorf("InvoicedQuantity unitCode = %q (present %v), want %q", got, ok, tc.want.qtyUnit)
				}
			}
			if (len(nodesAt(nodes, linePath+"/cac:Price")) == 1) != tc.want.hasPrice {
				t.Errorf("cac:Price present != %v", tc.want.hasPrice)
			}
			b := nodesAt(nodes, linePath+"/cac:Price/cbc:BaseQuantity")
			if (len(b) == 1) != tc.want.hasBase {
				t.Fatalf("%d BaseQuantity, want present=%v", len(b), tc.want.hasBase)
			}
			if tc.want.hasBase {
				if got, ok := b[0].attrs["unitCode"]; got != tc.want.baseUnit || ok != (tc.want.baseUnit != "") {
					t.Errorf("BaseQuantity unitCode = %q (present %v), want %q", got, ok, tc.want.baseUnit)
				}
			}
		})
	}
}

func TestRender_ClassificationCodesEachAlone(t *testing.T) {
	for _, tc := range []struct{ name, list string }{{"hsn", "HS"}, {"isic", "ISIC"}} {
		t.Run(tc.name, func(t *testing.T) {
			c := lineWith(func(l *submission.CanonicalLine) {
				if tc.name == "hsn" {
					l.HSNCode = ublStr("C-1")
				} else {
					l.ISICCode = ublStr("C-1")
				}
			})
			nodes := walkDocument(t, mustRender(t, c))
			code := oneAt(t, nodes, linePath+"/cac:Item/cac:CommodityClassification/cbc:ItemClassificationCode")
			if code.text != "C-1" || code.attrs["listID"] != tc.list {
				t.Errorf("code = %q listID %q, want C-1 / %s", code.text, code.attrs["listID"], tc.list)
			}
		})
	}
	c := lineWith(func(l *submission.CanonicalLine) { l.ServiceCategory = ublStr("Svc") })
	nodes := walkDocument(t, mustRender(t, c))
	wantTextsAt(t, nodes, linePath+"/cac:Item/cac:AdditionalItemProperty/cbc:Name", []string{"service_category"}, "service alone")
}

func TestRender_TaxTotalWithNoRenderableSubtotalHoldsOnlyTheVAT(t *testing.T) {
	c := nrsCanonical(t)
	for i := range c.TaxSubtotals {
		c.TaxSubtotals[i].TaxAmount = nil
	}
	nodes := walkDocument(t, mustRender(t, c))
	wantChildOrder(t, nodes, "Invoice/cac:TaxTotal", []string{"cbc:TaxAmount"})

	c = nrsCanonical(t)
	c.TaxSubtotals = append([]submission.TaxSubtotal{{Category: "A", TaxAmount: ublStr("1.00")}, {Category: "B"}, {Category: "C", TaxAmount: ublStr("2.00")}}, c.TaxSubtotals...)
	nodes = walkDocument(t, mustRender(t, c))
	wantTextsAt(t, nodes, "Invoice/cac:TaxTotal/cac:TaxSubtotal/cac:TaxCategory/cbc:ID",
		[]string{"A", "C", "STANDARD_VAT", "ZERO_VAT"}, "slice order kept, incomplete one skipped")
}

func TestRender_AC6KindAndPaymentStatusNeverAppearWithEverythingElseStored(t *testing.T) {
	c := nrsCanonical(t)
	c.InvoiceKind, c.PaymentStatus = ublStr("KINDSENTINEL_Qz9x"), ublStr("PAYSENTINEL_Qz9x")
	out := mustRender(t, c)
	for _, s := range []string{"KINDSENTINEL", "PAYSENTINEL", "Qz9x"} {
		if bytes.Contains(out, []byte(s)) {
			t.Errorf("output contains %q", s)
		}
	}
	if !bytes.Contains(out, []byte("14:30:00")) || !bytes.Contains(out, []byte("STANDARD_VAT")) {
		t.Error("the NRS fixture did not render; the absence checks above are vacuous")
	}
}

// Every NRS free-text slot, element and attribute, keeps &, <, ", ', ]]> and whitespace
// exactly and the document stays well-formed.
func TestRender_NRSFreeTextRoundTripsThroughEscaping(t *testing.T) {
	nasty := "a&b <c> \"d\" 'e' ]]> t\tx\ny\rz &amp; &#x41;"
	c := nrsCanonical(t)
	set := func(p *submission.Party) {
		p.Street, p.City, p.PostalZone, p.State, p.LGA = ublStr(nasty), ublStr(nasty), ublStr(nasty), ublStr(nasty), ublStr(nasty)
		p.Country, p.Telephone, p.Email = ublStr(nasty), ublStr(nasty), ublStr(nasty)
	}
	set(&c.Supplier)
	set(&c.Buyer)
	c.IssueTime, c.TaxCurrencyCode = ublStr(nasty), ublStr(nasty)
	c.TaxSubtotals = []submission.TaxSubtotal{{Category: nasty, Percent: ublStr(nasty), TaxableAmount: ublStr(nasty), TaxAmount: ublStr(nasty)}}
	l := &c.Lines[0]
	l.HSNCode, l.ISICCode, l.ProductCategory, l.ServiceCategory = ublStr(nasty), ublStr(nasty), ublStr(nasty), ublStr(nasty)
	l.SellersItemIdentification, l.PriceUnit, l.BaseQuantity = ublStr(nasty), ublStr(nasty), ublStr(nasty)
	l.TaxCategory, l.TaxPercent = ublStr(nasty), ublStr(nasty)

	out := mustRender(t, c)
	if err := wellFormed(out); err != nil {
		t.Fatalf("not well-formed: %v", err)
	}
	nodes := walkDocument(t, out)
	for _, base := range []string{supplierPath, buyerPath} {
		for _, p := range []string{
			"cac:PostalAddress/cbc:StreetName", "cac:PostalAddress/cbc:CityName", "cac:PostalAddress/cbc:PostalZone",
			"cac:PostalAddress/cbc:CountrySubentityCode", "cac:PostalAddress/cbc:District",
			"cac:PostalAddress/cac:Country/cbc:IdentificationCode", "cac:Contact/cbc:Telephone", "cac:Contact/cbc:ElectronicMail",
		} {
			wantTextAt(t, nodes, base+"/"+p, nasty, "party free text")
		}
	}
	it := linePath + "/cac:Item/"
	for _, p := range []string{
		"Invoice/cbc:IssueTime", "Invoice/cbc:TaxCurrencyCode",
		"Invoice/cac:TaxTotal/cac:TaxSubtotal/cbc:TaxableAmount", "Invoice/cac:TaxTotal/cac:TaxSubtotal/cbc:TaxAmount",
		"Invoice/cac:TaxTotal/cac:TaxSubtotal/cac:TaxCategory/cbc:ID", "Invoice/cac:TaxTotal/cac:TaxSubtotal/cac:TaxCategory/cbc:Percent",
		it + "cac:SellersItemIdentification/cbc:ID", it + "cac:ClassifiedTaxCategory/cbc:ID", it + "cac:ClassifiedTaxCategory/cbc:Percent",
		linePath + "/cac:Price/cbc:BaseQuantity",
	} {
		wantTextAt(t, nodes, p, nasty, "free text")
	}
	wantTextsAt(t, nodes, it+"cac:CommodityClassification/cbc:ItemClassificationCode", []string{nasty, nasty}, "codes")
	wantTextsAt(t, nodes, it+"cac:AdditionalItemProperty/cbc:Value", []string{nasty, nasty}, "properties")
	for _, p := range []string{linePath + "/cbc:InvoicedQuantity", linePath + "/cac:Price/cbc:BaseQuantity"} {
		if got := nodesAt(nodes, p)[0].attrs["unitCode"]; got != nasty {
			t.Errorf("%s unitCode = %q, want the nasty text round-tripped", p, got)
		}
	}
}

// Postgres text allows C0 controls other than NUL. Go substitutes U+FFFD, so the document stays
// well-formed; the stored value is not preserved.
func TestRender_NRSControlCharactersKeepTheDocumentWellFormed(t *testing.T) {
	bad := "a\u0001b\u0008c\u000bd\u000ce\u001ff￾g￿"
	c := nrsCanonical(t)
	c.Buyer.Street, c.Buyer.Email, c.IssueTime = ublStr(bad), ublStr(bad), ublStr(bad)
	c.Lines[0].HSNCode, c.Lines[0].PriceUnit = ublStr(bad), ublStr(bad)
	c.Lines[0].SellersItemIdentification = ublStr(bad)
	c.TaxSubtotals[0].Category = bad

	out := mustRender(t, c)
	if err := wellFormed(out); err != nil {
		t.Fatalf("not well-formed: %v\n%s", err, out)
	}
	for _, r := range []rune{0x01, 0x08, 0x0b, 0x0c, 0x1f} {
		if bytes.ContainsRune(out, r) {
			t.Errorf("output carries raw control rune %U", r)
		}
	}
	if !bytes.Contains(out, []byte("a�b")) {
		t.Error("the printable parts around a control rune did not survive")
	}
}

func TestRender_NeverMutatesTheNRSCanonical(t *testing.T) {
	c := nrsCanonical(t)
	snap := func() string {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	before := snap()
	if !strings.Contains(before, "STANDARD_VAT") || !strings.Contains(before, "SKU-1") {
		t.Fatal("snapshot lacks the NRS fields; the comparison would be vacuous")
	}
	mustRender(t, c)
	if after := snap(); after != before {
		t.Errorf("Render mutated its input\nbefore: %s\n after: %s", before, after)
	}
}

func TestMissing_IgnoresEveryNRSField(t *testing.T) {
	nrs := nrsCanonical(t)
	c := submission.Canonical{
		IssueTime: nrs.IssueTime, DueDate: nrs.DueDate, TaxPointDate: nrs.TaxPointDate, TaxCurrencyCode: nrs.TaxCurrencyCode,
		InvoiceKind: nrs.InvoiceKind, PaymentStatus: nrs.PaymentStatus, TaxSubtotals: nrs.TaxSubtotals,
		Supplier: submission.Party{Street: nrs.Supplier.Street, Email: nrs.Supplier.Email, Country: nrs.Supplier.Country},
		Buyer:    submission.Party{Street: nrs.Buyer.Street, Email: nrs.Buyer.Email, Country: nrs.Buyer.Country},
	}
	want := []string{"an invoice number", "an issue date", "a currency", "a supplier name", "a buyer name", "at least one line item"}
	got := ubl.Missing(c)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Missing = %v, want %v", got, want)
	}
	if _, err := ubl.Render(c); !errors.Is(err, ubl.ErrIncomplete) {
		t.Errorf("Render err = %v, want ErrIncomplete", err)
	}
	if m := ubl.Missing(nrsCanonical(t)); m != nil {
		t.Errorf("Missing(complete NRS) = %v, want nil", m)
	}
}
