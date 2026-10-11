package extraction_test

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

func TestHeaderFields_AreTheNineteenInOrder(t *testing.T) {
	want := []string{"invoice_number", "issue_date", "supplier_tin", "supplier_name",
		"buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total",
		"due_date", "buyer_email", "buyer_telephone", "buyer_street", "buyer_city",
		"buyer_postal_zone", "buyer_country", "buyer_state", "buyer_lga"}
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
