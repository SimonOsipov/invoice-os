package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const staffReasonText = "customer asked by email"

// staffDelWorld is a firm tenant with an active admin and an active house member acting as staff.
type staffDelWorld struct {
	invWorld
	houseID, staff string
	ctx            context.Context
}

func newStaffDelWorld(t *testing.T) staffDelWorld {
	t.Helper()
	w := newInvWorld(t, obi, "Ada Obi")
	houseID, staff := houseMember(t, w.super, "active")
	return staffDelWorld{invWorld: w, houseID: houseID, staff: staff, ctx: staffCtx(staff)}
}

type staffRow struct {
	Actor, Event string
	Payload      map[string]any
}

// staffRowsOf reads the tenant's staff_audit_log rows of one event, oldest first.
func staffRowsOf(t *testing.T, super *pgxpool.Pool, tenant, event string) []staffRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor::text, event, payload FROM public.staff_audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id`, tenant, event)
	if err != nil {
		t.Fatalf("read staff_audit_log: %v", err)
	}
	defer rows.Close()
	var out []staffRow
	for rows.Next() {
		var r staffRow
		var raw []byte
		if err := rows.Scan(&r.Actor, &r.Event, &raw); err != nil {
			t.Fatalf("scan staff_audit_log: %v", err)
		}
		r.Payload = map[string]any{}
		if err := json.Unmarshal(raw, &r.Payload); err != nil {
			t.Fatalf("decode staff payload %s: %v", raw, err)
		}
		out = append(out, r)
	}
	return out
}

// staffRowCount counts every staff_audit_log row written by one staff subject.
func staffRowCount(t *testing.T, super *pgxpool.Pool, actor string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM public.staff_audit_log WHERE actor = $1`, actor).Scan(&n); err != nil {
		t.Fatalf("count staff_audit_log: %v", err)
	}
	return n
}

func tenantRowsOfStaff(t *testing.T, super *pgxpool.Pool, tenant string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM public.staff_audit_log WHERE tenant_id = $1`, tenant).Scan(&n); err != nil {
		t.Fatalf("count staff_audit_log by tenant: %v", err)
	}
	return n
}

func workspaceLocked(t *testing.T, app *pgxpool.Pool, tenant string) bool {
	t.Helper()
	var locked bool
	err := db.WithinTenantTx(context.Background(), app, tenant, func(tx pgx.Tx) error {
		var err error
		locked, err = db.WorkspaceLocked(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatalf("read the workspace lock: %v", err)
	}
	return locked
}

// failStaffAudit makes the insert of one staff event of a tenant raise; the returned func drops the trigger.
func failStaffAudit(t *testing.T, super *pgxpool.Pool, tenant, event string) (drop func()) {
	t.Helper()
	ctx := context.Background()
	name := "staff_audit_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := super.Exec(ctx,
		`CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced staff audit failure' USING ERRCODE = 'check_violation'; END; $$`); err != nil {
		t.Fatalf("create the forced-failure function: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+name+`() CASCADE`) })
	if _, err := super.Exec(ctx,
		`CREATE TRIGGER `+name+` BEFORE INSERT ON public.staff_audit_log FOR EACH ROW
		 WHEN (NEW.tenant_id = `+quoteLiteral(tenant)+`::uuid AND NEW.event = `+quoteLiteral(event)+`) EXECUTE FUNCTION `+name+`()`); err != nil {
		t.Fatalf("create the forced-failure trigger: %v", err)
	}
	return func() {
		if _, err := super.Exec(context.Background(), `DROP TRIGGER `+name+` ON public.staff_audit_log`); err != nil {
			t.Fatalf("drop the forced-failure trigger: %v", err)
		}
	}
}

func TestStaffDeletion_StartLocksTheWorkspaceWithStaffAsActor(t *testing.T) {
	w := newStaffDelWorld(t)
	if workspaceLocked(t, w.app, w.tenant) {
		t.Fatal("setup: the workspace is locked before the start")
	}
	before := time.Now()

	due, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("StaffScheduleWorkspaceDeletion: %v", err)
	}
	near(t, "due", due, before.Add(720*time.Hour), time.Minute) // the DEL-04 trigger's 30 days

	c := deletionOf(t, w.super, w.tenant)
	if c.ScheduledAt == nil {
		t.Error("deletion_scheduled_at is NULL after the start")
	}
	if c.By == nil || *c.By != w.staff {
		t.Errorf("deletion_scheduled_by = %v, want the staff subject %s", c.By, w.staff)
	}
	if c.DueAt == nil || !c.DueAt.Equal(due) {
		t.Errorf("stored due = %v, want the returned %v", c.DueAt, due)
	}
	if !workspaceLocked(t, w.app, w.tenant) {
		t.Error("db.WorkspaceLocked = false after the start, want true")
	}
	if _, err := w.store.ListMemberships(w.adminCtx()); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Errorf("a member's ListMemberships after the start: err = %v, want db.ErrWorkspaceLocked", err)
	}
}

func TestStaffDeletion_StartWritesOneStaffRowAndTheCustomerRow(t *testing.T) {
	w := newStaffDelWorld(t)

	due, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("StaffScheduleWorkspaceDeletion: %v", err)
	}

	rows := staffRowsOf(t, w.super, w.tenant, evScheduled)
	if len(rows) != 1 {
		t.Fatalf("staff_audit_log %s rows = %d, want 1", evScheduled, len(rows))
	}
	if rows[0].Actor != w.staff {
		t.Errorf("staff row actor = %s, want the staff subject %s", rows[0].Actor, w.staff)
	}
	if !slices.Equal(keysOf(rows[0].Payload), []string{"due_at", "reason"}) {
		t.Errorf("staff payload keys = %v, want exactly [due_at reason]", keysOf(rows[0].Payload))
	}
	if got := rows[0].Payload["due_at"]; got != due.UTC().Format(time.RFC3339) {
		t.Errorf("payload due_at = %v, want %s", got, due.UTC().Format(time.RFC3339))
	}
	if got := rows[0].Payload["reason"]; got != staffReasonText {
		t.Errorf("payload reason = %v, want %q", got, staffReasonText)
	}

	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 1 {
		t.Fatalf("audit_log %s rows = %d, want 1", evScheduled, n)
	}
	if got := auditActor(t, w.app, w.tenant, evScheduled); got != audit.SupportActor { // "ASComply support"
		t.Errorf("audit_log actor = %q, want %q", got, audit.SupportActor)
	}
}

func TestStaffDeletion_AFailedStaffRowLeavesTheTenantUnscheduled(t *testing.T) {
	w := newStaffDelWorld(t)
	drop := failStaffAudit(t, w.super, w.tenant, evScheduled)

	_, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
	if err == nil {
		t.Fatal("StaffScheduleWorkspaceDeletion succeeded although its staff row fails")
	}
	requireNoSentinel(t, err)
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns after the failed staff row = %+v, want all NULL", c)
	}
	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 0 {
		t.Errorf("audit_log %s rows = %d after the rollback, want 0", evScheduled, n)
	}

	drop()
	if _, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Errorf("start after the trigger is gone: %v", err)
	}
}

func TestStaffDeletion_StartRefusals(t *testing.T) {
	w := newStaffDelWorld(t)
	scheduled := seedTenant(t, w.super, "already scheduled")
	lockByOwner(t, w.super, scheduled, w.staff)
	dueBefore := deletionOf(t, w.super, scheduled).DueAt
	if dueBefore == nil {
		t.Fatal("setup: the scheduled tenant has no due time")
	}

	for _, tc := range []struct {
		name   string
		tenant string
		want   error
	}{
		{"an already scheduled tenant", scheduled, db.ErrWorkspaceLocked},
		{"the house tenant", w.houseID, ErrHouseWorkspace},
		{"an unknown tenant", uuid.NewString(), ErrTenantNotFound},
		{"a malformed id", "x", ErrTenantNotFound},
		{"an empty id", "", ErrTenantNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rowsBefore := staffRowCount(t, w.super, w.staff)
			_, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, tc.tenant, staffReasonText)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				t.Errorf("err = %v, want the mapped sentinel, not the raw database error", err)
			}
			if got := staffRowCount(t, w.super, w.staff); got != rowsBefore {
				t.Errorf("staff_audit_log rows by the staff subject = %d, want %d unchanged", got, rowsBefore)
			}
		})
	}
	if got := deletionOf(t, w.super, scheduled).DueAt; got == nil || !got.Equal(*dueBefore) {
		t.Errorf("the scheduled tenant's due = %v, want %v unchanged", got, dueBefore)
	}
	if c := deletionOf(t, w.super, w.houseID); !c.empty() {
		t.Errorf("the house tenant's deletion columns = %+v, want all NULL", c)
	}

	t.Run("control: an unscheduled firm tenant starts", func(t *testing.T) {
		if _, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
			t.Errorf("start of an unscheduled tenant: %v", err)
		}
	})
}

func TestStaffDeletion_CancelUnlocksAndAudits(t *testing.T) {
	w := newStaffDelWorld(t)
	due, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("setup start: %v", err)
	}
	if !workspaceLocked(t, w.app, w.tenant) {
		t.Fatal("setup: the workspace is not locked after the start")
	}

	const reason = "customer changed their mind"
	if err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, reason); err != nil {
		t.Fatalf("StaffCancelWorkspaceDeletion: %v", err)
	}

	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns after the cancel = %+v, want all NULL", c)
	}
	if workspaceLocked(t, w.app, w.tenant) {
		t.Error("db.WorkspaceLocked = true after the cancel, want false")
	}
	if _, err := w.store.ListMemberships(w.adminCtx()); err != nil {
		t.Errorf("a member's ListMemberships after the cancel: %v", err)
	}
	rows := staffRowsOf(t, w.super, w.tenant, evCancelled)
	if len(rows) != 1 {
		t.Fatalf("staff_audit_log %s rows = %d, want 1", evCancelled, len(rows))
	}
	if rows[0].Actor != w.staff {
		t.Errorf("staff row actor = %s, want the staff subject %s", rows[0].Actor, w.staff)
	}
	if !slices.Equal(keysOf(rows[0].Payload), []string{"due_at", "reason"}) {
		t.Errorf("payload keys = %v, want exactly [due_at reason]", keysOf(rows[0].Payload))
	}
	if got := rows[0].Payload["due_at"]; got != due.UTC().Format(time.RFC3339) {
		t.Errorf("payload due_at = %v, want the cancelled due %s", got, due.UTC().Format(time.RFC3339))
	}
	if got := rows[0].Payload["reason"]; got != reason {
		t.Errorf("payload reason = %v, want %q", got, reason)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 1 {
		t.Fatalf("audit_log %s rows = %d, want 1", evCancelled, n)
	}
	if got := auditActor(t, w.app, w.tenant, evCancelled); got != audit.SupportActor { // "ASComply support"
		t.Errorf("audit_log actor = %q, want %q", got, audit.SupportActor)
	}
	if got := len(staffRowsOf(t, w.super, w.tenant, evScheduled)); got != 1 {
		t.Errorf("%s rows after the cancel = %d, want the start's 1 kept", evScheduled, got)
	}
}

func TestStaffDeletion_CancelOfAnUnscheduledTenantWritesNothing(t *testing.T) {
	w := newStaffDelWorld(t)

	if err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("cancel of an unscheduled tenant: err = %v, want nil", err)
	}
	if n := tenantRowsOfStaff(t, w.super, w.tenant); n != 0 {
		t.Errorf("staff_audit_log rows of the tenant = %d, want 0", n)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("audit_log %s rows = %d, want 0", evCancelled, n)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", c)
	}

	t.Run("an unknown tenant is not a no-op", func(t *testing.T) {
		if err := w.store.StaffCancelWorkspaceDeletion(w.ctx, uuid.NewString(), staffReasonText); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("err = %v, want ErrTenantNotFound", err)
		}
		if err := w.store.StaffCancelWorkspaceDeletion(w.ctx, "x", staffReasonText); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("malformed id: err = %v, want ErrTenantNotFound", err)
		}
	})
}

func TestStaffDeletion_CancelOfAStartedPurgeIsRefused(t *testing.T) {
	w := newStaffDelWorld(t)
	startPurge(t, w.super, w.tenant, w.staff)
	before := deletionOf(t, w.super, w.tenant)
	if before.DueAt == nil {
		t.Fatal("setup: no due time")
	}
	marker := purgeStartedAt(t, w.super, w.tenant)
	if marker == nil {
		t.Fatal("setup: no purge marker")
	}

	err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
	if !errors.Is(err, ErrDeletionUnderway) {
		t.Fatalf("err = %v, want ErrDeletionUnderway", err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Errorf("err = %v, want the mapped sentinel, not the raw database error", err)
	}
	if after := deletionOf(t, w.super, w.tenant); after.DueAt == nil || !after.DueAt.Equal(*before.DueAt) {
		t.Errorf("deletion_due_at = %v, want %v unchanged", after.DueAt, before.DueAt)
	}
	if got := purgeStartedAt(t, w.super, w.tenant); got == nil || !got.Equal(*marker) {
		t.Errorf("deletion_purge_started_at = %v, want %v unchanged", got, marker)
	}
	if n := tenantRowsOfStaff(t, w.super, w.tenant); n != 0 {
		t.Errorf("staff_audit_log rows of the tenant = %d, want 0", n)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("audit_log %s rows = %d, want 0", evCancelled, n)
	}
}

func TestStaffDeletion_RefusesWithoutAStaffCaller(t *testing.T) {
	w := newStaffDelWorld(t)
	other := seedTenant(t, w.super, "scheduled for the cancel")
	lockByOwner(t, w.super, other, w.staff)
	otherDue := deletionOf(t, w.super, other).DueAt
	_, suspended := houseMember(t, w.super, "suspended")

	callers := map[string]context.Context{
		"no caller":                    context.Background(),
		"a customer admin":             w.adminCtx(),
		"a customer admin, tenantless": auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: w.admin, Role: "authenticated"}),
		// The staff flag alone grants nothing: the staff_read_tenants check refuses a non-house subject.
		"the staff flag on a customer admin": staffCtx(w.admin),
		"a suspended house member":           staffCtx(suspended),
	}
	for name, ctx := range callers {
		t.Run(name, func(t *testing.T) {
			if _, err := w.store.StaffScheduleWorkspaceDeletion(ctx, w.tenant, staffReasonText); !errors.Is(err, db.ErrNotStaff) {
				t.Errorf("start: err = %v, want db.ErrNotStaff", err)
			}
			if err := w.store.StaffCancelWorkspaceDeletion(ctx, other, staffReasonText); !errors.Is(err, db.ErrNotStaff) {
				t.Errorf("cancel: err = %v, want db.ErrNotStaff", err)
			}
			if c := deletionOf(t, w.super, w.tenant); !c.empty() {
				t.Errorf("the unscheduled tenant's columns = %+v, want all NULL", c)
			}
			if got := deletionOf(t, w.super, other).DueAt; got == nil || !got.Equal(*otherDue) {
				t.Errorf("the scheduled tenant's due = %v, want %v unchanged", got, otherDue)
			}
		})
	}
	for _, tenant := range []string{w.tenant, other} {
		if n := tenantRowsOfStaff(t, w.super, tenant); n != 0 {
			t.Errorf("staff_audit_log rows of %s = %d, want 0", tenant, n)
		}
	}

	t.Run("control: the staff caller acts on the same tenants", func(t *testing.T) {
		if _, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
			t.Errorf("staff start: %v", err)
		}
		if err := w.store.StaffCancelWorkspaceDeletion(w.ctx, other, staffReasonText); err != nil {
			t.Errorf("staff cancel: %v", err)
		}
	})
}

func TestStaffDeletion_ActsOnlyOnThePathTenant(t *testing.T) {
	w := newStaffDelWorld(t)
	b := seedTenant(t, w.super, "bystander")

	if _, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("start of A: %v", err)
	}
	if c := deletionOf(t, w.super, w.tenant); c.empty() {
		t.Fatal("control: A is not scheduled after its start")
	}
	if c := deletionOf(t, w.super, b); !c.empty() {
		t.Errorf("B's deletion columns after A's start = %+v, want all NULL", c)
	}
	if n := tenantRowsOfStaff(t, w.super, b); n != 0 {
		t.Errorf("staff_audit_log rows of B = %d, want 0", n)
	}
	if n := auditCount(t, w.app, b, evScheduled); n != 0 {
		t.Errorf("audit_log %s rows of B = %d, want 0", evScheduled, n)
	}

	t.Run("a cancel of A leaves a scheduled B alone", func(t *testing.T) {
		lockByOwner(t, w.super, b, w.staff)
		bBefore := deletionOf(t, w.super, b)
		if err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
			t.Fatalf("cancel of A: %v", err)
		}
		if c := deletionOf(t, w.super, w.tenant); !c.empty() {
			t.Errorf("control: A's columns after its cancel = %+v, want all NULL", c)
		}
		if got := deletionOf(t, w.super, b); got.DueAt == nil || !got.DueAt.Equal(*bBefore.DueAt) {
			t.Errorf("B's due after A's cancel = %v, want %v unchanged", got.DueAt, bBefore.DueAt)
		}
		if n := tenantRowsOfStaff(t, w.super, b); n != 0 {
			t.Errorf("staff_audit_log rows of B = %d, want 0", n)
		}
		if n := auditCount(t, w.app, b, evCancelled); n != 0 {
			t.Errorf("audit_log %s rows of B = %d, want 0", evCancelled, n)
		}
	})
}

func TestStaffDeletion_AFailedCancelRowKeepsTheSchedule(t *testing.T) {
	w := newStaffDelWorld(t)
	due, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("setup start: %v", err)
	}
	drop := failStaffAudit(t, w.super, w.tenant, evCancelled)

	err = w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
	if err == nil {
		t.Fatal("StaffCancelWorkspaceDeletion succeeded although its staff row fails")
	}
	requireNoSentinel(t, err)
	if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil || !c.DueAt.Equal(due) || c.By == nil || *c.By != w.staff {
		t.Errorf("deletion columns after the failed cancel = %+v, want the schedule kept (due %v, by %s)", c, due, w.staff)
	}
	if !workspaceLocked(t, w.app, w.tenant) {
		t.Error("db.WorkspaceLocked = false after the failed cancel, want true")
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("audit_log %s rows = %d after the rollback, want 0", evCancelled, n)
	}

	drop()
	if err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Errorf("cancel after the trigger is gone: %v", err)
	}
}

func TestStaffDeletion_ConcurrentStartsWriteOneScheduleAndOneRow(t *testing.T) {
	w := newStaffDelWorld(t)
	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, errs[i] = w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
		}()
	}
	close(gate)
	wg.Wait()

	won, locked := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, db.ErrWorkspaceLocked):
			locked++
		default:
			t.Errorf("a concurrent start failed with %v, want nil or db.ErrWorkspaceLocked", err)
		}
	}
	if won != 1 || locked != callers-1 {
		t.Errorf("%d starts won and %d were refused, want 1 and %d", won, locked, callers-1)
	}
	if n := len(staffRowsOf(t, w.super, w.tenant, evScheduled)); n != 1 {
		t.Errorf("staff_audit_log %s rows = %d, want 1", evScheduled, n)
	}
	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 1 {
		t.Errorf("audit_log %s rows = %d, want 1", evScheduled, n)
	}
}

// Whichever order the races land in, the rows say whether the tenant ends scheduled: starts minus cancels is 1 or 0.
func TestStaffDeletion_StartsAndCancelsRacingKeepTheRowsInStepWithTheState(t *testing.T) {
	w := newStaffDelWorld(t)
	if _, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("setup start: %v", err)
	}
	const pairs = 6
	var wg sync.WaitGroup
	gate := make(chan struct{})
	run := func(f func() error, allowed ...error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			err := f()
			if err != nil && !slices.ContainsFunc(allowed, func(a error) bool { return errors.Is(err, a) }) {
				t.Errorf("a racing call failed with %v", err)
			}
		}()
	}
	for range pairs {
		run(func() error {
			_, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
			return err
		}, db.ErrWorkspaceLocked)
		run(func() error { return w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText) })
	}
	close(gate)
	wg.Wait()

	starts := len(staffRowsOf(t, w.super, w.tenant, evScheduled))
	cancels := len(staffRowsOf(t, w.super, w.tenant, evCancelled))
	scheduled := !deletionOf(t, w.super, w.tenant).empty()
	if want := map[bool]int{true: 1, false: 0}[scheduled]; starts-cancels != want {
		t.Errorf("%d start rows and %d cancel rows with scheduled=%v, want starts-cancels = %d", starts, cancels, scheduled, want)
	}
	if got := auditCount(t, w.app, w.tenant, evScheduled); got != starts {
		t.Errorf("audit_log %s rows = %d, staff rows = %d, want equal", evScheduled, got, starts)
	}
	if got := auditCount(t, w.app, w.tenant, evCancelled); got != cancels {
		t.Errorf("audit_log %s rows = %d, staff rows = %d, want equal", evCancelled, got, cancels)
	}
}

func TestStaffDeletion_ACancelTwiceWritesOneRowAndARestartSchedulesAgain(t *testing.T) {
	w := newStaffDelWorld(t)
	if _, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("setup start: %v", err)
	}
	for i := range 2 {
		if err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
			t.Fatalf("cancel %d: %v", i+1, err)
		}
	}
	if n := len(staffRowsOf(t, w.super, w.tenant, evCancelled)); n != 1 {
		t.Errorf("staff_audit_log %s rows after two cancels = %d, want 1", evCancelled, n)
	}
	if _, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("restart after the cancel: %v", err)
	}
	if n := len(staffRowsOf(t, w.super, w.tenant, evScheduled)); n != 2 {
		t.Errorf("staff_audit_log %s rows after the restart = %d, want 2", evScheduled, n)
	}
	if !workspaceLocked(t, w.app, w.tenant) {
		t.Error("db.WorkspaceLocked = false after the restart, want true")
	}
}
