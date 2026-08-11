package approval

import "github.com/shopspring/decimal"

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
