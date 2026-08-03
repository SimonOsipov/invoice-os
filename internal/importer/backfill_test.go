// backfill_test.go: RED specs for BackfillSourceRows, written before
// backfill.go exists. dbTestPools/seedTenant/seedEntity/newTestService/
// storeDocumentAs/sourceRowsOf/invoiceIDByNumber/stdHeader/stdMapping/mkRow
// are the same-package helpers already defined elsewhere in this package —
// reused, not redefined.
package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- fixture helpers ---------------------------------------------------

// seedInvoiceForDocument inserts one invoices row directly, pointing at
// documentID with source_rows left NULL and no line_items — for fixtures
// that need an invoice wired to a document without a real Import.
func seedInvoiceForDocument(t *testing.T, super *pgxpool.Pool, tenantID, entityID, documentID, number string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, entityID, number, documentID,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoice for document: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	return id
}

// nullSourceRows NULLs out source_rows for each id, simulating the
// pre-DOC-01 population the backfill exists to repair.
func nullSourceRows(t *testing.T, super *pgxpool.Pool, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := super.Exec(context.Background(),
			`UPDATE invoices SET source_rows = NULL WHERE id = $1`, id,
		); err != nil {
			t.Fatalf("null source_rows for %s: %v", id, err)
		}
	}
}

// addExtraLineItem inserts one more line_items row for invoiceID, so its
// stored line-item count no longer matches what the source file's rows
// would recompute.
func addExtraLineItem(t *testing.T, super *pgxpool.Pool, tenantID, invoiceID string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO line_items (tenant_id, invoice_id, line_no, description) VALUES ($1, $2, 99, 'extra')`,
		tenantID, invoiceID,
	); err != nil {
		t.Fatalf("insert extra line_items row: %v", err)
	}
}

// bfHeaderWithExtra is stdHeader plus one extra, unmapped trailing column —
// the importer's own mapping never sees it, but the backfill's column
// inference scans every column, mapped or not.
func bfHeaderWithExtra(name string) []string {
	return append(append([]string{}, stdHeader...), name)
}

// bfRowWithExtra is a single-line-item row in stdHeader's column order, plus
// one extra trailing cell.
func bfRowWithExtra(invoiceNo, extra string) []string {
	row := mkRow(invoiceNo, "2026-01-10", "TIN-1", "Buyer", "NGN", "100.00", "10.00", "110.00", "Item", "1", "100.00")
	return append(row, extra)
}

// openFunc is the seam BackfillSourceRows and SheetHandler both take.
type openFunc = func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error)

// bfFixture is one tenant/entity with a real document imported through the
// real importer, producing two single-line invoices whose source_rows the
// importer itself wrote — the oracle every AC-1 assertion below compares
// against.
type bfFixture struct {
	tenantID, entityID, documentID string
	invA, invB                     string
	rowsA, rowsB                   []int
	open                           openFunc
}

func newBFTwoInvoiceFixture(t *testing.T, super, app *pgxpool.Pool, label string) bfFixture {
	t.Helper()
	tenantID := seedTenant(t, super, label+" tenant")
	entityID := seedEntity(t, super, tenantID, label+" entity")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	rows := [][]string{
		mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "100.00", "10.00", "110.00", "Item A", "1", "100.00"),
		mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Item B", "1", "200.00"),
	}
	doc := storeDocumentAs(t, docSvc, tenantID, label+".csv", "text/csv", csvBody(t, stdHeader, rows))

	svc := newTestService(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(c, entityID, "", doc.ID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("import fixture: %v", err)
	}

	invA := invoiceIDByNumber(t, super, entityID, "INV-A")
	invB := invoiceIDByNumber(t, super, entityID, "INV-B")
	rowsA := sourceRowsOf(t, super, invA)
	rowsB := sourceRowsOf(t, super, invB)
	if len(rowsA) == 0 || len(rowsB) == 0 {
		t.Fatalf("fixture invoices already have empty source_rows before the test starts: A=%v B=%v", rowsA, rowsB)
	}

	return bfFixture{
		tenantID: tenantID, entityID: entityID, documentID: doc.ID,
		invA: invA, invB: invB, rowsA: rowsA, rowsB: rowsB,
		open: docSvc.Open,
	}
}

// --- AC-1 -----------------------------------------------------------------

// TestBackfill_RecoversRowsForACleanDocument: NULL both invoices' source_rows,
// run for real, and expect each recovered array to equal the importer's own
// captured original, element-for-element.
func TestBackfill_RecoversRowsForACleanDocument(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "recover")
	nullSourceRows(t, super, fx.invA, fx.invB)

	res, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesWritten != 2 {
		t.Errorf("InvoicesWritten = %d, want 2", res.InvoicesWritten)
	}
	if res.DocumentsAmbiguous != 0 {
		t.Errorf("DocumentsAmbiguous = %d, want 0", res.DocumentsAmbiguous)
	}

	gotA := sourceRowsOf(t, super, fx.invA)
	gotB := sourceRowsOf(t, super, fx.invB)
	if !intSliceEqual(gotA, fx.rowsA) {
		t.Errorf("invoice A source_rows = %v, want %v (the importer's own captured value)", gotA, fx.rowsA)
	}
	if !intSliceEqual(gotB, fx.rowsB) {
		t.Errorf("invoice B source_rows = %v, want %v (the importer's own captured value)", gotB, fx.rowsB)
	}
}

// --- AC-3 -------------------------------------------------------------------

// TestBackfill_DryRunWritesNothing: InvoicesRecoverable reports what a real
// run would write (the vacuity floor), but both rows stay SQL NULL.
func TestBackfill_DryRunWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "dryrun")
	nullSourceRows(t, super, fx.invA, fx.invB)

	res, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, true)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesRecoverable != 2 {
		t.Errorf("InvoicesRecoverable = %d, want 2", res.InvoicesRecoverable)
	}
	if got := sourceRowsOf(t, super, fx.invA); got != nil {
		t.Errorf("invoice A source_rows = %v, want SQL NULL after a dry run", got)
	}
	if got := sourceRowsOf(t, super, fx.invB); got != nil {
		t.Errorf("invoice B source_rows = %v, want SQL NULL after a dry run", got)
	}
}

// --- AC-2 -------------------------------------------------------------------

// TestBackfill_TiedColumnIsAmbiguous: a second column duplicates the
// invoice-number column for every row, so two columns each reach full
// coverage — nothing is written for either invoice.
func TestBackfill_TiedColumnIsAmbiguous(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "tied-column tenant")
	entityID := seedEntity(t, super, tenantID, "tied-column entity")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	header := bfHeaderWithExtra("Copy No")
	rows := [][]string{
		bfRowWithExtra("INV-1", "INV-1"),
		bfRowWithExtra("INV-2", "INV-2"),
	}
	doc := storeDocumentAs(t, docSvc, tenantID, "tied.csv", "text/csv", csvBody(t, header, rows))

	svc := newTestService(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(c, entityID, "", doc.ID, stdMapping, header, rows, false); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	inv1 := invoiceIDByNumber(t, super, entityID, "INV-1")
	inv2 := invoiceIDByNumber(t, super, entityID, "INV-2")
	nullSourceRows(t, super, inv1, inv2)

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.DocumentsAmbiguous != 1 {
		t.Errorf("DocumentsAmbiguous = %d, want 1", res.DocumentsAmbiguous)
	}
	if len(res.Notes) == 0 {
		t.Error("Notes is empty, want at least one line naming the ambiguous document")
	}
	if got := sourceRowsOf(t, super, inv1); got != nil {
		t.Errorf("invoice 1 source_rows = %v, want SQL NULL — a tied column must write nothing", got)
	}
	if got := sourceRowsOf(t, super, inv2); got != nil {
		t.Errorf("invoice 2 source_rows = %v, want SQL NULL — a tied column must write nothing", got)
	}
}

// TestBackfill_PartialCoverageIsAmbiguous: a third invoice on the same
// document whose number never appears in any column — the whole document
// (including its two clean siblings) writes nothing.
func TestBackfill_PartialCoverageIsAmbiguous(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "partial")
	nullSourceRows(t, super, fx.invA, fx.invB)
	ghost := seedInvoiceForDocument(t, super, fx.tenantID, fx.entityID, fx.documentID, "GHOST-999")

	res, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.DocumentsAmbiguous != 1 {
		t.Errorf("DocumentsAmbiguous = %d, want 1", res.DocumentsAmbiguous)
	}
	if got := sourceRowsOf(t, super, fx.invA); got != nil {
		t.Errorf("invoice A source_rows = %v, want SQL NULL — partial coverage refuses the whole document", got)
	}
	if got := sourceRowsOf(t, super, fx.invB); got != nil {
		t.Errorf("invoice B source_rows = %v, want SQL NULL — partial coverage refuses the whole document", got)
	}
	if got := sourceRowsOf(t, super, ghost); got != nil {
		t.Errorf("ghost invoice source_rows = %v, want SQL NULL", got)
	}
}

// TestBackfill_LineItemCountMismatchWritesNothing: invoice A's line_items
// were edited after import (one extra row added) — only A refuses; its
// unedited sibling B still recovers.
func TestBackfill_LineItemCountMismatchWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "mismatch")
	nullSourceRows(t, super, fx.invA, fx.invB)
	addExtraLineItem(t, super, fx.tenantID, fx.invA)

	res, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesAmbiguous != 1 {
		t.Errorf("InvoicesAmbiguous = %d, want 1", res.InvoicesAmbiguous)
	}
	if got := sourceRowsOf(t, super, fx.invA); got != nil {
		t.Errorf("invoice A source_rows = %v, want SQL NULL — its row/line-item count no longer agree", got)
	}
	if got := sourceRowsOf(t, super, fx.invB); !intSliceEqual(got, fx.rowsB) {
		t.Errorf("invoice B source_rows = %v, want %v — its unedited sibling must still recover", got, fx.rowsB)
	}
}

// TestBackfill_DuplicateInvoiceNumberInOneDocumentIsAmbiguous: the same
// document imported into two entities under one tenant, each producing an
// invoice with the same number — the unique index forbids this within one
// entity, so two entities are the only way to construct it.
func TestBackfill_DuplicateInvoiceNumberInOneDocumentIsAmbiguous(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "dup-number tenant")
	entityA := seedEntity(t, super, tenantID, "dup-number entity A")
	entityB := seedEntity(t, super, tenantID, "dup-number entity B")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	rows := [][]string{
		mkRow("DUP-1", "2026-01-10", "TIN-1", "Buyer", "NGN", "100.00", "10.00", "110.00", "Item", "1", "100.00"),
	}
	doc := storeDocumentAs(t, docSvc, tenantID, "dup.csv", "text/csv", csvBody(t, stdHeader, rows))

	svc := newTestService(app)
	cA := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	cB := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(cA, entityA, "", doc.ID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("import into entity A: %v", err)
	}
	if _, err := svc.Import(cB, entityB, "", doc.ID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("import into entity B: %v", err)
	}
	invA := invoiceIDByNumber(t, super, entityA, "DUP-1")
	invB := invoiceIDByNumber(t, super, entityB, "DUP-1")
	nullSourceRows(t, super, invA, invB)

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.DocumentsAmbiguous != 1 {
		t.Errorf("DocumentsAmbiguous = %d, want 1", res.DocumentsAmbiguous)
	}
	if got := sourceRowsOf(t, super, invA); got != nil {
		t.Errorf("entity A invoice source_rows = %v, want SQL NULL", got)
	}
	if got := sourceRowsOf(t, super, invB); got != nil {
		t.Errorf("entity B invoice source_rows = %v, want SQL NULL", got)
	}
}

// TestBackfill_MatchesRawUntrimmedCellValue: the invoice-number cell carries
// literal leading/trailing whitespace, which csv.Reader (TrimLeadingSpace
// false) preserves and the importer stores verbatim — a trimmed comparison
// would mis-key against the stored raw value.
func TestBackfill_MatchesRawUntrimmedCellValue(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "raw-cell tenant")
	entityID := seedEntity(t, super, tenantID, "raw-cell entity")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	const padded = " INV-1 "
	rows := [][]string{
		mkRow(padded, "2026-01-10", "TIN-1", "Buyer", "NGN", "100.00", "10.00", "110.00", "Item", "1", "100.00"),
	}
	doc := storeDocumentAs(t, docSvc, tenantID, "raw.csv", "text/csv", csvBody(t, stdHeader, rows))

	svc := newTestService(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(c, entityID, "", doc.ID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	inv := invoiceIDByNumber(t, super, entityID, padded)
	want := sourceRowsOf(t, super, inv)
	if len(want) == 0 {
		t.Fatal("fixture invoice already has empty source_rows before the test starts")
	}
	nullSourceRows(t, super, inv)

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesWritten != 1 {
		t.Errorf("InvoicesWritten = %d, want 1", res.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, inv); !intSliceEqual(got, want) {
		t.Errorf("source_rows = %v, want %v — the padded cell must match the padded stored invoice_number exactly", got, want)
	}
}

// --- AC-4 -------------------------------------------------------------------

// TestBackfill_IsIdempotent: a second dryRun=false run over the same tenant
// finds nothing left to recover and leaves both arrays byte-identical to
// after the first run.
func TestBackfill_IsIdempotent(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "idempotent")
	nullSourceRows(t, super, fx.invA, fx.invB)

	if _, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, false); err != nil {
		t.Fatalf("first BackfillSourceRows run: %v", err)
	}
	afterFirstA := sourceRowsOf(t, super, fx.invA)
	afterFirstB := sourceRowsOf(t, super, fx.invB)
	if !intSliceEqual(afterFirstA, fx.rowsA) || !intSliceEqual(afterFirstB, fx.rowsB) {
		t.Fatalf("first run did not recover the expected rows: A=%v B=%v", afterFirstA, afterFirstB)
	}

	res2, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, false)
	if err != nil {
		t.Fatalf("second BackfillSourceRows run: %v", err)
	}
	if res2.InvoicesRecoverable != 0 {
		t.Errorf("second run InvoicesRecoverable = %d, want 0", res2.InvoicesRecoverable)
	}
	if res2.InvoicesWritten != 0 {
		t.Errorf("second run InvoicesWritten = %d, want 0", res2.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, fx.invA); !intSliceEqual(got, afterFirstA) {
		t.Errorf("invoice A source_rows changed on the second run: %v, want %v", got, afterFirstA)
	}
	if got := sourceRowsOf(t, super, fx.invB); !intSliceEqual(got, afterFirstB) {
		t.Errorf("invoice B source_rows changed on the second run: %v, want %v", got, afterFirstB)
	}
}

// TestBackfill_SkipsInvoicesThatAlreadyHaveRows: only A is NULLed; B keeps
// its importer-written rows — A recovers, B is untouched.
func TestBackfill_SkipsInvoicesThatAlreadyHaveRows(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "skip-populated")
	nullSourceRows(t, super, fx.invA)

	res, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesWritten != 1 {
		t.Errorf("InvoicesWritten = %d, want 1", res.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, fx.invA); !intSliceEqual(got, fx.rowsA) {
		t.Errorf("invoice A source_rows = %v, want %v (recovered)", got, fx.rowsA)
	}
	if got := sourceRowsOf(t, super, fx.invB); !intSliceEqual(got, fx.rowsB) {
		t.Errorf("invoice B source_rows = %v, want %v (untouched, already populated)", got, fx.rowsB)
	}
}

// --- AC-5 -------------------------------------------------------------------

// TestRLS_BackfillDoesNotCrossTenants: a run scoped to tenant A never
// touches tenant B's invoice. The vacuity floor: B is then run for real and
// must recover too, proving it was genuinely recoverable all along.
func TestRLS_BackfillDoesNotCrossTenants(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "cross-tenant A")
	entityA := seedEntity(t, super, tenantA, "cross-tenant A entity")
	tenantB := seedTenant(t, super, "cross-tenant B")
	entityB := seedEntity(t, super, tenantB, "cross-tenant B entity")

	// One shared object store/service: real StorageKey(tenantID, hash)
	// namespacing means both tenants' bytes coexist without collision, and
	// backfill's own per-call identity (built from ITS tenantID argument)
	// is what must keep the two runs apart, not separate services.
	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	svc := newTestService(app)

	rowsA := [][]string{mkRow("XT-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "100.00", "10.00", "110.00", "Item", "1", "100.00")}
	rowsB := [][]string{mkRow("XT-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Item", "1", "200.00")}

	docA := storeDocumentAs(t, docSvc, tenantA, "a.csv", "text/csv", csvBody(t, stdHeader, rowsA))
	docB := storeDocumentAs(t, docSvc, tenantB, "b.csv", "text/csv", csvBody(t, stdHeader, rowsB))

	cA := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	cB := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	if _, err := svc.Import(cA, entityA, "", docA.ID, stdMapping, stdHeader, rowsA, false); err != nil {
		t.Fatalf("import tenant A: %v", err)
	}
	if _, err := svc.Import(cB, entityB, "", docB.ID, stdMapping, stdHeader, rowsB, false); err != nil {
		t.Fatalf("import tenant B: %v", err)
	}

	invA := invoiceIDByNumber(t, super, entityA, "XT-A")
	invB := invoiceIDByNumber(t, super, entityB, "XT-B")
	wantA := sourceRowsOf(t, super, invA)
	wantB := sourceRowsOf(t, super, invB)
	nullSourceRows(t, super, invA, invB)

	if _, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantA, false); err != nil {
		t.Fatalf("BackfillSourceRows for tenant A: %v", err)
	}
	if got := sourceRowsOf(t, super, invA); !intSliceEqual(got, wantA) {
		t.Errorf("tenant A invoice source_rows = %v, want %v", got, wantA)
	}
	if got := sourceRowsOf(t, super, invB); got != nil {
		t.Errorf("tenant B invoice source_rows = %v, want SQL NULL — a tenant-A-scoped run must not touch it", got)
	}

	// Vacuity floor: B is genuinely recoverable, not merely untouched.
	if _, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantB, false); err != nil {
		t.Fatalf("BackfillSourceRows for tenant B: %v", err)
	}
	if got := sourceRowsOf(t, super, invB); !intSliceEqual(got, wantB) {
		t.Errorf("tenant B invoice source_rows = %v, want %v after its own run", got, wantB)
	}
}

// TestRLS_BackfillRefusesABypassRLSRole: handed the superuser pool, the run
// must refuse before opening a single document.
func TestRLS_BackfillRefusesABypassRLSRole(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "bypassrls tenant")
	entityID := seedEntity(t, super, tenantID, "bypassrls entity")
	docID := seedDocument(t, super, tenantID)
	inv := seedInvoiceForDocument(t, super, tenantID, entityID, docID, "BYPASS-1")

	var opened bool
	open := func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error) {
		opened = true
		return document.Document{}, document.Object{}, errors.New("open must never be reached")
	}

	res, err := BackfillSourceRows(context.Background(), super, open, tenantID, false)
	if err == nil {
		t.Fatal("BackfillSourceRows over the superuser pool returned nil error, want a refusal")
	}
	if opened {
		t.Error("open was called despite the BYPASSRLS/superuser refusal")
	}
	if res.DocumentsScanned != 0 || res.InvoicesWritten != 0 {
		t.Errorf("result = %+v, want the zero value on refusal", res)
	}
	if got := sourceRowsOf(t, super, inv); got != nil {
		t.Errorf("invoice source_rows = %v, want SQL NULL — nothing may be written on refusal", got)
	}
}

// --- AC-6 -------------------------------------------------------------------

// TestBackfill_UndecodableOrMissingDocumentIsSkippedNotFatal: a document
// containing a disallowed control byte, one whose object key was never Put,
// and one clean document in the same run — the two bad ones are reported
// and skipped, the clean one still recovers, and the run itself succeeds.
func TestBackfill_UndecodableOrMissingDocumentIsSkippedNotFatal(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "skip-bad-docs tenant")
	entityID := seedEntity(t, super, tenantID, "skip-bad-docs entity")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	// A document whose stored bytes contain a NUL byte: firstDisallowedControlByte
	// rejects it deterministically, unlike CSV's otherwise-permissive decode.
	badDoc := storeDocumentAs(t, docSvc, tenantID, "bad.csv", "text/csv", []byte("Invoice No,Item\n\x00BAD,Widget\n"))
	badInv := seedInvoiceForDocument(t, super, tenantID, entityID, badDoc.ID, "BAD-1")

	// A documents row whose object key was never Put.
	missingDocID := seedDocument(t, super, tenantID)
	missingInv := seedInvoiceForDocument(t, super, tenantID, entityID, missingDocID, "MISSING-1")

	// A genuinely clean document alongside the two bad ones.
	cleanRows := [][]string{
		mkRow("CLEAN-1", "2026-01-10", "TIN-1", "Buyer", "NGN", "100.00", "10.00", "110.00", "Item A", "1", "100.00"),
		mkRow("CLEAN-1", "2026-01-10", "TIN-1", "Buyer", "NGN", "100.00", "10.00", "110.00", "Item B", "1", "100.00"),
	}
	cleanDoc := storeDocumentAs(t, docSvc, tenantID, "clean.csv", "text/csv", csvBody(t, stdHeader, cleanRows))
	svc := newTestService(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(c, entityID, "", cleanDoc.ID, stdMapping, stdHeader, cleanRows, false); err != nil {
		t.Fatalf("import clean fixture: %v", err)
	}
	cleanInv := invoiceIDByNumber(t, super, entityID, "CLEAN-1")
	wantClean := sourceRowsOf(t, super, cleanInv)
	if len(wantClean) == 0 {
		t.Fatal("clean fixture invoice already has empty source_rows before the test starts")
	}
	nullSourceRows(t, super, cleanInv)

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.DocumentsScanned != 3 {
		t.Errorf("DocumentsScanned = %d, want 3", res.DocumentsScanned)
	}
	if res.DocumentsSkipped != 2 {
		t.Errorf("DocumentsSkipped = %d, want 2", res.DocumentsSkipped)
	}
	if len(res.Notes) < 2 {
		t.Errorf("Notes has %d lines, want at least one per skipped document", len(res.Notes))
	}
	if got := sourceRowsOf(t, super, badInv); got != nil {
		t.Errorf("bad-document invoice source_rows = %v, want SQL NULL", got)
	}
	if got := sourceRowsOf(t, super, missingInv); got != nil {
		t.Errorf("missing-object invoice source_rows = %v, want SQL NULL", got)
	}
	if got := sourceRowsOf(t, super, cleanInv); !intSliceEqual(got, wantClean) {
		t.Errorf("clean invoice source_rows = %v, want %v — a bad sibling document must not block it", got, wantClean)
	}
}
