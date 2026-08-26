// M5-06-02 (task-244): the drift model + per-tenant detection scan (Scan). Every case here
// is RED against the reconciliation.go stub, which always returns (nil, nil) regardless of
// what is seeded — so a case asserting PRESENCE of a Finding fails immediately (real RED).
//
// Four cases are fundamentally NEGATIVE assertions (a kind must be ABSENT, or the slice
// must be empty) that a nil-returning stub would satisfy VACUOUSLY — that is not RED for
// the right reason. Each of those four is paired with a POSITIVE control that DOES fail
// against the stub, following the repo's own "positive half, own tx" convention
// (submission_jobs_rls_test.go SJ-02/SJ-04/SJ-07 etc.):
//
//	TestRLS_ScanLostPollSuppressedByLiveRiverJob  — before/after: lost_poll fires with no
//	                                                 live river_job, then is suppressed once one exists.
//	TestRLS_ScanLostPollSuppressedWhenNotOverdue  — before/after: lost_poll fires while
//	                                                 overdue, then is suppressed once next_poll_at moves to the future.
//	TestRLS_ScanUsesLatestCycle                   — before/after: lost_poll fires on the lone
//	                                                 old pending cycle, then is suppressed once a newer terminal cycle exists.
//	TestRLS_ScanCleanInvoiceNoFindings            — a DIRTY companion invoice under the same
//	                                                 tenant must yield a finding; the clean invoice specifically must yield none.
//
// Spec-to-test map (M5-06 story, [M5-06-02] Test Specs table):
//
//	AC-1,3 TestRLS_ScanLostPollDetected
//	AC-3   TestRLS_ScanLostPollSuppressedByLiveRiverJob
//	AC-3   TestRLS_ScanLostPollSuppressedWhenNotOverdue
//	AC-1   TestRLS_ScanQueuedNeverSent
//	AC-1   TestRLS_ScanSubmittingOrphan
//	AC-1   TestRLS_ScanContradictionIRNWithoutAccepted
//	AC-1   TestRLS_ScanContradictionAcceptedWithoutIRN
//	AC-1   TestRLS_ScanContradictionVerdictNotRouted
//	AC-1   TestRLS_ScanPendingTooManyHops
//	AC-1   TestRLS_ScanPendingTooLong
//	AC-2   TestRLS_ScanUsesLatestCycle
//	AC-4   TestRLS_ScanCleanInvoiceNoFindings
//
// APPR-06-09 (task-485) adds four more cases for the two approval-drift kinds
// (approval_run_orphaned, approval_blocked_unstaffed) — RED against the shipped scanQuery,
// which has no arm for either kind yet:
//
//	AC-1,3 TestRLS_ScanApprovalRunOrphaned
//	AC-3   TestRLS_ScanApprovalRunOrphanedIgnoresValidated
//	AC-1,4 TestRLS_ScanApprovalBlockedUnstaffed
//	AC-5   TestRLS_ScanHealthyArmedInvoiceNoApprovalFindings
package reconciliation

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// L1: a genuine lost poll — invoice submitted, latest job pending, next_poll_at overdue
// past grace, attempts within the hop ceiling, no live river_job.
func TestRLS_ScanLostPollDetected(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	overdue := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJob()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}
	if len(got) != 1 || !findingEqual(got[0], want) {
		t.Errorf("Scan findings = %+v, want exactly [%+v]", got, want)
	}
}

// AC-3: a lost poll is suppressed once a non-terminal submission_poll river_job exists for
// the job. Positive control: without the river_job, the SAME fixture must trip lost_poll
// (proven first) — otherwise "no lost_poll after adding the river_job" would be vacuously
// true against a Scan that never returns anything.
func TestRLS_ScanLostPollSuppressedByLiveRiverJob(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	overdue := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJob()

	before, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (before the live river_job exists): %v", err)
	}
	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}
	if len(before) != 1 || !findingEqual(before[0], want) {
		t.Fatalf("Scan (no live river_job yet) findings = %+v, want exactly [%+v] — the positive "+
			"control this suppression case is measured against", before, want)
	}

	_, cleanupRiverJob := rcSeedRiverJob(t, h, "submission_poll", "scheduled", map[string]any{
		"tenant_id": tenantID, "invoice_id": invoiceID, "submission_job_id": jobID, "sequence": 1,
	})
	defer cleanupRiverJob()

	after, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (after the live river_job exists): %v", err)
	}
	if containsKind(after, LostPoll) {
		t.Errorf("Scan findings = %+v, want no lost_poll once a non-terminal submission_poll "+
			"river_job exists for the job", after)
	}
}

// AC-3: a lost poll is suppressed while next_poll_at has not yet passed grace. Same
// before/after shape as the live-river-job case above, moving next_poll_at into the future
// instead of adding a river_job.
func TestRLS_ScanLostPollSuppressedWhenNotOverdue(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	overdue := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJob()

	before, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (next_poll_at overdue): %v", err)
	}
	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}
	if len(before) != 1 || !findingEqual(before[0], want) {
		t.Fatalf("Scan (next_poll_at overdue) findings = %+v, want exactly [%+v] — the positive "+
			"control this suppression case is measured against", before, want)
	}

	future := time.Now().Add(30 * time.Minute)
	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE submission_jobs SET next_poll_at = $1 WHERE id = $2`, future, jobID)
		return e
	}); err != nil {
		t.Fatalf("move next_poll_at into the future: %v", err)
	}

	after, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (next_poll_at not overdue): %v", err)
	}
	if containsKind(after, LostPoll) {
		t.Errorf("Scan findings = %+v, want no lost_poll once next_poll_at is not yet overdue", after)
	}
}

// Q1: invoice queued, no job row at all, no live submission_submit river_job.
func TestRLS_ScanQueuedNeverSent(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "queued"})
	defer cleanupInvoice()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: nil, Kind: QueuedNeverSent, Healable: false}
	if len(got) != 1 || !findingEqual(got[0], want) {
		t.Errorf("Scan findings = %+v, want exactly [%+v]", got, want)
	}
}

// O1: latest job stuck submitting, updated_at stale past grace, no live submission_submit
// river_job.
func TestRLS_ScanSubmittingOrphan(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	stale := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "submitting", updatedAt: &stale})
	defer cleanupJob()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: &jobID, Kind: SubmittingOrphan, Healable: false}
	if len(got) != 1 || !findingEqual(got[0], want) {
		t.Errorf("Scan findings = %+v, want exactly [%+v]", got, want)
	}
}

// C1: an IRN with a status that never reached accepted.
func TestRLS_ScanContradictionIRNWithoutAccepted(t *testing.T) {
	h := requireHarness(t)

	irn := "NG-1"
	tenantID, _, _, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted", irn: &irn})
	defer cleanupInvoice()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !containsKind(got, IRNWithoutAccepted) {
		t.Errorf("Scan findings = %+v, want irn_without_accepted present", got)
	}
}

// C2: status accepted with no IRN on record.
func TestRLS_ScanContradictionAcceptedWithoutIRN(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, _, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "accepted"})
	defer cleanupInvoice()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !containsKind(got, AcceptedWithoutIRN) {
		t.Errorf("Scan findings = %+v, want accepted_without_irn present", got)
	}
}

// C3: the authority already returned a terminal verdict on the job, but the invoice is
// still sitting at submitted — a verdict that was never routed onto the invoice.
func TestRLS_ScanContradictionVerdictNotRouted(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	_, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "accepted"})
	defer cleanupJob()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !containsKind(got, VerdictNotRouted) {
		t.Errorf("Scan findings = %+v, want verdict_not_routed present", got)
	}
}

// H1: attempts exceed the hop ceiling — flagged, never re-armed, EVEN WITH a live poll in
// flight (H1 does not share L1's live-job suppression clause: a runaway chain must be
// flagged regardless of whether its current hop happens to still be in flight).
func TestRLS_ScanPendingTooManyHops(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 99})
	defer cleanupJob()
	_, cleanupRiverJob := rcSeedRiverJob(t, h, "submission_poll", "scheduled", map[string]any{
		"tenant_id": tenantID, "invoice_id": invoiceID, "submission_job_id": jobID, "sequence": 1,
	})
	defer cleanupRiverJob()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !containsKind(got, PendingTooManyHops) {
		t.Errorf("Scan findings = %+v, want pending_too_many_hops present", got)
	}
	if containsKind(got, LostPoll) {
		t.Errorf("Scan findings = %+v, want lost_poll ABSENT (attempts=99 > hop_ceiling=%d "+
			"excludes it via [rearm-skips-runaway-chains])", got, rcThresholds.HopCeiling)
	}
}

// H2: the job has sat pending far longer than any real retry budget should allow.
func TestRLS_ScanPendingTooLong(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	old := time.Now().Add(-72 * time.Hour)
	_, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", createdAt: &old})
	defer cleanupJob()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !containsKind(got, PendingTooLong) {
		t.Errorf("Scan findings = %+v, want pending_too_long present", got)
	}
}

// AC-2: detection must evaluate the LATEST submission_jobs cycle per invoice. Before/after:
// a lone old overdue-pending cycle trips lost_poll (positive control); once a newer
// terminal cycle exists for the same invoice, lost_poll must no longer fire.
func TestRLS_ScanUsesLatestCycle(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	oldCreated := time.Now().Add(-2 * time.Hour)
	oldOverdue := time.Now().Add(-1 * time.Hour)
	oldJobID, cleanupOldJob := rcSeedJob(t, h, tenantID, invoiceID,
		rcJobOpts{state: "pending", attempts: 1, nextPollAt: &oldOverdue, createdAt: &oldCreated})
	defer cleanupOldJob()

	before, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (single old pending cycle): %v", err)
	}
	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: &oldJobID, Kind: LostPoll, Healable: true}
	if len(before) != 1 || !findingEqual(before[0], want) {
		t.Fatalf("Scan (single old pending cycle) findings = %+v, want exactly [%+v] — the "+
			"positive control this latest-cycle case is measured against", before, want)
	}

	newer := time.Now().Add(-30 * time.Minute)
	_, cleanupNewJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "accepted", createdAt: &newer})
	defer cleanupNewJob()

	after, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (with a newer terminal cycle): %v", err)
	}
	if containsKind(after, LostPoll) {
		t.Errorf("Scan findings = %+v, want no lost_poll once a NEWER terminal job cycle exists "+
			"for the same invoice (detection must evaluate the latest cycle only, "+
			"[drift-scan-uses-latest-cycle])", after)
	}
}

// AC-4: a fully-consistent invoice (accepted + IRN + latest job accepted) yields NOTHING.
// Paired with a DIRTY companion invoice under the same tenant/Scan call, so an empty
// result for the clean invoice specifically is never a vacuous pass against a Scan that
// finds nothing for anyone.
func TestRLS_ScanCleanInvoiceNoFindings(t *testing.T) {
	h := requireHarness(t)

	dirtyIRN := "NG-DIRTY"
	tenantID, entityID, _, cleanupDirty := rcSeedInvoice(t, h,
		rcInvoiceOpts{status: "submitted", irn: &dirtyIRN})
	defer cleanupDirty()

	cleanIRN := "NG-CLEAN"
	cleanInvoiceID, cleanupClean := rcSeedInvoiceIn(t, h, tenantID, entityID,
		rcInvoiceOpts{status: "accepted", irn: &cleanIRN})
	defer cleanupClean()
	_, cleanupJob := rcSeedJob(t, h, tenantID, cleanInvoiceID, rcJobOpts{state: "accepted"})
	defer cleanupJob()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if !containsKind(got, IRNWithoutAccepted) {
		t.Fatalf("Scan findings = %+v, want the DIRTY companion invoice's irn_without_accepted "+
			"present — the positive control this clean-invoice case is measured against", got)
	}
	for _, f := range got {
		if f.InvoiceID == cleanInvoiceID {
			t.Errorf("Scan findings for the CLEAN invoice = %+v, want none (accepted + IRN + "+
				"latest job accepted is fully consistent)", f)
		}
	}
}

// AC-1, AC-3: an open approval run on an invoice no longer at 'validated' is drift — the run
// should have closed (APPR-06-07's demotion-cancel path) once the invoice moved off the
// state that justified it. Second leg pins D20: the ungated validated -> queued window must
// ALSO flag, not just the draft demotion path.
func TestRLS_ScanApprovalRunOrphaned(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, entityID, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupInvoice()
	invoiceID2, cleanupInvoice2 := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupInvoice2()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	rcSeedRun(t, h, tenantID, invoiceID, versionID, "open")
	rcSeedRun(t, h, tenantID, invoiceID2, versionID, "open")

	if _, err := h.super.Exec(ctx, `UPDATE invoices SET status = 'draft' WHERE id = $1`, invoiceID); err != nil {
		t.Fatalf("flip invoice to draft out of band: %v", err)
	}
	if _, err := h.super.Exec(ctx, `UPDATE invoices SET status = 'queued' WHERE id = $1`, invoiceID2); err != nil {
		t.Fatalf("flip invoice2 to queued out of band: %v", err)
	}

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: nil, Kind: rcApprovalRunOrphaned, Healable: false}
	if n := countForInvoice(got, invoiceID); n != 1 {
		t.Fatalf("Scan findings for the draft-demoted invoice %q = %d (%+v), want exactly 1", invoiceID, n, got)
	}
	for _, f := range got {
		if f.InvoiceID == invoiceID && !findingEqual(f, want) {
			t.Errorf("finding = %+v, want %+v", f, want)
		}
	}

	if !containsFindingFor(got, invoiceID2, rcApprovalRunOrphaned) {
		t.Errorf("Scan findings = %+v, want approval_run_orphaned ALSO for invoice %q "+
			"(D20 — the ungated validated -> queued window must stay visible)", got, invoiceID2)
	}
	// A bare 'queued' status with no submission_jobs row is ALSO the pre-existing Q1
	// (queued_never_sent) trigger — that arm legitimately co-fires here, so only an
	// unrelated THIRD kind on invoiceID2 would be unexpected.
	for _, f := range got {
		if f.InvoiceID == invoiceID2 && f.Kind != rcApprovalRunOrphaned && f.Kind != QueuedNeverSent {
			t.Errorf("Scan findings for invoice %q includes unexpected kind %+v", invoiceID2, f)
		}
	}
}

// AC-3: a validated invoice with an open run whose current step is STAFFED must not flag as
// orphaned. Positive control in the same test: the same invoice flipped to draft produces
// exactly one approval_run_orphaned — staffing the step is required, or a stray
// approval_blocked_unstaffed would muddy the count.
func TestRLS_ScanApprovalRunOrphanedIgnoresValidated(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupInvoice()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	roleID := rcSeedWorkflowRole(t, h, tenantID, "staffed-role", false)
	userID := rcSeedMember(t, h, tenantID, "admin", "active")
	rcStaffRole(t, h, tenantID, roleID, userID, 0)
	roleKey := "staffed-role"

	runID := rcSeedRun(t, h, tenantID, invoiceID, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runID, 1, "approval", &roleKey, "pending")

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (validated, staffed current step): %v", err)
	}
	if n := countForInvoice(got, invoiceID); n != 0 {
		t.Errorf("Scan findings for invoice %q = %d (%+v), want 0 — validated invoice, open "+
			"run, staffed current step must produce neither approval finding", invoiceID, n, got)
	}

	if _, err := h.super.Exec(ctx, `UPDATE invoices SET status = 'draft' WHERE id = $1`, invoiceID); err != nil {
		t.Fatalf("flip invoice to draft: %v", err)
	}

	after, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (after draft demotion): %v", err)
	}
	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: nil, Kind: rcApprovalRunOrphaned, Healable: false}
	if n := countForInvoice(after, invoiceID); n != 1 {
		t.Fatalf("Scan (after draft demotion) findings for invoice %q = %d (%+v), want exactly "+
			"1 — the positive control this ignores-validated case is measured against", invoiceID, n, after)
	}
	for _, f := range after {
		if f.InvoiceID == invoiceID && !findingEqual(f, want) {
			t.Errorf("finding after demotion = %+v, want %+v", f, want)
		}
	}
}

// AC-1, AC-4: a validated invoice, open run, whose lowest-ord pending approval step names a
// live role with zero holders is drift.
func TestRLS_ScanApprovalBlockedUnstaffed(t *testing.T) {
	h := requireHarness(t)

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupInvoice()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	rcSeedWorkflowRole(t, h, tenantID, "empty-role", false) // zero holders, on purpose
	roleKey := "empty-role"

	runID := rcSeedRun(t, h, tenantID, invoiceID, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runID, 1, "approval", &roleKey, "pending")

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := Finding{InvoiceID: invoiceID, InvoiceNumber: rcInvoiceNumberFor(t, h, invoiceID), SubmissionJobID: nil, Kind: rcApprovalBlockedUnstaffed, Healable: false}
	if n := countForInvoice(got, invoiceID); n != 1 {
		t.Fatalf("Scan findings for invoice %q = %d (%+v), want exactly 1", invoiceID, n, got)
	}
	for _, f := range got {
		if f.InvoiceID == invoiceID && !findingEqual(f, want) {
			t.Errorf("finding = %+v, want %+v", f, want)
		}
	}
}

// AC-5: a fully-healthy armed invoice (validated, open run, current step staffed by an
// active reviewer) yields NEITHER new kind. Positive control: a deliberately drifted sibling
// invoice under the same tenant/Scan call DOES return a finding, so the empty result for the
// healthy invoice is never a vacuous pass against a Scan that finds nothing for anyone.
func TestRLS_ScanHealthyArmedInvoiceNoApprovalFindings(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, entityID, invoiceHealthy, cleanupHealthy := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupHealthy()
	invoiceDrifted, cleanupDrifted := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupDrifted()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	roleID := rcSeedWorkflowRole(t, h, tenantID, "healthy-role", false)
	userID := rcSeedMember(t, h, tenantID, "reviewer", "active")
	rcStaffRole(t, h, tenantID, roleID, userID, 0)
	roleKey := "healthy-role"

	healthyRun := rcSeedRun(t, h, tenantID, invoiceHealthy, versionID, "open")
	rcSeedRunStep(t, h, tenantID, healthyRun, 1, "approval", &roleKey, "pending")

	rcSeedRun(t, h, tenantID, invoiceDrifted, versionID, "open")
	if _, err := h.super.Exec(ctx, `UPDATE invoices SET status = 'draft' WHERE id = $1`, invoiceDrifted); err != nil {
		t.Fatalf("flip drifted sibling to draft: %v", err)
	}

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if !containsFindingFor(got, invoiceDrifted, rcApprovalRunOrphaned) {
		t.Fatalf("Scan findings = %+v, want approval_run_orphaned for the deliberately drifted "+
			"sibling invoice %q — the positive control this healthy-invoice case is measured "+
			"against, proving Scan can return findings in this same tenant", got, invoiceDrifted)
	}
	if n := countForInvoice(got, invoiceHealthy); n != 0 {
		t.Errorf("Scan findings for the healthy invoice %q = %d (%+v), want 0 — validated, open "+
			"run, current step staffed by an active reviewer", invoiceHealthy, n, got)
	}
}
