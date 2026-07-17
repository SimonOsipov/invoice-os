// M4-18-01 (Test-first: yes) -- Mode A RED specs for the `active ⟹ sealed`
// invariant. Transcribes the M4-18 story's Test Specs table (ASI-01..07)
// into runnable Go tests, authored BEFORE:
//
//	(a) migrations/<fresh_timestamp>_active_seal_invariant.sql exists (no
//	    `rule_set_versions_active_is_sealed` CHECK constraint); and
//	(b) the fixture rework (schema_test.go's `sealAndActivate` helper, the
//	    reworked `seedVersion`, and rule_set_v2_test.go's reworked
//	    `simulateActiveVersion`) lands -- none of that exists yet, so this
//	    file deliberately does NOT reference `sealAndActivate` or any other
//	    not-yet-created symbol. It asserts observable DB state via EXISTING
//	    symbols (`dbTestPools`, `migratorPool`, `assertSQLState`,
//	    `attemptWithSavepoint`, `nextVersion`, `fixtureNotes`, `seedVersion`,
//	    `simulateActiveVersion`) plus raw SQL only, so it compiles against
//	    current HEAD and fails on the target assertion, not on a compile or
//	    setup error.
//
// See the M4-18 story (Obsidian, "Simon Vault/Projects/FiscalBridge Africa/
// User Stories/M4/M4-18 Active-Version Seal Invariant.md") for the
// authoritative Objective/Acceptance Criteria/System Design/Test Specs this
// file transcribes, and rule_immutability_test.go / schema_test.go for the
// harness conventions (dbTestPools, migratorPool, assertSQLState,
// attemptWithSavepoint, nextVersion, fixtureNotes, seedVersion) this file
// reuses verbatim.
//
// RED SHAPE (why each ASI test fails today, and why that is the RIGHT
// reason -- not a compile error, a panic, or a skip):
//   - ASI-01/02: the CHECK constraint does not exist yet, so an
//     active-unsealed INSERT/activate-UPDATE that should be rejected with
//     23514 actually SUCCEEDS today -- assertSQLState(nil, "23514") fails
//     loudly because the op returned no error at all.
//   - ASI-03: a positive guardrail -- green both pre- and post-migration (it
//     does not discriminate the migration landing; it is a regression guard
//     against the seal-then-activate publish flow ever breaking).
//   - ASI-04: `pg_constraint` has no row named
//     `rule_set_versions_active_is_sealed` pre-migration -- the existence
//     assertion fails.
//   - ASI-05: asserts the constraint exists FIRST (the vacuous-pass guard
//     mirroring this package's `requireSealed`/RIL-08-style convention) --
//     pre-migration that precondition itself t.Fatals, which is the
//     spec-accepted inherent RED for any Down-behaviour test (mirrors the
//     doc comment at rule_set_v2_test.go:312-314: it cannot discriminate "Up
//     unwritten" from "Up written but Down broken").
//   - ASI-06: `seedVersion(t, super, true)` still yields an active-UNSEALED
//     row pre-rework (today's `seedVersion` activates inline without
//     sealing) -- the `sealed=true` assertion fails.
//   - ASI-07: `simulateActiveVersion` still hand-rolls an unsealed
//     activate pre-rework (rule_set_v2_test.go:454-456) -- the
//     `is_active=true AND sealed=true` assertion on its fixture fails (it is
//     active but NOT sealed).
//
// LEAK-FREE DISCIPLINE (the shared 5432 DB persists across runs): every spec
// that mutates the real v1/v2 or inserts throwaway rows runs inside an
// explicit transaction that is UNCONDITIONALLY rolled back via `defer
// tx.Rollback`, using attemptWithSavepoint around the one statement that may
// legitimately fail (post-migration) or unexpectedly succeed
// (pre-migration), so one abort never poisons the rest of the transaction's
// assertions and nothing persists either way. ASI-06/07 are the exception:
// they call the existing, already-cleanup-registering `seedVersion` /
// `simulateActiveVersion` fixtures directly (not inside an extra wrapping
// tx), exactly as every other caller of those fixtures in this package does
// -- their own t.Cleanup is what keeps the shared DB clean.
//
// Run (same env gate as the rest of the package, plus
// DATABASE_MIGRATION_URL for the owner-proof migrator-role pool):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run 'TestASI' -v ./internal/validation/...
package validation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------
// ASI-01 -- INSERT of an active-unsealed row rejected, super AND migrator.
// ---------------------------------------------------------------------

// TestASI01_InsertActiveUnsealedRejected (ASI-01): inserting a brand-new
// `rule_set_versions` row with `is_active=true, sealed=false` must be
// rejected with 23514 (check_violation) -- as both super and migrator
// (table-driven), mirroring RIL's owner-proof convention even though a CHECK
// (unlike a trigger) has no owner bypass.
//
// The real v2 already holds the one-active partial-unique index's single
// slot, so a naive 2nd active insert would trip 23505 (unique_violation)
// EVEN WITHOUT the CHECK -- an incidental, misleading red. To keep the RED
// unambiguous (pre-migration: "insert succeeded", not a masked 23505), the
// active slot is cleared FIRST, inside the same rolled-back tx, so the ONLY
// thing under test is the CHECK; the active-unsealed insert itself then runs
// under a savepoint.
func TestASI01_InsertActiveUnsealedRejected(t *testing.T) {
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

			// Clear the active slot so the only thing under test is the
			// CHECK, not the (unrelated) one-active partial-unique index.
			if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE is_active`); err != nil {
				t.Fatalf("clear the active slot: %v", err)
			}

			probeVersion := nextVersion()
			opErr := attemptWithSavepoint(t, ctx, tx,
				`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, true, false, $2)`,
				probeVersion, fixtureNotes,
			)
			assertSQLState(t, opErr, "23514")

			var count int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM rule_set_versions WHERE version = $1`, probeVersion,
			).Scan(&count); err != nil {
				t.Fatalf("count probe rows: %v", err)
			}
			if count != 0 {
				t.Errorf("probe rule_set_versions row count = %d, want 0 -- active-unsealed INSERT must not persist", count)
			}
		})
	}
}

// ---------------------------------------------------------------------
// ASI-02 -- activating an unsealed draft rejected, super AND migrator.
// ---------------------------------------------------------------------

// TestASI02_ActivateUnsealedDraftRejected (ASI-02): inserting an
// inactive+unsealed draft succeeds (pre- and post-migration); activating it
// (`UPDATE is_active=true`) while it is still unsealed must be rejected with
// 23514, as both super and migrator. The active slot is cleared first (same
// rationale as ASI-01) so the activate attempt cannot instead trip the
// unrelated one-active unique index.
func TestASI02_ActivateUnsealedDraftRejected(t *testing.T) {
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

			var draftID string
			if err := tx.QueryRow(ctx,
				`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, false, false, $2) RETURNING id`,
				nextVersion(), fixtureNotes,
			).Scan(&draftID); err != nil {
				t.Fatalf("insert inactive+unsealed draft: %v -- must succeed", err)
			}

			if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE is_active`); err != nil {
				t.Fatalf("clear the active slot: %v", err)
			}

			opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rule_set_versions SET is_active = true WHERE id = $1`, draftID)
			assertSQLState(t, opErr, "23514")

			var stillInactive bool
			if err := tx.QueryRow(ctx, `SELECT NOT is_active FROM rule_set_versions WHERE id = $1`, draftID).Scan(&stillInactive); err != nil {
				t.Fatalf("read is_active after rejected activate: %v", err)
			}
			if !stillInactive {
				t.Error("draft is_active = true after a rejected activate-while-unsealed, want still false")
			}
		})
	}
}

// ---------------------------------------------------------------------
// ASI-03 -- activating a SEALED draft succeeds (positive guardrail).
// ---------------------------------------------------------------------

// TestASI03_ActivateSealedSucceeds (ASI-03): the seal-then-activate publish
// flow must remain legal -- insert a draft, seal it (false->true, already
// legal per M4-17's Guard C), clear the active slot, then activate it. This
// is a regression guard, not a migration-discriminating spec: it is green
// both pre- and post-migration (sealed=true always satisfies `NOT is_active
// OR sealed`), per the Test Specs table's explicit note.
func TestASI03_ActivateSealedSucceeds(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var draftID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, false, false, $2) RETURNING id`,
		nextVersion(), fixtureNotes,
	).Scan(&draftID); err != nil {
		t.Fatalf("insert inactive+unsealed draft: %v", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET sealed = true WHERE id = $1`, draftID); err != nil {
		t.Fatalf("seal the draft (false->true): %v -- must succeed", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE is_active`); err != nil {
		t.Fatalf("clear the active slot: %v", err)
	}

	tag, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE id = $1`, draftID)
	if err != nil {
		t.Fatalf("activate the now-sealed draft: %v -- want success (the seal-then-publish flow must remain legal)", err)
	}
	if got := tag.RowsAffected(); got != 1 {
		t.Fatalf("RowsAffected = %d, want 1", got)
	}

	var isActive, sealed bool
	if err := tx.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE id = $1`, draftID).Scan(&isActive, &sealed); err != nil {
		t.Fatalf("read is_active/sealed after activate: %v", err)
	}
	if !isActive {
		t.Error("is_active = false after activating the sealed draft, want true")
	}
	if !sealed {
		t.Error("sealed = false after activating the sealed draft, want still true")
	}
}

// ---------------------------------------------------------------------
// ASI-04 -- the constraint exists and holds against real data.
// ---------------------------------------------------------------------

// TestASI04_ConstraintExistsAndHolds (ASI-04): the migration must have added
// a CHECK constraint named `rule_set_versions_active_is_sealed`, and no
// `rule_set_versions` row anywhere may currently violate it. Read-only (no
// tx wrap needed) -- pre-migration the constraint row is simply absent.
func TestASI04_ConstraintExistsAndHolds(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var constraintExists bool
	if err := app.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = 'rule_set_versions_active_is_sealed'
			  AND conrelid = 'rule_set_versions'::regclass
		)`,
	).Scan(&constraintExists); err != nil {
		t.Fatalf("query pg_constraint for rule_set_versions_active_is_sealed: %v", err)
	}
	if !constraintExists {
		t.Error("pg_constraint has no rule_set_versions_active_is_sealed row -- the active⟹sealed CHECK is missing")
	}

	var violatingCount int
	if err := app.QueryRow(ctx,
		`SELECT count(*) FROM rule_set_versions WHERE is_active AND NOT sealed`,
	).Scan(&violatingCount); err != nil {
		t.Fatalf("count active-unsealed rows: %v", err)
	}
	if violatingCount != 0 {
		t.Errorf("count(is_active AND NOT sealed) = %d, want 0 -- an active-unsealed row exists in the real table", violatingCount)
	}
}

// ---------------------------------------------------------------------
// ASI-05 -- the migration's Down removes the constraint.
// ---------------------------------------------------------------------

// TestASI05_DownRemovesConstraint (ASI-05): asserts the constraint's
// existence FIRST (the vacuous-pass guard mirroring this package's
// requireSealed/RIL-08 convention: never assert "op X succeeds because the
// guard is gone" without first proving the guard was really there) --
// pre-migration this precondition itself t.Fatals, which is the
// spec-accepted inherent RED for any Down-behaviour test (it cannot
// discriminate "Up unwritten" from "Up written but Down broken", mirroring
// the doc comment at rule_set_v2_test.go:312-314). Then, inside a
// rolled-back super tx, DROPs the constraint and shows an active-unsealed
// INSERT now succeeds -- proving the Down statement has the right shape.
func TestASI05_DownRemovesConstraint(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	var constraintExists bool
	if err := super.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = 'rule_set_versions_active_is_sealed'
			  AND conrelid = 'rule_set_versions'::regclass
		)`,
	).Scan(&constraintExists); err != nil {
		t.Fatalf("query pg_constraint for rule_set_versions_active_is_sealed: %v", err)
	}
	if !constraintExists {
		t.Fatalf("pg_constraint has no rule_set_versions_active_is_sealed row -- cannot test the Down's " +
			"removal of a constraint that was never added by the Up (this precondition Fatal is the accepted " +
			"inherent RED for a Down-behaviour test, per the story's ASI-05 note)")
	}

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions DROP CONSTRAINT rule_set_versions_active_is_sealed`); err != nil {
		t.Fatalf("DROP CONSTRAINT rule_set_versions_active_is_sealed (simulating the migration's Down): %v", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE is_active`); err != nil {
		t.Fatalf("clear the active slot: %v", err)
	}

	probeVersion := nextVersion()
	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, true, false, $2)`,
		probeVersion, fixtureNotes,
	); err != nil {
		t.Fatalf("INSERT active-unsealed row after DROP CONSTRAINT: %v -- want success (the Down must fully remove the CHECK)", err)
	}
}

// ---------------------------------------------------------------------
// ASI-06 -- seedVersion(true) yields an active version that is ALSO sealed.
// ---------------------------------------------------------------------

// TestASI06_SeedVersionActiveIsSealed (ASI-06): the EXISTING `seedVersion(t,
// super, true)` fixture must yield a row that is both `is_active=true` AND
// `sealed=true` -- pre-rework it activates inline WITHOUT sealing, so this
// assertion fails (active-unsealed). Deliberately does NOT call
// `sealAndActivate` (not yet written) -- it only asserts the observable
// state of the existing fixture call.
func TestASI06_SeedVersionActiveIsSealed(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	id, _ := seedVersion(t, super, true)

	var isActive, sealed bool
	if err := super.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE id = $1`, id).Scan(&isActive, &sealed); err != nil {
		t.Fatalf("read is_active/sealed for seedVersion(true) fixture: %v", err)
	}
	if !isActive {
		t.Errorf("seedVersion(t, super, true) row is_active = false, want true")
	}
	if !sealed {
		t.Errorf("seedVersion(t, super, true) row sealed = false, want true -- an active version must also be "+
			"sealed under the active⟹sealed invariant (pre-rework, seedVersion activates without sealing)")
	}
}

// ---------------------------------------------------------------------
// ASI-07 -- simulateActiveVersion's fixture is active AND sealed, and its
// restore-cleanup still restores the real active version.
// ---------------------------------------------------------------------

// TestASI07_SimulateActiveVersionRestoresAndSeals (ASI-07): runs the
// EXISTING `simulateActiveVersion` fixture inside a subtest and asserts,
// WHILE the subtest is still active, that its fixture row is `is_active=true
// AND sealed=true` -- pre-rework `simulateActiveVersion` hand-rolls an
// activate without ever sealing (rule_set_v2_test.go:454-456), so this is
// active-UNSEALED and the assertion fails. After the subtest returns (its
// t.Cleanup has fired), asserts the real previously-active version (captured
// before the subtest runs) is restored as the SOLE active row.
func TestASI07_SimulateActiveVersionRestoresAndSeals(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	var originalActiveID string
	if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&originalActiveID); err != nil {
		t.Fatalf("capture the originally active version id: %v", err)
	}

	t.Run("fixture_is_active_and_sealed", func(t *testing.T) {
		id, _ := simulateActiveVersion(t, super)

		var isActive, sealed bool
		if err := super.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE id = $1`, id).Scan(&isActive, &sealed); err != nil {
			t.Fatalf("read is_active/sealed for simulateActiveVersion fixture: %v", err)
		}
		if !isActive {
			t.Errorf("simulateActiveVersion fixture is_active = false, want true")
		}
		if !sealed {
			t.Errorf("simulateActiveVersion fixture sealed = false, want true -- an active fixture must also be "+
				"sealed under the active⟹sealed invariant (pre-rework, simulateActiveVersion activates without sealing)")
		}
	})

	// The subtest's t.Cleanup has now fired (subtests complete, and their
	// Cleanups run, before the parent test proceeds past t.Run).
	var restoredID string
	var soleActiveCount int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE is_active`).Scan(&soleActiveCount); err != nil {
		t.Fatalf("count active rows after subtest cleanup: %v", err)
	}
	if soleActiveCount != 1 {
		t.Fatalf("count(is_active) after simulateActiveVersion's cleanup = %d, want exactly 1", soleActiveCount)
	}
	if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&restoredID); err != nil {
		t.Fatalf("read the sole active version id after cleanup: %v", err)
	}
	if restoredID != originalActiveID {
		t.Errorf("active version id after simulateActiveVersion's cleanup = %s, want restored original %s",
			restoredID, originalActiveID)
	}
}

// ---------------------------------------------------------------------
// QA Mode B adversarial coverage -- combined unseal-while-active, multi-row
// activate (no partial commit), and a permanent convalidated regression guard.
// Mirrors the TestRILAdv_* naming/placement convention (rule_immutability_adversarial_test.go)
// for QA-added coverage on top of an architect-authored ASI-NN suite. All rolled back.
// ---------------------------------------------------------------------

// TestASIAdv_UnsealWhileActiveRejectedByGuardC: a combined
// `SET is_active = true, sealed = false` on the real sealed ACTIVE row
// (attempting to unseal it while it stays active) must be rejected -- but by
// Guard C (M4-17's rule_set_versions_seal_guard, BEFORE UPDATE), not by the
// M4-18 CHECK. Guard C's condition (`OLD.sealed AND NOT NEW.sealed`) fires
// unconditionally on ANY sealed->unsealed transition, regardless of what else
// the same UPDATE changes, and a BEFORE-trigger abort happens before Postgres
// ever evaluates the row's CHECK constraints -- so the SQLSTATE must be
// restrict_violation (23001), not check_violation (23514). Verified against
// the running migration (docker exec psql) before being encoded here: the
// combined UPDATE on v2 raises "a sealed rule-set version cannot be unsealed
// (version=2)" with ERRCODE restrict_violation.
func TestASIAdv_UnsealWhileActiveRejectedByGuardC(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var activeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&activeID); err != nil {
		t.Fatalf("capture the currently active version id: %v", err)
	}

	opErr := attemptWithSavepoint(t, ctx, tx,
		`UPDATE rule_set_versions SET is_active = true, sealed = false WHERE id = $1`, activeID,
	)
	assertSQLState(t, opErr, "23001")

	var isActive, sealed bool
	if err := tx.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE id = $1`, activeID).Scan(&isActive, &sealed); err != nil {
		t.Fatalf("read is_active/sealed after rejected unseal-while-active: %v", err)
	}
	if !isActive || !sealed {
		t.Errorf("active version (id=%s) is_active=%t sealed=%t after a rejected unseal-while-active UPDATE, want both still true (unchanged)",
			activeID, isActive, sealed)
	}
}

// TestASIAdv_MultiRowActivateMixedSealNoPartialActivate: a single UPDATE
// statement flips is_active=true across TWO rows in one command -- one
// already sealed (would satisfy the CHECK in isolation), one still unsealed
// (violates it). Per-row CHECK evaluation means the unsealed row's own
// violation aborts the statement; Postgres statement-level atomicity means
// NEITHER row's change survives -- the sealed row must not end up "partially
// activated" just because its own row would have passed the CHECK alone.
func TestASIAdv_MultiRowActivateMixedSealNoPartialActivate(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin super tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Clear the active slot so this multi-row UPDATE's failure is unambiguously the
	// CHECK (23514), not the (unrelated) one-active partial-unique index (23505).
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE is_active`); err != nil {
		t.Fatalf("clear the active slot: %v", err)
	}

	var sealedDraftID, unsealedDraftID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, false, true, $2) RETURNING id`,
		nextVersion(), fixtureNotes,
	).Scan(&sealedDraftID); err != nil {
		t.Fatalf("insert inactive+SEALED draft: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, false, false, $2) RETURNING id`,
		nextVersion(), fixtureNotes,
	).Scan(&unsealedDraftID); err != nil {
		t.Fatalf("insert inactive+UNSEALED draft: %v", err)
	}

	opErr := attemptWithSavepoint(t, ctx, tx,
		`UPDATE rule_set_versions SET is_active = true WHERE id IN ($1, $2)`, sealedDraftID, unsealedDraftID,
	)
	assertSQLState(t, opErr, "23514")

	var sealedIsActive, unsealedIsActive bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM rule_set_versions WHERE id = $1`, sealedDraftID).Scan(&sealedIsActive); err != nil {
		t.Fatalf("read sealed draft is_active after rejected multi-row activate: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT is_active FROM rule_set_versions WHERE id = $1`, unsealedDraftID).Scan(&unsealedIsActive); err != nil {
		t.Fatalf("read unsealed draft is_active after rejected multi-row activate: %v", err)
	}
	if sealedIsActive {
		t.Error("sealed draft is_active = true after the multi-row UPDATE was rejected -- the statement's failure on " +
			"the unsealed row must roll back ALL of its row changes, not just the failing row (no partial activate)")
	}
	if unsealedIsActive {
		t.Error("unsealed draft is_active = true after the multi-row UPDATE was rejected, want still false")
	}
}

// TestASIAdv_ConstraintIsValidated: a permanent regression guard that the
// CHECK was added as a plain, VALIDATED constraint (per the story's §1.2
// "plain ADD CONSTRAINT, not NOT VALID + VALIDATE" decision), not `NOT
// VALID`. A `NOT VALID` constraint would still pass ASI-04's existence check
// and would still reject NEW violations, but would silently leave any
// PRE-EXISTING violating rows unenforced until a later `VALIDATE
// CONSTRAINT` -- defeating the "no backfill needed, scan is trivial" claim
// this migration relies on. Read-only, no tx wrap needed.
func TestASIAdv_ConstraintIsValidated(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var convalidated bool
	if err := app.QueryRow(ctx,
		`SELECT convalidated FROM pg_constraint
		  WHERE conname = 'rule_set_versions_active_is_sealed'
		    AND conrelid = 'rule_set_versions'::regclass`,
	).Scan(&convalidated); err != nil {
		t.Fatalf("query pg_constraint.convalidated for rule_set_versions_active_is_sealed: %v", err)
	}
	if !convalidated {
		t.Error("rule_set_versions_active_is_sealed.convalidated = false -- the CHECK was added NOT VALID (or " +
			"never validated), so it does not guarantee pre-existing rows satisfy the active⟹sealed invariant")
	}
}
