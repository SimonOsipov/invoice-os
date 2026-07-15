// Unit coverage for lineSumEval (evaluators_math.go), the `line_sum` aggregate
// rule type: Σ(amount × quantity) over a line-item list compared to a scalar
// target with a tolerance. Pure Go (no DB) — mirrors the taxMathEval suite's
// style (mustEval helper, config-fault vs data-fault vs pass distinctions).
package validation

import (
	"encoding/json"
	"testing"
)

// lineSumRule is the seeded shape: sum unit_price*quantity over line_items,
// compared to subtotal within a kobo tolerance.
func lineSumRule() Rule {
	return Rule{
		Key:      "line-items-sum-subtotal",
		Type:     TypeLineSum,
		Params:   json.RawMessage(`{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0.005}`),
		Severity: "error",
		Message:  "Line item amounts must sum to the invoice subtotal.",
	}
}

func lineSumPayload(subtotal float64, items ...map[string]any) Payload {
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return Payload{"invoice": map[string]any{
		"subtotal":   subtotal,
		"line_items": list,
	}}
}

func TestLineSum_MatchesSubtotalPasses(t *testing.T) {
	e := lineSumEval{}
	// 10*100 + 1*75 = 1075 == subtotal.
	p := lineSumPayload(1075,
		map[string]any{"unit_price": 100.0, "quantity": 10.0},
		map[string]any{"unit_price": 75.0, "quantity": 1.0},
	)
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: 10*100 + 1*75 = 1075 matches subtotal 1075", v)
	}
}

func TestLineSum_MismatchViolates(t *testing.T) {
	e := lineSumEval{}
	// 10*100 = 1000, subtotal 900 -> off by 100.
	p := lineSumPayload(900, map[string]any{"unit_price": 100.0, "quantity": 10.0})
	r := lineSumRule()
	v, err := mustEval(t, e, p, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: Σ 1000 != subtotal 900")
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

func TestLineSum_QuantityAbsentIsWeightOne(t *testing.T) {
	e := lineSumEval{}
	// No quantity key on either line -> each contributes its raw unit_price:
	// 600 + 400 = 1000 == subtotal.
	p := lineSumPayload(1000,
		map[string]any{"unit_price": 600.0},
		map[string]any{"unit_price": 400.0},
	)
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: absent quantity is weight 1, 600+400=1000", v)
	}
}

func TestLineSum_NoLineItemsNotApplicable(t *testing.T) {
	e := lineSumEval{}
	// line_items absent entirely -> pass (line-items-required owns that case).
	p := Payload{"invoice": map[string]any{"subtotal": 1000.0}}
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: no line_items -> not applicable", v)
	}
}

func TestLineSum_EmptyLineItemsNotApplicable(t *testing.T) {
	e := lineSumEval{}
	p := lineSumPayload(1000) // empty list
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: empty line_items -> not applicable", v)
	}
}

func TestLineSum_BoundaryEqualsTolerancePasses(t *testing.T) {
	e := lineSumEval{}
	// Σ 1000.005, subtotal 1000 -> mismatch exactly 0.005 == tolerance (strict >).
	p := lineSumPayload(1000, map[string]any{"unit_price": 1000.005, "quantity": 1.0})
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: mismatch 0.005 == tolerance (strict >, so passes)", v)
	}
}

func TestLineSum_AbsentAmountOnLineViolates(t *testing.T) {
	e := lineSumEval{}
	// second line has no unit_price -> data fault -> violation.
	p := lineSumPayload(1000,
		map[string]any{"unit_price": 1000.0, "quantity": 1.0},
		map[string]any{"quantity": 2.0},
	)
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: a line with no amount cannot reconcile")
	}
}

func TestLineSum_NonNumericAmountViolates(t *testing.T) {
	e := lineSumEval{}
	p := lineSumPayload(1000, map[string]any{"unit_price": "abc", "quantity": 1.0})
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: non-numeric amount is a data fault")
	}
}

func TestLineSum_NonNumericQuantityViolates(t *testing.T) {
	e := lineSumEval{}
	p := lineSumPayload(1000, map[string]any{"unit_price": 100.0, "quantity": "ten"})
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: non-numeric quantity is a data fault")
	}
}

func TestLineSum_NonObjectLineViolates(t *testing.T) {
	e := lineSumEval{}
	p := Payload{"invoice": map[string]any{
		"subtotal":   1000.0,
		"line_items": []any{"not-an-object"},
	}}
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: a non-object line is malformed data")
	}
}

func TestLineSum_AbsentExpectedViolates(t *testing.T) {
	e := lineSumEval{}
	// line_items present but subtotal absent -> cannot reconcile -> violation.
	p := Payload{"invoice": map[string]any{
		"line_items": []any{map[string]any{"unit_price": 100.0, "quantity": 10.0}},
	}}
	v, err := mustEval(t, e, p, lineSumRule())
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: absent expected (subtotal) cannot reconcile")
	}
}

func TestLineSum_ExactDecimalNoFloatError(t *testing.T) {
	e := lineSumEval{}
	// 3 * 0.1 = 0.30000000000000004 in binary float; decimal math must land
	// exactly on 0.3 so a zero-tolerance rule still passes.
	r := Rule{
		Key:      "line-exact",
		Type:     TypeLineSum,
		Params:   json.RawMessage(`{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0}`),
		Severity: "error",
		Message:  "lines must reconcile",
	}
	p := lineSumPayload(0.3, map[string]any{"unit_price": 0.1, "quantity": 3.0})
	v, err := mustEval(t, e, p, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: exact decimal 3*0.1=0.3 must not accrue float error", v)
	}
}

func TestLineSum_BadParamsError(t *testing.T) {
	e := lineSumEval{}
	p := lineSumPayload(1000, map[string]any{"unit_price": 100.0, "quantity": 10.0})
	cases := []struct {
		name, params string
	}{
		{"missing amount", `{"items":"line_items","expected":"subtotal"}`},
		{"missing expected", `{"items":"line_items","amount":"unit_price"}`},
		{"missing items", `{"amount":"unit_price","expected":"subtotal"}`},
		{"negative tolerance", `{"items":"line_items","amount":"unit_price","expected":"subtotal","tolerance":-0.01}`},
		{"undecodable params", `["not","an","object"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Rule{Key: "line-bad", Type: TypeLineSum, Params: json.RawMessage(tc.params), Severity: "error", Message: "x"}
			v, err := mustEval(t, e, p, r)
			if err == nil {
				t.Fatalf("Eval() error = nil, want a config-fault error for %q (violation=%+v)", tc.name, v)
			}
			if v != nil {
				t.Errorf("Eval() violation = %+v, want nil on a config fault (fail loud, never a silent violation)", v)
			}
		})
	}
}
