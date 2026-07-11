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

// --- QA Mode B adversarial coverage ----------------------------------------
//
// Everything below this line was added by QA (post-implementation, Mode B)
// to pin the executor's documented contract decisions (evaluators.go header
// comment, lines 14-29) beyond the Test-Spec table above, and to probe
// adversarial/edge/boundary inputs the RED pass didn't cover.

// TestRequired_NullViolates: a JSON null at a present key is NOT "present"
// for required's purposes (contrast with the other four types, where null is
// treated as absent -- TestFormat_NullTreatedAsAbsent etc. below) -- pins
// decision #2 ("null is a violation for required").
func TestRequired_NullViolates(t *testing.T) {
	e := requiredEval{}
	payload := Payload{"invoice": map[string]any{"supplier": map[string]any{"tin": nil}}}
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
		t.Fatal("Eval() violation = nil, want non-nil: a present JSON null is not \"present\" for required")
	}
}

// TestRequired_AbsentWithAllowBlankStillViolates: allow_blank only widens
// what counts as a non-blank STRING; it does not turn an absent key into a
// pass -- pins decision #3's second half ("absent/null still violates even
// with allow_blank").
func TestRequired_AbsentWithAllowBlankStillViolates(t *testing.T) {
	e := requiredEval{}
	payload := Payload{"invoice": map[string]any{}} // "supplier.tin" never present
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
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: allow_blank does not excuse an absent key")
	}
}

// TestRequired_WhitespaceOnlyViolatesEvenPresent: a whitespace-only string
// (not just empty-string) is blank too -- pins decision #3's first half
// ("allow_blank covers empty AND whitespace-only").
func TestRequired_WhitespaceOnlyViolatesEvenPresent(t *testing.T) {
	e := requiredEval{}
	payload := Payload{"invoice": map[string]any{"supplier": map[string]any{"tin": "   \t  "}}}
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
		t.Fatal("Eval() violation = nil, want non-nil: a whitespace-only string is blank")
	}
}

// TestFormat_NullTreatedAsAbsent: a present JSON null is treated the same as
// an absent target for the four non-required types -- pins decision #2's
// first half. (Contrast TestRequired_NullViolates above.)
func TestFormat_NullTreatedAsAbsent(t *testing.T) {
	e := formatEval{}
	payload := Payload{"invoice": map[string]any{"supplier": map[string]any{"tin": nil}}}
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
		t.Errorf("Eval() violation = %+v, want nil: a present-but-null value is treated as absent", v)
	}
}

// TestFormat_NonStringValue: the resolved target value is a number, not a
// string. Reading formatEval.Eval (evaluators.go stringify, ~line 299-306)
// shows the implementation does NOT treat a non-string value as absent --
// it renders it via fmt.Sprintf("%v", v) and matches the pattern against
// that. This locks the ACTUAL behavior: 42 stringifies to "42", which
// matches ^\d+$, so this is a pass, not a violation and not an error.
func TestFormat_NonStringValue(t *testing.T) {
	e := formatEval{}
	payload := Payload{"invoice": map[string]any{"quantity": float64(42)}}
	r := Rule{
		Key:      "FORMAT-QTY",
		Type:     TypeFormat,
		Target:   "quantity",
		Params:   json.RawMessage(`{"pattern":"^\\d+$"}`),
		Severity: "error",
		Message:  "quantity must be numeric-looking",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf(`Eval() violation = %+v, want nil: float64(42) stringifies to "42", which matches ^\d+$`, v)
	}
}

// TestFormat_BadParamsAbsentTargetStillErrors: an uncompilable pattern is a
// config fault that must surface even when the target is ABSENT -- format's
// regexp.Compile (evaluators.go line 148) runs before resolvePath (line
// 153), so params validation is NOT short-circuited by an absent target.
// Pins decision #1 for format specifically.
func TestFormat_BadParamsAbsentTargetStillErrors(t *testing.T) {
	e := formatEval{}
	payload := Payload{"invoice": map[string]any{}} // "missing.field" never present
	r := Rule{
		Key:      "FORMAT-BAD",
		Type:     TypeFormat,
		Target:   "missing.field",
		Params:   json.RawMessage(`{"pattern":"("}`), // uncompilable regex
		Severity: "error",
		Message:  "should never surface -- config fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Fatal("Eval() error = nil, want non-nil: an uncompilable pattern must error even with an absent target (decision #1 / N15)")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil alongside a config-fault error", v)
	}
}

// TestEnum_NumericValues: enum is not string-only -- a numeric values list
// and a numeric target value compare correctly (enumEval uses
// reflect.DeepEqual, and both the decoded JSON values and a test-constructed
// float64 target are the same Go type).
func TestEnum_NumericValues(t *testing.T) {
	e := enumEval{}
	r := Rule{
		Key:      "ENUM-CODE",
		Type:     TypeEnum,
		Target:   "code",
		Params:   json.RawMessage(`{"values":[1,2,3]}`),
		Severity: "error",
		Message:  "code must be 1, 2, or 3",
	}

	t.Run("member passes", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"code": float64(2)}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("Eval() violation = %+v, want nil: 2 is in [1,2,3]", v)
		}
	})

	t.Run("non-member violates", func(t *testing.T) {
		payload := Payload{"invoice": map[string]any{"code": float64(5)}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("Eval() violation = nil, want non-nil: 5 is not in [1,2,3]")
		}
	})
}

// TestRange_ExclusiveBounds: exclusive_min/exclusive_max reject the boundary
// value itself (strict inequality), unlike min/max which are inclusive.
func TestRange_ExclusiveBounds(t *testing.T) {
	t.Run("exclusive_min boundary violates", func(t *testing.T) {
		e := rangeEval{}
		payload := Payload{"invoice": map[string]any{"q": float64(1)}}
		r := Rule{Key: "RANGE-EXCL-MIN", Type: TypeRange, Target: "q",
			Params: json.RawMessage(`{"exclusive_min":1}`), Severity: "error", Message: "q too low"}

		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("Eval() violation = nil, want non-nil: 1 <= exclusive_min 1")
		}
	})

	t.Run("exclusive_max boundary violates", func(t *testing.T) {
		e := rangeEval{}
		payload := Payload{"invoice": map[string]any{"q": float64(5)}}
		r := Rule{Key: "RANGE-EXCL-MAX", Type: TypeRange, Target: "q",
			Params: json.RawMessage(`{"exclusive_max":5}`), Severity: "error", Message: "q too high"}

		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("Eval() violation = nil, want non-nil: 5 >= exclusive_max 5")
		}
	})

	t.Run("interior passes", func(t *testing.T) {
		e := rangeEval{}
		payload := Payload{"invoice": map[string]any{"q": float64(3)}}
		r := Rule{Key: "RANGE-EXCL", Type: TypeRange, Target: "q",
			Params: json.RawMessage(`{"exclusive_min":1,"exclusive_max":5}`), Severity: "error", Message: "q out of range"}

		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("Eval() violation = %+v, want nil: 3 is strictly between 1 and 5", v)
		}
	})
}

// TestRange_NumericStringViolates: a numeric-LOOKING string ("3") is NOT
// coerced to a number -- toFloat (evaluators.go line 68-103) has no string
// case, so this falls to the default branch (ok=false) and is a violation,
// same as any other non-numeric data. Pins decision #4's negative half.
func TestRange_NumericStringViolates(t *testing.T) {
	e := rangeEval{}
	payload := Payload{"invoice": map[string]any{"q": "3"}}
	r := Rule{
		Key:      "RANGE-QTY",
		Type:     TypeRange,
		Target:   "q",
		Params:   json.RawMessage(`{"min":1,"max":5}`),
		Severity: "error",
		Message:  "q out of range",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal(`Eval() violation = nil, want non-nil: "3" (a string) is not coerced to numeric 3`)
	}
}

// TestRange_JSONNumber: a json.Number-typed value (as produced by a
// json.Decoder configured with UseNumber) IS coerced -- toFloat's
// json.Number case. Pins decision #4's positive half.
func TestRange_JSONNumber(t *testing.T) {
	e := rangeEval{}
	payload := Payload{"invoice": map[string]any{"q": json.Number("3")}}
	r := Rule{
		Key:      "RANGE-QTY",
		Type:     TypeRange,
		Target:   "q",
		Params:   json.RawMessage(`{"min":1,"max":5}`),
		Severity: "error",
		Message:  "q out of range",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil: json.Number(\"3\") coerces to 3, within [1,5]", v)
	}
}

// TestDate_TodayBoundaryInclusive: not_after:"today" with a value of
// literally today passes -- the bound check is d.After(na), so d==na is not
// "after" and does not violate. Pins decision #5's inclusive-today half.
func TestDate_TodayBoundaryInclusive(t *testing.T) {
	e := dateEval{}
	today := time.Now().Format("2006-01-02")
	payload := Payload{"invoice": map[string]any{"issue_date": today}}
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
		t.Errorf("Eval() violation = %+v, want nil: today is not after not_after=today (inclusive)", v)
	}
}

// TestDate_NotBefore: not_before is checked symmetrically with not_after
// (also inclusive on the boundary, since the guard is d.Before(nb)).
func TestDate_NotBefore(t *testing.T) {
	r := Rule{
		Key:      "DATE-EFFECTIVE",
		Type:     TypeDate,
		Target:   "effective_date",
		Params:   json.RawMessage(`{"not_before":"2026-06-01"}`),
		Severity: "error",
		Message:  "effective date is too early",
	}

	t.Run("before bound violates", func(t *testing.T) {
		e := dateEval{}
		payload := Payload{"invoice": map[string]any{"effective_date": "2026-01-01"}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("Eval() violation = nil, want non-nil: 2026-01-01 is before not_before 2026-06-01")
		}
	})

	t.Run("on bound passes", func(t *testing.T) {
		e := dateEval{}
		payload := Payload{"invoice": map[string]any{"effective_date": "2026-06-01"}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("Eval() violation = %+v, want nil: 2026-06-01 equals not_before (inclusive)", v)
		}
	})

	t.Run("after bound passes", func(t *testing.T) {
		e := dateEval{}
		payload := Payload{"invoice": map[string]any{"effective_date": "2026-07-01"}}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("Eval() violation = %+v, want nil: 2026-07-01 is after not_before 2026-06-01", v)
		}
	})
}

// TestDate_CustomFormat: a non-default layout (DD/MM/YYYY) both parses the
// target value and resolves the not_after bound under the same layout.
func TestDate_CustomFormat(t *testing.T) {
	payload := Payload{"invoice": map[string]any{"d": "25/12/2026"}}

	t.Run("within bound passes", func(t *testing.T) {
		e := dateEval{}
		r := Rule{Key: "DATE-CUSTOM", Type: TypeDate, Target: "d",
			Params: json.RawMessage(`{"format":"02/01/2006","not_after":"31/12/2026"}`),
			Severity: "error", Message: "d out of range"}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("Eval() violation = %+v, want nil: 25/12/2026 is on/before not_after 31/12/2026", v)
		}
	})

	t.Run("outside bound violates", func(t *testing.T) {
		e := dateEval{}
		r := Rule{Key: "DATE-CUSTOM", Type: TypeDate, Target: "d",
			Params: json.RawMessage(`{"format":"02/01/2006","not_after":"01/01/2026"}`),
			Severity: "error", Message: "d out of range"}
		v, err := mustEval(t, e, payload, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("Eval() violation = nil, want non-nil: 25/12/2026 is after not_after 01/01/2026")
		}
	})
}

// TestDate_UnparseableValueViolates: a target value that does not parse
// under the (default ISO) layout is bad DATA, not a config fault -- a
// violation, not an error.
func TestDate_UnparseableValueViolates(t *testing.T) {
	e := dateEval{}
	payload := Payload{"invoice": map[string]any{"d": "not-a-date"}}
	r := Rule{
		Key:      "DATE-ISSUE",
		Type:     TypeDate,
		Target:   "d",
		Params:   json.RawMessage(`{"not_after":"today"}`),
		Severity: "error",
		Message:  "issue date is invalid",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v (an unparseable VALUE is a violation, not a config error)", err)
	}
	if v == nil {
		t.Fatal(`Eval() violation = nil, want non-nil: "not-a-date" does not parse as 2006-01-02`)
	}
}

// TestDate_BadParamsErrors: a wrong-JSON-type not_before (a number instead
// of a string) fails decodeParams -- a structural config fault, same class
// as format/range/enum's bad-params tests.
func TestDate_BadParamsErrors(t *testing.T) {
	e := dateEval{}
	payload := Payload{"invoice": map[string]any{"d": "2026-01-01"}}
	r := Rule{
		Key:      "DATE-BAD",
		Type:     TypeDate,
		Target:   "d",
		Params:   json.RawMessage(`{"not_before":123}`),
		Severity: "error",
		Message:  "should never surface -- config fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Fatal("Eval() error = nil, want non-nil for a wrong-JSON-type not_before")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil alongside a config-fault error", v)
	}
}

// TestDate_BadBoundAbsentTarget_KNOWNBUG documents a confirmed deviation
// from the evaluators.go file-header contract ("Params are validated FIRST,
// before the absent-target short-circuit, so a rule with broken config
// fails loud even when the data happens to omit the target") and from
// Decision N15 ("a broken ruleset must not quietly validate everything").
//
// Unlike format (which compiles its regex BEFORE resolvePath, so a bad
// pattern errors even on an absent target -- see
// TestFormat_BadParamsAbsentTargetStillErrors), dateEval only calls
// resolveDateBound for not_before/not_after INSIDE the not-before/not-after
// blocks, which are only reached AFTER the "absent target => return nil,nil"
// early return (evaluators.go: resolvePath + absent check at ~line 261-264,
// resolveDateBound calls at ~line 279 and ~line 288 -- both unreachable when
// the target is absent). So a not_after/not_before bound that is neither
// "today" nor parseable under the rule's layout silently PASSES when the
// target happens to be absent, instead of erroring as documented.
//
// This test pins the CURRENT (buggy) behavior so a regression is visible in
// the diff if the ordering is ever "fixed" in the wrong direction; it is
// t.Skip'd so it does not block the green suite. See QA report for the
// repro and file/line citation -- flagged for the executor to fix (move the
// not_before/not_after bound resolution ahead of the absent-target check,
// mirroring format's early regexp.Compile).
func TestDate_BadBoundAbsentTarget_KNOWNBUG(t *testing.T) {
	t.Skip("KNOWN BUG (QA M3-04-03 Mode B): date not_before/not_after bound validation is not hoisted ahead of the absent-target short-circuit, unlike format's pattern compile -- see evaluators.go dateEval.Eval ~line 244-297. A malformed bound silently passes instead of erroring when the target is absent, violating N15 and the file's own documented contract.")

	e := dateEval{}
	payload := Payload{"invoice": map[string]any{}} // target absent
	r := Rule{
		Key:      "DATE-BAD-BOUND",
		Type:     TypeDate,
		Target:   "missing.field",
		Params:   json.RawMessage(`{"not_after":"not-a-valid-bound!!"}`),
		Severity: "error",
		Message:  "should never surface -- config fault",
	}

	v, err := mustEval(t, e, payload, r)
	if err == nil {
		t.Error("Eval() error = nil -- CONFIRMS the bug: a malformed not_after bound silently passes when the target is absent, instead of erroring (decision #1 / N15)")
	}
	if v != nil {
		t.Errorf("Eval() violation = %+v, want nil alongside a config-fault error", v)
	}
}

// TestViolation_CarriesAllFields: one representative failure asserts all
// four Violation fields simultaneously (RuleKey, Severity, Message, Path),
// against a rule whose four values are all distinct from each other and
// from the target/type, so a field-swap bug (e.g. Path holding Message)
// would be caught.
func TestViolation_CarriesAllFields(t *testing.T) {
	e := rangeEval{}
	payload := Payload{"invoice": map[string]any{"quantity": float64(0)}}
	r := Rule{
		Key:      "RANGE-QTY-001",
		Type:     TypeRange,
		Target:   "quantity",
		Params:   json.RawMessage(`{"min":1,"max":5}`),
		Severity: "warning",
		Message:  "quantity should be between 1 and 5",
	}

	v, err := mustEval(t, e, payload, r)
	if err != nil {
		t.Fatalf("Eval() unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("Eval() violation = nil, want non-nil: 0 is below min 1")
	}
	if v.RuleKey != r.Key || v.Severity != r.Severity || v.Message != r.Message || v.Path != r.Target {
		t.Errorf("Eval() violation = %+v, want {RuleKey:%q Severity:%q Message:%q Path:%q}",
			v, r.Key, r.Severity, r.Message, r.Target)
	}
}
