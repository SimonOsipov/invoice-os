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
