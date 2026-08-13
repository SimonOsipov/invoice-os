// APPR-08-04 (task-501): the transmit gate inside Submitter.BatchSubmit — the batch
// door: the read, the guard arm and the canonical-id keying. The permissive cases are
// controls that pin what the guard must NOT touch.
//
// batch_submit_test.go / batch_submit_adversarial_test.go keep their M5-04-07 scope and
// are not edited. Fixtures come from apply_validation_arming_test.go and
// transition_gate_test.go, the harness from store_test.go / batch_submit_test.go.
//
// Run: DATABASE_URL=… DATABASE_SUPERUSER_URL=… DATABASE_READER_URL=… \
// go test -p 1 -count=1 ./internal/invoice/...
package invoice

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- harness ----------------------------------------------------------------

// gateSubmitter builds a Submitter over a Store carrying the flag. Submitter reads the
// flag off its own *Store (batch_submit.go), so this is the whole wiring.
func gateSubmitter(t *testing.T, pool *pgxpool.Pool, enforced bool) *Submitter {
	t.Helper()
	return NewSubmitter(NewStore(pool, WithApprovalsEnforced(enforced)), newInsertOnlyQueueClient(t, pool))
}

// upperIDOrFatal uppercases a fixture uuid and fails when that is a no-op — a uuid whose
// hex happens to carry no lowercase letter would make its spec silently vacuous.
func upperIDOrFatal(t *testing.T, id string) string {
	t.Helper()
	upper := strings.ToUpper(id)
	if upper == id {
		t.Fatalf("fixture id %q has no lowercase hex digits — the case this test exists for is not exercised", id)
	}
	return upper
}

// lastIndexMentioning is firstIndexMentioning's mirror (transition_gate_adversarial_test.go):
// the index of the LAST recorded statement containing substr, so "after every row lock" is
// assertable over a multi-id batch.
func (r *sqlRecorder) lastIndexMentioning(t *testing.T, substr string) int {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.sql) - 1; i >= 0; i-- {
		if strings.Contains(r.sql[i], substr) {
			return i
		}
	}
	t.Fatalf("no recorded statement mentions %q; recorded: %v", substr, r.sql)
	return -1
}

// eligibilityLockSQL is the substring of BatchSubmit's per-distinct-id lock statement that
// survives Stage 3 widening its select list from `status` to `id, status`.
const eligibilityLockSQL = "FROM invoices WHERE id = $1 FOR UPDATE"

// wantItem asserts one result position whole, so a wrong reason and a wrong status are
// never reported as the same failure.
func wantItem(t *testing.T, got BatchSubmitResultItem, pos int, wantID string, wantEnqueued bool, wantStatus Status, wantReason string) {
	t.Helper()
	if got.InvoiceID != wantID {
		t.Errorf("results[%d].invoice_id = %q, want %q", pos, got.InvoiceID, wantID)
	}
	if got.Enqueued != wantEnqueued {
		t.Errorf("results[%d].enqueued = %v, want %v", pos, got.Enqueued, wantEnqueued)
	}
	if got.Status != string(wantStatus) {
		t.Errorf("results[%d].status = %q, want %q", pos, got.Status, wantStatus)
	}
	if got.Reason != wantReason {
		t.Errorf("results[%d].reason = %q, want %q", pos, got.Reason, wantReason)
	}
}

// seedInactivePolicyTenant is seedOneStepActivePolicyTenant's sealed-but-INACTIVE twin —
// the shape that drives TransmitClearTx's one-statement short-circuit.
func seedInactivePolicyTenant(t *testing.T, super *pgxpool.Pool, label string) (tenantID, entityID, versionID string) {
	t.Helper()
	tenantID = seedTenant(t, super, label+" tenant")
	entityID = seedEntity(t, super, tenantID, label+" entity")
	policyID := seedApprovalPolicyFor(t, super, tenantID, label+" policy")
	versionID = seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	seedApprovalStepFor(t, super, tenantID, versionID, approvalStepSpecFor{
		Ord: 0, Kind: "approval", WorkflowRoleKey: strPtr("finance-lead"),
	})
	sealApprovalPolicyVersionFor(t, super, versionID)
	return tenantID, entityID, versionID
}

// --- the two links no behavioural spec can reach ----------------------------

// TestBatchSubmitReasonTokens_PinTheWireLiterals: SKIP_REASON_LABELS
// (frontend/app/src/lib/invoices.ts) keys on these exact strings and cannot import a Go
// const. Renaming one here would leave every Go spec green — they follow the const — while
// the SPA silently fell through to the raw token.
func TestBatchSubmitReasonTokens_PinTheWireLiterals(t *testing.T) {
	for _, tt := range []struct{ got, want string }{
		{batchSubmitReasonNotValidated, "not_validated"},
		{batchSubmitReasonDuplicate, "duplicate_request"},
		{batchSubmitReasonAwaitingApproval, "awaiting_approval"},
	} {
		if tt.got != tt.want {
			t.Errorf("reason token = %q, want %q — SKIP_REASON_LABELS keys on this literal", tt.got, tt.want)
		}
	}
}

// TestBatchSubmit_SubmitterGetsTheFlaggedStoreInMain: this subtask changes no wiring only
// because cmd/invoice/main.go builds ONE Store, flagged, and hands that same value to
// NewSubmitter. A second, unflagged invoice.NewStore for the submitter would leave every
// spec in this package green and the batch door ungated in production.
func TestBatchSubmit_SubmitterGetsTheFlaggedStoreInMain(t *testing.T) {
	src, err := os.ReadFile("../../cmd/invoice/main.go")
	if err != nil {
		t.Fatalf("read cmd/invoice/main.go: %v", err)
	}
	main := string(src)

	if n := strings.Count(main, "invoice.NewStore("); n != 1 {
		t.Errorf("cmd/invoice/main.go calls invoice.NewStore %d time(s), want exactly 1 — a second store is a second, unflagged answer to APPROVALS_ENFORCED", n)
	}
	const flagged = "store := invoice.NewStore(pool, invoice.WithApprovalsEnforced(enforced))"
	if !strings.Contains(main, flagged) {
		t.Errorf("cmd/invoice/main.go does not build the flagged store as\n\t%s", flagged)
	}
	const wired = "invoice.NewSubmitter(store, "
	if !strings.Contains(main, wired) {
		t.Errorf("cmd/invoice/main.go does not hand the flagged store to the submitter as\n\t%s", wired)
	}
}

// --- AC #2/#4: an open run skips the item, it does not fail the batch --------

// TestBatchSubmit_AwaitingApprovalSkip: flag ON, active policy, an open run -> one skipped
// item carrying the invoice's REAL status, no queue row, no transition.
func TestBatchSubmit_AwaitingApprovalSkip(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-GATED", StatusValidated)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID) // open

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit over a gated invoice: %v (want nil — a gated invoice is a SKIP, not an error)", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, false, StatusValidated, batchSubmitReasonAwaitingApproval)

	if n := countBatchSubmitJobs(t, app, fx.invID); n != 0 {
		t.Errorf("river_job rows for the gated invoice = %d, want 0", n)
	}
	if s := statusOf(t, super, fx.invID); s != StatusValidated {
		t.Errorf("stored status = %q, want unchanged %q", s, StatusValidated)
	}
}

// TestBatchSubmit_MixedBatchSkipsOnlyTheGatedOnes: the gate is per invoice, and the
// results keep request order. An approved run passes, an open run skips
// awaiting_approval, a draft skips not_validated — one batch, three outcomes.
func TestBatchSubmit_MixedBatchSkipsOnlyTheGatedOnes(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-MIX", StatusValidated)
	approvedRun := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, approvedRun, "approved", "fixture")

	gatedID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-MIX-B", StatusValidated)
	seedApprovalRunFor(t, super, fx.tenantID, gatedID, fx.versionID) // open

	draftID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-MIX-C", StatusDraft)

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID, gatedID, draftID}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit over a mixed batch: %v (want nil)", err)
	}
	if len(res.Results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, true, StatusQueued, "")
	wantItem(t, res.Results[1], 1, gatedID, false, StatusValidated, batchSubmitReasonAwaitingApproval)
	wantItem(t, res.Results[2], 2, draftID, false, StatusDraft, batchSubmitReasonNotValidated)

	if n := countBatchSubmitJobs(t, app, fx.invID); n != 1 {
		t.Errorf("river_job rows for the approved invoice = %d, want 1", n)
	}
	if n := countBatchSubmitJobs(t, app, gatedID); n != 0 {
		t.Errorf("river_job rows for the gated invoice = %d, want 0", n)
	}
	if s := statusOf(t, super, gatedID); s != StatusValidated {
		t.Errorf("gated invoice status = %q, want unchanged %q", s, StatusValidated)
	}
}

// TestBatchSubmit_NotValidatedStillWinsOverAwaitingApproval (AC #5): a draft with an open
// run reports not_validated. Control — an implementation that put the approval arm ahead
// of the status arm fails here.
func TestBatchSubmit_NotValidatedStillWinsOverAwaitingApproval(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-DRAFTGATED", StatusDraft)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID) // open

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit over a gated draft: %v (want nil)", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, false, StatusDraft, batchSubmitReasonNotValidated)
}

// TestBatchSubmit_DuplicateIdsStillResolveOnceUnderTheGate (AC #3): the T07-4 ordering trap
// survives the gate. One approved invoice listed three times enqueues once and reports
// duplicate_request twice — never not_validated, never awaiting_approval.
func TestBatchSubmit_DuplicateIdsStillResolveOnceUnderTheGate(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-DUP", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID, fx.invID, fx.invID}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit over one id three times: %v (want nil)", err)
	}
	if len(res.Results) != 3 {
		t.Fatalf("len(results) = %d, want 3 (one per requested position)", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, true, StatusQueued, "")
	wantItem(t, res.Results[1], 1, fx.invID, false, StatusQueued, batchSubmitReasonDuplicate)
	wantItem(t, res.Results[2], 2, fx.invID, false, StatusQueued, batchSubmitReasonDuplicate)

	if n := countBatchSubmitJobs(t, app, fx.invID); n != 1 {
		t.Errorf("river_job rows = %d, want exactly 1", n)
	}
}

// --- AC #2: one read, after the locks, and only under the flag ---------------

// TestBatchSubmit_ApprovalReadRunsAfterTheRowLocks: the approval read follows EVERY
// per-distinct-id lock, per the invoices -> approval_* lock order.
func TestBatchSubmit_ApprovalReadRunsAfterTheRowLocks(t *testing.T) {
	super, _ := dbTestPools(t)
	tracedApp, rec := tracedAppPool(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-ORDER", StatusValidated)
	runA := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runA, "approved", "fixture")
	secondID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-ORDER-B", StatusValidated)
	runB := seedApprovalRunFor(t, super, fx.tenantID, secondID, fx.versionID)
	closeApprovalRunFor(t, super, runB, "approved", "fixture")

	submitter := gateSubmitter(t, tracedApp, true)

	rec.reset()
	if _, err := submitter.BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID, secondID}, IdempotencyKey: uuid.NewString(),
	}); err != nil {
		t.Fatalf("BatchSubmit over two approved invoices: %v (want nil)", err)
	}

	if got := rec.mentioning("approval_"); len(got) == 0 {
		t.Fatalf("BatchSubmit issued no statement mentioning approval_ under the flag — the gate read never runs")
	}
	lastLockAt := rec.lastIndexMentioning(t, eligibilityLockSQL)
	approvalAt := rec.firstIndexMentioning(t, "approval_")
	if approvalAt <= lastLockAt {
		t.Errorf("the first approval read is statement %d and the last row lock is statement %d — the gate read a fact before every row was locked", approvalAt, lastLockAt)
	}
}

// TestBatchSubmit_ApprovalReadIsConstantInBatchSize: ONE TransmitClearTx call over the
// distinct id set, so 50 invoices cost the same two statements one does. A per-id call
// would record 100.
func TestBatchSubmit_ApprovalReadIsConstantInBatchSize(t *testing.T) {
	super, _ := dbTestPools(t)
	tracedApp, rec := tracedAppPool(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-BATCH50", StatusValidated)
	ids := []string{fx.invID}
	for i := 1; i < 50; i++ {
		ids = append(ids, seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID,
			"APPR-08-04-BATCH50-"+uuid.NewString(), StatusValidated))
	}

	submitter := gateSubmitter(t, tracedApp, true)

	rec.reset()
	if _, err := submitter.BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: ids, IdempotencyKey: uuid.NewString(),
	}); err != nil {
		t.Fatalf("BatchSubmit over 50 invoices: %v (want nil)", err)
	}

	if got := rec.mentioning("approval_"); len(got) != 2 {
		t.Errorf("BatchSubmit over %d invoices issued %d approval_ statement(s), want exactly 2: %v", len(ids), len(got), got)
	}
}

// TestBatchSubmit_FlagOffEnqueuesAGatedInvoice (AC #2, the silent half): with the flag off
// the batch door is byte-for-byte the door it was. Control.
func TestBatchSubmit_FlagOffEnqueuesAGatedInvoice(t *testing.T) {
	super, _ := dbTestPools(t)
	tracedApp, rec := tracedAppPool(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-FLAGOFF", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID) // open

	submitter := gateSubmitter(t, tracedApp, false)

	rec.reset()
	res, err := submitter.BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit with the flag off: %v (want nil)", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, true, StatusQueued, "")

	if s := statusOf(t, super, fx.invID); s != StatusQueued {
		t.Errorf("stored status = %q, want %q", s, StatusQueued)
	}
	if got := rec.mentioning("approval_runs"); len(got) != 0 {
		t.Errorf("flag-off BatchSubmit issued %d statement(s) mentioning approval_runs: %v", len(got), got)
	}
	if got := rec.mentioning("approval_policy_versions"); len(got) != 0 {
		t.Errorf("flag-off BatchSubmit issued %d statement(s) mentioning approval_policy_versions: %v", len(got), got)
	}
	if s := runStateOf(t, super, runID); s != "open" {
		t.Errorf("run state = %q, want unchanged %q", s, "open")
	}
}

// TestBatchSubmit_NoActivePolicyEnqueuesUnderTheFlag: a tenant that published no policy
// pays ONE statement and its invoices still transmit, open run or not.
func TestBatchSubmit_NoActivePolicyEnqueuesUnderTheFlag(t *testing.T) {
	super, _ := dbTestPools(t)
	tracedApp, rec := tracedAppPool(t)

	tenantID, entityID, versionID := seedInactivePolicyTenant(t, super, "APPR-08-04-INACTIVE")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-08-04-INACTIVE-A", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, invID, versionID) // open, but the policy is not active
	ctx := gateCtx(tenantID)

	submitter := gateSubmitter(t, tracedApp, true)

	rec.reset()
	res, err := submitter.BatchSubmit(ctx, BatchSubmitInput{
		InvoiceIDs: []string{invID}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit on a tenant with no active policy: %v (want nil)", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, invID, true, StatusQueued, "")

	if got := rec.mentioning("approval_"); len(got) != 1 {
		t.Errorf("no-active-policy BatchSubmit issued %d approval_ statement(s), want exactly 1 (the short-circuit): %v", len(got), got)
	}
}

// TestBatchSubmit_FlagOffWithNilClearMapDoesNotSkipEverything: the mutant-killer. With the
// flag off TransmitClearTx never runs, so the clear map is nil and a nil-map read yields
// false — dropping the approvalsEnforced conjunct from the guard would classify EVERY
// invoice in EVERY batch as awaiting_approval. TransmitClear is clear-shaped so absence
// fails closed (approval/gate.go), which is right when the gate ran and catastrophic when
// it did not.
func TestBatchSubmit_FlagOffWithNilClearMapDoesNotSkipEverything(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "APPR-08-04-NILMAP tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-08-04-NILMAP entity")
	ctx := gateCtx(tenantID)

	ids := []string{
		seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-08-04-NILMAP-A", StatusValidated),
		seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-08-04-NILMAP-B", StatusValidated),
		seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-08-04-NILMAP-C", StatusValidated),
	}

	res, err := gateSubmitter(t, app, false).BatchSubmit(ctx, BatchSubmitInput{
		InvoiceIDs: ids, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit with the flag off and no policy at all: %v (want nil)", err)
	}
	if len(res.Results) != len(ids) {
		t.Fatalf("len(results) = %d, want %d", len(res.Results), len(ids))
	}
	for i, id := range ids {
		wantItem(t, res.Results[i], i, id, true, StatusQueued, "")
		if n := countBatchSubmitJobs(t, app, id); n != 1 {
			t.Errorf("river_job rows for %s = %d, want 1", id, n)
		}
	}
}

// --- AC #8/#9: the canonical id, and its refusing twin -----------------------

// TestBatchSubmit_UppercaseIdOnAnApprovedInvoiceEnqueues (AC #8): TransmitClearTx's
// row-returning branch keys its map on Postgres's canonical lowercase, so a non-canonical
// id is ABSENT and reads false — a wrong refusal of an APPROVED invoice. The ACTIVE policy
// is load-bearing: the no-active-policy short-circuit maps every REQUESTED id verbatim, so
// the same input reads true and this spec would be blind.
func TestBatchSubmit_UppercaseIdOnAnApprovedInvoiceEnqueues(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-UPPER-OK", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	upper := upperIDOrFatal(t, fx.invID)

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{upper}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit(UPPERCASE id) on an approved invoice: %v (want nil)", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, true, StatusQueued, "")

	if s := statusOf(t, super, fx.invID); s != StatusQueued {
		t.Errorf("stored status = %q, want %q", s, StatusQueued)
	}
	if n := countBatchSubmitJobs(t, app, fx.invID); n != 1 {
		t.Errorf("river_job rows carrying the CANONICAL id = %d, want 1 — the job args must not echo the caller's spelling", n)
	}
	if n := countBatchSubmitJobs(t, app, upper); n != 0 {
		t.Errorf("river_job rows carrying the UPPERCASE id = %d, want 0", n)
	}
}

// TestBatchSubmit_UppercaseIdOnAGatedInvoiceStillRefuses: the permissive twin's control.
// Normalising the id must not become a bypass — an open run still refuses.
func TestBatchSubmit_UppercaseIdOnAGatedInvoiceStillRefuses(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-UPPER-GATED", StatusValidated)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID) // open

	upper := upperIDOrFatal(t, fx.invID)

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{upper}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit(UPPERCASE id) on a gated invoice: %v (want nil — a gated invoice is a SKIP)", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, false, StatusValidated, batchSubmitReasonAwaitingApproval)

	if s := statusOf(t, super, fx.invID); s != StatusValidated {
		t.Errorf("stored status = %q, want unchanged %q", s, StatusValidated)
	}
	if n := countBatchSubmitJobs(t, app, fx.invID); n != 0 {
		t.Errorf("river_job rows = %d, want 0", n)
	}
}

// TestBatchSubmit_SameIdInTwoSpellingsResolvesOnce (AC #9): eligibility resolves once per
// DISTINCT id, and two spellings of one uuid are one id. Both positions must derive the
// SAME idempotency key, so position 2 hits EnqueueTx's (tenant_id, key) dedupe.
func TestBatchSubmit_SameIdInTwoSpellingsResolvesOnce(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-TWOSPELL", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	upper := upperIDOrFatal(t, fx.invID)

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID, upper}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit over one uuid in two spellings: %v (want nil)", err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2 (one per requested position)", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, true, StatusQueued, "")
	wantItem(t, res.Results[1], 1, fx.invID, false, StatusQueued, batchSubmitReasonDuplicate)

	if n := countBatchSubmitJobs(t, app, fx.invID); n != 1 {
		t.Errorf("river_job rows carrying the CANONICAL id = %d, want exactly 1", n)
	}
	if n := countBatchSubmitJobs(t, app, upper); n != 0 {
		t.Errorf("river_job rows carrying the UPPERCASE id = %d, want 0 — two spellings must derive ONE key", n)
	}
}
