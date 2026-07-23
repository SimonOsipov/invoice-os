// M5-04-01 (task-235): tests for `submission_rate_limits`, the per-tenant rate-limit
// ceiling table M5-04-04's in-memory RateLimiter reads from (falling back to an env-var
// default when no row exists for a tenant). Written BEFORE the migration exists — every
// case here is RED against SQLSTATE 42P01 undefined_table. The table the Executor will add
// (task-235 Implementation Plan, "Step 2 — Up/Down"):
//
//	submission_rate_limits: tenant_id uuid PRIMARY KEY REFERENCES tenants(id)
//	    ON DELETE CASCADE, max_per_minute int NOT NULL CHECK (max_per_minute > 0),
//	    created_at timestamptz NOT NULL DEFAULT now();
//	    ENABLE + FORCE ROW LEVEL SECURITY, a `tenant_isolation` policy carrying no TO
//	    clause (applies to every role, docs/migrations.md §4's verbatim template), and
//	    GRANT SELECT ON submission_rate_limits TO invoice_app — no INSERT/UPDATE/DELETE.
//
// Three things about this table differ from its M5-01 siblings and shape the cases below:
//
//   - It is READ-ONLY for the app role (Decision [rate-limit-table-is-read-only-in-M5-04]):
//     no writer ships in M5-04, so an operator or a test's superuser pool seeds it until
//     M7-04 ships a cockpit. T01-5 is the grant-layer half of that claim; T01-4 is the RLS
//     half. Unlike submission_jobs/app_exchange (SELECT+INSERT[+UPDATE]), there is no
//     positive INSERT case for invoice_app anywhere in this file — there deliberately is
//     none to write.
//   - `tenant_id` IS the PRIMARY KEY, not a separate id column with a composite unique
//     constraint alongside it (unlike submission_jobs' three-column
//     submission_jobs_tenant_id_invoice_uq). One row per tenant, keyed directly — closest
//     shipped shape is `tenants` itself (id is both PK and the isolation column).
//   - FORCE is the entire point of T01-8. The table owner (invoice_migrator) would
//     otherwise bypass RLS on reads exactly as any owner does; T01-4's h.app case cannot
//     stand in for this because invoice_app never had BYPASSRLS to begin with. Mirrors
//     TestRLS_SubmissionJobsOwnerInsertRefusedUnderForce (submission_jobs_rls_test.go:328)
//     for the write side and TestRLS_OwnerCannotBypassSelectUnderForce (rls_test.go:147)
//     for the read side, but neither of those seeds BOTH tenants and asserts a SCOPED count
//     of 1 — the shape that actually distinguishes "owner sees nothing without context"
//     from "owner sees only its own tenant's row WITH context set", which is what FORCE
//     must additionally prove for a table whose whole justification (M5-04-04's limiter
//     reading a per-tenant ceiling) depends on that scoping being real.
//
// T01-1/T01-2/T01-3 (the `submission_jobs.state` CHECK widening to include `dead_lettered`)
// live in submission_jobs_rls_test.go, extending TestRLS_SubmissionJobsStateCheck — NOT
// here, since that is a different table's existing suite. T01-7 (reversibility) is a
// deliberate architect scope call, not a silent gap: this repo's Down-reassert convention
// (TestASI05_DownRemovesConstraint et al., internal/validation) needs a TestRLS_ prefix to
// be -run-reachable at all in this package, and AC-6 is satisfied instead by the CI
// `migrations` job's generic reset/up round-trip plus a manual local check — see task-235's
// CORRECTION section. No bespoke Go test for T01-7 is added in this file.
//
// Spec-to-test map (Test Specs table, task-235):
//
//	T01-4 TestRLS_SubmissionRateLimitsCrossTenantInvisible
//	T01-5 TestRLS_SubmissionRateLimitsAppInsertRefused
//	T01-6 TestRLS_SubmissionRateLimitsMaxPerMinutePositive
//	T01-8 TestRLS_SubmissionRateLimitsOwnerSelectScopedUnderForce (ADDITIONAL GAP, closes
//	      AC-4's FORCE claim, modeled on submission_jobs' SJ-04)
//
// Every negative assertion is paired with a positive half or a mutation-verify re-read as
// the superuser, so no case can pass against a table that simply refuses everything or is
// empty. Each rejected statement gets its own transaction (an explicit WithinTenantTx, or a
// single implicit-transaction Exec on a pool) — a failed statement poisons the surrounding
// transaction (idempotency_keys_rls_test.go:445-446).
//
// Rows are seeded per-test (seedSubmissionRateLimit below), NOT in the shared
// harness.seed() in rls_harness_test.go — that runs in TestMain before every test in the
// package, so a missing submission_rate_limits table would break the ENTIRE suite instead
// of failing only these cases. Cleanup defers are registered BEFORE any assertion that can
// t.Fatalf, so the failure path leaks nothing into sibling cases.
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS
// ./internal/platform/db/...` (.github/workflows/ci.yml) and `make test-rls` both pick these
// up with no workflow edit. Every case calls requireHarness(t), which SKIPS when the
// per-role DATABASE_* URLs are unset so a bare `go test ./...` stays green with no DB — note
// that under the CI gate (scripts/ci/rls-test-gate.sh) a SKIP is itself a failure, so no case
// here may add a t.Skip of its own.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_SubmissionRateLimits ./internal/platform/db/...
//
// (A worktree running the compose DB on an alternate host port must substitute it in all
// four DSNs — e.g. `DEV_DB_PORT=5433 make test-rls`, since Makefile:32 defaults to 5432.)
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// failIfUndefinedSubmissionRateLimits turns the pre-migration failure mode into an
// explicit, self-explaining message instead of a raw driver error, following the
// tenants_kind_test.go (:63-66) / submission_jobs_rls_test.go (:140-145) precedent. Returns
// true when it fired.
func failIfUndefinedSubmissionRateLimits(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the submission_rate_limits migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedSubmissionRateLimit inserts one submission_rate_limits row for tenantID as the
// superuser (BYPASSRLS, so seeding needs neither tenant context nor an INSERT grant — the
// app role holds none here) and returns a cleanup func. tenant_id is the PK, so at most one
// row per tenant may exist at a time; every case that seeds one cleans it up before the
// next case runs.
func seedSubmissionRateLimit(t *testing.T, tenantID string, maxPerMinute int) (cleanup func()) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, $2)`,
		tenantID, maxPerMinute,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed submission_rate_limits (tenant %s): undefined_table (42P01) — "+
				"submission_rate_limits migration not applied yet: %v", tenantID, err)
		}
		t.Fatalf("seed submission_rate_limits (tenant %s): %v", tenantID, err)
	}
	return func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_rate_limits WHERE tenant_id = $1`, tenantID)
	}
}

// T01-4: submission_rate_limits is tenant-isolated. A row seeded under tenant A via the
// superuser (BYPASSRLS) is invisible to tenant B's app-role connection — filtered out, not
// an error. The positive half (the SAME row visible to tenant A) is what stops this case
// passing against a table that refuses every read.
func TestRLS_SubmissionRateLimitsCrossTenantInvisible(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cleanupA := seedSubmissionRateLimit(t, h.tenantA, 5)
	defer cleanupA()

	var crossCount int
	err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		n, e := scanCount(ctx, tx, `SELECT count(*) FROM submission_rate_limits`)
		if e != nil {
			return e
		}
		crossCount = n
		return nil
	})
	if failIfUndefinedSubmissionRateLimits(t, "tenant B SELECT of tenant A's row", err) {
		return
	}
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant B): %v", err)
	}
	if crossCount != 0 {
		t.Errorf("tenant A's row visible to tenant B = %d, want 0", crossCount)
	}

	// Positive half, own tx: tenant A's own row IS visible to tenant A — the zero above is
	// isolation, not a blanket-empty or blanket-inaccessible table.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx,
			`SELECT count(*) FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("tenant A's own row visible to tenant A = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx (tenant A, positive half): %v", err)
	}
}

// T01-5: the table is read-only for the app role (Decision
// [rate-limit-table-is-read-only-in-M5-04] — no writer ships in M5-04; an operator or a
// test's superuser pool seeds it until M7-04 ships a cockpit). A same-tenant INSERT is
// refused at the GRANT layer, SQLSTATE 42501, before RLS's USING clause is even reached —
// this is the grant half of read-only; T01-4 is the RLS half.
func TestRLS_SubmissionRateLimitsAppInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, $2)`,
			h.tenantA, 10,
		)
		return e
	})
	if failIfUndefinedSubmissionRateLimits(t, "app-role INSERT", err) {
		return
	}
	if err == nil {
		// Should never happen once the migration lands, but clean up defensively so a
		// missing grant doesn't leave a poison row behind for other tests.
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA)
		t.Fatal("app-role INSERT into submission_rate_limits succeeded, want permission denied " +
			"(SQLSTATE 42501) — invoice_app holds SELECT only, no writer ships in M5-04")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role INSERT on submission_rate_limits: SQLSTATE = %q, want 42501 (insufficient_privilege): %v",
			code, err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA); n != 0 {
		t.Errorf("rows after the refused app-role INSERT = %d, want 0", n)
	}
}

// T01-6: max_per_minute cannot be zero or negative — a limiter reading a non-positive
// ceiling would either block every submission (0) or be nonsensical (negative). Superuser
// inserts of 0 and -1 each raise 23514, table-driven, each its own statement so one
// unexpected acceptance cannot poison the other. The positive half — an ordinary positive
// value inserting and reading back — proves the CHECK bounds the column from below without
// pinning it to a single accepted value.
func TestRLS_SubmissionRateLimitsMaxPerMinutePositive(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, bad := range []int{0, -1} {
		_, err := h.super.Exec(ctx,
			`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, $2)`,
			h.tenantA, bad,
		)
		if failIfUndefinedSubmissionRateLimits(t, "superuser INSERT with a non-positive max_per_minute", err) {
			return
		}
		if err == nil {
			// Not reachable on a correct schema, but if the CHECK were dropped the row
			// would commit — remove it so the failure does not leak into later cases.
			_, _ = h.super.Exec(context.Background(),
				`DELETE FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA)
			t.Errorf("INSERT with max_per_minute = %d succeeded, want CHECK violation (SQLSTATE 23514)", bad)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with max_per_minute = %d: SQLSTATE = %q, want 23514 (check_violation): %v",
				bad, code, err)
		}
	}

	// Positive half: an ordinary positive value inserts and reads back.
	cleanup := seedSubmissionRateLimit(t, h.tenantA, 30)
	defer cleanup()
	var got int
	if err := h.super.QueryRow(ctx,
		`SELECT max_per_minute FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA,
	).Scan(&got); err != nil {
		t.Fatalf("read back max_per_minute: %v", err)
	}
	if got != 30 {
		t.Errorf("max_per_minute round-trip = %d, want 30", got)
	}
}

// T01-8 (ADDITIONAL GAP, closes AC-4's FORCE claim): FORCE actually subjects the table
// OWNER (invoice_migrator) to tenant_isolation on reads, not just ENABLE. One row each for
// tenant A and tenant B is seeded via the superuser (BYPASSRLS); as the owner, with tenant
// A's GUC set via WithinTenantTx, SELECT count(*) must be 1 — not 2. Without FORCE the
// owner would bypass RLS entirely and see both rows (a silent RLS bypass that every other
// case here is powerless to catch: T01-4/5/6 exercise h.app, which was never going to
// bypass RLS either way since it does not own the table). Mirrors
// TestRLS_SubmissionJobsOwnerInsertRefusedUnderForce (submission_jobs_rls_test.go:328) for
// the write side.
func TestRLS_SubmissionRateLimitsOwnerSelectScopedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cleanupA := seedSubmissionRateLimit(t, h.tenantA, 15)
	defer cleanupA()
	cleanupB := seedSubmissionRateLimit(t, h.tenantB, 20)
	defer cleanupB()

	var n int
	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		got, e := scanCount(ctx, tx, `SELECT count(*) FROM submission_rate_limits`)
		if e != nil {
			return e
		}
		n = got
		return nil
	})
	if failIfUndefinedSubmissionRateLimits(t, "owner SELECT scoped to tenant A", err) {
		return
	}
	if err != nil {
		t.Fatalf("WithinTenantTx (owner, tenant A): %v", err)
	}
	if n != 1 {
		t.Errorf("owner's visible row count scoped to tenant A = %d, want 1 (both tenants have a row; "+
			"without FORCE the owner would see both = 2, a silent RLS bypass)", n)
	}
}

// ---- QA adversarial coverage (task-235, RALPH Phase 1 Stage 4 Mode B) ----
//
// Five cases the architect's Test Specs (T01-4/5/6/8 above) do not cover, each
// proving a distinct fact: FK cascade (AC-5's "ON DELETE CASCADE" half, never
// exercised — T01-4/8 only ever DELETE rows, never a tenant), the max_per_minute
// lower boundary's off-by-one (T01-6 only probes the illegal side, 0 and -1; never
// confirms 1 — the first LEGAL value — is actually accepted), a cross-tenant WRITE
// refused via the policy's implicit WITH CHECK rather than the grant layer (T01-5
// tests invoice_app, which never had INSERT to begin with, so it cannot distinguish
// grant-refusal from RLS-refusal), fail-closed behavior with the GUC entirely UNSET
// (T01-4 only tests a MISMATCHED tenant, never an absent one), and the PK's
// uniqueness (no case above ever attempts a second row for an existing tenant).

// QA-1: deleting a tenant cascades to its submission_rate_limits row (AC-5, "ON
// DELETE CASCADE"). Uses a throwaway tenant — not h.tenantA/h.tenantB, which every
// other case in this package shares — so the DELETE cannot disturb sibling tests.
func TestRLS_SubmissionRateLimitsTenantDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'QA-1 throwaway tenant')`, tenantID,
	); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	defer func() {
		// Idempotent: a no-op once the DELETE below runs; guards only the path where
		// an earlier assertion fails and returns before that point.
		_, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	}()

	if _, err := h.super.Exec(ctx,
		`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, $2)`,
		tenantID, 5,
	); err != nil {
		if failIfUndefinedSubmissionRateLimits(t, "seed rate-limit row for cascade probe", err) {
			return
		}
		t.Fatalf("seed rate-limit row for cascade probe: %v", err)
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("DELETE tenant: %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM submission_rate_limits WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("submission_rate_limits rows after deleting the owning tenant = %d, want 0 (ON DELETE CASCADE)", n)
	}
}

// QA-2: max_per_minute's lower boundary is exactly 1, not 2 — the CHECK is
// `max_per_minute > 0`, so 1 is the FIRST legal value. T01-6 only probes the
// illegal side (0 and -1); an accidental `> 1` off-by-one would silently reject
// this boundary with no other case here catching it.
func TestRLS_SubmissionRateLimitsMaxPerMinuteBoundaryOneAccepted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cleanup := seedSubmissionRateLimit(t, h.tenantA, 1)
	defer cleanup()

	var got int
	if err := h.super.QueryRow(ctx,
		`SELECT max_per_minute FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA,
	).Scan(&got); err != nil {
		t.Fatalf("read back max_per_minute: %v", err)
	}
	if got != 1 {
		t.Errorf("max_per_minute round-trip = %d, want 1 (the CHECK's first legal value)", got)
	}
}

// QA-3: a cross-tenant WRITE — not merely a read — is refused via the policy's
// implicit WITH CHECK, not the grant layer. Uses h.mig (invoice_migrator, the
// table owner, which already holds every DDL/DML privilege) so the refusal can
// only come from RLS itself; T01-5 tests invoice_app, which never had INSERT to
// begin with, so it cannot isolate this path. With tenant A's GUC set, an INSERT
// naming tenant B's id violates WITH CHECK — mirrors
// TestRLS_SubmissionJobsOwnerInsertRefusedUnderForce (submission_jobs_rls_test.go).
func TestRLS_SubmissionRateLimitsOwnerCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, $2)`,
			h.tenantB, 10,
		)
		return e
	})
	if failIfUndefinedSubmissionRateLimits(t, "owner cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantB); n != 0 {
		t.Errorf("rows after the owner's refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half: the owner CAN write within its own tenant scope — the refusal
	// above is cross-tenant isolation, not a blanket ban on the owner writing at all.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, $2)`,
			h.tenantA, 12,
		)
		return e
	}); err != nil {
		t.Fatalf("owner INSERT within own tenant scope: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA)
	}()
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA); n != 1 {
		t.Errorf("rows after the owner's own-tenant INSERT = %d, want 1", n)
	}
}

// QA-4: with app.current_tenant entirely UNSET (not merely mismatched, which is
// all T01-4 tests), the app role sees ZERO rows — fail-closed, not an error and
// not every row. Seeds a row first so an empty-table false positive is impossible.
// Mirrors the "SELECT with no tenant context" idiom in app_exchange_rls_test.go.
func TestRLS_SubmissionRateLimitsNoTenantContextZeroRows(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cleanup := seedSubmissionRateLimit(t, h.tenantA, 7)
	defer cleanup()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	n, err := scanCount(ctx, tx, `SELECT count(*) FROM submission_rate_limits`)
	if failIfUndefinedSubmissionRateLimits(t, "SELECT with no tenant context", err) {
		return
	}
	if err != nil {
		t.Fatalf("SELECT with no tenant context: %v", err)
	}
	if n != 0 {
		t.Errorf("submission_rate_limits visible with no tenant context set = %d, want 0 (fail-closed)", n)
	}

	// The row genuinely exists and is genuinely readable — with the GUC set.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if got := mustCount(t, tx,
			`SELECT count(*) FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA); got != 1 {
			t.Errorf("tenant A's own row visible with context set = %d, want 1", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx (tenant A, positive half): %v", err)
	}
}

// QA-5: tenant_id is the PRIMARY KEY, so a second row for the same tenant is
// refused — "at most one row per tenant", not merely "at least one permitted". No
// case above ever attempts a second INSERT against an already-occupied PK.
func TestRLS_SubmissionRateLimitsDuplicateTenantRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cleanup := seedSubmissionRateLimit(t, h.tenantA, 9)
	defer cleanup()

	_, err := h.super.Exec(ctx,
		`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, $2)`,
		h.tenantA, 99,
	)
	if failIfUndefinedSubmissionRateLimits(t, "duplicate-tenant INSERT", err) {
		return
	}
	if err == nil {
		t.Fatal("second INSERT for the same tenant_id succeeded, want a primary-key violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate-tenant INSERT: SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM submission_rate_limits WHERE tenant_id = $1`, h.tenantA); n != 1 {
		t.Errorf("rows for tenant A after the refused duplicate INSERT = %d, want 1 (only the original)", n)
	}
}
