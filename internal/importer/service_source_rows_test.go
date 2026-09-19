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
// rowIdxs=[0,1,2] maps to sheet rows [2,3,4] (sheetRow(1, i) = i+2).
func TestBuildCreateInput_SetsSourceRowsFromGroup(t *testing.T) {
	colIndex := buildCreateInputFixture(t)
	rows := [][]string{
		mkRow("INV-1", "", "", "", "", "", "", "", "Item A", "1", "1.00"),
		mkRow("INV-1", "", "", "", "", "", "", "", "Item B", "1", "1.00"),
		mkRow("INV-1", "", "", "", "", "", "", "", "Item C", "1", "1.00"),
	}
	g := &invoiceGroup{number: "INV-1", rowIdxs: []int{0, 1, 2}}

	in := buildCreateInput("entity-1", rows, colIndex, g, "batch-1", "doc-1", 1, "Acme", nil)

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

	in := buildCreateInput("entity-1", rows, colIndex, g, "batch-1", "", 1, "Acme", nil)

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

	in := buildCreateInput("entity-1", rows, colIndex, g, "batch-1", "doc-1", 1, "Acme", nil)

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

	res, err := svc.Import(c, entityID, "", documentID, 1, stdMapping, stdHeader, rows, false)
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

	res, err := svc.Import(c, entityID, "", documentID, 1, stdMapping, stdHeader, rows, true)
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

	res, err := svc.Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, false)
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

// TestSheetRow_CountsFromTheRowAfterTheHeader: sheetRow/sheetRows count from
// the row AFTER headerRow, not always row 2. RED against the stub: sheetRow
// still returns i+2 regardless of headerRow.
func TestSheetRow_CountsFromTheRowAfterTheHeader(t *testing.T) {
	if got := sheetRow(1, 0); got != 2 {
		t.Errorf("sheetRow(1, 0) = %d, want 2", got)
	}
	if got := sheetRow(1, 5); got != 7 {
		t.Errorf("sheetRow(1, 5) = %d, want 7", got)
	}
	if got := sheetRow(3, 0); got != 4 {
		t.Errorf("sheetRow(3, 0) = %d, want 4", got)
	}
	if got := sheetRow(3, 2); got != 6 {
		t.Errorf("sheetRow(3, 2) = %d, want 6", got)
	}

	want := []int{4, 6}
	if got := sheetRows(3, []int{2, 0}); !intSliceEqual(got, want) {
		t.Errorf("sheetRows(3, [2 0]) = %v, want %v (sorted)", got, want)
	}
	if got := sheetRows(3, nil); len(got) != 0 {
		t.Errorf("sheetRows(3, nil) = %v, want length 0", got)
	}
}

// TestBuildCreateInput_SetsSourceRowsFromGroupBelowATitle is
// TestBuildCreateInput_SetsSourceRowsFromGroup's twin with a title row above
// the header: the same group now counts from row 3, not row 1. RED against
// the stub, same reason as the test above.
func TestBuildCreateInput_SetsSourceRowsFromGroupBelowATitle(t *testing.T) {
	colIndex := buildCreateInputFixture(t)
	rows := [][]string{
		mkRow("INV-1", "", "", "", "", "", "", "", "Item A", "1", "1.00"),
		mkRow("INV-1", "", "", "", "", "", "", "", "Item B", "1", "1.00"),
		mkRow("INV-1", "", "", "", "", "", "", "", "Item C", "1", "1.00"),
	}
	g := &invoiceGroup{number: "INV-1", rowIdxs: []int{0, 1, 2}}

	in := buildCreateInput("entity-1", rows, colIndex, g, "batch-1", "doc-1", 3, "Acme", nil)

	want := []int{4, 5, 6}
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

// TestServiceImport_PersistsSourceRowsBelowATitle is
// TestServiceImport_PersistsSourceRowsPerInvoice's DB twin with headerRow 3:
// each invoice's own sheet rows count from row 4, and the batch itself
// records the header row it was read from. RED both on the source_rows
// values (sheetRow stub) and on the header_row read (no such column yet).
func TestServiceImport_PersistsSourceRowsBelowATitle(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AIR-06-02 below-title tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-06-02 below-title entity")
	documentID := seedDocument(t, super, tenantID)

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget A", "1", "100.00"),  // file row 4
		mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Gadget B1", "1", "100.00"), // file row 5
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget B", "1", "100.00"),  // file row 6
		mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Gadget B2", "1", "100.00"), // file row 7
	}

	res, err := svc.Import(c, entityID, "", documentID, 3, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 2 {
		t.Fatalf("ReadyInvoices = %d, want 2", res.ReadyInvoices)
	}

	invA := invoiceIDByNumber(t, super, entityID, "INV-A")
	invB := invoiceIDByNumber(t, super, entityID, "INV-B")

	wantA, wantB := []int{4, 6}, []int{5, 7}
	if got := sourceRowsOf(t, super, invA); !intSliceEqual(got, wantA) {
		t.Errorf("INV-A source_rows = %v, want %v", got, wantA)
	}
	if got := sourceRowsOf(t, super, invB); !intSliceEqual(got, wantB) {
		t.Errorf("INV-B source_rows = %v, want %v", got, wantB)
	}

	var headerRow int
	if err := super.QueryRow(ctx, `SELECT header_row FROM import_batches WHERE id = $1`, res.ID).Scan(&headerRow); err != nil {
		t.Fatalf("read import_batches.header_row: %v", err)
	}
	if headerRow != 3 {
		t.Errorf("import_batches.header_row = %d, want 3", headerRow)
	}
}

// TestServiceImport_RowErrorsCountFromTheHeaderRow (dry run): a row error's
// Row/Rows count from headerRow, not always row 1. RED against the stub --
// both headerRow=3 and the headerRow=1 control compute the SAME numbers
// until sheetRow stops ignoring headerRow.
func TestServiceImport_RowErrorsCountFromTheHeaderRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AIR-06-02 row-errors tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-06-02 row-errors entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-X", "2026-01-10", "", "", "", "", "", "", "", "", ""),
		mkRow("", "2026-01-10", "", "", "", "", "", "", "", "", ""),
		mkRow("INV-X", "2026-01-11", "", "", "", "", "", "", "", "", ""),
	}

	res, err := svc.Import(c, entityID, "", "", 3, stdMapping, stdHeader, rows, true)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.RowsTotal != 3 || res.RowsInvalid != 3 {
		t.Fatalf("RowsTotal/RowsInvalid = %d/%d, want 3/3", res.RowsTotal, res.RowsInvalid)
	}
	want := []RowError{
		{Rows: []int{4, 6}, Field: "issue_date", Message: "rows disagree on issue_date"},
		{Row: 5, Message: "blank invoice number: row cannot be grouped"},
	}
	if len(res.Errors) != len(want) {
		t.Fatalf("Errors = %+v, want %+v", res.Errors, want)
	}
	for i, w := range want {
		if got := res.Errors[i]; got.Row != w.Row || !intSliceEqual(got.Rows, w.Rows) || got.Field != w.Field || got.Message != w.Message {
			t.Errorf("Errors[%d] = %+v, want %+v", i, got, w)
		}
	}

	// Control: the identical rows read from row 1 (no title above the header).
	controlRes, err := svc.Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, true)
	if err != nil {
		t.Fatalf("Import (control): %v", err)
	}
	wantControl := []RowError{
		{Rows: []int{2, 4}, Field: "issue_date", Message: "rows disagree on issue_date"},
		{Row: 3, Message: "blank invoice number: row cannot be grouped"},
	}
	if len(controlRes.Errors) != len(wantControl) {
		t.Fatalf("control Errors = %+v, want %+v", controlRes.Errors, wantControl)
	}
	for i, w := range wantControl {
		if got := controlRes.Errors[i]; got.Row != w.Row || !intSliceEqual(got.Rows, w.Rows) || got.Field != w.Field || got.Message != w.Message {
			t.Errorf("control Errors[%d] = %+v, want %+v", i, got, w)
		}
	}
}
