// Package reconciliation is the M5-06 "who watches the watcher" sweep: it compares
// invoices.status against submission_jobs.state (the latest cycle per invoice), re-arms
// lost polls through the existing PollWorker path, and flags every other drift as an
// append-only reconciliation.* audit record. See the M5-06 Obsidian story (§ System
// Design) for the full drift-signature table and the auto-heal storm-guard proof.
//
// M5-06-02..04 (this file): Scan is the pure detection query, ReArmPoll the enqueue-only
// auto-heal, recordDriftAudit/recordAutoFixAudit the audit writes. None of the three
// writes anything outside its own tx, and none writes invoice/job status directly — the
// orchestrator (M5-06-05) composes them per tenant inside db.WithinTenantTx, and the
// existing PollWorker is the only writer of a re-armed poll's eventual verdict.
package reconciliation

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// DriftKind names one of the 10 drift signatures Scan detects (M5-06 System Design table,
// plus the two approval signatures from APPR-06). Nothing binds these constants to the kind
// literals in scanQuery — adding a kind means editing both.
type DriftKind string

const (
	LostPoll           DriftKind = "lost_poll"
	PendingTooManyHops DriftKind = "pending_too_many_hops"
	PendingTooLong     DriftKind = "pending_too_long"
	SubmittingOrphan   DriftKind = "submitting_orphan"
	QueuedNeverSent    DriftKind = "queued_never_sent"
	IRNWithoutAccepted DriftKind = "irn_without_accepted"
	AcceptedWithoutIRN DriftKind = "accepted_without_irn"
	VerdictNotRouted   DriftKind = "verdict_not_routed"

	ApprovalRunOrphaned      DriftKind = "approval_run_orphaned"
	ApprovalBlockedUnstaffed DriftKind = "approval_blocked_unstaffed"
)

// Finding is one row Scan returns: an invoice (and, where relevant, the submission_jobs
// row) whose state diverges from expectations, tagged with the drift kind and whether the
// reconciler auto-heals it (Healable is true only for LostPoll — [drift-action]).
type Finding struct {
	InvoiceID       string
	InvoiceNumber   string
	SubmissionJobID *string
	Kind            DriftKind
	Healable        bool
}

// Thresholds parameterizes Scan's overdue/ceiling/age cutoffs. Defaults live in the
// M5-06-06 Sweeper config (RECONCILE_POLL_OVERDUE_GRACE, RECONCILE_HOP_CEILING,
// RECONCILE_MAX_PENDING_AGE); Scan itself takes them as plain arguments.
type Thresholds struct {
	Grace         time.Duration
	MaxPendingAge time.Duration
	HopCeiling    int
}

// scanQuery implements the 10 drift signatures (L1/H1/H2/O1/Q1/C1/C2/C3, M5-06 System
// Design table, plus APPR-06's two approval arms) as one UNION ALL over the `latest`-cycle CTE
// (DISTINCT ON (invoice_id) … ORDER BY created_at DESC — [drift-scan-uses-latest-cycle]),
// so a resubmission's new job row is always the cycle evaluated. The two "live river_job"
// sub-predicates are inlined per branch rather than shared via a second CTE: L1 keys off
// the job id, O1/Q1 off the invoice id, and river_job carries neither tenant_id nor RLS —
// each EXISTS is scoped by the caller's own tenant-unique uuid, safe inside tx
// (db.WithinTenantTx already holds the tenant lock via app.current_tenant).
//
// $1 = the grace cutoff (now - RECONCILE_POLL_OVERDUE_GRACE), shared by L1's next_poll_at
// check and O1's updated_at check (the story's predicate table uses the same :grace for
// both). $2 = HopCeiling (int). $3 = the max-pending-age cutoff (now - RECONCILE_MAX_-
// PENDING_AGE). Cutoffs are computed once in Go rather than as `now() - $n::interval` SQL
// arithmetic, so every branch of the UNION sees an identical "now" without re-evaluating
// now() per row.
const scanQuery = `
WITH latest AS (
    SELECT DISTINCT ON (invoice_id) *
    FROM submission_jobs
    ORDER BY invoice_id, created_at DESC
)
SELECT i.id, i.invoice_number, j.id, 'lost_poll', true
FROM invoices i
JOIN latest j ON j.invoice_id = i.id
WHERE i.status = 'submitted'
  AND j.state = 'pending'
  AND j.next_poll_at < $1
  AND j.attempts <= $2
  AND NOT EXISTS (
      SELECT 1 FROM river_job rj
       WHERE rj.kind = 'submission_poll'
         AND rj.args @> jsonb_build_object('submission_job_id', j.id::text)
         AND rj.state NOT IN ('completed', 'cancelled', 'discarded')
  )

UNION ALL

SELECT i.id, i.invoice_number, j.id, 'pending_too_many_hops', false
FROM invoices i
JOIN latest j ON j.invoice_id = i.id
WHERE j.state = 'pending' AND j.attempts > $2

UNION ALL

SELECT i.id, i.invoice_number, j.id, 'pending_too_long', false
FROM invoices i
JOIN latest j ON j.invoice_id = i.id
WHERE j.state = 'pending' AND j.created_at < $3

UNION ALL

SELECT i.id, i.invoice_number, j.id, 'submitting_orphan', false
FROM invoices i
JOIN latest j ON j.invoice_id = i.id
WHERE j.state = 'submitting'
  AND j.updated_at < $1
  AND NOT EXISTS (
      SELECT 1 FROM river_job rj
       WHERE rj.kind = 'submission_submit'
         AND rj.args @> jsonb_build_object('invoice_id', i.id::text)
         AND rj.state NOT IN ('completed', 'cancelled', 'discarded')
  )

UNION ALL

SELECT i.id, i.invoice_number, j.id, 'queued_never_sent', false
FROM invoices i
LEFT JOIN latest j ON j.invoice_id = i.id
WHERE i.status = 'queued'
  AND (j.id IS NULL OR j.state = 'queued')
  AND NOT EXISTS (
      SELECT 1 FROM river_job rj
       WHERE rj.kind = 'submission_submit'
         AND rj.args @> jsonb_build_object('invoice_id', i.id::text)
         AND rj.state NOT IN ('completed', 'cancelled', 'discarded')
  )

UNION ALL

SELECT i.id, i.invoice_number, j.id, 'irn_without_accepted', false
FROM invoices i
LEFT JOIN latest j ON j.invoice_id = i.id
WHERE i.irn IS NOT NULL AND i.status <> 'accepted'

UNION ALL

SELECT i.id, i.invoice_number, j.id, 'accepted_without_irn', false
FROM invoices i
LEFT JOIN latest j ON j.invoice_id = i.id
WHERE i.status = 'accepted' AND i.irn IS NULL

UNION ALL

SELECT i.id, i.invoice_number, j.id, 'verdict_not_routed', false
FROM invoices i
JOIN latest j ON j.invoice_id = i.id
WHERE i.status = 'submitted' AND j.state IN ('accepted', 'rejected')

UNION ALL

-- The explicit tenant_id predicates on these two arms are not redundancy with RLS: Scan also
-- runs on a superuser pool with no app.current_tenant set (TestSeedTripsNoReconciliationDrift),
-- where the unscoped form cross-joins tenants and drops real drift. A watchdog must not fail
-- silent.
SELECT i.id, i.invoice_number, NULL::uuid, 'approval_run_orphaned', false
FROM invoices i
JOIN approval_runs r ON r.tenant_id = i.tenant_id AND r.invoice_id = i.id
WHERE r.state = 'open'
  AND i.status <> 'validated'

UNION ALL

-- LATERAL … LIMIT 1 picks the one current step, and NOT EXISTS (never LEFT JOIN … IS NULL)
-- collapses the holders: a run with several pending steps or several holders must still
-- produce exactly one finding.
SELECT i.id, i.invoice_number, NULL::uuid, 'approval_blocked_unstaffed', false
FROM invoices i
JOIN approval_runs r ON r.tenant_id = i.tenant_id AND r.invoice_id = i.id AND r.state = 'open'
JOIN LATERAL (
    SELECT s.workflow_role_key
    FROM approval_run_steps s
    WHERE s.tenant_id = r.tenant_id
      AND s.run_id = r.id
      AND s.kind = 'approval'
      AND s.state = 'pending'
    ORDER BY s.ord
    LIMIT 1
) cur ON true
WHERE NOT EXISTS (
    SELECT 1
    FROM workflow_roles wr
    JOIN workflow_role_members wrm
      ON wrm.tenant_id = wr.tenant_id AND wrm.workflow_role_id = wr.id
    JOIN memberships m
      ON m.tenant_id = wrm.tenant_id AND m.user_id = wrm.user_id
    WHERE wr.tenant_id = r.tenant_id
      AND wr.key = cur.workflow_role_key
      AND wr.deleted_at IS NULL
      AND m.status = 'active'
      AND m.role IN ('admin', 'reviewer')
)
`

// Scan walks the tenant-scoped `invoices` ⋈ latest `submission_jobs` ⋈ river_job
// live-job EXISTS clauses inside tx and returns one Finding per matched drift signature
// (L1/H1/H2/O1/Q1/C1/C2/C3, M5-06 System Design table). Pure read — no writes.
func Scan(ctx context.Context, tx pgx.Tx, th Thresholds) ([]Finding, error) {
	now := time.Now()
	graceCutoff := now.Add(-th.Grace)
	maxAgeCutoff := now.Add(-th.MaxPendingAge)

	rows, err := tx.Query(ctx, scanQuery, graceCutoff, th.HopCeiling, maxAgeCutoff)
	if err != nil {
		return nil, fmt.Errorf("reconciliation: scan: %w", err)
	}
	defer rows.Close()

	var findings []Finding
	for rows.Next() {
		var (
			f     Finding
			kind  string
			jobID *string
		)
		if err := rows.Scan(&f.InvoiceID, &f.InvoiceNumber, &jobID, &kind, &f.Healable); err != nil {
			return nil, fmt.Errorf("reconciliation: scan row: %w", err)
		}
		f.SubmissionJobID = jobID
		f.Kind = DriftKind(kind)
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reconciliation: scan rows: %w", err)
	}
	return findings, nil
}

// ReArmPoll re-enqueues the existing poll job for a LostPoll finding via the house
// EnqueueTx outbox — key "reconcile-poll:<submission_job_id>:<attempts>",
// submission.PollArgs{..., Sequence: attempts} — and never touches invoice/job status
// directly (AC-3). skipped is true when the idempotency_keys guard already holds this key
// ([rearm-key-and-sequence]). opts is nil (default InsertOpts): PollArgs.InsertOpts()
// pins MaxAttempts=8 the same way the worker's own hops do.
//
// f.SubmissionJobID is assumed non-nil: the only caller path is a Healable (LostPoll)
// finding, and Scan's lost_poll branch always joins a submission_jobs row (L1 requires
// j.state='pending', which cannot hold for a non-existent job).
func ReArmPoll(ctx context.Context, tx pgx.Tx, q *queue.Client, tenantID string, f Finding, attempts int) (skipped bool, err error) {
	jobID := *f.SubmissionJobID
	args := submission.PollArgs{
		TenantID:        tenantID,
		InvoiceID:       f.InvoiceID,
		SubmissionJobID: jobID,
		Sequence:        attempts,
	}
	key := fmt.Sprintf("reconcile-poll:%s:%d", jobID, attempts)
	return q.EnqueueTx(ctx, tx, tenantID, key, args, nil)
}

// recordDriftAudit writes one reconciliation.drift_detected audit_log row for a flagged
// (non-healable) finding, actor "system", summary-only payload
// ({invoice_id, invoice_number, submission_job_id?, drift_kind} — [audit-drift-kind-payload]).
func recordDriftAudit(ctx context.Context, tx pgx.Tx, f Finding) error {
	return audit.Record(ctx, tx, "system", "reconciliation.drift_detected", driftPayload(f))
}

// recordAutoFixAudit writes one reconciliation.auto_fixed audit_log row for a healed
// LostPoll finding, actor "system", summary-only payload incl. action:"repoll_reenqueued".
func recordAutoFixAudit(ctx context.Context, tx pgx.Tx, f Finding) error {
	payload := driftPayload(f)
	payload["action"] = "repoll_reenqueued"
	return audit.Record(ctx, tx, "system", "reconciliation.auto_fixed", payload)
}

// driftPayload builds the shared summary-only body — ids + drift_kind, never a wire body
// (mirrors internal/submission/verdict_audit.go's recordVerdictAudit). invoice_number is
// written unconditionally, mirroring the NOT NULL column. submission_job_id is included
// only when the finding carries one (Q1/C1/C2 can fire with no job row at all), left
// absent from the map entirely rather than written as an empty string.
func driftPayload(f Finding) map[string]any {
	payload := map[string]any{
		"invoice_id":     f.InvoiceID,
		"invoice_number": f.InvoiceNumber,
		"drift_kind":     string(f.Kind),
	}
	if f.SubmissionJobID != nil {
		payload["submission_job_id"] = *f.SubmissionJobID
	}
	return payload
}
