// M5-04-07 (task-231) Mode A (test-first): DB-backed specs for Submitter.BatchSubmit,
// written BEFORE the real implementation exists (RED against batch_submit.go's
// not-implemented stub: BatchSubmit currently always returns errBatchSubmitNotImplemented
// unconditionally, touching neither the store nor the queue, so every assertion below fails
// on its OWN target value -- a wrong error, an empty/absent result, an unchanged DB row --
// never a compile error). Reuses the dbTestPools/seedTenant/seedEntity/seedInvoiceAtStatus/
// mustCount/auditCount harness from store_test.go/transition_adversarial_test.go (same
// package).
//
// Spec-to-test map (Test Specs table, task-231 story, plus the Stage-1+2-added AC-4
// success-path proof):
//
//	T07-1  TestBatchSubmit_PartialBatchEnqueuesValidatedSkipsRest
//	T07-2  TestBatchSubmit_AtomicityRollsBackOnInjectedFailureAfterLastEnqueue
//	T07-3  TestBatchSubmit_ReplayIsExactlyOnce
//	T07-4  TestBatchSubmit_DuplicateIDWithinOneRequestEnqueuesOnce
//	T07-5  TestBatchSubmit_UnknownIDHardFailsWholeRequest
//	T07-6  TestRLS_BatchSubmitCrossTenantNotFound
//	T07-7  TestDeriveBatchSubmitKey_Shape (shape half -- the bound half is
//	       batch_submit_handler_test.go's TestBatchSubmitHandler_IdempotencyKeyLengthBound)
//	T07-10 TestBatchSubmit_EnqueuedJobArgsCorrect
//	T07-11 TestBatchSubmit_TransitionActorIsJWTSubjectNotSystem
//	T07-12 TestBatchSubmit_SuccessPathInvoiceQueuedAndJobExistTogether (added at Stage 1+2:
//	       the AC-4 "both happen together" half T07-2 alone does not prove)
//
// T07-8/T07-9 and T07-7's bound half are handler-level (no DB) --
// batch_submit_handler_test.go.
//
// Run: `make test-rls` (or DEV_DB_PORT=5433 make test-rls in this worktree), or directly:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// newInsertOnlyQueueClient builds an INSERT-ONLY River client (Workers/Queues both nil) --
// the cmd/invoice shape task-231's Implementation Notes describe (queue.New(pool,
// queue.Config{})), mirroring internal/submission/failure_modes_test.go's newInsertClient.
// An insert-only client skips River's kind-registration check at InsertTx time, so it needs
// no Workers bundle for submission_submit to be reachable here.
func newInsertOnlyQueueClient(t *testing.T, pool *pgxpool.Pool) *queue.Client {
	t.Helper()
	q, err := queue.New(pool, queue.Config{})
	if err != nil {
		t.Fatalf("build insert-only queue client: %v", err)
	}
	return q
}

// countBatchSubmitJobs counts river_job rows for a submission_submit job whose args carry
// invoiceID -- river_job has no RLS (cross-tenant infrastructure), so a bare pool.QueryRow
// against the app-role pool sees every row (precedent: countSubmitJobs,
// internal/submission/worker_smoke_test.go).
func countBatchSubmitJobs(t *testing.T, pool *pgxpool.Pool, invoiceID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'submission_submit' AND args->>'invoice_id' = $1`,
		invoiceID,
	).Scan(&n); err != nil {
		t.Fatalf("count river_job: %v", err)
	}
	return n
}

// --- T07-7 (shape half) -----------------------------------------------------

// TestDeriveBatchSubmitKey_Shape (T07-7, shape half): deriveBatchSubmitKey's format is
// exactly "<request key>:<invoice id>", verbatim -- no re-escaping even when the request
// key itself contains a colon. This is a pure, one-line format function (not "the loop"),
// implemented for real in batch_submit.go rather than stubbed, so this half of T07-7 is
// GREEN from the start (reported as such -- see this subtask's QA report); the bound half
// (a too-long request key rejected 400 BEFORE any write) is RED at the handler layer,
// TestBatchSubmitHandler_IdempotencyKeyLengthBound below this file's sibling
// batch_submit_handler_test.go.
func TestDeriveBatchSubmitKey_Shape(t *testing.T) {
	tests := []struct {
		name                  string
		requestKey, invoiceID string
		want                  string
	}{
		{
			name:       "simple request key and invoice id",
			requestKey: "req-key-1",
			invoiceID:  "11111111-1111-1111-1111-111111111111",
			want:       "req-key-1:11111111-1111-1111-1111-111111111111",
		},
		{
			name:       "a colon inside the request key is preserved verbatim, not re-escaped",
			requestKey: "a:b:c",
			invoiceID:  "22222222-2222-2222-2222-222222222222",
			want:       "a:b:c:22222222-2222-2222-2222-222222222222",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveBatchSubmitKey(tt.requestKey, tt.invoiceID); got != tt.want {
				t.Errorf("deriveBatchSubmitKey(%q, %q) = %q, want %q", tt.requestKey, tt.invoiceID, got, tt.want)
			}
		})
	}
}

// --- T07-1..T07-6, T07-10..T07-12 -------------------------------------------

// TestBatchSubmit_PartialBatchEnqueuesValidatedSkipsRest (T07-1): a batch of 1 validated +
// 1 draft + 1 submitted invoice enqueues exactly the validated one and reports
// batchSubmitReasonNotValidated for the other two -- no error (the HTTP 200 contract, AC-1).
func TestBatchSubmit_PartialBatchEnqueuesValidatedSkipsRest(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "T07-1 tenant")
	entityID := seedEntity(t, super, tenantID, "T07-1 entity")
	validatedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-1-validated", StatusValidated)
	draftID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-1-draft", StatusDraft)
	submittedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-1-submitted", StatusSubmitted)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	resp, err := submitter.BatchSubmit(c, BatchSubmitInput{
		InvoiceIDs:     []string{validatedID, draftID, submittedID},
		IdempotencyKey: "T07-1-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit: %v, want nil (HTTP 200 contract)", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(resp.Results))
	}

	byID := make(map[string]BatchSubmitResultItem, 3)
	for _, item := range resp.Results {
		byID[item.InvoiceID] = item
	}

	if got := byID[validatedID]; !got.Enqueued || got.Status != string(StatusQueued) || got.Reason != "" {
		t.Errorf("validated invoice result = %+v, want {enqueued:true status:queued reason:\"\"}", got)
	}
	if got := byID[draftID]; got.Enqueued || got.Reason != batchSubmitReasonNotValidated {
		t.Errorf("draft invoice result = %+v, want {enqueued:false reason:%q}", got, batchSubmitReasonNotValidated)
	}
	if got := byID[submittedID]; got.Enqueued || got.Reason != batchSubmitReasonNotValidated {
		t.Errorf("submitted invoice result = %+v, want {enqueued:false reason:%q}", got, batchSubmitReasonNotValidated)
	}

	if n := countBatchSubmitJobs(t, app, validatedID); n != 1 {
		t.Errorf("river_job rows for the validated invoice = %d, want 1", n)
	}
	if n := countBatchSubmitJobs(t, app, draftID); n != 0 {
		t.Errorf("river_job rows for the draft (skipped) invoice = %d, want 0", n)
	}
}

// TestBatchSubmit_AtomicityRollsBackOnInjectedFailureAfterLastEnqueue (T07-2, AC-4's
// "neither happens" half): with in.failAfterLastEnqueue set, BatchSubmit must inject a
// failure after the (single, here) invoice's EnqueueTx call succeeds but before commit --
// leaving ZERO rows across idempotency_keys, river_job and invoices.status (still
// 'validated', not 'queued').
//
// The error assertion below deliberately checks for errBatchSubmitInjectedTestFailure, NOT
// merely "err != nil": an unimplemented stub that touches nothing would trivially satisfy
// every "zero rows" assertion in this test too (an absent implementation vacuously proves
// "nothing was written on failure"), so a weaker error check would make this whole test
// structurally green even against batch_submit.go's current stub. Checking the SPECIFIC
// injected-failure sentinel is what makes this RED today (the stub returns
// errBatchSubmitNotImplemented, not errBatchSubmitInjectedTestFailure) for the right
// reason, and forces the real Stage-3 implementation to have actually run the enqueue
// before honouring the hook. T07-12 below is this test's positive-path complement --
// together they prove AC-4's "the two happen together, or neither happens" both ways.
func TestBatchSubmit_AtomicityRollsBackOnInjectedFailureAfterLastEnqueue(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "T07-2 tenant")
	entityID := seedEntity(t, super, tenantID, "T07-2 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-2", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	requestKey := "T07-2-" + uuid.NewString()

	_, err := submitter.BatchSubmit(c, BatchSubmitInput{
		InvoiceIDs:           []string{invID},
		IdempotencyKey:       requestKey,
		failAfterLastEnqueue: true,
	})
	if !errors.Is(err, errBatchSubmitInjectedTestFailure) {
		t.Fatalf("BatchSubmit err = %v, want errBatchSubmitInjectedTestFailure (the Mode-A "+
			"stub returns errBatchSubmitNotImplemented -- Stage 3 must check "+
			"in.failAfterLastEnqueue after the LAST EnqueueTx call and return this sentinel "+
			"before commit; see batch_submit.go's own doc comment)", err)
	}

	derivedKey := deriveBatchSubmitKey(requestKey, invID)
	if n := mustCount(t, super, `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, tenantID, derivedKey); n != 0 {
		t.Errorf("idempotency_keys rows for the derived key = %d, want 0 (forced rollback)", n)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 0 {
		t.Errorf("river_job rows for invoice %s = %d, want 0 (forced rollback)", invID, n)
	}
	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if status != string(StatusValidated) {
		t.Errorf("invoice status = %q, want unchanged %q (forced rollback)", status, StatusValidated)
	}
}

// TestBatchSubmit_ReplayIsExactlyOnce (T07-3): calling BatchSubmit twice with the
// byte-identical input produces no additional river_job or idempotency_keys rows, and the
// replay's result is enqueued:false. AC-2 deliberately allows EITHER skip reason here
// (duplicate_request OR not_validated): after the first call the invoice is no longer
// 'validated' (it moved to 'queued'), so the eligibility check legitimately fires before
// EnqueueTx is ever reached on replay -- this test's oracle is the unchanged job/key count
// plus enqueued:false, not a specific reason string. T07-4 (not this test) pins the
// specific-reason case, where the SAME request enqueues the id once and reports
// duplicate_request for a second occurrence.
func TestBatchSubmit_ReplayIsExactlyOnce(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "T07-3 tenant")
	entityID := seedEntity(t, super, tenantID, "T07-3 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-3", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	in := BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: "T07-3-" + uuid.NewString()}

	if _, err := submitter.BatchSubmit(c, in); err != nil {
		t.Fatalf("first BatchSubmit: %v, want nil", err)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Fatalf("river_job rows after first call = %d, want 1", n)
	}

	resp2, err := submitter.BatchSubmit(c, in)
	if err != nil {
		t.Fatalf("second (replay) BatchSubmit: %v, want nil", err)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Errorf("river_job rows after replay = %d, want unchanged 1", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1`, tenantID); n != 1 {
		t.Errorf("idempotency_keys rows for the tenant after replay = %d, want unchanged 1", n)
	}
	if len(resp2.Results) != 1 {
		t.Fatalf("len(replay results) = %d, want 1", len(resp2.Results))
	}
	if resp2.Results[0].Enqueued {
		t.Errorf("replay result = %+v, want enqueued:false", resp2.Results[0])
	}
}

// TestBatchSubmit_DuplicateIDWithinOneRequestEnqueuesOnce (T07-4): the same invoice id
// listed TWICE in one request produces exactly one river_job, and among the two result
// items the SECOND occurrence reports batchSubmitReasonDuplicate (not
// batchSubmitReasonNotValidated -- see batch_submit.go's doc comment on BatchSubmit for why
// a naive per-list-position status re-read would get this wrong).
func TestBatchSubmit_DuplicateIDWithinOneRequestEnqueuesOnce(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "T07-4 tenant")
	entityID := seedEntity(t, super, tenantID, "T07-4 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-4", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	resp, err := submitter.BatchSubmit(c, BatchSubmitInput{
		InvoiceIDs:     []string{invID, invID}, // same id twice in one request
		IdempotencyKey: "T07-4-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit: %v, want nil (HTTP 200 contract)", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2 (one per requested position, even for a duplicate id)", len(resp.Results))
	}

	enqueuedCount, duplicateCount := 0, 0
	for _, item := range resp.Results {
		if item.InvoiceID != invID {
			t.Errorf("result item invoice_id = %q, want %q", item.InvoiceID, invID)
		}
		switch {
		case item.Enqueued:
			enqueuedCount++
		case item.Reason == batchSubmitReasonDuplicate:
			duplicateCount++
		default:
			t.Errorf("unexpected result item %+v, want either enqueued:true or reason:%q", item, batchSubmitReasonDuplicate)
		}
	}
	if enqueuedCount != 1 || duplicateCount != 1 {
		t.Errorf("enqueued=%d duplicate_request=%d, want exactly 1 of each -- the SECOND "+
			"occurrence must resolve via EnqueueTx's own (tenant_id, key) dedupe, not a "+
			"stale not_validated re-read of the row the FIRST occurrence just transitioned "+
			"to queued inside the SAME transaction", enqueuedCount, duplicateCount)
	}

	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Errorf("river_job rows for invoice %s = %d, want exactly 1", invID, n)
	}
}

// TestBatchSubmit_UnknownIDHardFailsWholeRequest (T07-5): an id with no row (well-formed
// uuid, no match under RLS) is a 404-class ErrNotFound that rolls back the WHOLE request --
// a sibling invoice that WOULD have enqueued stays untouched.
func TestBatchSubmit_UnknownIDHardFailsWholeRequest(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "T07-5 tenant")
	entityID := seedEntity(t, super, tenantID, "T07-5 entity")
	validID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-5", StatusValidated)
	unknownID := uuid.NewString() // well-formed uuid, no row anywhere

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	_, err := submitter.BatchSubmit(c, BatchSubmitInput{
		InvoiceIDs:     []string{validID, unknownID},
		IdempotencyKey: "T07-5-" + uuid.NewString(),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("BatchSubmit err = %v, want ErrNotFound (404)", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, validID).Scan(&status); err != nil {
		t.Fatalf("read back sibling invoice status: %v", err)
	}
	if status != string(StatusValidated) {
		t.Errorf("sibling invoice status = %q, want unchanged %q (whole request rolled back)", status, StatusValidated)
	}
	if n := countBatchSubmitJobs(t, app, validID); n != 0 {
		t.Errorf("river_job rows for the sibling invoice = %d, want 0 (whole request rolled back)", n)
	}
}

// TestRLS_BatchSubmitCrossTenantNotFound (T07-6): tenant B submitting tenant A's invoice id
// gets 404, exactly like the unknown-id case (RLS makes the row invisible -> 0 rows), and
// tenant A's invoice is completely untouched.
func TestRLS_BatchSubmitCrossTenantNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantA := seedTenant(t, super, "T07-6 tenant A")
	tenantB := seedTenant(t, super, "T07-6 tenant B")
	entityA := seedEntity(t, super, tenantA, "T07-6 entity A")
	invoiceA := seedInvoiceAtStatus(t, super, tenantA, entityA, "T07-6", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})

	_, err := submitter.BatchSubmit(cB, BatchSubmitInput{
		InvoiceIDs:     []string{invoiceA},
		IdempotencyKey: "T07-6-" + uuid.NewString(),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("BatchSubmit (tenant B submitting tenant A's invoice) err = %v, want ErrNotFound", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceA).Scan(&status); err != nil {
		t.Fatalf("read back tenant A's invoice status: %v", err)
	}
	if status != string(StatusValidated) {
		t.Errorf("tenant A's invoice status = %q, want unchanged %q", status, StatusValidated)
	}
	if n := countBatchSubmitJobs(t, app, invoiceA); n != 0 {
		t.Errorf("river_job rows for tenant A's invoice = %d, want 0", n)
	}
}

// TestBatchSubmit_EnqueuedJobArgsCorrect (T07-10): the enqueued River job's kind is
// submission_submit, its args.tenant_id equals the caller's tenant (EnqueueTx's
// TenantScoped check would otherwise fail closed), and its args.idempotency_key equals the
// exact derived key (deriveBatchSubmitKey), not the bare request key.
func TestBatchSubmit_EnqueuedJobArgsCorrect(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "T07-10 tenant")
	entityID := seedEntity(t, super, tenantID, "T07-10 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-10", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	requestKey := "T07-10-" + uuid.NewString()

	if _, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: requestKey}); err != nil {
		t.Fatalf("BatchSubmit: %v, want nil", err)
	}

	wantKey := deriveBatchSubmitKey(requestKey, invID)
	var n int
	if err := app.QueryRow(ctx,
		`SELECT count(*) FROM river_job
		 WHERE kind = 'submission_submit'
		   AND args->>'tenant_id' = $1
		   AND args->>'invoice_id' = $2
		   AND args->>'idempotency_key' = $3`,
		tenantID, invID, wantKey,
	).Scan(&n); err != nil {
		t.Fatalf("count river_job: %v", err)
	}
	if n != 1 {
		t.Errorf("river_job rows matching kind=submission_submit tenant_id=%s invoice_id=%s idempotency_key=%s = %d, want 1",
			tenantID, invID, wantKey, n)
	}
}

// TestBatchSubmit_TransitionActorIsJWTSubjectNotSystem (T07-11): the validated->queued
// invoice_status_history row's actor is the caller's JWT subject -- BatchSubmit is a user
// action (actorFromContext), never SystemActor (the worker's identity).
func TestBatchSubmit_TransitionActorIsJWTSubjectNotSystem(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "T07-11 tenant")
	entityID := seedEntity(t, super, tenantID, "T07-11 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-11", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	if _, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: "T07-11-" + uuid.NewString()}); err != nil {
		t.Fatalf("BatchSubmit: %v, want nil", err)
	}

	var actor string
	if err := super.QueryRow(ctx,
		`SELECT actor FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'queued'`,
		invID,
	).Scan(&actor); err != nil {
		t.Fatalf("read back validated->queued history actor: %v", err)
	}
	if actor != subject {
		t.Errorf("validated->queued history actor = %q, want the JWT subject %q (never %q)", actor, subject, "system")
	}
}

// TestBatchSubmit_SuccessPathInvoiceQueuedAndJobExistTogether (T07-12, added at Stage 1+2:
// the AC-4 success-path proof gap): a committed enqueue must show BOTH halves TOGETHER --
// invoices.status='queued' AND a matching river_job row -- joined on the same invoice id in
// ONE query, so neither half can silently be missing while the other is present. This is
// T07-2's positive-path complement: T07-2 alone only proves "neither happens" on a forced
// failure; this proves "both happen" on a genuine success.
func TestBatchSubmit_SuccessPathInvoiceQueuedAndJobExistTogether(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	q := newInsertOnlyQueueClient(t, app)

	tenantID := seedTenant(t, super, "T07-12 tenant")
	entityID := seedEntity(t, super, tenantID, "T07-12 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T07-12", StatusValidated)

	submitter := NewSubmitter(NewStore(app), q)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	if _, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: "T07-12-" + uuid.NewString()}); err != nil {
		t.Fatalf("BatchSubmit: %v, want nil", err)
	}

	// $1/$2 (not $1 reused) -- deliberately: reusing $1 for both the args->>'invoice_id'
	// (text) comparison and the invoices.id (uuid) comparison makes Postgres infer a
	// single type for $1 across the whole statement and fails to prepare with
	// "operator does not exist: uuid = text" (42883, reproduced independently via a bare
	// psql PREPARE against this schema) -- a pre-existing SQL-binding bug in this RED
	// spec's own query, unrelated to Submitter.BatchSubmit. Splitting into two
	// placeholders (same invID value, bound twice) lets each site infer its own natural
	// type and changes NOTHING about what is asserted -- the joined boolean, "does a
	// queued invoice AND its matching river_job exist together", is identical either way.
	var both bool
	if err := super.QueryRow(ctx,
		`SELECT (status = 'queued') AND EXISTS (
			SELECT 1 FROM river_job WHERE kind = 'submission_submit' AND args->>'invoice_id' = $1
		) FROM invoices WHERE id = $2`,
		invID, invID,
	).Scan(&both); err != nil {
		t.Fatalf("joined status+job existence check: %v", err)
	}
	if !both {
		t.Errorf("invoices.status='queued' AND a matching river_job must hold TOGETHER for invoice %s, want true", invID)
	}
}
