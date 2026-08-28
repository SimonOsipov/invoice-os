// RLS, grant and constraint suite for `extraction_page_images`. Written before its
// migration exists, so every case fails with an explicit 42P01 until that migration lands.
//
// Rows are seeded per test, never in harness.seed(): a missing table must fail only these
// cases, not the whole package. Each rejected statement gets its own db.WithinTenantTx,
// because a failed statement poisons the surrounding transaction.
//
// Run: `DEV_DB_PORT=5434 make test-rls`. requireHarness skips without the four per-role
// DSNs, and a skip is itself a failure under scripts/ci/rls-test-gate.sh — so no case here
// adds a t.Skip of its own.
package db_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	epiWidth  = 1275
	epiHeight = 1651
)

// epiInsert names every column, so a test that means to violate one constraint cannot
// silently trip a NOT NULL instead.
const epiInsert = `INSERT INTO extraction_page_images
	    (id, tenant_id, document_id, page_number, width_px, height_px, storage_key)
	 VALUES ($1, $2, $3, $4, $5, $6, $7)`

// epiKey is the shape extraction_page_images_key_tenant_scoped admits. uuid::text renders
// lowercase and the CHECK compares bytes, so the tenant segment is never case-transformed.
func epiKey(tenantID string, page int) string {
	return fmt.Sprintf("tenants/%s/pages/%s/v1/p%04d.png", tenantID, "epi"+uuid.NewString()[:8], page)
}

// failIfUndefinedPageImages turns the pre-migration failure mode into a self-explaining
// message instead of a raw driver error. Returns true when it fired.
func failIfUndefinedPageImages(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the extraction_page_images migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedPageImage inserts one row as the superuser (BYPASSRLS, so seeding needs neither
// tenant context nor an INSERT grant) and returns its id plus a cleanup func.
func seedPageImage(t *testing.T, tenantID, documentID string, page int) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx, epiInsert,
		id, tenantID, documentID, page, epiWidth, epiHeight, epiKey(tenantID, page),
	); err != nil {
		if pgCode(err) == "42P01" {
			t.Fatalf("seed extraction_page_images: undefined_table (42P01) — the migration is not applied yet: %v", err)
		}
		t.Fatalf("seed extraction_page_images: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_page_images WHERE id = $1`, id)
	}
}

// epiAsApp runs one statement as invoice_app under tenantID and returns its error.
func epiAsApp(t *testing.T, tenantID, sql string, args ...any) error {
	t.Helper()
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, sql, args...)
		return e
	})
}

// epiAssertPGCode names the constraint too: a CHECK renamed or widened trips a different
// one, and the SQLSTATE alone cannot tell those apart.
func epiAssertPGCode(t *testing.T, err error, wantCode, wantConstraint, what string) {
	t.Helper()
	if failIfUndefinedPageImages(t, what, err) {
		return
	}
	if got := pgCode(err); got != wantCode {
		t.Fatalf("%s returned SQLSTATE %q (%v), want %q on %s", what, got, err, wantCode, wantConstraint)
	}
	if wantConstraint == "" {
		return
	}
	if got := pgConstraint(err); got != wantConstraint {
		t.Errorf("%s tripped constraint %q, want %q", what, got, wantConstraint)
	}
}

// EPI-01: the catalog half of the isolation posture. ENABLE alone would let the owner
// bypass the policy, and a TO clause on tenant_isolation would leave unnamed roles unbound.
func TestRLS_ExtractionPageImagesForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var enabled, forced bool
	err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity
		   FROM pg_class WHERE oid = 'public.extraction_page_images'::regclass`,
	).Scan(&enabled, &forced)
	if failIfUndefinedPageImages(t, "read pg_class for extraction_page_images", err) {
		return
	}
	if err != nil {
		t.Fatalf("read pg_class for extraction_page_images: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("extraction_page_images relrowsecurity/relforcerowsecurity = %v/%v, want true/true "+
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
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'extraction_page_images'`)
	if err != nil {
		t.Fatalf("query pg_policies for extraction_page_images: %v", err)
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

	if len(got) != 1 {
		t.Fatalf("policies on extraction_page_images = %d (%v), want exactly 1 (tenant_isolation)", len(got), got)
	}
	p, ok := got["tenant_isolation"]
	if !ok {
		t.Fatalf("no tenant_isolation policy on extraction_page_images; got %v", got)
	}
	if p.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want ALL — a per-command policy leaves the other commands unbound", p.cmd)
	}
	if !reflect.DeepEqual(p.roles, []string{"public"}) {
		t.Errorf("tenant_isolation roles = %v, want [public] — a TO clause would leave unnamed roles unbound", p.roles)
	}
	if p.qual == "" {
		t.Error("tenant_isolation carries an empty USING; it would admit every row")
	}
}

// EPI-02: FORCE, behaviourally. The migrator OWNS the table, so without FORCE this insert
// succeeds and every other case here still passes.
func TestRLS_ExtractionPageImagesOwnerIsSubjectToTheForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EPI-02/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EPI-02/b.pdf")
	defer cleanupDocB()

	crossID, ownID := uuid.NewString(), uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_page_images WHERE id IN ($1, $2)`, crossID, ownID)
	}()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, epiInsert, crossID, h.tenantB, docB, 1, epiWidth, epiHeight, epiKey(h.tenantB, 1))
		return e
	})
	if failIfUndefinedPageImages(t, "owner cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the owner's refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half: the owner writes inside its own scope, so the 42501 above is isolation
	// and not a missing owner privilege.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, epiInsert, ownID, h.tenantA, docA, 1, epiWidth, epiHeight, epiKey(h.tenantA, 1))
		return e
	}); err != nil {
		t.Fatalf("owner own-tenant INSERT: want success, got: %v", err)
	}
}

// EPI-03: reads are scoped both ways, and the bare count catches a policy that admits
// everything while the by-id lookups still agree.
func TestRLS_ExtractionPageImagesCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EPI-03/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EPI-03/b.pdf")
	defer cleanupDocB()

	rowA, cleanupA := seedPageImage(t, h.tenantA, docA, 1)
	defer cleanupA()
	rowB, cleanupB := seedPageImage(t, h.tenantB, docB, 1)
	defer cleanupB()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, rowA); n != 1 {
			t.Errorf("tenant A sees %d of its own rows, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, rowB); n != 0 {
			t.Errorf("tenant A sees %d of tenant B's rows, want 0", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_page_images`); n != 1 {
			t.Errorf("an unfiltered count under tenant A returns %d rows, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("tenant A read: %v", err)
	}
}

// EPI-04: the USING doubles as the INSERT WITH CHECK. The composite FK is perfectly
// satisfied here, so only the policy can refuse this row.
func TestRLS_ExtractionPageImagesCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)

	docB, cleanupDocB := seedDocument(t, h.tenantB, "EPI-04/b.pdf")
	defer cleanupDocB()

	crossID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_page_images WHERE id = $1`, crossID)
	}()

	err := epiAsApp(t, h.tenantA, epiInsert, crossID, h.tenantB, docB, 1, epiWidth, epiHeight, epiKey(h.tenantB, 1))
	if failIfUndefinedPageImages(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}
}

// EPI-05: an unset app.current_tenant yields NULL, which matches no row.
func TestRLS_ExtractionPageImagesMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EPI-05/a.pdf")
	defer cleanupDoc()
	rowA, cleanupRow := seedPageImage(t, h.tenantA, docA, 1)
	defer cleanupRow()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin without a tenant GUC: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM extraction_page_images`).Scan(&n)
	if failIfUndefinedPageImages(t, "count with no tenant set", err) {
		return
	}
	if err != nil {
		t.Fatalf("count with no tenant set: %v", err)
	}
	if n != 0 {
		t.Errorf("rows visible with app.current_tenant unset = %d, want 0 (row %s exists)", n, rowA)
	}
}

// EPI-06: the FK is composite. A bare document_id REFERENCES documents(id) accepts the row
// below — the referential-integrity check runs with RLS bypassed, so nothing else would.
func TestRLS_ExtractionPageImagesRefusesADocumentFromAnotherTenant(t *testing.T) {
	h := requireHarness(t)

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EPI-06/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EPI-06/b.pdf")
	defer cleanupDocB()

	crossID, ownID := uuid.NewString(), uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_page_images WHERE id IN ($1, $2)`, crossID, ownID)
	}()

	// tenant_id is the caller's own, so the policy is satisfied and only the FK can refuse.
	err := epiAsApp(t, h.tenantA, epiInsert, crossID, h.tenantA, docB, 1, epiWidth, epiHeight, epiKey(h.tenantA, 1))
	epiAssertPGCode(t, err, "23503", "extraction_page_images_tenant_document_fk", "a row naming another tenant's document")
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the refused cross-tenant document = %d, want 0", n)
	}

	// Positive half: the same statement against the tenant's own document succeeds.
	if err := epiAsApp(t, h.tenantA, epiInsert, ownID, h.tenantA, docA, 1, epiWidth, epiHeight, epiKey(h.tenantA, 1)); err != nil {
		t.Fatalf("own-document INSERT: want success, got: %v", err)
	}
}

// EPI-07: the schema refuses a key outside the row's own tenant prefix. RLS protects the
// row; this protects what the row points at.
func TestRLS_ExtractionPageImagesRefusesAKeyOutsideTheTenantPrefix(t *testing.T) {
	h := requireHarness(t)

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EPI-07/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_page_images WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what string
		key  string
	}{
		{"another tenant's prefix", epiKey(h.tenantB, 1)},
		{"no tenants/ prefix at all", "pages/anything/v1/p0001.png"},
		{"the tenant id without the trailing separator", "tenants/" + h.tenantA + "-evil/pages/v1/p0001.png"},
		{"an upper-cased tenant segment", "tenants/" + strings.ToUpper(h.tenantA) + "/pages/v1/p0001.png"},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := epiAsApp(t, h.tenantA, epiInsert, id, h.tenantA, docA, 1, epiWidth, epiHeight, c.key)
		epiAssertPGCode(t, err, "23514", "extraction_page_images_key_tenant_scoped", "a storage_key with "+c.what)
		if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, id); n != 0 {
			t.Errorf("rows after the refused key (%s) = %d, want 0", c.what, n)
		}
	}

	// Positive half: the tenant's own prefix is admitted, so the four rejections above are
	// the CHECK and not a broken statement.
	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := epiAsApp(t, h.tenantA, epiInsert, okID, h.tenantA, docA, 1, epiWidth, epiHeight, epiKey(h.tenantA, 1)); err != nil {
		t.Fatalf("a key under the tenant's own prefix: want success, got: %v", err)
	}
}

// EPI-08: one row per page of a document. Scoped to the document, not global — the same
// page number under a second document is a different row.
func TestRLS_ExtractionPageImagesRefusesADuplicatePage(t *testing.T) {
	h := requireHarness(t)

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EPI-08/a.pdf")
	defer cleanupDocA()
	docA2, cleanupDocA2 := seedDocument(t, h.tenantA, "EPI-08/a2.pdf")
	defer cleanupDocA2()

	firstID, cleanupFirst := seedPageImage(t, h.tenantA, docA, 1)
	defer cleanupFirst()

	dupID, otherPageID, otherDocID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_page_images WHERE id = ANY($1)`,
			[]string{dupID, otherPageID, otherDocID})
	}()

	err := epiAsApp(t, h.tenantA, epiInsert, dupID, h.tenantA, docA, 1, epiWidth, epiHeight, epiKey(h.tenantA, 1))
	epiAssertPGCode(t, err, "23505", "extraction_page_images_tenant_document_page_uq",
		"a second row for page 1 of the same document")
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE document_id = $1`, docA); n != 1 {
		t.Errorf("rows for the document after the refused duplicate = %d, want 1 (%s)", n, firstID)
	}

	// A different page of the same document, and the same page of a different document, are
	// both admitted — the index is (tenant_id, document_id, page_number), not a global one.
	if err := epiAsApp(t, h.tenantA, epiInsert, otherPageID, h.tenantA, docA, 2, epiWidth, epiHeight, epiKey(h.tenantA, 2)); err != nil {
		t.Fatalf("page 2 of the same document: want success, got: %v", err)
	}
	if err := epiAsApp(t, h.tenantA, epiInsert, otherDocID, h.tenantA, docA2, 1, epiWidth, epiHeight, epiKey(h.tenantA, 1)); err != nil {
		t.Fatalf("page 1 of a second document: want success, got: %v", err)
	}
}

// EPI-09: pages count from 1 and a rendered page has area. A zero page_number is the
// 0-based off-by-one every canvas would then misindex.
func TestRLS_ExtractionPageImagesRefusesANonPositivePageOrDimension(t *testing.T) {
	h := requireHarness(t)

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EPI-09/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_page_images WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what                string
		page, width, height int
		constraint          string
	}{
		{"page_number 0", 0, epiWidth, epiHeight, "extraction_page_images_page_number_check"},
		{"page_number -1", -1, epiWidth, epiHeight, "extraction_page_images_page_number_check"},
		{"width_px 0", 1, 0, epiHeight, "extraction_page_images_width_px_check"},
		{"height_px 0", 1, epiWidth, 0, "extraction_page_images_height_px_check"},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := epiAsApp(t, h.tenantA, epiInsert, id, h.tenantA, docA, c.page, c.width, c.height, epiKey(h.tenantA, c.page))
		epiAssertPGCode(t, err, "23514", c.constraint, "a row with "+c.what)
		if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, id); n != 0 {
			t.Errorf("rows after the refused %s = %d, want 0", c.what, n)
		}
	}

	// Positive half: page 1 with real dimensions is admitted.
	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := epiAsApp(t, h.tenantA, epiInsert, okID, h.tenantA, docA, 1, epiWidth, epiHeight, epiKey(h.tenantA, 1)); err != nil {
		t.Fatalf("page 1 at %dx%d: want success, got: %v", epiWidth, epiHeight, err)
	}
}

// EPI-10: least privilege, asked as the superuser on purpose —
// information_schema.role_table_grants shows only the current role's own grants, so the
// "reader holds nothing" half cannot be proven from the app pool. The three `true` rows
// make this notice a MISSING grant, not only a forbidden present one.
func TestRLS_ExtractionPageImagesGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "DELETE", true},
		{"invoice_app", "UPDATE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_app", "REFERENCES", false},
		{"invoice_tenant_reader", "SELECT", false},
		{"invoice_tenant_reader", "INSERT", false},
		{"invoice_tenant_reader", "UPDATE", false},
		{"invoice_tenant_reader", "DELETE", false},
		{"invoice_tenant_reader", "TRUNCATE", false},
		{"invoice_tenant_reader", "REFERENCES", false},
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.extraction_page_images', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedPageImages(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, extraction_page_images, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, extraction_page_images, %q) = %v, want %v — the grant is "+
				"exactly SELECT, INSERT, DELETE to invoice_app and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}
}

// EPI-11: no UPDATE. A re-render arrives as a whole-set replace, so an in-place edit of a
// stored key or dimension has no writer and must have no privilege either.
func TestRLS_ExtractionPageImagesUpdateIsNotGranted(t *testing.T) {
	h := requireHarness(t)

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EPI-11/a.pdf")
	defer cleanupDoc()
	rowA, cleanupRow := seedPageImage(t, h.tenantA, docA, 1)
	defer cleanupRow()

	before := epiStorageKey(t, rowA)

	err := epiAsApp(t, h.tenantA, `UPDATE extraction_page_images SET width_px = 1 WHERE id = $1`, rowA)
	if failIfUndefinedPageImages(t, "UPDATE as invoice_app", err) {
		return
	}
	if got := pgCode(err); got != "42501" {
		t.Errorf("invoice_app ran UPDATE on extraction_page_images and got SQLSTATE %q (%v), want 42501 "+
			"— the table holds no UPDATE grant", got, err)
	}
	if after := epiStorageKey(t, rowA); after != before {
		t.Errorf("storage_key moved from %q to %q under a refused UPDATE", before, after)
	}
	var width int
	if err := h.super.QueryRow(context.Background(),
		`SELECT width_px FROM extraction_page_images WHERE id = $1`, rowA).Scan(&width); err != nil {
		t.Fatalf("read width_px for %s: %v", rowA, err)
	}
	if width != epiWidth {
		t.Errorf("width_px = %d after a refused UPDATE, want the seeded %d", width, epiWidth)
	}
}

// EPI-12: DELETE is granted, and the policy is what confines it. An unscoped DELETE under
// tenant B must reach none of tenant A's rows.
func TestRLS_ExtractionPageImagesUnscopedDeleteCannotReachAnotherTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EPI-12/a.pdf")
	defer cleanupDocA()
	rowA, cleanupA := seedPageImage(t, h.tenantA, docA, 1)
	defer cleanupA()

	var affected int64
	if err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `DELETE FROM extraction_page_images`)
		affected = tag.RowsAffected()
		return e
	}); err != nil {
		if failIfUndefinedPageImages(t, "unscoped DELETE under tenant B", err) {
			return
		}
		t.Fatalf("unscoped DELETE under tenant B: %v", err)
	}
	if affected != 0 {
		t.Errorf("an unscoped DELETE under tenant B removed %d row(s), want 0", affected)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, rowA); n != 1 {
		t.Errorf("tenant A's row survives the unscoped DELETE = %d, want 1", n)
	}

	// Positive half: the owning tenant does delete its own row, so the 0 above is the policy
	// and not a missing DELETE grant.
	if err := epiAsApp(t, h.tenantA, `DELETE FROM extraction_page_images WHERE id = $1`, rowA); err != nil {
		t.Fatalf("own-tenant DELETE: want success, got: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, rowA); n != 0 {
		t.Errorf("rows after the tenant's own DELETE = %d, want 0", n)
	}
}

// EPI-13: a page image is a derived copy with no meaning once its document is gone, so the
// FK cascades. RESTRICT here would raise 23503 on the document delete instead.
func TestRLS_ExtractionPageImagesCascadeFromDocuments(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	doomedDoc, _ := seedDocument(t, h.tenantA, "EPI-13/doomed.pdf")
	survivingDoc, cleanupSurviving := seedDocument(t, h.tenantA, "EPI-13/surviving.pdf")
	defer cleanupSurviving()

	doomedRow, _ := seedPageImage(t, h.tenantA, doomedDoc, 1)
	survivingRow, cleanupSurvivingRow := seedPageImage(t, h.tenantA, survivingDoc, 1)
	defer cleanupSurvivingRow()

	if _, err := h.super.Exec(ctx, `DELETE FROM documents WHERE id = $1`, doomedDoc); err != nil {
		t.Fatalf("delete the document: %v — the page-image FK must cascade, not restrict", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, doomedRow); n != 0 {
		t.Errorf("rows for the deleted document = %d, want 0", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_page_images WHERE id = $1`, survivingRow); n != 1 {
		t.Errorf("the other document's row = %d, want 1 — the cascade took more than its own document", n)
	}
}

// EPI-14: eight columns, no updated_at and no trigger. A re-render replaces the row set, so
// a row is never edited and has no moment to stamp.
func TestRLS_ExtractionPageImagesColumnsAndNoTriggers(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull
		   FROM pg_attribute a
		  WHERE a.attrelid = 'public.extraction_page_images'::regclass
		    AND a.attnum > 0 AND NOT a.attisdropped
		  ORDER BY a.attnum`)
	if failIfUndefinedPageImages(t, "query pg_attribute for extraction_page_images", err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_attribute for extraction_page_images: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name, typ string
		var notNull bool
		if e := rows.Scan(&name, &typ, &notNull); e != nil {
			t.Fatalf("scan pg_attribute row: %v", e)
		}
		got = append(got, fmt.Sprintf("%s %s notnull=%v", name, typ, notNull))
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_attribute rows: %v", e)
	}

	want := []string{
		"id uuid notnull=true",
		"tenant_id uuid notnull=true",
		"document_id uuid notnull=true",
		"page_number integer notnull=true",
		"width_px integer notnull=true",
		"height_px integer notnull=true",
		"storage_key text notnull=true",
		"created_at timestamp with time zone notnull=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extraction_page_images columns =\n%v\nwant\n%v", got, want)
	}

	var triggers int
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM pg_trigger
		  WHERE tgrelid = 'public.extraction_page_images'::regclass AND NOT tgisinternal`).
		Scan(&triggers); err != nil {
		t.Fatalf("query pg_trigger for extraction_page_images: %v", err)
	}
	if triggers != 0 {
		t.Errorf("non-internal triggers on extraction_page_images = %d, want 0", triggers)
	}
}

// EPI-15: the two indexes the design names, by column order. A unique CONSTRAINT and a bare
// unique INDEX are different catalog objects, and only the constraint can be an FK target.
func TestRLS_ExtractionPageImagesIndexesAndCompositeUnique(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT c.relname, x.indisunique,
		        (SELECT array_agg(att.attname ORDER BY k.ord)
		           FROM unnest(x.indkey) WITH ORDINALITY AS k(attnum, ord)
		           JOIN pg_attribute att
		             ON att.attrelid = x.indrelid AND att.attnum = k.attnum)
		   FROM pg_index x JOIN pg_class c ON c.oid = x.indexrelid
		  WHERE x.indrelid = 'public.extraction_page_images'::regclass
		  ORDER BY c.relname`)
	if failIfUndefinedPageImages(t, "query pg_index for extraction_page_images", err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_index for extraction_page_images: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var name string
		var unique bool
		var cols []string
		if e := rows.Scan(&name, &unique, &cols); e != nil {
			t.Fatalf("scan pg_index row: %v", e)
		}
		got[name] = fmt.Sprintf("unique=%v cols=%v", unique, cols)
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_index rows: %v", e)
	}

	want := map[string]string{
		"extraction_page_images_pkey":                    "unique=true cols=[id]",
		"extraction_page_images_tenant_id_id_uq":         "unique=true cols=[tenant_id id]",
		"extraction_page_images_tenant_document_page_uq": "unique=true cols=[tenant_id document_id page_number]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("indexes on extraction_page_images =\n%v\nwant\n%v", got, want)
	}

	// The composite FK target must be a CONSTRAINT: a bare CREATE UNIQUE INDEX leaves no
	// pg_constraint row and cannot be referenced. PG18 stores NOT NULLs as contype 'n', so
	// the contype filter is mandatory.
	var uq int
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint
		  WHERE conrelid = 'public.extraction_page_images'::regclass
		    AND contype = 'u' AND conname = 'extraction_page_images_tenant_id_id_uq'`).Scan(&uq); err != nil {
		t.Fatalf("query pg_constraint for the composite unique: %v", err)
	}
	if uq != 1 {
		t.Errorf("UNIQUE constraints named extraction_page_images_tenant_id_id_uq = %d, want 1", uq)
	}
}

// epiStorageKey reads one row's storage_key as the superuser.
func epiStorageKey(t *testing.T, id string) string {
	t.Helper()
	var key string
	if err := h.super.QueryRow(context.Background(),
		`SELECT storage_key FROM extraction_page_images WHERE id = $1`, id).Scan(&key); err != nil {
		t.Fatalf("read storage_key for %s: %v", id, err)
	}
	return key
}
