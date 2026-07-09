// M3-01-04 (task-27): tests for the `memberships` tenant-owned table, written BEFORE
// the migration exists (RED against SQLSTATE 42P01 undefined_table). The table the
// Executor will add:
//
//	memberships: id uuid PK DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL
//	    REFERENCES tenants(id) ON DELETE CASCADE, user_id uuid NOT NULL (the GoTrue
//	    subject — no FK, GoTrue is not this DB), role text NOT NULL REFERENCES
//	    roles(name), created_at timestamptz NOT NULL DEFAULT now(),
//	    UNIQUE (tenant_id, user_id) — FORCE RLS, policy `tenant_isolation` copied
//	    from the tenants/business_entities template (docs/migrations.md §6, §8; no
//	    tenant_enumerate policy). GRANT SELECT/INSERT/UPDATE/DELETE TO invoice_app.
//	    `roles` already exists (M3-01, rows: admin, preparer, reviewer).
//
// Each case attacks the same guarantees M2-07/BE-RLS prove for the tenants/
// business_entities shape, transplanted onto memberships, plus two
// membership-specific constraints the Test Spec calls out: the (tenant_id, user_id)
// UNIQUE index (MEM-RLS-03) and the role -> roles(name) FK (MEM-FK-05).
//
// Rows are seeded per-test (seedMembership below), NOT in the shared harness.seed()
// in rls_harness_test.go — that runs in TestMain before every test in the package, so
// a missing memberships table would break the ENTIRE suite instead of failing only
// these MEM-RLS cases.
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
//	go test -count=1 -run TestRLS_Memberships -v ./internal/platform/db/...
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedMembership inserts one memberships row for tenantID/userID/role as the
// superuser (BYPASSRLS, so seeding needs no tenant context) and returns its id plus a
// cleanup func. Scoped per-test — see the package doc comment above for why this must
// NOT move into the shared harness.seed().
func seedMembership(t *testing.T, tenantID, userID, role string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO memberships (id, tenant_id, user_id, role) VALUES ($1, $2, $3, $4)`,
		id, tenantID, userID, role,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed memberships: undefined_table (42P01) — memberships migration not applied yet: %v", err)
		}
		t.Fatalf("seed memberships: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id)
	}
}

// MEM-RLS-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees
// only A's membership row; B's is invisible (filtered out, not an error).
func TestRLS_MembershipsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	_, cleanupA := seedMembership(t, h.tenantA, uuid.NewString(), "admin")
	defer cleanupA()
	_, cleanupB := seedMembership(t, h.tenantB, uuid.NewString(), "admin")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// MEM-RLS-02: a cross-tenant INSERT (row named for tenant B while scoped to A) is
// refused with a WITH CHECK violation, SQLSTATE 42501.
func TestRLS_MembershipsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin')`,
			h.tenantB, uuid.NewString(),
		)
		return e
	})
	assertRLSViolation(t, err)
}

// MEM-RLS-03: (tenant_id, user_id) is UNIQUE — a second membership for the same user
// in the same tenant is refused, SQLSTATE 23505 unique_violation. Both rows are
// inserted by the app role scoped to the SAME tenant (A), so RLS visibility is not
// the obstacle here — only the unique index is under test.
func TestRLS_MembershipsTenantUserUnique(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	userID := uuid.NewString()
	var firstID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin') RETURNING id`,
			h.tenantA, userID,
		).Scan(&firstID)
	})
	if err != nil {
		t.Fatalf("insert first membership: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, firstID)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'preparer')`,
			h.tenantA, userID,
		)
		return e
	})
	if err == nil {
		t.Fatal("duplicate (tenant_id, user_id) succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate (tenant_id, user_id): SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
}

// MEM-RLS-04: a missing app.current_tenant GUC fails closed — with no context set,
// the isolation predicate is false for every row and the connection sees nothing.
func TestRLS_MembershipsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM memberships`); n != 0 {
		t.Errorf("memberships visible with no tenant set = %d, want 0", n)
	}
}

// MEM-FK-05: `role` references roles(name) — an unrecognized role value is refused
// with a foreign-key violation, SQLSTATE 23503 (not an RLS or CHECK failure).
func TestRLS_MembershipsRoleForeignKey(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'not_a_role')`,
			h.tenantA, uuid.NewString(),
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with role = 'not_a_role' succeeded, want foreign_key_violation (SQLSTATE 23503)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("insert with role = 'not_a_role': SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
}

// MEM-RLS-06: a positive own-tenant INSERT succeeds — proves RLS's WITH CHECK, the
// tenants(id) FK, and the roles(name) FK all coexist for a same-tenant write, and the
// row becomes visible to its own tenant.
func TestRLS_MembershipsOwnTenantInsertSucceeds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	userID := uuid.NewString()
	var (
		id     string
		before int
	)
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		before = mustCount(t, tx, `SELECT count(*) FROM memberships WHERE tenant_id = $1`, h.tenantA)
		return tx.QueryRow(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin') RETURNING id`,
			h.tenantA, userID,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if after := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE tenant_id = $1`, h.tenantA); after != before+1 {
			t.Errorf("count after own-tenant insert = %d, want %d", after, before+1)
		}
		var role string
		if e := tx.QueryRow(ctx, `SELECT role FROM memberships WHERE id = $1`, id).Scan(&role); e != nil {
			return e
		}
		if role != "admin" {
			t.Errorf("role read back = %q, want %q", role, "admin")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify own-tenant insert: %v", err)
	}
}

// MEM-RLS-07 (F2): reassigning an OWN, visible row to another tenant is refused. This
// is the case that catches a per-table policy copy-paste regression where the
// USING/WITH CHECK clause was narrowed to only validate fresh INSERTs and stopped
// re-checking an UPDATE's target tenant_id.
func TestRLS_MembershipsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	_, cleanup := seedMembership(t, h.tenantA, uuid.NewString(), "admin")
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE memberships SET tenant_id = $1 WHERE tenant_id = $2`, h.tenantB, h.tenantA)
		return e
	})
	assertRLSViolation(t, err)
}

// MEM-RLS-08 (QA-added): the SAME user_id can hold a membership in BOTH tenant A and
// tenant B at once — the (tenant_id, user_id) UNIQUE constraint (MEM-RLS-03) is scoped
// PER TENANT, not globally on user_id alone. MEM-RLS-03 only proves the negative
// (duplicate within one tenant is refused); this is the matching positive case that
// proves the scope wasn't accidentally narrowed to "one membership per user, period" —
// the entire point of a multi-tenant membership model (e.g. one accountant serving
// clients in more than one tenant).
func TestRLS_MembershipsSameUserAcrossTenants(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	userID := uuid.NewString()

	_, cleanupA := seedMembership(t, h.tenantA, userID, "admin")
	defer cleanupA()

	var idB string
	err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'preparer') RETURNING id`,
			h.tenantB, userID,
		).Scan(&idB)
	})
	if err != nil {
		t.Fatalf("insert membership for same user in tenant B: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, idB)
	}()

	// Each tenant sees exactly one membership for this user — its own — and the other
	// tenant's row for the same user stays invisible, proving the two rows are
	// RLS-isolated peers rather than one row somehow shared across tenants.
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var role string
		if e := tx.QueryRow(ctx,
			`SELECT role FROM memberships WHERE tenant_id = $1 AND user_id = $2`, h.tenantA, userID,
		).Scan(&role); e != nil {
			return e
		}
		if role != "admin" {
			t.Errorf("tenant A role for shared user = %q, want %q", role, "admin")
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE user_id = $1`, userID); n != 1 {
			t.Errorf("rows visible to A for shared user = %d, want 1 (B's row must stay invisible)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify tenant A view: %v", err)
	}

	err = db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		var role string
		if e := tx.QueryRow(ctx,
			`SELECT role FROM memberships WHERE tenant_id = $1 AND user_id = $2`, h.tenantB, userID,
		).Scan(&role); e != nil {
			return e
		}
		if role != "preparer" {
			t.Errorf("tenant B role for shared user = %q, want %q", role, "preparer")
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE user_id = $1`, userID); n != 1 {
			t.Errorf("rows visible to B for shared user = %d, want 1 (A's row must stay invisible)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify tenant B view: %v", err)
	}
}
