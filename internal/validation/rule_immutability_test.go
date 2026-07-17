// M4-17-01 (Test-first: yes) -- Mode A RED specs for the rule-set
// immutability lock. Transcribes the M4-17 story's Test Specs table
// (RIL-01..14) into runnable Go tests, authored BEFORE:
//
//	(a) migrations/20260717120000_rule_immutability_lock.sql exists (no
//	    `sealed` column, no Guard A/B/C triggers); and
//	(b) the two reversibility fixtures (seed_test.go's
//	    TestSeed_ReversibilityRollback, rule_set_v2_test.go's
//	    TestRuleSetV2_DownRestoresV1) are reworked to disable USER triggers --
//	    both untouched by this file.
//
// See the M4-17 story (Obsidian, "Simon Vault/Projects/FiscalBridge Africa/
// User Stories/M4/M4-17 Rule-set Immutability Trigger.md") for the
// authoritative Objective/Acceptance Criteria/System Design/Test Specs this
// file transcribes, and schema_test.go / seed_test.go / store.go for the
// harness conventions (dbTestPools, seedVersion, assertSQLState, the
// always-rolled-back-superuser-tx pattern) this file reuses and extends.
//
// RED SHAPE (why each RIL test fails today, and why that is the RIGHT
// reason -- not a compile error, a panic, or a skip):
//   - Specs whose Setup column names a SEALED version assert that
//     precondition first via requireSealed (mirroring this package's
//     existing "guard against vacuous pass" convention --
//     TestSeed_ReversibilityRollback, TestRuleSetV2_DownRestoresV1: assert
//     the precondition a spec's Setup requires is REALLY true before running
//     its Action). Pre-migration `sealed` does not exist, so this SELECT
//     itself fails with undefined_column (42703) -- RIL-03/07/08/09/10/11/12.
//   - Specs whose Action performs a now-forbidden op directly against
//     `rules`/`rule_set_versions` (no `sealed` reference in the op itself)
//     currently SUCCEED -- there is no trigger yet -- so the
//     assertSQLState(..., "23001") call fails loudly because the op returned
//     no error at all: RIL-01/02/04/05/06/13/14 (the exact
//     `line_rules`-style incident this story exists to close).
//
// LEAK-FREE DISCIPLINE (critical -- the shared 5433 DB persists across
// runs, and pre-migration the "rejected" ops above actually SUCCEED):
// every spec that mutates the REAL sealed v1/v2 runs inside an explicit
// transaction that is UNCONDITIONALLY rolled back via `defer tx.Rollback`,
// using a SAVEPOINT (pgx v5's pseudo-nested `tx.Begin`, attemptWithSavepoint
// below) around the one statement that may legitimately fail, so one abort
// never poisons the rest of the transaction's assertions. This is what makes
// RED-authoring safe even though the ops it is asserting against currently
// succeed: the outer transaction is never committed, so nothing persists
// EITHER WAY -- pre-migration (op succeeds, rolled back anyway) or
// post-migration (op rejected, nothing to roll back). RIL-03's enabled
// flip-and-flip-back is the one deliberate exception (self-cleaning, not
// tx-wrapped, matching the story's Testing Strategy).
//
// Run (same env gate as the rest of the package, plus the new
// DATABASE_MIGRATION_URL for the owner-proof migrator-role pool):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5433/invoice_os?sslmode=disable" \
//	go test -count=1 -run 'TestRIL' -v ./internal/validation/...
package validation

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// queryRower is satisfied by both *pgxpool.Pool and pgx.Tx -- lets this
// suite's small lookup/precondition helpers run identically whether the
// caller passes a bare pool or an already-open transaction.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// migratorPool returns the migrator-role (table owner) pool built from
// DATABASE_MIGRATION_URL, or skips the calling test when unset -- mirrors
// dbTestPools' env-gated skip and pool-construction pattern (schema_test.go).
// The owner-proof RIL specs must attack as this role: a regular BEFORE
// trigger's whole point is that it fires for the table owner too, which
// neither the super pool (bypasses RLS, but is not the owner) nor the app
// pool (blocked by grants before any trigger logic even runs) can prove.
func migratorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	migURL := os.Getenv("DATABASE_MIGRATION_URL")
	if migURL == "" {
		t.Skip("owner-proof migrator-role test skipped: set DATABASE_MIGRATION_URL " +
			"(e.g. postgres://invoice_migrator:migrator@localhost:5433/invoice_os?sslmode=disable)")
	}
	ctx := context.Background()

	m, err := pgxpool.New(ctx, migURL)
	if err != nil {
		t.Fatalf("connect migrator: %v", err)
	}
	t.Cleanup(m.Close)
	if err := m.Ping(ctx); err != nil {
		t.Fatalf("ping migrator (is the DB up and bootstrapped?): %v", err)
	}

	// CodeRabbit C2: self-assert this pool is really the non-superuser,
	// table-owning migrator -- not a mistakenly-supplied superuser DSN. A
	// superuser DSN would make every "owner-proof" assertion in this file
	// pass VACUOUSLY (a BEFORE trigger fires for a superuser too, but a
	// superuser also bypasses RLS/grants, so it would silently duplicate the
	// `super` pool's coverage rather than proving the trigger binds the
	// actual NOSUPERUSER table owner). Fail loudly, before returning the
	// pool, if any of the three checks below don't hold.
	var currentUser string
	if err := m.QueryRow(ctx, `SELECT current_user`).Scan(&currentUser); err != nil {
		t.Fatalf("read current_user on the migrator pool: %v", err)
	}
	if currentUser != "invoice_migrator" {
		t.Fatalf("migrator pool's current_user = %q, want %q -- DATABASE_MIGRATION_URL is not "+
			"connecting as the table-owning migrator role", currentUser, "invoice_migrator")
	}

	var isSuperuser bool
	if err := m.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&isSuperuser); err != nil {
		t.Fatalf("read rolsuper for %s: %v", currentUser, err)
	}
	if isSuperuser {
		t.Fatalf("migrator pool's role %q is a SUPERUSER -- DATABASE_MIGRATION_URL must point at the "+
			"NOSUPERUSER invoice_migrator role, or the owner-proof assertions in this file are "+
			"meaningless (a superuser bypasses everything the `super` pool already covers)", currentUser)
	}

	for _, table := range []string{"rules", "rule_set_versions"} {
		var owner string
		if err := m.QueryRow(ctx,
			`SELECT tableowner FROM pg_tables WHERE schemaname = 'public' AND tablename = $1`, table,
		).Scan(&owner); err != nil {
			t.Fatalf("read tableowner for %s: %v", table, err)
		}
		if owner != currentUser {
			t.Fatalf("table %s is owned by %q, want %q (the migrator pool) -- the owner-proof "+
				"assertions in this file require the migrator to actually OWN the tables it attacks",
				table, owner, currentUser)
		}
	}

	return m
}

// versionIDByVersion resolves a rule_set_versions.id by its version number,
// over pool or an open tx -- targets the REAL migration-seeded v1/v2 rows
// without hardcoding their uuid.
func versionIDByVersion(t *testing.T, ctx context.Context, q queryRower, version int) string {
	t.Helper()
	var id string
	if err := q.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE version = $1`, version).Scan(&id); err != nil {
		t.Fatalf("read rule_set_versions.id WHERE version=%d: %v", version, err)
	}
	return id
}

// ruleIDByKey resolves a rules.id under versionID by its key.
func ruleIDByKey(t *testing.T, ctx context.Context, q queryRower, versionID, key string) string {
	t.Helper()
	var id string
	if err := q.QueryRow(ctx, `SELECT id FROM rules WHERE rule_set_version_id = $1 AND key = $2`, versionID, key).Scan(&id); err != nil {
		t.Fatalf("read rules.id WHERE rule_set_version_id=%s AND key=%q: %v", versionID, key, err)
	}
	return id
}

// requireSealed asserts version's sealed column equals want, Fatal'ing
// otherwise -- mirrors this package's existing "guard against vacuous pass"
// convention (seed_test.go's TestSeed_ReversibilityRollback,
// rule_set_v2_test.go's TestRuleSetV2_DownRestoresV1: assert a spec's Setup
// precondition actually holds before running its Action). Pre-migration this
// SELECT itself fails with undefined_column (42703) -- the expected RED
// reason for every RIL spec whose Setup column names a sealed version, ahead
// of the migration landing.
func requireSealed(t *testing.T, ctx context.Context, q queryRower, version int, want bool) {
	t.Helper()
	var got bool
	if err := q.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE version = $1`, version).Scan(&got); err != nil {
		t.Fatalf("read sealed for version=%d (want %t): %v -- expected the rule_immutability_lock migration's "+
			"`sealed` column to exist and this version to already carry it; pre-migration this is "+
			"undefined_column (42703)", version, want, err)
	}
	if got != want {
		t.Fatalf("rule_set_versions.sealed for version=%d = %t, want %t", version, got, want)
	}
}

// attemptWithSavepoint runs sql inside a SAVEPOINT nested in tx (pgx v5's
// pseudo-nested tx.Begin() issues SAVEPOINT / RELEASE SAVEPOINT / ROLLBACK TO
// SAVEPOINT under the hood), returning the exec error. On error, only the
// savepoint is rolled back -- undoing just this statement -- so tx itself
// stays usable for whatever post-condition assertions follow; on success the
// savepoint is released (folding the write into tx), which the caller's own
// `defer tx.Rollback` still discards in full regardless of the outcome here.
// This is what lets a single test assert "expect error" against an op that,
// pre-migration, actually SUCCEEDS (no guard yet) without either poisoning
// the rest of the transaction or leaking the successful write to the shared
// DB -- exactly the hazard the story's Testing Strategy calls out for RIL-07,
// generalized to every rejected-op spec in this file.
func attemptWithSavepoint(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	t.Helper()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("open savepoint: %v", err)
	}
	_, execErr := sp.Exec(ctx, sql, args...)
	if execErr != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			t.Fatalf("rollback savepoint after op error (%v): %v", execErr, rbErr)
		}
		return execErr
	}
	if commitErr := sp.Commit(ctx); commitErr != nil {
		t.Fatalf("release savepoint after op success: %v", commitErr)
	}
	return nil
}

// ---------------------------------------------------------------------
// RIL-01 -- incident-repro INSERT into sealed v1 rejected, as the OWNER.
// ---------------------------------------------------------------------

// TestRIL01_IncidentInsertIntoSealedRejectedAsOwner (RIL-01): mirrors the
// exact `line_rules` incident (migrations/20260715120000_line_rules.sql) --
// INSERTing a rule into the real, already-published v1 -- run as the
// MIGRATOR (table owner), the one role a grant-only defense cannot stop.
// Wrapped in an always-rolled-back tx + savepoint: pre-migration this INSERT
// actually succeeds (that IS the incident), so nothing may persist to the
// shared v1 either way.
func TestRIL01_IncidentInsertIntoSealedRejectedAsOwner(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	v1ID := versionIDByVersion(t, ctx, tx, 1)

	opErr := attemptWithSavepoint(t, ctx, tx,
		`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
		 VALUES ($1, 'ril-01-incident-probe', 'required', 'error', 'RIL-01 incident-repro probe')`,
		v1ID,
	)
	assertSQLState(t, opErr, "23001")

	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM rules WHERE rule_set_version_id = $1 AND key = $2`,
		v1ID, "ril-01-incident-probe",
	).Scan(&count); err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if count != 0 {
		t.Errorf("probe rule row count = %d, want 0 -- INSERT into sealed v1 must not persist", count)
	}
}

// ---------------------------------------------------------------------
// RIL-02 -- content UPDATE on a sealed rule rejected, super AND migrator.
// ---------------------------------------------------------------------

// TestRIL02_ContentUpdateOnSealedRejected (RIL-02): a content column
// (message) UPDATE on a real rule under sealed v2, as both super and
// migrator. Tx+savepoint wrapped: pre-migration this UPDATE actually
// succeeds against the real v2 rule.
func TestRIL02_ContentUpdateOnSealedRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	migrator := migratorPool(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"super", super}, {"migrator", migrator}} {
		t.Run(role.name, func(t *testing.T) {
			tx, err := role.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin %s tx: %v", role.name, err)
			}
			defer func() { _ = tx.Rollback(ctx) }()

			v2ID := versionIDByVersion(t, ctx, tx, 2)
			ruleID := ruleIDByKey(t, ctx, tx, v2ID, "vat-standard-rate")

			var before string
			if err := tx.QueryRow(ctx, `SELECT message FROM rules WHERE id = $1`, ruleID).Scan(&before); err != nil {
				t.Fatalf("read message before: %v", err)
			}

			opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rules SET message = 'ril-02-mutated' WHERE id = $1`, ruleID)
			assertSQLState(t, opErr, "23001")

			var after string
			if err := tx.QueryRow(ctx, `SELECT message FROM rules WHERE id = $1`, ruleID).Scan(&after); err != nil {
				t.Fatalf("read message after: %v", err)
			}
			if after != before {
				t.Errorf("message = %q after rejected content UPDATE, want unchanged %q", after, before)
			}
		})
	}
}

// ---------------------------------------------------------------------
// RIL-03 -- enabled-only UPDATE on a sealed rule ALLOWED (the carve-out).
// ---------------------------------------------------------------------

// TestRIL03_EnabledOnlyUpdateOnSealedAllowed (RIL-03): flips a real v2
// rule's `enabled` column and flips it back -- self-cleaning, per the
// story's Testing Strategy, so NOT tx-wrapped (the write is meant to
// persist across the flip/restore, matching the real kill-switch's shape).
// Precondition (requireSealed) makes this fail 42703 pre-migration, as the
// spec's Setup ("a rule under sealed v2") requires v2 to already be sealed.
func TestRIL03_EnabledOnlyUpdateOnSealedAllowed(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	requireSealed(t, ctx, super, 2, true)

	v2ID := versionIDByVersion(t, ctx, super, 2)
	ruleID := ruleIDByKey(t, ctx, super, v2ID, "vat-standard-rate")

	var original bool
	if err := super.QueryRow(ctx, `SELECT enabled FROM rules WHERE id = $1`, ruleID).Scan(&original); err != nil {
		t.Fatalf("read enabled before: %v", err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(), `UPDATE rules SET enabled = $1 WHERE id = $2`, original, ruleID); err != nil {
			t.Errorf("cleanup: restore enabled=%t for rule %s: %v", original, ruleID, err)
		}
	})

	tag, err := super.Exec(ctx, `UPDATE rules SET enabled = NOT enabled WHERE id = $1`, ruleID)
	if err != nil {
		t.Fatalf("UPDATE enabled (flip): want success (the kill-switch carve-out), got error: %v", err)
	}
	if got := tag.RowsAffected(); got != 1 {
		t.Fatalf("RowsAffected = %d, want 1", got)
	}

	var flipped bool
	if err := super.QueryRow(ctx, `SELECT enabled FROM rules WHERE id = $1`, ruleID).Scan(&flipped); err != nil {
		t.Fatalf("read enabled after flip: %v", err)
	}
	if flipped == original {
		t.Fatalf("enabled after flip = %t, want %t (flipped)", flipped, !original)
	}

	if _, err := super.Exec(ctx, `UPDATE rules SET enabled = $1 WHERE id = $2`, original, ruleID); err != nil {
		t.Fatalf("restore enabled=%t: %v", original, err)
	}
}

// ---------------------------------------------------------------------
// RIL-04 -- direct DELETE of a sealed rule rejected, super AND migrator.
// ---------------------------------------------------------------------

// TestRIL04_DirectDeleteOfSealedRuleRejected (RIL-04): DELETE of a single
// real rule under sealed v2 (the direct-child-delete path, distinct from
// RIL-13's cascade-parent-delete path), as both super and migrator.
// Tx+savepoint wrapped: pre-migration this DELETE actually succeeds.
func TestRIL04_DirectDeleteOfSealedRuleRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	migrator := migratorPool(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"super", super}, {"migrator", migrator}} {
		t.Run(role.name, func(t *testing.T) {
			tx, err := role.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin %s tx: %v", role.name, err)
			}
			defer func() { _ = tx.Rollback(ctx) }()

			v2ID := versionIDByVersion(t, ctx, tx, 2)
			ruleID := ruleIDByKey(t, ctx, tx, v2ID, "currency-allowed")

			opErr := attemptWithSavepoint(t, ctx, tx, `DELETE FROM rules WHERE id = $1`, ruleID)
			assertSQLState(t, opErr, "23001")

			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rules WHERE id = $1)`, ruleID).Scan(&exists); err != nil {
				t.Fatalf("check rule survival: %v", err)
			}
			if !exists {
				t.Error("rule no longer exists after rejected direct DELETE -- must survive")
			}
		})
	}
}

// ---------------------------------------------------------------------
// RIL-05 -- combined enabled+content UPDATE rejected.
// ---------------------------------------------------------------------

// TestRIL05_CombinedEnabledAndContentUpdateRejected (RIL-05): an UPDATE that
// touches BOTH `enabled` and a content column must be rejected in full --
// the carve-out is "enabled-only", not "enabled-among-others". Tx+savepoint
// wrapped: pre-migration this UPDATE actually succeeds against the real v2
// rule.
func TestRIL05_CombinedEnabledAndContentUpdateRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	v2ID := versionIDByVersion(t, ctx, tx, 2)
	ruleID := ruleIDByKey(t, ctx, tx, v2ID, "currency-required")

	var beforeEnabled bool
	var beforeMessage string
	if err := tx.QueryRow(ctx, `SELECT enabled, message FROM rules WHERE id = $1`, ruleID).Scan(&beforeEnabled, &beforeMessage); err != nil {
		t.Fatalf("read enabled/message before: %v", err)
	}

	opErr := attemptWithSavepoint(t, ctx, tx,
		`UPDATE rules SET enabled = NOT enabled, message = 'ril-05-mutated' WHERE id = $1`, ruleID)
	assertSQLState(t, opErr, "23001")

	var afterEnabled bool
	var afterMessage string
	if err := tx.QueryRow(ctx, `SELECT enabled, message FROM rules WHERE id = $1`, ruleID).Scan(&afterEnabled, &afterMessage); err != nil {
		t.Fatalf("read enabled/message after: %v", err)
	}
	if afterEnabled != beforeEnabled {
		t.Errorf("enabled = %t after rejected combined UPDATE, want unchanged %t", afterEnabled, beforeEnabled)
	}
	if afterMessage != beforeMessage {
		t.Errorf("message = %q after rejected combined UPDATE, want unchanged %q", afterMessage, beforeMessage)
	}
}

// ---------------------------------------------------------------------
// RIL-06 -- reparent-into-sealed rejected (the UPDATE-NEW-parent bypass).
// ---------------------------------------------------------------------

// TestRIL06_ReparentIntoSealedRejected (RIL-06): a rule under a fresh,
// UNSEALED throwaway version U is reparented (rule_set_version_id UPDATE)
// into sealed v2 -- must be rejected, closing the "insert under an unsealed
// draft, then UPDATE its parent to a sealed version" bypass. Entirely inside
// a rolled-back super tx (both U/R and the reparent attempt), per the
// story's Testing Strategy.
func TestRIL06_ReparentIntoSealedRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var uID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, notes) VALUES ($1, false, $2) RETURNING id`,
		nextVersion(), fixtureNotes,
	).Scan(&uID); err != nil {
		t.Fatalf("insert throwaway unsealed version: %v", err)
	}

	var rID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
		 VALUES ($1, 'ril-06-reparent-probe', 'required', 'error', 'RIL-06 probe') RETURNING id`,
		uID,
	).Scan(&rID); err != nil {
		t.Fatalf("insert rule under throwaway unsealed version: %v -- must succeed (unsealed versions accept inserts)", err)
	}

	v2ID := versionIDByVersion(t, ctx, tx, 2)

	opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rules SET rule_set_version_id = $1 WHERE id = $2`, v2ID, rID)
	assertSQLState(t, opErr, "23001")

	var currentParent string
	if err := tx.QueryRow(ctx, `SELECT rule_set_version_id FROM rules WHERE id = $1`, rID).Scan(&currentParent); err != nil {
		t.Fatalf("read rule's parent after rejected reparent: %v", err)
	}
	if currentParent != uID {
		t.Errorf("rule's rule_set_version_id = %s after rejected reparent, want still %s (the throwaway unsealed version)", currentParent, uID)
	}
}

// ---------------------------------------------------------------------
// RIL-07 -- the publish-new-version flow (insert unsealed -> seal -> post-
// seal INSERT rejected).
// ---------------------------------------------------------------------

// TestRIL07_PublishNewVersionFlowAllowed (RIL-07): the seal-on-publish
// shape -- INSERT a version (born unsealed) -> INSERT its rules -> seal it
// -- must all succeed; a post-seal INSERT under it must then be rejected.
// Entirely inside a rolled-back super tx, with a savepoint around the final
// expected-failure INSERT. Pre-migration this fails at the seal step itself
// (`sealed` column undefined, 42703).
func TestRIL07_PublishNewVersionFlowAllowed(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, notes) VALUES ($1, false, $2) RETURNING id`,
		nextVersion(), fixtureNotes,
	).Scan(&newID); err != nil {
		t.Fatalf("insert draft version: %v", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
		 VALUES ($1, 'ril-07-rule-a', 'required', 'error', 'RIL-07 pre-seal rule')`,
		newID,
	); err != nil {
		t.Fatalf("insert rule into unsealed draft version: %v -- must succeed", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET sealed = true WHERE id = $1`, newID); err != nil {
		t.Fatalf("seal the draft version (sealed false->true): %v -- expected success "+
			"(this IS the publish step's last action; pre-migration `sealed` is undefined_column, 42703)", err)
	}

	opErr := attemptWithSavepoint(t, ctx, tx,
		`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
		 VALUES ($1, 'ril-07-rule-b-post-seal', 'required', 'error', 'RIL-07 post-seal rule')`,
		newID,
	)
	assertSQLState(t, opErr, "23001")

	var postSealCount int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM rules WHERE rule_set_version_id = $1 AND key = 'ril-07-rule-b-post-seal'`, newID,
	).Scan(&postSealCount); err != nil {
		t.Fatalf("count post-seal probe rows: %v", err)
	}
	if postSealCount != 0 {
		t.Errorf("post-seal probe rule count = %d, want 0 -- INSERT after sealing must not persist", postSealCount)
	}
}

// ---------------------------------------------------------------------
// RIL-08 -- v1 & v2 sealed after migration (the retroactive seal).
// ---------------------------------------------------------------------

// TestRIL08_V1AndV2AreSealed (RIL-08): the migration's retroactive seal
// (`UPDATE rule_set_versions SET sealed = true WHERE version IN (1, 2)`)
// must have run -- both must read sealed=true. Read-only (no tx wrap
// needed); pre-migration this is 42703 (undefined_column).
func TestRIL08_V1AndV2AreSealed(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	for _, v := range []int{1, 2} {
		t.Run(fmt.Sprintf("version=%d", v), func(t *testing.T) {
			var sealed bool
			if err := app.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE version = $1`, v).Scan(&sealed); err != nil {
				t.Fatalf("read sealed for version=%d: %v", v, err)
			}
			if !sealed {
				t.Errorf("rule_set_versions.sealed for version=%d = false, want true (retroactive seal)", v)
			}
		})
	}
}

// ---------------------------------------------------------------------
// RIL-09 -- seal is irreversible: unseal rejected, super AND migrator.
// ---------------------------------------------------------------------

// TestRIL09_UnsealRejected (RIL-09): `UPDATE rule_set_versions SET
// sealed=false WHERE version=2` must be rejected -- as both super and
// migrator -- and v2 must still read sealed=true. The mutating statement
// itself references `sealed`, so pre-migration this is 42703
// (undefined_column) directly at the attempted UPDATE.
func TestRIL09_UnsealRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	migrator := migratorPool(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"super", super}, {"migrator", migrator}} {
		t.Run(role.name, func(t *testing.T) {
			tx, err := role.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin %s tx: %v", role.name, err)
			}
			defer func() { _ = tx.Rollback(ctx) }()

			opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rule_set_versions SET sealed = false WHERE version = 2`)
			assertSQLState(t, opErr, "23001")

			var stillSealed bool
			if err := tx.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE version = 2`).Scan(&stillSealed); err != nil {
				t.Fatalf("read sealed after rejected unseal: %v", err)
			}
			if !stillSealed {
				t.Error("v2.sealed = false after a rejected unseal attempt, want still true (sealing is irreversible)")
			}
		})
	}
}

// ---------------------------------------------------------------------
// RIL-10 -- is_active flip still allowed on sealed versions.
// ---------------------------------------------------------------------

// TestRIL10_IsActiveFlipAllowedOnSealed (RIL-10): the legitimate `is_active`
// activation flip (deactivate v2, activate v1) must remain legal even
// though both versions are sealed -- Guard C's UPDATE branch only rejects an
// unseal transition, never touches is_active. Rolled-back super tx.
// Precondition (requireSealed) makes this 42703 pre-migration, per the
// spec's Setup ("sealed v1 (inactive) + v2 (active)").
func TestRIL10_IsActiveFlipAllowedOnSealed(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	requireSealed(t, ctx, tx, 1, true)
	requireSealed(t, ctx, tx, 2, true)

	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE version = 2`); err != nil {
		t.Fatalf("deactivate sealed v2: %v -- is_active flips must remain legal on a sealed version", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE version = 1`); err != nil {
		t.Fatalf("activate sealed v1: %v -- is_active flips must remain legal on a sealed version", err)
	}

	var v1Active, v2Active bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM rule_set_versions WHERE version = 1`).Scan(&v1Active); err != nil {
		t.Fatalf("read v1.is_active: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT is_active FROM rule_set_versions WHERE version = 2`).Scan(&v2Active); err != nil {
		t.Fatalf("read v2.is_active: %v", err)
	}
	if !v1Active {
		t.Error("v1.is_active = false after the flip, want true")
	}
	if v2Active {
		t.Error("v2.is_active = true after the flip, want false")
	}
}

// ---------------------------------------------------------------------
// RIL-11 -- seal false->true and true->true (no-op) both allowed.
// ---------------------------------------------------------------------

// TestRIL11_SealFalseToTrueAndNoOpAllowed (RIL-11): a fresh throwaway
// version is born unsealed (sealed=false, the default), then sealed
// (false->true, must succeed), then sealed again (true->true, a no-op, must
// also succeed -- only true->false is rejected, per Guard C). Rolled-back
// super tx. Pre-migration this fails 42703 at the very first `SELECT
// sealed` (the column does not exist yet to even carry a default).
func TestRIL11_SealFalseToTrueAndNoOpAllowed(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, notes) VALUES ($1, false, $2) RETURNING id`,
		nextVersion(), fixtureNotes,
	).Scan(&id); err != nil {
		t.Fatalf("insert throwaway version: %v", err)
	}

	var bornSealed bool
	if err := tx.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE id = $1`, id).Scan(&bornSealed); err != nil {
		t.Fatalf("read sealed on freshly-inserted version: %v -- new versions must be born unsealed (DEFAULT false)", err)
	}
	if bornSealed {
		t.Fatalf("sealed on a freshly-inserted version = true, want false (born unsealed)")
	}

	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET sealed = true WHERE id = $1`, id); err != nil {
		t.Fatalf("seal false->true: %v -- must succeed", err)
	}
	var sealedNow bool
	if err := tx.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE id = $1`, id).Scan(&sealedNow); err != nil {
		t.Fatalf("read sealed after false->true: %v", err)
	}
	if !sealedNow {
		t.Error("sealed after false->true = false, want true")
	}

	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET sealed = true WHERE id = $1`, id); err != nil {
		t.Fatalf("seal true->true (no-op): %v -- must succeed", err)
	}
}

// ---------------------------------------------------------------------
// RIL-12 -- the production kill-switch path (store.ToggleRule) unbroken.
// ---------------------------------------------------------------------

// TestRIL12_KillSwitchProductionPathUnbroken (RIL-12): the real production
// Store.ToggleRule (store.go:229) must still succeed against the sealed
// active version -- flip a known rule's enabled value and flip it back,
// round-tripping and writing exactly one audit row per call (M3-06).
// Precondition (requireSealed) makes this 42703 pre-migration, per the
// spec's Setup ("sealed active v2 with a known rule"). Restores the rule's
// original enabled state in Cleanup unconditionally, mirroring
// TestSeed_KillSwitch.
//
// CodeRabbit C3: toggles to `!original` then back to `original`, never a
// hardcoded false-then-true -- ToggleRule returns ErrRedundantTransition
// when the rule's current value already equals the requested target
// (store.go:259-261), so a hardcoded false-then-true would spuriously
// t.Fatalf if a prior run (or a leaked fixture on the shared 5433 DB)
// already left the rule disabled. Also asserts the audit count against a
// captured BASELINE for an EXACT delta of 2, not a bare `>= 2`: audit_log
// is append-only and shared across every run against this DB, so `>= 2`
// would pass vacuously even if THIS run wrote zero new rows, as long as a
// prior run already left two or more behind.
func TestRIL12_KillSwitchProductionPathUnbroken(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	requireSealed(t, ctx, super, 2, true)

	const key = "vat-standard-rate"
	var original bool
	if err := super.QueryRow(ctx,
		`SELECT r.enabled FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		 WHERE v.is_active AND r.key = $1`, key,
	).Scan(&original); err != nil {
		t.Fatalf("read original enabled for %s: %v", key, err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(),
			`UPDATE rules r SET enabled = $1 FROM rule_set_versions v
			 WHERE r.rule_set_version_id = v.id AND v.is_active AND r.key = $2`,
			original, key,
		); err != nil {
			t.Errorf("cleanup: restore %s enabled=%t: %v", key, original, err)
		}
	})

	var auditBaseline int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log
		 WHERE event IN ('validation.rule.disabled', 'validation.rule.enabled') AND payload->>'key' = $1`,
		key,
	).Scan(&auditBaseline); err != nil {
		t.Fatalf("count baseline audit_log rows for %s: %v", key, err)
	}

	identityCtx := newTestIdentity()
	store := NewStore(app)

	flipped := !original
	if _, err := store.ToggleRule(identityCtx, key, flipped); err != nil {
		t.Fatalf("ToggleRule(%s, %t) against the sealed active version: %v", key, flipped, err)
	}
	var afterFlip bool
	if err := super.QueryRow(ctx,
		`SELECT r.enabled FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		 WHERE v.is_active AND r.key = $1`, key,
	).Scan(&afterFlip); err != nil {
		t.Fatalf("read enabled after first flip: %v", err)
	}
	if afterFlip != flipped {
		t.Errorf("enabled = %t after ToggleRule(%t), want %t", afterFlip, flipped, flipped)
	}

	if _, err := store.ToggleRule(identityCtx, key, original); err != nil {
		t.Fatalf("ToggleRule(%s, %t) (restore) against the sealed active version: %v", key, original, err)
	}
	var afterRestore bool
	if err := super.QueryRow(ctx,
		`SELECT r.enabled FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		 WHERE v.is_active AND r.key = $1`, key,
	).Scan(&afterRestore); err != nil {
		t.Fatalf("read enabled after restore flip: %v", err)
	}
	if afterRestore != original {
		t.Errorf("enabled = %t after ToggleRule(%t) (restore), want %t", afterRestore, original, original)
	}

	var auditAfter int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log
		 WHERE event IN ('validation.rule.disabled', 'validation.rule.enabled') AND payload->>'key' = $1`,
		key,
	).Scan(&auditAfter); err != nil {
		t.Fatalf("count audit_log rows for %s: %v", key, err)
	}
	if want := auditBaseline + 2; auditAfter != want {
		t.Errorf("audit_log rows for key=%s = %d, want exactly %d (baseline %d + one per ToggleRule call)",
			key, auditAfter, want, auditBaseline)
	}
}

// ---------------------------------------------------------------------
// RIL-13 -- cascade parent-DELETE of a sealed version rejected (F1), super
// AND migrator.
// ---------------------------------------------------------------------

// TestRIL13_CascadeParentDeleteOfSealedVersionRejected (RIL-13): the F1
// blocker -- `DELETE FROM rule_set_versions WHERE version = 1` (and =2), the
// real FK-cascade path -- must be rejected by the version-row guard (Guard
// C) BEFORE any cascade to `rules` runs, as both super and migrator. The
// most destructive spec in this file: pre-migration there is no guard at
// all, so this DELETE actually cascades and would permanently wipe the real
// seeded version + every one of its rules -- hence the mandatory
// tx+savepoint wrap (defer tx.Rollback always fires, so nothing persists
// either way).
func TestRIL13_CascadeParentDeleteOfSealedVersionRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	migrator := migratorPool(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"super", super}, {"migrator", migrator}} {
		for _, v := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/version=%d", role.name, v), func(t *testing.T) {
				tx, err := role.pool.Begin(ctx)
				if err != nil {
					t.Fatalf("begin %s tx: %v", role.name, err)
				}
				defer func() { _ = tx.Rollback(ctx) }()

				var preRuleCount int
				if err := tx.QueryRow(ctx,
					`SELECT count(*) FROM rules r JOIN rule_set_versions rv ON rv.id = r.rule_set_version_id WHERE rv.version = $1`, v,
				).Scan(&preRuleCount); err != nil {
					t.Fatalf("count rules under version=%d before DELETE: %v", v, err)
				}
				if preRuleCount == 0 {
					t.Fatalf("count(rules under version=%d) = 0 before DELETE, want > 0 -- nothing to protect "+
						"(has the DB been migrated via `make migrate-up`?)", v)
				}

				opErr := attemptWithSavepoint(t, ctx, tx, `DELETE FROM rule_set_versions WHERE version = $1`, v)
				assertSQLState(t, opErr, "23001")

				var versionSurvives bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rule_set_versions WHERE version = $1)`, v).Scan(&versionSurvives); err != nil {
					t.Fatalf("check version survival: %v", err)
				}
				if !versionSurvives {
					t.Errorf("version=%d row gone after rejected DELETE, want present", v)
				}

				var postRuleCount int
				if err := tx.QueryRow(ctx,
					`SELECT count(*) FROM rules r JOIN rule_set_versions rv ON rv.id = r.rule_set_version_id WHERE rv.version = $1`, v,
				).Scan(&postRuleCount); err != nil {
					t.Fatalf("count rules under version=%d after DELETE: %v", v, err)
				}
				if postRuleCount != preRuleCount {
					t.Errorf("rules under version=%d after rejected DELETE = %d, want %d (unchanged -- the FK "+
						"cascade must never have run)", v, postRuleCount, preRuleCount)
				}
			})
		}
	}
}

// ---------------------------------------------------------------------
// RIL-14 -- TRUNCATE rules rejected (F2), super AND migrator.
// ---------------------------------------------------------------------

// TestRIL14_TruncateRulesRejected (RIL-14): `TRUNCATE rules` must be
// rejected -- row triggers never fire on TRUNCATE, so this is Guard B's
// statement-level trigger, tested as both super and migrator. Pre-migration
// TRUNCATE actually wipes the ENTIRE table (worse than the `line_rules`
// incident), so this is wrapped in an always-rolled-back tx -- TRUNCATE is
// transactional in Postgres, so the wrap is safe and sufficient.
func TestRIL14_TruncateRulesRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	migrator := migratorPool(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"super", super}, {"migrator", migrator}} {
		t.Run(role.name, func(t *testing.T) {
			tx, err := role.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin %s tx: %v", role.name, err)
			}
			defer func() { _ = tx.Rollback(ctx) }()

			var preCount int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules`).Scan(&preCount); err != nil {
				t.Fatalf("count rules before TRUNCATE: %v", err)
			}
			if preCount == 0 {
				t.Fatalf("count(rules) = 0 before TRUNCATE, want > 0 -- nothing to protect " +
					"(has the DB been migrated via `make migrate-up`?)")
			}

			opErr := attemptWithSavepoint(t, ctx, tx, `TRUNCATE rules`)
			assertSQLState(t, opErr, "23001")

			var postCount int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules`).Scan(&postCount); err != nil {
				t.Fatalf("count rules after rejected TRUNCATE: %v", err)
			}
			if postCount != preCount {
				t.Errorf("count(rules) after rejected TRUNCATE = %d, want %d (unchanged)", postCount, preCount)
			}
		})
	}
}
