// M3-01-04 (task-27): tests for the `memberships` tenant-owned table, written BEFORE
// the migration exists (RED against SQLSTATE 42P01 undefined_table). The table the
// Executor will add:
//
//	memberships: id uuid PK DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL
//	    REFERENCES tenants(id) ON DELETE CASCADE, user_id uuid NOT NULL (the GoTrue
//	    subject — no FK, GoTrue is not this DB), role text NOT NULL REFERENCES
//	    roles(name), created_at timestamptz NOT NULL DEFAULT now(),
//	    UNIQUE (tenant_id, user_id) — FORCE RLS, policy `tenant_isolation` copied
//	    from the tenants/business_entities template (.claude/rules/db-migrations.md; no
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
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

	rowA, cleanupA := seedMembership(t, h.tenantA, uuid.NewString(), "admin")
	defer cleanupA()
	rowB, cleanupB := seedMembership(t, h.tenantB, uuid.NewString(), "admin")
	defer cleanupB()

	// Control: the zero below means nothing unless the rows are there to hide.
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE id IN ($1, $2)`, rowA, rowB); n != 2 {
		t.Fatalf("superuser sees %d of the two seeded rows, want 2", n)
	}

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE id IN ($1, $2)`, rowA, rowB); n != 0 {
		t.Errorf("seeded memberships rows visible with no tenant set = %d, want 0", n)
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

// AC-1: a status outside the vocabulary is refused with a check_violation, SQLSTATE 23514.
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
	return migrationVersion(t, "*_memberships_status_and_identity.sql")
}

// migrationVersion is the goose version of the one embedded migration matching glob.
func migrationVersion(t *testing.T, glob string) int64 {
	t.Helper()
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
// all_clients (client_access migration) is left out: it is not this migration's column.
func membershipsColumns(t *testing.T, ctx context.Context) []string {
	t.Helper()
	rows, err := h.super.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = 'memberships' AND column_name <> 'all_clients'
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
// re-applying Up restores them. Driven through goose (ApplyVersion on the real
// provider, reading migrations.FS) rather than a hand-copied ALTER TABLE, so this
// proves the shipped Down body itself, not a restatement of it. ApplyVersion targets
// this one version directly instead of scanning the apply-order ledger (DownTo/UpTo),
// so it is immune to another test in this package (e.g.
// TestDemoRepairMigrationAppliesThroughGoose's goose.WithAllowOutOfOrder) leaving
// goose_db_version's apply order out of step with version-number order.
func TestRLS_MembershipsDownDropsExactlyThreeColumns(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	version := membershipsMigrationVersion(t)

	migDSN := os.Getenv("DATABASE_MIGRATION_URL")
	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	// Registered before the restore cleanup below so LIFO runs restore first, close
	// last — a plain `defer` here would close the connection before t.Cleanup runs.
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}
	reapplyHookMigrationOnCleanup(t, provider)

	// Restore the column set regardless of how the assertions below turn out — later
	// tests in this package depend on status/display_name/email existing.
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore memberships schema after the Down/Up round-trip: %v", err)
		}
	})

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the memberships identity migration: %v", err)
	}

	wantDown := []string{"id", "tenant_id", "user_id", "role", "created_at"}
	if got := membershipsColumns(t, ctx); !reflect.DeepEqual(got, wantDown) {
		t.Fatalf("columns after Down = %v, want %v", got, wantDown)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the memberships identity migration: %v", err)
	}

	wantUp := []string{"id", "tenant_id", "user_id", "role", "created_at", "status", "display_name", "email"}
	if got := membershipsColumns(t, ctx); !reflect.DeepEqual(got, wantUp) {
		t.Fatalf("columns after re-applying Up = %v, want %v", got, wantUp)
	}
}

// AC-1 (QA-added): a row that existed BEFORE the migration ran backfills to 'active'
// via the column's own DEFAULT. TestRLS_MembershipsStatusDefaultsActive only proves a
// fresh insert takes the DEFAULT; this proves the migration's actual backfill claim by
// rolling the migration back, seeding a row while status/display_name/email don't
// exist, then re-applying Up and reading that same pre-existing row back.
func TestRLS_MembershipsPreMigrationRowBackfillsToActive(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	version := membershipsMigrationVersion(t)

	migDSN := os.Getenv("DATABASE_MIGRATION_URL")
	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}
	reapplyHookMigrationOnCleanup(t, provider)
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore memberships schema: %v", err)
		}
	})

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the memberships identity migration: %v", err)
	}

	id, cleanup := seedMembership(t, h.tenantA, uuid.NewString(), "admin")
	defer cleanup()

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the memberships identity migration: %v", err)
	}

	var status string
	var displayName, email *string
	if err := h.super.QueryRow(ctx,
		`SELECT status, display_name, email FROM memberships WHERE id = $1`, id,
	).Scan(&status, &displayName, &email); err != nil {
		t.Fatalf("read pre-migration row back after re-applying Up: %v", err)
	}
	if status != "active" {
		t.Errorf("pre-migration row status = %q, want %q", status, "active")
	}
	if displayName != nil {
		t.Errorf("pre-migration row display_name = %q, want NULL", *displayName)
	}
	if email != nil {
		t.Errorf("pre-migration row email = %q, want NULL", *email)
	}
}

// AC-2 (QA-added): cross-tenant SELECT is refused for the new identity columns too,
// not just status (TestRLS_MembershipsCrossTenantStatusUpdateInvisible covers the
// UPDATE case) — an app tx scoped to A cannot read B's display_name/email.
func TestRLS_MembershipsCrossTenantIdentityColumnsInvisible(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	idB, cleanupB := seedMembership(t, h.tenantB, uuid.NewString(), "admin")
	defer cleanupB()
	if _, err := h.super.Exec(ctx,
		`UPDATE memberships SET display_name = 'B Name', email = 'b@example.com' WHERE id = $1`, idB,
	); err != nil {
		t.Fatalf("seed B's identity columns: %v", err)
	}

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var displayName, email *string
		e := tx.QueryRow(ctx,
			`SELECT display_name, email FROM memberships WHERE id = $1`, idB,
		).Scan(&displayName, &email)
		if e == pgx.ErrNoRows {
			return nil // correct: B's row, and its identity columns, are invisible to A
		}
		if e != nil {
			return e
		}
		t.Errorf("A read B's identity columns: display_name=%v email=%v, want no row", displayName, email)
		return nil
	})
	if err != nil {
		t.Fatalf("cross-tenant identity-column SELECT: %v", err)
	}
}

// AC-1 (QA-added): status is NOT NULL — an explicit NULL is refused with
// not_null_violation, SQLSTATE 23502 (not silently coerced to the DEFAULT).
func TestRLS_MembershipsStatusExplicitNullRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', NULL)`,
			h.tenantA, uuid.NewString(),
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with status = NULL succeeded, want not_null_violation (SQLSTATE 23502)")
	}
	if code := pgCode(err); code != "23502" {
		t.Fatalf("insert with status = NULL: SQLSTATE = %q, want 23502 (not_null_violation): %v", code, err)
	}
}

// AC-1 (QA-added): the CHECK is an exact-string match — not case-insensitive, and
// empty string isn't a silent alias for any accepted value.
func TestRLS_MembershipsStatusVocabularyRejectsCaseAndEmpty(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, status := range []string{"", "Active", "ACTIVE", "invited "} {
		status := status
		t.Run("status="+status, func(t *testing.T) {
			err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx,
					`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', $3)`,
					h.tenantA, uuid.NewString(), status,
				)
				return e
			})
			if err == nil {
				t.Fatalf("insert with status = %q succeeded, want check_violation (SQLSTATE 23514)", status)
			}
			if code := pgCode(err); code != "23514" {
				t.Fatalf("insert with status = %q: SQLSTATE = %q, want 23514 (check_violation): %v", status, code, err)
			}
		})
	}
}

func staffMigrationProvider(t *testing.T) *goose.Provider {
	t.Helper()
	sqlDB, err := sql.Open("pgx", os.Getenv("DATABASE_MIGRATION_URL"))
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}
	return provider
}

// rollBackRemovedFirst undoes workspace_deletion_mails, deletion_purge_started, then workspace_deletion, then person_erasures, removed_flag_ignores_live_membership, then membership_removed, so an older migration's Down and Up see their own schema.
// The cleanup rolls them back again (a provider.Up in the test may have applied them early) and re-applies them last, in order,
// over the older bodies and the older CHECK that Up puts back, so they sit last in the goose ledger.
func rollBackRemovedFirst(t *testing.T, provider *goose.Provider) {
	t.Helper()
	removed := migrationVersion(t, "*_membership_removed.sql")
	flag := migrationVersion(t, "*_removed_flag_ignores_live_membership.sql")
	deletion := migrationVersion(t, "*_workspace_deletion.sql")
	purge := migrationVersion(t, "*_deletion_purge_started.sql")
	erasures := migrationVersion(t, "*_person_erasures.sql")
	mails := migrationVersion(t, "*_workspace_deletion_mails.sql")
	roll := func(version int64, up bool) error {
		_, err := provider.ApplyVersion(context.Background(), version, up)
		if errors.Is(err, goose.ErrNotApplied) || errors.Is(err, goose.ErrAlreadyApplied) {
			return nil
		}
		return err
	}
	for _, v := range []int64{mails, purge, deletion, erasures, flag, removed} {
		if err := roll(v, false); err != nil {
			t.Fatalf("roll back migration %d: %v", v, err)
		}
	}
	t.Cleanup(func() {
		for _, v := range []int64{mails, purge, deletion, erasures, flag, removed} {
			if err := roll(v, false); err != nil {
				t.Errorf("roll back migration %d: %v", v, err)
			}
		}
		for _, v := range []int64{removed, flag, erasures, deletion, purge, mails} {
			if err := roll(v, true); err != nil {
				t.Errorf("re-apply migration %d: %v", v, err)
			}
		}
		requirePurgeGuard(t)
	})
}

// restoreMigrationOnCleanup re-applies the migration at version after a test that rolled it back.
func restoreMigrationOnCleanup(t *testing.T, provider *goose.Provider, version int64) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := provider.ApplyVersion(context.Background(), version, true); err != nil && !errors.Is(err, goose.ErrAlreadyApplied) {
			t.Errorf("restore migration %d: %v", version, err)
		}
	})
}

// memberSnap is every column a refused or revived accept may touch.
type memberSnap struct {
	ID, Role, Status, CreatedAt string
	Email, Display              *string
	AllClients                  bool
}

// seedMemberRow inserts a full row 30 days old with all_clients on. Before the removed
// migration a 'removed' seed stops here with 23514 on memberships_status_check.
func seedMemberRow(t *testing.T, tenant, user, role, status string, display, email any) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO memberships (id, tenant_id, user_id, role, status, display_name, email, all_clients, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, true, now() - interval '30 days')`,
		id, tenant, user, role, status, display, email,
	); err != nil {
		t.Fatalf("seed a %q membership: %v", status, err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id) })
	return id
}

func readMemberSnap(t *testing.T, tenant, user string) memberSnap {
	t.Helper()
	var s memberSnap
	if err := h.super.QueryRow(context.Background(),
		`SELECT id::text, role, status, created_at::text, email, display_name, all_clients
		   FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenant, user,
	).Scan(&s.ID, &s.Role, &s.Status, &s.CreatedAt, &s.Email, &s.Display, &s.AllClients); err != nil {
		t.Fatalf("read the membership of %s in %s: %v", user, tenant, err)
	}
	return s
}

// callBool runs a one-argument boolean function and fails on any error, so a missing
// function surfaces as its 42883 text.
func callBool(t *testing.T, q querier, fn string, user string) bool {
	t.Helper()
	var got bool
	if err := q.QueryRow(context.Background(), `SELECT `+fn+`($1::uuid)`, user).Scan(&got); err != nil {
		t.Fatalf("%s(%s): %v", fn, user, err)
	}
	return got
}

// setStatusAsMigrator updates inside the tenant: the migrator owns the table but FORCE RLS still scopes it.
func setStatusAsMigrator(t *testing.T, tenant, id, status string) error {
	t.Helper()
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.mig, tenant, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE memberships SET status = $2 WHERE id = $1`, id, status)
		if err == nil && tag.RowsAffected() != 1 {
			t.Fatalf("set status %q touched %d rows, want 1", status, tag.RowsAffected())
		}
		return err
	})
}

func TestRLS_MembershipStatusAdmitsRemoved(t *testing.T) {
	h := requireHarness(t)
	id, cleanup := seedMembership(t, h.tenantA, uuid.NewString(), "admin")
	defer cleanup()

	if err := setStatusAsMigrator(t, h.tenantA, id, "removed"); err != nil {
		t.Fatalf("set status 'removed' as the migrator: %v", err)
	}

	var got string
	if err := h.super.QueryRow(context.Background(), `SELECT status FROM memberships WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("read status back: %v", err)
	}
	if got != "removed" {
		t.Errorf("status = %q, want removed", got)
	}
}

// The removed step first proves the constraint is the new vocabulary, not a blanket refusal.
func TestRLS_MembershipStatusRefusesAnUnknownValue(t *testing.T) {
	h := requireHarness(t)
	id, cleanup := seedMembership(t, h.tenantA, uuid.NewString(), "admin")
	defer cleanup()
	if err := setStatusAsMigrator(t, h.tenantA, id, "removed"); err != nil {
		t.Fatalf("control: set status 'removed': %v", err)
	}

	for _, bad := range []string{"ended", "Removed", "removed "} {
		err := setStatusAsMigrator(t, h.tenantA, id, bad)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "memberships_status_check" {
			t.Errorf("set status %q: want 23514 on memberships_status_check, got %v", bad, err)
		}
	}
	var got string
	if err := h.super.QueryRow(context.Background(), `SELECT status FROM memberships WHERE id = $1`, id).Scan(&got); err != nil || got != "removed" {
		t.Errorf("status after the refusals = %q (%v), want removed unchanged", got, err)
	}
}

func TestRLS_IdentityHasMembershipIgnoresRemovedRows(t *testing.T) {
	h := requireHarness(t)
	const fn = "public.identity_has_membership"

	for _, c := range []struct {
		status string
		want   bool
	}{{"removed", false}, {"active", true}, {"suspended", true}, {"invited", true}} {
		t.Run(c.status, func(t *testing.T) {
			user := uuid.NewString()
			seedMemberRow(t, h.tenantA, user, "preparer", c.status, nil, nil)
			if got := callBool(t, h.mig, fn, user); got != c.want {
				t.Errorf("identity_has_membership with only a %s row = %v, want %v", c.status, got, c.want)
			}
		})
	}

	t.Run("removed here, active there", func(t *testing.T) {
		user := uuid.NewString()
		seedMemberRow(t, h.tenantA, user, "preparer", "removed", nil, nil)
		seedMemberRow(t, h.tenantB, user, "preparer", "active", nil, nil)
		if !callBool(t, h.mig, fn, user) {
			t.Error("identity_has_membership = false, want true: the active row still counts")
		}
	})
	t.Run("no row", func(t *testing.T) {
		if callBool(t, h.mig, fn, uuid.NewString()) {
			t.Error("identity_has_membership = true for an unknown user, want false")
		}
	})
}

func TestRLS_IdentityHasRemovedMembership(t *testing.T) {
	h := requireHarness(t)
	const fn = "public.identity_has_removed_membership"

	t.Run("no row", func(t *testing.T) {
		if callBool(t, h.app, fn, uuid.NewString()) {
			t.Error("identity_has_removed_membership = true for an unknown user, want false")
		}
	})
	for _, c := range []struct {
		status string
		want   bool
	}{{"removed", true}, {"active", false}, {"suspended", false}, {"invited", false}} {
		t.Run(c.status, func(t *testing.T) {
			user := uuid.NewString()
			seedMemberRow(t, h.tenantA, user, "preparer", c.status, nil, nil)
			if got := callBool(t, h.app, fn, user); got != c.want {
				t.Errorf("identity_has_removed_membership with only a %s row = %v, want %v", c.status, got, c.want)
			}
		})
	}
	for _, live := range []string{"active", "suspended", "invited"} {
		t.Run("removed in one tenant, "+live+" in another", func(t *testing.T) {
			user := uuid.NewString()
			seedMemberRow(t, h.tenantA, user, "preparer", "removed", nil, nil)
			seedMemberRow(t, h.tenantB, user, "preparer", live, nil, nil)
			if callBool(t, h.app, fn, user) {
				t.Errorf("identity_has_removed_membership = true with a %s row elsewhere, want false", live)
			}
		})
	}
	// ceiling 1 of the migration: the answer ignores app.current_tenant, so it is the same under another tenant's GUC and an unset one.
	t.Run("the answer ignores the tenant GUC", func(t *testing.T) {
		user := uuid.NewString()
		seedMemberRow(t, h.tenantA, user, "preparer", "removed", nil, nil)
		for _, guc := range []string{h.tenantB, ""} {
			var got, other bool
			err := inAppTx(context.Background(), guc, func(tx pgx.Tx) error {
				if err := tx.QueryRow(context.Background(), `SELECT public.identity_has_removed_membership($1::uuid)`, user).Scan(&got); err != nil {
					return err
				}
				return tx.QueryRow(context.Background(), `SELECT public.identity_has_removed_membership($1::uuid)`, uuid.NewString()).Scan(&other)
			})
			if err != nil || !got || other {
				t.Errorf("GUC %q: (removed user, stranger) = (%v, %v) err %v, want (true, false)", guc, got, other, err)
			}
		}
	})
	t.Run("a NULL user id", func(t *testing.T) {
		var got *bool
		if err := h.app.QueryRow(context.Background(), `SELECT public.identity_has_removed_membership(NULL::uuid)`).Scan(&got); err != nil || got == nil || *got {
			t.Errorf("identity_has_removed_membership(NULL) = %v (%v), want false", got, err)
		}
	})
}

func TestRLS_IdentityHasRemovedMembershipIsNotPublic(t *testing.T) {
	h := requireHarness(t)
	const sig = "public.identity_has_removed_membership(uuid)"
	user := uuid.NewString()
	seedMemberRow(t, h.tenantA, user, "preparer", "removed", nil, nil)

	if !callBool(t, h.app, "public.identity_has_removed_membership", user) {
		t.Fatal("control: invoice_app got false for a removed user, want true")
	}

	var got bool
	err := h.reader.QueryRow(context.Background(), `SELECT public.identity_has_removed_membership($1::uuid)`, user).Scan(&got)
	if code := pgCode(err); code != "42501" {
		t.Errorf("invoice_tenant_reader call: SQLSTATE = %q (%v), want 42501", code, err)
	}
	for _, c := range []struct {
		role string
		want bool
	}{{"invoice_app", true}, {"invoice_tenant_reader", false}, {"supabase_auth_admin", false}} {
		var has bool
		if err := h.super.QueryRow(context.Background(),
			`SELECT has_function_privilege($1, to_regprocedure($2), 'EXECUTE')`, c.role, sig).Scan(&has); err != nil {
			t.Fatalf("has_function_privilege(%s): %v", c.role, err)
		}
		if has != c.want {
			t.Errorf("%s EXECUTE on %s = %v, want %v", c.role, sig, has, c.want)
		}
	}
	s, ok := readProc(t, sig)
	if !ok {
		t.Fatalf("function %s does not exist", sig)
	}
	assertExecuteOnlyForApp(t, sig, s, "auth_hook_reader")
	if s.owner != "auth_hook_reader" || !s.secdef || s.volatile != "s" || !reflect.DeepEqual(s.config, []string{`search_path=""`}) {
		t.Errorf("(owner, secdef, volatile, config) = (%s, %v, %s, %v), want (auth_hook_reader, true, s, [search_path=\"\"])", s.owner, s.secdef, s.volatile, s.config)
	}
}

// Down puts back the any-removed-row answer with the same owner, SECURITY DEFINER, search_path and invoice_app-only grant; Up turns the live-row rule on again.
func TestRLS_RemovedFlagMigrationDownAndUpKeepTheFunctionShape(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	const sig = "public.identity_has_removed_membership(uuid)"
	provider := staffMigrationProvider(t)
	flag := migrationVersion(t, "*_removed_flag_ignores_live_membership.sql")
	restoreMigrationOnCleanup(t, provider, flag)

	user := uuid.NewString()
	seedMemberRow(t, h.tenantA, user, "preparer", "removed", nil, nil)
	seedMemberRow(t, h.tenantB, user, "preparer", "active", nil, nil)

	assertShape := func(phase string) {
		t.Helper()
		s, ok := readProc(t, sig)
		if !ok {
			t.Fatalf("%s: %s does not exist", phase, sig)
		}
		assertExecuteOnlyForApp(t, sig, s, "auth_hook_reader")
		if s.owner != "auth_hook_reader" || !s.secdef || s.volatile != "s" || !reflect.DeepEqual(s.config, []string{`search_path=""`}) {
			t.Errorf("%s: (owner, secdef, volatile, config) = (%s, %v, %s, %v), want (auth_hook_reader, true, s, [search_path=\"\"])", phase, s.owner, s.secdef, s.volatile, s.config)
		}
	}

	if _, err := provider.ApplyVersion(ctx, flag, false); err != nil && !errors.Is(err, goose.ErrNotApplied) {
		t.Fatalf("Down of the flag migration: %v", err)
	}
	assertShape("after Down")
	if !callBool(t, h.app, "public.identity_has_removed_membership", user) {
		t.Error("after Down: removed here, active there = false, want true (the membership_removed answer)")
	}
	if _, err := provider.ApplyVersion(ctx, flag, true); err != nil && !errors.Is(err, goose.ErrAlreadyApplied) {
		t.Fatalf("Up of the flag migration: %v", err)
	}
	assertShape("after Up")
	if callBool(t, h.app, "public.identity_has_removed_membership", user) {
		t.Error("after Up: removed here, active there = true, want false")
	}
}

// Down runs under FORCE RLS: it must reach removed rows in every tenant, then put FORCE back.
func TestRLS_MembershipRemovedDownTurnsRemovedRowsSuspended(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	provider := staffMigrationProvider(t)
	version := migrationVersion(t, "*_membership_removed.sql")

	removedA, removedB, kept := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedMemberRow(t, h.tenantA, removedA, "preparer", "removed", "Ada", nil)
	seedMemberRow(t, h.tenantB, removedB, "preparer", "removed", nil, nil)
	seedMemberRow(t, h.tenantA, kept, "preparer", "active", "Bola", "bola@obi.test")

	// The flag and deletion migrations redefine functions this Down drops: undo them first, restore them last.
	deletion := migrationVersion(t, "*_workspace_deletion.sql")
	purge := migrationVersion(t, "*_deletion_purge_started.sql")
	flag := migrationVersion(t, "*_removed_flag_ignores_live_membership.sql")
	t.Cleanup(func() { requirePurgeGuard(t) })
	restoreMigrationOnCleanup(t, provider, purge)
	restoreMigrationOnCleanup(t, provider, deletion)
	restoreMigrationOnCleanup(t, provider, flag)
	if _, err := provider.ApplyVersion(ctx, purge, false); err != nil && !errors.Is(err, goose.ErrNotApplied) {
		t.Fatalf("Down of the purge-started migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, deletion, false); err != nil && !errors.Is(err, goose.ErrNotApplied) {
		t.Fatalf("Down of the deletion migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, flag, false); err != nil && !errors.Is(err, goose.ErrNotApplied) {
		t.Fatalf("Down of the flag migration: %v", err)
	}
	t.Cleanup(func() {
		if _, err := provider.ApplyVersion(context.Background(), version, true); err != nil && !errors.Is(err, goose.ErrAlreadyApplied) {
			t.Errorf("re-apply membership_removed: %v", err)
		}
	})
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("Down with removed rows present: %v", err)
	}

	for _, c := range []struct{ tenant, user, status string }{{h.tenantA, removedA, "suspended"}, {h.tenantB, removedB, "suspended"}, {h.tenantA, kept, "active"}} {
		if s := readMemberSnap(t, c.tenant, c.user); s.Status != c.status {
			t.Errorf("status after Down = %q, want %q", s.Status, c.status)
		}
	}
	if s := readMemberSnap(t, h.tenantA, removedA); s.Display == nil || *s.Display != "Ada" {
		t.Errorf("display_name after Down = %v, want Ada kept", s.Display)
	}
	var forced bool
	if err := h.super.QueryRow(ctx, `SELECT relforcerowsecurity FROM pg_class WHERE oid = 'public.memberships'::regclass`).Scan(&forced); err != nil || !forced {
		t.Errorf("memberships FORCE ROW LEVEL SECURITY after Down = %v (%v), want true", forced, err)
	}
	if err := setStatusAsMigrator(t, h.tenantA, readMemberSnap(t, h.tenantA, kept).ID, "removed"); pgCode(err) != "23514" {
		t.Errorf("status 'removed' after Down: SQLSTATE %q (%v), want 23514", pgCode(err), err)
	}
	for _, sig := range []string{
		"public.identity_has_removed_membership(uuid)",
		"public.accept_invitation(uuid, text, uuid, text, text)",
		"public.accept_invitation_by_id(uuid, uuid, uuid, text, text)",
	} {
		if _, ok := readProc(t, sig); ok {
			t.Errorf("%s still exists after Down", sig)
		}
	}
	for _, sig := range []string{"public.accept_invitation(uuid, text, uuid, text)", "public.accept_invitation_by_id(uuid, uuid, uuid, text)"} {
		if _, ok := readProc(t, sig); !ok {
			t.Errorf("%s is gone after Down, want the old form restored", sig)
		}
	}
}
