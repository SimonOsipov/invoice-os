// ratelimit_db_test.go: the DB-BACKED half of M5-04-04's (task-232) spec. Authored BEFORE
// internal/submission/ratelimit.go's real RateLimitFor body exists (RALPH Stage 2.5, Mode
// A) -- the stub makes this compile and fail on each case's target assertion, never on a
// compile error, a connection failure, a fixture failure, or a skip.
//
// Reuses this package's shared TestMain fixture (failure_modes_test.go:57, requireExchangeDB
// / exChain, exchange_db_test.go) rather than declaring a second seedTenant/seedEntity pair
// -- submission_rate_limits FKs only to tenants, and exChain already seeds one; the unused
// invoiceID/jobID it also returns are simply discarded.
//
// SEEDING ROLE. invoice_app holds SELECT ONLY on submission_rate_limits (M5-04-01's
// least-privilege grant, migrations/20260723133200_submission_dead_letter_and_rate_limits.sql
// :95), so the fixture row is seeded as the MIGRATOR -- but FORCE ROW LEVEL SECURITY still
// binds the migrator/owner to the tenant_isolation predicate, so the seed still runs inside
// db.WithinTenantTx, exactly like exChain's own seed. RateLimitFor itself is called against
// the APP pool (f.app), matching the real caller (SubmitWorker runs as invoice_app).
//
// Spec-to-test map (task-232's Test Specs table):
//
//	T04-6 TestRateLimitFor_ReturnsConfiguredRowWhenOneExists
//	T04-7 TestRateLimitFor_ReturnsDefaultWhenNoRowExists
//	T04-8 TestRLS_RateLimitForIsTenantScoped
//
// Local run: `DEV_DB_PORT=5433 make test-queue` from this worktree, or export DATABASE_URL /
// DATABASE_MIGRATION_URL and run `go test ./internal/submission/... -run 'RateLimitFor|RLS_RateLimitFor'`.
package submission_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// rlSeedLimit inserts one submission_rate_limits row for tenantID as the migrator, inside a
// tenant-scoped tx (FORCE RLS binds the owner too).
func rlSeedLimit(ctx context.Context, t *testing.T, f *effectsFixture, tenantID string, maxPerMinute int) {
	t.Helper()
	err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, $2)`,
			tenantID, maxPerMinute)
		return e
	})
	if err != nil {
		t.Fatalf("seed submission_rate_limits (tenant=%s, max=%d): %v", tenantID, maxPerMinute, err)
	}
}

// T04-6: with a seeded max_per_minute=5, RateLimitFor(tx, 60) returns 5 -- the configured
// row wins over the caller's def.
func TestRateLimitFor_ReturnsConfiguredRowWhenOneExists(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, _, cleanup := exChain(t, f)
	defer cleanup()

	rlSeedLimit(ctx, t, f, tenantID, 5)

	var got int
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		var e error
		got, e = submission.RateLimitFor(ctx, tx, 60)
		return e
	})
	if err != nil {
		t.Fatalf("RateLimitFor with a seeded max_per_minute=5 row: %v", err)
	}
	if got != 5 {
		t.Errorf("RateLimitFor with max_per_minute=5 seeded = %d, want 5 (the configured row, not the def)", got)
	}
}

// T04-7: with no row, RateLimitFor(tx, 60) returns (60, nil) -- NOT pgx.ErrNoRows. An
// unconfigured firm is the expected common case (self-service configuration is M7-04), not
// an error condition.
func TestRateLimitFor_ReturnsDefaultWhenNoRowExists(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, _, cleanup := exChain(t, f)
	defer cleanup()

	var got int
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		var e error
		got, e = submission.RateLimitFor(ctx, tx, 60)
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RateLimitFor with no row returned pgx.ErrNoRows -- an unconfigured firm is the "+
			"expected common case and must fall back to def, not surface as an error: %v", err)
	}
	if err != nil {
		t.Fatalf("RateLimitFor with no row: %v", err)
	}
	if got != 60 {
		t.Errorf("RateLimitFor with no row = %d, want 60 (the caller's def)", got)
	}
}

// T04-8: tenant B's tx returns the default even though tenant A has a row -- proving the RLS
// + app.current_tenant GUC scoping, not an application-level WHERE tenant_id = $1 filter.
// RateLimitFor(ctx, tx, def) takes no tenant parameter, so there is no value to bind such a
// filter to in the first place; an app-level filter would also make this test pass even with
// RLS disabled, defeating the point of an RLS-prefixed name.
func TestRLS_RateLimitForIsTenantScoped(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantA, _, _, cleanupA := exChain(t, f)
	defer cleanupA()
	tenantB, _, _, cleanupB := exChain(t, f)
	defer cleanupB()

	rlSeedLimit(ctx, t, f, tenantA, 5)

	var got int
	err := db.WithinTenantTx(ctx, f.app, tenantB, func(tx pgx.Tx) error {
		var e error
		got, e = submission.RateLimitFor(ctx, tx, 60)
		return e
	})
	if err != nil {
		t.Fatalf("RateLimitFor under tenant B's tx: %v", err)
	}
	if got != 60 {
		t.Errorf("RateLimitFor under tenant B's tx = %d, want 60 (the default) -- tenant A's "+
			"max_per_minute=5 row must not be visible across tenants", got)
	}
}
