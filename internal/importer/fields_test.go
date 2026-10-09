package importer

import (
	"slices"
	"sort"
	"testing"
)

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestHeaderFieldOrder_IsTheSevenImportHeaderFieldsWithoutTheNumber(t *testing.T) {
	want := []string{"issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total"}
	if !slices.Equal(headerFieldOrder, want) {
		t.Errorf("headerFieldOrder = %v, want %v", headerFieldOrder, want)
	}
}

func TestNumericFields_AreTheMoneyAndQuantityImportFields(t *testing.T) {
	want := []string{"line_quantity", "line_unit_price", "subtotal", "total", "vat"}
	if got := sortedKeys(numericFields); !slices.Equal(got, want) {
		t.Errorf("numericFields = %v, want %v", got, want)
	}
	for _, absent := range []string{"line_description", "issue_date"} {
		if numericFields[absent] {
			t.Errorf("numericFields holds %q", absent)
		}
	}
}

func TestCanonicalFields_AreTheElevenImportKeys(t *testing.T) {
	want := []string{"buyer_name", "buyer_tin", "currency", "invoice_number", "issue_date",
		"line_description", "line_quantity", "line_unit_price", "subtotal", "total", "vat"}
	if got := sortedKeys(canonicalFields); !slices.Equal(got, want) {
		t.Errorf("canonicalFields = %v, want %v", got, want)
	}
	for _, absent := range []string{"supplier_tin", "line_total"} {
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
	want := []string{"subtotal", "vat", "total", "line_quantity", "line_unit_price"}
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
	want := []string{"line_quantity", "line_unit_price"}
	if !slices.Equal(lineNumericOrder, want) {
		t.Errorf("lineNumericOrder = %v, want %v", lineNumericOrder, want)
	}
}
