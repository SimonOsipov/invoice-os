// mock_adapter_test.go: M5-03-03 (task-226) RED specs (QA Mode A) for MockAdapter's identity,
// its two-phase Submit, the ten-row evidence matrix, the request/response header record, the
// latency model and -- above all -- the mid-flight cancellation oracle.
//
// What this file proves, once green:
//
//   - that Name() and Version() are CONSTANTS no MockConfig can leak into (L02), and that
//     Version() specifically carries no rendered duration (S1);
//   - that an already-cancelled or expired context short-circuits Submit before anything else
//     runs, while a LIVE context on the same wire reaches the accept path -- the positive
//     control without which an always-Retryable implementation passes (S2);
//   - that an empty or non-JSON Wire is Retryable and never Rejected, with the request body
//     recorded only when there actually were bytes (S3);
//   - that each of the seven allocated triggers, plus the unallocated and absent-TIN default
//     paths, produces its documented Result, HTTP status and response-body code (S4/S5);
//   - that Evidence.ReachedWire is false for EXACTLY the pre-cancelled, unparseable and
//     connection rows, and that every such row carries nil status, nil response body and no
//     response headers -- checked directly AND through the shipped CheckEvidence (S6/S7/S8);
//   - that the four request headers are always recorded, that the Idempotency-Key appears if and
//     only if a key was passed, and that every header the adapter records survives ScrubHeaders
//     unchanged -- i.e. every name it chose is on exchange.go's 12-name allowlist (S9/S10);
//   - that LatencyMS is measured on every path past the context check and stays nil on the one
//     row the matrix says it must (S11);
//   - that the latency baseline is really applied and that the slow/timeout MULTIPLIERS are
//     really executed -- at MockConfig{} both multiply to zero, so without S13 nothing in this
//     suite ever runs mockSlowFactor or mockTimeoutFactor and they could hold any value
//     (S12/S13);
//   - that two separately constructed adapters agree byte for byte on the same wire, and that a
//     DIFFERENT wire produces a different body -- the negative control without which an adapter
//     returning one constant body passes (S14/S15);
//   - and that a context cancelled DURING the in-flight wait aborts that wait: Retryable,
//     ReachedWire TRUE, and back in materially less than the configured duration (S17), which
//     ExchangeFor must record as "sent", not "connection_failed" (S18).
//
// S17 IS THE ENTIRE ORACLE FOR QA-VERIFY FINDING F1. A `time.Sleep(d)` implementation of
// mockWait returns exactly the same Retryable that a correct `select` does -- it just takes the
// full 5 seconds to do it. The elapsed-time bound is the only assertion in this file that can
// tell the two apart. Do not weaken it into a variant assertion, and do not "stabilise" it by
// raising it: at 5s latency / 250ms deadline / <2s bound it has a ~12x margin over the deadline
// and still fails a time.Sleep implementation by three seconds. See
// [cancellation-is-observable-in-the-wait].
//
// PACKAGE submission_test (EXTERNAL), deliberately unlike the two in-package files next door
// (mock_wire_test.go, mock_script_test.go). This subtask ships the EXPORTED MockAdapter seam and
// every spec below was checked to need only exported symbols plus CheckResult / CheckEvidence /
// lawRecorder / strPtr / intPtr, which already live in this package
// (contract_test.go:108,475,530; contract_red_test.go:117). Story decision
// [test-package-follows-the-symbol]. strPtr and intPtr are USED here and NOT redeclared.
//
// RED STATE AT AUTHORING TIME: mock_adapter.go ships the full declaration set with real constant
// values and no-op bodies for Submit, Poll, mockWait, mockInFlight and mockRequestHeaders, so
// every failure below is an ASSERTION failure, not a compile error. Two honest exceptions,
// labelled at their own sites: S1 is GREEN from the start because Name/Version/Transform are
// one-liners that are already implemented (their specs would otherwise be untestable), and S16
// is a GUARD rather than a red-first spec -- an always-Retryable implementation passes its
// no-panic half, and its teeth are the Wire("null") / Wire("{}") rows that must be Accepted.
//
// Helper prefix `ma`, mirroring mock_wire_test.go's `mw` and mock_script_test.go's `ms`.
// Standard library only -- no testify. No TestMain: exactly one exists
// (failure_modes_test.go:57) and both packages in this directory build into ONE test binary. No
// t.Skip anywhere: internal/tools/rlsgate/rlsgate.go fails the CI queue job on any test-level
// skip, and nothing here needs a database, a network or an injected clock.
package submission_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// ---------------------------------------------------------------------------------------
// Retyped literals.
//
// The trigger TINs, the NGE- response codes, the Ref prefix, the reject path and the
// Retry-After / pollAfter seconds are all UNEXPORTED in package submission (mock_script.go:73-145
// and mock_adapter.go's const block), so an external test package cannot reach the symbols and
// has to retype the values. That is safe in this direction: a drifted TIN simply stops matching
// mockTriggerFor's exact-match table and the spec fails LOUDLY on the wrong Result, rather than
// passing vacuously. If one of these ever fails for no visible reason, diff it against
// mock_script.go / mock_adapter.go before touching the assertion.
// ---------------------------------------------------------------------------------------

const (
	maTINAccept      = "99999999-0001" // mock_script.go:76
	maTINReject      = "99999999-0002"
	maTINPending     = "99999999-0003"
	maTINUnavailable = "99999999-0004"
	maTINSlow        = "99999999-0005"
	maTINTimeout     = "99999999-0006"
	maTINConnection  = "99999999-0007"

	// maTINUnallocated is inside the reserved block but deliberately never given a trigger
	// (mock_script.go:89-92 mockNeverAllocate) -- it must take the ordinary accept path
	// ([non-reserved-defaults-to-accept]).
	maTINUnallocated = "99999999-0008"
	// maTINOrdinary is outside the reserved block entirely.
	maTINOrdinary = "12345678-9012"

	maCodeAccepted    = "NGE-2000" // mock_script.go:119
	maCodePending     = "NGE-2020"
	maCodeRejected    = "NGE-4102"
	maCodeUnavailable = "NGE-5030"

	maRefPrefix   = "mockapp-v1."            // mock_script.go:136
	maRejectPath  = "buyer.tin"              // mock_script.go:128 -- OURS, on Reason.Path
	maRejectField = "customer.taxIdentifier" // mock_script.go:130 -- THEIRS, only in the 422 body

	maContentTypeJSON = "application/json" // mock_adapter.go mockContentTypeJSON
	maUserAgent       = "FiscalBridge-MockAPP/v1"
	maRetryAfter      = "30" // mock_adapter.go mockRetryAfterSeconds
	maSlowFactor      = 4    // mock_adapter.go mockSlowFactor
	maTimeoutFactor   = 8    // mock_adapter.go mockTimeoutFactor
)

// ---------------------------------------------------------------------------------------
// Shared helpers.
// ---------------------------------------------------------------------------------------

// maCanonical builds a fully-populated canonical whose BUYER TIN is the trigger channel
// ([trigger-read-from-the-real-bis-field]). invoiceNumber varies so S14's negative control can
// produce a genuinely different wire without touching the trigger.
func maCanonical(buyerTIN, invoiceNumber string) submission.Canonical {
	issue := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	c := submission.Canonical{
		InvoiceID:     "inv-mock-1",
		InvoiceNumber: invoiceNumber,
		IssueDate:     &issue,
		Supplier:      submission.Party{TIN: strPtr("11111111-2222"), Name: strPtr("Supplier Co")},
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("1000.00"),
		VAT:           strPtr("75.00"),
		Total:         strPtr("1075.00"),
		Lines: []submission.CanonicalLine{{
			LineID:      "line-1",
			LineNo:      1,
			Description: strPtr("Widget"),
			Quantity:    strPtr("2"),
			UnitPrice:   strPtr("500.00"),
			LineTotal:   strPtr("1000.00"),
			LineTax:     strPtr("75.00"),
		}},
	}
	// "" means "no buyer TIN at all" -- a nil *string, so the whole PartyTaxScheme block is
	// omitted from the wire rather than carrying an empty CompanyID.
	if buyerTIN != "" {
		c.Buyer = submission.Party{TIN: strPtr(buyerTIN), Name: strPtr("Buyer Ltd")}
	} else {
		c.Buyer = submission.Party{Name: strPtr("Buyer Ltd")}
	}
	return c
}

// maWire transforms a canonical carrying buyerTIN into the wire bytes Submit reads the trigger
// back out of. It goes through the REAL Transform rather than hand-building JSON, so the trigger
// round-trip this story rests on is exercised end to end.
func maWire(t *testing.T, a *submission.MockAdapter, buyerTIN string) submission.Wire {
	t.Helper()
	return maWireFor(t, a, buyerTIN, "INV-MOCK-0001")
}

func maWireFor(t *testing.T, a *submission.MockAdapter, buyerTIN, invoiceNumber string) submission.Wire {
	t.Helper()
	w, err := a.Transform(context.Background(), maCanonical(buyerTIN, invoiceNumber))
	if err != nil {
		t.Fatalf("Transform(buyerTIN=%q, invoiceNumber=%q) failed: %v", buyerTIN, invoiceNumber, err)
	}
	if len(w) == 0 {
		t.Fatalf("Transform(buyerTIN=%q) returned an empty Wire with a nil error (L05)", buyerTIN)
	}
	return w
}

// maSubmit calls Submit under a recover, so a panicking implementation is reported as a test
// FAILURE at the exact spec that provoked it rather than tearing down the whole binary.
func maSubmit(t *testing.T, a *submission.MockAdapter, ctx context.Context, w submission.Wire, idemKey string) (r submission.Result, ev submission.Evidence) {
	t.Helper()
	defer func() {
		if rec := recover(); rec != nil {
			t.Errorf("Submit panicked: %v", rec)
		}
	}()
	r, ev = a.Submit(ctx, w, idemKey)
	return
}

func maAccepted(t *testing.T, label string, r submission.Result) submission.Accepted {
	t.Helper()
	v, ok := r.(submission.Accepted)
	if !ok {
		t.Fatalf("%s: Result is %T (%v), want submission.Accepted", label, r, r)
	}
	return v
}

func maRetryable(t *testing.T, label string, r submission.Result) submission.Retryable {
	t.Helper()
	v, ok := r.(submission.Retryable)
	if !ok {
		t.Fatalf("%s: Result is %T (%v), want submission.Retryable", label, r, r)
	}
	if v.Err == nil {
		t.Fatalf("%s: Retryable.Err is nil (L10)", label)
	}
	return v
}

// maBody decodes the synthesized response body into a map and asserts on FIELDS.
//
// NEVER compare a retyped literal body: mock_script.go builds every body as a map[string]any, so
// encoding/json emits its keys SORTED ALPHABETICALLY rather than in declaration order, and the
// story's own body literals are stale against what M5-03-02 actually shipped (three of the four
// messages differ). Field-level assertions are immune to both.
func maBody(t *testing.T, label string, ev submission.Evidence) map[string]any {
	t.Helper()
	if ev.ResponseBody == nil {
		t.Fatalf("%s: Evidence.ResponseBody is nil, want a synthesized body", label)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(*ev.ResponseBody), &m); err != nil {
		t.Fatalf("%s: response body is not JSON: %v\nbody: %s", label, err, *ev.ResponseBody)
	}
	return m
}

// maField reads a dotted path out of a decoded body, failing rather than panicking on a missing
// or wrongly-typed node.
func maField(t *testing.T, label string, m map[string]any, path ...string) string {
	t.Helper()
	cur := any(m)
	for i, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s: %v is not an object at segment %q", label, path[:i], key)
		}
		cur, ok = obj[key]
		if !ok {
			t.Fatalf("%s: response body has no %v", label, path[:i+1])
		}
	}
	s, ok := cur.(string)
	if !ok {
		t.Fatalf("%s: %v is %T, want a string", label, path, cur)
	}
	return s
}

// maHeaderPresent reports whether h carries name, matching by canonicalising each stored key --
// the same reasoning ScrubHeaders and idempotencyKeyValue use, so a header stored through a raw
// map literal cannot hide from this check.
func maHeaderPresent(h http.Header, name string) bool {
	want := http.CanonicalHeaderKey(name)
	for k := range h {
		if http.CanonicalHeaderKey(k) == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------------------
// S1 (AC-1) -- identity.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_NameAndVersionAreConstantsIgnoringConfig (S1, AC-1).
//
// HONEST LABEL: this spec is GREEN from the moment it compiles. Name, Version and Transform are
// one-liners that mock_adapter.go implements at authoring time on purpose -- stubbing them would
// make their own specs untestable and would break every other spec's wire-building helper. It is
// still worth pinning: it is RED against the plausible mistake `return "v1-"+a.cfg.Latency.String()`,
// which L02 (contract_test.go:241) would also catch but only once M5-03-05 runs the mock through
// the contract suite.
//
// The assertion is deliberately NOT "Version() contains no digits", as the story's table
// originally had it: the version IS "v1", which contains a digit, so that assertion would fail a
// correct implementation. What must not appear is a rendered duration.
func TestMockAdapter_NameAndVersionAreConstantsIgnoringConfig(t *testing.T) {
	plain := submission.NewMockAdapter(submission.MockConfig{})
	configured := submission.NewMockAdapter(submission.MockConfig{Latency: 5 * time.Second})

	for _, a := range []struct {
		label string
		a     *submission.MockAdapter
	}{{"MockConfig{}", plain}, {"MockConfig{Latency:5s}", configured}} {
		if got := a.a.Name(); got != "mock" {
			t.Errorf("%s: Name() = %q, want %q", a.label, got, "mock")
		}
		if a.a.Version() == "" {
			t.Errorf("%s: Version() is empty (L01)", a.label)
		}
	}

	if plain.Name() != configured.Name() {
		t.Errorf("Name() differs across two differently-configured adapters: %q vs %q (L02)",
			plain.Name(), configured.Name())
	}
	if plain.Version() != configured.Version() {
		t.Errorf("Version() differs across two differently-configured adapters: %q vs %q (L02)",
			plain.Version(), configured.Version())
	}

	// The specific leak this guards: a Version() derived from MockConfig.Latency. 5*time.Second
	// renders as "5s" via Duration.String and as "5000" via Milliseconds; neither may appear.
	for _, bad := range []string{"5s", "5000"} {
		if strings.Contains(configured.Version(), bad) {
			t.Errorf("Version() = %q contains %q -- the configured latency has leaked into the "+
				"version string, which L02 pins across freshly constructed instances",
				configured.Version(), bad)
		}
	}
}

// ---------------------------------------------------------------------------------------
// S2 (AC-2) -- the context check runs FIRST.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_SubmitHonoursCancelledContextFirst (S2, AC-2, RED-FIRST).
//
// Both a cancelled and an EXPIRED context are exercised -- the AC names both, and an
// implementation that tested ctx.Done() instead of ctx.Err() would behave differently on a
// deadline that has already passed.
//
// The LIVE-CONTEXT POSITIVE CONTROL is mandatory: without it, an implementation that returns
// Retryable unconditionally passes every assertion here. The control proves that the SAME wire
// on a live context does reach the accept path, so the short-circuit is genuinely attributable
// to the context.
func TestMockAdapter_SubmitHonoursCancelledContextFirst(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	w := maWire(t, a, maTINAccept)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancelExpired()

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"already-cancelled", cancelled},
		{"already-expired", expired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ev := maSubmit(t, a, tc.ctx, w, "idem-"+tc.name)
			maRetryable(t, tc.name, r)

			if ev.ReachedWire {
				t.Errorf("%s: Evidence.ReachedWire = true, want false -- the ctx check runs before "+
					"the connect phase (L16, [two-phase-wire])", tc.name)
			}
			if ev.HTTPStatus != nil {
				t.Errorf("%s: Evidence.HTTPStatus = %d, want nil -- the accept path must not have run",
					tc.name, *ev.HTTPStatus)
			}
			if ev.ResponseBody != nil {
				t.Errorf("%s: Evidence.ResponseBody = %q, want nil", tc.name, *ev.ResponseBody)
			}
			if ev.RequestBody != nil {
				t.Errorf("%s: Evidence.RequestBody = %q, want nil -- nothing was captured on the "+
					"pre-cancelled row of the evidence matrix", tc.name, *ev.RequestBody)
			}
			if len(ev.ResponseHeaders) != 0 {
				t.Errorf("%s: Evidence.ResponseHeaders = %v, want empty (L11)", tc.name, ev.ResponseHeaders)
			}
		})
	}

	// POSITIVE CONTROL. Without this an always-Retryable Submit passes the two rows above.
	t.Run("live-context-control", func(t *testing.T) {
		r, ev := maSubmit(t, a, context.Background(), w, "idem-live")
		acc := maAccepted(t, "live-context-control", r)
		if acc.IRN == "" {
			t.Errorf("live-context-control: Accepted.IRN is empty (L07)")
		}
		if !ev.ReachedWire {
			t.Errorf("live-context-control: Evidence.ReachedWire = false, want true")
		}
	})
}

// ---------------------------------------------------------------------------------------
// S3 (AC-3) -- an unparseable wire is Retryable, never Rejected.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_SubmitRejectsUnparseableWire (S3, AC-3, RED-FIRST).
//
// [unparseable-wire]: this path is REACHED by the shipped contract suite -- RunAdapterContract
// calls Submit with submission.Wire{} (contract_test.go:291) and with the non-JSON bytes
// "contract-suite-cancelled-ctx-wire" (line 314) -- so it must be specified. It is not an
// authority verdict, so [errors-never-verdicts] forbids Rejected.
//
// The wrapped sentinel matters: errors.Is must reach ErrMockUnparseableWire while the decoder's
// own reason survives inside the message, because that message is what lands in the M5-07
// archive (mock_wire.go:226).
//
// The ACCEPT CONTROL is mandatory: without it an always-Retryable implementation passes.
func TestMockAdapter_SubmitRejectsUnparseableWire(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	for _, tc := range []struct {
		name        string
		wire        submission.Wire
		wantReqBody bool // never-captured is not the same evidence as empty (exchange.go:213-221)
	}{
		{"empty-wire", submission.Wire{}, false},
		{"nil-wire", nil, false},
		{"non-json", submission.Wire("not json"), true},
		{"truncated-json", submission.Wire(`{"ID":`), true},
		{"invalid-utf8", submission.Wire([]byte{0xff, 0xfe, 0xfd}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ev := maSubmit(t, a, ctx, tc.wire, "idem-"+tc.name)

			if _, isRejected := r.(submission.Rejected); isRejected {
				t.Fatalf("%s: Result is Rejected -- an unparseable wire is a transport failure, "+
					"never an authority verdict ([unparseable-wire], [errors-never-verdicts])", tc.name)
			}
			rt := maRetryable(t, tc.name, r)
			if !errors.Is(rt.Err, submission.ErrMockUnparseableWire) {
				t.Errorf("%s: Retryable.Err = %v, want it to wrap ErrMockUnparseableWire", tc.name, rt.Err)
			}

			if ev.ReachedWire {
				t.Errorf("%s: Evidence.ReachedWire = true, want false -- the parse happens before "+
					"the connect phase", tc.name)
			}
			if ev.HTTPStatus != nil {
				t.Errorf("%s: Evidence.HTTPStatus = %d, want nil (L11)", tc.name, *ev.HTTPStatus)
			}
			if ev.ResponseBody != nil {
				t.Errorf("%s: Evidence.ResponseBody = %q, want nil (L11)", tc.name, *ev.ResponseBody)
			}
			if got := ev.RequestBody != nil; got != tc.wantReqBody {
				t.Errorf("%s: (Evidence.RequestBody != nil) = %t, want %t -- a wire with no bytes "+
					"leaves the request body NIL, which is different evidence from an empty string",
					tc.name, got, tc.wantReqBody)
			}
		})
	}

	// ACCEPT CONTROL. Without this an always-Retryable Submit passes every row above.
	t.Run("parseable-wire-control", func(t *testing.T) {
		r, _ := maSubmit(t, a, ctx, maWire(t, a, maTINAccept), "idem-control")
		maAccepted(t, "parseable-wire-control", r)
	})
}

// ---------------------------------------------------------------------------------------
// S4/S5 (AC-4) -- the scripted outcome table.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_SubmitScriptedOutcomes (S4, AC-4, RED-FIRST): one row per allocated trigger,
// plus the three paths that must all fall through to accept -- an unallocated reserved suffix, a
// TIN outside the block entirely, and no buyer TIN at all ([non-reserved-defaults-to-accept]).
//
// Response bodies are asserted by FIELD, never by comparing a retyped literal: the shipped
// bodies are map[string]any, so their keys come out alphabetically sorted, and the story's own
// literals are stale (see maBody).
func TestMockAdapter_SubmitScriptedOutcomes(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	cases := []struct {
		name            string
		tin             string
		wantStatus      *int
		wantCode        string // "" when there is no response body at all
		wantStatusField string
		check           func(t *testing.T, r submission.Result, ev submission.Evidence)
	}{
		{
			name: "accept", tin: maTINAccept, wantStatus: intPtr(200),
			wantCode: maCodeAccepted, wantStatusField: "ACCEPTED",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				maAccepted(t, "accept", r)
			},
		},
		{
			name: "reject", tin: maTINReject, wantStatus: intPtr(422),
			wantCode: maCodeRejected, wantStatusField: "REJECTED",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				rej, ok := r.(submission.Rejected)
				if !ok {
					t.Fatalf("reject: Result is %T, want submission.Rejected", r)
				}
				if len(rej.Reasons) != 1 {
					t.Fatalf("reject: %d Reasons, want exactly 1 ([one-rejection-reason])", len(rej.Reasons))
				}
				if rej.Reasons[0].Code != maCodeRejected {
					t.Errorf("reject: Reason.Code = %q, want %q", rej.Reasons[0].Code, maCodeRejected)
				}
				if rej.Reasons[0].Message == "" {
					t.Errorf("reject: Reason.Message is empty (L08)")
				}
				// [their-field-our-path]: the Reason we hand upward names the field in OUR
				// vocabulary; the body names it in THEIRS. The asymmetry is the whole exercise.
				if rej.Reasons[0].Path != maRejectPath {
					t.Errorf("reject: Reason.Path = %q, want %q", rej.Reasons[0].Path, maRejectPath)
				}
				if body := *ev.ResponseBody; !strings.Contains(body, maRejectField) {
					t.Errorf("reject: 422 body does not name %q (the APP's own field vocabulary): %s",
						maRejectField, body)
				}
				if body := *ev.ResponseBody; strings.Contains(body, maRejectPath) {
					t.Errorf("reject: 422 body leaks OUR dotted path %q -- the body speaks the APP's "+
						"vocabulary only ([their-field-our-path]): %s", maRejectPath, body)
				}
			},
		},
		{
			name: "pending", tin: maTINPending, wantStatus: intPtr(202),
			wantCode: maCodePending, wantStatusField: "PENDING",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				p, ok := r.(submission.Pending)
				if !ok {
					t.Fatalf("pending: Result is %T, want submission.Pending", r)
				}
				if !strings.HasPrefix(string(p.Ref), maRefPrefix) {
					t.Errorf("pending: Ref = %q, want the %q prefix ([ref-carries-the-verdict])",
						p.Ref, maRefPrefix)
				}
				if p.PollAfter.IsZero() {
					t.Errorf("pending: PollAfter is zero (L09)")
				}
				// The 202 body must carry the SAME ref the Pending does -- the caller who reads
				// the archive and the caller who polls must not see two different handles.
				if got := maField(t, "pending", maBody(t, "pending", ev), "data", "reference"); got != string(p.Ref) {
					t.Errorf("pending: 202 body data.reference = %q, want the Pending.Ref %q", got, p.Ref)
				}
			},
		},
		{
			name: "unavailable", tin: maTINUnavailable, wantStatus: intPtr(503),
			wantCode: maCodeUnavailable, wantStatusField: "ERROR",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				rt := maRetryable(t, "unavailable", r)
				if !errors.Is(rt.Err, submission.ErrMockUnavailable) {
					t.Errorf("unavailable: Retryable.Err = %v, want it to wrap ErrMockUnavailable", rt.Err)
				}
				if got := ev.ResponseHeaders.Get("Retry-After"); got != maRetryAfter {
					t.Errorf("unavailable: response Retry-After = %q, want %q", got, maRetryAfter)
				}
			},
		},
		{
			// At MockConfig{} slow is INDISTINGUISHABLE from accept -- same Accepted, same 200,
			// byte-identical body -- and that is correct: what makes it slow is its duration, and
			// 4 x 0 is 0. S13 is the spec that executes the multiplier.
			name: "slow", tin: maTINSlow, wantStatus: intPtr(200),
			wantCode: maCodeAccepted, wantStatusField: "ACCEPTED",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				maAccepted(t, "slow", r)
			},
		},
		{
			name: "timeout", tin: maTINTimeout, wantStatus: nil, wantCode: "",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				rt := maRetryable(t, "timeout", r)
				if !errors.Is(rt.Err, submission.ErrMockTimeout) {
					t.Errorf("timeout: Retryable.Err = %v, want it to wrap ErrMockTimeout", rt.Err)
				}
				if !ev.ReachedWire {
					t.Errorf("timeout: ReachedWire = false, want true -- a timeout happens IN FLIGHT, " +
						"after the bytes left the process ([two-phase-wire])")
				}
			},
		},
		{
			name: "connection", tin: maTINConnection, wantStatus: nil, wantCode: "",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				rt := maRetryable(t, "connection", r)
				if !errors.Is(rt.Err, submission.ErrMockConnectionRefused) {
					t.Errorf("connection: Retryable.Err = %v, want it to wrap ErrMockConnectionRefused", rt.Err)
				}
				if ev.ReachedWire {
					t.Errorf("connection: ReachedWire = true, want false -- the connect phase " +
						"short-circuits before anything leaves the process ([two-phase-wire])")
				}
			},
		},
		{
			name: "unallocated-reserved-suffix", tin: maTINUnallocated, wantStatus: intPtr(200),
			wantCode: maCodeAccepted, wantStatusField: "ACCEPTED",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				maAccepted(t, "unallocated-reserved-suffix", r)
			},
		},
		{
			name: "outside-the-reserved-block", tin: maTINOrdinary, wantStatus: intPtr(200),
			wantCode: maCodeAccepted, wantStatusField: "ACCEPTED",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				maAccepted(t, "outside-the-reserved-block", r)
			},
		},
		{
			name: "no-buyer-tin-at-all", tin: "", wantStatus: intPtr(200),
			wantCode: maCodeAccepted, wantStatusField: "ACCEPTED",
			check: func(t *testing.T, r submission.Result, ev submission.Evidence) {
				maAccepted(t, "no-buyer-tin-at-all", r)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ev := maSubmit(t, a, ctx, maWire(t, a, tc.tin), "idem-"+tc.name)

			switch {
			case tc.wantStatus == nil && ev.HTTPStatus != nil:
				t.Errorf("%s: HTTPStatus = %d, want nil -- no response was synthesized on this path",
					tc.name, *ev.HTTPStatus)
			case tc.wantStatus != nil && ev.HTTPStatus == nil:
				t.Errorf("%s: HTTPStatus is nil, want %d", tc.name, *tc.wantStatus)
			case tc.wantStatus != nil && *ev.HTTPStatus != *tc.wantStatus:
				t.Errorf("%s: HTTPStatus = %d, want %d", tc.name, *ev.HTTPStatus, *tc.wantStatus)
			}

			if tc.wantCode != "" {
				body := maBody(t, tc.name, ev)
				if got := maField(t, tc.name, body, "code"); got != tc.wantCode {
					t.Errorf("%s: response body code = %q, want %q", tc.name, got, tc.wantCode)
				}
				if got := maField(t, tc.name, body, "status"); got != tc.wantStatusField {
					t.Errorf("%s: response body status = %q, want %q", tc.name, got, tc.wantStatusField)
				}
				if got := maField(t, tc.name, body, "message"); strings.TrimSpace(got) == "" {
					t.Errorf("%s: response body message is blank -- the archive must carry something "+
						"a human can read", tc.name)
				}
				if got := ev.ResponseHeaders.Get("Content-Type"); got != maContentTypeJSON {
					t.Errorf("%s: response Content-Type = %q, want %q", tc.name, got, maContentTypeJSON)
				}
			} else if ev.ResponseBody != nil {
				t.Errorf("%s: ResponseBody = %q, want nil -- no response came back on this path",
					tc.name, *ev.ResponseBody)
			}

			tc.check(t, r, ev)
		})
	}
}

// TestMockAdapter_AcceptCarriesAllThreeIdentifiers (S5, AC-4, RED-FIRST).
//
// "All three non-blank" on its own is passed by three unrelated constants, so the CROSS-CHECK
// against the decoded 200 body is what gives this spec teeth: the identifiers the caller gets
// back must be the SAME strings the archived response body reports, or the M5-07 archive and
// invoices.irn/csid/qr_payload would tell two different stories about the same clearance.
func TestMockAdapter_AcceptCarriesAllThreeIdentifiers(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	r, ev := maSubmit(t, a, context.Background(), maWire(t, a, maTINOrdinary), "idem-ids")
	acc := maAccepted(t, "accept", r)

	for _, f := range []struct {
		name  string
		value string
	}{{"IRN", acc.IRN}, {"CSID", acc.CSID}, {"QRPayload", acc.QRPayload}} {
		if strings.TrimSpace(f.value) == "" {
			t.Errorf("Accepted.%s = %q, want non-blank", f.name, f.value)
		}
	}

	body := maBody(t, "accept", ev)
	for _, f := range []struct {
		name string
		path []string
		want string
	}{
		{"IRN", []string{"data", "irn"}, acc.IRN},
		{"CSID", []string{"data", "csid"}, acc.CSID},
		{"QRPayload", []string{"data", "qr"}, acc.QRPayload},
	} {
		if got := maField(t, "accept", body, f.path...); got != f.want {
			t.Errorf("200 body %v = %q, want the Accepted.%s %q -- the archived body and the "+
				"returned Result must agree", f.path, got, f.name, f.want)
		}
	}
}

// ---------------------------------------------------------------------------------------
// S6/S7/S8 (AC-5) -- the evidence matrix.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_EvidenceMatrix (S6, AC-5, RED-FIRST): the full matrix from task-226's plan, one
// row per path, asserted field by field. ReachedWire is false for EXACTLY three paths --
// pre-cancelled, unparseable and connection -- and every one of them carries nil HTTPStatus, nil
// ResponseBody and no ResponseHeaders (L11).
//
// Each row is ALSO run through the shipped CheckEvidence with a lawRecorder, so a row that
// satisfies this table but violates L11/L12/L13 is caught by the contract suite's own code
// rather than by a paraphrase of it.
func TestMockAdapter_EvidenceMatrix(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	type row struct {
		name         string
		tin          string          // used unless rawWire is non-nil
		rawWire      submission.Wire // a wire that is NOT a transform output
		ctx          context.Context
		wantReached  bool
		wantStatus   *int
		wantReqBody  bool
		wantRespBody bool
		wantRespHdrs []string // empty means ResponseHeaders must be empty
		wantLatency  bool
	}

	rows := []row{
		{name: "pre-cancelled", tin: maTINAccept, ctx: cancelled,
			wantReached: false, wantStatus: nil, wantReqBody: false, wantRespBody: false,
			wantRespHdrs: nil, wantLatency: false},
		{name: "unparseable-empty", rawWire: submission.Wire{}, ctx: context.Background(),
			wantReached: false, wantStatus: nil, wantReqBody: false, wantRespBody: false,
			wantRespHdrs: nil, wantLatency: true},
		{name: "unparseable-non-json", rawWire: submission.Wire("not json"), ctx: context.Background(),
			wantReached: false, wantStatus: nil, wantReqBody: true, wantRespBody: false,
			wantRespHdrs: nil, wantLatency: true},
		{name: "connection", tin: maTINConnection, ctx: context.Background(),
			wantReached: false, wantStatus: nil, wantReqBody: true, wantRespBody: false,
			wantRespHdrs: nil, wantLatency: true},
		{name: "timeout", tin: maTINTimeout, ctx: context.Background(),
			wantReached: true, wantStatus: nil, wantReqBody: true, wantRespBody: false,
			wantRespHdrs: nil, wantLatency: true},
		{name: "slow", tin: maTINSlow, ctx: context.Background(),
			wantReached: true, wantStatus: intPtr(200), wantReqBody: true, wantRespBody: true,
			wantRespHdrs: []string{"Content-Type"}, wantLatency: true},
		{name: "pending", tin: maTINPending, ctx: context.Background(),
			wantReached: true, wantStatus: intPtr(202), wantReqBody: true, wantRespBody: true,
			wantRespHdrs: []string{"Content-Type"}, wantLatency: true},
		{name: "reject", tin: maTINReject, ctx: context.Background(),
			wantReached: true, wantStatus: intPtr(422), wantReqBody: true, wantRespBody: true,
			wantRespHdrs: []string{"Content-Type"}, wantLatency: true},
		{name: "accept", tin: maTINAccept, ctx: context.Background(),
			wantReached: true, wantStatus: intPtr(200), wantReqBody: true, wantRespBody: true,
			wantRespHdrs: []string{"Content-Type"}, wantLatency: true},
		{name: "unallocated", tin: maTINUnallocated, ctx: context.Background(),
			wantReached: true, wantStatus: intPtr(200), wantReqBody: true, wantRespBody: true,
			wantRespHdrs: []string{"Content-Type"}, wantLatency: true},
		{name: "unavailable", tin: maTINUnavailable, ctx: context.Background(),
			wantReached: true, wantStatus: intPtr(503), wantReqBody: true, wantRespBody: true,
			wantRespHdrs: []string{"Content-Type", "Retry-After"}, wantLatency: true},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.rawWire
			if w == nil {
				w = maWire(t, a, tc.tin)
			}
			idemKey := "idem-" + tc.name
			_, ev := maSubmit(t, a, tc.ctx, w, idemKey)

			if ev.ReachedWire != tc.wantReached {
				t.Errorf("%s: ReachedWire = %t, want %t", tc.name, ev.ReachedWire, tc.wantReached)
			}
			switch {
			case tc.wantStatus == nil && ev.HTTPStatus != nil:
				t.Errorf("%s: HTTPStatus = %d, want nil", tc.name, *ev.HTTPStatus)
			case tc.wantStatus != nil && ev.HTTPStatus == nil:
				t.Errorf("%s: HTTPStatus is nil, want %d", tc.name, *tc.wantStatus)
			case tc.wantStatus != nil && *ev.HTTPStatus != *tc.wantStatus:
				t.Errorf("%s: HTTPStatus = %d, want %d", tc.name, *ev.HTTPStatus, *tc.wantStatus)
			}
			if got := ev.RequestBody != nil; got != tc.wantReqBody {
				t.Errorf("%s: (RequestBody != nil) = %t, want %t", tc.name, got, tc.wantReqBody)
			}
			if ev.RequestBody != nil && *ev.RequestBody != string(w) {
				t.Errorf("%s: RequestBody is not the wire verbatim:\n got %q\nwant %q",
					tc.name, *ev.RequestBody, string(w))
			}
			if got := ev.ResponseBody != nil; got != tc.wantRespBody {
				t.Errorf("%s: (ResponseBody != nil) = %t, want %t", tc.name, got, tc.wantRespBody)
			}
			if len(tc.wantRespHdrs) == 0 {
				if len(ev.ResponseHeaders) != 0 {
					t.Errorf("%s: ResponseHeaders = %v, want empty", tc.name, ev.ResponseHeaders)
				}
			} else {
				for _, name := range tc.wantRespHdrs {
					if !maHeaderPresent(ev.ResponseHeaders, name) {
						t.Errorf("%s: ResponseHeaders is missing %s: %v", tc.name, name, ev.ResponseHeaders)
					}
				}
			}
			if got := ev.LatencyMS != nil; got != tc.wantLatency {
				t.Errorf("%s: (LatencyMS != nil) = %t, want %t -- the pre-cancelled row is the ONLY "+
					"one that leaves it unmeasured", tc.name, got, tc.wantLatency)
			}

			// Cross-check against the SHIPPED law checks rather than a paraphrase of them.
			rec := &lawRecorder{}
			CheckEvidence(rec, tc.name, ev, idemKey)
			if len(rec.messages) != 0 {
				t.Errorf("%s: CheckEvidence recorded %d contract failure(s): %v",
					tc.name, len(rec.messages), rec.messages)
			}
		})
	}
}

// TestMockAdapter_TimeoutReachesWireWithNoResponse (S7, AC-5, RED-FIRST): the timeout trigger is
// the row that most easily gets confused with the connection one. It reports the bytes DID leave
// the process, so ExchangeFor must derive "sent" -- a timeout after transmission is exactly the
// case M5-01 kept the two outcomes apart for (exchange.go:143-149).
func TestMockAdapter_TimeoutReachesWireWithNoResponse(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	r, ev := maSubmit(t, a, context.Background(), maWire(t, a, maTINTimeout), "idem-timeout")

	rt := maRetryable(t, "timeout", r)
	if !errors.Is(rt.Err, submission.ErrMockTimeout) {
		t.Errorf("timeout: Retryable.Err = %v, want it to wrap ErrMockTimeout", rt.Err)
	}
	if !ev.ReachedWire {
		t.Errorf("timeout: ReachedWire = false, want true ([two-phase-wire])")
	}
	if ev.HTTPStatus != nil {
		t.Errorf("timeout: HTTPStatus = %d, want nil -- nothing came back", *ev.HTTPStatus)
	}
	if ev.ResponseBody != nil {
		t.Errorf("timeout: ResponseBody = %q, want nil", *ev.ResponseBody)
	}
	if got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev).Outcome; got != submission.OutcomeSent {
		t.Errorf("timeout: ExchangeFor(...).Outcome = %q, want %q", got, submission.OutcomeSent)
	}
}

// TestMockAdapter_ConnectionTriggerNeverLeavesTheProcess (S8, AC-5, RED-FIRST): the mirror of
// S7. The request body IS recorded (we built the bytes) but nothing was transmitted, so
// ReachedWire is false and ExchangeFor derives "connection_failed".
//
// The ACCEPT CONTRAST ROW is mandatory: without it an implementation that always reported
// connection_failed passes.
func TestMockAdapter_ConnectionTriggerNeverLeavesTheProcess(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	w := maWire(t, a, maTINConnection)
	r, ev := maSubmit(t, a, ctx, w, "idem-connection")

	rt := maRetryable(t, "connection", r)
	if !errors.Is(rt.Err, submission.ErrMockConnectionRefused) {
		t.Errorf("connection: Retryable.Err = %v, want it to wrap ErrMockConnectionRefused", rt.Err)
	}
	if ev.ReachedWire {
		t.Errorf("connection: ReachedWire = true, want false ([two-phase-wire])")
	}
	if ev.RequestBody == nil {
		t.Fatalf("connection: RequestBody is nil -- the bytes were built even though they never " +
			"left the process, and the archive must show what we were about to send")
	}
	if *ev.RequestBody != string(w) {
		t.Errorf("connection: RequestBody is not the wire verbatim:\n got %q\nwant %q", *ev.RequestBody, string(w))
	}
	if got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev).Outcome; got != submission.OutcomeConnectionFailed {
		t.Errorf("connection: ExchangeFor(...).Outcome = %q, want %q", got, submission.OutcomeConnectionFailed)
	}

	// CONTRAST ROW. Without this an always-connection_failed implementation passes.
	_, acceptEv := maSubmit(t, a, ctx, maWire(t, a, maTINAccept), "idem-accept")
	if got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", acceptEv).Outcome; got != submission.OutcomeSent {
		t.Errorf("accept contrast: ExchangeFor(...).Outcome = %q, want %q -- only the connection "+
			"trigger reports that nothing left the process", got, submission.OutcomeSent)
	}
}

// ---------------------------------------------------------------------------------------
// S9/S10 (AC-6) -- the request header record.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_RequestHeadersEchoIdempotencyKey (S9, AC-6, RED-FIRST): the three unconditional
// request headers appear on EVERY path, including the pre-cancelled one (the headers are built
// before the context check, or L13 would be vacuous for the contract suite's cancelled-Submit
// call at contract_test.go:314, which passes a real key).
//
// An empty idemKey must produce NO header at all rather than an empty-valued one:
// idempotencyKeyValue (contract_test.go:566) reports present=true for an empty value slice, so an
// empty-valued header would be indistinguishable from a real one and is meaningless evidence
// ([poll-sets-no-idempotency-key] rests on this).
func TestMockAdapter_RequestHeadersEchoIdempotencyKey(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	w := maWire(t, a, maTINAccept)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name    string
		ctx     context.Context
		idemKey string
	}{
		{"accept-path-with-key", context.Background(), "idem-abc"},
		{"accept-path-without-key", context.Background(), ""},
		{"pre-cancelled-with-key", cancelled, "idem-cancelled"},
		{"pre-cancelled-without-key", cancelled, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ev := maSubmit(t, a, tc.ctx, w, tc.idemKey)

			for _, h := range []struct{ name, want string }{
				{"Content-Type", maContentTypeJSON},
				{"Accept", maContentTypeJSON},
				{"User-Agent", maUserAgent},
			} {
				if got := ev.RequestHeaders.Get(h.name); got != h.want {
					t.Errorf("%s: RequestHeaders.Get(%q) = %q, want %q -- the three unconditional "+
						"request headers are recorded on EVERY path", tc.name, h.name, got, h.want)
				}
			}

			present := maHeaderPresent(ev.RequestHeaders, "Idempotency-Key")
			if tc.idemKey == "" {
				if present {
					t.Errorf("%s: Idempotency-Key is present with an empty key -- it must be ABSENT, "+
						"not empty-valued (%v)", tc.name, ev.RequestHeaders)
				}
				return
			}
			if !present {
				t.Fatalf("%s: Idempotency-Key is absent, want %q", tc.name, tc.idemKey)
			}
			if got := ev.RequestHeaders.Get("Idempotency-Key"); got != tc.idemKey {
				t.Errorf("%s: Idempotency-Key = %q, want %q (L13)", tc.name, got, tc.idemKey)
			}
		})
	}
}

// TestMockAdapter_RequestHeadersSurviveScrub (S10, AC-6, RED-FIRST): every header the adapter
// records must be drawn from exchange.go's 12-name write-time allowlist
// ([headers-from-the-scrub-allowlist]), or it would be silently absent from the
// customer-downloadable M5-07 archive.
//
// The assertion is DeepEqual(ScrubHeaders(h), h) rather than a per-name spot check: that is what
// catches a fourth, non-allowlisted header nobody thought to look for. It is run over the
// RESPONSE headers too -- Retry-After is on the list, and an invented response header would be
// lost just as quietly.
func TestMockAdapter_RequestHeadersSurviveScrub(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		tin  string
	}{
		{"accept", maTINAccept},
		{"reject", maTINReject},
		{"pending", maTINPending},
		{"unavailable", maTINUnavailable}, // the only row carrying Retry-After
		{"connection", maTINConnection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ev := maSubmit(t, a, ctx, maWire(t, a, tc.tin), "idem-"+tc.name)

			if !reflect.DeepEqual(submission.ScrubHeaders(ev.RequestHeaders), ev.RequestHeaders) {
				t.Errorf("%s: ScrubHeaders dropped or altered a REQUEST header -- every name the "+
					"adapter records must be on exchange.go's allowlist\n before: %v\n  after: %v",
					tc.name, ev.RequestHeaders, submission.ScrubHeaders(ev.RequestHeaders))
			}
			// A no-response row leaves ResponseHeaders nil; ScrubHeaders(nil) is a non-nil empty
			// map, so compare by CONTENT, not by DeepEqual, on that side.
			scrubbedResp := submission.ScrubHeaders(ev.ResponseHeaders)
			if len(scrubbedResp) != len(ev.ResponseHeaders) {
				t.Errorf("%s: ScrubHeaders dropped a RESPONSE header\n before: %v\n  after: %v",
					tc.name, ev.ResponseHeaders, scrubbedResp)
			}
			for name, values := range ev.ResponseHeaders {
				if !reflect.DeepEqual(scrubbedResp[http.CanonicalHeaderKey(name)], values) {
					t.Errorf("%s: response header %s did not survive ScrubHeaders: %v -> %v",
						tc.name, name, values, scrubbedResp[http.CanonicalHeaderKey(name)])
				}
			}
		})
	}

	// The four request names, spelled out once: a rename that still happened to be on the
	// allowlist would slip past the DeepEqual above.
	_, ev := maSubmit(t, a, ctx, maWire(t, a, maTINAccept), "idem-scrub")
	scrubbed := submission.ScrubHeaders(ev.RequestHeaders)
	for _, h := range []struct{ name, want string }{
		{"Content-Type", maContentTypeJSON},
		{"Accept", maContentTypeJSON},
		{"User-Agent", maUserAgent},
		{"Idempotency-Key", "idem-scrub"},
	} {
		if got := scrubbed.Get(h.name); got != h.want {
			t.Errorf("after ScrubHeaders: %s = %q, want %q", h.name, got, h.want)
		}
	}
}

// ---------------------------------------------------------------------------------------
// S11/S12/S13 (AC-7, AC-8) -- latency.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_LatencyMSAlwaysMeasured (S11, AC-7, RED-FIRST): LatencyMS is non-nil and >= 0
// on every path that got past the context check, and NIL on the one path that did not.
//
// The pre-cancelled boundary is what stops "always set it to 0" from passing: it is the only row
// in the matrix where the measurement legitimately never happened.
func TestMockAdapter_LatencyMSAlwaysMeasured(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		tin     string
		rawWire submission.Wire
	}{
		{name: "accept", tin: maTINAccept},
		{name: "reject", tin: maTINReject},
		{name: "pending", tin: maTINPending},
		{name: "unavailable", tin: maTINUnavailable},
		{name: "slow", tin: maTINSlow},
		{name: "timeout", tin: maTINTimeout},
		{name: "connection", tin: maTINConnection},
		{name: "unparseable", rawWire: submission.Wire("not json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.rawWire
			if w == nil {
				w = maWire(t, a, tc.tin)
			}
			_, ev := maSubmit(t, a, ctx, w, "idem-"+tc.name)
			if ev.LatencyMS == nil {
				t.Fatalf("%s: LatencyMS is nil -- every path past the context check is measured", tc.name)
			}
			if *ev.LatencyMS < 0 {
				t.Errorf("%s: LatencyMS = %d, want >= 0 (L12, app_exchange CHECK)", tc.name, *ev.LatencyMS)
			}
		})
	}

	// BOUNDARY. The pre-cancelled row is the ONE place LatencyMS must stay nil; without this an
	// implementation that stamps 0 unconditionally passes everything above.
	t.Run("pre-cancelled-is-unmeasured", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		_, ev := maSubmit(t, a, cancelled, maWire(t, a, maTINAccept), "idem-cancelled")
		if ev.LatencyMS != nil {
			t.Errorf("pre-cancelled: LatencyMS = %d, want nil -- nothing was measured because "+
				"nothing ran", *ev.LatencyMS)
		}
	})
}

// TestMockAdapter_LatencyIsApplied (S12, AC-8, RED-FIRST): the configured baseline is really
// waited out, and MockConfig{} really is instant (which is what keeps the contract suite's ~10
// Submit/Poll calls fast).
func TestMockAdapter_LatencyIsApplied(t *testing.T) {
	const baseline = 40 * time.Millisecond
	ctx := context.Background()

	slowAdapter := submission.NewMockAdapter(submission.MockConfig{Latency: baseline})
	w := maWire(t, slowAdapter, maTINAccept)

	start := time.Now()
	r, _ := maSubmit(t, slowAdapter, ctx, w, "idem-latency")
	elapsed := time.Since(start)
	maAccepted(t, "configured-latency", r)
	if elapsed < baseline {
		t.Errorf("MockConfig{Latency: %v}: Submit returned after %v, want at least %v -- the "+
			"baseline is what makes queued/submitting/accepted observable as distinct states",
			baseline, elapsed, baseline)
	}

	instant := submission.NewMockAdapter(submission.MockConfig{})
	start = time.Now()
	maSubmit(t, instant, ctx, w, "idem-instant")
	if elapsed := time.Since(start); elapsed >= baseline {
		t.Errorf("MockConfig{}: Submit returned after %v, want well under %v -- the zero value "+
			"means instant ([one-latency-knob])", elapsed, baseline)
	}
}

// TestMockAdapter_SlowAndTimeoutMultiplyTheBaseline (S13, AC-8, RED-FIRST, NEW).
//
// THIS IS THE ONLY SPEC IN THE SUITE THAT EXECUTES mockSlowFactor AND mockTimeoutFactor. Every
// other spec runs at MockConfig{}, where 4 x 0 and 8 x 0 are both 0 -- so without this spec the
// two constants could hold ANY value and the whole suite would stay green.
//
// LOWER BOUNDS ONLY, deliberately: an upper bound on a 10ms baseline under a loaded CI runner is
// a flake generator. The bounds cannot be satisfied by an implementation that ignores the
// multipliers.
func TestMockAdapter_SlowAndTimeoutMultiplyTheBaseline(t *testing.T) {
	const baseline = 10 * time.Millisecond
	a := submission.NewMockAdapter(submission.MockConfig{Latency: baseline})
	ctx := context.Background()

	measure := func(tin string) time.Duration {
		w := maWire(t, a, tin)
		start := time.Now()
		maSubmit(t, a, ctx, w, "idem-multiplier")
		return time.Since(start)
	}

	accept := measure(maTINAccept)
	slow := measure(maTINSlow)
	timeout := measure(maTINTimeout)

	if slow < maSlowFactor*baseline {
		t.Errorf("slow trigger took %v, want at least %d x %v = %v (mockSlowFactor)",
			slow, maSlowFactor, baseline, maSlowFactor*baseline)
	}
	if timeout < maTimeoutFactor*baseline {
		t.Errorf("timeout trigger took %v, want at least %d x %v = %v (mockTimeoutFactor)",
			timeout, maTimeoutFactor, baseline, maTimeoutFactor*baseline)
	}
	if slow < accept {
		t.Errorf("slow trigger took %v but accept took %v -- slow must never be faster than the "+
			"baseline it multiplies", slow, accept)
	}
}

// ---------------------------------------------------------------------------------------
// S14/S15 (AC-9) -- determinism.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_SubmitIsDeterministicAcrossInstances (S14, AC-9, RED-FIRST): Core AC-2 demands
// the same invoice yield the same outcome on every replica, and [deterministic-evidence] makes
// the BODY deterministic too so this can be asserted on exact bytes.
//
// It MUST use the ACCEPT path. Pending.PollAfter is time.Now()+backoff and is deliberately
// non-deterministic, so a DeepEqual over two Pendings is GUARANTEED to fail; S15 covers pending's
// determinism without comparing PollAfter.
//
// The DIFFERENT-WIRE NEGATIVE CONTROL is mandatory: without it, an adapter returning one constant
// body for everything passes.
func TestMockAdapter_SubmitIsDeterministicAcrossInstances(t *testing.T) {
	// Different Latency values on purpose: configuration must not reach the synthesized outcome.
	// Both are small enough not to slow the suite.
	a1 := submission.NewMockAdapter(submission.MockConfig{})
	a2 := submission.NewMockAdapter(submission.MockConfig{Latency: time.Millisecond})
	ctx := context.Background()

	w := maWire(t, a1, maTINAccept)
	const idemKey = "idem-determinism"

	r1, ev1 := maSubmit(t, a1, ctx, w, idemKey)
	r2, ev2 := maSubmit(t, a2, ctx, w, idemKey)

	maAccepted(t, "instance-1", r1)
	maAccepted(t, "instance-2", r2)

	if !reflect.DeepEqual(r1, r2) {
		t.Errorf("two freshly constructed adapters returned different Results for the same wire:\n"+
			" instance-1: %#v\n instance-2: %#v", r1, r2)
	}
	if ev1.ResponseBody == nil || ev2.ResponseBody == nil {
		t.Fatalf("an accepted submission must carry a synthesized response body: %v / %v",
			ev1.ResponseBody, ev2.ResponseBody)
	}
	if *ev1.ResponseBody != *ev2.ResponseBody {
		t.Errorf("two freshly constructed adapters synthesized different response bodies:\n"+
			" instance-1: %s\n instance-2: %s", *ev1.ResponseBody, *ev2.ResponseBody)
	}
	if !reflect.DeepEqual(ev1.RequestHeaders, ev2.RequestHeaders) {
		t.Errorf("two freshly constructed adapters recorded different request headers:\n"+
			" instance-1: %v\n instance-2: %v", ev1.RequestHeaders, ev2.RequestHeaders)
	}

	// NEGATIVE CONTROL: a genuinely different accept wire must produce a different body.
	other := maWireFor(t, a1, maTINAccept, "INV-MOCK-0002")
	if string(other) == string(w) {
		t.Fatalf("the negative control's wire is identical to the first -- the control proves nothing")
	}
	r3, ev3 := maSubmit(t, a1, ctx, other, idemKey)
	maAccepted(t, "different-wire", r3)
	if ev3.ResponseBody == nil {
		t.Fatalf("different-wire: no response body")
	}
	if *ev3.ResponseBody == *ev1.ResponseBody {
		t.Errorf("a different wire synthesized a byte-identical response body -- the body is a "+
			"constant, not a function of the request ([deterministic-evidence]):\n%s", *ev3.ResponseBody)
	}
	if reflect.DeepEqual(r3, r1) {
		t.Errorf("a different wire returned a DeepEqual Result: %#v", r3)
	}
}

// TestMockAdapter_PendingRefIsContentDeterministicButPollAfterIsNot (S15, AC-9, RED-FIRST, NEW):
// the pending path's Ref and body are pure functions of the wire, while PollAfter is now+backoff
// and is deliberately excluded from the comparison ([deterministic-evidence] names it and
// LatencyMS as the only two non-deterministic values the adapter produces).
func TestMockAdapter_PendingRefIsContentDeterministicButPollAfterIsNot(t *testing.T) {
	a1 := submission.NewMockAdapter(submission.MockConfig{})
	a2 := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	w := maWire(t, a1, maTINPending)
	start := time.Now()

	r1, ev1 := maSubmit(t, a1, ctx, w, "idem-pending")
	r2, ev2 := maSubmit(t, a2, ctx, w, "idem-pending")

	p1, ok1 := r1.(submission.Pending)
	p2, ok2 := r2.(submission.Pending)
	if !ok1 || !ok2 {
		t.Fatalf("the pending trigger returned %T and %T, want two submission.Pending", r1, r2)
	}

	if p1.Ref != p2.Ref {
		t.Errorf("two instances issued different Refs for the same wire:\n instance-1: %q\n instance-2: %q",
			p1.Ref, p2.Ref)
	}
	if !strings.HasPrefix(string(p1.Ref), maRefPrefix) {
		t.Errorf("Pending.Ref = %q, want the %q prefix", p1.Ref, maRefPrefix)
	}
	if ev1.ResponseBody == nil || ev2.ResponseBody == nil {
		t.Fatalf("the pending path must carry a synthesized 202 body: %v / %v", ev1.ResponseBody, ev2.ResponseBody)
	}
	if *ev1.ResponseBody != *ev2.ResponseBody {
		t.Errorf("two instances synthesized different 202 bodies:\n instance-1: %s\n instance-2: %s",
			*ev1.ResponseBody, *ev2.ResponseBody)
	}

	// PollAfter: non-zero (L09) and in the future, but NEVER compared between the two -- it is
	// now+backoff and the two calls happened at different instants by construction.
	for i, p := range []submission.Pending{p1, p2} {
		if p.PollAfter.IsZero() {
			t.Errorf("instance-%d: Pending.PollAfter is zero (L09)", i+1)
		}
		if !p.PollAfter.After(start) {
			t.Errorf("instance-%d: Pending.PollAfter = %v, want after the call started (%v) -- it is "+
				"now + a fixed backoff", i+1, p.PollAfter, start)
		}
	}
}

// ---------------------------------------------------------------------------------------
// S16 (AC-10) -- the panic guard.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_SubmitNeverPanics (S16, AC-10, GUARD -- honestly NOT a red-first spec).
//
// An implementation that returns Retryable for everything passes the no-panic half of this
// outright, so most of it records no transition. It is here to lock the property against a later
// draft that starts dereferencing the wire (L15), the same framing contract_test.go uses for
// TestContractSuite_UsesNarrowT.
//
// ITS TEETH ARE THE LAST TWO ROWS. The JSON documents `null` and `{}` parse WITHOUT error into
// the ZERO envelope (mock_wire.go:213-216) -> buyer TIN "" -> the accept path, so they must be
// Accepted specifically. That half IS red against a do-nothing Submit.
func TestMockAdapter_SubmitNeverPanics(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	deep := strings.Repeat(`{"a":`, 500) + `1` + strings.Repeat(`}`, 500)
	huge := `{"ID":"` + strings.Repeat("x", 1<<20) + `"}`

	for _, tc := range []struct {
		name       string
		wire       submission.Wire
		wantAccept bool
	}{
		{name: "zero-wire", wire: submission.Wire{}},
		{name: "nil-wire", wire: nil},
		{name: "invalid-utf8", wire: submission.Wire([]byte{0xff, 0xfe, 0x00, 0x80})},
		{name: "one-mib", wire: submission.Wire(huge), wantAccept: true},
		{name: "deeply-nested", wire: submission.Wire(deep)},
		{name: "json-null", wire: submission.Wire("null"), wantAccept: true},
		{name: "json-empty-object", wire: submission.Wire("{}"), wantAccept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ev := maSubmit(t, a, ctx, tc.wire, "idem-"+tc.name)

			// "A well-formed Result each time": checked with the SHIPPED law checks, so a nil
			// Result or a pointer variant trips L06 here exactly as it would in the contract suite.
			rec := &lawRecorder{}
			CheckResult(rec, tc.name, r)
			CheckEvidence(rec, tc.name, ev, "idem-"+tc.name)
			if len(rec.messages) != 0 {
				t.Errorf("%s: CheckResult/CheckEvidence recorded %d contract failure(s): %v",
					tc.name, len(rec.messages), rec.messages)
			}

			// THE TEETH: `null` and `{}` are legal JSON that parse into the zero envelope, so they
			// take the ordinary accept path -- they are NOT unparseable.
			if tc.wantAccept {
				acc := maAccepted(t, tc.name, r)
				if strings.TrimSpace(acc.IRN) == "" {
					t.Errorf("%s: Accepted.IRN is blank (L07)", tc.name)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// S17/S18 (AC-11) -- the mid-flight cancellation oracle.
// ---------------------------------------------------------------------------------------

// maCancelInFlight runs the one experiment S17 and S18 both read: a 5-second in-flight wait
// interrupted by a 250ms context deadline. Shared so S18 pins the SAME evidence S17 measured.
//
// The numbers are load-bearing. 5s / 250ms / a 2s bound gives the deadline a ~12x margin over the
// entry-check race (a 20ms deadline, as the story first had it, flips to the PRE-CANCELLED row on
// a single GC pause and fails spuriously) while still failing a time.Sleep(5s) implementation by
// three full seconds.
func maCancelInFlight(t *testing.T) (elapsed time.Duration, r submission.Result, ev submission.Evidence, a *submission.MockAdapter) {
	t.Helper()

	a = submission.NewMockAdapter(submission.MockConfig{Latency: 5 * time.Second})
	// Built on a LIVE context, before the deadline exists: Transform is pure and must not be part
	// of the measurement.
	w := maWire(t, a, maTINAccept)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	r, ev = maSubmit(t, a, ctx, w, "idem-inflight-cancel")
	elapsed = time.Since(start)
	return elapsed, r, ev, a
}

// TestMockAdapter_SubmitAbortsTheInFlightWaitOnCancellation (S17, AC-11, RED-FIRST).
//
// THIS SPEC IS THE ENTIRE ORACLE FOR QA-VERIFY FINDING F1 ([cancellation-is-observable-in-the-wait]).
//
// Every other assertion below is also satisfied by `time.Sleep(d)` followed by a post-wake
// ctx.Err() re-check: that implementation ALSO returns Retryable, ALSO reports ReachedWire true,
// ALSO leaves the response fields nil. It simply takes the full five seconds to do it, and
// M5-04's retry budget is what would pay for that. The ELAPSED BOUND is the only assertion that
// separates the two. Do not weaken it to a variant assertion and do not raise it to "< 5s".
func TestMockAdapter_SubmitAbortsTheInFlightWaitOnCancellation(t *testing.T) {
	const bound = 2 * time.Second
	elapsed, r, ev, _ := maCancelInFlight(t)

	// THE ORACLE.
	if elapsed >= bound {
		t.Errorf("Submit returned after %v, want under %v -- the in-flight wait did NOT observe the "+
			"cancelled context. A bare time.Sleep (with or without a post-wake ctx.Err() re-check) "+
			"produces exactly the Result asserted below and takes the full configured 5s to do it; "+
			"this bound is the only thing that tells the two apart "+
			"([cancellation-is-observable-in-the-wait])", elapsed, bound)
	}

	if _, isAccepted := r.(submission.Accepted); isAccepted {
		t.Fatalf("Submit returned Accepted -- the wait completed and the scripted verdict was " +
			"synthesized despite the context dying in flight")
	}
	rt := maRetryable(t, "in-flight-cancel", r)
	if !errors.Is(rt.Err, context.DeadlineExceeded) {
		t.Errorf("Retryable.Err = %v, want it to wrap context.DeadlineExceeded -- the wait must "+
			"return ctx.Err(), not a mock sentinel", rt.Err)
	}

	// ReachedWire TRUE: the bytes left the process before the wait began ([two-phase-wire]). This
	// is the assertion that distinguishes this row from the PRE-cancelled one, which is false.
	if !ev.ReachedWire {
		t.Errorf("Evidence.ReachedWire = false, want true -- ReachedWire is set BEFORE the in-flight " +
			"wait, so a request cancelled mid-flight may well have been received")
	}
	if ev.HTTPStatus != nil {
		t.Errorf("Evidence.HTTPStatus = %d, want nil -- no response was synthesized", *ev.HTTPStatus)
	}
	if ev.ResponseBody != nil {
		t.Errorf("Evidence.ResponseBody = %q, want nil", *ev.ResponseBody)
	}
	if len(ev.ResponseHeaders) != 0 {
		t.Errorf("Evidence.ResponseHeaders = %v, want empty", ev.ResponseHeaders)
	}
	if ev.LatencyMS == nil {
		t.Errorf("Evidence.LatencyMS is nil -- this path got well past the context check and was " +
			"measured; only the PRE-cancelled row is unmeasured")
	} else if *ev.LatencyMS < 0 {
		t.Errorf("Evidence.LatencyMS = %d, want >= 0 (L12)", *ev.LatencyMS)
	}
	if ev.RequestBody == nil {
		t.Errorf("Evidence.RequestBody is nil -- the bytes were built and sent before the wait")
	}
}

// TestMockAdapter_InFlightCancellationRecordsSent (S18, AC-11, RED-FIRST): the evidence S17
// measured, run through the shipped bridge. This is the row [two-phase-wire] exists for -- a
// request cancelled in flight may well have been received, which is exactly why M5-01 kept
// "sent" and "connection_failed" apart.
//
// The PRE-CANCELLED CONTRAST ROW is mandatory: the two are trivially confused, and without the
// contrast an implementation that reported ReachedWire true everywhere would pass.
func TestMockAdapter_InFlightCancellationRecordsSent(t *testing.T) {
	_, _, ev, a := maCancelInFlight(t)

	if got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job", "inv", ev).Outcome; got != submission.OutcomeSent {
		t.Errorf("ctx died in flight: ExchangeFor(...).Outcome = %q, want %q -- the bytes had already "+
			"left the process ([two-phase-wire])", got, submission.OutcomeSent)
	}

	// CONTRAST ROW: the PRE-cancelled path, which never reached the connect phase at all.
	instant := submission.NewMockAdapter(submission.MockConfig{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, preEv := maSubmit(t, instant, cancelled, maWire(t, instant, maTINAccept), "idem-pre-cancelled")

	if got := submission.ExchangeFor(instant, submission.OpSubmit, 1, "job", "inv", preEv).Outcome; got != submission.OutcomeConnectionFailed {
		t.Errorf("pre-cancelled: ExchangeFor(...).Outcome = %q, want %q -- L16's check runs BEFORE "+
			"the connect phase, so nothing left the process", got, submission.OutcomeConnectionFailed)
	}
}

// ---------------------------------------------------------------------------------------
// QA Mode B (task-226 Stage-2.5 finding, gap a) -- the negative-latency clamp.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_NegativeLatencyBehavesLikeZeroConfig (QA Mode B gap-fill): pins the
// OBSERVABLE behaviour NewMockAdapter's negative-Latency clamp
// ([negative-latency-rejected-at-the-env-edge]) exists for -- a negative baseline must not
// make Submit hang or error, and must behave identically to MockConfig{}.
//
// HONEST LABEL, in the same spirit as S16's above: mutation-tested by removing
// `if cfg.Latency < 0 { cfg.Latency = 0 }` from NewMockAdapter entirely -- the WHOLE existing
// 18-spec suite, and this spec, stayed green. That is a STRUCTURAL fact, not a coverage gap:
// mockWait's own `d <= 0` branch already absorbs any non-positive duration on every path that
// reaches it (accept/reject/pending/unavailable pass cfg.Latency through unmultiplied;
// slow/timeout multiply it by a POSITIVE factor, which preserves its sign). A negative
// cfg.Latency and a zero cfg.Latency are therefore byte-for-byte indistinguishable from
// outside the package on every Result/Evidence/elapsed-time axis -- the only way to tell them
// apart would be reading the unexported `cfg` field via unsafe reflection, which is out of
// scope for an external contract test. The clamp is still correct to KEEP: it is the
// documented invariant and a belt-and-suspenders guard against a future mockInFlight/mockWait
// that no longer routes every path through `d <= 0`. This spec is kept because it pins the
// actually user-facing property (a misconfigured negative latency must never hang), not
// because it can distinguish clamped-to-zero from left-negative.
func TestMockAdapter_NegativeLatencyBehavesLikeZeroConfig(t *testing.T) {
	negative := submission.NewMockAdapter(submission.MockConfig{Latency: -1 * time.Second})
	zero := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()
	w := maWire(t, negative, maTINAccept)

	done := make(chan struct{})
	var r submission.Result
	var ev submission.Evidence
	var elapsed time.Duration
	go func() {
		start := time.Now()
		r, ev = maSubmit(t, negative, ctx, w, "idem-negative-latency")
		elapsed = time.Since(start)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Submit with MockConfig{Latency: -1s} did not return within 2s -- a negative " +
			"baseline must never hang Submit")
	}

	if elapsed > 500*time.Millisecond {
		t.Errorf("Submit with MockConfig{Latency: -1s} took %v, want effectively instant -- same as "+
			"MockConfig{}", elapsed)
	}
	acc := maAccepted(t, "negative-latency", r)
	if strings.TrimSpace(acc.IRN) == "" {
		t.Errorf("negative-latency: Accepted.IRN is blank")
	}

	// Identical behaviour to MockConfig{}, on the SAME wire and idemKey.
	r2, ev2 := maSubmit(t, zero, ctx, w, "idem-negative-latency")
	if !reflect.DeepEqual(r, r2) {
		t.Errorf("MockConfig{Latency: -1s} and MockConfig{} returned different Results:\n"+
			" negative: %#v\n zero: %#v", r, r2)
	}
	if ev.ResponseBody == nil || ev2.ResponseBody == nil || *ev.ResponseBody != *ev2.ResponseBody {
		t.Errorf("MockConfig{Latency: -1s} and MockConfig{} synthesized different response bodies")
	}
}

// ---------------------------------------------------------------------------------------
// QA Mode B -- adversarial coverage the 18 RED specs did not include.
// ---------------------------------------------------------------------------------------

// TestMockAdapter_IdempotencyKeyWithUnusualBytesIsRecordedVerbatim (QA Mode B): an idemKey
// containing spaces, non-ASCII, an embedded CRLF (a header-injection attempt) or a very long
// value must be recorded VERBATIM in Evidence.RequestHeaders and must never smuggle a second
// header name into the map -- http.Header.Set stores the value as an opaque map entry, it does
// not parse it as raw wire text, so this is safe by construction as long as nothing downstream
// ever re-serializes it as a literal HTTP header line (RecordExchange does not: it goes through
// ScrubHeaders -> json.Marshal, never net/http's wire writer).
func TestMockAdapter_IdempotencyKeyWithUnusualBytesIsRecordedVerbatim(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()
	w := maWire(t, a, maTINAccept)

	for _, tc := range []struct {
		name string
		key  string
	}{
		{"embedded-spaces", "idem with spaces"},
		{"leading-and-trailing-whitespace", "  idem-padded  "},
		{"embedded-crlf-injection-attempt", "idem-key\r\nX-Injected-Header: evil"},
		{"non-ascii", "idem-δοκιμή-你好-🎉"},
		{"very-long", "idem-" + strings.Repeat("x", 8192)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ev := maSubmit(t, a, ctx, w, tc.key)

			if got := ev.RequestHeaders.Get("Idempotency-Key"); got != tc.key {
				t.Errorf("Idempotency-Key = %q, want the key verbatim %q", got, tc.key)
			}
			// No smuggled second header: the map has exactly the four names Submit ever sets.
			if len(ev.RequestHeaders) != 4 {
				t.Errorf("RequestHeaders has %d names, want exactly 4 (Content-Type, Accept, "+
					"User-Agent, Idempotency-Key) -- got %v", len(ev.RequestHeaders), ev.RequestHeaders)
			}
			if maHeaderPresent(ev.RequestHeaders, "X-Injected-Header") {
				t.Errorf("the CRLF-embedded idemKey smuggled a second header into the map: %v",
					ev.RequestHeaders)
			}
			// Still survives ScrubHeaders unchanged -- Idempotency-Key is on the allowlist
			// regardless of what value it carries.
			if got := submission.ScrubHeaders(ev.RequestHeaders).Get("Idempotency-Key"); got != tc.key {
				t.Errorf("ScrubHeaders altered the Idempotency-Key value: got %q, want %q", got, tc.key)
			}
			// Submit must still take the ordinary accept path -- the idemKey is evidence, never
			// part of the trigger channel.
			maAccepted(t, tc.name, r)
		})
	}
}

// TestMockAdapter_ConcurrentSubmitsOnOneInstance (QA Mode B): MockAdapter claims to hold no
// mutable state ([one-latency-knob]'s doc comment, mock_adapter.go:101-105) -- this drives many
// concurrent Submit calls through ONE shared instance and checks per-call correctness. Run with
// `go test -race` to catch a shared-state bug the type system alone does not rule out.
//
// Every wire and idemKey is built BEFORE any goroutine is spawned: the helpers that build them
// (maWireFor) call t.Fatalf, which testing.T forbids from any goroutine but the one running the
// test function itself. Each goroutine's own check callback uses only t.Errorf (safe for
// concurrent use) and recovers its own panic so one goroutine's failure does not corrupt the
// others' reporting.
func TestMockAdapter_ConcurrentSubmitsOnOneInstance(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	type job struct {
		label string
		wire  submission.Wire
		idem  string
		check func(t *testing.T, r submission.Result)
	}

	const rounds = 25
	var jobs []job
	for i := 0; i < rounds; i++ {
		jobs = append(jobs,
			job{
				label: "accept",
				wire:  maWireFor(t, a, maTINAccept, fmt.Sprintf("INV-CONC-A-%d", i)),
				idem:  fmt.Sprintf("idem-conc-a-%d", i),
				check: func(t *testing.T, r submission.Result) {
					if _, ok := r.(submission.Accepted); !ok {
						t.Errorf("concurrent accept: Result is %T, want Accepted", r)
					}
				},
			},
			job{
				label: "reject",
				wire:  maWireFor(t, a, maTINReject, fmt.Sprintf("INV-CONC-R-%d", i)),
				idem:  fmt.Sprintf("idem-conc-r-%d", i),
				check: func(t *testing.T, r submission.Result) {
					if _, ok := r.(submission.Rejected); !ok {
						t.Errorf("concurrent reject: Result is %T, want Rejected", r)
					}
				},
			},
			job{
				label: "pending",
				wire:  maWireFor(t, a, maTINPending, fmt.Sprintf("INV-CONC-P-%d", i)),
				idem:  fmt.Sprintf("idem-conc-p-%d", i),
				check: func(t *testing.T, r submission.Result) {
					if _, ok := r.(submission.Pending); !ok {
						t.Errorf("concurrent pending: Result is %T, want Pending", r)
					}
				},
			},
			job{
				label: "unavailable",
				wire:  maWireFor(t, a, maTINUnavailable, fmt.Sprintf("INV-CONC-U-%d", i)),
				idem:  fmt.Sprintf("idem-conc-u-%d", i),
				check: func(t *testing.T, r submission.Result) {
					rt, ok := r.(submission.Retryable)
					if !ok || rt.Err == nil {
						t.Errorf("concurrent unavailable: Result is %#v, want Retryable with a "+
							"non-nil Err", r)
					}
				},
			},
		)
	}

	var wg sync.WaitGroup
	for _, jb := range jobs {
		wg.Add(1)
		go func(jb job) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("%s: Submit panicked: %v", jb.label, rec)
				}
			}()
			r, ev := a.Submit(ctx, jb.wire, jb.idem)
			jb.check(t, r)
			if got := ev.RequestHeaders.Get("Content-Type"); got != maContentTypeJSON {
				t.Errorf("%s: RequestHeaders Content-Type = %q, want %q", jb.label, got, maContentTypeJSON)
			}
			if got := ev.RequestHeaders.Get("Idempotency-Key"); got != jb.idem {
				t.Errorf("%s: RequestHeaders Idempotency-Key = %q, want %q", jb.label, got, jb.idem)
			}
		}(jb)
	}
	wg.Wait()
}

// TestMockAdapter_ReservedTriggerTINWithOtherwiseZeroCanonical (QA Mode B): every field except
// Buyer.TIN is the zero value -- no InvoiceID, no lines, no currency, no money -- so the trigger
// must fire from the buyer TIN alone, independent of everything else in the envelope.
func TestMockAdapter_ReservedTriggerTINWithOtherwiseZeroCanonical(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	c := submission.Canonical{Buyer: submission.Party{TIN: strPtr(maTINReject)}}
	w, err := a.Transform(ctx, c)
	if err != nil {
		t.Fatalf("Transform(zero canonical + reserved buyer TIN) failed: %v", err)
	}

	r, ev := maSubmit(t, a, ctx, w, "idem-zero-canonical-reject")
	rej, ok := r.(submission.Rejected)
	if !ok {
		t.Fatalf("Result is %T, want submission.Rejected -- the trigger must fire even when every "+
			"other envelope field is the zero value", r)
	}
	if len(rej.Reasons) != 1 || rej.Reasons[0].Code != maCodeRejected {
		t.Errorf("Reasons = %+v, want exactly one with code %q", rej.Reasons, maCodeRejected)
	}

	rec := &lawRecorder{}
	CheckResult(rec, "zero-canonical-reject", r)
	CheckEvidence(rec, "zero-canonical-reject", ev, "idem-zero-canonical-reject")
	if len(rec.messages) != 0 {
		t.Errorf("contract law failure(s) on the zero-canonical reject path: %v", rec.messages)
	}
}

// TestMockAdapter_EvidenceDoesNotAliasAcrossCalls (QA Mode B): two Submit calls on the same
// wire must not share any backing array or map between their two Evidences -- mutating one
// must never affect the other. mockRequestHeaders documents building "a FRESH map per call";
// this spec holds it to that claim directly rather than trusting the comment.
func TestMockAdapter_EvidenceDoesNotAliasAcrossCalls(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()
	w := maWire(t, a, maTINAccept)

	_, ev1 := maSubmit(t, a, ctx, w, "idem-alias-1")
	_, ev2 := maSubmit(t, a, ctx, w, "idem-alias-2")

	if reflect.ValueOf(ev1.RequestHeaders).Pointer() == reflect.ValueOf(ev2.RequestHeaders).Pointer() {
		t.Errorf("two Submit calls share the SAME RequestHeaders map")
	}
	ev1.RequestHeaders.Set("Idempotency-Key", "tampered")
	if got := ev2.RequestHeaders.Get("Idempotency-Key"); got != "idem-alias-2" {
		t.Errorf("mutating ev1.RequestHeaders changed ev2's: got %q, want %q", got, "idem-alias-2")
	}

	if ev1.RequestBody == nil || ev2.RequestBody == nil {
		t.Fatalf("RequestBody is nil on the accept path: %v / %v", ev1.RequestBody, ev2.RequestBody)
	}
	if ev1.RequestBody == ev2.RequestBody {
		t.Errorf("two Submit calls returned the SAME *string for RequestBody")
	}
	if *ev1.RequestBody != *ev2.RequestBody {
		t.Errorf("RequestBody content differs for the identical wire:\n got %q\nwant %q",
			*ev1.RequestBody, *ev2.RequestBody)
	}

	if ev1.ResponseBody == nil || ev2.ResponseBody == nil {
		t.Fatalf("ResponseBody is nil on the accept path")
	}
	if ev1.ResponseBody == ev2.ResponseBody {
		t.Errorf("two Submit calls returned the SAME *string for ResponseBody")
	}

	if reflect.ValueOf(ev1.ResponseHeaders).Pointer() == reflect.ValueOf(ev2.ResponseHeaders).Pointer() {
		t.Errorf("two Submit calls share the SAME ResponseHeaders map")
	}
	ev1.ResponseHeaders.Set("Content-Type", "text/plain")
	if got := ev2.ResponseHeaders.Get("Content-Type"); got != maContentTypeJSON {
		t.Errorf("mutating ev1.ResponseHeaders changed ev2's: got %q, want %q", got, maContentTypeJSON)
	}
}

// TestMockAdapter_TransformSubmitRoundTripAcrossCanonicalCorpus (QA Mode B): every shape in
// contract_test.go's canonicalCorpus -- full, minimal, no-lines, all-nil-money,
// multi-byte-long-text and zero -- driven through the REAL Transform and then Submit, asserting
// each produces a law-clean Result/Evidence pair via CheckResult/CheckEvidence. None of the
// corpus's buyer TINs are reserved trigger values (they are "BUY-TIN-N" or absent entirely), so
// every case must take the ordinary accept path ([non-reserved-defaults-to-accept]).
func TestMockAdapter_TransformSubmitRoundTripAcrossCanonicalCorpus(t *testing.T) {
	a := submission.NewMockAdapter(submission.MockConfig{})
	ctx := context.Background()

	for _, tc := range canonicalCorpus {
		t.Run(tc.name, func(t *testing.T) {
			w, err := a.Transform(ctx, tc.c)
			if err != nil {
				t.Fatalf("Transform(%s) failed: %v", tc.name, err)
			}
			idemKey := "idem-corpus-" + tc.name
			r, ev := maSubmit(t, a, ctx, w, idemKey)

			rec := &lawRecorder{}
			CheckResult(rec, tc.name, r)
			CheckEvidence(rec, tc.name, ev, idemKey)
			if len(rec.messages) != 0 {
				t.Errorf("%s: contract law failure(s): %v", tc.name, rec.messages)
			}

			acc, ok := r.(submission.Accepted)
			if !ok {
				t.Fatalf("%s: Result is %T, want submission.Accepted -- none of the corpus's buyer "+
					"TINs are reserved trigger values", tc.name, r)
			}
			if strings.TrimSpace(acc.IRN) == "" {
				t.Errorf("%s: Accepted.IRN is blank", tc.name)
			}
		})
	}
}

// TestMockAdapter_ExtremeLatencyOverflowDoesNotHangOnTimeout (QA Mode B): pins the doc comment's
// own claim -- "an x8 that wrapped negative lands in mockWait's `d <= 0` branch" -- by actually
// constructing a Latency for which mockTimeoutFactor's x8 multiplication overflows int64 and
// wraps to a large NEGATIVE duration, then asserting Submit still returns fast rather than
// hanging. The 2s guard turns a hang into a fast, diagnosable test failure instead of a stuck
// CI job.
//
// MUTATION NOTE (honesty, matching the negative-latency spec above): removing mockWait's
// `d <= 0` early return entirely and letting a very-negative d reach `time.NewTimer(d)` directly
// did NOT make this spec fail on this toolchain -- Go's runtime Timer already fires a
// non-positive duration immediately, so overflow-driven hangs are a belt-and-suspenders concern,
// not the sole line of defence. The `d <= 0` guard's primary, load-bearing purpose (per
// mockWait's own doc comment, rule 2) is avoiding a SECOND racy cancellation gate on a
// zero-length wait, which is review-enforced and cannot be pinned by any non-flaky spec. This
// test still earns its keep: it directly exercises the overflow arithmetic the doc comment
// claims happens, on the actual production constant mockTimeoutFactor, rather than trusting the
// comment.
func TestMockAdapter_ExtremeLatencyOverflowDoesNotHangOnTimeout(t *testing.T) {
	const huge = time.Duration(math.MaxInt64)/8 + 1 // x8 (mockTimeoutFactor) overflows int64
	a := submission.NewMockAdapter(submission.MockConfig{Latency: huge})
	w := maWire(t, a, maTINTimeout)

	done := make(chan struct{})
	var r submission.Result
	go func() {
		r, _ = a.Submit(context.Background(), w, "idem-overflow")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Submit with an overflow-inducing Latency did not return within 2s -- mockInFlight's " +
			"x8 multiplier must have wrapped negative and mockWait's `d <= 0` branch should have " +
			"absorbed it instantly")
	}

	rt, ok := r.(submission.Retryable)
	if !ok || !errors.Is(rt.Err, submission.ErrMockTimeout) {
		t.Errorf("Result = %#v, want Retryable wrapping ErrMockTimeout", r)
	}
}
