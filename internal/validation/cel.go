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
// STUB NOTICE (QA Mode A, M3-04-05): this file is the minimal compilable
// skeleton RALPH's RED pass needs -- both Eval and celGuard panic
// "validation: not implemented" until the executor wires the real cel-go
// (github.com/google/cel-go) program compile+eval. Do NOT add the cel-go
// dependency here; that is the executor's job for this subtask. Registry
// assembly (wiring celEvaluator into the map[RuleType]Evaluator NewEngine
// takes, and celGuard as the Engine's GuardFunc) is deferred to M3-04-08.
package validation

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
	panic("validation: not implemented")
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
	panic("validation: not implemented")
}
