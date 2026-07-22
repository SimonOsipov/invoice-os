// QA Mode B (task-218): adversarial / edge coverage added AFTER exchange_bridge_test.go's
// Test Specs went green. Nothing here duplicates that file's seven specs; each test below
// targets a case the story's Test Specs table does not name -- internally inconsistent
// Evidence, OpPoll, non-positive attempt, header casing/multi-value, malformed body bytes,
// and nil-vs-empty header pass-through.
//
// Package submission_test (external), matching every other test file in this package.
// TestMain already exists at failure_modes_test.go:57, so this file defines none. No
// testify. Reuses exBridgeAdapter and exExtractCheckValues from exchange_bridge_test.go --
// same package, not redeclared.
package submission_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// TestExchangeFor_InternallyInconsistentEvidencePassesThroughUnjudged: Evidence claiming
// ReachedWire == false yet also carrying a non-nil HTTPStatus is internally inconsistent --
// law L11 (!ReachedWire ⟹ HTTPStatus == nil) belongs to the M5-02-06/07 contract suite,
// enforced against ADAPTERS, not to ExchangeFor. ExchangeFor must derive Outcome from
// ReachedWire alone and pass HTTPStatus through unexamined -- it must not "fix" the
// inconsistency by clearing HTTPStatus, nor let HTTPStatus's presence override the
// ReachedWire-derived Outcome.
func TestExchangeFor_InternallyInconsistentEvidencePassesThroughUnjudged(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}
	status := 200
	ev := submission.Evidence{
		ReachedWire: false, // claims nothing reached the wire...
		HTTPStatus:  &status, // ...yet carries a response status anyway.
	}

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	if got.Outcome != submission.OutcomeConnectionFailed {
		t.Errorf("ExchangeFor(...).Outcome = %q, want %q -- Outcome is derived from "+
			"ReachedWire alone, never overridden by an inconsistent HTTPStatus",
			got.Outcome, submission.OutcomeConnectionFailed)
	}
	if got.HTTPStatus == nil || *got.HTTPStatus != status {
		t.Errorf("ExchangeFor(...).HTTPStatus = %v, want %d -- ExchangeFor must not silently "+
			"clear HTTPStatus to make the record internally consistent; that judgement is "+
			"L11's business elsewhere, not this bridge's", got.HTTPStatus, status)
	}
}

// TestExchangeFor_OpPollProducesLegalPollOperation: the story's Test Specs table only
// exercises ExchangeFor with OpSubmit. OpPoll must produce the string "poll" on the
// returned Exchange, and "poll" must be a member of the LIVE operation CHECK vocabulary --
// verified against the migration text via exExtractCheckValues (exchange_bridge_test.go),
// not a hardcoded literal compared to itself.
func TestExchangeFor_OpPollProducesLegalPollOperation(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}
	ev := submission.Evidence{ReachedWire: true}

	got := submission.ExchangeFor(a, submission.OpPoll, 2, "job-1", "inv-1", ev)

	if got.Operation != "poll" {
		t.Errorf("ExchangeFor(a, OpPoll, ...).Operation = %q, want %q", got.Operation, "poll")
	}

	opSQL, err := migrations.FS.ReadFile("20260722093218_app_exchange.sql")
	if err != nil {
		t.Fatalf("read 20260722093218_app_exchange.sql from migrations.FS: %v", err)
	}
	legalOps := exExtractCheckValues(t, string(opSQL), "CHECK (operation IN (")

	found := false
	for _, op := range legalOps {
		if op == got.Operation {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ExchangeFor(...).Operation = %q is not a member of the live operation CHECK "+
			"vocabulary %v", got.Operation, legalOps)
	}
}

// TestExchangeFor_NonPositiveAttemptPassesThroughUnvalidated: ExchangeFor is pure and total
// -- it does not validate attempt >= 1. The app_exchange CHECK (attempt >= 1) is the actual
// guard, enforced by Postgres at RecordExchange's INSERT, not by this function. A caller
// passing 0 or a negative attempt gets it back untouched; only the database rejects it.
func TestExchangeFor_NonPositiveAttemptPassesThroughUnvalidated(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}
	ev := submission.Evidence{ReachedWire: true}

	for _, attempt := range []int{0, -1} {
		got := submission.ExchangeFor(a, submission.OpSubmit, attempt, "job-1", "inv-1", ev)
		if got.Attempt != attempt {
			t.Errorf("ExchangeFor(..., attempt=%d, ...).Attempt = %d, want %d -- ExchangeFor "+
				"passes attempt through without validation; app_exchange's CHECK "+
				"(attempt >= 1), not this function, is what rejects a non-positive value",
				attempt, got.Attempt, attempt)
		}
	}
}

// TestExchangeFor_HeaderCasingAndMultiValueSurviveVerbatim: an http.Header built as a raw
// map literal (not through Set/Add) stores a non-canonical key verbatim, and a repeated
// header name holds multiple values. ExchangeFor must not run any canonicalisation or
// flattening over Evidence's headers -- that is ScrubHeaders' job at write time, not this
// builder's -- so both the raw key and every value must survive untouched.
func TestExchangeFor_HeaderCasingAndMultiValueSurviveVerbatim(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}

	h := http.Header{}
	//nolint:staticcheck // SA1008: deliberately non-canonical key -- proves ExchangeFor performs no header canonicalisation
	h["x-request-id"] = []string{"one", "two"} // raw, non-canonical key + multiple values,
	// bypassing Set/Add's canonicalisation on purpose.

	ev := submission.Evidence{RequestHeaders: h, ReachedWire: true}

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	vs, ok := got.RequestHeaders["x-request-id"]
	if !ok {
		t.Fatalf("ExchangeFor(...).RequestHeaders lost the raw, non-canonical key %q -- "+
			"ExchangeFor must not canonicalise (that is ScrubHeaders' job, not this builder's)",
			"x-request-id")
	}
	if len(vs) != 2 || vs[0] != "one" || vs[1] != "two" {
		t.Errorf("ExchangeFor(...).RequestHeaders[%q] = %v, want [\"one\" \"two\"] -- a "+
			"multi-value header must survive verbatim", "x-request-id", vs)
	}
}

// TestExchangeFor_MalformedBodyPassesThroughUnrepaired: a body containing a NUL byte and an
// invalid UTF-8 byte must pass through UNREPAIRED. Repair (NUL removal, UTF-8 coercion) is
// SafeBody's job, applied by RecordExchange at write time (Decision
// [scrub-is-the-recorders-job]) -- not ExchangeFor's. This is a second, content-based proof
// alongside TestExchangeFor_PassesEvidenceThroughUnchanged's pointer-identity proof: even if
// some future change allocated a new string instead of reusing the pointer, the malformed
// bytes themselves must still be present, not sanitised.
func TestExchangeFor_MalformedBodyPassesThroughUnrepaired(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}

	bad := "before\x00after\xffgarbled" // a NUL byte and a byte (0xFF) that is never valid
	// UTF-8 on its own -- exactly what SafeBody would strip/coerce.

	ev := submission.Evidence{RequestBody: &bad, ReachedWire: true}

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	if got.RequestBody != &bad {
		t.Fatal("ExchangeFor(...).RequestBody is not the same pointer as the input -- cannot " +
			"assert content pass-through against a body that was reallocated")
	}
	if !strings.Contains(*got.RequestBody, "\x00") {
		t.Error("ExchangeFor(...).RequestBody lost its NUL byte -- ExchangeFor must not repair " +
			"the body; SafeBody is RecordExchange's job at write time, not this builder's")
	}
	if *got.RequestBody != bad {
		t.Errorf("ExchangeFor(...).RequestBody content changed: got %q, want %q unrepaired",
			*got.RequestBody, bad)
	}
}

// TestExchangeFor_EmptyHeaderVsNilHeaderPassThroughDistinctly documents ExchangeFor's
// observed behaviour for the two header states a caller could hand it: a non-nil, empty
// http.Header{}, and Evidence's zero-value nil http.Header. ExchangeFor is a direct
// pass-through (no defensive copy, no defaulting), so each state must come out exactly as
// it went in -- nil stays nil, and a non-nil empty map stays non-nil.
func TestExchangeFor_EmptyHeaderVsNilHeaderPassThroughDistinctly(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}

	evEmpty := submission.Evidence{RequestHeaders: http.Header{}, ReachedWire: true}
	gotEmpty := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", evEmpty)
	if gotEmpty.RequestHeaders == nil {
		t.Error("ExchangeFor(...) turned a non-nil, empty http.Header{} into nil -- " +
			"pass-through must preserve nil-ness exactly, not default an empty map to nil")
	}
	if len(gotEmpty.RequestHeaders) != 0 {
		t.Errorf("ExchangeFor(...).RequestHeaders = %v, want empty", gotEmpty.RequestHeaders)
	}

	evNil := submission.Evidence{ReachedWire: true} // RequestHeaders left at its zero value: nil
	gotNil := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", evNil)
	if gotNil.RequestHeaders != nil {
		t.Errorf("ExchangeFor(...).RequestHeaders = %#v, want nil -- a nil Evidence header "+
			"must stay nil, never defaulted to http.Header{}", gotNil.RequestHeaders)
	}
}
