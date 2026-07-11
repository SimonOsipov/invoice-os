// M3-04-01 (Test-first: yes) — schema/grant contract tests for the two GLOBAL reference
// tables the M3-04-01 migration introduces: rule_set_versions and rules. Written BEFORE
// the migration exists, so this suite is RED against `undefined_table` (SQLSTATE 42P01)
// until the migration lands. It mirrors the `roles` precedent
// (migrations/20260709151759_roles.sql): no tenant_id, no RLS — every tenant sees the
// same rule content, so isolation here is a plain GRANT contract, not RLS.
//
// Coverage (see M3-04-01 Test Specs):
//  1. TestSchema_AppCannotMutateContent      — app cannot UPDATE key/severity/type (42501).
//  2. TestSchema_AppCanToggleEnabled         — app CAN UPDATE the enabled kill-switch column.
//  3. TestSchema_AppCannotInsertVersionOrRule — app has no INSERT grant on either table (42501).
//  4. TestSchema_AppCannotDeleteRule         — app has no DELETE grant on either table (42501).
//  5. TestSchema_OneActiveVersionEnforced    — the partial unique index allows <=1 active version (23505).
//  6. TestSchema_NoRuleContentShipped        — the migration itself ships tables only, no seed rows.
//
// Run: `make dev-db` once, then with the per-role DSNs set directly (see dbTestPools):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/validation/...
package validation

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fixtureNotes tags every rule_set_versions row this suite seeds (via seedVersion), so
// TestSchema_NoRuleContentShipped can tell "content this test suite created" apart from
// "content the M3-04-01 migration shipped" without depending on test execution order or
// on every other test's t.Cleanup having already run (see that test's doc comment).
const fixtureNotes = "qa-fixture:internal/validation/schema_test.go"

// versionSeq hands out globally-unique `version` ints for seeded rule_set_versions rows
// across this whole test binary run. Based well above any real published version (which
// starts at 1), so fixture rows can never collide with production-shaped data.
var versionSeq atomic.Int64

func nextVersion() int {
	return int(900000 + versionSeq.Add(1))
}

// dbTestPools returns the superuser (seed) and app-role pools for this db-integration
// suite, or skips when the per-role DSNs are unset — the same env gate `make test-rls`
// and internal/portfolio/portfolio_test.go's dbTestPools (~line 731) use (DATABASE_URL
// for invoice_app, DATABASE_SUPERUSER_URL for seeding as the BYPASSRLS superuser).
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("validation db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	// Registered before the app pool's Cleanup, so per LIFO ordering it closes AFTER
	// app's pool — and callers that register a row-delete Cleanup of their own (after
	// calling dbTestPools) get it run BEFORE either pool closes.
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// seedVersion inserts one rule_set_versions row as the superuser (BYPASSRLS; these
// tables have no RLS anyway, but seeding via the migrator-adjacent superuser role keeps
// fixture setup outside the grant contract under test) and registers its own cleanup.
// Every fixture row is tagged with fixtureNotes (see that const's doc comment). Cleanup
// deletes the version row only — rules.rule_set_version_id is
// ON DELETE CASCADE, so any rule seeded under this version (via seedRule or directly by
// a test) is cleaned up transitively.
func seedVersion(t *testing.T, super *pgxpool.Pool, isActive bool) (id string, version int) {
	t.Helper()
	ctx := context.Background()
	version = nextVersion()
	if err := super.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, notes) VALUES ($1, $2, $3) RETURNING id`,
		version, isActive, fixtureNotes,
	).Scan(&id); err != nil {
		t.Fatalf("seed rule_set_versions(version=%d, is_active=%t): %v", version, isActive, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM rule_set_versions WHERE id = $1`, id)
	})
	return id, version
}

// seedRule inserts one rules row under versionID as the superuser, with otherwise-valid
// placeholder content (type/severity/message satisfy the NOT NULL + CHECK constraints).
// No cleanup of its own is registered: it is always reachable from a seedVersion call,
// whose cleanup cascades onto this row (see seedVersion's doc comment).
func seedRule(t *testing.T, super *pgxpool.Pool, versionID, key string) (id string) {
	t.Helper()
	ctx := context.Background()
	if err := super.QueryRow(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
		 VALUES ($1, $2, 'required', 'error', 'qa fixture rule')
		 RETURNING id`,
		versionID, key,
	).Scan(&id); err != nil {
		t.Fatalf("seed rules(key=%q): %v", key, err)
	}
	return id
}

// assertSQLState asserts err is a Postgres error carrying the given SQLSTATE — copied
// verbatim from internal/audit/audit_test.go:345-352 / internal/platform/db/tenants_kind_test.go:33-40
// (pgx v5 surfaces Postgres errors as *pgconn.PgError, unwrappable via errors.As).
func assertSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s, got non-Postgres err %v", want, err)
	}
	if pgErr.Code != want {
		t.Fatalf("want SQLSTATE %s, got %s (%s)", want, pgErr.Code, pgErr.Message)
	}
}

// TestSchema_AppCannotMutateContent (Test Spec #1): as invoice_app, an UPDATE naming any
// content column (key, severity, type — anything other than enabled) must fail with
// insufficient_privilege (42501). The column-level GRANT ("GRANT SELECT, UPDATE (enabled)
// ON rules TO invoice_app") makes rule content immutable to the app; only the kill-switch
// is app-writable (TestSchema_AppCanToggleEnabled below).
func TestSchema_AppCannotMutateContent(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, false)
	ruleID := seedRule(t, super, versionID, "content-immutable-probe")

	cases := []struct {
		col, sql string
	}{
		{"key", `UPDATE rules SET key = 'mutated' WHERE id = $1`},
		{"severity", `UPDATE rules SET severity = 'warning' WHERE id = $1`},
		{"type", `UPDATE rules SET type = 'enum' WHERE id = $1`},
	}
	for _, tc := range cases {
		t.Run(tc.col, func(t *testing.T) {
			_, err := app.Exec(ctx, tc.sql, ruleID)
			if err == nil {
				t.Fatalf("UPDATE rules SET %s=...: want SQLSTATE 42501 (insufficient_privilege), got no error -- app must not be able to mutate rule content", tc.col)
			}
			assertSQLState(t, err, "42501")
		})
	}
}

// TestSchema_AppCanToggleEnabled (Test Spec #2): as invoice_app, `UPDATE rules SET
// enabled=false` on a seeded rule must succeed (1 row affected, no error), and the new
// value must be durable — the positive counterpart to
// TestSchema_AppCannotMutateContent, proving the column-level grant is scoped exactly to
// `enabled`, not accidentally denying everything or accidentally granting everything.
func TestSchema_AppCanToggleEnabled(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, false)
	ruleID := seedRule(t, super, versionID, "enabled-toggle-probe")

	tag, err := app.Exec(ctx, `UPDATE rules SET enabled = false WHERE id = $1`, ruleID)
	if err != nil {
		t.Fatalf("UPDATE rules SET enabled=false: want success (the kill-switch column is app-writable), got error: %v", err)
	}
	if got := tag.RowsAffected(); got != 1 {
		t.Fatalf("RowsAffected = %d, want 1", got)
	}

	var enabled bool
	if err := super.QueryRow(ctx, `SELECT enabled FROM rules WHERE id = $1`, ruleID).Scan(&enabled); err != nil {
		t.Fatalf("read back enabled: %v", err)
	}
	if enabled {
		t.Error("enabled = true after UPDATE ... SET enabled = false, want false -- the update did not persist")
	}
}

// TestSchema_AppCannotInsertVersionOrRule (Test Spec #3): as invoice_app, INSERT into
// either table must fail with insufficient_privilege (42501) -- the app has SELECT-only
// on rule_set_versions and SELECT + UPDATE(enabled)-only on rules, no INSERT grant on
// either.
func TestSchema_AppCannotInsertVersionOrRule(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	t.Run("rule_set_versions", func(t *testing.T) {
		v := nextVersion()
		_, err := app.Exec(ctx, `INSERT INTO rule_set_versions (version) VALUES ($1)`, v)
		if err == nil {
			// Only reachable if the grant is missing (a bug this test exists to catch) --
			// clean up the row it should never have been able to create.
			t.Cleanup(func() {
				_, _ = super.Exec(context.Background(), `DELETE FROM rule_set_versions WHERE version = $1`, v)
			})
			t.Fatal("INSERT INTO rule_set_versions: want SQLSTATE 42501 (insufficient_privilege), got no error -- app has SELECT only")
		}
		assertSQLState(t, err, "42501")
	})

	t.Run("rules", func(t *testing.T) {
		// Seed a valid FK target as superuser first, so a successful insert (a bug) is
		// never masked by an unrelated foreign_key_violation -- the assertion under test
		// is the grant, not referential integrity.
		versionID, _ := seedVersion(t, super, false)
		_, err := app.Exec(ctx,
			`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
			 VALUES ($1, 'app-insert-probe', 'required', 'error', 'should be rejected')`,
			versionID,
		)
		if err == nil {
			t.Fatal("INSERT INTO rules: want SQLSTATE 42501 (insufficient_privilege), got no error -- app has SELECT + UPDATE(enabled) only, no INSERT")
		}
		assertSQLState(t, err, "42501")
	})
}

// TestSchema_AppCannotDeleteRule (Test Spec #4): as invoice_app, DELETE from either table
// must fail with insufficient_privilege (42501) -- rule content is immutable and
// versions are permanent once published; only SELECT (+ UPDATE(enabled) on rules) is
// granted.
func TestSchema_AppCannotDeleteRule(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	t.Run("rules", func(t *testing.T) {
		versionID, _ := seedVersion(t, super, false)
		ruleID := seedRule(t, super, versionID, "delete-rule-probe")
		_, err := app.Exec(ctx, `DELETE FROM rules WHERE id = $1`, ruleID)
		if err == nil {
			t.Fatal("DELETE FROM rules: want SQLSTATE 42501 (insufficient_privilege), got no error")
		}
		assertSQLState(t, err, "42501")
	})

	t.Run("rule_set_versions", func(t *testing.T) {
		versionID, _ := seedVersion(t, super, false)
		_, err := app.Exec(ctx, `DELETE FROM rule_set_versions WHERE id = $1`, versionID)
		if err == nil {
			t.Fatal("DELETE FROM rule_set_versions: want SQLSTATE 42501 (insufficient_privilege), got no error")
		}
		assertSQLState(t, err, "42501")
	})
}

// TestSchema_OneActiveVersionEnforced (Test Spec #5): the partial unique index
// (`CREATE UNIQUE INDEX rule_set_versions_one_active ON rule_set_versions ((is_active))
// WHERE is_active`) allows at most one is_active=true row at a time. Seeding a first
// active version then inserting a second must fail with unique_violation (23505). Run as
// superuser: this is a schema-level constraint, not a grant, so it must hold for every
// role, and using super here isolates the assertion from the (separately tested)
// INSERT grant.
func TestSchema_OneActiveVersionEnforced(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	seedVersion(t, super, true) // version A: is_active = true

	secondVersion := nextVersion()
	_, err := super.Exec(ctx,
		`INSERT INTO rule_set_versions (version, is_active, notes) VALUES ($1, true, $2)`,
		secondVersion, fixtureNotes,
	)
	if err == nil {
		// Only reachable if the partial unique index is missing/wrong (a bug this test
		// exists to catch) -- clean up the row that should never have been created.
		t.Cleanup(func() {
			_, _ = super.Exec(context.Background(), `DELETE FROM rule_set_versions WHERE version = $1`, secondVersion)
		})
		t.Fatal("INSERT second is_active=true rule_set_versions row: want SQLSTATE 23505 (unique_violation on rule_set_versions_one_active), got no error")
	}
	assertSQLState(t, err, "23505")
}

// TestSchema_NoRuleContentShipped (Test Spec #6): the M3-04-01 migration must ship the
// rule_set_versions/rules TABLES ONLY -- no seeded rule_set_versions or rules rows. This
// suite's own fixtures (seedVersion/seedRule) are the only writers to these tables under
// test, and every fixture row is tagged with fixtureNotes, so this test asserts "zero
// rows NOT tagged as a QA fixture" rather than a bare `count(*) == 0`. That makes the
// assertion independent of:
//   - test execution order within this file/binary (it does not need to run before any
//     fixture-seeding test),
//   - other tests' t.Cleanup having already fired,
//   - stray fixture rows left behind by a crashed/interrupted prior run (still tagged,
//     still excluded) -- what it CANNOT distinguish is a stray row from a prior run that
//     was NOT created via seedVersion (e.g. hand-inserted while debugging); the local dev
//     DB is expected to be reset (`make dev-db-reset` / `make migrate-reset`) if that ever
//     happens.
func TestSchema_NoRuleContentShipped(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	var versionCount int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM rule_set_versions WHERE notes IS DISTINCT FROM $1`, fixtureNotes,
	).Scan(&versionCount); err != nil {
		t.Fatalf("count non-fixture rule_set_versions: %v", err)
	}
	if versionCount != 0 {
		t.Errorf("rule_set_versions has %d row(s) not tagged as a QA fixture -- the M3-04-01 migration must ship the tables only, no seeded rule_set_versions content", versionCount)
	}

	var ruleCount int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM rules r
		   JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.notes IS DISTINCT FROM $1`, fixtureNotes,
	).Scan(&ruleCount); err != nil {
		t.Fatalf("count non-fixture rules: %v", err)
	}
	if ruleCount != 0 {
		t.Errorf("rules has %d row(s) not tagged as a QA fixture -- the M3-04-01 migration must ship the tables only, no seeded rule content", ruleCount)
	}
}
