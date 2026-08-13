package approval

// The transmit gate: the pure predicate plus the three tx-scoped reads that feed it.
// RLS is the only tenant scope here (store.go:27-30) -- TestGateFile_NoTenantIdPredicate
// scans this file for a per-tenant column predicate and fails on one.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// GateFacts is one invoice's approval standing, for the detail page's gate.
type GateFacts struct {
	PolicyActive    bool
	ApprovedRun     bool
	RunState        string // "" when the invoice has no run at all
	PendingStepOrd  *int   // nil when no kind='approval' step is pending
	CallerHoldsRole bool
}

// RowFacts is one invoice row's approval standing, for the list.
type RowFacts struct {
	RunState          string     `json:"run_state"`
	PendingOrd        *int       `json:"pending_ord"`
	PendingRoleTitle  *string    `json:"pending_role_title"`
	PendingHolderWarn bool       `json:"pending_holder_warn"`
	DueAt             *time.Time `json:"due_at"`
	Overdue           bool       `json:"overdue"`
}

// TransmitClear reports whether an invoice may pass into queued.
//
// Stated as "clear", not "blocked", so an absent or zero answer fails closed: seeded
// invoices sit at validated with no run (db/seed.dev.sql UPSERTs them there), and a
// blocked-shaped predicate would read false -- clear -- for every one of them.
func TransmitClear(policyActive, approvedRun bool) bool {
	return !policyActive || approvedRun
}

// TransmitClearTx answers TransmitClear for a set of invoice ids. An id invisible under
// RLS returns no row and is ABSENT from the map, which reads false in Go. Presence is not
// proof the invoice exists: the no-active-policy short-circuit maps every requested id.
func TransmitClearTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error) {
	clear := make(map[string]bool, len(ids))

	// is_active alone is the whole resolve (ArmTx, engine.go:130-134).
	var policyActive bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM approval_policy_versions WHERE is_active)`,
	).Scan(&policyActive); err != nil {
		return nil, err
	}
	// A tenant that has published no policy pays one statement, not two
	// (TestTransmitClearTx_NoActivePolicyClearsEveryInvoice).
	if !policyActive {
		for _, id := range ids {
			clear[id] = TransmitClear(policyActive, false)
		}
		return clear, nil
	}

	// Set-shaped, so the count is constant in the batch size
	// (TestTransmitClearTx_ConstantInBatchSize).
	rows, err := tx.Query(ctx,
		`SELECT i.id,
		        EXISTS (SELECT 1 FROM approval_runs r
		                 WHERE r.invoice_id = i.id AND r.state = 'approved')
		   FROM invoices i
		  WHERE i.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var approvedRun bool
		if err := rows.Scan(&id, &approvedRun); err != nil {
			rows.Close()
			return nil, err
		}
		clear[id] = TransmitClear(policyActive, approvedRun)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return clear, nil
}

// GateFactsTx reads one invoice's gate standing for subject. A refused rung is fields,
// never an error -- an invoice with no run reads as RunState ""
// (TestGateFactsTx_NoRunIsEmptyStateNotAnError).
func GateFactsTx(ctx context.Context, tx pgx.Tx, invoiceID, subject string) (GateFacts, error) {
	var gf GateFacts

	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM approval_policy_versions WHERE is_active)`,
	).Scan(&gf.PolicyActive); err != nil {
		return GateFacts{}, err
	}

	// ApprovedRun is EXISTS over every run while RunState is the newest one: the two
	// disjuncts are read by different rules
	// (TestGateFactsTx_ApprovedRunIsExistsNotLatestRun).
	var runID string
	if err := tx.QueryRow(ctx,
		`SELECT r.id, r.state,
		        EXISTS (SELECT 1 FROM approval_runs a
		                 WHERE a.invoice_id = $1 AND a.state = 'approved')
		   FROM approval_runs r
		  WHERE r.invoice_id = $1
		  ORDER BY r.opened_at DESC
		  LIMIT 1`, invoiceID,
	).Scan(&runID, &gf.RunState, &gf.ApprovedRun); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gf, nil
		}
		return GateFacts{}, err
	}

	// decideTx's pending-step predicate (decision.go:162-166) minus FOR UPDATE: this is a
	// read path, and a GET must take no row lock.
	var stepOrd int
	var roleKey *string
	if err := tx.QueryRow(ctx,
		`SELECT ord, workflow_role_key
		   FROM approval_run_steps
		  WHERE run_id = $1 AND kind = 'approval' AND state = 'pending'
		  ORDER BY ord LIMIT 1`, runID,
	).Scan(&stepOrd, &roleKey); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gf, nil
		}
		return GateFacts{}, err
	}
	gf.PendingStepOrd = &stepOrd

	// AXIS 2, copied from decideTx (decision.go:183-193) with its roleKey != nil guard, so
	// the gate and the decision refuse on the same rung
	// (TestGateFactsTx_NullRoleKeyOnThePendingStepIsNotHolding). AXIS 1 is absent: the
	// caller's own access role is internal/invoice's rung, and this already implies it.
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
			 )`, *roleKey, subject,
		).Scan(&gf.CallerHoldsRole); err != nil {
			return GateFacts{}, err
		}
	}
	return gf, nil
}

// RowFactsTx reads the list-row approval standing of a set of invoice ids. An id with no
// run gets no entry (TestRowFactsTx_InvoiceWithNoRunIsAbsentFromTheMap). Five statements
// whatever the row and role count (TestRowFactsTx_FiveStatementsRegardlessOfRowAndRoleCount).
func RowFactsTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]RowFacts, error) {
	facts := make(map[string]RowFacts, len(ids))
	invoiceOfRun := map[string]string{}
	runIDs := []string{}

	// No tie-break beyond opened_at DESC: ApprovalRun (read_model.go:105-111) has the same
	// property, and adding one here would make a row and the panel disagree.
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT ON (invoice_id) invoice_id, id, state
		   FROM approval_runs
		  WHERE invoice_id = ANY($1::uuid[])
		  ORDER BY invoice_id, opened_at DESC`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var invoiceID, runID, state string
		if err := rows.Scan(&invoiceID, &runID, &state); err != nil {
			rows.Close()
			return nil, err
		}
		facts[invoiceID] = RowFacts{RunState: state}
		invoiceOfRun[runID] = invoiceID
		runIDs = append(runIDs, runID)
	}
	// Closed explicitly rather than deferred: the next Query reuses the transaction's
	// connection (read_model.go:169).
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type pendingStep struct {
		invoiceID string
		ord       int
		roleKey   *string
		dueAt     *time.Time
	}
	pending := []pendingStep{}
	roleKeys := []string{}
	seenKeys := map[string]bool{}

	steps, err := tx.Query(ctx,
		`SELECT DISTINCT ON (run_id) run_id, ord, workflow_role_key, due_at
		   FROM approval_run_steps
		  WHERE run_id = ANY($1::uuid[]) AND kind = 'approval' AND state = 'pending'
		  ORDER BY run_id, ord`, runIDs)
	if err != nil {
		return nil, err
	}
	for steps.Next() {
		var runID string
		var p pendingStep
		if err := steps.Scan(&runID, &p.ord, &p.roleKey, &p.dueAt); err != nil {
			steps.Close()
			return nil, err
		}
		p.invoiceID = invoiceOfRun[runID]
		if p.roleKey != nil && !seenKeys[*p.roleKey] {
			seenKeys[*p.roleKey] = true
			roleKeys = append(roleKeys, *p.roleKey)
		}
		pending = append(pending, p)
	}
	steps.Close()
	if err := steps.Err(); err != nil {
		return nil, err
	}

	exists, titles, holders, err := resolveRunRoles(ctx, tx, roleKeys)
	if err != nil {
		return nil, err
	}
	for _, p := range pending {
		rf := facts[p.invoiceID]
		ord := p.ord
		rf.PendingOrd = &ord
		rf.DueAt = p.dueAt
		// Go's clock, never SQL now(): the run panel reads the same one
		// (read_model.go:161). The pending-only read above supplies its state conjunct.
		rf.Overdue = rf.DueAt != nil && rf.DueAt.Before(time.Now())
		// A NULL key is skipped, never run through resolveHolder, which would answer
		// "Role no longer exists" (read_model.go:183-186).
		if p.roleKey != nil {
			title := roleTitle(exists[*p.roleKey], titles[*p.roleKey])
			rf.PendingRoleTitle = &title
			rf.PendingHolderWarn = resolveHolder(exists[*p.roleKey], holders[*p.roleKey]).Warn
		}
		facts[p.invoiceID] = rf
	}
	return facts, nil
}
