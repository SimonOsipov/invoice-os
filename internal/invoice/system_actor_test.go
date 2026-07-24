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
//
// M5-05-03 (task-239) QA Mode A adds the store-layer half of its own Test
// Specs table below -- MarkAcceptedTx/MarkRejectedTx, the outcome-writing
// twins of MarkSubmittedTx/MarkFailedTx. Written BEFORE the real
// implementation exists (RED against actor.go's Stage 2.5 stubs, which
// always return errOutcomeNotImplemented and write nothing: every assertion
// below fails for that reason, not a compile error).
//
//	TestMarkAcceptedTx_WritesOutcomeAndTransitionsInOneTx
//	TestMarkRejectedTx_WritesReasonsAndTransitionsInOneTx
//	TestMarkAcceptedTx_BlankIRNRaises23514AndWritesNothing
//	TestMarkAcceptedTx_BlankCSIDAndQRBecomeNull
//	TestMarkRejectedTx_NilReasonsStoresEmptyArrayNotJSONNull
//	TestMarkAcceptedTx_IdempotentReplayDoesNotRewriteOutcomeOrHistory
//	TestMarkRejectedTx_IllegalFromDraftReturnsErrIllegalTransition
//	TestRLS_MarkAcceptedTxCrossTenantIsNotFound
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

// --- M5-05-03 (task-239) -- MarkAcceptedTx/MarkRejectedTx store-layer RED tests ---------

// TestMarkAcceptedTx_WritesOutcomeAndTransitionsInOneTx (task-239's own Test
// Specs table, AC#1/#2): MarkAcceptedTx moves an invoice to accepted from
// EITHER legal source state -- queued and, in a separate subtest, submitted
// (both edges are legal, legalTransitions store.go:661-662) -- writing
// irn/csid/qr_payload verbatim on the SAME tx as the transition, plus
// exactly one invoice_status_history row (actor "system") and one
// invoice.transitioned audit row. Asserted via BOTH the returned Invoice and
// a superuser re-read of the columns. Modelled on
// TestMarkFailedTx_RecordsSystemActorInHistoryAndAudit's history/audit shape
// above.
//
// RED today: MarkAcceptedTx is a Stage 2.5 stub (actor.go) that
// unconditionally returns errOutcomeNotImplemented and writes nothing --
// this fails on that sentinel error, not a compile error.
func TestMarkAcceptedTx_WritesOutcomeAndTransitionsInOneTx(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	cases := []struct {
		name string
		from Status
	}{
		{"queued source", StatusQueued},
		{"submitted source", StatusSubmitted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := seedTenant(t, super, "T03-1 tenant "+tc.name)
			entityID := seedEntity(t, super, tenantID, "T03-1 entity")
			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-1-"+string(tc.from), tc.from)

			beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

			irn, csid, qrPayload := "IRN-T03-1", "CSID-T03-1", "QR-T03-1"
			var got Invoice
			err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
				var err error
				got, err = store.MarkAcceptedTx(ctx, tx, invID, tenantID, irn, csid, qrPayload)
				return err
			})
			if err != nil {
				t.Fatalf("MarkAcceptedTx (%s->accepted): %v (want nil)", tc.from, err)
			}

			if got.Status != StatusAccepted {
				t.Errorf("returned Invoice.Status = %q, want %q", got.Status, StatusAccepted)
			}
			if got.IRN == nil || *got.IRN != irn {
				t.Errorf("returned Invoice.IRN = %v, want %q", got.IRN, irn)
			}
			if got.CSID == nil || *got.CSID != csid {
				t.Errorf("returned Invoice.CSID = %v, want %q", got.CSID, csid)
			}
			if got.QRPayload == nil || *got.QRPayload != qrPayload {
				t.Errorf("returned Invoice.QRPayload = %v, want %q", got.QRPayload, qrPayload)
			}

			var status string
			var gotIRN, gotCSID, gotQR *string
			if err := super.QueryRow(context.Background(),
				`SELECT status, irn, csid, qr_payload FROM invoices WHERE id = $1`, invID,
			).Scan(&status, &gotIRN, &gotCSID, &gotQR); err != nil {
				t.Fatalf("read back invoice row: %v", err)
			}
			if Status(status) != StatusAccepted {
				t.Errorf("invoice status = %q, want %q", status, StatusAccepted)
			}
			if gotIRN == nil || *gotIRN != irn {
				t.Errorf("irn = %v, want %q", gotIRN, irn)
			}
			if gotCSID == nil || *gotCSID != csid {
				t.Errorf("csid = %v, want %q", gotCSID, csid)
			}
			if gotQR == nil || *gotQR != qrPayload {
				t.Errorf("qr_payload = %v, want %q", gotQR, qrPayload)
			}

			if n := mustCount(t, super,
				`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = $2 AND to_status = 'accepted'`,
				invID, string(tc.from),
			); n != 1 {
				t.Errorf("invoice_status_history rows (%s->accepted) = %d, want 1", tc.from, n)
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
				t.Errorf("audit_log invoice.transitioned rows = %d, want %d (+1)", n, beforeAudit+1)
			}
			if got := auditActor(t, app, tenantID, "invoice.transitioned"); got != "system" {
				t.Errorf("audit_log.actor for invoice.transitioned = %q, want %q", got, "system")
			}
		})
	}
}

// TestMarkRejectedTx_WritesReasonsAndTransitionsInOneTx (task-239's Test
// Specs table, AC#2): MarkRejectedTx moves an invoice to rejected from
// EITHER legal source state -- queued and submitted -- writing
// rejection_reasons as a jsonb array matching the input []submission.Reason
// verbatim (order + content), on the SAME tx as the transition.
//
// RED today: MarkRejectedTx is a Stage 2.5 stub that unconditionally returns
// errOutcomeNotImplemented and writes nothing.
func TestMarkRejectedTx_WritesReasonsAndTransitionsInOneTx(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	reasons := []submission.Reason{
		{Code: "APP-ERR-0417", Message: "Supplier TIN not registered", Path: "supplier_tin"},
		{Code: "APP-ERR-0501", Message: "Currency mismatch"},
	}

	cases := []struct {
		name string
		from Status
	}{
		{"queued source", StatusQueued},
		{"submitted source", StatusSubmitted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := seedTenant(t, super, "T03-2 tenant "+tc.name)
			entityID := seedEntity(t, super, tenantID, "T03-2 entity")
			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-2-"+string(tc.from), tc.from)

			var got Invoice
			err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
				var err error
				got, err = store.MarkRejectedTx(ctx, tx, invID, tenantID, reasons)
				return err
			})
			if err != nil {
				t.Fatalf("MarkRejectedTx (%s->rejected): %v (want nil)", tc.from, err)
			}
			if got.Status != StatusRejected {
				t.Errorf("returned Invoice.Status = %q, want %q", got.Status, StatusRejected)
			}

			var status, reasonsText string
			if err := super.QueryRow(context.Background(),
				`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
			).Scan(&status, &reasonsText); err != nil {
				t.Fatalf("read back invoice row: %v", err)
			}
			if Status(status) != StatusRejected {
				t.Errorf("invoice status = %q, want %q", status, StatusRejected)
			}

			var gotReasons []submission.Reason
			if err := json.Unmarshal([]byte(reasonsText), &gotReasons); err != nil {
				t.Fatalf("unmarshal rejection_reasons: %v (raw = %s)", err, reasonsText)
			}
			if !reflect.DeepEqual(gotReasons, reasons) {
				t.Errorf("rejection_reasons = %+v, want %+v (order + content must match verbatim)", gotReasons, reasons)
			}

			if n := mustCount(t, super,
				`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = $2 AND to_status = 'rejected'`,
				invID, string(tc.from),
			); n != 1 {
				t.Errorf("invoice_status_history rows (%s->rejected) = %d, want 1", tc.from, n)
			}
		})
	}
}

// TestMarkAcceptedTx_BlankIRNRaises23514AndWritesNothing (task-239 AC#4): irn
// binds RAW, never NULLIF'd -- a blank IRN trips
// invoices' `CHECK (irn IS NULL OR char_length(irn) > 0)`
// (migrations/20260722083015_invoices_fiscal_outcome.sql), raising SQLSTATE
// 23514, and the whole write (including the transition) must not land.
func TestMarkAcceptedTx_BlankIRNRaises23514AndWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T03-3 tenant")
	entityID := seedEntity(t, super, tenantID, "T03-3 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-3", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "", "CSID-T03-3", "QR-T03-3")
		return err
	})
	if err == nil {
		t.Fatal("MarkAcceptedTx with blank IRN succeeded, want a CHECK violation (SQLSTATE 23514, irn is bound RAW, never NULLIF'd)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("MarkAcceptedTx with blank IRN: pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	var status string
	var gotIRN *string
	if err := super.QueryRow(context.Background(),
		`SELECT status, irn FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &gotIRN); err != nil {
		t.Fatalf("read back invoice row: %v", err)
	}
	if Status(status) != StatusQueued {
		t.Errorf("invoice status after refused blank-IRN write = %q, want unchanged %q", status, StatusQueued)
	}
	if gotIRN != nil {
		t.Errorf("irn after refused blank-IRN write = %q, want still NULL", *gotIRN)
	}
}

// TestMarkAcceptedTx_BlankCSIDAndQRBecomeNull (task-239 AC#4): unlike IRN,
// csid/qr_payload bind via NULLIF($n, '') -- a blank string means "the
// authority returned none" (result.go's own doc on submission.Accepted) and
// must land as SQL NULL, not the empty string.
func TestMarkAcceptedTx_BlankCSIDAndQRBecomeNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T03-4 tenant")
	entityID := seedEntity(t, super, tenantID, "T03-4 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-4", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "IRN-T03-4", "", "")
		return err
	})
	if err != nil {
		t.Fatalf("MarkAcceptedTx (blank csid/qr_payload): %v (want nil)", err)
	}

	var gotCSID, gotQR *string
	if err := super.QueryRow(context.Background(),
		`SELECT csid, qr_payload FROM invoices WHERE id = $1`, invID,
	).Scan(&gotCSID, &gotQR); err != nil {
		t.Fatalf("read back invoice row: %v", err)
	}
	if gotCSID != nil {
		t.Errorf("csid = %q, want SQL NULL (blank input must NULLIF, not store '')", *gotCSID)
	}
	if gotQR != nil {
		t.Errorf("qr_payload = %q, want SQL NULL", *gotQR)
	}
}

// TestMarkRejectedTx_NilReasonsStoresEmptyArrayNotJSONNull (task-239 AC#5,
// the M4-16 write-side trap): a nil Go slice marshals to JSON `null`, which
// binds successfully to a `jsonb NOT NULL` column and poisons it --
// MarkRejectedTx must normalise nil/empty reasons to the literal `[]`
// before the write.
func TestMarkRejectedTx_NilReasonsStoresEmptyArrayNotJSONNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T03-5 tenant")
	entityID := seedEntity(t, super, tenantID, "T03-5 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-5", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, nil) // nil Go slice, deliberately
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx (nil reasons): %v (want nil)", err)
	}

	var reasonsText, typeofResult string
	if err := super.QueryRow(context.Background(),
		`SELECT rejection_reasons::text, jsonb_typeof(rejection_reasons) FROM invoices WHERE id = $1`, invID,
	).Scan(&reasonsText, &typeofResult); err != nil {
		t.Fatalf("read back rejection_reasons: %v", err)
	}
	if reasonsText != "[]" {
		t.Errorf("rejection_reasons::text = %q, want %q (a nil Go slice must normalise to the empty array, never JSON null)", reasonsText, "[]")
	}
	if typeofResult != "array" {
		t.Errorf("jsonb_typeof(rejection_reasons) = %q, want %q", typeofResult, "array")
	}
}

// TestMarkAcceptedTx_IdempotentReplayDoesNotRewriteOutcomeOrHistory
// (task-239 AC#6): calling MarkAcceptedTx twice on the same invoice -- e.g.
// a replayed job after a crash between commit and the queue's ack -- must
// take the idempotent no-op branch on the second call: no second history
// row, and the ALREADY-STORED irn/csid/qr_payload must not be clobbered by
// whatever the second call's (different) arguments were. Modelled on
// TestMarkSubmittedTx_CalledTwiceIsIdempotentNoOp above.
func TestMarkAcceptedTx_IdempotentReplayDoesNotRewriteOutcomeOrHistory(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T03-6 tenant")
	entityID := seedEntity(t, super, tenantID, "T03-6 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-6", StatusQueued)

	markOnce := func(irn, csid, qr string) error {
		return db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, irn, csid, qr)
			return err
		})
	}

	if err := markOnce("IRN-FIRST", "CSID-FIRST", "QR-FIRST"); err != nil {
		t.Fatalf("first MarkAcceptedTx (queued->accepted): %v", err)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'accepted'`, invID); n != 1 {
		t.Fatalf("history rows (to_status=accepted) after first call = %d, want 1", n)
	}

	// Second call, DIFFERENT irn/csid/qr -- a broken implementation that
	// skips the idempotent short-circuit would clobber the stored outcome.
	if err := markOnce("IRN-SECOND-SHOULD-NOT-LAND", "CSID-SECOND", "QR-SECOND"); err != nil {
		t.Fatalf("second MarkAcceptedTx (already accepted, must be an idempotent no-op): %v", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'accepted'`, invID); n != 1 {
		t.Errorf("history rows (to_status=accepted) after second (idempotent) call = %d, want still 1 (no second row)", n)
	}

	var status string
	var gotIRN, gotCSID, gotQR *string
	if err := super.QueryRow(context.Background(),
		`SELECT status, irn, csid, qr_payload FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &gotIRN, &gotCSID, &gotQR); err != nil {
		t.Fatalf("read back invoice row: %v", err)
	}
	if Status(status) != StatusAccepted {
		t.Errorf("invoice status after two MarkAcceptedTx calls = %q, want %q", status, StatusAccepted)
	}
	if gotIRN == nil || *gotIRN != "IRN-FIRST" {
		t.Errorf("irn after idempotent replay = %v, want unchanged %q (the outcome must not be rewritten)", gotIRN, "IRN-FIRST")
	}
	if gotCSID == nil || *gotCSID != "CSID-FIRST" {
		t.Errorf("csid after idempotent replay = %v, want unchanged %q", gotCSID, "CSID-FIRST")
	}
	if gotQR == nil || *gotQR != "QR-FIRST" {
		t.Errorf("qr_payload after idempotent replay = %v, want unchanged %q", gotQR, "QR-FIRST")
	}
}

// TestMarkRejectedTx_IllegalFromDraftReturnsErrIllegalTransition
// (task-239 AC#7's "sole-sequence gate" corollary): markTerminalTx's shared
// legality guard still applies to the new outcome writers -- there is no
// draft->rejected edge (legalTransitions store.go:659), so MarkRejectedTx on
// a draft invoice must return ErrIllegalTransition and leave the row
// completely unchanged (status AND rejection_reasons), because the outcome
// write and the transition share one tx: the caller's db.WithinTenantTx
// rolls back the whole attempt on a non-nil return.
func TestMarkRejectedTx_IllegalFromDraftReturnsErrIllegalTransition(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T03-7 tenant")
	entityID := seedEntity(t, super, tenantID, "T03-7 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-7", StatusDraft)

	reasons := []submission.Reason{{Code: "APP-ERR-0000", Message: "should never land"}}

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, reasons)
		return err
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("MarkRejectedTx (draft->rejected): err = %v, want ErrIllegalTransition (there is no draft->rejected edge)", err)
	}

	var status, reasonsText string
	if err := super.QueryRow(context.Background(),
		`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &reasonsText); err != nil {
		t.Fatalf("read back invoice row: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("invoice status after refused illegal MarkRejectedTx = %q, want unchanged %q", status, StatusDraft)
	}
	if reasonsText != "[]" {
		t.Errorf("rejection_reasons after refused illegal MarkRejectedTx = %q, want unchanged default %q", reasonsText, "[]")
	}
}

// TestRLS_MarkAcceptedTxCrossTenantIsNotFound (task-239 Test Specs table --
// "the port is RLS-scoped like its four siblings"): both MarkAcceptedTx and
// MarkRejectedTx, driven from tenant B's tx against tenant A's invoice id,
// must fail closed via RLS's 0-rows -> pgx.ErrNoRows -> ErrNotFound mapping
// (markTerminalTx's existing behaviour), writing nothing.
func TestRLS_MarkAcceptedTxCrossTenantIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantA := seedTenant(t, super, "T03-8 tenant A")
	tenantB := seedTenant(t, super, "T03-8 tenant B")
	entityA := seedEntity(t, super, tenantA, "T03-8 entity A")
	invoiceA := seedInvoiceAtStatus(t, super, tenantA, entityA, "T03-8", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invoiceA, tenantB, "IRN-T03-8", "CSID-T03-8", "QR-T03-8")
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkAcceptedTx (tenant B tx, tenant A's invoice): err = %v, want ErrNotFound (RLS must 0-row this)", err)
	}

	var status string
	var gotIRN *string
	if err := super.QueryRow(context.Background(),
		`SELECT status, irn FROM invoices WHERE id = $1`, invoiceA,
	).Scan(&status, &gotIRN); err != nil {
		t.Fatalf("read back invoice row: %v", err)
	}
	if Status(status) != StatusQueued {
		t.Errorf("invoice status after refused cross-tenant MarkAcceptedTx = %q, want unchanged %q", status, StatusQueued)
	}
	if gotIRN != nil {
		t.Errorf("irn after refused cross-tenant MarkAcceptedTx = %q, want still NULL", *gotIRN)
	}

	reasons := []submission.Reason{{Code: "APP-ERR-0000", Message: "should never land"}}
	err = db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invoiceA, tenantB, reasons)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkRejectedTx (tenant B tx, tenant A's invoice): err = %v, want ErrNotFound (RLS must 0-row this)", err)
	}

	var reasonsText string
	if err := super.QueryRow(context.Background(),
		`SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invoiceA,
	).Scan(&reasonsText); err != nil {
		t.Fatalf("read back rejection_reasons: %v", err)
	}
	if reasonsText != "[]" {
		t.Errorf("rejection_reasons after refused cross-tenant MarkRejectedTx = %q, want unchanged default %q", reasonsText, "[]")
	}
}

// --- QA Mode B (task-239) -- adversarial / edge coverage added AFTER the RED
// specs above, during the post-implementation verify pass. ------------------
//
// Closes four gaps the RED Test Specs table left open:
//
//  1. TestMarkAcceptedTx_WritesOutcomeAndTransitionsInOneTx and
//     TestMarkRejectedTx_WritesReasonsAndTransitionsInOneTx already prove the
//     submitted->{accepted,rejected} edges (their "submitted source"
//     subtests) -- so the PollWorker-path edges are already covered and are
//     NOT re-tested here.
//  2. TestMarkRejectedTx_IllegalFromDraftReturnsErrIllegalTransition only
//     asserts rejection_reasons at the column level on an illegal source --
//     there was no equivalent column-level assertion on the ACCEPT side
//     (irn/csid/qr_payload). TestMarkAcceptedTx_IllegalFromDraftReturnsErrIllegalTransitionAndWritesNothing
//     closes that.
//  3. The existing round-trip test (TestMarkRejectedTx_WritesReasonsAndTransitionsInOneTx)
//     unmarshals rejection_reasons back into []submission.Reason and compares
//     via reflect.DeepEqual -- that passes whether or not Reason.Path's
//     `omitempty` actually elides the JSON key for a zero-value Path (both
//     an omitted key and a `"path":""` key unmarshal to Path=="").
//     TestMarkRejectedTx_ReasonsRoundTripExactJSONKeyShape inspects the RAW
//     jsonb text instead, proving the omitted-Path element truly has no
//     "path" key at all, not merely an empty one.
//  4. Two cross-subtask compositions that only exist once BOTH halves are
//     real: TestStoreEdit_ComposesWithRealMarkRejectedTxWrite drives 03's
//     MarkRejectedTx (real write, not a raw SQL seed) followed by 01's
//     Store.Edit demotion, proving the two halves compose; and
//     TestHasFiscalOutcome_GoesLiveAfterMarkAcceptedTx drives 03's real
//     MarkAcceptedTx followed by submission_port.go's HasFiscalOutcome
//     (dead code before 03 shipped -- irn was never written by anything),
//     proving it now reflects reality end to end through production code on
//     both sides.

// TestMarkAcceptedTx_IllegalFromDraftReturnsErrIllegalTransitionAndWritesNothing
// mirrors TestMarkRejectedTx_IllegalFromDraftReturnsErrIllegalTransition
// above, but on the accept side: draft has no legal edge to accepted
// (legalTransitions store.go:661 only reaches accepted from queued/
// submitted), so the outcome UPDATE inside markTerminalTx's outcome
// callback runs, then transitionTx refuses the illegal (draft, accepted)
// pair -- the whole attempt must roll back together (same tx), leaving
// irn/csid/qr_payload ALL still NULL, not just the status unchanged.
func TestMarkAcceptedTx_IllegalFromDraftReturnsErrIllegalTransitionAndWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "QA-239-1 tenant")
	entityID := seedEntity(t, super, tenantID, "QA-239-1 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "QA-239-1", StatusDraft)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "IRN-QA-239-1", "CSID-QA-239-1", "QR-QA-239-1")
		return err
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("MarkAcceptedTx (draft->accepted): err = %v, want ErrIllegalTransition", err)
	}

	var status string
	var gotIRN, gotCSID, gotQR *string
	if err := super.QueryRow(context.Background(),
		`SELECT status, irn, csid, qr_payload FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &gotIRN, &gotCSID, &gotQR); err != nil {
		t.Fatalf("read back invoice row: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("invoice status after refused illegal MarkAcceptedTx = %q, want unchanged %q", status, StatusDraft)
	}
	if gotIRN != nil {
		t.Errorf("irn after refused illegal MarkAcceptedTx = %q, want still NULL (the outcome UPDATE must roll back with the failed transition)", *gotIRN)
	}
	if gotCSID != nil {
		t.Errorf("csid after refused illegal MarkAcceptedTx = %q, want still NULL", *gotCSID)
	}
	if gotQR != nil {
		t.Errorf("qr_payload after refused illegal MarkAcceptedTx = %q, want still NULL", *gotQR)
	}
}

// TestMarkRejectedTx_ReasonsRoundTripExactJSONKeyShape (task-239 AC#5's
// omitempty corollary): a Reason with Path set must marshal three keys
// (code/message/path); a Reason with Path left at its zero value must
// marshal exactly TWO keys -- "path" must be ABSENT from the jsonb, not
// present as "". Inspects the raw jsonb text/keys directly rather than
// round-tripping through []submission.Reason, which cannot distinguish
// "path omitted" from "path present as empty string" (both unmarshal to
// Path=="").
func TestMarkRejectedTx_ReasonsRoundTripExactJSONKeyShape(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "QA-239-2 tenant")
	entityID := seedEntity(t, super, tenantID, "QA-239-2 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "QA-239-2", StatusQueued)

	reasons := []submission.Reason{
		{Code: "APP-ERR-0417", Message: "Supplier TIN not registered", Path: "supplier_tin"},
		{Code: "APP-ERR-0501", Message: "Currency mismatch"}, // Path deliberately left zero-value
	}

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, reasons)
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx: %v (want nil)", err)
	}

	var reasonsText string
	if err := super.QueryRow(context.Background(),
		`SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&reasonsText); err != nil {
		t.Fatalf("read back rejection_reasons: %v", err)
	}

	var elements []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(reasonsText), &elements); err != nil {
		t.Fatalf("unmarshal rejection_reasons into raw key maps: %v (raw = %s)", err, reasonsText)
	}
	if len(elements) != 2 {
		t.Fatalf("rejection_reasons has %d elements, want 2 (raw = %s)", len(elements), reasonsText)
	}

	if len(elements[0]) != 3 {
		t.Errorf("element[0] keys = %v (%d keys), want exactly 3 (code, message, path)", keysOf(elements[0]), len(elements[0]))
	}
	if pathVal, ok := elements[0]["path"]; !ok {
		t.Errorf("element[0] is missing the \"path\" key entirely, want present with value %q", "supplier_tin")
	} else if string(pathVal) != `"supplier_tin"` {
		t.Errorf("element[0][\"path\"] = %s, want %q", pathVal, `"supplier_tin"`)
	}

	if len(elements[1]) != 2 {
		t.Errorf("element[1] keys = %v (%d keys), want exactly 2 (code, message)", keysOf(elements[1]), len(elements[1]))
	}
	if _, ok := elements[1]["path"]; ok {
		t.Errorf("element[1] has a \"path\" key = %s, want ABSENT entirely (omitempty on a zero-value Path must elide the key, not emit \"\")", elements[1]["path"])
	}
}

// TestMarkRejectedTx_IdempotentReplayDoesNotRewriteReasons (task-239 AC#6's
// missing reject-side half): AC#6 requires BOTH MarkAcceptedTx and
// MarkRejectedTx to be idempotent no-ops that do not rewrite the stored
// outcome. TestMarkAcceptedTx_IdempotentReplayDoesNotRewriteOutcomeOrHistory
// above proves the accept side; MarkRejectedTx shares the exact same
// markTerminalTx sequence, but nothing directly proved the reject side until
// now -- a second MarkRejectedTx call with DIFFERENT reasons must not
// clobber the already-stored rejection_reasons.
func TestMarkRejectedTx_IdempotentReplayDoesNotRewriteReasons(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "QA-239-5 tenant")
	entityID := seedEntity(t, super, tenantID, "QA-239-5 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "QA-239-5", StatusQueued)

	markOnce := func(reasons []submission.Reason) error {
		return db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, reasons)
			return err
		})
	}

	firstReasons := []submission.Reason{{Code: "APP-ERR-FIRST", Message: "first reasons"}}
	if err := markOnce(firstReasons); err != nil {
		t.Fatalf("first MarkRejectedTx (queued->rejected): %v", err)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'rejected'`, invID); n != 1 {
		t.Fatalf("history rows (to_status=rejected) after first call = %d, want 1", n)
	}

	// Second call, DIFFERENT reasons -- a broken implementation that skips
	// the idempotent short-circuit would clobber the stored reasons.
	secondReasons := []submission.Reason{{Code: "APP-ERR-SECOND-SHOULD-NOT-LAND", Message: "second reasons"}}
	if err := markOnce(secondReasons); err != nil {
		t.Fatalf("second MarkRejectedTx (already rejected, must be an idempotent no-op): %v", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'rejected'`, invID); n != 1 {
		t.Errorf("history rows (to_status=rejected) after second (idempotent) call = %d, want still 1 (no second row)", n)
	}

	var status, reasonsText string
	if err := super.QueryRow(context.Background(),
		`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &reasonsText); err != nil {
		t.Fatalf("read back invoice row: %v", err)
	}
	if Status(status) != StatusRejected {
		t.Errorf("invoice status after two MarkRejectedTx calls = %q, want %q", status, StatusRejected)
	}

	var gotReasons []submission.Reason
	if err := json.Unmarshal([]byte(reasonsText), &gotReasons); err != nil {
		t.Fatalf("unmarshal rejection_reasons: %v (raw = %s)", err, reasonsText)
	}
	if !reflect.DeepEqual(gotReasons, firstReasons) {
		t.Errorf("rejection_reasons after idempotent replay = %+v, want unchanged %+v (the second call's DIFFERENT reasons must not land)", gotReasons, firstReasons)
	}
}

// keysOf is a small t.Errorf formatting helper for
// TestMarkRejectedTx_ReasonsRoundTripExactJSONKeyShape above.
func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestStoreEdit_ComposesWithRealMarkRejectedTxWrite (cross-subtask
// integration, task-239 + task-236/M5-05-01): MarkRejectedTx (03) writes
// rejection_reasons via the REAL production write path (not a raw SQL
// seed, unlike every other Edit-clears-reasons test in edit_test.go), then
// Store.Edit (01) demotes rejected->draft on a content change and clears
// them -- proving 03's write and 01's clear compose correctly end to end,
// through two independently-shipped subtasks' production code.
func TestStoreEdit_ComposesWithRealMarkRejectedTxWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "QA-239-3 tenant")
	entityID := seedEntity(t, super, tenantID, "QA-239-3 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "QA-239-3", StatusQueued)

	reasons := []submission.Reason{{Code: "APP-ERR-0417", Message: "Supplier TIN not registered", Path: "supplier_tin"}}
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, reasons)
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx (queued->rejected, real write): %v (want nil)", err)
	}

	// Sanity: the real write actually landed non-empty before Edit touches it.
	var seededReasons string
	if err := super.QueryRow(context.Background(),
		`SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&seededReasons); err != nil {
		t.Fatalf("read back rejection_reasons after MarkRejectedTx: %v", err)
	}
	if seededReasons == "[]" {
		t.Fatalf("rejection_reasons after MarkRejectedTx = %q, want non-empty (the real write must have landed before Edit's clear is a meaningful test)", seededReasons)
	}

	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	newVAT := "9.50"
	got, err := store.Edit(c, invID, UpdateInput{VAT: &newVAT})
	if err != nil {
		t.Fatalf("Edit (content change on rejected invoice): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted)", got.Status, StatusDraft)
	}

	var dbStatus, clearedReasons string
	if err := super.QueryRow(context.Background(),
		`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&dbStatus, &clearedReasons); err != nil {
		t.Fatalf("read back status/rejection_reasons after Edit: %v", err)
	}
	if Status(dbStatus) != StatusDraft {
		t.Errorf("invoices.status after Edit = %q, want %q", dbStatus, StatusDraft)
	}
	if clearedReasons != "[]" {
		t.Errorf("invoices.rejection_reasons after Edit = %q, want %q (03's real write cleared by 01's real demotion)", clearedReasons, "[]")
	}

	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'queued' AND to_status = 'rejected'`,
		invID,
	); n != 1 {
		t.Errorf("history rows (queued->rejected, from MarkRejectedTx) = %d, want 1", n)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'rejected' AND to_status = 'draft'`,
		invID,
	); n != 1 {
		t.Errorf("history rows (rejected->draft, from Edit) = %d, want 1", n)
	}
}

// TestHasFiscalOutcome_GoesLiveAfterMarkAcceptedTx: submission_port.go's
// HasFiscalOutcome (irn IS NOT NULL) was dead code before this subtask --
// nothing wrote irn. Drives BOTH real production methods (never a raw SQL
// seed): false before MarkAcceptedTx, true immediately after, on the same
// invoice.
func TestHasFiscalOutcome_GoesLiveAfterMarkAcceptedTx(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "QA-239-4 tenant")
	entityID := seedEntity(t, super, tenantID, "QA-239-4 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "QA-239-4", StatusQueued)

	var before bool
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		before, err = store.HasFiscalOutcome(ctx, tx, invID)
		return err
	})
	if err != nil {
		t.Fatalf("HasFiscalOutcome (before MarkAcceptedTx): %v (want nil)", err)
	}
	if before {
		t.Fatalf("HasFiscalOutcome (before MarkAcceptedTx) = true, want false (nothing has written irn yet)")
	}

	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "IRN-QA-239-4", "CSID-QA-239-4", "QR-QA-239-4")
		return err
	})
	if err != nil {
		t.Fatalf("MarkAcceptedTx: %v (want nil)", err)
	}

	var after bool
	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		after, err = store.HasFiscalOutcome(ctx, tx, invID)
		return err
	})
	if err != nil {
		t.Fatalf("HasFiscalOutcome (after MarkAcceptedTx): %v (want nil)", err)
	}
	if !after {
		t.Errorf("HasFiscalOutcome (after MarkAcceptedTx) = false, want true (irn was just written -- HasFiscalOutcome must reflect it, no longer dead code)")
	}
}
