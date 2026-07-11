// This file (engine.go) is the M3-04-02 pipeline: Engine.Evaluate runs
// cache -> select -> evaluate-ALL -> aggregate against a RuleSet (story
// Core AC #2, #4). It dispatches to whatever Evaluators are registered and
// whatever GuardFunc is injected -- both get concrete implementations in
// later subtasks (evaluators: M3-04-03/04; CEL guard backend: M3-04-05).
// The engine core has NO DB import: LoadActiveRuleSet (the DB "load" stage)
// lives one layer up, in the M3-04-06 Store.
//
// STUB NOTICE (M3-04-02, QA Mode A / RED): Evaluate and resolvePath below
// are intentionally unimplemented -- every call panics with
// "validation: not implemented". This lets the package compile (so
// schema_test.go's unrelated DB-backed suite still builds) and lets
// engine_test.go's RED suite fail for the right reason (not-implemented)
// rather than a compile error. The executor replaces both bodies next; the
// exported surface fixed here -- the Engine struct's field set, the
// NewEngine constructor signature, and the Evaluate method signature -- is
// the contract engine_test.go is written against and must not change
// silently.
package validation

// Engine is the stateless rules-as-data pipeline: a registry mapping each
// RuleType to its Evaluator, plus the GuardFunc used to resolve a rule's
// optional `when` select-stage guard.
type Engine struct {
	registry map[RuleType]Evaluator
	guard    GuardFunc
}

// NewEngine constructs an Engine from a fully-populated evaluator registry
// and a guard backend. Both are caller-supplied so engine_test.go can
// inject fakes without depending on the concrete evaluators (M3-04-03/04)
// or the CEL guard backend (M3-04-05); cmd/validation/main.go (M3-04-08)
// supplies the real registry + CEL guard.
func NewEngine(registry map[RuleType]Evaluator, guard GuardFunc) *Engine {
	return &Engine{registry: registry, guard: guard}
}

// Evaluate runs cache -> select -> evaluate-ALL -> aggregate against rs and
// returns a Result stamped with rs.Version (story Core AC #2).
//
//   - select: a rule is applicable iff Enabled && Scope=="document" &&
//     (When==nil || guard(*When, p)==true).
//   - evaluate-ALL: every applicable rule's registered Evaluator runs;
//     ALL violations are collected -- never fail-fast (story Core AC #4).
//   - aggregate: violations are sorted deterministically (rule key
//     ascending, then path -- Decision N16) before being returned;
//     Violations is [] when nothing violated, never nil.
//   - an unknown RuleType (not present in the registry) is an
//     engine/config fault: Evaluate returns a non-nil error and no partial
//     Result (Decision N15 -- fail loud, never a silent pass).
//
// STUB (M3-04-02 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func (e *Engine) Evaluate(p Payload, rs RuleSet) (Result, error) {
	panic("validation: not implemented")
}

// resolvePath resolves a dotted target (e.g. "supplier.tin") against the
// invoice object rooted at p["invoice"] -- NO "invoice." prefix (Decision
// N19; contrast with CEL expressions, which DO prefix "invoice." -- that is
// the M3-04-05 CEL backend's concern, not this resolver's). Returns the
// resolved value and whether the full path was present; a missing key, or
// a path segment that walks through a value that is not itself a map, both
// report present=false.
//
// STUB (M3-04-02 RED): unimplemented -- panics. See the file-header STUB
// NOTICE above.
func resolvePath(p Payload, dotted string) (any, bool) {
	panic("validation: not implemented")
}
