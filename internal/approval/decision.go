package approval

// The approve/reject decision seam (§1.3 of the story). Decide/decideTx implement the
// approve half; the rejected half lands in a later subtask on this branch.

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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
func (s *Store) Decide(ctx context.Context, invoiceID, decision string, reason *string) (Run, error) {
	// Parsed above the tx (GetPolicy's precedent, policy_store.go:218-221): a malformed
	// id never reaches SQL as an unmappable 22P02.
	u, err := uuid.Parse(invoiceID)
	if err != nil {
		return Run{}, ErrRunNotFound
	}

	var run Run
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Guaranteed present: WithinRequestTenantTx resolved it before this ran.
		caller, _ := auth.IdentityFromContext(ctx)
		var derr error
		run, derr = decideTx(ctx, tx, u.String(), decision, reason, caller)
		return derr
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// requireApprover refuses any caller that is not an active {admin, reviewer} -- AXIS
// 1 (Q1: preparers excluded). Mirrors requireActiveAdmin's shape (store.go:470-484)
// with the wider role set. Must run before any approval_run/approval_run_steps row
// is read (AC-1, AC-2's PermissionCheckPrecedesRowLock).
func requireApprover(ctx context.Context, tx pgx.Tx, subject string) error {
	var role string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM memberships WHERE user_id = $1 AND status = 'active'`, subject,
	).Scan(&role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPermitted
		}
		return err
	}
	if !isApprover(role) {
		return ErrNotPermitted
	}
	return nil
}

// decideTx is Decide's tx-scoped core (resolve the invoice/run/current step in the
// invoices -> approval_* lock order, both axes, then commitDecisionTx's write).
// Exposed at package scope, unexported, so a test can wrap it in its own
// db.WithinTenantTx and force a rollback after a real write -- the ArmTx/
// CancelLiveRunTx precedent for same-tx atomicity proofs.
func decideTx(ctx context.Context, tx pgx.Tx, invoiceID, decision string, reason *string, caller auth.Identity) (Run, error) {
	if err := requireApprover(ctx, tx, caller.Subject); err != nil {
		return Run{}, err
	}

	// invoices -> approval_* lock order (policy_store.go:699-718) -- reversed, this
	// opens the same deadlock class the publish sweep already hit once.
	var status string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM invoices WHERE id = $1 FOR UPDATE`, invoiceID,
	).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrRunNotFound
		}
		return Run{}, err
	}
	if status != "validated" {
		return Run{}, ErrNotAwaitingApproval
	}

	var runID, runState string
	if err := tx.QueryRow(ctx,
		`SELECT id, state FROM approval_runs
		  WHERE invoice_id = $1
		  ORDER BY opened_at DESC LIMIT 1 FOR UPDATE`, invoiceID,
	).Scan(&runID, &runState); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrRunNotFound
		}
		return Run{}, err
	}
	if runState != "open" {
		return Run{}, ErrRunClosed
	}

	var stepID string
	var stepOrd int
	var roleKey *string
	if err := tx.QueryRow(ctx,
		`SELECT id, ord, workflow_role_key
		   FROM approval_run_steps
		  WHERE run_id = $1 AND kind = 'approval' AND state = 'pending'
		  ORDER BY ord LIMIT 1 FOR UPDATE`, runID,
	).Scan(&stepID, &stepOrd, &roleKey); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrRunClosed
		}
		return Run{}, err
	}

	// AXIS 2: the inverse of reconciliation's approval_blocked_unstaffed NOT EXISTS
	// body (internal/reconciliation/reconciliation.go:179-204), narrowed to the
	// caller -- no tenant_id predicate, RLS is the only filter (store.go:27-30).
	var holds bool
	if roleKey != nil {
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1
			     FROM workflow_roles wr
			     JOIN workflow_role_members wrm ON wrm.workflow_role_id = wr.id
			     JOIN memberships m ON m.user_id = wrm.user_id
			    WHERE wr.key = $1
			      AND wr.deleted_at IS NULL
			      AND wrm.user_id = $2
			      AND m.status = 'active'
			      AND m.role IN ('admin', 'reviewer')
			 )`, *roleKey, caller.Subject,
		).Scan(&holds); err != nil {
			return Run{}, err
		}
	}
	if !holds {
		return Run{}, ErrNotRoleHolder
	}

	if _, err := commitDecisionTx(ctx, tx, invoiceID, runID, stepID, stepOrd, decision, caller.Subject, reason); err != nil {
		return Run{}, err
	}

	// Re-read inside the same tx: commitDecisionTx may have advanced or closed it.
	run := Run{RunID: runID}
	if err := tx.QueryRow(ctx,
		`SELECT state, opened_at, closed_at, closed_by FROM approval_runs WHERE id = $1`, runID,
	).Scan(&run.State, &run.OpenedAt, &run.ClosedAt, &run.ClosedBy); err != nil {
		return Run{}, err
	}
	return run, nil
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
func commitDecisionTx(ctx context.Context, tx pgx.Tx, invoiceID, runID, stepID string, stepOrd int, decision, actor string, reason *string) (satisfied bool, err error) {
	// approval_decisions holds only SELECT+INSERT for invoice_app (migrations/
	// 20260809232011_approval_runs.sql:114) -- the UPDATE must claim the row FIRST,
	// or a 0-row UPDATE would leave an unremovable phantom decision.
	stepState := "satisfied"
	if decision == "rejected" {
		stepState = "rejected"
	}
	var claimed string
	err = tx.QueryRow(ctx,
		`UPDATE approval_run_steps
		    SET state = $2, satisfied_at = now(), satisfied_by = $3
		  WHERE id = $1 AND state = 'pending'
		RETURNING id`,
		stepID, stepState, actor,
	).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// tenant_id has no DEFAULT (unlike audit_log) and commitDecisionTx's signature is
	// pinned by its own unit test with no tenantID parameter, so it comes from the
	// transaction-local GUC directly -- the RLS policy's own expression verbatim
	// (migrations/20260809232011_approval_runs.sql:106).
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor, reason)
		 VALUES (nullif(current_setting('app.current_tenant', true), '')::uuid, $1, $2, $3, $4, $5)`,
		runID, stepID, decision, actor, reason,
	); err != nil {
		return false, err
	}

	// Close-or-advance and audit: the approved half only -- rejected lands in a
	// later subtask on this branch.
	if decision == "approved" {
		var stillPending bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM approval_run_steps
			    WHERE run_id = $1 AND kind = 'approval' AND state = 'pending'
			 )`, runID,
		).Scan(&stillPending); err != nil {
			return false, err
		}
		if !stillPending {
			if _, err := tx.Exec(ctx,
				`UPDATE approval_runs SET state = 'approved', closed_at = now(), closed_by = $2 WHERE id = $1`,
				runID, actor,
			); err != nil {
				return false, err
			}
		}

		if err := audit.Record(ctx, tx, actor, "invoice.approval_approved", map[string]any{
			"invoice_id": invoiceID,
			"run_id":     runID,
			"step_ord":   stepOrd,
			"reason":     reason,
		}); err != nil {
			return false, err
		}
	}

	return true, nil
}
