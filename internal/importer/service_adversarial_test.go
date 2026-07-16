// M4-03-04 (task-105) Stage 4 QA (Mode B): adversarial Service.Import coverage
// added post-implementation, on top of the 16 IMP-SVC-01..16 acceptance-
// criteria tests in service_test.go. This file pins three of the executor's
// flagged assumptions plus a few more adversarial probes task-105 doesn't
// already spell out:
//
//   - a blank OPTIONAL numeric cell (e.g. subtotal) -> nil/NULL, not
//     quarantined (store-invalid-faithfully: "they wrote nothing" is not an
//     error) -- TestServiceImport_BlankSubtotalCellCommitsAsNullNotQuarantined.
//
//   - issue_date: a NON-EMPTY value that fails to parse as the one canonical
//     YYYY-MM-DD format the importer accepts must QUARANTINE that invoice
//     (RowError{Field:"issue_date"}), not silently commit with issue_date
//     NULL -- silently NULLing a firm-written bad date is a form of
//     correction that erases the error Core AC#7 says must never happen, and
//     defeats the whole store-invalid-faithfully wedge (M4-04 needs to see
//     "date format wrong", not a laundered "date missing"). This is
//     INTENTIONALLY RED against the current implementation (parseIssueDate
//     silently returns nil for any unparseable string, see service.go) --
//     TestServiceImport_UnparseableIssueDateQuarantines. Its companion,
//     TestServiceImport_BlankIssueDateCommitsAsNull, pins the (already
//     correct, and must stay correct once the defect above is fixed) blank
//     -> NULL case.
//
//   - rowsTotal==0 on a real import mints a batch and finalizes it straight
//     to 'failed', with no panic and no per-group work --
//     TestServiceImport_ZeroDataRowsRealImportFinalizesFailed.
//
// Plus three more probes (Part C, task-105's Stage 4 QA prompt) that stay
// deliberately narrow -- NOT M4-15's exhaustive malformed-row cataloguing:
//
//   - a whitespace-only invoice_number cell is ungroupable (blank-after-trim,
//     same as a literally empty cell) --
//     TestServiceImport_WhitespaceOnlyInvoiceNumberTreatedAsBlankUngroupable.
//   - a LINE field (line_description/line_quantity) differing across a
//     group's rows must NOT trip the in-file HEADER-conflict check --
//     TestServiceImport_LineFieldsVaryAcrossGroupRowsNoConflictCommitsWithDistinctLines.
//   - a HEADER numeric field given as "1,000" in one row and "1000" in
//     another of the SAME group must compare equal post-normalization, not
//     spuriously conflict --
//     TestServiceImport_HeaderSubtotalNormalizedBeforeConflictCompareNoSpuriousConflict.
//
// Reuses every fixture/helper already defined in service_test.go
// (stdHeader/stdMapping/mkRow, dbTestPools/seedTenant/seedEntity/seedInvoice,
// newTestService, the count*/invoice* read-back helpers, rowNumbersOf/
// findRowErrorWithRows/intSliceEqual) -- same package, not redefined.
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/importer/...
package importer

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- Part B1: blank optional numeric cell -> NULL, not quarantined --------

// A blank subtotal cell (mapped, present, but "") must commit with subtotal
// NULL, not be treated as a parse failure -- fieldValue's numeric branch
// returns nil for a normalized-empty numeric cell (service.go), which is the
// faithful "they wrote nothing" reading, consistent with
// store-invalid-faithfully (a firm that truly left the cell blank gets NULL,
// not a fabricated 0.00 and not a quarantine). This is the executor's flagged
// assumption #1 -- pinned here as expected/acceptable behavior.
func TestServiceImport_BlankSubtotalCellCommitsAsNullNotQuarantined(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-B1 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-B1 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-BLANKSUB", "2026-01-10", "T1", "B1", "NGN", "", "0.00", "0.00", "Item1", "1", "10.00"), // sheet 2 -- blank subtotal
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (1,0,[]) -- a blank optional numeric cell must not quarantine", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}

	invID := invoiceIDByNumber(t, super, entityID, "INV-BLANKSUB")
	got := invoiceSubtotal(t, super, invID)
	if got != nil {
		t.Errorf("persisted subtotal = %q, want nil (blank cell -> NULL, not 0.00 and not quarantined)", *got)
	}
}

// --- Part B2 (KEY ISSUE): issue_date -----------------------------------

// TestServiceImport_UnparseableIssueDateQuarantines is INTENTIONALLY RED
// against the current implementation. Empirically, Import today silently
// stores issue_date as NULL for ANY unparseable non-empty cell (verified by
// probe: "03/06/2026" and "not-a-date" both commit with issue_date NULL,
// QuarantinedInvoices 0) -- see parseIssueDate in service.go, which returns
// nil on both a blank AND a time.Parse error, with no distinction and no
// RowError.
//
// That collapses two semantically different inputs into the same NULL:
//   - the firm truly left the cell blank ("no date recorded" -- a faithful
//     NULL, store-invalid-faithfully's correct reading, and what
//     TestServiceImport_BlankIssueDateCommitsAsNull below pins), vs.
//   - the firm WROTE a date, just not in the one canonical YYYY-MM-DD format
//     this importer accepts (e.g. their own DD/MM/YYYY convention, or a
//     stray value) -- silently discarding this and storing NULL is a form of
//     correction ("I'll decide you meant no date") that Core AC#7 forbids
//     ("no field is computed, reconciled, or corrected from other fields" --
//     here it's corrected away entirely) and that defeats the whole
//     store-invalid-faithfully wedge M4-04 depends on: a firm importing 500
//     invoices with DD/MM/YYYY dates would get 500 silently-NULL issue_dates
//     and a misleading "date missing" signal from M4-04, instead of "date
//     format wrong" -- the actionable, real error.
//
// The consistent behavior -- matching how a numeric field already quarantines
// on a bad ::numeric cast at Create time ([review-authority]) -- is: a
// NON-EMPTY issue_date that doesn't parse as YYYY-MM-DD quarantines that
// invoice with RowError{Field:"issue_date"}; only a genuinely EMPTY cell
// yields NULL. This test asserts that target behavior, so it is RED today.
// The re-spawned executor should make it pass (whichever mechanism it
// chooses -- pre-Create classification or a Create-time check -- the
// observable BatchResult contract below is what must hold).
func TestServiceImport_UnparseableIssueDateQuarantines(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-B2 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-B2 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-BADDATE1", "03/06/2026", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"), // sheet 2 -- DD/MM/YYYY, not YYYY-MM-DD
		mkRow("INV-BADDATE2", "not-a-date", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "Item2", "1", "20.00"), // sheet 3 -- garbage
		mkRow("INV-GOODDATE", "2026-01-10", "T3", "B3", "NGN", "30.00", "3.00", "33.00", "Item3", "1", "30.00"), // sheet 4 -- clean sibling
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if res.Status != "completed" {
		t.Errorf("Status = %q, want %q (an issue_date quarantine must not fail the whole run)", res.Status, "completed")
	}
	if res.RowsTotal != 3 || res.RowsValid != 1 || res.RowsInvalid != 2 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (3,1,2)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 2 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,2) -- both unparseable-date invoices must quarantine, the clean sibling must still commit", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	if len(res.Errors) != 2 {
		t.Fatalf("len(Errors) = %d, want 2: %+v", len(res.Errors), res.Errors)
	}
	re1, found1 := findRowErrorWithRows(res.Errors, []int{2})
	if !found1 {
		t.Fatalf("no RowError citing row 2 (INV-BADDATE1, \"03/06/2026\") in %+v", res.Errors)
	}
	if re1.Field != "issue_date" {
		t.Errorf("row-2 RowError.Field = %q, want %q", re1.Field, "issue_date")
	}
	re2, found2 := findRowErrorWithRows(res.Errors, []int{3})
	if !found2 {
		t.Fatalf("no RowError citing row 3 (INV-BADDATE2, \"not-a-date\") in %+v", res.Errors)
	}
	if re2.Field != "issue_date" {
		t.Errorf("row-3 RowError.Field = %q, want %q", re2.Field, "issue_date")
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-BADDATE1"); got != 0 {
		t.Errorf("INV-BADDATE1 persisted = %d, want 0 (quarantined, unparseable issue_date)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-BADDATE2"); got != 0 {
		t.Errorf("INV-BADDATE2 persisted = %d, want 0 (quarantined, unparseable issue_date)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-GOODDATE"); got != 1 {
		t.Errorf("INV-GOODDATE persisted = %d, want 1 (clean sibling still commits)", got)
	}
}

// TestServiceImport_BlankIssueDateCommitsAsNull is the companion to the RED
// test above: a genuinely EMPTY issue_date cell ("they wrote nothing") must
// commit with issue_date NULL, not quarantine -- this is the one case where
// silently mapping to NULL IS the faithful reading, and must stay true once
// the defect above is fixed. Green today (parseIssueDate already returns nil
// for a blank string) and must remain green after the fix.
func TestServiceImport_BlankIssueDateCommitsAsNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-B2-companion tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-B2-companion entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-NODATE", "", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"), // sheet 2 -- blank issue_date
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (1,0,[]) -- a blank issue_date must not quarantine", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}

	var issueDate *string
	if err := super.QueryRow(ctx,
		`SELECT issue_date::text FROM invoices WHERE entity_id = $1 AND invoice_number = $2`,
		entityID, "INV-NODATE",
	).Scan(&issueDate); err != nil {
		t.Fatalf("read issue_date: %v", err)
	}
	if issueDate != nil {
		t.Errorf("persisted issue_date = %q, want nil (blank cell -> NULL)", *issueDate)
	}
}

// --- Part B3: rowsTotal==0 on a real import -------------------------------

// A real import (dryRun=false) given zero data rows mints a batch (so the
// attempt is auditable) and finalizes it straight to 'failed', without
// panicking and without attempting any per-group Create -- task-105's
// documented rowsTotal==0 special case. This is the "run itself can't report
// anything" path, distinct from a partial-success completed run.
func TestServiceImport_ZeroDataRowsRealImportFinalizesFailed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-B3 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-B3 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, [][]string{}, false)
	if err != nil {
		t.Fatalf("Import (zero data rows): %v", err)
	}
	if res.ID == "" || res.Status != "failed" {
		t.Errorf("(ID=%q Status=%q), want (non-empty, %q)", res.ID, res.Status, "failed")
	}
	if res.RowsTotal != 0 || res.RowsValid != 0 || res.RowsInvalid != 0 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (0,0,0)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 0 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (0,0,[])", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}

	if got := countImportBatchesForEntity(t, super, entityID); got != 1 {
		t.Errorf("import_batches rows = %d, want 1 (the attempt is auditable even though it failed)", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices rows = %d, want 0", got)
	}
}

// --- Part C1: whitespace-only invoice_number ------------------------------

// A whitespace-only invoice_number cell ("   ") is ungroupable, exactly like
// a literally empty cell -- Import's own grouping loop does
// strings.TrimSpace(raw) == "" (service.go), so this is the blank-after-trim
// reading, not a real (if odd-looking) group key.
func TestServiceImport_WhitespaceOnlyInvoiceNumberTreatedAsBlankUngroupable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-C1 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-C1 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-OK", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"), // sheet 2
		mkRow("   ", "2026-01-10", "T2", "B2", "NGN", "5.00", "0.00", "5.00", "Blank", "1", "5.00"),       // sheet 3 -- whitespace-only invoice_number
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1) -- whitespace-only invoice_number must be ungroupable, same as blank", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
	}
	re := res.Errors[0]
	if re.Row != 3 {
		t.Errorf("whitespace-invoice_number RowError.Row = %d, want 3 (scalar, ungroupable)", re.Row)
	}
	if len(re.Rows) != 0 {
		t.Errorf("whitespace-invoice_number RowError.Rows = %v, want empty (scalar Row, not plural Rows)", re.Rows)
	}

	if got := countInvoicesForEntity(t, super, entityID); got != 1 {
		t.Errorf("persisted invoices = %d, want 1 (INV-OK only)", got)
	}
}

// --- Part C2: a LINE field varying across a group's rows is NOT a conflict -

// Two rows of the same invoice_number legitimately differing on
// line_description AND line_quantity (LINE fields, not HEADER fields) must
// NOT trip the in-file header-conflict check -- headerConflictField only
// walks headerFieldOrder (issue_date/buyer_tin/buyer_name/currency/subtotal/
// vat/total), never the 3 line fields, so this pins that the conflict check
// stays correctly scoped. The invoice commits with 2 distinct line items in
// file order.
func TestServiceImport_LineFieldsVaryAcrossGroupRowsNoConflictCommitsWithDistinctLines(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-C2 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-C2 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		// Header fields (issue_date/buyer_tin/buyer_name/currency/subtotal/vat/total) agree.
		// Line fields (line_description/line_quantity/line_unit_price) legitimately differ.
		mkRow("INV-VARYLINES", "2026-01-10", "T1", "B1", "NGN", "140.00", "0.00", "140.00", "Widget", "2", "40.00"), // sheet 2
		mkRow("INV-VARYLINES", "2026-01-10", "T1", "B1", "NGN", "140.00", "0.00", "140.00", "Gadget", "3", "20.00"), // sheet 3 -- different description AND quantity
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (1,0,[]) -- line fields varying per row must never trip the header-conflict check", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}

	invID := invoiceIDByNumber(t, super, entityID, "INV-VARYLINES")
	if got := countLineItems(t, super, invID); got != 2 {
		t.Errorf("line item count = %d, want 2", got)
	}
	if got, want := lineItemDescriptions(t, super, invID), []string{"Widget", "Gadget"}; !stringSliceEqual(got, want) {
		t.Errorf("line item descriptions (line_no order) = %v, want %v", got, want)
	}
}

// --- Part C3: normalization symmetry in the header-conflict compare -------

// A HEADER numeric field ("subtotal") given as "1,000" in one row and "1000"
// in another row of the SAME group must compare EQUAL post-normalization --
// headerConflictField's cellAt helper normalizes numeric fields before
// comparing (service.go), so this must not be a spurious in-file conflict.
// Pins [numeric-normalization] applying symmetrically to the conflict
// detector, not just to the value ultimately persisted.
func TestServiceImport_HeaderSubtotalNormalizedBeforeConflictCompareNoSpuriousConflict(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-C3 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-C3 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-NORMSYM", "2026-01-10", "T1", "B1", "NGN", "1,000", "0.00", "1000.00", "Item1", "1", "500.00"), // sheet 2 -- subtotal "1,000"
		mkRow("INV-NORMSYM", "2026-01-10", "T1", "B1", "NGN", "1000", "0.00", "1000.00", "Item2", "1", "500.00"),  // sheet 3 -- subtotal "1000", same value
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (1,0,[]) -- \"1,000\" and \"1000\" are the SAME normalized value, not a conflict", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}

	invID := invoiceIDByNumber(t, super, entityID, "INV-NORMSYM")
	got := invoiceSubtotal(t, super, invID)
	if got == nil || *got != "1000.00" {
		t.Errorf("persisted subtotal = %v, want \"1000.00\"", got)
	}
	if n := countLineItems(t, super, invID); n != 2 {
		t.Errorf("line item count = %d, want 2", n)
	}
}

// --- Part C4: issue_date whitespace symmetry in the header-conflict compare
//     (CodeRabbit, M4-03 PR review) ------------------------------------------

// A HEADER field ("issue_date") given as " 2026-01-10 " (surrounding
// whitespace) in one row and "2026-01-10" in another row of the SAME group
// must compare EQUAL post-trim -- headerConflictField's cellAt helper now
// trims issue_date before comparing (mirroring parseIssueDate's own
// strings.TrimSpace, service.go), so this must not be a spurious in-file
// conflict. Before the fix, parsing already trimmed (so the persisted date
// would have been correct) but conflict DETECTION didn't, so this group
// would have been wrongly quarantined despite producing the same stored
// date -- this test is RED against that bug.
func TestServiceImport_HeaderIssueDateTrimmedBeforeConflictCompareNoSpuriousConflict(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-C4 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-C4 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-DATETRIM", " 2026-01-10 ", "T1", "B1", "NGN", "100.00", "0.00", "100.00", "Item1", "1", "50.00"), // sheet 2 -- surrounding whitespace
		mkRow("INV-DATETRIM", "2026-01-10", "T1", "B1", "NGN", "100.00", "0.00", "100.00", "Item2", "1", "50.00"),   // sheet 3 -- same date, no whitespace
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (1,0,[]) -- \" 2026-01-10 \" and \"2026-01-10\" are the SAME date post-trim, not a conflict", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}

	invID := invoiceIDByNumber(t, super, entityID, "INV-DATETRIM")
	if got := countLineItems(t, super, invID); got != 2 {
		t.Errorf("line item count = %d, want 2", got)
	}

	var issueDate *string
	if err := super.QueryRow(ctx,
		`SELECT issue_date::text FROM invoices WHERE entity_id = $1 AND invoice_number = $2`,
		entityID, "INV-DATETRIM",
	).Scan(&issueDate); err != nil {
		t.Fatalf("read issue_date: %v", err)
	}
	if issueDate == nil || *issueDate != "2026-01-10" {
		t.Errorf("persisted issue_date = %v, want %q", issueDate, "2026-01-10")
	}
}
