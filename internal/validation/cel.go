// This file (cel.go) is the M3-04-05 subtask: the `type: cel` escape-hatch
// Evaluator plus the production GuardFunc backend for a rule's optional
// `when` clause -- the two CEL surfaces from the story's "9 rule-type
// evaluators (contracts)" table and the engine's select-stage guard
// (engine.go, GuardFunc). The eight parameterized evaluators
// (required/format/enum/range/date in evaluators.go, tax_math/cross_field/
// conditional in evaluators_math.go) resolve Rule.Target with NO "invoice."
// prefix (Decision N19); CEL is the deliberate exception -- the CEL
// activation binds a single top-level variable named "invoice" to
// p["invoice"], so every CEL expression (both a `type: cel` rule's `expr`
// param and any rule's `when` guard) MUST reference it via the
// "invoice."-prefixed form, e.g. "invoice.total > 0" (Decision N19,
// rule.go's Rule.Target/When doc comments).
//
// Both surfaces share one evalCELBool helper: it builds a cel.Env exposing
// the single "invoice" variable (cel.DynType), compiles + programs the
// expression, evaluates it against an activation binding "invoice" to
// p["invoice"], and requires a bool result. A compile fault, an eval fault,
// or a non-bool result are all engine/config faults (Decision N15) surfaced
// as an error -- never a coercion, a violation, or a silent pass. Registry
// assembly (wiring celEvaluator into the map[RuleType]Evaluator NewEngine
// takes, and celGuard as the Engine's GuardFunc) is deferred to M3-04-08.
package validation

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

// evalCELBool compiles and evaluates a CEL expression against a payload and
// requires a bool result. The activation binds the single top-level variable
// "invoice" to p["invoice"] (Decision N19 -- every CEL expression references
// invoice via the "invoice."-prefixed form). A compile error, an eval error,
// or a non-bool result type are all engine/config faults (Decision N15: fail
// loud, never a silent pass) surfaced as a non-nil error -- the result is
// never coerced. Shared verbatim by celEvaluator.Eval and celGuard so the
// `type: cel` rule's expr and any rule's `when` guard compile+evaluate
// identically.
func evalCELBool(expr string, p Payload) (bool, error) {
	env, err := cel.NewEnv(cel.Variable("invoice", cel.DynType))
	if err != nil {
		return false, fmt.Errorf("validation: cel env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return false, fmt.Errorf("validation: cel compile %q: %w", expr, iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("validation: cel program %q: %w", expr, err)
	}
	out, _, err := prg.Eval(map[string]any{"invoice": p["invoice"]})
	if err != nil {
		return false, fmt.Errorf("validation: cel eval %q: %w", expr, err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("validation: cel expression %q did not evaluate to bool (got %T)", expr, out.Value())
	}
	return b, nil
}

// celEvaluator implements Evaluator for `type: cel` rules: params
// {"expr": <CEL string>}. expr is compiled and evaluated against an
// activation binding the single variable "invoice" to p["invoice"] (Decision
// N19 -- see file header). expr MUST evaluate to a bool: true => the rule
// passes (nil, nil); false => a violation (violation(r), nil). A compile
// error, an eval error, or a non-bool result type are all engine/config
// faults (Decision N15: fail loud on a broken rule, never a silent pass) --
// surfaced as (nil, non-nil error), never as a violation.
type celEvaluator struct{}

func (celEvaluator) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		Expr string `json:"expr"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: cel rule %q params: %w", r.Key, err)
	}
	if params.Expr == "" {
		return nil, fmt.Errorf("validation: cel rule %q: empty expr", r.Key)
	}
	ok, err := evalCELBool(params.Expr, p)
	if err != nil {
		return nil, fmt.Errorf("validation: cel rule %q: %w", r.Key, err)
	}
	if ok {
		return nil, nil
	}
	return violation(r), nil
}

// celGuard is the production GuardFunc (rule.go's GuardFunc type) backend
// for a rule's optional `when` select-stage clause: expr is compiled and
// evaluated the same way as celEvaluator.Eval's expr (activation variable
// "invoice" bound to p["invoice"], Decision N19), but the polarity is guard
// semantics rather than violation semantics -- true => the rule is
// applicable and evaluation proceeds; false => the rule is skipped with no
// violation. A compile error or an eval error is a non-nil error (same
// engine/config-fault class as celEvaluator -- Decision N15), NOT a silent
// skip. The executor wires this as the Engine's guard (engine.go's
// NewEngine second argument) at task-47 (M3-04-08 wiring subtask).
func celGuard(expr string, p Payload) (bool, error) {
	return evalCELBool(expr, p)
}
