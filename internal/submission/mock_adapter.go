// mock_adapter.go: M5-03-03 (task-226) and M5-03-04 (task-227). The exported MockAdapter seam --
// MockConfig, NewMockAdapter, Name/Version/Transform, the two-phase Submit, the ref-driven Poll,
// and the context-aware wait helper both Submit and Poll share.
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

	// mockLatencyDefault is the in-flight baseline MockConfigFromEnv applies when
	// mockLatencyEnv is unset or empty. It is the default AT THE ENV EDGE ONLY -- MockConfig's
	// zero value still means "instant", which every unit test and both contract runs use.
	//
	// The VALUE is not a free choice: docs/mock-app-adapter.md:123 already publishes `800ms`,
	// so a different number here is doc drift. TestMockAdapterDoc_DocumentsEveryAllocation
	// (mock_script_test.go) is the mechanical tie between the two.
	//
	// Declared by M5-03-05's RED authoring pass because that doc pin cannot compile without
	// it; the body that CONSUMES it (MockConfigFromEnv, below) is the executor's.
	mockLatencyDefault = 800 * time.Millisecond
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

// MockConfigFromEnv reads mockLatencyEnv (APP_ADAPTER_MOCK_LATENCY) into a MockConfig.
//
// TODO(M5-03-05): implemented by the executor. This is a STUB returning the zero MockConfig and
// a nil error so the M5-03-05 RED specs compile; TestMockConfigFromEnv is red against it.
//
// The shipped body owes three branches (task-228's plan):
//   - unset or empty  -> (MockConfig{Latency: mockLatencyDefault}, nil)
//   - unparseable     -> (MockConfig{}, error naming the env var and the offending value)
//   - parsed NEGATIVE -> (MockConfig{}, error) -- NET-NEW logic with no precedent to copy.
//     internal/platform/config.go:71-81's envDuration errors ONLY on a ParseDuration failure and
//     time.ParseDuration("-1s") returns (-1s, nil), so "mirrors envDuration" is NOT license to
//     skip this guard. Write it as `< 0`, never `<= 0`: an explicitly configured "0s" is
//     legitimate (it is what CI would set) and must return (MockConfig{Latency: 0}, nil).
func MockConfigFromEnv() (MockConfig, error) {
	return MockConfig{}, nil
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

// Poll resumes a deferred verdict from the Ref a prior Pending carried. Like Submit it returns
// exactly one Result variant plus the Evidence of the attempt; the step comments below are
// task-227's five-row evidence matrix read top to bottom.
//
// THE HAND-OFF CONTRACT M5-04 DEPENDS ON. The ref is the ONLY state that exists
// ([ref-carries-the-verdict]): it carries the deferred identifiers AND a decrementing poll count,
// and this adapter remembers nothing between calls. So the caller MUST persist the ref carried by
// EACH Pending and poll the NEWEST one. Re-polling a stale ref returns the same Pending forever --
// it is a pure function of the handle it was given, not a subscription that ages.
//
// The rejected alternative was a wall-clock deadline baked into the ref, which would converge
// regardless of what the caller persisted. It was refused because it makes the outcome
// TIME-dependent: the same ref would return Pending or Accepted depending on when it was polled,
// which breaks Core AC-2's determinism (a worker restarted between polls, or a second replica,
// must converge identically).
//
// This contract belongs HERE and not on Ref: Ref is declared in adapter.go and belongs to the
// generic M5-02 seam that every future adapter's handle uses. The decrementing counter is the
// MOCK's design, not the seam's.
//
// [poll-consumes-one-then-tests]: `remaining := n - 1` is computed FIRST -- this call consumes one
// -- and convergence tests `remaining <= 0`. n means "polls still required, INCLUDING this one",
// so the first ref (n = mockPendingPolls = 2) converges on the SECOND poll, which is AC-2's "twice
// in total".
func (a *MockAdapter) Poll(ctx context.Context, ref Ref) (Result, Evidence) {
	// STEP 0, BEFORE the ctx check, exactly as Submit does: the headers describe the call we were
	// ABOUT to make. mockRequestHeaders("") stamps Content-Type, Accept and User-Agent and, BY
	// CONSTRUCTION, no Idempotency-Key -- which is what keeps L13 satisfied for the contract
	// suite's two Poll drives (contract_test.go:304 and :340, both with idemKey "").
	start := time.Now()
	ev := Evidence{RequestHeaders: mockRequestHeaders("")}

	// STEP 1 (L16), BEFORE the decode. The ONE row of Poll's matrix where LatencyMS stays nil:
	// nothing ran, so nothing was measured. The ordering is OBSERVABLE -- contract_test.go:340
	// drives a cancelled ctx AND an unissued ref, so both orderings return Retryable and only the
	// nil latency (and the ctx error rather than ErrMockUnknownRef) tells them apart.
	if err := ctx.Err(); err != nil {
		return Retryable{Err: err}, ev
	}

	// STEP 2. The decoder's error travels UNWRAPPED: it already wraps ErrMockUnknownRef and already
	// carries exactly one "submission: " prefix, so a second fmt.Errorf would double it (the defect
	// QA fixed in M5-03-01). No re-validation of n or the IRN either -- decodeMockRef owns those
	// invariants ([ref-enforces-its-own-invariants]), which is why its guard is `n < 0` and a
	// hand-built n = 0 ref is legal and converges immediately. ReachedWire stays false: a handle we
	// cannot READ is a call we never MADE.
	n, ids, err := decodeMockRef(ref)
	if err != nil {
		ev.LatencyMS = mockElapsedMS(start)
		return Retryable{Err: err}, ev
	}

	// STEP 3. ReachedWire goes true BEFORE the wait ([two-phase-wire]), so a context that dies
	// mid-flight reports "sent". The wait is mockWait with the BASELINE directly, never
	// mockInFlight: a ref carries no trigger, so there is nothing to multiply.
	ev.ReachedWire = true
	if err := mockWait(ctx, a.cfg.Latency); err != nil {
		ev.LatencyMS = mockElapsedMS(start)
		return Retryable{Err: err}, ev
	}

	// STEP 4 -- synthesize the response. RequestBody is left nil on EVERY path above and below: a
	// poll is GET /submissions/{ref} and there is no document to send. exchange.go:213-221
	// preserves nil as SQL NULL distinctly from "" as an empty string, so `request_body IS NULL` on
	// a poll row IS the evidence that we asked without transmitting anything.
	respHeaders := http.Header{}
	respHeaders.Set("Content-Type", mockContentTypeJSON)

	var (
		result Result
		status int
		body   string
	)
	if remaining := n - 1; remaining > 0 {
		// The SAME new ref goes into the Pending and into the archived 202 body: the caller who
		// polls and the caller who reads the archive must not see two different handles. The
		// identifiers are RE-ENCODED unchanged, never regenerated -- Poll has no wire to
		// regenerate them from.
		next := encodeMockRef(remaining, ids)
		status, body = http.StatusAccepted, mockPendingBody(next)
		result = Pending{Ref: next, PollAfter: time.Now().Add(mockPollBackoff)}
	} else {
		// Convergence. The verdict is READ OUT of the ref, never recomputed.
		status, body = http.StatusOK, mockAcceptedBody(ids)
		result = Accepted{IRN: ids.IRN, CSID: ids.CSID, QRPayload: ids.QRPayload}
	}

	ev.HTTPStatus = mockIntPtr(status)
	ev.ResponseBody = &body
	ev.ResponseHeaders = respHeaders
	ev.LatencyMS = mockElapsedMS(start)
	return result, ev
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
