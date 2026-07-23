package invoice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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
