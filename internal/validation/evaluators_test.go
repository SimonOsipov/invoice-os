// M3-04-03 (Test-first: yes) -- evaluator tests for the five
// presence/shape rule types (required/format/regex/enum/range/date),
// authored BEFORE the evaluators exist (RALPH Phase 3.5 / QA Mode A). Every
// evaluator's Eval currently panics("validation: not implemented") (see
// evaluators.go's STUB NOTICE), so this whole suite is RED until M3-04-03's
// implementation lands -- fixtures only, no real Nigerian rule content
// (that's M3-05; see story Out of Scope).
//
// Coverage (see M3-04-03 Test Specs, plus edge cases the QA task brief
// asked to be pulled forward into this RED pass rather than deferred to
// Mode B):
//  1. TestRequired_MissingViolates / PresentPasses / BlankViolates / AllowBlankPasses
//  2. TestFormat_NoMatchViolates / MatchPasses
//  3. TestEnum_OutOfSetViolates / InSetPasses
//  4. TestRange_BelowMinViolates / AboveMaxViolates / WithinPasses / NonNumericViolates
//  5. TestDate_FutureViolates / WithinPasses
//  6. TestEvaluator_BadParamsErrors        -- format/range/enum, undecodable params => error, not violation.
//  7. TestEvaluator_AbsentTargetNonRequiredPasses -- format/enum/range/date, absent target => nil.
//
// Payloads are built as Payload{"invoice": map[string]any{...}} and
// Rule.Target is set WITHOUT the "invoice." prefix (resolvePath, engine.go,
// Decision N19). Go's testing package does not isolate a panic to the
// single test that raised it, so mustEval below recovers the current STUB
// panic into an ordinary t.Fatalf (mirrors engine_test.go's mustEvaluate/
// mustResolvePath) -- each test fails independently and legibly during
// this RED phase; once the executor implements each Eval for real, mustEval
// is a simple pass-through.
package validation

import (
	"encoding/json"
	"testing"
	"time"
)

// mustEval calls e.Eval(p, r) and recovers Eval's current "not implemented"
// STUB panic into a t.Fatalf naming the concrete evaluator type, so a
// pre-implementation run fails each test independently instead of crashing
// the whole binary.
func mustEval(t *testing.T, e Evaluator, p Payload, r Rule) (*Violation, error) {
	t.Helper()
	var (
		v   *Violation
		err error
	)
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("%T.Eval panicked (pre-implementation STUB): %v", e, rec)
			}
		}()
		v, err = e.Eval(p, r)
	}()
	return v, err
}

// --- required -----------------------------------------------------------

// TestRequired_MissingViolates (Test Spec): target absent => a *Violation
// carrying the rule's key + severity + message + resolved path.
func TestRequired_MissingViolates(t *testing.T) {
	e := requiredEval{}
	payload := Payload{"invoice": map[string]any{}}
	r := Rule{
		Key:      "REQUIRED-TIN",
		Type:     TypeRequired,
		Target:   "supplier.tin",
		Params:   json.RawMessage(`{}`),
		Severity: "error",
		Message:  "supplier TIN is required",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil for an absent required target")
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

// TestRequired_PresentPasses (Test Spec): target present, non-blank => nil.
func TestRequired_PresentPasses(t *testing.T) {
	e := requiredEval{}
	payload := Payload{"invoice": map[string]any{
		"supplier": map[string]any{"tin": "01234567-0001"},
	}}
	r := Rule{
		Key:      "REQUIRED-TIN",
		Type:     TypeRequired,
		Target:   "supplier.tin",
		Params:   json.RawMessage(`{}`),
		Severity: "error",
		Message:  "supplier TIN is required",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil for a present, non-blank target", v)
	}
}

// TestRequired_BlankViolates: an empty-string target violates by default
// (blank is not "present" for required's purposes, absent an explicit
// allow_blank opt-out -- see TestRequired_AllowBlankPasses).
func TestRequired_BlankViolates(t *testing.T) {
	e := requiredEval{}
	payload := Payload{"invoice": map[string]any{
		"supplier": map[string]any{"tin": ""},
	}}
	r := Rule{
		Key:      "REQUIRED-TIN",
		Type:     TypeRequired,
		Target:   "supplier.tin",
		Params:   json.RawMessage(`{}`),
		Severity: "error",
		Message:  "supplier TIN is required",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: an empty string is blank, and default required semantics reject blank")
	}
}

// TestRequired_AllowBlankPasses: the same blank target passes when the
// rule opts in via params {"allow_blank":true}.
func TestRequired_AllowBlankPasses(t *testing.T) {
	e := requiredEval{}
	payload := Payload{"invoice": map[string]any{
		"supplier": map[string]any{"tin": ""},
	}}
	r := Rule{
		Key:      "REQUIRED-TIN",
		Type:     TypeRequired,
		Target:   "supplier.tin",
		Params:   json.RawMessage(`{"allow_blank":true}`),
		Severity: "error",
		Message:  "supplier TIN is required",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: allow_blank=true permits an empty-string target", v)
	}
}

// --- format/regex ---------------------------------------------------------

// TestFormat_NoMatchViolates (Test Spec): pattern ^\d{10}$, value "abc" =>
// violation.
func TestFormat_NoMatchViolates(t *testing.T) {
	e := formatEval{}
	payload := Payload{"invoice": map[string]any{
		"supplier": map[string]any{"tin": "abc"},
	}}
	r := Rule{
		Key:      "FORMAT-TIN",
		Type:     TypeFormat,
		Target:   "supplier.tin",
		Params:   json.RawMessage(`{"pattern":"^\\d{10}$"}`),
		Severity: "error",
		Message:  "TIN must be exactly 10 digits",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal(`Eval() violation = nil, want non-nil: "abc" does not match ^\d{10}$`)
	}
	if v.Path != r.Target {
		t.Errorf("Path = %q, want %q", v.Path, r.Target)
	}
}

// TestFormat_MatchPasses (Test Spec): same pattern, value "0123456789" =>
// nil.
func TestFormat_MatchPasses(t *testing.T) {
	e := formatEval{}
	payload := Payload{"invoice": map[string]any{
		"supplier": map[string]any{"tin": "0123456789"},
	}}
	r := Rule{
		Key:      "FORMAT-TIN",
		Type:     TypeFormat,
		Target:   "supplier.tin",
		Params:   json.RawMessage(`{"pattern":"^\\d{10}$"}`),
		Severity: "error",
		Message:  "TIN must be exactly 10 digits",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf(`Eval() violation = %+v, want nil: "0123456789" matches ^\d{10}$`, v)
	}
}

// --- enum -------------------------------------------------------------

// TestEnum_OutOfSetViolates (Test Spec): values [NGN,USD], value "EUR" =>
// violation.
func TestEnum_OutOfSetViolates(t *testing.T) {
	e := enumEval{}
	payload := Payload{"invoice": map[string]any{"currency": "EUR"}}
	r := Rule{
		Key:      "ENUM-CURRENCY",
		Type:     TypeEnum,
		Target:   "currency",
		Params:   json.RawMessage(`{"values":["NGN","USD"]}`),
		Severity: "error",
		Message:  "currency must be NGN or USD",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal(`Eval() violation = nil, want non-nil: "EUR" is not in [NGN,USD]`)
	}
}

// TestEnum_InSetPasses: value "NGN" (a member of the enum) => nil.
func TestEnum_InSetPasses(t *testing.T) {
	e := enumEval{}
	payload := Payload{"invoice": map[string]any{"currency": "NGN"}}
	r := Rule{
		Key:      "ENUM-CURRENCY",
		Type:     TypeEnum,
		Target:   "currency",
		Params:   json.RawMessage(`{"values":["NGN","USD"]}`),
		Severity: "error",
		Message:  "currency must be NGN or USD",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf(`Eval() violation = %+v, want nil: "NGN" is in [NGN,USD]`, v)
	}
}

// --- range --------------------------------------------------------------

// TestRange_BelowMinViolates (Test Spec): {min:1,max:5}, value 0 =>
// violation.
func TestRange_BelowMinViolates(t *testing.T) {
	e := rangeEval{}
	payload := Payload{"invoice": map[string]any{"quantity": float64(0)}}
	r := Rule{
		Key:      "RANGE-QTY",
		Type:     TypeRange,
		Target:   "quantity",
		Params:   json.RawMessage(`{"min":1,"max":5}`),
		Severity: "error",
		Message:  "quantity out of range",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: 0 is below min 1")
	}
}

// TestRange_AboveMaxViolates: value 6 (above max 5) => violation.
func TestRange_AboveMaxViolates(t *testing.T) {
	e := rangeEval{}
	payload := Payload{"invoice": map[string]any{"quantity": float64(6)}}
	r := Rule{
		Key:      "RANGE-QTY",
		Type:     TypeRange,
		Target:   "quantity",
		Params:   json.RawMessage(`{"min":1,"max":5}`),
		Severity: "error",
		Message:  "quantity out of range",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: 6 is above max 5")
	}
}

// TestRange_WithinPasses (Test Spec): {min:1,max:5}, value 3 => nil.
func TestRange_WithinPasses(t *testing.T) {
	e := rangeEval{}
	payload := Payload{"invoice": map[string]any{"quantity": float64(3)}}
	r := Rule{
		Key:      "RANGE-QTY",
		Type:     TypeRange,
		Target:   "quantity",
		Params:   json.RawMessage(`{"min":1,"max":5}`),
		Severity: "error",
		Message:  "quantity out of range",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: 3 is within [1,5]", v)
	}
}

// TestRange_NonNumericViolates: a present but non-numeric value is a
// VIOLATION per the story's evaluator table ("outside bounds / not
// numeric"), NOT a config-fault error -- the rule/params are fine, the
// DATA is bad. Contrast with TestEvaluator_BadParamsErrors below, where the
// PARAMS themselves are undecodable.
func TestRange_NonNumericViolates(t *testing.T) {
	e := rangeEval{}
	payload := Payload{"invoice": map[string]any{"quantity": "x"}}
	r := Rule{
		Key:      "RANGE-QTY",
		Type:     TypeRange,
		Target:   "quantity",
		Params:   json.RawMessage(`{"min":1,"max":5}`),
		Severity: "error",
		Message:  "quantity out of range",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v (a non-numeric present VALUE must be a violation, not a config error)", err)
	}
	if v == nil {
		t.Fatal(`Eval() violation = nil, want non-nil: "x" is not numeric`)
	}
}

// --- date -----------------------------------------------------------------

// TestDate_FutureViolates (Test Spec): {not_after:"today"}, value tomorrow
// => violation. Computed relative to time.Now() so the test is robust to
// the clock it runs at; date-only granularity (YYYY-MM-DD) avoids
// time-of-day flakiness.
func TestDate_FutureViolates(t *testing.T) {
	e := dateEval{}
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	payload := Payload{"invoice": map[string]any{"issue_date": tomorrow}}
	r := Rule{
		Key:      "DATE-ISSUE",
		Type:     TypeDate,
		Target:   "issue_date",
		Params:   json.RawMessage(`{"not_after":"today"}`),
		Severity: "error",
		Message:  "issue date cannot be in the future",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: tomorrow is after not_after=today")
	}
}

// TestDate_WithinPasses: value yesterday (on/before not_after=today) =>
// nil.
func TestDate_WithinPasses(t *testing.T) {
	e := dateEval{}
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	payload := Payload{"invoice": map[string]any{"issue_date": yesterday}}
	r := Rule{
		Key:      "DATE-ISSUE",
		Type:     TypeDate,
		Target:   "issue_date",
		Params:   json.RawMessage(`{"not_after":"today"}`),
		Severity: "error",
		Message:  "issue date cannot be in the future",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: yesterday is not after not_after=today", v)
	}
}

// --- cross-cutting: bad params / absent target -----------------------------

// TestEvaluator_BadParamsErrors (Test Spec): undecodable params => a
// non-nil error (an engine/config fault, Decision N15), never a violation.
// Covers format (wrong JSON type for pattern), range (wrong JSON type for
// min), and enum (wrong JSON type for values) -- one decode-error path per
// evaluator, per the task brief's "cover at least format + range + enum".
func TestEvaluator_BadParamsErrors(t *testing.T) {
	// Target resolves to a present value for every case, so a broken
	// evaluator that ignored bad params and fell through to a "present,
	// passes" path would be caught by err==nil here rather than masked by
	// an absent-target no-op.
	payload := Payload{"invoice": map[string]any{
		"supplier": map[string]any{"tin": "0123456789"},
		"currency": "NGN",
		"quantity": float64(3),
	}}

	tests := []struct {
		name   string
		eval   Evaluator
		typ    RuleType
		target string
		params json.RawMessage
	}{
		{"format: pattern wrong JSON type", formatEval{}, TypeFormat, "supplier.tin", json.RawMessage(`{"pattern":123}`)},
		{"format: malformed JSON", formatEval{}, TypeFormat, "supplier.tin", json.RawMessage(`{"pattern":`)},
		{"range: min wrong JSON type", rangeEval{}, TypeRange, "quantity", json.RawMessage(`{"min":"not-a-number"}`)},
		{"enum: values wrong JSON type", enumEval{}, TypeEnum, "currency", json.RawMessage(`{"values":"NGN"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Rule{
				Key:      "BAD-PARAMS",
				Type:     tt.typ,
				Target:   tt.target,
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

// TestEvaluator_AbsentTargetNonRequiredPasses (Test Spec, edge case): for
// every type except required, an absent target is a pass -- presence
// checking is required's job alone, per the story's evaluator table
// ("Absent target + non-required type => pass").
func TestEvaluator_AbsentTargetNonRequiredPasses(t *testing.T) {
	payload := Payload{"invoice": map[string]any{}} // "missing.field" never present

	tests := []struct {
		name   string
		eval   Evaluator
		typ    RuleType
		params json.RawMessage
	}{
		{"format", formatEval{}, TypeFormat, json.RawMessage(`{"pattern":"^\\d{10}$"}`)},
		{"enum", enumEval{}, TypeEnum, json.RawMessage(`{"values":["NGN","USD"]}`)},
		{"range", rangeEval{}, TypeRange, json.RawMessage(`{"min":1,"max":5}`)},
		{"date", dateEval{}, TypeDate, json.RawMessage(`{"not_after":"today"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Rule{
				Key:      "ABSENT-TARGET",
				Type:     tt.typ,
				Target:   "missing.field",
				Params:   tt.params,
				Severity: "error",
				Message:  "should never fire on an absent target",
			}

			v, err := mustEval(t, tt.eval, payload, r)
			if err != nil {
				t.Fatalf("Eval() unexpected error: %v", err)
			}
			if v != nil {
				t.Errorf("Eval() violation = %+v, want nil: presence is required's job, not %s's", v, tt.typ)
			}
		})
	}
}
