// This file (registry_test.go) is QA Mode B coverage for M3-04-08's wiring
// seam (registry.go's NewDefaultEngine): the tests upstream in this package
// (engine_test.go, evaluators_test.go, evaluators_math_test.go, cel_test.go)
// all exercise their subject through a hand-rolled registry (NewEngine with
// a fake/partial map) or an Evaluator's Eval method directly -- none of them
// ever build the REAL production registry.go assembly and prove all ten
// RuleType keys actually resolve through it. That is exactly the shape of
// bug a wiring subtask can introduce silently: a RuleType constant added to
// rule.go's const block (or a typo'd map key in registry.go) that never
// makes it into NewDefaultEngine's registry map compiles fine and only
// surfaces at runtime as the engine's "unknown rule type" fault (Decision
// N15) -- for that ONE rule type, in production, on the first request that
// exercises it. This file closes that gap.
package validation

import (
	"encoding/json"
	"testing"
)

// registeredTypeCase pairs a RuleType with a minimal Rule of that type that
// is deliberately constructed to PASS (no violation) against
// registeredTypesPayload below when the type dispatches through the real
// registry. Using a passing fixture rather than merely checking err==nil
// makes the assertion positive (Eval actually ran the real evaluator logic
// and reached its pass branch), not just "didn't blow up" -- a registry
// entry that dispatched to the WRONG evaluator would very likely still
// error or violate against these params, so a clean pass is meaningful
// signal, not a vacuous one.
type registeredTypeCase struct {
	ruleType RuleType
	rule     Rule
}

// registeredTypesPayload is the shared fixture registeredTypeCases below
// evaluate against. Every field a case needs is present and shaped so that
// case's rule passes.
func registeredTypesPayload() Payload {
	return Payload{
		"invoice": map[string]any{
			"id":         "INV-1",
			"amount":     100.0,
			"currency":   "NGN",
			"issue_date": "2024-01-01",
			"left":       5.0,
			"right":      5.0,
			"supplier": map[string]any{
				"tin": "1234567890",
			},
		},
	}
}

// registeredTypeCases builds one passing Rule per RuleType constant defined
// in rule.go. If a new RuleType is ever added there, it must be added here
// too (or TestNewDefaultEngine_AllTypesRegistered silently stops covering
// it) -- there is no reflection-based enumeration of the RuleType consts to
// keep this list honest automatically.
func registeredTypeCases() []registeredTypeCase {
	return []registeredTypeCase{
		{TypeRequired, Rule{
			Key: "req-id", Type: TypeRequired, Target: "id",
			Params: json.RawMessage(`{}`), Severity: "error", Message: "id required",
			Scope: "document", Enabled: true,
		}},
		{TypeFormat, Rule{
			Key: "fmt-tin", Type: TypeFormat, Target: "supplier.tin",
			Params: json.RawMessage(`{"pattern":"^\\d{10}$"}`), Severity: "error", Message: "tin format",
			Scope: "document", Enabled: true,
		}},
		{TypeEnum, Rule{
			Key: "enum-currency", Type: TypeEnum, Target: "currency",
			Params: json.RawMessage(`{"values":["NGN","USD"]}`), Severity: "error", Message: "currency enum",
			Scope: "document", Enabled: true,
		}},
		{TypeRange, Rule{
			Key: "range-amount", Type: TypeRange, Target: "amount",
			Params: json.RawMessage(`{"min":0,"max":1000}`), Severity: "error", Message: "amount range",
			Scope: "document", Enabled: true,
		}},
		{TypeDate, Rule{
			Key: "date-issue", Type: TypeDate, Target: "issue_date",
			Params: json.RawMessage(`{}`), Severity: "error", Message: "issue date",
			Scope: "document", Enabled: true,
		}},
		{TypeTaxMath, Rule{
			Key: "tax-math", Type: TypeTaxMath,
			Params:   json.RawMessage(`{"base":100,"rate":0.075,"expected":7.5,"tolerance":0.01}`),
			Severity: "error", Message: "tax math", Scope: "document", Enabled: true,
		}},
		{TypeCrossField, Rule{
			Key: "cross-field", Type: TypeCrossField,
			Params:   json.RawMessage(`{"left":"left","op":"eq","right":"right"}`),
			Severity: "error", Message: "cross field", Scope: "document", Enabled: true,
		}},
		{TypeConditional, Rule{
			Key: "conditional", Type: TypeConditional,
			Params:   json.RawMessage(`{"if":{"field":"currency","op":"eq","value":"NGN"},"then":{"field":"id","required":true}}`),
			Severity: "error", Message: "conditional", Scope: "document", Enabled: true,
		}},
		{TypeCEL, Rule{
			Key: "cel-amount", Type: TypeCEL,
			Params:   json.RawMessage(`{"expr":"invoice.amount > 0"}`),
			Severity: "error", Message: "cel amount", Scope: "document", Enabled: true,
		}},
		{TypeLineSum, Rule{
			// registeredTypesPayload has no line_items, so a line_sum rule is
			// not-applicable and passes cleanly -- exactly the "constructed to
			// PASS under the correct evaluator" contract this table needs.
			Key: "line-sum", Type: TypeLineSum,
			Params:   json.RawMessage(`{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0.005}`),
			Severity: "error", Message: "line sum", Scope: "document", Enabled: true,
		}},
	}
}

// TestNewDefaultEngine_AllTypesRegistered proves every one of the ten
// RuleType constants (rule.go) resolves to a registered Evaluator in
// registry.go's NewDefaultEngine assembly -- the highest-risk wiring bug
// this subtask could introduce (an omitted map entry). A missing
// registration surfaces as Engine.Evaluate's "unknown rule type %q" error
// (engine.go, Decision N15); each case's fixture is additionally
// constructed to PASS cleanly (zero violations) when the CORRECT evaluator
// runs, so this also guards against a registry entry silently pointing at
// the wrong Evaluator for its key.
func TestNewDefaultEngine_AllTypesRegistered(t *testing.T) {
	engine := NewDefaultEngine()
	payload := registeredTypesPayload()

	for _, tc := range registeredTypeCases() {
		t.Run(string(tc.ruleType), func(t *testing.T) {
			result, err := engine.Evaluate(payload, RuleSet{Version: 1, Rules: []Rule{tc.rule}})
			if err != nil {
				t.Fatalf("NewDefaultEngine().Evaluate() with rule type %q: got error %v -- want the type to dispatch to a registered Evaluator with no dispatch/config fault (a missing NewDefaultEngine registration surfaces as \"unknown rule type\")", tc.ruleType, err)
			}
			if len(result.Violations) != 0 {
				t.Errorf("NewDefaultEngine().Evaluate() with rule type %q: got %d violation(s) %+v, want 0 -- fixture is constructed to pass under the correct evaluator", tc.ruleType, len(result.Violations), result.Violations)
			}
		})
	}
}

// TestNewDefaultEngine_EndToEndMixed proves the REAL assembled engine (not a
// fake/partial registry, as engine_test.go's suite uses) dispatches a
// mixed-type RuleSet correctly end to end: a failing `required`, a failing
// `cel`, and a passing `format/regex` rule together. Collect-ALL semantics
// (story Core AC #4) mean both failures are returned -- never fail-fast on
// the first -- sorted by rule key ascending (Decision N16, engine.go's
// aggregate stage), and the Result is stamped with the RuleSet's version.
func TestNewDefaultEngine_EndToEndMixed(t *testing.T) {
	engine := NewDefaultEngine()
	payload := Payload{
		"invoice": map[string]any{
			"currency": "NGN",
			"total":    0.0,
			// no "supplier" key at all: the required rule's target
			// ("supplier.tin") is absent -> violates.
		},
	}

	rules := []Rule{
		{
			Key: "missing-tin", Type: TypeRequired, Target: "supplier.tin",
			Params: json.RawMessage(`{}`), Severity: "error", Message: "TIN is required",
			Scope: "document", Enabled: true,
		},
		{
			Key: "bad-total", Type: TypeCEL,
			Params:   json.RawMessage(`{"expr":"invoice.total > 0"}`),
			Severity: "error", Message: "total must be positive", Scope: "document", Enabled: true,
		},
		{
			Key: "currency-format", Type: TypeFormat, Target: "currency",
			Params: json.RawMessage(`{"pattern":"^[A-Z]{3}$"}`), Severity: "error", Message: "currency format",
			Scope: "document", Enabled: true,
		},
	}

	result, err := engine.Evaluate(payload, RuleSet{Version: 42, Rules: rules})
	if err != nil {
		t.Fatalf("Evaluate() unexpected error: %v", err)
	}
	if result.RuleSetVersion != 42 {
		t.Errorf("RuleSetVersion = %d, want 42", result.RuleSetVersion)
	}
	if len(result.Violations) != 2 {
		t.Fatalf("len(Violations) = %d, want 2 (missing-tin + bad-total; currency-format passes) -- got %+v", len(result.Violations), result.Violations)
	}
	// Decision N16: sorted by rule key ascending -- "bad-total" < "missing-tin".
	if got, want := result.Violations[0].RuleKey, "bad-total"; got != want {
		t.Errorf("Violations[0].RuleKey = %q, want %q", got, want)
	}
	if got, want := result.Violations[1].RuleKey, "missing-tin"; got != want {
		t.Errorf("Violations[1].RuleKey = %q, want %q", got, want)
	}
	for _, v := range result.Violations {
		if v.RuleKey == "currency-format" {
			t.Errorf("currency-format should have passed (value %q matches ^[A-Z]{3}$), got a violation: %+v", "NGN", v)
		}
	}
}

// TestNewDefaultEngine_WhenGuardHonored proves celGuard (cel.go) is wired as
// NewDefaultEngine's select-stage GuardFunc -- not just that celGuard
// composes with a hand-rolled NewEngine call (cel_test.go's
// TestEngine_WithCELGuard_Integration already covers that), but that the
// factory this subtask ships actually passes it through. A rule with a
// `when` guard that would violate if evaluated is skipped for a payload the
// guard rejects, and applied (and violates) for a payload the guard
// accepts.
func TestNewDefaultEngine_WhenGuardHonored(t *testing.T) {
	engine := NewDefaultEngine()
	guardExpr := "invoice.country == 'NG'"
	rule := Rule{
		Key: "ng-tin-required", Type: TypeRequired, Target: "supplier.tin",
		Params: json.RawMessage(`{}`), Severity: "error", Message: "TIN required for Nigerian suppliers",
		Scope: "document", Enabled: true, When: &guardExpr,
	}
	rs := RuleSet{Version: 7, Rules: []Rule{rule}}

	t.Run("guard false: rule skipped despite would-be-violating data", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"country": "GH"}}
		result, err := engine.Evaluate(payload, rs)
		if err != nil {
			t.Fatalf("Evaluate() unexpected error: %v", err)
		}
		if len(result.Violations) != 0 {
			t.Errorf("guard should have skipped the rule for country=GH, got violations: %+v", result.Violations)
		}
	})

	t.Run("guard true: rule applies and violates on missing tin", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"country": "NG"}}
		result, err := engine.Evaluate(payload, rs)
		if err != nil {
			t.Fatalf("Evaluate() unexpected error: %v", err)
		}
		if len(result.Violations) != 1 || result.Violations[0].RuleKey != "ng-tin-required" {
			t.Errorf("guard should have applied the rule for country=NG (missing tin should violate), got: %+v", result.Violations)
		}
	})
}
