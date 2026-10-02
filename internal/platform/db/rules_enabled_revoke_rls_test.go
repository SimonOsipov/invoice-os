// Migration proofs for revoke_rules_enabled_from_app: the shipped Down and Up
// run as invoice_migrator inside a superuser tx that is always rolled back, so the shared
// dev DB keeps its grants. The Up goes out as one argument-less Exec (simple protocol)
// because its DO body holds semicolons.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	revokeRulesEnabledGlob = "*_revoke_rules_enabled_from_app.sql"
	appColumnGrantQuery    = `SELECT has_column_privilege('invoice_app', 'public.rules', 'enabled', 'UPDATE')`
	revokePostConditionMsg = "invoice_app still holds UPDATE on rules.enabled"
)

// revokeRulesEnabledMigration returns the migration's name, failing when it is absent.
func revokeRulesEnabledMigration(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, revokeRulesEnabledGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob %s in migrations.FS = %v (err %v), want exactly one file: the migration is not shipped",
			revokeRulesEnabledGlob, matches, err)
	}
	return matches[0]
}

// revokeProbeTx opens a superuser tx that always rolls back, with a lock_timeout.
func revokeProbeTx(t *testing.T) pgx.Tx {
	t.Helper()
	tx, err := h.super.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() {
		if e := tx.Rollback(context.Background()); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			t.Errorf("rollback the revoke probe: %v", e)
		}
	})
	if _, err := tx.Exec(context.Background(), `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	return tx
}

func appHoldsEnabledUpdate(t *testing.T, tx pgx.Tx, when string) bool {
	t.Helper()
	var has bool
	if err := tx.QueryRow(context.Background(), appColumnGrantQuery).Scan(&has); err != nil {
		t.Fatalf("has_column_privilege %s: %v", when, err)
	}
	return has
}

// runShippedDown executes each Down statement in tx.
func runShippedDown(t *testing.T, tx pgx.Tx) {
	t.Helper()
	stmts := shippedDownStatements(t, revokeRulesEnabledGlob)
	if len(stmts) == 0 {
		t.Fatalf("Down of %s holds no statement", revokeRulesEnabledGlob)
	}
	for _, s := range stmts {
		if _, err := tx.Exec(context.Background(), s); err != nil {
			t.Fatalf("execute the shipped Down statement %q as the migrator: %v", s, err)
		}
	}
}

func TestRLS_RulesRevokeDownRestoresTheGrant(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	up := auditEntitySectionOf(t, revokeRulesEnabledMigration(t), "Up")

	tx := revokeProbeTx(t)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_migrator`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_migrator: %v", err)
	}

	// Non-vacuous: the grant is absent before the Down.
	if appHoldsEnabledUpdate(t, tx, "before the Down") {
		t.Fatal("invoice_app holds UPDATE on rules.enabled before the Down, want f (is the migration applied?)")
	}
	runShippedDown(t, tx)
	if !appHoldsEnabledUpdate(t, tx, "after the Down") {
		t.Error("invoice_app holds UPDATE on rules.enabled after the Down = f, want t")
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("execute the shipped Up section as the migrator: %v", err)
	}
	if appHoldsEnabledUpdate(t, tx, "after the Up") {
		t.Error("invoice_app holds UPDATE on rules.enabled after the Up = t, want f")
	}
}

func TestRLS_RulesRevokeFailsWhileAnotherGrantorKeepsTheGrant(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	up := auditEntitySectionOf(t, revokeRulesEnabledMigration(t), "Up")

	// Another grantor re-grants invoice_app: the migrator's REVOKE then removes only its own
	// grant, and the Up's post-condition must raise.
	tx := revokeProbeTx(t)
	for _, s := range []string{
		`GRANT UPDATE (enabled) ON rules TO invoice_tenant_reader WITH GRANT OPTION`,
		`SET LOCAL ROLE invoice_tenant_reader`,
		`GRANT UPDATE (enabled) ON rules TO invoice_app`,
		`SET LOCAL ROLE invoice_migrator`,
	} {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("set up the second grantor (%s): %v", s, err)
		}
	}
	if !appHoldsEnabledUpdate(t, tx, "before the Up") {
		t.Fatal("invoice_app lacks UPDATE on rules.enabled after the second grantor's GRANT, want t")
	}
	_, err := tx.Exec(ctx, up)
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		t.Error("the shipped Up succeeded while another grantor kept the grant, want an error")
	case !errors.As(err, &pgErr) || pgErr.Message != revokePostConditionMsg:
		t.Errorf("Up error = %v, want message %q", err, revokePostConditionMsg)
	}

	// Release the second grantor's locks before the control tx touches the same ACL row.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback the second-grantor tx: %v", err)
	}

	t.Run("control: no other grantor", func(t *testing.T) {
		tx := revokeProbeTx(t)
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_migrator`); err != nil {
			t.Fatalf("SET LOCAL ROLE invoice_migrator: %v", err)
		}
		runShippedDown(t, tx)
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("shipped Up with no other grantor: want no error, got %v", err)
		}
		if appHoldsEnabledUpdate(t, tx, "after the control Up") {
			t.Error("invoice_app holds UPDATE on rules.enabled after the control Up = t, want f")
		}
	})
}

// A grant inherited through a role membership still counts, and only a check on invoice_app
// itself sees it: the grantor-role test above also passes for a check on invoice_tenant_reader.
func TestRLS_RulesRevokeFailsWhileAnInheritedGrantRemains(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	up := auditEntitySectionOf(t, revokeRulesEnabledMigration(t), "Up")

	tx := revokeProbeTx(t)
	for _, s := range []string{
		`CREATE ROLE qa_rules_enabled_writer NOLOGIN`,
		`GRANT UPDATE (enabled) ON rules TO qa_rules_enabled_writer`,
		`GRANT qa_rules_enabled_writer TO invoice_app`,
		`SET LOCAL ROLE invoice_migrator`,
	} {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("set up the inherited grant (%s): %v", s, err)
		}
	}
	if !appHoldsEnabledUpdate(t, tx, "before the Up") {
		t.Fatal("invoice_app lacks UPDATE on rules.enabled through its membership, want t")
	}
	_, err := tx.Exec(ctx, up)
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		t.Error("the shipped Up succeeded while invoice_app inherits the grant, want an error")
	case !errors.As(err, &pgErr) || pgErr.Message != revokePostConditionMsg:
		t.Errorf("Up error = %v, want message %q", err, revokePostConditionMsg)
	}
}
