// The approval_step_delegation Down, replayed in a rolled-back transaction. The round-trip in CI
// cannot catch a Down that leaves the old lock function in place or drops too much.
package db_test

import (
	"context"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	delegationMigrationGlob = "*_approval_step_delegation.sql"
	sealLockMigration       = "20260809210326_approval_policies.sql"
)

// The three tables' columns the Down must remove, plus notify_channel as the catalog query's positive control.
const delegationColumnProbe = `
	SELECT table_name || '.' || column_name FROM information_schema.columns
	WHERE table_schema = 'public'
	  AND table_name IN ('approval_policy_steps', 'approval_run_steps', 'approval_decisions')
	  AND column_name IN ('allow_delegation', 'delegate_user_id', 'as_delegate', 'notify_channel')
	ORDER BY 1`

// The six CHECK/FK names and the three named NOT NULLs, then three untouched constraints as controls.
const delegationConstraintProbe = `
	SELECT conname FROM pg_constraint
	WHERE conrelid IN ('public.approval_policy_steps'::regclass, 'public.approval_run_steps'::regclass, 'public.approval_decisions'::regclass)
	  AND conname = ANY($1)
	ORDER BY 1`

var delegationOwnConstraints = []string{
	"approval_decisions_as_delegate_not_null",
	"approval_policy_steps_allow_delegation_not_null",
	"approval_policy_steps_delegate_needs_allow",
	"approval_policy_steps_delegation_kind",
	"approval_policy_steps_tenant_delegate_fk",
	"approval_run_steps_allow_delegation_not_null",
	"approval_run_steps_delegate_needs_allow",
	"approval_run_steps_delegation_kind",
	"approval_run_steps_tenant_delegate_fk",
}

var delegationControlConstraints = []string{
	"approval_decisions_tenant_run_fk",
	"approval_policy_steps_tenant_version_fk",
	"approval_run_steps_tenant_run_fk",
}

func approvalDelegationMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, delegationMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", delegationMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1 -- scaffold it with `make migrate-create name=approval_step_delegation`",
			len(matches), delegationMigrationGlob, matches)
	}
	return matches[0]
}

// originalSealLockBody is the body of approval_policy_steps_content_lock as 20260809210326 created it.
func originalSealLockBody(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(migrations.FS, sealLockMigration)
	if err != nil {
		t.Fatalf("read %s: %v", sealLockMigration, err)
	}
	m := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION approval_policy_steps_content_lock\(\).*?AS \$\$(.*?)\$\$;`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s: no approval_policy_steps_content_lock body found", sealLockMigration)
	}
	return string(m[1])
}

func queryStrings(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) []string {
	t.Helper()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

// The Down removes the three columns' worth of data, the nine constraints and the lock's two
// comparisons, and puts the 20260809210326 function body back byte for byte.
func TestRLS_ApprovalDelegationMigrationDownRestoresTheSealLock(t *testing.T) {
	requireHarness(t)
	ctx := t.Context()
	name := approvalDelegationMigrationName(t)
	down := auditEntitySectionOf(t, name, "Down")
	original := originalSealLockBody(t)
	tx := migratorTx(t, ctx)

	// Before the Down, the probes see the delegation objects: a probe that finds nothing proves nothing.
	wantBefore := []string{
		"approval_decisions.as_delegate",
		"approval_policy_steps.allow_delegation", "approval_policy_steps.delegate_user_id", "approval_policy_steps.notify_channel",
		"approval_run_steps.allow_delegation", "approval_run_steps.delegate_user_id", "approval_run_steps.notify_channel",
	}
	if got := queryStrings(t, ctx, tx, delegationColumnProbe); !reflect.DeepEqual(got, wantBefore) {
		t.Fatalf("columns before the Down = %v, want %v (is the migration applied?)", got, wantBefore)
	}
	allConstraints := append(append([]string{}, delegationOwnConstraints...), delegationControlConstraints...)
	if got := queryStrings(t, ctx, tx, delegationConstraintProbe, allConstraints); len(got) != len(allConstraints) {
		t.Fatalf("constraints before the Down = %v, want all of %v", got, allConstraints)
	}
	var lockBefore string
	if err := tx.QueryRow(ctx, `SELECT pg_get_functiondef('public.approval_policy_steps_content_lock()'::regprocedure)`).Scan(&lockBefore); err != nil {
		t.Fatalf("read the lock function: %v", err)
	}
	for _, col := range []string{"allow_delegation", "delegate_user_id"} {
		if !strings.Contains(lockBefore, col) {
			t.Fatalf("the lock function before the Down does not compare %s", col)
		}
	}

	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("Down body failed: %v", err)
	}

	wantAfter := []string{"approval_policy_steps.notify_channel", "approval_run_steps.notify_channel"}
	if got := queryStrings(t, ctx, tx, delegationColumnProbe); !reflect.DeepEqual(got, wantAfter) {
		t.Errorf("columns after the Down = %v, want only %v", got, wantAfter)
	}
	if got := queryStrings(t, ctx, tx, delegationConstraintProbe, allConstraints); !reflect.DeepEqual(got, delegationControlConstraints) {
		t.Errorf("constraints after the Down = %v, want only the untouched %v", got, delegationControlConstraints)
	}

	var def, src string
	if err := tx.QueryRow(ctx,
		`SELECT pg_get_functiondef(oid), prosrc FROM pg_proc
		 WHERE oid = 'public.approval_policy_steps_content_lock()'::regprocedure`).Scan(&def, &src); err != nil {
		t.Fatalf("read the lock function after the Down: %v", err)
	}
	for _, col := range []string{"allow_delegation", "delegate_user_id"} {
		if strings.Contains(def, col) {
			t.Errorf("the lock function after the Down still mentions %s", col)
		}
	}
	if !strings.Contains(def, "notify_channel") {
		t.Error("the lock function after the Down no longer compares notify_channel")
	}
	if src != original {
		t.Errorf("the lock function body after the Down differs from %s:\n got: %s\nwant: %s", sealLockMigration, src, original)
	}
	var triggers int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM pg_trigger WHERE tgname = 'approval_policy_steps_content_lock'
		 AND tgrelid = 'public.approval_policy_steps'::regclass AND NOT tgisinternal`).Scan(&triggers); err != nil {
		t.Fatalf("read the lock trigger: %v", err)
	}
	if triggers != 1 {
		t.Errorf("approval_policy_steps_content_lock triggers after the Down = %d, want 1", triggers)
	}
}
