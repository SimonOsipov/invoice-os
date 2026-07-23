// M5-04-07 (task-231): QA adversarial coverage ON TOP OF T07-1..T07-12
// (batch_submit_test.go/batch_submit_handler_test.go), written during the Mode B
// (post-implementation) verify pass. Closes gaps the mutation-testing pass surfaced --
// specifically: (1) no existing spec drives TWO DISTINCT validated invoices through one
// batch, so a regression collapsing deriveBatchSubmitKey back onto the bare request key
// would collide two unrelated invoices onto one outbox key and go undetected; (2) no
// existing spec calls Submitter.BatchSubmit directly with an empty id list, so a
// regression reverting BatchSubmit's own make([]T, 0, ...) to a nil var would go
// undetected (T07-9 only exercises a FAKE submit closure at the handler layer, never
// BatchSubmit's own slice construction); (3) no existing spec drives the 218-char
// idempotency-key bound through a REAL committed idempotency_keys row against the live
// CHECK constraint -- T07-7's bound half only proves the handler's pre-tx string-length
// guard. Also adds: the >=3x duplicate-id case, a genuine independent-status-advance
// replay, result order/cardinality preservation, hard-fail dominance over a mixed batch,
// the exact 200-id cap boundary, and a real concurrent-race proof. Reuses the
// dbTestPools/seedTenant/seedEntity/seedInvoiceAtStatus/mustCount harness from
// store_test.go/transition_adversarial_test.go (same package).
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- gap 1: two DISTINCT validated invoices in one batch --------------------

// TestBatchSubmit_MultipleDistinctValidatedInvoicesEachGetOwnKeyAndJob: a batch of TWO
// different validated invoices under the same request-level idempotency_key must produce
// TWO distinct river_job rows and TWO distinct idempotency_keys rows -- one per invoice,
// per deriveBatchSubmitKey's whole point ([per-invoice-key-derivation]). A regression that
// enqueues with the bare in.IdempotencyKey (instead of the per-invoice derived key) would
// make the SECOND invoice's EnqueueTx collide with the first's already-inserted key and
// silently skip it -- exactly the class of bug none of T07-1/T07-3/T07-4/T07-10 can catch,
// since each seeds only ONE validated invoice per batch.
func TestBatchSubmit_MultipleDistinctValidatedInvoicesEachGetOwnKeyAndJob(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-MULTI tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-MULTI entity")
	inv1 := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-MULTI-1", StatusValidated)
	inv2 := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-MULTI-2", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	requestKey := "ADV-MULTI-" + uuid.NewString()

	resp, err := submitter.BatchSubmit(c, BatchSubmitInput{
		InvoiceIDs:     []string{inv1, inv2},
		IdempotencyKey: requestKey,
	})
	if err != nil {
		t.Fatalf("BatchSubmit: %v, want nil", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(resp.Results))
	}
	for _, item := range resp.Results {
		if !item.Enqueued {
			t.Errorf("result %+v: want enqueued:true for both distinct invoices", item)
		}
	}

	if n := countBatchSubmitJobs(t, app, inv1); n != 1 {
		t.Errorf("river_job rows for inv1 = %d, want 1", n)
	}
	if n := countBatchSubmitJobs(t, app, inv2); n != 1 {
		t.Errorf("river_job rows for inv2 = %d, want 1", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1`, tenantID); n != 2 {
		t.Errorf("idempotency_keys rows for the tenant = %d, want 2 (one per invoice, distinct derived keys)", n)
	}
	// Pin the derived keys are actually distinct, not the bare request key collapsed twice.
	if n := mustCount(t, super, `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, tenantID, deriveBatchSubmitKey(requestKey, inv1)); n != 1 {
		t.Errorf("idempotency_keys row for inv1's derived key = %d, want 1", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, tenantID, deriveBatchSubmitKey(requestKey, inv2)); n != 1 {
		t.Errorf("idempotency_keys row for inv2's derived key = %d, want 1", n)
	}
}

// --- gap 2: BatchSubmit's OWN nil-vs-empty defense, not the handler's fake ---

// TestBatchSubmit_DirectCallWithEmptyIDsMarshalsEmptyResultsNotNull: calling
// Submitter.BatchSubmit DIRECTLY (bypassing BatchSubmitHandler's pre-tx "empty
// invoice_ids -> 400" guard -- something the handler enforces but BatchSubmit itself does
// not) with an empty InvoiceIDs must still marshal "results":[], never "results":null.
// T07-9 (batch_submit_handler_test.go) only proves this for a FAKE submit closure that
// hard-codes make([]BatchSubmitResultItem, 0); it never exercises BatchSubmit's own
// make([]BatchSubmitResultItem, 0, len(in.InvoiceIDs)) line, so a regression there (e.g.
// reverting to `var results []BatchSubmitResultItem`) would go completely undetected by
// the existing suite -- confirmed by mutation testing during this QA pass.
func TestBatchSubmit_DirectCallWithEmptyIDsMarshalsEmptyResultsNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-EMPTY tenant")
	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	resp, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{}, IdempotencyKey: "ADV-EMPTY-" + uuid.NewString()})
	if err != nil {
		t.Fatalf("BatchSubmit with empty InvoiceIDs: %v, want nil", err)
	}
	if resp.Results == nil {
		t.Fatal("BatchSubmit result Results is nil, want a non-nil empty slice")
	}
	if len(resp.Results) != 0 {
		t.Fatalf("len(results) = %d, want 0", len(resp.Results))
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal BatchSubmitResult: %v", err)
	}
	if got := string(raw); got != `{"results":[]}` {
		t.Errorf("json.Marshal(BatchSubmit's own empty result) = %s, want {\"results\":[]}", got)
	}
}

// --- gap 3: the 218-char bound proven against the LIVE idempotency_keys CHECK ----

// TestBatchSubmit_218CharKeyBoundHoldsAgainstLiveCheckConstraint: drives a batch through
// the REAL handler+Submitter+queue stack with an idempotency_key at the exact 218-char
// accepted bound, then reads back the COMMITTED idempotency_keys row directly and asserts
// its key is exactly 255 chars -- the shared idempotency_keys/submission_jobs
// char_length<=255 CHECK's exact boundary (migrations/20260707193000_river_and_idempotency.sql).
// TestBatchSubmitHandler_IdempotencyKeyLengthBound (batch_submit_handler_test.go, T07-7's
// bound half) proves only that the HANDLER's own pre-tx string-length guard accepts 218 --
// it uses a fake submit closure and never touches the database, so it cannot prove the
// derived key the real implementation builds actually clears the live CHECK rather than
// merely satisfying the handler's own (potentially miscalibrated) arithmetic.
func TestBatchSubmit_218CharKeyBoundHoldsAgainstLiveCheckConstraint(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-218 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-218 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-218", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	requestKey := ""
	for i := 0; i < 218; i++ {
		requestKey += "k"
	}
	if len(requestKey) != 218 {
		t.Fatalf("test setup: requestKey len = %d, want 218", len(requestKey))
	}

	identity := &auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	body := marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{invID}, IdempotencyKey: requestKey})
	rec, _ := doBatchSubmit(t, submitter.BatchSubmit, identity, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	wantKey := deriveBatchSubmitKey(requestKey, invID)
	if len(wantKey) != 255 {
		t.Fatalf("test setup: derived key len = %d, want exactly 255 (the CHECK boundary)", len(wantKey))
	}
	var gotKey string
	if err := super.QueryRow(ctx,
		`SELECT key FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, tenantID, wantKey,
	).Scan(&gotKey); err != nil {
		t.Fatalf("read back committed idempotency_keys row at the 255-char boundary: %v (the 218-char handler "+
			"bound must survive the live CHECK char_length<=255, not just the handler's own arithmetic)", err)
	}
	if len(gotKey) != 255 {
		t.Errorf("committed idempotency_keys.key length = %d, want 255", len(gotKey))
	}
}

// --- gap 4: same id 3+ times in one request ----------------------------------

// TestBatchSubmit_SameIDThreeTimesOnlyFirstEnqueuesRestDuplicate: extends T07-4 (2
// occurrences) to 3 -- the SAME invoice id listed three times must enqueue exactly once
// (first occurrence) and report duplicate_request for BOTH later occurrences, not just the
// second. Guards against an off-by-one "only the immediately-following duplicate is
// caught" regression.
func TestBatchSubmit_SameIDThreeTimesOnlyFirstEnqueuesRestDuplicate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-3X tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-3X entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-3X", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	resp, err := submitter.BatchSubmit(c, BatchSubmitInput{
		InvoiceIDs:     []string{invID, invID, invID},
		IdempotencyKey: "ADV-3X-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit: %v, want nil", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(resp.Results))
	}

	enqueuedCount, duplicateCount := 0, 0
	for _, item := range resp.Results {
		switch {
		case item.Enqueued:
			enqueuedCount++
		case item.Reason == batchSubmitReasonDuplicate:
			duplicateCount++
		default:
			t.Errorf("unexpected result item %+v", item)
		}
	}
	if enqueuedCount != 1 || duplicateCount != 2 {
		t.Errorf("enqueued=%d duplicate_request=%d, want exactly 1 enqueued and 2 duplicate_request", enqueuedCount, duplicateCount)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Errorf("river_job rows = %d, want exactly 1", n)
	}
}

// --- gap 5: replay after the invoice was genuinely advanced elsewhere --------

// TestBatchSubmit_ReplayAfterInvoiceIndependentlyAdvancedReportsNotValidatedCleanly:
// between the first BatchSubmit call and a byte-identical replay, something ELSE (e.g. the
// worker) moves the invoice past 'queued' -- simulated here with a direct superuser status
// write, mirroring how the real submission worker would advance it outside BatchSubmit
// entirely. The replay must still return HTTP 200 with enqueued:false, reason:
// "not_validated" (the eligibility check reads the LIVE status, which is no longer
// 'validated') and must add NO additional river_job or idempotency_keys row -- it must
// NOT error, and must NOT attempt EnqueueTx again for a key that was already consumed by
// the first call.
func TestBatchSubmit_ReplayAfterInvoiceIndependentlyAdvancedReportsNotValidatedCleanly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-ADVANCED tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-ADVANCED entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-ADVANCED", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	in := BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: "ADV-ADVANCED-" + uuid.NewString()}

	if _, err := submitter.BatchSubmit(c, in); err != nil {
		t.Fatalf("first BatchSubmit: %v, want nil", err)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Fatalf("river_job rows after first call = %d, want 1", n)
	}

	// Simulate the worker independently moving the invoice past 'queued' before the
	// caller ever replays -- a direct superuser write, exactly like seedInvoiceAtStatus
	// does, standing in for a real Store.Transition/MarkSubmittedTx call this test does
	// not need to drive for real.
	if _, err := super.Exec(ctx, `UPDATE invoices SET status = 'submitted' WHERE id = $1`, invID); err != nil {
		t.Fatalf("force-advance invoice status: %v", err)
	}

	resp2, err := submitter.BatchSubmit(c, in)
	if err != nil {
		t.Fatalf("replay BatchSubmit: %v, want nil (still HTTP 200 contract)", err)
	}
	if len(resp2.Results) != 1 {
		t.Fatalf("len(replay results) = %d, want 1", len(resp2.Results))
	}
	if got := resp2.Results[0]; got.Enqueued || got.Reason != batchSubmitReasonNotValidated || got.Status != "submitted" {
		t.Errorf("replay result = %+v, want {enqueued:false status:submitted reason:%q}", got, batchSubmitReasonNotValidated)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Errorf("river_job rows after replay = %d, want unchanged 1", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1`, tenantID); n != 1 {
		t.Errorf("idempotency_keys rows after replay = %d, want unchanged 1", n)
	}
}

// --- gap 6: response preserves input order and cardinality -------------------

// TestBatchSubmit_ResultsPreserveInputOrderAndCardinality: N ids in (a mix of two
// distinct validated invoices plus one duplicate of the first) must produce EXACTLY N
// results out, in the SAME order as the request, duplicates included -- not
// deduplicated, not reordered, not collapsed.
func TestBatchSubmit_ResultsPreserveInputOrderAndCardinality(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-ORDER tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-ORDER entity")
	inv1 := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-ORDER-1", StatusValidated)
	inv2 := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-ORDER-2", StatusDraft)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	requestIDs := []string{inv1, inv2, inv1} // position 0 and 2 are the SAME id
	resp, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: requestIDs, IdempotencyKey: "ADV-ORDER-" + uuid.NewString()})
	if err != nil {
		t.Fatalf("BatchSubmit: %v, want nil", err)
	}
	if len(resp.Results) != len(requestIDs) {
		t.Fatalf("len(results) = %d, want %d (cardinality must match the request exactly)", len(resp.Results), len(requestIDs))
	}
	for i, id := range requestIDs {
		if resp.Results[i].InvoiceID != id {
			t.Errorf("results[%d].InvoiceID = %q, want %q (order must match the request)", i, resp.Results[i].InvoiceID, id)
		}
	}
	// Position 0 (inv1, first occurrence): enqueued. Position 1 (inv2, draft): skipped
	// not_validated. Position 2 (inv1, second occurrence): duplicate_request.
	if !resp.Results[0].Enqueued {
		t.Errorf("results[0] (inv1 first occurrence) = %+v, want enqueued:true", resp.Results[0])
	}
	if resp.Results[1].Enqueued || resp.Results[1].Reason != batchSubmitReasonNotValidated {
		t.Errorf("results[1] (inv2, draft) = %+v, want enqueued:false reason:%q", resp.Results[1], batchSubmitReasonNotValidated)
	}
	if resp.Results[2].Enqueued || resp.Results[2].Reason != batchSubmitReasonDuplicate {
		t.Errorf("results[2] (inv1 second occurrence) = %+v, want enqueued:false reason:%q", resp.Results[2], batchSubmitReasonDuplicate)
	}
}

// --- gap 7: hard-fail dominates a mixed batch even with an enqueueable id ----

// TestBatchSubmit_UnknownIDDominatesEvenWithEnqueueableAndSkippableSiblings: a batch
// mixing ONE enqueueable (validated) id, ONE skippable (draft, not_validated) id, and ONE
// hard-fail (unknown) id must roll back the WHOLE request -- the unknown id's ErrNotFound
// dominates regardless of how many siblings COULD have succeeded or been skipped
// harmlessly. Extends T07-5 (which pairs only one valid + one unknown) with a third,
// skippable sibling to prove the skip path never masks or defers the hard-fail.
func TestBatchSubmit_UnknownIDDominatesEvenWithEnqueueableAndSkippableSiblings(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-DOMINATE tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-DOMINATE entity")
	validID := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-DOMINATE-valid", StatusValidated)
	draftID := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-DOMINATE-draft", StatusDraft)
	unknownID := uuid.NewString()

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	_, err := submitter.BatchSubmit(c, BatchSubmitInput{
		InvoiceIDs:     []string{validID, draftID, unknownID},
		IdempotencyKey: "ADV-DOMINATE-" + uuid.NewString(),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("BatchSubmit err = %v, want ErrNotFound", err)
	}

	var validStatus, draftStatus string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, validID).Scan(&validStatus); err != nil {
		t.Fatalf("read back valid sibling status: %v", err)
	}
	if validStatus != string(StatusValidated) {
		t.Errorf("valid sibling status = %q, want unchanged %q (whole request rolled back)", validStatus, StatusValidated)
	}
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, draftID).Scan(&draftStatus); err != nil {
		t.Fatalf("read back draft sibling status: %v", err)
	}
	if draftStatus != string(StatusDraft) {
		t.Errorf("draft sibling status = %q, want unchanged %q", draftStatus, StatusDraft)
	}
	if n := countBatchSubmitJobs(t, app, validID); n != 0 {
		t.Errorf("river_job rows for the sibling that WOULD have enqueued = %d, want 0 (whole request rolled back)", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("idempotency_keys rows for the tenant = %d, want 0", n)
	}
}

// --- gap 8: exact 200-id cap boundary is ACCEPTED end-to-end -----------------

// TestBatchSubmit_ExactlyAt200IDCapSucceeds: a batch of EXACTLY 200 distinct validated
// invoice ids -- the handler's own cap -- must be accepted and fully processed (all 200
// enqueued), not rejected. T07-8 (batch_submit_handler_test.go) already proves 201 is
// rejected; this proves the boundary value itself is NOT off-by-one on the accept side,
// end-to-end through the real Submitter (not a fake submit closure).
func TestBatchSubmit_ExactlyAt200IDCapSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-CAP200 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-CAP200 entity")

	const n = 200
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-CAP200-"+uuid.NewString(), StatusValidated)
	}

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	resp, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: ids, IdempotencyKey: "ADV-CAP200-" + uuid.NewString()})
	if err != nil {
		t.Fatalf("BatchSubmit with exactly 200 ids: %v, want nil", err)
	}
	if len(resp.Results) != n {
		t.Fatalf("len(results) = %d, want %d", len(resp.Results), n)
	}
	enqueuedCount := 0
	for _, item := range resp.Results {
		if item.Enqueued {
			enqueuedCount++
		}
	}
	if enqueuedCount != n {
		t.Errorf("enqueued count = %d, want all %d enqueued", enqueuedCount, n)
	}
}

// --- gap 9: a genuine concurrent race, not a sequential replay ---------------

// TestBatchSubmit_ConcurrentIdenticalBatchesRaceToExactlyOneJob: N goroutines call
// BatchSubmit with the BYTE-IDENTICAL body (same single invoice id, same idempotency_key)
// truly concurrently. Exactly ONE must observe enqueued:true and exactly ONE river_job
// row must exist afterward -- the invoices row's `SELECT ... FOR UPDATE` lock serializes
// the racers (loser(s) then observe status='queued' under their own FOR UPDATE read and
// classify not_validated), backed by idempotency_keys' UNIQUE(tenant_id,key) as the
// second, independent line of defense (queue.go's EnqueueTx doc comment: "a concurrent
// duplicate blocks on the unique index until the first tx resolves"). This is what T07-3
// (a SEQUENTIAL replay, one call fully committed before the next starts) cannot exercise.
func TestBatchSubmit_ConcurrentIdenticalBatchesRaceToExactlyOneJob(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "ADV-RACE tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-RACE entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "ADV-RACE", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	in := BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: "ADV-RACE-" + uuid.NewString()}

	const races = 8
	results := make([]BatchSubmitResult, races)
	errs := make([]error, races)
	var wg sync.WaitGroup
	wg.Add(races)
	for i := 0; i < races; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = submitter.BatchSubmit(c, in)
		}(i)
	}
	wg.Wait()

	enqueuedCount := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d BatchSubmit: %v, want nil", i, err)
		}
		if len(results[i].Results) != 1 {
			t.Fatalf("racer %d: len(results) = %d, want 1", i, len(results[i].Results))
		}
		if results[i].Results[0].Enqueued {
			enqueuedCount++
		}
	}
	if enqueuedCount != 1 {
		t.Errorf("enqueued=true count across %d concurrent identical calls = %d, want exactly 1", races, enqueuedCount)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Errorf("river_job rows after the race = %d, want exactly 1", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1`, tenantID); n != 1 {
		t.Errorf("idempotency_keys rows after the race = %d, want exactly 1", n)
	}
}
