// M5-02-05 (task-222): the DB-BACKED spec for submission_jobs.poll_ref — a nullable text
// column that persists a Pending.Ref (result.go:36) so a deferred verdict survives a
// restart (Core AC-3). Authored BEFORE the migration existed (cef5cd7), when RED here was
// SQLSTATE 42703 undefined_column, never a compile error, a connection failure, a fixture
// failure, or a skip. The migration (1b6f581,
// migrations/20260722182935_submission_jobs_poll_ref.sql) has since landed:
//
//	ALTER TABLE submission_jobs
//	    ADD COLUMN poll_ref text CHECK (poll_ref IS NULL OR char_length(poll_ref) > 0);
//
// All four cases below are now GREEN (QA Mode B re-verified this live against the dev DB).
//
// Package `submission_test`, matching exchange_db_test.go and failure_modes_test.go — the
// fixture this file reuses (requireExchangeDB, the package-level fx, exChain) is unexported,
// so only that package declaration can reach it. TestMain already exists at
// failure_modes_test.go:57 — one per test binary — so this file defines none, and no writer
// function ships here either ([no-poll-ref-writer-in-02]): every write below is a raw
// UPDATE, never a call into production code, because M5-04 owns the job-state UPDATE that
// sets poll_ref for real.
//
// GATING. Every case self-skips via requireExchangeDB when the suite is unconfigured, and
// ONLY then — the same pair of env vars (DATABASE_URL, DATABASE_MIGRATION_URL) every other
// case in this package gates on, so scripts/ci/rls-test-gate.sh's zero-skip / zero-tests
// rule stays satisfied under the CI `queue` job exactly as it already is for this package.
//
// Spec-to-test map (Test Specs table, task-222 / M5-02.md):
//
//	AC-3 TestPollRef_RoundTripsAcrossTransactions
//	AC-4 TestPollRef_EmptyStringRefused
//	AC-1 TestPollRef_NullIsTheDefault
//	AC-5 TestRLS_PollRefNotVisibleAcrossTenants
//
// All four went RED with 42703 before the migration landed; now GREEN. failPollRefUndefined
// below turns that raw driver error into an explicit, self-explaining t.Fatalf rather than a
// cryptic SQLSTATE, following the invoices_fiscal_rls_test.go:90-101 (failIfUndefinedColumn)
// precedent — it stays in place post-migration as a guard against running this suite
// against a DB that has not had 1b6f581 applied yet (same convention as
// failIfUndefinedAppExchange, internal/platform/db/app_exchange_rls_test.go:200), reusing
// this package's own exPgCode (exchange_db_test.go:235-241) rather than re-deriving errors.As.
//
// Local run: `DEV_DB_PORT=5433 make test-queue` from this worktree, or export DATABASE_URL /
// DATABASE_MIGRATION_URL and run `go test ./internal/submission/... -run TestPollRef`.
package submission_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// failPollRefUndefined turns SQLSTATE 42703 against poll_ref into an explicit, self-
// explaining message instead of a raw driver error, and reports whether it fired so callers
// that must not proceed (the column doesn't exist, so nothing downstream is meaningful) can
// stop. This is the ONLY expected failure mode until the M5-02-05 migration lands; any other
// error (wrong DSN, RLS refusal, a different SQLSTATE) must still surface as its own
// t.Fatalf/t.Errorf so this helper never masks a different bug.
func failPollRefUndefined(t *testing.T, what string, err error) bool {
	t.Helper()
	if exPgCode(err) == "42703" {
		t.Fatalf("%s: undefined_column (42703) — the submission_jobs.poll_ref migration "+
			"(M5-02-05) is not applied yet: %v", what, err)
		return true
	}
	return false
}

// pollRefWrite runs one UPDATE of poll_ref inside its own tenant-scoped transaction (the
// app role, since AC #6 is about invoice_app's own access, not the migrator's).
func pollRefWrite(ctx context.Context, f *effectsFixture, tenantID, jobID string, ref *string) error {
	return db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE submission_jobs SET poll_ref = $1 WHERE id = $2`, ref, jobID)
		return err
	})
}

// pollRefRead reads poll_ref back inside its OWN tenant-scoped transaction — separate from
// whichever transaction wrote it, which is the entire point of AC-3's "survives a restart"
// proof: one shared transaction would prove nothing about durability.
func pollRefRead(ctx context.Context, f *effectsFixture, tenantID, jobID string) (*string, error) {
	var got *string
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT poll_ref FROM submission_jobs WHERE id = $1`, jobID).Scan(&got)
	})
	return got, err
}

// AC-3 (Core AC-3, "a deferred verdict survives a restart"): a Pending.Ref value written to
// poll_ref inside one tenant-scoped transaction reads back byte-identical in a SEPARATE
// transaction. The ref is a non-trivial, mixed-case, punctuated authority-style token —
// not a trivial fixture string — so silent truncation or encoding damage would show.
func TestPollRef_RoundTripsAcrossTransactions(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, jobID, cleanup := exChain(t, f)
	defer cleanup()

	want := "FIRS-Batch#7/Retry:β-2026Q3~poll"

	if err := pollRefWrite(ctx, f, tenantID, jobID, &want); err != nil {
		if failPollRefUndefined(t, "UPDATE poll_ref (write transaction)", err) {
			return
		}
		t.Fatalf("write poll_ref in the first transaction: %v", err)
	}

	got, err := pollRefRead(ctx, f, tenantID, jobID)
	if err != nil {
		if failPollRefUndefined(t, "SELECT poll_ref (separate read transaction)", err) {
			return
		}
		t.Fatalf("read poll_ref back in a separate transaction: %v", err)
	}
	if got == nil {
		t.Fatal("poll_ref read back NULL, want the written ref — the write did not survive " +
			"into a separate transaction")
	}
	if *got != want {
		t.Errorf("poll_ref read back = %q, want %q (byte-identical) — a mismatch here means "+
			"truncation or encoding damage across the transaction boundary", *got, want)
	}

	// poll_ref = NULL is a legal UPDATE target: clearing a previously-set ref back to NULL
	// must be allowed. `IS NULL OR char_length(poll_ref) > 0` is easy to get backwards under
	// a moment of inattention, so this is asserted explicitly rather than assumed.
	if err := pollRefWrite(ctx, f, tenantID, jobID, nil); err != nil {
		if failPollRefUndefined(t, "UPDATE poll_ref = NULL (clearing a set ref)", err) {
			return
		}
		t.Fatalf("clear poll_ref back to NULL: %v", err)
	}
	cleared, err := pollRefRead(ctx, f, tenantID, jobID)
	if err != nil {
		if failPollRefUndefined(t, "SELECT poll_ref (after clearing)", err) {
			return
		}
		t.Fatalf("read poll_ref after clearing: %v", err)
	}
	if cleared != nil {
		t.Errorf("poll_ref after clearing = %q, want NULL — clearing a ref back to NULL must "+
			"be a legal UPDATE, not silently refused or coerced", *cleared)
	}

	// A whitespace-only ref (char_length(' ') = 1 > 0) is INTENTIONALLY accepted by this
	// CHECK ([irn-is-opaque]-style: no format validation on an opaque authority string).
	// This documents the boundary; it must not be "improved" into a trim/reject.
	whitespace := " "
	if err := pollRefWrite(ctx, f, tenantID, jobID, &whitespace); err != nil {
		if failPollRefUndefined(t, "UPDATE poll_ref = ' ' (whitespace-only)", err) {
			return
		}
		t.Errorf("UPDATE poll_ref to a whitespace-only string failed, want success — "+
			"char_length(' ') = 1 > 0, so the CHECK must accept it (opaque string, no "+
			"format validation): %v", err)
	}
}

// AC-4: an UPDATE writing the empty string is refused by the CHECK, SQLSTATE 23514 — an
// empty string is not a second, silent encoding of "no ref" alongside NULL.
func TestPollRef_EmptyStringRefused(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, jobID, cleanup := exChain(t, f)
	defer cleanup()

	empty := ""
	err := pollRefWrite(ctx, f, tenantID, jobID, &empty)
	if err == nil {
		t.Fatal("UPDATE poll_ref = '' succeeded, want a CHECK violation (SQLSTATE 23514) — " +
			"an empty ref is not the same state as no ref")
	}
	if code := exPgCode(err); code != "23514" {
		t.Errorf("UPDATE poll_ref = '': SQLSTATE = %q, want 23514 (check_violation) — a 42703 "+
			"here would mean the poll_ref migration (1b6f581) is not applied against this DB: "+
			"%v", code, err)
	}
}

// AC-1: a freshly inserted submission_jobs row has poll_ref NULL by default — no fabricated
// default value. exChain's own INSERT names only the pre-existing columns
// (exchange_db_test.go:132-137), so this proves the column's own DEFAULT (or absence of
// one), not something the fixture happened to set.
func TestPollRef_NullIsTheDefault(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, jobID, cleanup := exChain(t, f)
	defer cleanup()

	got, err := pollRefRead(ctx, f, tenantID, jobID)
	if err != nil {
		if failPollRefUndefined(t, "SELECT poll_ref (fresh row)", err) {
			return
		}
		t.Fatalf("read poll_ref on a freshly inserted job: %v", err)
	}
	if got != nil {
		t.Errorf("poll_ref on a fresh row = %q, want NULL (no fabricated default)", *got)
	}
}

// AC-5: tenant A's poll_ref is invisible under tenant B's RLS GUC — filtered out (zero
// rows), never an error. Models internal/platform/db/submission_jobs_rls_test.go:182-212
// (TestRLS_SubmissionJobsCrossTenantSelectRefused), which asserts the same shape for the
// whole row; this is the poll_ref-specific instance, staying on the fx/exChain fixture (AC
// #8) rather than that package's separate requireHarness fixture, since the two are not
// interchangeable without rebuilding the FK seed.
func TestRLS_PollRefNotVisibleAcrossTenants(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()

	tenantA, _, jobA, cleanupA := exChain(t, f)
	defer cleanupA()
	tenantB, _, _, cleanupB := exChain(t, f)
	defer cleanupB()

	refA := "TENANT-A-ONLY-REF"
	if err := pollRefWrite(ctx, f, tenantA, jobA, &refA); err != nil {
		if failPollRefUndefined(t, "UPDATE poll_ref (tenant A's own row)", err) {
			return
		}
		t.Fatalf("write poll_ref on tenant A's job: %v", err)
	}

	// Read tenant A's job id while scoped to tenant B: RLS filters it out entirely, so this
	// must return zero rows (sql.ErrNoRows via QueryRow), never poll_ref's actual value and
	// never a permission error.
	err := db.WithinTenantTx(ctx, f.app, tenantB, func(tx pgx.Tx) error {
		var leaked *string
		scanErr := tx.QueryRow(ctx,
			`SELECT poll_ref FROM submission_jobs WHERE id = $1`, jobA).Scan(&leaked)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		t.Errorf("tenant A's poll_ref (%q) was visible under tenant B's RLS context — "+
			"want zero rows, not the value or an error", strDeref(leaked))
		return nil
	})
	if err != nil {
		if failPollRefUndefined(t, "SELECT poll_ref (tenant A's row, under tenant B's GUC)", err) {
			return
		}
		t.Fatalf("read tenant A's job under tenant B's tenant context: %v", err)
	}
}

// strDeref renders a possibly-nil *string for an error message without a nil-pointer panic.
func strDeref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
