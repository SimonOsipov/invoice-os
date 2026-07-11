// This file (registry.go) is the M3-04-08 wiring seam: NewDefaultEngine
// assembles the production Engine by registering all nine rule-type
// Evaluators (the five presence/shape evaluators from evaluators.go, the
// three arithmetic/relational evaluators from evaluators_math.go, and the
// CEL escape hatch from cel.go) against their RuleType keys, and injects
// celGuard as the select-stage `when`-clause GuardFunc.
//
// The nine evaluator structs are deliberately unexported (unit-tested
// directly against their Eval method, no registry needed), so cmd/validation
// (package main) cannot build the registry itself — this exported factory is
// the single seam through which the binary obtains a fully-wired Engine.
package validation

// NewDefaultEngine builds the production engine: all nine rule-type
// evaluators registered against their RuleType keys, with celGuard as the
// select-stage `when`-clause guard. This is the registry assembly deferred
// out of M3-04-03/04/05 (see those files' headers) into this wiring subtask.
func NewDefaultEngine() *Engine {
	registry := map[RuleType]Evaluator{
		TypeRequired:    requiredEval{},
		TypeFormat:      formatEval{},
		TypeEnum:        enumEval{},
		TypeRange:       rangeEval{},
		TypeDate:        dateEval{},
		TypeTaxMath:     taxMathEval{},
		TypeCrossField:  crossFieldEval{},
		TypeConditional: conditionalEval{},
		TypeCEL:         celEvaluator{},
	}
	return NewEngine(registry, celGuard)
}
