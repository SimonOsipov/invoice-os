package approval

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// runStep is one materialised approval_run_steps row before it is written. The columns
// it omits — state, due_at, satisfied_at, satisfied_by — are all derived downstream.
type runStep struct {
	Ord             int
	Kind            string
	WorkflowRoleKey *string
	SLAHours        *int
	NotifyTarget    *string
	NotifyChannel   *string
}

// materialise ports the SPA's simulate (frontend/app/src/lib/workflows.ts:491-505): one
// pass over the sealed root lane, where a condition emits exactly one of its lanes and no
// step of its own. `auto` is REPORTED here and applied once, downstream in ArmTx — this
// function rewrites no kind, decides no state and never truncates the walk.
//
// The lane order the caller supplies is already fixed twice (policy_store.go:97's ORDER BY
// and policy.go:196's per-lane sort), so sorting here would only hide a reader regression.
func materialise(tree []Step, total *decimal.Decimal) (steps []runStep, auto bool) {
	steps = make([]runStep, 0, len(tree))

	take := func(lane []Step) {
		for _, n := range lane {
			// The depth-cap CHECK forbids a condition below the root, so this shape can
			// only come from a Go literal. Skipping keeps a kind='condition' run step —
			// which nothing could ever satisfy — out of the run.
			if n.Kind == "condition" {
				continue
			}
			if n.Kind == "autoapprove" {
				auto = true
			}
			steps = append(steps, runStep{
				Ord:             len(steps),
				Kind:            n.Kind,
				WorkflowRoleKey: n.WorkflowRoleKey,
				SLAHours:        n.SLAHours,
				NotifyTarget:    n.NotifyTarget,
				NotifyChannel:   n.NotifyChannel,
			})
		}
	}

	for _, n := range tree {
		if n.Kind != "condition" {
			take([]Step{n})
			continue
		}
		// cond_op is nullable, so deref through a local: evalCondition reads "" as false
		// and takes the else lane, where a bare *n.CondOp would panic mid-transaction.
		op := ""
		if n.CondOp != nil {
			op = *n.CondOp
		}
		if evalCondition(op, n.CondAmount, total) {
			take(n.Then)
		} else {
			take(n.Else)
		}
	}
	return steps, auto
}

// evalCondition ports the amount arm of the SPA's evalCondition
// (frontend/app/src/lib/workflows.ts:462-471). Each side folds to zero when absent or
// unparseable, mirroring its `Number(x) || 0` — a NULL invoices.total and a NULL
// cond_amount both read as 0.
func evalCondition(op string, condAmount *string, total *decimal.Decimal) bool {
	v := decimal.Zero
	if condAmount != nil {
		if parsed, err := decimal.NewFromString(*condAmount); err == nil {
			v = parsed
		}
	}

	a := decimal.Zero
	if total != nil {
		a = *total
	}

	switch op {
	case ">":
		return a.GreaterThan(v)
	case ">=":
		return a.GreaterThanOrEqual(v)
	case "<":
		return a.LessThan(v)
	case "<=":
		return a.LessThanOrEqual(v)
	}
	// Deliberate deviation from the mock, whose ladder falls through to `<=`
	// (workflows.ts:470). The only reachable case here is a NULL cond_op, and an
	// unspecified condition must take the else lane rather than silently mean "≤".
	return false
}

// ArmResult is what one arm did. RunID is "" only when the tenant has no active version.
type ArmResult struct {
	RunID  string
	Steps  int
	Closed bool // written closed 'approved' — no approval step was left pending
}

// ArmTx resolves the tenant's active sealed policy version against one invoice and writes
// its run and ordered steps inside the caller's transaction. A tenant with no active
// version is the ONE arm that writes nothing at all — no run, no step, no audit row.
//
// ArmTx does NOT lock the invoice row; the caller must already hold it
// (Store.ApplyValidation's SELECT ... FOR UPDATE, internal/invoice/store.go:1697).
//
// fingerprint and actor are parameters, never derived here: contentFingerprint is
// unexported in internal/invoice and that import edge must not reverse.
//
// Errors propagate RAW so their SQLSTATE survives. A 23505 on approval_runs_one_open is
// deliberately not caught — it means the invoice already held an open run, an invariant
// breach that must roll the caller's promotion back rather than read as a conflict.
func ArmTx(ctx context.Context, tx pgx.Tx, tenantID, invoiceID, fingerprint, actor string) (ArmResult, error) {
	// is_active alone is the whole resolve: approval_policy_versions_one_active caps it at
	// one row per tenant and approval_policy_versions_active_is_sealed makes active imply
	// sealed, so neither ORDER BY nor LIMIT would add anything.
	var versionID string
	err := tx.QueryRow(ctx, `SELECT id FROM approval_policy_versions WHERE is_active`).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ArmResult{}, nil
	}
	if err != nil {
		return ArmResult{}, err
	}

	// total::text into a *string, never a bare decimal.Decimal: numeric(14,2) accepts NaN
	// and decimal's Scan errors on it, which would roll back the caller's promotion. An
	// unparseable total reads as absent, which evalCondition folds to zero.
	var totalText *string
	if err := tx.QueryRow(ctx,
		`SELECT total::text FROM invoices WHERE id = $1`, invoiceID).Scan(&totalText); err != nil {
		return ArmResult{}, err
	}
	var total *decimal.Decimal
	if totalText != nil {
		if parsed, err := decimal.NewFromString(*totalText); err == nil {
			total = &parsed
		}
	}

	trees, err := readPolicyTrees(ctx, tx, []string{versionID})
	if err != nil {
		return ArmResult{}, err
	}
	steps, auto := materialise(trees[versionID], total)

	// One pass building all seven arrays and the state of every step. unnest pads a short
	// array with NULLs instead of erroring, so they must stay the same length; the
	// nullable four stay []*string/[]*int so a nil member encodes as a NULL element rather
	// than a zero, which is a different stored row.
	ords := make([]int, len(steps))
	kinds := make([]string, len(steps))
	roleKeys := make([]*string, len(steps))
	slaHours := make([]*int, len(steps))
	notifyTargets := make([]*string, len(steps))
	notifyChannels := make([]*string, len(steps))
	states := make([]string, len(steps))
	for i, s := range steps {
		// notify is skipped always, and so is an approval that an autoapprove already
		// settled — `auto` is reported by materialise and applied here, once.
		state := "skipped"
		switch {
		case s.Kind == "autoapprove":
			state = "satisfied"
		case s.Kind == "approval" && !auto:
			state = "pending"
		}
		ords[i], kinds[i], states[i] = s.Ord, s.Kind, state
		roleKeys[i], slaHours[i] = s.WorkflowRoleKey, s.SLAHours
		notifyTargets[i], notifyChannels[i] = s.NotifyTarget, s.NotifyChannel
	}

	// The closure predicate, over the states just decided: no approval step left pending
	// means nothing can ever satisfy this run, so it is written closed in the same INSERT
	// that creates it. NOT len(steps) == 0 — a notify-only run has a step and still has
	// nothing to satisfy.
	runState := "approved"
	for i, s := range steps {
		if s.Kind == "approval" && states[i] == "pending" {
			runState = "open"
			break
		}
	}

	var runID string
	// closed_by is the literal 'system'. internal/invoice.SystemActor is its convention's
	// source, but importing it here would close a cycle once internal/invoice calls ArmTx.
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_runs
		        (tenant_id, invoice_id, policy_version_id, content_fingerprint,
		         state, closed_at, closed_by)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5,
		         CASE WHEN $5 = 'approved' THEN now() END,
		         CASE WHEN $5 = 'approved' THEN 'system' END)
		 RETURNING id`,
		tenantID, invoiceID, versionID, fingerprint, runState).Scan(&runID); err != nil {
		return ArmResult{}, err
	}

	// Issued unconditionally: unnest of empty arrays inserts zero rows. due_at is gated on
	// the KIND as well as the SLA, because materialise copies sla_hours through for every
	// kind and a notify carrying one is seedable.
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_run_steps
		        (tenant_id, run_id, ord, kind, workflow_role_key, sla_hours,
		         notify_target, notify_channel, state, due_at, satisfied_at, satisfied_by)
		 SELECT $1::uuid, $2::uuid,
		        s.ord, s.kind, s.role_key, s.sla, s.notify_target, s.notify_channel, s.state,
		        CASE WHEN s.kind = 'approval' AND s.sla > 0
		             THEN now() + s.sla * interval '1 hour' END,
		        CASE WHEN s.state = 'satisfied' THEN now() END,
		        CASE WHEN s.state = 'satisfied' THEN 'system' END
		   FROM unnest($3::int[], $4::text[], $5::text[], $6::int[],
		               $7::text[], $8::text[], $9::text[])
		        AS s(ord, kind, role_key, sla, notify_target, notify_channel, state)`,
		tenantID, runID, ords, kinds, roleKeys, slaHours, notifyTargets, notifyChannels, states,
	); err != nil {
		return ArmResult{}, err
	}

	// Last statement, so a failing audit rolls the whole arm — and its caller's promotion —
	// back. Summary only: no total, no line items, no role keys.
	if err := audit.Record(ctx, tx, actor, "invoice.approval_armed", map[string]any{
		"id":                invoiceID,
		"run_id":            runID,
		"policy_version_id": versionID,
		"steps":             len(steps),
	}); err != nil {
		return ArmResult{}, err
	}

	return ArmResult{RunID: runID, Steps: len(steps), Closed: runState == "approved"}, nil
}

// CancelLiveRunTx cancels the invoice's LIVE runs — state 'open' or 'approved' — inside
// the caller's transaction and audits each. Reports false when nothing was live.
//
// Plural by construction, singular in practice: the invariant is at most one live run,
// and this function is what maintains it. Every path that walks an invoice back to draft
// calls it, so a stale run can never outlive the promotion it belonged to.
//
// A 'rejected' run is deliberately NOT cancelled: it cannot satisfy APPR-08's gate,
// re-arming beside one is legal, and rewriting a refusal as a cancellation would destroy
// the evidence APPR-07 reads. Errors propagate RAW, matching ArmTx.
func CancelLiveRunTx(ctx context.Context, tx pgx.Tx, invoiceID, actor string) (bool, error) {
	// COALESCE, not plain assignment: an open run has both columns NULL so this IS plain
	// assignment, while an already-closed 'approved' run keeps who closed it and when —
	// the cancellation itself is what the audit row records.
	rows, err := tx.Query(ctx,
		`UPDATE approval_runs
		    SET state     = 'cancelled',
		        closed_at = COALESCE(closed_at, now()),
		        closed_by = COALESCE(closed_by, $2)
		  WHERE invoice_id = $1 AND state IN ('open','approved')
		RETURNING id`,
		invoiceID, actor)
	if err != nil {
		return false, err
	}

	// Query, never QueryRow: approval_runs_one_open constrains only 'open', so an invoice
	// can hold an 'approved' AND an 'open' run at once. QueryRow would cancel both and
	// audit one (TestCancelLiveRun_CancelsEveryLiveRunAndAuditsEach).
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return false, err
		}
		runIDs = append(runIDs, runID)
	}
	// Closed explicitly rather than deferred: the audit writes below reuse this
	// transaction's connection.
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(runIDs) == 0 {
		return false, nil
	}

	for _, runID := range runIDs {
		if err := audit.Record(ctx, tx, actor, "invoice.approval_cancelled", map[string]any{
			"id":     invoiceID,
			"run_id": runID,
		}); err != nil {
			return false, err
		}
	}
	return true, nil
}
