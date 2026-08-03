// RLS, grant and constraint suite for the `documents` table plus the two composite-FK
// pointer columns it adds (invoices.source_document_id, import_batches.document_id).
// Written before the migration exists, so every case fails with an explicit 42P01 /
// 42703 message until it lands.
//
// Run: `DEV_DB_PORT=5433 make test-rls`. requireHarness skips without the four per-role
// DSNs, and a skip is itself a failure under scripts/ci/rls-test-gate.sh.
package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// docMaxSize is the 15 MB upload cap the size_bytes CHECK encodes (15 << 20).
const docMaxSize = 15728640

// failIfUndefinedDocuments turns the pre-migration failure mode into an explicit message
// instead of a raw driver error or a misleading "want 23514, got 42P01", following
// failIfUndefinedSubmissionJobs (submission_jobs_rls_test.go:142). Returns true when it fired.
func failIfUndefinedDocuments(t *testing.T, what string, err error) bool {
	t.Helper()
	switch pgCode(err) {
	case "42P01":
		t.Fatalf("%s: undefined_table (42P01) — the documents migration is not applied yet: %v", what, err)
		return true
	case "42703":
		t.Fatalf("%s: undefined_column (42703) — the documents migration's pointer columns "+
			"(invoices.source_document_id / import_batches.document_id) are not applied yet: %v", what, err)
		return true
	}
	return false
}

// pgConstraint extracts the CONSTRAINT NAME from err. The RESTRICT cases must prove WHICH
// foreign key refused the delete, not merely that one did.
func pgConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// docHash returns a fresh 64-char hex string — exactly the length the content_hash CHECK
// requires. Fresh per call so the per-tenant unique index never collides by accident.
func docHash() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "") + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// seedDocument inserts one documents row for tenantID as the superuser (BYPASSRLS, so
// seeding needs neither tenant context nor an INSERT grant) and returns its id plus a
// cleanup func. Seed a document BEFORE the invoice/batch that will point at it: cleanup
// runs LIFO and ON DELETE RESTRICT blocks removing the document first.
func seedDocument(t *testing.T, tenantID, storageKey string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, storageKey, docHash(), 1024,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed documents: undefined_table (42P01) — documents migration not applied yet: %v", err)
		}
		t.Fatalf("seed documents: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM documents WHERE id = $1`, id)
	}
}

// DOC-01: the table is born ENABLE + FORCE RLS carrying exactly two policies — the
// role-agnostic tenant_isolation whose USING doubles as the INSERT WITH CHECK, and
// tenant_enumerate scoped by TO to the one cross-tenant reader. The catalog half of AC-1
// and AC-2; the behavioural half is DOC-02..09.
func TestRLS_DocumentsForceRLSAndPoliciesDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var enabled, forced bool
	err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.documents')`,
	).Scan(&enabled, &forced)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("no pg_class row for public.documents — the documents migration is not applied yet")
	}
	if err != nil {
		t.Fatalf("read pg_class for documents: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("documents relrowsecurity/relforcerowsecurity = %v/%v, want true/true "+
			"(ENABLE alone would let the migrator/owner bypass the policy)", enabled, forced)
	}

	type policy struct {
		roles []string
		cmd   string
		qual  string
	}
	got := map[string]policy{}
	rows, err := h.super.Query(ctx,
		`SELECT policyname, roles::text[], cmd, coalesce(qual, '')
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'documents'`)
	if err != nil {
		t.Fatalf("query pg_policies for documents: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var p policy
		if e := rows.Scan(&name, &p.roles, &p.cmd, &p.qual); e != nil {
			t.Fatalf("scan pg_policies row: %v", e)
		}
		got[name] = p
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_policies rows: %v", e)
	}

	if len(got) != 2 {
		t.Errorf("policies on documents = %d (%v), want 2 (tenant_isolation, tenant_enumerate)", len(got), got)
	}

	iso, ok := got["tenant_isolation"]
	if !ok {
		t.Fatal("no tenant_isolation policy on documents — the documents migration is not applied yet")
	}
	if strings.Join(iso.roles, ",") != "public" {
		t.Errorf("tenant_isolation roles = %v, want [public] (no TO clause — it binds every role)", iso.roles)
	}
	if iso.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want %q (its USING must double as the INSERT WITH CHECK)", iso.cmd, "ALL")
	}
	if !strings.Contains(iso.qual, "app.current_tenant") {
		t.Errorf("tenant_isolation qual = %q, want a comparison against the app.current_tenant GUC", iso.qual)
	}

	enum, ok := got["tenant_enumerate"]
	if !ok {
		t.Fatal("no tenant_enumerate policy on documents — documents is the second table (after tenants) to carry one")
	}
	if strings.Join(enum.roles, ",") != "invoice_tenant_reader" {
		t.Errorf("tenant_enumerate roles = %v, want [invoice_tenant_reader] — an unscoped TO would OR "+
			"USING(true) into invoice_app's own view", enum.roles)
	}
	if enum.cmd != "SELECT" {
		t.Errorf("tenant_enumerate cmd = %q, want %q (read-only enumeration)", enum.cmd, "SELECT")
	}
	if enum.qual != "true" {
		t.Errorf("tenant_enumerate qual = %q, want %q", enum.qual, "true")
	}
}

// DOC-02: cross-tenant SELECT is refused. The id-scoped count is the load-bearing one —
// a tenant_id-filtered query would return the right number even if RLS did nothing.
func TestRLS_DocumentsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupA := seedDocument(t, h.tenantA, "DOC-02/a.csv")
	defer cleanupA()
	docB, cleanupB := seedDocument(t, h.tenantB, "DOC-02/b.csv")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM documents WHERE id = $1`, docA); n != 1 {
			t.Errorf("A's own document visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM documents WHERE id = $1`, docB); n != 0 {
			t.Errorf("B's document visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// DOC-03: a cross-tenant INSERT is refused by the policy's WITH CHECK (42501, "row-level
// security"), nothing lands, and the SAME statement shape succeeds for A's own tenant —
// which is what stops this passing against a table invoice_app cannot write at all.
func TestRLS_DocumentsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	crossHash := docHash()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM documents WHERE content_hash = $1`, crossHash)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4)`,
			h.tenantB, "DOC-03/cross.csv", crossHash, 10,
		)
		return e
	})
	if failIfUndefinedDocuments(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("cross-tenant INSERT refusal message = %q, want an RLS WITH CHECK violation — a missing "+
			"INSERT grant answers with the same 42501 and would prove nothing about isolation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE content_hash = $1`, crossHash); n != 0 {
		t.Errorf("rows with the cross-tenant hash after the refused INSERT = %d, want 0", n)
	}

	ownHash := docHash()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM documents WHERE content_hash = $1`, ownHash)
	}()
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4)`,
			h.tenantA, "DOC-03/own.csv", ownHash, 10,
		)
		return e
	}); err != nil {
		t.Fatalf("own-tenant INSERT of the same shape: want success, got: %v", err)
	}
}

// DOC-04: A's tx cannot rewrite B's document. invoice_app holds no UPDATE grant at all,
// so the refusal lands at the GRANT layer (42501, "permission denied") before RLS is
// consulted — the row is unreachable twice over. Mutation-verified.
func TestRLS_DocumentsCrossTenantUpdateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docB, cleanupB := seedDocument(t, h.tenantB, "DOC-04/b.csv")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE documents SET storage_key = 'pwned-by-a' WHERE id = $1`, docB)
		return e
	})
	if failIfUndefinedDocuments(t, "cross-tenant UPDATE", err) {
		return
	}
	if err == nil {
		t.Fatal("app-role cross-tenant UPDATE on documents succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("cross-tenant UPDATE: SQLSTATE = %q, want 42501: %v", code, err)
	}

	var gotKey string
	if err := h.super.QueryRow(ctx, `SELECT storage_key FROM documents WHERE id = $1`, docB).Scan(&gotKey); err != nil {
		t.Fatalf("read back B's storage_key as superuser: %v", err)
	}
	if gotKey != "DOC-04/b.csv" {
		t.Errorf("B's storage_key after the refused UPDATE = %q, want unchanged %q", gotKey, "DOC-04/b.csv")
	}
}

// DOC-05: an unset app.current_tenant GUC fails closed — the isolation predicate is NULL
// for every row, so the app connection sees nothing and raises no error.
func TestRLS_DocumentsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupA := seedDocument(t, h.tenantA, "DOC-05/a.csv")
	defer cleanupA()
	docB, cleanupB := seedDocument(t, h.tenantB, "DOC-05/b.csv")
	defer cleanupB()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	n, err := scanCount(ctx, tx, `SELECT count(*) FROM documents WHERE id IN ($1, $2)`, docA, docB)
	if failIfUndefinedDocuments(t, "SELECT with no tenant context", err) {
		return
	}
	if err != nil {
		t.Fatalf("SELECT with no tenant context: %v", err)
	}
	if n != 0 {
		t.Errorf("documents visible with no tenant set = %d, want 0", n)
	}
}

// DOC-06: FORCE binds the table OWNER too — invoice_migrator inserting with no tenant
// context is refused by the policy, not merely unprivileged.
func TestRLS_DocumentsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	ownerHash := docHash()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM documents WHERE content_hash = $1`, ownerHash)
	}()

	_, err := h.mig.Exec(ctx,
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4)`,
		h.tenantA, "DOC-06/owner.csv", ownerHash, 10,
	)
	if failIfUndefinedDocuments(t, "owner INSERT with no tenant context", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("owner INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE content_hash = $1`, ownerHash); n != 0 {
		t.Errorf("rows after the refused owner INSERT = %d, want 0", n)
	}
}

// DOC-07: the positive write path — invoice_app inserts its own tenant's row naming only
// the four required columns, and id/created_at default while filename and
// declared_content_type stay NULL (both are optional caller content).
func TestRLS_DocumentsOwnTenantInsertSucceedsWithDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes)
			 VALUES ($1, $2, $3, $4) RETURNING id`,
			h.tenantA, "DOC-07/a.csv", docHash(), 4096,
		).Scan(&id)
	})
	if failIfUndefinedDocuments(t, "own-tenant INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM documents WHERE id = $1`, id)
	}()

	if _, e := uuid.Parse(id); e != nil {
		t.Errorf("RETURNING id = %q, want a defaulted uuid: %v", id, e)
	}

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			filename, contentType *string
			createdAtFresh        bool
		)
		if e := tx.QueryRow(ctx,
			`SELECT filename, declared_content_type, created_at > now() - interval '1 hour'
			   FROM documents WHERE id = $1`, id,
		).Scan(&filename, &contentType, &createdAtFresh); e != nil {
			return e
		}
		if filename != nil {
			t.Errorf("filename with none supplied = %q, want NULL", *filename)
		}
		if contentType != nil {
			t.Errorf("declared_content_type with none supplied = %q, want NULL", *contentType)
		}
		if !createdAtFresh {
			t.Error("created_at is not within the last hour — the now() DEFAULT is not load-bearing")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify insert defaults: %v", err)
	}
}

// DOC-08: invoice_tenant_reader enumerates across tenants with NO tenant context — the
// tenant_enumerate policy's USING(true) ORs over tenant_isolation for that role only.
func TestRLS_DocumentsReaderEnumeratesAllTenants(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupA := seedDocument(t, h.tenantA, "DOC-08/a.csv")
	defer cleanupA()
	docB, cleanupB := seedDocument(t, h.tenantB, "DOC-08/b.csv")
	defer cleanupB()

	n, err := scanCount(ctx, h.reader, `SELECT count(*) FROM documents WHERE id IN ($1, $2)`, docA, docB)
	if failIfUndefinedDocuments(t, "reader SELECT", err) {
		return
	}
	if err != nil {
		t.Fatalf("reader SELECT on documents: %v", err)
	}
	if n != 2 {
		t.Errorf("documents visible to invoice_tenant_reader with no GUC = %d, want 2 (both tenants')", n)
	}
}

// DOC-09: the permissive reader policy must NOT widen invoice_app's own view. RLS ORs
// permissive policies together, so a tenant_enumerate written without its TO clause would
// hand every tenant's document to the app role.
func TestRLS_DocumentsAppUnaffectedByEnumeratePolicy(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupA := seedDocument(t, h.tenantA, "DOC-09/a.csv")
	defer cleanupA()
	docB, cleanupB := seedDocument(t, h.tenantB, "DOC-09/b.csv")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM documents WHERE id IN ($1, $2)`, docA, docB)
		if e != nil {
			return e
		}
		defer rows.Close()
		var seen []string
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				return e
			}
			seen = append(seen, id)
		}
		if e := rows.Err(); e != nil {
			return e
		}
		if len(seen) != 1 || seen[0] != docA {
			t.Errorf("documents visible to invoice_app under tenant A = %v, want exactly [%s] "+
				"(the tenant_enumerate policy leaked into the app role's view)", seen, docA)
		}
		return nil
	})
	if failIfUndefinedDocuments(t, "app SELECT under tenant A", err) {
		return
	}
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// DOC-10: the catalog half of least privilege — invoice_app holds exactly SELECT+INSERT
// (documents are append-only by grant) and invoice_tenant_reader exactly SELECT. Asked as
// the SUPERUSER: information_schema.role_table_grants shows only the current role's own
// grants, so "the reader holds nothing but SELECT" cannot be proven from the app pool
// (submission_jobs_rls_test.go:1004-1047).
func TestRLS_DocumentsGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", false},
		{"invoice_app", "DELETE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_app", "REFERENCES", false},
		{"invoice_tenant_reader", "SELECT", true},
		{"invoice_tenant_reader", "INSERT", false},
		{"invoice_tenant_reader", "UPDATE", false},
		{"invoice_tenant_reader", "DELETE", false},
		{"invoice_tenant_reader", "TRUNCATE", false},
		{"invoice_tenant_reader", "REFERENCES", false},
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.documents', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedDocuments(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, documents, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, documents, %q) = %v, want %v — the grants are exactly "+
				"`GRANT SELECT, INSERT ON documents TO invoice_app` and `GRANT SELECT ON documents "+
				"TO invoice_tenant_reader`", c.role, c.priv, got, c.want)
		}
	}
}

// DOC-11: a document is immutable. Even its OWN tenant cannot rewrite a row it can see —
// no UPDATE grant, so 42501 at the grant layer — and the row survives byte-identical.
func TestRLS_DocumentsAppUpdateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupA := seedDocument(t, h.tenantA, "DOC-11/a.csv")
	defer cleanupA()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE documents SET storage_key = 'rewritten' WHERE id = $1`, docA)
		return e
	})
	if failIfUndefinedDocuments(t, "own-tenant UPDATE", err) {
		return
	}
	if err == nil {
		t.Fatal("app-role UPDATE on its own documents row succeeded, want permission denied (SQLSTATE 42501) — " +
			"the grant is SELECT/INSERT only")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("own-tenant UPDATE: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	var gotKey string
	if err := h.super.QueryRow(ctx, `SELECT storage_key FROM documents WHERE id = $1`, docA).Scan(&gotKey); err != nil {
		t.Fatalf("read back storage_key as superuser: %v", err)
	}
	if gotKey != "DOC-11/a.csv" {
		t.Errorf("storage_key after the refused UPDATE = %q, want unchanged %q", gotKey, "DOC-11/a.csv")
	}
}

// DOC-12: the other half of append-only — no DELETE grant either, and a same-tenant
// DELETE of a visible row is refused (42501) with the row still there. The SELECT that
// follows is the positive half: the SAME row is reachable by the SAME role, so the
// refusal is about DELETE and not about the row being invisible.
func TestRLS_DocumentsAppDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupA := seedDocument(t, h.tenantA, "DOC-12/a.csv")
	defer cleanupA()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM documents WHERE id = $1`, docA)
		return e
	})
	if failIfUndefinedDocuments(t, "own-tenant DELETE", err) {
		return
	}
	if err == nil {
		t.Fatal("app-role DELETE on documents succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("own-tenant DELETE: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM documents WHERE id = $1`, docA); n != 1 {
			t.Errorf("own row visible to its own tenant after the refused DELETE = %d, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("re-read own row: %v", err)
	}
}

// DOC-13: dedupe is per-tenant. The same content hash under two different tenants is two
// legitimate documents — a (content_hash)-only unique index would silently make dedupe
// cross-tenant and leak one tenant's upload into another's.
func TestRLS_DocumentsSameHashDifferentTenantsAllowed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	shared := docHash()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM documents WHERE content_hash = $1`, shared)
	}()

	for _, tenant := range []string{h.tenantA, h.tenantB} {
		err := db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4)`,
				tenant, "DOC-13/"+tenant+".csv", shared, 512,
			)
			return e
		})
		if failIfUndefinedDocuments(t, "INSERT shared hash for tenant "+tenant, err) {
			return
		}
		if err != nil {
			t.Fatalf("INSERT shared hash for tenant %s: want success, got: %v", tenant, err)
		}
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE content_hash = $1`, shared); n != 2 {
		t.Errorf("rows holding the shared hash = %d, want 2 (one per tenant)", n)
	}
}

// DOC-14: one tenant may not hold the same hash twice — the second insert is 23505 named
// for documents_tenant_content_hash_uq.
func TestRLS_DocumentsSameHashSameTenantRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	hash := docHash()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM documents WHERE content_hash = $1`, hash)
	}()

	insert := func(storageKey string) error {
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4)`,
				h.tenantA, storageKey, hash, 512,
			)
			return e
		})
	}

	err := insert("DOC-14/first.csv")
	if failIfUndefinedDocuments(t, "first INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("first INSERT: want success, got: %v", err)
	}

	err = insert("DOC-14/second.csv")
	if err == nil {
		t.Fatal("second INSERT of the same (tenant_id, content_hash) succeeded, want 23505")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("second INSERT: SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "documents_tenant_content_hash_uq" {
		t.Errorf("second INSERT constraint = %q, want %q — a different index rejected it", name,
			"documents_tenant_content_hash_uq")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE content_hash = $1`, hash); n != 1 {
		t.Errorf("rows holding the hash after the refused duplicate = %d, want 1", n)
	}
}

// DOC-15: the 15 MB cap boundary. A zero-byte upload is exactly what this story exists to
// keep (unparseable, so it is the evidence), the cap itself is inclusive, and one byte
// over — or a negative size — is 23514.
func TestRLS_DocumentsSizeBoundsCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		name    string
		size    int64
		wantErr bool
	}{
		{"zero", 0, false},
		{"cap", docMaxSize, false},
		{"cap+1", docMaxSize + 1, true},
		{"negative", -1, true},
	} {
		hash := docHash()
		var id string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes)
				 VALUES ($1, $2, $3, $4) RETURNING id`,
				h.tenantA, "DOC-15/"+c.name+".csv", hash, c.size,
			).Scan(&id)
		})
		if failIfUndefinedDocuments(t, "INSERT size_bytes = "+c.name, err) {
			return
		}
		if !c.wantErr {
			if err != nil {
				t.Errorf("INSERT size_bytes = %d (%s): want success, got: %v", c.size, c.name, err)
				continue
			}
			_, _ = h.super.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id)
			continue
		}
		if err == nil {
			_, _ = h.super.Exec(ctx, `DELETE FROM documents WHERE content_hash = $1`, hash)
			t.Errorf("INSERT size_bytes = %d (%s) succeeded, want CHECK violation (SQLSTATE 23514)", c.size, c.name)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT size_bytes = %d (%s): SQLSTATE = %q, want 23514 (check_violation): %v",
				c.size, c.name, code, err)
		}
	}
}

// DOC-16: content_hash is system-computed, so its length is pinned at exactly 64 — one
// short or one long is 23514. The 64-char leg keeps this from passing against a CHECK
// that rejects everything.
func TestRLS_DocumentsContentHashLengthCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		name    string
		hash    string
		wantErr bool
	}{
		{"63-char", strings.Repeat("a", 63), true},
		{"65-char", strings.Repeat("b", 65), true},
		{"64-char", docHash(), false},
	} {
		var id string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes)
				 VALUES ($1, $2, $3, $4) RETURNING id`,
				h.tenantA, "DOC-16/"+c.name+".csv", c.hash, 128,
			).Scan(&id)
		})
		if failIfUndefinedDocuments(t, "INSERT content_hash "+c.name, err) {
			return
		}
		if !c.wantErr {
			if err != nil {
				t.Errorf("INSERT %s content_hash: want success, got: %v", c.name, err)
				continue
			}
			_, _ = h.super.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id)
			continue
		}
		if err == nil {
			_, _ = h.super.Exec(ctx, `DELETE FROM documents WHERE content_hash = $1`, c.hash)
			t.Errorf("INSERT %s content_hash succeeded, want CHECK violation (SQLSTATE 23514)", c.name)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT %s content_hash: SQLSTATE = %q, want 23514 (check_violation): %v", c.name, code, err)
		}
	}
}

// DOC-17: documents_tenant_id_id_uq must be a real UNIQUE CONSTRAINT on (tenant_id, id),
// not a bare unique index — a composite FOREIGN KEY can only reference a constraint, so a
// bare index would leave both pointer FKs unbuildable.
func TestRLS_DocumentsTenantIdIdUniqueConstraintExists(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var (
		contype string
		cols    []string
	)
	err := h.super.QueryRow(ctx,
		`SELECT c.contype::text,
		        (SELECT array_agg(a.attname::text ORDER BY k.ord)
		           FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		           JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum)
		   FROM pg_constraint c
		   JOIN pg_class t ON t.oid = c.conrelid
		   JOIN pg_namespace n ON n.oid = t.relnamespace
		  WHERE n.nspname = 'public' AND t.relname = 'documents'
		    AND c.conname = 'documents_tenant_id_id_uq'`,
	).Scan(&contype, &cols)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("no pg_constraint row named documents_tenant_id_id_uq — the documents migration is not " +
			"applied yet, or the uniqueness was declared as a bare index")
	}
	if err != nil {
		t.Fatalf("read pg_constraint for documents_tenant_id_id_uq: %v", err)
	}
	if contype != "u" {
		t.Errorf("documents_tenant_id_id_uq contype = %q, want %q (UNIQUE constraint)", contype, "u")
	}
	if got := strings.Join(cols, ","); got != "tenant_id,id" {
		t.Errorf("documents_tenant_id_id_uq columns = %q, want %q (the composite FK target)", got, "tenant_id,id")
	}
}

// DOC-18: a document referenced by an invoice cannot be deleted. Deliberately the ONLY
// pointer set — with the batch pointer also set the batch FK fires first and this would
// prove the wrong constraint — so the error must name invoices_tenant_source_document_fk.
// 23001 exactly: NO ACTION answers 23503, and accepting both would let that downgrade pass.
func TestRLS_DocumentsDeleteRestrictedByInvoiceRef(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "DOC-18 A Corp")
	defer cleanupEntityA()
	docA, cleanupDoc := seedDocument(t, h.tenantA, "DOC-18/a.csv")
	defer cleanupDoc()
	invoiceA, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "DOC-18-A")
	defer cleanupInvoice()

	if _, err := h.super.Exec(ctx,
		`UPDATE invoices SET source_document_id = $1 WHERE id = $2`, docA, invoiceA,
	); err != nil {
		if failIfUndefinedDocuments(t, "point the invoice at the document", err) {
			return
		}
		t.Fatalf("point the invoice at the document: %v", err)
	}

	_, err := h.super.Exec(ctx, `DELETE FROM documents WHERE id = $1`, docA)
	if err == nil {
		t.Fatal("deleting a document an invoice still cites succeeded, want RESTRICT (SQLSTATE 23001)")
	}
	if code := pgCode(err); code != "23001" {
		t.Fatalf("delete a cited document: SQLSTATE = %q, want 23001 (restrict_violation) — 23503 means the "+
			"FK was written ON DELETE NO ACTION, not RESTRICT: %v", code, err)
	}
	if name := pgConstraint(err); name != "invoices_tenant_source_document_fk" {
		t.Errorf("delete a cited document: constraint = %q, want %q", name, "invoices_tenant_source_document_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE id = $1`, docA); n != 1 {
		t.Errorf("document rows after the refused DELETE = %d, want 1", n)
	}
}

// DOC-19: the mirror of DOC-18 on the batch pointer — no invoice cites this document, so
// import_batches_tenant_document_fk is the only constraint that can refuse.
func TestRLS_DocumentsDeleteRestrictedByBatchRef(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "DOC-19 A Corp")
	defer cleanupEntityA()
	docA, cleanupDoc := seedDocument(t, h.tenantA, "DOC-19/a.csv")
	defer cleanupDoc()
	batchA, cleanupBatch := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupBatch()

	if _, err := h.super.Exec(ctx,
		`UPDATE import_batches SET document_id = $1 WHERE id = $2`, docA, batchA,
	); err != nil {
		if failIfUndefinedDocuments(t, "point the batch at the document", err) {
			return
		}
		t.Fatalf("point the batch at the document: %v", err)
	}

	_, err := h.super.Exec(ctx, `DELETE FROM documents WHERE id = $1`, docA)
	if err == nil {
		t.Fatal("deleting a document an import batch still cites succeeded, want RESTRICT (SQLSTATE 23001)")
	}
	if code := pgCode(err); code != "23001" {
		t.Fatalf("delete a cited document: SQLSTATE = %q, want 23001 (restrict_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "import_batches_tenant_document_fk" {
		t.Errorf("delete a cited document: constraint = %q, want %q", name, "import_batches_tenant_document_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE id = $1`, docA); n != 1 {
		t.Errorf("document rows after the refused DELETE = %d, want 1", n)
	}
}

// DOC-20: the composite FK is the whole point. Referential checks run with RLS bypassed,
// so a single-column FOREIGN KEY (source_document_id) REFERENCES documents(id) would
// happily accept another tenant's document; (tenant_id, source_document_id) cannot.
// Attempted as the superuser on purpose — this must hold with no policy in the way.
func TestRLS_InvoicesCrossTenantSourceDocumentRefRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "DOC-20/a.csv")
	defer cleanupDoc()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "DOC-20 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoice := seedInvoice(t, h.tenantB, entityB, "DOC-20-B")
	defer cleanupInvoice()

	_, err := h.super.Exec(ctx, `UPDATE invoices SET source_document_id = $1 WHERE id = $2`, docA, invoiceB)
	if failIfUndefinedDocuments(t, "point B's invoice at A's document", err) {
		return
	}
	if err == nil {
		t.Fatal("pointing tenant B's invoice at tenant A's document succeeded, want FK violation (SQLSTATE 23503) — " +
			"the FK is not composite on (tenant_id, source_document_id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant source_document_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	var got *string
	if err := h.super.QueryRow(ctx, `SELECT source_document_id FROM invoices WHERE id = $1`, invoiceB).Scan(&got); err != nil {
		t.Fatalf("read back B's source_document_id: %v", err)
	}
	if got != nil {
		t.Errorf("B's source_document_id after the refused UPDATE = %q, want NULL", *got)
	}
}

// DOC-21: the mirror of DOC-20 on import_batches.document_id.
func TestRLS_ImportBatchesCrossTenantDocumentRefRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "DOC-21/a.csv")
	defer cleanupDoc()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "DOC-21 B Corp")
	defer cleanupEntityB()
	batchB, cleanupBatch := seedImportBatch(t, h.tenantB, entityB)
	defer cleanupBatch()

	_, err := h.super.Exec(ctx, `UPDATE import_batches SET document_id = $1 WHERE id = $2`, docA, batchB)
	if failIfUndefinedDocuments(t, "point B's batch at A's document", err) {
		return
	}
	if err == nil {
		t.Fatal("pointing tenant B's import batch at tenant A's document succeeded, want FK violation (SQLSTATE 23503)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant document_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	var got *string
	if err := h.super.QueryRow(ctx, `SELECT document_id FROM import_batches WHERE id = $1`, batchB).Scan(&got); err != nil {
		t.Fatalf("read back B's document_id: %v", err)
	}
	if got != nil {
		t.Errorf("B's document_id after the refused UPDATE = %q, want NULL", *got)
	}
}

// DOC-22: the reason the pointer lives on the invoice at all. invoices.import_batch_id is
// ON DELETE SET NULL, so deleting the batch severs the import-run record — the evidence
// pointer must survive it, along with every other column. Same shape as
// TestRLS_InvoicesImportBatchDeleteOnlyNullsImportBatchID (invoices_rls_test.go:870).
func TestRLS_InvoicesSourceDocumentSurvivesBatchNulling(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "DOC-22 A Corp")
	defer cleanupEntityA()
	docA, cleanupDoc := seedDocument(t, h.tenantA, "DOC-22/a.csv")
	defer cleanupDoc()
	batchA, cleanupBatch := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupBatch()

	// The batch cites the same upload, so the delete below removes the only OTHER
	// reference to it — the document must still be reachable from the invoice.
	if _, err := h.super.Exec(ctx,
		`UPDATE import_batches SET document_id = $1 WHERE id = $2`, docA, batchA,
	); err != nil {
		if failIfUndefinedDocuments(t, "point the batch at the document", err) {
			return
		}
		t.Fatalf("point the batch at the document: %v", err)
	}

	invoiceID := uuid.NewString()
	_, err := h.super.Exec(ctx,
		`INSERT INTO invoices
		    (id, tenant_id, entity_id, import_batch_id, source_document_id, invoice_number, status, currency, subtotal)
		 VALUES ($1, $2, $3, $4, $5, 'DOC-22-A', 'validated', 'NGN', 123.45)`,
		invoiceID, h.tenantA, entityA, batchA, docA,
	)
	if failIfUndefinedDocuments(t, "seed invoice with both pointers", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed invoice with both pointers: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, invoiceID)
	}()

	if _, err := h.super.Exec(ctx, `DELETE FROM import_batches WHERE id = $1`, batchA); err != nil {
		t.Fatalf("delete the parent import_batches row: %v", err)
	}

	var (
		gotBatchID, gotDocID                *string
		gotTenantID, gotEntityID, gotNumber string
		gotStatus, gotCurrency, gotSubtotal string
	)
	if err := h.super.QueryRow(ctx,
		`SELECT import_batch_id, source_document_id::text, tenant_id::text, entity_id::text,
		        invoice_number, status, currency, subtotal::text
		   FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&gotBatchID, &gotDocID, &gotTenantID, &gotEntityID, &gotNumber, &gotStatus, &gotCurrency, &gotSubtotal); err != nil {
		t.Fatalf("read back the invoice after the batch delete: %v", err)
	}

	if gotBatchID != nil {
		t.Errorf("import_batch_id after the batch delete = %q, want NULL", *gotBatchID)
	}
	if gotDocID == nil {
		t.Fatal("source_document_id after the batch delete = NULL, want it unchanged — the evidence pointer " +
			"must not be severable by deleting the import-run record")
	}
	if *gotDocID != docA {
		t.Errorf("source_document_id after the batch delete = %q, want unchanged %q", *gotDocID, docA)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE id = $1`, docA); n != 1 {
		t.Errorf("document rows after the batch delete = %d, want 1 (the document itself must survive)", n)
	}
	if gotTenantID != h.tenantA {
		t.Errorf("tenant_id = %q, want unchanged %q", gotTenantID, h.tenantA)
	}
	if gotEntityID != entityA {
		t.Errorf("entity_id = %q, want unchanged %q", gotEntityID, entityA)
	}
	if gotNumber != "DOC-22-A" {
		t.Errorf("invoice_number = %q, want unchanged %q", gotNumber, "DOC-22-A")
	}
	if gotStatus != "validated" {
		t.Errorf("status = %q, want unchanged %q", gotStatus, "validated")
	}
	if gotCurrency != "NGN" {
		t.Errorf("currency = %q, want unchanged %q", gotCurrency, "NGN")
	}
	if gotSubtotal != "123.45" {
		t.Errorf("subtotal = %q, want unchanged %q", gotSubtotal, "123.45")
	}
}

// DOC-23: RESTRICT must not outlive the tenant. Postgres removes the referencing rows
// inside the same cascade, so DELETE FROM tenants still succeeds with a cited document
// present — the property every teardown in this suite depends on
// (import_batches_rls_test.go:355).
func TestRLS_DocumentsTenantDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'DOC-23 throwaway tenant')`, tenantID,
	); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}

	entityID, _ := seedBusinessEntity(t, tenantID, "DOC-23 throwaway entity")
	docID, cleanupDoc := seedDocument(t, tenantID, "DOC-23/a.csv")
	defer cleanupDoc()
	invoiceID, cleanupInvoice := seedInvoice(t, tenantID, entityID, "DOC-23-A")
	defer cleanupInvoice()

	if _, err := h.super.Exec(ctx,
		`UPDATE invoices SET source_document_id = $1 WHERE id = $2`, docID, invoiceID,
	); err != nil {
		if failIfUndefinedDocuments(t, "point the invoice at the document", err) {
			return
		}
		t.Fatalf("point the invoice at the document: %v", err)
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete the tenant with a cited document present: %v — RESTRICT must not survive the "+
			"tenant CASCADE, or every teardown in this suite leaks", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE id = $1`, docID); n != 0 {
		t.Errorf("document rows after the tenant delete = %d, want 0 (tenant_id ON DELETE CASCADE)", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE id = $1`, invoiceID); n != 0 {
		t.Errorf("invoice rows after the tenant delete = %d, want 0", n)
	}
}

// DOC-24: FORCE binds the owner on READS too, not just the INSERT DOC-06 covers. The
// membership leg is the silent way this breaks — policy TO clauses match via
// pg_has_role MEMBER, so a role grant would hand tenant_enumerate away with no policy edit.
func TestRLS_DocumentsOwnerCannotEnumerateUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupA := seedDocument(t, h.tenantA, "DOC-24/a.csv")
	defer cleanupA()
	docB, cleanupB := seedDocument(t, h.tenantB, "DOC-24/b.csv")
	defer cleanupB()

	tx, err := h.mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin as owner: %v", err)
	}
	n, err := scanCount(ctx, tx, `SELECT count(*) FROM documents WHERE id IN ($1, $2)`, docA, docB)
	if failIfUndefinedDocuments(t, "owner SELECT with no tenant context", err) {
		_ = tx.Rollback(ctx)
		return
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("owner SELECT with no tenant context: %v", err)
	}
	if n != 0 {
		t.Errorf("documents visible to the owner (invoice_migrator) with no context = %d, want 0 — is FORCE effective?", n)
	}
	_ = tx.Rollback(ctx)

	// With a context the owner is scoped like anyone else: A's row only, never B's.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		if got := mustCount(t, tx, `SELECT count(*) FROM documents WHERE id = $1`, docA); got != 1 {
			t.Errorf("A's document visible to the owner under tenant A = %d, want 1", got)
		}
		if got := mustCount(t, tx, `SELECT count(*) FROM documents WHERE id = $1`, docB); got != 0 {
			t.Errorf("B's document visible to the owner under tenant A = %d, want 0", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("owner SELECT under tenant A: %v", err)
	}

	for _, role := range []string{"invoice_migrator", "invoice_app"} {
		var member bool
		if err := h.super.QueryRow(ctx,
			`SELECT pg_has_role($1, 'invoice_tenant_reader', 'MEMBER')`, role,
		).Scan(&member); err != nil {
			t.Fatalf("pg_has_role(%q, invoice_tenant_reader): %v", role, err)
		}
		if member {
			t.Errorf("%s is a MEMBER of invoice_tenant_reader — tenant_enumerate's TO clause would apply "+
				"to it and hand it every tenant's documents", role)
		}
	}
}

// DOC-25: the cross-tenant pointer refusal on the INSERT path. DOC-20/21 only exercise
// UPDATE, but the importer and the invoice writer both set these columns at INSERT time.
func TestRLS_DocumentsCrossTenantPointerRefRejectedOnInsert(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "DOC-25/a.csv")
	defer cleanupDoc()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "DOC-25 B Corp")
	defer cleanupEntityB()

	for _, c := range []struct {
		what   string
		sql    string
		verify string
	}{
		{
			what: "invoices.source_document_id",
			sql: `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id)
			      VALUES ($1, $2, $3, 'DOC-25-B', $4)`,
			verify: `SELECT count(*) FROM invoices WHERE id = $1`,
		},
		{
			what: "import_batches.document_id",
			sql: `INSERT INTO import_batches (id, tenant_id, entity_id, document_id)
			      VALUES ($1, $2, $3, $4)`,
			verify: `SELECT count(*) FROM import_batches WHERE id = $1`,
		},
	} {
		rowID := uuid.NewString()
		// Superuser on purpose: this must hold with no policy in the way, since FK
		// checks themselves run with RLS bypassed.
		_, err := h.super.Exec(ctx, c.sql, rowID, h.tenantB, entityB, docA)
		if failIfUndefinedDocuments(t, "INSERT "+c.what+" cross-tenant", err) {
			return
		}
		if err == nil {
			_, _ = h.super.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, rowID)
			_, _ = h.super.Exec(ctx, `DELETE FROM import_batches WHERE id = $1`, rowID)
			t.Errorf("INSERT of a tenant-B row citing tenant A's document via %s succeeded, "+
				"want 23503 — the FK is not composite", c.what)
			continue
		}
		if code := pgCode(err); code != "23503" {
			t.Errorf("INSERT %s cross-tenant: SQLSTATE = %q, want 23503 (foreign_key_violation): %v",
				c.what, code, err)
			continue
		}
		if n := mustCount(t, h.super, c.verify, rowID); n != 0 {
			t.Errorf("rows after the refused INSERT of %s = %d, want 0", c.what, n)
		}
	}
}

// DOC-26: both pointer FKs are MATCH SIMPLE, which skips the check entirely when ANY
// referencing column is NULL. The nullable pointer is the intended half; a nullable
// tenant_id would void the cross-tenant defence DOC-20/21/25 assert, with no FK edit.
func TestRLS_DocumentsPointerTenantIDNotNullable(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		table, column string
		wantNullable  bool
	}{
		{"documents", "tenant_id", false},
		{"invoices", "tenant_id", false},
		{"invoices", "source_document_id", true},
		{"import_batches", "tenant_id", false},
		{"import_batches", "document_id", true},
	} {
		var nullable bool
		err := h.super.QueryRow(ctx,
			`SELECT NOT a.attnotnull
			   FROM pg_attribute a
			  WHERE a.attrelid = to_regclass('public.' || $1) AND a.attname = $2 AND a.attnum > 0`,
			c.table, c.column,
		).Scan(&nullable)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("no pg_attribute row for %s.%s — the documents migration is not applied yet", c.table, c.column)
		}
		if err != nil {
			t.Fatalf("read pg_attribute for %s.%s: %v", c.table, c.column, err)
		}
		if nullable != c.wantNullable {
			t.Errorf("%s.%s nullable = %v, want %v", c.table, c.column, nullable, c.wantNullable)
		}
	}

	for _, name := range []string{"invoices_tenant_source_document_fk", "import_batches_tenant_document_fk"} {
		var matchType string
		if err := h.super.QueryRow(ctx,
			`SELECT confmatchtype::text FROM pg_constraint WHERE conname = $1`, name,
		).Scan(&matchType); err != nil {
			t.Fatalf("read confmatchtype for %s: %v", name, err)
		}
		if matchType != "s" {
			t.Errorf("%s confmatchtype = %q, want %q — the NULL pointer must skip the check, "+
				"which is what makes the column nullable at all", name, matchType, "s")
		}
	}
}

// DOC-27: the two GUC shapes DOC-05 (unset) does not reach — empty string collapses
// through nullif, a malformed value raises on the ::uuid cast. WithinTenantTx rejects
// both client-side, so this bypasses it to exercise what the SQL layer alone does.
func TestRLS_DocumentsBadTenantGUCFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupA := seedDocument(t, h.tenantA, "DOC-27/a.csv")
	defer cleanupA()

	for _, c := range []struct {
		name, guc, wantCode string
	}{
		{"empty", "", "42501"},               // nullif -> NULL -> predicate NULL -> WITH CHECK fails
		{"malformed", "not-a-uuid", "22P02"}, // ::uuid raises before any row is considered
	} {
		hash := docHash()
		tx, err := h.app.Begin(ctx)
		if err != nil {
			t.Fatalf("begin (%s): %v", c.name, err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, c.guc); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("set %s app.current_tenant: %v", c.name, err)
		}

		n, selErr := scanCount(ctx, tx, `SELECT count(*) FROM documents WHERE id = $1`, docA)
		switch c.name {
		case "empty":
			if selErr != nil {
				t.Errorf("SELECT with an empty app.current_tenant: want 0 rows and no error, got: %v", selErr)
			} else if n != 0 {
				t.Errorf("documents visible with an empty app.current_tenant = %d, want 0", n)
			}
		case "malformed":
			if code := pgCode(selErr); code != "22P02" {
				t.Errorf("SELECT with a malformed app.current_tenant: SQLSTATE = %q, want 22P02: %v", code, selErr)
			}
			// The tx is poisoned by the failed SELECT; the INSERT leg needs a fresh one.
			_ = tx.Rollback(ctx)
			tx, err = h.app.Begin(ctx)
			if err != nil {
				t.Fatalf("re-begin (%s): %v", c.name, err)
			}
			if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, c.guc); err != nil {
				_ = tx.Rollback(ctx)
				t.Fatalf("re-set %s app.current_tenant: %v", c.name, err)
			}
		}

		_, insErr := tx.Exec(ctx,
			`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4)`,
			h.tenantA, "DOC-27/"+c.name+".csv", hash, 10,
		)
		if insErr == nil {
			t.Errorf("INSERT with a %s app.current_tenant succeeded, want SQLSTATE %s", c.name, c.wantCode)
		} else if code := pgCode(insErr); code != c.wantCode {
			t.Errorf("INSERT with a %s app.current_tenant: SQLSTATE = %q, want %s: %v",
				c.name, code, c.wantCode, insErr)
		}
		_ = tx.Rollback(ctx)

		if got := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE content_hash = $1`, hash); got != 0 {
			t.Errorf("rows written under a %s app.current_tenant = %d, want 0", c.name, got)
		}
	}

	// The same statement shape under a VALID context succeeds — without this the two
	// legs above would also pass against a documents table nobody can write at all.
	okHash := docHash()
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4)`,
			h.tenantA, "DOC-27/ok.csv", okHash, 10)
		return e
	}); err != nil {
		t.Fatalf("INSERT under a valid tenant context: want success, got: %v", err)
	}
	_, _ = h.super.Exec(ctx, `DELETE FROM documents WHERE content_hash = $1`, okHash)
}

// DOC-28: RESTRICT guards the document in one direction only and is a live reference
// count, not a permanent lock — deleting the citing invoice must succeed, leave the
// document standing, and release the delete DOC-18 saw blocked.
func TestRLS_DocumentsSurviveCitingInvoiceDeletion(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "DOC-28 A Corp")
	defer cleanupEntityA()
	docA, cleanupDoc := seedDocument(t, h.tenantA, "DOC-28/a.csv")
	defer cleanupDoc()
	invoiceA, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "DOC-28-A")
	defer cleanupInvoice()

	if _, err := h.super.Exec(ctx,
		`UPDATE invoices SET source_document_id = $1 WHERE id = $2`, docA, invoiceA,
	); err != nil {
		if failIfUndefinedDocuments(t, "point the invoice at the document", err) {
			return
		}
		t.Fatalf("point the invoice at the document: %v", err)
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceA); err != nil {
		t.Fatalf("delete the citing invoice: %v — RESTRICT guards the document, not the invoice", err)
	}

	var storageKey string
	if err := h.super.QueryRow(ctx, `SELECT storage_key FROM documents WHERE id = $1`, docA).Scan(&storageKey); err != nil {
		t.Fatalf("read the document back after deleting the citing invoice: %v — it must not cascade", err)
	}
	if storageKey != "DOC-28/a.csv" {
		t.Errorf("storage_key after the invoice delete = %q, want unchanged %q", storageKey, "DOC-28/a.csv")
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM documents WHERE id = $1`, docA); err != nil {
		t.Fatalf("delete the document once nothing cites it: %v — RESTRICT must release with the last reference", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE id = $1`, docA); n != 0 {
		t.Errorf("document rows after the unblocked DELETE = %d, want 0", n)
	}
}
