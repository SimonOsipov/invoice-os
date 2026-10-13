package db_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/migrations"
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

// staffAuditMigrationVersions lists every migration that names staff_audit_log, newest first; the base is last.
func staffAuditMigrationVersions(t *testing.T) []int64 {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	var versions []int64
	for _, f := range files {
		raw, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !bytes.Contains(raw, []byte("staff_audit_log")) {
			continue
		}
		v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("parse goose version out of %q: %v", f, err)
		}
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
	return versions
}

// staffAuditBaseColumns is the shape of the base migration; the change migrations make rule_set_version_id nullable.
var staffAuditBaseColumns = []string{
	"actor:uuid:NO", "created_at:timestamp with time zone:NO", "event:text:NO", "id:bigint:NO",
	"payload:jsonb:NO", "rule_set_version_id:uuid:NO",
}

// staffAuditAnyChangeColumns is the shape once the table takes a change of any kind.
var staffAuditAnyChangeColumns = []string{
	"actor:uuid:NO", "created_at:timestamp with time zone:NO", "event:text:NO", "id:bigint:NO",
	"payload:jsonb:NO", "rule_set_version_id:uuid:YES", "subject_id:uuid:YES", "tenant_id:uuid:YES",
}

func requireStaffAuditColumns(t *testing.T, when string, want []string) {
	t.Helper()
	got := collectStrings(t, `SELECT column_name || ':' || data_type || ':' || is_nullable FROM information_schema.columns
	                           WHERE table_schema = 'public' AND table_name = 'staff_audit_log' ORDER BY column_name`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("staff_audit_log columns %s (name:type:nullable) = %v, want %v", when, got, want)
	}
}

func requireStaffAuditChecks(t *testing.T, when string, want ...string) {
	t.Helper()
	got := collectStrings(t, `SELECT conname FROM pg_constraint
	                           WHERE conrelid = 'public.staff_audit_log'::regclass AND contype = 'c' ORDER BY conname`)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("staff_audit_log CHECKs %s = %v, want %v", when, got, want)
	}
}

var staffAuditBaseChecks = []string{"staff_audit_actor_set", "staff_audit_event_length", "staff_audit_version_set"}

func TestRLS_StaffAuditLogGrantsAreInsertOnly(t *testing.T) {
	requireHarness(t)
	requireStaffAuditGrantsInsertOnly(t)
	requireStaffAuditColumns(t, "after the migrations", staffAuditAnyChangeColumns)
}

func insertStaffAudit(pool *pgxpool.Pool, actor, event string, version, tenant, subject any) error {
	_, err := pool.Exec(context.Background(),
		`INSERT INTO public.staff_audit_log (actor, event, rule_set_version_id, tenant_id, subject_id) VALUES ($1, $2, $3, $4, $5)`,
		actor, event, version, tenant, subject)
	return err
}

// requireStaffAuditAcceptsAnyChange: invoice_app writes a row with no version, tenant or subject, and one with all three.
func requireStaffAuditAcceptsAnyChange(t *testing.T) {
	t.Helper()
	bare, full := uuid.NewString(), uuid.NewString()
	version, tenant, subject := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := insertStaffAudit(h.app, bare, "support.test", nil, nil, nil); err != nil {
		t.Fatalf("invoice_app INSERT with version, tenant and subject NULL: %v", err)
	}
	if err := insertStaffAudit(h.app, full, "rules.test", version, tenant, subject); err != nil {
		t.Fatalf("invoice_app INSERT with version, tenant and subject set: %v", err)
	}
	read := func(actor string) (v, ten, sub *string) {
		t.Helper()
		if err := h.super.QueryRow(context.Background(),
			`SELECT rule_set_version_id::text, tenant_id::text, subject_id::text FROM public.staff_audit_log WHERE actor = $1`, actor,
		).Scan(&v, &ten, &sub); err != nil {
			t.Fatalf("read back the row of actor %s: %v", actor, err)
		}
		return
	}
	if v, ten, sub := read(bare); v != nil || ten != nil || sub != nil {
		t.Errorf("bare row reads version=%v tenant=%v subject=%v, want all NULL", v, ten, sub)
	}
	if v, ten, sub := read(full); v == nil || ten == nil || sub == nil || *v != version || *ten != tenant || *sub != subject {
		t.Errorf("full row reads version=%v tenant=%v subject=%v, want %s %s %s", v, ten, sub, version, tenant, subject)
	}
}

// requireStaffAuditRefusesNilIDs: each nil UUID names its own CHECK, and a valid row beside them is accepted.
func requireStaffAuditRefusesNilIDs(t *testing.T) {
	t.Helper()
	nilID := uuid.Nil.String()
	if err := insertStaffAudit(h.app, uuid.NewString(), "rules.test", uuid.NewString(), uuid.NewString(), uuid.NewString()); err != nil {
		t.Fatalf("control row with every id set: %v", err)
	}
	for _, tc := range []struct {
		name, event, constraint  string
		version, tenant, subject any
	}{
		{"nil_tenant", "rules.test", "staff_audit_tenant_set", uuid.NewString(), nilID, uuid.NewString()},
		{"nil_subject", "rules.test", "staff_audit_subject_set", uuid.NewString(), uuid.NewString(), nilID},
		{"nil_version", "rules.test", "staff_audit_version_set", nilID, uuid.NewString(), uuid.NewString()},
		{"blank_event", "", "staff_audit_event_length", uuid.NewString(), uuid.NewString(), uuid.NewString()},
	} {
		err := insertStaffAudit(h.app, uuid.NewString(), tc.event, tc.version, tc.tenant, tc.subject)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != tc.constraint {
			t.Errorf("%s: error %v, want 23514 on %s", tc.name, err, tc.constraint)
		}
	}
}

func TestRLS_StaffAuditLogTakesAChangeOfAnyKind(t *testing.T) {
	requireHarness(t)
	requireStaffAuditAcceptsAnyChange(t)
}

func TestRLS_StaffAuditLogRefusesANilTenantSubjectOrVersion(t *testing.T) {
	requireHarness(t)
	requireStaffAuditRefusesNilIDs(t)
}

// Drives the shipped Downs and Ups through goose. Phase 1 is every change migration, phase 2 the base.
func TestRLS_StaffAuditLogDownAndUp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	versions := staffAuditMigrationVersions(t)
	base := migrationVersion(t, "*_staff_audit_log.sql")
	if len(versions) < 2 || versions[len(versions)-1] != base {
		t.Fatalf("staffAuditMigrationVersions = %v, want the base %d last and at least one newer version", versions, base)
	}
	newer := versions[:len(versions)-1]
	provider := staffMigrationProvider(t)

	if table, fn := staffAuditObjects(t); table != 1 || fn != 1 {
		t.Fatalf("before the Down: table=%d function=%d, want 1 and 1", table, fn)
	}
	requireStaffAuditColumns(t, "before the Down", staffAuditAnyChangeColumns)

	// A change row the Down cannot keep whole: no version, a tenant and a subject.
	actor := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO public.staff_audit_log (actor, event, tenant_id, subject_id) VALUES ($1, 'support.test', $2, $3)`,
		actor, uuid.NewString(), uuid.NewString()); err != nil {
		t.Fatalf("seed the change row: %v", err)
	}

	// Phase 1 alone: the Down must run with that row present and keep it.
	reapplyNewer := rollBackMigrations(t, provider, newer...)
	requireStaffAuditColumns(t, "after phase 1 rolled back", []string{
		"actor:uuid:NO", "created_at:timestamp with time zone:NO", "event:text:NO", "id:bigint:NO",
		"payload:jsonb:NO", "rule_set_version_id:uuid:YES",
	})
	requireStaffAuditChecks(t, "after phase 1 rolled back", staffAuditBaseChecks...)
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_audit_log WHERE actor = $1 AND rule_set_version_id IS NULL`, actor); n != 1 {
		t.Errorf("change row after phase 1 rolled back: %d matches, want 1", n)
	}
	requireStaffAuditGrantsInsertOnly(t)
	reapplyNewer()
	requireStaffAuditColumns(t, "after phase 1 re-applied", staffAuditAnyChangeColumns)
	requireStaffAuditChecks(t, "after phase 1 re-applied", append([]string{"staff_audit_tenant_set", "staff_audit_subject_set"}, staffAuditBaseChecks...)...)
	var tenant, subject *string
	if err := h.super.QueryRow(ctx, `SELECT tenant_id::text, subject_id::text FROM public.staff_audit_log WHERE actor = $1`, actor).Scan(&tenant, &subject); err != nil {
		t.Fatalf("read the change row after the Up: %v", err)
	}
	if tenant != nil || subject != nil {
		t.Errorf("change row after the Down and Up: tenant=%v subject=%v, want both NULL (the Down loses them)", tenant, subject)
	}
	requireStaffAuditGrantsInsertOnly(t)
	requireStaffAuditUpBehaviour(t)

	// Both phases: nothing of the table or its function is left, and the Ups rebuild it.
	reapplyNewer = rollBackMigrations(t, provider, newer...)
	reapplyBase := rollBackMigrations(t, provider, base)
	if table, fn := staffAuditObjects(t); table != 0 || fn != 0 {
		t.Errorf("after both phases rolled back: table=%d function=%d, want 0 and 0", table, fn)
	}
	reapplyBase()
	if table, fn := staffAuditObjects(t); table != 1 || fn != 1 {
		t.Fatalf("after the base Up: table=%d function=%d, want 1 and 1", table, fn)
	}
	requireStaffAuditColumns(t, "after the base re-applied", staffAuditBaseColumns)
	requireStaffAuditChecks(t, "after the base re-applied", staffAuditBaseChecks...)
	requireStaffAuditGrantsInsertOnly(t)
	reapplyNewer()
	requireStaffAuditColumns(t, "after both phases re-applied", staffAuditAnyChangeColumns)
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

	requireStaffAuditAcceptsAnyChange(t)
	requireStaffAuditRefusesNilIDs(t)

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
