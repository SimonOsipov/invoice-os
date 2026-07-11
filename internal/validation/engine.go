// This file (engine.go) is the M3-04-02 pipeline: Engine.Evaluate runs
// cache -> select -> evaluate-ALL -> aggregate against a RuleSet (story
// Core AC #2, #4). It dispatches to whatever Evaluators are registered and
// whatever GuardFunc is injected -- both get concrete implementations in
// later subtasks (evaluators: M3-04-03/04; CEL guard backend: M3-04-05).
// The engine core has NO DB import: LoadActiveRuleSet (the DB "load" stage)
// lives one layer up, in the M3-04-06 Store.
//
// The exported surface -- the Engine struct's field set, the NewEngine
// constructor signature, and the Evaluate method signature -- is the contract
// engine_test.go is written against and must not change silently.
package validation

import (
	"fmt"
	"sort"
	"strings"
)

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
//     Result (Decision N15 -- fail loud, never a silent pass). A guard error
//     and an Evaluator error are the same class of fault -- both surface as
//     (Result{}, err), never as a violation.
func (e *Engine) Evaluate(p Payload, rs RuleSet) (Result, error) {
	// cache: no-op at this stage -- per-request compilation lives in the
	// evaluators (M3-04-03/04/05), not the pipeline core.

	// Violations must never be nil so an empty result marshals as [] not
	// null (Result doc / story Core AC).
	violations := []Violation{}

	for i := range rs.Rules {
		rule := rs.Rules[i]

		// select stage.
		if !rule.Enabled || rule.Scope != "document" {
			continue
		}
		if rule.When != nil {
			ok, err := e.guard(*rule.When, p)
			if err != nil {
				return Result{}, err
			}
			if !ok {
				continue
			}
		}

		// evaluate-ALL stage (collect-all, not fail-fast).
		ev, ok := e.registry[rule.Type]
		if !ok {
			return Result{}, fmt.Errorf("validation: unknown rule type %q", rule.Type)
		}
		v, err := ev.Eval(p, rule)
		if err != nil {
			return Result{}, err
		}
		if v != nil {
			violations = append(violations, *v)
		}
	}

	// aggregate stage: deterministic order -- rule key ascending, then path
	// ascending (Decision N16 -- stable output for the M3-10 golden suite).
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].RuleKey != violations[j].RuleKey {
			return violations[i].RuleKey < violations[j].RuleKey
		}
		return violations[i].Path < violations[j].Path
	})

	return Result{RuleSetVersion: rs.Version, Violations: violations}, nil
}

// resolvePath resolves a dotted target (e.g. "supplier.tin") against the
// invoice object rooted at p["invoice"] -- NO "invoice." prefix (Decision
// N19; contrast with CEL expressions, which DO prefix "invoice." -- that is
// the M3-04-05 CEL backend's concern, not this resolver's). Returns the
// resolved value and whether the full path was present; a missing key, or
// a path segment that walks through a value that is not itself a map, both
// report present=false. An absent p["invoice"] reports present=false; an
// empty dotted path resolves to the invoice object itself.
func resolvePath(p Payload, dotted string) (any, bool) {
	current, ok := p["invoice"]
	if !ok {
		return nil, false
	}
	if dotted == "" {
		return current, true
	}
	for _, seg := range strings.Split(dotted, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		current = v
	}
	return current, true
}
