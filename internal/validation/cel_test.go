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
