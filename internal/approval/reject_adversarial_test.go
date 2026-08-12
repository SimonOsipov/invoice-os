// task-490 (APPR-07-05, Mode B): QA adversarial coverage ON TOP OF the AC-shaped specs
// in decision_test.go -- concurrency, cross-decision ordering, a later-ord pending
// step, a failing demoter's rollback, and the multi-byte reason at exactly the byte
// bound (decision_test.go's own multi-byte spec is OVER the bound, never AT it).
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate
// step fails the build on any skip.
package approval

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"

	"github.com/google/uuid"
)

// TestReject_ConcurrentSingleDecision is TestApprove_ConcurrentSingleDecision's reject
// twin: two callers racing the SAME one-step run, both via Decide(rejected). The
// resolving SELECT is FOR UPDATE, so exactly one call wins the row and the other finds
// the run already closed.
func TestReject_ConcurrentSingleDecision(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07-05 QA concurrent-reject", "concurrent-reject-role")

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
			_, errs[i] = reject(t, app, f.tenantID, adminID, f.invoiceID, ptr("concurrent reject"))
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
			t.Fatalf("Decide(rejected) returned %v, want nil or ErrRunClosed", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Errorf("succeeded = %d, conflicted (ErrRunClosed) = %d, want exactly 1 and 1: %v", succeeded, conflicted, errs)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 1 {
		t.Errorf("approval_decisions rows for the run = %d, want exactly 1", n)
	}
}

// TestReject_AfterApproveClosedTheRunIsConflict: an approve closes the run first: a
// racing reject must see the closed run and refuse, writing nothing of its own.
func TestReject_AfterApproveClosedTheRunIsConflict(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07-05 QA reject-after-approve", "reject-after-approve-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	if _, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil); err != nil {
		t.Fatalf("approve fixture: %v, want success", err)
	}

	_, err := reject(t, app, f.tenantID, adminID, f.invoiceID, ptr("too late"))
	if !errors.Is(err, ErrRunClosed) {
		t.Errorf("Decide(rejected) on an already-approved run: err = %v, want ErrRunClosed", err)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 1 {
		t.Errorf("approval_decisions rows = %d, want 1 (only the approve, the reject wrote nothing)", n)
	}
	run := oneApprovalRun(t, super, f.invoiceID)
	if run.State != "approved" {
		t.Errorf("run state = %q, want unchanged approved", run.State)
	}
	var invoiceStatus string
	if err := super.QueryRow(context.Background(),
		`SELECT status FROM invoices WHERE id = $1`, f.invoiceID).Scan(&invoiceStatus); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if invoiceStatus != "validated" {
		t.Errorf("invoice status = %q, want unchanged validated -- a refused reject must never demote", invoiceStatus)
	}
}

// TestApprove_AfterRejectClosedTheRunIsConflict is the reverse ordering:
// TestApprove_SecondApproveOnClosedRunIsConflict's sibling with a reject as the first
// decision instead of an approve. reject() here uses stubDemoter (this package cannot
// import internal/invoice, so the invoice row is never actually demoted) -- the run's
// own state is what a racing approve must see and refuse on, regardless of demotion.
func TestApprove_AfterRejectClosedTheRunIsConflict(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07-05 QA approve-after-reject", "approve-after-reject-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	if _, err := reject(t, app, f.tenantID, adminID, f.invoiceID, ptr("wrong VAT")); err != nil {
		t.Fatalf("reject fixture: %v, want success", err)
	}

	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil)
	if !errors.Is(err, ErrRunClosed) {
		t.Errorf("Decide(approved) on an already-rejected run: err = %v, want ErrRunClosed", err)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 1 {
		t.Errorf("approval_decisions rows = %d, want 1 (only the reject, the approve wrote nothing)", n)
	}
	run := oneApprovalRun(t, super, f.invoiceID)
	if run.State != "rejected" {
		t.Errorf("run state = %q, want unchanged rejected", run.State)
	}
}

// TestReject_OnALaterPendingStepLeavesTheEarlierSatisfiedStepUntouched: step 0 is
// approved first (advancing the run to step 1, still open), then step 1 -- NOT step 0
// -- is rejected. Proves the "current pending step" resolver (ORDER BY ord LIMIT 1
// WHERE state='pending') targets the real later step rather than accidentally
// re-touching step 0, which is the only step every other reject spec ever exercises.
func TestReject_OnALaterPendingStepLeavesTheEarlierSatisfiedStepUntouched(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07-05 QA reject-later-pending-step")
	entityID := seedBusinessEntity(t, super, tenantID, "Reject Later Pending Step Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "reject-later-pending-invoice-1")
	setInvoiceStatus(t, super, invoiceID, "validated")

	roleA, roleB := "reject-later-pending-role-a", "reject-later-pending-role-b"
	roleAID := seedWorkflowRole(t, super, tenantID, roleA, roleA)
	roleBID := seedWorkflowRole(t, super, tenantID, roleB, roleB)
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, tenantID, roleAID, adminID, 0)
	staffWorkflowRole(t, super, tenantID, roleBID, adminID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Reject later pending step policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleA), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr(roleB), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-reject-later-pending", "fixture-arm")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}

	if _, err := approve(t, app, tenantID, adminID, invoiceID, nil); err != nil {
		t.Fatalf("Decide(approved) on step 0: %v, want success", err)
	}

	run, err := reject(t, app, tenantID, adminID, invoiceID, ptr("step 1 rejected"))
	if err != nil {
		t.Fatalf("Decide(rejected) on step 1: %v, want success", err)
	}
	if run.State != "rejected" {
		t.Errorf("run.State = %q, want rejected", run.State)
	}

	steps := runStepsOf(t, super, res.RunID)
	if len(steps) != 2 {
		t.Fatalf("runStepsOf = %d rows, want 2", len(steps))
	}
	if steps[0].State != "satisfied" {
		t.Errorf("step 0 state = %q, want satisfied (untouched by the later reject)", steps[0].State)
	}
	if steps[1].State != "rejected" {
		t.Errorf("step 1 state = %q, want rejected", steps[1].State)
	}
	if steps[1].SatisfiedAt != nil || steps[1].SatisfiedBy != nil {
		t.Errorf("step 1 satisfied_at/by = %v/%v, want both NULL -- rejected is not satisfied", steps[1].SatisfiedAt, steps[1].SatisfiedBy)
	}

	decisions := decisionsForRun(t, super, res.RunID)
	if len(decisions) != 2 {
		t.Fatalf("approval_decisions rows = %d, want 2 (one approve on step 0, one reject on step 1)", len(decisions))
	}
}

// TestReject_DemoterErrorRollsBackTheWholeDecision (AC-6's failure twin): a demoter
// that returns an error must roll back EVERYTHING commitDecisionTx wrote before it ran
// -- the decision row, the run's close to 'rejected', and the audit row that would
// have followed. Nothing here uses the forced-rollback decideTx-in-its-own-tx harness
// (TestReject_AuditsInSameTx) -- this drives the real Decide seam end to end, so the
// rollback is Go's own pgx error-return-aborts-the-tx behavior, not a test harness.
func TestReject_DemoterErrorRollsBackTheWholeDecision(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07-05 QA reject-demoter-error", "reject-demoter-error-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: adminID, Role: "authenticated", TenantID: f.tenantID})

	demoterErr := errors.New("APPR-07-05 QA: forced demoter failure")
	failingDemoter := func(ctx context.Context, tx pgx.Tx, invoiceID, tenantID, subject string) error {
		return demoterErr
	}

	_, err := NewStore(app, stubFingerprinter, failingDemoter).Decide(c, f.invoiceID, "rejected", ptr("wrong VAT"))
	if !errors.Is(err, demoterErr) {
		t.Fatalf("Decide(rejected) with a failing demoter: err = %v, want %v", err, demoterErr)
	}

	if n := len(decisionsForRun(t, super, f.runID)); n != 0 {
		t.Errorf("approval_decisions rows = %d, want 0 -- a failed demotion must roll back the decision too", n)
	}
	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 || steps[0].State != "pending" {
		t.Errorf("run steps = %+v, want the single step unchanged at pending", steps)
	}
	run := oneApprovalRun(t, super, f.invoiceID)
	if run.State != "open" {
		t.Errorf("run state = %q, want unchanged open -- the run's close must roll back with the failed demotion", run.State)
	}
	if n := auditCount(t, super, f.tenantID, "invoice.approval_rejected"); n != 0 {
		t.Errorf("invoice.approval_rejected audit rows = %d, want 0", n)
	}
}

// TestReject_MultiByteReasonAtByteBoundarySucceeds is TestReject_
// ReasonAtByteBoundarySucceeds's multi-byte twin: decision_test.go's own multi-byte
// spec (TestReject_MultiByteReasonOverByteBoundaryRefused) only proves the bound is
// byte- not rune-counted OVER the limit; this proves a multi-byte reason exactly AT
// the 1000-byte bound is legal and preserved byte-for-byte, not truncated or mangled.
func TestReject_MultiByteReasonAtByteBoundarySucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07-05 QA reject-multibyte-at-boundary", "reject-multibyte-at-boundary-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("é", 500) // 500 runes, 2 bytes each = 1000 bytes exactly
	if len(reason) != 1000 {
		t.Fatalf("fixture reason has %d bytes, want exactly 1000", len(reason))
	}

	run, err := reject(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if err != nil {
		t.Fatalf("Decide(rejected) with a 1000-byte multi-byte reason: %v, want success", err)
	}
	if run.State != "rejected" {
		t.Errorf("run.State = %q, want rejected -- a multi-byte reason exactly at the 1000-byte bound must be legal", run.State)
	}

	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 {
		t.Fatalf("approval_decisions rows = %d, want exactly 1", len(decisions))
	}
	if decisions[0].Reason == nil || *decisions[0].Reason != reason {
		t.Errorf("stored decision reason = %v, want the full 1000-byte reason preserved byte-for-byte", decisions[0].Reason)
	}
}
