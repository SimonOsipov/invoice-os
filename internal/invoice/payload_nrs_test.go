package invoice

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// nonContentTags are the Invoice and LineItem JSON tags that are not MBS content.
var nonContentTags = map[string]bool{
	"id": true, "entity_id": true, "import_batch_id": true, "status": true, "violations": true,
	"rule_set_version_id": true, "created_at": true, "irn": true, "csid": true, "qr_payload": true,
	"rejection_reasons": true, "kept_as_is_at": true, "kept_as_is_by": true, "kept_as_is_reason": true,
	"failure_kind": true, "line_items": true, "-": true,
}

// contentFields lists the struct fields of typ whose JSON tag is content, with their tags.
func contentFields(typ reflect.Type) (idx []int, tags []string) {
	for i := 0; i < typ.NumField(); i++ {
		tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if tag == "" || nonContentTags[tag] {
			continue
		}
		idx = append(idx, i)
		tags = append(tags, tag)
	}
	return idx, tags
}

// setSample sets f to a distinct non-zero value of its type and returns it.
func setSample(t *testing.T, f reflect.Value, tag string) {
	t.Helper()
	switch f.Type() {
	case reflect.TypeOf((*string)(nil)):
		s := "sample-" + tag
		f.Set(reflect.ValueOf(&s))
	case reflect.TypeOf((*time.Time)(nil)):
		d := time.Date(2031, 5, 6, 0, 0, 0, 0, time.UTC)
		f.Set(reflect.ValueOf(&d))
	case reflect.TypeOf(""):
		f.SetString("sample-" + tag)
	case reflect.TypeOf(0):
		f.SetInt(99)
	default:
		t.Fatalf("field %s has unhandled type %s", tag, f.Type())
	}
}

var legacyHeaderTags = map[string]bool{
	"invoice_number": true, "issue_date": true, "currency": true, "subtotal": true, "vat": true, "total": true,
}

func isPartyTag(tag string) (party, key string, ok bool) {
	for _, p := range []string{"supplier", "buyer"} {
		if k, found := strings.CutPrefix(tag, p+"_"); found {
			return p, k, true
		}
	}
	return "", "", false
}

func TestMBSPayload_NRSHeaderKeysPresentWhenSetAbsentWhenNull(t *testing.T) {
	idx, tags := contentFields(reflect.TypeOf(Invoice{}))
	walked := 0
	for n, i := range idx {
		tag := tags[n]
		if _, _, party := isPartyTag(tag); party || legacyHeaderTags[tag] {
			continue
		}
		walked++
		t.Run(tag, func(t *testing.T) {
			var inv Invoice
			if _, absent := MBSPayload(inv)[tag]; absent {
				t.Fatalf("key %q present with the field NULL", tag)
			}
			setSample(t, reflect.ValueOf(&inv).Elem().Field(i), tag)
			want := "sample-" + tag
			if reflect.TypeOf(inv).Field(i).Type == reflect.TypeOf((*time.Time)(nil)) {
				want = "2031-05-06"
			}
			if got := MBSPayload(inv)[tag]; got != want {
				t.Errorf("payload[%q] = %#v, want %q", tag, got, want)
			}
		})
	}
	if walked != 6 {
		t.Fatalf("walked %d NRS header fields, want 6", walked)
	}
}

func TestMBSPayload_DatesUseTheMBSLayout(t *testing.T) {
	d := time.Date(2026, 8, 1, 23, 59, 0, 0, time.FixedZone("x", 3600))
	inv := Invoice{DueDate: &d, TaxPointDate: &d}
	p := MBSPayload(inv)
	if p["due_date"] != "2026-08-01" || p["tax_point_date"] != "2026-08-01" {
		t.Errorf("due_date=%v tax_point_date=%v, want 2026-08-01", p["due_date"], p["tax_point_date"])
	}
}

func TestMBSPayload_PartyKeysNestUnderTheirParty(t *testing.T) {
	idx, tags := contentFields(reflect.TypeOf(Invoice{}))
	walked := 0
	for n, i := range idx {
		party, key, ok := isPartyTag(tags[n])
		if !ok {
			continue
		}
		walked++
		t.Run(tags[n], func(t *testing.T) {
			var inv Invoice
			setSample(t, reflect.ValueOf(&inv).Elem().Field(i), tags[n])
			nested, ok := MBSPayload(inv)[party].(map[string]any)
			if !ok {
				t.Fatalf("payload[%q] missing for a party with only %s set", party, tags[n])
			}
			if len(nested) != 1 || nested[key] != "sample-"+tags[n] {
				t.Errorf("payload[%q] = %#v, want only %q", party, nested, key)
			}
		})
	}
	if walked != 20 {
		t.Fatalf("walked %d party fields, want 20 (4 legacy + 16 NRS)", walked)
	}

	p := MBSPayload(Invoice{BuyerEmail: strPtr("b@x.ng")})
	b, _ := json.Marshal(p["buyer"])
	if string(b) != `{"email":"b@x.ng"}` {
		t.Errorf("buyer = %s, want only email", b)
	}
	if _, ok := p["supplier"]; ok {
		t.Error("supplier present with every supplier field NULL")
	}
}

func TestMBSPayload_NRSLineKeysAndNumbers(t *testing.T) {
	li := LineItem{
		LineNo: 1, TaxCategory: strPtr("STANDARD_VAT"), HSNCode: strPtr("8471"), ISICCode: strPtr("6201"),
		ProductCategory: strPtr("goods"), ServiceCategory: strPtr("svc"), SellersItemIdentification: strPtr("SKU-1"),
		PriceUnit: strPtr("NGN per EA"), TaxPercent: strPtr("7.50"), BaseQuantity: strPtr("1.000"),
	}
	raw := marshalLine(t, li)
	for key, want := range map[string]string{
		"tax_category": `"STANDARD_VAT"`, "hsn_code": `"8471"`, "isic_code": `"6201"`, "product_category": `"goods"`,
		"service_category": `"svc"`, "sellers_item_identification": `"SKU-1"`, "price_unit": `"NGN per EA"`,
		"tax_percent": `7.50`, "base_quantity": `1.000`,
	} {
		if got := string(raw[key]); got != want {
			t.Errorf("line[%q] = %s, want %s", key, got, want)
		}
	}

	li.TaxPercent, li.BaseQuantity = strPtr("x"), strPtr("")
	raw = marshalLine(t, li)
	if string(raw["tax_percent"]) != `"x"` || string(raw["base_quantity"]) != `""` {
		t.Errorf("non-number fallback: tax_percent=%s base_quantity=%s, want raw strings", raw["tax_percent"], raw["base_quantity"])
	}

	raw = marshalLine(t, LineItem{LineNo: 2})
	if len(raw) != 1 {
		t.Errorf("a line with no NRS field has keys %v, want only line_no", raw)
	}
}

func marshalLine(t *testing.T, li LineItem) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(MBSPayload(Invoice{LineItems: []LineItem{li}}))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Lines []map[string]json.RawMessage `json:"line_items"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Lines) != 1 {
		t.Fatalf("decode %s: %v", b, err)
	}
	return out.Lines[0]
}

func TestMBSPayload_TaxSubtotalsFromLines(t *testing.T) {
	inv := Invoice{LineItems: []LineItem{
		{LineNo: 1, TaxCategory: strPtr("STANDARD_VAT"), TaxPercent: strPtr("7.50"), LineTotal: strPtr("100.00"), LineTax: strPtr("7.50")},
		{LineNo: 2, TaxCategory: strPtr("STANDARD_VAT"), TaxPercent: strPtr("7.50"), LineTotal: strPtr("50.00"), LineTax: strPtr("3.75")},
	}}
	b, _ := json.Marshal(MBSPayload(inv))
	var out struct {
		Subtotals []map[string]json.RawMessage `json:"tax_subtotals"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Subtotals) != 1 {
		t.Fatalf("tax_subtotals = %v, want one entry", out.Subtotals)
	}
	for key, want := range map[string]string{
		"tax_category": `"STANDARD_VAT"`, "tax_percent": `7.50`, "taxable_amount": `150.00`, "tax_amount": `11.25`,
	} {
		if got := string(out.Subtotals[0][key]); got != want {
			t.Errorf("tax_subtotals[0][%q] = %s, want %s", key, got, want)
		}
	}

	inv.LineItems[0].TaxCategory, inv.LineItems[1].TaxCategory = nil, nil
	if _, ok := MBSPayload(inv)["tax_subtotals"]; ok {
		t.Error("tax_subtotals present with no categorised line")
	}
}

// legacyHeadPayload is json.Marshal(MBSPayload(legacyFixture())) captured by running the
// same fixture at commit 10b2e701, before any NRS key existed.
const legacyHeadPayload = `{"buyer":{"name":"Beta Ltd","tin":"87654321-0002"},"currency":"NGN","invoice_number":"INV-001","issue_date":"2026-07-01","line_items":[{"description":"Widget","id":"l1","line_no":1,"line_tax":3.75,"line_total":50.00,"quantity":2.000,"unit_price":25.00},{"description":"Gadget","line_no":2,"line_tax":3.75,"line_total":50.00,"quantity":1.000,"unit_price":50.00}],"subtotal":100.00,"supplier":{"name":"Acme Ltd","tin":"12345678-0001"},"total":107.50,"vat":7.50}`

func TestMBSPayload_LegacyInvoiceIsByteIdenticalToHead(t *testing.T) {
	d := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	inv := Invoice{
		InvoiceNumber: "INV-001", IssueDate: &d,
		SupplierTIN: strPtr("12345678-0001"), SupplierName: strPtr("Acme Ltd"),
		BuyerTIN: strPtr("87654321-0002"), BuyerName: strPtr("Beta Ltd"),
		Currency: strPtr("NGN"), Subtotal: strPtr("100.00"), VAT: strPtr("7.50"), Total: strPtr("107.50"),
		LineItems: []LineItem{
			{ID: "l1", LineNo: 1, Description: strPtr("Widget"), Quantity: strPtr("2.000"), UnitPrice: strPtr("25.00"), LineTotal: strPtr("50.00"), LineTax: strPtr("3.75")},
			{LineNo: 2, Description: strPtr("Gadget"), Quantity: strPtr("1.000"), UnitPrice: strPtr("50.00"), LineTotal: strPtr("50.00"), LineTax: strPtr("3.75")},
		},
	}
	b, err := json.Marshal(MBSPayload(inv))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != legacyHeadPayload {
		t.Errorf("payload drifted from 10b2e701:\n got %s\nwant %s", b, legacyHeadPayload)
	}
}

// An empty string is a value, not NULL: the key is present with "" (header, party and line text fields).
func TestMBSPayload_EmptyNRSStringIsPresentNotAbsent(t *testing.T) {
	empty := ""
	hIdx, hTags := contentFields(reflect.TypeOf(Invoice{}))
	checked := 0
	for n, i := range hIdx {
		tag := hTags[n]
		if reflect.TypeOf(Invoice{}).Field(i).Type != reflect.TypeOf((*string)(nil)) || legacyHeaderTags[tag] {
			continue
		}
		var inv Invoice
		reflect.ValueOf(&inv).Elem().Field(i).Set(reflect.ValueOf(&empty))
		p := MBSPayload(inv)
		if party, key, ok := isPartyTag(tag); ok {
			nested, _ := p[party].(map[string]any)
			if v, found := nested[key]; !found || v != "" {
				t.Errorf("%s = %q (present %v), want present empty string", tag, v, found)
			}
		} else if v, found := p[tag]; !found || v != "" {
			t.Errorf("%s = %q (present %v), want present empty string", tag, v, found)
		}
		checked++
	}
	if checked != 24 {
		t.Fatalf("checked %d header and party text fields, want 24 (4 header + 20 party)", checked)
	}

	li := LineItem{LineNo: 1, TaxCategory: &empty, HSNCode: &empty, ISICCode: &empty, ProductCategory: &empty,
		ServiceCategory: &empty, SellersItemIdentification: &empty, PriceUnit: &empty}
	raw := marshalLine(t, li)
	for _, key := range []string{"tax_category", "hsn_code", "isic_code", "product_category", "service_category", "sellers_item_identification", "price_unit"} {
		if string(raw[key]) != `""` {
			t.Errorf("line[%q] = %s, want an empty string", key, raw[key])
		}
	}
}

// Subtotal entries leave out an absent percent or amount, keep non-number text as a string, and keep an empty category.
func TestMBSPayload_TaxSubtotalsEdgeEntries(t *testing.T) {
	inv := Invoice{LineItems: []LineItem{
		{LineNo: 1, TaxCategory: strPtr("A"), TaxPercent: strPtr("7.50"), LineTotal: strPtr("100.00"), LineTax: strPtr("7.50")},
		{LineNo: 2, TaxCategory: strPtr("B"), LineTotal: strPtr("x"), LineTax: strPtr("1.00")},
		{LineNo: 3, TaxCategory: strPtr(""), TaxPercent: strPtr("5"), LineTotal: strPtr("10"), LineTax: nil},
		{LineNo: 4, TaxCategory: strPtr("C"), TaxPercent: strPtr("x"), LineTotal: strPtr("1"), LineTax: strPtr("1")},
	}}
	b, err := json.Marshal(MBSPayload(inv)["tax_subtotals"])
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"tax_amount":7.50,"tax_category":"A","tax_percent":7.50,"taxable_amount":100.00},` +
		`{"tax_amount":1.00,"tax_category":"B"},` +
		`{"tax_category":"","tax_percent":5,"taxable_amount":10.00},` +
		`{"tax_amount":1.00,"tax_category":"C","tax_percent":"x","taxable_amount":1.00}]`
	if string(b) != want {
		t.Errorf("tax_subtotals =\n%s\nwant\n%s", b, want)
	}
}

// Legacy shapes marshal as at 10b2e701 (literals captured there): no NRS key, no tax_subtotals, nesting unchanged.
func TestMBSPayload_LegacyShapesAreByteIdenticalToHead(t *testing.T) {
	d := time.Date(2026, 7, 1, 23, 30, 0, 0, time.FixedZone("x", 3600))
	shapes := map[string]struct {
		inv  Invoice
		want string
	}{
		"empty": {Invoice{InvoiceNumber: "E-1"}, `{"invoice_number":"E-1"}`},
		"party tin only": {Invoice{InvoiceNumber: "P-1", SupplierTIN: strPtr("12345678-0001"), BuyerName: strPtr("Beta")},
			`{"buyer":{"name":"Beta"},"invoice_number":"P-1","supplier":{"tin":"12345678-0001"}}`},
		"empty strings": {Invoice{InvoiceNumber: "S-1", SupplierTIN: strPtr(""), SupplierName: strPtr(""), BuyerTIN: strPtr(""), BuyerName: strPtr(""),
			Currency: strPtr(""), Subtotal: strPtr(""), VAT: strPtr(""), Total: strPtr(""),
			LineItems: []LineItem{{LineNo: 1, Description: strPtr(""), Quantity: strPtr(""), UnitPrice: strPtr(""), LineTotal: strPtr(""), LineTax: strPtr("")}}},
			`{"buyer":{"name":"","tin":""},"currency":"","invoice_number":"S-1","line_items":[{"description":"","line_no":1,"line_tax":"","line_total":"","quantity":"","unit_price":""}],"subtotal":"","supplier":{"name":"","tin":""},"total":"","vat":""}`},
		"non numeric": {Invoice{InvoiceNumber: "N-1", Subtotal: strPtr("abc"), VAT: strPtr("NaN"), Total: strPtr("1e5"),
			LineItems: []LineItem{{LineNo: 1, Quantity: strPtr("-0"), UnitPrice: strPtr("1e5"), LineTotal: strPtr("0x10"), LineTax: strPtr(".5")}}},
			`{"invoice_number":"N-1","line_items":[{"line_no":1,"line_tax":".5","line_total":"0x10","quantity":-0,"unit_price":1e5}],"subtotal":"abc","total":1e5,"vat":"NaN"}`},
		"gappy line numbers": {Invoice{InvoiceNumber: "G-1", IssueDate: &d, Currency: strPtr("USD"),
			LineItems: []LineItem{{ID: "b", LineNo: 3, Description: strPtr("c")}, {LineNo: 1}, {LineNo: 7, LineTax: strPtr("1.00")}}},
			`{"currency":"USD","invoice_number":"G-1","issue_date":"2026-07-01","line_items":[{"description":"c","id":"b","line_no":3},{"line_no":1},{"line_no":7,"line_tax":1.00}]}`},
	}
	for name, s := range shapes {
		t.Run(name, func(t *testing.T) {
			b, err := json.Marshal(MBSPayload(s.inv))
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != s.want {
				t.Errorf("payload drifted from 10b2e701:\n got %s\nwant %s", b, s.want)
			}
		})
	}
}
