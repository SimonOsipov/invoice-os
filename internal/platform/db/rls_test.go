// The M2-07 adversarial RLS suite: attack the tenant-isolation guarantees of the
// M2-06 layer (FORCE RLS + WithinTenantTx) as each role and assert every attack
// fails. Each case maps to a named guard in the migration / WithinTenantTx; if a guard
// regresses (FORCE dropped, set_config switched to session scope, a grant widened) the
// matching case turns red. The shared harness (pools, fixture table, seeded tenants)
// lives in rls_harness_test.go; run via the CI `rls` job or `make test-rls`.
package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Cross-tenant SELECT is refused: an app-role tx scoped to tenant A sees only A's
// rows, in both `tenants` (the self-referential root) and the fixture table (the
// downstream shape). B's rows are invisible — filtered out, not an error.
func TestRLS_CrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM tenants WHERE id = $1`, h.tenantA); n != 1 {
			t.Errorf("tenants: A visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM tenants WHERE id = $1`, h.tenantB); n != 0 {
			t.Errorf("tenants: B visible to A = %d, want 0", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM rls_fixture WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("fixture: B rows visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// Cross-tenant writes by the app role are refused. INSERT/UPDATE that would place a
// row outside the current tenant raise a WITH CHECK violation; UPDATE that targets an
// invisible tenant's rows simply affects zero rows.
func TestRLS_CrossTenantWriteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// INSERT a row for another tenant -> WITH CHECK violation.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO rls_fixture (tenant_id, payload) VALUES ($1, 'rogue')`, h.tenantB)
		return e
	})
	assertRLSViolation(t, err)

	// UPDATE that moves a visible (A) row into another tenant -> WITH CHECK violation.
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE rls_fixture SET tenant_id = $1 WHERE tenant_id = $2`, h.tenantB, h.tenantA)
		return e
	})
	assertRLSViolation(t, err)

	// UPDATE targeting another tenant's rows -> 0 rows affected, no error (B invisible).
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE rls_fixture SET payload = 'hacked' WHERE tenant_id = $1`, h.tenantB)
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

// The table OWNER (invoice_migrator) is subject to the policy under FORCE, so even it
// cannot write across tenants. Without FORCE the owner would bypass RLS entirely.
func TestRLS_OwnerWriteRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// Scoped to A, create a row that defaults to a fresh id (!= A) -> WITH CHECK violation.
	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO tenants (name) VALUES ('rogue')`)
		return e
	})
	assertRLSViolation(t, err)

	// Scoped to A, B's row is invisible -> UPDATE affects zero rows.
	err = db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE tenants SET name = 'hacked' WHERE id = $1`, h.tenantB)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 0 {
			t.Errorf("owner cross-tenant UPDATE affected %d rows, want 0", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("owner cross-tenant UPDATE (expected 0 rows): %v", err)
	}
}

// A missing tenant context fails CLOSED: with no app.current_tenant set the GUC is
// NULL, the isolation predicate is false for every row, and the connection sees
// nothing. And WithinTenantTx refuses an empty / malformed tenant before opening a tx.
func TestRLS_MissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM tenants`); n != 0 {
		t.Errorf("tenants visible with no tenant set = %d, want 0", n)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM rls_fixture`); n != 0 {
		t.Errorf("fixture rows visible with no tenant set = %d, want 0", n)
	}

	for _, bad := range []string{"", "not-a-uuid"} {
		ran := false
		err := db.WithinTenantTx(ctx, h.app, bad, func(pgx.Tx) error {
			ran = true
			return nil
		})
		if !errors.Is(err, db.ErrNoTenant) {
			t.Errorf("WithinTenantTx(%q) error = %v, want ErrNoTenant", bad, err)
		}
		if ran {
			t.Errorf("WithinTenantTx(%q) ran fn; it must issue no statement", bad)
		}
	}
}

// The owner cannot bypass a FORCE'd table on reads either: with no context it sees
// zero rows. Companion assertion: no app role holds BYPASSRLS or SUPERUSER, so RLS
// can never silently become a no-op for them.
func TestRLS_OwnerCannotBypassSelectUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM tenants`); n != 0 {
		t.Errorf("owner sees %d tenants with no context, want 0 (is FORCE effective?)", n)
	}
}

func TestRLS_NoRoleCanBypassRLS(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, role := range []string{"invoice_app", "invoice_migrator", "invoice_tenant_reader"} {
		var bypass, super bool
		if err := h.super.QueryRow(ctx,
			`SELECT rolbypassrls, rolsuper FROM pg_roles WHERE rolname = $1`, role,
		).Scan(&bypass, &super); err != nil {
			t.Fatalf("read pg_roles for %s: %v", role, err)
		}
		if bypass {
			t.Errorf("%s has BYPASSRLS; RLS would be a no-op for it", role)
		}
		if super {
			t.Errorf("%s is SUPERUSER; it must be NOSUPERUSER", role)
		}
	}
}

// A reused pooled connection cannot carry tenant context across transactions. This is
// the invariant behind WithinTenantTx's set_config(..., is_local=true): SET LOCAL is
// transaction-scoped, so the setting evaporates on commit and the next transaction on
// the same physical connection starts clean. A session-scoped SET would leak here.
func TestRLS_PooledConnReuseDoesNotCarryContext(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// MaxConns=1 guarantees tx2 reuses the exact connection tx1 ran on.
	cfg, err := pgxpool.ParseConfig(h.appURL)
	if err != nil {
		t.Fatalf("parse app url: %v", err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("new single-conn pool: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	// tx1: set tenant A exactly as WithinTenantTx does, confirm A is visible, commit.
	tx1, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("tx1 begin: %v", err)
	}
	if _, err := tx1.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, h.tenantA); err != nil {
		t.Fatalf("tx1 set_config: %v", err)
	}
	if n := mustCount(t, tx1, `SELECT count(*) FROM tenants WHERE id = $1`, h.tenantA); n != 1 {
		t.Errorf("tx1 sees A = %d, want 1", n)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}

	// tx2: SAME connection, no context set. The LOCAL setting must be gone.
	tx2, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("tx2 begin: %v", err)
	}
	defer tx2.Rollback(ctx)
	if n := mustCount(t, tx2, `SELECT count(*) FROM tenants`); n != 0 {
		t.Errorf("tx2 (same conn, no context) sees %d rows, want 0 — tenant context bled across transactions", n)
	}
}

// The enumeration seam works and is scoped: invoice_tenant_reader sees ALL tenants
// (the tenant_enumerate policy's USING(true)) regardless of context, while invoice_app
// running the same query still sees only its current tenant — the `TO
// invoice_tenant_reader` scoping does not leak to the app role.
func TestRLS_EnumerationReaderScopedToReader(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.reader.Begin(ctx)
	if err != nil {
		t.Fatalf("reader begin: %v", err)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM tenants WHERE id = $1`, h.tenantA); n != 1 {
		t.Errorf("reader sees A = %d, want 1", n)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM tenants WHERE id = $1`, h.tenantB); n != 1 {
		t.Errorf("reader sees B = %d, want 1", n)
	}
	_ = tx.Rollback(ctx)

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM tenants WHERE id = $1`, h.tenantA); n != 1 {
			t.Errorf("app sees A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM tenants WHERE id = $1`, h.tenantB); n != 0 {
			t.Errorf("app sees B = %d, want 0 (enumerate policy leaked to app?)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("app enumerate check: %v", err)
	}
}
