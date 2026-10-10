package importer

import (
	"slices"
	"sort"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
)

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestHeaderFieldOrder_IsTheTwentyOneImportHeaderFieldsWithoutTheNumber(t *testing.T) {
	want := []string{"issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total",
		"invoice_kind", "tax_currency_code", "due_date", "issue_time", "tax_point_date", "payment_status",
		"buyer_email", "buyer_telephone", "buyer_street", "buyer_city", "buyer_postal_zone", "buyer_country", "buyer_state", "buyer_lga"}
	if !slices.Equal(headerFieldOrder, want) {
		t.Errorf("headerFieldOrder = %v, want %v", headerFieldOrder, want)
	}
}

func TestNumericFields_AreTheMoneyAndQuantityImportFields(t *testing.T) {
	want := []string{"line_base_quantity", "line_quantity", "line_tax", "line_tax_percent", "line_total", "line_unit_price", "subtotal", "total", "vat"}
	if got := sortedKeys(numericFields); !slices.Equal(got, want) {
		t.Errorf("numericFields = %v, want %v", got, want)
	}
	for _, absent := range []string{"line_description", "issue_date"} {
		if numericFields[absent] {
			t.Errorf("numericFields holds %q", absent)
		}
	}
}

func TestCanonicalFields_AreTheImportKeys(t *testing.T) {
	want := invoicefields.ImportKeys()
	sort.Strings(want)
	if len(want) != 36 {
		t.Fatalf("ImportKeys() has %d keys, want 36", len(want))
	}
	if got := sortedKeys(canonicalFields); !slices.Equal(got, want) {
		t.Errorf("canonicalFields = %v, want %v", got, want)
	}
	for _, absent := range []string{"supplier_tin", "supplier_name", "supplier_email", "line_line_total"} {
		if canonicalFields[absent] {
			t.Errorf("canonicalFields holds %q", absent)
		}
	}
}

func TestMapperLists_AreTheExtractionProjections(t *testing.T) {
	wantFields := []string{"invoice_number", "issue_date", "supplier_tin", "supplier_name",
		"buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total"}
	if !slices.Equal(mapperFieldNames, wantFields) {
		t.Errorf("mapperFieldNames = %v, want %v", mapperFieldNames, wantFields)
	}
	wantRoles := []string{"description", "quantity", "unit_price", "line_total", "line_tax"}
	if !slices.Equal(mapperLineRoles, wantRoles) {
		t.Errorf("mapperLineRoles = %v, want %v", mapperLineRoles, wantRoles)
	}
}

func TestNumericOrder_IsTheCommaDecimalScanOrder(t *testing.T) {
	want := []string{"subtotal", "vat", "total", "line_quantity", "line_unit_price", "line_total", "line_tax", "line_tax_percent", "line_base_quantity"}
	if !slices.Equal(numericOrder, want) {
		t.Errorf("numericOrder = %v, want %v", numericOrder, want)
	}
}

func TestHeaderNumericOrder_IsTheHeaderOnlySubset(t *testing.T) {
	want := []string{"subtotal", "vat", "total"}
	if !slices.Equal(headerNumericOrder, want) {
		t.Errorf("headerNumericOrder = %v, want %v", headerNumericOrder, want)
	}
}

func TestLineNumericOrder_IsTheLineOnlySubset(t *testing.T) {
	want := []string{"line_quantity", "line_unit_price", "line_total", "line_tax", "line_tax_percent", "line_base_quantity"}
	if !slices.Equal(lineNumericOrder, want) {
		t.Errorf("lineNumericOrder = %v, want %v", lineNumericOrder, want)
	}
}
