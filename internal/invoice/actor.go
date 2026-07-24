package invoice

import (
	"context"
	"encoding/json"
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
	return s.markTerminalTx(ctx, tx, id, tenantID, StatusSubmitted, nil)
}

// MarkFailedTx is MarkSubmittedTx's sibling for the queued->failed
// dead-letter edge.
func (s *Store) MarkFailedTx(ctx context.Context, tx pgx.Tx, id, tenantID string) (Invoice, error) {
	return s.markTerminalTx(ctx, tx, id, tenantID, StatusFailed, nil)
}

// markTerminalTx is the shared tail of all four Mark*Tx siblings
// (MarkSubmittedTx, MarkFailedTx, MarkAcceptedTx, MarkRejectedTx): lock+read
// the full row FOR UPDATE, short-circuit as an idempotent no-op when already
// at target (a replayed job after a crash between commit and the queue's ack
// must not write a second history/audit row, and -- for the two outcome
// writers -- must not rewrite an already-stored outcome either), otherwise
// run the optional outcome write and delegate to transitionTx with
// SystemActor(tenantID) on the SAME tx.
//
// The full row (not just status) is selected FOR UPDATE: unlike
// Store.Transition, the idempotent branch below must still return the
// current Invoice, so one locked read serves both the legality check and
// that return value -- no second query needed either way. If inv.Status is
// neither target nor a status canTransition allows into target (e.g. called
// on a draft invoice), transitionTx returns ErrIllegalTransition exactly as
// it does for any other illegal pair -- no special-casing needed here beyond
// the idempotent short-circuit.
//
// outcome -- M5-05-03 (task-239), [outcome-write-precedes-the-transition] -- is an
// optional extra step -- MarkAcceptedTx/MarkRejectedTx pass a closure that
// writes their outcome columns (irn/csid/qr_payload or rejection_reasons) on
// the SAME tx, invoked AFTER the idempotent short-circuit but BEFORE
// transitionTx, so a replayed call never rewrites a stored outcome and an
// outcome write followed by an illegal transitionTx call rolls back the
// whole attempt together (the caller's db.WithinTenantTx aborts on any
// non-nil error). MarkSubmittedTx/MarkFailedTx pass nil -- they write no
// outcome columns, so this step is skipped entirely for them, unchanged
// behaviour from before this parameter existed. This callback is the only
// addition; the lock+read, the idempotent short-circuit, and the single
// transitionTx call all stay solely inside this function, unchanged in
// count and order -- nothing outside markTerminalTx calls transitionTx.
func (s *Store) markTerminalTx(ctx context.Context, tx pgx.Tx, id, tenantID string, target Status, outcome func(context.Context, pgx.Tx) error) (Invoice, error) {
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
		// Idempotent no-op: already at the terminal state. The outcome write
		// below is skipped -- an already-stored outcome must not be clobbered
		// by a replayed call's (possibly different) arguments.
		return inv, nil
	}

	if outcome != nil {
		if err := outcome(ctx, tx); err != nil {
			return Invoice{}, err
		}
	}

	return transitionTx(ctx, tx, id, inv.Status, target, SystemActor(tenantID))
}

// MarkAcceptedTx -- M5-05-03 (task-239), AC#1/#2 -- transitions id -> accepted as
// SystemActor(tenantID), writing irn/csid/qr_payload in the SAME tx as the
// transition -- routed through markTerminalTx's shared lock/idempotency/
// transition sequence via the outcome callback, NOT a second, parallel
// implementation of that sequence.
//
// irn binds RAW, never NULLIF'd: a blank irn trips invoices' own
// CHECK (irn IS NULL OR char_length(irn) > 0)
// (migrations/20260722083015_invoices_fiscal_outcome.sql), raising SQLSTATE
// 23514 and aborting the whole outcome UPDATE -- the deliberate NULLIF
// asymmetry ([blank-irn-is-the-databases-to-refuse]): irn is REQUIRED
// non-blank (law L07), so letting the CHECK refuse a blank value is correct,
// not a bug to work around here. csid/qr_payload bind via NULLIF, blank -> NULL:
// "" means the authority returned none (submission.Accepted's own doc) and
// must land as SQL NULL, never the empty string.
//
// Because the outcome write runs inside markTerminalTx's outcome callback --
// AFTER the idempotent short-circuit but on the SAME tx as transitionTx --
// an illegal source status (e.g. draft->accepted) rolls back the whole
// attempt together: transitionTx's ErrIllegalTransition propagates out of
// markTerminalTx, the caller's db.WithinTenantTx aborts, and neither the
// outcome UPDATE nor the transition lands.
func (s *Store) MarkAcceptedTx(ctx context.Context, tx pgx.Tx, id, tenantID, irn, csid, qrPayload string) (Invoice, error) {
	return s.markTerminalTx(ctx, tx, id, tenantID, StatusAccepted, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE invoices SET irn = $1, csid = NULLIF($2, ''), qr_payload = NULLIF($3, '') WHERE id = $4`,
			irn, csid, qrPayload, id)
		return err
	})
}

// MarkRejectedTx -- M5-05-03 (task-239), AC#1/#2 -- is MarkAcceptedTx's sibling:
// transitions id -> rejected as SystemActor(tenantID), writing
// rejection_reasons in the SAME tx as the transition, same
// markTerminalTx-outcome-callback routing.
//
// A nil/empty reasons is normalised to the literal []submission.Reason{}
// BEFORE json.Marshal ([reasons-never-json-null], the M4-16 write-side
// trap): json.Marshal(nil []submission.Reason) yields the JSON scalar null,
// which binds SUCCESSFULLY to invoices.rejection_reasons jsonb NOT NULL and
// silently poisons the column with 'null'::jsonb -- only normalizing the
// slice first yields the literal '[]'. Same discipline as
// Store.ApplyValidation's own violations guard (store.go).
func (s *Store) MarkRejectedTx(ctx context.Context, tx pgx.Tx, id, tenantID string, reasons []submission.Reason) (Invoice, error) {
	if reasons == nil {
		reasons = []submission.Reason{}
	}
	payload, err := json.Marshal(reasons)
	if err != nil {
		return Invoice{}, err
	}
	return s.markTerminalTx(ctx, tx, id, tenantID, StatusRejected, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, payload, id)
		return err
	})
}
