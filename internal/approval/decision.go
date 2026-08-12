package approval

// The approve/reject decision seam (§1.3 of the story, Stage-1/2 validation appendix
// on task-489). task-489 (this subtask) is Test-first Mode A: every symbol below is a
// STUB -- it compiles and returns zero values so the specs in decision_test.go fail on
// their own assertions, never on a panic or an undefined identifier. The executor fills
// these in next; the "rejected" half of Decide lands in subtask 05.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// Sentinels for the decision seam. ErrRunNotFound already exists (read_model.go) and
// is reused here -- an unknown, cross-tenant, malformed-uuid or no-run invoice id all
// answer alike.
var (
	ErrRunClosed           = errors.New("approval: run is not open")
	ErrNotRoleHolder       = errors.New("approval: caller does not hold the step's role")
	ErrNotAwaitingApproval = errors.New("approval: invoice is not awaiting approval")
)

// Decide is the approve/reject seam: two-axis authorization, the current pending
// step's satisfaction, the ledger write, and the advance-or-close, all in one
// transaction. decision is "approved" or "rejected".
//
// STUB: always returns the zero Run and a nil error.
func (s *Store) Decide(ctx context.Context, invoiceID, decision string, reason *string) (Run, error) {
	return Run{}, nil
}

// requireApprover refuses any caller that is not an active {admin, reviewer} -- AXIS
// 1 (Q1: preparers excluded). Mirrors requireActiveAdmin's shape (store.go:470-484)
// with the wider role set. Must run before any approval_run/approval_run_steps row
// is read (AC-1, AC-2's PermissionCheckPrecedesRowLock).
//
// STUB: always nil.
func requireApprover(ctx context.Context, tx pgx.Tx, subject string) error {
	return nil
}

// decideTx is Decide's tx-scoped core (resolve the invoice/run/current step in the
// invoices -> approval_* lock order, both axes, then commitDecisionTx's write).
// Exposed at package scope, unexported, so a test can wrap it in its own
// db.WithinTenantTx and force a rollback after a real write -- the ArmTx/
// CancelLiveRunTx precedent for same-tx atomicity proofs.
//
// STUB: always returns the zero Run and a nil error.
func decideTx(ctx context.Context, tx pgx.Tx, invoiceID, decision string, reason *string, caller auth.Identity) (Run, error) {
	return Run{}, nil
}

// commitDecisionTx is the write half of the approve/reject path, given an already-
// resolved and authorized pending step: the step UPDATE (whose "AND state =
// 'pending'" predicate is both the existence check and the idempotency guard, the
// DeleteRole idiom, store.go:335-338), the decision INSERT only if that UPDATE
// claimed a row, the close-or-advance on the run, and audit.Record as the LAST
// statement.
//
// Decoupled from resolution on purpose: the step's own resolving SELECT is FOR
// UPDATE, which makes "the UPDATE affects 0 rows" unreachable through the full
// Decide seam under normal concurrency (the resolving SELECT would simply find no
// pending row instead) -- this lets the guard be driven directly as its own unit.
//
// STUB: always returns satisfied=false, nil.
func commitDecisionTx(ctx context.Context, tx pgx.Tx, invoiceID, runID, stepID string, stepOrd int, decision, actor string, reason *string) (satisfied bool, err error) {
	return false, nil
}
