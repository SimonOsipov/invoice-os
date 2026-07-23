// mock_adapter.go: M5-03-03 (task-226). The exported MockAdapter seam -- MockConfig,
// NewMockAdapter, Name/Version/Transform, the two-phase Submit, and the context-aware wait
// helper both Submit and Poll share.
//
// Everything M5-03-03 owns is implemented here. Poll alone is still a no-op stub: it belongs to
// M5-03-04, which reuses mockWait unchanged.
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
	"strconv"
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
// A negative cfg.Latency is CLAMPED to zero rather than refused
// ([negative-latency-rejected-at-the-env-edge]): the HARD failure for a negative value belongs at
// the env edge in M5-03-05, where an operator typed it; reaching this constructor with one is Go
// code's -- i.e. a test's -- business. Clamping keeps a.cfg.Latency a value mockInFlight can
// multiply without producing a negative in-flight duration that only mockWait's `d <= 0` branch
// happens to absorb.
func NewMockAdapter(cfg MockConfig) *MockAdapter {
	if cfg.Latency < 0 {
		cfg.Latency = 0
	}
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
// plus the Evidence of the attempt. The step comments below are the ten-row evidence matrix in
// task-226's implementation plan, read top to bottom.
func (a *MockAdapter) Submit(ctx context.Context, w Wire, idemKey string) (Result, Evidence) {
	// STEP 0, BEFORE the ctx check on purpose. The headers describe the call we were ABOUT to
	// make, so they belong on every row of the matrix without exception -- including the
	// pre-cancelled one, where omitting them would make L13 vacuous for the contract suite's
	// cancelled-Submit call (contract_test.go:314 passes a real key). reference_adapter_test.go:65
	// already builds them first for the same reason.
	start := time.Now()
	ev := Evidence{RequestHeaders: mockRequestHeaders(idemKey)}

	// STEP 1 (L16). The ONE row of the matrix where LatencyMS stays nil: nothing ran, so nothing
	// was measured, and stamping a 0 here would be a measurement we never took. ReachedWire stays
	// false, so ExchangeFor derives "connection_failed" -- correct, the bytes never left.
	if err := ctx.Err(); err != nil {
		return Retryable{Err: err}, ev
	}

	// STEP 2. Only when there were bytes: a never-captured body is different evidence from an
	// empty one, and exchange.go:213-221 preserves that difference all the way to SQL NULL.
	if len(w) > 0 {
		body := string(w)
		ev.RequestBody = &body
	}

	// STEP 3. The WRAPPED error travels upward, never the bare sentinel: errors.Is still reaches
	// ErrMockUnparseableWire, and the decoder's own reason survives into the M5-07 archive
	// (mock_wire.go:226). NEVER Rejected -- an unparseable wire is a transport failure, not an
	// authority verdict ([errors-never-verdicts]).
	env, err := parseMockEnvelope(w)
	if err != nil {
		ev.LatencyMS = mockElapsedMS(start)
		return Retryable{Err: err}, ev
	}

	// STEP 4. The trigger is read back OUT of the wire ([trigger-read-from-the-real-bis-field]) --
	// Submit never sees the Canonical. Then the connect phase, which is INSTANT: it does not wait
	// even at a large baseline, because nothing has left the process yet to be in flight
	// ([two-phase-wire]). ReachedWire therefore stays false.
	trigger := mockTriggerFor(mockBuyerTIN(env))
	if trigger == mockTriggerConnection {
		ev.LatencyMS = mockElapsedMS(start)
		return Retryable{Err: ErrMockConnectionRefused}, ev
	}

	// STEP 5. ReachedWire goes true BEFORE the wait: from here on the bytes are on the wire and a
	// context that dies mid-flight must report "sent", because the APP may well have received
	// them. That is the row M5-01 kept the two outcomes apart for.
	ev.ReachedWire = true
	if err := mockWait(ctx, mockInFlight(a.cfg, trigger)); err != nil {
		ev.LatencyMS = mockElapsedMS(start)
		return Retryable{Err: err}, ev
	}
	// A timeout is the in-flight failure: we waited the whole (multiplied) duration and no
	// response came back. ReachedWire is already true and stays true.
	if trigger == mockTriggerTimeout {
		ev.LatencyMS = mockElapsedMS(start)
		return Retryable{Err: ErrMockTimeout}, ev
	}

	// STEP 6 -- synthesize the response. Everything below this line is a function of w alone
	// ([deterministic-evidence]); the only value that is not is Pending.PollAfter.
	ids := mockIdentifiersFor(w, env)

	respHeaders := http.Header{}
	respHeaders.Set("Content-Type", mockContentTypeJSON)

	var (
		result Result
		status int
		body   string
	)
	switch trigger {
	case mockTriggerReject:
		status, body = http.StatusUnprocessableEntity, mockRejectedBody()
		result = Rejected{Reasons: mockRejectionReasons()}
	case mockTriggerPending:
		// The SAME ref goes into the Pending and into the archived 202 body: the caller who polls
		// and the caller who reads the archive must not see two different handles.
		ref := encodeMockRef(mockPendingPolls, ids)
		status, body = http.StatusAccepted, mockPendingBody(ref)
		result = Pending{Ref: ref, PollAfter: time.Now().Add(mockPollBackoff)}
	case mockTriggerUnavailable:
		status, body = http.StatusServiceUnavailable, mockUnavailableBody()
		respHeaders.Set("Retry-After", strconv.Itoa(mockRetryAfterSeconds))
		// A 503 is a transport verdict, not a validation one, so Retryable and never Rejected.
		result = Retryable{Err: ErrMockUnavailable}
	default:
		// MANDATORY default, covering accept, slow AND any trigger a later story allocates but
		// forgets to case here: four explicit cases with no default would leave result nil, an L06
		// hard failure. Deliberately the opposite ruling from mockTriggerFor (mock_script.go:212),
		// where a default would silently WIDEN the set of inputs that activate a scripted outcome;
		// here it narrows the blast radius of an omission to "behaves like accept".
		//
		// slow lands here on purpose: what makes it slow is its duration (already waited out in
		// step 5), not its verdict, so at MockConfig{} it is indistinguishable from accept.
		status, body = http.StatusOK, mockAcceptedBody(ids)
		result = Accepted{IRN: ids.IRN, CSID: ids.CSID, QRPayload: ids.QRPayload}
	}

	ev.HTTPStatus = mockIntPtr(status)
	ev.ResponseBody = &body
	ev.ResponseHeaders = respHeaders
	ev.LatencyMS = mockElapsedMS(start)
	return result, ev
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
// A bare time.Sleep is FORBIDDEN here ([cancellation-is-observable-in-the-wait]), and so is a
// time.Sleep followed by a post-wake ctx.Err() re-check: both return exactly the Retryable a
// correct select returns, they just take the full duration to do it. The only thing that tells
// them apart is ELAPSED TIME, which is what
// TestMockAdapter_SubmitAbortsTheInFlightWaitOnCancellation measures.
//
// TWO RULES BELOW LOOK LIKE OMISSIONS AND ARE NOT -- neither is covered by a spec, both are
// review-enforced:
//
//  1. No ctx.Err() re-check after a wait that returned nil. The select's outcome is
//     AUTHORITATIVE. Re-checking would reintroduce exactly the race the select removes: a
//     context cancelled in the instant between the timer firing and the re-check would flip a
//     completed in-flight wait to Retryable, a row the evidence matrix does not have.
//  2. `d <= 0` returns immediately WITHOUT consulting ctx. Submit's entry check (L16) has
//     already run; a zero-length wait must not become a second, racy cancellation gate. At
//     MockConfig{} -- what the whole suite and CI use -- there is no in-flight window to cancel
//     in at all.
func mockWait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// mockInFlight is the ONLY place a latency multiple is computed -- mockWait knows nothing about
// triggers. slow -> Latency*mockSlowFactor, timeout -> Latency*mockTimeoutFactor, everything
// else -> Latency. The connection trigger never reaches the wait at all.
func mockInFlight(cfg MockConfig, trigger mockTrigger) time.Duration {
	switch trigger {
	case mockTriggerSlow:
		return cfg.Latency * mockSlowFactor
	case mockTriggerTimeout:
		return cfg.Latency * mockTimeoutFactor
	default:
		return cfg.Latency
	}
}

// mockRequestHeaders builds a FRESH http.Header per call via Set (which canonicalises keys); no
// package-level map is ever shared across two Evidences. Content-Type, Accept and User-Agent are
// unconditional; Idempotency-Key appears only when idemKey != "".
//
// Content-Length is on the allowlist but deliberately NOT set: AC-6 names three unconditional
// request headers, and a fourth is smuggled scope.
func mockRequestHeaders(idemKey string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", mockContentTypeJSON)
	h.Set("Accept", mockContentTypeJSON)
	h.Set("User-Agent", mockUserAgent)
	// ABSENT, not empty-valued, when there is no key: idempotencyKeyValue
	// (contract_test.go:566) reports present=true for an empty value slice, so an empty-valued
	// header would read as a real one and is meaningless evidence. Poll reaches this helper with
	// "" and therefore stamps none -- [poll-sets-no-idempotency-key], enforced by construction.
	if idemKey != "" {
		h.Set("Idempotency-Key", idemKey)
	}
	return h
}

// mockElapsedMS measures one attempt for Evidence.LatencyMS.
//
// Non-negative BY CONSTRUCTION, which is what satisfies L12 and app_exchange's `latency_ms >= 0`
// CHECK without a clamp: time.Now carries a monotonic reading and time.Since subtracts through
// it, so the result cannot go backwards across a wall-clock adjustment.
func mockElapsedMS(start time.Time) *int {
	return mockIntPtr(int(time.Since(start).Milliseconds()))
}

// mockIntPtr returns a pointer to its own copy of v, so no two Evidences ever share one.
func mockIntPtr(v int) *int { return &v }
