package approval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// lastHolderFixture is a runFixture whose role tax-reviewer exists and has one open run waiting on it.
type lastHolderFixture struct {
	runFixture
	roleID, invoiceID, runID string
}

func newLastHolderFixture(t *testing.T, name string) lastHolderFixture {
	t.Helper()
	f := newRunFixture(t, name)
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	inv, run := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"))
	return lastHolderFixture{runFixture: f, roleID: roleID, invoiceID: inv, runID: run}
}

func (f lastHolderFixture) put(members ...string) (Role, error) {
	if members == nil {
		members = []string{}
	}
	return f.store.SetRoleMembers(f.admin, "tax-reviewer", members)
}

func (f lastHolderFixture) wantRefused(t *testing.T, members ...string) *roleInUse {
	t.Helper()
	_, err := f.put(members...)
	var inUse *roleInUse
	if !errors.Is(err, ErrRoleInUse) || !errors.As(err, &inUse) {
		t.Fatalf("SetRoleMembers err = %v, want *roleInUse", err)
	}
	if !inUse.lastHolder {
		t.Error("lastHolder = false, want true")
	}
	return inUse
}

func (f lastHolderFixture) wantSaved(t *testing.T, members ...string) {
	t.Helper()
	if _, err := f.put(members...); err != nil {
		t.Fatalf("SetRoleMembers err = %v, want success", err)
	}
}

func userIDs(rows []staffingRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.UserID)
	}
	return out
}

func TestSetRoleMembers_EmptySetRefusedWhileAnOpenRunWaits(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 empty refused")
	admin := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "admin", roleID: f.roleID})

	inUse := f.wantRefused(t)
	if len(inUse.invoices) != 1 || inUse.invoices[0] != (InvoiceRef{ID: f.invoiceID, Number: "INV-1"}) || inUse.invoicesTotal != 1 {
		t.Errorf("invoices = %+v, total = %d, want [INV-1] and 1", inUse.invoices, inUse.invoicesTotal)
	}
	if got := userIDs(staffingRows(t, f.super, f.roleID)); len(got) != 1 || got[0] != admin {
		t.Errorf("members = %v, want [%s]", got, admin)
	}
	if n := auditCount(t, f.super, f.tenantID, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed rows = %d, want 0", n)
	}
}

func TestSetRoleMembers_RefusalWritesNothing(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 writes nothing")
	seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "admin", roleID: f.roleID})
	seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	before := staffingRows(t, f.super, f.roleID)

	f.wantRefused(t)

	after := staffingRows(t, f.super, f.roleID)
	if len(after) != len(before) {
		t.Fatalf("rows = %v, want %v", after, before)
	}
	for i := range before {
		if !after[i].equal(before[i]) {
			t.Errorf("row %d = %v, want %v (id, ord and created_at unchanged)", i, after[i], before[i])
		}
	}
	if n := auditCount(t, f.super, f.tenantID, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed rows = %d, want 0", n)
	}
}

func TestSetRoleMembers_JudgesTheNewSetNotTheOldOne(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 new set")
	seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "admin", roleID: f.roleID})
	preparer := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "preparer"})
	f.wantRefused(t, preparer)

	g := newLastHolderFixture(t, "APSC-11 new set, old set weak")
	seedMemberOf(t, g.super, g.tenantID, memberSpec{role: "preparer", roleID: g.roleID})
	admin := seedMemberOf(t, g.super, g.tenantID, memberSpec{role: "admin"})
	g.wantSaved(t, admin)
}

func TestSetRoleMembers_OnlyNonSignersAreRefused(t *testing.T) {
	for _, c := range []struct{ name, role, status string }{
		{"preparer", "preparer", "active"},
		{"suspended reviewer", "reviewer", "suspended"},
		{"invited admin", "admin", "invited"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLastHolderFixture(t, "APSC-11 non-signer "+c.name)
			m := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: c.role, status: c.status})
			f.wantRefused(t, m)
		})
	}
}

func TestSetRoleMembers_OneSignerAmongNonSignersSaves(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 one signer")
	preparer := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "preparer"})
	suspended := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer", status: "suspended"})
	reviewer := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer"})
	want := []string{preparer, suspended, reviewer}

	f.wantSaved(t, want...)

	got := userIDs(staffingRows(t, f.super, f.roleID))
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("members = %v, want %v in submitted order", got, want)
	}
}

func TestSetRoleMembers_ActiveAdminCountsAsASigner(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 admin signs")
	f.wantSaved(t, seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "admin"}))
}

func TestSetRoleMembers_IgnoresARunOnAnInvoiceThatLeftValidated(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 left validated")
	setInvoiceStatus(t, f.super, f.invoiceID, "queued")
	f.wantSaved(t)
}

func TestSetRoleMembers_SavesOnceTheRunIsApprovedRejectedOrCancelled(t *testing.T) {
	closers := map[string]func(t *testing.T, f lastHolderFixture){
		"approved": func(t *testing.T, f lastHolderFixture) {
			closeApprovalRunFor(t, f.super, f.runID, "approved", "someone")
		},
		"rejected": func(t *testing.T, f lastHolderFixture) {
			if _, err := f.super.Exec(context.Background(),
				`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind, state, workflow_role_key)
				 VALUES ($1, $2, 1, 'approval', 'pending', 'tax-reviewer')`, f.tenantID, f.runID); err != nil {
				t.Fatalf("seed later step: %v", err)
			}
			closeApprovalRunFor(t, f.super, f.runID, "rejected", "someone")
		},
		"cancelled": func(t *testing.T, f lastHolderFixture) {
			if ok, err := cancel(t, f.app, f.tenantID, f.invoiceID, "editor"); err != nil || !ok {
				t.Fatalf("cancel = %v, %v, want true, nil", ok, err)
			}
		},
	}
	for name, closeRun := range closers {
		t.Run(name, func(t *testing.T) {
			f := newLastHolderFixture(t, "APSC-11 run ends "+name)
			f.wantRefused(t)
			closeRun(t, f)
			f.wantSaved(t)
		})
	}
}

func TestSetRoleMembers_AnotherRolesPendingStepDoesNotBlock(t *testing.T) {
	f := newRunFixture(t, "APSC-11 other role")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedWorkflowRole(t, f.super, f.tenantID, "other", "Other")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("other"))

	if _, err := f.store.SetRoleMembers(f.admin, "tax-reviewer", []string{}); err != nil {
		t.Fatalf("SetRoleMembers err = %v, want success", err)
	}
}

func TestSetRoleMembers_AnotherTenantsOpenRunDoesNotBlock(t *testing.T) {
	f := newRunFixture(t, "APSC-11 tenant A")
	tenantB := policyTenant(t, f.super, "APSC-11 tenant B")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedWorkflowRole(t, f.super, tenantB, "tax-reviewer", "Tax Reviewer")
	policyB := seedApprovalPolicy(t, f.super, tenantB, "B policy")
	versionB := seedApprovalPolicyVersionN(t, f.super, tenantB, policyB, 1)
	entityB := seedBusinessEntity(t, f.super, tenantB, "B Corp")
	seedOpenRun(t, f.super, tenantB, entityB, "INV-B", versionB, pendingOn("tax-reviewer"))

	if _, err := f.store.SetRoleMembers(f.admin, "tax-reviewer", []string{}); err != nil {
		t.Fatalf("SetRoleMembers err = %v, want success", err)
	}
}

func TestSetRoleMembersHandler_RealStoreRefusalBody(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 handler body")

	req := httptest.NewRequest("PUT", "/v1/workflow-roles/tax-reviewer/members", strings.NewReader(`{"members":[]}`)).WithContext(f.admin)
	req.SetPathValue("key", "tax-reviewer")
	rec := httptest.NewRecorder()
	SetRoleMembersHandler(f.store.SetRoleMembers, slog.New(slog.NewJSONHandler(io.Discard, nil))).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if got := strings.Join(keySet(t, body), ","); got != "error,invoices,invoices_total,policies" {
		t.Errorf("body keys = %s, want error,invoices,invoices_total,policies", got)
	}
	if got := rawField(t, body, "policies"); got != "[]" {
		t.Errorf("policies = %s, want []", got)
	}
	if got := rawField(t, body, "invoices"); got != fmt.Sprintf(`[{"id":%q,"invoice_number":"INV-1"}]`, f.invoiceID) {
		t.Errorf("invoices = %s, want [INV-1]", got)
	}
	if got := errorMessage(t, body); got != roleLastHolderMessage {
		t.Errorf("error = %q, want roleLastHolderMessage", got)
	}
}

func TestSetRoleMembers_RefusalListsEachWaitingInvoiceOnceOldestFirst(t *testing.T) {
	f := newRunFixture(t, "APSC-11 list order")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	a, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-A", f.versionID,
		pendingOn("tax-reviewer"),
		runStepSeed{Ord: 1, Kind: "approval", State: "pending", Key: ptr("tax-reviewer")})
	b, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-B", f.versionID, pendingOn("tax-reviewer"))

	_, err := f.store.SetRoleMembers(f.admin, "tax-reviewer", []string{})
	var inUse *roleInUse
	if !errors.As(err, &inUse) {
		t.Fatalf("SetRoleMembers err = %v, want *roleInUse", err)
	}
	want := []InvoiceRef{{ID: a, Number: "INV-A"}, {ID: b, Number: "INV-B"}}
	if len(inUse.invoices) != 2 || inUse.invoices[0] != want[0] || inUse.invoices[1] != want[1] || inUse.invoicesTotal != 2 {
		t.Errorf("invoices = %+v, total = %d, want %+v and 2", inUse.invoices, inUse.invoicesTotal, want)
	}
}

func TestSetRoleMembers_RefusalCapsTheListAtTwentyAndCountsAll(t *testing.T) {
	for _, c := range []struct{ runs, wantListed, wantTotal int }{{25, 20, 25}, {20, 20, 20}} {
		t.Run(fmt.Sprint(c.runs), func(t *testing.T) {
			f := newRunFixture(t, fmt.Sprintf("APSC-11 cap %d", c.runs))
			seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
			seedWaitingRuns(t, f, c.runs)

			_, err := f.store.SetRoleMembers(f.admin, "tax-reviewer", []string{})
			var inUse *roleInUse
			if !errors.As(err, &inUse) {
				t.Fatalf("SetRoleMembers err = %v, want *roleInUse", err)
			}
			if len(inUse.invoices) != c.wantListed || inUse.invoicesTotal != c.wantTotal {
				t.Errorf("listed = %d, total = %d, want %d and %d", len(inUse.invoices), inUse.invoicesTotal, c.wantListed, c.wantTotal)
			}
		})
	}
}

func TestSetRoleMembers_NoOpenRunSavesTheEmptySet(t *testing.T) {
	f := newRunFixture(t, "APSC-11 no run")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "admin", roleID: roleID})

	if _, err := f.store.SetRoleMembers(f.admin, "tax-reviewer", []string{}); err != nil {
		t.Fatalf("SetRoleMembers err = %v, want success", err)
	}
	if n := auditCount(t, f.super, f.tenantID, "workflow_role.staffed"); n != 1 {
		t.Errorf("workflow_role.staffed rows = %d, want 1", n)
	}
	if rows := staffingRows(t, f.super, roleID); len(rows) != 0 {
		t.Errorf("members = %v, want none", rows)
	}
}

func TestSetRoleMembers_ReplaceStillWholeSetWhileARunWaits(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 replace")
	a := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	b := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer"})

	f.wantSaved(t, b, a)

	if got := userIDs(staffingRows(t, f.super, f.roleID)); strings.Join(got, ",") != b+","+a {
		t.Errorf("members = %v, want [%s %s]", got, b, a)
	}
}

// waitForAdvisoryWaiter polls until a session waits on an advisory lock.
func waitForAdvisoryWaiter(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	for range 500 {
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE wait_event_type = 'Lock' AND wait_event = 'advisory' AND datname = current_database()`).Scan(&n); err != nil {
			t.Fatalf("read waiters: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session is waiting on the tenant advisory lock")
}

func TestSetRoleMembers_TakesTheTenantLockAfterTheAdminCheck(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 tenant lock")
	signer := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer"})
	preparer, _ := callerCtx(t, f.super, f.tenantID, "preparer", "active")

	hold, err := f.super.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer hold.Rollback(context.Background()) //nolint:errcheck
	if err := LockTenantPublishTx(context.Background(), hold, f.tenantID); err != nil {
		t.Fatalf("hold the tenant lock: %v", err)
	}

	forbidden := make(chan error, 1)
	go func() { _, err := f.store.SetRoleMembers(preparer, "tax-reviewer", []string{}); forbidden <- err }()
	select {
	case err := <-forbidden:
		if !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("non-admin err = %v, want ErrNotPermitted without waiting", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a non-admin waited on the tenant lock; requireActiveAdmin must come first")
	}

	done := make(chan error, 1)
	go func() { _, err := f.put(signer); done <- err }()
	waitForAdvisoryWaiter(t, f.super)
	select {
	case err := <-done:
		t.Fatalf("SetRoleMembers returned (%v) while another tx held the tenant lock", err)
	default:
	}
	if err := hold.Commit(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetRoleMembers err = %v, want success once the lock is free", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetRoleMembers did not finish after the lock was released")
	}
}

func TestSetRoleMembers_NonAdminGetsForbiddenNotWaiting(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 non-admin")
	preparer, _ := callerCtx(t, f.super, f.tenantID, "preparer", "active")

	_, err := f.store.SetRoleMembers(preparer, "tax-reviewer", []string{})
	if !errors.Is(err, ErrNotPermitted) || errors.Is(err, ErrRoleInUse) {
		t.Fatalf("SetRoleMembers err = %v, want ErrNotPermitted", err)
	}
	if status, _ := statusForErr(err); status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", status)
	}
}

func TestSetRoleMembers_UnknownAndDeletedRoleAreNotFoundNotWaiting(t *testing.T) {
	f := newRunFixture(t, "APSC-11 not found")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	softDeleteWorkflowRole(t, f.super, roleID)
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"), runStepSeed{Ord: 1, Kind: "approval", State: "pending", Key: ptr("ghost-key")})

	for _, key := range []string{"tax-reviewer", "ghost-key"} {
		if _, err := f.store.SetRoleMembers(f.admin, key, []string{}); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrRoleInUse) {
			t.Errorf("SetRoleMembers(%q) err = %v, want ErrNotFound", key, err)
		}
	}
}

func TestSetRoleMembers_RemovedMemberIsValidationNotWaiting(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-11 removed member")
	removed := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer", status: "removed"})

	_, err := f.put(removed)
	if !errors.Is(err, ErrValidation) || errors.Is(err, ErrRoleInUse) {
		t.Fatalf("SetRoleMembers err = %v, want ErrValidation", err)
	}
}
