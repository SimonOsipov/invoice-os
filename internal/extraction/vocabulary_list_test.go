package extraction_test

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

func TestHeaderFields_AreTheTenInOrder(t *testing.T) {
	want := []string{"invoice_number", "issue_date", "supplier_tin", "supplier_name",
		"buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total"}
	if !slices.Equal(extraction.HeaderFields, want) {
		t.Errorf("HeaderFields = %v, want %v", extraction.HeaderFields, want)
	}
}

func TestLineRoles_AreTheFiveRoleConstantsInEmitOrder(t *testing.T) {
	consts := []string{extraction.LineRoleDescription, extraction.LineRoleQuantity,
		extraction.LineRoleUnitPrice, extraction.LineRoleLineTotal, extraction.LineRoleLineTax}
	if !slices.Equal(extraction.LineRoles, consts) {
		t.Errorf("LineRoles = %v, want the LineRole* constants %v", extraction.LineRoles, consts)
	}
	literal := []string{"description", "quantity", "unit_price", "line_total", "line_tax"}
	if !slices.Equal(extraction.LineRoles, literal) {
		t.Errorf("LineRoles = %v, want %v", extraction.LineRoles, literal)
	}
}
