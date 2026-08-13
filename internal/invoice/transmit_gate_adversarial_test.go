// APPR-08-10 (task-502): the transmit gate driven through BOTH doors at once.
//
// Each door alone is already covered -- transition_gate_test.go (12) and
// batch_submit_gate_test.go (14) each cover their own door in both flag states. The only
// additive claims here are the CONJUNCTION (both doors, one tenant, one test), the wire
// agreeing with the door it advertises, and the direct-UPDATE scope boundary.
//
// gate_adversarial_test.go / gate_test.go are a DIFFERENT gate (the validation gate,
// gate.go) and are not opened by this file.
//
// Run: DATABASE_URL=… DATABASE_SUPERUSER_URL=… DATABASE_READER_URL=… \
// go test -p 1 -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness -----------------------------------------------------------------

// gatedTenant is seedGatedTenant's wire-capable sibling: an active one-step policy plus a
// memberships row for the caller, and no invoice of its own.
//
// The membership is load-bearing for anything that reads can_submit. callerRoleTx answers
// ("", nil) -- not an error -- for a subject with no membership, so submitGate refuses at
// its FIRST rung and can_submit reads false for a reason that has nothing to do with
// approvals. A wire spec on a bare gateCtx would pass with the approval arm deleted.
type gatedTenant struct {
	tenantID  string
	entityID  string
	versionID string
	identity  auth.Identity
	ctx       context.Context
}

func seedGatedTenantAsAdmin(t *testing.T, super *pgxpool.Pool, label string) gatedTenant {
	t.Helper()
	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, label)
	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	id := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}
	return gatedTenant{
		tenantID:  tenantID,
		entityID:  entityID,
		versionID: versionID,
		identity:  id,
		ctx:       auth.WithIdentity(context.Background(), id),
	}
}

// invoiceWith seeds one validated invoice and gives it a run in runState: "open" leaves it
// awaiting a decision, "approved" closes it clear, "" seeds no run at all.
func (g gatedTenant) invoiceWith(t *testing.T, super *pgxpool.Pool, label, runState string) string {
	t.Helper()
	invID := seedInvoiceAtStatus(t, super, g.tenantID, g.entityID, label, StatusValidated)
	if runState == "" {
		return invID
	}
	runID := seedApprovalRunFor(t, super, g.tenantID, invID, g.versionID)
	if runState != "open" {
		closeApprovalRunFor(t, super, runID, runState, "fixture")
	}
	return invID
}

// transitionDoor drives door 1 and answers "did it reach queued". Anything other than nil
// or ErrAwaitingApproval is a broken fixture, not a gate verdict -- ErrRedundantTransition
// in particular means the invoice was already consumed by an earlier permissive door.
func transitionDoor(t *testing.T, store *Store, ctx context.Context, id string) bool {
	t.Helper()
	_, err := store.Transition(ctx, id, StatusQueued)
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrAwaitingApproval):
		return false
	default:
		t.Fatalf("Store.Transition(%s -> queued): err = %v, want nil or ErrAwaitingApproval", id, err)
		return false
	}
}

// batchDoor drives door 2 over one id and answers "did it enqueue". A skip must read
// awaiting_approval: not_validated would mean the invoice was already consumed, which is
// the false green the two-invoice fixtures exist to prevent.
func batchDoor(t *testing.T, sub *Submitter, ctx context.Context, id string) bool {
	t.Helper()
	res, err := sub.BatchSubmit(ctx, BatchSubmitInput{
		InvoiceIDs: []string{id}, IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("BatchSubmit(%s): %v (want nil -- a gated invoice is a SKIP, not an error)", id, err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(res.Results))
	}
	if res.Results[0].Enqueued {
		wantItem(t, res.Results[0], 0, id, true, StatusQueued, "")
		return true
	}
	wantItem(t, res.Results[0], 0, id, false, StatusValidated, batchSubmitReasonAwaitingApproval)
	return false
}

// --- AC #1: no HTTP door reaches queued while a run is open ------------------

// TestGate_NoHTTPDoorReachesQueuedWhileGated: ONE gated invoice, BOTH doors. Neither
// consumes it -- both refuse -- so a single fixture is honest here. The per-door specs
// prove each door; this proves there is nothing reachable between them.
func TestGate_NoHTTPDoorReachesQueuedWhileGated(t *testing.T) {
	super, app := dbTestPools(t)

	g := seedGatedTenantAsAdmin(t, super, "APPR-08-10-BOTH-GATED")
	invID := g.invoiceWith(t, super, "APPR-08-10-BOTH-GATED-A", "open")

	store := NewStore(app, WithApprovalsEnforced(true))

	if _, err := store.Transition(g.ctx, invID, StatusQueued); !errors.Is(err, ErrAwaitingApproval) {
		t.Errorf("door 1 (Store.Transition): err = %v, want ErrAwaitingApproval", err)
	}
	if batchDoor(t, gateSubmitter(t, app, true), g.ctx, invID) {
		t.Error("door 2 (Submitter.BatchSubmit) enqueued a gated invoice, want an awaiting_approval skip")
	}

	if s := statusOf(t, super, invID); s != StatusValidated {
		t.Errorf("stored status after both doors = %q, want unchanged %q", s, StatusValidated)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 0 {
		t.Errorf("river_job rows = %d, want 0", n)
	}
}

// TestGate_FlagOnBlocksBothDoors: the SEEDED shape -- validated under an active policy
// with no run at all. TransmitClear(true, false) is false with no run exactly as it is
// with an open one, so a backlog nobody has armed is held by both doors too.
func TestGate_FlagOnBlocksBothDoors(t *testing.T) {
	super, app := dbTestPools(t)

	g := seedGatedTenantAsAdmin(t, super, "APPR-08-10-NORUN")
	invID := g.invoiceWith(t, super, "APPR-08-10-NORUN-A", "")

	store := NewStore(app, WithApprovalsEnforced(true))

	if _, err := store.Transition(g.ctx, invID, StatusQueued); !errors.Is(err, ErrAwaitingApproval) {
		t.Errorf("door 1 with no run at all: err = %v, want ErrAwaitingApproval", err)
	}
	if batchDoor(t, gateSubmitter(t, app, true), g.ctx, invID) {
		t.Error("door 2 enqueued an invoice with no run under an active policy, want an awaiting_approval skip")
	}

	if s := statusOf(t, super, invID); s != StatusValidated {
		t.Errorf("stored status = %q, want unchanged %q", s, StatusValidated)
	}
}

// --- AC #2: with the flag off, both doors are the doors they were ------------

// TestGate_FlagOffLeavesBothDoorsUnchanged. TWO invoices, one per door: a PERMISSIVE door
// CONSUMES its invoice, so reusing one would have the batch door skip not_validated on an
// already-queued row and report a pass for the wrong reason.
func TestGate_FlagOffLeavesBothDoorsUnchanged(t *testing.T) {
	super, app := dbTestPools(t)

	g := seedGatedTenantAsAdmin(t, super, "APPR-08-10-FLAGOFF")
	forTransition := g.invoiceWith(t, super, "APPR-08-10-FLAGOFF-T", "open")
	forBatch := g.invoiceWith(t, super, "APPR-08-10-FLAGOFF-B", "open")

	store := NewStore(app) // flag OFF

	if _, err := store.Transition(g.ctx, forTransition, StatusQueued); err != nil {
		t.Errorf("door 1 with the flag off over an open run: %v (want nil)", err)
	}
	if !batchDoor(t, gateSubmitter(t, app, false), g.ctx, forBatch) {
		t.Error("door 2 with the flag off skipped a gated invoice, want an enqueue")
	}

	for _, id := range []string{forTransition, forBatch} {
		if s := statusOf(t, super, id); s != StatusQueued {
			t.Errorf("stored status of %s = %q, want %q", id, s, StatusQueued)
		}
	}
}

// --- AC #1/#2: the wire and the doors answer the same question ---------------

// TestGate_WireAgreesWithBothDoorsInBothFlagStates: can_submit PREDICTS what each door
// then does, across both flag states and both approval shapes. The SPA reads that flag to
// enable a button, so a wire that disagrees with its own door is a lie on screen.
//
// One invoice per (combination, door): a permissive door consumes its own.
func TestGate_WireAgreesWithBothDoorsInBothFlagStates(t *testing.T) {
	super, app := dbTestPools(t)

	for _, tc := range []struct {
		name     string
		enforced bool
		runState string
		want     bool
	}{
		{"flagoff-gated", false, "open", true},
		{"flagoff-clear", false, "approved", true},
		{"flagon-gated", true, "open", false},
		{"flagon-clear", true, "approved", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := "APPR-08-10-WIRE-" + tc.name
			g := seedGatedTenantAsAdmin(t, super, label)
			forTransition := g.invoiceWith(t, super, label+"-T", tc.runState)
			forBatch := g.invoiceWith(t, super, label+"-B", tc.runState)

			store := NewStore(app, WithApprovalsEnforced(tc.enforced))

			for _, id := range []string{forTransition, forBatch} {
				rec, body := doInvoiceGetGated(t, store.Get, store.CallerRole, store.ApprovalFacts, &g.identity, id)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s: status = %d, want 200 (body=%s)", id, rec.Code, rec.Body.String())
				}
				if body.SubmitBlockedReason != nil && *body.SubmitBlockedReason == notApproverTransmitReason {
					t.Fatalf("can_submit refused at the ROLE rung -- the tenant's memberships row is missing, so this spec never reaches the approval rung and would pass with the gate deleted")
				}
				if body.CanSubmit != tc.want {
					t.Errorf("can_submit = %v for %s, want %v", body.CanSubmit, id, tc.want)
				}
			}

			if got := transitionDoor(t, store, g.ctx, forTransition); got != tc.want {
				t.Errorf("door 1 reached queued = %v, want %v -- the wire advertised can_submit = %v", got, tc.want, tc.want)
			}
			if got := batchDoor(t, gateSubmitter(t, app, tc.enforced), g.ctx, forBatch); got != tc.want {
				t.Errorf("door 2 enqueued = %v, want %v -- the wire advertised can_submit = %v", got, tc.want, tc.want)
			}
		})
	}
}

// --- AC #1: the gate opens, it does not merely close ------------------------

// TestGate_ApprovingUnblocksBothDoors. The before half is what makes the after half worth
// anything: a gate that never ran would pass the after half alone. Refusals consume
// nothing, so the same two invoices carry both halves -- but the AFTER half needs one
// invoice per door, since approving makes both doors permissive.
func TestGate_ApprovingUnblocksBothDoors(t *testing.T) {
	super, app := dbTestPools(t)

	g := seedGatedTenantAsAdmin(t, super, "APPR-08-10-APPROVE")

	seed := func(label string) (invID, runID string) {
		invID = seedInvoiceAtStatus(t, super, g.tenantID, g.entityID, label, StatusValidated)
		return invID, seedApprovalRunFor(t, super, g.tenantID, invID, g.versionID)
	}
	transitionInv, transitionRun := seed("APPR-08-10-APPROVE-T")
	batchInv, batchRun := seed("APPR-08-10-APPROVE-B")

	store := NewStore(app, WithApprovalsEnforced(true))

	if _, err := store.Transition(g.ctx, transitionInv, StatusQueued); !errors.Is(err, ErrAwaitingApproval) {
		t.Fatalf("before: door 1 on an open run: err = %v, want ErrAwaitingApproval", err)
	}
	if batchDoor(t, gateSubmitter(t, app, true), g.ctx, batchInv) {
		t.Fatal("before: door 2 enqueued an invoice whose run is still open")
	}

	closeApprovalRunFor(t, super, transitionRun, "approved", "fixture")
	closeApprovalRunFor(t, super, batchRun, "approved", "fixture")

	if _, err := store.Transition(g.ctx, transitionInv, StatusQueued); err != nil {
		t.Errorf("after: door 1 on an approved run: %v (want nil)", err)
	}
	if !batchDoor(t, gateSubmitter(t, app, true), g.ctx, batchInv) {
		t.Error("after: door 2 skipped an invoice whose run is approved, want an enqueue")
	}

	for _, id := range []string{transitionInv, batchInv} {
		if s := statusOf(t, super, id); s != StatusQueued {
			t.Errorf("stored status of %s = %q, want %q", id, s, StatusQueued)
		}
	}
}

// --- AC #3: the scope boundary, asserted so the docs cannot drift off it -----

// TestGate_DirectStatusUpdateIsNotDefended: a raw UPDATE past both doors SUCCEEDS, by
// design. invoice_app holds UPDATE on invoices, status carries only its 7-value CHECK,
// and the table has no trigger -- the schema has never been state-machine-aware, and this
// gate did not make it so. Pinned because docs/approvals.md states that boundary in prose:
// if a future migration defends it, this spec reddens and the prose gets corrected with it.
func TestGate_DirectStatusUpdateIsNotDefended(t *testing.T) {
	super, app := dbTestPools(t)

	g := seedGatedTenantAsAdmin(t, super, "APPR-08-10-DIRECT")
	invID := g.invoiceWith(t, super, "APPR-08-10-DIRECT-A", "open")

	store := NewStore(app, WithApprovalsEnforced(true))
	if _, err := store.Transition(g.ctx, invID, StatusQueued); !errors.Is(err, ErrAwaitingApproval) {
		t.Fatalf("setup: the door must refuse this invoice first: err = %v, want ErrAwaitingApproval", err)
	}

	const historySQL = `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`
	beforeHistory := mustCount(t, super, historySQL, invID)

	if err := db.WithinRequestTenantTx(g.ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(g.ctx, `UPDATE invoices SET status = 'queued' WHERE id = $1`, invID)
		return err
	}); err != nil {
		t.Fatalf("raw UPDATE as invoice_app: %v -- if this now FAILS the schema grew a defense and docs/approvals.md's scope statement is stale", err)
	}

	if s := statusOf(t, super, invID); s != StatusQueued {
		t.Errorf("stored status after the raw UPDATE = %q, want %q", s, StatusQueued)
	}
	if n := mustCount(t, super, historySQL, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d -- writing no history is exactly why this path is a scope boundary and not a supported one", n, beforeHistory)
	}
}
