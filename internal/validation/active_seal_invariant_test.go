// Seal invariant for rule_set_versions: a dated version is sealed
// (rule_set_versions_dated_is_sealed). The old active ⟹ sealed cases went with
// is_active (ENGI-04-05).
package validation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestASIAdv_ConstraintIsValidated: the dated ⟹ sealed CHECK is VALIDATED, not
// `NOT VALID`, so pre-existing rows are covered too. Read-only.
func TestASIAdv_ConstraintIsValidated(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	for _, name := range []string{"rule_set_versions_dated_is_sealed"} {
		var convalidated bool
		if err := app.QueryRow(ctx,
			`SELECT convalidated FROM pg_constraint
			  WHERE conname = $1
			    AND conrelid = 'rule_set_versions'::regclass`, name,
		).Scan(&convalidated); err != nil {
			t.Fatalf("query pg_constraint.convalidated for %s: %v", name, err)
		}
		if !convalidated {
			t.Errorf("%s.convalidated = false -- the CHECK was added NOT VALID (or "+
				"never validated), so it does not guarantee pre-existing rows satisfy the invariant", name)
		}
	}
}

// ---------------------------------------------------------------------
// ENGI-04-01 -- successor invariant: dated ⟹ sealed
// (rule_set_versions_dated_is_sealed). All rolled back.
// ---------------------------------------------------------------------

func TestASI_DatedUnsealedInsertRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	migrator := migratorPool(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"super", super}, {"migrator", migrator}} {
		t.Run(role.name, func(t *testing.T) {
			tx := edBegin(t, ctx, role.pool)
			version := nextVersion()
			opErr := attemptWithSavepoint(t, ctx, tx,
				`INSERT INTO rule_set_versions (version, sealed, effective_from, notes)
				 VALUES ($1, false, '3001-01-01', $2)`, version, fixtureNotes)
			assertSQLState(t, opErr, "23514")
			assertConstraintName(t, opErr, "rule_set_versions_dated_is_sealed")

			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE version = $1`, version).Scan(&n); err != nil {
				t.Fatalf("count probe rows: %v", err)
			}
			if n != 0 {
				t.Errorf("probe row count = %d, want 0", n)
			}
		})
	}
}

func TestASI_DatingAnUnsealedDraftRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	migrator := migratorPool(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"super", super}, {"migrator", migrator}} {
		t.Run(role.name, func(t *testing.T) {
			tx := edBegin(t, ctx, role.pool)
			id := edFixture(t, ctx, tx, nextVersion(), false, "")
			opErr := attemptWithSavepoint(t, ctx, tx,
				`UPDATE rule_set_versions SET effective_from = '3001-01-01' WHERE id = $1`, id)
			assertSQLState(t, opErr, "23514")
			assertConstraintName(t, opErr, "rule_set_versions_dated_is_sealed")
			if got := edFrom(t, ctx, tx, id); got != nil {
				t.Errorf("draft effective_from = %s after the refused UPDATE, want NULL", *got)
			}
		})
	}
}

func TestASI_SealAndDateInOneStatementSucceeds(t *testing.T) {
	super, _ := dbTestPools(t)
	migrator := migratorPool(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"super", super}, {"migrator", migrator}} {
		t.Run(role.name, func(t *testing.T) {
			tx := edBegin(t, ctx, role.pool)
			id := edFixture(t, ctx, tx, nextVersion(), false, "")
			if _, err := tx.Exec(ctx,
				`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
				 VALUES ($1, 'asi-publish-probe', 'required', 'error', 'probe')`, id); err != nil {
				t.Fatalf("insert the fixture's rule: %v", err)
			}
			tag, err := tx.Exec(ctx,
				`UPDATE rule_set_versions SET sealed = true, effective_from = '3001-01-01' WHERE id = $1`, id)
			if err != nil {
				t.Fatalf("seal and date in one statement: %v -- want success (the publish path)", err)
			}
			if tag.RowsAffected() != 1 {
				t.Fatalf("RowsAffected = %d, want 1", tag.RowsAffected())
			}
			var sealed bool
			if err := tx.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE id = $1`, id).Scan(&sealed); err != nil {
				t.Fatalf("read sealed: %v", err)
			}
			if !sealed {
				t.Error("sealed = false, want true")
			}
			edWant(t, edFrom(t, ctx, tx, id), "3001-01-01", "effective_from")
		})
	}
}
