// M5-06-03 (task-245): the auto-heal re-arm (ReArmPoll). RED against the reconciliation.go
// stub, which always returns (false, nil) and inserts nothing — every assertion below that
// reads back a river_job or idempotency_keys row fails on a real row-count/value mismatch,
// never on a compile or connection error.
//
// Spec-to-test map (M5-06 story, [M5-06-03] Test Specs table):
//
//	AC-1 TestRLS_ReArmInsertsOnePollJob
//	AC-2 TestRLS_ReArmSecondCallDeduped
//	AC-3 TestRLS_ReArmKeyNamespace
package reconciliation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// rcCleanupPollJobsFor deletes every submission_poll river_job row tagged with jobID in its
// args — precise enough to be safe under sequential test execution (each test seeds its own
// fresh submission_jobs uuid), unlike a blanket `DELETE ... WHERE kind = 'submission_poll'`.
func rcCleanupPollJobsFor(h *harness, jobID string) {
	_, _ = h.super.Exec(context.Background(),
		`DELETE FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobID)
}

// AC-1: a lost_poll re-arm inserts exactly one submission_poll river_job whose
// args.submission_job_id matches and args.sequence == attempts, in a non-terminal state.
func TestRLS_ReArmInsertsOnePollJob(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	overdue := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 5, nextPollAt: &overdue})
	defer cleanupJob()
	defer rcCleanupPollJobsFor(h, jobID)

	f := Finding{InvoiceID: invoiceID, SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}

	var skipped bool
	err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		var e error
		skipped, e = ReArmPoll(ctx, tx, h.queue, tenantID, f, 5)
		return e
	})
	if err != nil {
		t.Fatalf("ReArmPoll: %v", err)
	}
	if skipped {
		t.Error("ReArmPoll skipped = true on the first call, want false")
	}

	n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobID)
	if n != 1 {
		t.Fatalf("submission_poll river_job rows for this job = %d, want exactly 1", n)
	}

	var gotSubmissionJobID string
	var gotSequence int
	var gotState string
	if err := h.super.QueryRow(ctx,
		`SELECT state::text, args->>'submission_job_id', (args->>'sequence')::int
		   FROM river_job
		  WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobID,
	).Scan(&gotState, &gotSubmissionJobID, &gotSequence); err != nil {
		t.Fatalf("read the re-armed river_job row: %v", err)
	}
	if gotSubmissionJobID != jobID {
		t.Errorf("re-armed job's args.submission_job_id = %q, want %q", gotSubmissionJobID, jobID)
	}
	if gotSequence != 5 {
		t.Errorf("re-armed job's args.sequence = %d, want 5 (== attempts)", gotSequence)
	}
	for _, terminal := range []string{"completed", "cancelled", "discarded"} {
		if gotState == terminal {
			t.Errorf("re-armed job's state = %q, want non-terminal (available/scheduled/pending/running/retryable)", gotState)
		}
	}
}

// AC-2: a second ReArmPoll call for the SAME (job, attempts) returns skipped=true and
// inserts NO second job — the idempotency_keys(tenant_id, key) UNIQUE guard.
func TestRLS_ReArmSecondCallDeduped(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	overdue := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 3, nextPollAt: &overdue})
	defer cleanupJob()
	defer rcCleanupPollJobsFor(h, jobID)

	f := Finding{InvoiceID: invoiceID, SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}

	rearm := func() (skipped bool) {
		t.Helper()
		if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
			var e error
			skipped, e = ReArmPoll(ctx, tx, h.queue, tenantID, f, 3)
			return e
		}); err != nil {
			t.Fatalf("ReArmPoll: %v", err)
		}
		return skipped
	}

	if first := rearm(); first {
		t.Fatal("ReArmPoll skipped = true on the FIRST call, want false — the positive control " +
			"this dedupe case is measured against")
	}
	if second := rearm(); !second {
		t.Error("ReArmPoll skipped = false on the SECOND call with the same (job, attempts), " +
			"want true (idempotency_keys dedupe)")
	}

	n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobID)
	if n != 1 {
		t.Errorf("submission_poll river_job rows after two ReArmPoll calls = %d, want exactly 1 (no duplicate)", n)
	}
}

// AC-3: the re-arm records idempotency_keys under "reconcile-poll:<job>:<attempts>" and
// NEVER under the worker chain's own "poll:<job>:<seq>" namespace.
func TestRLS_ReArmKeyNamespace(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	overdue := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 5, nextPollAt: &overdue})
	defer cleanupJob()
	defer rcCleanupPollJobsFor(h, jobID)
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key LIKE $2`, tenantID, "%"+jobID+"%")
	}()

	f := Finding{InvoiceID: invoiceID, SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}
	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		_, e := ReArmPoll(ctx, tx, h.queue, tenantID, f, 5)
		return e
	}); err != nil {
		t.Fatalf("ReArmPoll: %v", err)
	}

	wantKey := fmt.Sprintf("reconcile-poll:%s:5", jobID)
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, tenantID, wantKey,
	); n != 1 {
		t.Errorf("idempotency_keys rows for key %q = %d, want 1", wantKey, n)
	}

	collisionKey := fmt.Sprintf("poll:%s:6", jobID)
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, tenantID, collisionKey,
	); n != 0 {
		t.Errorf("idempotency_keys rows for the worker-chain key %q = %d, want 0 (the re-arm must "+
			"never write into the poll:<job>:<seq> namespace)", collisionKey, n)
	}
}
