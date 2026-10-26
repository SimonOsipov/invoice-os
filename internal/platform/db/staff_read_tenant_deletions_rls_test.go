package db_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const staffReadTenantDeletionsSig = "public.staff_read_tenant_deletions(uuid)"

// requireStaffDeletionRead fails unless the function and both column grants are in place.
func requireStaffDeletionRead(t *testing.T) {
	t.Helper()
	var grant bool
	var reg *string
	if err := h.super.QueryRow(context.Background(),
		`SELECT has_column_privilege('staff_reader', 'public.tenants', 'deletion_purge_started_at', 'SELECT'),
		        to_regprocedure($1)::text`, staffReadTenantDeletionsSig).Scan(&grant, &reg); err != nil {
		t.Fatalf("read the staff deletion-read footprint: %v", err)
	}
	if !grant || reg == nil {
		t.Fatalf("staff_reader SELECT on tenants.deletion_purge_started_at = %v, %s registered = %v, want true and true: the staff_read_tenant_deletions migration is not applied", grant, staffReadTenantDeletionsSig, reg != nil)
	}
}

// withoutStaffDeletionRead drops the one staff_read_ migration that rollBackRemovedFirst rolls back and re-applies itself.
func withoutStaffDeletionRead(t *testing.T, versions []int64) []int64 {
	t.Helper()
	own := migrationVersion(t, "*_staff_read_tenant_deletions.sql")
	return slices.DeleteFunc(versions, func(v int64) bool { return v == own })
}

type staffDeletionRow struct {
	tenantID  string
	due, mark *time.Time
}

// collectStaffDeletions returns the query error too: a refusal surfaces from Query or rows.Err.
func collectStaffDeletions(q rowQuerier, actor any) ([]staffDeletionRow, error) {
	// SELECT * scans by position: pins the result column order and count.
	rows, err := q.Query(context.Background(), `SELECT * FROM public.staff_read_tenant_deletions($1::uuid)`, actor)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (staffDeletionRow, error) {
		var x staffDeletionRow
		return x, r.Scan(&x.tenantID, &x.due, &x.mark)
	})
}

func mustCollectStaffDeletions(t *testing.T, q rowQuerier, actor any) map[string]staffDeletionRow {
	t.Helper()
	got, err := collectStaffDeletions(q, actor)
	if err != nil {
		t.Fatalf("staff_read_tenant_deletions: %v", err)
	}
	byID := map[string]staffDeletionRow{}
	for _, r := range got {
		if _, dup := byID[r.tenantID]; dup {
			t.Errorf("tenant %s returned twice, want one row per tenant", r.tenantID)
		}
		byID[r.tenantID] = r
	}
	return byID
}

func readDeletionColumns(t *testing.T, id string) (due, mark *time.Time) {
	t.Helper()
	if err := h.super.QueryRow(context.Background(),
		`SELECT deletion_due_at, deletion_purge_started_at FROM tenants WHERE id = $1`, id).Scan(&due, &mark); err != nil {
		t.Fatalf("read the deletion columns of %s: %v", id, err)
	}
	return due, mark
}

// seedDeletionStates commits tenant A unscheduled, B scheduled and C scheduled with the purge marker.
func seedDeletionStates(t *testing.T) (a, b, c string) {
	t.Helper()
	wdRequireColumns(t)
	requirePurgeGuard(t)
	a, b, c = newNamedTenant(t, "sd unscheduled"), newNamedTenant(t, "sd scheduled"), newNamedTenant(t, "sd started")
	wdSchedule(t, b, `now()`)
	wdSchedule(t, c, `now() - interval '1 day'`)
	ppStart(t, c)
	return a, b, c
}

func requireSameInstant(t *testing.T, what string, got, want *time.Time) {
	t.Helper()
	if (got == nil) != (want == nil) || (got != nil && !got.Equal(*want)) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// One REPEATABLE READ snapshot: other packages create tenants while this runs.
func TestRLS_StaffReadTenantDeletionsReturnsEveryTenantsState(t *testing.T) {
	requireHarness(t)
	requireStaffDeletionRead(t)
	ctx := context.Background()
	member := seedHouseMember(t, "reviewer", "active")
	a, b, c := seedDeletionStates(t)
	bDue, bMark := readDeletionColumns(t, b)
	cDue, cMark := readDeletionColumns(t, c)
	if bDue == nil || bMark != nil || cDue == nil || cMark == nil {
		t.Fatalf("control: seeded B = (%v, %v), C = (%v, %v), want B (due, NULL) and C (due, marker)", bDue, bMark, cDue, cMark)
	}

	tx, err := h.super.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	want := mustCount(t, tx, `SELECT count(*) FROM tenants`)
	txExec(t, tx, `SET LOCAL ROLE invoice_app`)
	got := mustCollectStaffDeletions(t, tx, member)

	if len(got) != want {
		t.Errorf("staff_read_tenant_deletions returned %d rows, superuser count(*) FROM tenants = %d, want equal", len(got), want)
	}
	for _, w := range []struct {
		name, id  string
		due, mark *time.Time
	}{{"A unscheduled", a, nil, nil}, {"B scheduled", b, bDue, nil}, {"C started", c, cDue, cMark}} {
		r, ok := got[w.id]
		if !ok {
			t.Errorf("tenant %s is missing from the result", w.name)
			continue
		}
		requireSameInstant(t, w.name+" deletion_due_at", r.due, w.due)
		requireSameInstant(t, w.name+" deletion_purge_started_at", r.mark, w.mark)
	}
}

func TestRLS_StaffReadTenantDeletionsRefusesANonStaffActor(t *testing.T) {
	requireHarness(t)
	requireStaffDeletionRead(t)
	staff := seedHouseMember(t, "reviewer", "active")
	customer := seedCommittedMembership(t, h.tenantA, "admin", "active")

	if got, err := collectStaffDeletions(h.app, staff); err != nil || len(got) == 0 {
		t.Fatalf("control: staff_read_tenant_deletions for a house member: %d rows, err %v, want rows and no error", len(got), err)
	}
	for _, c := range []struct{ name, actor string }{
		{"customer admin", customer},
		{"stranger", "00000000-0000-4000-8000-0000000000a1"},
	} {
		got, err := collectStaffDeletions(h.app, c.actor)
		if len(got) != 0 {
			t.Errorf("%s: refused call returned %d rows", c.name, len(got))
		}
		requireNotStaffRefusal(t, c.name, err, "staff_read_tenant_deletions")
	}
}

func TestRLS_StaffReadTenantDeletionsIgnoresTheSetTenant(t *testing.T) {
	requireHarness(t)
	requireStaffDeletionRead(t)
	staff := seedHouseMember(t, "reviewer", "active")
	a, b, c := seedDeletionStates(t)

	var got map[string]staffDeletionRow
	err := db.WithinTenantTx(context.Background(), h.app, a, func(tx pgx.Tx) error {
		got = mustCollectStaffDeletions(t, tx, staff)
		return nil
	})
	if err != nil {
		t.Fatalf("staff_read_tenant_deletions with tenant A set: %v", err)
	}
	for name, id := range map[string]string{"A": a, "B": b, "C": c} {
		if _, ok := got[id]; !ok {
			t.Errorf("tenant %s is missing with tenant A set (%d rows), want every tenant", name, len(got))
		}
	}
	if r := got[c]; r.mark == nil {
		t.Errorf("tenant C marker with tenant A set = nil, want the marker")
	}
}

// The scans over every staff_read_ function pass vacuously until the function exists.
func TestRLS_StaffReadTenantDeletionsIsADefinerOfStaffReaderWithAppExecute(t *testing.T) {
	requireHarness(t)
	requireStaffDeletionRead(t)
	var found bool
	for _, fn := range requireStaffReadFunctions(t) {
		if fn.name != "staff_read_tenant_deletions" {
			continue
		}
		found = true
		if fn.owner != "staff_reader" || !fn.secdef || !reflect.DeepEqual(fn.config, []string{`search_path=""`}) || fn.publicExecute {
			t.Errorf("%s = %+v, want owner staff_reader, SECURITY DEFINER, search_path fixed, no PUBLIC EXECUTE", fn.sig, fn)
		}
		if !reflect.DeepEqual(fn.argTypes, []string{"uuid"}) || fn.firstArgName != "p_actor" {
			t.Errorf("%s arguments = %q %v, want p_actor uuid", fn.sig, fn.firstArgName, fn.argTypes)
		}
	}
	if !found {
		t.Fatal("staff_read_tenant_deletions is not among the staff_read_ functions the scans cover")
	}
	var app bool
	var result string
	if err := h.super.QueryRow(context.Background(),
		`SELECT has_function_privilege('invoice_app', $1::regprocedure, 'EXECUTE'), pg_get_function_result($1::regprocedure)`,
		staffReadTenantDeletionsSig).Scan(&app, &result); err != nil {
		t.Fatalf("read the function footprint: %v", err)
	}
	if !app {
		t.Error("invoice_app holds no EXECUTE, want it")
	}
	if want := "TABLE(tenant_id uuid, deletion_due_at timestamp with time zone, deletion_purge_started_at timestamp with time zone)"; result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

func TestRLS_StaffReaderCannotWriteTheDeletionColumns(t *testing.T) {
	requireHarness(t)
	requireStaffDeletionRead(t)
	_, _, started := seedDeletionStates(t)

	tx := revokeProbeTx(t)
	txExec(t, tx, `SELECT set_config('app.current_tenant', '', true)`)
	txExec(t, tx, `SET LOCAL ROLE staff_reader`)
	var due, mark *time.Time
	if err := tx.QueryRow(context.Background(),
		`SELECT deletion_due_at, deletion_purge_started_at FROM tenants WHERE id = $1`, started).Scan(&due, &mark); err != nil || due == nil || mark == nil {
		t.Fatalf("control: staff_reader reads the started tenant's columns as (%v, %v), err %v, want both set", due, mark, err)
	}
	for _, col := range []string{"deletion_purge_started_at", "deletion_due_at"} {
		txExec(t, tx, `SAVEPOINT w`)
		_, err := tx.Exec(context.Background(), `UPDATE tenants SET `+col+` = now() WHERE id = $1`, started)
		txExec(t, tx, `ROLLBACK TO SAVEPOINT w`)
		assertPgRefusal(t, "staff_reader UPDATE of "+col, err, sqlstateInsufficientPrivilege, "permission denied for table tenants")
	}
}

func TestRLS_StaffReadTenantDeletionsDownAndUp(t *testing.T) {
	requireHarness(t)
	requireStaffDeletionRead(t)
	ctx := context.Background()
	provider := staffMigrationProvider(t)
	version := migrationVersion(t, "*_staff_read_tenant_deletions.sql")
	member := seedHouseMember(t, "reviewer", "active")
	_, b, _ := seedDeletionStates(t)
	bDue, _ := readDeletionColumns(t, b)

	restoreMigrationOnCleanup(t, provider, version)
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	var reg *string
	var due, mark bool
	if err := h.super.QueryRow(ctx,
		`SELECT to_regprocedure($1)::text, has_column_privilege('staff_reader', 'public.tenants', 'deletion_due_at', 'SELECT'),
		        has_column_privilege('staff_reader', 'public.tenants', 'deletion_purge_started_at', 'SELECT')`,
		staffReadTenantDeletionsSig).Scan(&reg, &due, &mark); err != nil {
		t.Fatalf("read the footprint after Down: %v", err)
	}
	if reg != nil || due || mark {
		t.Errorf("after Down: function %v, SELECT on deletion_due_at %v, on deletion_purge_started_at %v, want none", reg, due, mark)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	requireStaffDeletionRead(t)
	r, ok := mustCollectStaffDeletions(t, h.app, member)[b]
	if !ok {
		t.Fatal("after Up: tenant B is missing from the result")
	}
	requireSameInstant(t, "B deletion_due_at after Up", r.due, bDue)
}

func TestRLS_StaffReadTenantDeletionsRefusesEveryOtherActor(t *testing.T) {
	requireHarness(t)
	requireStaffDeletionRead(t)
	active := seedHouseMember(t, "reviewer", "active")
	suspended := seedHouseMember(t, "reviewer", "suspended")
	customer := seedCommittedMembership(t, h.tenantA, "admin", "active")

	if got, err := collectStaffDeletions(h.app, active); err != nil || len(got) == 0 {
		t.Fatalf("control: an active house member reads %d rows, err %v, want rows and no error", len(got), err)
	}
	for _, c := range []struct {
		name  string
		actor any
	}{{"suspended house member", suspended}, {"NULL actor", nil}} {
		got, err := collectStaffDeletions(h.app, c.actor)
		if len(got) != 0 {
			t.Errorf("%s: refused call returned %d rows", c.name, len(got))
		}
		requireNotStaffRefusal(t, c.name, err, "staff_read_tenant_deletions")
	}

	// A customer admin with their own tenant set is still not staff.
	var got []staffDeletionRow
	var callErr error
	_ = db.WithinTenantTx(context.Background(), h.app, h.tenantA, func(tx pgx.Tx) error {
		got, callErr = collectStaffDeletions(tx, customer)
		return callErr
	})
	if len(got) != 0 {
		t.Errorf("customer admin with tenant A set: refused call returned %d rows", len(got))
	}
	requireNotStaffRefusal(t, "customer admin with tenant A set", callErr, "staff_read_tenant_deletions")

	// invoice_tenant_reader holds no EXECUTE: PUBLIC was revoked.
	got, err := collectStaffDeletions(h.reader, active)
	if len(got) != 0 {
		t.Errorf("invoice_tenant_reader: refused call returned %d rows", len(got))
	}
	assertPgRefusal(t, "invoice_tenant_reader call", err, sqlstateInsufficientPrivilege, "permission denied for function staff_read_tenant_deletions")
}
