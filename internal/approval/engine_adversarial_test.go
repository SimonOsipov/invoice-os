package approval

// QA: adversarial coverage for ArmTx, beyond the nine acceptance-criteria specs in
// arm_test.go — the closure predicate's edge shapes (an empty chosen lane, a
// notify-only policy), the audit-failure rollback, active-vs-newest-sealed
// resolution, and the two RLS/constraint-propagation specs (D-B's mismatched-tenant
// refusal, approval_runs_one_open's raw 23505).
//
// Every spec here starts RED against an undefined ArmTx/ArmResult, same as arm_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- AC-3, AC-4: an empty CHOSEN lane still arms a closed run ------------------------

// TestArm_EmptyChosenLaneArmsClosedApprovedRun: one root condition (> ₦500m), then =
// one approval step, else = empty (polH1's h1n2 shape, D14). Below threshold selects
// the empty else lane → zero steps, run closed approved/system. Pins that closure is
// decided AFTER materialisation (D34): a `len(tree)==0` implementation never even
// reads the chosen lane and would fail the control below.
func TestArm_EmptyChosenLaneArmsClosedApprovedRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-empty-chosen-lane")
	entityID := seedBusinessEntity(t, super, tenantID, "Empty Lane Corp")

	policyID := seedApprovalPolicy(t, super, tenantID, "Threshold-gated policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	condID := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500000000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("then"), Ord: 0,
		Kind: "approval", WorkflowRoleKey: ptr("fin_dir"), SLAHours: ptr(48),
	})
	// else lane intentionally left empty (zero rows)
	activateApprovalPolicyVersion(t, super, versionID)

	belowInvoice := seedInvoice(t, super, tenantID, entityID, "empty-lane-below")
	setInvoiceTotal(t, super, belowInvoice, "100000000.00")

	res, err := arm(t, app, tenantID, belowInvoice, "fp-empty-lane-below", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx below threshold: %v", err)
	}
	if res.Steps != 0 {
		t.Errorf("Steps = %d, want 0 — the chosen (else) lane is empty", res.Steps)
	}
	if !res.Closed {
		t.Error("Closed = false, want true — no approval step remains pending")
	}
	run := oneApprovalRun(t, super, belowInvoice)
	if run.State != "approved" || run.ClosedBy == nil || *run.ClosedBy != "system" {
		t.Errorf("run = %+v, want state approved, closed_by system", run)
	}
	if n := rowCount(t, super, "approval_run_steps", tenantID); n != 0 {
		t.Errorf("approval_run_steps rows = %d, want 0", n)
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 1 {
		t.Errorf("invoice.approval_armed audit rows = %d, want 1", n)
	}

	// Control: the same policy at 750,000,000 selects the THEN lane and arms open with
	// one pending step.
	aboveInvoice := seedInvoice(t, super, tenantID, entityID, "empty-lane-above")
	setInvoiceTotal(t, super, aboveInvoice, "750000000.00")
	if _, err := arm(t, app, tenantID, aboveInvoice, "fp-empty-lane-above", "test-actor"); err != nil {
		t.Fatalf("control: ArmTx above threshold: %v", err)
	}
	aboveRun := oneApprovalRun(t, super, aboveInvoice)
	if aboveRun.State != "open" {
		t.Errorf("control run state = %q, want open", aboveRun.State)
	}
	aboveSteps := runStepsOf(t, super, aboveRun.ID)
	if len(aboveSteps) != 1 || aboveSteps[0].State != "pending" {
		t.Errorf("control steps = %+v, want one pending step", aboveSteps)
	}
}

// --- AC-4: a notify-only policy still closes the run ----------------------------------

// TestArm_NotifyOnlyPolicyArmsClosedApprovedRun: the only node is a notify — the run
// is written approved/system with the notify row skipped, NOT left open. Control: a
// second tenant's notify-plus-approval policy arms open/pending — the shape a
// `len(steps)==0` guard fails to close: it HAS a step, so that guard never fires, yet
// nothing here could ever be satisfied (D35).
func TestArm_NotifyOnlyPolicyArmsClosedApprovedRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-notify-only")
	entityID := seedBusinessEntity(t, super, tenantID, "Notify Only Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "notify-only-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Notify-only policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-notify-only", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx against a notify-only policy: %v", err)
	}
	if res.Steps != 1 {
		t.Errorf("Steps = %d, want 1", res.Steps)
	}
	if !res.Closed {
		t.Error("Closed = false, want true — a notify step can never satisfy an approval")
	}
	run := oneApprovalRun(t, super, invoiceID)
	if run.State != "approved" || run.ClosedBy == nil || *run.ClosedBy != "system" {
		t.Errorf("run = %+v, want state approved, closed_by system — NOT left open", run)
	}
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 1 {
		t.Fatalf("runStepsOf = %d rows, want 1", len(steps))
	}
	notify := steps[0]
	if notify.State != "skipped" {
		t.Errorf("notify step state = %q, want skipped", notify.State)
	}
	if notify.NotifyTarget == nil || *notify.NotifyTarget != "Tax Team" {
		t.Errorf("notify_target = %v, want \"Tax Team\"", notify.NotifyTarget)
	}
	if notify.NotifyChannel == nil || *notify.NotifyChannel != "In-app" {
		t.Errorf("notify_channel = %v, want \"In-app\"", notify.NotifyChannel)
	}

	// Control: a second tenant whose active policy is notify plus approval arms
	// open/pending.
	controlTenant := policyTenant(t, super, "APPR-06 arm-notify-only-control")
	controlEntity := seedBusinessEntity(t, super, controlTenant, "Notify Plus Approval Corp")
	controlInvoice := seedInvoice(t, super, controlTenant, controlEntity, "notify-plus-approval-invoice")
	controlPolicy := seedApprovalPolicy(t, super, controlTenant, "Notify plus approval policy")
	controlVersion := seedApprovalPolicyVersionN(t, super, controlTenant, controlPolicy, 1)
	seedApprovalPolicyStepInLane(t, super, controlTenant, controlVersion, seedStepSpec{
		Ord: 0, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	seedApprovalPolicyStepInLane(t, super, controlTenant, controlVersion, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, controlVersion)
	if _, err := arm(t, app, controlTenant, controlInvoice, "fp-control", "test-actor"); err != nil {
		t.Fatalf("control: ArmTx: %v", err)
	}
	controlRun := oneApprovalRun(t, super, controlInvoice)
	if controlRun.State != "open" {
		t.Errorf("control run state = %q, want open", controlRun.State)
	}
}

// --- AC-9: a failing audit write rolls the whole arm back -----------------------------

// TestArm_AuditFailureRollsBackTheWholeArm forces 23514 via a 256-char actor —
// audit_actor_length CHECK (char_length(actor) > 0 AND <= 255) — and pins that the
// audit write is the LAST statement: all three ledger tables end empty, and the error
// propagates RAW (pgCode, not a sentinel).
func TestArm_AuditFailureRollsBackTheWholeArm(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-audit-failure-rollback")
	entityID := seedBusinessEntity(t, super, tenantID, "Audit Failure Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "audit-failure-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Audit failure policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	longActor := strings.Repeat("a", 256) // audit_actor_length admits <= 255

	_, err := arm(t, app, tenantID, invoiceID, "fp-audit-failure", longActor)
	if err == nil {
		t.Fatal("ArmTx with a 256-char actor succeeded, want audit_actor_length to refuse it")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("ArmTx with a 256-char actor: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	for _, table := range []string{"approval_runs", "approval_run_steps"} {
		if n := rowCount(t, super, table, tenantID); n != 0 {
			t.Errorf("%s rows = %d, want 0 — a failed audit must roll back the whole arm", table, n)
		}
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 0 {
		t.Errorf("invoice.approval_armed audit rows = %d, want 0", n)
	}
}

// --- AC-10: the active version is resolved by is_active alone, never "newest sealed" -

// TestArm_ResolvesActiveVersionNotNewestSealed: version 1 is active; a NEWER version 2
// is sealed but never activated and carries a different tree. The run must resolve
// version 1's id and materialise version 1's tree.
func TestArm_ResolvesActiveVersionNotNewestSealed(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-resolves-active-not-newest")
	entityID := seedBusinessEntity(t, super, tenantID, "Active Not Newest Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "active-not-newest-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Two sealed versions policy")
	activeVersion := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, activeVersion, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("active-version-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, activeVersion)

	// A NEWER version, sealed but never activated, whose tree differs.
	newerVersion := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
	seedApprovalPolicyStepInLane(t, super, tenantID, newerVersion, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("newer-version-role"), SLAHours: ptr(99),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, newerVersion, seedStepSpec{
		Ord: 1, Kind: "notify", NotifyTarget: ptr("Someone Else"), NotifyChannel: ptr("Email"),
	})
	sealApprovalPolicyVersion(t, super, newerVersion) // sealed, never activated

	res, err := arm(t, app, tenantID, invoiceID, "fp-active-not-newest", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	run := oneApprovalRun(t, super, invoiceID)
	if run.PolicyVersionID != activeVersion {
		t.Errorf("run.PolicyVersionID = %q, want the ACTIVE version %q, not the newer sealed one %q",
			run.PolicyVersionID, activeVersion, newerVersion)
	}
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 1 {
		t.Fatalf("runStepsOf = %d rows, want 1 (the active version's own tree)", len(steps))
	}
	if steps[0].WorkflowRoleKey == nil || *steps[0].WorkflowRoleKey != "active-version-role" {
		t.Errorf("step role = %v, want active-version-role — the newer sealed version's tree must not be used", steps[0].WorkflowRoleKey)
	}
	if res.Steps != 1 {
		t.Errorf("Steps = %d, want 1", res.Steps)
	}
}

// --- AC-1: D-B — the USING-only policy refuses a mismatched INSERT closed -----------

// TestArm_MismatchedTenantRefusedByRLS proves tenant_id is bound from the tenantID
// PARAMETER, not derived from the tx's own GUC: called inside tenant A's
// WithinTenantTx (so the resolve and invoice reads — both RLS-scoped by the tx's own
// app.current_tenant, never by the tenantID argument — see tenant A's own rows) but
// passed tenant B's tenantID, the run INSERT's tenant_id (bound explicitly from the
// parameter) disagrees with the tx's GUC and Postgres uses the USING clause as the
// WITH CHECK when none is given (D-B) — refused 42501, not silently written to the
// wrong tenant. Control: the same call with tenant A's own ids arms one run.
func TestArm_MismatchedTenantRefusedByRLS(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := policyTenant(t, super, "APPR-06 arm-mismatched-tenant-a")
	tenantB := policyTenant(t, super, "APPR-06 arm-mismatched-tenant-b")

	entityA := seedBusinessEntity(t, super, tenantA, "Tenant A Corp")
	invoiceA := seedInvoice(t, super, tenantA, entityA, "mismatched-tenant-invoice-a")
	policyA := seedApprovalPolicy(t, super, tenantA, "Tenant A policy")
	versionA := seedApprovalPolicyVersionN(t, super, tenantA, policyA, 1)
	seedApprovalPolicyStepInLane(t, super, tenantA, versionA, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionA)

	entityB := seedBusinessEntity(t, super, tenantB, "Tenant B Corp")
	seedInvoice(t, super, tenantB, entityB, "mismatched-tenant-invoice-b")
	policyB := seedApprovalPolicy(t, super, tenantB, "Tenant B policy")
	versionB := seedApprovalPolicyVersionN(t, super, tenantB, policyB, 1)
	seedApprovalPolicyStepInLane(t, super, tenantB, versionB, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionB)

	err := db.WithinTenantTx(context.Background(), app, tenantA, func(tx pgx.Tx) error {
		_, err := ArmTx(context.Background(), tx, tenantB, invoiceA, "fp-mismatched-tenant", "test-actor")
		return err
	})
	if err == nil {
		t.Fatal("ArmTx with tenant B's id inside tenant A's tx succeeded, want a refusal")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("SQLSTATE = %q, want 42501 (insufficient_privilege / RLS refusal): %v", code, err)
	}
	for _, tenantID := range []string{tenantA, tenantB} {
		for _, table := range []string{"approval_runs", "approval_run_steps"} {
			if n := rowCount(t, super, table, tenantID); n != 0 {
				t.Errorf("tenant %s %s rows = %d, want 0", tenantID, table, n)
			}
		}
		if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 0 {
			t.Errorf("tenant %s invoice.approval_armed audit rows = %d, want 0", tenantID, n)
		}
	}

	// Control: the same call with tenant A's own ids arms one run.
	if _, err := arm(t, app, tenantA, invoiceA, "fp-mismatched-tenant-control", "test-actor"); err != nil {
		t.Fatalf("control: ArmTx(tenantA, invoiceA): %v, want success", err)
	}
	if n := rowCount(t, super, "approval_runs", tenantA); n != 1 {
		t.Errorf("control: tenant A approval_runs rows = %d, want 1", n)
	}
}

// --- AC-1: approval_runs_one_open propagates a raw 23505 -----------------------------

// TestArm_SecondOpenRunPropagates23505Raw: an invoice already holding an open run
// (seedApprovalRun) refuses a second arm with a raw 23505 on approval_runs_one_open —
// no ErrConflict mapping, no swallow. Control: closing the existing run first lets a
// second arm succeed, proving a closed run does not occupy the partial-unique slot.
func TestArm_SecondOpenRunPropagates23505Raw(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-second-open-run")
	entityID := seedBusinessEntity(t, super, tenantID, "Second Open Run Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "second-open-run-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Second open run policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	existingRunID := seedApprovalRun(t, super, tenantID, invoiceID, versionID) // state defaults 'open'

	_, err := arm(t, app, tenantID, invoiceID, "fp-second-open-run", "test-actor")
	if err == nil {
		t.Fatal("ArmTx over an invoice already holding an open run succeeded, want a refusal")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_runs_one_open" {
		t.Errorf("constraint = %q, want approval_runs_one_open: %v", name, err)
	}
	if errors.Is(err, ErrConflict) {
		t.Error("err wraps ErrConflict — want the raw 23505 to propagate, no sentinel mapping")
	}
	if n := rowCount(t, super, "approval_runs", tenantID); n != 1 {
		t.Errorf("approval_runs rows = %d, want 1 (only the pre-existing run)", n)
	}
	if n := rowCount(t, super, "approval_run_steps", tenantID); n != 0 {
		t.Errorf("approval_run_steps rows = %d, want 0 — the failed arm wrote no steps", n)
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 0 {
		t.Errorf("invoice.approval_armed audit rows = %d, want 0", n)
	}

	// Control: close the existing run, then a second open run on the same invoice
	// succeeds — a closed run does not occupy approval_runs_one_open's slot.
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET state = 'approved', closed_at = now(), closed_by = 'test-fixture' WHERE id = $1`,
		existingRunID); err != nil {
		t.Fatalf("close the existing run: %v", err)
	}

	res, err := arm(t, app, tenantID, invoiceID, "fp-second-open-run-control", "test-actor")
	if err != nil {
		t.Fatalf("control: ArmTx after the existing run closed: %v, want success", err)
	}
	if res.RunID == "" || res.RunID == existingRunID {
		t.Errorf("control: RunID = %q, want a NEW run id distinct from the closed one %q", res.RunID, existingRunID)
	}
	if n := rowCount(t, super, "approval_runs", tenantID); n != 2 {
		t.Errorf("control: approval_runs rows = %d, want 2 (the closed one plus the new one)", n)
	}
}
