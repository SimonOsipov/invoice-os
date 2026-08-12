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

// --- QA: inherited constraint 1 — a NaN total must not roll back the arm ------------

// TestArm_NaNTotalArmsSuccessfullyFoldedToZero pins the ArmTx step-2 constraint from
// task-480's Implementation Notes: total is read as total::text into a *string and
// parsed with decimal.NewFromString, mapping unparseable to nil — because
// decimal.Decimal.Scan errors on the literal "NaN", and Postgres accepts 'NaN'::numeric
// (verified live; 'Infinity' is rejected 22003 but 'NaN' is not). Arming runs inside
// the caller's promotion transaction, so a Scan error here would roll back the whole
// draft->validated promotion, not just the arm. seedInvoice + setInvoiceTotal write
// directly via SQL, bypassing the app-level total-non-negative rule that blocks NaN
// from reaching ArmTx through the normal invoice flow today (a runtime kill switch,
// not a schema guarantee — invoices.total carries no CHECK).
func TestArm_NaNTotalArmsSuccessfullyFoldedToZero(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-nan-total")
	entityID := seedBusinessEntity(t, super, tenantID, "NaN Total Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "nan-total-invoice-1")
	setInvoiceTotal(t, super, invoiceID, "NaN")

	policyID := seedApprovalPolicy(t, super, tenantID, "NaN-total threshold policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	condID := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500000000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("then"), Ord: 0,
		Kind: "approval", WorkflowRoleKey: ptr("fin_dir"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("else"), Ord: 0,
		Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-nan-total", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx over a NaN total: %v, want success — the arm must not roll back", err)
	}
	if res.Steps != 1 {
		t.Fatalf("Steps = %d, want 1", res.Steps)
	}
	run := oneApprovalRun(t, super, invoiceID)
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 1 || steps[0].Kind != "notify" {
		t.Fatalf("steps = %+v, want one notify step — NaN folds to 0, below the ₦500m threshold, so the else lane is chosen", steps)
	}
	if !res.Closed || run.State != "approved" {
		t.Errorf("run.State = %q, Closed = %v, want approved/true — a notify-only chosen lane closes the run", run.State, res.Closed)
	}
}

// --- QA: a NULL invoice total, distinct from NaN, also folds to 0 --------------------

// TestArm_NullInvoiceTotalFoldsToZero: seedInvoice leaves total NULL (no default, no
// CHECK). Against the SAME threshold policy as the NaN spec, a NULL total must select
// the else lane exactly like NaN does — proving the *string nil path (no row parse
// attempted at all) and the unparseable-string path both fold to absent/0, not two
// different behaviours.
func TestArm_NullInvoiceTotalFoldsToZero(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-null-total")
	entityID := seedBusinessEntity(t, super, tenantID, "Null Total Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "null-total-invoice-1") // total left NULL

	policyID := seedApprovalPolicy(t, super, tenantID, "Null-total threshold policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	condID := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500000000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("then"), Ord: 0,
		Kind: "approval", WorkflowRoleKey: ptr("fin_dir"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("else"), Ord: 0,
		Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-null-total", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx over a NULL total: %v, want success", err)
	}
	if res.Steps != 1 {
		t.Fatalf("Steps = %d, want 1", res.Steps)
	}
	steps := runStepsOf(t, super, oneApprovalRun(t, super, invoiceID).ID)
	if len(steps) != 1 || steps[0].Kind != "notify" {
		t.Fatalf("steps = %+v, want one notify step — a NULL total folds to 0, below the ₦500m threshold", steps)
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

// --- QA: the resolve query itself stays tenant-scoped, not just the write ------------

// TestArm_CrossTenantVersionResolutionStaysScoped: two tenants each hold their OWN
// active version, at the same time, with distinguishing role keys. Arming tenant B
// through its own matched tenantID/tx must resolve and materialise tenant B's version
// only — TestArm_MismatchedTenantRefusedByRLS already pins the write side (a mismatched
// tenantID is refused by the run INSERT's RLS check); this pins the READ side, that the
// unqualified `SELECT id FROM approval_policy_versions WHERE is_active` resolve
// (RLS-scoped by the tx's own app.current_tenant, not by any WHERE tenant_id clause)
// cannot see tenant A's active row while arming under tenant B's tx.
func TestArm_CrossTenantVersionResolutionStaysScoped(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := policyTenant(t, super, "APPR-06 arm-cross-tenant-resolve-a")
	tenantB := policyTenant(t, super, "APPR-06 arm-cross-tenant-resolve-b")

	policyA := seedApprovalPolicy(t, super, tenantA, "Tenant A policy")
	versionA := seedApprovalPolicyVersionN(t, super, tenantA, policyA, 1)
	seedApprovalPolicyStepInLane(t, super, tenantA, versionA, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("tenant-a-role"), SLAHours: ptr(11),
	})
	activateApprovalPolicyVersion(t, super, versionA)

	entityB := seedBusinessEntity(t, super, tenantB, "Tenant B Corp")
	invoiceB := seedInvoice(t, super, tenantB, entityB, "cross-tenant-resolve-invoice-b")
	policyB := seedApprovalPolicy(t, super, tenantB, "Tenant B policy")
	versionB := seedApprovalPolicyVersionN(t, super, tenantB, policyB, 1)
	seedApprovalPolicyStepInLane(t, super, tenantB, versionB, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("tenant-b-role"), SLAHours: ptr(22),
	})
	activateApprovalPolicyVersion(t, super, versionB)

	res, err := arm(t, app, tenantB, invoiceB, "fp-cross-tenant-resolve", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx(tenantB, invoiceB) with tenant A also holding an active version: %v", err)
	}
	if res.Steps != 1 {
		t.Fatalf("Steps = %d, want 1", res.Steps)
	}
	run := oneApprovalRun(t, super, invoiceB)
	if run.PolicyVersionID != versionB {
		t.Errorf("run.PolicyVersionID = %q, want tenant B's own version %q, not tenant A's %q",
			run.PolicyVersionID, versionB, versionA)
	}
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 1 || steps[0].WorkflowRoleKey == nil || *steps[0].WorkflowRoleKey != "tenant-b-role" {
		t.Errorf("steps = %+v, want one step with role tenant-b-role — tenant A's active version must not leak in", steps)
	}
}

// --- QA: ord is densely reassigned 0..N-1 across a mix of root and condition-lane kinds

// TestArm_OrdDensityAcrossMixedKinds: a root lane of [approval, condition, notify] whose
// condition's THEN lane holds [notify, autoapprove] and whose ELSE lane is empty. Above
// the threshold, materialise emits 4 steps drawn from two different source positions —
// the root approval, the chosen lane's two steps, and the root's trailing notify — none
// of which share the source tree's own per-lane ordinals (root ord 0/1/2; then-lane ord
// 0/1). The stored run must show ord 0..3 densely, in emission order, surviving the DB
// round trip through the bulk unnest insert.
func TestArm_OrdDensityAcrossMixedKinds(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-ord-density-mixed-kinds")
	entityID := seedBusinessEntity(t, super, tenantID, "Ord Density Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "ord-density-invoice-1")
	setInvoiceTotal(t, super, invoiceID, "750000000.00") // above the 500m threshold below

	policyID := seedApprovalPolicy(t, super, tenantID, "Ord density mixed-kinds policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("root-approval-role"), SLAHours: ptr(48),
	})
	condID := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500000000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("then"), Ord: 0,
		Kind: "notify", NotifyTarget: ptr("Then Lane Target"), NotifyChannel: ptr("Email"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("then"), Ord: 1,
		Kind: "autoapprove",
	})
	// else lane intentionally left empty
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "notify", NotifyTarget: ptr("Root Trailing Target"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-ord-density", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.Steps != 4 {
		t.Fatalf("Steps = %d, want 4", res.Steps)
	}

	run := oneApprovalRun(t, super, invoiceID)
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 4 {
		t.Fatalf("runStepsOf = %d rows, want 4", len(steps))
	}
	wantKinds := []string{"approval", "notify", "autoapprove", "notify"}
	for i, want := range wantKinds {
		if steps[i].Ord != i {
			t.Errorf("steps[%d].Ord = %d, want %d — ord must be dense 0..3 in emission order", i, steps[i].Ord, i)
		}
		if steps[i].Kind != want {
			t.Errorf("steps[%d].Kind = %q, want %q", i, steps[i].Kind, want)
		}
	}
	if steps[1].NotifyTarget == nil || *steps[1].NotifyTarget != "Then Lane Target" {
		t.Errorf("steps[1].NotifyTarget = %v, want \"Then Lane Target\"", steps[1].NotifyTarget)
	}
	if steps[3].NotifyTarget == nil || *steps[3].NotifyTarget != "Root Trailing Target" {
		t.Errorf("steps[3].NotifyTarget = %v, want \"Root Trailing Target\"", steps[3].NotifyTarget)
	}

	// `auto` is a single sticky flag over the WHOLE walk (materialise ports the SPA's
	// simulate verbatim): the then-lane's autoapprove sets it even though the root
	// approval sits at an earlier ord, so that root approval is written skipped, not
	// pending — and with no approval step left pending, the run closes. This is the one
	// DB-level pin of the auto-consumption seam this subtask owns (step 6); the pure
	// `materialise` boolean is already covered in engine_test.go, but nothing previously
	// checked the state ArmTx derives from it against a real stored row.
	if steps[0].State != "skipped" {
		t.Errorf("steps[0] (root approval) state = %q, want skipped — an autoapprove anywhere in the walk consumes every approval step", steps[0].State)
	}
	if !res.Closed || run.State != "approved" {
		t.Errorf("run.State = %q, Closed = %v, want approved/true — no approval step is left pending", run.State, res.Closed)
	}
}

// --- APPR-06-05 AC-6: an empty (NULL and "") role key still arms pending -------------

// TestArm_EmptyRoleKeyStillArmsPending: two approval steps, one with a NULL
// workflow_role_key and one with "". Neither carries a CHECK
// (approval_policy_steps and approval_run_steps both leave the column plain nullable
// text), so both are seedable, and both must arm pending — arming does no role lookup
// at all. The stored keys must round-trip distinctly: NULL stays NULL, "" stays "",
// mirroring the NULL-vs-0 sla_hours discipline at arm_test.go's due-at spec.
func TestArm_EmptyRoleKeyStillArmsPending(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-empty-role-key-pending")
	entityID := seedBusinessEntity(t, super, tenantID, "Empty Role Key Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "empty-role-key-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Empty role key policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: nil, SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr(""), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-empty-role-key", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.Steps != 2 {
		t.Fatalf("Steps = %d, want 2", res.Steps)
	}
	if res.Closed {
		t.Error("Closed = true, want false — both approval steps are pending")
	}

	run := oneApprovalRun(t, super, invoiceID)
	if run.State != "open" {
		t.Errorf("run.State = %q, want open", run.State)
	}
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 2 {
		t.Fatalf("runStepsOf = %d rows, want 2", len(steps))
	}

	if steps[0].State != "pending" {
		t.Errorf("steps[0] (NULL key) state = %q, want pending — neither satisfied nor skipped", steps[0].State)
	}
	if steps[0].WorkflowRoleKey != nil {
		t.Errorf("steps[0].WorkflowRoleKey = %q, want NULL, not '' — NULL and '' are distinct stored rows", *steps[0].WorkflowRoleKey)
	}

	if steps[1].State != "pending" {
		t.Errorf("steps[1] ('' key) state = %q, want pending — neither satisfied nor skipped", steps[1].State)
	}
	if steps[1].WorkflowRoleKey == nil {
		t.Error("steps[1].WorkflowRoleKey is NULL, want '', not NULL")
	} else if *steps[1].WorkflowRoleKey != "" {
		t.Errorf("steps[1].WorkflowRoleKey = %q, want ''", *steps[1].WorkflowRoleKey)
	}
}
