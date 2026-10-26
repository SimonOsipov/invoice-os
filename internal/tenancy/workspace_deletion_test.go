package tenancy

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	evScheduled = "workspace.deletion_scheduled"
	evCancelled = "workspace.deletion_cancelled"
	obi         = "Obi Partners"
)

// deletionCols is the tenants row's three deletion columns, read as superuser.
type deletionCols struct {
	ScheduledAt, DueAt *time.Time
	By                 *string
}

func (c deletionCols) empty() bool { return c.ScheduledAt == nil && c.DueAt == nil && c.By == nil }

func deletionOf(t *testing.T, super *pgxpool.Pool, tenant string) deletionCols {
	t.Helper()
	var c deletionCols
	if err := super.QueryRow(context.Background(),
		`SELECT deletion_scheduled_at, deletion_due_at, deletion_scheduled_by::text FROM tenants WHERE id = $1`, tenant,
	).Scan(&c.ScheduledAt, &c.DueAt, &c.By); err != nil {
		t.Fatalf("read the deletion columns: %v", err)
	}
	return c
}

// lockByOwner schedules the deletion as superuser, so a test of another behaviour does not lean on the store method.
func lockByOwner(t *testing.T, super *pgxpool.Pool, tenant, by string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $2 WHERE id = $1`, tenant, by); err != nil {
		t.Fatalf("lock the tenant: %v", err)
	}
}

func unlockByOwner(t *testing.T, super *pgxpool.Pool, tenant string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL WHERE id = $1`, tenant); err != nil {
		t.Fatalf("unlock the tenant: %v", err)
	}
}

// race starts a and b together and returns their results.
func race(a, b func() error) (errA, errB error) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; errA = a() }()
	go func() { defer wg.Done(); <-start; errB = b() }()
	close(start)
	wg.Wait()
	return errA, errB
}

func requireNoSentinel(t *testing.T, err error) {
	t.Helper()
	for _, s := range []error{ErrDeleteWorkspaceNotPermitted, ErrCancelDeletionNotPermitted, ErrWorkspaceNameMismatch,
		ErrHouseWorkspace, db.ErrWorkspaceLocked, db.ErrNotActiveMember, db.ErrNoTenant} {
		if errors.Is(err, s) {
			t.Errorf("err = %v, want the audit failure, not the refusal %v", err, s)
		}
	}
}

func TestWorkspaceDeletion_AnAdminSchedulesAndItIsAudited(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	before := time.Now()

	sched, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi)
	if err != nil {
		t.Fatalf("ScheduleWorkspaceDeletion: %v", err)
	}
	due := sched.DueAt
	near(t, "due", due, before.Add(720*time.Hour), time.Minute)

	c := deletionOf(t, w.super, w.tenant)
	if c.By == nil || *c.By != w.admin {
		t.Errorf("deletion_scheduled_by = %v, want the caller %s", c.By, w.admin)
	}
	if c.DueAt == nil || !c.DueAt.Equal(due) {
		t.Errorf("stored due = %v, want the returned %v", c.DueAt, due)
	}
	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 1 {
		t.Fatalf("%s rows = %d, want 1", evScheduled, n)
	}
	if got := auditActor(t, w.app, w.tenant, evScheduled); got != w.admin {
		t.Errorf("audit actor = %q, want the scheduling admin %q", got, w.admin)
	}
	p := auditPayloads(t, w.super, w.tenant, evScheduled)[0]
	if !slices.Equal(keysOf(p), []string{"due_at"}) {
		t.Errorf("payload keys = %v, want exactly [due_at] (no workspace name)", keysOf(p))
	}
	if p["due_at"] != due.UTC().Format(time.RFC3339) {
		t.Errorf("payload due_at = %v, want %s", p["due_at"], due.UTC().Format(time.RFC3339))
	}
}

func TestWorkspaceDeletion_AFailedAuditRollsTheScheduleBack(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	drop := failAudit(t, w.super, w.tenant, evScheduled)

	_, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi)
	if err == nil {
		t.Fatal("ScheduleWorkspaceDeletion succeeded although its audit write fails")
	}
	requireNoSentinel(t, err)
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns after the failed audit = %+v, want all NULL", c)
	}

	drop()
	if _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi); err != nil {
		t.Errorf("schedule after the trigger is gone: %v", err)
	}
}

func TestWorkspaceDeletion_ANonAdminIsRefusedBeforeTheName(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	preparer := w.addMember(t, "preparer", "active", "Grace Eze")
	reviewer := w.addMember(t, "reviewer", "active", "Kemi Eze")
	suspended := w.addMember(t, "admin", "suspended", "Tunde Bello")
	foreign := newInvWorld(t, "Elsewhere Ltd", "Bola Eze")

	cases := []struct {
		name    string
		subject string
		want    error
	}{
		{"an active preparer", preparer, ErrDeleteWorkspaceNotPermitted},
		{"an active reviewer", reviewer, ErrDeleteWorkspaceNotPermitted},
		{"a suspended admin", suspended, db.ErrNotActiveMember},
		{"a caller with no row", uuid.NewString(), db.ErrNotActiveMember},
		{"an admin of another workspace", foreign.admin, db.ErrNotActiveMember},
	}
	// The wrong name and the exact name must both be refused alike: a refused caller learns nothing about the name.
	for _, tc := range cases {
		for _, typed := range []string{"not the name", obi} {
			t.Run(tc.name+" typing "+typed, func(t *testing.T) {
				_, err := w.store.ScheduleWorkspaceDeletion(w.as(tc.subject), typed)
				if !errors.Is(err, tc.want) {
					t.Errorf("err = %v, want %v (the caller is checked before the name)", err, tc.want)
				}
				if errors.Is(err, ErrWorkspaceNameMismatch) {
					t.Errorf("err = %v leaks the name check to a caller who may not delete", err)
				}
				if c := deletionOf(t, w.super, w.tenant); !c.empty() {
					t.Errorf("deletion columns = %+v, want all NULL", c)
				}
				if c := deletionOf(t, w.super, foreign.tenant); !c.empty() {
					t.Errorf("the other workspace's deletion columns = %+v, want all NULL", c)
				}
			})
		}
	}
	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evScheduled, n)
	}
	// Control: the admin with the exact name schedules, so the refusals above were about the caller.
	if _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi); err != nil {
		t.Errorf("the admin with the exact name: %v", err)
	}
}

func TestWorkspaceDeletion_ANameThatDiffersInAnyByteIsRefused(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	for name, typed := range map[string]string{
		"lower-cased":    strings.ToLower(obi),
		"trailing space": obi + " ",
		"leading space":  " " + obi,
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), typed)
			if !errors.Is(err, ErrWorkspaceNameMismatch) {
				t.Errorf("err = %v, want ErrWorkspaceNameMismatch", err)
			}
			if c := deletionOf(t, w.super, w.tenant); !c.empty() {
				t.Errorf("deletion columns = %+v, want all NULL", c)
			}
		})
	}
	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evScheduled, n)
	}
	// Control: the exact name schedules, so the refusals above were about the name.
	if _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi); err != nil {
		t.Errorf("the exact name: %v", err)
	}
}

func TestWorkspaceDeletion_TheHouseWorkspaceIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	house, admin := houseMember(t, super, "active")
	var name string
	if err := super.QueryRow(context.Background(), `SELECT name FROM tenants WHERE id = $1`, house).Scan(&name); err != nil {
		t.Fatalf("read the house name: %v", err)
	}
	// A failing build must not leave the shared house tenant locked.
	t.Cleanup(func() { unlockByOwner(t, super, house) })
	store := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: admin, Role: "authenticated", TenantID: house})
	before := auditCount(t, app, house, evScheduled)

	_, err := store.ScheduleWorkspaceDeletion(ctx, name)
	if !errors.Is(err, ErrHouseWorkspace) {
		t.Errorf("err = %v, want ErrHouseWorkspace", err)
	}
	if c := deletionOf(t, super, house); !c.empty() {
		t.Errorf("house deletion columns = %+v, want all NULL", c)
	}
	if n := auditCount(t, app, house, evScheduled); n != before {
		t.Errorf("%s rows = %d, want %d unchanged", evScheduled, n, before)
	}
}

func TestWorkspaceDeletion_ASecondScheduleIsRefusedByTheLock(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	if _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi); err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	first := deletionOf(t, w.super, w.tenant)

	_, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi)
	if !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Errorf("second schedule err = %v, want db.ErrWorkspaceLocked", err)
	}
	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 1 {
		t.Errorf("%s rows = %d, want 1 in total", evScheduled, n)
	}
	if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil || !c.DueAt.Equal(*first.DueAt) {
		t.Errorf("due after the second schedule = %v, want %v unchanged", c.DueAt, first.DueAt)
	}
}

func TestWorkspaceDeletion_TwoConcurrentSchedulesLockOnce(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	second := w.addMember(t, "admin", "active", "Bola Eze")

	errA, errB := race(
		func() error { _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi); return err },
		func() error { _, err := w.store.ScheduleWorkspaceDeletion(w.as(second), obi); return err },
	)
	nils, locked := 0, 0
	for _, err := range []error{errA, errB} {
		switch {
		case err == nil:
			nils++
		case errors.Is(err, db.ErrWorkspaceLocked):
			locked++
		default:
			t.Errorf("unexpected err = %v", err)
		}
	}
	if nils != 1 || locked != 1 {
		t.Errorf("results = %v, %v: want one nil and one db.ErrWorkspaceLocked", errA, errB)
	}
	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 1 {
		t.Errorf("%s rows = %d, want 1", evScheduled, n)
	}
}

func TestWorkspaceDeletion_AnAdminCancelsAndTheWorkspaceUnlocks(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	other := w.addMember(t, "admin", "active", "Bola Eze")
	lockByOwner(t, w.super, w.tenant, w.admin)
	old := deletionOf(t, w.super, w.tenant)

	// Control: the lock refuses a gated read-write request before the cancel.
	if _, err := w.store.ListMemberships(w.adminCtx()); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Fatalf("ListMemberships while locked: err = %v, want db.ErrWorkspaceLocked", err)
	}

	if _, err := w.store.CancelWorkspaceDeletion(w.as(other)); err != nil {
		t.Fatalf("CancelWorkspaceDeletion: %v", err)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns after the cancel = %+v, want all NULL", c)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 1 {
		t.Fatalf("%s rows = %d, want 1", evCancelled, n)
	}
	if got := auditActor(t, w.app, w.tenant, evCancelled); got != other {
		t.Errorf("audit actor = %q, want the cancelling admin %q", got, other)
	}
	p := auditPayloads(t, w.super, w.tenant, evCancelled)[0]
	if !slices.Equal(keysOf(p), []string{"due_at"}) || p["due_at"] != old.DueAt.UTC().Format(time.RFC3339) {
		t.Errorf("payload = %v, want exactly due_at = the cancelled due %s", p, old.DueAt.UTC().Format(time.RFC3339))
	}
	if _, err := w.store.ListMemberships(w.adminCtx()); err != nil {
		t.Errorf("ListMemberships after the cancel: %v", err)
	}
}

func TestWorkspaceDeletion_OnlyAnActiveAdminCancels(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	preparer := w.addMember(t, "preparer", "active", "Grace Eze")
	reviewer := w.addMember(t, "reviewer", "active", "Kemi Eze")
	suspended := w.addMember(t, "admin", "suspended", "Tunde Bello")
	foreign := newInvWorld(t, "Elsewhere Ltd", "Bola Eze")
	lockByOwner(t, w.super, w.tenant, w.admin)

	cases := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"an active preparer", w.as(preparer), ErrCancelDeletionNotPermitted},
		{"an active reviewer", w.as(reviewer), ErrCancelDeletionNotPermitted},
		{"a suspended admin", w.as(suspended), db.ErrNotActiveMember},
		{"a caller with no row", w.as(uuid.NewString()), db.ErrNotActiveMember},
		{"an admin of another workspace naming this one", w.as(foreign.admin), db.ErrNotActiveMember},
		{"a non-UUID subject", w.as("not-a-uuid"), db.ErrNoTenant},
		{"no identity", context.Background(), db.ErrNoTenant},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.store.CancelWorkspaceDeletion(tc.ctx)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil {
				t.Errorf("the workspace is no longer scheduled")
			}
		})
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evCancelled, n)
	}
}

func TestWorkspaceDeletion_CancelOfAnUnscheduledWorkspaceIsANoOp(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	if _, err := w.store.CancelWorkspaceDeletion(w.adminCtx()); err != nil {
		t.Errorf("CancelWorkspaceDeletion on an unscheduled workspace: %v, want nil", err)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evCancelled, n)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", c)
	}
}

func TestStoreMe_AnswersTheDeletionDueDate(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	preparer := w.addMember(t, "preparer", "active", "Grace Eze")
	idle := newInvWorld(t, "Elsewhere Ltd", "Bola Eze")

	if _, me, err := idle.store.Me(idle.adminCtx()); err != nil || me.Role != "admin" {
		t.Fatalf("Me of an unscheduled workspace: role = %q, err = %v", me.Role, err)
	}
	if tn, _, _ := idle.store.Me(idle.adminCtx()); tn.DeletionDueAt != nil {
		t.Errorf("unscheduled DeletionDueAt = %v, want nil", tn.DeletionDueAt)
	}

	lockByOwner(t, w.super, w.tenant, w.admin)
	want := deletionOf(t, w.super, w.tenant).DueAt
	for name, ctx := range map[string]context.Context{"admin": w.adminCtx(), "preparer": w.as(preparer)} {
		t.Run(name, func(t *testing.T) {
			tn, _, err := w.store.Me(ctx)
			if err != nil {
				t.Fatalf("Me while locked: %v", err)
			}
			if tn.DeletionDueAt == nil || !tn.DeletionDueAt.Equal(*want) {
				t.Errorf("DeletionDueAt = %v, want the column %v", tn.DeletionDueAt, want)
			}
		})
	}
}

func TestAccept_ALockedWorkspaceTakesNoNewMember(t *testing.T) {
	locked := func(t *testing.T) acceptWorld {
		w := newAcceptWorld(t, "reviewer")
		lockByOwner(t, w.super, w.tenant, w.admin)
		return w
	}
	t.Run("by token", func(t *testing.T) {
		w := locked(t)
		user := uuid.NewString()
		if _, _, _, err := w.store.AcceptInvitation(tenantless(user, inviteeAddr), w.invite.Token, nil); !errors.Is(err, ErrInvitationNotValid) {
			t.Errorf("err = %v, want ErrInvitationNotValid", err)
		}
		requireNothingWritten(t, w, user, 0)
		// Control: the same invite is accepted once the lock is gone.
		unlockByOwner(t, w.super, w.tenant)
		if _, _, _, err := w.store.AcceptInvitation(tenantless(user, inviteeAddr), w.invite.Token, nil); err != nil {
			t.Errorf("accept after the unlock: %v", err)
		}
	})
	t.Run("by id", func(t *testing.T) {
		w := locked(t)
		user := uuid.NewString()
		if _, _, _, err := w.store.AcceptInvitationByID(tenantless(user, inviteeAddr), w.invite.ID, nil); !errors.Is(err, ErrInvitationNotValid) {
			t.Errorf("err = %v, want ErrInvitationNotValid", err)
		}
		requireNothingWritten(t, w, user, 0)
	})
	// The invite readers already hide a locked invite; this reaches the accept tail itself,
	// the check for a lock that lands between the reader and the accept.
	t.Run("the accept tail alone", func(t *testing.T) {
		w := locked(t)
		user := uuid.NewString()
		_, _, _, err := w.store.acceptInTenant(tenantless(user, inviteeAddr), w.tenant, user,
			`SELECT invitation_id, role FROM public.accept_invitation($1, $2, $3, $4, $5)`,
			w.tenant, w.invite.Token, user, inviteeAddr, (*string)(nil))
		if !errors.Is(err, ErrInvitationNotValid) {
			t.Errorf("err = %v, want ErrInvitationNotValid", err)
		}
		requireNothingWritten(t, w, user, 0)
	})
}

func TestWorkspaceDeletion_ALockedWorkspacesInviteReadsAsRevoked(t *testing.T) {
	w := newAcceptWorld(t, "reviewer")
	user := uuid.NewString()
	invitee := tenantless(user, inviteeAddr)
	lists := func() bool {
		got, err := w.store.MyPendingInvitations(invitee)
		if err != nil {
			t.Fatalf("MyPendingInvitations: %v", err)
		}
		for _, p := range got.Invitations {
			if p.ID == w.invite.ID {
				return true
			}
		}
		return false
	}
	if !lists() {
		t.Fatal("control: the pending invite is absent before the lock")
	}

	lockByOwner(t, w.super, w.tenant, w.admin)
	if lists() {
		t.Error("MyPendingInvitations lists the invite of a locked workspace")
	}
	if _, err := w.store.PreviewInvitation(context.Background(), w.invite.Token); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("PreviewInvitation err = %v, want ErrInvitationNotValid", err)
	}
	if _, _, err := w.store.ClaimInvitationRegistration(context.Background(), w.invite.Token); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("ClaimInvitationRegistration err = %v, want ErrInvitationNotValid", err)
	}

	if _, err := w.store.CancelWorkspaceDeletion(w.adminCtx()); err != nil {
		t.Fatalf("CancelWorkspaceDeletion: %v", err)
	}
	if got, err := w.store.PreviewInvitation(context.Background(), w.invite.Token); err != nil || got.Workspace != obi {
		t.Errorf("PreviewInvitation after the cancel = %+v, %v, want the workspace %q", got, err, obi)
	}
	if !lists() {
		t.Error("MyPendingInvitations omits the invite after the cancel")
	}
	if _, first, err := w.store.ClaimInvitationRegistration(context.Background(), w.invite.Token); err != nil || !first {
		t.Errorf("ClaimInvitationRegistration after the cancel: first = %v, err = %v", first, err)
	}
	if _, _, _, err := w.store.AcceptInvitation(invitee, w.invite.Token, nil); err != nil {
		t.Errorf("AcceptInvitation after the cancel: %v", err)
	}
	if ms := membersOf(t, w.super, user); len(ms) != 1 || ms[0].Status != "active" {
		t.Errorf("memberships after the accept = %+v, want one active", ms)
	}
}

func TestWorkspaceDeletion_AFailedCancelAuditKeepsTheLock(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	old := deletionOf(t, w.super, w.tenant)
	drop := failAudit(t, w.super, w.tenant, evCancelled)

	_, err := w.store.CancelWorkspaceDeletion(w.adminCtx())
	if err == nil {
		t.Fatal("CancelWorkspaceDeletion succeeded although its audit write fails")
	}
	requireNoSentinel(t, err)
	if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil || !c.DueAt.Equal(*old.DueAt) {
		t.Errorf("deletion_due_at = %v, want %v unchanged", c.DueAt, old.DueAt)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evCancelled, n)
	}

	drop()
	if _, err := w.store.CancelWorkspaceDeletion(w.adminCtx()); err != nil {
		t.Errorf("cancel after the trigger is gone: %v", err)
	}
}

func TestWorkspaceDeletion_TwoConcurrentCancelsAuditOnce(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	second := w.addMember(t, "admin", "active", "Bola Eze")
	lockByOwner(t, w.super, w.tenant, w.admin)

	errA, errB := race(
		func() error { _, err := w.store.CancelWorkspaceDeletion(w.adminCtx()); return err },
		func() error { _, err := w.store.CancelWorkspaceDeletion(w.as(second)); return err },
	)
	if errA != nil || errB != nil {
		t.Errorf("results = %v, %v, want both nil", errA, errB)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", c)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 1 {
		t.Errorf("%s rows = %d, want exactly 1", evCancelled, n)
	}
}

func TestWorkspaceDeletion_AScheduleRacingACancelEndsSerially(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	second := w.addMember(t, "admin", "active", "Bola Eze")

	sawLocked, sawUnlocked := 0, 0
	for round := 0; round < 20; round++ {
		sched0 := auditCount(t, w.app, w.tenant, evScheduled)
		canc0 := auditCount(t, w.app, w.tenant, evCancelled)

		errS, errC := race(
			func() error { _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi); return err },
			func() error { _, err := w.store.CancelWorkspaceDeletion(w.as(second)); return err },
		)
		if errS != nil || errC != nil {
			t.Fatalf("round %d: schedule err = %v, cancel err = %v, want both nil", round, errS, errC)
		}
		sched := auditCount(t, w.app, w.tenant, evScheduled) - sched0
		canc := auditCount(t, w.app, w.tenant, evCancelled) - canc0
		lockedNow := deletionOf(t, w.super, w.tenant).DueAt != nil
		switch {
		case lockedNow && sched == 1 && canc == 0:
			sawLocked++
		case !lockedNow && sched == 1 && canc == 1:
			sawUnlocked++
		default:
			t.Fatalf("round %d: locked = %v with %d scheduled and %d cancelled rows, want locked with 1/0 or unlocked with 1/1", round, lockedNow, sched, canc)
		}
		unlockByOwner(t, w.super, w.tenant)
	}
	if sawLocked+sawUnlocked != 20 {
		t.Errorf("rounds judged = %d, want 20", sawLocked+sawUnlocked)
	}
}

func TestWorkspaceDeletion_ALongNameMatchesExactly(t *testing.T) {
	super, app := dbTestPools(t)
	name := strings.Repeat("W", 192) + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	if n := len([]rune(name)); n != 200 {
		t.Fatalf("fixture: name has %d runes, want 200", n)
	}
	w := invWorld{super: super, app: app, store: NewStore(app)}
	w.tenant = seedTenant(t, super, name)
	w.admin = w.addMember(t, "admin", "active", "Ada Obi")

	short := string([]rune(name)[:199])
	if _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), short); !errors.Is(err, ErrWorkspaceNameMismatch) {
		t.Errorf("name minus its last rune: err = %v, want ErrWorkspaceNameMismatch", err)
	}
	if c := deletionOf(t, super, w.tenant); !c.empty() {
		t.Errorf("deletion columns after the refusal = %+v, want all NULL", c)
	}
	if _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), name); err != nil {
		t.Errorf("the exact 200-character name: %v", err)
	}
}

func TestWorkspaceDeletion_ANameIsMatchedWithoutUnicodeNormalisation(t *testing.T) {
	const composed = "Caf\u00e9 Ltd"
	w := newInvWorld(t, composed, "Ada Obi")
	for name, typed := range map[string]string{
		"decomposed accent": "Cafe\u0301 Ltd",
		"a doubled space":   "Caf\u00e9  Ltd",
		"a no-break space":  "Caf\u00e9\u00a0Ltd",
		"upper-cased":       strings.ToUpper(composed),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), typed); !errors.Is(err, ErrWorkspaceNameMismatch) {
				t.Errorf("err = %v, want ErrWorkspaceNameMismatch", err)
			}
		})
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", c)
	}
	if _, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), composed); err != nil {
		t.Errorf("the exact name: %v", err)
	}
}

func TestWorkspaceDeletion_ACancelTouchesOnlyTheCallersWorkspace(t *testing.T) {
	a := newInvWorld(t, obi, "Ada Obi")
	b := newInvWorld(t, "Elsewhere Ltd", "Bola Eze")
	lockByOwner(t, a.super, a.tenant, a.admin)
	lockByOwner(t, b.super, b.tenant, b.admin)
	bDue := deletionOf(t, b.super, b.tenant).DueAt

	// The admin of A cancels: A unlocks, B stays locked.
	if _, err := a.store.CancelWorkspaceDeletion(a.adminCtx()); err != nil {
		t.Fatalf("cancel in A: %v", err)
	}
	if c := deletionOf(t, a.super, a.tenant); !c.empty() {
		t.Errorf("A's deletion columns = %+v, want all NULL", c)
	}
	if c := deletionOf(t, b.super, b.tenant); c.DueAt == nil || !c.DueAt.Equal(*bDue) {
		t.Errorf("B's due = %v, want %v unchanged", c.DueAt, bDue)
	}
	if n := auditCount(t, b.app, b.tenant, evCancelled); n != 0 {
		t.Errorf("B has %d %s rows, want 0", n, evCancelled)
	}
	// A second cancel is a no-op and writes no second row.
	if _, err := a.store.CancelWorkspaceDeletion(a.adminCtx()); err != nil {
		t.Errorf("second cancel: %v, want nil", err)
	}
	if n := auditCount(t, a.app, a.tenant, evCancelled); n != 1 {
		t.Errorf("A's %s rows = %d, want 1", evCancelled, n)
	}
}

func TestWorkspaceDeletion_ACancelledWorkspaceCanBeScheduledAgain(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	firstS, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi)
	if err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	if _, err := w.store.CancelWorkspaceDeletion(w.adminCtx()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	secondS, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi)
	if err != nil {
		t.Fatalf("schedule after the cancel: %v", err)
	}
	first, second := firstS.DueAt, secondS.DueAt
	if !second.After(first) {
		t.Errorf("second due %v is not after the first %v: the cancelled schedule was kept", second, first)
	}
	if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil || !c.DueAt.Equal(second) {
		t.Errorf("stored due = %v, want the second %v", c.DueAt, second)
	}
	if n := auditCount(t, w.app, w.tenant, evScheduled); n != 2 {
		t.Errorf("%s rows = %d, want 2", evScheduled, n)
	}
}

// startPurge schedules the tenant 31 days ago (due a day ago), then sets the purge marker, as superuser.
func startPurge(t *testing.T, super *pgxpool.Pool, tenant, by string) {
	t.Helper()
	ctx := context.Background()
	if _, err := super.Exec(ctx,
		`UPDATE tenants SET deletion_scheduled_at = now() - interval '31 days', deletion_scheduled_by = $2 WHERE id = $1`,
		tenant, by); err != nil {
		t.Fatalf("schedule the tenant in the past: %v", err)
	}
	if _, err := super.Exec(ctx, `UPDATE tenants SET deletion_purge_started_at = now() WHERE id = $1`, tenant); err != nil {
		t.Fatalf("set deletion_purge_started_at: %v", err)
	}
}

func purgeStartedAt(t *testing.T, super *pgxpool.Pool, tenant string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := super.QueryRow(context.Background(),
		`SELECT deletion_purge_started_at FROM tenants WHERE id = $1`, tenant).Scan(&at); err != nil {
		t.Fatalf("read deletion_purge_started_at: %v", err)
	}
	return at
}

func TestWorkspaceDeletion_AStartedPurgeCannotBeCancelled(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	startPurge(t, w.super, w.tenant, w.admin)
	before := deletionOf(t, w.super, w.tenant)
	if before.DueAt == nil || !before.DueAt.Before(time.Now()) {
		t.Fatalf("setup: deletion_due_at = %v, want a past time", before.DueAt)
	}
	marker := purgeStartedAt(t, w.super, w.tenant)

	_, err := w.store.CancelWorkspaceDeletion(w.adminCtx())
	if err == nil {
		t.Fatal("CancelWorkspaceDeletion succeeded after the purge started")
	}
	if !errors.Is(err, ErrDeletionUnderway) {
		t.Errorf("err = %v, want ErrDeletionUnderway", err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Errorf("err = %v, want the mapped sentinel, not the raw database error", err)
	}
	requireNoSentinel(t, err)
	if after := deletionOf(t, w.super, w.tenant); after.DueAt == nil || !after.DueAt.Equal(*before.DueAt) {
		t.Errorf("deletion_due_at = %v, want %v unchanged", after.DueAt, before.DueAt)
	}
	if got := purgeStartedAt(t, w.super, w.tenant); got == nil || !got.Equal(*marker) {
		t.Errorf("deletion_purge_started_at = %v, want %v unchanged", got, marker)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evCancelled, n)
	}
}

func TestWorkspaceDeletion_AStartedPurgeAnswers409OnTheCancelRoute(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	preparer := w.addMember(t, "preparer", "active", "Grace Eze")
	startPurge(t, w.super, w.tenant, w.admin)
	const target = "/v1/workspace/deletion/cancel"
	h := CancelDeletionHandler(func(ctx context.Context) error { _, err := w.store.CancelWorkspaceDeletion(ctx); return err }, nil)
	idOf := func(sub string) *auth.Identity {
		return &auth.Identity{Subject: sub, Role: "authenticated", TenantID: w.tenant}
	}

	status, body := deletionCall(h, idOf(w.admin), target, "")
	if status != http.StatusConflict || errorSentence(body) != "this workspace is already being deleted" {
		t.Errorf("admin: status = %d, error = %q, want 409 and %q", status, errorSentence(body), "this workspace is already being deleted")
	}
	// Control: the role check still runs first.
	status, body = deletionCall(h, idOf(preparer), target, "")
	if status != http.StatusForbidden || errorSentence(body) != "only an admin can cancel the deletion" {
		t.Errorf("preparer: status = %d, error = %q, want 403 and %q", status, errorSentence(body), "only an admin can cancel the deletion")
	}
}

func TestWorkspaceDeletion_AStartedPurgeKeepsTheRoleCheckFirst(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	var roles []string
	rows, err := w.super.Query(context.Background(), `SELECT name FROM roles WHERE name <> 'admin' ORDER BY name`)
	if err != nil {
		t.Fatalf("read the roles: %v", err)
	}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan a role: %v", err)
		}
		roles = append(roles, r)
	}
	rows.Close()
	if len(roles) == 0 {
		t.Fatal("setup: no non-admin role")
	}
	members := map[string]string{}
	for _, r := range roles {
		members[r] = w.addMember(t, r, "active", "Grace "+r)
	}
	suspended := w.addMember(t, "admin", "suspended", "Tunde Bello")
	startPurge(t, w.super, w.tenant, w.admin)

	for _, r := range roles {
		if _, err := w.store.CancelWorkspaceDeletion(w.as(members[r])); !errors.Is(err, ErrCancelDeletionNotPermitted) {
			t.Errorf("%s: err = %v, want ErrCancelDeletionNotPermitted", r, err)
		}
	}
	if _, err := w.store.CancelWorkspaceDeletion(w.as(suspended)); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("suspended admin: err = %v, want db.ErrNotActiveMember", err)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evCancelled, n)
	}
}

func TestWorkspaceDeletion_AStartedPurgeOnADeletionNotYetDueCannotBeCancelled(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	lockByOwner(t, w.super, w.tenant, w.admin)
	if _, err := w.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_purge_started_at = now() WHERE id = $1`, w.tenant); err != nil {
		t.Fatalf("set deletion_purge_started_at: %v", err)
	}
	before := deletionOf(t, w.super, w.tenant)
	if before.DueAt == nil || !before.DueAt.After(time.Now()) {
		t.Fatalf("setup: deletion_due_at = %v, want a future time", before.DueAt)
	}

	for i := 1; i <= 2; i++ {
		if _, err := w.store.CancelWorkspaceDeletion(w.adminCtx()); !errors.Is(err, ErrDeletionUnderway) {
			t.Errorf("cancel %d: err = %v, want ErrDeletionUnderway", i, err)
		}
	}
	if after := deletionOf(t, w.super, w.tenant); after.DueAt == nil || !after.DueAt.Equal(*before.DueAt) {
		t.Errorf("deletion_due_at = %v, want %v unchanged", after.DueAt, before.DueAt)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evCancelled, n)
	}
}

func TestWorkspaceDeletion_AStartedPurgeOfAnotherWorkspaceDoesNotBlockTheCancel(t *testing.T) {
	a := newInvWorld(t, obi, "Ada Obi")
	b := newInvWorld(t, "Eze Holdings", "Bola Eze")
	lockByOwner(t, a.super, a.tenant, a.admin)
	startPurge(t, b.super, b.tenant, b.admin)
	startedB := purgeStartedAt(t, b.super, b.tenant)
	if startedB == nil {
		t.Fatal("setup: the other workspace has no purge marker")
	}

	if _, err := a.store.CancelWorkspaceDeletion(a.adminCtx()); err != nil {
		t.Fatalf("cancel of the workspace with no marker: %v", err)
	}
	if c := deletionOf(t, a.super, a.tenant); !c.empty() {
		t.Errorf("own deletion columns = %+v, want all NULL", c)
	}
	if c := deletionOf(t, b.super, b.tenant); c.DueAt == nil {
		t.Error("the started workspace lost its schedule")
	}
	if got := purgeStartedAt(t, b.super, b.tenant); got == nil || !got.Equal(*startedB) {
		t.Errorf("other marker = %v, want %v unchanged", got, startedB)
	}
}

// txExec is a pgx.Tx whose Exec returns a fixed error; clearDeletionSchedule calls nothing else.
type txExec struct {
	pgx.Tx
	err error
}

func (x txExec) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, x.err
}

func TestClearDeletionSchedule_MapsOnlyTheUnderwayConstraint(t *testing.T) {
	other := &pgconn.PgError{Code: "42501", ConstraintName: "house_tenant_not_deletable"}
	noName := &pgconn.PgError{Code: "42501"}
	plain := errors.New("boom")
	for _, tc := range []struct {
		name string
		in   error
		is   error // errors.Is target of the result; nil = result is nil
	}{
		{"the underway constraint", &pgconn.PgError{Code: "42501", ConstraintName: "tenant_deletion_underway"}, ErrDeletionUnderway},
		{"the underway constraint, wrapped", fmt.Errorf("wrap: %w", &pgconn.PgError{Code: "42501", ConstraintName: "tenant_deletion_underway"}), ErrDeletionUnderway},
		{"another constraint", other, other},
		{"a 42501 with no constraint", noName, noName},
		{"a plain error", plain, plain},
		{"no error", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clearDeletionSchedule(context.Background(), txExec{err: tc.in})
			if tc.is == nil {
				if got != nil {
					t.Fatalf("err = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.is) {
				t.Errorf("err = %v, want %v", got, tc.is)
			}
			if tc.is != ErrDeletionUnderway && errors.Is(got, ErrDeletionUnderway) {
				t.Errorf("err = %v, must not read as ErrDeletionUnderway", got)
			}
		})
	}
}

// ceiling: a clear that builds its SQL outside a string literal in this package escapes the scan.
func TestWorkspaceDeletion_OnlyClearDeletionScheduleClearsTheSchedule(t *testing.T) {
	// No runtime test sees a clear site that does not exist yet: a second clear would skip the 409 mapping.
	clearRe := regexp.MustCompile(`(?is)deletion_scheduled_at\s*=\s*NULL`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var parsed int
	sites := map[string]int{} // enclosing func -> clears
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed++
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0) // mode 0: comments dropped
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range file.Decls {
			var lits []string
			ast.Inspect(decl, func(n ast.Node) bool {
				if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					lits = append(lits, bl.Value)
				}
				return true
			})
			if n := len(clearRe.FindAllString(strings.Join(lits, " "), -1)); n > 0 {
				name := f + ": top-level declaration"
				if fd, ok := decl.(*ast.FuncDecl); ok {
					name = fd.Name.Name
				}
				sites[name] += n
			}
		}
	}
	if parsed < 5 {
		t.Fatalf("parsed %d non-test files, want >= 5", parsed)
	}
	if sites["clearDeletionSchedule"] != 1 {
		t.Errorf("clearDeletionSchedule holds %d clears, want 1 (control)", sites["clearDeletionSchedule"])
	}
	for fn, n := range sites {
		if fn != "clearDeletionSchedule" {
			t.Errorf("%s clears the schedule %d time(s); only clearDeletionSchedule may", fn, n)
		}
	}
}

func TestWorkspaceDeletion_ADueDeletionNotYetStartedStillCancels(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	if _, err := w.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now() - interval '31 days', deletion_scheduled_by = $2 WHERE id = $1`,
		w.tenant, w.admin); err != nil {
		t.Fatalf("schedule the tenant in the past: %v", err)
	}
	if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil || !c.DueAt.Before(time.Now()) {
		t.Fatalf("setup: deletion_due_at = %v, want a past time", c.DueAt)
	}

	if _, err := w.store.CancelWorkspaceDeletion(w.adminCtx()); err != nil {
		t.Fatalf("CancelWorkspaceDeletion of a due deletion with no marker: %v", err)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns after the cancel = %+v, want all NULL", c)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 1 {
		t.Errorf("%s rows = %d, want 1", evCancelled, n)
	}
}
