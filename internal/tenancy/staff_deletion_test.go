package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
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

	due, err := staffScheduleDue(w.store, w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("StaffScheduleWorkspaceDeletion: %v", err)
	}
	near(t, "due", due, before.Add(720*time.Hour), time.Minute) // the tenants_deletion_guard trigger's 30 days

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

	due, err := staffScheduleDue(w.store, w.ctx, w.tenant, staffReasonText)
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
	due, err := staffScheduleDue(w.store, w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("setup start: %v", err)
	}
	if !workspaceLocked(t, w.app, w.tenant) {
		t.Fatal("setup: the workspace is not locked after the start")
	}

	const reason = "customer changed their mind"
	if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, reason); err != nil {
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

	if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
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
		if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, uuid.NewString(), staffReasonText); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("err = %v, want ErrTenantNotFound", err)
		}
		if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, "x", staffReasonText); !errors.Is(err, ErrTenantNotFound) {
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

	_, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
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
			if _, err := w.store.StaffCancelWorkspaceDeletion(ctx, other, staffReasonText); !errors.Is(err, db.ErrNotStaff) {
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
		if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, other, staffReasonText); err != nil {
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
		if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
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
	due, err := staffScheduleDue(w.store, w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("setup start: %v", err)
	}
	drop := failStaffAudit(t, w.super, w.tenant, evCancelled)

	_, err = w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
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
	if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
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
		run(func() error { return staffCancelErr(w.store, w.ctx, w.tenant, staffReasonText) })
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
		if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
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

func TestStaffDeletion_ConcurrentCancelsWriteOneRowPerSchedule(t *testing.T) {
	w := newStaffDelWorld(t)
	const rounds, callers = 10, 8
	for round := 1; round <= rounds; round++ {
		lockByOwner(t, w.super, w.tenant, w.staff)
		errs := make([]error, callers)
		var wg sync.WaitGroup
		gate := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				_, errs[i] = w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
			}()
		}
		close(gate)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("round %d: a concurrent cancel failed: %v", round, err)
			}
		}
		if n := len(staffRowsOf(t, w.super, w.tenant, evCancelled)); n != round {
			t.Fatalf("round %d: staff_audit_log %s rows = %d, want %d (one per schedule)", round, evCancelled, n, round)
		}
	}
}

// A tenant purged between the staff check and the write answers ErrTenantNotFound, not locked or 500.
func TestStaffDeletion_ATenantPurgedAfterTheStaffCheckIsNotFound(t *testing.T) {
	for _, route := range []string{"start", "cancel"} {
		t.Run(route, func(t *testing.T) {
			w := newStaffDelWorld(t)
			if route == "cancel" {
				if _, err := w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
					t.Fatalf("setup start: %v", err)
				}
			}
			w.store.afterStaffCheck = func() {
				ctx := context.Background()
				tx, err := w.super.Begin(ctx)
				if err != nil {
					t.Errorf("begin: %v", err)
					return
				}
				defer tx.Rollback(ctx) //nolint:errcheck
				if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, w.tenant); err != nil {
					t.Errorf("set tenant: %v", err)
				}
				if _, err := tx.Exec(ctx, `SELECT public.purge_tenant($1::uuid)`, w.tenant); err != nil {
					t.Errorf("purge_tenant: %v", err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Errorf("commit: %v", err)
				}
			}
			var err error
			if route == "start" {
				_, err = w.store.StaffScheduleWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
			} else {
				_, err = w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText)
			}
			if !errors.Is(err, ErrTenantNotFound) {
				t.Errorf("err = %v, want ErrTenantNotFound", err)
			}
			wantScheduled := map[string]int{"start": 0, "cancel": 1}[route] // the setup start's row stays
			if n := len(staffRowsOf(t, w.super, w.tenant, evScheduled)); n != wantScheduled {
				t.Errorf("staff_audit_log %s rows = %d, want %d", evScheduled, n, wantScheduled)
			}
			if n := len(staffRowsOf(t, w.super, w.tenant, evCancelled)); n != 0 {
				t.Errorf("staff_audit_log %s rows = %d, want 0", evCancelled, n)
			}
		})
	}
}

const (
	wantPurgeStartedEvent = "workspace.purge_started"
	wantPurgeJobKind      = "workspace_purge" // purgeargs.Args.Kind()
	purgeReason           = "court order"
	adminMarkerEmail      = "marker-adaeze@members.test"
)

// newStaffPurgeWorld is a staff world whose store holds an insert-only erase queue, with the tenant's admin named.
func newStaffPurgeWorld(t *testing.T, adminName string) staffDelWorld {
	t.Helper()
	w := newInvWorld(t, obi, adminName)
	houseID, staff := houseMember(t, w.super, "active")
	q, err := queue.New(w.app, queue.Config{})
	if err != nil {
		t.Fatalf("build the insert-only queue client: %v", err)
	}
	w.store.SetEraseQueue(q.River())
	dropPurgeJobsOnCleanup(t, w.super, w.tenant)
	if _, err := w.super.Exec(context.Background(), `UPDATE memberships SET email = $2 WHERE user_id = $1`, w.admin, adminMarkerEmail); err != nil {
		t.Fatalf("set the admin's marker email: %v", err)
	}
	return staffDelWorld{invWorld: w, houseID: houseID, staff: staff, ctx: staffCtx(staff)}
}

func dropPurgeJobsOnCleanup(t *testing.T, super *pgxpool.Pool, tenants ...string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(),
			`DELETE FROM river_job WHERE kind = $1 AND args->>'tenant_id' = ANY($2::text[])`, wantPurgeJobKind, tenants)
	})
}

// purgeJobsOf returns the encoded args of the tenant's queued workspace_purge jobs.
func purgeJobsOf(t *testing.T, super *pgxpool.Pool, tenant string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT args::text FROM river_job WHERE kind = $1 AND args->>'tenant_id' = $2 ORDER BY id`, wantPurgeJobKind, tenant)
	if err != nil {
		t.Fatalf("read river_job: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatalf("scan river_job: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func tenantNameOf(t *testing.T, super *pgxpool.Pool, tenant string) string {
	t.Helper()
	var name string
	if err := super.QueryRow(context.Background(), `SELECT name FROM tenants WHERE id = $1`, tenant).Scan(&name); err != nil {
		t.Fatalf("read the tenant name: %v", err)
	}
	return name
}

// requireUntouched fails when the tenant has a deletion column, a marker, a queued purge job or a staff row.
func requireUntouched(t *testing.T, w staffDelWorld, tenant string) {
	t.Helper()
	requireUnchanged(t, w, tenant, deletionCols{})
}

// requireUnchanged is requireUntouched for a tenant whose deletion columns must equal before.
func requireUnchanged(t *testing.T, w staffDelWorld, tenant string, before deletionCols) {
	t.Helper()
	if c := deletionOf(t, w.super, tenant); !reflect.DeepEqual(c, before) {
		t.Errorf("deletion columns of %s = %+v, want %+v", tenant, c, before)
	}
	if m := purgeStartedAt(t, w.super, tenant); m != nil {
		t.Errorf("deletion_purge_started_at of %s = %v, want NULL", tenant, m)
	}
	if jobs := purgeJobsOf(t, w.super, tenant); len(jobs) != 0 {
		t.Errorf("queued %s jobs of %s = %v, want none", wantPurgeJobKind, tenant, jobs)
	}
	if n := tenantRowsOfStaff(t, w.super, tenant); n != 0 {
		t.Errorf("staff_audit_log rows of %s = %d, want 0", tenant, n)
	}
}

func TestStaffPurge_MarksAuditsAndQueuesInOneTransaction(t *testing.T) {
	w := newStaffPurgeWorld(t, "Adaeze Obi")
	lockByOwner(t, w.super, w.tenant, w.admin) // scheduled by someone else, so the purge must keep that schedule
	before := deletionOf(t, w.super, w.tenant)
	if before.DueAt == nil {
		t.Fatal("setup: the scheduled tenant has no due time")
	}

	at, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason)
	if err != nil {
		t.Fatalf("StaffPurgeWorkspace: %v", err)
	}

	marker := purgeStartedAt(t, w.super, w.tenant)
	if marker == nil || !marker.Equal(at) {
		t.Errorf("deletion_purge_started_at = %v, want the returned %v", marker, at)
	}
	if after := deletionOf(t, w.super, w.tenant); after.By == nil || *after.By != w.admin || after.DueAt == nil || !after.DueAt.Equal(*before.DueAt) {
		t.Errorf("schedule after the purge = %+v, want the earlier schedule (by %s, due %v) kept", after, w.admin, before.DueAt)
	}

	rows := staffRowsOf(t, w.super, w.tenant, wantPurgeStartedEvent)
	if len(rows) != 1 {
		t.Fatalf("staff_audit_log %s rows = %d, want 1", wantPurgeStartedEvent, len(rows))
	}
	if rows[0].Actor != w.staff {
		t.Errorf("staff row actor = %s, want the staff subject %s", rows[0].Actor, w.staff)
	}
	if !slices.Equal(keysOf(rows[0].Payload), []string{"reason"}) || rows[0].Payload["reason"] != purgeReason {
		t.Errorf("staff payload = %v, want exactly {reason: %q}", rows[0].Payload, purgeReason)
	}
	if n := tenantRowsOfStaff(t, w.super, w.tenant); n != 1 {
		t.Errorf("staff_audit_log rows of the tenant = %d, want only the purge_started row", n)
	}
	var raw string
	if err := w.super.QueryRow(context.Background(),
		`SELECT payload::text FROM public.staff_audit_log WHERE tenant_id = $1 AND event = $2`, w.tenant, wantPurgeStartedEvent).Scan(&raw); err != nil {
		t.Fatalf("read the payload text: %v", err)
	}
	for _, leak := range []string{adminMarkerEmail, "Adaeze", w.admin} {
		if strings.Contains(raw, leak) {
			t.Errorf("staff payload %s carries %q", raw, leak)
		}
	}

	jobs := purgeJobsOf(t, w.super, w.tenant)
	if len(jobs) != 1 {
		t.Fatalf("queued %s jobs = %d, want 1", wantPurgeJobKind, len(jobs))
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(jobs[0]), &args); err != nil || len(args) != 1 || args["tenant_id"] != w.tenant {
		t.Errorf("job args = %s (%v), want exactly {tenant_id: %s}", jobs[0], err, w.tenant)
	}
}

func TestStaffPurge_RefusesAnUnscheduledTenant(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")

	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); !errors.Is(err, ErrDeletionNotScheduled) {
		t.Fatalf("StaffPurgeWorkspace on an unscheduled tenant: err = %v, want ErrDeletionNotScheduled", err)
	}
	requireUntouched(t, w, w.tenant)
	if workspaceLocked(t, w.app, w.tenant) {
		t.Error("db.WorkspaceLocked = true after the refusal, want false")
	}
}

func TestStaffPurge_ACancelThatWinsTheRaceRefusesThePurge(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("staff cancel: %v", err)
	}

	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); !errors.Is(err, ErrDeletionNotScheduled) {
		t.Fatalf("purge after the cancel: err = %v, want ErrDeletionNotScheduled", err)
	}
	if m := purgeStartedAt(t, w.super, w.tenant); m != nil {
		t.Errorf("deletion_purge_started_at = %v, want NULL", m)
	}
	if n := len(purgeJobsOf(t, w.super, w.tenant)); n != 0 {
		t.Errorf("queued %s jobs = %d, want 0", wantPurgeJobKind, n)
	}
	if n := len(staffRowsOf(t, w.super, w.tenant, wantPurgeStartedEvent)); n != 0 {
		t.Errorf("staff_audit_log %s rows = %d, want 0", wantPurgeStartedEvent, n)
	}
}

func TestStaffPurge_ClosesEveryCancel(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	at, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason)
	if err != nil {
		t.Fatalf("StaffPurgeWorkspace: %v", err)
	}
	due := deletionOf(t, w.super, w.tenant).DueAt
	if due == nil {
		t.Fatal("setup: no due time after the purge")
	}

	if _, err := w.store.CancelWorkspaceDeletion(w.adminCtx()); !errors.Is(err, ErrDeletionUnderway) {
		t.Errorf("an admin's cancel: err = %v, want ErrDeletionUnderway", err)
	}
	if _, err := w.store.StaffCancelWorkspaceDeletion(w.ctx, w.tenant, staffReasonText); !errors.Is(err, ErrDeletionUnderway) {
		t.Errorf("the staff cancel: err = %v, want ErrDeletionUnderway", err)
	}
	// A second purge (a double click) is refused too and queues no second job.
	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); !errors.Is(err, ErrDeletionUnderway) {
		t.Errorf("a second purge: err = %v, want ErrDeletionUnderway", err)
	}

	if m := purgeStartedAt(t, w.super, w.tenant); m == nil || !m.Equal(at) {
		t.Errorf("deletion_purge_started_at = %v, want %v unchanged", m, at)
	}
	if got := deletionOf(t, w.super, w.tenant).DueAt; got == nil || !got.Equal(*due) {
		t.Errorf("deletion_due_at = %v, want %v unchanged", got, due)
	}
	if n := tenantRowsOfStaff(t, w.super, w.tenant); n != 1 {
		t.Errorf("staff_audit_log rows of the tenant = %d, want only the first purge's 1", n)
	}
	if n := len(purgeJobsOf(t, w.super, w.tenant)); n != 1 {
		t.Errorf("queued %s jobs = %d, want 1", wantPurgeJobKind, n)
	}
}

func TestStaffPurge_ANameMismatchWritesNothing(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	bName := "Bystander Holdings"
	b := seedTenant(t, w.super, bName)
	dropPurgeJobsOnCleanup(t, w.super, b)

	for _, tc := range []struct{ name, typed string }{
		{"lower-cased", strings.ToLower(obi)},
		{"upper-cased", strings.ToUpper(obi)},
		{"a trailing space", obi + " "},
		{"a leading space", " " + obi},
		{"a non-breaking space", strings.Replace(obi, " ", "\u00a0", 1)},
		{"a zero-width space", obi + "\u200b"},
		{"another tenant's name", bName},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, tc.typed, purgeReason)
			if !errors.Is(err, ErrWorkspaceNameMismatch) {
				t.Errorf("err = %v, want ErrWorkspaceNameMismatch", err)
			}
			requireUntouched(t, w, w.tenant)
			requireUntouched(t, w, b)
		})
	}

	t.Run("control: the exact name purges", func(t *testing.T) {
		lockByOwner(t, w.super, w.tenant, w.admin)
		if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); err != nil {
			t.Fatalf("StaffPurgeWorkspace with the exact name: %v", err)
		}
		if n := len(purgeJobsOf(t, w.super, w.tenant)); n != 1 {
			t.Errorf("queued %s jobs = %d, want 1", wantPurgeJobKind, n)
		}
	})
}

func TestStaffPurge_RefusesTheHouseAndAStartedTenant(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	dropPurgeJobsOnCleanup(t, w.super, w.houseID)
	houseName := tenantNameOf(t, w.super, w.houseID)
	houseRowsBefore := tenantRowsOfStaff(t, w.super, w.houseID)

	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.houseID, houseName, purgeReason); !errors.Is(err, ErrHouseWorkspace) {
		t.Errorf("the house tenant: err = %v, want ErrHouseWorkspace", err)
	}
	if c := deletionOf(t, w.super, w.houseID); !c.empty() {
		t.Errorf("the house tenant's deletion columns = %+v, want all NULL", c)
	}
	if m := purgeStartedAt(t, w.super, w.houseID); m != nil {
		t.Errorf("the house tenant's marker = %v, want NULL", m)
	}
	if n := len(purgeJobsOf(t, w.super, w.houseID)); n != 0 {
		t.Errorf("queued %s jobs of the house tenant = %d, want 0", wantPurgeJobKind, n)
	}
	if n := tenantRowsOfStaff(t, w.super, w.houseID); n != houseRowsBefore {
		t.Errorf("staff_audit_log rows of the house tenant = %d, want %d unchanged", n, houseRowsBefore)
	}

	startPurge(t, w.super, w.tenant, w.staff)
	marker := purgeStartedAt(t, w.super, w.tenant)
	if marker == nil {
		t.Fatal("setup: no purge marker")
	}
	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); !errors.Is(err, ErrDeletionUnderway) {
		t.Errorf("a started tenant: err = %v, want ErrDeletionUnderway", err)
	}
	if got := purgeStartedAt(t, w.super, w.tenant); got == nil || !got.Equal(*marker) {
		t.Errorf("deletion_purge_started_at = %v, want %v unchanged", got, marker)
	}
	if n := len(purgeJobsOf(t, w.super, w.tenant)); n != 0 {
		t.Errorf("queued %s jobs of the started tenant = %d, want 0", wantPurgeJobKind, n)
	}
	if n := tenantRowsOfStaff(t, w.super, w.tenant); n != 0 {
		t.Errorf("staff_audit_log rows of the started tenant = %d, want 0", n)
	}
}

func TestStaffPurge_RefusesWithoutAStaffCaller(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	_, suspended := houseMember(t, w.super, "suspended")

	callers := map[string]context.Context{
		"no caller":                          context.Background(),
		"a customer admin":                   w.adminCtx(),
		"a customer admin, tenantless":       auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: w.admin, Role: "authenticated"}),
		"the staff flag on a customer admin": staffCtx(w.admin),
		"a suspended house member":           staffCtx(suspended),
	}
	for name, ctx := range callers {
		t.Run(name, func(t *testing.T) {
			if _, err := w.store.StaffPurgeWorkspace(ctx, w.tenant, obi, purgeReason); !errors.Is(err, db.ErrNotStaff) {
				t.Errorf("err = %v, want db.ErrNotStaff", err)
			}
			requireUntouched(t, w, w.tenant)
		})
	}

	t.Run("control: the staff caller purges the same tenant", func(t *testing.T) {
		lockByOwner(t, w.super, w.tenant, w.admin)
		if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); err != nil {
			t.Errorf("staff purge: %v", err)
		}
	})
}

func TestStaffPurge_ActsOnlyOnThePathTenant(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	b := seedTenant(t, w.super, "Bystander Holdings")
	c := seedTenant(t, w.super, "Scheduled Bystander")
	dropPurgeJobsOnCleanup(t, w.super, b, c)
	lockByOwner(t, w.super, c, w.staff)
	lockByOwner(t, w.super, w.tenant, w.admin)
	cBefore := deletionOf(t, w.super, c)

	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); err != nil {
		t.Fatalf("purge of A: %v", err)
	}
	if m := purgeStartedAt(t, w.super, w.tenant); m == nil {
		t.Fatal("control: A has no marker after its purge")
	}

	requireUntouched(t, w, b)
	if got := deletionOf(t, w.super, c); got.DueAt == nil || !got.DueAt.Equal(*cBefore.DueAt) || got.By == nil || *got.By != w.staff {
		t.Errorf("scheduled bystander's columns = %+v, want %+v unchanged", got, cBefore)
	}
	if m := purgeStartedAt(t, w.super, c); m != nil {
		t.Errorf("scheduled bystander's marker = %v, want NULL", m)
	}
	for _, other := range []string{b, c} {
		if n := len(purgeJobsOf(t, w.super, other)); n != 0 {
			t.Errorf("queued %s jobs of %s = %d, want 0", wantPurgeJobKind, other, n)
		}
		if n := tenantRowsOfStaff(t, w.super, other); n != 0 {
			t.Errorf("staff_audit_log rows of %s = %d, want 0", other, n)
		}
		if n := auditCount(t, w.app, other, wantPurgeStartedEvent); n != 0 {
			t.Errorf("audit_log %s rows of %s = %d, want 0", wantPurgeStartedEvent, other, n)
		}
	}
}

func TestStaffPurge_AnUnknownTenantIsNotFound(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	for _, id := range []string{uuid.NewString(), "x", ""} {
		if _, err := w.store.StaffPurgeWorkspace(w.ctx, id, obi, purgeReason); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("id %q: err = %v, want ErrTenantNotFound", id, err)
		}
	}
	requireUntouched(t, w, w.tenant)
}

func TestStaffPurge_WithoutAnEraseQueueFailsBeforeAnyTransaction(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	w.store.SetEraseQueue(nil)

	// An unknown tenant would answer ErrTenantNotFound if a transaction ran first.
	for _, id := range []string{w.tenant, uuid.NewString()} {
		if _, err := w.store.StaffPurgeWorkspace(w.ctx, id, obi, purgeReason); !errors.Is(err, errNoEraseQueue) {
			t.Errorf("id %s: err = %v, want errNoEraseQueue", id, err)
		}
	}
	requireUntouched(t, w, w.tenant)
}

func TestStaffPurge_AFailedStaffRowRollsBack(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	before := deletionOf(t, w.super, w.tenant)
	drop := failStaffAudit(t, w.super, w.tenant, wantPurgeStartedEvent)

	_, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason)
	if err == nil {
		t.Fatal("StaffPurgeWorkspace succeeded although its staff row fails")
	}
	requireNoSentinel(t, err)
	requireUnchanged(t, w, w.tenant, before)

	drop()
	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); err != nil {
		t.Errorf("purge after the trigger is gone: %v", err)
	}
}

func TestStaffPurge_AFailedJobInsertRollsBackTheStaffRowToo(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	before := deletionOf(t, w.super, w.tenant)
	ctx := context.Background()
	name := "river_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := w.super.Exec(ctx,
		`CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced job insert failure' USING ERRCODE = 'check_violation'; END; $$`); err != nil {
		t.Fatalf("create the forced-failure function: %v", err)
	}
	t.Cleanup(func() { _, _ = w.super.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+name+`() CASCADE`) })
	if _, err := w.super.Exec(ctx,
		`CREATE TRIGGER `+name+` BEFORE INSERT ON public.river_job FOR EACH ROW
		 WHEN (NEW.kind = `+quoteLiteral(wantPurgeJobKind)+` AND NEW.args->>'tenant_id' = `+quoteLiteral(w.tenant)+`) EXECUTE FUNCTION `+name+`()`); err != nil {
		t.Fatalf("create the forced-failure trigger: %v", err)
	}

	_, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason)
	if err == nil {
		t.Fatal("StaffPurgeWorkspace succeeded although its job insert fails")
	}
	requireNoSentinel(t, err)
	requireUnchanged(t, w, w.tenant, before)
	if n := auditCount(t, w.app, w.tenant, wantPurgeStartedEvent); n != 0 {
		t.Errorf("audit_log %s rows = %d after the rollback, want 0", wantPurgeStartedEvent, n)
	}

	if _, err := w.super.Exec(ctx, `DROP TRIGGER `+name+` ON public.river_job`); err != nil {
		t.Fatalf("drop the forced-failure trigger: %v", err)
	}
	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); err != nil {
		t.Errorf("purge after the trigger is gone: %v", err)
	}
}

func TestStaffPurge_ConcurrentCallsQueueOneJobAndWriteOneRow(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, errs[i] = w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason)
		}()
	}
	close(gate)
	wg.Wait()

	won, underway := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrDeletionUnderway):
			underway++
		default:
			t.Errorf("a concurrent purge failed with %v, want nil or ErrDeletionUnderway", err)
		}
	}
	if won != 1 || underway != callers-1 {
		t.Errorf("%d purges won and %d were refused, want 1 and %d", won, underway, callers-1)
	}
	if n := len(purgeJobsOf(t, w.super, w.tenant)); n != 1 {
		t.Errorf("queued %s jobs = %d, want 1", wantPurgeJobKind, n)
	}
	if n := len(staffRowsOf(t, w.super, w.tenant, wantPurgeStartedEvent)); n != 1 {
		t.Errorf("staff_audit_log %s rows = %d, want 1", wantPurgeStartedEvent, n)
	}
}

func TestStaffPurge_TheCustomerAuditRowCarriesOnlyTheReason(t *testing.T) {
	w := newStaffPurgeWorld(t, "Adaeze Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	if _, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason); err != nil {
		t.Fatalf("StaffPurgeWorkspace: %v", err)
	}
	var actor, raw string
	if err := w.super.QueryRow(context.Background(),
		`SELECT actor, payload::text FROM public.audit_log WHERE tenant_id = $1 AND event = $2`, w.tenant, wantPurgeStartedEvent).Scan(&actor, &raw); err != nil {
		t.Fatalf("read the customer's %s row: %v", wantPurgeStartedEvent, err)
	}
	if actor != audit.SupportActor {
		t.Errorf("customer row actor = %q, want %q", actor, audit.SupportActor)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || !slices.Equal(keysOf(payload), []string{"reason"}) || payload["reason"] != purgeReason {
		t.Errorf("customer row payload = %s (%v), want exactly {reason: %q}", raw, err, purgeReason)
	}
	for _, leak := range []string{adminMarkerEmail, "Adaeze", w.admin, w.staff} {
		if strings.Contains(actor+raw, leak) {
			t.Errorf("customer row carries %q: actor %q payload %s", leak, actor, raw)
		}
	}
}

type purgeRaceErrs struct{ purge, adminCancel, staffCancel error }

// runPurgeRound runs the purge and both cancels against a scheduled tenant, together or one after another.
func runPurgeRound(w staffDelWorld, together bool, order ...string) purgeRaceErrs {
	var e purgeRaceErrs
	ops := map[string]func(){
		"purge":        func() { _, e.purge = w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason) },
		"admin cancel": func() { _, e.adminCancel = w.store.CancelWorkspaceDeletion(w.adminCtx()) },
		"staff cancel": func() { e.staffCancel = staffCancelErr(w.store, w.ctx, w.tenant, staffReasonText) },
	}
	if !together {
		for _, name := range order {
			ops[name]()
		}
		return e
	}
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for _, op := range ops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			op()
		}()
	}
	close(gate)
	wg.Wait()
	return e
}

// requirePurgeRoundConsistent asserts the round ended in exactly one of two states: the purge started or both cancels won.
func requirePurgeRoundConsistent(t *testing.T, w staffDelWorld, e purgeRaceErrs) (purgeWon bool) {
	t.Helper()
	jobs := len(purgeJobsOf(t, w.super, w.tenant))
	rows := len(staffRowsOf(t, w.super, w.tenant, wantPurgeStartedEvent))
	c := deletionOf(t, w.super, w.tenant)
	if purgeStartedAt(t, w.super, w.tenant) != nil {
		if e.purge != nil || !errors.Is(e.adminCancel, ErrDeletionUnderway) || !errors.Is(e.staffCancel, ErrDeletionUnderway) {
			t.Errorf("purge won: errors purge = %v, admin cancel = %v, staff cancel = %v, want nil, ErrDeletionUnderway, ErrDeletionUnderway", e.purge, e.adminCancel, e.staffCancel)
		}
		if c.ScheduledAt == nil || c.DueAt == nil {
			t.Errorf("deletion columns = %+v, want the schedule kept under the marker", c)
		}
		if jobs != 1 || rows != 1 {
			t.Errorf("jobs = %d, %s rows = %d, want 1 and 1", jobs, wantPurgeStartedEvent, rows)
		}
		return true
	}
	if !errors.Is(e.purge, ErrDeletionNotScheduled) {
		t.Errorf("cancel won: purge = %v, want ErrDeletionNotScheduled", e.purge)
	}
	for name, err := range map[string]error{"admin cancel": e.adminCancel, "staff cancel": e.staffCancel} {
		if err != nil {
			t.Errorf("cancel won: %s = %v, want nil (a lost cancel is a no-op)", name, err)
		}
	}
	if !c.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", c)
	}
	if jobs != 0 || rows != 0 {
		t.Errorf("jobs = %d, %s rows = %d, want 0 and 0", jobs, wantPurgeStartedEvent, rows)
	}
	return false
}

func TestStaffPurge_RacingCancelsEndInOneConsistentState(t *testing.T) {
	for i := range 5 {
		t.Run(fmt.Sprintf("round %d", i), func(t *testing.T) {
			w := newStaffPurgeWorld(t, "Ada Obi")
			lockByOwner(t, w.super, w.tenant, w.admin)
			requirePurgeRoundConsistent(t, w, runPurgeRound(w, true))
		})
	}
}

func TestStaffPurge_PurgeFirstBeatsBothCancels(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	if !requirePurgeRoundConsistent(t, w, runPurgeRound(w, false, "purge", "admin cancel", "staff cancel")) {
		t.Error("purge first: the purge did not win")
	}
}

func TestStaffPurge_CancelFirstBeatsThePurge(t *testing.T) {
	w := newStaffPurgeWorld(t, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	if requirePurgeRoundConsistent(t, w, runPurgeRound(w, false, "staff cancel", "admin cancel", "purge")) {
		t.Error("cancel first: the purge won")
	}
}

func staffScheduleDue(s *Store, ctx context.Context, tenant, reason string) (time.Time, error) {
	sched, err := s.StaffScheduleWorkspaceDeletion(ctx, tenant, reason)
	return sched.DueAt, err
}

func staffCancelErr(s *Store, ctx context.Context, tenant, reason string) error {
	_, err := s.StaffCancelWorkspaceDeletion(ctx, tenant, reason)
	return err
}
