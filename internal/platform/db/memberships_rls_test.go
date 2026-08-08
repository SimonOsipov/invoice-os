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
	"database/sql"
	"io/fs"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
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

// AC-1: a row inserted with no status column named backfills to 'active' via the
// column's own DEFAULT.
func TestRLS_MembershipsStatusDefaultsActive(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	id, cleanup := seedMembership(t, h.tenantA, uuid.NewString(), "admin")
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var status string
		if e := tx.QueryRow(ctx, `SELECT status FROM memberships WHERE id = $1`, id).Scan(&status); e != nil {
			return e
		}
		if status != "active" {
			t.Errorf("status = %q, want %q", status, "active")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read status back: %v", err)
	}
}

// AC-1: status accepts exactly active/invited/suspended; anything else is refused
// with a check_violation, SQLSTATE 23514.
func TestRLS_MembershipsStatusVocabulary(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, status := range []string{"active", "invited", "suspended"} {
		t.Run(status, func(t *testing.T) {
			var id string
			err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx,
					`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', $3) RETURNING id`,
					h.tenantA, uuid.NewString(), status,
				).Scan(&id)
			})
			if err != nil {
				t.Fatalf("insert with status = %q: %v", status, err)
			}
			defer func() {
				_, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id)
			}()
		})
	}

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'deleted')`,
			h.tenantA, uuid.NewString(),
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with status = 'deleted' succeeded, want check_violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with status = 'deleted': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
}

// AC-1: display_name/email default to NULL, not empty string, when not named on
// insert.
func TestRLS_MembershipsIdentityColumnsDefaultNull(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	id, cleanup := seedMembership(t, h.tenantA, uuid.NewString(), "admin")
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var displayName, email *string
		if e := tx.QueryRow(ctx,
			`SELECT display_name, email FROM memberships WHERE id = $1`, id,
		).Scan(&displayName, &email); e != nil {
			return e
		}
		if displayName != nil {
			t.Errorf("display_name = %q, want NULL", *displayName)
		}
		if email != nil {
			t.Errorf("email = %q, want NULL", *email)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read identity columns back: %v", err)
	}
}

// AC-2: status is subject to the same tenant_isolation policy as the rest of the
// row — an app tx scoped to A cannot UPDATE B's status, and B's row stays 'active'.
func TestRLS_MembershipsCrossTenantStatusUpdateInvisible(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	idB, cleanupB := seedMembership(t, h.tenantB, uuid.NewString(), "admin")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1`, h.tenantB)
		if e != nil {
			return e
		}
		if n := tag.RowsAffected(); n != 0 {
			t.Errorf("rows affected by cross-tenant status UPDATE = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cross-tenant status UPDATE: %v", err)
	}

	var status string
	if e := h.super.QueryRow(ctx, `SELECT status FROM memberships WHERE id = $1`, idB).Scan(&status); e != nil {
		t.Fatalf("read back B's status as superuser: %v", e)
	}
	if status != "active" {
		t.Errorf("B's status after the refused cross-tenant UPDATE = %q, want %q", status, "active")
	}
}

// membershipsMigrationVersion is the memberships-identity migration's goose version
// id, taken from the embedded filename rather than hardcoded.
func membershipsMigrationVersion(t *testing.T) int64 {
	t.Helper()
	const glob = "*_memberships_status_and_identity.sql"
	matches, err := fs.Glob(migrations.FS, glob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", glob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1", len(matches), glob, matches)
	}
	v, err := strconv.ParseInt(strings.SplitN(matches[0], "_", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("parse goose version out of %q: %v", matches[0], err)
	}
	return v
}

// membershipsColumns reads memberships' current column set, ordinal-position order.
func membershipsColumns(t *testing.T, ctx context.Context) []string {
	t.Helper()
	rows, err := h.super.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = 'memberships'
		 ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("query memberships columns: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate memberships columns: %v", err)
	}
	return out
}

// AC-3: the migration's own Down drops exactly status/display_name/email, and
// re-applying Up restores them. Driven through goose (DownTo/UpTo on the real
// provider, reading migrations.FS) rather than a hand-copied ALTER TABLE, so this
// proves the shipped Down body itself, not a restatement of it. The migration is
// the newest in the embedded set, so DownTo(version-1) rolls back only this one.
func TestRLS_MembershipsDownDropsExactlyThreeColumns(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	version := membershipsMigrationVersion(t)

	migDSN := os.Getenv("DATABASE_MIGRATION_URL")
	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	defer sqlDB.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}

	// Restore the column set regardless of how the assertions below turn out — later
	// tests in this package depend on status/display_name/email existing.
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore memberships schema after the Down/Up round-trip: %v", err)
		}
	})

	if _, err := provider.DownTo(ctx, version-1); err != nil {
		t.Fatalf("roll back the memberships identity migration: %v", err)
	}

	wantDown := []string{"id", "tenant_id", "user_id", "role", "created_at"}
	if got := membershipsColumns(t, ctx); !reflect.DeepEqual(got, wantDown) {
		t.Fatalf("columns after Down = %v, want %v", got, wantDown)
	}

	if _, err := provider.UpTo(ctx, version); err != nil {
		t.Fatalf("re-apply the memberships identity migration: %v", err)
	}

	wantUp := []string{"id", "tenant_id", "user_id", "role", "created_at", "status", "display_name", "email"}
	if got := membershipsColumns(t, ctx); !reflect.DeepEqual(got, wantUp) {
		t.Fatalf("columns after re-applying Up = %v, want %v", got, wantUp)
	}
}
