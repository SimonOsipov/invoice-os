// batch_submit.go: M5-04-07 (task-231) -- POST /v1/invoices/submissions ([trigger-surface]):
// a batch endpoint that takes N invoice ids plus ONE request-level idempotency key and
// enqueues every eligible (validated) invoice via queue.EnqueueTx inside a SINGLE
// db.WithinRequestTenantTx, transitioning each enqueued invoice to queued -- while
// best-effort skipping ineligible ones with a per-invoice reason ([partial-batch]).
//
// BatchSubmit lives on Submitter, a NEW sibling type to Store -- NOT a Store method
// (Stage 1+2 architectural decision, task-231 Implementation Notes): Store holds only a
// *pgxpool.Pool and has zero queue access; widening NewStore to add a queue.Client field
// would touch ~148 call sites across cmd/invoice and internal/invoice's/internal/importer's
// own tests. Submitter mirrors Gate's exact shape (gate.go): a struct wrapping the
// dependencies it drives, its own constructor, wired as one extra local in
// cmd/invoice/main.go (M5-04-08's job, not this file's -- that subtask also builds the
// insert-only queue.Client this Submitter needs, per the same Implementation Notes).
//
// Mode A (test-first) scaffold for M5-04-07 -- BatchSubmit is STUBBED, always returning
// errBatchSubmitNotImplemented (mirrors the errActorNotImplemented / errApplyValidationNotImplemented
// / errPortNotImplemented / errWorkerNotImplemented precedent across 02/03/05/06), so every
// T07-* assertion in batch_submit_test.go/batch_submit_handler_test.go fails on ITS OWN
// target value, never a compile error. The real tx1-loop-tx2 body (System Design, task-231)
// is Stage 3's job.
package invoice

import (
	"context"
	"errors"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// errBatchSubmitNotImplemented is BatchSubmit's Mode-A stub error.
var errBatchSubmitNotImplemented = errors.New("invoice: batch submit not implemented")

// errBatchSubmitInjectedTestFailure is the error the REAL (Stage 3) BatchSubmit must return
// when BatchSubmitInput's failAfterLastEnqueue test hook fires -- the T07-2 atomicity
// injection seam (task-231 note: "design the injection seam now"). Deliberately distinct
// from errBatchSubmitNotImplemented so TestBatchSubmit_AtomicityRollsBackOnInjectedFailureAfterLastEnqueue
// (batch_submit_test.go) can assert BatchSubmit actually exercised the injected-failure path
// rather than vacuously matching against an unimplemented stub that never enqueues anything
// (see that test's own doc comment for why the distinction is load-bearing).
var errBatchSubmitInjectedTestFailure = errors.New("invoice: batch submit test-injected failure after last enqueue")

// The two reachable skip reasons ([partial-batch] eligibility table, task-231 System
// Design). Every other outcome is a hard-fail (ErrNotFound/ErrValidation), never a skip.
const (
	batchSubmitReasonNotValidated = "not_validated"
	batchSubmitReasonDuplicate    = "duplicate_request"
)

// deriveBatchSubmitKey builds the per-invoice outbox key from the request's single
// idempotency_key ([per-invoice-key-derivation], System Design): "<request key>:<invoice
// id>". N invoices in one request therefore produce N distinct EnqueueTx opportunities
// instead of collapsing onto one shared key ([D-per-invoice-not-collapsed]). A pure,
// one-line format function -- not "the loop" -- so it is implemented for real here (not
// stubbed) and reused verbatim by both this test suite (batch_submit_test.go's
// TestDeriveBatchSubmitKey_Shape, T07-7's shape half) and Stage 3's real BatchSubmit body.
func deriveBatchSubmitKey(requestKey, invoiceID string) string {
	return requestKey + ":" + invoiceID
}

// BatchSubmitInput is Submitter.BatchSubmit's request. InvoiceIDs is the caller's batch;
// the >200-id cap and the idempotency-key blank/length guards are the HANDLER's job, run
// BEFORE BatchSubmit is ever called (batch_submit_handler_test.go's T07-8/T07-7-bound --
// "the 218-char-bound rejection happens BEFORE any write"). IdempotencyKey is the ONE
// request-level key BatchSubmit derives each invoice's outbox key from via
// deriveBatchSubmitKey.
type BatchSubmitInput struct {
	InvoiceIDs     []string
	IdempotencyKey string

	// failAfterLastEnqueue is a TEST-ONLY injection seam, unexported so no production
	// caller can ever set it (only this package's own _test.go files can, being
	// same-package white-box tests -- cmd/invoice's wiring, in package main, has no access
	// to an unexported field of another package's struct). Stage 3's real implementation
	// must check it after the LAST successful queue.EnqueueTx call in the batch and, if
	// true, return errBatchSubmitInjectedTestFailure BEFORE the transaction commits --
	// proving the outbox write and the invoices.status transition are one atomic unit
	// (T07-2, AC-4's "neither happens" half) against an implementation that actually did
	// the work first, not against an unimplemented stub that vacuously satisfies "nothing
	// was written" by doing nothing at all.
	failAfterLastEnqueue bool
}

// BatchSubmitResultItem is one invoice's outcome in a BatchSubmitResult (task-231 System
// Design response body). Reason is empty (omitted from the wire) when Enqueued is true --
// the two reachable skip reasons are batchSubmitReasonNotValidated/batchSubmitReasonDuplicate.
type BatchSubmitResultItem struct {
	InvoiceID string `json:"invoice_id"`
	Enqueued  bool   `json:"enqueued"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

// BatchSubmitResult is Submitter.BatchSubmit's response. Results has NO omitempty on its
// own tag, paired with BatchSubmit always building it via
// make([]BatchSubmitResultItem, 0, len(in.InvoiceIDs)) (Stage 3) -- so a zero-enqueue
// response marshals "results":[], never "results":null (AC-5, T07-9; mirrors Store.List's
// own []Invoice{} convention, the M4-16 precedent named in this story's Implementation
// Notes).
type BatchSubmitResult struct {
	Results []BatchSubmitResultItem `json:"results"`
}

// Submitter wraps the two dependencies BatchSubmit drives -- a *Store and an insert-only
// *queue.Client -- mirroring Gate's exact shape (gate.go: store + validator). The caller
// owns both dependencies' lifecycle, exactly as NewGate's caller owns the store's pool and
// the validator's http.Client.
type Submitter struct {
	store *Store
	queue *queue.Client
}

// NewSubmitter wraps the two dependencies BatchSubmit drives.
func NewSubmitter(store *Store, q *queue.Client) *Submitter {
	return &Submitter{store: store, queue: q}
}

// BatchSubmit is the endpoint's store-layer operation: POST /v1/invoices/submissions
// ([trigger-surface]) exposes it via BatchSubmitHandler (handlers.go). STUBBED for Mode A
// -- see this file's header -- always errBatchSubmitNotImplemented, touching neither
// s.store nor s.queue. The real tx1-loop-tx2 body (System Design, task-231) is Stage 3's
// job:
//
//  1. one db.WithinRequestTenantTx over the WHOLE batch ([one-tx-per-batch]);
//  2. per invoice id: SELECT status ... FOR UPDATE (0 rows -> ErrNotFound, hard-fails the
//     whole request); status != validated -> skip batchSubmitReasonNotValidated; else
//     queue.EnqueueTx(ctx, tx, tenantID, deriveBatchSubmitKey(in.IdempotencyKey, id),
//     submission.SubmitArgs{...}, nil) -- skipped=true -> skip batchSubmitReasonDuplicate;
//     skipped=false -> transitionTx(ctx, tx, id, StatusValidated, StatusQueued,
//     actorFromContext(ctx)) (T07-11: the JWT subject, never SystemActor -- this is a user
//     action);
//  3. after the LAST enqueue in the batch, honour in.failAfterLastEnqueue (see its own doc
//     comment) by returning errBatchSubmitInjectedTestFailure before commit;
//  4. results is built non-nil from the start (make(..., 0, len(in.InvoiceIDs))).
//
// T07-4's "same invoice id appears twice in one request" case needs care here: a naive
// per-list-position FOR UPDATE re-read would see the FIRST occurrence's own transitionTx
// (validated->queued, same transaction, same-transaction writes are visible to later
// statements in that transaction) and misclassify the SECOND occurrence as
// batchSubmitReasonNotValidated instead of batchSubmitReasonDuplicate. The eligibility
// decision must therefore be made ONCE per DISTINCT invoice id (e.g. a single upfront
// FOR UPDATE read over the deduplicated id set, or a per-id "already decided" cache
// consulted before re-deriving eligibility) while EnqueueTx is still attempted once per
// REQUESTED LIST POSITION (not deduplicated) -- so the second position's EnqueueTx call
// legitimately hits its own (tenant_id, key) dedupe and reports duplicate_request, per
// TestBatchSubmit_DuplicateIDWithinOneRequestEnqueuesOnce (batch_submit_test.go, T07-4).
func (s *Submitter) BatchSubmit(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
	return BatchSubmitResult{}, errBatchSubmitNotImplemented
}
