// M5-04-02 (task-233): QA Mode B adversarial coverage ON TOP OF T02-1..T02-8
// (system_actor_test.go), written during the post-implementation verify
// pass. Reuses the dbTestPools/seedTenant/seedEntity/seedInvoiceAtStatus/
// mustCount/pgCode harness from store_test.go/transition_adversarial_test.go
// (same package). system_actor_test.go itself is NOT modified.
//
// T02-1..T02-8 prove the new edge exists, the system actor is recorded
// correctly, the three pre-existing call sites are untouched, and the
// idempotent no-op takes no second write. They do NOT cover:
//
//  1. MarkFailedTx called on a status with NO legal edge to failed at all
//     (draft) -- must return ErrIllegalTransition, not silently succeed or
//     panic.
//  2. MarkSubmittedTx on a nonexistent id, and on a cross-tenant id (RLS
//     makes it invisible) -- both must be ErrNotFound, never a leak that
//     distinguishes "doesn't exist" from "exists but isn't yours".
//  3. A malformed (non-UUID) invoice id -- must be ErrValidation (the
//     22P02 mapping markTerminalTx shares with Store.Transition/Get/Update).
//  4. MarkFailedTx succeeding from BOTH the new edge (queued) and the
//     pre-existing edge (submitted) in the same test run -- proves the new
//     edge did not displace the old one.
//  5. The idempotent no-op's return value is the ACTUAL current invoice
//     (non-zero ID, real status), not a zero-value Invoke{} that would
//     silently corrupt a caller relying on the returned row.
//  6. Two MarkSubmittedTx calls racing on the SAME invoice in separate,
//     concurrently-open transactions -- T02-7 calls MarkSubmittedTx twice
//     SEQUENTIALLY, which would pass even without a real FOR UPDATE lock
//     (call order alone serializes it). This drives two goroutines that open
//     their own db.WithinTenantTx concurrently, to prove the row lock -- not
//     just Go's own call ordering -- is what prevents a duplicate history row.
package invoice

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// SA-ADV-1: MarkFailedTx on a draft invoice -- draft has no legal edge to
// failed (legalTransitions[StatusDraft] = {StatusValidated} only) -- must
// return ErrIllegalTransition, not succeed or panic, and must leave
// status/history untouched.
func TestMarkFailedTx_IllegalFromDraftReturnsErrIllegalTransition(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SA-ADV-1 tenant")
	entityID := seedEntity(t, super, tenantID, "SA-ADV-1 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "SA-ADV-1", StatusDraft)

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID)
		return err
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("MarkFailedTx(draft->failed): err = %v, want ErrIllegalTransition", err)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("status after illegal MarkFailedTx = %q, want unchanged %q", status, StatusDraft)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("history rows after illegal MarkFailedTx = %d, want unchanged %d", n, beforeHistory)
	}
}

// SA-ADV-2: MarkSubmittedTx on an id that does not exist at all -- ErrNotFound,
// mirroring Store.Transition's own nonexistent-id handling (the SELECT ...
// FOR UPDATE returns pgx.ErrNoRows).
func TestMarkSubmittedTx_NonexistentIDReturnsErrNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SA-ADV-2 tenant")

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkSubmittedTx(ctx, tx, "00000000-0000-0000-0000-000000000000", tenantID)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkSubmittedTx(nonexistent id): err = %v, want ErrNotFound", err)
	}
}

// SA-ADV-2b: MarkSubmittedTx on a REAL id that belongs to a different
// tenant -- RLS scopes the SELECT ... FOR UPDATE to the tx's own
// app.current_tenant GUC, so a cross-tenant row is invisible and comes back
// as the SAME pgx.ErrNoRows a genuinely nonexistent id would -- ErrNotFound,
// never a leak that would let a caller distinguish "not yours" from
// "doesn't exist".
func TestMarkSubmittedTx_CrossTenantIDReturnsErrNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantA := seedTenant(t, super, "SA-ADV-2b tenant A")
	tenantB := seedTenant(t, super, "SA-ADV-2b tenant B")
	entityA := seedEntity(t, super, tenantA, "SA-ADV-2b entity")
	invID := seedInvoiceAtStatus(t, super, tenantA, entityA, "SA-ADV-2b", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		_, err := store.MarkSubmittedTx(ctx, tx, invID, tenantB)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkSubmittedTx(cross-tenant id): err = %v, want ErrNotFound (never a leak)", err)
	}

	// The invoice itself must be untouched -- proves this was refused before
	// any write, not a successful write to the wrong tenant's copy.
	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusQueued {
		t.Errorf("status after cross-tenant MarkSubmittedTx = %q, want unchanged %q", status, StatusQueued)
	}
}

// SA-ADV-3: a malformed (non-UUID) invoice id maps to ErrValidation via the
// same 22P02 (invalid_text_representation) handling markTerminalTx shares
// with Store.Get/Update/Transition.
func TestMarkFailedTx_MalformedIDReturnsErrValidation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SA-ADV-3 tenant")

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, "not-a-uuid", tenantID)
		return err
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("MarkFailedTx(malformed id): err = %v, want ErrValidation", err)
	}
}

// SA-ADV-4: MarkFailedTx succeeds from BOTH the new edge (queued->failed,
// M5-04-02) and the pre-existing edge (submitted->failed, M4-05) in the SAME
// test run -- proves adding queued->failed to legalTransitions did not
// displace or otherwise disturb the submitted->failed edge it sits beside.
func TestMarkFailedTx_NewAndPreexistingEdgesBothSucceed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SA-ADV-4 tenant")
	entityID := seedEntity(t, super, tenantID, "SA-ADV-4 entity")

	fromQueued := seedInvoiceAtStatus(t, super, tenantID, entityID, "SA-ADV-4-queued", StatusQueued)
	fromSubmitted := seedInvoiceAtStatus(t, super, tenantID, entityID, "SA-ADV-4-submitted", StatusSubmitted)

	for _, tc := range []struct {
		label string
		id    string
		from  Status
	}{
		{"new edge (queued->failed)", fromQueued, StatusQueued},
		{"pre-existing edge (submitted->failed)", fromSubmitted, StatusSubmitted},
	} {
		t.Run(tc.label, func(t *testing.T) {
			err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
				_, err := store.MarkFailedTx(ctx, tx, tc.id, tenantID)
				return err
			})
			if err != nil {
				t.Fatalf("MarkFailedTx(%s): %v", tc.from, err)
			}

			var status string
			if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, tc.id).Scan(&status); err != nil {
				t.Fatalf("read back status: %v", err)
			}
			if Status(status) != StatusFailed {
				t.Errorf("status after MarkFailedTx from %q = %q, want %q", tc.from, status, StatusFailed)
			}

			var fromStatus string
			if err := super.QueryRow(context.Background(),
				`SELECT from_status FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'failed' ORDER BY changed_at DESC LIMIT 1`, tc.id,
			).Scan(&fromStatus); err != nil {
				t.Fatalf("read back history from_status: %v", err)
			}
			if Status(fromStatus) != tc.from {
				t.Errorf("history from_status = %q, want %q", fromStatus, tc.from)
			}
		})
	}
}

// SA-ADV-5: the idempotent no-op branch returns the ACTUAL current Invoice
// row (real ID, real Status), never a zero-value Invoice{} -- a caller that
// relies on the returned row (e.g. to re-derive a response or log a field
// from it) must not silently receive an empty struct on the redundant path.
func TestMarkSubmittedTx_IdempotentNoOpReturnsActualInvoiceNotZeroValue(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SA-ADV-5 tenant")
	entityID := seedEntity(t, super, tenantID, "SA-ADV-5 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "SA-ADV-5", StatusSubmitted)

	var inv Invoice
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		inv, err = store.MarkSubmittedTx(ctx, tx, invID, tenantID)
		return err
	})
	if err != nil {
		t.Fatalf("MarkSubmittedTx (already submitted, idempotent no-op): %v", err)
	}

	if inv.ID != invID {
		t.Errorf("idempotent no-op returned Invoice.ID = %q, want %q (not a zero-value Invoice{})", inv.ID, invID)
	}
	if inv.Status != StatusSubmitted {
		t.Errorf("idempotent no-op returned Invoice.Status = %q, want %q", inv.Status, StatusSubmitted)
	}
	if inv.EntityID != entityID {
		t.Errorf("idempotent no-op returned Invoice.EntityID = %q, want %q", inv.EntityID, entityID)
	}
}

// SA-ADV-6: two MarkSubmittedTx calls racing on the SAME invoice in
// separate, concurrently-open transactions -- unlike T02-7 (which calls
// MarkSubmittedTx twice SEQUENTIALLY, an ordering that would pass even with
// no real lock at all), this actually exercises the FOR UPDATE row lock:
// one goroutine's transaction blocks on the lock until the other commits,
// then observes status already == submitted and takes the idempotent
// no-op branch. Both calls must succeed (nil error) and exactly ONE history
// row (to_status=submitted) must exist afterward -- a lock that failed to
// serialize would let both goroutines read status=queued concurrently and
// each attempt a real transitionTx, producing two history rows (or one
// erroring on a stale read of a status that changed under it).
func TestMarkSubmittedTx_ConcurrentCallsSerializeToOneHistoryRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SA-ADV-6 tenant")
	entityID := seedEntity(t, super, tenantID, "SA-ADV-6 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "SA-ADV-6", StatusQueued)

	const n = 4
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
				_, err := store.MarkSubmittedTx(ctx, tx, invID, tenantID)
				return err
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent MarkSubmittedTx[%d]: err = %v, want nil (winner transitions, losers take the idempotent no-op)", i, err)
		}
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusSubmitted {
		t.Errorf("status after concurrent MarkSubmittedTx = %q, want %q", status, StatusSubmitted)
	}
	if hn := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'submitted'`, invID); hn != 1 {
		t.Errorf("history rows (to_status=submitted) = %d, want exactly 1 (FOR UPDATE serialized the race)", hn)
	}
}
