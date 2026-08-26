package approval

// task-489 (APPR-07-04, Mode A): RED specs for the approve half of Decide -- every
// case below fails against decision.go's stubs, on its own assertion. decision.go's
// Decide/decideTx/commitDecisionTx all return zero values unconditionally, so every
// "want success" case fails on its own read-back and every "want ErrX" case fails
// because the stub's error is nil.
//
// Test Specs table (task-489):
//
//	AC-2 TestApprove_AdminWithWorkflowRoleAllowed
//	AC-2 TestApprove_ReviewerWithWorkflowRoleAllowed
//	AC-2 TestApprove_NonApproverRoleRefused
//	AC-2 TestApprove_ApproverWithoutWorkflowRoleRefused
//	AC-2 TestApprove_SuspendedHolderRefused
//	AC-2 TestApprove_HolderOfASoftDeletedRoleRefused
//	AC-2 TestApprove_PermissionCheckPrecedesRowLock
//	AC-2 TestApprove_AxisTwoMatchesTheUnstaffedDetector
//	AC-3 TestApprove_AdvancesToTheNextPendingStep
//	AC-3 TestApprove_ClosesRunWithoutTouchingStatus
//	AC-3 TestApprove_SkipsNotifyStepsWhenAdvancing
//	AC-3 TestApprove_NonValidatedInvoiceIsConflict
//	AC-5 TestApprove_AuditsInSameTx
//	AC-5 TestApprove_AuditPayloadShape
//	AC-6 TestApprove_ConcurrentSingleDecision
//	AC-6 TestApprove_SecondApproveOnClosedRunIsConflict
//	AC-6 TestApprove_CancelledRunIsConflict
//	AC-6 TestApprove_SatisfiedStepUpdateAffectsNoRowsAndWritesNoDecision
//
// task-490 (APPR-07-05, Mode A) adds the reject half below: decideTx/commitDecisionTx
// gain tenantID/demoter parameters so a reject can call the Demoter seam, but the
// rejected branch itself is still the pre-existing stub (step+decision write only, no
// close, no demotion, no audit) -- every reject spec below fails on that.
//
//	AC-4 TestReject_CallsTheDemoterOnceAfterTheRunCloses
//	AC-4 TestReject_DemoterReceivesTheCallersSubjectNotSystem
//	AC-4 TestReject_ClosesRunRejectedAndWritesTheDecisionWithItsReason
//	AC-4 TestReject_StepIsRejectedWithNullSatisfiedColumns
//	AC-4 TestReject_LaterStepsStayPending
//	AC-4 TestReject_DoesNotCancelTheRun
//	AC-4 TestReject_NonValidatedInvoiceIsConflictAndWritesNothing
//	AC-4 TestReject_ReasonRequiredRefusesEmptyAndWhitespaceOnly
//	AC-4 TestReject_ReasonAtByteBoundarySucceeds
//	AC-4 TestReject_ReasonOverByteBoundaryRefused
//	AC-4 TestReject_MultiByteReasonOverByteBoundaryRefused
//	AC-3 TestReject_NilDemoterFailsClosedAndWritesNothing
//	AC-5 TestReject_AuditsInSameTx
//	AC-5 TestReject_AuditPayloadShape
//
// TestReject_DemotesThroughTheRealTransitionEdge (AC-8) cannot live in this package --
// internal/approval must not import internal/invoice -- so it lives in
// internal/invoice/reject_demotion_test.go, mirroring publish_sweep_fingerprint_test.go's
// identical cross-package problem for the Fingerprinter.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate
// step fails the build on any skip.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/reconciliation"
)

// --- fixtures ----------------------------------------------------------------------

// setInvoiceStatus overwrites the seedInvoice default ('draft') directly -- Decide's
// AC-3 precondition needs 'validated', and AC-3's own conflict case needs a status
// other than 'validated' on an otherwise-armed invoice.
func setInvoiceStatus(t *testing.T, super *pgxpool.Pool, invoiceID, status string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = $2 WHERE id = $1`, invoiceID, status)
	if err != nil {
		t.Fatalf("set invoice %s status to %s: %v", invoiceID, status, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set invoice %s status affected %d rows, want 1", invoiceID, tag.RowsAffected())
	}
}

// oneApprovalStep resolves one step's id by (run, ord) -- ArmResult carries no
// per-step ids.
func oneApprovalStep(t *testing.T, super *pgxpool.Pool, runID string, ord int) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM approval_run_steps WHERE run_id = $1 AND ord = $2`, runID, ord).Scan(&id); err != nil {
		t.Fatalf("read approval_run_steps id for run %s ord %d: %v", runID, ord, err)
	}
	return id
}

// invoiceStatusHistoryCount reads back invoice_status_history's row count for one
// invoice -- AC-11's "closing the run writes no history row" needs a before/after.
func invoiceStatusHistoryCount(t *testing.T, super *pgxpool.Pool, invoiceID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invoiceID).Scan(&n); err != nil {
		t.Fatalf("count invoice_status_history for %s: %v", invoiceID, err)
	}
	return n
}

// storedDecision is one approval_decisions row, read back as the superuser.
type storedDecision struct {
	RunStepID string
	Decision  string
	Actor     string
	Reason    *string
	DecidedAt time.Time
}

// decisionsForRun reads every approval_decisions row for runID, in decided_at order.
// No helper records a decision (there is no write path yet) -- schema_constraints_
// test.go:314-317 is the raw-insert precedent this mirrors for reads.
func decisionsForRun(t *testing.T, super *pgxpool.Pool, runID string) []storedDecision {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT run_step_id, decision, actor, reason, decided_at
		   FROM approval_decisions WHERE run_id = $1 ORDER BY decided_at`, runID)
	if err != nil {
		t.Fatalf("read approval_decisions for run %s: %v", runID, err)
	}
	defer rows.Close()
	out := []storedDecision{}
	for rows.Next() {
		var d storedDecision
		if err := rows.Scan(&d.RunStepID, &d.Decision, &d.Actor, &d.Reason, &d.DecidedAt); err != nil {
			t.Fatalf("scan approval_decisions for run %s: %v", runID, err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_decisions for run %s: %v", runID, err)
	}
	return out
}

// approve runs Decide(decision="approved") through a caller context built from
// (subject, tenantID) -- CreateRole's calling convention, since Decide resolves the
// caller from ctx like every other write in store.go, unlike arm()/cancel()'s
// tenantID+actor form.
func approve(t *testing.T, app *pgxpool.Pool, tenantID, subject, invoiceID string, reason *string) (Run, error) {
	t.Helper()
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	return NewStore(app, stubFingerprinter, nil).Decide(c, invoiceID, "approved", reason)
}

// reject runs Decide(decision="rejected") through a store built with stubDemoter --
// approve()'s twin. Most reject specs only need the demoter to be non-nil and
// successful; the ones that care WHAT was called (order, arguments) build their own
// store with spyDemoter instead.
func reject(t *testing.T, app *pgxpool.Pool, tenantID, subject, invoiceID string, reason *string) (Run, error) {
	t.Helper()
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	return NewStore(app, stubFingerprinter, stubDemoter).Decide(c, invoiceID, "rejected", reason)
}

// stubDemoter is the local Demoter double every reject spec needs (stubFingerprinter's
// precedent, policy_publish_test.go:127): succeeds and writes nothing of its own --
// decideTx's positive-path plumbing is what these specs pin, never a real
// invoice_status_history write. TestReject_DemotesThroughTheRealTransitionEdge
// (internal/invoice) is the honest oracle for the real transitionTx edge.
func stubDemoter(ctx context.Context, tx pgx.Tx, invoiceID, tenantID, subject string) error {
	return nil
}

// demoterCall records one Demoter invocation as spyDemoter observed it, including the
// run's own state read on the SAME tx at call time -- the only way to prove ORDER
// (AC-6: the run closes rejected BEFORE the demotion runs) rather than merely assume it.
type demoterCall struct {
	invoiceID, tenantID, subject string
	runStateAtCall               string
}

// spyDemoter returns a Demoter that records every call into calls and never performs
// the real demotion itself -- a demoter that DID demote would make ordering assertions
// against invoices.status indistinguishable from assertions against the spy's own
// side effect, proving nothing about what Decide actually did.
func spyDemoter(calls *[]demoterCall) Demoter {
	return func(ctx context.Context, tx pgx.Tx, invoiceID, tenantID, subject string) error {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM approval_runs WHERE invoice_id = $1`, invoiceID).Scan(&state); err != nil {
			return err
		}
		*calls = append(*calls, demoterCall{invoiceID: invoiceID, tenantID: tenantID, subject: subject, runStateAtCall: state})
		return nil
	}
}

// approveFixture is one open run on a validated invoice, gated by a single approval
// step naming roleKey. Shared by most specs below; each stages the caller/step state
// it needs on top.
type approveFixture struct {
	tenantID, entityID, invoiceID, roleID, runID, stepID string
}

func newApproveFixture(t *testing.T, super, app *pgxpool.Pool, name, roleKey string) approveFixture {
	t.Helper()
	var f approveFixture
	f.tenantID = policyTenant(t, super, name)
	f.entityID = seedBusinessEntity(t, super, f.tenantID, name+" Corp")
	f.invoiceID = seedInvoice(t, super, f.tenantID, f.entityID, name+"-invoice-1")
	setInvoiceStatus(t, super, f.invoiceID, "validated")
	f.roleID = seedWorkflowRole(t, super, f.tenantID, roleKey, roleKey)

	policyID := seedApprovalPolicy(t, super, f.tenantID, name+" policy")
	versionID := seedApprovalPolicyVersionN(t, super, f.tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, f.tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleKey), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, f.tenantID, f.invoiceID, "fp-"+name, "fixture-arm")
	if err != nil {
		t.Fatalf("arm fixture %q: %v", name, err)
	}
	f.runID = res.RunID
	f.stepID = oneApprovalStep(t, super, f.runID, 0)
	return f
}

// assertNothingWritten pins the "a refused caller writes nothing" half every
// refusal spec below needs: the lone step is still pending, no decision landed, the
// run is still open.
func assertNothingWritten(t *testing.T, super *pgxpool.Pool, f approveFixture) {
	t.Helper()
	if n := len(decisionsForRun(t, super, f.runID)); n != 0 {
		t.Errorf("approval_decisions rows for the run = %d, want 0 -- a refused caller must write nothing", n)
	}
	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 || steps[0].State != "pending" {
		t.Errorf("run steps = %+v, want the single step unchanged at pending", steps)
	}
	run := oneApprovalRun(t, super, f.invoiceID)
	if run.State != "open" {
		t.Errorf("run state = %q, want unchanged open", run.State)
	}
}

// --- AC-2: AXIS 1 (access role) and AXIS 2 (workflow-role staffing) -----------------

func TestApprove_AdminWithWorkflowRoleAllowed(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 admin-allowed", "admin-allowed-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := ptr("looks right")
	run, err := approve(t, app, f.tenantID, adminID, f.invoiceID, reason)
	if err != nil {
		t.Fatalf("Decide(approved) as a staffed active admin: %v, want success", err)
	}
	if run.State != "approved" {
		t.Errorf("returned run.State = %q, want approved -- the lone approval step just closed it", run.State)
	}

	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 {
		t.Fatalf("runStepsOf = %d rows, want 1", len(steps))
	}
	if steps[0].State != "satisfied" {
		t.Errorf("step state = %q, want satisfied", steps[0].State)
	}
	if steps[0].SatisfiedBy == nil || *steps[0].SatisfiedBy != adminID {
		t.Errorf("step satisfied_by = %v, want %q", steps[0].SatisfiedBy, adminID)
	}

	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 {
		t.Fatalf("approval_decisions rows = %d, want exactly 1", len(decisions))
	}
	d := decisions[0]
	if d.RunStepID != f.stepID || d.Decision != "approved" || d.Actor != adminID {
		t.Errorf("decision = %+v, want {run_step_id:%q decision:approved actor:%q}", d, f.stepID, adminID)
	}
	if d.Reason == nil || *d.Reason != "looks right" {
		t.Errorf("decision reason = %v, want %q", d.Reason, "looks right")
	}
}

// The other half of Q1's {admin, reviewer} set.
func TestApprove_ReviewerWithWorkflowRoleAllowed(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reviewer-allowed", "reviewer-allowed-role")

	reviewerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, reviewerID, "reviewer", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, reviewerID, 0)

	run, err := approve(t, app, f.tenantID, reviewerID, f.invoiceID, nil)
	if err != nil {
		t.Fatalf("Decide(approved) as a staffed active reviewer: %v, want success", err)
	}
	if run.State != "approved" {
		t.Errorf("returned run.State = %q, want approved", run.State)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 1 {
		t.Errorf("approval_decisions rows = %d, want exactly 1", n)
	}
}

func TestApprove_NonApproverRoleRefused(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 non-approver-refused", "non-approver-role")

	preparerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, preparerID, "preparer", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, preparerID, 0)

	_, err := approve(t, app, f.tenantID, preparerID, f.invoiceID, nil)
	if !errors.Is(err, ErrNotPermitted) {
		t.Errorf("Decide(approved) as a staffed preparer: err = %v, want ErrNotPermitted -- preparers are excluded (Q1)", err)
	}
	assertNothingWritten(t, super, f)
}

func TestApprove_ApproverWithoutWorkflowRoleRefused(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 unstaffed-approver-refused", "unstaffed-approver-role")

	reviewerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, reviewerID, "reviewer", "active") // never staffed into f.roleID

	_, err := approve(t, app, f.tenantID, reviewerID, f.invoiceID, nil)
	if !errors.Is(err, ErrNotRoleHolder) {
		t.Errorf("Decide(approved) as an unstaffed active reviewer: err = %v, want ErrNotRoleHolder -- a staffed non-approver does not satisfy a step, and neither does an unstaffed approver", err)
	}
	assertNothingWritten(t, super, f)
}

// TestApprove_NullRoleKeyGuardIsTracedThroughStoreDecide (AC-9, task-538): closes a gap
// TestGateFactsTx_NullRoleKeyOnThePendingStepIsNotHolding (gate_test.go:665) cannot --
// that test's own approve() call runs on the untraced app pool, like every approve() call
// site in this package, so nothing observes decideTx's SQL. A decideTx that dropped its
// `if roleKey != nil` guard and called HeldRoleKeysTx unconditionally with an empty key
// slice would not panic and would still leave holds false, passing every refusal test.
// This traces store.Decide itself against a NULL-role-key step.
func TestApprove_NullRoleKeyGuardIsTracedThroughStoreDecide(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-16-06 null-role-key-guard")
	entityID := seedBusinessEntity(t, super, tenantID, "Null Role Key Guard Corp")
	policyID := seedApprovalPolicy(t, super, tenantID, "Null role key guard policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: nil, SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "null-role-key-guard-invoice")
	setInvoiceStatus(t, super, invoiceID, "validated")
	if _, err := arm(t, app, tenantID, invoiceID, "fp-null-role-key-guard", "fixture-arm"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin", "active")

	traced, rec := tracedAppPool(t)
	rec.reset()
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	_, err := NewStore(traced, stubFingerprinter, nil).Decide(c, invoiceID, "approved", nil)

	if !errors.Is(err, ErrNotRoleHolder) {
		t.Errorf("Decide = %v, want ErrNotRoleHolder -- a NULL role key is held by nobody", err)
	}
	if got := rec.mentioning("FROM workflow_roles"); len(got) != 0 {
		t.Errorf("statements mentioning %q = %d, want 0 -- decideTx's roleKey != nil guard must skip AXIS 2 entirely on a NULL key: %v", "FROM workflow_roles", len(got), got)
	}
}

func TestApprove_SuspendedHolderRefused(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 suspended-holder-refused", "suspended-holder-role")

	suspendedID := uuid.NewString()
	seedMembership(t, super, f.tenantID, suspendedID, "reviewer", "suspended")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, suspendedID, 0)

	_, err := approve(t, app, f.tenantID, suspendedID, f.invoiceID, nil)
	// The request seam refuses a non-active caller before the store reads anything
	// (db.WithinRequestTenantTxOpts) -- before AXIS 1, not merely before AXIS 2.
	if !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("Decide(approved) as a suspended (but staffed) reviewer: err = %v, want db.ErrNotActiveMember -- the seam refuses before either axis is read", err)
	}
	assertNothingWritten(t, super, f)
}

func TestApprove_HolderOfASoftDeletedRoleRefused(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 soft-deleted-role-refused", "soft-deleted-role")

	reviewerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, reviewerID, "reviewer", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, reviewerID, 0)

	// Deleted AFTER the run was armed against it -- the workflow_role_key has no FK,
	// so the step keeps it verbatim (TestArm_SoftDeletedRoleStillArmsPending); AXIS 2
	// must still refuse because its eligibility read carries wr.deleted_at IS NULL.
	softDeleteWorkflowRole(t, super, f.roleID)

	_, err := approve(t, app, f.tenantID, reviewerID, f.invoiceID, nil)
	if !errors.Is(err, ErrNotRoleHolder) {
		t.Errorf("Decide(approved) staffed into a since-soft-deleted role: err = %v, want ErrNotRoleHolder", err)
	}
	assertNothingWritten(t, super, f)
}

// AC-2's no-existence-oracle guarantee: requireApprover is the FIRST statement, so a
// non-approver cannot tell an unknown invoice id from a real, forbidden one.
func TestApprove_PermissionCheckPrecedesRowLock(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 perm-precedes-lock", "perm-precedes-lock-role")

	preparerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, preparerID, "preparer", "active")

	traced, rec := tracedAppPool(t)
	store := NewStore(traced, stubFingerprinter, nil)
	callAs := func(t *testing.T, subject, invoiceID string) error {
		t.Helper()
		rec.reset()
		c := auth.WithIdentity(context.Background(),
			auth.Identity{Subject: subject, Role: "authenticated", TenantID: f.tenantID})
		_, err := store.Decide(c, invoiceID, "approved", nil)
		if got := rec.mentioning("invoices"); len(got) != 0 {
			t.Errorf("Decide against %s issued %d statement(s) mentioning \"invoices\" before refusing a non-approver, want 0 (the permission check must precede the row lock): %v", invoiceID, len(got), got)
		}
		return err
	}

	unknownErr := callAs(t, preparerID, uuid.NewString())
	forbiddenErr := callAs(t, preparerID, f.invoiceID) // real invoice, real open run -- caller just isn't an approver

	if !errors.Is(unknownErr, ErrNotPermitted) {
		t.Errorf("unknown invoice id: err = %v, want ErrNotPermitted", unknownErr)
	}
	if !errors.Is(forbiddenErr, ErrNotPermitted) {
		t.Errorf("forbidden (real) invoice id: err = %v, want ErrNotPermitted", forbiddenErr)
	}
	if unknownErr != forbiddenErr {
		t.Errorf("unknown-id error (%v) and forbidden-id error (%v) are not identical -- a non-approver must not be able to distinguish the two", unknownErr, forbiddenErr)
	}
	assertNothingWritten(t, super, f)
}

// Binds AXIS 2 to approval_blocked_unstaffed's eligibility semantics (active AND
// approver AND holds the role), not to literal SQL text -- one query carries
// tenant_id predicates (reconciliation runs superuser, RLS-bypassed) and the other
// must not (store.go:27-30).
func TestApprove_AxisTwoMatchesTheUnstaffedDetector(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 axis2-matches-unstaffed", "axis2-unstaffed-role")

	scan := func(t *testing.T) []reconciliation.Finding {
		t.Helper()
		var findings []reconciliation.Finding
		// The app pool, tenant-scoped -- the production role/shape (M5-06 System
		// Design step 2a; the two approval arms carry their own tenant_id joins on top
		// of RLS, so this matches whether or not RLS itself is active).
		err := db.WithinTenantTx(context.Background(), app, f.tenantID, func(tx pgx.Tx) error {
			var scanErr error
			findings, scanErr = reconciliation.Scan(context.Background(), tx, reconciliation.Thresholds{})
			return scanErr
		})
		if err != nil {
			t.Fatalf("reconciliation.Scan: %v", err)
		}
		return findings
	}
	hasUnstaffedFinding := func(findings []reconciliation.Finding) bool {
		for _, fnd := range findings {
			if fnd.InvoiceID == f.invoiceID && fnd.Kind == reconciliation.ApprovalBlockedUnstaffed {
				return true
			}
		}
		return false
	}

	if !hasUnstaffedFinding(scan(t)) {
		t.Fatal("reconciliation.Scan reports no approval_blocked_unstaffed finding for the unstaffed fixture -- the test setup is wrong, not AXIS 2")
	}

	unstaffedAdminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, unstaffedAdminID, "admin", "active")
	if _, err := approve(t, app, f.tenantID, unstaffedAdminID, f.invoiceID, nil); !errors.Is(err, ErrNotRoleHolder) {
		t.Errorf("Decide as an unstaffed active admin, while the detector flags this run unstaffed: err = %v, want ErrNotRoleHolder", err)
	}

	// Staff the SAME admin the detector says is missing -- the finding must
	// disappear AND that exact caller must now be let through.
	staffWorkflowRole(t, super, f.tenantID, f.roleID, unstaffedAdminID, 0)

	if hasUnstaffedFinding(scan(t)) {
		t.Error("reconciliation.Scan still reports approval_blocked_unstaffed after staffing an eligible active admin")
	}
	run, err := approve(t, app, f.tenantID, unstaffedAdminID, f.invoiceID, nil)
	if err != nil {
		t.Fatalf("Decide as the now-staffed admin: %v, want success", err)
	}
	if run.State != "approved" {
		t.Errorf("returned run.State = %q, want approved", run.State)
	}
}

// --- AC-3: precondition, advance, close, notify skip --------------------------------

func TestApprove_NonValidatedInvoiceIsConflict(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 non-validated-conflict", "non-validated-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	// The run is still open (an orphaned-run shape reconciliation's
	// approval_run_orphaned exists to flag) -- the precondition read must still catch it.
	setInvoiceStatus(t, super, f.invoiceID, "queued")

	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil)
	if !errors.Is(err, ErrNotAwaitingApproval) {
		t.Errorf("Decide(approved) against a queued (not validated) invoice: err = %v, want ErrNotAwaitingApproval", err)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 0 {
		t.Errorf("approval_decisions rows = %d, want 0", n)
	}
	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 || steps[0].State != "pending" {
		t.Errorf("run steps = %+v, want the single step unchanged at pending", steps)
	}
}

func TestApprove_AdvancesToTheNextPendingStep(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 advances-to-next-step")
	entityID := seedBusinessEntity(t, super, tenantID, "Advances Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "advances-invoice-1")
	setInvoiceStatus(t, super, invoiceID, "validated")

	roleA, roleB := "advances-role-a", "advances-role-b"
	roleAID := seedWorkflowRole(t, super, tenantID, roleA, roleA)
	roleBID := seedWorkflowRole(t, super, tenantID, roleB, roleB)
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, tenantID, roleAID, adminID, 0)
	staffWorkflowRole(t, super, tenantID, roleBID, adminID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Advances policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleA), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr(roleB), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-advances", "fixture-arm")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}

	run, err := approve(t, app, tenantID, adminID, invoiceID, nil)
	if err != nil {
		t.Fatalf("Decide(approved) on the first step: %v, want success", err)
	}
	if run.State != "open" {
		t.Errorf("returned run.State = %q, want open -- a second approval step remains pending", run.State)
	}

	steps := runStepsOf(t, super, res.RunID)
	if len(steps) != 2 {
		t.Fatalf("runStepsOf = %d rows, want 2", len(steps))
	}
	if steps[0].State != "satisfied" {
		t.Errorf("step 0 state = %q, want satisfied", steps[0].State)
	}
	if steps[1].State != "pending" {
		t.Errorf("step 1 state = %q, want pending", steps[1].State)
	}
	if n := len(decisionsForRun(t, super, res.RunID)); n != 1 {
		t.Errorf("approval_decisions rows = %d, want exactly 1", n)
	}
	storedRun := oneApprovalRun(t, super, invoiceID)
	if storedRun.State != "open" {
		t.Errorf("stored run.state = %q, want open", storedRun.State)
	}
}

func TestApprove_ClosesRunWithoutTouchingStatus(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 closes-run-no-status-touch", "closes-run-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	beforeHistory := invoiceStatusHistoryCount(t, super, f.invoiceID)

	run, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil)
	if err != nil {
		t.Fatalf("Decide(approved): %v, want success", err)
	}
	if run.State != "approved" {
		t.Errorf("returned run.State = %q, want approved", run.State)
	}
	if run.ClosedBy == nil || *run.ClosedBy != adminID {
		t.Errorf("returned run.ClosedBy = %v, want %q -- a real actor's decision, unlike ArmTx's own auto-close (\"system\")", run.ClosedBy, adminID)
	}

	storedRun := oneApprovalRun(t, super, f.invoiceID)
	if storedRun.State != "approved" {
		t.Errorf("stored run.state = %q, want approved", storedRun.State)
	}
	if storedRun.ClosedBy == nil || *storedRun.ClosedBy != adminID {
		t.Errorf("stored run.closed_by = %v, want %q", storedRun.ClosedBy, adminID)
	}

	var invoiceStatus string
	if err := super.QueryRow(context.Background(),
		`SELECT status FROM invoices WHERE id = $1`, f.invoiceID).Scan(&invoiceStatus); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if invoiceStatus != "validated" {
		t.Errorf("invoice status after the run closed = %q, want unchanged validated", invoiceStatus)
	}
	if n := invoiceStatusHistoryCount(t, super, f.invoiceID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d -- closing the run must write no history row", n, beforeHistory)
	}
}

func TestApprove_SkipsNotifyStepsWhenAdvancing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 skips-notify-when-advancing")
	entityID := seedBusinessEntity(t, super, tenantID, "Skips Notify Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "skips-notify-invoice-1")
	setInvoiceStatus(t, super, invoiceID, "validated")

	roleA, roleB := "skips-notify-role-a", "skips-notify-role-b"
	roleAID := seedWorkflowRole(t, super, tenantID, roleA, roleA)
	roleBID := seedWorkflowRole(t, super, tenantID, roleB, roleB)
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, tenantID, roleAID, adminID, 0)
	staffWorkflowRole(t, super, tenantID, roleBID, adminID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Skips notify policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleA), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "approval", WorkflowRoleKey: ptr(roleB), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-skips-notify", "fixture-arm")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	steps := runStepsOf(t, super, res.RunID)
	if len(steps) != 3 || steps[1].Kind != "notify" || steps[1].State != "skipped" {
		t.Fatalf("fixture steps = %+v, want [approval, notify(skipped), approval]", steps)
	}

	if _, err := approve(t, app, tenantID, adminID, invoiceID, nil); err != nil {
		t.Fatalf("Decide(approved) on step 0: %v, want success", err)
	}
	run, err := approve(t, app, tenantID, adminID, invoiceID, nil)
	if err != nil {
		t.Fatalf("Decide(approved) on step 2 (skipping the notify at ord 1): %v, want success", err)
	}
	if run.State != "approved" {
		t.Errorf("returned run.State = %q, want approved", run.State)
	}

	steps = runStepsOf(t, super, res.RunID)
	if steps[0].State != "satisfied" {
		t.Errorf("step 0 state = %q, want satisfied", steps[0].State)
	}
	if steps[1].State != "skipped" {
		t.Errorf("notify step state = %q, want unchanged skipped -- Decide must never touch a notify step", steps[1].State)
	}
	if steps[2].State != "satisfied" {
		t.Errorf("step 2 state = %q, want satisfied", steps[2].State)
	}
	if n := len(decisionsForRun(t, super, res.RunID)); n != 2 {
		t.Errorf("approval_decisions rows = %d, want exactly 2 (the notify step never gets one)", n)
	}
}

// --- AC-5: audit in the same transaction, payload shape -----------------------------

// decideTx has no free external lever the way ArmTx's caller-supplied `actor` does
// (caller.Subject feeds requireApprover's memberships lookup before it ever reaches
// the decision INSERT or the audit call, so a value crafted to fail late would
// already refuse earlier -- CreateRole hits the identical wall,
// TestWorkflowRole_CreateAuditsInSameTx). This drives decideTx directly inside its
// own db.WithinTenantTx, proves from WITHIN the still-open transaction that a real
// decision row exists, then forces the wrapping transaction to roll back regardless
// of decideTx's own result. If the decision INSERT and the audit INSERT were on two
// separate transactions, only the decision row would vanish here; both vanishing is
// the same-tx proof, and specifically proves audit_log's owner-proof/append-only
// grants do not stop a plain ROLLBACK from undoing an uncommitted INSERT.
func TestApprove_AuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 audit-same-tx", "audit-same-tx-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	beforeDecisions := len(decisionsForRun(t, super, f.runID))
	beforeAudit := auditCount(t, super, f.tenantID, "invoice.approval_approved")

	caller := auth.Identity{Subject: adminID, Role: "authenticated", TenantID: f.tenantID}
	forceErr := errors.New("APPR-07-04 QA: force rollback after a real decideTx write")
	err := db.WithinTenantTx(context.Background(), app, f.tenantID, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, decErr := decideTx(ctx, tx, f.invoiceID, "approved", nil, caller, nil); decErr != nil {
			return decErr
		}
		// Anti-vacuity: the decision row must be visible from WITHIN this
		// still-open transaction before it's thrown away, or the rollback
		// assertions below would pass trivially against a decideTx that never
		// wrote anything at all.
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM approval_decisions WHERE run_id = $1`, f.runID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("approval_decisions rows visible inside the uncommitted tx = %d, want 1 -- decideTx did not really write anything, so the rollback assertions below would prove nothing", n)
		}
		return forceErr
	})
	if !errors.Is(err, forceErr) {
		t.Fatalf("forced rollback: err = %v, want the forced sentinel %v", err, forceErr)
	}

	if n := len(decisionsForRun(t, super, f.runID)); n != beforeDecisions {
		t.Errorf("approval_decisions rows after the rollback = %d, want unchanged %d", n, beforeDecisions)
	}
	if n := auditCount(t, super, f.tenantID, "invoice.approval_approved"); n != beforeAudit {
		t.Errorf("invoice.approval_approved audit rows after the rollback = %d, want unchanged %d", n, beforeAudit)
	}
	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 || steps[0].State != "pending" {
		t.Errorf("step state after the rolled-back approve = %+v, want still pending", steps)
	}
}

func TestApprove_AuditPayloadShape(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 audit-payload-shape", "audit-payload-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := ptr("all documentation verified")
	if _, err := approve(t, app, f.tenantID, adminID, f.invoiceID, reason); err != nil {
		t.Fatalf("Decide(approved): %v, want success", err)
	}

	if n := auditCount(t, super, f.tenantID, "invoice.approval_approved"); n != 1 {
		t.Fatalf("invoice.approval_approved audit rows = %d, want exactly 1", n)
	}

	var payload []byte
	var actor string
	if err := super.QueryRow(context.Background(),
		`SELECT payload, actor FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.approval_approved'`,
		f.tenantID).Scan(&payload, &actor); err != nil {
		t.Fatalf("read the audit row: %v", err)
	}
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// Anti-vacuity guard first: an empty {} would trivially satisfy the key-set
	// check below without proving anything was actually written.
	if len(body) == 0 {
		t.Fatal("payload is empty {} -- want invoice_id/run_id/step_ord/reason populated")
	}
	allowed := map[string]bool{"invoice_id": true, "run_id": true, "step_ord": true, "reason": true}
	for k := range body {
		if !allowed[k] {
			t.Errorf("payload key %q is not in the allowlist {invoice_id, run_id, step_ord, reason} -- deliberately unlike ArmTx/CancelLiveRunTx's \"id\" key", k)
		}
	}
	for k := range allowed {
		if _, ok := body[k]; !ok {
			t.Errorf("payload is missing key %q", k)
		}
	}
	if got, ok := body["invoice_id"].(string); !ok || got != f.invoiceID {
		t.Errorf("payload invoice_id = %v, want %q", body["invoice_id"], f.invoiceID)
	}
	if got, ok := body["run_id"].(string); !ok || got != f.runID {
		t.Errorf("payload run_id = %v, want %q", body["run_id"], f.runID)
	}
	if got, ok := body["step_ord"].(float64); !ok || int(got) != 0 {
		t.Errorf("payload step_ord = %v, want 0", body["step_ord"])
	}
	if got, ok := body["reason"].(string); !ok || got != "all documentation verified" {
		t.Errorf("payload reason = %v, want %q", body["reason"], "all documentation verified")
	}
}

// --- AC-6: idempotency and run-state conflicts --------------------------------------

// A ONE-step run, deliberately: on a multi-step run two concurrent approves can
// legitimately decide two DIFFERENT steps, since the endpoint always addresses "the
// current pending step" and carries no step id of its own -- conflating that with
// idempotency would pin the wrong behaviour.
//
// Deterministic, not racy: the current-pending-step resolution takes FOR UPDATE on
// the one step row, so the two transactions serialise on that lock rather than
// race. Under READ COMMITTED, the loser's blocked SELECT re-evaluates its WHERE
// clause once the winner commits; the row no longer matches state='pending', and
// with only one step in this run there is no next row to fall through to, so the
// loser's own resolution finds zero rows -- which maps to ErrRunClosed, not a
// retry. The start-gate channel only removes goroutine-launch skew; the actual
// ordering guarantee comes from Postgres's row lock, not from timing, so this does
// not flake.
func TestApprove_ConcurrentSingleDecision(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 concurrent-single-decision", "concurrent-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	const n = 2
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = approve(t, app, f.tenantID, adminID, f.invoiceID, nil)
		}()
	}
	close(start)
	wg.Wait()

	var succeeded, conflicted int
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrRunClosed):
			conflicted++
		default:
			t.Fatalf("Decide returned %v, want nil or ErrRunClosed", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Errorf("succeeded = %d, conflicted (ErrRunClosed) = %d, want exactly 1 and 1: %v", succeeded, conflicted, errs)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 1 {
		t.Errorf("approval_decisions rows for the run = %d, want exactly 1", n)
	}
}

func TestApprove_SecondApproveOnClosedRunIsConflict(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 second-approve-closed-run", "second-approve-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	closeApprovalRunFor(t, super, f.runID, "approved", "someone-else")

	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil)
	if !errors.Is(err, ErrRunClosed) {
		t.Errorf("Decide(approved) on an already-closed run: err = %v, want ErrRunClosed", err)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 0 {
		t.Errorf("approval_decisions rows = %d, want 0 -- a closed run must accept no new decision", n)
	}
}

func TestApprove_CancelledRunIsConflict(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 cancelled-run-conflict", "cancelled-run-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	if cancelled, err := cancel(t, app, f.tenantID, f.invoiceID, "cancel-test-actor"); err != nil || !cancelled {
		t.Fatalf("cancel fixture: cancelled=%v err=%v, want true, nil", cancelled, err)
	}

	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil)
	if !errors.Is(err, ErrRunClosed) {
		t.Errorf("Decide(approved) on a cancelled run: err = %v, want ErrRunClosed", err)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 0 {
		t.Errorf("approval_decisions rows = %d, want 0", n)
	}
}

// AC-9's guard is unreachable through the full Decide seam under normal
// concurrency (the resolving SELECT is FOR UPDATE, so a racing transaction's own
// resolution simply finds no pending row instead -- TestApprove_
// ConcurrentSingleDecision above). This drives commitDecisionTx directly as a unit
// so the "AND state = 'pending'" predicate on its UPDATE is what has to refuse the
// second call.
func TestApprove_SatisfiedStepUpdateAffectsNoRowsAndWritesNoDecision(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 satisfied-step-no-op", "satisfied-step-role")

	call := func(t *testing.T) (satisfied bool, err error) {
		t.Helper()
		err = db.WithinTenantTx(context.Background(), app, f.tenantID, func(tx pgx.Tx) error {
			var callErr error
			satisfied, callErr = commitDecisionTx(context.Background(), tx, f.invoiceID, f.tenantID, f.runID, f.stepID, 0, "approved", "commit-tx-actor", nil, nil)
			return callErr
		})
		return satisfied, err
	}

	// Positive control FIRST: a genuinely pending step must satisfy and write
	// exactly one decision row -- without this, "satisfied == false" against the
	// pre-settled step below would pass vacuously against a commitDecisionTx that
	// never satisfies anything.
	satisfied, err := call(t)
	if err != nil {
		t.Fatalf("commitDecisionTx against a pending step: %v, want nil", err)
	}
	if !satisfied {
		t.Fatal("commitDecisionTx against a genuinely pending step reported satisfied=false, want true")
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 1 {
		t.Fatalf("approval_decisions rows after the control satisfy = %d, want exactly 1", n)
	}

	// Reset and settle the step DIRECTLY, bypassing commitDecisionTx entirely --
	// "the step is settled between resolution and the UPDATE" reproduced as a
	// precondition, so the guard's own predicate is what has to refuse the second call.
	if _, err := super.Exec(context.Background(),
		`DELETE FROM approval_decisions WHERE run_step_id = $1`, f.stepID); err != nil {
		t.Fatalf("reset: delete the control decision: %v", err)
	}
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_run_steps SET state = 'satisfied', satisfied_at = now(), satisfied_by = 'someone-else' WHERE id = $1`,
		f.stepID); err != nil {
		t.Fatalf("reset: settle the step directly: %v", err)
	}

	satisfied, err = call(t)
	if err != nil {
		t.Fatalf("commitDecisionTx against an already-satisfied step: %v, want nil (the idempotent no-op)", err)
	}
	if satisfied {
		t.Error("commitDecisionTx against an already-satisfied step reported satisfied=true, want false")
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 0 {
		t.Errorf("approval_decisions rows after the no-op = %d, want 0 -- a zero-rows-affected UPDATE must write no decision", n)
	}
}

// --- task-490 (APPR-07-05): the reject half of Decide -------------------------------

// TestReject_CallsTheDemoterOnceAfterTheRunCloses is the in-package companion to
// TestReject_DemotesThroughTheRealTransitionEdge (internal/invoice) -- this package
// cannot import internal/invoice, so a spy Demoter is what pins that Decide calls it
// exactly once, with the right invoiceID, AFTER the run is already closed; the real
// invoice_status_history write is proved from internal/invoice.
func TestReject_CallsTheDemoterOnceAfterTheRunCloses(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-calls-demoter", "reject-calls-demoter-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: adminID, Role: "authenticated", TenantID: f.tenantID})

	var calls []demoterCall
	if _, err := NewStore(app, stubFingerprinter, spyDemoter(&calls)).Decide(c, f.invoiceID, "rejected", ptr("wrong VAT")); err != nil {
		t.Fatalf("Decide(rejected): %v, want success", err)
	}
	if len(calls) != 1 {
		t.Fatalf("demoter calls = %d, want exactly 1: %+v", len(calls), calls)
	}
	got := calls[0]
	if got.invoiceID != f.invoiceID || got.tenantID != f.tenantID {
		t.Errorf("demoter call = %+v, want invoiceID:%q tenantID:%q", got, f.invoiceID, f.tenantID)
	}
	if got.runStateAtCall != "rejected" {
		t.Errorf("run state observed BY the demoter = %q, want rejected -- the run must close before the demotion runs (AC-6)", got.runStateAtCall)
	}
}

// TestReject_DemoterReceivesTheCallersSubjectNotSystem is TestReject_
// CallsTheDemoterOnceAfterTheRunCloses's companion for the actor argument
// specifically: the subject Decide passes to the demoter must be the real caller,
// never a fixed literal like "system" (the real invoice_status_history.actor proof
// is internal/invoice's TestReject_DemotesThroughTheRealTransitionEdge).
func TestReject_DemoterReceivesTheCallersSubjectNotSystem(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-demoter-actor", "reject-demoter-actor-role")

	reviewerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, reviewerID, "reviewer", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, reviewerID, 0)
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: reviewerID, Role: "authenticated", TenantID: f.tenantID})

	var calls []demoterCall
	if _, err := NewStore(app, stubFingerprinter, spyDemoter(&calls)).Decide(c, f.invoiceID, "rejected", ptr("wrong VAT")); err != nil {
		t.Fatalf("Decide(rejected): %v, want success", err)
	}
	if len(calls) != 1 {
		t.Fatalf("demoter calls = %d, want exactly 1", len(calls))
	}
	if calls[0].subject != reviewerID {
		t.Errorf("demoter subject = %q, want the caller's own subject %q", calls[0].subject, reviewerID)
	}
	if calls[0].subject == "system" {
		t.Error(`demoter subject = "system" -- reject must carry the real reviewer, never a system actor`)
	}
}

func TestReject_ClosesRunRejectedAndWritesTheDecisionWithItsReason(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-closes-run", "reject-closes-run-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := ptr("wrong VAT rate")
	run, err := reject(t, app, f.tenantID, adminID, f.invoiceID, reason)
	if err != nil {
		t.Fatalf("Decide(rejected): %v, want success", err)
	}
	if run.State != "rejected" {
		t.Errorf("returned run.State = %q, want rejected", run.State)
	}
	if run.ClosedBy == nil || *run.ClosedBy != adminID {
		t.Errorf("returned run.ClosedBy = %v, want %q", run.ClosedBy, adminID)
	}

	storedRun := oneApprovalRun(t, super, f.invoiceID)
	if storedRun.State != "rejected" {
		t.Errorf("stored run.state = %q, want rejected", storedRun.State)
	}
	if storedRun.ClosedBy == nil || *storedRun.ClosedBy != adminID {
		t.Errorf("stored run.closed_by = %v, want %q", storedRun.ClosedBy, adminID)
	}
	if storedRun.ClosedAt == nil {
		t.Error("stored run.closed_at = nil, want set")
	}

	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 {
		t.Fatalf("approval_decisions rows = %d, want exactly 1", len(decisions))
	}
	d := decisions[0]
	if d.Decision != "rejected" || d.Actor != adminID {
		t.Errorf("decision = %+v, want {decision:rejected actor:%q}", d, adminID)
	}
	if d.Reason == nil || *d.Reason != "wrong VAT rate" {
		t.Errorf("decision reason = %v, want %q", d.Reason, "wrong VAT rate")
	}
}

func TestReject_StepIsRejectedWithNullSatisfiedColumns(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-step-null-satisfied", "reject-step-null-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	if _, err := reject(t, app, f.tenantID, adminID, f.invoiceID, ptr("bad totals")); err != nil {
		t.Fatalf("Decide(rejected): %v, want success", err)
	}

	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 {
		t.Fatalf("runStepsOf = %d rows, want 1", len(steps))
	}
	if steps[0].State != "rejected" {
		t.Errorf("step state = %q, want rejected", steps[0].State)
	}
	if steps[0].SatisfiedAt != nil {
		t.Errorf("step satisfied_at = %v, want NULL -- rejected is not satisfied", steps[0].SatisfiedAt)
	}
	if steps[0].SatisfiedBy != nil {
		t.Errorf("step satisfied_by = %v, want NULL -- rejected is not satisfied", steps[0].SatisfiedBy)
	}
}

func TestReject_LaterStepsStayPending(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 reject-later-steps-pending")
	entityID := seedBusinessEntity(t, super, tenantID, "Reject Later Steps Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "reject-later-steps-invoice-1")
	setInvoiceStatus(t, super, invoiceID, "validated")

	roleA, roleB, roleC := "reject-later-role-a", "reject-later-role-b", "reject-later-role-c"
	roleAID := seedWorkflowRole(t, super, tenantID, roleA, roleA)
	roleBID := seedWorkflowRole(t, super, tenantID, roleB, roleB)
	roleCID := seedWorkflowRole(t, super, tenantID, roleC, roleC)
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, tenantID, roleAID, adminID, 0)
	staffWorkflowRole(t, super, tenantID, roleBID, adminID, 0)
	staffWorkflowRole(t, super, tenantID, roleCID, adminID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Reject later steps policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleA), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr(roleB), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "approval", WorkflowRoleKey: ptr(roleC), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-reject-later", "fixture-arm")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}

	run, err := reject(t, app, tenantID, adminID, invoiceID, ptr("step 0 rejected"))
	if err != nil {
		t.Fatalf("Decide(rejected) on step 0: %v, want success", err)
	}
	if run.State != "rejected" {
		t.Errorf("returned run.State = %q, want rejected", run.State)
	}

	steps := runStepsOf(t, super, res.RunID)
	if len(steps) != 3 {
		t.Fatalf("runStepsOf = %d rows, want 3", len(steps))
	}
	if steps[0].State != "rejected" {
		t.Errorf("step 0 state = %q, want rejected", steps[0].State)
	}
	if steps[1].State != "pending" {
		t.Errorf("step 1 state = %q, want pending -- a rejection must not touch later steps", steps[1].State)
	}
	if steps[2].State != "pending" {
		t.Errorf("step 2 state = %q, want pending", steps[2].State)
	}

	// Neither reconciliation detector fires on the closed run: approval_run_orphaned
	// and approval_blocked_unstaffed both gate on state = 'open' (AC-7's corroboration).
	var findings []reconciliation.Finding
	err = db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		var scanErr error
		findings, scanErr = reconciliation.Scan(context.Background(), tx, reconciliation.Thresholds{})
		return scanErr
	})
	if err != nil {
		t.Fatalf("reconciliation.Scan: %v", err)
	}
	for _, fnd := range findings {
		if fnd.InvoiceID == invoiceID {
			t.Errorf("reconciliation.Scan reports %v for the rejected invoice, want no findings -- the run is closed", fnd.Kind)
		}
	}
}

func TestReject_DoesNotCancelTheRun(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-not-cancelled", "reject-not-cancelled-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	run, err := reject(t, app, f.tenantID, adminID, f.invoiceID, ptr("wrong VAT"))
	if err != nil {
		t.Fatalf("Decide(rejected): %v, want success", err)
	}
	if run.State != "rejected" {
		t.Errorf("run.State = %q, want rejected, never cancelled", run.State)
	}

	storedRun := oneApprovalRun(t, super, f.invoiceID)
	if storedRun.State != "rejected" {
		t.Errorf("stored run.state = %q, want rejected (never cancelled -- CancelLiveRunTx must never run this path)", storedRun.State)
	}
}

func TestReject_NonValidatedInvoiceIsConflictAndWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-non-validated", "reject-non-validated-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	setInvoiceStatus(t, super, f.invoiceID, "queued")

	_, err := reject(t, app, f.tenantID, adminID, f.invoiceID, ptr("wrong VAT"))
	if !errors.Is(err, ErrNotAwaitingApproval) {
		t.Errorf("Decide(rejected) against a queued (not validated) invoice: err = %v, want ErrNotAwaitingApproval", err)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 0 {
		t.Errorf("approval_decisions rows = %d, want 0", n)
	}
	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 || steps[0].State != "pending" {
		t.Errorf("run steps = %+v, want the single step unchanged at pending", steps)
	}
	run := oneApprovalRun(t, super, f.invoiceID)
	if run.State != "open" {
		t.Errorf("run state = %q, want unchanged open", run.State)
	}
}

// TestReject_ReasonRequiredRefusesEmptyAndWhitespaceOnly: a reject reason is REQUIRED,
// unlike approve's -- nil, "" and whitespace-only must all be refused. Not yet enforced
// anywhere (decideTx/commitDecisionTx don't look at reason's content), so every case
// fails on the ErrValidation assertion today.
func TestReject_ReasonRequiredRefusesEmptyAndWhitespaceOnly(t *testing.T) {
	cases := []struct {
		name   string
		reason *string
	}{
		{"nil", nil},
		{"empty", ptr("")},
		{"whitespace-only", ptr("   \t\n  ")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			f := newApproveFixture(t, super, app, "APPR-07 reject-reason-required-"+tc.name, "reject-reason-required-role-"+tc.name)
			adminID := uuid.NewString()
			seedMembership(t, super, f.tenantID, adminID, "admin", "active")
			staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

			_, err := reject(t, app, f.tenantID, adminID, f.invoiceID, tc.reason)
			if !errors.Is(err, ErrValidation) {
				t.Errorf("Decide(rejected) with reason=%v: err = %v, want ErrValidation -- a reject reason is required", tc.reason, err)
			}
			assertNothingWritten(t, super, f)
		})
	}
}

// TestReject_ReasonAtByteBoundarySucceeds: exactly 1000 bytes is legal --
// maxKeepAsIsReasonLen's own bound (internal/invoice/handlers.go:865), mirrored here.
func TestReject_ReasonAtByteBoundarySucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-reason-at-boundary", "reject-reason-at-boundary-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("a", 1000)
	run, err := reject(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if err != nil {
		t.Fatalf("Decide(rejected) with a 1000-byte reason: %v, want success", err)
	}
	if run.State != "rejected" {
		t.Errorf("run.State = %q, want rejected -- a reason exactly at the 1000-byte bound must be legal", run.State)
	}
}

func TestReject_ReasonOverByteBoundaryRefused(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-reason-over-boundary", "reject-reason-over-boundary-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("a", 1001)
	_, err := reject(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("Decide(rejected) with a 1001-byte reason: err = %v, want ErrValidation", err)
	}
	assertNothingWritten(t, super, f)
}

// TestReject_MultiByteReasonOverByteBoundaryRefused proves the bound is byte-counted,
// not rune-counted (len(), not utf8.RuneCountInString) -- "€" is 3 bytes.
func TestReject_MultiByteReasonOverByteBoundaryRefused(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-reason-multibyte-over-boundary", "reject-reason-multibyte-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("€", 334) // 1002 bytes, 334 runes
	if n := utf8.RuneCountInString(reason); n >= 1000 {
		t.Fatalf("fixture reason has %d runes, want under 1000 -- the point of this test is the byte/rune gap", n)
	}
	if len(reason) <= 1000 {
		t.Fatalf("fixture reason has %d bytes, want over 1000", len(reason))
	}

	_, err := reject(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("Decide(rejected) with a %d-byte/%d-rune reason: err = %v, want ErrValidation (byte-counted, not rune-counted)",
			len(reason), utf8.RuneCountInString(reason), err)
	}
	assertNothingWritten(t, super, f)
}

// TestReject_NilDemoterFailsClosedAndWritesNothing (D31's fail-closed rule, mirroring
// TestPublish_NilFingerprinterFailsRatherThanWritingEmpty line for line): 200 in-package
// stores hold demoter == nil after the mechanical NewStore edit -- a nil demoter must
// refuse rather than nil-panic at reject time. Positive control in a FRESH fixture: f's
// lone step is no longer pending after the refusal, so re-deciding it would hit
// ErrRunClosed regardless of the demoter and prove nothing.
func TestReject_NilDemoterFailsClosedAndWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 nil-demoter", "nil-demoter-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: adminID, Role: "authenticated", TenantID: f.tenantID})

	_, err := NewStore(app, stubFingerprinter, nil).Decide(c, f.invoiceID, "rejected", ptr("wrong VAT"))
	if err == nil {
		t.Error("Decide(rejected) with a nil demoter succeeded, want a fail-closed error")
	}
	assertNothingWritten(t, super, f)

	// Positive control: a second, independent fixture -- a store built with a working
	// demoter rejects successfully. Without this the refusal above is vacuous.
	fc := newApproveFixture(t, super, app, "APPR-07 nil-demoter-control", "nil-demoter-control-role")
	controlAdminID := uuid.NewString()
	seedMembership(t, super, fc.tenantID, controlAdminID, "admin", "active")
	staffWorkflowRole(t, super, fc.tenantID, fc.roleID, controlAdminID, 0)
	cc := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: controlAdminID, Role: "authenticated", TenantID: fc.tenantID})

	run, err := NewStore(app, stubFingerprinter, stubDemoter).Decide(cc, fc.invoiceID, "rejected", ptr("wrong VAT"))
	if err != nil {
		t.Fatalf("control: Decide(rejected) with a working demoter: %v, want success -- the refusal above is vacuous unless this succeeds", err)
	}
	if run.State != "rejected" {
		t.Errorf("control: run.State = %q, want rejected", run.State)
	}
}

// TestReject_AuditsInSameTx mirrors TestApprove_AuditsInSameTx: decideTx driven directly
// (with spyDemoter, so the demoter's own participation is observable) inside its own
// db.WithinTenantTx, forced to roll back after a real write -- proves the decision, the
// demotion call and the audit all share one transaction. Using stubDemoter here would
// make the "same tx" property trivially true regardless of whether the demoter was ever
// wired in (any write on the passed-in tx rolls back with it, by construction) -- the
// demoter-was-called check is what makes this test about task-490's new behavior rather
// than a tautology about Go's pgx.Tx semantics.
func TestReject_AuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-audit-same-tx", "reject-audit-same-tx-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	beforeDecisions := len(decisionsForRun(t, super, f.runID))
	beforeAudit := auditCount(t, super, f.tenantID, "invoice.approval_rejected")
	beforeHistory := invoiceStatusHistoryCount(t, super, f.invoiceID)

	caller := auth.Identity{Subject: adminID, Role: "authenticated", TenantID: f.tenantID}
	forceErr := errors.New("APPR-07-05 QA: force rollback after a real decideTx write")
	reason := ptr("forced rollback reason")
	var calls []demoterCall
	err := db.WithinTenantTx(context.Background(), app, f.tenantID, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, decErr := decideTx(ctx, tx, f.invoiceID, "rejected", reason, caller, spyDemoter(&calls)); decErr != nil {
			return decErr
		}
		// Anti-vacuity: the decision row must be visible from WITHIN this still-open
		// transaction before it's thrown away, or the rollback assertions below would
		// pass trivially against a decideTx that never wrote anything at all.
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM approval_decisions WHERE run_id = $1`, f.runID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("approval_decisions rows visible inside the uncommitted tx = %d, want 1 -- decideTx did not really write anything, so the rollback assertions below would prove nothing", n)
		}
		if len(calls) != 1 {
			t.Fatalf("demoter calls observed inside the uncommitted tx = %d, want exactly 1 -- decideTx did not call the demoter on THIS tx, so the same-tx proof below would be about the decision write only, not the demotion", len(calls))
		}
		return forceErr
	})
	if !errors.Is(err, forceErr) {
		t.Fatalf("forced rollback: err = %v, want the forced sentinel %v", err, forceErr)
	}

	if n := len(decisionsForRun(t, super, f.runID)); n != beforeDecisions {
		t.Errorf("approval_decisions rows after the rollback = %d, want unchanged %d", n, beforeDecisions)
	}
	if n := auditCount(t, super, f.tenantID, "invoice.approval_rejected"); n != beforeAudit {
		t.Errorf("invoice.approval_rejected audit rows after the rollback = %d, want unchanged %d", n, beforeAudit)
	}
	if n := invoiceStatusHistoryCount(t, super, f.invoiceID); n != beforeHistory {
		t.Errorf("invoice_status_history rows after the rollback = %d, want unchanged %d -- the demotion must roll back too", n, beforeHistory)
	}
	run := oneApprovalRun(t, super, f.invoiceID)
	if run.State != "open" {
		t.Errorf("run state after the rollback = %q, want unchanged open", run.State)
	}
	var invoiceStatus string
	if err := super.QueryRow(context.Background(),
		`SELECT status FROM invoices WHERE id = $1`, f.invoiceID).Scan(&invoiceStatus); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if invoiceStatus != "validated" {
		t.Errorf("invoice status after the rollback = %q, want unchanged validated", invoiceStatus)
	}
}

func TestReject_AuditPayloadShape(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 reject-audit-payload-shape", "reject-audit-payload-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := ptr("incorrect line items")
	if _, err := reject(t, app, f.tenantID, adminID, f.invoiceID, reason); err != nil {
		t.Fatalf("Decide(rejected): %v, want success", err)
	}

	if n := auditCount(t, super, f.tenantID, "invoice.approval_rejected"); n != 1 {
		t.Fatalf("invoice.approval_rejected audit rows = %d, want exactly 1", n)
	}

	var payload []byte
	var actor string
	if err := super.QueryRow(context.Background(),
		`SELECT payload, actor FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.approval_rejected'`,
		f.tenantID).Scan(&payload, &actor); err != nil {
		t.Fatalf("read the audit row: %v", err)
	}
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("payload is empty {} -- want invoice_id/run_id/step_ord/reason populated")
	}
	allowed := map[string]bool{"invoice_id": true, "run_id": true, "step_ord": true, "reason": true}
	for k := range body {
		if !allowed[k] {
			t.Errorf("payload key %q is not in the allowlist {invoice_id, run_id, step_ord, reason}", k)
		}
	}
	for k := range allowed {
		if _, ok := body[k]; !ok {
			t.Errorf("payload is missing key %q", k)
		}
	}
	if got, ok := body["invoice_id"].(string); !ok || got != f.invoiceID {
		t.Errorf("payload invoice_id = %v, want %q", body["invoice_id"], f.invoiceID)
	}
	if got, ok := body["run_id"].(string); !ok || got != f.runID {
		t.Errorf("payload run_id = %v, want %q", body["run_id"], f.runID)
	}
	if got, ok := body["step_ord"].(float64); !ok || int(got) != 0 {
		t.Errorf("payload step_ord = %v, want 0", body["step_ord"])
	}
	if got, ok := body["reason"].(string); !ok || got != "incorrect line items" {
		t.Errorf("payload reason = %v, want %q", body["reason"], "incorrect line items")
	}
}
