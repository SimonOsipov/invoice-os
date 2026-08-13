// APPR-08-04 (task-501, Mode B): adversarial coverage for the batch door's transmit gate,
// written against the shipped implementation. batch_submit_gate_test.go holds the
// acceptance-criteria specs; this file holds what survived them — a swallowed gate error,
// the flag-OFF half of id normalisation, the run states that are not "approved", and the
// statement budget claims no behavioural assertion reaches.
//
// Run: DATABASE_URL=… DATABASE_SUPERUSER_URL=… DATABASE_READER_URL=… \
// go test -p 1 -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- harness ----------------------------------------------------------------

// idempotencyKeysFor reads the tenant's business keys out of band. EnqueueTx writes one
// row per key it accepts (queue.go), so this is where deriveBatchSubmitKey's output lands.
func idempotencyKeysFor(t *testing.T, super *pgxpool.Pool, tenantID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT key FROM idempotency_keys WHERE tenant_id = $1 ORDER BY key`, tenantID)
	if err != nil {
		t.Fatalf("read idempotency_keys: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan idempotency_keys: %v", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate idempotency_keys: %v", err)
	}
	return out
}

// noDashIDOrFatal strips a uuid's hyphens — the 32-hex spelling uuid.Parse and Postgres
// both accept. Fails the fixture when stripping is a no-op.
func noDashIDOrFatal(t *testing.T, id string) string {
	t.Helper()
	bare := strings.ReplaceAll(id, "-", "")
	if bare == id {
		t.Fatalf("fixture id %q carries no hyphens — the case this test exists for is not exercised", id)
	}
	return bare
}

// --- the flag-OFF half of normalisation -------------------------------------

// TestBatchSubmit_FlagOffCanonicalisesTheEchoedIdAndTheDerivedKey: keying eligibility on
// the LOCKED row's id is NOT flag-scoped, so it changes the flag-off door too — the echoed
// invoice_id and the derived idempotency key both become canonical. Every other
// canonical-id spec runs with the flag ON, so nothing else pins this.
func TestBatchSubmit_FlagOffCanonicalisesTheEchoedIdAndTheDerivedKey(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "APPR-08-04-OFFCANON tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-08-04-OFFCANON entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-08-04-OFFCANON-A", StatusValidated)
	ctx := gateCtx(tenantID)

	upper := upperIDOrFatal(t, invID)
	reqKey := uuid.NewString()

	res, err := gateSubmitter(t, app, false).BatchSubmit(ctx, BatchSubmitInput{
		InvoiceIDs: []string{upper}, IdempotencyKey: reqKey,
	})
	if err != nil {
		t.Fatalf("BatchSubmit(UPPERCASE id) with the flag off: %v (want nil)", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, invID, true, StatusQueued, "")

	// The key literal, not deriveBatchSubmitKey — an assertion that called the production
	// function would follow it wherever it went.
	keys := idempotencyKeysFor(t, super, tenantID)
	if len(keys) != 1 {
		t.Fatalf("idempotency_keys rows = %d (%v), want 1", len(keys), keys)
	}
	if want := reqKey + ":" + invID; keys[0] != want {
		t.Errorf("derived key = %q, want %q — a retry of this request must still dedupe", keys[0], want)
	}
	if keys[0] == reqKey+":"+upper {
		t.Errorf("derived key echoes the caller's spelling %q", keys[0])
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 1 {
		t.Errorf("river_job rows carrying the CANONICAL id = %d, want 1", n)
	}
	if n := countBatchSubmitJobs(t, app, upper); n != 0 {
		t.Errorf("river_job rows carrying the UPPERCASE id = %d, want 0", n)
	}
}

// --- a gate that cannot be read is not a verdict -----------------------------

// TestBatchSubmit_TransmitClearTxErrorIsReturnedNotSwallowed: the batch door's twin of
// TestTransition_TransmitClearTxErrorIsReturnedNotSwallowed. Swallowing the error
// (clear, _ = …) leaves a nil map, which reads false and settles EVERY validated invoice
// in the batch as awaiting_approval — a database outage committed as an approval verdict.
// The whole acceptance suite passes with the error swallowed; this is the spec that
// does not.
func TestBatchSubmit_TransmitClearTxErrorIsReturnedNotSwallowed(t *testing.T) {
	super, app := dbTestPools(t)
	failing := appPoolWithTracer(t, cancelOnSQL{match: "approval_policy_versions"})

	fx := seedGatedTenant(t, super, "APPR-08-04-GATEERR", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture") // would otherwise CLEAR

	secondID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-GATEERR-B", StatusValidated)

	submitter := NewSubmitter(NewStore(failing, WithApprovalsEnforced(true)), newInsertOnlyQueueClient(t, app))

	res, err := submitter.BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID, secondID}, IdempotencyKey: uuid.NewString(),
	})
	if err == nil {
		t.Fatal("BatchSubmit with a failing TransmitClearTx returned nil — the gate's error was swallowed")
	}
	for i, it := range res.Results {
		if it.Reason == batchSubmitReasonAwaitingApproval {
			t.Errorf("results[%d] reports %q on an unreadable gate — an outage is not a verdict", i, it.Reason)
		}
	}
	if status, _ := statusForErr(err); status != http.StatusInternalServerError {
		t.Errorf("statusForErr(%v) = %d, want 500", err, status)
	}

	// The whole tx rolls back: no status change, no job, no key.
	for _, id := range []string{fx.invID, secondID} {
		if s := statusOf(t, super, id); s != StatusValidated {
			t.Errorf("invoice %s status = %q, want unchanged %q", id, s, StatusValidated)
		}
		if n := countBatchSubmitJobs(t, app, id); n != 0 {
			t.Errorf("river_job rows for %s = %d, want 0", id, n)
		}
	}
	if keys := idempotencyKeysFor(t, super, fx.tenantID); len(keys) != 0 {
		t.Errorf("idempotency_keys rows = %d (%v), want 0 — the failed batch must leave no key behind", len(keys), keys)
	}
}

// --- mixed spellings, mixed outcomes ----------------------------------------

// TestBatchSubmit_MixedSpellingsAcrossDifferentInvoices: the caller->canonical map is per
// id, not one global assumption. Three invoices, three spellings, three outcomes, and
// every echoed id is canonical.
func TestBatchSubmit_MixedSpellingsAcrossDifferentInvoices(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-MIXSPELL", StatusValidated)
	approvedRun := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, approvedRun, "approved", "fixture")

	gatedID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-MIXSPELL-B", StatusValidated)
	seedApprovalRunFor(t, super, fx.tenantID, gatedID, fx.versionID) // open

	draftID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-MIXSPELL-C", StatusDraft)

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{
			upperIDOrFatal(t, fx.invID), // approved run, UPPERCASE
			noDashIDOrFatal(t, gatedID), // open run, 32-hex-no-dash
			draftID,                     // draft, already canonical
		},
		IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit over three spellings: %v (want nil)", err)
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
}

// --- run states other than "approved" ---------------------------------------

// TestBatchSubmit_OnlyAnApprovedRunClears: TransmitClearTx tests state = "approved"
// (approval/gate.go), so cancelled and rejected runs are as blocking as an open one. A
// predicate widened to "closed" would let a REJECTED invoice transmit.
func TestBatchSubmit_OnlyAnApprovedRunClears(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-RUNSTATES", StatusValidated)
	approvedRun := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, approvedRun, "approved", "fixture")

	cancelledID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-RUNSTATES-CANCELLED", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, fx.tenantID, cancelledID, fx.versionID), "cancelled", "fixture")

	rejectedID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-RUNSTATES-REJECTED", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, fx.tenantID, rejectedID, fx.versionID), "rejected", "fixture")

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID, cancelledID, rejectedID}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit over three run states: %v (want nil)", err)
	}
	if len(res.Results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(res.Results))
	}
	wantItem(t, res.Results[0], 0, fx.invID, true, StatusQueued, "")
	wantItem(t, res.Results[1], 1, cancelledID, false, StatusValidated, batchSubmitReasonAwaitingApproval)
	wantItem(t, res.Results[2], 2, rejectedID, false, StatusValidated, batchSubmitReasonAwaitingApproval)

	for _, id := range []string{cancelledID, rejectedID} {
		if n := countBatchSubmitJobs(t, app, id); n != 0 {
			t.Errorf("river_job rows for %s = %d, want 0", id, n)
		}
		if s := statusOf(t, super, id); s != StatusValidated {
			t.Errorf("invoice %s status = %q, want unchanged %q", id, s, StatusValidated)
		}
	}
}

// --- a cross-tenant id hard-fails, it never becomes a gate answer ------------

// TestBatchSubmit_CrossTenantIdHardFailsTheWholeBatch: the eligibility lock runs under
// RLS and sees no row, so ErrNotFound aborts the batch BEFORE the gate is consulted.
// TransmitClearTx would otherwise answer for it — presence in that map is not proof the
// invoice exists (approval/gate.go), so a foreign id reaching the gate would be reported
// as awaiting_approval, leaking that the id resolves somewhere.
func TestBatchSubmit_CrossTenantIdHardFailsTheWholeBatch(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-XTENANT", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture") // would otherwise enqueue

	otherTenant := seedTenant(t, super, "APPR-08-04-XTENANT other tenant")
	otherEntity := seedEntity(t, super, otherTenant, "APPR-08-04-XTENANT other entity")
	foreignID := seedInvoiceAtStatus(t, super, otherTenant, otherEntity, "APPR-08-04-XTENANT-FOREIGN", StatusValidated)

	res, err := gateSubmitter(t, app, true).BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: []string{fx.invID, foreignID}, IdempotencyKey: uuid.NewString(),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("BatchSubmit over a cross-tenant id: err = %v, want ErrNotFound", err)
	}
	if len(res.Results) != 0 {
		t.Errorf("len(results) = %d, want 0 — a hard fail returns no partial verdict: %+v", len(res.Results), res.Results)
	}

	// The caller's own approved invoice must roll back with the batch.
	if s := statusOf(t, super, fx.invID); s != StatusValidated {
		t.Errorf("caller's invoice status = %q, want unchanged %q", s, StatusValidated)
	}
	if n := countBatchSubmitJobs(t, app, fx.invID); n != 0 {
		t.Errorf("river_job rows for the caller's invoice = %d, want 0", n)
	}
	if s := statusOf(t, super, foreignID); s != StatusValidated {
		t.Errorf("foreign invoice status = %q, want untouched %q", s, StatusValidated)
	}
	if keys := idempotencyKeysFor(t, super, fx.tenantID); len(keys) != 0 {
		t.Errorf("idempotency_keys rows = %d (%v), want 0", len(keys), keys)
	}
}

// --- the statement budget no behavioural assertion reaches -------------------

// TestBatchSubmit_EligibilityLocksOncePerDistinctId (AC #3, directly): the seen guard is
// what makes the lock count DISTINCT rather than per-position. Its removal is invisible to
// every outcome assertion — the two-phase structure alone keeps the T07-4 trap shut — so
// this is the only spec that observes it.
func TestBatchSubmit_EligibilityLocksOncePerDistinctId(t *testing.T) {
	super, _ := dbTestPools(t)
	tracedApp, rec := tracedAppPool(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-LOCKCOUNT", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID), "approved", "fixture")

	secondID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-LOCKCOUNT-B", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, fx.tenantID, secondID, fx.versionID), "approved", "fixture")

	submitter := gateSubmitter(t, tracedApp, true)

	rec.reset()
	res, err := submitter.BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs:     []string{fx.invID, fx.invID, secondID, fx.invID},
		IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit over 4 positions covering 2 invoices: %v (want nil)", err)
	}
	if len(res.Results) != 4 {
		t.Fatalf("len(results) = %d, want 4 (one per requested position)", len(res.Results))
	}
	if got := rec.mentioning(eligibilityLockSQL); len(got) != 2 {
		t.Errorf("BatchSubmit took %d eligibility lock(s) over 4 positions covering 2 distinct ids, want 2", len(got))
	}
}

// TestBatchSubmit_TwoSpellingsCostOneExtraLockAndNoExtraGateRead: two spellings of one
// uuid are two raw keys, so the seen guard lets both take the (same, no-op) row lock and
// both append the SAME canonical id to the set handed to TransmitClearTx. The duplicate in
// that set is deliberate and free — `= ANY($1::uuid[])` returns one row per invoice, and
// the no-active-policy branch writes the same key twice. This is the spec behind that
// claim.
func TestBatchSubmit_TwoSpellingsCostOneExtraLockAndNoExtraGateRead(t *testing.T) {
	t.Run("active policy: the set-shaped read", func(t *testing.T) {
		super, _ := dbTestPools(t)
		tracedApp, rec := tracedAppPool(t)

		fx := seedGatedTenant(t, super, "APPR-08-04-DUPSET-ACTIVE", StatusValidated)
		closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID), "approved", "fixture")

		upper := upperIDOrFatal(t, fx.invID)
		submitter := gateSubmitter(t, tracedApp, true)

		rec.reset()
		res, err := submitter.BatchSubmit(fx.ctx, BatchSubmitInput{
			InvoiceIDs: []string{fx.invID, upper}, IdempotencyKey: uuid.NewString(),
		})
		if err != nil {
			t.Fatalf("BatchSubmit over two spellings: %v (want nil)", err)
		}
		if len(res.Results) != 2 {
			t.Fatalf("len(results) = %d, want 2", len(res.Results))
		}
		wantItem(t, res.Results[0], 0, fx.invID, true, StatusQueued, "")
		wantItem(t, res.Results[1], 1, fx.invID, false, StatusQueued, batchSubmitReasonDuplicate)

		if got := rec.mentioning(eligibilityLockSQL); len(got) != 2 {
			t.Errorf("eligibility locks = %d, want 2 — the seen guard is keyed on the caller's raw spelling", len(got))
		}
		if got := rec.mentioning("approval_"); len(got) != 2 {
			t.Errorf("approval_ statements = %d, want 2 — a duplicated canonical id in the set must cost nothing: %v", len(got), got)
		}
	})

	t.Run("no active policy: the short-circuit", func(t *testing.T) {
		super, _ := dbTestPools(t)
		tracedApp, rec := tracedAppPool(t)

		tenantID, entityID, _ := seedInactivePolicyTenant(t, super, "APPR-08-04-DUPSET-INACTIVE")
		invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-08-04-DUPSET-INACTIVE-A", StatusValidated)
		ctx := gateCtx(tenantID)

		upper := upperIDOrFatal(t, invID)
		submitter := gateSubmitter(t, tracedApp, true)

		rec.reset()
		res, err := submitter.BatchSubmit(ctx, BatchSubmitInput{
			InvoiceIDs: []string{invID, upper}, IdempotencyKey: uuid.NewString(),
		})
		if err != nil {
			t.Fatalf("BatchSubmit over two spellings with no active policy: %v (want nil)", err)
		}
		if len(res.Results) != 2 {
			t.Fatalf("len(results) = %d, want 2", len(res.Results))
		}
		wantItem(t, res.Results[0], 0, invID, true, StatusQueued, "")
		wantItem(t, res.Results[1], 1, invID, false, StatusQueued, batchSubmitReasonDuplicate)

		if got := rec.mentioning("approval_"); len(got) != 1 {
			t.Errorf("approval_ statements = %d, want 1 (the short-circuit): %v", len(got), got)
		}
	})
}

// --- the largest legal batch --------------------------------------------------

// TestBatchSubmit_AtThe200IdCapUnderTheFlag: the handler's cap is the biggest batch the
// gate ever sees. 200 invoices, alternating approved and open runs, still cost two
// approval statements and still classify per position.
func TestBatchSubmit_AtThe200IdCapUnderTheFlag(t *testing.T) {
	super, _ := dbTestPools(t)
	tracedApp, rec := tracedAppPool(t)

	fx := seedGatedTenant(t, super, "APPR-08-04-CAP200", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID), "approved", "fixture")

	ids := []string{fx.invID}
	wantEnqueued := []bool{true}
	for i := 1; i < maxBatchSubmitInvoiceIDs; i++ {
		id := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-08-04-CAP200-"+uuid.NewString(), StatusValidated)
		runID := seedApprovalRunFor(t, super, fx.tenantID, id, fx.versionID)
		approved := i%2 == 0
		if approved {
			closeApprovalRunFor(t, super, runID, "approved", "fixture")
		}
		ids = append(ids, id)
		wantEnqueued = append(wantEnqueued, approved)
	}

	submitter := gateSubmitter(t, tracedApp, true)

	rec.reset()
	res, err := submitter.BatchSubmit(fx.ctx, BatchSubmitInput{
		InvoiceIDs: ids, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit at the %d-id cap: %v (want nil)", maxBatchSubmitInvoiceIDs, err)
	}
	if len(res.Results) != len(ids) {
		t.Fatalf("len(results) = %d, want %d", len(res.Results), len(ids))
	}

	enqueued, skipped := 0, 0
	for i, id := range ids {
		if wantEnqueued[i] {
			wantItem(t, res.Results[i], i, id, true, StatusQueued, "")
			enqueued++
			continue
		}
		wantItem(t, res.Results[i], i, id, false, StatusValidated, batchSubmitReasonAwaitingApproval)
		skipped++
	}
	if enqueued == 0 || skipped == 0 {
		t.Fatalf("fixture is one-sided: %d enqueued, %d skipped — both outcomes must appear at the cap", enqueued, skipped)
	}
	if got := rec.mentioning("approval_"); len(got) != 2 {
		t.Errorf("BatchSubmit at the %d-id cap issued %d approval_ statement(s), want exactly 2", maxBatchSubmitInvoiceIDs, len(got))
	}
	if got := rec.mentioning(eligibilityLockSQL); len(got) != len(ids) {
		t.Errorf("eligibility locks = %d, want %d (one per distinct id)", len(got), len(ids))
	}
}
