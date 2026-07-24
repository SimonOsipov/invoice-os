// Package reconciliation is the M5-06 "who watches the watcher" sweep: it compares
// invoices.status against submission_jobs.state (the latest cycle per invoice), re-arms
// lost polls through the existing PollWorker path, and flags every other drift as an
// append-only reconciliation.* audit record. See the M5-06 Obsidian story (§ System
// Design) for the full drift-signature table and the auto-heal storm-guard proof.
//
// QA MODE A (RALPH M5-06): this file holds STUB signatures only, matching the real
// db.WithinTenantTx / queue.Client.EnqueueTx / audit.Record shapes so the RED test suite
// in this package compiles. Every function is a TODO for the executor — no detection SQL,
// no re-arm, no audit writes live here yet.
package reconciliation

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// DriftKind names one of the 8 drift signatures Scan detects (M5-06 System Design table).
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
)

// Finding is one row Scan returns: an invoice (and, where relevant, the submission_jobs
// row) whose state diverges from expectations, tagged with the drift kind and whether the
// reconciler auto-heals it (Healable is true only for LostPoll — [drift-action]).
type Finding struct {
	InvoiceID       string
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

// Scan walks the tenant-scoped `invoices` ⋈ latest `submission_jobs` ⋈ river_job
// live-job EXISTS clauses inside tx and returns one Finding per matched drift signature
// (L1/H1/H2/O1/Q1/C1/C2/C3, M5-06 System Design table). Pure read — no writes.
//
// TODO(M5-06-02): implemented by executor.
func Scan(ctx context.Context, tx pgx.Tx, th Thresholds) ([]Finding, error) {
	return nil, nil
}

// ReArmPoll re-enqueues the existing poll job for a LostPoll finding via the house
// EnqueueTx outbox — key "reconcile-poll:<submission_job_id>:<attempts>",
// submission.PollArgs{..., Sequence: attempts} — and never touches invoice/job status
// directly (AC-3). skipped is true when the idempotency_keys guard already holds this key.
//
// TODO(M5-06-03): implemented by executor.
func ReArmPoll(ctx context.Context, tx pgx.Tx, q *queue.Client, tenantID string, f Finding, attempts int) (skipped bool, err error) {
	return false, nil
}

// recordDriftAudit writes one reconciliation.drift_detected audit_log row for a flagged
// (non-healable) finding, actor "system", summary-only payload.
//
// TODO(M5-06-04): implemented by executor.
func recordDriftAudit(ctx context.Context, tx pgx.Tx, f Finding) error {
	return nil
}

// recordAutoFixAudit writes one reconciliation.auto_fixed audit_log row for a healed
// LostPoll finding, actor "system", summary-only payload incl. action:"repoll_reenqueued".
//
// TODO(M5-06-04): implemented by executor.
func recordAutoFixAudit(ctx context.Context, tx pgx.Tx, f Finding) error {
	return nil
}
