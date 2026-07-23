// M5-04-02 (task-233): tests for internal/invoice's system-actor status
// transitions -- Store.MarkSubmittedTx/MarkFailedTx, the worker-callable
// twins of Store.Transition that write actor='system' instead of deriving it
// from a JWT identity in ctx. Written BEFORE the real implementation exists
// (RED against actor.go's not-implemented stub: MarkSubmittedTx/MarkFailedTx
// currently always return errActorNotImplemented, so every assertion below
// fails for that reason, not a compile error). Reuses the dbTestPools/
// seedTenant/seedEntity/seedInvoiceAtStatus/mustCount/auditCount/auditActor/
// pgCode harness from store_test.go/transition_adversarial_test.go (same
// package).
//
// Spec-to-test map (task-233 Test Specs table T02-1..T02-8):
//
//	T02-1 TestTransition_QueuedToFailedLegalityUnit (transition_test.go, beside
//	      TestTransition_ValidatedToDraftLegalityUnit)
//	T02-2 TestTransition_ExhaustiveMatrixLocksLegalEdgeTable, EXTENDED
//	      (transition_adversarial_test.go's wantLegalEdge oracle + counts)
//	T02-3 TestMarkFailedTx_NoIdentityInContextSucceeds
//	T02-4 TestMarkFailedTx_RecordsSystemActorInHistoryAndAudit
//	T02-5 TestMarkFailedTx_HistoryTenantScopedByRLS
//	T02-6 TestTransition_HTTPPathActorStaysJWTSubjectNotSystem
//	T02-7 TestMarkSubmittedTx_CalledTwiceIsIdempotentNoOp
//	T02-8 TestTransition_AtomicityRollsBackOnActorCheckFailure (transition_test.go:354,
//	      UNMODIFIED -- confirmed still green by this subtask, not re-authored here)
package invoice

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// T02-3: MarkFailedTx, called inside a db.WithinTenantTx whose ctx carries NO
// auth.Identity at all (the worker path -- no JWT, ever), must succeed on a
// queued invoice. Before this subtask, transitionTx's
// `callerID, _ := auth.IdentityFromContext(ctx)` silently defaults to the
// Identity zero value (Subject=""), and the invoice_status_history INSERT
// then trips `char_length(actor) > 0` (SQLSTATE 23514) -- this proves that
// failure mode is gone for the queued->failed edge.
func TestMarkFailedTx_NoIdentityInContextSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background() // deliberately no auth.WithIdentity
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T02-3 tenant")
	entityID := seedEntity(t, super, tenantID, "T02-3 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T02-3", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID)
		return err
	})
	if err != nil {
		t.Fatalf("MarkFailedTx with no identity in ctx: err = %v, want nil (queued->failed must succeed without a forged auth.Identity)", err)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusFailed {
		t.Errorf("invoice status after MarkFailedTx = %q, want %q", status, StatusFailed)
	}
}

// T02-4: the system actor is RECORDED, not blank -- the newest
// invoice_status_history row's actor and the invoice.transitioned audit
// row's actor are both literally "system" (SystemActor's Subject), never the
// empty string a bare zero-value Identity would have written pre-fix.
func TestMarkFailedTx_RecordsSystemActorInHistoryAndAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T02-4 tenant")
	entityID := seedEntity(t, super, tenantID, "T02-4 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T02-4", StatusQueued)

	beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID)
		return err
	})
	if err != nil {
		t.Fatalf("MarkFailedTx (system actor, queued->failed): %v", err)
	}

	var historyActor string
	if err := super.QueryRow(context.Background(),
		`SELECT actor FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`, invID,
	).Scan(&historyActor); err != nil {
		t.Fatalf("read newest history actor: %v", err)
	}
	if historyActor != "system" {
		t.Errorf("invoice_status_history.actor = %q, want %q", historyActor, "system")
	}

	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeAudit+1 {
		t.Fatalf("audit_log invoice.transitioned rows = %d, want %d (+1)", n, beforeAudit+1)
	}
	if got := auditActor(t, app, tenantID, "invoice.transitioned"); got != "system" {
		t.Errorf("audit_log.actor for invoice.transitioned = %q, want %q", got, "system")
	}
}

// T02-5: the history row's tenant_id is the tx's app.current_tenant GUC, not
// whatever SystemActor(tenantID) happened to carry -- and a caller that
// passes a tenantID mismatching the tx's own GUC is refused by RLS (42501),
// exactly like any other cross-tenant write attempt (invoice_status_history's
// tenant_isolation policy, migrations/20260714111246_invoice_status_history.sql:68,
// doubles USING as the INSERT WITH CHECK).
func TestMarkFailedTx_HistoryTenantScopedByRLS(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	t.Run("history tenant_id matches the tx GUC", func(t *testing.T) {
		tenantID := seedTenant(t, super, "T02-5a tenant")
		entityID := seedEntity(t, super, tenantID, "T02-5a entity")
		invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T02-5a", StatusQueued)

		err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			_, err := store.MarkFailedTx(ctx, tx, invID, tenantID)
			return err
		})
		if err != nil {
			t.Fatalf("MarkFailedTx (matching tenant): %v", err)
		}

		var histTenant string
		if err := super.QueryRow(context.Background(),
			`SELECT tenant_id FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`, invID,
		).Scan(&histTenant); err != nil {
			t.Fatalf("read newest history tenant_id: %v", err)
		}
		if histTenant != tenantID {
			t.Errorf("invoice_status_history.tenant_id = %q, want the tx GUC's tenant %q", histTenant, tenantID)
		}
	})

	t.Run("mismatched Actor.TenantID refused by RLS (42501)", func(t *testing.T) {
		tenantA := seedTenant(t, super, "T02-5b tenant A")
		tenantB := seedTenant(t, super, "T02-5b tenant B")
		entityA := seedEntity(t, super, tenantA, "T02-5b entity")
		invID := seedInvoiceAtStatus(t, super, tenantA, entityA, "T02-5b", StatusQueued)

		beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)

		// The tx's GUC is tenantA (via WithinTenantTx); the tenantID PARAMETER
		// passed to MarkFailedTx -- and hence SystemActor(tenantID).TenantID,
		// and hence the history INSERT's tenant_id value -- is tenantB. The row
		// itself is still visible/lockable under tenantA's GUC (it belongs to
		// tenantA), so this exercises the actor/GUC mismatch specifically, not
		// a "can't find the row" case.
		err := db.WithinTenantTx(ctx, app, tenantA, func(tx pgx.Tx) error {
			_, err := store.MarkFailedTx(ctx, tx, invID, tenantB)
			return err
		})
		if err == nil {
			t.Fatal("MarkFailedTx with a mismatched Actor.TenantID succeeded, want an RLS violation (SQLSTATE 42501)")
		}
		if code := pgCode(err); code != "42501" {
			t.Fatalf("MarkFailedTx with a mismatched Actor.TenantID: pgCode = %q, want 42501 (insufficient_privilege / RLS WITH CHECK): %v", code, err)
		}

		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
			t.Errorf("invoice_status_history rows after refused mismatched-tenant MarkFailedTx = %d, want unchanged %d", n, beforeHistory)
		}
	})
}

// T02-6: the HTTP path is UNTOUCHED by this subtask -- Store.Transition still
// writes actor = the JWT subject, never "system". A regression guard on
// Store.Transition/transitionTx's existing behaviour (already covered by
// INV-SM-07's TestTransition_HistoryAndAuditActorMatchCallerSubject); pinned
// here as its own case so it sits beside the system-actor tests it directly
// contrasts with, matching the Test Specs table 1:1.
func TestTransition_HTTPPathActorStaysJWTSubjectNotSystem(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T02-6 tenant")
	entityID := seedEntity(t, super, tenantID, "T02-6 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "T02-6"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition(draft->validated): %v", err)
	}

	var historyActor string
	if err := super.QueryRow(ctx,
		`SELECT actor FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`, inv.ID,
	).Scan(&historyActor); err != nil {
		t.Fatalf("read newest history actor: %v", err)
	}
	if historyActor != subject {
		t.Errorf("invoice_status_history.actor after Store.Transition = %q, want the JWT subject %q", historyActor, subject)
	}
	if historyActor == "system" {
		t.Errorf("invoice_status_history.actor = %q, the HTTP path must never write the system-actor label", historyActor)
	}
	if got := auditActor(t, app, tenantID, "invoice.transitioned"); got != subject {
		t.Errorf("audit_log.actor for invoice.transitioned = %q, want the JWT subject %q", got, subject)
	}
}

// T02-7: calling MarkSubmittedTx twice on the same invoice -- e.g. a replayed
// job after a crash between commit and the queue's ack -- succeeds both
// times and writes exactly ONE invoice_status_history row (to_status =
// submitted). The second call observes status already == submitted and must
// take the idempotent-no-op branch rather than re-attempting an illegal
// submitted->submitted self-edge or a second real transition.
func TestMarkSubmittedTx_CalledTwiceIsIdempotentNoOp(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T02-7 tenant")
	entityID := seedEntity(t, super, tenantID, "T02-7 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T02-7", StatusQueued)

	markOnce := func() error {
		return db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			_, err := store.MarkSubmittedTx(ctx, tx, invID, tenantID)
			return err
		})
	}

	if err := markOnce(); err != nil {
		t.Fatalf("first MarkSubmittedTx (queued->submitted): %v", err)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'submitted'`, invID); n != 1 {
		t.Fatalf("history rows (to_status=submitted) after first call = %d, want 1", n)
	}

	if err := markOnce(); err != nil {
		t.Fatalf("second MarkSubmittedTx (already submitted, must be an idempotent no-op): %v", err)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'submitted'`, invID); n != 1 {
		t.Errorf("history rows (to_status=submitted) after second (idempotent) call = %d, want still 1 (no second row)", n)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusSubmitted {
		t.Errorf("invoice status after two MarkSubmittedTx calls = %q, want %q", status, StatusSubmitted)
	}
}
