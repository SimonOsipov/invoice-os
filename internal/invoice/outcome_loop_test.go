// M5-05-06 (task-242): the FINAL subtask of this story -- one DB-backed integration
// spec proving the whole fix -> re-validate -> resubmit loop (AC#4) and the
// accepted dead-end (AC#5) compose correctly on REAL, already-shipped
// production machinery: Store.MarkRejectedTx (M5-05-03/task-239) ->
// Store.Edit's rejected-leg demotion (M5-05-01/task-237) ->
// Store.ApplyValidation (M4-04-05/task-112, unmodified) ->
// Submitter.BatchSubmit (M5-04-07/task-231, unmodified) ->
// Store.MarkAcceptedTx (M5-05-03/task-239). No new production code backs
// this file -- every method it drives already exists and is already
// unit/adversarially tested in its own subtask's file; this file's only job
// is to prove the COMPOSITION, mirroring TestStoreEdit_DemoteThenRevalidateSucceeds's
// shape (edit_test.go) extended through MarkRejectedTx at the front and
// BatchSubmit+MarkAcceptedTx at the back, and
// TestTransition_MultiHopHistoryIntegrityChain's (transition_adversarial_test.go)
// full-chain assertion style.
//
// Unlike every earlier subtask's Mode A RED spec in this story (01/03/04/05),
// this is NOT expected
// to fail today -- the machinery it drives already shipped in 01/03/04/05.
// It is a Mode A "integration-proof variant": authored before this
// subtask's own (comment-only) change lands, but exercising code that is
// already real. A failure here would mean the shipped subtasks do not
// actually compose end to end -- a genuine integration gap, not an
// unimplemented stub.
//
// Reuses the dbTestPools/seedTenant/seedEntity/seedInvoiceAtStatus/
// seedRuleSetVersionID/mustCount/auditCount/auditActor/strPtr harness (same
// package) and newInsertOnlyQueueClient (batch_submit_test.go). No new
// harness is invented.
//
// Run: `make test-rls` (or DEV_DB_PORT=5433 make test-rls in this
// worktree), or directly:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	go test -p 1 -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestOutcomeLoop_RejectFixRevalidateResubmitAccept (M5-05-06/task-242, AC#4
// spine + AC#2's history-chain/actor shape): drives ONE invoice through
// queued -> rejected -> draft -> validated -> queued -> accepted using only
// real, shipped Store/Submitter methods, asserting the observable state
// after every hop plus the invoice_status_history chain's exact ordered
// (from,to,actor) sequence at the end.
func TestOutcomeLoop_RejectFixRevalidateResubmitAccept(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	q := newInsertOnlyQueueClient(t, app)
	submitter := NewSubmitter(store, q)

	tenantID := seedTenant(t, super, "LOOP-1 tenant")
	entityID := seedEntity(t, super, tenantID, "LOOP-1 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	// Step 1: seed at queued -- seedInvoiceAtStatus force-seeds via a raw
	// UPDATE (no Store.Create/Transition call), so invoice_status_history
	// starts completely EMPTY for this invoice; every row asserted below
	// comes from one of the 5 real hops that follow.
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "LOOP-1", StatusQueued)

	// Step 2: MarkRejectedTx (queued -> rejected), reasons stored verbatim.
	reasons := []submission.Reason{{Code: "TIN_MISMATCH", Message: "supplier TIN does not match", Path: "supplier_tin"}}
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, reasons)
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx (queued->rejected): %v (want nil)", err)
	}

	var statusAfterReject, reasonsAfterReject string
	if err := super.QueryRow(ctx,
		`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&statusAfterReject, &reasonsAfterReject); err != nil {
		t.Fatalf("read back after MarkRejectedTx: %v", err)
	}
	if Status(statusAfterReject) != StatusRejected {
		t.Fatalf("status after MarkRejectedTx = %q, want %q", statusAfterReject, StatusRejected)
	}
	var gotReasons []submission.Reason
	if err := json.Unmarshal([]byte(reasonsAfterReject), &gotReasons); err != nil {
		t.Fatalf("unmarshal rejection_reasons: %v (raw=%s)", err, reasonsAfterReject)
	}
	if !reflect.DeepEqual(gotReasons, reasons) {
		t.Fatalf("rejection_reasons after MarkRejectedTx = %+v, want %+v", gotReasons, reasons)
	}

	// Step 3: Store.Edit under an authenticated ctx, a REAL content change ->
	// demotes rejected -> draft AND resets rejection_reasons to '[]' in the
	// SAME tx ([reason-lifecycle]).
	newVAT := "12.34"
	edited, err := store.Edit(c, invID, UpdateInput{VAT: &newVAT})
	if err != nil {
		t.Fatalf("Edit (content change on rejected invoice): %v (want nil)", err)
	}
	if edited.Status != StatusDraft {
		t.Fatalf("Edit returned status = %q, want %q (demoted)", edited.Status, StatusDraft)
	}

	var reasonsAfterEdit, typeofAfterEdit string
	if err := super.QueryRow(ctx,
		`SELECT rejection_reasons::text, jsonb_typeof(rejection_reasons) FROM invoices WHERE id = $1`, invID,
	).Scan(&reasonsAfterEdit, &typeofAfterEdit); err != nil {
		t.Fatalf("read back rejection_reasons after Edit: %v", err)
	}
	if reasonsAfterEdit != "[]" {
		t.Errorf("rejection_reasons after Edit = %q, want %q ([reason-lifecycle]: the demotion clears the stale rejection)", reasonsAfterEdit, "[]")
	}
	if typeofAfterEdit != "array" {
		t.Errorf("jsonb_typeof(rejection_reasons) after Edit = %q, want %q (never JSON null)", typeofAfterEdit, "array")
	}

	// Step 4: Store.ApplyValidation (unmodified since M4-04-05/task-112) -- requires
	// status==draft, which the demotion above just produced; a FRESH
	// fingerprint of the edited row satisfies the content re-check.
	freshFP := contentFingerprint(edited)
	versionID := seedRuleSetVersionID(t, super)
	validated, err := store.ApplyValidation(c, invID, []Violation{}, versionID, freshFP)
	if err != nil {
		t.Fatalf("ApplyValidation (re-validate after demotion): %v (want nil)", err)
	}
	if validated.Status != StatusValidated {
		t.Fatalf("ApplyValidation returned status = %q, want %q", validated.Status, StatusValidated)
	}

	// Step 5: Submitter.BatchSubmit with a FRESH idempotency key (never
	// reused from any prior call in this test) -> enqueues + validated ->
	// queued.
	freshKey := "LOOP-1-fresh-" + uuid.NewString()
	resp, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: freshKey})
	if err != nil {
		t.Fatalf("BatchSubmit (resubmit with fresh key): %v (want nil)", err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].Enqueued || resp.Results[0].Status != string(StatusQueued) {
		t.Fatalf("BatchSubmit result = %+v, want {enqueued:true status:queued}", resp.Results)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Fatalf("river_job rows for invoice after resubmit = %d, want 1 (new outbox row)", n)
	}
	var statusAfterResubmit string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&statusAfterResubmit); err != nil {
		t.Fatalf("read back status after BatchSubmit: %v", err)
	}
	if Status(statusAfterResubmit) != StatusQueued {
		t.Fatalf("status after BatchSubmit = %q, want %q", statusAfterResubmit, StatusQueued)
	}

	// Step 6: MarkAcceptedTx (queued -> accepted), irn/csid/qr_payload
	// written verbatim; rejection_reasons must be STILL '[]' -- not stale
	// from step 2's original rejection ([reason-lifecycle] holds across the
	// WHOLE loop, not just at the clearing hop).
	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "NG-LOOP-1", "CSID-1", "QR-1")
		return err
	})
	if err != nil {
		t.Fatalf("MarkAcceptedTx (queued->accepted): %v (want nil)", err)
	}

	var finalStatus string
	var finalIRN *string
	var finalReasons string
	if err := super.QueryRow(ctx,
		`SELECT status, irn, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&finalStatus, &finalIRN, &finalReasons); err != nil {
		t.Fatalf("read back after MarkAcceptedTx: %v", err)
	}
	if Status(finalStatus) != StatusAccepted {
		t.Fatalf("status after MarkAcceptedTx = %q, want %q", finalStatus, StatusAccepted)
	}
	if finalIRN == nil || *finalIRN != "NG-LOOP-1" {
		t.Errorf("irn after MarkAcceptedTx = %v, want %q", finalIRN, "NG-LOOP-1")
	}
	if finalReasons != "[]" {
		t.Errorf("rejection_reasons after MarkAcceptedTx = %q, want STILL %q ([reason-lifecycle] holds end to end, not stale from the original rejection)", finalReasons, "[]")
	}

	// Step 7 (AC#2): the FULL ordered invoice_status_history chain --
	// queued->rejected (system), rejected->draft (JWT subject),
	// draft->validated (JWT subject, ApplyValidation's own actorFromContext),
	// validated->queued (JWT subject, T07-11's own invariant), queued->accepted
	// (system) -- monotonic, exactly 5 rows (no genesis row: seedInvoiceAtStatus
	// never calls Store.Create).
	rows, err := super.Query(ctx,
		`SELECT from_status, to_status, actor, changed_at FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at ASC`,
		invID,
	)
	if err != nil {
		t.Fatalf("query history chain: %v", err)
	}
	defer rows.Close()

	type histRow struct {
		from, to, actor string
	}
	var got []histRow
	var lastChangedAt any
	for rows.Next() {
		var r histRow
		var from *string
		var changedAt any
		if err := rows.Scan(&from, &r.to, &r.actor, &changedAt); err != nil {
			t.Fatalf("scan history row: %v", err)
		}
		if from != nil {
			r.from = *from
		}
		got = append(got, r)
		lastChangedAt = changedAt
	}
	_ = lastChangedAt
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate history rows: %v", err)
	}

	wantChain := []histRow{
		{string(StatusQueued), string(StatusRejected), "system"},
		{string(StatusRejected), string(StatusDraft), subject},
		{string(StatusDraft), string(StatusValidated), subject},
		{string(StatusValidated), string(StatusQueued), subject},
		{string(StatusQueued), string(StatusAccepted), "system"},
	}
	if len(got) != len(wantChain) {
		t.Fatalf("history chain length = %d, want %d (chain=%+v)", len(got), len(wantChain), got)
	}
	for i, want := range wantChain {
		if got[i] != want {
			t.Errorf("chain[%d] = %+v, want %+v", i, got[i], want)
		}
	}

	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != 5 {
		t.Errorf("audit_log invoice.transitioned rows = %d, want 5 (one per hop)", n)
	}
}

// TestOutcomeLoop_ResubmitWithFreshKeyEnqueuesAgain (M5-05-06/task-242,
// [resubmit-needs-a-fresh-idempotency-key]): a rejected -> draft -> validated
// invoice resubmitted with a FRESH idempotency key enqueues a new outbox row,
// whereas reusing the ORIGINAL key (already consumed by an earlier, real
// submission attempt on this exact invoice) returns duplicate_request and
// writes nothing -- idempotency_keys' dedupe is permanent
// (internal/platform/queue/queue.go's own doc: "Dedupe is authoritative and
// permanent via idempotency_keys' UNIQUE(tenant_id, key)"), so a stale key
// from BEFORE the reject/fix/revalidate cycle is still recognised after it.
func TestOutcomeLoop_ResubmitWithFreshKeyEnqueuesAgain(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	q := newInsertOnlyQueueClient(t, app)
	submitter := NewSubmitter(store, q)

	tenantID := seedTenant(t, super, "LOOP-2 tenant")
	entityID := seedEntity(t, super, tenantID, "LOOP-2 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "LOOP-2", StatusValidated)

	// The ORIGINAL submission: a real BatchSubmit call, validated -> queued,
	// consuming originalKey.
	originalKey := "LOOP-2-original-" + uuid.NewString()
	if _, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: originalKey}); err != nil {
		t.Fatalf("original BatchSubmit (validated->queued): %v (want nil)", err)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Fatalf("river_job rows after original submit = %d, want 1", n)
	}

	// Reject -> fix -> revalidate, exactly the AC#4 loop's front half.
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, []submission.Reason{
			{Code: "APP-ERR-0417", Message: "Supplier TIN not registered", Path: "supplier_tin"},
		})
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx (queued->rejected): %v (want nil)", err)
	}

	newVAT := "5.55"
	edited, err := store.Edit(c, invID, UpdateInput{VAT: &newVAT})
	if err != nil {
		t.Fatalf("Edit (rejected->draft): %v (want nil)", err)
	}
	if edited.Status != StatusDraft {
		t.Fatalf("Edit returned status = %q, want %q", edited.Status, StatusDraft)
	}

	freshFP := contentFingerprint(edited)
	versionID := seedRuleSetVersionID(t, super)
	revalidated, err := store.ApplyValidation(c, invID, []Violation{}, versionID, freshFP)
	if err != nil {
		t.Fatalf("ApplyValidation (draft->validated): %v (want nil)", err)
	}
	if revalidated.Status != StatusValidated {
		t.Fatalf("ApplyValidation returned status = %q, want %q", revalidated.Status, StatusValidated)
	}

	// Branch A: reusing the ORIGINAL key -- BatchSubmit's eligibility check
	// sees status==validated and proceeds to EnqueueTx, which finds
	// originalKey:invID already present from the FIRST submission above and
	// reports it a duplicate. No new job, no transition.
	beforeStaleJobs := countBatchSubmitJobs(t, app, invID)
	respStale, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: originalKey})
	if err != nil {
		t.Fatalf("BatchSubmit (stale original key): %v (want nil, HTTP 200 contract)", err)
	}
	if len(respStale.Results) != 1 || respStale.Results[0].Enqueued || respStale.Results[0].Reason != batchSubmitReasonDuplicate {
		t.Errorf("BatchSubmit (stale original key) result = %+v, want {enqueued:false reason:%q}", respStale.Results, batchSubmitReasonDuplicate)
	}
	// QA finding (M5-05-06/task-242, NOT fixed here -- batch_submit.go is
	// outside this subtask's file list): respStale.Results[0].Status is the
	// STALE literal string(StatusQueued) BatchSubmit's skipped-branch
	// hard-codes (batch_submit.go's own comment: "the invoice's real status
	// is genuinely queued now"), an assumption that holds for the T07-4
	// same-request-duplicate case that comment was written for, but is FALSE
	// here: this skip is reached via a STALE key from an EARLIER, separately
	// committed request, and the invoice's real status is 'validated' (proved
	// by the DB read below), not 'queued'. This story is what newly makes
	// this reachable in production -- before M5-05-01 (task-237) shipped
	// queued->rejected/rejected->draft, a queued invoice had no edge back to
	// validated, so BatchSubmit could never be legitimately re-called on
	// an id whose key was already consumed. Deliberately NOT asserted against
	// respStale.Results[0].Status above (that would either hard-fail this
	// otherwise-green composition proof on a pre-existing M5-04-07 defect, or
	// "work around" it by asserting the wrong value) -- flagged here as a
	// fast-follow candidate instead, per [audit-the-surface-fix-the-scope].
	// The DB ground truth immediately below is what actually matters for this
	// test's own invariant (a duplicate-key skip must not silently transition
	// the invoice) and IS correct.
	if n := countBatchSubmitJobs(t, app, invID); n != beforeStaleJobs {
		t.Errorf("river_job rows after stale-key resubmit = %d, want unchanged %d (no new outbox row)", n, beforeStaleJobs)
	}
	var statusAfterStale string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&statusAfterStale); err != nil {
		t.Fatalf("read back status after stale-key resubmit: %v", err)
	}
	if Status(statusAfterStale) != StatusValidated {
		t.Errorf("status after stale-key resubmit = %q, want unchanged %q (a duplicate-key skip must not transition the invoice)", statusAfterStale, StatusValidated)
	}

	// Branch B: a FRESH key succeeds -- enqueues a NEW outbox row and
	// transitions validated -> queued.
	freshKey := "LOOP-2-fresh-" + uuid.NewString()
	respFresh, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: freshKey})
	if err != nil {
		t.Fatalf("BatchSubmit (fresh key): %v (want nil)", err)
	}
	if len(respFresh.Results) != 1 || !respFresh.Results[0].Enqueued || respFresh.Results[0].Status != string(StatusQueued) {
		t.Fatalf("BatchSubmit (fresh key) result = %+v, want {enqueued:true status:queued}", respFresh.Results)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != beforeStaleJobs+1 {
		t.Errorf("river_job rows after fresh-key resubmit = %d, want %d (+1 new outbox row)", n, beforeStaleJobs+1)
	}
	var statusAfterFresh string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&statusAfterFresh); err != nil {
		t.Fatalf("read back status after fresh-key resubmit: %v", err)
	}
	if Status(statusAfterFresh) != StatusQueued {
		t.Errorf("status after fresh-key resubmit = %q, want %q", statusAfterFresh, StatusQueued)
	}
}

// TestOutcomeLoop_AcceptedInvoiceIsATrueDeadEnd (M5-05-06/task-242, AC#5 in
// loop-context): drives an invoice through the SAME queued -> rejected ->
// draft -> validated -> queued -> accepted arc as
// TestOutcomeLoop_RejectFixRevalidateResubmitAccept, then asserts the
// resulting ACCEPTED invoice -- the one the loop itself produced, not a
// synthetic seeded fixture -- refuses Store.Edit with ErrNotFixable
// (TestStoreEdit_AcceptedStaysNotFixable, edit_test.go, already proves this
// generically) and has zero legal outgoing transitions
// (TestTransition_TerminalStatesHaveNoLegalOutgoingEdges,
// transition_adversarial_test.go, already proves this generically). This
// test does not re-derive either guard in isolation; it glues both onto the
// actual invoice the loop produced, closing the loop end to end.
func TestOutcomeLoop_AcceptedInvoiceIsATrueDeadEnd(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	q := newInsertOnlyQueueClient(t, app)
	submitter := NewSubmitter(store, q)

	tenantID := seedTenant(t, super, "LOOP-3 tenant")
	entityID := seedEntity(t, super, tenantID, "LOOP-3 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "LOOP-3", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, []submission.Reason{{Code: "TIN_MISMATCH", Message: "supplier TIN does not match", Path: "supplier_tin"}})
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx (queued->rejected): %v (want nil)", err)
	}

	newVAT := "3.21"
	edited, err := store.Edit(c, invID, UpdateInput{VAT: &newVAT})
	if err != nil {
		t.Fatalf("Edit (rejected->draft): %v (want nil)", err)
	}

	freshFP := contentFingerprint(edited)
	versionID := seedRuleSetVersionID(t, super)
	if _, err := store.ApplyValidation(c, invID, []Violation{}, versionID, freshFP); err != nil {
		t.Fatalf("ApplyValidation (draft->validated): %v (want nil)", err)
	}

	freshKey := "LOOP-3-fresh-" + uuid.NewString()
	if _, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: freshKey}); err != nil {
		t.Fatalf("BatchSubmit (resubmit with fresh key): %v (want nil)", err)
	}

	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "NG-LOOP-3", "CSID-3", "QR-3")
		return err
	})
	if err != nil {
		t.Fatalf("MarkAcceptedTx (queued->accepted): %v (want nil)", err)
	}

	var statusBefore string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&statusBefore); err != nil {
		t.Fatalf("read back status (precondition): %v", err)
	}
	if Status(statusBefore) != StatusAccepted {
		t.Fatalf("precondition: status = %q, want %q (the loop must have actually reached accepted)", statusBefore, StatusAccepted)
	}

	// AC#5, first half: Store.Edit on the loop-produced accepted invoice ->
	// ErrNotFixable, nothing written.
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	newVAT2 := "9.99"
	if _, err := store.Edit(c, invID, UpdateInput{VAT: &newVAT2}); !errors.Is(err, ErrNotFixable) {
		t.Fatalf("Edit(loop-produced accepted invoice) err = %v, want ErrNotFixable", err)
	}
	var statusAfterRefusedEdit string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&statusAfterRefusedEdit); err != nil {
		t.Fatalf("read back status after refused Edit: %v", err)
	}
	if Status(statusAfterRefusedEdit) != StatusAccepted {
		t.Errorf("status after refused Edit = %q, want unchanged %q", statusAfterRefusedEdit, StatusAccepted)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows after refused Edit = %d, want unchanged %d", n, beforeHistory)
	}

	// AC#5, second half: every non-self transition target from accepted ->
	// ErrIllegalTransition, on the SAME loop-produced invoice.
	illegalCount := 0
	for _, target := range allStatuses {
		if target == StatusAccepted {
			continue // self-edge is not this invariant
		}
		target := target
		t.Run("accepted->"+string(target), func(t *testing.T) {
			if _, err := store.Transition(c, invID, target); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Transition(accepted->%s) err = %v, want ErrIllegalTransition", target, err)
			}
			var status string
			if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
				t.Fatalf("read back status: %v", err)
			}
			if Status(status) != StatusAccepted {
				t.Errorf("status after refused Transition(accepted->%s) = %q, want unchanged %q", target, status, StatusAccepted)
			}
		})
		illegalCount++
	}
	if illegalCount != 6 {
		t.Fatalf("illegal-target attempts = %d, want 6 (7 statuses minus the self-edge)", illegalCount)
	}
}
