package approval

import "github.com/shopspring/decimal"

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
