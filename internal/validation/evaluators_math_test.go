// M3-04-04 (Test-first: yes) -- evaluator tests for the three
// arithmetic/relational rule types (tax_math/cross_field/conditional),
// authored BEFORE the evaluators exist (RALPH Phase 3.5 / QA Mode A). Every
// evaluator's Eval currently panics("validation: not implemented") (see
// evaluators_math.go's STUB NOTICE), so this whole suite is RED until
// M3-04-04's implementation lands -- fixtures only, no real Nigerian rule
// content (that's M3-05; see story Out of Scope).
//
// Coverage (story Test Specs table rows, plus tolerance-boundary and
// conditional-predicate-variant edge cases pulled forward into this RED
// pass per the task brief):
//  1. TestTaxMath_WrongVatViolates / CorrectVatPasses / WithinTolerancePasses / OutsideToleranceViolates
//  2. TestCrossField_SumMismatchViolates / EqualPasses / LessThanPasses / LessThanViolates
//  3. TestConditional_IfTrueThenRequiredFails / IfTrueThenRequiredPasses / IfFalseSkips / IfTrueThenComparisonFails
//  4. TestMathEvaluator_BadParamsErrors -- tax_math/cross_field/conditional, undecodable params => error.
//
// Payloads are built as Payload{"invoice": map[string]any{...}} and, per
// the evaluators_math.go file-header contract, Rule.Target is left unset
// ("") for all three types -- their operative path(s) live inside Params,
// not on Rule.Target -- so these tests assert RuleKey/Severity/Message on a
// violation but not Path. mustEval (defined in evaluators_test.go, same
// package) recovers the current STUB panic into a t.Fatalf naming the
// concrete evaluator type, so each test fails independently and legibly
// during this RED phase; once the executor implements each Eval for real,
// mustEval is a simple pass-through.
package validation

import (
	"encoding/json"
	"testing"
)

// --- tax_math ---------------------------------------------------------

// TestTaxMath_WrongVatViolates (Test Spec): base "subtotal"=1000,
// rate 0.075, expected "vat"=50, tolerance 0.01 => violation
// (75 != 50, mismatch 25 far exceeds tolerance).
func TestTaxMath_WrongVatViolates(t *testing.T) {
	e := taxMathEval{}
	payload := Payload{"invoice": map[string]any{
		"subtotal": float64(1000),
		"vat":      float64(50),
	}}
	r := Rule{
		Key:      "TAXMATH-VAT",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.01}`),
		Severity: "error",
		Message:  "VAT does not match subtotal * rate",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: 1000*0.075=75, expected 50 -- mismatch 25 exceeds tolerance 0.01")
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

// TestTaxMath_CorrectVatPasses: vat=75 (exactly base*rate) => nil.
func TestTaxMath_CorrectVatPasses(t *testing.T) {
	e := taxMathEval{}
	payload := Payload{"invoice": map[string]any{
		"subtotal": float64(1000),
		"vat":      float64(75),
	}}
	r := Rule{
		Key:      "TAXMATH-VAT",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.01}`),
		Severity: "error",
		Message:  "VAT does not match subtotal * rate",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: 1000*0.075=75 exactly matches expected 75", v)
	}
}

// TestTaxMath_WithinTolerancePasses: vat=75.004, tolerance 0.01 => nil
// (mismatch 0.004 is within tolerance).
func TestTaxMath_WithinTolerancePasses(t *testing.T) {
	e := taxMathEval{}
	payload := Payload{"invoice": map[string]any{
		"subtotal": float64(1000),
		"vat":      75.004,
	}}
	r := Rule{
		Key:      "TAXMATH-VAT",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.01}`),
		Severity: "error",
		Message:  "VAT does not match subtotal * rate",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: mismatch 0.004 is within tolerance 0.01", v)
	}
}

// TestTaxMath_OutsideToleranceViolates: vat=75.02, tolerance 0.01 =>
// violation (mismatch 0.02 exceeds tolerance).
func TestTaxMath_OutsideToleranceViolates(t *testing.T) {
	e := taxMathEval{}
	payload := Payload{"invoice": map[string]any{
		"subtotal": float64(1000),
		"vat":      75.02,
	}}
	r := Rule{
		Key:      "TAXMATH-VAT",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.01}`),
		Severity: "error",
		Message:  "VAT does not match subtotal * rate",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: mismatch 0.02 exceeds tolerance 0.01")
	}
}

// --- cross_field --------------------------------------------------------

// TestCrossField_SumMismatchViolates (Test Spec): left="total"(100),
// op="eq", right="lines_sum"(90) => violation (100 != 90).
func TestCrossField_SumMismatchViolates(t *testing.T) {
	e := crossFieldEval{}
	payload := Payload{"invoice": map[string]any{
		"total":     float64(100),
		"lines_sum": float64(90),
	}}
	r := Rule{
		Key:      "CROSSFIELD-TOTAL",
		Type:     TypeCrossField,
		Params:   json.RawMessage(`{"left":"total","op":"eq","right":"lines_sum"}`),
		Severity: "error",
		Message:  "total must equal the sum of line items",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: total 100 != lines_sum 90")
	}
	if v.RuleKey != r.Key || v.Severity != r.Severity || v.Message != r.Message {
		t.Errorf("Eval() violation = %+v, want RuleKey=%q Severity=%q Message=%q", v, r.Key, r.Severity, r.Message)
	}
}

// TestCrossField_EqualPasses: total=100, lines_sum=100 => nil.
func TestCrossField_EqualPasses(t *testing.T) {
	e := crossFieldEval{}
	payload := Payload{"invoice": map[string]any{
		"total":     float64(100),
		"lines_sum": float64(100),
	}}
	r := Rule{
		Key:      "CROSSFIELD-TOTAL",
		Type:     TypeCrossField,
		Params:   json.RawMessage(`{"left":"total","op":"eq","right":"lines_sum"}`),
		Severity: "error",
		Message:  "total must equal the sum of line items",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: total 100 == lines_sum 100", v)
	}
}

// TestCrossField_LessThanPasses: op="lt", left=5, right=10 => nil (5 < 10
// holds).
func TestCrossField_LessThanPasses(t *testing.T) {
	e := crossFieldEval{}
	payload := Payload{"invoice": map[string]any{
		"paid":  float64(5),
		"total": float64(10),
	}}
	r := Rule{
		Key:      "CROSSFIELD-PAID-LT-TOTAL",
		Type:     TypeCrossField,
		Params:   json.RawMessage(`{"left":"paid","op":"lt","right":"total"}`),
		Severity: "error",
		Message:  "paid must be less than total",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: paid 5 < total 10", v)
	}
}

// TestCrossField_LessThanViolates: op="lt", left=10, right=5 => violation
// (10 < 5 does not hold).
func TestCrossField_LessThanViolates(t *testing.T) {
	e := crossFieldEval{}
	payload := Payload{"invoice": map[string]any{
		"paid":  float64(10),
		"total": float64(5),
	}}
	r := Rule{
		Key:      "CROSSFIELD-PAID-LT-TOTAL",
		Type:     TypeCrossField,
		Params:   json.RawMessage(`{"left":"paid","op":"lt","right":"total"}`),
		Severity: "error",
		Message:  "paid must be less than total",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: paid 10 is not less than total 5")
	}
}

// --- conditional ----------------------------------------------------------

// conditionalTINRule is the shared `if country==NG then supplier.tin
// required` rule used by TestConditional_IfTrueThenRequiredFails/Passes and
// TestConditional_IfFalseSkips.
func conditionalTINRule() Rule {
	return Rule{
		Key:      "CONDITIONAL-NG-TIN",
		Type:     TypeConditional,
		Params:   json.RawMessage(`{"if":{"field":"country","op":"eq","value":"NG"},"then":{"field":"supplier.tin","required":true}}`),
		Severity: "error",
		Message:  "NG invoices require a supplier TIN",
	}
}

// TestConditional_IfTrueThenRequiredFails (Test Spec): country="NG" (if
// holds) and supplier.tin ABSENT (then, a required predicate, fails) =>
// violation.
func TestConditional_IfTrueThenRequiredFails(t *testing.T) {
	e := conditionalEval{}
	payload := Payload{"invoice": map[string]any{"country": "NG"}} // supplier.tin absent
	r := conditionalTINRule()

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: country=NG (if true) and supplier.tin is absent (then fails)")
	}
	if v.RuleKey != r.Key || v.Severity != r.Severity || v.Message != r.Message {
		t.Errorf("Eval() violation = %+v, want RuleKey=%q Severity=%q Message=%q", v, r.Key, r.Severity, r.Message)
	}
}

// TestConditional_IfTrueThenRequiredPasses: same rule, supplier.tin PRESENT
// => nil (if true, then holds).
func TestConditional_IfTrueThenRequiredPasses(t *testing.T) {
	e := conditionalEval{}
	payload := Payload{"invoice": map[string]any{
		"country":  "NG",
		"supplier": map[string]any{"tin": "12345"},
	}}
	r := conditionalTINRule()

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: country=NG (if true) and supplier.tin is present (then holds)", v)
	}
}

// TestConditional_IfFalseSkips: same rule, country="GH" (if false) and
// supplier.tin STILL absent => nil -- `then` must never be evaluated when
// `if` is false, even though `then` would fail if it were checked.
func TestConditional_IfFalseSkips(t *testing.T) {
	e := conditionalEval{}
	payload := Payload{"invoice": map[string]any{"country": "GH"}} // supplier.tin absent, but if is false
	r := conditionalTINRule()

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: country=GH makes if false, so then (supplier.tin required) is never evaluated", v)
	}
}

// TestConditional_IfTrueThenComparisonFails: `then` is the comparison
// predicate variant (not the required variant) -- if country==NG (true)
// and amount=2000 fails then's amount<=1000 => violation.
func TestConditional_IfTrueThenComparisonFails(t *testing.T) {
	e := conditionalEval{}
	payload := Payload{"invoice": map[string]any{
		"country": "NG",
		"amount":  float64(2000),
	}}
	r := Rule{
		Key:      "CONDITIONAL-NG-AMOUNT-CAP",
		Type:     TypeConditional,
		Params:   json.RawMessage(`{"if":{"field":"country","op":"eq","value":"NG"},"then":{"field":"amount","op":"le","value":1000}}`),
		Severity: "warning",
		Message:  "NG invoices over 1000 require additional review",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: country=NG (if true) and amount 2000 is not <= 1000 (then fails)")
	}
	if v.RuleKey != r.Key || v.Severity != r.Severity || v.Message != r.Message {
		t.Errorf("Eval() violation = %+v, want RuleKey=%q Severity=%q Message=%q", v, r.Key, r.Severity, r.Message)
	}
}

// --- cross-cutting: bad params -------------------------------------------

// TestMathEvaluator_BadParamsErrors (Test Spec): undecodable params => a
// non-nil error (an engine/config fault, Decision N15), never a violation.
// Covers tax_math (rate wrong JSON type -- a non-number), cross_field (op
// wrong JSON type), and conditional (if wrong JSON type) -- one
// decode-error path per evaluator.
func TestMathEvaluator_BadParamsErrors(t *testing.T) {
	// Every target referenced by these params resolves to a present value,
	// so a broken evaluator that ignored bad params and fell through to a
	// "present, passes" path would be caught by err==nil here rather than
	// masked by an absent-path no-op.
	payload := Payload{"invoice": map[string]any{
		"subtotal": float64(1000),
		"vat":      float64(75),
		"total":    float64(100),
		"total2":   float64(100),
		"country":  "NG",
		"supplier": map[string]any{"tin": "12345"},
	}}

	tests := []struct {
		name   string
		eval   Evaluator
		typ    RuleType
		params json.RawMessage
	}{
		{
			"tax_math: rate is a non-number",
			taxMathEval{}, TypeTaxMath,
			json.RawMessage(`{"base":"subtotal","rate":"not-a-number","expected":"vat"}`),
		},
		{
			"tax_math: malformed JSON",
			taxMathEval{}, TypeTaxMath,
			json.RawMessage(`{"base":"subtotal","rate":`),
		},
		{
			"cross_field: op wrong JSON type",
			crossFieldEval{}, TypeCrossField,
			json.RawMessage(`{"left":"total","op":123,"right":"total2"}`),
		},
		{
			"conditional: if wrong JSON type",
			conditionalEval{}, TypeConditional,
			json.RawMessage(`{"if":"not-an-object","then":{"field":"supplier.tin","required":true}}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Rule{
				Key:      "BAD-PARAMS",
				Type:     tt.typ,
				Params:   tt.params,
				Severity: "error",
				Message:  "should never surface as a message -- config fault",
			}

			v, err := mustEval(t, tt.eval, payload, r)
			if err == nil {
				t.Fatal("Eval() error = nil, want non-nil for undecodable params")
			}
			if v != nil {
				t.Errorf("Eval() violation = %+v, want nil: a config-fault error and a violation are mutually exclusive", v)
			}
		})
	}
}
