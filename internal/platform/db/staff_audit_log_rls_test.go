package db_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	// information_schema lists only USAGE for a sequence, so read the ACL: SELECT or UPDATE would hide there.
	if got, want := collectStrings(t, `SELECT grantee::regrole::text || ':' || privilege_type
	                                    FROM aclexplode((SELECT relacl FROM pg_class WHERE oid = 'public.staff_audit_log_id_seq'::regclass))
	                                    WHERE grantee <> $1::regrole ORDER BY 1`, owner),
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
	requireStaffAuditUpBehaviour(t)
}

// requireStaffAuditUpBehaviour reads the table the shipped Up just built: the app roles reach it
// by INSERT only, the owner is held by the trigger, and the CHECKs refuse nil and blank values.
func requireStaffAuditUpBehaviour(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	actor := uuid.NewString()
	if _, err := h.app.Exec(ctx,
		`INSERT INTO public.staff_audit_log (actor, event, rule_set_version_id) VALUES ($1, 'rules.test', $2)`, actor, uuid.NewString()); err != nil {
		t.Fatalf("invoice_app INSERT after the Up: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_audit_log WHERE actor = $1`, actor); n != 1 {
		t.Fatalf("seeded staff rows = %d, want 1", n)
	}

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"invoice_app", h.app}, {"invoice_tenant_reader", h.reader}} {
		for _, verb := range []string{"SELECT", "UPDATE", "DELETE", "TRUNCATE"} {
			stmt := map[string]string{
				"SELECT":   `SELECT id FROM public.staff_audit_log`,
				"UPDATE":   `UPDATE public.staff_audit_log SET event = 'tampered'`,
				"DELETE":   `DELETE FROM public.staff_audit_log`,
				"TRUNCATE": `TRUNCATE public.staff_audit_log`,
			}[verb]
			if _, err := role.pool.Exec(ctx, stmt); pgCode(err) != "42501" {
				t.Errorf("%s %s after the Up: SQLSTATE %q, want 42501: %v", role.name, verb, pgCode(err), err)
			}
		}
	}
	if _, err := h.reader.Exec(ctx,
		`INSERT INTO public.staff_audit_log (actor, event, rule_set_version_id) VALUES ($1, 'rules.test', $2)`, actor, uuid.NewString()); pgCode(err) != "42501" {
		t.Errorf("invoice_tenant_reader INSERT after the Up: SQLSTATE %q, want 42501: %v", pgCode(err), err)
	}

	for _, verb := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
		stmt := map[string]string{
			"UPDATE":   `UPDATE public.staff_audit_log SET event = 'tampered' WHERE actor = '` + actor + `'`,
			"DELETE":   `DELETE FROM public.staff_audit_log WHERE actor = '` + actor + `'`,
			"TRUNCATE": `TRUNCATE public.staff_audit_log`,
		}[verb]
		if _, err := h.mig.Exec(ctx, stmt); pgCode(err) != "23001" {
			t.Errorf("owner %s after the Up: SQLSTATE %q, want 23001: %v", verb, pgCode(err), err)
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_audit_log WHERE actor = $1 AND event = 'rules.test'`, actor); n != 1 {
		t.Errorf("seeded row after the refused owner statements: %d matches, want 1 unchanged", n)
	}

	nilID := uuid.Nil.String()
	for _, tc := range []struct{ name, actor, version, event, constraint string }{
		{"nil_actor", nilID, uuid.NewString(), "rules.test", "staff_audit_actor_set"},
		{"nil_version", uuid.NewString(), nilID, "rules.test", "staff_audit_version_set"},
		{"empty_event", uuid.NewString(), uuid.NewString(), "", "staff_audit_event_length"},
		{"event_128_chars", uuid.NewString(), uuid.NewString(), strings.Repeat("e", 128), "staff_audit_event_length"},
	} {
		_, err := h.app.Exec(ctx,
			`INSERT INTO public.staff_audit_log (actor, event, rule_set_version_id) VALUES ($1, $2, $3)`, tc.actor, tc.event, tc.version)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != tc.constraint {
			t.Errorf("%s after the Up: error %v, want 23514 on %s", tc.name, err, tc.constraint)
		}
	}
	for _, event := range []string{strings.Repeat("e", 127), strings.Repeat("é", 127)} {
		if _, err := h.app.Exec(ctx,
			`INSERT INTO public.staff_audit_log (actor, event, rule_set_version_id) VALUES ($1, $2, $3)`,
			uuid.NewString(), event, uuid.NewString()); err != nil {
			t.Errorf("127-character event %q after the Up: %v, want accepted", event[:3], err)
		}
	}
}
