package db_test

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const staffAuditFunction = "staff_audit_log_append_only"

func staffAuditObjects(t *testing.T) (table, function int) {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename = 'staff_audit_log'`),
		mustCount(t, h.super, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		                        WHERE n.nspname = 'public' AND p.proname = $1`, staffAuditFunction)
}

func collectStrings(t *testing.T, sql string, args ...any) []string {
	t.Helper()
	rows, err := h.super.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("read %q: %v", sql, err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect %q: %v", sql, err)
	}
	return got
}

// requireStaffAuditGrantsInsertOnly reads the ACLs: the table has no RLS, so a write grant cannot hide behind a policy.
func requireStaffAuditGrantsInsertOnly(t *testing.T) {
	t.Helper()
	var owner string
	if err := h.super.QueryRow(context.Background(),
		`SELECT tableowner FROM pg_tables WHERE schemaname = 'public' AND tablename = 'staff_audit_log'`,
	).Scan(&owner); err != nil {
		t.Fatalf("public.staff_audit_log does not exist: %v", err)
	}
	if owner != "invoice_migrator" {
		t.Fatalf("staff_audit_log owner = %q, want invoice_migrator", owner)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.table_privileges
		  WHERE table_schema = 'public' AND table_name = 'staff_audit_log' AND grantee = $1`, owner); n == 0 {
		t.Fatal("the view lists no privilege for the owner, so an empty read below proves nothing")
	}

	if got, want := collectStrings(t, `SELECT grantee || ':' || privilege_type FROM information_schema.table_privileges
	                                    WHERE table_schema = 'public' AND table_name = 'staff_audit_log' AND grantee <> $1 ORDER BY 1`, owner),
		[]string{"invoice_app:INSERT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("table privileges beyond the owner = %v, want %v", got, want)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM pg_attribute WHERE attrelid = 'public.staff_audit_log'::regclass AND attnum > 0 AND attacl IS NOT NULL`); n != 0 {
		t.Errorf("columns with their own ACL = %d, want 0", n)
	}
	if got, want := collectStrings(t, `SELECT grantee || ':' || privilege_type FROM information_schema.usage_privileges
	                                    WHERE object_schema = 'public' AND object_name = 'staff_audit_log_id_seq' AND object_type = 'SEQUENCE'
	                                      AND grantee <> $1 ORDER BY 1`, owner),
		[]string{"invoice_app:USAGE"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sequence privileges beyond the owner = %v, want %v", got, want)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM pg_class WHERE oid = 'public.staff_audit_log'::regclass AND NOT relrowsecurity AND NOT relforcerowsecurity`); n != 1 {
		t.Error("staff_audit_log has row-level security on, want none (D11: grants guard it, and the owner must read it)")
	}
}

// AC-7.
func TestRLS_StaffAuditLogGrantsAreInsertOnly(t *testing.T) {
	requireHarness(t)
	requireStaffAuditGrantsInsertOnly(t)

	got := collectStrings(t, `SELECT column_name FROM information_schema.columns
	                           WHERE table_schema = 'public' AND table_name = 'staff_audit_log'`)
	sort.Strings(got)
	if want := []string{"actor", "created_at", "event", "id", "payload", "rule_set_version_id"}; !reflect.DeepEqual(got, want) {
		t.Errorf("staff_audit_log columns = %v, want %v (no tenant_id: the table is global)", got, want)
	}
}

// AC-7. Drives the shipped Down and Up through goose.
func TestRLS_StaffAuditLogDownAndUp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	version, provider := migrationVersion(t, "*_staff_audit_log.sql"), staffMigrationProvider(t)

	if table, fn := staffAuditObjects(t); table != 1 || fn != 1 {
		t.Fatalf("before the Down: table=%d function=%d, want 1 and 1", table, fn)
	}
	restoreRulesRoleOnCleanup(t, provider, version)

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the staff audit migration: %v", err)
	}
	if table, fn := staffAuditObjects(t); table != 0 || fn != 0 {
		t.Errorf("after the Down: table=%d function=%d, want 0 and 0", table, fn)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("apply the staff audit migration: %v", err)
	}
	if table, fn := staffAuditObjects(t); table != 1 || fn != 1 {
		t.Fatalf("after the Up: table=%d function=%d, want 1 and 1", table, fn)
	}
	requireStaffAuditGrantsInsertOnly(t)

	actor := uuid.NewString()
	if _, err := h.app.Exec(ctx,
		`INSERT INTO public.staff_audit_log (actor, event, rule_set_version_id) VALUES ($1, 'rules.test', $2)`, actor, uuid.NewString()); err != nil {
		t.Fatalf("invoice_app INSERT after the Up: %v", err)
	}
	_, err := h.mig.Exec(ctx, `UPDATE public.staff_audit_log SET event = 'tampered' WHERE actor = $1`, actor)
	if code := pgCode(err); code != "23001" {
		t.Errorf("owner UPDATE after the Up: SQLSTATE %q, want 23001 (the Up restores the trigger): %v", code, err)
	}
	_, err = h.mig.Exec(ctx, `TRUNCATE public.staff_audit_log`)
	if code := pgCode(err); code != "23001" {
		t.Errorf("owner TRUNCATE after the Up: SQLSTATE %q, want 23001: %v", code, err)
	}
}
