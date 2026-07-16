// M4-03-06 (task-107): the LOCAL half of the joint M4 gate #2 (AC#1) — a
// cheap regression guard for [reuse-create]'s ~9-round-trips/invoice
// estimate, run BEFORE the expensive shared deploy gate. Generates a 500
// invoice / 1,500-row in-memory dataset (3 line rows per invoice, header
// fields repeating within a group exactly like a real CSV upload) and drives
// it through Service.Import(dryRun=false) end-to-end, timing the wall clock.
//
// This is NOT the AC#1 verification itself -- that's the deployed Playwright
// spec (e2e/api/import.spec.ts), which drives the same shape of fixture over
// the real HTTP/multipart/gateway path and asserts the < 60s wall-clock
// budget the story cares about. This test's < 30s LOCAL budget is
// deliberately generous (in-process Import is expected to take ~1-5s): a
// blown local budget signals an N+1/O(n^2) regression BEFORE the deploy gate
// ever runs, since [reuse-create] does one invoice.Store.Create round trip
// per ready invoice (no batching).
//
// dbTestPools/seedTenant/seedEntity (store_test.go) and newTestService/
// stdHeader/stdMapping/mkRow (service_test.go) are the same-package helpers
// already defined for M4-03-03/04 -- reused here verbatim, not redefined.
//
// Spec-to-test map (Test Specs table, M4-03-06 story / task-107):
//
//	IMP-PERF-01 TestServiceImport_500InvoicePerfBudget (elapsed < 30s)
//	IMP-PERF-02 TestServiceImport_500InvoicePerfBudget (counts + persisted rows)
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/importer/... -run PerfBudget -v
package importer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// countLineItemsForEntity counts every line_items row belonging to any
// invoice under entityID -- a whole-entity total, unlike service_test.go's
// countLineItems (which counts one invoice at a time).
func countLineItemsForEntity(t *testing.T, super *pgxpool.Pool, entityID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM line_items li JOIN invoices i ON i.id = li.invoice_id WHERE i.entity_id = $1`,
		entityID,
	).Scan(&n); err != nil {
		t.Fatalf("count line_items for entity: %v", err)
	}
	return n
}

// IMP-PERF-01/02: 500 invoices (INV-PERF-00001..00500), 3 clean line rows
// each (1,500 rows total), all against ONE seeded entity via ONE
// Service.Import(dryRun=false) call -- asserts both the wall-clock budget
// (IMP-PERF-01) and the counts/persisted-rows split (IMP-PERF-02), since a
// perf test that silently imported garbage would be worse than no perf test.
func TestServiceImport_500InvoicePerfBudget(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-PERF-01 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-PERF-01 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const invoiceCount = 500
	const linesPerInvoice = 3
	rows := make([][]string, 0, invoiceCount*linesPerInvoice)
	for i := 1; i <= invoiceCount; i++ {
		invNo := fmt.Sprintf("INV-PERF-%05d", i)
		for line := 1; line <= linesPerInvoice; line++ {
			// Header fields (issue_date/buyer_tin/buyer_name/currency/subtotal/
			// vat/total) repeat identically across the group's rows -- exactly
			// what a real CSV export looks like, and required for the group to
			// classify as READY rather than a header-field-conflict quarantine.
			rows = append(rows, mkRow(
				invNo, "2026-01-15", "87654321-0002", "IMP-PERF Buyer Co", "NGN",
				"1000.00", "75.00", "1075.00",
				fmt.Sprintf("Item %d", line), "1", "100.00",
			))
		}
	}

	start := time.Now()
	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	elapsed := time.Since(start)
	t.Logf("IMP-PERF: Service.Import(500 invoices / 1500 rows, dryRun=false) took %s", elapsed)

	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	// IMP-PERF-01: the LOCAL perf budget.
	const budget = 30 * time.Second
	if elapsed >= budget {
		t.Errorf("Import took %s, want < %s (LOCAL budget -- possible [reuse-create] N+1/O(n^2) regression)", elapsed, budget)
	}

	// IMP-PERF-02: counts + persisted rows.
	if res.RowsTotal != invoiceCount*linesPerInvoice {
		t.Errorf("RowsTotal = %d, want %d", res.RowsTotal, invoiceCount*linesPerInvoice)
	}
	if res.RowsValid != invoiceCount*linesPerInvoice {
		t.Errorf("RowsValid = %d, want %d", res.RowsValid, invoiceCount*linesPerInvoice)
	}
	if res.RowsInvalid != 0 {
		t.Errorf("RowsInvalid = %d, want 0", res.RowsInvalid)
	}
	if res.ReadyInvoices != invoiceCount {
		t.Errorf("ReadyInvoices = %d, want %d", res.ReadyInvoices, invoiceCount)
	}
	if res.QuarantinedInvoices != 0 {
		t.Errorf("QuarantinedInvoices = %d, want 0: errors=%+v", res.QuarantinedInvoices, res.Errors)
	}

	gotInvoices := countInvoicesForEntity(t, super, entityID)
	if gotInvoices != invoiceCount {
		t.Errorf("persisted invoices for entity = %d, want %d", gotInvoices, invoiceCount)
	}
	gotLineItems := countLineItemsForEntity(t, super, entityID)
	if gotLineItems != invoiceCount*linesPerInvoice {
		t.Errorf("persisted line_items for entity = %d, want %d", gotLineItems, invoiceCount*linesPerInvoice)
	}
}
