// M3-01-03 (task-26): tests for the `business_entities` tenant-owned table, written
// BEFORE the migration exists (RED against SQLSTATE 42P01 undefined_table). The table
// the Executor will add:
//
//	business_entities: id uuid PK, tenant_id uuid NOT NULL REFERENCES tenants(id)
//	    ON DELETE CASCADE, name text NOT NULL, tin text, registration text, sector
//	    text, address text, status text NOT NULL DEFAULT 'active'
//	    CHECK (status IN ('active','archived')), created_at timestamptz NOT NULL
//	    DEFAULT now() — FORCE RLS, policy `tenant_isolation` copied from the
//	    `tenants` template (docs/migrations.md §6, §8), GRANT SELECT/INSERT/UPDATE/
//	    DELETE TO invoice_app.
//
// Each case attacks the same guarantees M2-07 (rls_test.go) proves for the
// tenants/rls_fixture shape, transplanted onto this real M3 table — including
// BE-RLS-07, which specifically catches a per-table policy copy-paste regression
// (a WITH CHECK that only re-validates on INSERT, not on UPDATE's target tenant).
//
// Rows are seeded per-test (seedBusinessEntity below), NOT in the shared
// harness.seed() in rls_harness_test.go — that runs in TestMain before every test in
// the package, so a missing business_entities table would break the ENTIRE suite
// instead of failing only these BE-RLS cases.
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS`
// (.github/workflows/ci.yml) and `make test-rls` both pick these up automatically.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_BusinessEntities ./internal/platform/db/...
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedBusinessEntity inserts one business_entities row for tenantID as the
// superuser (BYPASSRLS, so seeding needs no tenant context) and returns its id plus
// a cleanup func. Scoped per-test — see the package doc comment above for why this
// must NOT move into the shared harness.seed().
func seedBusinessEntity(t *testing.T, tenantID, name string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
		id, tenantID, name,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed business_entities: undefined_table (42P01) — business_entities migration not applied yet: %v", err)
		}
		t.Fatalf("seed business_entities: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	}
}

// BE-RLS-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees
// only A's business_entities row; B's is invisible (filtered out, not an error).
func TestRLS_BusinessEntitiesCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	_, cleanupA := seedBusinessEntity(t, h.tenantA, "A Corp")
	defer cleanupA()
	_, cleanupB := seedBusinessEntity(t, h.tenantB, "B Corp")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// BE-RLS-02: a cross-tenant INSERT (row named for tenant B while scoped to A) is
// refused with a WITH CHECK violation, SQLSTATE 42501.
func TestRLS_BusinessEntitiesCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO business_entities (tenant_id, name) VALUES ($1, 'rogue')`, h.tenantB)
		return e
	})
	assertRLSViolation(t, err)
}

// BE-RLS-03: an UPDATE that targets another tenant's rows affects zero rows and
// raises no error — B's row is simply invisible to a tx scoped to A.
func TestRLS_BusinessEntitiesCrossTenantUpdateAffectsZeroRows(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	_, cleanupB := seedBusinessEntity(t, h.tenantB, "B Corp")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE business_entities SET status = 'archived' WHERE tenant_id = $1`, h.tenantB)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 0 {
			t.Errorf("cross-tenant UPDATE affected %d rows, want 0", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cross-tenant UPDATE (expected 0 rows): %v", err)
	}
}

// BE-RLS-04: a missing app.current_tenant GUC fails closed — with no context set,
// the isolation predicate is false for every row and the connection sees nothing.
func TestRLS_BusinessEntitiesMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM business_entities`); n != 0 {
		t.Errorf("business_entities visible with no tenant set = %d, want 0", n)
	}
}

// BE-RLS-05: a positive own-tenant INSERT succeeds — proves RLS's WITH CHECK and
// the tenants(id) FK coexist for a same-tenant write, the row becomes visible, and
// `status` actually defaults to 'active'.
func TestRLS_BusinessEntitiesOwnTenantInsertSucceeds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var (
		id     string
		before int
	)
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		before = mustCount(t, tx, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, h.tenantA)
		return tx.QueryRow(ctx,
			`INSERT INTO business_entities (tenant_id, name) VALUES ($1, 'Acme') RETURNING id`,
			h.tenantA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if after := mustCount(t, tx, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, h.tenantA); after != before+1 {
			t.Errorf("count after own-tenant insert = %d, want %d", after, before+1)
		}
		var status string
		if e := tx.QueryRow(ctx, `SELECT status FROM business_entities WHERE id = $1`, id).Scan(&status); e != nil {
			return e
		}
		if status != "active" {
			t.Errorf("status default = %q, want %q", status, "active")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify own-tenant insert: %v", err)
	}
}

// BE-RLS-06: the table OWNER (invoice_migrator) is bound by the policy under FORCE
// exactly like the `tenants` template — a cross-tenant INSERT is refused even for
// the owner, SQLSTATE 42501.
func TestRLS_BusinessEntitiesOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO business_entities (tenant_id, name) VALUES ($1, 'rogue')`, h.tenantB)
		return e
	})
	assertRLSViolation(t, err)
}

// BE-RLS-07: reassigning an OWN, visible row to another tenant is refused. This is
// the case that catches a per-table policy copy-paste regression where the
// USING/WITH CHECK clause was narrowed to only validate fresh INSERTs and stopped
// re-checking an UPDATE's target tenant_id.
func TestRLS_BusinessEntitiesOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	_, cleanup := seedBusinessEntity(t, h.tenantA, "A Corp")
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE business_entities SET tenant_id = $1 WHERE tenant_id = $2`, h.tenantB, h.tenantA)
		return e
	})
	assertRLSViolation(t, err)
}

// BE-RLS-08 (QA-added): the RLS Test-Spec above covers tenant isolation, not the
// table's own constraints — `business_entities_tenant_tin_uq` is a *partial* unique
// index (`WHERE tin IS NOT NULL`), which has three distinct failure modes a naive
// plain UNIQUE(tenant_id, tin) would get wrong: (1) a duplicate non-NULL tin within
// the SAME tenant must be rejected (23505 unique_violation); (2) two rows with tin
// IS NULL must both succeed — the partial index excludes NULLs, so NULL is not
// "duplicated"; (3) the SAME tin string under a DIFFERENT tenant must succeed —
// uniqueness is scoped per-tenant, not global. All three run as h.app inside
// WithinTenantTx (the real runtime path), and clean up their own probe rows via the
// superuser pool (bypasses RLS, so cleanup doesn't depend on tenant context).
func TestRLS_BusinessEntitiesTinUniquePerTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cleanupIDs := func(ids ...string) {
		for _, id := range ids {
			if id == "" {
				continue
			}
			_, _ = h.super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
		}
	}

	// (1) duplicate non-NULL tin within the SAME tenant (A) is rejected.
	var firstID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, 'First Co', 'TIN-DUP-1') RETURNING id`,
			h.tenantA,
		).Scan(&firstID)
	})
	if err != nil {
		t.Fatalf("insert first row with tin: %v", err)
	}
	defer cleanupIDs(firstID)

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, 'Second Co', 'TIN-DUP-1')`,
			h.tenantA,
		)
		return e
	})
	if err == nil {
		t.Fatal("duplicate tin within same tenant succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate tin within same tenant: SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}

	// (2) two rows with tin IS NULL under the same tenant both succeed — the partial
	// index excludes NULLs, so NULL is never "duplicated".
	var nullID1, nullID2 string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, 'Null TIN Co 1', NULL) RETURNING id`,
			h.tenantA,
		).Scan(&nullID1)
	})
	if err != nil {
		t.Fatalf("insert first NULL-tin row: %v", err)
	}
	defer cleanupIDs(nullID1)

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, 'Null TIN Co 2', NULL) RETURNING id`,
			h.tenantA,
		).Scan(&nullID2)
	})
	if err != nil {
		t.Fatalf("insert second NULL-tin row (want success, partial index excludes NULLs): %v", err)
	}
	defer cleanupIDs(nullID2)

	// (3) the SAME tin string under a DIFFERENT tenant (B) succeeds — uniqueness is
	// per-tenant, not global.
	var otherTenantID string
	err = db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, 'Other Tenant Co', 'TIN-DUP-1') RETURNING id`,
			h.tenantB,
		).Scan(&otherTenantID)
	})
	if err != nil {
		t.Fatalf("insert same tin under different tenant (want success, uniqueness is per-tenant): %v", err)
	}
	cleanupIDs(otherTenantID)
}

// BE-RLS-09 (QA-added): `status` has a CHECK constraint restricting it to
// ('active','archived') — the RLS Test-Spec only ever inserts with the default
// ('active'), so the CHECK itself was never exercised. Confirm it actually rejects a
// value outside the allowed set (23514 check_violation) and actually accepts the
// other legitimate value, 'archived' (not just the default).
func TestRLS_BusinessEntitiesStatusCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// A bogus status is rejected.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO business_entities (tenant_id, name, status) VALUES ($1, 'Bogus Status Co', 'pending')`,
			h.tenantA,
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with status = 'pending' succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with status = 'pending': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}

	// The other legitimate value, 'archived', is accepted and round-trips.
	var id string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO business_entities (tenant_id, name, status) VALUES ($1, 'Archived Co', 'archived') RETURNING id`,
			h.tenantA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert with status = 'archived': want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	}()

	var status string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM business_entities WHERE id = $1`, id).Scan(&status)
	})
	if err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if status != "archived" {
		t.Errorf("status read back = %q, want %q", status, "archived")
	}
}

// BE-RLS-10 (M4-13-02, least-privilege proof): invoice_tenant_reader has NO grant on
// business_entities at all (the migration header: "Deliberately NO
// tenant_enumerate/invoice_tenant_reader policy"; GRANT line names only invoice_app).
// A bare SELECT as that role must fail at the GRANT level (SQLSTATE 42501
// insufficient_privilege) before RLS is even evaluated — proving the table was never
// exposed to the one cross-tenant enumeration identity. None of BE-RLS-01..09 connect
// as h.reader, so a future migration that widened the GRANT to include this role would
// slip through unnoticed without this case (same guarantee TestRLS_InvoicesReaderHasNoGrant
// and TestRLS_ImportBatchesReaderHasNoGrant prove for their tables). The reader pool
// carries no tenant GUC/tx — the grant check fires first, so no context is needed.
func TestRLS_BusinessEntitiesReaderHasNoGrant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM business_entities`).Scan(&n)
	if err == nil {
		t.Fatal("invoice_tenant_reader SELECT on business_entities succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("invoice_tenant_reader SELECT on business_entities: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}
}

// BE-RLS-11 (M4-13-02): deleting the parent `tenants` row cascades its
// business_entities rows away — `tenant_id` is `REFERENCES tenants(id) ON DELETE
// CASCADE` (migration 20260709155011). Uses a fresh, throwaway tenant (NOT the shared
// h.tenantA/B, whose teardown owns them) since deleting it is the whole point of the
// test — mirrors TestRLS_ImportBatchesTenantDeleteCascades's throwaway-tenant seeding.
// The child entity's cleanup func is discarded: the CASCADE removes it, so there is
// nothing left to clean up (and no defer for the tenant row itself — deleting it is the
// action under test). The survival count runs as h.super (BYPASSRLS) so RLS visibility
// can't mask a row that outlived the cascade.
func TestRLS_BusinessEntitiesTenantDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'BE-11 throwaway tenant')`, tenantID,
	); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	// Deliberately no defer cleanup for the tenant row itself — deleting it is the
	// action under test, and its CASCADE is expected to take the entity too.

	entityID, _ := seedBusinessEntity(t, tenantID, "BE-11 throwaway entity")

	// Establish the pre-state explicitly (as h.super/BYPASSRLS): the child row exists
	// == 1 BEFORE the parent delete. Without this, a future regression that made the
	// seed a silent no-op would let the post-delete count == 0 pass vacuously (a row
	// that never existed can't prove a cascade removed it).
	if n := mustCount(t, h.super, `SELECT count(*) FROM business_entities WHERE id = $1`, entityID); n != 1 {
		t.Fatalf("pre-state: seeded business_entities count = %d, want 1 (before parent tenant delete)", n)
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete parent tenants row: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM business_entities WHERE id = $1`, entityID); n != 0 {
		t.Errorf("business_entities rows after tenant delete = %d, want 0 (tenant_id ON DELETE CASCADE)", n)
	}
}

// BE-RLS-12 (M4-13-02): the composite FK invoices_tenant_entity_fk is ON DELETE
// RESTRICT (migration 20260718104103). Deleting a business_entities row that still has
// a live invoice is refused with 23001 restrict_violation — even for invoice_app, which
// DOES hold the DELETE grant (GRANT ...,DELETE... TO invoice_app). This is the net-new
// path vs the sibling TestRLS_InvoicesEntityDeleteRestricted (invoices_rls_test.go:648),
// which deletes via h.super: BE-RLS-12 deletes via h.app inside a tenantA tx (the real
// runtime identity + context), proving the RESTRICT bites at the DB layer regardless of
// role privilege, not merely for the superuser. The entity must survive the refusal.
//
// Cleanup order matters and copies INV-RLS-16 exactly (invoices_rls_test.go:654-659):
// the invoice must be removed BEFORE the entity (entity_id is ON DELETE RESTRICT, so
// cleaning up the entity first would recreate the very violation under test). Deferred
// funcs run LIFO, so defer the entity cleanup FIRST and the invoice cleanup SECOND —
// making the invoice cleanup run first.
func TestRLS_BusinessEntitiesEntityDeleteRestrictedForApp(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entity, cleanupE := seedBusinessEntity(t, h.tenantA, "BE-12 Corp")
	inv, cleanupI := seedInvoice(t, h.tenantA, entity, "BE-12-INV")
	defer cleanupE()
	defer cleanupI()
	_ = inv

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM business_entities WHERE id = $1`, entity)
		return e
	})
	if err == nil {
		t.Fatal("app-role DELETE of a business_entities row with a live invoice succeeded, want restrict_violation (SQLSTATE 23001, ON DELETE RESTRICT)")
	}
	if code := pgCode(err); code != "23001" {
		t.Fatalf("app-role DELETE of a business_entities row with a live invoice: SQLSTATE = %q, want 23001 (restrict_violation): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM business_entities WHERE id = $1`, entity); n != 1 {
		t.Errorf("business_entities rows after refused delete = %d, want 1 (row must survive)", n)
	}
}
