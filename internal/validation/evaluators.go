// This file (evaluators.go) is the M3-04-03 subtask: the five
// presence/shape rule-type Evaluators -- required, format/regex, enum,
// range, date -- from the story's "9 rule-type evaluators (contracts)"
// table. The three arithmetic/relational evaluators (tax_math, cross_field,
// conditional) land in M3-04-04 (evaluators_math.go); the cel escape hatch
// in M3-04-05 (cel.go). Registry assembly (wiring these structs into the
// map[RuleType]Evaluator NewEngine takes) is deferred to M3-04-08 -- these
// are unit-tested directly against their Eval method here, no registry
// needed for this subtask.
//
// Each evaluator resolves Rule.Target via resolvePath (engine.go, rooted at
// p["invoice"], NO "invoice." prefix -- Decision N19) and decodes
// Rule.Params (json.RawMessage) into its own typed params struct. Contract,
// verbatim from the story's evaluator table:
//   - Absent target + non-required type => pass (nil, nil); presence is
//     required's job, not the other four's.
//   - A present value that fails the type's check => a non-nil *Violation
//     carrying RuleKey=r.Key, Severity=r.Severity, Message=r.Message,
//     Path=r.Target (the resolved dotted path, for the M3-09 UI).
//   - Malformed/undecodable Params => a non-nil error (an engine/config
//     fault -- Decision N15: fail loud on a broken rule, NEVER a silent
//     pass), not a violation. A present-but-not-the-right-shape VALUE
//     (e.g. range's target resolving to a non-numeric string) is the
//     opposite case: that's a violation, not a config error -- the rule
//     itself is fine, the DATA is bad.
//
// STUB NOTICE (M3-04-03, QA Mode A / RED): every Eval method below is
// intentionally unimplemented -- each call panics with
// "validation: not implemented". This lets the package compile (so
// engine_test.go and schema_test.go's unrelated suites still build) and
// lets evaluators_test.go's RED suite fail for the right reason
// (not-implemented) rather than a compile error. The executor replaces
// each body next; the five struct names and the Evaluator interface they
// satisfy are the contract evaluators_test.go is written against and must
// not change silently.
package validation

// requiredEval implements the `required` rule type: params `{}` (optional
// `allow_blank` bool). Passes when the target is present (and, unless
// allow_blank, non-blank); violates when absent, null, or blank.
type requiredEval struct{}

// STUB (M3-04-03 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (requiredEval) Eval(p Payload, r Rule) (*Violation, error) {
	panic("validation: not implemented")
}

// formatEval implements the `format/regex` rule type: params `{pattern}`.
// Passes when the present target value matches pattern (compiled once per
// request/call); violates when it does not match. Absent target => pass
// (presence is required's job).
type formatEval struct{}

// STUB (M3-04-03 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (formatEval) Eval(p Payload, r Rule) (*Violation, error) {
	panic("validation: not implemented")
}

// enumEval implements the `enum` rule type: params `{values:[...]}`.
// Passes when the present target value is a member of values; violates
// when it is not. Absent target => pass.
type enumEval struct{}

// STUB (M3-04-03 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (enumEval) Eval(p Payload, r Rule) (*Violation, error) {
	panic("validation: not implemented")
}

// rangeEval implements the `range` rule type: params
// `{min?,max?,exclusive_min?,exclusive_max?}`. Passes when the present
// target value is numeric and within bounds; violates when it is outside
// bounds OR not numeric at all (a bad VALUE is a violation, not a config
// error -- see file-header contract). Absent target => pass.
type rangeEval struct{}

// STUB (M3-04-03 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (rangeEval) Eval(p Payload, r Rule) (*Violation, error) {
	panic("validation: not implemented")
}

// dateEval implements the `date` rule type: params
// `{format?, not_before?, not_after?, relative_to?}`. Passes when the
// present target value parses (per format, default ISO date) and falls
// within the temporal bounds; violates when unparseable or out of bounds.
// Absent target => pass.
type dateEval struct{}

// STUB (M3-04-03 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (dateEval) Eval(p Payload, r Rule) (*Violation, error) {
	panic("validation: not implemented")
}
