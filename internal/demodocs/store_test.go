// DB-backed half of the demodocs suite, gated on the per-role DSNs like every
// other db-integration suite in this repo (internal/importer/store_test.go's
// dbTestPools idiom, copied per-package by convention).
//
// These drive seedTenant directly rather than Seed: Seed's tenant list is the
// hardcoded allowlist, so a throwaway tenant is unreachable through it — which
// is the point, and is itself asserted in TestRLS_DemoDocsSeedIgnoresNonDemoTenants.
package demodocs

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("demodocs db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// newTenant inserts a throwaway tenant plus the admin membership seedTenant
// needs to resolve an actor, and unwinds the source_document_id pointers before
// the tenant cascade — invoices -> documents is ON DELETE RESTRICT, so a bare
// tenant delete can deadlock against its own cascade.
func newTenant(t *testing.T, super *pgxpool.Pool, label string) (tenantID, adminID string) {
	t.Helper()
	ctx := context.Background()
	tenantID = uuid.NewString()
	adminID = uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, tenantID, label); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	t.Cleanup(func() {
		c := context.Background()
		_, _ = super.Exec(c, `UPDATE invoices SET source_document_id = NULL, source_rows = NULL WHERE tenant_id = $1`, tenantID)
		_, _ = super.Exec(c, `DELETE FROM documents WHERE tenant_id = $1`, tenantID)
	})

	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin')`,
		tenantID, adminID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	return tenantID, adminID
}

func newEntity(t *testing.T, super *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, name).Scan(&id); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	return id
}

// newInvoice inserts one invoice and lineCount line items. lineCount 0 leaves
// it with none, which is the "never came from a file" case.
func newInvoice(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string, lineCount int) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, issue_date, buyer_tin, buyer_name,
		                       currency, subtotal, vat, total)
		 VALUES ($1, $2, $3, '2026-06-02', '20011122-0001', 'Zenith Freight', 'NGN', 1000.00, 75.00, 1075.00)
		 RETURNING id`, tenantID, entityID, number).Scan(&id); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	for i := 1; i <= lineCount; i++ {
		if _, err := super.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total)
			 VALUES ($1, $2, $3, $4, 1, 1000.00, 1000.00)`,
			tenantID, id, i, "Item "+string(rune('A'+i-1))); err != nil {
			t.Fatalf("seed line_items: %v", err)
		}
	}
	return id
}

// stubStore stands in for document.Service.Store: it records the bytes and
// writes the documents row the composite FK needs, without an object store. The
// tenant comes from the identity in ctx exactly as the real Store resolves it,
// so a caller that seeds several tenants still writes each document under the
// right one.
func stubStore(t *testing.T, super *pgxpool.Pool, bodies *[][]byte) StoreFunc {
	t.Helper()
	return func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (document.Document, error) {
		id, ok := auth.IdentityFromContext(ctx)
		if !ok {
			return document.Document{}, db.ErrNoTenant
		}
		b, err := io.ReadAll(body)
		if err != nil {
			return document.Document{}, err
		}
		*bodies = append(*bodies, b)
		sum := sha256.Sum256(b)
		hash := hex.EncodeToString(sum[:])

		var docID string
		err = super.QueryRow(ctx,
			`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, filename)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (tenant_id, content_hash) DO UPDATE SET filename = documents.filename
			 RETURNING id::text`,
			id.TenantID, "test/"+hash, hash, len(b), filename).Scan(&docID)
		return document.Document{ID: docID, ContentHash: hash, SizeBytes: int64(len(b))}, err
	}
}

func sourceOf(t *testing.T, super *pgxpool.Pool, invoiceID string) (docID *string, rows []int) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT source_document_id::text, source_rows FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&docID, &rows); err != nil {
		t.Fatalf("read invoice %s: %v", invoiceID, err)
	}
	return docID, rows
}

// One file per supplier entity, and each invoice's source_rows are the rows it
// actually occupies in that file.
func TestRLS_DemoDocsSeedsOneDocumentPerEntity(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, _ := newTenant(t, super, "demodocs one-per-entity")
	entityA := newEntity(t, super, tenantID, "Adeyemi & Sons Trading Ltd")
	entityB := newEntity(t, super, tenantID, "Chukwu Global Ventures Ltd")

	inv1 := newInvoice(t, super, tenantID, entityA, "DEMO-T-1001", 2)
	inv2 := newInvoice(t, super, tenantID, entityA, "DEMO-T-1002", 1)
	inv3 := newInvoice(t, super, tenantID, entityB, "DEMO-T-2001", 1)

	var bodies [][]byte
	res, err := seedTenant(context.Background(), app, stubStore(t, super, &bodies), tenantID)
	if err != nil {
		t.Fatalf("seedTenant: %v", err)
	}
	if res.DocumentsStored != 2 {
		t.Errorf("DocumentsStored = %d, want 2 (one per entity)", res.DocumentsStored)
	}
	if res.InvoicesLinked != 3 {
		t.Errorf("InvoicesLinked = %d, want 3", res.InvoicesLinked)
	}

	docA, rowsA := sourceOf(t, super, inv1)
	docB, rowsB := sourceOf(t, super, inv2)
	docC, rowsC := sourceOf(t, super, inv3)
	if docA == nil || docB == nil || docC == nil {
		t.Fatal("an invoice was left without a source document")
	}
	if *docA != *docB {
		t.Error("two invoices of the same entity landed on different documents")
	}
	if *docA == *docC {
		t.Error("invoices of different entities share one document; the file should be per supplier")
	}

	// inv1 has 2 line items so it owns sheet rows 2-3, inv2 the next row.
	if len(rowsA) != 2 || rowsA[0] != 2 || rowsA[1] != 3 {
		t.Errorf("inv1 source_rows = %v, want [2 3]", rowsA)
	}
	if len(rowsB) != 1 || rowsB[0] != 4 {
		t.Errorf("inv2 source_rows = %v, want [4]", rowsB)
	}
	// A separate file restarts the numbering.
	if len(rowsC) != 1 || rowsC[0] != 2 {
		t.Errorf("inv3 source_rows = %v, want [2] (its own file's first data row)", rowsC)
	}

	// The stored bytes must actually contain the rows the numbers point at —
	// asserting the numbers alone would pass for a file that was never written.
	if len(bodies) != 2 {
		t.Fatalf("stored %d bodies, want 2", len(bodies))
	}
	records, err := csv.NewReader(strings.NewReader(string(bodies[0]))).ReadAll()
	if err != nil {
		t.Fatalf("stored file is not valid CSV: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("entity A's file has %d records, want 4 (header + 3 line items)", len(records))
	}
	if records[rowsB[0]-1][0] != "DEMO-T-1002" {
		t.Errorf("sheet row %d of the stored file is %q, want DEMO-T-1002", rowsB[0], records[rowsB[0]-1][0])
	}
}

// A row in an import file IS a line item, so an invoice with none was never in
// a file and must keep reading "no source document".
func TestRLS_DemoDocsLeavesInvoicesWithoutLineItemsAlone(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, _ := newTenant(t, super, "demodocs no-line-items")
	entity := newEntity(t, super, tenantID, "Aliyu Logistics Services Ltd")

	withItems := newInvoice(t, super, tenantID, entity, "DEMO-T-3001", 1)
	without := newInvoice(t, super, tenantID, entity, "DEMO-T-3002", 0)

	var bodies [][]byte
	res, err := seedTenant(context.Background(), app, stubStore(t, super, &bodies), tenantID)
	if err != nil {
		t.Fatalf("seedTenant: %v", err)
	}
	if res.InvoicesLinked != 1 {
		t.Errorf("InvoicesLinked = %d, want 1", res.InvoicesLinked)
	}
	if doc, _ := sourceOf(t, super, withItems); doc == nil {
		t.Error("the invoice with line items was not linked")
	}
	if doc, rows := sourceOf(t, super, without); doc != nil || rows != nil {
		t.Errorf("the invoice with no line items got source_document_id=%v source_rows=%v, want both NULL", doc, rows)
	}
}

// Re-running must write nothing: the boot path calls this on every deploy.
func TestRLS_DemoDocsSecondRunIsANoOp(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, _ := newTenant(t, super, "demodocs idempotent")
	entity := newEntity(t, super, tenantID, "Emeka Pharmaceuticals Ltd")
	inv := newInvoice(t, super, tenantID, entity, "DEMO-T-5001", 2)

	var bodies [][]byte
	store := stubStore(t, super, &bodies)
	ctx := context.Background()

	first, err := seedTenant(ctx, app, store, tenantID)
	if err != nil {
		t.Fatalf("first seedTenant: %v", err)
	}
	_, rowsBefore := sourceOf(t, super, inv)

	second, err := seedTenant(ctx, app, store, tenantID)
	if err != nil {
		t.Fatalf("second seedTenant: %v", err)
	}
	if first.InvoicesLinked != 1 {
		t.Errorf("first run linked %d, want 1", first.InvoicesLinked)
	}
	if second.DocumentsStored != 0 || second.InvoicesLinked != 0 {
		t.Errorf("second run stored %d documents and linked %d invoices, want 0 and 0",
			second.DocumentsStored, second.InvoicesLinked)
	}
	if _, rowsAfter := sourceOf(t, super, inv); len(rowsAfter) != len(rowsBefore) {
		t.Errorf("source_rows changed across runs: %v -> %v", rowsBefore, rowsAfter)
	}
}

// linkInvoices repeats the IS NULL predicate that pendingRows already applied,
// so two seeders racing at boot (two invoice replicas) leave the first one's
// pointer intact instead of last-writer-wins. Driven directly: the second
// caller has to arrive with rows pendingRows would no longer return.
func TestRLS_DemoDocsLinkWillNotReclaimALinkedInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, adminID := newTenant(t, super, "demodocs relink guard")
	entity := newEntity(t, super, tenantID, "Okonkwo Textiles Nigeria Ltd")
	inv := newInvoice(t, super, tenantID, entity, "DEMO-T-6001", 1)

	ctx := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
	var bodies [][]byte
	store := stubStore(t, super, &bodies)

	first, err := store(ctx, "first.csv", "text/csv", 3, strings.NewReader("a,b\n1,2\n"))
	if err != nil {
		t.Fatalf("store first: %v", err)
	}
	second, err := store(ctx, "second.csv", "text/csv", 3, strings.NewReader("c,d\n3,4\n"))
	if err != nil {
		t.Fatalf("store second: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("the two stub documents collided; the guard would be untestable")
	}

	if n, err := linkInvoices(ctx, app, first.ID, map[string][]int{inv: {2}}); err != nil || n != 1 {
		t.Fatalf("first link = %d, %v; want 1, nil", n, err)
	}
	n, err := linkInvoices(ctx, app, second.ID, map[string][]int{inv: {7}})
	if err != nil {
		t.Fatalf("second link: %v", err)
	}
	if n != 0 {
		t.Errorf("second link claimed %d invoices, want 0", n)
	}

	doc, rows := sourceOf(t, super, inv)
	if doc == nil || *doc != first.ID {
		t.Errorf("source_document_id = %v, want the first document %s", doc, first.ID)
	}
	if len(rows) != 1 || rows[0] != 2 {
		t.Errorf("source_rows = %v, want [2] (the first link's rows, not the second's)", rows)
	}
}

// No admin membership means no honest actor for the document.created audit row
// the previewer reads as "Uploaded by", so the tenant is skipped rather than
// attributed to a fabricated subject.
func TestRLS_DemoDocsSkipsTenantWithoutAnAdmin(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, adminID := newTenant(t, super, "demodocs no admin")
	if _, err := super.Exec(context.Background(),
		`DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenantID, adminID); err != nil {
		t.Fatalf("drop membership: %v", err)
	}
	entity := newEntity(t, super, tenantID, "Balogun Agro-Allied Ltd")
	inv := newInvoice(t, super, tenantID, entity, "DEMO-T-4001", 1)

	var bodies [][]byte
	res, err := seedTenant(context.Background(), app, stubStore(t, super, &bodies), tenantID)
	if err != nil {
		t.Fatalf("seedTenant: %v", err)
	}
	if res.DocumentsStored != 0 || res.InvoicesLinked != 0 {
		t.Errorf("stored %d / linked %d, want 0 / 0", res.DocumentsStored, res.InvoicesLinked)
	}
	if doc, _ := sourceOf(t, super, inv); doc != nil {
		t.Error("an invoice was linked for a tenant with no admin")
	}
}

// The allowlist is the safety boundary. Seed against a live throwaway tenant
// holding exactly the shape demodocs looks for must still touch nothing.
func TestRLS_DemoDocsSeedIgnoresNonDemoTenants(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, _ := newTenant(t, super, "demodocs non-demo tenant")
	entity := newEntity(t, super, tenantID, "Not A Demo Supplier Ltd")
	inv := newInvoice(t, super, tenantID, entity, "REAL-2026-0001", 2)

	for _, id := range DemoTenants {
		if id == tenantID {
			t.Fatalf("throwaway tenant %s collided with the allowlist", tenantID)
		}
	}

	var bodies [][]byte
	if _, err := Seed(context.Background(), app, stubStore(t, super, &bodies), nil); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if doc, rows := sourceOf(t, super, inv); doc != nil || rows != nil {
		t.Errorf("Seed touched a non-allowlisted tenant: source_document_id=%v source_rows=%v", doc, rows)
	}
	// bodies is not asserted empty: on a seeded database Seed legitimately
	// writes the real demo tenants' files through this same stub. What must be
	// zero is the count under THIS tenant.
	var docs int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM documents WHERE tenant_id = $1`, tenantID).Scan(&docs); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if docs != 0 {
		t.Errorf("Seed stored %d documents for a non-allowlisted tenant", docs)
	}
}
