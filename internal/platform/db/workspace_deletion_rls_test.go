package db_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var wdColumns = []string{"deletion_scheduled_at", "deletion_due_at", "deletion_scheduled_by"}

// wdRequireColumns fails the test when the migration is absent, so the red reason is the missing columns.
func wdRequireColumns(t *testing.T) {
	t.Helper()
	var n int
	if err := h.super.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = ANY($1)`, wdColumns,
	).Scan(&n); err != nil {
		t.Fatalf("read tenants columns: %v", err)
	}
	if n != len(wdColumns) {
		t.Fatalf("tenants has %d of the %d deletion columns %v: the workspace_deletion migration is not applied", n, len(wdColumns), wdColumns)
	}
}

// wdHouseTenant returns the house tenant, inserting a throwaway one (removed on cleanup) when none exists.
func wdHouseTenant(t *testing.T) string {
	t.Helper()
	requireHouseTable(t)
	ctx := context.Background()
	var id string
	err := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&id)
	if err == nil {
		return id
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("read the house row: %v", err)
	}
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'House (deletion probe)')`, id); err != nil {
		t.Fatalf("seed the house tenant: %v", err)
	}
	if _, err := h.super.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, id); err != nil {
		t.Fatalf("seed the house row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM public.house_company WHERE tenant_id = $1`, id)
		_, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

type wdState struct {
	scheduledAt, dueAt *time.Time
	scheduledBy        *string
}

func wdRead(t *testing.T, id string) wdState {
	t.Helper()
	var s wdState
	if err := h.super.QueryRow(context.Background(),
		`SELECT deletion_scheduled_at, deletion_due_at, deletion_scheduled_by::text FROM tenants WHERE id = $1`, id,
	).Scan(&s.scheduledAt, &s.dueAt, &s.scheduledBy); err != nil {
		t.Fatalf("read the deletion columns of %s: %v", id, err)
	}
	return s
}

func wdAssertUnset(t *testing.T, what, id string) {
	t.Helper()
	if s := wdRead(t, id); s.scheduledAt != nil || s.dueAt != nil || s.scheduledBy != nil {
		t.Errorf("%s: deletion columns = %+v, want all NULL", what, s)
	}
}

// wdSchedule writes the schedule as the superuser, at the given instant (SQL expression).
func wdSchedule(t *testing.T, id, at string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = `+at+`, deletion_scheduled_by = $2 WHERE id = $1`, id, uuid.NewString(),
	); err != nil {
		t.Fatalf("schedule %s: %v", id, err)
	}
}

func wdClear(t *testing.T, id string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL WHERE id = $1`, id,
	); err != nil {
		t.Fatalf("clear the schedule of %s: %v", id, err)
	}
}

func wdPgErr(t *testing.T, what string, err error, code string) *pgconn.PgError {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: want SQLSTATE %s, got %v", what, code, err)
	}
	if pgErr.Code != code {
		t.Fatalf("%s: want SQLSTATE %s, got %s %q", what, code, pgErr.Code, pgErr.Message)
	}
	return pgErr
}

func TestRLS_TenantsDeletionColumnsAreNullableAndEmpty(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)

	rows, err := h.super.Query(ctx,
		`SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = ANY($1) ORDER BY column_name`, wdColumns)
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var name, typ, nullable string
		var dflt *string
		if err := rows.Scan(&name, &typ, &nullable, &dflt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if nullable != "YES" || dflt != nil {
			t.Errorf("%s: nullable = %s, default = %v, want YES and no default", name, nullable, dflt)
		}
		got[name] = typ
	}
	want := map[string]string{
		"deletion_scheduled_at": "timestamp with time zone",
		"deletion_due_at":       "timestamp with time zone",
		"deletion_scheduled_by": "uuid",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("column types = %v, want %v", got, want)
	}

	if mustCount(t, h.super, `SELECT count(*) FROM tenants`) == 0 {
		t.Fatal("no tenants rows: the empty check below would be vacuous")
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM tenants WHERE deletion_scheduled_at IS NOT NULL OR deletion_due_at IS NOT NULL OR deletion_scheduled_by IS NOT NULL`); n != 0 {
		t.Errorf("tenants with a deletion column set = %d, want 0", n)
	}
}

func TestRLS_TenantsDeletionDueIsThirtyUTCDaysAfterScheduling(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)

	// The 30 days cross the 2026-10-25 EU and 2026-11-01 US DST ends; a local-time interval lands an hour off.
	for _, zone := range []string{"Europe/Amsterdam", "America/New_York", "UTC"} {
		t.Run(zone, func(t *testing.T) {
			id := newNamedTenant(t, "Due probe "+zone)
			by := uuid.NewString()
			var due time.Time
			var sessionZone string
			err := inAppTx(ctx, id, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SELECT set_config('TimeZone', $1, true)`, zone); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx,
					`UPDATE tenants SET deletion_scheduled_at = '2026-10-10 12:00:00+00', deletion_scheduled_by = $1`, by); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT current_setting('TimeZone')`).Scan(&sessionZone); err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT deletion_due_at FROM tenants`).Scan(&due)
			})
			if err != nil {
				t.Fatalf("schedule as invoice_app: %v", err)
			}
			if want := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC); !due.Equal(want) {
				t.Errorf("deletion_due_at = %s, want %s (720 h after the schedule)", due.UTC(), want)
			}
			if sessionZone != zone {
				t.Errorf("session TimeZone after the update = %q, want %q: the trigger must not leak its own", sessionZone, zone)
			}
			if s := wdRead(t, id); s.scheduledBy == nil || *s.scheduledBy != by {
				t.Errorf("deletion_scheduled_by = %v, want %s", s.scheduledBy, by)
			}
		})
	}
}

func TestRLS_TenantsDeletionClearingTheScheduleClearsTheDue(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)
	id := newNamedTenant(t, "Clear probe")
	wdSchedule(t, id, `now()`)
	if s := wdRead(t, id); s.dueAt == nil {
		t.Fatal("control: a scheduled tenant has no deletion_due_at, so clearing it proves nothing")
	}

	err := inAppTx(ctx, id, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL`)
		return e
	})
	if err != nil {
		t.Fatalf("clear as invoice_app: %v", err)
	}
	wdAssertUnset(t, "after the clear", id)
}

func TestRLS_TenantsDeletionIsAllOrNone(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)
	const constraint = "tenants_deletion_all_or_none"

	scheduled := newNamedTenant(t, "All-or-none scheduled")
	wdSchedule(t, scheduled, `now()`)
	before := wdRead(t, scheduled)
	if before.scheduledAt == nil || before.dueAt == nil || before.scheduledBy == nil {
		t.Fatalf("control: scheduled tenant = %+v, want all three set", before)
	}
	_, err := h.super.Exec(ctx, `UPDATE tenants SET deletion_scheduled_by = NULL WHERE id = $1`, scheduled)
	if pgErr := wdPgErr(t, "clear only deletion_scheduled_by", err, "23514"); pgErr.ConstraintName != constraint {
		t.Errorf("constraint = %q, want %q", pgErr.ConstraintName, constraint)
	}
	if after := wdRead(t, scheduled); !reflect.DeepEqual(after, before) {
		t.Errorf("row after the refusal = %+v, want unchanged %+v", after, before)
	}

	fresh := newNamedTenant(t, "All-or-none fresh")
	_, err = h.super.Exec(ctx, `UPDATE tenants SET deletion_scheduled_at = now() WHERE id = $1`, fresh)
	if pgErr := wdPgErr(t, "set only deletion_scheduled_at", err, "23514"); pgErr.ConstraintName != constraint {
		t.Errorf("constraint = %q, want %q", pgErr.ConstraintName, constraint)
	}
	wdAssertUnset(t, "after the refusal", fresh)

	// With the trigger bypassed the CHECK alone must still refuse a half-set row.
	for _, set := range []string{`deletion_due_at = now()`, `deletion_scheduled_at = now(), deletion_scheduled_by = gen_random_uuid()`} {
		tx, err := h.super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatalf("bypass the trigger: %v", err)
		}
		_, err = tx.Exec(ctx, `UPDATE tenants SET `+set+` WHERE id = $1`, fresh)
		_ = tx.Rollback(ctx)
		if pgErr := wdPgErr(t, "trigger bypassed, "+set, err, "23514"); pgErr.ConstraintName != constraint {
			t.Errorf("trigger bypassed, %s: constraint = %q, want %q", set, pgErr.ConstraintName, constraint)
		}
	}
}

func TestRLS_AppUpdatesOnlyTheTwoScheduleColumnsOfItsTenant(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)
	a, b := newNamedTenant(t, "Grant probe A"), newNamedTenant(t, "Grant probe B")
	const set = `UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $1`

	var n int64
	if err := inAppTx(ctx, "", func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, set, uuid.NewString())
		n = tag.RowsAffected()
		return e
	}); err != nil {
		t.Fatalf("update with no GUC: %v", err)
	}
	if n != 0 {
		t.Errorf("rows updated with no GUC = %d, want 0", n)
	}
	wdAssertUnset(t, "A after the no-GUC update", a)
	wdAssertUnset(t, "B after the no-GUC update", b)

	by := uuid.NewString()
	if err := inAppTx(ctx, a, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, set, by)
		n = tag.RowsAffected()
		return e
	}); err != nil {
		t.Fatalf("update bound to A: %v", err)
	}
	if n != 1 {
		t.Errorf("rows updated bound to A = %d, want 1", n)
	}
	if s := wdRead(t, a); s.scheduledAt == nil || s.dueAt == nil || s.scheduledBy == nil || *s.scheduledBy != by {
		t.Errorf("A after its own update = %+v, want scheduled by %s", s, by)
	}
	wdAssertUnset(t, "B after an update bound to A", b)

	if err := inAppTx(ctx, a, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, set+` WHERE id = $2`, uuid.NewString(), b)
		n = tag.RowsAffected()
		return e
	}); err != nil {
		t.Fatalf("update of B by id bound to A: %v", err)
	}
	if n != 0 {
		t.Errorf("rows of B updated by id while bound to A = %d, want 0", n)
	}
	wdAssertUnset(t, "B after an update that names it, bound to A", b)

	freshID := uuid.NewString()
	cleanupTenant(t, freshID)
	for _, c := range []struct{ what, guc, sql string }{
		{"UPDATE deletion_due_at", a, `UPDATE tenants SET deletion_due_at = now()`},
		{"UPDATE name", a, `UPDATE tenants SET name = 'x'`},
		{"INSERT", freshID, `INSERT INTO tenants (id, name) VALUES ('` + freshID + `', 'x')`},
	} {
		err := inAppTx(ctx, c.guc, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, c.sql)
			return e
		})
		assertPgRefusal(t, c.what+" as invoice_app", err, "42501", "permission denied for table tenants")
	}
}

func TestRLS_TheHouseTenantRefusesADeletionSchedule(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)
	house := wdHouseTenant(t)
	other := newNamedTenant(t, "House control")
	const set = `UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $1`

	if err := inAppTx(ctx, other, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, set, uuid.NewString())
		return e
	}); err != nil {
		t.Fatalf("control: schedule a non-house tenant as invoice_app: %v", err)
	}

	for _, c := range []struct {
		who  string
		pool func() error
	}{
		{"invoice_app", func() error {
			return inAppTx(ctx, house, func(tx pgx.Tx) error { _, e := tx.Exec(ctx, set, uuid.NewString()); return e })
		}},
		{"invoice_migrator", func() error {
			tx, err := h.mig.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, house); err != nil {
				return err
			}
			_, e := tx.Exec(ctx, set+` WHERE id = '`+house+`'`, uuid.NewString())
			return e
		}},
	} {
		pgErr := wdPgErr(t, "schedule the house tenant as "+c.who, c.pool(), "42501")
		if pgErr.ConstraintName != "house_tenant_not_deletable" {
			t.Errorf("%s: constraint = %q, want house_tenant_not_deletable (message %q)", c.who, pgErr.ConstraintName, pgErr.Message)
		}
		wdAssertUnset(t, "house tenant after "+c.who, house)
	}
}

func TestRLS_TenantsDeletionGuardIsADefinerWithAFixedConfig(t *testing.T) {
	requireHarness(t)
	const sig = "public.tenants_deletion_guard()"
	s, ok := readProc(t, sig)
	if !ok {
		t.Fatalf("function %s does not exist", sig)
	}
	if s.owner != "invoice_migrator" {
		t.Errorf("owner = %q, want invoice_migrator", s.owner)
	}
	if !s.secdef {
		t.Errorf("prosecdef = false, want SECURITY DEFINER")
	}
	got, want := append([]string(nil), s.config...), []string{`TimeZone=UTC`, `search_path=""`}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("proconfig = %q, want %q", s.config, want)
	}
	if s.publicAny {
		t.Errorf("PUBLIC holds EXECUTE (or proacl is the NULL default), want revoked")
	}
	var app bool
	if err := h.super.QueryRow(context.Background(),
		`SELECT has_function_privilege('invoice_app', to_regprocedure($1), 'EXECUTE')`, sig).Scan(&app); err != nil {
		t.Fatalf("has_function_privilege: %v", err)
	}
	if app {
		t.Errorf("invoice_app holds EXECUTE on %s, want none", sig)
	}
}

func TestRLS_ProvisionedTenantStartsUnscheduled(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)
	id := uuid.NewString()
	cleanupTenant(t, id)

	if err := provisionAs(ctx, h.app, id, newProvisionArgs(id)); err != nil {
		t.Fatalf("provision_workspace as invoice_app: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM tenants WHERE id = $1`, id); n != 1 {
		t.Fatalf("tenants rows for the provisioned id = %d, want 1", n)
	}
	wdAssertUnset(t, "provisioned tenant", id)
}

func TestRLS_ALockedWorkspacesInvitesReadAsNotPending(t *testing.T) {
	requireHarness(t)
	wdRequireColumns(t)
	type seeded struct {
		tenant, token, email string
	}
	seed := func(name string) seeded {
		s := seeded{tenant: newNamedTenant(t, name), token: newToken(t), email: uniqueAddr("locked")}
		seedAcceptInvite(t, s.tenant, "reviewer", s.email, s.token)
		return s
	}
	a, b := seed("Locked invite A"), seed("Open invite B")

	answers := func(s seeded) (byToken, pending int, pendingForEmail bool) {
		return len(lookupAs(t, "", s.token)), len(pendingFor(t, "", s.email)), invitationPendingFor(t, "", s.email)
	}
	expect := func(what string, s seeded, byToken, pending int, pendingForEmail bool) {
		t.Helper()
		gt, gp, ge := answers(s)
		if gt != byToken || gp != pending || ge != pendingForEmail {
			t.Errorf("%s: (invitation_by_token rows, pending_invites_for_email rows, invitation_pending_for_email) = (%d, %d, %v), want (%d, %d, %v)",
				what, gt, gp, ge, byToken, pending, pendingForEmail)
		}
	}

	expect("A before the schedule (control)", a, 1, 1, true)
	wdSchedule(t, a.tenant, `now()`)
	expect("A scheduled", a, 0, 0, false)
	expect("B while A is scheduled", b, 1, 1, true)
	wdClear(t, a.tenant)
	expect("A after the schedule is cleared", a, 1, 1, true)
}

func TestRLS_InviteReadersStayDefinersOfAuthHookReader(t *testing.T) {
	requireHarness(t)
	for _, sig := range []string{byTokenSig, pendingSig, pendingForEmailSig} {
		s, ok := readProc(t, sig)
		if !ok {
			t.Errorf("function %s does not exist", sig)
			continue
		}
		if s.owner != "auth_hook_reader" {
			t.Errorf("%s owner = %q, want auth_hook_reader", sig, s.owner)
		}
		if !s.secdef {
			t.Errorf("%s prosecdef = false, want SECURITY DEFINER", sig)
		}
		if !reflect.DeepEqual(s.config, []string{`search_path=""`}) {
			t.Errorf("%s proconfig = %q, want [search_path=\"\"]", sig, s.config)
		}
		assertExecuteOnlyForApp(t, sig, s, "auth_hook_reader")
	}

	rows, err := h.super.Query(context.Background(),
		`SELECT column_name FROM information_schema.column_privileges
		  WHERE grantee = 'auth_hook_reader' AND table_schema = 'public' AND table_name = 'tenants' AND privilege_type = 'SELECT'
		  ORDER BY column_name`)
	if err != nil {
		t.Fatalf("read column privileges: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, c)
	}
	if want := []string{"deletion_due_at", "id", "name"}; !reflect.DeepEqual(got, want) {
		t.Errorf("auth_hook_reader SELECT columns on tenants = %v, want %v", got, want)
	}
}

func TestRLS_TenantsDeletionDueCannotBeForgedByAnyWriter(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)
	at1 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	at2 := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	id := newNamedTenant(t, "Forge probe")
	wdSchedule(t, id, `'2026-10-10 12:00:00+00'`)
	if _, err := h.super.Exec(ctx, `UPDATE tenants SET deletion_due_at = '2030-01-01 00:00:00+00' WHERE id = $1`, id); err != nil {
		t.Fatalf("write deletion_due_at as the superuser: %v", err)
	}
	if s := wdRead(t, id); s.dueAt == nil || !s.dueAt.Equal(at1.AddDate(0, 0, 30)) {
		t.Errorf("deletion_due_at after a direct write = %v, want the derived %s", s.dueAt, at1.AddDate(0, 0, 30))
	}

	wdSchedule(t, id, `'2026-12-01 00:00:00+00'`)
	if s := wdRead(t, id); s.dueAt == nil || !s.dueAt.Equal(at2.AddDate(0, 0, 30)) {
		t.Errorf("deletion_due_at after a reschedule = %v, want %s", s.dueAt, at2.AddDate(0, 0, 30))
	}

	ins := uuid.NewString()
	cleanupTenant(t, ins)
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name, deletion_scheduled_at, deletion_due_at, deletion_scheduled_by)
		 VALUES ($1, 'Forge insert', '2026-10-10 12:00:00+00', '2000-01-01 00:00:00+00', $2)`, ins, uuid.NewString()); err != nil {
		t.Fatalf("insert a scheduled tenant as the superuser: %v", err)
	}
	if s := wdRead(t, ins); s.dueAt == nil || !s.dueAt.Equal(at1.AddDate(0, 0, 30)) {
		t.Errorf("deletion_due_at of an inserted row = %v, want the derived %s", s.dueAt, at1.AddDate(0, 0, 30))
	}
}

func TestRLS_ALockedWorkspaceHidesOnlyItsOwnInviteForASharedAddress(t *testing.T) {
	requireHarness(t)
	wdRequireColumns(t)
	email := uniqueAddr("shared")
	a, b := newNamedTenant(t, "Shared invite A"), newNamedTenant(t, "Shared invite B")
	tokA, tokB := newToken(t), newToken(t)
	idA := seedAcceptInvite(t, a, "reviewer", email, tokA)
	idB := seedAcceptInvite(t, b, "reviewer", email, tokB)

	ids := func() []string {
		var out []string
		for _, r := range pendingFor(t, "", email) {
			out = append(out, r.id)
		}
		return out
	}
	if got := ids(); len(got) != 2 {
		t.Fatalf("control: pending invites for the shared address = %v, want both", got)
	}
	wdSchedule(t, a, `now()`)
	if got := ids(); !reflect.DeepEqual(got, []string{idB}) {
		t.Errorf("pending invites with A locked = %v, want only B's %s", got, idB)
	}
	if !invitationPendingFor(t, "", email) {
		t.Error("invitation_pending_for_email with A locked and B open = false, want true")
	}
	if n := len(lookupAs(t, "", tokA)); n != 0 {
		t.Errorf("invitation_by_token for A's token = %d rows, want 0", n)
	}
	if n := len(lookupAs(t, "", tokB)); n != 1 {
		t.Errorf("invitation_by_token for B's token = %d rows, want 1", n)
	}
	wdSchedule(t, b, `now()`)
	if got := ids(); len(got) != 0 {
		t.Errorf("pending invites with both locked = %v, want none", got)
	}
	if invitationPendingFor(t, "", email) {
		t.Error("invitation_pending_for_email with both locked = true, want false")
	}
	wdClear(t, a)
	if got := ids(); !reflect.DeepEqual(got, []string{idA}) {
		t.Errorf("pending invites with only A cleared = %v, want %s", got, idA)
	}
}

// Down must put the three old bodies back: a SQL function body is not checked against dropped columns.
func TestRLS_WorkspaceDeletionDownRestoresTheInviteReadersAndRoundTrips(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	wdRequireColumns(t)
	provider := staffMigrationProvider(t)
	version := migrationVersion(t, "*_workspace_deletion.sql")
	purge := migrationVersion(t, "*_deletion_purge_started.sql")

	tenant, token, email := newNamedTenant(t, "Down probe"), newToken(t), uniqueAddr("down")
	seedAcceptInvite(t, tenant, "reviewer", email, token)
	wdSchedule(t, tenant, `now()`)
	deletionRead := migrationVersion(t, "*_staff_read_tenant_deletions.sql")
	t.Cleanup(func() { requirePurgeGuard(t); requireStaffDeletionRead(t) })
	restoreMigrationOnCleanup(t, provider, deletionRead)
	restoreMigrationOnCleanup(t, provider, purge)
	restoreMigrationOnCleanup(t, provider, version)
	if _, err := provider.ApplyVersion(ctx, deletionRead, false); err != nil {
		t.Fatalf("Down of the staff-read-deletions migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, purge, false); err != nil {
		t.Fatalf("Down of the purge-started migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("Down of the deletion migration with a scheduled tenant: %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = ANY($1)`, wdColumns); n != 0 {
		t.Errorf("deletion columns after Down = %d, want 0", n)
	}
	if _, ok := readProc(t, "public.tenants_deletion_guard()"); ok {
		t.Error("tenants_deletion_guard still exists after Down")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM pg_trigger WHERE tgrelid = 'public.tenants'::regclass AND tgname = 'tenants_deletion_guard'`); n != 0 {
		t.Errorf("tenants_deletion_guard triggers after Down = %d, want 0", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.column_privileges WHERE table_schema = 'public' AND table_name = 'tenants' AND grantee = 'invoice_app' AND privilege_type = 'UPDATE'`); n != 0 {
		t.Errorf("invoice_app UPDATE column grants on tenants after Down = %d, want 0", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.column_privileges WHERE table_schema = 'public' AND table_name = 'tenants' AND grantee = 'auth_hook_reader' AND privilege_type = 'SELECT'`); n != 2 {
		t.Errorf("auth_hook_reader SELECT column grants on tenants after Down = %d, want 2 (id, name)", n)
	}
	for _, sig := range []string{byTokenSig, pendingSig, pendingForEmailSig} {
		s, ok := readProc(t, sig)
		if !ok {
			t.Errorf("%s is gone after Down", sig)
			continue
		}
		if s.owner != "auth_hook_reader" || !s.secdef || !reflect.DeepEqual(s.config, []string{`search_path=""`}) {
			t.Errorf("%s after Down = %+v, want a SECURITY DEFINER of auth_hook_reader with search_path fixed", sig, s)
		}
		assertExecuteOnlyForApp(t, sig, s, "auth_hook_reader")
		var src string
		if err := h.super.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid = to_regprocedure($1)`, sig).Scan(&src); err != nil {
			t.Fatalf("read the body of %s: %v", sig, err)
		}
		if strings.Contains(src, "deletion_due_at") {
			t.Errorf("%s still reads deletion_due_at after Down", sig)
		}
	}
	if n := len(lookupAs(t, "", token)); n != 1 {
		t.Errorf("invitation_by_token after Down = %d rows, want 1", n)
	}
	if n := len(pendingFor(t, "", email)); n != 1 {
		t.Errorf("pending_invites_for_email after Down = %d rows, want 1", n)
	}
	if !invitationPendingFor(t, "", email) {
		t.Error("invitation_pending_for_email after Down = false, want true")
	}

	revEmail, revToken := uniqueAddr("down-revoked"), newToken(t)
	setInviteState(t, seedAcceptInvite(t, tenant, "reviewer", revEmail, revToken), "status = 'revoked'")
	if n := len(lookupAs(t, "", revToken)) + len(pendingFor(t, "", revEmail)); n != 0 || invitationPendingFor(t, "", revEmail) {
		t.Errorf("a revoked invite after Down: by_token + pending rows = %d, pending_for_email = %v, want 0 and false", n, invitationPendingFor(t, "", revEmail))
	}
	removedUser, activeUser := uuid.NewString(), uuid.NewString()
	seedMemberRow(t, h.tenantA, removedUser, "preparer", "removed", nil, nil)
	seedMemberRow(t, h.tenantB, activeUser, "preparer", "active", nil, nil)
	if n := len(pendingForUser(t, "", email, removedUser)); n != 1 {
		t.Errorf("pending_invites_for_email for a removed member after Down = %d rows, want 1", n)
	}
	if n := len(pendingForUser(t, "", email, activeUser)); n != 0 {
		t.Errorf("pending_invites_for_email for an active member after Down = %d rows, want 0", n)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("Up of the deletion migration after Down: %v", err)
	}
	wdAssertUnset(t, "a tenant that existed before Up", tenant)
	wdSchedule(t, tenant, `now()`)
	if n := len(lookupAs(t, "", token)); n != 0 {
		t.Errorf("invitation_by_token after the round trip, tenant scheduled = %d rows, want 0", n)
	}
}
