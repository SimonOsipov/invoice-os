// M4-15-03: characterization pin for column-count anomalies at the
// Service.Import layer -- a row WIDER than the header (extra trailing
// cells) or SHORTER than the header (a missing trailing cell) must never
// trigger wholesale row rejection. This is a PIN of the ALREADY-SHIPPED
// behavior, not a new rule: fieldValue/cellAt bounds-check every cell read
// (`if idx < len(row)`, service.go), so an extra cell past colIndex's widest
// mapped field is simply never read (ignored), and a mapped field whose
// index falls past a short row's length reads as "" -- for a numeric field
// (subtotal here) that normalizes to nil, i.e. NULL, exactly like a blank
// cell (TestServiceImport_BlankSubtotalCellCommitsAsNullNotQuarantined in
// service_adversarial_test.go). Out of scope (deliberately, per the story):
// adding a "wrong column count -> reject" rule. If either assertion below
// doesn't hold, that is a real finding, not something to paper over.
//
// Determinism: the header's TRAILING column is Subtotal, so INV-SHORT's
// only out-of-range mapped field is subtotal (nullable-at-import) -- no
// other field is affected, isolating the anomaly to exactly the field this
// test characterizes.
//
// Reuses newTestService/dbTestPools/seedTenant/seedEntity/stdMapping and the
// invoice*/count* read-back helpers already defined in service_test.go (same
// package, not redefined).
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/importer/... -run ColumnCountAnomalies
package importer

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// anomalyHeader is stdHeader's same 11 fields reordered so Subtotal is the
// TRAILING column -- stdMapping resolves fields by header TEXT (not
// position, see resolveMapping), so it works unchanged against this header.
var anomalyHeader = []string{
	"Invoice No", "Issue Date", "Buyer TIN", "Buyer", "Currency",
	"VAT", "Total", "Item", "Qty", "Unit Price", "Subtotal",
}

func TestServiceImport_ColumnCountAnomaliesDegradeGracefully(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-15-03 tenant")
	entityID := seedEntity(t, super, tenantID, "M4-15-03 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// INV-WIDE: 11 valid cells (anomalyHeader order) + 2 EXTRA trailing
	// cells beyond the header width. The extras must be silently ignored --
	// no field's colIndex points past index 10, so nothing ever reads them.
	wideRow := []string{
		"INV-WIDE", "2026-01-10", "T1", "B1", "NGN",
		"1.00", "11.00", "Item1", "1", "10.00", "10.00",
		"x", "y",
	}

	// INV-SHORT: a complete, valid invoice OMITTING only the trailing
	// Subtotal cell (10 cells, header has 11) -- the mapped subtotal read
	// (colIndex["subtotal"]==10) falls out of range, so fieldValue returns
	// nil/NULL for subtotal alone. Nothing else is missing.
	shortRow := []string{
		"INV-SHORT", "2026-01-10", "T2", "B2", "NGN",
		"1.00", "11.00", "Item1", "1", "10.00",
	}

	res, err := svc.Import(c, entityID, stdMapping, anomalyHeader, [][]string{wideRow, shortRow}, false)
	if err != nil {
		t.Fatalf("Import: %v (column-count anomalies must never trigger wholesale rejection)", err)
	}

	if res.Status != "completed" {
		t.Errorf("Status = %q, want %q", res.Status, "completed")
	}
	if res.RowsTotal != 2 {
		t.Errorf("RowsTotal = %d, want 2", res.RowsTotal)
	}
	if res.ReadyInvoices != 2 || res.QuarantinedInvoices != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (2,0) -- a column-count mismatch must degrade gracefully (bounds-checked reads), not quarantine", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %+v, want none", res.Errors)
	}

	wideID := invoiceIDByNumber(t, super, entityID, "INV-WIDE")
	shortID := invoiceIDByNumber(t, super, entityID, "INV-SHORT")

	if got := invoiceSubtotal(t, super, shortID); got != nil {
		t.Errorf("INV-SHORT persisted subtotal = %q, want nil (out-of-range mapped read -> NULL, same as a blank cell)", *got)
	}
	if got, want := invoiceSubtotal(t, super, wideID), "10.00"; got == nil || *got != want {
		t.Errorf("INV-WIDE persisted subtotal = %v, want %q (mapped cells persisted intact; the 2 extra trailing cells are ignored)", got, want)
	}
}

// TestServiceImport_DrasticallyShortRowNoPanic pushes the bounds-checking
// argument above to its breaking point: a row containing ONLY the
// invoice_number cell -- every OTHER mapped field (10 of 11) is out of
// range, not just one. This still must not panic: fieldValue/cellAt guard
// every read, regardless of how many fields are out of range at once.
//
// This is NOT a "wrong column count -> reject" assertion (out of scope, per
// the story) -- it characterizes whatever the ACTUAL outcome is. Reasoned
// ahead of running: the invoices/line_items schema keeps every MBS-content
// column NULLABLE with no CHECK (store-invalid-faithfully,
// migrations/20260714103137_invoices.sql), classify's three quarantine
// checks (headerConflictField, issueDateParseError,
// bestEffortBadNumericField) all skip a blank/out-of-range cell rather than
// flagging it, and newTestService wires an INERT fakeGate (zero violations,
// no rejection) -- so the tiny row is expected to COMMIT with every
// MBS-content column NULL/empty, exactly like INV-SHORT's single missing
// field above, just with more fields affected at once. Asserted, not
// assumed: if the real outcome differs (a panic, a wholesale error, or a
// quarantine), that is a genuine finding, not something to paper over.
func TestServiceImport_DrasticallyShortRowNoPanic(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-15-03 tenant (short-row)")
	entityID := seedEntity(t, super, tenantID, "M4-15-03 entity (short-row)")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// INV-TINY: ONE cell -- invoice_number only. Every other mapped field
	// (issue_date, buyer_tin, buyer_name, currency, vat, total,
	// line_description, line_quantity, line_unit_price, subtotal -- 10 of
	// anomalyHeader's 11 fields) is out of range.
	tinyRow := []string{"INV-TINY"}

	// INV-FULL: a complete, valid invoice -- proves the tiny row's anomaly
	// doesn't take the whole batch down with it.
	fullRow := []string{
		"INV-FULL", "2026-01-12", "T3", "B3", "NGN",
		"1.00", "11.00", "Item1", "1", "10.00", "10.00",
	}

	res, err := svc.Import(c, entityID, stdMapping, anomalyHeader, [][]string{tinyRow, fullRow}, false)
	if err != nil {
		t.Fatalf("Import: %v (a drastically out-of-range row must never panic or wholesale-reject the batch)", err)
	}

	if res.Status != "completed" {
		t.Errorf("Status = %q, want %q", res.Status, "completed")
	}
	if res.RowsTotal != 2 {
		t.Errorf("RowsTotal = %d, want 2", res.RowsTotal)
	}

	// Characterized outcome: both rows commit. classify has no "field
	// missing" check (only conflict/bad-format/duplicate), Create rejects
	// nothing but empty entity_id/invoice_number, and the inert fakeGate
	// reports zero violations -- so INV-TINY is READY, not quarantined.
	if res.ReadyInvoices != 2 || res.QuarantinedInvoices != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (2,0) -- characterized: even an almost-entirely-out-of-range row degrades to NULLs and commits, it does not quarantine or panic", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %+v, want none", res.Errors)
	}

	tinyID := invoiceIDByNumber(t, super, entityID, "INV-TINY")
	fullID := invoiceIDByNumber(t, super, entityID, "INV-FULL")

	if got := invoiceSubtotal(t, super, tinyID); got != nil {
		t.Errorf("INV-TINY persisted subtotal = %q, want nil (out-of-range mapped read -> NULL)", *got)
	}
	if got, want := invoiceSubtotal(t, super, fullID), "10.00"; got == nil || *got != want {
		t.Errorf("INV-FULL persisted subtotal = %v, want %q (the companion valid row is unaffected)", got, want)
	}
}
