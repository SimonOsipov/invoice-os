// M5-02-06 (task-220), Stage 1: the compliant, PROGRAMMABLE reference adapter. Unlike
// contract_test.go's three suite functions, refAdapter itself is NOT a stub -- it must be
// genuinely lawful, because M5-02-07 runs it unmodified as one baseline case, and it doubles
// as M5-04's test double ([suite-proven-by-red]). Deliberately NOT the M5-03 mock: no
// content-keyed deterministic outcomes, no magic TINs -- that behavior belongs to M5-03 alone.
//
// Package submission_test (external), same package as contract_test.go and every other test
// file in this directory. See this test's own doc comments for the honest-framing caveat that
// applies to both tests in this file: RunAdapterContract's own body is a Stage 2.5 no-op right
// now, so "zero failures" is trivially true regardless of whether refAdapter is actually
// lawful -- it becomes meaningful proof once RunAdapterContract's Stage 2 body lands, and stays
// meaningful through M5-02-07's own reuse of this adapter.
package submission_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// refAdapter is the reference adapter. Its Result/Evidence are struct fields the test
// programs before each RunAdapterContract call, so a single type plays all four outcome modes
// (Accepted/Rejected/Pending/Retryable) without four separate implementations.
type refAdapter struct {
	name    string
	version string

	submitResult   submission.Result
	submitEvidence submission.Evidence
	pollResult     submission.Result
	pollEvidence   submission.Evidence
}

var _ submission.Adapter = (*refAdapter)(nil)

// Name/Version: fixed, non-empty, stable across every call and every instance (L01/L02) --
// newRefFactory below always sets the same two literals.
func (a *refAdapter) Name() string    { return a.name }
func (a *refAdapter) Version() string { return a.version }

// Transform is pure and deterministic (L03/L04/L05): json.Marshal only reads through c's
// pointers and slice, never writes, so c is never mutated, and the same c always marshals to
// the same bytes. It never panics on the zero Canonical (L15) -- json.Marshal handles a
// struct's zero value fine -- and a successful marshal of a non-empty struct is always
// non-empty Wire.
func (a *refAdapter) Transform(_ context.Context, c submission.Canonical) (submission.Wire, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return submission.Wire(b), nil
}

// Submit honours context cancellation FIRST (L16): an already-cancelled/expired ctx always
// yields Retryable + ReachedWire false, regardless of the programmed submitResult. Otherwise it
// echoes idemKey into the Idempotency-Key request header unconditionally (L13, exercised
// non-vacuously per the parent story's note) and returns the programmed outcome. Never
// dereferences w, so an empty Wire never panics (L15).
func (a *refAdapter) Submit(ctx context.Context, _ submission.Wire, idemKey string) (submission.Result, submission.Evidence) {
	reqHeaders := http.Header{}
	if idemKey != "" {
		reqHeaders.Set("Idempotency-Key", idemKey)
	}
	if err := ctx.Err(); err != nil {
		return submission.Retryable{Err: err}, submission.Evidence{
			RequestHeaders: reqHeaders,
			ReachedWire:    false,
		}
	}
	ev := a.submitEvidence
	ev.RequestHeaders = reqHeaders
	return a.submitResult, ev
}

// Poll ignores ref entirely (it is stateless), so an unissued ref never panics (L14) and always
// returns a well-formed, programmed result. Context cancellation is honoured first, same as
// Submit (L16).
func (a *refAdapter) Poll(ctx context.Context, _ submission.Ref) (submission.Result, submission.Evidence) {
	if err := ctx.Err(); err != nil {
		return submission.Retryable{Err: err}, submission.Evidence{ReachedWire: false}
	}
	return a.pollResult, a.pollEvidence
}

// newRefFactory returns a factory that builds a FRESH *refAdapter on every call, each
// programmed identically with result/evidence for both Submit and Poll. Returning a fresh
// instance per call (never the same pointer twice) is what lets RunAdapterContract's L02/L04
// identity checks compare two genuinely separate instances.
func newRefFactory(result submission.Result, evidence submission.Evidence) func() submission.Adapter {
	return func() submission.Adapter {
		return &refAdapter{
			name:           "reference",
			version:        "v1",
			submitResult:   result,
			submitEvidence: evidence,
			pollResult:     result,
			pollEvidence:   evidence,
		}
	}
}

// newRef is the default reference-adapter factory, programmed to Accepted. Used wherever a
// baseline lawful adapter is needed without caring which outcome mode it plays --
// TestContractSuite_UsesNarrowT (contract_test.go) uses it, and M5-02-07 runs it unmodified as
// its own baseline case.
func newRef() submission.Adapter {
	status := 200
	latency := 5
	return newRefFactory(
		submission.Accepted{IRN: "IRN-DEFAULT", CSID: "CSID-DEFAULT", QRPayload: "QR-DEFAULT"},
		submission.Evidence{
			ReachedWire:     true,
			HTTPStatus:      &status,
			LatencyMS:       &latency,
			ResponseHeaders: http.Header{},
		},
	)()
}

// TestReferenceAdapter_PassesContract (AC-5, confirmatory -- see this file's header for the
// honest-framing caveat: RunAdapterContract's own body is a Stage 2.5 no-op, so "zero failures"
// is trivially true here regardless of whether refAdapter is actually lawful).
func TestReferenceAdapter_PassesContract(t *testing.T) {
	rec := &lawRecorder{}
	RunAdapterContract(rec, newRef)
	if len(rec.messages) != 0 {
		t.Errorf("RunAdapterContract(newRef) recorded %d failure(s), want 0: %v", len(rec.messages), rec.messages)
	}
}

// TestReferenceAdapter_PassesContractInEveryOutcomeMode (AC-5, confirmatory -- same
// honest-framing caveat as above): refAdapter reprogrammed to each of the four Result variants
// in turn, run through RunAdapterContract each time.
func TestReferenceAdapter_PassesContractInEveryOutcomeMode(t *testing.T) {
	future := time.Now().Add(time.Hour)
	status := 200
	latency := 5
	evidence := submission.Evidence{
		ReachedWire:     true,
		HTTPStatus:      &status,
		LatencyMS:       &latency,
		ResponseHeaders: http.Header{},
	}

	modes := []struct {
		name   string
		result submission.Result
	}{
		{"Accepted", submission.Accepted{IRN: "IRN-1", CSID: "CSID-1", QRPayload: "QR-1"}},
		{"Rejected", submission.Rejected{Reasons: []submission.Reason{{Code: "E1", Message: "bad TIN"}}}},
		{"Pending", submission.Pending{Ref: "poll-ref-1", PollAfter: future}},
		{"Retryable", submission.Retryable{Err: errors.New("upstream 503")}},
	}

	for _, m := range modes {
		rec := &lawRecorder{}
		RunAdapterContract(rec, newRefFactory(m.result, evidence))
		if len(rec.messages) != 0 {
			t.Errorf("RunAdapterContract with reference adapter programmed to %s recorded %d "+
				"failure(s), want 0: %v", m.name, len(rec.messages), rec.messages)
		}
	}
}
