package invoice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// errActorNotImplemented is the RED-stage stub body MarkSubmittedTx/
// MarkFailedTx return below (M5-04-02, Mode A / RALPH Stage 2.5): the real
// implementation (SELECT ... FOR UPDATE lock+read, redundancy short-circuit,
// then transitionTx with SystemActor(tenantID)) lands in this subtask's
// Mode B (Executor, Stage 3) pass. It returns (rather than panics) so
// system_actor_test.go's T02-3/4/5/7 reach their assertions and fail for
// the right reason (this sentinel), not a compile or panic error. Mirrors
// the errNotImplemented precedent from M4-02-02's own RED commit
// (b96e3c0, since superseded once Transition itself was implemented).
var errActorNotImplemented = errors.New("invoice: MarkSubmittedTx/MarkFailedTx not implemented")

// Actor identifies who is driving a status transition: either the verified
// JWT caller (the HTTP path) or the background worker (M5-04's submission
// worker, via SystemActor). A struct rather than a bare Subject string
// because the invoice_status_history INSERT binds BOTH tenant_id and actor --
// a Subject-only parameter would have to re-derive TenantID from somewhere
// else anyway, and could then only ever disagree with the tenant_id beside
// it [Stage-1 F3]. Stage 3 threads this into transitionTx; it is declared
// here now only so this file's stubs (and Stage 3's real implementation)
// have a home.
type Actor struct {
	TenantID string
	Subject  string
}

// SystemActor is the background-worker identity for MarkSubmittedTx/
// MarkFailedTx -- never a forged auth.Identity in ctx
// ([system-actor-is-a-parameter]). "system" is 6 chars: satisfies both
// invoice_status_history.actor's and audit_log.actor's char_length CHECKs.
func SystemActor(tenantID string) Actor {
	return Actor{TenantID: tenantID, Subject: "system"}
}

// MarkSubmittedTx will mark id submitted as SystemActor(tenantID), tx-scoped
// (the caller already holds an open pgx.Tx from its own db.WithinTenantTx) --
// idempotent no-op when id is already submitted (Stage 3, [markTerminalTx]).
// Stubbed here only so it compiles; body is Stage 3's.
func (s *Store) MarkSubmittedTx(ctx context.Context, tx pgx.Tx, id, tenantID string) (Invoice, error) {
	return Invoice{}, errActorNotImplemented
}

// MarkFailedTx is MarkSubmittedTx's sibling for the queued->failed
// dead-letter edge. Stubbed here only so it compiles; body is Stage 3's.
func (s *Store) MarkFailedTx(ctx context.Context, tx pgx.Tx, id, tenantID string) (Invoice, error) {
	return Invoice{}, errActorNotImplemented
}
