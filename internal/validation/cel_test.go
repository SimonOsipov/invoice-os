// M3-04-05 (Test-first: yes) -- tests for the `type: cel` escape-hatch
// Evaluator (celEvaluator) and the production `when`-guard backend
// (celGuard), authored BEFORE the CEL wiring exists (RALPH Phase 3.5 / QA
// Mode A). Both celEvaluator.Eval and celGuard currently panic
// "validation: not implemented" (see cel.go's STUB NOTICE), so this whole
// suite is RED until M3-04-05's implementation lands -- fixtures only, no
// real Nigerian rule content (that's M3-05; see story Out of Scope).
//
// Coverage (see cel.go file-header contract, Decision N19):
//  1. TestCEL_FalseExprViolates / TrueExprPasses -- expr => bool drives
//     violation vs pass.
//  2. TestCEL_BadExprErrors / NonBoolExprErrors -- a compile fault and a
//     non-bool result are both engine/config faults (Decision N15), never a
//     violation and never a silent pass.
//  3. TestGuard_FalseSkipsRule / TrueApplies / BadExprErrors -- celGuard's
//     (bool, error) contract, called directly (celGuard is a plain
//     GuardFunc, not something reached through the Engine in this
//     subtask -- registry/guard wiring is M3-04-08).
//  4. TestCEL_StringComparison -- non-numeric operand sanity check.
//
// Payloads are built as Payload{"invoice": map[string]any{...}} and every
// expr below uses the "invoice."-prefixed CEL rooting form (Decision N19) --
// UNLIKE the no-prefix Rule.Target convention the other eight evaluators use
// (evaluators.go/evaluators_math.go's resolvePath). mustEval (defined in
// evaluators_test.go, same package) recovers celEvaluator.Eval's current
// STUB panic into a t.Fatalf; mustGuard below does the equivalent for
// celGuard, which is a plain function rather than an Evaluator so it cannot
// reuse mustEval directly.
package validation

import (
	"encoding/json"
	"strings"
	"testing"
)

// mustGuard calls celGuard(expr, p) and recovers its current
// "not implemented" STUB panic into a t.Fatalf, mirroring mustEval
// (evaluators_test.go) for celGuard's plain-function shape (GuardFunc, not
// an Evaluator) so a pre-implementation run fails each guard test
// independently and legibly instead of crashing the whole test binary.
func mustGuard(t *testing.T, expr string, p Payload) (bool, error) {
	t.Helper()
	var (
		ok  bool
		err error
	)
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("celGuard panicked (pre-implementation STUB): %v", rec)
			}
		}()
		ok, err = celGuard(expr, p)
	}()
	return ok, err
}

// --- celEvaluator (type: cel) -----------------------------------------

// TestCEL_FalseExprViolates: expr "invoice.total > 0" against total=-5 =>
// a *Violation carrying the rule's key/severity/message.
func TestCEL_FalseExprViolates(t *testing.T) {
	e := celEvaluator{}
	payload := Payload{"invoice": map[string]any{"total": float64(-5)}}
	r := Rule{
		Key:      "CEL-TOTAL-POSITIVE",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{"expr":"invoice.total > 0"}`),
		Severity: "error",
		Message:  "invoice total must be positive",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: invoice.total=-5 does not satisfy invoice.total > 0")
	}
	if v.RuleKey != r.Key {
		t.Errorf("RuleKey = %q, want %q", v.RuleKey, r.Key)
	}
	if v.Severity != r.Severity {
		t.Errorf("Severity = %q, want %q", v.Severity, r.Severity)
	}
	if v.Message != r.Message {
		t.Errorf("Message = %q, want %q", v.Message, r.Message)
	}
}

// TestCEL_TrueExprPasses: same expr, total=10 => nil.
func TestCEL_TrueExprPasses(t *testing.T) {
	e := celEvaluator{}
	payload := Payload{"invoice": map[string]any{"total": float64(10)}}
	r := Rule{
		Key:      "CEL-TOTAL-POSITIVE",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{"expr":"invoice.total > 0"}`),
		Severity: "error",
		Message:  "invoice total must be positive",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: invoice.total=10 satisfies invoice.total > 0", v)
	}
}

// TestCEL_BadExprErrors: an expr that does not compile as CEL is a config
// fault (Decision N15) -- a non-nil error, NOT a violation and NOT a silent
// pass.
func TestCEL_BadExprErrors(t *testing.T) {
	e := celEvaluator{}
	payload := Payload{"invoice": map[string]any{"total": float64(10)}}
	r := Rule{
		Key:      "CEL-BAD-EXPR",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{"expr":"this is not cel @@@"}`),
		Severity: "error",
		Message:  "should never surface -- config fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Fatal("Eval() error = nil, want non-nil: expr does not compile as CEL")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil alongside a config-fault error", v)
	}
}

// TestCEL_NonBoolExprErrors: expr "invoice.total" evaluates to a number, not
// a bool. A `type: cel` rule's expr contract (cel.go file header) requires a
// bool result -- a non-bool result is a config fault (Decision N15), same
// class as a compile error, NOT a coercion and NOT a silent pass.
func TestCEL_NonBoolExprErrors(t *testing.T) {
	e := celEvaluator{}
	payload := Payload{"invoice": map[string]any{"total": float64(10)}}
	r := Rule{
		Key:      "CEL-NON-BOOL",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{"expr":"invoice.total"}`),
		Severity: "error",
		Message:  "should never surface -- config fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Fatal("Eval() error = nil, want non-nil: invoice.total evaluates to a number, not a bool")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil alongside a config-fault error", v)
	}
}

// TestCEL_StringComparison: a non-numeric (string) operand sanity check --
// expr "invoice.currency == 'NGN'" violates for "USD" and passes for "NGN".
func TestCEL_StringComparison(t *testing.T) {
	r := Rule{
		Key:      "CEL-CURRENCY-NGN",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{"expr":"invoice.currency == 'NGN'"}`),
		Severity: "error",
		Message:  "invoice currency must be NGN",
	}

	t.Run("mismatch violates", func(t *testing.T) {
		e := celEvaluator{}
		payload := Payload{"invoice": map[string]any{"currency": "USD"}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("Eval() violation = nil, want non-nil: \"USD\" != \"NGN\"")
		}
	})

	t.Run("match passes", func(t *testing.T) {
		e := celEvaluator{}
		payload := Payload{"invoice": map[string]any{"currency": "NGN"}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("Eval() violation = %+v, want nil: \"NGN\" == \"NGN\"", v)
		}
	})
}

// --- celGuard (when-guard backend) --------------------------------------

// TestGuard_FalseSkipsRule: celGuard("invoice.country == 'NG'", ...) against
// country="GH" => (false, nil) -- the rule is not applicable, no error.
func TestGuard_FalseSkipsRule(t *testing.T) {
	payload := Payload{"invoice": map[string]any{"country": "GH"}}

	ok, err := mustGuard(t, "invoice.country == 'NG'", payload)
	if err != nil {
		t.Fatalf("celGuard() unexpected error: %v", err)
	}
	if ok {
		t.Error("celGuard() = true, want false: country=\"GH\" != \"NG\"")
	}
}

// TestGuard_TrueApplies: same expr, country="NG" => (true, nil).
func TestGuard_TrueApplies(t *testing.T) {
	payload := Payload{"invoice": map[string]any{"country": "NG"}}

	ok, err := mustGuard(t, "invoice.country == 'NG'", payload)
	if err != nil {
		t.Fatalf("celGuard() unexpected error: %v", err)
	}
	if !ok {
		t.Error("celGuard() = false, want true: country=\"NG\" == \"NG\"")
	}
}

// TestGuard_BadExprErrors: an expr that does not compile as CEL is a
// non-nil error (same config-fault class as celEvaluator -- Decision N15),
// never a silent skip.
func TestGuard_BadExprErrors(t *testing.T) {
	payload := Payload{"invoice": map[string]any{"country": "NG"}}

	ok, err := mustGuard(t, "!!bad", payload)
	if err == nil {
		t.Fatal("celGuard() error = nil, want non-nil: \"!!bad\" does not compile as CEL")
	}
	if ok {
		t.Error("celGuard() = true, want false alongside a config-fault error")
	}
}

// --- QA Mode B: adversarial + integration coverage ----------------------
//
// The suite above (RALPH Phase 3.5 / QA Mode A) proves celEvaluator and
// celGuard each satisfy their bool-contract in isolation. It does NOT prove
// (a) celGuard actually composes with the M3-04-02 Engine pipeline as the
// injected GuardFunc -- the unit tests above call celGuard directly, never
// through Engine.Evaluate -- nor (b) the escape hatch's headline uses
// (nested field access, cross-field arithmetic) or its sharper edges
// (absent field, absent expr, compound guard, full violation-field
// carry-through). QA Mode B adds both below.

// TestEngine_WithCELGuard_Integration proves celGuard composes with
// Engine.Evaluate as a real GuardFunc (engine.go's select stage calls
// e.guard(*rule.When, p) exactly as NewEngine wires it here) -- not a fake
// guard standing in for it. A rule with When=&guardExpr backed by a fake
// Evaluator that would always violate is skipped when the guard expr
// evaluates false and DOES run (and its violation is collected) when the
// guard expr evaluates true -- the same false/true pairing
// TestEngine_WhenGuardSkips uses with a fake guard, but here the guard is
// the production celGuard (M3-04-05's own deliverable), proving the two
// subtasks actually integrate as the story's design claims.
func TestEngine_WithCELGuard_Integration(t *testing.T) {
	const fakeType RuleType = "fake-cel-guard-probe"
	guardExpr := "invoice.country == 'NG'"
	registry := map[RuleType]Evaluator{
		fakeType: fakeEval{v: &Violation{RuleKey: "guarded-rule", Severity: "error", Message: "guarded rule fired"}},
	}
	rule := Rule{
		Key: "guarded-rule", Type: fakeType, Severity: "error", Message: "guarded rule fired",
		Scope: "document", Enabled: true, When: &guardExpr,
	}
	e := NewEngine(registry, celGuard)

	t.Run("country != NG: real celGuard skips the rule via Engine.Evaluate", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"country": "GH"}}
		got, err := mustEvaluate(t, e, payload, RuleSet{Version: 1, Rules: []Rule{rule}})
		if err != nil {
			t.Fatalf("Evaluate() unexpected error: %v", err)
		}
		if len(got.Violations) != 0 {
			t.Fatalf("len(Violations) = %d, want 0 -- celGuard(\"invoice.country == 'NG'\", ...) must evaluate false for country=GH and select-stage-skip the rule when wired as NewEngine's GuardFunc", len(got.Violations))
		}
	})

	t.Run("country == NG: real celGuard applies, rule runs and violates", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"country": "NG"}}
		got, err := mustEvaluate(t, e, payload, RuleSet{Version: 1, Rules: []Rule{rule}})
		if err != nil {
			t.Fatalf("Evaluate() unexpected error: %v", err)
		}
		if len(got.Violations) != 1 {
			t.Fatalf("len(Violations) = %d, want 1 -- celGuard must evaluate true for country=NG and let Engine.Evaluate run the rule through to a collected violation (positive counterpart proving the skip above isn't vacuous)", len(got.Violations))
		}
		if got.Violations[0].RuleKey != "guarded-rule" {
			t.Errorf("Violations[0].RuleKey = %q, want %q", got.Violations[0].RuleKey, "guarded-rule")
		}
	})
}

// TestCEL_NestedFieldAccess: expr "invoice.supplier.tin == '12345'" over a
// nested {"invoice":{"supplier":{"tin": ...}}} payload -- the escape hatch
// must walk multi-level dotted CEL field selection, not just top-level
// invoice.<field>.
func TestCEL_NestedFieldAccess(t *testing.T) {
	e := celEvaluator{}
	r := Rule{
		Key:      "CEL-SUPPLIER-TIN",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{"expr":"invoice.supplier.tin == '12345'"}`),
		Severity: "error",
		Message:  "supplier TIN must match",
	}

	t.Run("mismatch violates", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"supplier": map[string]any{"tin": "99999"}}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("Eval() violation = nil, want non-nil: nested supplier.tin=99999 != 12345")
		}
	})

	t.Run("match passes", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"supplier": map[string]any{"tin": "12345"}}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("Eval() violation = %+v, want nil: nested supplier.tin=12345 == 12345", v)
		}
	})
}

// TestCEL_ArithmeticCrossField: expr
// "invoice.total == invoice.subtotal + invoice.vat" -- the escape hatch's
// headline use (a cross-field arithmetic check no fixed evaluator covers).
func TestCEL_ArithmeticCrossField(t *testing.T) {
	e := celEvaluator{}
	r := Rule{
		Key:      "CEL-TOTAL-EQUALS-SUM",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{"expr":"invoice.total == invoice.subtotal + invoice.vat"}`),
		Severity: "error",
		Message:  "total must equal subtotal + vat",
	}

	t.Run("mismatch violates", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"total": float64(200), "subtotal": float64(100), "vat": float64(15)}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("Eval() violation = nil, want non-nil: 200 != 100+15")
		}
	})

	t.Run("match passes", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"total": float64(115), "subtotal": float64(100), "vat": float64(15)}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("Eval() violation = %+v, want nil: 115 == 100+15", v)
		}
	})
}

// TestCEL_MissingInvoiceFieldErrors: expr "invoice.nonexistent > 0"
// references a field absent from the payload. Pinned via a probe
// (evalCELBool directly, outside this file) against the actual cel-go
// behavior: field selection on a DynType map for an absent key is an
// EVAL-time fault ("no such key: nonexistent"), not a compile fault and not
// a silent false/pass -- confirming Decision N15 (fail loud) covers this
// case too, distinct from TestCEL_BadExprErrors's compile-time fault.
func TestCEL_MissingInvoiceFieldErrors(t *testing.T) {
	e := celEvaluator{}
	payload := Payload{"invoice": map[string]any{"total": float64(10)}}
	r := Rule{
		Key:      "CEL-MISSING-FIELD",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{"expr":"invoice.nonexistent > 0"}`),
		Severity: "error",
		Message:  "should never surface -- config/data fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Fatal("Eval() error = nil, want non-nil: invoice.nonexistent is absent from the payload -- cel-go's dynamic map field selection reports \"no such key\" as an eval-time error, and celEvaluator.Eval must surface it (Decision N15), not silently pass or violate")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil alongside the error", v)
	}
	if !strings.Contains(err.Error(), "no such key") {
		t.Errorf("error = %q, want it to mention the missing key -- pins cel-go's actual no-such-key eval fault rather than asserting on any error string", err.Error())
	}
}

// TestGuard_CompoundExpr: celGuard("invoice.country == 'NG' && invoice.total
// > 100", ...) -- a compound (&&) guard expr, exercising both operands
// independently so this test cannot pass by only checking one clause.
func TestGuard_CompoundExpr(t *testing.T) {
	expr := "invoice.country == 'NG' && invoice.total > 100"

	t.Run("both clauses true: guard applies", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"country": "NG", "total": float64(150)}}
		ok, err := mustGuard(t, expr, payload)
		if err != nil {
			t.Fatalf("celGuard() unexpected error: %v", err)
		}
		if !ok {
			t.Error("celGuard() = false, want true: country=NG && total=150>100")
		}
	})

	t.Run("country clause false: guard skips", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"country": "GH", "total": float64(150)}}
		ok, err := mustGuard(t, expr, payload)
		if err != nil {
			t.Fatalf("celGuard() unexpected error: %v", err)
		}
		if ok {
			t.Error("celGuard() = true, want false: country=GH fails the first && clause regardless of total")
		}
	})

	t.Run("total clause false: guard skips", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"country": "NG", "total": float64(50)}}
		ok, err := mustGuard(t, expr, payload)
		if err != nil {
			t.Fatalf("celGuard() unexpected error: %v", err)
		}
		if ok {
			t.Error("celGuard() = true, want false: total=50 fails the second && clause even though country=NG")
		}
	})
}

// TestCEL_BadParamsMissingExpr: rule params {} (no "expr" key) decodes to
// params.Expr=="" -- celEvaluator.Eval's explicit empty-expr guard (cel.go)
// must surface this as an error, not silently no-op the rule.
func TestCEL_BadParamsMissingExpr(t *testing.T) {
	e := celEvaluator{}
	payload := Payload{"invoice": map[string]any{"total": float64(10)}}
	r := Rule{
		Key:      "CEL-NO-EXPR",
		Type:     TypeCEL,
		Params:   json.RawMessage(`{}`),
		Severity: "error",
		Message:  "should never surface -- config fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Fatal("Eval() error = nil, want non-nil: params carry no \"expr\" key")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil alongside a config-fault error", v)
	}
}

// TestCEL_ViolationCarriesAllFields: a false-expr rule with a distinct
// Key/Severity/Message (and a Target, for Path) from every other test in
// this file, so a copy-paste bug that hard-codes a fixture's fields instead
// of reading them off r would be caught here.
func TestCEL_ViolationCarriesAllFields(t *testing.T) {
	e := celEvaluator{}
	payload := Payload{"invoice": map[string]any{"currency": "USD"}}
	r := Rule{
		Key:      "CEL-FIELD-CARRY-CHECK",
		Type:     TypeCEL,
		Target:   "currency",
		Params:   json.RawMessage(`{"expr":"invoice.currency == 'NGN'"}`),
		Severity: "warning",
		Message:  "distinct message for the field-carry assertion",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: currency=USD != NGN")
	}
	if v.RuleKey != r.Key {
		t.Errorf("RuleKey = %q, want %q", v.RuleKey, r.Key)
	}
	if v.Severity != r.Severity {
		t.Errorf("Severity = %q, want %q", v.Severity, r.Severity)
	}
	if v.Message != r.Message {
		t.Errorf("Message = %q, want %q", v.Message, r.Message)
	}
	if v.Path != r.Target {
		t.Errorf("Path = %q, want %q", v.Path, r.Target)
	}
}
