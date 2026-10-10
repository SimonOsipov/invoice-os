// M3-04-01 (Test-first: yes) — schema/grant contract tests for the two GLOBAL reference
// tables the M3-04-01 migration introduces: rule_set_versions and rules. Written BEFORE
// the migration exists, so this suite is RED against `undefined_table` (SQLSTATE 42P01)
// until the migration lands. It mirrors the `roles` precedent
// (migrations/20260709151759_roles.sql): no tenant_id, no RLS — every tenant sees the
// same rule content, so isolation here is a plain GRANT contract, not RLS.
//
// Coverage (see M3-04-01 Test Specs):
//  1. TestSchema_AppCannotMutateContent      — app cannot UPDATE key/severity/type (42501).
//  2. TestSchema_AppCannotToggleEnabled      — app cannot UPDATE the enabled column (42501).
//  3. TestSchema_AppCannotInsertVersionOrRule — app has no INSERT grant on either table (42501).
//  4. TestSchema_AppCannotDeleteRule         — app has no DELETE grant on either table (42501).
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
	"fmt"
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
// 5432 DB. sealAndDate's throwaway cleanup (below) necessarily seals its fixture row
// BEFORE deleting it -- a hard abort in that window (a `go test -timeout` kill, a panic in
// another goroutine, SIGKILL) can leave a SEALED, possibly dated orphan that a plain
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
// left behind. Sealed orphans resist a plain DELETE (Guard C), so it brackets the
// delete in DISABLE/ENABLE TRIGGER USER. Targets
// ONLY fixtureNotes-tagged rows (never the v1/v2 seeds) -- fixtureNotes is already a
// fixed, greppable const (this file) shared across runs, so no marker redefinition is
// needed. Triple-guarded: the fixtureNotes match, the explicit `version NOT IN (1,2)`
// (belt-and-suspenders), and the fact every fixture row uses nextVersion() values >=
// 900001 (above), which can never collide with v1/v2. Best-effort: a connection or query
// failure here just means the sweep no-ops -- it is a safety net for ABNORMAL aborts, not
// the primary teardown (sealAndDate's per-fixture committed delete is).
func sweepOrphanFixtures(superURL string) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, superURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweepOrphanFixtures: connect superuser: %v\n", err)
		return
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweepOrphanFixtures: begin tx: %v\n", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Best-effort: keep attempting every step even if one fails, but remember the FIRST
	// error so an abnormal sweep failure surfaces on stderr instead of silently leaving
	// the shared DB unrecovered (which would defeat the self-heal this sweep exists for).
	var firstErr error
	record := func(op string, err error) {
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", op, err)
		}
	}
	_, err = tx.Exec(ctx, `ALTER TABLE rule_set_versions DISABLE TRIGGER USER`)
	record("disable rule_set_versions triggers", err)
	_, err = tx.Exec(ctx, `ALTER TABLE rules DISABLE TRIGGER USER`)
	record("disable rules triggers", err)
	_, err = tx.Exec(ctx, `DELETE FROM rule_set_versions WHERE notes = $1 AND version NOT IN (1, 2)`, fixtureNotes)
	record("delete orphan fixtures", err)
	_, err = tx.Exec(ctx, `ALTER TABLE rules ENABLE TRIGGER USER`)
	record("enable rules triggers", err)
	_, err = tx.Exec(ctx, `ALTER TABLE rule_set_versions ENABLE TRIGGER USER`)
	record("enable rule_set_versions triggers", err)
	if err := tx.Commit(ctx); err != nil {
		record("commit", err)
	}
	if firstErr != nil {
		fmt.Fprintf(os.Stderr, "sweepOrphanFixtures: best-effort pre-flight sweep hit an error (shared DB may need manual cleanup): %v\n", firstErr)
	}
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

// seedVersion inserts one unsealed, undated rule_set_versions row as the superuser and
// registers its cleanup. Every fixture row is tagged with fixtureNotes. Rules seeded under
// it go with it (rules.rule_set_version_id is ON DELETE CASCADE); sealAndDate takes over
// the teardown once the row is sealed.
func seedVersion(t *testing.T, super *pgxpool.Pool) (id string, version int) {
	t.Helper()
	ctx := context.Background()
	version = nextVersion()

	if err := super.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, sealed, notes) VALUES ($1, false, $2) RETURNING id`,
		version, fixtureNotes,
	).Scan(&id); err != nil {
		t.Fatalf("seed rule_set_versions(version=%d): %v", version, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM rule_set_versions WHERE id = $1`, id)
	})

	return id, version
}

// sealAndDate seals a fixture version and gives it a start date in one UPDATE, so it is
// in force from `from` (YYYY-MM-DD). Insert all its rules first (Guard A). Cleanup
// deletes it with user triggers disabled. Dates in year 3001+
// keep real versions out of the way; use todayUTC() to make the fixture today's version.
func sealAndDate(t *testing.T, super *pgxpool.Pool, versionID, from string) {
	t.Helper()
	ctx := context.Background()

	t.Cleanup(func() {
		ctx := context.Background()
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Errorf("sealAndDate cleanup(versionID=%s): begin delete tx: %v", versionID, err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, q := range []string{
			`ALTER TABLE rule_set_versions DISABLE TRIGGER USER`,
			`ALTER TABLE rules DISABLE TRIGGER USER`,
			`DELETE FROM rule_set_versions WHERE id = '` + versionID + `'`,
			`ALTER TABLE rules ENABLE TRIGGER USER`,
			`ALTER TABLE rule_set_versions ENABLE TRIGGER USER`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Errorf("sealAndDate cleanup(versionID=%s): %s: %v", versionID, q, err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("sealAndDate cleanup(versionID=%s): commit: %v", versionID, err)
		}
	})

	if _, err := super.Exec(ctx,
		`UPDATE rule_set_versions SET sealed = true, effective_from = $2::date WHERE id = $1`, versionID, from,
	); err != nil {
		t.Fatalf("sealAndDate(versionID=%s, from=%s): %v", versionID, from, err)
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

// assertAppRefused asserts err is a 42501 refusal.
func assertAppRefused(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s as invoice_app: want SQLSTATE 42501, got no error", what)
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("%s as invoice_app: want SQLSTATE 42501, got %v", what, err)
	}
}

// TestSchema_AppCannotMutateContent (Test Spec #1): as invoice_app, an UPDATE naming any
// content column (key, severity, type — anything other than enabled) must fail with
// insufficient_privilege (42501). The app holds SELECT only on rules; the kill switch runs
// as the owner (TestKillSwitch_OnlyTheOwnerCanRunIt).
func TestSchema_AppCannotMutateContent(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super)
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

// TestSchema_AppCannotToggleEnabled: as invoice_app, UPDATE rules SET enabled fails with
// 42501 and the row is unchanged. A fixture rule, not a seeded row.
func TestSchema_AppCannotToggleEnabled(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super)
	ruleID := seedRule(t, super, versionID, "enabled-toggle-probe")

	_, err := app.Exec(ctx, `UPDATE rules SET enabled = false WHERE id = $1`, ruleID)
	assertAppRefused(t, err, "UPDATE rules SET enabled = false")

	var enabled bool
	if err := super.QueryRow(ctx, `SELECT enabled FROM rules WHERE id = $1`, ruleID).Scan(&enabled); err != nil {
		t.Fatalf("read back enabled: %v", err)
	}
	if !enabled {
		t.Error("enabled = false after the app's UPDATE, want true unchanged")
	}
}

// TestSchema_AppCannotLockRuleRows: SELECT ... FOR UPDATE needs UPDATE on a column, so it is
// refused with 42501 once the app holds no UPDATE grant on rules.
func TestSchema_AppCannotLockRuleRows(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM rules WHERE key = 'vat-standard-rate' FOR UPDATE`).Scan(&id)
	assertAppRefused(t, err, "SELECT ... FROM rules FOR UPDATE")
}

// TestSchema_AppHoldsNoWritePrivilegeOnRules: no column or table write privilege on rules.
func TestSchema_AppHoldsNoWritePrivilegeOnRules(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	var colUpdate bool
	if err := super.QueryRow(ctx,
		`SELECT has_column_privilege('invoice_app', 'public.rules', 'enabled', 'UPDATE')`,
	).Scan(&colUpdate); err != nil {
		t.Fatalf("has_column_privilege: %v", err)
	}
	if colUpdate {
		t.Error("has_column_privilege(invoice_app, rules.enabled, UPDATE) = true, want false")
	}
	for _, p := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
		var has bool
		if err := super.QueryRow(ctx,
			`SELECT has_table_privilege('invoice_app', 'public.rules', $1)`, p,
		).Scan(&has); err != nil {
			t.Fatalf("has_table_privilege(%s): %v", p, err)
		}
		if has {
			t.Errorf("has_table_privilege(invoice_app, rules, %s) = true, want false", p)
		}
	}
}

// TestSchema_OnlyTheOwnerCanWriteRules: no role but the owner and superusers can write rules,
// directly, through PUBLIC, or by SET ROLE to the owner.
func TestSchema_OnlyTheOwnerCanWriteRules(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	rows, err := super.Query(ctx, `
		SELECT rolname,
		       has_column_privilege(oid, 'public.rules', 'enabled', 'UPDATE'),
		       has_table_privilege(oid, 'public.rules', 'INSERT'),
		       has_table_privilege(oid, 'public.rules', 'UPDATE'),
		       has_table_privilege(oid, 'public.rules', 'DELETE'),
		       has_table_privilege(oid, 'public.rules', 'TRUNCATE'),
		       pg_has_role(oid, 'invoice_migrator', 'USAGE')
		FROM pg_roles
		WHERE NOT rolsuper AND rolname !~ '^pg_' AND rolname <> 'invoice_migrator'
		ORDER BY rolname`)
	if err != nil {
		t.Fatalf("read non-owner roles: %v", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		var col, ins, upd, del, trunc, asOwner bool
		if err := rows.Scan(&name, &col, &ins, &upd, &del, &trunc, &asOwner); err != nil {
			t.Fatalf("scan role: %v", err)
		}
		seen[name] = true
		for what, held := range map[string]bool{
			"UPDATE rules.enabled": col, "INSERT rules": ins, "UPDATE rules": upd, "DELETE rules": del,
			"TRUNCATE rules": trunc, "SET ROLE invoice_migrator": asOwner,
		} {
			if held {
				t.Errorf("role %s can %s, want only the owner to write rules", name, what)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate roles: %v", err)
	}
	for _, want := range []string{"invoice_app", "invoice_tenant_reader"} {
		if !seen[want] {
			t.Errorf("role %s missing from the audited set %v: the check would be vacuous", want, seen)
		}
	}

	for _, p := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
		var has bool
		if err := super.QueryRow(ctx,
			`SELECT has_table_privilege('public', 'public.rules', $1)`, p,
		).Scan(&has); err != nil {
			t.Fatalf("has_table_privilege(PUBLIC, %s): %v", p, err)
		}
		if has {
			t.Errorf("PUBLIC can %s rules, want false", p)
		}
	}
	var publicCol bool
	if err := super.QueryRow(ctx,
		`SELECT has_column_privilege('public', 'public.rules', 'enabled', 'UPDATE')`,
	).Scan(&publicCol); err != nil {
		t.Fatalf("has_column_privilege(PUBLIC): %v", err)
	}
	if publicCol {
		t.Error("PUBLIC can UPDATE rules.enabled, want false")
	}
}

// TestSchema_AppKeepsReadOnRules: the revoke leaves SELECT on both tables and the global
// loader working.
func TestSchema_AppKeepsReadOnRules(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	for _, table := range []string{"rules", "rule_set_versions"} {
		var has bool
		if err := super.QueryRow(ctx,
			`SELECT has_table_privilege('invoice_app', $1, 'SELECT')`, "public."+table,
		).Scan(&has); err != nil {
			t.Fatalf("has_table_privilege(%s): %v", table, err)
		}
		if !has {
			t.Errorf("has_table_privilege(invoice_app, %s, SELECT) = false, want true", table)
		}
	}

	rs, err := NewStore(app).LoadActiveRuleSetGlobal(ctx)
	if err != nil {
		t.Fatalf("LoadActiveRuleSetGlobal: %v", err)
	}
	if len(rs.Rules) == 0 {
		t.Error("LoadActiveRuleSetGlobal returned no rules, want the seeded active set")
	}
}

// TestSchema_AppCannotInsertVersionOrRule (Test Spec #3): as invoice_app, INSERT into
// either table must fail with insufficient_privilege (42501) -- the app has SELECT only
// on both, no INSERT grant.
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
		versionID, _ := seedVersion(t, super)
		_, err := app.Exec(ctx,
			`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
			 VALUES ($1, 'app-insert-probe', 'required', 'error', 'should be rejected')`,
			versionID,
		)
		if err == nil {
			t.Fatal("INSERT INTO rules: want SQLSTATE 42501 (insufficient_privilege), got no error -- app has SELECT only, no INSERT")
		}
		assertSQLState(t, err, "42501")
	})
}

// TestSchema_AppCannotDeleteRule (Test Spec #4): as invoice_app, DELETE from either table
// must fail with insufficient_privilege (42501) -- rule content is immutable and
// versions are permanent once published; only SELECT is granted.
func TestSchema_AppCannotDeleteRule(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	t.Run("rules", func(t *testing.T) {
		versionID, _ := seedVersion(t, super)
		ruleID := seedRule(t, super, versionID, "delete-rule-probe")
		_, err := app.Exec(ctx, `DELETE FROM rules WHERE id = $1`, ruleID)
		if err == nil {
			t.Fatal("DELETE FROM rules: want SQLSTATE 42501 (insufficient_privilege), got no error")
		}
		assertSQLState(t, err, "42501")
	})

	t.Run("rule_set_versions", func(t *testing.T) {
		versionID, _ := seedVersion(t, super)
		_, err := app.Exec(ctx, `DELETE FROM rule_set_versions WHERE id = $1`, versionID)
		if err == nil {
			t.Fatal("DELETE FROM rule_set_versions: want SQLSTATE 42501 (insufficient_privilege), got no error")
		}
		assertSQLState(t, err, "42501")
	})
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
//  9. TestSchema_RulesCascadeOnVersionDelete — the FK's ON DELETE CASCADE, asserted
//     directly rather than relying on seedVersion's own (error-swallowing) Cleanup.
// 10. TestSchema_CheckConstraintsRejectInvalidEnums — the three CHECK constraints
//     (type, severity, scope) reject out-of-list values with 23514.

// TestSchema_AppCannotMutateRemainingContentColumns (QA addition): invoice_app holds no
// UPDATE grant on rules, so every content column other than key/severity/type (covered by
// TestSchema_AppCannotMutateContent) fails 42501: target, params, message, scope, "when",
// and the rule_set_version_id FK itself.
func TestSchema_AppCannotMutateRemainingContentColumns(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super)
	ruleID := seedRule(t, super, versionID, "content-immutable-remaining-probe")
	otherVersionID, _ := seedVersion(t, super)

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

// TestSchema_RulesCascadeOnVersionDelete (QA addition): rules.rule_set_version_id is
// `REFERENCES rule_set_versions(id) ON DELETE CASCADE` -- deleting a version must
// delete every rule under it too. Asserted directly here (not inferred from
// seedVersion's own Cleanup, whose delete errors are swallowed with `_, _ =`), so a
// regression to ON DELETE RESTRICT/NO ACTION shows up as a test failure instead of a
// silently-failing Cleanup.
func TestSchema_RulesCascadeOnVersionDelete(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super)
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
// rationale as the other superuser-run tests in this file).
func TestSchema_CheckConstraintsRejectInvalidEnums(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	versionID, _ := seedVersion(t, super)

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
