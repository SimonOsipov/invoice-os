// QA Mode B adversarial coverage for the M4-17 rule-set immutability lock
// (rule_immutability_test.go's RIL-01..14, now green). These specs were not
// derived from the story's Test Specs table -- they probe gaps RIL-01..14
// don't cover but that carry real risk against the shipped guards
// (migrations/20260717120000_rule_immutability_lock.sql):
//
//   - bulk / multi-row ops (a no-WHERE content UPDATE hitting every sealed
//     rule at once; a bulk enabled-only flip across a whole sealed version,
//     the exact shape internal/platform/db/demo_reset_test.go's
//     setAllRulesEnabled/disableRule already rely on)
//   - reparent OUT of a sealed version (RIL-06 only covers reparent INTO;
//     Guard A's UPDATE branch checks OLD's parent too, so the outbound
//     direction needs its own probe)
//   - an id-only / true no-op UPDATE on a sealed rule (nothing IS DISTINCT
//     FROM itself, so this must be allowed), contrasted with a genuine PK
//     value change (rejected as content, per the explicit
//     `OLD.id IS DISTINCT FROM NEW.id` branch in the trigger)
//   - the full seal-on-publish lifecycle in one flow: an unsealed version
//     with rules already under it accepts writes, then the moment it is
//     sealed every INSERT/UPDATE/DELETE flips to rejected except the
//     enabled carve-out
//
// Cross-tenant RLS coverage is N/A here: rules/rule_set_versions are GLOBAL
// tables (no tenant_id column, no RLS policy -- see
// migrations/20260711051711_rule_set_versions.sql and the M4-17 story's
// Grounding table) -- every tenant evaluates the same active rule-set, so
// there is no tenant boundary for this package's guards to violate. Adding a
// cross-tenant-refusal test here would assert nothing real.
//
// Same leak-free discipline as rule_immutability_test.go: every spec either
// (a) attempts a rejected op against real sealed v1/v2 inside an
// always-rolled-back tx + savepoint (attemptWithSavepoint), so nothing
// persists whether the op is rejected (post-migration) or would have
// succeeded, or (b) runs entirely inside a rolled-back super tx when it needs
// to seed its own throwaway version/rule first.
package validation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------
// Bulk / multi-row ops.
// ---------------------------------------------------------------------

// TestRILAdv_BulkContentUpdateNoWhereRejected: a single UPDATE with no WHERE
// clause at all -- the worst-case blast radius, touching every row in
// `rules` including every sealed v1/v2 row in one statement. Guard A is a
// FOR EACH ROW trigger, so it must reject on the first sealed row it
// encounters regardless of how many other rows the statement would have
// touched. Tx+savepoint wrapped: pre-fix (or if a future regression skipped
// row-level evaluation) this would silently rewrite every rule's message.
func TestRILAdv_BulkContentUpdateNoWhereRejected(t *testing.T) {
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
				t.Fatalf("count rules before bulk UPDATE: %v", err)
			}
			if preCount == 0 {
				t.Fatalf("count(rules) = 0 before bulk UPDATE, want > 0 -- nothing to protect")
			}

			opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rules SET message = 'ril-adv-bulk-probe'`)
			assertSQLState(t, opErr, "23001")

			var mutatedCount int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules WHERE message = 'ril-adv-bulk-probe'`).Scan(&mutatedCount); err != nil {
				t.Fatalf("count mutated rows: %v", err)
			}
			if mutatedCount != 0 {
				t.Errorf("rows with mutated message = %d after rejected no-WHERE bulk UPDATE, want 0 -- "+
					"the whole statement must abort, not just skip sealed rows", mutatedCount)
			}
		})
	}
}

// TestRILAdv_BulkEnabledFlipAcrossSealedVersionAllowed: a single UPDATE that
// flips `enabled` for every rule under one sealed version at once -- the
// exact shape internal/platform/db/demo_reset_test.go's setAllRulesEnabled
// (bulk `UPDATE rules SET enabled = true`) and disableRule already depend
// on. Must succeed for every affected row; the carve-out is column-scoped,
// not row-count-scoped. Rolled-back super tx (proves the guard behavior
// without leaving the whole sealed v2 rule-set toggled).
func TestRILAdv_BulkEnabledFlipAcrossSealedVersionAllowed(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	requireSealed(t, ctx, tx, 2, true)
	v2ID := versionIDByVersion(t, ctx, tx, 2)

	var preCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules WHERE rule_set_version_id = $1`, v2ID).Scan(&preCount); err != nil {
		t.Fatalf("count rules under sealed v2: %v", err)
	}
	if preCount == 0 {
		t.Fatalf("count(rules under sealed v2) = 0, want > 0 -- nothing to flip")
	}

	tag, err := tx.Exec(ctx, `UPDATE rules SET enabled = NOT enabled WHERE rule_set_version_id = $1`, v2ID)
	if err != nil {
		t.Fatalf("bulk enabled-only UPDATE across sealed v2: %v -- must succeed "+
			"(matches internal/platform/db/demo_reset_test.go's setAllRulesEnabled/disableRule bulk usage)", err)
	}
	if got := tag.RowsAffected(); got != int64(preCount) {
		t.Errorf("RowsAffected = %d, want %d (every rule under sealed v2 flipped)", got, preCount)
	}
}

// ---------------------------------------------------------------------
// Reparent OUT of a sealed version (RIL-06 only covers reparent INTO).
// ---------------------------------------------------------------------

// TestRILAdv_ReparentOutOfSealedRejected: a rule that lives under sealed v1
// is reparented (rule_set_version_id UPDATE) OUT to a fresh unsealed
// throwaway version -- must be rejected because OLD's parent (v1) is
// sealed, even though NEW's parent is not. RIL-06 only proves the reverse
// direction (reparent INTO a sealed version); Guard A's UPDATE branch
// checks both OLD's and NEW's parent seal state, so this direction needs
// its own probe. Entirely inside a rolled-back super tx.
func TestRILAdv_ReparentOutOfSealedRejected(t *testing.T) {
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
		t.Fatalf("insert throwaway unsealed target version: %v", err)
	}

	v1ID := versionIDByVersion(t, ctx, tx, 1)
	rID := ruleIDByKey(t, ctx, tx, v1ID, "vat-standard-rate")

	opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rules SET rule_set_version_id = $1 WHERE id = $2`, uID, rID)
	assertSQLState(t, opErr, "23001")

	var currentParent string
	if err := tx.QueryRow(ctx, `SELECT rule_set_version_id FROM rules WHERE id = $1`, rID).Scan(&currentParent); err != nil {
		t.Fatalf("read rule's parent after rejected reparent-out: %v", err)
	}
	if currentParent != v1ID {
		t.Errorf("rule's rule_set_version_id = %s after rejected reparent-out, want still %s (sealed v1)", currentParent, v1ID)
	}
}

// ---------------------------------------------------------------------
// id-only / no-op UPDATE vs. a genuine PK content change.
// ---------------------------------------------------------------------

// TestRILAdv_NoOpUpdateOnSealedAllowedButPKChangeRejected: `UPDATE rules SET
// id = id WHERE id = <sealed rule>` is a true no-op -- every column
// (including id) is IS DISTINCT FROM itself = false, so Guard A's content
// branch never fires and the statement must succeed harmlessly. Contrasted
// with actually changing the PK value on a sealed rule, which the trigger's
// explicit `OLD.id IS DISTINCT FROM NEW.id` arm must reject as content. Both
// halves inside a rolled-back super tx (the PK-change half only via
// savepoint, since it is the expected-failure half).
func TestRILAdv_NoOpUpdateOnSealedAllowedButPKChangeRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	v1ID := versionIDByVersion(t, ctx, tx, 1)
	rID := ruleIDByKey(t, ctx, tx, v1ID, "vat-standard-rate")

	if _, err := tx.Exec(ctx, `UPDATE rules SET id = id WHERE id = $1`, rID); err != nil {
		t.Fatalf("no-op UPDATE (SET id = id) on a sealed rule: %v -- must succeed, "+
			"nothing IS DISTINCT FROM itself", err)
	}

	opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rules SET id = gen_random_uuid() WHERE id = $1`, rID)
	assertSQLState(t, opErr, "23001")

	var stillThere bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rules WHERE id = $1)`, rID).Scan(&stillThere); err != nil {
		t.Fatalf("check rule survives under its original id: %v", err)
	}
	if !stillThere {
		t.Error("rule no longer found under its original id after a rejected PK-change UPDATE")
	}
}

// ---------------------------------------------------------------------
// Full seal-on-publish lifecycle, end to end.
// ---------------------------------------------------------------------

// TestRILAdv_PartialSealLifecycleEndToEnd: an unsealed draft version with
// rules already under it (the state a real draft sits in before publish)
// freely accepts INSERT/UPDATE/DELETE -- then, the instant it is sealed,
// every one of those three ops flips to rejected against the SAME rows,
// while the enabled-only carve-out keeps working. RIL-07 only exercises
// INSERT before/after seal; this closes the loop for UPDATE and DELETE too,
// on rows that predate the seal (not just post-seal INSERT attempts).
// Entirely inside a rolled-back super tx.
func TestRILAdv_PartialSealLifecycleEndToEnd(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var draftID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, notes) VALUES ($1, false, $2) RETURNING id`,
		nextVersion(), fixtureNotes,
	).Scan(&draftID); err != nil {
		t.Fatalf("insert draft version: %v", err)
	}

	var rID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
		 VALUES ($1, 'ril-adv-lifecycle-rule', 'required', 'error', 'pre-seal') RETURNING id`,
		draftID,
	).Scan(&rID); err != nil {
		t.Fatalf("insert rule under unsealed draft: %v -- must succeed", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE rules SET message = 'pre-seal-edited' WHERE id = $1`, rID); err != nil {
		t.Fatalf("content UPDATE under unsealed draft: %v -- must succeed", err)
	}

	// Seal it -- the publish step's last action.
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET sealed = true WHERE id = $1`, draftID); err != nil {
		t.Fatalf("seal the draft (false->true): %v -- must succeed", err)
	}
	var sealedNow bool
	if err := tx.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE id = $1`, draftID).Scan(&sealedNow); err != nil {
		t.Fatalf("read sealed after sealing draft: %v", err)
	}
	if !sealedNow {
		t.Fatalf("draft version sealed = false immediately after sealing it, want true")
	}

	// Now every content op against the SAME pre-existing row must be rejected.
	insertErr := attemptWithSavepoint(t, ctx, tx,
		`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
		 VALUES ($1, 'ril-adv-lifecycle-rule-2', 'required', 'error', 'post-seal')`,
		draftID,
	)
	assertSQLState(t, insertErr, "23001")

	updateErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rules SET message = 'post-seal-edit' WHERE id = $1`, rID)
	assertSQLState(t, updateErr, "23001")

	deleteErr := attemptWithSavepoint(t, ctx, tx, `DELETE FROM rules WHERE id = $1`, rID)
	assertSQLState(t, deleteErr, "23001")

	// The enabled-only carve-out must still work on this now-sealed row.
	if _, err := tx.Exec(ctx, `UPDATE rules SET enabled = false WHERE id = $1`, rID); err != nil {
		t.Fatalf("enabled-only UPDATE on the now-sealed row: %v -- the kill-switch carve-out "+
			"must survive the seal transition", err)
	}

	var message string
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT message, enabled FROM rules WHERE id = $1`, rID).Scan(&message, &enabled); err != nil {
		t.Fatalf("read final row state: %v", err)
	}
	if message != "pre-seal-edited" {
		t.Errorf("message = %q after the seal, want unchanged %q (all post-seal content writes rejected)", message, "pre-seal-edited")
	}
	if enabled {
		t.Errorf("enabled = true after the enabled-only disable, want false")
	}
}
