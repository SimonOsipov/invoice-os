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
// STUB NOTICE (M3-04-04, QA Mode A / RED): every Eval method below is
// intentionally unimplemented -- each call panics with
// "validation: not implemented". This lets the package compile (so
// engine_test.go, schema_test.go, and evaluators_test.go's unrelated suites
// still build) and lets evaluators_math_test.go's RED suite fail for the
// right reason (not-implemented) rather than a compile error. The executor
// replaces each body next; the three struct names and the Evaluator
// interface they satisfy are the contract evaluators_math_test.go is
// written against and must not change silently.
package validation

// taxMathEval implements the `tax_math` rule type: params
// `{base, rate, expected, tolerance?}`. Passes when
// |expected - base*rate| <= tolerance; violates when the mismatch exceeds
// tolerance. base/expected are each a payload path (JSON string) or a
// literal (JSON number) -- see file-header contract.
type taxMathEval struct{}

// STUB (M3-04-04 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (taxMathEval) Eval(p Payload, r Rule) (*Violation, error) {
	panic("validation: not implemented")
}

// crossFieldEval implements the `cross_field` rule type: params
// `{left, op, right}`, left/right ALWAYS dotted payload paths. Passes when
// the relation `left op right` holds; violates when it does not.
type crossFieldEval struct{}

// STUB (M3-04-04 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (crossFieldEval) Eval(p Payload, r Rule) (*Violation, error) {
	panic("validation: not implemented")
}

// conditionalEval implements the `conditional` rule type: params
// `{if:{field,op,value}, then:{...predicate}}`. Passes when `if` is false
// (then is never evaluated) or when `if` is true and `then` holds;
// violates when `if` is true and `then` fails. See file-header contract for
// the two `then` predicate shapes.
type conditionalEval struct{}

// STUB (M3-04-04 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (conditionalEval) Eval(p Payload, r Rule) (*Violation, error) {
	panic("validation: not implemented")
}
