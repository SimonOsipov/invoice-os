// M4-11-03 (task-195) QA Mode-B adversarial extension. Mode A/B's own
// pre-check (fixtures_green_test.go) confirmed the AC-1/AC-2 clean
// invariant is a REAL assertion, not a vacuous one -- pointing
// TestFixtures_Green500ImportsAndValidatesClean's body at
// testdata/invoices/edge_vat_math_wrong.csv (a known-dirty fixture) turns
// InvoicesWithViolations/InvoiceViolations RED (a genuine vat-standard-rate
// hit), then was reverted cleanly. That still leaves one open question:
// does a genuinely PASSING BatchResult (ReadyInvoices==500, Status
// "completed") correspond to 500 real rows actually committed to
// invoices/line_items -- or could readyCount++ (service.go) and a real
// Store.Create() write silently diverge under some future refactor? The
// test below answers that by reading the rows back out-of-band, via the
// superuser pool, bypassing the Service entirely.
//
// Two candidates from the task brief were evaluated and INTENTIONALLY
// SKIPPED here, not forced:
//
//   - Cross-tenant/RLS: internal/importer/dup_parity_test.go's PAR-05
//     (TestPredicateParity_CrossTenantNoFalseCollision) already exercises
//     the identical Postgres RLS policy (`tenant_id = current_setting(...)`)
//     that would gate a green-fixture-scale cross-tenant check, and
//     internal/invoice/store_test.go's TestStoreList_TenantScopedAndPaginated
//     covers the same boundary on List(). RLS enforcement is a per-row
//     Postgres policy, not a batch-size-dependent code path -- a
//     green_500-specific repeat would exercise the SAME SQL policy PAR-05
//     already does, at 500x the fixture cost, for no new failure class.
//   - dry_run parity: service_gate_test.go's IMPV-07
//     (TestServiceImport_DryRunWritesZeroRowsBothTables) already locks
//     "dryRun writes nothing" structurally -- Import's dry-run branch
//     (service.go) returns BEFORE CreateBatch/Create are ever called, a
//     control-flow fact independent of which fixture is fed in. Re-running
//     green_500 through it would add ~1.4s for zero new coverage.
package importer

import (
	"context"
	"testing"
)

// TestFixtures_GreenPersistsInvoicesAndLineItemsToDatabase is the M4-11-03
// QA adversarial positive-count lock (AC-1/AC-2's [import-report-shape]
// counters, verified against the actual persisted rows rather than trusted
// on BatchResult's own say-so): for each committed green fixture, after a
// real (dryRun=false) import reports the expected clean BatchResult, the
// entity's invoices/line_items rows are read back directly via the
// superuser pool (bypassing the Service and RLS both) and must show:
//
//   - exactly wantInvoices invoices rows for this entity, ALL with a
//     DISTINCT invoice_number (guards against a bug that overwrote or
//     collapsed groups rather than genuinely creating wantInvoices of them)
//   - exactly wantInvoices*3 line_items rows (every fixture invoice carries
//     exactly 3 lines, tools/fixturegen's generateGreen contract)
//   - every one of those invoices promoted to status='validated' (the gate
//     actually ran end-to-end and promoted each one, per
//     [Stage-1 F1]/ApplyValidation's clean-evaluation path in
//     internal/invoice/store.go -- not merely that BatchResult.Status read
//     "completed" while individual rows silently stayed 'draft')
//
// This closes a real gap distinct from AC-1/AC-2's own assertCleanImport:
// that function only checks BatchResult's self-reported counters, which are
// in-memory bookkeeping (readyCount++ in service.go) computed ALONGSIDE the
// real Store.Create() calls, not derived FROM a read-back of them -- a
// future refactor that let the two drift (e.g. counting a create attempt
// before confirming it committed) would still satisfy assertCleanImport
// while under-persisting. Reading the rows back independently is the only
// way to rule that out.
func TestFixtures_GreenPersistsInvoicesAndLineItemsToDatabase(t *testing.T) {
	cases := []struct {
		path         string
		label        string
		wantInvoices int
	}{
		{"../../testdata/invoices/green_500.csv", "green_500.csv", 500},
		{"../../testdata/invoices/green_second.csv", "green_second.csv", 250},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			res, entityID, super := importGreenFixture(t, tc.path,
				"M4-11-03 adv "+tc.label+" tenant", "M4-11-03 adv "+tc.label+" entity")
			assertCleanImport(t, tc.label+" (persistence read-back)", res, tc.wantInvoices)

			ctx := context.Background()

			var invoiceCount, distinctCount int
			if err := super.QueryRow(ctx,
				`SELECT count(*), count(DISTINCT invoice_number) FROM invoices WHERE entity_id = $1`,
				entityID,
			).Scan(&invoiceCount, &distinctCount); err != nil {
				t.Fatalf("read back invoices for entity %s: %v", entityID, err)
			}
			if invoiceCount != tc.wantInvoices {
				t.Errorf("%s: invoices row count for entity = %d, want %d (BatchResult.ReadyInvoices could be reporting a count that never actually committed)",
					tc.label, invoiceCount, tc.wantInvoices)
			}
			if distinctCount != tc.wantInvoices {
				t.Errorf("%s: distinct invoice_number count for entity = %d, want %d (a bug that collapsed or overwrote groups would still satisfy the plain row count above)",
					tc.label, distinctCount, tc.wantInvoices)
			}

			var lineCount int
			if err := super.QueryRow(ctx,
				`SELECT count(*) FROM line_items li JOIN invoices i ON i.id = li.invoice_id WHERE i.entity_id = $1`,
				entityID,
			).Scan(&lineCount); err != nil {
				t.Fatalf("read back line_items for entity %s: %v", entityID, err)
			}
			wantLines := tc.wantInvoices * 3
			if lineCount != wantLines {
				t.Errorf("%s: line_items row count for entity = %d, want %d (%d invoices x 3 lines each)",
					tc.label, lineCount, wantLines, tc.wantInvoices)
			}

			var validatedCount int
			if err := super.QueryRow(ctx,
				`SELECT count(*) FROM invoices WHERE entity_id = $1 AND status = 'validated'`,
				entityID,
			).Scan(&validatedCount); err != nil {
				t.Fatalf("read back validated status for entity %s: %v", entityID, err)
			}
			if validatedCount != tc.wantInvoices {
				t.Errorf("%s: invoices with status='validated' for entity = %d, want %d (a clean batch must promote every invoice via ApplyValidation, not merely report InvoicesWithViolations==0)",
					tc.label, validatedCount, tc.wantInvoices)
			}
		})
	}
}
