// service_source_rows_test.go: buildCreateInput/Service.Import wiring
// invoiceGroup.rowIdxs into invoice.CreateInput.SourceRows, inside the same
// documentID != "" guard SourceDocumentID already uses.
//
// Written RED, before CreateInput.SourceRows or buildCreateInput's one new
// line exist -- see the DOC-02-01 story / task-357 Test Specs table.
package importer

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedDocument inserts one documents row as the superuser and returns its
// id -- mirrors internal/invoice/source_document_test.go's own helper of the
// same name (different package, so not a redefinition).
func seedDocument(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, "t/"+tenantID+"/"+uuid.NewString(), strings.Repeat("a", 64), int64(11),
	).Scan(&id); err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	return id
}

// sourceRowsOf reads invoices.source_rows directly via the superuser pool.
func sourceRowsOf(t *testing.T, super *pgxpool.Pool, invoiceID string) []int {
	t.Helper()
	var out []int
	if err := super.QueryRow(context.Background(),
		`SELECT source_rows FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&out); err != nil {
		t.Fatalf("read invoices.source_rows: %v", err)
	}
	return out
}

// intSliceEqual is defined in service_test.go (same package) -- reused, not
// redeclared.

// buildCreateInputFixture returns a 1-invoice-group colIndex resolved from
// stdMapping/stdHeader, reused by the three buildCreateInput unit tests
// below.
func buildCreateInputFixture(t *testing.T) map[string]int {
	t.Helper()
	colIndex, err := resolveMapping(stdMapping, stdHeader)
	if err != nil {
		t.Fatalf("resolveMapping: %v", err)
	}
	return colIndex
}

// TestBuildCreateInput_SetsSourceRowsFromGroup (AC-2): a group at
// rowIdxs=[0,1,2] maps to sheet rows [2,3,4] (sheetRow(i) = i+2).
func TestBuildCreateInput_SetsSourceRowsFromGroup(t *testing.T) {
	colIndex := buildCreateInputFixture(t)
	rows := [][]string{
		mkRow("INV-1", "", "", "", "", "", "", "", "Item A", "1", "1.00"),
		mkRow("INV-1", "", "", "", "", "", "", "", "Item B", "1", "1.00"),
		mkRow("INV-1", "", "", "", "", "", "", "", "Item C", "1", "1.00"),
	}
	g := &invoiceGroup{number: "INV-1", rowIdxs: []int{0, 1, 2}}

	in := buildCreateInput("entity-1", rows, colIndex, g, "batch-1", "doc-1", "Acme", nil)

	want := []int{2, 3, 4}
	if len(in.SourceRows) != len(want) {
		t.Fatalf("SourceRows length = %d, want %d (got %v)", len(in.SourceRows), len(want), in.SourceRows)
	}
	if !intSliceEqual(in.SourceRows, want) {
		t.Errorf("SourceRows = %v, want %v", in.SourceRows, want)
	}
	if in.SourceDocumentID == nil || *in.SourceDocumentID != "doc-1" {
		t.Errorf("SourceDocumentID = %v, want \"doc-1\"", in.SourceDocumentID)
	}
}

// TestBuildCreateInput_NoDocumentLeavesSourceRowsNil (AC-5): documentID=""
// must leave SourceRows nil, not merely empty -- []int{} has len 0 too and
// is precisely the failure mode this guards against.
func TestBuildCreateInput_NoDocumentLeavesSourceRowsNil(t *testing.T) {
	colIndex := buildCreateInputFixture(t)
	rows := [][]string{
		mkRow("INV-1", "", "", "", "", "", "", "", "Item A", "1", "1.00"),
		mkRow("INV-1", "", "", "", "", "", "", "", "Item B", "1", "1.00"),
		mkRow("INV-1", "", "", "", "", "", "", "", "Item C", "1", "1.00"),
	}
	g := &invoiceGroup{number: "INV-1", rowIdxs: []int{0, 1, 2}}

	in := buildCreateInput("entity-1", rows, colIndex, g, "batch-1", "", "Acme", nil)

	if in.SourceRows != nil {
		t.Errorf("SourceRows = %#v, want nil (not merely empty) when documentID is \"\"", in.SourceRows)
	}
	if in.SourceDocumentID != nil {
		t.Errorf("SourceDocumentID = %q, want nil when documentID is \"\"", *in.SourceDocumentID)
	}
}

// TestBuildCreateInput_NonContiguousGroupKeepsEveryRow (AC-2): rows 0, 2, 5
// sharing one invoice_number map to sheet rows [2,4,7], ascending -- every
// row survives grouping, not just a contiguous run.
func TestBuildCreateInput_NonContiguousGroupKeepsEveryRow(t *testing.T) {
	colIndex := buildCreateInputFixture(t)
	rows := make([][]string, 6)
	for i := range rows {
		rows[i] = mkRow("INV-1", "", "", "", "", "", "", "", "Item", "1", "1.00")
	}
	g := &invoiceGroup{number: "INV-1", rowIdxs: []int{0, 2, 5}}

	in := buildCreateInput("entity-1", rows, colIndex, g, "batch-1", "doc-1", "Acme", nil)

	want := []int{2, 4, 7}
	if len(in.SourceRows) != len(want) {
		t.Fatalf("SourceRows length = %d, want %d (got %v)", len(in.SourceRows), len(want), in.SourceRows)
	}
	if !intSliceEqual(in.SourceRows, want) {
		t.Errorf("SourceRows = %v, want %v (ascending, non-contiguous)", in.SourceRows, want)
	}
}

// TestServiceImport_PersistsSourceRowsPerInvoice (AC-2): a real import with
// a seeded documentID stores each invoice's own, disjoint sheet rows, with
// count matching its line items.
func TestServiceImport_PersistsSourceRowsPerInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 import-rows tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 import-rows entity")
	documentID := seedDocument(t, super, tenantID)

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget A", "1", "100.00"),  // sheet 2
		mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Gadget B1", "1", "100.00"), // sheet 3
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget B", "1", "100.00"),  // sheet 4
		mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Gadget B2", "1", "100.00"), // sheet 5
	}

	res, err := svc.Import(c, entityID, "", documentID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 2 {
		t.Fatalf("ReadyInvoices = %d, want 2", res.ReadyInvoices)
	}

	invA := invoiceIDByNumber(t, super, entityID, "INV-A")
	invB := invoiceIDByNumber(t, super, entityID, "INV-B")

	rowsA := sourceRowsOf(t, super, invA)
	rowsB := sourceRowsOf(t, super, invB)

	if len(rowsA) == 0 {
		t.Fatal("INV-A source_rows is empty, want its own sheet rows")
	}
	if len(rowsB) == 0 {
		t.Fatal("INV-B source_rows is empty, want its own sheet rows")
	}

	wantA, wantB := []int{2, 4}, []int{3, 5}
	if !intSliceEqual(rowsA, wantA) {
		t.Errorf("INV-A source_rows = %v, want %v", rowsA, wantA)
	}
	if !intSliceEqual(rowsB, wantB) {
		t.Errorf("INV-B source_rows = %v, want %v", rowsB, wantB)
	}
	for _, ra := range rowsA {
		for _, rb := range rowsB {
			if ra == rb {
				t.Fatalf("source_rows overlap between INV-A and INV-B: %v vs %v", rowsA, rowsB)
			}
		}
	}

	if got, want := len(rowsA), len(lineItemDescriptions(t, super, invA)); got != want {
		t.Errorf("INV-A len(source_rows)=%d != len(line_items)=%d", got, want)
	}
	if got, want := len(rowsB), len(lineItemDescriptions(t, super, invB)); got != want {
		t.Errorf("INV-B len(source_rows)=%d != len(line_items)=%d", got, want)
	}
}

// TestServiceImport_DryRunWritesNoSourceRows (AC-2): a dry run persists
// nothing -- no invoices row exists to carry source_rows at all. Vacuity
// floor: ReadyInvoices must show the run actually happened.
func TestServiceImport_DryRunWritesNoSourceRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 dry-run tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 dry-run entity")
	documentID := seedDocument(t, super, tenantID)

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget A", "1", "100.00"),
		mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Gadget B1", "1", "100.00"),
	}

	res, err := svc.Import(c, entityID, "", documentID, stdMapping, stdHeader, rows, true)
	if err != nil {
		t.Fatalf("Import (dry-run): %v", err)
	}
	if res.ReadyInvoices != 2 {
		t.Fatalf("ReadyInvoices = %d, want 2 (vacuity floor: the run must actually happen)", res.ReadyInvoices)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("dry-run wrote %d invoices rows, want 0", got)
	}
}

// TestServiceImport_NoDocumentStillImports (AC-5): the perf_test.go:94 call
// shape (documentID == "") must still complete cleanly, with both new
// columns left NULL and nothing quarantined.
func TestServiceImport_NoDocumentStillImports(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 no-doc-import tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 no-doc-import entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget A", "1", "100.00"),
	}

	res, err := svc.Import(c, entityID, "", "", stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("Status = %q, want completed", res.Status)
	}
	if res.RowsValid <= 0 {
		t.Errorf("RowsValid = %d, want > 0", res.RowsValid)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %+v, want empty (nothing quarantined)", res.Errors)
	}

	invA := invoiceIDByNumber(t, super, entityID, "INV-A")
	if got := sourceRowsOf(t, super, invA); got != nil {
		t.Errorf("source_rows = %v, want nil", got)
	}

	var docID *string
	if err := super.QueryRow(ctx, `SELECT source_document_id::text FROM invoices WHERE id = $1`, invA).Scan(&docID); err != nil {
		t.Fatalf("read source_document_id: %v", err)
	}
	if docID != nil {
		t.Errorf("source_document_id = %q, want NULL", *docID)
	}
}
