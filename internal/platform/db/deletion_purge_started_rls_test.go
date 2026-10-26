package db_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	purgeGuardFn    = "public.tenants_purge_guard()"
	purgeStartedCol = "deletion_purge_started_at"
)

// requirePurgeGuard fails the test unless the purge-started column and its trigger are in place.
func requirePurgeGuard(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var cols, trigs int
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = $1`, purgeStartedCol).Scan(&cols); err != nil {
		t.Fatalf("read tenants columns: %v", err)
	}
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM pg_trigger
		  WHERE tgrelid = 'public.tenants'::regclass AND tgname = 'tenants_purge_guard' AND NOT tgisinternal`).Scan(&trigs); err != nil {
		t.Fatalf("read tenants triggers: %v", err)
	}
	if cols != 1 || trigs != 1 {
		t.Fatalf("tenants.%s columns = %d, tenants_purge_guard triggers = %d, want 1 and 1: the deletion_purge_started migration is not applied", purgeStartedCol, cols, trigs)
	}
}

// ppStart sets the marker as the superuser, which the trigger does not exempt.
func ppStart(t *testing.T, id string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_purge_started_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("start the purge of %s: %v", id, err)
	}
}

func ppMarker(t *testing.T, id string) *time.Time {
	t.Helper()
	var m *time.Time
	if err := h.super.QueryRow(context.Background(),
		`SELECT deletion_purge_started_at FROM tenants WHERE id = $1`, id).Scan(&m); err != nil {
		t.Fatalf("read the purge marker of %s: %v", id, err)
	}
	return m
}

// ppExec runs one UPDATE as invoice_app or invoice_migrator, bound to tenant when it is not empty.
func ppExec(t *testing.T, who, tenant, sql string, args ...any) (int64, error) {
	t.Helper()
	ctx := context.Background()
	if who == "invoice_app" {
		var n int64
		err := inAppTx(ctx, tenant, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, sql, args...)
			n = tag.RowsAffected()
			return e
		})
		return n, err
	}
	tx, err := h.mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin as migrator: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenant); err != nil {
		t.Fatalf("bind the migrator: %v", err)
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

func TestRLS_TenantsPurgeStartedIsNullableAndEmpty(t *testing.T) {
	requireHarness(t)
	requirePurgeGuard(t)

	var typ, nullable string
	var dflt *string
	if err := h.super.QueryRow(context.Background(),
		`SELECT data_type, is_nullable, column_default FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = $1`, purgeStartedCol,
	).Scan(&typ, &nullable, &dflt); err != nil {
		t.Fatalf("read the column: %v", err)
	}
	if typ != "timestamp with time zone" || nullable != "YES" || dflt != nil {
		t.Errorf("column = %s, nullable %s, default %v, want timestamptz, YES, no default", typ, nullable, dflt)
	}
	if mustCount(t, h.super, `SELECT count(*) FROM tenants`) == 0 {
		t.Fatal("no tenants rows: the empty check below would be vacuous")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM tenants WHERE deletion_purge_started_at IS NOT NULL`); n != 0 {
		t.Errorf("tenants with the marker set = %d, want 0", n)
	}
}

func TestRLS_AStartedPurgeRefusesEveryChange(t *testing.T) {
	requireHarness(t)
	requirePurgeGuard(t)
	wdRequireColumns(t)

	for _, who := range []string{"invoice_app", "invoice_migrator"} {
		a := newNamedTenant(t, "Started probe "+who)
		wdSchedule(t, a, `now() - interval '1 day'`)
		ppStart(t, a)
		before, marker := wdRead(t, a), ppMarker(t, a)
		if before.scheduledAt == nil || marker == nil {
			t.Fatalf("%s: seed has schedule %v and marker %v, want both set", who, before.scheduledAt, marker)
		}

		for _, c := range []struct{ what, sql string }{
			{"clear the schedule", `UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL WHERE id = $1`},
			{"reschedule", `UPDATE tenants SET deletion_scheduled_at = now() WHERE id = $1`},
			{"change the marker", `UPDATE tenants SET deletion_purge_started_at = now() WHERE id = $1`},
			{"clear the marker", `UPDATE tenants SET deletion_purge_started_at = NULL WHERE id = $1`},
			{"clear the schedule and the marker together", `UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL, deletion_purge_started_at = NULL WHERE id = $1`},
		} {
			_, err := ppExec(t, who, a, c.sql, a)
			pgErr := wdPgErr(t, who+" "+c.what, err, "42501")
			if pgErr.ConstraintName != "tenant_deletion_underway" {
				t.Errorf("%s %s: constraint = %q, want tenant_deletion_underway (message %q)", who, c.what, pgErr.ConstraintName, pgErr.Message)
			}
			if after := wdRead(t, a); !reflect.DeepEqual(after, before) {
				t.Errorf("%s %s: deletion columns = %+v, want unchanged %+v", who, c.what, after, before)
			}
			if got := ppMarker(t, a); got == nil || !got.Equal(*marker) {
				t.Errorf("%s %s: marker = %v, want unchanged %v", who, c.what, got, marker)
			}
		}
	}
}

func TestRLS_ADeletionNotYetStartedStillClears(t *testing.T) {
	requireHarness(t)
	requirePurgeGuard(t)
	wdRequireColumns(t)

	for _, c := range []struct{ what, at string }{
		{"scheduled now", `now()`},
		{"due", `now() - interval '31 days'`},
	} {
		id := newNamedTenant(t, "Unstarted "+c.what)
		wdSchedule(t, id, c.at)
		if wdRead(t, id).scheduledAt == nil {
			t.Fatalf("%s: seed is not scheduled", c.what)
		}
		n, err := ppExec(t, "invoice_app", id,
			`UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL`)
		if err != nil {
			t.Fatalf("%s: clear as invoice_app: %v", c.what, err)
		}
		if n != 1 {
			t.Errorf("%s: rows cleared = %d, want 1", c.what, n)
		}
		wdAssertUnset(t, c.what+" after the clear", id)
		if m := ppMarker(t, id); m != nil {
			t.Errorf("%s: marker = %v, want NULL", c.what, m)
		}
	}
}

func TestRLS_AnUnscheduledTenantCannotBeStarted(t *testing.T) {
	requireHarness(t)
	requirePurgeGuard(t)
	wdRequireColumns(t)
	const start = `UPDATE tenants SET deletion_purge_started_at = now()`

	c := newNamedTenant(t, "Unscheduled start")
	_, err := ppExec(t, "invoice_app", c, start)
	pgErr := wdPgErr(t, "start an unscheduled tenant", err, "23514")
	if pgErr.ConstraintName != "tenants_purge_started_needs_schedule" {
		t.Errorf("constraint = %q, want tenants_purge_started_needs_schedule (message %q)", pgErr.ConstraintName, pgErr.Message)
	}
	if m := ppMarker(t, c); m != nil {
		t.Errorf("marker after the refused start = %v, want NULL", m)
	}

	house := wdHouseTenant(t)
	for _, who := range []string{"invoice_app", "invoice_migrator"} {
		_, err := ppExec(t, who, house, start)
		if pgErr := wdPgErr(t, who+" starts the house tenant", err, "23514"); pgErr.ConstraintName != "tenants_purge_started_needs_schedule" {
			t.Errorf("%s house start: constraint = %q, want tenants_purge_started_needs_schedule", who, pgErr.ConstraintName)
		}
	}
	if m := ppMarker(t, house); m != nil {
		t.Errorf("house marker after the refused starts = %v, want NULL", m)
	}

	wdSchedule(t, c, `now()`)
	if n, err := ppExec(t, "invoice_app", c, start); err != nil || n != 1 {
		t.Fatalf("control: start the scheduled tenant: rows %d, err %v, want 1 and nil", n, err)
	}
	if ppMarker(t, c) == nil {
		t.Error("control: marker of the scheduled tenant is NULL after the start")
	}
}

func TestRLS_AppStartsOnlyItsOwnTenant(t *testing.T) {
	requireHarness(t)
	requirePurgeGuard(t)
	wdRequireColumns(t)
	const start = `UPDATE tenants SET deletion_purge_started_at = now()`
	a, b := newNamedTenant(t, "Start isolation A"), newNamedTenant(t, "Start isolation B")
	wdSchedule(t, a, `now()`)
	wdSchedule(t, b, `now()`)

	if n, err := ppExec(t, "invoice_app", "", start); err != nil || n != 0 {
		t.Fatalf("start with no GUC: rows %d, err %v, want 0 and nil", n, err)
	}
	if ppMarker(t, a) != nil || ppMarker(t, b) != nil {
		t.Error("a start with no GUC set a marker, want none")
	}
	if n, err := ppExec(t, "invoice_app", a, start+` WHERE id = $1`, b); err != nil || n != 0 {
		t.Fatalf("start of B by id bound to A: rows %d, err %v, want 0 and nil", n, err)
	}
	if n, err := ppExec(t, "invoice_app", a, start); err != nil || n != 1 {
		t.Fatalf("start bound to A: rows %d, err %v, want 1 and nil", n, err)
	}
	if ppMarker(t, a) == nil {
		t.Error("A marker is NULL after its own start")
	}
	if m := ppMarker(t, b); m != nil {
		t.Errorf("B marker = %v after a start bound to A, want NULL", m)
	}
}

func TestRLS_TenantsPurgeGuardIsAPlainFunctionWithAFixedConfig(t *testing.T) {
	requireHarness(t)
	s, ok := readProc(t, purgeGuardFn)
	if !ok {
		t.Fatalf("function %s does not exist", purgeGuardFn)
	}
	if s.owner != "invoice_migrator" {
		t.Errorf("owner = %q, want invoice_migrator", s.owner)
	}
	if s.secdef {
		t.Error("prosecdef = true, want a plain function")
	}
	if want := []string{`search_path=""`}; !reflect.DeepEqual(s.config, want) {
		t.Errorf("proconfig = %q, want %q", s.config, want)
	}
	if s.publicAny {
		t.Error("PUBLIC holds EXECUTE (or proacl is the NULL default), want revoked")
	}
	var app bool
	if err := h.super.QueryRow(context.Background(),
		`SELECT has_function_privilege('invoice_app', to_regprocedure($1), 'EXECUTE')`, purgeGuardFn).Scan(&app); err != nil {
		t.Fatalf("has_function_privilege: %v", err)
	}
	if app {
		t.Errorf("invoice_app holds EXECUTE on %s, want none", purgeGuardFn)
	}
}

func TestRLS_DeletionPurgeStartedDownDropsTheGuard(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requirePurgeGuard(t)
	wdRequireColumns(t)
	provider := staffMigrationProvider(t)
	version := migrationVersion(t, "*_deletion_purge_started.sql")

	started := newNamedTenant(t, "Down started")
	wdSchedule(t, started, `now()`)
	ppStart(t, started)
	restoreMigrationOnCleanup(t, provider, version)
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("Down with a started tenant: %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = $1`, purgeStartedCol); n != 0 {
		t.Errorf("purge-started columns after Down = %d, want 0", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM pg_trigger WHERE tgrelid = 'public.tenants'::regclass AND tgname = 'tenants_purge_guard'`); n != 0 {
		t.Errorf("tenants_purge_guard triggers after Down = %d, want 0", n)
	}
	if _, ok := readProc(t, purgeGuardFn); ok {
		t.Error("tenants_purge_guard() still exists after Down")
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.column_privileges
		  WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = $1`, purgeStartedCol); n != 0 {
		t.Errorf("column grants on %s after Down = %d, want 0", purgeStartedCol, n)
	}
	if n, err := ppExec(t, "invoice_app", started,
		`UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL`); err != nil || n != 1 {
		t.Fatalf("clear a formerly started tenant after Down: rows %d, err %v, want 1 and nil", n, err)
	}
	wdAssertUnset(t, "a formerly started tenant after Down", started)

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	requirePurgeGuard(t)
	if m := ppMarker(t, started); m != nil {
		t.Errorf("marker of a tenant that existed before Up = %v, want NULL", m)
	}
	again := newNamedTenant(t, "Up started")
	wdSchedule(t, again, `now()`)
	ppStart(t, again)
	_, err := ppExec(t, "invoice_app", again, `UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL`)
	if pgErr := wdPgErr(t, "clear a started tenant after Up", err, "42501"); pgErr.ConstraintName != "tenant_deletion_underway" {
		t.Errorf("constraint after Up = %q, want tenant_deletion_underway", pgErr.ConstraintName)
	}
}

func TestRLS_OnlyInvoiceAppAndTheOwnerCanUpdateThePurgeMarker(t *testing.T) {
	requireHarness(t)
	requirePurgeGuard(t)

	rows, err := h.super.Query(context.Background(),
		`SELECT rolname FROM pg_roles
		  WHERE NOT rolsuper AND rolname !~ '^pg_'
		    AND (has_column_privilege(oid, 'public.tenants', $1, 'UPDATE') OR has_table_privilege(oid, 'public.tenants', 'UPDATE'))
		  ORDER BY rolname`, purgeStartedCol)
	if err != nil {
		t.Fatalf("read the roles that can update the marker: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan role: %v", err)
		}
		got = append(got, r)
	}
	if want := []string{"invoice_app", "invoice_migrator"}; !reflect.DeepEqual(got, want) {
		t.Errorf("roles that can update %s = %v, want %v", purgeStartedCol, got, want)
	}
}
