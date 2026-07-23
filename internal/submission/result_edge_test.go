// QA Mode B (task-217): adversarial / edge coverage added AFTER the Stage 2.5 specs in
// result_test.go went green, per the story's "add coverage the plan did not think of" charge.
// Nothing here duplicates result_test.go's five specs; each test below targets a case the
// plan's Test Specs table does not name.
//
// Package submission_test (external), matching every other test file in this package.
// TestMain already exists at failure_modes_test.go:57 -- one per test binary -- so this file
// defines none. No testify; standard library only (encoding/json, reflect, testing).
package submission_test

import (
	"encoding/json"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestKindOf_PointerVariantIsADistinctTypeFromTypeSwitchsPerspective documents a real, narrow
// gap between AC-1's plain-English "exactly Accepted, Rejected, Pending, Retryable implement
// [Result]" and Go's actual method-set rules: isResult() is declared with a VALUE receiver
// (func (Accepted) isResult() {}), and a pointer type's method set includes every method
// declared on the value type, so *Accepted (and *Rejected/*Pending/*Retryable) ALSO satisfy
// submission.Result -- confirmed by the line below compiling at all. Cross-package sealing
// still holds (isResult unexported blocks a genuinely independent type in another package;
// see TestResult_SealedVariants and this subtask's own probe against internal/submission from
// a sibling package, done out-of-band during QA verification), but WITHIN this fact, a
// Result holding a pointer is a distinct concrete type from the value type the KindOf type
// switch matches on. It therefore falls through to KindOf's default arm and returns "",
// identical to what a nil Result reports -- silently, with no panic and no error.
//
// This is not fixed here (QA does not modify production code); it is reported as a low-
// severity design-intent gap. No production code path is known to construct a pointer
// variant today -- Adapter.Submit/Poll return Result by value, never *Result or a pointer-
// typed variant -- so the risk is latent, not currently triggered.
func TestKindOf_PointerVariantIsADistinctTypeFromTypeSwitchsPerspective(t *testing.T) {
	// Compiles only because *Accepted's method set includes the promoted value-receiver
	// isResult() -- this line IS the compile-time confirmation.
	var r submission.Result = &submission.Accepted{IRN: "should-be-irrelevant"}

	// r is a non-nil interface value here: it holds a concrete, non-nil *Accepted, so
	// `r == nil` is provably always false -- not asserted below (that would be dead code,
	// SA4023). That very fact is *why* the case matters: an interface holding a typed
	// non-nil pointer is itself non-nil, so KindOf(r) returning "" below is indistinguishable
	// from the nil-Result case (see TestKindOf_TypedNilResultDoesNotPanicAndReturnsEmpty) --
	// a caller cannot use KindOf's "" result to tell "no Result at all" apart from "a Result
	// whose concrete type doesn't match any arm".
	if got := submission.KindOf(r); got != "" {
		t.Errorf("KindOf(&Accepted{}) = %q, want \"\" (pointer variant matches no value-type "+
			"case arm in KindOf's type switch, same as nil -- this is the documented gap, not "+
			"a fix)", got)
	}
}

// TestKindOf_TypedNilResultDoesNotPanicAndReturnsEmpty: a Result variable can hold a typed
// nil pointer (the classic Go "non-nil interface wrapping a nil concrete value" trap) because
// *Retryable also satisfies Result (see the test above). KindOf must not panic on it and must
// report it the same way it reports every other non-matching concrete type: "".
func TestKindOf_TypedNilResultDoesNotPanicAndReturnsEmpty(t *testing.T) {
	var p *submission.Retryable // nil *Retryable
	var r submission.Result = p // r is a NON-nil interface: (type=*Retryable, value=nil)

	if r == nil {
		t.Errorf("an interface variable holding a typed nil pointer must itself compare != nil " +
			"(got r == nil, want r != nil) -- this is the trap this test exists to catch")
	}

	defer func() {
		if rec := recover(); rec != nil {
			t.Errorf("KindOf panicked on a typed-nil *Retryable Result: %v", rec)
		}
	}()
	if got := submission.KindOf(r); got != "" {
		t.Errorf("KindOf(typed-nil *Retryable) = %q, want \"\"", got)
	}
}

// TestReason_JSONShape_OmitsPathWhenEmpty (adversarial coverage for [reason-shape]):
// invoices.rejection_reasons documents each element as {"code","message","path"} with path
// OPTIONAL. Nothing in result_test.go checks the actual encoding/json tags do what the doc
// comment claims -- a Path field without `omitempty`, or a wrong JSON key, would compile and
// pass every Stage 2.5 spec while silently breaking the persisted shape.
func TestReason_JSONShape_OmitsPathWhenEmpty(t *testing.T) {
	reason := submission.Reason{Code: "SCHEMA_INVALID", Message: "TIN failed checksum"}

	b, err := json.Marshal(reason)
	if err != nil {
		t.Fatalf("json.Marshal(Reason with empty Path): %v", err)
	}

	want := `{"code":"SCHEMA_INVALID","message":"TIN failed checksum"}`
	if string(b) != want {
		t.Errorf("json.Marshal(Reason{Code, Message}) = %s, want %s (path must be OMITTED, "+
			"not present as \"\")", b, want)
	}

	// Round-trip: unmarshalling back must reproduce the zero Path, not error.
	var round submission.Reason
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("json.Unmarshal round-trip: %v", err)
	}
	if round != reason {
		t.Errorf("round-tripped Reason = %+v, want %+v", round, reason)
	}
}

// TestReason_JSONShape_IncludesPathWhenSet: the companion case -- when Path is non-empty it
// MUST appear, using the exact key "path" (not "Path", "field", or "pointer").
func TestReason_JSONShape_IncludesPathWhenSet(t *testing.T) {
	reason := submission.Reason{Code: "MISSING_FIELD", Message: "buyer TIN is required", Path: "buyer.tin"}

	b, err := json.Marshal(reason)
	if err != nil {
		t.Fatalf("json.Marshal(Reason with Path set): %v", err)
	}

	want := `{"code":"MISSING_FIELD","message":"buyer TIN is required","path":"buyer.tin"}`
	if string(b) != want {
		t.Errorf("json.Marshal(Reason{..., Path: %q}) = %s, want %s", reason.Path, b, want)
	}

	var round submission.Reason
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("json.Unmarshal round-trip: %v", err)
	}
	if round != reason {
		t.Errorf("round-tripped Reason = %+v, want %+v", round, reason)
	}
}
