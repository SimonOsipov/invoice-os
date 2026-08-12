// QA Mode B adversarial coverage for M5-06-02..04, added on top of the RED-authored AC
// tests in scan_test.go / rearm_test.go / audit_test.go. These cases target properties the
// architect's Test Specs table names but does not fully exercise at the primitive level:
// river_job's lack of tenant_id/RLS as a cross-tenant leak/suppression vector, the
// job-specificity of the live-poll suppression clause, the re-arm→re-scan storm-guard closing
// the loop end-to-end, the nil-SubmissionJobID payload path, and reconciliation.* audit rows
// sharing audit_log's append-only guarantee.
//
// APPR-06-09 (task-485) adds four RED cases for the two approval-drift kinds, pinning the
// edges the scan_test.go AC cases don't reach: only-signable-holders dedup, current-step
// (lowest-ord pending) selection, dead/missing/NULL role-key handling, and closed-run
// suppression. All four fail today because the shipped scanQuery has no arm for either kind:
//
//	AC-4   TestRLS_ScanApprovalBlockedUnstaffedCountsOnlySignableHolders
//	AC-4   TestRLS_ScanApprovalBlockedUnstaffedUsesCurrentStepOnly
//	AC-4   TestRLS_ScanApprovalBlockedUnstaffedRoleKeyEdges
//	AC-3,4 TestRLS_ScanApprovalFindingsIgnoreClosedRuns
package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// asPgError unwraps err to a *pgconn.PgError, mirroring internal/audit/audit_test.go's
// assertSQLState helper (kept local since this package's helper set otherwise has none).
func asPgError(err error) (*pgconn.PgError, bool) {
	var pgErr *pgconn.PgError
	ok := errors.As(err, &pgErr)
	return pgErr, ok
}

// CRITICAL: river_job carries no tenant_id and no RLS (System Design, "The two-role
// transaction model"). This proves the no-RLS join can neither (a) let an unrelated
// river_job row seeded independently of tenant A suppress tenant A's own lost_poll, nor
// (b) leak tenant A's invoice into a Scan run under tenant B's context — RLS on
// invoices/submission_jobs, not river_job, is what actually scopes the outer join.
func TestRLS_ScanCrossTenantRiverJobIsolation(t *testing.T) {
	h := requireHarness(t)

	tenantA, _, invoiceA, cleanupA := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupA()
	overdue := time.Now().Add(-1 * time.Hour)
	jobA, cleanupJobA := rcSeedJob(t, h, tenantA, invoiceA, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJobA()

	tenantB, _, cleanupB := rcSeedTenant(t, h)
	defer cleanupB()
	// A live submission_poll river_job that exists independently of tenant A's job — its
	// args name a DIFFERENT job id than jobA. river_job has no tenant_id, so the only thing
	// that can stop this row from (wrongly) suppressing tenant A's finding is the EXISTS
	// clause actually matching on args.submission_job_id, not merely "any live poll exists".
	otherJobID := uuid.NewString()
	_, cleanupRiverJob := rcSeedRiverJob(t, h, "submission_poll", "scheduled", map[string]any{
		"tenant_id": tenantB, "invoice_id": uuid.NewString(), "submission_job_id": otherJobID, "sequence": 1,
	})
	defer cleanupRiverJob()

	gotA, err := rcScan(t, h, tenantA, rcThresholds)
	if err != nil {
		t.Fatalf("Scan(tenantA): %v", err)
	}
	wantA := Finding{InvoiceID: invoiceA, SubmissionJobID: &jobA, Kind: LostPoll, Healable: true}
	if len(gotA) != 1 || !findingEqual(gotA[0], wantA) {
		t.Errorf("Scan(tenantA) findings = %+v, want exactly [%+v] — an unrelated river_job "+
			"row (seeded for a different job id) must not suppress tenant A's own lost_poll", gotA, wantA)
	}

	gotB, err := rcScan(t, h, tenantB, rcThresholds)
	if err != nil {
		t.Fatalf("Scan(tenantB): %v", err)
	}
	for _, f := range gotB {
		if f.InvoiceID == invoiceA {
			t.Errorf("Scan(tenantB) findings = %+v, want NOTHING about tenant A's invoice %q "+
				"(RLS on invoices/submission_jobs must scope the outer join even though "+
				"river_job itself carries no tenant_id)", gotB, invoiceA)
		}
	}
	if len(gotB) != 0 {
		t.Errorf("Scan(tenantB) findings = %+v, want empty (tenant B has no invoices seeded)", gotB)
	}
}

// AC-3's live-job suppression is job-SPECIFIC, not tenant-wide: a live submission_poll
// river_job for job X must not suppress a lost_poll on a DIFFERENT job Y under the SAME
// tenant. The scan_test.go suppression case only ever seeds one job; this proves the EXISTS
// clause actually keys off args.submission_job_id rather than "does this tenant have any
// live poll at all".
func TestRLS_ScanLiveRiverJobSuppressionIsJobSpecific(t *testing.T) {
	h := requireHarness(t)

	tenantID, entityID, invoiceX, cleanupX := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupX()
	overdue := time.Now().Add(-1 * time.Hour)
	jobX, cleanupJobX := rcSeedJob(t, h, tenantID, invoiceX, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJobX()

	invoiceY, cleanupY := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "submitted"})
	defer cleanupY()
	jobY, cleanupJobY := rcSeedJob(t, h, tenantID, invoiceY, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJobY()

	// A live poll for job Y only — job X has none.
	_, cleanupRiverJob := rcSeedRiverJob(t, h, "submission_poll", "scheduled", map[string]any{
		"tenant_id": tenantID, "invoice_id": invoiceY, "submission_job_id": jobY, "sequence": 1,
	})
	defer cleanupRiverJob()

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	wantX := Finding{InvoiceID: invoiceX, SubmissionJobID: &jobX, Kind: LostPoll, Healable: true}
	foundX := false
	for _, f := range got {
		if f.InvoiceID == invoiceX {
			foundX = true
			if !findingEqual(f, wantX) {
				t.Errorf("finding for job X = %+v, want %+v", f, wantX)
			}
		}
		if f.InvoiceID == invoiceY && f.Kind == LostPoll {
			t.Errorf("Scan findings = %+v, want NO lost_poll for job Y (it has its own live poll)", got)
		}
	}
	if !foundX {
		t.Errorf("Scan findings = %+v, want job X's lost_poll present — job Y's live "+
			"river_job must not suppress a DIFFERENT job's finding under the same tenant", got)
	}
}

// The make-or-break storm-guard property proven end-to-end at the scan+rearm seam
// ([lost-poll-is-no-live-river-job]): after ReArmPoll inserts a live submission_poll
// river_job for the lost job, the VERY NEXT Scan in the same tenant no longer returns
// lost_poll for it — the re-arm's own row now satisfies L1's NOT EXISTS(live poll) clause.
// This is what actually stops a real sweep loop from stacking duplicate re-arms; scan_test.go
// and rearm_test.go each prove their half in isolation but never chain them.
func TestRLS_ReArmThenRescanClosesTheLoop(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	overdue := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 4, nextPollAt: &overdue})
	defer cleanupJob()
	defer rcCleanupPollJobsFor(h, jobID)
	// ReArmPoll below also writes an idempotency_keys row (rearm_test.go's own
	// TestRLS_ReArmInsertsOnePollJob/TestRLS_ReArmSecondCallDeduped leave the same row
	// behind — TestRLS_ReArmKeyNamespace is the only sibling case that cleans it up).
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key LIKE $2`, tenantID, "reconcile-poll:"+jobID+":%")
	}()

	before, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (before ReArmPoll): %v", err)
	}
	want := Finding{InvoiceID: invoiceID, SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}
	if len(before) != 1 || !findingEqual(before[0], want) {
		t.Fatalf("Scan (before ReArmPoll) findings = %+v, want exactly [%+v] — the positive "+
			"control this closes-the-loop case is measured against", before, want)
	}

	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		skipped, e := ReArmPoll(ctx, tx, h.queue, tenantID, before[0], 4)
		if e != nil {
			return e
		}
		if skipped {
			t.Error("ReArmPoll skipped = true on the first call, want false")
		}
		return nil
	}); err != nil {
		t.Fatalf("ReArmPoll: %v", err)
	}

	after, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan (after ReArmPoll): %v", err)
	}
	if containsKind(after, LostPoll) {
		t.Errorf("Scan (after ReArmPoll) findings = %+v, want no lost_poll — the re-arm's own "+
			"submission_poll river_job must now satisfy L1's live-poll EXISTS clause "+
			"[lost-poll-is-no-live-river-job], closing the storm-guard loop", after)
	}
}

// Q1 (queued_never_sent) is the one detected kind that carries NO submission_jobs row at
// all, so Finding.SubmissionJobID is nil (already asserted structurally by
// TestRLS_ScanQueuedNeverSent's findingEqual check). This proves recordDriftAudit handles
// that nil cleanly: no panic, and the payload OMITS submission_job_id entirely (never a JSON
// null or empty string) — [audit-drift-kind-payload]'s "submission_job_id?" is optional, not
// nullable.
func TestRLS_DriftAuditHandlesNilSubmissionJobID(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	invoiceID := uuid.NewString()
	f := Finding{InvoiceID: invoiceID, SubmissionJobID: nil, Kind: QueuedNeverSent, Healable: false}

	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		return recordDriftAudit(ctx, tx, f)
	}); err != nil {
		t.Fatalf("recordDriftAudit (nil SubmissionJobID): %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()

	var payload []byte
	if err := h.super.QueryRow(ctx,
		`SELECT payload FROM audit_log WHERE tenant_id = $1 ORDER BY id DESC LIMIT 1`, tenantID,
	).Scan(&payload); err != nil {
		t.Fatalf("read the drift audit row: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, present := body["submission_job_id"]; present {
		t.Errorf("payload = %+v, want the submission_job_id key ABSENT (never null/empty) "+
			"when the finding carries no job row", body)
	}
	if got, _ := body["drift_kind"].(string); got != string(QueuedNeverSent) {
		t.Errorf("payload drift_kind = %v, want %q", body["drift_kind"], QueuedNeverSent)
	}
	if got, _ := body["invoice_id"].(string); got != invoiceID {
		t.Errorf("payload invoice_id = %v, want %q", body["invoice_id"], invoiceID)
	}
}

// audit_log's append-only guarantee (M2-10, exhaustively proven generically in
// internal/audit/audit_test.go) is schema-level, not per-caller — this confirms a
// reconciliation.* row specifically inherits it: neither the app role (42501, missing
// UPDATE/DELETE grant) nor the table owner/migrator (23001, the immutability trigger) can
// mutate or delete a row this package wrote via recordDriftAudit.
func TestRLS_ReconciliationAuditImmutable(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	invoiceID := uuid.NewString()
	jobID := uuid.NewString()
	f := Finding{InvoiceID: invoiceID, SubmissionJobID: &jobID, Kind: SubmittingOrphan, Healable: false}

	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		return recordDriftAudit(ctx, tx, f)
	}); err != nil {
		t.Fatalf("recordDriftAudit: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()

	// The app role: rejected by the missing grant (42501) before RLS is even consulted.
	_, appErr := h.app.Exec(ctx, `UPDATE audit_log SET actor = 'tampered' WHERE tenant_id = $1`, tenantID)
	if pgErr, ok := asPgError(appErr); !ok || pgErr.Code != "42501" {
		t.Errorf("app role UPDATE err = %v, want SQLSTATE 42501", appErr)
	}

	// The table owner/migrator: the app-role grant gap doesn't apply to it, so the
	// append-only trigger (23001) must be what blocks it.
	updErr := db.WithinTenantTx(ctx, h.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE audit_log SET actor = 'tampered' WHERE tenant_id = $1`, tenantID)
		return e
	})
	if pgErr, ok := asPgError(updErr); !ok || pgErr.Code != "23001" {
		t.Errorf("owner UPDATE err = %v, want SQLSTATE 23001", updErr)
	}

	delErr := db.WithinTenantTx(ctx, h.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
		return e
	})
	if pgErr, ok := asPgError(delErr); !ok || pgErr.Code != "23001" {
		t.Errorf("owner DELETE err = %v, want SQLSTATE 23001", delErr)
	}

	n := mustCount(t, h.super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenantID)
	if n != 1 {
		t.Errorf("audit_log rows after blocked mutations = %d, want 1 (unchanged)", n)
	}
}

// AC-4: five runs in one tenant whose current pending approval step names a role held only
// by, respectively: a suspended reviewer; an active preparer (wrong role — preparers cannot
// sign); an invited (not yet active) admin; TWO suspended reviewers; an active admin. The
// first four must flag, the fifth must not, and the two-suspended-holders case must yield
// exactly ONE row — the NOT EXISTS shape (never a LEFT JOIN) must not fan out per holder.
func TestRLS_ScanApprovalBlockedUnstaffedCountsOnlySignableHolders(t *testing.T) {
	h := requireHarness(t)

	tenantID, entityID, invoiceA, cleanupA := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupA()
	invoiceB, cleanupB := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupB()
	invoiceC, cleanupC := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupC()
	invoiceD, cleanupD := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupD()
	invoiceE, cleanupE := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupE()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	// A: role held only by a suspended reviewer.
	roleA := rcSeedWorkflowRole(t, h, tenantID, "role-a-suspended-reviewer", false)
	userA := rcSeedMember(t, h, tenantID, "reviewer", "suspended")
	rcStaffRole(t, h, tenantID, roleA, userA, 0)
	keyA := "role-a-suspended-reviewer"
	runA := rcSeedRun(t, h, tenantID, invoiceA, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runA, 1, "approval", &keyA, "pending")

	// B: role held only by an active preparer.
	roleB := rcSeedWorkflowRole(t, h, tenantID, "role-b-active-preparer", false)
	userB := rcSeedMember(t, h, tenantID, "preparer", "active")
	rcStaffRole(t, h, tenantID, roleB, userB, 0)
	keyB := "role-b-active-preparer"
	runB := rcSeedRun(t, h, tenantID, invoiceB, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runB, 1, "approval", &keyB, "pending")

	// C: role held only by an invited (not yet active) admin.
	roleC := rcSeedWorkflowRole(t, h, tenantID, "role-c-invited-admin", false)
	userC := rcSeedMember(t, h, tenantID, "admin", "invited")
	rcStaffRole(t, h, tenantID, roleC, userC, 0)
	keyC := "role-c-invited-admin"
	runC := rcSeedRun(t, h, tenantID, invoiceC, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runC, 1, "approval", &keyC, "pending")

	// D: role held by TWO suspended reviewers — must dedup to exactly one row.
	roleD := rcSeedWorkflowRole(t, h, tenantID, "role-d-two-suspended", false)
	userD1 := rcSeedMember(t, h, tenantID, "reviewer", "suspended")
	rcStaffRole(t, h, tenantID, roleD, userD1, 0)
	userD2 := rcSeedMember(t, h, tenantID, "reviewer", "suspended")
	rcStaffRole(t, h, tenantID, roleD, userD2, 1)
	keyD := "role-d-two-suspended"
	runD := rcSeedRun(t, h, tenantID, invoiceD, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runD, 1, "approval", &keyD, "pending")

	// E: role held by an active admin — signable, must NOT flag.
	roleE := rcSeedWorkflowRole(t, h, tenantID, "role-e-active-admin", false)
	userE := rcSeedMember(t, h, tenantID, "admin", "active")
	rcStaffRole(t, h, tenantID, roleE, userE, 0)
	keyE := "role-e-active-admin"
	runE := rcSeedRun(t, h, tenantID, invoiceE, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runE, 1, "approval", &keyE, "pending")

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	for _, tc := range []struct {
		name      string
		invoiceID string
		want      int
	}{
		{"suspended reviewer only", invoiceA, 1},
		{"active preparer only", invoiceB, 1},
		{"invited admin only", invoiceC, 1},
		{"two suspended reviewers", invoiceD, 1},
		{"active admin", invoiceE, 0},
	} {
		n := countForInvoice(got, tc.invoiceID)
		if n != tc.want {
			t.Errorf("%s: Scan findings for invoice %q = %d, want %d (%+v)", tc.name, tc.invoiceID, n, tc.want, got)
			continue
		}
		if tc.want == 1 && !containsFindingFor(got, tc.invoiceID, rcApprovalBlockedUnstaffed) {
			t.Errorf("%s: Scan findings for invoice %q = %+v, want approval_blocked_unstaffed", tc.name, tc.invoiceID, got)
		}
	}
}

// AC-4: only the lowest-ord PENDING approval step is "current". Run A: ord-1 SATISFIED
// (unstaffed role key), ord-2 pending + staffed -> current is ord-2, staffed -> NOT flagged.
// Run B: ord-1 pending + unstaffed, ord-2 pending + staffed -> current is ord-1, unstaffed ->
// flagged. Both runs share one tenant, so the negative half is measured beside a live
// positive.
func TestRLS_ScanApprovalBlockedUnstaffedUsesCurrentStepOnly(t *testing.T) {
	h := requireHarness(t)

	tenantID, entityID, invoiceA, cleanupA := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupA()
	invoiceB, cleanupB := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupB()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	staffedRole := rcSeedWorkflowRole(t, h, tenantID, "staffed-role", false)
	staffedUser := rcSeedMember(t, h, tenantID, "admin", "active")
	rcStaffRole(t, h, tenantID, staffedRole, staffedUser, 0)
	staffedKey := "staffed-role"

	rcSeedWorkflowRole(t, h, tenantID, "unstaffed-role", false) // zero holders, on purpose
	unstaffedKey := "unstaffed-role"

	// Run A: ord-1 satisfied (unstaffed role, but not the current step), ord-2 pending+staffed.
	runA := rcSeedRun(t, h, tenantID, invoiceA, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runA, 1, "approval", &unstaffedKey, "satisfied")
	rcSeedRunStep(t, h, tenantID, runA, 2, "approval", &staffedKey, "pending")

	// Run B: ord-1 pending+unstaffed (the current step), ord-2 pending+staffed.
	runB := rcSeedRun(t, h, tenantID, invoiceB, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runB, 1, "approval", &unstaffedKey, "pending")
	rcSeedRunStep(t, h, tenantID, runB, 2, "approval", &staffedKey, "pending")

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if n := countForInvoice(got, invoiceA); n != 0 {
		t.Errorf("Scan findings for invoice A (satisfied ord-1 unstaffed, current pending ord-2 "+
			"staffed) = %d (%+v), want 0 — only the lowest-ord PENDING step is current", n, got)
	}
	if n := countForInvoice(got, invoiceB); n != 1 {
		t.Fatalf("Scan findings for invoice B (current pending ord-1 unstaffed) = %d (%+v), want exactly 1", n, got)
	}
	if !containsFindingFor(got, invoiceB, rcApprovalBlockedUnstaffed) {
		t.Errorf("Scan findings for invoice B = %+v, want approval_blocked_unstaffed", got)
	}
}

// AC-4: a signable person behind a DEAD role is still unsignable, and a NULL role key is not
// silently healthy. Three runs whose current pending approval step carries, respectively: a
// workflow_role_key naming no workflow_roles row; a key naming a soft-deleted role whose lone
// holder IS an active admin; a NULL workflow_role_key. All three must flag.
func TestRLS_ScanApprovalBlockedUnstaffedRoleKeyEdges(t *testing.T) {
	h := requireHarness(t)

	tenantID, entityID, invoiceA, cleanupA := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupA()
	invoiceB, cleanupB := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupB()
	invoiceC, cleanupC := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupC()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	// A: workflow_role_key names no workflow_roles row at all.
	ghostKey := "ghost-role-does-not-exist"
	runA := rcSeedRun(t, h, tenantID, invoiceA, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runA, 1, "approval", &ghostKey, "pending")

	// B: role soft-deleted, even though its lone holder is an active admin.
	deletedRoleID := rcSeedWorkflowRole(t, h, tenantID, "deleted-role", true)
	activeAdmin := rcSeedMember(t, h, tenantID, "admin", "active")
	rcStaffRole(t, h, tenantID, deletedRoleID, activeAdmin, 0)
	deletedKey := "deleted-role"
	runB := rcSeedRun(t, h, tenantID, invoiceB, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runB, 1, "approval", &deletedKey, "pending")

	// C: NULL workflow_role_key.
	runC := rcSeedRun(t, h, tenantID, invoiceC, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runC, 1, "approval", nil, "pending")

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	for _, tc := range []struct {
		name      string
		invoiceID string
	}{
		{"role key names no workflow_roles row", invoiceA},
		{"role soft-deleted despite an active admin holder", invoiceB},
		{"NULL workflow_role_key", invoiceC},
	} {
		if n := countForInvoice(got, tc.invoiceID); n != 1 {
			t.Errorf("%s: Scan findings for invoice %q = %d (%+v), want exactly 1", tc.name, tc.invoiceID, n, got)
			continue
		}
		if !containsFindingFor(got, tc.invoiceID, rcApprovalBlockedUnstaffed) {
			t.Errorf("%s: Scan findings for invoice %q = %+v, want approval_blocked_unstaffed", tc.name, tc.invoiceID, got)
		}
	}
}

// AC-3, AC-4 (D37 control): a transmitted/closed run must not flag forever, and an unstaffed
// step behind a CLOSED run must not flag either. One tenant: a submitted invoice whose run is
// approved; a validated invoice whose run is cancelled with an unstaffed pending step; a
// validated invoice whose open run holds only a notify/skipped step; a validated invoice
// whose open run has no steps at all -> none of the four produce either new kind. Positive
// control: a fifth, deliberately orphaned invoice in the same tenant DOES flag.
func TestRLS_ScanApprovalFindingsIgnoreClosedRuns(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, entityID, invoiceApproved, cleanupApproved := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupApproved()
	invoiceCancelled, cleanupCancelled := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupCancelled()
	invoiceNotify, cleanupNotify := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupNotify()
	invoiceNoSteps, cleanupNoSteps := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupNoSteps()
	invoiceOrphaned, cleanupOrphaned := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupOrphaned()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	// D37: a transmitted invoice with an APPROVED (closed) run must not flag forever.
	rcSeedRun(t, h, tenantID, invoiceApproved, versionID, "approved")

	// A CANCELLED run with an unstaffed pending step still must not flag — the arm requires
	// run.state = 'open'.
	cancelledRun := rcSeedRun(t, h, tenantID, invoiceCancelled, versionID, "cancelled")
	unstaffedKey := "unstaffed-role-on-cancelled-run"
	rcSeedRunStep(t, h, tenantID, cancelledRun, 1, "approval", &unstaffedKey, "pending")

	// An OPEN run with only a notify/skipped step — no pending 'approval' step at all.
	notifyRun := rcSeedRun(t, h, tenantID, invoiceNotify, versionID, "open")
	rcSeedRunStep(t, h, tenantID, notifyRun, 1, "notify", nil, "skipped")

	// An OPEN run with NO steps at all.
	rcSeedRun(t, h, tenantID, invoiceNoSteps, versionID, "open")

	// Positive control: a deliberately orphaned invoice (open run, invoice not validated).
	rcSeedRun(t, h, tenantID, invoiceOrphaned, versionID, "open")
	if _, err := h.super.Exec(ctx, `UPDATE invoices SET status = 'draft' WHERE id = $1`, invoiceOrphaned); err != nil {
		t.Fatalf("flip control invoice to draft: %v", err)
	}

	got, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if !containsFindingFor(got, invoiceOrphaned, rcApprovalRunOrphaned) {
		t.Fatalf("Scan findings = %+v, want approval_run_orphaned for the deliberately-orphaned "+
			"control invoice %q — the positive control this closed-runs case is measured against", got, invoiceOrphaned)
	}

	for _, tc := range []struct {
		name      string
		invoiceID string
	}{
		{"approved (closed) run", invoiceApproved},
		{"cancelled run with an unstaffed pending step", invoiceCancelled},
		{"open run with only a notify/skipped step", invoiceNotify},
		{"open run with no steps at all", invoiceNoSteps},
	} {
		if n := countForInvoice(got, tc.invoiceID); n != 0 {
			t.Errorf("%s: Scan findings for invoice %q = %d (%+v), want 0", tc.name, tc.invoiceID, n, got)
		}
	}
}
