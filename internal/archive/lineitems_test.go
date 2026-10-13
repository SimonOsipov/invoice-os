package archive

import (
	"slices"
	"strings"
	"testing"
)

func TestLineItemsCSVHeader_IsKeyLegacyThenNRSColumns(t *testing.T) {
	want := []string{
		"invoice_id", "invoice_number", "line_no", "description", "quantity", "unit_price", "line_total", "line_tax",
		"tax_category", "hsn_code", "isic_code", "product_category", "service_category",
		"sellers_item_identification", "price_unit", "tax_percent", "base_quantity",
	}
	if !slices.Equal(lineItemsCSVHeader, want) {
		t.Errorf("lineItemsCSVHeader = %v, want %v", lineItemsCSVHeader, want)
	}
}

func TestLineItemsSQL_ContainsNoJoinAgainstInvoices(t *testing.T) {
	for name, sql := range map[string]string{"selectLineItemsSQL": selectLineItemsSQL, "countLineItemsSQL": countLineItemsSQL} {
		for _, banned := range []string{"invoices", "tenant_id"} {
			if strings.Contains(sql, banned) {
				t.Errorf("%s contains %q, want none (invoice_number comes from invoiceNumbers; RLS isolates the tenant):\n%s", name, banned, sql)
			}
		}
	}
}
