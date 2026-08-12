package approval

// Store.ApprovalRun under a real Postgres: the run read-model assembly -- steps in ord
// order, holder/title resolution through subtask 01's port, overdue, and the decision
// ledger. Written before the method body exists (task-487's Test-first stage), so every
// spec here starts RED against read_model.go's ApprovalRun stub, which returns the zero
// Run and a nil error.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate step
// fails the build on any skip.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- fixtures ---------------------------------------------------------------

// setMembershipDisplayName gives a seeded membership a display name, so holder-text
// assertions read a real name instead of falling back to the bare user id.
func setMembershipDisplayName(t *testing.T, super *pgxpool.Pool, userID, displayName string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE memberships SET display_name = $2 WHERE user_id = $1`, userID, displayName)
	if err != nil {
		t.Fatalf("set membership display_name for %s: %v", userID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set membership display_name for %s affected %d rows, want 1", userID, tag.RowsAffected())
	}
}

// runStepID reads one approval_run_steps row's id by (run, ord) -- ArmResult carries no
// per-step ids and runStepsOf's storedRunStep (arm_test.go:107-118) omits the row id.
func runStepID(t *testing.T, super *pgxpool.Pool, runID string, ord int) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM approval_run_steps WHERE run_id = $1 AND ord = $2`, runID, ord,
	).Scan(&id); err != nil {
		t.Fatalf("read run step id (run %s ord %d): %v", runID, ord, err)
	}
	return id
}

// backdateRunStepDueAt rewrites one step's due_at, simulating time passing after arm --
// ArmTx only ever stamps due_at as now()+sla, so a past due_at needs a direct write.
func backdateRunStepDueAt(t *testing.T, super *pgxpool.Pool, stepID string, at time.Time) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_run_steps SET due_at = $2 WHERE id = $1`, stepID, at)
	if err != nil {
		t.Fatalf("backdate due_at for step %s: %v", stepID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("backdate due_at for step %s affected %d rows, want 1", stepID, tag.RowsAffected())
	}
}

// seedApprovalDecision inserts one approval_decisions row directly -- no SatisfyTx/DecideTx
// exists yet, so this is the raw-insert precedent (schema_constraints_test.go:314-317).
func seedApprovalDecision(t *testing.T, super *pgxpool.Pool, tenantID, runID, stepID, decision, actor string, reason *string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor, reason)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		tenantID, runID, stepID, decision, actor, reason,
	).Scan(&id); err != nil {
		t.Fatalf("seed approval_decisions: %v", err)
	}
	return id
}

// --- AC-2, AC-7: ordered steps, holder resolution -----------------------------------

// TestApprovalRun_ReturnsOrderedStepsWithHolders: a published 2-step policy armed on a
// validated invoice, role staffed with an active reviewer -- both steps come back in ord
// order, step 0 pending with the resolved holder text.
func TestApprovalRun_ReturnsOrderedStepsWithHolders(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 ordered-steps-with-holders")
	entityID := seedBusinessEntity(t, super, tenantID, "Ordered Steps Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "ordered-steps-invoice-1")

	roleID := seedWorkflowRole(t, super, tenantID, "reviewer-role", "Reviewer Role")
	holderUserID := uuid.NewString()
	seedMembership(t, super, tenantID, holderUserID, "reviewer", "active")
	setMembershipDisplayName(t, super, holderUserID, "Musa Danjuma")
	staffWorkflowRole(t, super, tenantID, roleID, holderUserID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Two-step policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("reviewer-role"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("other-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-ordered-steps", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 2 {
		t.Fatalf("len(run.Steps) = %d, want 2", len(run.Steps))
	}
	if run.Steps[0].Ord != 0 || run.Steps[1].Ord != 1 {
		t.Errorf("step order = [%d, %d], want [0, 1]", run.Steps[0].Ord, run.Steps[1].Ord)
	}
	if run.Steps[0].State != "pending" {
		t.Errorf("steps[0].State = %q, want pending", run.Steps[0].State)
	}
	want := resolveHolder(true, []holderInput{{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"}})
	if run.Steps[0].Holder == nil || *run.Steps[0].Holder != want {
		t.Errorf("steps[0].Holder = %v, want %+v", run.Steps[0].Holder, want)
	}
}

// --- AC-4, D22: card wording, not the inspector's ------------------------------------

// TestApprovalRun_HolderUsesResolveWordingNotInspector: a step whose role has one active
// holder reads the CARD wording (resolveHolder) -- the bare name, never the inspector's
// "Currently: " prefix.
func TestApprovalRun_HolderUsesResolveWordingNotInspector(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 resolve-not-inspector-wording")
	entityID := seedBusinessEntity(t, super, tenantID, "Resolve Wording Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "resolve-wording-invoice-1")

	roleID := seedWorkflowRole(t, super, tenantID, "sole-holder-role", "Sole Holder Role")
	holderUserID := uuid.NewString()
	seedMembership(t, super, tenantID, holderUserID, "admin", "active")
	setMembershipDisplayName(t, super, holderUserID, "Halima Yusuf")
	staffWorkflowRole(t, super, tenantID, roleID, holderUserID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sole holder policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("sole-holder-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-resolve-wording", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("len(run.Steps) = %d, want 1", len(run.Steps))
	}
	holder := run.Steps[0].Holder
	if holder == nil {
		t.Fatal("steps[0].Holder is nil, want a resolved value")
	}
	if holder.Text != "Halima Yusuf" {
		t.Errorf("holder.Text = %q, want the bare name %q", holder.Text, "Halima Yusuf")
	}
	if strings.Contains(holder.Text, "Currently:") {
		t.Errorf("holder.Text = %q, must never carry the inspector's \"Currently: \" prefix", holder.Text)
	}
}

// --- AC-3: the notify step is materialised, its role/holder fields are null ---------

// TestApprovalRun_NotifyStepIsIncludedSkippedWithTargetAndChannel: a policy with approval
// then notify(target, channel) armed -- the notify step is present at its ord, state
// "skipped", target and channel echoed, and its role/title/holder fields are all null
// (keyed off workflow_role_key IS NULL, not off kind -- drift finding 3).
func TestApprovalRun_NotifyStepIsIncludedSkippedWithTargetAndChannel(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 notify-step-included")
	entityID := seedBusinessEntity(t, super, tenantID, "Notify Included Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "notify-included-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Approval plus notify policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-notify-included", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 2 {
		t.Fatalf("len(run.Steps) = %d, want 2", len(run.Steps))
	}
	notify := run.Steps[1]
	if notify.Kind != "notify" || notify.State != "skipped" {
		t.Fatalf("steps[1] = %+v, want kind notify, state skipped", notify)
	}
	if notify.NotifyTarget == nil || *notify.NotifyTarget != "Tax Team" {
		t.Errorf("notify.NotifyTarget = %v, want \"Tax Team\"", notify.NotifyTarget)
	}
	if notify.NotifyChannel == nil || *notify.NotifyChannel != "In-app" {
		t.Errorf("notify.NotifyChannel = %v, want \"In-app\"", notify.NotifyChannel)
	}
	if notify.WorkflowRoleKey != nil {
		t.Errorf("notify.WorkflowRoleKey = %v, want nil", notify.WorkflowRoleKey)
	}
	if notify.WorkflowRoleTitle != nil {
		t.Errorf("notify.WorkflowRoleTitle = %v, want nil", notify.WorkflowRoleTitle)
	}
	if notify.Holder != nil {
		t.Errorf("notify.Holder = %v, want nil", notify.Holder)
	}
}

// --- AC-2: the autoapprove step is materialised, satisfied by "system" --------------

// TestApprovalRun_AutoapproveStepIsIncludedSatisfied: a policy with autoapprove before an
// approval step armed -- both steps are present, the autoapprove satisfied with
// satisfied_by "system".
func TestApprovalRun_AutoapproveStepIsIncludedSatisfied(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 autoapprove-step-included")
	entityID := seedBusinessEntity(t, super, tenantID, "Autoapprove Included Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "autoapprove-included-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Autoapprove then approval policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "autoapprove",
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-autoapprove-included", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 2 {
		t.Fatalf("len(run.Steps) = %d, want 2", len(run.Steps))
	}
	auto := run.Steps[0]
	if auto.Kind != "autoapprove" || auto.State != "satisfied" {
		t.Fatalf("steps[0] = %+v, want kind autoapprove, state satisfied", auto)
	}
	if auto.SatisfiedBy == nil || *auto.SatisfiedBy != "system" {
		t.Errorf("steps[0].SatisfiedBy = %v, want \"system\"", auto.SatisfiedBy)
	}
	if auto.SatisfiedAt == nil {
		t.Error("steps[0].SatisfiedAt is nil, want non-nil")
	}
}

// --- AC-5: overdue is state-gated, not just a due_at comparison ---------------------

// TestApprovalRun_OverdueOnlyForPendingSteps: two armed runs -- one with a pending
// approval step whose due_at is past, one whose approval step an autoapprove already
// settled but which still carries a past due_at -- overdue reads true then false.
func TestApprovalRun_OverdueOnlyForPendingSteps(t *testing.T) {
	super, app := dbTestPools(t)

	t.Run("pending step with a past due_at is overdue", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-07 overdue-pending-past-due-at")
		entityID := seedBusinessEntity(t, super, tenantID, "Overdue Pending Corp")
		invoiceID := seedInvoice(t, super, tenantID, entityID, "overdue-pending-invoice-1")

		policyID := seedApprovalPolicy(t, super, tenantID, "Overdue pending policy")
		versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(1),
		})
		activateApprovalPolicyVersion(t, super, versionID)

		res, err := arm(t, app, tenantID, invoiceID, "fp-overdue-pending", "test-actor")
		if err != nil {
			t.Fatalf("arm: %v", err)
		}
		backdateRunStepDueAt(t, super, runStepID(t, super, res.RunID, 0), time.Now().Add(-time.Hour))

		c, _ := callerCtx(t, super, tenantID, "preparer", "active")
		run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
		if err != nil {
			t.Fatalf("ApprovalRun: %v", err)
		}
		if len(run.Steps) != 1 {
			t.Fatalf("len(run.Steps) = %d, want 1", len(run.Steps))
		}
		if run.Steps[0].State != "pending" {
			t.Fatalf("steps[0].State = %q, want pending (the overdue assertion below is vacuous otherwise)", run.Steps[0].State)
		}
		if !run.Steps[0].Overdue {
			t.Error("steps[0].Overdue = false, want true")
		}
	})

	t.Run("autoapprove-settled step with a past due_at is not overdue", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-07 overdue-settled-past-due-at")
		entityID := seedBusinessEntity(t, super, tenantID, "Overdue Settled Corp")
		invoiceID := seedInvoice(t, super, tenantID, entityID, "overdue-settled-invoice-1")

		policyID := seedApprovalPolicy(t, super, tenantID, "Overdue settled policy")
		versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: 0, Kind: "autoapprove",
		})
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(1),
		})
		activateApprovalPolicyVersion(t, super, versionID)

		res, err := arm(t, app, tenantID, invoiceID, "fp-overdue-settled", "test-actor")
		if err != nil {
			t.Fatalf("arm: %v", err)
		}
		backdateRunStepDueAt(t, super, runStepID(t, super, res.RunID, 1), time.Now().Add(-time.Hour))

		c, _ := callerCtx(t, super, tenantID, "preparer", "active")
		run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
		if err != nil {
			t.Fatalf("ApprovalRun: %v", err)
		}
		if len(run.Steps) != 2 {
			t.Fatalf("len(run.Steps) = %d, want 2", len(run.Steps))
		}
		if run.Steps[1].State == "pending" {
			t.Fatal("steps[1].State = pending, want the autoapprove-settled skipped state (the overdue assertion below is vacuous otherwise)")
		}
		if run.Steps[1].Overdue {
			t.Error("steps[1].Overdue = true, want false -- state is not pending")
		}
	})
}

// --- AC-7: role-title and holder fallbacks ------------------------------------------

// TestApprovalRun_DeletedRoleTitleAndHolderFallback: a run whose step names a role
// soft-deleted after arm -- workflow_role_title reads "Deleted role" and holder reads
// {"Role no longer exists", true}.
func TestApprovalRun_DeletedRoleTitleAndHolderFallback(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 deleted-role-fallback")
	entityID := seedBusinessEntity(t, super, tenantID, "Deleted Role Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "deleted-role-invoice-1")

	roleID := seedWorkflowRole(t, super, tenantID, "soon-deleted-role", "Soon Deleted Role")

	policyID := seedApprovalPolicy(t, super, tenantID, "Deleted role policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("soon-deleted-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID) // sealed + activated while the role is still live

	if _, err := arm(t, app, tenantID, invoiceID, "fp-deleted-role", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	softDeleteWorkflowRole(t, super, roleID)

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("len(run.Steps) = %d, want 1", len(run.Steps))
	}
	step := run.Steps[0]
	if step.WorkflowRoleTitle == nil || *step.WorkflowRoleTitle != "Deleted role" {
		t.Errorf("WorkflowRoleTitle = %v, want \"Deleted role\"", step.WorkflowRoleTitle)
	}
	want := Resolved{Text: "Role no longer exists", Warn: true}
	if step.Holder == nil || *step.Holder != want {
		t.Errorf("Holder = %v, want %+v", step.Holder, want)
	}
}

// TestApprovalRun_UnstaffedRoleReadsNobodyAssigned: a run whose step names a live but
// unstaffed role -- holder reads {"Nobody assigned", true}.
func TestApprovalRun_UnstaffedRoleReadsNobodyAssigned(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 unstaffed-role")
	entityID := seedBusinessEntity(t, super, tenantID, "Unstaffed Role Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "unstaffed-role-invoice-1")

	seedWorkflowRole(t, super, tenantID, "unstaffed-role", "Unstaffed Role")

	policyID := seedApprovalPolicy(t, super, tenantID, "Unstaffed role policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("unstaffed-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-unstaffed-role", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("len(run.Steps) = %d, want 1", len(run.Steps))
	}
	step := run.Steps[0]
	if step.WorkflowRoleTitle == nil || *step.WorkflowRoleTitle != "Unstaffed Role" {
		t.Errorf("WorkflowRoleTitle = %v, want \"Unstaffed Role\"", step.WorkflowRoleTitle)
	}
	want := Resolved{Text: "Nobody assigned", Warn: true}
	if step.Holder == nil || *step.Holder != want {
		t.Errorf("Holder = %v, want %+v", step.Holder, want)
	}
}

// TestApprovalRun_SuspendedSoleHolderWarns: the step's only holder is a suspended
// reviewer -- holder.Warn is true and the text is that holder's bare name, no +N.
func TestApprovalRun_SuspendedSoleHolderWarns(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 suspended-sole-holder")
	entityID := seedBusinessEntity(t, super, tenantID, "Suspended Sole Holder Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "suspended-sole-holder-invoice-1")

	roleID := seedWorkflowRole(t, super, tenantID, "suspended-holder-role", "Suspended Holder Role")
	holderUserID := uuid.NewString()
	seedMembership(t, super, tenantID, holderUserID, "reviewer", "suspended")
	setMembershipDisplayName(t, super, holderUserID, "Halima Yusuf")
	staffWorkflowRole(t, super, tenantID, roleID, holderUserID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Suspended sole holder policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("suspended-holder-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-suspended-sole-holder", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("len(run.Steps) = %d, want 1", len(run.Steps))
	}
	holder := run.Steps[0].Holder
	if holder == nil {
		t.Fatal("Holder is nil, want a resolved value")
	}
	if !holder.Warn {
		t.Error("Holder.Warn = false, want true")
	}
	if holder.Text != "Halima Yusuf" {
		t.Errorf("Holder.Text = %q, want the bare name \"Halima Yusuf\" (no +N)", holder.Text)
	}
}

// TestApprovalRun_AllHoldersSuspendedWarnsWithPlusN: a role whose holders are ALL
// suspended, not just its sole one -- resolveHolder falls to resBlocked, primary is the
// first holder in wrm.ord order, extra counts the rest.
func TestApprovalRun_AllHoldersSuspendedWarnsWithPlusN(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 all-holders-suspended")
	entityID := seedBusinessEntity(t, super, tenantID, "All Suspended Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "all-suspended-invoice-1")

	roleID := seedWorkflowRole(t, super, tenantID, "all-suspended-role", "All Suspended Role")
	firstHolder := uuid.NewString()
	seedMembership(t, super, tenantID, firstHolder, "reviewer", "suspended")
	setMembershipDisplayName(t, super, firstHolder, "Halima Yusuf")
	staffWorkflowRole(t, super, tenantID, roleID, firstHolder, 0)
	secondHolder := uuid.NewString()
	seedMembership(t, super, tenantID, secondHolder, "admin", "suspended")
	setMembershipDisplayName(t, super, secondHolder, "Musa Danjuma")
	staffWorkflowRole(t, super, tenantID, roleID, secondHolder, 1)

	policyID := seedApprovalPolicy(t, super, tenantID, "All suspended policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("all-suspended-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-all-suspended", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("len(run.Steps) = %d, want 1", len(run.Steps))
	}
	holder := run.Steps[0].Holder
	if holder == nil {
		t.Fatal("Holder is nil, want a resolved value")
	}
	if !holder.Warn {
		t.Error("Holder.Warn = false, want true -- no eligible active holder exists")
	}
	if holder.Text != "Halima Yusuf +1" {
		t.Errorf("Holder.Text = %q, want \"Halima Yusuf +1\" (first-staffed holder, +1 for the other)", holder.Text)
	}
}

// --- AC-1: the no-oracle sentinel ----------------------------------------------------

// TestApprovalRun_NoRunIsNotFound: a validated invoice in a tenant with no active policy
// (so ArmTx wrote nothing) -- ApprovalRun answers ErrRunNotFound.
func TestApprovalRun_NoRunIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 no-run")
	entityID := seedBusinessEntity(t, super, tenantID, "No Run Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "no-run-invoice-1")

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	_, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("ApprovalRun with no run row: err = %v, want ErrRunNotFound", err)
	}
}

// TestApprovalRun_UnknownCrossTenantAndMalformedIdsAgree: an unknown uuid, another
// tenant's real (armed) invoice id, and "not-a-uuid" all answer ErrRunNotFound, with no
// raw Postgres SQLSTATE leaking through -- the GetPolicy no-oracle rule, and the point of
// this package's RLS-forced posture.
func TestApprovalRun_UnknownCrossTenantAndMalformedIdsAgree(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	tenantA := policyTenant(t, super, "APPR-07 cross-tenant A")
	entityA := seedBusinessEntity(t, super, tenantA, "Cross Tenant A Corp")
	invoiceA := seedInvoice(t, super, tenantA, entityA, "cross-tenant-invoice-a")
	policyA := seedApprovalPolicy(t, super, tenantA, "A policy")
	versionA := seedApprovalPolicyVersionN(t, super, tenantA, policyA, 1)
	seedApprovalPolicyStepInLane(t, super, tenantA, versionA, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionA)
	if _, err := arm(t, app, tenantA, invoiceA, "fp-cross-tenant-a", "test-actor"); err != nil {
		t.Fatalf("arm tenant A: %v", err)
	}

	tenantB := policyTenant(t, super, "APPR-07 cross-tenant B")
	cB, _ := callerCtx(t, super, tenantB, "preparer", "active")

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"unknown uuid", uuid.NewString()},
		{"malformed id", "not-a-uuid"},
		{"cross-tenant id (tenant A's real, armed invoice)", invoiceA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.ApprovalRun(cB, tc.id)
			if !errors.Is(err, ErrRunNotFound) {
				t.Errorf("ApprovalRun(%q) as tenant B: err = %v, want ErrRunNotFound", tc.id, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("ApprovalRun(%q) surfaced a raw Postgres error (SQLSTATE %s) -- it answers 404, not 500", tc.id, code)
			}
		})
	}

	// Control: A still sees its own armed run, so the refusals above are not a store
	// that answers not-found to everyone.
	cA, _ := callerCtx(t, super, tenantA, "preparer", "active")
	if _, err := store.ApprovalRun(cA, invoiceA); err != nil {
		t.Fatalf("ApprovalRun(A's invoice) as A: %v -- the refusals above are vacuous unless this succeeds", err)
	}
}

// --- AC-6: the decision ledger --------------------------------------------------------

// TestApprovalRun_DecisionLedgerCarriesActorTimeAndReason: a run with one
// approval_decisions row seeded directly -- the ledger entry echoes run_step_id, ord,
// decision, actor, decided_at, reason.
func TestApprovalRun_DecisionLedgerCarriesActorTimeAndReason(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 decision-ledger")
	entityID := seedBusinessEntity(t, super, tenantID, "Decision Ledger Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "decision-ledger-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Decision ledger policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-decision-ledger", "test-actor")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	stepID := runStepID(t, super, res.RunID, 0)
	reason := "Looks fine"
	seedApprovalDecision(t, super, tenantID, res.RunID, stepID, "approved", "qa-tester", &reason)

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Decisions) != 1 {
		t.Fatalf("len(run.Decisions) = %d, want 1", len(run.Decisions))
	}
	d := run.Decisions[0]
	if d.RunStepID != stepID {
		t.Errorf("RunStepID = %q, want %q", d.RunStepID, stepID)
	}
	if d.Ord != 0 {
		t.Errorf("Ord = %d, want 0", d.Ord)
	}
	if d.Decision != "approved" {
		t.Errorf("Decision = %q, want \"approved\"", d.Decision)
	}
	if d.Actor != "qa-tester" {
		t.Errorf("Actor = %q, want \"qa-tester\"", d.Actor)
	}
	if d.Reason == nil || *d.Reason != "Looks fine" {
		t.Errorf("Reason = %v, want \"Looks fine\"", d.Reason)
	}
	if d.DecidedAt.IsZero() {
		t.Error("DecidedAt is zero, want the seeded decision's timestamp")
	}
}

// TestApprovalRun_MultiStepRunWithNotifyAndDecisionAssemblesTogether (coverage: multiple
// approval steps AND a notify step AND a recorded decision in the same run): every piece
// the read model assembles is exercised at once, not only in isolation.
func TestApprovalRun_MultiStepRunWithNotifyAndDecisionAssemblesTogether(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 multi-step-notify-decision")
	entityID := seedBusinessEntity(t, super, tenantID, "Multi Step Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "multi-step-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Multi-step policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("fin_dir"), SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-multi-step", "test-actor")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	firstStepID := runStepID(t, super, res.RunID, 0)
	seedApprovalDecision(t, super, tenantID, res.RunID, firstStepID, "approved", "qa-tester", nil)

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 3 {
		t.Fatalf("len(run.Steps) = %d, want 3", len(run.Steps))
	}
	if run.Steps[2].Kind != "notify" || run.Steps[2].State != "skipped" {
		t.Errorf("steps[2] = %+v, want kind notify, state skipped", run.Steps[2])
	}
	if len(run.Decisions) != 1 {
		t.Fatalf("len(run.Decisions) = %d, want 1", len(run.Decisions))
	}
	if run.Decisions[0].RunStepID != firstStepID || run.Decisions[0].Ord != 0 {
		t.Errorf("Decisions[0] = %+v, want run_step_id %q ord 0", run.Decisions[0], firstStepID)
	}
}

// --- AC-1: a closed run is still readable ---------------------------------------------

// TestApprovalRun_ClosedRunStillReadable: a run closed approved is still readable --
// state "approved", closed_at/closed_by populated.
func TestApprovalRun_ClosedRunStillReadable(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 closed-run-readable")
	entityID := seedBusinessEntity(t, super, tenantID, "Closed Run Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "closed-run-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Empty policy (closes on arm)")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	activateApprovalPolicyVersion(t, super, versionID) // zero steps, sealed + active

	res, err := arm(t, app, tenantID, invoiceID, "fp-closed-run", "test-actor")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	if !res.Closed {
		t.Fatal("res.Closed = false, want true (the read assertions below are vacuous otherwise)")
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if run.State != "approved" {
		t.Errorf("run.State = %q, want \"approved\"", run.State)
	}
	if run.ClosedAt == nil {
		t.Error("run.ClosedAt is nil, want non-nil")
	}
	if run.ClosedBy == nil || *run.ClosedBy != "system" {
		t.Errorf("run.ClosedBy = %v, want \"system\"", run.ClosedBy)
	}
}
