// M4-03-04 (task-105): red, DB-backed tests for Service.Import — THE HEART of
// the importer: map -> normalize -> group -> classify -> orchestrate. Written
// BEFORE the real implementation exists, against the not-implemented stub
// body in service.go (always returns the zero BatchResult, nil error) — see
// that file's doc comment. Every assertion below is a behavioral one (a
// count, a persisted row, an error sentinel), so it fails on ASSERTION
// against the stub, never on a compile or panic.
//
// dbTestPools/seedTenant/seedEntity/seedInvoice are the same-package helpers
// already defined in store_test.go (M4-03-03) -- reused here verbatim, not
// redefined.
//
// Spec-to-test map (Test Specs table, M4-03-04 story / task-105; IMP-SVC-16
// added per M4-03-03's QA advisory -- entity-scoped dedup):
//
//	IMP-SVC-01 TestServiceImport_NonContiguousRowsGroupIntoOneInvoiceWithOrderedLineItems
//	IMP-SVC-02 TestServiceImport_GroupDisagreeingOnTotalQuarantinedOthersCommit
//	IMP-SVC-03 TestServiceImport_CollidesWithPreSeededStoredInvoiceQuarantinedSiblingCommits
//	IMP-SVC-04 TestServiceImport_HeaderFieldConflictRowErrorCitesExactSheetRows
//	IMP-SVC-05 TestServiceImport_SubtotalPersistsVerbatimNotDerivedFromLineItems
//	IMP-SVC-06 TestServiceImport_DryRunClassifiesOnlyDBUnchanged
//	IMP-SVC-07 TestServiceImport_SameMixedFileRealImportMatchesDryRunVerdict
//	IMP-SVC-08 TestServiceImport_BlankInvoiceNumberRowQuarantinedUngroupableScalarRow
//	IMP-SVC-09 TestServiceImport_MappingMissingInvoiceNumberValidationBeforeAnyWrite
//	IMP-SVC-10 TestServiceImport_MappingReferencesAbsentHeaderValidationBeforeAnyWrite
//	IMP-SVC-11 TestServiceImport_TinLessEntityCommitsWithNilSupplierTIN
//	IMP-SVC-12 TestServiceImport_ConcurrentDuplicateAtCreateTimeQuarantinesLoser
//	IMP-SVC-13 TestServiceImport_AllInvalidSourcesMixedCountersExactNoDoubleCount
//	IMP-SVC-14 TestServiceImport_AccountingFormattedSubtotalNormalizesAndCommits
//	IMP-SVC-15 TestServiceImport_NonNumericTotalQuarantinesViaCreateErrorOthersCommit
//	IMP-SVC-16 TestServiceImport_EntityScopedDedupSameNumberUnderDifferentEntityNotDuplicate
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/importer/... -run 'Import|Service|SVC'
package importer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- fixture builders --------------------------------------------------

// stdHeader/stdMapping is the representative header + canonical-field
// mapping shared by most specs below (task-105's "A REPRESENTATIVE header
// for most tests"). Header fields (issue_date, buyer_tin, buyer_name,
// currency, subtotal, vat, total) are expected to repeat identically across
// a group's rows; line fields (line_description, line_quantity,
// line_unit_price) vary per row.
var stdHeader = []string{
	"Invoice No", "Issue Date", "Buyer TIN", "Buyer", "Currency",
	"Subtotal", "VAT", "Total", "Item", "Qty", "Unit Price",
}

var stdMapping = map[string]string{
	"invoice_number":   "Invoice No",
	"issue_date":       "Issue Date",
	"buyer_tin":        "Buyer TIN",
	"buyer_name":       "Buyer",
	"currency":         "Currency",
	"subtotal":         "Subtotal",
	"vat":              "VAT",
	"total":            "Total",
	"line_description": "Item",
	"line_quantity":    "Qty",
	"line_unit_price":  "Unit Price",
}

// mkRow builds one data row in stdHeader's column order.
func mkRow(invoiceNo, issueDate, buyerTIN, buyerName, currency, subtotal, vat, total, item, qty, unitPrice string) []string {
	return []string{invoiceNo, issueDate, buyerTIN, buyerName, currency, subtotal, vat, total, item, qty, unitPrice}
}

// cloneMapping returns a shallow copy of m, so IMP-SVC-09/10 can mutate a
// private copy without disturbing stdMapping for other tests.
func cloneMapping(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// newTestService builds a Service over the app-role pool, wiring the
// importer Store and invoice.Store exactly as production code would
// (NewService(importer.NewStore(appPool), invoice.NewStore(appPool), gate)),
// with an INERT fakeGate (service_gate_test.go): every test in this file
// exercises M4-03 (pre-gate) orchestration -- decode/classify/create/
// finalize and the five M4-03 counters -- for which the gate is a
// collaborator, not the subject.
//
// Inert, NOT nil: as of task-114/M4-04-07 Import() dereferences s.gate on
// both paths (dry-run Evaluate, real ValidateBatch) for any file with at
// least one READY group, so nil now panics. A zero-value fakeGate returns a
// zero-value BatchOutcome and nil error, so ApplyValidation never runs and
// invoices stay draft with no violations -- PRECISELY the pre-gate behavior
// these M4-03 specs were written against, preserved rather than weakened.
// task-114's own gate-integrated specs use newTestServiceWithGate (a real
// or recording gate) instead.
func newTestService(app *pgxpool.Pool) *Service {
	return NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
}

// --- super-pool read-back helpers (out-of-band verification, RLS-bypassing) ---

func countInvoicesForEntity(t *testing.T, super *pgxpool.Pool, entityID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE entity_id = $1`, entityID,
	).Scan(&n); err != nil {
		t.Fatalf("count invoices for entity: %v", err)
	}
	return n
}

func countInvoicesByNumber(t *testing.T, super *pgxpool.Pool, entityID, number string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE entity_id = $1 AND invoice_number = $2`, entityID, number,
	).Scan(&n); err != nil {
		t.Fatalf("count invoices by number: %v", err)
	}
	return n
}

func countImportBatchesForEntity(t *testing.T, super *pgxpool.Pool, entityID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM import_batches WHERE entity_id = $1`, entityID,
	).Scan(&n); err != nil {
		t.Fatalf("count import_batches for entity: %v", err)
	}
	return n
}

func invoiceIDByNumber(t *testing.T, super *pgxpool.Pool, entityID, number string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM invoices WHERE entity_id = $1 AND invoice_number = $2`, entityID, number,
	).Scan(&id); err != nil {
		t.Fatalf("find invoice id by number %q: %v", number, err)
	}
	return id
}

func lineItemDescriptions(t *testing.T, super *pgxpool.Pool, invoiceID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT description FROM line_items WHERE invoice_id = $1 ORDER BY line_no ASC`, invoiceID,
	)
	if err != nil {
		t.Fatalf("query line_items for invoice %s: %v", invoiceID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d *string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan line_items.description: %v", err)
		}
		if d == nil {
			out = append(out, "")
			continue
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate line_items: %v", err)
	}
	return out
}

func countLineItems(t *testing.T, super *pgxpool.Pool, invoiceID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM line_items WHERE invoice_id = $1`, invoiceID,
	).Scan(&n); err != nil {
		t.Fatalf("count line_items: %v", err)
	}
	return n
}

func invoiceSubtotal(t *testing.T, super *pgxpool.Pool, invoiceID string) *string {
	t.Helper()
	var s *string
	if err := super.QueryRow(context.Background(),
		`SELECT subtotal::text FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&s); err != nil {
		t.Fatalf("read subtotal: %v", err)
	}
	return s
}

func invoiceSupplier(t *testing.T, super *pgxpool.Pool, invoiceID string) (name *string, tin *string) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT supplier_name, supplier_tin FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&name, &tin); err != nil {
		t.Fatalf("read supplier fields: %v", err)
	}
	return name, tin
}

// --- RowError inspection helpers ----------------------------------------

// rowNumbersOf returns re's row numbers, whichever of Row (scalar,
// ungroupable rows) / Rows (plural, group-wide problems) is populated.
func rowNumbersOf(re RowError) []int {
	if re.Row != 0 {
		return []int{re.Row}
	}
	return re.Rows
}

// allRowNumbers flattens every RowError's row numbers into one slice, for
// asserting the full invalid-row set with no gaps and no double-counting.
func allRowNumbers(errs []RowError) []int {
	var out []int
	for _, e := range errs {
		out = append(out, rowNumbersOf(e)...)
	}
	return out
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// findRowErrorWithRows returns the first RowError whose row-number set
// (scalar or plural) equals want, sorted.
func findRowErrorWithRows(errs []RowError, want []int) (RowError, bool) {
	sortedWant := append([]int(nil), want...)
	sort.Ints(sortedWant)
	for _, e := range errs {
		got := append([]int(nil), rowNumbersOf(e)...)
		sort.Ints(got)
		if intSliceEqual(got, sortedWant) {
			return e, true
		}
	}
	return RowError{}, false
}

// --- IMP-SVC-01 -----------------------------------------------------------

// IMP-SVC-01: 3 non-contiguous rows sharing one invoice_number (interleaved
// with a second invoice's rows) group into a single invoice with all 3 line
// items in group (file) order; the interleaved second invoice groups into
// its own single invoice too ([grouping], AC#1). RED against the stub:
// ReadyInvoices is 0 (want 2), and no invoices row exists at all.
func TestServiceImport_NonContiguousRowsGroupIntoOneInvoiceWithOrderedLineItems(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-01 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-01 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget A", "1", "100.00"),  // sheet 2
		mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Gadget B1", "1", "100.00"), // sheet 3
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget B", "1", "100.00"),  // sheet 4
		mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Gadget B2", "1", "100.00"), // sheet 5
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget C", "1", "100.00"),  // sheet 6
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.RowsTotal != 5 || res.RowsValid != 5 || res.RowsInvalid != 0 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (5,5,0)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 2 || res.QuarantinedInvoices != 0 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (2,0)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %+v, want empty", res.Errors)
	}
	if res.Status != "completed" || res.ID == "" {
		t.Errorf("(Status=%q ID=%q), want (completed, non-empty)", res.Status, res.ID)
	}

	if got := countInvoicesForEntity(t, super, entityID); got != 2 {
		t.Fatalf("persisted invoices count = %d, want 2", got)
	}

	invA := invoiceIDByNumber(t, super, entityID, "INV-A")
	if got, want := lineItemDescriptions(t, super, invA), []string{"Widget A", "Widget B", "Widget C"}; !stringSliceEqual(got, want) {
		t.Errorf("INV-A line item descriptions (line_no order) = %v, want %v", got, want)
	}

	invB := invoiceIDByNumber(t, super, entityID, "INV-B")
	if got, want := lineItemDescriptions(t, super, invB), []string{"Gadget B1", "Gadget B2"}; !stringSliceEqual(got, want) {
		t.Errorf("INV-B line item descriptions (line_no order) = %v, want %v", got, want)
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- IMP-SVC-02 -----------------------------------------------------------

// IMP-SVC-02: a group whose rows (sheet rows 4 and 6) disagree on `total`
// (a header field) is quarantined with RowError.Rows == [4,6]; the OTHER
// group (3 rows, sheet 2/3/5) still commits ([dedup]/[errors-shape],
// AC#2/AC#6). RED against the stub: 0 invoices persist, ReadyInvoices is 0
// (want 1), no RowError with Rows [4,6] exists.
func TestServiceImport_GroupDisagreeingOnTotalQuarantinedOthersCommit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-02 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-02 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-CLEAN", "2026-01-10", "TIN-C", "Clean Co", "NGN", "300.00", "30.00", "330.00", "Item1", "1", "100.00"), // sheet 2
		mkRow("INV-CLEAN", "2026-01-10", "TIN-C", "Clean Co", "NGN", "300.00", "30.00", "330.00", "Item2", "1", "100.00"), // sheet 3
		mkRow("INV-BAD", "2026-02-01", "TIN-D", "Bad Co", "NGN", "100.00", "0.00", "100.00", "BadItem1", "1", "100.00"),   // sheet 4
		mkRow("INV-CLEAN", "2026-01-10", "TIN-C", "Clean Co", "NGN", "300.00", "30.00", "330.00", "Item3", "1", "100.00"), // sheet 5
		mkRow("INV-BAD", "2026-02-01", "TIN-D", "Bad Co", "NGN", "100.00", "0.00", "200.00", "BadItem2", "1", "200.00"),   // sheet 6 -- total differs
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.RowsTotal != 5 || res.RowsValid != 3 || res.RowsInvalid != 2 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (5,3,2)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.RowsValid+res.RowsInvalid != res.RowsTotal {
		t.Errorf("RowsValid+RowsInvalid = %d, want RowsTotal %d", res.RowsValid+res.RowsInvalid, res.RowsTotal)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	re, found := findRowErrorWithRows(res.Errors, []int{4, 6})
	if !found {
		t.Fatalf("no RowError with Rows==[4,6] in %+v", res.Errors)
	}
	if re.Field != "total" {
		t.Errorf("conflicting RowError.Field = %q, want %q", re.Field, "total")
	}

	if got := countInvoicesForEntity(t, super, entityID); got != 1 {
		t.Fatalf("persisted invoices count = %d, want 1 (only INV-CLEAN)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-BAD"); got != 0 {
		t.Errorf("INV-BAD persisted rows = %d, want 0 (quarantined)", got)
	}
	invClean := invoiceIDByNumber(t, super, entityID, "INV-CLEAN")
	if got := countLineItems(t, super, invClean); got != 3 {
		t.Errorf("INV-CLEAN line item count = %d, want 3", got)
	}
}

// --- IMP-SVC-03 -----------------------------------------------------------

// IMP-SVC-03: the file's invoice_number collides with a PRE-SEEDED stored
// invoice for the entity -> quarantined pre-insert (against-stored,
// [dedup-boundary]); a clean sibling in the same file still commits (AC#3).
// RED against the stub: ReadyInvoices 0 (want 1), no quarantine error, and
// the pre-seeded row is the only invoice (persisted-count assertions on the
// clean sibling fail).
func TestServiceImport_CollidesWithPreSeededStoredInvoiceQuarantinedSiblingCommits(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-03 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-03 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-DUP")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-DUP", "2026-01-10", "TIN-E", "Dup Co", "NGN", "150.00", "15.00", "165.00", "DupItem1", "1", "150.00"),    // sheet 2
		mkRow("INV-DUP", "2026-01-10", "TIN-E", "Dup Co", "NGN", "150.00", "15.00", "165.00", "DupItem2", "1", "150.00"),    // sheet 3
		mkRow("INV-CLEAN2", "2026-01-12", "TIN-F", "Clean2 Co", "NGN", "80.00", "8.00", "88.00", "CleanItem", "1", "80.00"), // sheet 4
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.RowsTotal != 3 || res.RowsValid != 1 || res.RowsInvalid != 2 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (3,1,2)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	re, found := findRowErrorWithRows(res.Errors, []int{2, 3})
	if !found {
		t.Fatalf("no RowError with Rows==[2,3] in %+v", res.Errors)
	}
	if re.Field != "invoice_number" {
		t.Errorf("against-stored RowError.Field = %q, want %q", re.Field, "invoice_number")
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP"); got != 1 {
		t.Errorf("INV-DUP rows = %d, want exactly 1 (the pre-seeded one, no duplicate inserted)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-CLEAN2"); got != 1 {
		t.Errorf("INV-CLEAN2 rows = %d, want 1 (committed)", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 2 {
		t.Errorf("total persisted invoices = %d, want 2", got)
	}
}

// --- IMP-SVC-04 -----------------------------------------------------------

// IMP-SVC-04: a group spanning sheet rows 5 and 8 disagreeing on buyer_tin
// -> RowError.Rows == [5,8] exactly (AC#2/AC#6); 5 other clean singleton
// invoices still commit. RED against the stub: no such RowError exists and
// ReadyInvoices is 0 (want 5).
func TestServiceImport_HeaderFieldConflictRowErrorCitesExactSheetRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-04 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-04 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-1", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),                    // sheet 2
		mkRow("INV-2", "2026-01-10", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "Item2", "1", "20.00"),                    // sheet 3
		mkRow("INV-3", "2026-01-10", "T3", "B3", "NGN", "30.00", "3.00", "33.00", "Item3", "1", "30.00"),                    // sheet 4
		mkRow("INV-CONFLICT", "2026-01-10", "TIN-A", "Conflict Co", "NGN", "40.00", "4.00", "44.00", "ItemA", "1", "40.00"), // sheet 5
		mkRow("INV-4", "2026-01-10", "T4", "B4", "NGN", "50.00", "5.00", "55.00", "Item4", "1", "50.00"),                    // sheet 6
		mkRow("INV-5", "2026-01-10", "T5", "B5", "NGN", "60.00", "6.00", "66.00", "Item5", "1", "60.00"),                    // sheet 7
		mkRow("INV-CONFLICT", "2026-01-10", "TIN-B", "Conflict Co", "NGN", "40.00", "4.00", "44.00", "ItemB", "1", "40.00"), // sheet 8 -- buyer_tin differs
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.RowsTotal != 7 || res.RowsValid != 5 || res.RowsInvalid != 2 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (7,5,2)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 5 || res.QuarantinedInvoices != 1 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (5,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	re, found := findRowErrorWithRows(res.Errors, []int{5, 8})
	if !found {
		t.Fatalf("no RowError with Rows==[5,8] in %+v", res.Errors)
	}
	if re.Field != "buyer_tin" {
		t.Errorf("conflicting RowError.Field = %q, want %q", re.Field, "buyer_tin")
	}

	if got := countInvoicesForEntity(t, super, entityID); got != 5 {
		t.Errorf("persisted invoices count = %d, want 5", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-CONFLICT"); got != 0 {
		t.Errorf("INV-CONFLICT persisted rows = %d, want 0", got)
	}
}

// --- IMP-SVC-05 -----------------------------------------------------------

// IMP-SVC-05: subtotal="100" while the group's 2 line rows' unit_price*qty
// sum to 90 -- the draft persists subtotal="100.00" VERBATIM, never derived
// from the lines ([no-derive], AC#7). RED against the stub: no invoice
// persists at all.
func TestServiceImport_SubtotalPersistsVerbatimNotDerivedFromLineItems(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-05 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-05 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-NODERIVE", "2026-01-10", "TIN-G", "NoDerive Co", "NGN", "100", "0", "100", "LineA", "1", "40.00"), // sheet 2
		mkRow("INV-NODERIVE", "2026-01-10", "TIN-G", "NoDerive Co", "NGN", "100", "0", "100", "LineB", "1", "50.00"), // sheet 3 -- 40+50=90 != 100
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,0) -- a subtotal/line mismatch is not a quarantine reason", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	invID := invoiceIDByNumber(t, super, entityID, "INV-NODERIVE")
	got := invoiceSubtotal(t, super, invID)
	if got == nil || *got != "100.00" {
		t.Errorf("persisted subtotal = %v, want \"100.00\" (verbatim, NOT derived from 40+50=90)", got)
	}
}

// --- IMP-SVC-06 / IMP-SVC-07 (shared fixture) ------------------------------

// mixedFileFixture returns one header+rows combination containing all four
// row dispositions in one file: a clean singleton (INV-CLEAN1), a clean
// 2-row group (INV-CLEAN2), a 2-row group quarantined for an in-file `total`
// conflict (INV-CONFLICT, sheet rows 4 and 7), and one ungroupable
// blank-invoice_number row (sheet 5). Used verbatim by both IMP-SVC-06
// (dry-run) and IMP-SVC-07 (real) so they exercise the SAME bytes.
func mixedFileFixture() [][]string {
	return [][]string{
		mkRow("INV-CLEAN1", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),       // sheet 2
		mkRow("INV-CLEAN2", "2026-01-11", "T2", "B2", "NGN", "50.00", "5.00", "55.00", "ItemA", "1", "25.00"),       // sheet 3
		mkRow("INV-CONFLICT", "2026-02-01", "T3", "B3", "NGN", "0.00", "0.00", "100.00", "BadItem1", "1", "100.00"), // sheet 4
		mkRow("", "2026-01-10", "T4", "B4", "NGN", "5.00", "0.00", "5.00", "Blank", "1", "5.00"),                    // sheet 5 -- blank invoice number
		mkRow("INV-CLEAN2", "2026-01-11", "T2", "B2", "NGN", "50.00", "5.00", "55.00", "ItemB", "1", "25.00"),       // sheet 6
		mkRow("INV-CONFLICT", "2026-02-01", "T3", "B3", "NGN", "0.00", "0.00", "200.00", "BadItem2", "1", "200.00"), // sheet 7 -- total differs
	}
}

// assertMixedFileVerdict checks the classification-derived counts that must
// hold identically whether the fixture was run dry or real (IMP-SVC-06/07's
// shared verdict): RowsTotal 6 (1+2 valid, 2+1 invalid), ReadyInvoices 2
// (INV-CLEAN1, INV-CLEAN2), QuarantinedInvoices 2 (INV-CONFLICT, the blank
// row), 2 RowErrors.
func assertMixedFileVerdict(t *testing.T, res BatchResult) {
	t.Helper()
	if res.RowsTotal != 6 || res.RowsValid != 3 || res.RowsInvalid != 3 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (6,3,3)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.RowsValid+res.RowsInvalid != res.RowsTotal {
		t.Errorf("RowsValid+RowsInvalid = %d, want RowsTotal %d", res.RowsValid+res.RowsInvalid, res.RowsTotal)
	}
	if res.ReadyInvoices != 2 || res.QuarantinedInvoices != 2 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (2,2)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 2 {
		t.Errorf("len(Errors) = %d, want 2", len(res.Errors))
	}
	if _, found := findRowErrorWithRows(res.Errors, []int{4, 7}); !found {
		t.Errorf("no RowError with Rows==[4,7] (INV-CONFLICT) in %+v", res.Errors)
	}
	if _, found := findRowErrorWithRows(res.Errors, []int{5}); !found {
		t.Errorf("no RowError with Row==5 (blank invoice_number) in %+v", res.Errors)
	}
}

// IMP-SVC-06: Import(dryRun=true) over the mixed file classifies and returns
// the verdict but writes NOTHING -- 0 import_batches, 0 invoices (AC#8). RED
// against the stub: the verdict counts are all 0 (want the mixed-file
// numbers above), though the "writes nothing" assertions happen to pass
// vacuously against the stub (it never touches the DB either) -- documented
// here so a future reader isn't confused why only some assertions are red.
func TestServiceImport_DryRunClassifiesOnlyDBUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-06 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-06 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, mixedFileFixture(), true)
	if err != nil {
		t.Fatalf("Import (dry-run): %v", err)
	}
	if res.ID != "" || res.Status != "" {
		t.Errorf("dry-run (ID=%q Status=%q), want both empty", res.ID, res.Status)
	}
	assertMixedFileVerdict(t, res)

	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("dry-run wrote %d import_batches rows, want 0", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("dry-run wrote %d invoices rows, want 0", got)
	}
}

// IMP-SVC-07: the SAME mixed file, Import(dryRun=false) -- persisted
// counts/errors match the IMP-SVC-06 verdict exactly, and the committed
// draft count equals ReadyInvoices ([batch semantics], AC#8). RED against
// the stub: ReadyInvoices is 0 (want 2), and no invoices persist.
func TestServiceImport_SameMixedFileRealImportMatchesDryRunVerdict(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-07 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-07 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, mixedFileFixture(), false)
	if err != nil {
		t.Fatalf("Import (real): %v", err)
	}
	if res.ID == "" || res.Status != "completed" {
		t.Errorf("real import (ID=%q Status=%q), want (non-empty, completed)", res.ID, res.Status)
	}
	assertMixedFileVerdict(t, res)

	if got := countImportBatchesForEntity(t, super, entityID); got != 1 {
		t.Errorf("import_batches rows = %d, want 1", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != res.ReadyInvoices {
		t.Errorf("committed invoices = %d, want == ReadyInvoices (%d)", got, res.ReadyInvoices)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 2 {
		t.Errorf("committed invoices = %d, want 2 (INV-CLEAN1, INV-CLEAN2)", got)
	}
	invClean2 := invoiceIDByNumber(t, super, entityID, "INV-CLEAN2")
	if got, want := lineItemDescriptions(t, super, invClean2), []string{"ItemA", "ItemB"}; !stringSliceEqual(got, want) {
		t.Errorf("INV-CLEAN2 line items = %v, want %v", got, want)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-CONFLICT"); got != 0 {
		t.Errorf("INV-CONFLICT persisted = %d, want 0", got)
	}
}

// --- IMP-SVC-08 -----------------------------------------------------------

// IMP-SVC-08: a row with a blank mapped invoice_number is ungroupable ->
// quarantined with a SCALAR RowError.Row citing its own 1-based sheet row
// (AC#6); the other, clean row still commits. RED against the stub: no
// invoice persists and no such RowError exists.
func TestServiceImport_BlankInvoiceNumberRowQuarantinedUngroupableScalarRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-08 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-08 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-OK", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"), // sheet 2
		mkRow("", "2026-01-10", "T2", "B2", "NGN", "5.00", "0.00", "5.00", "Blank", "1", "5.00"),          // sheet 3 -- blank invoice_number
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.RowsTotal != 2 || res.RowsValid != 1 || res.RowsInvalid != 1 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (2,1,1)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
	}
	re := res.Errors[0]
	if re.Row != 3 {
		t.Errorf("blank-row RowError.Row = %d, want 3 (scalar, its own sheet row)", re.Row)
	}
	if len(re.Rows) != 0 {
		t.Errorf("blank-row RowError.Rows = %v, want empty (scalar Row, not plural Rows)", re.Rows)
	}
	if re.Message == "" {
		t.Errorf("blank-row RowError.Message is empty, want a description")
	}

	if got := countInvoicesForEntity(t, super, entityID); got != 1 {
		t.Errorf("persisted invoices = %d, want 1 (INV-OK only)", got)
	}
}

// --- IMP-SVC-09 -----------------------------------------------------------

// IMP-SVC-09: mapping omits invoice_number entirely -> ErrValidation BEFORE
// any write (AC's validation contract). RED against the stub: err is nil,
// want ErrValidation.
func TestServiceImport_MappingMissingInvoiceNumberValidationBeforeAnyWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-09 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-09 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	mapping := cloneMapping(stdMapping)
	delete(mapping, "invoice_number")

	rows := [][]string{
		mkRow("INV-X", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	res, err := svc.Import(c, entityID, mapping, stdHeader, rows, false)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Import err = %v, want ErrValidation (mapping has no invoice_number)", err)
	}
	if res.RowsTotal != 0 || res.RowsValid != 0 || res.RowsInvalid != 0 || len(res.Errors) != 0 {
		t.Errorf("on validation failure, res = %+v, want the zero BatchResult", res)
	}

	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("import_batches rows = %d, want 0 (validation fails before any write)", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices rows = %d, want 0", got)
	}
}

// --- IMP-SVC-10 -----------------------------------------------------------

// IMP-SVC-10: mapping references a header string not present in row 1 ->
// ErrValidation BEFORE any write. RED against the stub: err is nil, want
// ErrValidation.
func TestServiceImport_MappingReferencesAbsentHeaderValidationBeforeAnyWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-10 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-10 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	mapping := cloneMapping(stdMapping)
	mapping["buyer_name"] = "Nonexistent Column" // not in stdHeader

	rows := [][]string{
		mkRow("INV-X", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	res, err := svc.Import(c, entityID, mapping, stdHeader, rows, false)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Import err = %v, want ErrValidation (mapped header string absent from row 1)", err)
	}
	if res.RowsTotal != 0 || len(res.Errors) != 0 {
		t.Errorf("on validation failure, res = %+v, want the zero BatchResult", res)
	}

	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("import_batches rows = %d, want 0", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices rows = %d, want 0", got)
	}
}

// --- IMP-SVC-10b (CodeRabbit, M4-03 PR review) -----------------------------

// TestServiceImport_MappingUnknownKeyValidationBeforeAnyWrite: a mapping
// containing a KEY that isn't one of the 11 canonical fields (e.g. a typo
// "totla" instead of "total") must ErrValidation BEFORE any write, by exact
// symmetry with IMP-SVC-10's absent-mapped-header check: [mapping]'s
// guarantee is that the server structurally cannot mis-map, so an unknown
// mapping KEY must 400 just as firmly as an absent mapped HEADER does --
// silently ignoring "totla" would otherwise import total as NULL with no
// error at all.
func TestServiceImport_MappingUnknownKeyValidationBeforeAnyWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-10b tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-10b entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	mapping := cloneMapping(stdMapping)
	delete(mapping, "total")
	mapping["totla"] = "Total" // typo: not a canonical field name

	rows := [][]string{
		mkRow("INV-X", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	res, err := svc.Import(c, entityID, mapping, stdHeader, rows, false)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Import err = %v, want ErrValidation (mapping key %q is not a recognized canonical field)", err, "totla")
	}
	if res.RowsTotal != 0 || len(res.Errors) != 0 {
		t.Errorf("on validation failure, res = %+v, want the zero BatchResult", res)
	}

	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("import_batches rows = %d, want 0 (validation fails before any write)", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices rows = %d, want 0", got)
	}
}

// --- IMP-SVC-11 -----------------------------------------------------------

// IMP-SVC-11: a tin-less entity (seedEntity leaves tin NULL, same as
// production for an entity that never recorded one) -- an otherwise clean
// file still commits, with supplier_tin persisted NULL
// ([supplier-from-entity]); import must NOT reject for a missing tin. RED
// against the stub: no invoice persists.
func TestServiceImport_TinLessEntityCommitsWithNilSupplierTIN(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-11 tenant")
	const entityName = "IMP-SVC-11 entity"
	entityID := seedEntity(t, super, tenantID, entityName)

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-TINLESS", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (1,0,[]) -- a tin-less entity must not be rejected", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}

	invID := invoiceIDByNumber(t, super, entityID, "INV-TINLESS")
	name, tin := invoiceSupplier(t, super, invID)
	if tin != nil {
		t.Errorf("supplier_tin = %q, want nil", *tin)
	}
	if name == nil || *name != entityName {
		t.Errorf("supplier_name = %v, want %q", name, entityName)
	}
}

// --- IMP-SVC-12 -----------------------------------------------------------

// IMP-SVC-12: simulates a concurrent stored duplicate that only appears at
// INSERT time -- the one ExistingNumbers's single upfront pre-check cannot
// see. HOW THIS IS SIMULATED (documented per task-105's guidance: a clean
// black-box 23505-at-Create reproduction is hard, so assert the observable
// outcome instead): rather than instrumenting a single Import() call, this
// fires `racers` (4) concurrent Import() calls -- each a separate one-row
// file for the SAME entity_id + invoice_number "INV-RACE" -- released
// together off one start barrier (mirrors internal/invoice's own
// TestTransition_ConcurrentSameEdgeSerializesToOneWinner, which races N=6
// concurrent Transition calls on one row for the same reason: more
// contenders raise the odds of a genuine DB-level race, though the
// AGGREGATE outcome asserted below is deterministic regardless of exact
// goroutine timing -- Postgres's unique index on
// (tenant_id, entity_id, invoice_number) guarantees exactly one of the N
// concurrent inserts of that key wins, full stop). Because none of the N
// racing imports pre-exists in invoices before they all start, whichever
// loses can only discover the conflict at Create's 23505 (if its own
// ExistingNumbers precheck ran before the winner committed) or, in the less
// interesting case, at its own precheck if the winner happened to commit
// first -- either way, task-105 requires "QUARANTINE ON ANY Create error",
// so a losing precheck-catch and a losing Create-time-catch both look
// identical from BatchResult's perspective, and that shared, observable
// outcome (only ONE invoice survives, every run reports 'completed', a
// concurrently-imported, non-colliding invoice still commits) is exactly
// what this test asserts. RED against the stub: every result is the zero
// BatchResult (Status "" not "completed", Ready/Quarantined both 0).
func TestServiceImport_ConcurrentDuplicateAtCreateTimeQuarantinesLoser(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-12 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-12 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const racers = 4
	results := make([]BatchResult, racers+1)
	errs := make([]error, racers+1)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(racers + 1)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			raceRow := [][]string{
				mkRow("INV-RACE", "2026-01-10", "TIN-RACE", "Racer", "NGN", "100.00", "0.00", "100.00", fmt.Sprintf("RaceItem%d", i), "1", "100.00"),
			}
			results[i], errs[i] = svc.Import(c, entityID, stdMapping, stdHeader, raceRow, false)
		}(i)
	}
	go func() {
		defer wg.Done()
		<-start
		cleanRow := [][]string{
			mkRow("INV-RACE-CLEAN", "2026-01-10", "TIN-CLEAN", "Clean", "NGN", "50.00", "0.00", "50.00", "CleanItem", "1", "50.00"),
		}
		results[racers], errs[racers] = svc.Import(c, entityID, stdMapping, stdHeader, cleanRow, false)
	}()
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Import[%d] unexpected top-level error: %v (per-group Create errors must be captured as RowErrors, not returned)", i, err)
		}
	}

	ready, quarantined := 0, 0
	for i := 0; i < racers; i++ {
		r := results[i]
		if r.Status != "completed" {
			t.Errorf("racer[%d].Status = %q, want %q (a losing race is still a completed run, never failed)", i, r.Status, "completed")
		}
		ready += r.ReadyInvoices
		quarantined += r.QuarantinedInvoices
	}
	if ready != 1 || quarantined != racers-1 {
		t.Errorf("aggregate racer outcome (ready=%d quarantined=%d), want (1,%d) -- exactly one of the %d concurrent INV-RACE imports must win", ready, quarantined, racers-1, racers)
	}

	clean := results[racers]
	if clean.Status != "completed" || clean.ReadyInvoices != 1 || clean.QuarantinedInvoices != 0 {
		t.Errorf("concurrently-run clean import = %+v, want (Status=completed ReadyInvoices=1 QuarantinedInvoices=0)", clean)
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-RACE"); got != 1 {
		t.Errorf("stored INV-RACE rows = %d, want exactly 1 despite %d concurrent commit attempts", got, racers)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-RACE-CLEAN"); got != 1 {
		t.Errorf("stored INV-RACE-CLEAN rows = %d, want 1", got)
	}
}

// --- IMP-SVC-13 -----------------------------------------------------------

// IMP-SVC-13: ONE run mixing every invalid source -- an ungroupable
// blank-number row, a header-conflict-quarantined group, an
// against-stored-quarantined group, and a clean group -- asserts
// rows_valid+rows_invalid==rows_total EXACTLY, ReadyInvoices+
// QuarantinedInvoices == the 4 distinct groups, and no row is
// double-counted across the RowErrors (AC#9). RED against the stub: all
// counts are 0, and there are no RowErrors to inspect.
func TestServiceImport_AllInvalidSourcesMixedCountersExactNoDoubleCount(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-13 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-13 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-STORED")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-CLEAN", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),                  // sheet 2
		mkRow("INV-STORED", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "StoredItem1", "1", "20.00"),           // sheet 3
		mkRow("", "2026-01-12", "T3", "B3", "NGN", "5.00", "0.00", "5.00", "BlankItem", "1", "5.00"),                          // sheet 4 -- blank
		mkRow("INV-CONFLICT", "2026-01-13", "T4", "Alpha Co", "NGN", "30.00", "3.00", "33.00", "ConflictItem1", "1", "30.00"), // sheet 5
		mkRow("INV-STORED", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "StoredItem2", "1", "20.00"),           // sheet 6
		mkRow("INV-CONFLICT", "2026-01-13", "T4", "Beta Co", "NGN", "30.00", "3.00", "33.00", "ConflictItem2", "1", "30.00"),  // sheet 7 -- buyer_name differs
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if res.RowsTotal != 6 {
		t.Fatalf("RowsTotal = %d, want 6", res.RowsTotal)
	}
	if res.RowsValid+res.RowsInvalid != res.RowsTotal {
		t.Errorf("RowsValid(%d)+RowsInvalid(%d) = %d, want RowsTotal %d", res.RowsValid, res.RowsInvalid, res.RowsValid+res.RowsInvalid, res.RowsTotal)
	}
	if res.RowsValid != 1 || res.RowsInvalid != 5 {
		t.Errorf("(RowsValid=%d RowsInvalid=%d), want (1,5)", res.RowsValid, res.RowsInvalid)
	}

	const distinctGroups = 4 // INV-CLEAN, INV-STORED, the blank row, INV-CONFLICT
	if res.ReadyInvoices+res.QuarantinedInvoices != distinctGroups {
		t.Errorf("ReadyInvoices(%d)+QuarantinedInvoices(%d) = %d, want %d distinct groups", res.ReadyInvoices, res.QuarantinedInvoices, res.ReadyInvoices+res.QuarantinedInvoices, distinctGroups)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 3 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,3)", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	if len(res.Errors) != 3 {
		t.Fatalf("len(Errors) = %d, want 3: %+v", len(res.Errors), res.Errors)
	}

	allRows := allRowNumbers(res.Errors)
	sort.Ints(allRows)
	wantRows := []int{3, 4, 5, 6, 7}
	if !intSliceEqual(allRows, wantRows) {
		t.Errorf("union of every RowError's row numbers = %v, want %v (no gap, no double-count)", allRows, wantRows)
	}
	if len(allRows) != res.RowsInvalid {
		t.Errorf("total row numbers referenced across Errors = %d, want == RowsInvalid (%d)", len(allRows), res.RowsInvalid)
	}
	seen := map[int]bool{}
	for _, r := range allRows {
		if seen[r] {
			t.Errorf("row %d referenced by more than one RowError (double-counted)", r)
		}
		seen[r] = true
	}

	if got := countInvoicesForEntity(t, super, entityID); got != 2 {
		t.Errorf("persisted invoices = %d, want 2 (pre-seeded INV-STORED + committed INV-CLEAN)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-CLEAN"); got != 1 {
		t.Errorf("INV-CLEAN rows = %d, want 1", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-STORED"); got != 1 {
		t.Errorf("INV-STORED rows = %d, want 1 (unchanged, no duplicate inserted)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-CONFLICT"); got != 0 {
		t.Errorf("INV-CONFLICT rows = %d, want 0", got)
	}
}

// --- IMP-SVC-14 -----------------------------------------------------------

// IMP-SVC-14: an accounting-formatted subtotal cell "1,058,875.00" (WITH
// separators, as Decode would surface it verbatim off a CSV or XLSX cell)
// normalizes to 1058875.00 and COMMITS, not quarantined
// ([numeric-normalization], AC#4 -- proves accounting-format import). RED
// against the stub: no invoice persists.
func TestServiceImport_AccountingFormattedSubtotalNormalizesAndCommits(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-14 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-14 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-ACCTFMT", "2026-01-10", "T1", "B1", "NGN", "1,058,875.00", "0.00", "1,058,875.00", "BigItem", "1", "1058875.00"),
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (1,0,[]) -- accounting-formatted commas must normalize, not quarantine", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}

	invID := invoiceIDByNumber(t, super, entityID, "INV-ACCTFMT")
	got := invoiceSubtotal(t, super, invID)
	if got == nil || *got != "1058875.00" {
		t.Errorf("persisted subtotal = %v, want \"1058875.00\" (comma-stripped)", got)
	}
}

// --- IMP-SVC-15 -----------------------------------------------------------

// IMP-SVC-15: a `total` cell "N/A" (non-numeric residue surviving
// normalization -- normalization only strips commas/whitespace, so "N/A" is
// untouched) quarantines JUST that invoice via the Create error (Postgres's
// 22P02 on the ::numeric cast); the run still reports 'completed'; another,
// clean invoice in the same file commits (AC#5). RED against the stub:
// ReadyInvoices/QuarantinedInvoices are both 0 (want 1,1), Status is "" not
// "completed".
func TestServiceImport_NonNumericTotalQuarantinesViaCreateErrorOthersCommit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-15 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-15 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-BADTOTAL", "2026-01-10", "T1", "B1", "NGN", "500.00", "0.00", "N/A", "BadItem", "1", "500.00"), // sheet 2
		mkRow("INV-CLEAN3", "2026-01-11", "T2", "B2", "NGN", "80.00", "8.00", "88.00", "CleanItem", "1", "80.00"), // sheet 3
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.RowsTotal != 2 || res.RowsValid != 1 || res.RowsInvalid != 1 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (2,1,1)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if res.Status != "completed" {
		t.Errorf("Status = %q, want %q (a Create-error quarantine must not fail the whole run)", res.Status, "completed")
	}

	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
	}
	re := res.Errors[0]
	if got := rowNumbersOf(re); !intSliceEqual(got, []int{2}) {
		t.Errorf("bad-total RowError rows = %v, want [2]", got)
	}
	if re.Field != "total" {
		// task-105 hedges this as "Field:\"total\" ideally" -- a bare 22P02
		// from Postgres does not disambiguate which numeric column broke, so
		// this is a soft (non-fatal-to-the-rest) expectation, not a hard
		// contract; still reported red so the executor tries for it.
		t.Errorf("bad-total RowError.Field = %q, want %q (best-effort, per task-105's \"ideally\")", re.Field, "total")
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-BADTOTAL"); got != 0 {
		t.Errorf("INV-BADTOTAL persisted = %d, want 0", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-CLEAN3"); got != 1 {
		t.Errorf("INV-CLEAN3 persisted = %d, want 1", got)
	}
}

// --- IMP-SVC-16 -----------------------------------------------------------

// IMP-SVC-16 (added per M4-03-03's QA advisory): entities A and B of the
// SAME tenant; invoice_number "INV-DUP" is pre-seeded under entity B;
// importing "INV-DUP" (clean) under entity A does NOT quarantine --
// ExistingNumbers is entity-scoped, and the unique index is
// (tenant_id, entity_id, invoice_number), so a same-numbered invoice under
// a DIFFERENT entity of the same tenant is correctly not a duplicate. RED
// against the stub: ReadyInvoices is 0 (want 1), no invoice persists under
// entity A.
func TestServiceImport_EntityScopedDedupSameNumberUnderDifferentEntityNotDuplicate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-16 tenant")
	entityA := seedEntity(t, super, tenantID, "IMP-SVC-16 entity A")
	entityB := seedEntity(t, super, tenantID, "IMP-SVC-16 entity B")
	seedInvoice(t, super, tenantID, entityB, "INV-DUP")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-DUP", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	res, err := svc.Import(c, entityA, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d Errors=%+v), want (1,0,[]) -- entity B's INV-DUP must not leak into entity A's dedup check", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}
	if res.Status != "completed" {
		t.Errorf("Status = %q, want %q", res.Status, "completed")
	}

	if got := countInvoicesByNumber(t, super, entityA, "INV-DUP"); got != 1 {
		t.Errorf("entity A's INV-DUP rows = %d, want 1 (committed)", got)
	}
	if got := countInvoicesByNumber(t, super, entityB, "INV-DUP"); got != 1 {
		t.Errorf("entity B's INV-DUP rows = %d, want 1 (unaffected pre-seeded row)", got)
	}
}

// --- Core AC#2 (CodeRabbit, M4-03 PR review) -------------------------------

// nonNumericTotalFixture returns a header+rows combination with ONE group
// whose `total` cell is "N/A" (a non-numeric residue surviving
// [numeric-normalization], which only strips commas/whitespace) and a clean
// sibling group -- used by BOTH the dry-run and real-import halves of
// TestServiceImport_DryRunExactVerdictForNonNumericField below, so Core AC#2
// ("the same file + mapping can be dry-run to get the EXACT READY/
// QUARANTINED verdict the real import will produce") is asserted on the
// SAME bytes.
func nonNumericTotalFixture() [][]string {
	return [][]string{
		mkRow("INV-BADNUM", "2026-01-10", "T1", "B1", "NGN", "500.00", "0.00", "N/A", "BadItem", "1", "500.00"),     // sheet 2
		mkRow("INV-CLEANNUM", "2026-01-11", "T2", "B2", "NGN", "80.00", "8.00", "88.00", "CleanItem", "1", "80.00"), // sheet 3
	}
}

// assertNonNumericTotalVerdict checks the classification-derived counts that
// must hold identically whether nonNumericTotalFixture was run dry or real.
func assertNonNumericTotalVerdict(t *testing.T, res BatchResult) {
	t.Helper()
	if res.RowsTotal != 2 || res.RowsValid != 1 || res.RowsInvalid != 1 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (2,1,1)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
	}
	re := res.Errors[0]
	if got := rowNumbersOf(re); !intSliceEqual(got, []int{2}) {
		t.Errorf("bad-total RowError rows = %v, want [2]", got)
	}
	if re.Field != "total" {
		t.Errorf("bad-total RowError.Field = %q, want %q", re.Field, "total")
	}
}

// TestServiceImport_DryRunExactVerdictForNonNumericField is Core AC#2's own
// assertion. Before this fix, numeric validity was deferred entirely to
// Postgres's ::numeric cast at Create time, which a dry-run never reaches --
// so a `total` cell like "N/A" reported READY in dry-run but quarantined in
// the real import, violating AC#2. bestEffortBadNumericField is now promoted
// into the classify step (service.go), run in BOTH dry-run and real, so both
// halves below -- run over the SAME fixture bytes -- must produce the
// IDENTICAL verdict.
func TestServiceImport_DryRunExactVerdictForNonNumericField(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-DRYNUM tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-DRYNUM entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	dryRes, err := svc.Import(c, entityID, stdMapping, stdHeader, nonNumericTotalFixture(), true)
	if err != nil {
		t.Fatalf("Import (dry-run): %v", err)
	}
	if dryRes.ID != "" || dryRes.Status != "" {
		t.Errorf("dry-run (ID=%q Status=%q), want both empty", dryRes.ID, dryRes.Status)
	}
	assertNonNumericTotalVerdict(t, dryRes)
	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("dry-run wrote %d import_batches rows, want 0", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("dry-run wrote %d invoices rows, want 0", got)
	}

	realRes, err := svc.Import(c, entityID, stdMapping, stdHeader, nonNumericTotalFixture(), false)
	if err != nil {
		t.Fatalf("Import (real): %v", err)
	}
	if realRes.Status != "completed" || realRes.ID == "" {
		t.Errorf("real import (Status=%q ID=%q), want (completed, non-empty)", realRes.Status, realRes.ID)
	}
	assertNonNumericTotalVerdict(t, realRes)

	if dryRes.RowsTotal != realRes.RowsTotal || dryRes.RowsValid != realRes.RowsValid ||
		dryRes.RowsInvalid != realRes.RowsInvalid || dryRes.ReadyInvoices != realRes.ReadyInvoices ||
		dryRes.QuarantinedInvoices != realRes.QuarantinedInvoices {
		t.Errorf("dry-run verdict %+v does not match real verdict %+v -- Core AC#2 requires them to be EXACTLY the same", dryRes, realRes)
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-BADNUM"); got != 0 {
		t.Errorf("INV-BADNUM persisted = %d, want 0 (quarantined at classify time, never reaches Create)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-CLEANNUM"); got != 1 {
		t.Errorf("INV-CLEANNUM persisted = %d, want 1", got)
	}
}

// --- Domain-only quarantine (CodeRabbit, M4-03 PR review) ------------------

// TestServiceImport_OperationalCreateFailureAbortsRunNotQuarantined: a
// genuinely OPERATIONAL Create failure must ABORT the whole run (Finalize
// best-effort to 'failed', Import returns the raw error) rather than being
// quarantined as a bad ROW -- a DB-level failure must never masquerade as
// "N invalid rows" or leak Postgres's raw error text to the client
// ([review-authority] governs bad DATA, not infrastructure failure).
//
// HOW THIS IS SIMULATED, without contorting Service's design to inject a
// fake Store (Service holds a concrete *invoice.Store, not an interface, so
// there is no seam to return an arbitrary mocked error): an empty
// auth.Identity.Subject passes db.WithinRequestTenantTx's own
// identity-presence check (it only requires TenantID, see
// internal/platform/db/tenant.go) but makes invoice_status_history's own
// `actor` CHECK (char_length(actor) > 0, see
// migrations/20260714111246_invoice_status_history.sql) fail once
// invoice.Store.Create reaches that INSERT -- a genuine check_violation
// (23514) on a DIFFERENT table's constraint, which is NOT one of Create's
// own domain sentinels (ErrDuplicateNumber's 23505, ErrValidation's
// 23503/22P02 on the invoices INSERT itself), so it propagates raw. This
// stands in for "a connection failure / unexpected bug" as a real,
// non-mocked non-domain error.
func TestServiceImport_OperationalCreateFailureAbortsRunNotQuarantined(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IMP-SVC-OPFAIL tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-SVC-OPFAIL entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "", Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-OPFAIL", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err == nil {
		t.Fatal("Import: err = nil, want the raw operational error to propagate (not be swallowed into a RowError)")
	}
	if errors.Is(err, invoice.ErrValidation) || errors.Is(err, invoice.ErrDuplicateNumber) {
		t.Errorf("Import err = %v, want a NON-domain error (not ErrValidation/ErrDuplicateNumber)", err)
	}
	if res.ID != "" {
		t.Errorf("res.ID = %q on an aborted run, want empty (BatchResult{} on error)", res.ID)
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-OPFAIL"); got != 0 {
		t.Errorf("INV-OPFAIL persisted = %d, want 0 (Create's tx rolled back)", got)
	}

	var status string
	if err := super.QueryRow(ctx,
		`SELECT status FROM import_batches WHERE entity_id = $1`, entityID,
	).Scan(&status); err != nil {
		t.Fatalf("read back the batch's terminal status: %v", err)
	}
	if status != "failed" {
		t.Errorf("import_batches.status = %q, want %q (a truthful failed batch, never a laundered 'completed' with a fake RowError)", status, "failed")
	}
}
