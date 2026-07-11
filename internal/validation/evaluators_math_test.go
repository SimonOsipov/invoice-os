// M3-04-04 (Test-first: yes) -- evaluator tests for the three
// arithmetic/relational rule types (tax_math/cross_field/conditional).
// Originally authored RED (QA Mode A, before evaluators_math.go existed);
// the implementation has since landed (evaluators_math.go) and this whole
// suite is GREEN. QA Mode B (below the original Coverage list) added
// adversarial/edge/negative coverage: an exact-decimal-vs-float64 proof,
// the tolerance boundary, the config-fault/data-fault operand split,
// number-literal operands, the full cross_field op table, absent-path/
// unknown-op cases, and the conditional if-uses-non-eq-op /
// then-comparison-passes / unknown-op / required:false predicate-variant
// cases -- fixtures only, no real Nigerian rule content (that's M3-05; see
// story Out of Scope).
//
// Coverage (original story Test Specs table rows):
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
// package) recovers any "not implemented" STUB panic into a t.Fatalf
// naming the concrete evaluator type -- now a simple pass-through here
// since all three Eval methods are implemented, but still load-bearing
// shared infra for any evaluator elsewhere in the package that isn't yet.
package validation

import (
	"encoding/json"
	"math"
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

// --- QA Mode B: adversarial / edge / negative coverage ---------------------
//
// Everything below was added post-implementation to pin the executor's
// documented contract decisions (evaluators_math.go doc comments) and to
// exercise cases the Mode A Test Specs above did not reach.

// --- tax_math: exact decimal math -------------------------------------

// TestTaxMath_ExactDecimalNoFloatError proves the file-header's "exact
// decimal math -- no float error" claim (AC #1) with a case that actually
// discriminates: base=123456789123.45, rate=0.075, expected=9259259184.25875
// (the nearest float64 to the EXACT decimal product) multiply to a mismatch
// of exactly 0 in decimal arithmetic, but in raw float64 arithmetic
// base*rate is off by ~1.9e-6 -- a ~1900x margin over the 1e-9 tolerance
// used here (deliberately wide, not a razor-thin boundary, so the proof is
// not sensitive to fused-multiply-add rounding variance across
// architectures/compilers). A naive float64 implementation would therefore
// wrongly report a violation here; the decimal implementation must not.
func TestTaxMath_ExactDecimalNoFloatError(t *testing.T) {
	e := taxMathEval{}
	// var, not const: Go untyped constant arithmetic is arbitrary-precision
	// and would NOT reproduce the float64 rounding error below -- these must
	// be actual float64-typed runtime values for the sanity check to mean
	// anything.
	var (
		base      float64 = 123456789123.45
		rate      float64 = 0.075
		expected  float64 = 9259259184.25875
		tolerance float64 = 0.000000001 // 1e-9
	)

	// Sanity check: confirm plain float64 arithmetic really does misjudge
	// this case, so the test is actually exercising the decimal-vs-float
	// distinction rather than a case where both approaches happen to agree.
	floatMismatch := math.Abs(expected - base*rate)
	if floatMismatch <= tolerance {
		t.Fatalf("test setup invalid: naive float64 mismatch %.20f does not exceed tolerance %.20f -- this case does not expose float error, pick different values", floatMismatch, tolerance)
	}

	payload := Payload{"invoice": map[string]any{
		"subtotal": float64(base),
		"vat":      float64(expected),
	}}
	r := Rule{
		Key:      "TAXMATH-EXACT-DECIMAL",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.000000001}`),
		Severity: "error",
		Message:  "should not fire -- exact decimal math must not see the float64 rounding error",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: decimal math gives mismatch exactly 0, but naive float64 arithmetic sees %.20f > tolerance %.20f and would wrongly violate -- this evaluator must use exact decimal math (shopspring/decimal), not float64", v, floatMismatch, tolerance)
	}
}

// TestTaxMath_BoundaryEqualsTolerancePasses pins the executor's documented
// boundary decision: |expected - base*rate| == tolerance passes (the impl
// uses `.GreaterThan(tolerance)`, not `>=`). 1000*0.075 = 75 exactly;
// expected 75.01 makes the mismatch exactly 0.01, equal to tolerance 0.01.
func TestTaxMath_BoundaryEqualsTolerancePasses(t *testing.T) {
	e := taxMathEval{}
	payload := Payload{"invoice": map[string]any{
		"subtotal": float64(1000),
		"vat":      75.01,
	}}
	r := Rule{
		Key:      "TAXMATH-BOUNDARY",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.01}`),
		Severity: "error",
		Message:  "should not fire at the tolerance boundary",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: mismatch 0.01 equals tolerance 0.01, which is not GREATER than it", v)
	}
}

// --- tax_math: config-fault vs data-fault operand split ----------------

// TestTaxMath_AbsentOperandPathViolates: base's param KEY is present
// ("missing"), but the payload path it names does not resolve -- a DATA
// fault (violation), not a config fault. Pins decision #1's split.
func TestTaxMath_AbsentOperandPathViolates(t *testing.T) {
	e := taxMathEval{}
	payload := Payload{"invoice": map[string]any{
		"vat": float64(75),
	}} // "missing" absent
	r := Rule{
		Key:      "TAXMATH-ABSENT-BASE",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":"missing","rate":0.075,"expected":"vat","tolerance":0.01}`),
		Severity: "error",
		Message:  "base path does not resolve",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() error = %v, want nil: an absent operand PATH is a data fault (violation), not a config fault", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: base path \"missing\" does not resolve in the payload")
	}
	if v.RuleKey != r.Key || v.Severity != r.Severity || v.Message != r.Message {
		t.Errorf("Eval() violation = %+v, want RuleKey=%q Severity=%q Message=%q", v, r.Key, r.Severity, r.Message)
	}
}

// TestTaxMath_RateNonNumberErrors: rate's param KEY decodes to the wrong
// JSON type -- a CONFIG fault (error), never a violation. Contrast with
// TestTaxMath_AbsentOperandPathViolates above: same "operand looks broken"
// symptom, opposite classification, because rate is always a literal
// number (never a path) per the file-header contract. Pins decision #1's
// split from the rate side.
func TestTaxMath_RateNonNumberErrors(t *testing.T) {
	e := taxMathEval{}
	payload := Payload{"invoice": map[string]any{
		"subtotal": float64(1000),
		"vat":      float64(75),
	}}
	r := Rule{
		Key:      "TAXMATH-BAD-RATE",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":"subtotal","rate":"x","expected":"vat"}`),
		Severity: "error",
		Message:  "should never surface -- config fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Fatal("Eval() error = nil, want non-nil: rate \"x\" is not a number")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: a config-fault error and a violation are mutually exclusive", v)
	}
}

// TestTaxMath_NumberLiteralOperands: base and expected are JSON NUMBERS
// (literals), not payload paths -- the file-header contract documents this
// as supported but no Mode A test exercised it. An empty invoice payload
// proves resolution did not fall through to a path lookup.
func TestTaxMath_NumberLiteralOperands(t *testing.T) {
	e := taxMathEval{}
	payload := Payload{"invoice": map[string]any{}}
	r := Rule{
		Key:      "TAXMATH-LITERALS",
		Type:     TypeTaxMath,
		Params:   json.RawMessage(`{"base":1000,"rate":0.075,"expected":75,"tolerance":0.01}`),
		Severity: "error",
		Message:  "should not fire: 1000*0.075 == 75 exactly",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: literal base=1000 * rate=0.075 == literal expected=75", v)
	}

	// A mismatched literal expected must still violate -- proves the
	// literal path is a real comparison, not an accidental always-pass.
	r.Params = json.RawMessage(`{"base":1000,"rate":0.075,"expected":999,"tolerance":0.01}`)
	v, err = mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: literal expected=999 does not match base*rate=75")
	}
}

// --- cross_field: full op table, absent path, unknown op ----------------

// TestCrossField_AllOps tables every cross_field op (eq/ne/lt/le/gt/ge)
// with both a passing and a failing case (AC #2's "flags a failed
// relation" plus its converse, "passes a held relation").
func TestCrossField_AllOps(t *testing.T) {
	tests := []struct {
		name        string
		op          string
		left, right float64
		wantViolate bool
	}{
		{"eq passes", "eq", 100, 100, false},
		{"eq fails", "eq", 100, 90, true},
		{"ne passes", "ne", 100, 90, false},
		{"ne fails", "ne", 100, 100, true},
		{"lt passes", "lt", 5, 10, false},
		{"lt fails (equal)", "lt", 10, 10, true},
		{"lt fails (greater)", "lt", 10, 5, true},
		{"le passes (less)", "le", 5, 10, false},
		{"le passes (equal)", "le", 10, 10, false},
		{"le fails", "le", 11, 10, true},
		{"gt passes", "gt", 10, 5, false},
		{"gt fails (equal)", "gt", 10, 10, true},
		{"gt fails (less)", "gt", 5, 10, true},
		{"ge passes (greater)", "ge", 10, 5, false},
		{"ge passes (equal)", "ge", 10, 10, false},
		{"ge fails", "ge", 5, 10, true},
	}
	e := crossFieldEval{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := Payload{"invoice": map[string]any{
				"left":  tt.left,
				"right": tt.right,
			}}
			r := Rule{
				Key:      "CROSSFIELD-OP",
				Type:     TypeCrossField,
				Params:   json.RawMessage(`{"left":"left","op":"` + tt.op + `","right":"right"}`),
				Severity: "error",
				Message:  "relation failed",
			}
			v, err := mustEval(t, e, payload, r)
			if err != nil {
				t.Fatalf("Eval() unexpected error: %v", err)
			}
			if gotViolate := v != nil; gotViolate != tt.wantViolate {
				t.Errorf("op %q left=%v right=%v: violation=%v, want violate=%v", tt.op, tt.left, tt.right, gotViolate, tt.wantViolate)
			}
		})
	}
}

// TestCrossField_AbsentPathViolates: right's path is absent from the
// payload -- resolvePath's nil is treated as "the relation does not hold",
// so this must be a violation (pins decision #3's absent-path handling),
// never a silent pass and never an error (left/right absence is data, not
// config -- unlike op).
func TestCrossField_AbsentPathViolates(t *testing.T) {
	e := crossFieldEval{}
	payload := Payload{"invoice": map[string]any{
		"total": float64(100),
	}} // "missing" absent
	r := Rule{
		Key:      "CROSSFIELD-ABSENT-RIGHT",
		Type:     TypeCrossField,
		Params:   json.RawMessage(`{"left":"total","op":"eq","right":"missing"}`),
		Severity: "error",
		Message:  "right path does not resolve",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: right path \"missing\" is absent, so the eq relation cannot hold")
	}
}

// TestCrossField_UnknownOpErrors: an unrecognized op is a config fault =>
// error, never a violation.
func TestCrossField_UnknownOpErrors(t *testing.T) {
	e := crossFieldEval{}
	payload := Payload{"invoice": map[string]any{
		"total":     float64(100),
		"lines_sum": float64(100),
	}}
	r := Rule{
		Key:      "CROSSFIELD-UNKNOWN-OP",
		Type:     TypeCrossField,
		Params:   json.RawMessage(`{"left":"total","op":"xx","right":"lines_sum"}`),
		Severity: "error",
		Message:  "should never surface -- config fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Fatal("Eval() error = nil, want non-nil: op \"xx\" is not a recognized cross_field operator")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: a config-fault error and a violation are mutually exclusive", v)
	}
}

// --- conditional: non-eq if op, then-comparison-passes, unknown op, ------
// --- required:false predicate variant -------------------------------------

// TestConditional_IfComparisonOps pins that `if` is not hardcoded to "eq"
// -- a "gt" if-op correctly drives whether `then` is evaluated at all, in
// both directions (if true evaluates then; if false skips it, even though
// then would fail if it were checked).
func TestConditional_IfComparisonOps(t *testing.T) {
	e := conditionalEval{}
	r := Rule{
		Key:      "CONDITIONAL-GT-DRIVES-THEN",
		Type:     TypeConditional,
		Params:   json.RawMessage(`{"if":{"field":"amount","op":"gt","value":1000},"then":{"field":"flag","required":true}}`),
		Severity: "error",
		Message:  "amounts over 1000 require flag",
	}

	tests := []struct {
		name        string
		amount      float64
		flagSet     bool
		wantViolate bool
	}{
		{"if true (gt holds), then fails (flag absent)", 1500, false, true},
		{"if true (gt holds), then passes (flag present)", 1500, true, false},
		{"if false (gt does not hold), then never evaluated though it would fail", 500, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invoice := map[string]any{"amount": tt.amount}
			if tt.flagSet {
				invoice["flag"] = "yes"
			}
			v, err := mustEval(t, e, Payload{"invoice": invoice}, r)
			if err != nil {
				t.Fatalf("Eval() unexpected error: %v", err)
			}
			if gotViolate := v != nil; gotViolate != tt.wantViolate {
				t.Errorf("amount=%v flagSet=%v: violation=%v, want violate=%v", tt.amount, tt.flagSet, gotViolate, tt.wantViolate)
			}
		})
	}
}

// TestConditional_ThenComparisonPasses: the `then` comparison-predicate
// variant (not `required`) when it HOLDS -- complements
// TestConditional_IfTrueThenComparisonFails (Mode A), which only covered
// the failing direction.
func TestConditional_ThenComparisonPasses(t *testing.T) {
	e := conditionalEval{}
	payload := Payload{"invoice": map[string]any{
		"country": "NG",
		"amount":  float64(500),
	}}
	r := Rule{
		Key:      "CONDITIONAL-THEN-COMPARISON-PASSES",
		Type:     TypeConditional,
		Params:   json.RawMessage(`{"if":{"field":"country","op":"eq","value":"NG"},"then":{"field":"amount","op":"le","value":1000}}`),
		Severity: "warning",
		Message:  "should not fire",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: if holds (country==NG) and then holds (amount 500 <= 1000)", v)
	}
}

// TestConditional_UnknownOpErrors: an unrecognized op in EITHER clause is a
// config fault => error. The "unknown op in then" case sets `if` true so
// `then` is actually reached (an unknown op that's skipped by a false `if`
// would prove nothing).
func TestConditional_UnknownOpErrors(t *testing.T) {
	e := conditionalEval{}
	payload := Payload{"invoice": map[string]any{
		"country": "NG",
		"amount":  float64(500),
	}}
	tests := []struct {
		name   string
		params json.RawMessage
	}{
		{
			"unknown op in if",
			json.RawMessage(`{"if":{"field":"country","op":"xx","value":"NG"},"then":{"field":"amount","op":"le","value":1000}}`),
		},
		{
			"unknown op in then (if true, so then is reached)",
			json.RawMessage(`{"if":{"field":"country","op":"eq","value":"NG"},"then":{"field":"amount","op":"xx","value":1000}}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Rule{
				Key:      "CONDITIONAL-UNKNOWN-OP",
				Type:     TypeConditional,
				Params:   tt.params,
				Severity: "error",
				Message:  "should never surface -- config fault",
			}
			v, err := mustEval(t, e, payload, r)
			if err == nil {
				t.Fatal("Eval() error = nil, want non-nil for an unknown comparison op")
			}
			if v != nil {
				t.Errorf("Eval() violation = %+v, want nil: a config-fault error and a violation are mutually exclusive", v)
			}
		})
	}
}

// TestConditional_RequiredFalseTriviallySatisfied pins decision #4's full
// claim: the `then` variant is chosen by the mere PRESENCE of the
// `required` key, not by its truthiness -- required:false must still
// select the presence variant and be trivially satisfied, regardless of
// whether the field is actually present.
func TestConditional_RequiredFalseTriviallySatisfied(t *testing.T) {
	e := conditionalEval{}
	payload := Payload{"invoice": map[string]any{
		"country": "NG",
		// "optional_field" intentionally absent
	}}
	r := Rule{
		Key:      "CONDITIONAL-REQUIRED-FALSE",
		Type:     TypeConditional,
		Params:   json.RawMessage(`{"if":{"field":"country","op":"eq","value":"NG"},"then":{"field":"optional_field","required":false}}`),
		Severity: "info",
		Message:  "should not fire",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: then's required:false is trivially satisfied even though optional_field is absent", v)
	}
}
