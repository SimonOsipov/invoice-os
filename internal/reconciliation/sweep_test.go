// M5-06-05 (task-247): the sweep orchestrator + tenant enumeration (Reconciler.SweepOnce).
// RED against the reconciliation.go/sweep.go stub, whose SweepOnce is a no-op that always
// returns nil and writes nothing — every case below that reads back a river_job or
// audit_log row after a call fails on a real row-count mismatch, never on a compile or
// connection error.
//
// TestRLS_SweepReArmFailureRollsBack pairs its negative "0 survives a rollback" assertion
// with its own positive control (a SEPARATE tenant, afterHeal left nil, same fixture
// shape) — otherwise a stub that heals NOTHING at all would make "0 rows survive" pass
// vacuously regardless of the sentinel hook. See internal/submission/failure_modes_test.go:244
// (TestExactlyOnce_OutboxThreeWayAtomicity) for the precedent this hook/test mirrors.
//
// TestRLS_SweepReaderSeesAllTenants is NOT gated by any reconciliation.go/sweep.go stub —
// it proves only the harness's newly-added reader pool rides the tenant_enumerate policy
// that already shipped in M2-06 (migrations/20260707122459_tenants_rls.sql:40), so it
// passes as soon as the pool is wired, independent of SweepOnce's own implementation. It
// is kept in this file because SweepOnce's enumeration step depends on this exact query
// succeeding under this exact role.
//
// Spec-to-test map (M5-06 story, [M5-06-05] Test Specs table):
//
//	AC-1,3 TestRLS_SweepEnumeratesAllTenantsIsolated
//	AC-2   TestRLS_SweepHealsLostPoll
//	AC-2   TestRLS_SweepFlagsNonHealable
//	AC-4   TestRLS_SweepIdempotentNoStorm
//	AC-5   TestRLS_SweepReArmFailureRollsBack
//	AC-1   TestRLS_SweepReaderSeesAllTenants
package reconciliation

import (
	"context"
	"errors"
	"testing"
	"time"
)

// AC-1,3: SweepOnce enumerates every tenant via the reader role and processes each under
// its own tenant tx — tenant A's lost_poll heals under A, tenant B's submitting_orphan is
// flagged under B, and neither tenant's rows leak into the other's.
func TestRLS_SweepEnumeratesAllTenantsIsolated(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	overdue := time.Now().Add(-1 * time.Hour)

	// Tenant A: a lost_poll — the ONE healable kind.
	tenantA, _, invoiceA, cleanupA := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupA()
	jobA, cleanupJobA := rcSeedJob(t, h, tenantA, invoiceA, rcJobOpts{state: "pending", attempts: 3, nextPollAt: &overdue})
	defer cleanupJobA()
	defer rcCleanupPollJobsFor(h, jobA)
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantA)
	}()

	// Tenant B: a submitting_orphan — flag-only, under a DIFFERENT tenant.
	tenantB, entityB, cleanupB := rcSeedTenant(t, h)
	defer cleanupB()
	invoiceB, cleanupInvoiceB := rcSeedInvoiceIn(t, h, tenantB, entityB, rcInvoiceOpts{})
	defer cleanupInvoiceB()
	jobB, cleanupJobB := rcSeedJob(t, h, tenantB, invoiceB, rcJobOpts{state: "submitting", updatedAt: &overdue})
	defer cleanupJobB()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantB)
	}()

	r := rcReconciler(h)
	if err := r.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobA); n != 1 {
		t.Errorf("tenant A submission_poll river_job rows = %d, want 1 (the lost_poll heal)", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantA); n != 1 {
		t.Errorf("tenant A reconciliation.auto_fixed audit rows = %d, want 1", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.drift_detected'`, tenantA); n != 0 {
		t.Errorf("tenant A reconciliation.drift_detected audit rows = %d, want 0 (its only finding is healable)", n)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.drift_detected'`, tenantB); n != 1 {
		t.Errorf("tenant B reconciliation.drift_detected audit rows = %d, want 1", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantB); n != 0 {
		t.Errorf("tenant B reconciliation.auto_fixed audit rows = %d, want 0 (submitting_orphan is flag-only)", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobB); n != 0 {
		t.Errorf("tenant B submission_poll river_job rows = %d, want 0 (nothing to re-arm)", n)
	}
}

// AC-2: a lost_poll heal inserts exactly one re-armed submission_poll job and exactly one
// auto_fixed audit row, and NEVER writes invoice.status or submission_jobs.state directly
// (AC-3 of M5-06-05 composes AC-3 of M5-06-03 here) — only the eventual real PollWorker run
// would change either.
func TestRLS_SweepHealsLostPoll(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	overdue := time.Now().Add(-1 * time.Hour)
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJob()
	defer rcCleanupPollJobsFor(h, jobID)
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()

	r := rcReconciler(h)
	if err := r.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobID); n != 1 {
		t.Errorf("submission_poll river_job rows = %d, want exactly 1 (the heal)", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantID); n != 1 {
		t.Errorf("reconciliation.auto_fixed audit rows = %d, want exactly 1", n)
	}

	var invoiceStatus string
	if err := h.super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&invoiceStatus); err != nil {
		t.Fatalf("read invoice status: %v", err)
	}
	if invoiceStatus != "submitted" {
		t.Errorf("invoice status after SweepOnce = %q, want unchanged %q — the reconciler must never "+
			"write invoice status directly (AC-3); only PollWorker applies a verdict", invoiceStatus, "submitted")
	}

	var jobState string
	if err := h.super.QueryRow(ctx, `SELECT state FROM submission_jobs WHERE id = $1`, jobID).Scan(&jobState); err != nil {
		t.Fatalf("read job state: %v", err)
	}
	if jobState != "pending" {
		t.Errorf("submission_jobs.state after SweepOnce = %q, want unchanged %q — the reconciler must "+
			"never write job status directly, only re-enqueue the poll", jobState, "pending")
	}
}

// AC-2: a non-healable finding (verdict_not_routed) writes exactly one drift_detected
// audit row and enqueues NO river_job.
func TestRLS_SweepFlagsNonHealable(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupInvoice()
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "accepted"})
	defer cleanupJob()
	defer rcCleanupPollJobsFor(h, jobID)
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()

	r := rcReconciler(h)
	if err := r.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.drift_detected'`, tenantID); n != 1 {
		t.Errorf("reconciliation.drift_detected audit rows = %d, want exactly 1 (verdict_not_routed)", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantID); n != 0 {
		t.Errorf("reconciliation.auto_fixed audit rows = %d, want 0 (verdict_not_routed is flag-only, not healable)", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobID); n != 0 {
		t.Errorf("submission_poll river_job rows for this job = %d, want 0 (nothing to re-arm)", n)
	}
}

// AC-4: two immediate SweepOnce calls against a tenant carrying BOTH a lost_poll and a
// persistent (non-healable) irn_without_accepted must re-arm/auto_fix the lost_poll only
// ONCE total (the live-river-job storm guard, [lost-poll-is-no-live-river-job]) while the
// persistently-flagged invoice is re-audited every sweep ([reconciler-is-stateless]).
func TestRLS_SweepIdempotentNoStorm(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, entityID, invoiceLost, cleanupLost := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupLost()
	overdue := time.Now().Add(-1 * time.Hour)
	jobLost, cleanupJobLost := rcSeedJob(t, h, tenantID, invoiceLost, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJobLost()
	defer rcCleanupPollJobsFor(h, jobLost)

	irn := "NG-STORM-1"
	_, cleanupPersistent := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "submitted", irn: &irn})
	defer cleanupPersistent()

	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key LIKE $2`, tenantID, "reconcile-poll:"+jobLost+":%")
	}()

	r := rcReconciler(h)
	if err := r.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce (1st): %v", err)
	}
	if err := r.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce (2nd): %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobLost); n != 1 {
		t.Errorf("submission_poll river_job rows after two sweeps = %d, want exactly 1 (the storm guard — "+
			"[lost-poll-is-no-live-river-job] must stop the 2nd sweep from re-arming the same job)", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantID); n != 1 {
		t.Errorf("reconciliation.auto_fixed audit rows after two sweeps = %d, want exactly 1", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.drift_detected'`, tenantID); n != 2 {
		t.Errorf("reconciliation.drift_detected audit rows after two sweeps = %d, want exactly 2 (the "+
			"persistently-flagged irn_without_accepted invoice must be re-audited every sweep — "+
			"[reconciler-is-stateless])", n)
	}
}

// AC-5: a post-heal sentinel error (the test-only afterHeal hook, invoked AFTER a genuine
// ReArmPoll/recordAutoFixAudit already succeeded inside the tenant tx) rolls back the WHOLE
// tenant tx — the re-arm's river_job insert and its auto_fixed audit row both vanish
// together. Paired with its own positive control (afterHeal nil, a DIFFERENT tenant, the
// identical fixture shape) so a stub SweepOnce that heals NOTHING at all cannot make the
// "0 rows survive" half pass vacuously. Mirrors TestExactlyOnce_OutboxThreeWayAtomicity
// (internal/submission/failure_modes_test.go:244-266).
func TestRLS_SweepReArmFailureRollsBack(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	overdue := time.Now().Add(-1 * time.Hour)

	// Positive control: the SAME fixture shape, afterHeal left nil — this must heal
	// normally. Without this half, a stub SweepOnce that never heals anything would make
	// the rollback assertions below trivially (and vacuously) true.
	tenantOK, _, invoiceOK, cleanupOK := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupOK()
	jobOK, cleanupJobOK := rcSeedJob(t, h, tenantOK, invoiceOK, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJobOK()
	defer rcCleanupPollJobsFor(h, jobOK)
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantOK)
	}()

	rOK := rcReconciler(h)
	if err := rOK.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce (positive control, afterHeal nil): %v", err)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobOK); n != 1 {
		t.Fatalf("positive control: submission_poll river_job rows = %d, want 1 — the fixture/harness "+
			"must prove a heal happens before the rollback case below can mean anything", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantOK); n != 1 {
		t.Fatalf("positive control: reconciliation.auto_fixed audit rows = %d, want 1", n)
	}

	// The rollback case: an identical fixture under a DIFFERENT tenant, afterHeal
	// returning a sentinel error immediately after a genuine ReArmPoll+recordAutoFixAudit
	// have already run inside the tx.
	tenantRB, _, invoiceRB, cleanupRB := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupRB()
	jobRB, cleanupJobRB := rcSeedJob(t, h, tenantRB, invoiceRB, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJobRB()
	defer rcCleanupPollJobsFor(h, jobRB)
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantRB)
	}()

	errSentinel := errors.New("reconciliation: intentional post-heal rollback")
	rRB := rcReconciler(h)
	rRB.afterHeal = func(tenantID string) error {
		if tenantID == tenantRB {
			return errSentinel
		}
		return nil
	}
	if err := rRB.SweepOnce(ctx); err != nil {
		t.Logf("SweepOnce (with afterHeal sentinel) returned: %v (a per-tenant failure need not fail "+
			"the whole sweep)", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobRB); n != 0 {
		t.Errorf("tenant RB submission_poll river_job rows after a post-heal rollback = %d, want 0 — "+
			"the whole tenant tx (including the re-arm insert) must roll back with the sentinel error", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantRB); n != 0 {
		t.Errorf("tenant RB reconciliation.auto_fixed audit rows after a post-heal rollback = %d, want 0 — "+
			"the audit write shares the same tx as the re-arm, so it must roll back too", n)
	}
}

// AC-1: the reader pool (invoice_tenant_reader, DATABASE_READER_URL) enumerates every
// tenant with NO app.current_tenant GUC set — the tenant_enumerate policy
// (migrations/20260707122459_tenants_rls.sql:40) ORs in every row for this role alone.
// NOT gated by any reconciliation.go/sweep.go stub (see file header) — proves the
// harness's own new pool, which SweepOnce's enumeration step depends on.
func TestRLS_SweepReaderSeesAllTenants(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantX, _, cleanupX := rcSeedTenant(t, h)
	defer cleanupX()
	tenantY, _, cleanupY := rcSeedTenant(t, h)
	defer cleanupY()

	rows, err := h.reader.Query(ctx, `SELECT id::text FROM tenants WHERE id = ANY($1)`, []string{tenantX, tenantY})
	if err != nil {
		t.Fatalf("reader-pool enumeration query: %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan tenant id: %v", err)
		}
		seen[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if !seen[tenantX] {
		t.Errorf("reader-pool enumeration missing tenant X %q — the tenant_enumerate policy "+
			"(FOR SELECT TO invoice_tenant_reader USING(true)) must return every tenant with no GUC set", tenantX)
	}
	if !seen[tenantY] {
		t.Errorf("reader-pool enumeration missing tenant Y %q", tenantY)
	}
}
