// mock_adapter.go: M5-03-03 (task-226). The exported MockAdapter seam -- MockConfig,
// NewMockAdapter, Name/Version/Transform, the two-phase Submit, and the context-aware wait
// helper both Submit and Poll share.
//
// STAGE 1 (QA Mode A, RED): this file ships the complete DECLARATION SET -- every constant with
// its real value, the three sentinels, the types and every function signature -- with
// deliberately no-op bodies for Submit, Poll, mockWait, mockInFlight and mockRequestHeaders, so
// that every spec in mock_adapter_test.go fails on an ASSERTION rather than on a compile error.
// Name, Version and Transform ARE fully implemented: each is a one-liner whose spec would
// otherwise be untestable, and neither carries any of this subtask's real behaviour.
//
// Decisions this file implements (the executor must not "improve" any of them):
//
//   - [cancellation-is-observable-in-the-wait] mockWait MUST be a select over ctx.Done() and a
//     time.Timer. A bare time.Sleep(d) -- and equally a time.Sleep(d) followed by a post-wake
//     ctx.Err() re-check -- satisfies contract law L16 (contract_test.go:309-341 only ever
//     PRE-cancels) while sleeping straight through a mid-flight cancellation. Both return
//     Retryable; they just take the full duration to do it. The ONLY oracle that separates them
//     is ELAPSED TIME, which is what TestMockAdapter_SubmitAbortsTheInFlightWaitOnCancellation
//     measures.
//   - Two rules inside mockWait that look like omissions and are not: (1) never re-check
//     ctx.Err() after a wait that returned nil -- the select's outcome is authoritative, and a
//     re-check reintroduces a race the matrix has no row for; (2) `d <= 0` returns immediately
//     WITHOUT consulting ctx -- the entry check already ran and a zero-length wait must not
//     become a second cancellation gate.
//   - [two-phase-wire] the connect phase is INSTANT and the in-flight phase is the wait.
//     Evidence.ReachedWire is set true BEFORE the wait, so a context that dies mid-flight
//     reports true (outcome "sent") while the connection trigger reports false (outcome
//     "connection_failed").
//   - [one-latency-knob] MockConfig has exactly one field. slow and timeout are fixed multiples
//     of it (mockSlowFactor, mockTimeoutFactor); the pending backoff is a package constant. The
//     zero value means "instant", which is what the whole test suite uses.
//   - [headers-from-the-scrub-allowlist] every header name below is on exchange.go's 12-name
//     write-time allowlist, so nothing the mock records is silently dropped by ScrubHeaders on
//     the way into the M5-07 archive.
//   - [poll-sets-no-idempotency-key] the Idempotency-Key header is stamped only when idemKey is
//     non-empty, never as an empty-valued header (an empty value reads as "present" to L13's
//     idempotencyKeyValue and is meaningless evidence). Poll reaches the same helper with "".
//   - Version() is a CONSTANT: L02 pins it across freshly constructed instances, so no config
//     may leak into it.
package submission

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	// mockAdapterName is ALSO M5-03-05's registry key -- registry.go:15 keys the map by Name(),
	// so the two can never disagree.
	mockAdapterName = "mock"
	// mockAdapterVersion is a constant, never derived from MockConfig (L02).
	mockAdapterVersion = "v1"

	mockContentTypeJSON = "application/json"
	mockUserAgent       = "FiscalBridge-MockAPP/v1"

	// The slow and timeout triggers are fixed multiples of the one latency knob
	// ([one-latency-knob]). At MockConfig{} both multiply to zero, which is why
	// TestMockAdapter_SlowAndTimeoutMultiplyTheBaseline exists: without it nothing in the suite
	// ever executes these two constants and they could hold any value.
	mockSlowFactor    = 4
	mockTimeoutFactor = 8

	// mockPendingPolls is how many polls a pending submission needs before it converges
	// ([ref-carries-the-verdict]); it is the N encoded into the first Ref.
	mockPendingPolls = 2
	// mockPollBackoff derives from mock_script.go's mockPollAfterSeconds -- never a second
	// literal 5 -- so the 202 body's pollAfterSeconds and Pending.PollAfter can never drift.
	mockPollBackoff = mockPollAfterSeconds * time.Second

	// mockRetryAfterSeconds is the 503's Retry-After. Deliberately NOT mockPollAfterSeconds:
	// one number behind two meanings ("when to re-submit" vs "when to poll") decays.
	mockRetryAfterSeconds = 30
)

// The three transport sentinels. Every one of them travels inside a Retryable, never a verdict
// ([errors-never-verdicts]): none of them is the authority deciding anything about the invoice.
var (
	// ErrMockConnectionRefused is the connect-phase failure: nothing left the process, so
	// Evidence.ReachedWire stays false and ExchangeFor derives "connection_failed".
	ErrMockConnectionRefused = errors.New("submission: mock APP refused the connection")
	// ErrMockTimeout is the in-flight failure: the bytes left the process and no response came
	// back, so ReachedWire is true and ExchangeFor derives "sent".
	ErrMockTimeout = errors.New("submission: mock APP timed out in flight")
	// ErrMockUnavailable accompanies the synthesized 503. A 503 is a transport verdict, not a
	// validation one, so it is Retryable and never Rejected.
	ErrMockUnavailable = errors.New("submission: mock APP is temporarily unavailable")
)

// MockConfig is the mock's entire configuration surface ([one-latency-knob]).
//
// A VALUE type with no pointer, map or slice field -- that is what lets MockAdapter hold no
// mutable state at all (M5-03-04's AC-8 asserts exactly that, by reflection).
type MockConfig struct {
	// Latency is the boot-time baseline applied to every in-flight wait. The zero value means
	// "instant". cmd/submission supplies the real value from APP_ADAPTER_MOCK_LATENCY
	// (mockLatencyEnv, mock_script.go).
	Latency time.Duration
}

// MockAdapter is the content-keyed simulator behind the M5-02 Adapter seam. Exactly one
// immutable field; no clock beyond measurement, no randomness, no counter, no database.
type MockAdapter struct {
	cfg MockConfig
}

// Pointer receivers throughout, and the interface assertion is on the POINTER type on purpose:
// declaring it on the value type as well would let a *MockAdapter and a MockAdapter both satisfy
// Adapter, the same aliasing hazard L06 documents for Result.
var _ Adapter = (*MockAdapter)(nil)

// NewMockAdapter builds a mock adapter from cfg.
//
// TODO(M5-03-03): implemented by the executor -- a negative cfg.Latency must be CLAMPED to zero
// here. The hard failure for a negative value lives at the env edge in M5-03-05
// ([negative-latency-rejected-at-the-env-edge]); reaching this constructor with one is a test's
// business, and a negative timer fires immediately anyway.
func NewMockAdapter(cfg MockConfig) *MockAdapter {
	return &MockAdapter{cfg: cfg}
}

// Name is the stable registry key and the value persisted as submission_jobs.adapter.
func (a *MockAdapter) Name() string { return mockAdapterName }

// Version is a constant, identical across every instance however configured (L02).
func (a *MockAdapter) Version() string { return mockAdapterVersion }

// Transform is exactly mockWireFrom and nothing else, so there is only ever one marshal path
// (mock_wire.go:204). ctx is unused on purpose: the projection is pure.
func (a *MockAdapter) Transform(_ context.Context, c Canonical) (Wire, error) {
	return mockWireFrom(c)
}

// Submit runs the simulated call in six ordered steps and returns exactly one Result variant
// plus the Evidence of the attempt.
//
// TODO(M5-03-03): implemented by the executor. The ordered steps and the ten-row evidence matrix
// are in task-226's implementation plan; the short version:
//
//	step 0  start := time.Now() and build the request headers -- BEFORE the ctx check, so the
//	        pre-cancelled row still carries headers (L13 is non-vacuous for the contract suite's
//	        cancelled-Submit call, contract_test.go:314).
//	step 1  ctx.Err() (L16) -> Retryable, ReachedWire false, and the ONE row where LatencyMS
//	        stays nil. Nothing else runs.
//	step 2  request body: set only when len(w) > 0 -- never-captured is not the same evidence as
//	        empty (exchange.go:213-221).
//	step 3  parse -> on error, Retryable carrying the WRAPPED error from parseMockEnvelope (so
//	        the decoder's reason reaches the M5-07 archive), ReachedWire false, NEVER Rejected.
//	step 4  trigger, then the INSTANT connect phase: connection -> Retryable{
//	        ErrMockConnectionRefused}, ReachedWire still false.
//	step 5  ReachedWire = true, THEN mockWait. A wait error -> Retryable{ctx.Err()} with
//	        ReachedWire true and no status/bodies. Then timeout -> Retryable{ErrMockTimeout}.
//	step 6  synthesize: reject 422, pending 202, unavailable 503 + Retry-After, and a MANDATORY
//	        default: arm (accept, slow and any future unallocated trigger) 200 -- four explicit
//	        cases with no default would leave Result nil for an unhandled trigger, an L06 hard
//	        failure.
func (a *MockAdapter) Submit(ctx context.Context, w Wire, idemKey string) (Result, Evidence) {
	return nil, Evidence{}
}

// Poll resumes a deferred verdict from the Ref a prior Pending carried.
//
// TODO(M5-03-04): implemented there, not here. It reuses mockWait with a.cfg.Latency (always the
// baseline -- a ref carries no trigger), placed after the ref decode and before the 202/200
// synthesis, with ReachedWire already true.
func (a *MockAdapter) Poll(ctx context.Context, ref Ref) (Result, Evidence) {
	return nil, Evidence{}
}

// mockWait is the ONE in-flight wait, shared by Submit and Poll. It returns nil when the full
// duration elapsed and ctx.Err() when the context ended first. No bool, no second return.
//
// TODO(M5-03-03): implemented by the executor, and it MUST be:
//
//	if d <= 0 { return nil }
//	timer := time.NewTimer(d)
//	defer timer.Stop()
//	select {
//	case <-ctx.Done(): return ctx.Err()
//	case <-timer.C:    return nil
//	}
//
// A bare time.Sleep is FORBIDDEN here ([cancellation-is-observable-in-the-wait]). Leaving this
// stubbed is deliberate: the elapsed-time specs must genuinely fail against it.
func mockWait(ctx context.Context, d time.Duration) error {
	return nil
}

// mockInFlight is the ONLY place a latency multiple is computed -- mockWait knows nothing about
// triggers. slow -> Latency*mockSlowFactor, timeout -> Latency*mockTimeoutFactor, everything
// else -> Latency. The connection trigger never reaches the wait at all.
//
// TODO(M5-03-03): implemented by the executor.
func mockInFlight(cfg MockConfig, trigger mockTrigger) time.Duration {
	return 0
}

// mockRequestHeaders builds a FRESH http.Header per call via Set (which canonicalises keys); no
// package-level map is ever shared across two Evidences. Content-Type, Accept and User-Agent are
// unconditional; Idempotency-Key appears only when idemKey != "".
//
// Content-Length is on the allowlist but deliberately NOT set: AC-6 names three unconditional
// request headers, and a fourth is smuggled scope.
//
// TODO(M5-03-03): implemented by the executor.
func mockRequestHeaders(idemKey string) http.Header {
	return nil
}
