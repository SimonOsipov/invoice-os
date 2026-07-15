// This file (evaluators_math.go) is the M3-04-04 subtask: the three
// arithmetic/relational rule-type Evaluators -- tax_math, cross_field,
// conditional -- from the story's "9 rule-type evaluators (contracts)"
// table. The five presence/shape evaluators (required/format/enum/range/
// date) live in evaluators.go (M3-04-03); the cel escape hatch lands in
// M3-04-05 (cel.go). Registry assembly (wiring these structs into the
// map[RuleType]Evaluator NewEngine takes) is deferred to M3-04-08 -- these
// are unit-tested directly against their Eval method here, no registry
// needed for this subtask.
//
// Unlike the five evaluators in evaluators.go, none of these three rule
// types has a single natural Rule.Target -- their operative path(s) live
// INSIDE Params instead (tax_math's base/expected, cross_field's left/
// right, conditional's if.field/then.field). Tests below therefore leave
// Rule.Target unset ("") and do not assert Violation.Path for these types;
// the shared violation(r) helper (evaluators.go) still copies r.Target
// verbatim, so a rule author who wants a specific Path may still set
// Rule.Target explicitly -- the executor's Eval bodies must not derive Path
// from Params on their own initiative.
//
// Param shapes (pinned here so the executor's implementation and this RED
// suite agree byte-for-byte; QA Mode A decision, not yet in the story's Test
// Specs table beyond the six baseline rows):
//
//   - tax_math: {"base": <string path OR number>, "rate": <number>,
//     "expected": <string path OR number>, "tolerance": <number, default 0>}.
//     base and expected each resolve as: a JSON STRING is a dotted payload
//     path (resolvePath, then coerced via toFloat); a JSON NUMBER is used as
//     that literal value directly. rate and tolerance are ALWAYS literal
//     numbers, never paths. Violation iff
//     abs(expected - base*rate) > tolerance (exact decimal math --
//     shopspring/decimal per the story's Data Model, to avoid float error).
//
//   - cross_field: {"left": <string path>, "op": "eq|ne|lt|le|gt|ge",
//     "right": <string path>}. left and right are ALWAYS dotted payload
//     paths (never literals, unlike tax_math's base/expected) -- both
//     resolved via resolvePath. Violation iff the relation left op right
//     does NOT hold. lt/le/gt/ge compare numerically (toFloat); eq/ne may
//     compare either numbers or strings.
//
//   - conditional: {"if": {"field": <path>, "op": <op>, "value": <literal>},
//     "then": <predicate>}, where <predicate> is EITHER
//     {"field": <path>, "required": true} (a presence check mirroring
//     requiredEval's semantics) OR {"field": <path>, "op": <op>,
//     "value": <literal>} (a comparison against a literal, NOT a second
//     path -- conditional has no cross_field-style two-path form). `if`'s
//     field is resolved and compared to its literal value with its op; if
//     that comparison is false, the rule passes and `then` is never
//     evaluated. If true, `then` is evaluated; a failed `then` is the
//     violation.
//
// Money/relational math here uses shopspring/decimal (exact decimal
// arithmetic, per the story's Data Model) rather than raw float64, so
// tax_math's base*rate never accrues binary-float error against a tolerance.
package validation

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/shopspring/decimal"
)

// taxMathEval implements the `tax_math` rule type: params
// `{base, rate, expected, tolerance?}`. Passes when
// |expected - base*rate| <= tolerance; violates when the mismatch exceeds
// tolerance. base/expected are each a payload path (JSON string) or a
// literal (JSON number) -- see file-header contract.
//
// Config faults (=> error, Decision N15): undecodable params; rate a
// non-number or absent; base/expected param key absent; a negative
// tolerance (which would make a zero mismatch on an exactly-correct invoice
// register as > tolerance and silently flag it). Data faults (=> violation,
// mirroring rangeEval's non-numeric handling): a base/expected path that is
// absent or resolves to a non-numeric value.
type taxMathEval struct{}

func (taxMathEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		Base      json.RawMessage `json:"base"`
		Rate      *float64        `json:"rate"`
		Expected  json.RawMessage `json:"expected"`
		Tolerance float64         `json:"tolerance"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: tax_math rule %q params: %w", r.Key, err)
	}
	// Config-fault checks (fail loud -- N15) before any data resolution.
	if params.Rate == nil {
		return nil, fmt.Errorf("validation: tax_math rule %q missing rate", r.Key)
	}
	if len(params.Base) == 0 {
		return nil, fmt.Errorf("validation: tax_math rule %q missing base", r.Key)
	}
	if len(params.Expected) == 0 {
		return nil, fmt.Errorf("validation: tax_math rule %q missing expected", r.Key)
	}
	// A negative tolerance is a misconfiguration: mismatch.GreaterThan(tol)
	// would be true even for a zero mismatch (0 > negative), silently
	// flagging CORRECT invoices. Fail loud (N15) rather than mis-flag.
	if params.Tolerance < 0 {
		return nil, fmt.Errorf("validation: tax_math rule %q tolerance must be non-negative, got %v", r.Key, params.Tolerance)
	}

	// A present operand whose path is absent/non-numeric is bad DATA -> a
	// violation (the invoice is malformed for this rule), not a config error.
	base, ok := resolveNumericOperand(p, params.Base)
	if !ok {
		return violation(r), nil
	}
	expected, ok := resolveNumericOperand(p, params.Expected)
	if !ok {
		return violation(r), nil
	}

	rate := decimal.NewFromFloat(*params.Rate)
	tolerance := decimal.NewFromFloat(params.Tolerance)
	mismatch := expected.Sub(base.Mul(rate)).Abs()
	if mismatch.GreaterThan(tolerance) {
		return violation(r), nil
	}
	return nil, nil
}

// resolveNumericOperand resolves a tax_math base/expected operand to an exact
// decimal. raw is a JSON STRING (a dotted payload path -- resolvePath, then
// toFloat) or a JSON NUMBER (used as the literal value). ok=false means the
// operand could not be resolved to a number (absent path or non-numeric
// value), which the caller maps to a violation. raw is a well-formed
// sub-value of an already-decoded params object, so the inner Unmarshal
// cannot fail.
func resolveNumericOperand(p Payload, raw json.RawMessage) (decimal.Decimal, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return decimal.Decimal{}, false
	}
	if path, isStr := v.(string); isStr {
		resolved, present := resolvePath(p, path)
		if !present {
			return decimal.Decimal{}, false
		}
		v = resolved
	}
	f, ok := toFloat(v)
	if !ok {
		return decimal.Decimal{}, false
	}
	return decimal.NewFromFloat(f), true
}

// lineSumEval implements the `line_sum` rule type: it folds a per-line-item
// amount across a list and compares the total to a scalar target with a
// tolerance. Params `{items, amount, quantity?, expected, tolerance?}`:
//   - items:    dotted path to the line-item list (e.g. "line_items").
//   - amount:   the per-ITEM field summed (a key on each item object, e.g.
//     "unit_price") — NOT a dotted payload path.
//   - quantity: an optional per-ITEM weight field (e.g. "quantity"); when
//     given and present on a line, that line contributes amount*quantity,
//     else amount*1.
//   - expected: dotted path to the scalar the sum must match (e.g.
//     "subtotal").
//   - tolerance: literal number, default 0.
//
// Passes when |sum - expected| <= tolerance. Exact decimal math
// (shopspring/decimal, mirroring taxMathEval) so a kobo-level rounding never
// mis-fires against the tolerance.
//
// Config faults (=> error, N15): undecodable params; a missing items/amount/
// expected key; a negative tolerance. Not-applicable (=> pass, nil): the
// items path is absent, resolves to a non-list, or is empty — line-items-
// required owns the "no lines at all" case, so this rule stays silent there
// rather than double-reporting. Data faults (=> violation, mirroring
// taxMathEval): a line item that is not an object; a line whose amount is
// absent or non-numeric, or whose quantity is present-but-non-numeric; an
// absent or non-numeric expected value.
type lineSumEval struct{}

func (lineSumEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		Items     string  `json:"items"`
		Amount    string  `json:"amount"`
		Quantity  string  `json:"quantity"`
		Expected  string  `json:"expected"`
		Tolerance float64 `json:"tolerance"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: line_sum rule %q params: %w", r.Key, err)
	}
	if params.Items == "" {
		return nil, fmt.Errorf("validation: line_sum rule %q missing items", r.Key)
	}
	if params.Amount == "" {
		return nil, fmt.Errorf("validation: line_sum rule %q missing amount", r.Key)
	}
	if params.Expected == "" {
		return nil, fmt.Errorf("validation: line_sum rule %q missing expected", r.Key)
	}
	// A negative tolerance would make |sum-expected| > tol true even for an
	// exact match (0 > negative), silently flagging correct invoices — fail
	// loud (N15), same rationale as taxMathEval.
	if params.Tolerance < 0 {
		return nil, fmt.Errorf("validation: line_sum rule %q tolerance must be non-negative, got %v", r.Key, params.Tolerance)
	}

	raw, present := resolvePath(p, params.Items)
	if !present {
		return nil, nil // no line-item list -> not applicable (line-items-required owns it)
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, nil
	}

	sum := decimal.Zero
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return violation(r), nil // a non-object line is malformed data for this rule
		}
		amountVal, ok := m[params.Amount]
		if !ok {
			return violation(r), nil // an amount-less line can't reconcile
		}
		amountF, ok := toFloat(amountVal)
		if !ok {
			return violation(r), nil
		}
		line := decimal.NewFromFloat(amountF)
		if params.Quantity != "" {
			if qtyVal, has := m[params.Quantity]; has {
				qtyF, ok := toFloat(qtyVal)
				if !ok {
					return violation(r), nil
				}
				line = line.Mul(decimal.NewFromFloat(qtyF))
			}
			// quantity field absent on this line -> implicit weight of 1.
		}
		sum = sum.Add(line)
	}

	expectedVal, present := resolvePath(p, params.Expected)
	if !present {
		return violation(r), nil
	}
	expectedF, ok := toFloat(expectedVal)
	if !ok {
		return violation(r), nil
	}
	if sum.Sub(decimal.NewFromFloat(expectedF)).Abs().GreaterThan(decimal.NewFromFloat(params.Tolerance)) {
		return violation(r), nil
	}
	return nil, nil
}

// crossFieldEval implements the `cross_field` rule type: params
// `{left, op, right}`, left/right ALWAYS dotted payload paths. Passes when
// the relation `left op right` holds; violates when it does not. An unknown
// op is a config fault => error.
type crossFieldEval struct{}

func (crossFieldEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		Left  string `json:"left"`
		Op    string `json:"op"`
		Right string `json:"right"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: cross_field rule %q params: %w", r.Key, err)
	}

	// left/right are always paths; an absent path yields a nil value, which
	// compareOp treats as the relation not holding (-> violation).
	left, _ := resolvePath(p, params.Left)
	right, _ := resolvePath(p, params.Right)

	holds, err := compareOp(params.Op, left, right)
	if err != nil {
		return nil, fmt.Errorf("validation: cross_field rule %q: %w", r.Key, err)
	}
	if !holds {
		return violation(r), nil
	}
	return nil, nil
}

// conditionalEval implements the `conditional` rule type: params
// `{if:{field,op,value}, then:{...predicate}}`. Passes when `if` is false
// (then is never evaluated) or when `if` is true and `then` holds;
// violates when `if` is true and `then` fails. See file-header contract for
// the two `then` predicate shapes. An unknown op in either clause is a
// config fault => error.
type conditionalEval struct{}

// predicate is one clause of a conditional: the `if` clause (always a
// field/op/value comparison) and the `then` clause (either a
// {field, required} presence check when Required is non-nil, or a
// {field, op, value} comparison otherwise).
type predicate struct {
	Field    string          `json:"field"`
	Op       string          `json:"op"`
	Value    json.RawMessage `json:"value"`
	Required *bool           `json:"required"`
}

func (conditionalEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		If   predicate `json:"if"`
		Then predicate `json:"then"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: conditional rule %q params: %w", r.Key, err)
	}

	ifHolds, err := evalPredicate(p, params.If)
	if err != nil {
		return nil, fmt.Errorf("validation: conditional rule %q if: %w", r.Key, err)
	}
	if !ifHolds {
		// `if` false: the rule passes and `then` is never evaluated.
		return nil, nil
	}
	thenHolds, err := evalPredicate(p, params.Then)
	if err != nil {
		return nil, fmt.Errorf("validation: conditional rule %q then: %w", r.Key, err)
	}
	if !thenHolds {
		return violation(r), nil
	}
	return nil, nil
}

// evalPredicate reports whether pred is satisfied against p. A Required
// (non-nil) pointer selects the presence variant -- present && non-blank,
// mirroring requiredEval (required:false is trivially satisfied). Otherwise
// pred is a field/op/value comparison. An unknown op surfaces as an error.
func evalPredicate(p Payload, pred predicate) (bool, error) {
	if pred.Required != nil {
		if !*pred.Required {
			return true, nil
		}
		val, present := resolvePath(p, pred.Field)
		if !present || val == nil {
			return false, nil
		}
		if s, ok := val.(string); ok && strings.TrimSpace(s) == "" {
			return false, nil
		}
		return true, nil
	}
	val, _ := resolvePath(p, pred.Field)
	var literal any
	if len(pred.Value) > 0 {
		if err := json.Unmarshal(pred.Value, &literal); err != nil {
			return false, err
		}
	}
	return compareOp(pred.Op, val, literal)
}

// compareOp reports whether `left op right` holds. lt/le/gt/ge are numeric
// (toFloat both sides; a non-numeric or absent operand makes the relation
// not hold). eq/ne compare numerically when both sides are numeric, else
// fall back to deep value equality (so number-vs-string is unequal). An
// unrecognized op is a config fault => error.
func compareOp(op string, left, right any) (bool, error) {
	switch op {
	case "eq", "ne":
		equal := valuesEqual(left, right)
		if op == "eq" {
			return equal, nil
		}
		return !equal, nil
	case "lt", "le", "gt", "ge":
		lf, lok := toFloat(left)
		rf, rok := toFloat(right)
		if !lok || !rok {
			return false, nil
		}
		switch op {
		case "lt":
			return lf < rf, nil
		case "le":
			return lf <= rf, nil
		case "gt":
			return lf > rf, nil
		default: // "ge"
			return lf >= rf, nil
		}
	default:
		return false, fmt.Errorf("unknown op %q", op)
	}
}

// valuesEqual compares two resolved values: numerically when both coerce to
// a number (so 100 and 100.0 match), else by deep value equality.
func valuesEqual(a, b any) bool {
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return reflect.DeepEqual(a, b)
}
