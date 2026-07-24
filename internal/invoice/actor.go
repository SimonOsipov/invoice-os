package invoice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// Actor identifies who is driving a status transition: either the verified
// JWT caller (the HTTP path, via actorFromContext) or the background worker
// (M5-04's submission worker, via SystemActor). A struct rather than a bare
// Subject string because the invoice_status_history INSERT binds BOTH
// tenant_id and actor -- a Subject-only parameter would have to re-derive
// TenantID from somewhere else anyway, and could then only ever disagree
// with the tenant_id beside it [Stage-1 F3]. transitionTx (store.go) takes
// this as its actor parameter.
type Actor struct {
	TenantID string
	Subject  string
}

// actorFromContext derives the caller's Actor from the verified JWT identity
// in ctx -- byte-identical to transitionTx's pre-M5-04-02 inline
// `callerID, _ := auth.IdentityFromContext(ctx)`: the bool is still ignored,
// so a request path with no identity in ctx still yields a zero-value Actor
// (empty TenantID/Subject), exactly as before this refactor. This is the
// actor every one of transitionTx's three pre-existing HTTP-path call sites
// (Store.Edit's demotion, Store.Transition, Store.ApplyValidation) now
// passes, keeping their observable behaviour unchanged.
func actorFromContext(ctx context.Context) Actor {
	id, _ := auth.IdentityFromContext(ctx)
	return Actor{TenantID: id.TenantID, Subject: id.Subject}
}

// SystemActor is the background-worker identity for MarkSubmittedTx/
// MarkFailedTx -- never a forged auth.Identity in ctx
// ([system-actor-is-a-parameter]). "system" is 6 chars: satisfies both
// invoice_status_history.actor's and audit_log.actor's char_length CHECKs.
func SystemActor(tenantID string) Actor {
	return Actor{TenantID: tenantID, Subject: "system"}
}

// MarkSubmittedTx marks id submitted as SystemActor(tenantID), tx-scoped:
// the caller (the M5-04 worker, in a later subtask) already holds an open
// pgx.Tx from its own db.WithinTenantTx, so this never opens one of its own
// -- mirroring transitionTx's own caller-owns-the-tx contract, not
// Store.Transition's db.WithinRequestTenantTx wrapper.
func (s *Store) MarkSubmittedTx(ctx context.Context, tx pgx.Tx, id, tenantID string) (Invoice, error) {
	return s.markTerminalTx(ctx, tx, id, tenantID, StatusSubmitted)
}

// MarkFailedTx is MarkSubmittedTx's sibling for the queued->failed
// dead-letter edge.
func (s *Store) MarkFailedTx(ctx context.Context, tx pgx.Tx, id, tenantID string) (Invoice, error) {
	return s.markTerminalTx(ctx, tx, id, tenantID, StatusFailed)
}

// markTerminalTx is MarkSubmittedTx/MarkFailedTx's shared tail: lock+read the
// full row FOR UPDATE, short-circuit as an idempotent no-op when already at
// target (a replayed job after a crash between commit and the queue's ack
// must not write a second history/audit row), otherwise delegate to
// transitionTx with SystemActor(tenantID) on the SAME tx.
//
// The full row (not just status) is selected FOR UPDATE: unlike
// Store.Transition, the idempotent branch below must still return the
// current Invoice, so one locked read serves both the legality check and
// that return value -- no second query needed either way. If inv.Status is
// neither target nor a status canTransition allows into target (e.g. called
// on a draft invoice), transitionTx returns ErrIllegalTransition exactly as
// it does for any other illegal pair -- no special-casing needed here beyond
// the idempotent short-circuit.
func (s *Store) markTerminalTx(ctx context.Context, tx pgx.Tx, id, tenantID string, target Status) (Invoice, error) {
	var inv Invoice
	if err := scanInvoice(tx.QueryRow(ctx,
		`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1 FOR UPDATE`, id,
	), &inv); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Invoice{}, ErrNotFound
		}
		if pgCode(err) == "22P02" {
			return Invoice{}, ErrValidation
		}
		return Invoice{}, err
	}

	if inv.Status == target {
		// Idempotent no-op: already at the terminal state.
		return inv, nil
	}

	return transitionTx(ctx, tx, id, inv.Status, target, SystemActor(tenantID))
}

// --- Stage 2.5 SCAFFOLDING for M5-05-03 (task-239) -- QA Mode A -----------
//
// MarkAcceptedTx/MarkRejectedTx below exist ONLY so the widened
// submission.InvoicePort (invoice_port.go) compiles against *Store (the
// var _ assertion in submission_port.go) and so submission_port.go's
// MarkAccepted/MarkRejected forwards have something to call. NEITHER body is
// real: both return errOutcomeNotImplemented unconditionally and write
// nothing. Stage 3 (the executor) REPLACES both bodies with real ones that
// route through markTerminalTx (given an extra
// outcome func(context.Context, pgx.Tx) error parameter, per the Stage-1
// architect's locked design) -- NOT a second, parallel implementation of the
// lock/idempotency/transition sequence markTerminalTx already owns alone --
// the repo's sole-sequence call-count gate (AC#7) must stay unchanged by
// this scaffolding and by Stage 3's real rewrite alike.
// Locked signatures, dual-citation M5-05-03 (task-239):
//
//	func (s *Store) MarkAcceptedTx(ctx context.Context, tx pgx.Tx, id, tenantID, irn, csid, qrPayload string) (Invoice, error)
//	func (s *Store) MarkRejectedTx(ctx context.Context, tx pgx.Tx, id, tenantID string, reasons []submission.Reason) (Invoice, error)
var errOutcomeNotImplemented = errors.New("invoice: MarkAcceptedTx/MarkRejectedTx not implemented [M5-05-03]")

// MarkAcceptedTx is a Stage 2.5 stub -- see the scaffolding note above.
func (s *Store) MarkAcceptedTx(ctx context.Context, tx pgx.Tx, id, tenantID, irn, csid, qrPayload string) (Invoice, error) {
	return Invoice{}, errOutcomeNotImplemented
}

// MarkRejectedTx is a Stage 2.5 stub -- see the scaffolding note above.
func (s *Store) MarkRejectedTx(ctx context.Context, tx pgx.Tx, id, tenantID string, reasons []submission.Reason) (Invoice, error) {
	return Invoice{}, errOutcomeNotImplemented
}
