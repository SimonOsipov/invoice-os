// M5-02-01 (task-217), Stage 2.5: the seam types are pure declarations, so a wrong-but-
// compiling implementation is the only thing most of these specs could ever catch, and a
// stub that merely returns zero values already satisfies every one of them except KindOf's.
// TestKindOf is this subtask's ONLY red-first spec — result.go's KindOf is a deliberate
// Stage 2.5 stub that returns "" unconditionally, so TestKindOf fails on a real assertion
// (not a compile error) until the executor implements the real type switch.
//
// The other three specs in this file — TestResult_SealedVariants,
// TestEvidence_ZeroValueMeansNothingSent, TestResult_ShapesCannotMix — are, like
// TestValidatorClient_DoesNotImportValidationPackage (internal/invoice/validator_test.go:
// 440-450), baseline/regression guards, not strict red-to-green specs: the properties they
// check already hold for any implementation that declares the types as this subtask's
// result.go/adapter.go/canonical.go do (no stub is needed to satisfy them, unlike KindOf).
// They exist to lock the declared shape against later drift, not to demonstrate a red-to-
// green transition, and they pass from this subtask's first commit onward.
//
// Package submission_test (external), matching every other test file in this package
// (exchange_test.go, exchange_db_test.go, failure_modes_test.go, worker_smoke_test.go).
// TestMain already exists at failure_modes_test.go:57 — one per test binary — so this file
// defines none.
package submission_test

import (
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestKindOf (AC-5, red-first): each of the four Result variants, plus a nil Result, names
// itself correctly. A stub that returns "" for everything (this subtask's Stage 2.5
// placeholder) fails every non-nil case.
func TestKindOf(t *testing.T) {
	tests := []struct {
		name string
		r    submission.Result
		want string
	}{
		{"Accepted", submission.Accepted{}, "accepted"},
		{"Rejected", submission.Rejected{}, "rejected"},
		{"Pending", submission.Pending{}, "pending"},
		{"Retryable", submission.Retryable{}, "retryable"},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := submission.KindOf(tt.r); got != tt.want {
				t.Errorf("KindOf(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// TestResult_SealedVariants (AC-1, regression guard): the four variants each implement
// Result (compile-time, via the var _ Result = ... lines) and, at runtime, a Result-typed
// value holding each one lands in exactly one type-switch arm — the default arm (which would
// fire for a hypothetical fifth variant) never executes.
func TestResult_SealedVariants(t *testing.T) {
	var _ submission.Result = submission.Accepted{}
	var _ submission.Result = submission.Rejected{}
	var _ submission.Result = submission.Pending{}
	var _ submission.Result = submission.Retryable{}

	variants := []submission.Result{
		submission.Accepted{},
		submission.Rejected{},
		submission.Pending{},
		submission.Retryable{},
	}
	for _, r := range variants {
		matched := 0
		switch r.(type) {
		case submission.Accepted:
			matched++
		case submission.Rejected:
			matched++
		case submission.Pending:
			matched++
		case submission.Retryable:
			matched++
		default:
			t.Errorf("Result %#v matched no known variant arm (default fired) -- a fifth "+
				"variant would break the seal [sealed-result-union]", r)
		}
		if matched != 1 {
			t.Errorf("Result %#v matched %d variant arms, want exactly 1", r, matched)
		}
	}
}

// TestEvidence_ZeroValueMeansNothingSent (AC-3, regression guard): Evidence{} means "nothing
// reached the wire" -- ReachedWire false and every pointer field nil, including LatencyMS
// (Core AC-3 names it explicitly even though the story's own Test Specs table row omitted
// it; task AC-3 requires this assertion).
func TestEvidence_ZeroValueMeansNothingSent(t *testing.T) {
	var ev submission.Evidence

	if ev.ReachedWire {
		t.Errorf("zero Evidence.ReachedWire = %v, want false", ev.ReachedWire)
	}
	if ev.HTTPStatus != nil {
		t.Errorf("zero Evidence.HTTPStatus = %v, want nil", ev.HTTPStatus)
	}
	if ev.LatencyMS != nil {
		t.Errorf("zero Evidence.LatencyMS = %v, want nil", ev.LatencyMS)
	}
	if ev.RequestBody != nil {
		t.Errorf("zero Evidence.RequestBody = %v, want nil", ev.RequestBody)
	}
	if ev.ResponseBody != nil {
		t.Errorf("zero Evidence.ResponseBody = %v, want nil", ev.ResponseBody)
	}
	if len(ev.RequestHeaders) != 0 {
		t.Errorf("zero Evidence.RequestHeaders has %d entries, want none", len(ev.RequestHeaders))
	}
	if len(ev.ResponseHeaders) != 0 {
		t.Errorf("zero Evidence.ResponseHeaders has %d entries, want none", len(ev.ResponseHeaders))
	}
}

// TestResult_ShapesCannotMix (AC-2, regression guard): Accepted carries no Reasons field and
// Rejected carries no IRN/CSID/QRPayload field, so the two shapes cannot be confused at
// compile time -- Core AC-2 holds structurally, not by validation.
func TestResult_ShapesCannotMix(t *testing.T) {
	accepted := reflect.TypeOf(submission.Accepted{})
	for i := 0; i < accepted.NumField(); i++ {
		if name := accepted.Field(i).Name; name == "Reasons" {
			t.Errorf("Accepted has a %q field, want none (Core AC-2: Accepted carries no reasons)", name)
		}
	}

	rejected := reflect.TypeOf(submission.Rejected{})
	forbidden := map[string]bool{"IRN": true, "CSID": true, "QRPayload": true}
	for i := 0; i < rejected.NumField(); i++ {
		if name := rejected.Field(i).Name; forbidden[name] {
			t.Errorf("Rejected has a %q field, want none (Core AC-2: Rejected carries no identifier)", name)
		}
	}
}
