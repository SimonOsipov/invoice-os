package invoicefields

import (
	"reflect"
	"strings"
	"testing"
)

func TestAll_HeaderFieldsPrecedeLineFields(t *testing.T) {
	var headers, lines int
	for _, f := range All {
		if f.Line {
			lines++
			continue
		}
		if lines > 0 {
			t.Errorf("header field %q follows a line field", f.Key)
		}
		headers++
	}
	if headers == 0 || lines == 0 {
		t.Fatalf("want both kinds, got %d header and %d line fields", headers, lines)
	}
}

func TestImportKeys_AreTheThirtySixImportFieldsInOrder(t *testing.T) {
	want := []string{
		"invoice_number", "issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total",
		"invoice_kind", "tax_currency_code", "due_date", "issue_time", "tax_point_date", "payment_status",
		"buyer_email", "buyer_telephone", "buyer_street", "buyer_city", "buyer_postal_zone", "buyer_country", "buyer_state", "buyer_lga",
		"line_description", "line_quantity", "line_unit_price", "line_total", "line_tax",
		"line_tax_category", "line_hsn_code", "line_isic_code", "line_product_category", "line_service_category",
		"line_sellers_item_identification", "line_price_unit", "line_tax_percent", "line_base_quantity",
	}
	if got := ImportKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ImportKeys() = %v, want %v", got, want)
	}
}

func TestImportKeys_LeaveOutTheSupplierFields(t *testing.T) {
	for _, k := range ImportKeys() {
		if strings.HasPrefix(k, "supplier_") || strings.HasPrefix(k, "line_line_") {
			t.Errorf("ImportKeys() holds %q", k)
		}
	}
}

func TestImportKey_PrefixesALineFieldOnce(t *testing.T) {
	byKey := map[string]Field{}
	for _, f := range All {
		byKey[f.Key] = f
	}
	for key, want := range map[string]string{
		"buyer_tin":   "buyer_tin",
		"description": "line_description",
		"unit_price":  "line_unit_price",
		"line_total":  "line_total",
		"line_tax":    "line_tax",
		"tax_percent": "line_tax_percent",
	} {
		if got := byKey[key].ImportKey(); got != want {
			t.Errorf("%s ImportKey() = %q, want %q", key, got, want)
		}
	}
}

func TestExtractHeaderKeys_AreTheTenInOrder(t *testing.T) {
	want := []string{"invoice_number", "issue_date", "supplier_tin", "supplier_name", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total"}
	if got := ExtractHeaderKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractHeaderKeys() = %v, want %v", got, want)
	}
}

func TestExtractLineKeys_AreTheFiveInOrder(t *testing.T) {
	want := []string{"description", "quantity", "unit_price", "line_total", "line_tax"}
	if got := ExtractLineKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractLineKeys() = %v, want %v", got, want)
	}
}

func TestAll_KeysAndImportKeysAreUnique(t *testing.T) {
	keys, imports := map[string]bool{}, map[string]bool{}
	for _, f := range All {
		if keys[f.Key] {
			t.Errorf("duplicate key %q", f.Key)
		}
		keys[f.Key] = true
	}
	for _, k := range ImportKeys() {
		if imports[k] {
			t.Errorf("duplicate import key %q", k)
		}
		imports[k] = true
	}
}

func TestAll_TypesAreTheClosedSet(t *testing.T) {
	for _, f := range All {
		switch f.Type {
		case Text, Date, Money, Quantity:
		default:
			t.Errorf("field %q has type %q", f.Key, f.Type)
		}
	}
}

func TestAll_RequiredAndFormKeyAreScoped(t *testing.T) {
	forms := map[string]bool{}
	for _, f := range All {
		if f.Required && !f.Import {
			t.Errorf("%q is Required but not Import", f.Key)
		}
		if f.FormKey == "" {
			continue
		}
		if f.Line {
			t.Errorf("%q has a FormKey but is a line field", f.Key)
		}
		if forms[f.FormKey] {
			t.Errorf("duplicate FormKey %q", f.FormKey)
		}
		forms[f.FormKey] = true
	}
}

func TestProjections_ReturnANewSlice(t *testing.T) {
	for name, fn := range map[string]func() []string{
		"ImportKeys":        ImportKeys,
		"ExtractHeaderKeys": ExtractHeaderKeys,
		"ExtractLineKeys":   ExtractLineKeys,
	} {
		first := fn()[0]
		r := fn()
		r[0] = "x"
		if got := fn()[0]; got != first {
			t.Errorf("%s: first element after mutation = %q, want %q", name, got, first)
		}
	}
}
