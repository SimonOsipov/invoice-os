// M3-01-02 (task-25): RED tests for the `tenants.kind` discriminator, written BEFORE
// the migration exists. The Executor's migration will add:
//
//	ALTER TABLE tenants ADD COLUMN kind text NOT NULL DEFAULT 'firm'
//	    CHECK (kind IN ('firm','in_house'))
//
// Both cases here assert against the LIVE dev DB via the shared M2-07 harness
// (rls_harness_test.go) and self-skip when it is not configured (requireHarness).
// Pre-migration, `kind` does not exist, so every query that names it fails with
// SQLSTATE 42703 (undefined_column) — that is the expected RED. Once the Executor
// lands the migration these turn GREEN with no changes required here.
//
// Run: `make test-rls` runs everything prefixed TestRLS; these are not, so exercise
// them directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestTenantsKind ./internal/platform/db/...
package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgCode extracts the SQLSTATE from err, or "" if err does not wrap a *pgconn.PgError.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TEN-KIND-01: inserting a tenant without naming `kind` must default it to 'firm' —
// proves the DEFAULT is load-bearing, not merely present in the DDL. Uses the
// superuser pool (BYPASSRLS) purely to isolate this assertion from RLS; the DEFAULT
// itself is enforced by Postgres regardless of role. Cleans up its own probe row.
func TestTenantsKind_DefaultOnInsert(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	id := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'kind-default-probe')`, id,
	); err != nil {
		t.Fatalf("insert probe tenant (naming only id, name): %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id)
	}()

	var kind string
	err := h.super.QueryRow(ctx, `SELECT kind FROM tenants WHERE id = $1`, id).Scan(&kind)
	if err != nil {
		if code := pgCode(err); code == "42703" {
			t.Fatalf("SELECT kind: undefined_column (42703) — tenants.kind migration not applied yet: %v", err)
		}
		t.Fatalf("SELECT kind: %v", err)
	}
	if kind != "firm" {
		t.Errorf("kind on insert with no explicit value = %q, want %q (default not load-bearing)", kind, "firm")
	}
}

// TEN-KIND-02: pre-existing rows are backfilled to a non-NULL kind (the NOT NULL
// DEFAULT applied to existing rows, not just future inserts), and the CHECK
// constraint actually rejects a value outside ('firm','in_house').
func TestTenantsKind_Backfill(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// No row (existing or seeded by the harness) may have a NULL kind.
	nullCount, err := scanCount(ctx, h.super, `SELECT count(*) FROM tenants WHERE kind IS NULL`)
	if err != nil {
		if code := pgCode(err); code == "42703" {
			t.Fatalf("count kind IS NULL: undefined_column (42703) — tenants.kind migration not applied yet: %v", err)
		}
		t.Fatalf("count kind IS NULL: %v", err)
	}
	if nullCount != 0 {
		t.Errorf("tenants with NULL kind = %d, want 0 (backfill incomplete)", nullCount)
	}

	// At least one pre-existing row backfilled to 'firm' (the harness itself seeds
	// tenantA/tenantB via plain (id, name), so they exercise the backfill/default).
	firmCount, err := scanCount(ctx, h.super, `SELECT count(*) FROM tenants WHERE kind = 'firm'`)
	if err != nil {
		if code := pgCode(err); code == "42703" {
			t.Fatalf("count kind = 'firm': undefined_column (42703) — tenants.kind migration not applied yet: %v", err)
		}
		t.Fatalf("count kind = 'firm': %v", err)
	}
	if firmCount < 1 {
		t.Errorf("tenants with kind = 'firm' = %d, want >= 1", firmCount)
	}

	// The CHECK constraint rejects any value outside ('firm', 'in_house').
	id := uuid.NewString()
	_, err = h.super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'kind-check-probe', 'partnership')`, id,
	)
	if err == nil {
		// Should never happen once the migration lands, but clean up defensively
		// so a broken CHECK doesn't leave a poison row behind for other tests.
		_, _ = h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id)
		t.Fatal("insert with kind = 'partnership' succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code == "42703" {
		t.Fatalf("insert bad kind: undefined_column (42703) — tenants.kind migration not applied yet: %v", err)
	} else if code != "23514" {
		t.Fatalf("insert with kind = 'partnership': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
}

// scanCount runs a single-column count query. Distinct from mustCount (rls_harness_test.go)
// because these two callers need the raw error (to distinguish 42703 from other
// failures) rather than a t.Fatal-on-any-error helper.
func scanCount(ctx context.Context, q querier, sql string, args ...any) (int, error) {
	var n int
	err := q.QueryRow(ctx, sql, args...).Scan(&n)
	return n, err
}
