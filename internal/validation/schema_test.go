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

// seededVersionNotes matches the notes marker every migration-seeded rule_set_versions
// row carries ("MBS global rule-set v1 (M3-05 seed)", "MBS global rule-set v2 (M4-04-01:
// ...)"). TestSchema_NoRuleContentShipped uses it to exclude the SANCTIONED seeds by what
// they ARE rather than by naming version numbers -- a literal `version <> 1` had to be
// edited on every publish, and silently mis-fired when v2 shipped.
const seededVersionNotes = "MBS global rule-set v%"

// versionSeq hands out globally-unique `version` ints for seeded rule_set_versions rows
// across this whole test binary run. Based well above any real published version (which
// starts at 1), so fixture rows can never collide with production-shaped data.
var versionSeq atomic.Int64

func nextVersion() int {
	return int(900000 + versionSeq.Add(1))
}

// TestMain (M4-18, §2.6): a package-wide pre-flight self-heal for the shared, persistent
// 5432 DB. sealAndActivate's throwaway cleanup (below) necessarily seals its fixture row
// BEFORE deleting it -- a hard abort in that window (a `go test -timeout` kill, a panic in
// another goroutine, SIGKILL) can leave a SEALED, possibly ACTIVE orphan that a plain
// DELETE can no longer remove (M4-17's Guard C). Env-gated on DATABASE_SUPERUSER_URL --
// this package also has many non-DB unit tests (cel/engine/evaluators/...) that must keep
// running when no DSN is set.
func TestMain(m *testing.M) {
	if superURL := os.Getenv("DATABASE_SUPERUSER_URL"); superURL != "" {
		sweepOrphanFixtures(superURL)
	}
	os.Exit(m.Run())
}

// sweepOrphanFixtures removes throwaway rule_set_versions rows a hard-aborted prior run
// left behind. Sealed orphans (M4-18) resist a plain DELETE (Guard C), so it brackets the
// delete in DISABLE/ENABLE TRIGGER USER, then restores v2 as the sole active row. Targets
// ONLY fixtureNotes-tagged rows (never the v1/v2 seeds) -- fixtureNotes is already a
// fixed, greppable const (this file) shared across runs, so no marker redefinition is
// needed. Triple-guarded: the fixtureNotes match, the explicit `version NOT IN (1,2)`
// (belt-and-suspenders), and the fact every fixture row uses nextVersion() values >=
// 900001 (above), which can never collide with v1/v2. Best-effort: a connection or query
// failure here just means the sweep no-ops -- it is a safety net for ABNORMAL aborts, not
// the primary teardown (sealAndActivate's per-fixture committed delete is).
func sweepOrphanFixtures(superURL string) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, superURL)
	if err != nil {
		return
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _ = tx.Exec(ctx, `ALTER TABLE rule_set_versions DISABLE TRIGGER USER`)
	_, _ = tx.Exec(ctx, `ALTER TABLE rules DISABLE TRIGGER USER`)
	_, _ = tx.Exec(ctx, `DELETE FROM rule_set_versions WHERE notes = $1 AND version NOT IN (1, 2)`, fixtureNotes)
	_, _ = tx.Exec(ctx, `ALTER TABLE rules ENABLE TRIGGER USER`)
	_, _ = tx.Exec(ctx, `ALTER TABLE rule_set_versions ENABLE TRIGGER USER`)
	// The DELETE above may have cleared an active orphan (freeing the one-active slot);
	// ensure v2 is active. Ordered delete-then-activate so the non-deferrable partial
	// unique index never sees two active rows at once.
	_, _ = tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE version = 2 AND NOT is_active`)
	_ = tx.Commit(ctx)
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
// fixture setup outside the grant contract under test) and registers its own cheap
// cleanup. Every fixture row is tagged with fixtureNotes (see that const's doc comment).
//
// M4-18 rework: ALWAYS inserts is_active=false, sealed=false -- satisfies the
// active⟹sealed CHECK and keeps the parent unsealed so rules can still be inserted
// under it (Guard A, M4-17) before any seal+activate happens. Signature UNCHANGED
// ((t, super, isActive) (id, version)): only if isActive does this delegate to the new
// sealAndActivate helper, which performs the real publish order (seal -> deactivate
// previous -> activate) and owns the throwaway's teardown. Cleanup here deletes the
// version row only — rules.rule_set_version_id is ON DELETE CASCADE, so any rule seeded
// under this version (via seedRule/seedFullRule or directly by a test) is cleaned up
// transitively (or, for a sealed+activated throwaway, by sealAndActivate's own
// disable-trigger delete, which runs first under LIFO -- see that function's doc
// comment).
func seedVersion(t *testing.T, super *pgxpool.Pool, isActive bool) (id string, version int) {
	t.Helper()
	ctx := context.Background()
	version = nextVersion()

	if err := super.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, false, false, $2) RETURNING id`,
		version, fixtureNotes,
	).Scan(&id); err != nil {
		t.Fatalf("seed rule_set_versions(version=%d): %v", version, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM rule_set_versions WHERE id = $1`, id)
	})

	if isActive {
		sealAndActivate(t, super, id)
	}

	return id, version
}

// sealAndActivate (M4-18, §2.1) performs the seal->activate half of the real publish
// flow for a version (versionID) that already carries whatever rules it needs, and owns
// the teardown. The active⟹sealed CHECK forbids activating an unsealed row, so this
// MUST seal before activating; Guard A (M4-17) forbids inserting rules into an
// already-sealed parent, so callers must insert all of versionID's rules BEFORE calling
// this.
func sealAndActivate(t *testing.T, super *pgxpool.Pool, versionID string) {
	t.Helper()
	ctx := context.Background()

	// Capture the row that is sanctioned-active RIGHT NOW -- the real active v2, or
	// whatever a nesting fixture made active. Capture-BY-ID (not by naming a version
	// number) is what makes arbitrarily-deep nesting compose correctly back to the
	// original active version (traced in the M4-18 story §2.4 against
	// rule_set_v2_qa_test.go's two-level nested restore).
	var prevActiveID string
	if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&prevActiveID); err != nil {
		t.Fatalf("sealAndActivate(versionID=%s): capture the currently active version id: %v", versionID, err)
	}

	// Register the restore-cleanup NOW, before any fallible mutation below -- the
	// cleanup-order fix (M4-18 §2.4): a failed seal/deactivate/activate can never leave
	// the real active version permanently dark, because the restore is already queued
	// via t.Cleanup before it could fail.
	t.Cleanup(func() {
		ctx := context.Background()

		// Deactivate whatever is active (robust to partial states -- it may be
		// versionID if the activate below succeeded, or still prevActiveID if this
		// helper never got that far), then reactivate prevActiveID.
		if _, err := super.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE is_active`); err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): deactivate whatever is active: %v", versionID, err)
		}
		if _, err := super.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE id = $1`, prevActiveID); err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): restore the previously-active version (id=%s): %v",
				versionID, prevActiveID, err)
		}

		// The throwaway may now be sealed, so Guard C (M4-17) can block a plain
		// DELETE. Remove it inside a COMMITTED tx bracketed by DISABLE/ENABLE TRIGGER
		// USER -- the exact teardown shape M4-17 established for the reversibility
		// fixtures (rule_set_v2_test.go:337, seed_test.go:665), adapted from
		// rolled-back to committed because the row must actually be removed (the
		// shared 5432 DB persists across runs). Best-effort: logged, not fatal, so
		// one cleanup failure never masks the rest of this test's cleanups.
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): begin delete tx: %v", versionID, err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions DISABLE TRIGGER USER`); err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): disable triggers on rule_set_versions: %v", versionID, err)
			return
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE rules DISABLE TRIGGER USER`); err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): disable triggers on rules: %v", versionID, err)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM rule_set_versions WHERE id = $1`, versionID); err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): delete throwaway: %v", versionID, err)
			return
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE rules ENABLE TRIGGER USER`); err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): re-enable triggers on rules: %v", versionID, err)
			return
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions ENABLE TRIGGER USER`); err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): re-enable triggers on rule_set_versions: %v", versionID, err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("sealAndActivate cleanup(versionID=%s): commit throwaway delete: %v", versionID, err)
		}
	})

	// The real publish order: seal first (Guard C allows false->true), THEN clear the
	// active slot, THEN activate -- now sealed=true, so the CHECK is satisfied and the
	// one-active partial-unique index is free.
	if _, err := super.Exec(ctx, `UPDATE rule_set_versions SET sealed = true WHERE id = $1`, versionID); err != nil {
		t.Fatalf("sealAndActivate(versionID=%s): seal (false->true): %v", versionID, err)
	}
	if _, err := super.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE id = $1`, prevActiveID); err != nil {
		t.Fatalf("sealAndActivate(versionID=%s): deactivate previously-active (id=%s): %v", versionID, prevActiveID, err)
	}
	if _, err := super.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE id = $1`, versionID); err != nil {
		t.Fatalf("sealAndActivate(versionID=%s): activate: %v", versionID, err)
	}
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
	// M4-18: sealed=true so this insert passes the active⟹sealed CHECK and genuinely
	// collides on the one-active partial-unique index (23505) instead of tripping the
	// CHECK (23514) first -- preserving this test's original intent (proving the
	// partial unique index), not retargeting it.
	_, err := super.Exec(ctx,
		`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, true, true, $2)`,
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

	// The migrations permanently ship sanctioned content rows: the (now
	// inactive) v1 rule_set_versions row with its 17 base rules, and the active
	// v2 row with 19 (v1's 17 + the 2 line-item rules) -- see
	// migrations/20260716185106_rule_set_v2.sql. Both are excluded here
	// alongside the fixtureNotes exclusion, so this test still guards against
	// an accidental EXTRA seed or stray hand-inserted rows -- a guard the
	// seed_test.go "the active version has exactly 19 rules" assertion does not
	// provide (NARROWED per the story's Decisions section, not retired).
	//
	// The exclusion is expressed as "not a migration-seeded version" by reading
	// the seeds' own notes marker, rather than by listing version numbers: a
	// literal list (`version <> 1`) silently under-excludes on the next version
	// publish and turns this guard into a false alarm -- which is exactly what
	// v2 did to it.
	var versionCount int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM rule_set_versions
		  WHERE notes IS DISTINCT FROM $1 AND notes NOT LIKE $2`, fixtureNotes, seededVersionNotes,
	).Scan(&versionCount); err != nil {
		t.Fatalf("count non-fixture rule_set_versions: %v", err)
	}
	if versionCount != 0 {
		t.Errorf("rule_set_versions has %d row(s) not tagged as a QA fixture (and not the sanctioned M3-05 v1 seed) -- an unsanctioned second seed or stray row exists", versionCount)
	}

	var ruleCount int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM rules r
		   JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.notes IS DISTINCT FROM $1 AND v.notes NOT LIKE $2`, fixtureNotes, seededVersionNotes,
	).Scan(&ruleCount); err != nil {
		t.Fatalf("count non-fixture rules: %v", err)
	}
	if ruleCount != 0 {
		t.Errorf("rules has %d row(s) not tagged as a QA fixture (and not under the sanctioned M3-05 v1 seed) -- an unsanctioned second seed or stray row exists", ruleCount)
	}
}

// ---- QA-added adversarial / edge coverage (post-implementation, M3-04-01 Mode B) ----
//
// The six tests above are the AC-derived RED suite (authored pre-implementation, now
// green). The tests below were added during QA verification to close gaps the AC suite
// didn't cover:
//
//  7. TestSchema_AppCannotMutateRemainingContentColumns — completeness for #1: every
//     OTHER content column (target, params, message, scope, "when",
//     rule_set_version_id), not just key/severity/type.
//  8. TestSchema_MultipleInactiveVersionsAllowed — the mirror image of #5: proves the
//     unique index is PARTIAL (WHERE is_active), not total, so it must not fire for
//     is_active=false rows. Guards the M3-05 seeding path (which will insert an
//     inactive version before flipping it active) against a regression to a total
//     unique index on `version` conflated with `is_active`.
//  9. TestSchema_RulesCascadeOnVersionDelete — the FK's ON DELETE CASCADE, asserted
//     directly rather than relying on seedVersion's own (error-swallowing) Cleanup.
// 10. TestSchema_CheckConstraintsRejectInvalidEnums — the three CHECK constraints
//     (type, severity, scope) reject out-of-list values with 23514.

// TestSchema_AppCannotMutateRemainingContentColumns (QA addition): the column-level
// grant `GRANT SELECT, UPDATE (enabled) ON rules TO invoice_app` must deny invoice_app
// UPDATE on every content column other than `enabled` -- TestSchema_AppCannotMutateContent
// above only exercises key/severity/type. Covers the remaining columns: target, params,
// message, scope, "when", and the rule_set_version_id FK itself. Each case must fail
// 42501 (insufficient_privilege), proving the grant scope is exactly {enabled}.
func TestSchema_AppCannotMutateRemainingContentColumns(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, false)
	ruleID := seedRule(t, super, versionID, "content-immutable-remaining-probe")
	otherVersionID, _ := seedVersion(t, super, false)

	cases := []struct {
		col  string
		sql  string
		args []any
	}{
		{"target", `UPDATE rules SET target = 'mutated' WHERE id = $1`, []any{ruleID}},
		{"params", `UPDATE rules SET params = '{"x":1}'::jsonb WHERE id = $1`, []any{ruleID}},
		{"message", `UPDATE rules SET message = 'mutated' WHERE id = $1`, []any{ruleID}},
		{"scope", `UPDATE rules SET scope = 'document' WHERE id = $1`, []any{ruleID}},
		{`"when"`, `UPDATE rules SET "when" = 'true' WHERE id = $1`, []any{ruleID}},
		{"rule_set_version_id", `UPDATE rules SET rule_set_version_id = $2 WHERE id = $1`, []any{ruleID, otherVersionID}},
	}
	for _, tc := range cases {
		t.Run(tc.col, func(t *testing.T) {
			_, err := app.Exec(ctx, tc.sql, tc.args...)
			if err == nil {
				t.Fatalf("UPDATE rules SET %s=...: want SQLSTATE 42501 (insufficient_privilege), got no error -- app must not be able to mutate rule content", tc.col)
			}
			assertSQLState(t, err, "42501")
		})
	}
}

// TestSchema_MultipleInactiveVersionsAllowed (QA addition): the mirror image of
// TestSchema_OneActiveVersionEnforced above. That test proves the partial unique index
// fires for is_active=true; this one proves it does NOT fire for is_active=false --
// i.e. it is genuinely partial (`WHERE is_active`), not a total unique index on some
// constant expression that would incorrectly cap the table at one row overall. Seeding
// several is_active=false rows must all succeed (seedVersion itself calls t.Fatalf on
// any insert error, so reaching the end of this test IS the assertion).
func TestSchema_MultipleInactiveVersionsAllowed(t *testing.T) {
	super, _ := dbTestPools(t)

	seedVersion(t, super, false)
	seedVersion(t, super, false)
	seedVersion(t, super, false)
}

// TestSchema_RulesCascadeOnVersionDelete (QA addition): rules.rule_set_version_id is
// `REFERENCES rule_set_versions(id) ON DELETE CASCADE` -- deleting a version must
// delete every rule under it too. Asserted directly here (not inferred from
// seedVersion's own Cleanup, whose delete errors are swallowed with `_, _ =`), so a
// regression to ON DELETE RESTRICT/NO ACTION shows up as a test failure instead of a
// silently-failing Cleanup.
func TestSchema_RulesCascadeOnVersionDelete(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, false)
	ruleID := seedRule(t, super, versionID, "cascade-probe")

	if _, err := super.Exec(ctx, `DELETE FROM rule_set_versions WHERE id = $1`, versionID); err != nil {
		t.Fatalf("DELETE FROM rule_set_versions: %v", err)
	}

	var exists bool
	if err := super.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM rules WHERE id = $1)`, ruleID,
	).Scan(&exists); err != nil {
		t.Fatalf("check rule survival: %v", err)
	}
	if exists {
		t.Error("rule still exists after its rule_set_versions row was deleted -- ON DELETE CASCADE did not fire")
	}
}

// TestSchema_CheckConstraintsRejectInvalidEnums (QA addition): the CHECK constraints on
// rules.type, rules.severity, and rules.scope must reject out-of-list values with
// check_violation (23514). Run as superuser (BYPASSRLS, full grants) so a rejection can
// only be the CHECK firing -- not a grant denial masquerading as one (same isolation
// rationale as TestSchema_OneActiveVersionEnforced's doc comment).
func TestSchema_CheckConstraintsRejectInvalidEnums(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	versionID, _ := seedVersion(t, super, false)

	cases := []struct {
		name, sql string
	}{
		{"type", `INSERT INTO rules (rule_set_version_id, key, type, severity, message)
			VALUES ($1, 'bad-type-probe', 'not_a_real_type', 'error', 'x')`},
		{"severity", `INSERT INTO rules (rule_set_version_id, key, type, severity, message)
			VALUES ($1, 'bad-severity-probe', 'required', 'not_a_real_severity', 'x')`},
		{"scope", `INSERT INTO rules (rule_set_version_id, key, type, severity, message, scope)
			VALUES ($1, 'bad-scope-probe', 'required', 'error', 'x', 'not_document')`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := super.Exec(ctx, tc.sql, versionID)
			if err == nil {
				t.Fatalf("INSERT rules with invalid %s: want SQLSTATE 23514 (check_violation), got no error", tc.name)
			}
			assertSQLState(t, err, "23514")
		})
	}
}
