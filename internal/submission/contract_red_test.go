// M5-02-07 (task-223), Stage 2 (RED scaffolding): the completeness proof this story's Core
// AC-5 depends on -- every law in AllLaws must have at least one demonstrated RED (a
// non-conforming adapter RunAdapterContract rejects, recording exactly the right law id(s)).
//
// This file ships the SCAFFOLDING only: the redCase table type, an EMPTY redCases table, the
// table-driven runner (TestContractSuite_RejectsNonConformingAdapters), and the completeness
// meta-test (TestAllLaws_EveryLawHasADemonstratedRed). Stage 3 (the executor) fills redCases
// with sixteen entries -- one non-conforming adapter per law, L01 through L16, no gaps -- per
// this story's Technical Design. Writing those sixteen adapters here would NOT be a RED: the
// suite they'd run against (RunAdapterContract, M5-02-06) already recorded every one of its 16
// laws correctly under QA Mode B's mutation testing, so a case asserting "the suite rejects a
// bad adapter" is green the instant it compiles -- not evidence of anything. The one genuine RED
// this stage can produce is TestAllLaws_EveryLawHasADemonstratedRed failing against an EMPTY
// table: that failure, naming all sixteen orphaned ids, IS Core AC-5's premise made executable.
//
// Package submission_test (external), matching every other test file in this package
// (contract_test.go, reference_adapter_test.go, exchange_test.go, ...). No testify. No
// t.Skip -- internal/tools/rlsgate fails the CI queue job on any test-level skip (or on zero
// tests observed). No new TestMain (one already exists, failure_modes_test.go:57).
package submission_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// redCase describes one non-conforming adapter engineered to demonstrate a specific law (or
// laws) failing under RunAdapterContract -- the RED half of this story's evidence for Core
// AC-5. Stage 3 fills redCases with sixteen of these; this file ships the type and an empty
// table only.
type redCase struct {
	// lawID is the primary law id this case exists to demonstrate, e.g. "L01" -- used to name
	// the subtest and in failure messages. It is NOT itself the assertion target: expected
	// (below) is, so a case is never accidentally graded against a hardcoded singleton derived
	// from this field alone.
	lawID string

	// description is a short human account of the injected defect, e.g. "Name() returns the
	// empty string" -- shown in the subtest name and failure messages so a broken case is
	// diagnosable without re-reading the adapter's source.
	description string

	// newAdapter builds the non-conforming adapter fresh -- the same factory shape
	// RunAdapterContract itself expects (submission.Adapter, called MORE THAN ONCE per run: see
	// contract_test.go's RunAdapterContract doc comment, and
	// TestRunAdapterContract_CallsNewAdapterMoreThanOnce). Nine of the sixteen Stage-3 cases
	// need a custom method override embedding *refAdapter (L01, L02, L03, L04, L05, L13, L14,
	// L15, L16); the rest reprogram newRefFactory's result/evidence arguments directly.
	//
	// L04's case, specifically, mutates canonicalCorpus in place through its Supplier.TIN
	// pointers (canonicalCorpus is package-level shared state; RunAdapterContract takes no
	// corpus parameter and always iterates the real one -- contract_test.go:248, so this
	// subtask cannot inject a private copy). Two hazards follow, both to be handled inside
	// Stage 3's L04 newAdapter/cleanup, never by editing contract_test.go:
	//   1. Nil-pointer panic: 3 of the 6 corpus cases ("minimal", "no-lines", "zero" --
	//      contract_test.go:101-105) have Supplier.TIN == nil. An unconditional
	//      *c.Supplier.TIN = "..." write panics on those three, and callTransform's recover()
	//      attributes that panic to L15, contaminating {L04} into {L04, L15}. Guard the write
	//      (nil-check before writing, or key the mutation off a field unique to one non-nil-TIN
	//      case) so zero panics occur across all 6 corpus cases.
	//   2. Corpus persistence across the binary: a write through Supplier.TIN's pointer
	//      outlives this one RunAdapterContract call and is visible to every later test in the
	//      same binary -- including this file's own dedicated per-law case (whichever Stage-3
	//      test demonstrates L04 standalone) AND its row inside
	//      TestContractSuite_RejectsNonConformingAdapters (the same adapter runs at both call
	//      sites). Use this case's cleanup field (below) to restore the pre-run value(s) via
	//      t.Cleanup, so whichever test the binary runs next observes a pristine corpus.
	newAdapter func() submission.Adapter

	// expected is the EXACT set of law ids RunAdapterContract must record for this case --
	// set equality (recorded == expected), never containment (AC-2): a non-conforming adapter
	// that trips one extra, unrelated law is itself a spec defect for that case, not a pass.
	// Build with lawSet(...) below.
	expected map[string]bool

	// cleanup, when non-nil, is registered via t.Cleanup immediately after this case's
	// RunAdapterContract call, so a case that mutates package-level state (L04's Supplier.TIN
	// writes reach into canonicalCorpus -- see newAdapter's doc comment above) restores what it
	// changed before the next test in the binary runs, regardless of this case's pass/fail.
	// Most cases leave this nil.
	cleanup func()
}

// lawSet builds a redCase.expected value from a list of law ids -- a small convenience so Stage
// 3's sixteen entries can write lawSet("L01") / lawSet("L05") rather than hand-building a map
// literal at each of sixteen call sites.
func lawSet(ids ...string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

// sortedLawIDs returns m's keys sorted ascending, for deterministic, readable failure messages --
// map iteration order is randomized by the Go runtime, and a failure message whose set ordering
// changes between runs is hard to diff against a previous CI log.
func sortedLawIDs(m map[string]bool) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// intPtr returns a pointer to a fresh int holding n -- this file's *int equivalent of
// contract_test.go's strPtr, needed for Evidence.HTTPStatus/LatencyMS literals in the L11/L12
// cases below.
func intPtr(n int) *int { return &n }

// newBaseRefAdapter returns a fresh, well-formed *refAdapter -- the same one newRef() builds,
// concretely typed so Stage 3's nine override cases can embed it and replace exactly one
// method. newRef() itself always returns a *refAdapter under the hood (reference_adapter_
// test.go), so this assertion never fails; it exists only to give the embedding cases a
// concrete type to embed (submission.Adapter, newRef's own return type, cannot be embedded to
// get method promotion).
func newBaseRefAdapter() *refAdapter {
	return newRef().(*refAdapter)
}

// wellFormedEvidence returns evidence that satisfies L11-L13 on its own (ReachedWire true, a
// present non-negative HTTPStatus/LatencyMS) -- used by every pure-reprogramming case below
// (L06-L12) whose defect is entirely in the Result, not the Evidence, so CheckEvidence records
// nothing and the case's recorded set stays exactly the one Result-shaped law under test.
// Idempotency-Key (L13) needs no help here: refAdapter.Submit rebuilds RequestHeaders from the
// real idemKey unconditionally before returning, overwriting whatever this function sets.
func wellFormedEvidence() submission.Evidence {
	status := 200
	latency := 5
	return submission.Evidence{
		ReachedWire:     true,
		HTTPStatus:      &status,
		LatencyMS:       &latency,
		ResponseHeaders: http.Header{},
	}
}

// assertExactLawIDs fails t if got is not exactly want (set equality, not containment -- AC-2):
// a non-conforming adapter that trips one extra, unrelated law is itself a spec defect for that
// case. Shared by every individually-named law test below and by
// TestContractSuite_RejectsNonConformingAdapters's own per-case loop's reasoning (that loop
// predates this helper and is left as-is; this helper exists so the sixteen named tests below
// don't each hand-roll the same two loops).
func assertExactLawIDs(t *testing.T, label string, got, want map[string]bool) {
	t.Helper()
	for id := range want {
		if !got[id] {
			t.Errorf("%s: expected law %s to be recorded, but it was not -- recorded set was %v, "+
				"want exactly %v", label, id, sortedLawIDs(got), sortedLawIDs(want))
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("%s: law %s was recorded but not expected (set equality, not containment -- "+
				"AC-2) -- recorded set was %v, want exactly %v", label, id, sortedLawIDs(got), sortedLawIDs(want))
		}
	}
}

// recordedLawIDs runs newAdapter through RunAdapterContract with a fresh lawRecorder and
// returns the set of law ids it recorded.
func recordedLawIDs(newAdapter func() submission.Adapter) map[string]bool {
	rec := &lawRecorder{}
	RunAdapterContract(rec, newAdapter)
	return rec.lawIDs()
}

// --- L01: Name()/Version() returns "" -----------------------------------------------------

// emptyNameAdapter (L01): Name() always returns "". Version() and everything else delegate to
// the embedded, well-formed reference adapter, so this case trips exactly {L01} -- Name()
// being constant (if blank) never breaks L02's stability checks, which only compare values for
// equality, not for non-emptiness.
type emptyNameAdapter struct{ *refAdapter }

func (a *emptyNameAdapter) Name() string { return "" }

func newEmptyNameAdapter() submission.Adapter {
	return &emptyNameAdapter{refAdapter: newBaseRefAdapter()}
}

// emptyVersionAdapter (L01): Version()'s twin of emptyNameAdapter.
type emptyVersionAdapter struct{ *refAdapter }

func (a *emptyVersionAdapter) Version() string { return "" }

func newEmptyVersionAdapter() submission.Adapter {
	return &emptyVersionAdapter{refAdapter: newBaseRefAdapter()}
}

// --- L02: Name()/Version() must be stable -------------------------------------------------

// driftingVersionAdapter (L02): Version() returns a new value on every call, on every instance
// -- violating L02's same-instance-repeated-call stability check the moment RunAdapterContract
// calls Version() twice in a row on instance-1 (contract_test.go's version1/version1Repeat).
// Name() and everything else delegate to the embedded, well-formed reference adapter.
type driftingVersionAdapter struct {
	*refAdapter
	calls int
}

func (a *driftingVersionAdapter) Version() string {
	a.calls++
	return fmt.Sprintf("v-drift-%d", a.calls)
}

func newDriftingVersionAdapter() submission.Adapter {
	return &driftingVersionAdapter{refAdapter: newBaseRefAdapter()}
}

// --- L03: Transform must be deterministic -------------------------------------------------

// l03Sequence is a package-level, monotonically increasing counter that
// nonDeterministicTransformAdapter appends to every Transform call's output. Deliberately
// shared ACROSS instances, not per-instance: RunAdapterContract drives L03's cross-instance
// comparison by calling Transform once on each of two fresh instances (a1, a2) for the same
// corpus input, and a per-instance counter would restart at zero on each fresh instance and
// tick in lockstep, never actually diverging. A single shared counter guarantees the two calls
// always disagree. Read/written only from this single-goroutine test binary (RunAdapterContract
// never uses t.Parallel), so a plain int suffices -- no locking, no atomic.
var l03Sequence int

// nonDeterministicTransformAdapter (L03): Transform delegates to the embedded, well-formed
// reference adapter for the actual marshal (so it stays pure with respect to c -- never trips
// L04) and then appends l03Sequence, guaranteeing two fresh instances given the identical
// Canonical input produce different, non-empty Wire bytes -- exactly the non-determinism L03
// exists to catch, and nothing else (the appended suffix never turns a non-empty Wire empty, so
// L05 is untouched).
type nonDeterministicTransformAdapter struct{ *refAdapter }

func (a *nonDeterministicTransformAdapter) Transform(ctx context.Context, c submission.Canonical) (submission.Wire, error) {
	w, err := a.refAdapter.Transform(ctx, c)
	if err != nil {
		return w, err
	}
	l03Sequence++
	return submission.Wire(fmt.Sprintf("%s|seq=%d", w, l03Sequence)), nil
}

func newNonDeterministicTransformAdapter() submission.Adapter {
	return &nonDeterministicTransformAdapter{refAdapter: newBaseRefAdapter()}
}

// --- L04: Transform must not mutate its Canonical argument ---------------------------------

// mutatingTransformAdapter (L04): writes through c.Supplier.TIN when it is non-nil, before
// delegating to the embedded reference adapter's own (otherwise pure) marshal. Nil-guarded so
// it never touches the three corpus cases whose Supplier.TIN is nil ("minimal", "no-lines",
// "zero" -- contract_test.go:101-105): an unconditional write there would panic, and
// callTransform's recover() would misattribute that panic to L15, contaminating {L04} into
// {L04, L15}. The write reaches through canonicalCorpus's own *string (Party.TIN), which is
// package-level, shared-across-the-binary state -- see restoreL04Corpus below for the required
// cleanup.
type mutatingTransformAdapter struct{ *refAdapter }

func (a *mutatingTransformAdapter) Transform(ctx context.Context, c submission.Canonical) (submission.Wire, error) {
	if c.Supplier.TIN != nil {
		*c.Supplier.TIN = "MUTATED-BY-L04"
	}
	return a.refAdapter.Transform(ctx, c)
}

func newMutatingTransformAdapter() submission.Adapter {
	return &mutatingTransformAdapter{refAdapter: newBaseRefAdapter()}
}

// restoreL04Corpus undoes mutatingTransformAdapter's writes through canonicalCorpus's
// Supplier.TIN pointers, restoring each of the three non-nil-TIN corpus cases' original literal
// value ("full" -> "SUP-TIN-1", "all-nil-money" -> "SUP-TIN-2", "multi-byte-long-text" ->
// "SUP-TIN-3"; contract_test.go:116,152,172). canonicalCorpus is package-level and shared by
// the whole test binary; RunAdapterContract iterates it directly and takes no corpus parameter,
// so this is the only remedy available from this file -- editing contract_test.go to inject a
// private copy is out of scope. Every call site that exercises newMutatingTransformAdapter
// registers this via t.Cleanup, so it runs regardless of pass/fail and whichever test the
// binary runs next -- in this file or any other -- observes a pristine corpus.
func restoreL04Corpus() {
	for _, tc := range canonicalCorpus {
		switch tc.name {
		case "full":
			if tc.c.Supplier.TIN != nil {
				*tc.c.Supplier.TIN = "SUP-TIN-1"
			}
		case "all-nil-money":
			if tc.c.Supplier.TIN != nil {
				*tc.c.Supplier.TIN = "SUP-TIN-2"
			}
		case "multi-byte-long-text":
			if tc.c.Supplier.TIN != nil {
				*tc.c.Supplier.TIN = "SUP-TIN-3"
			}
		}
	}
}

// --- L05: Transform's Wire/error pair must be mutually exclusive ---------------------------

// mismatchedWireErrorAdapter (L05): Transform returns a Wire/error pair that violates the law's
// mutual-exclusivity rule in one of two ways, selected by nonEmptyWireWithErr -- (true) a
// non-empty Wire alongside a non-nil error, or (false) an empty Wire alongside a nil error.
// Deterministic on every call (same pair every time), so it never also trips L03, and it never
// reads or writes c, so it never trips L04.
type mismatchedWireErrorAdapter struct {
	*refAdapter
	nonEmptyWireWithErr bool
}

func (a *mismatchedWireErrorAdapter) Transform(context.Context, submission.Canonical) (submission.Wire, error) {
	if a.nonEmptyWireWithErr {
		return submission.Wire("l05-non-empty-wire"), errors.New("l05 injected transform error")
	}
	return submission.Wire{}, nil
}

func newMismatchedWireErrorAdapter(nonEmptyWireWithErr bool) func() submission.Adapter {
	return func() submission.Adapter {
		return &mismatchedWireErrorAdapter{refAdapter: newBaseRefAdapter(), nonEmptyWireWithErr: nonEmptyWireWithErr}
	}
}

// --- L13: Submit must echo the idemKey it was actually called with, never mint its own -----

// mintedIdemKeyAdapter (L13): Submit delegates to the embedded reference adapter for the Result
// and the ctx-cancellation check FIRST (so L16 stays intact for the cancelled-context call --
// the same reasoning as the story's L06-must-reuse-newRefFactory-verbatim note applies to any
// Submit override), then overwrites the Idempotency-Key header with a value that is never the
// idemKey argument it was actually called with.
type mintedIdemKeyAdapter struct{ *refAdapter }

func (a *mintedIdemKeyAdapter) Submit(ctx context.Context, w submission.Wire, idemKey string) (submission.Result, submission.Evidence) {
	result, ev := a.refAdapter.Submit(ctx, w, idemKey)
	minted := http.Header{}
	minted.Set("Idempotency-Key", "minted-not-the-real-key")
	ev.RequestHeaders = minted
	return result, ev
}

func newMintedIdemKeyAdapter() submission.Adapter {
	return &mintedIdemKeyAdapter{refAdapter: newBaseRefAdapter()}
}

// --- L14: Poll must not panic, even on a Ref this adapter never issued ---------------------

// panicsOnUnissuedRefAdapter (L14): well-formed for the one Ref it "issued" (issuedRef), but
// panics on any other Ref -- including RunAdapterContract's unissued-ref probe. Honours ctx
// cancellation FIRST, exactly like the embedded reference adapter's own Poll, so the
// cancelled-context Poll call (which uses yet another Ref this adapter never issued) returns
// Retryable rather than panicking and tripping L16 alongside L14.
type panicsOnUnissuedRefAdapter struct {
	*refAdapter
	issuedRef submission.Ref
}

func (a *panicsOnUnissuedRefAdapter) Poll(ctx context.Context, ref submission.Ref) (submission.Result, submission.Evidence) {
	if ctx.Err() == nil && ref != a.issuedRef {
		panic("panicsOnUnissuedRefAdapter: Poll called with an unissued ref: " + string(ref))
	}
	return a.refAdapter.Poll(ctx, ref)
}

func newPanicsOnUnissuedRefAdapter() submission.Adapter {
	return &panicsOnUnissuedRefAdapter{
		refAdapter: newBaseRefAdapter(),
		issuedRef:  submission.Ref("l14-the-one-ref-this-adapter-issued"),
	}
}

// --- L15: Name/Version/Transform/Submit must never panic -----------------------------------

// panicsInSubmitAdapter (L15): Submit panics unconditionally, on every call. Poll is left
// delegating to the embedded reference adapter, so this adapter's panic surface never reaches
// Poll -- L14 and L15 partition the panic surface, and an adapter demonstrating one must not
// also demonstrate the other.
type panicsInSubmitAdapter struct{ *refAdapter }

func (a *panicsInSubmitAdapter) Submit(context.Context, submission.Wire, string) (submission.Result, submission.Evidence) {
	panic("panicsInSubmitAdapter: Submit always panics")
}

func newPanicsInSubmitAdapter() submission.Adapter {
	return &panicsInSubmitAdapter{refAdapter: newBaseRefAdapter()}
}

// --- L16: an already-cancelled context must force Retryable + ReachedWire false -------------

// ignoresCancelledContextAdapter (L16): Submit and Poll both ignore ctx entirely and always
// return a well-formed Accepted with ReachedWire true -- including when handed an
// already-cancelled context, which is exactly the violation L16 exists to catch. Everything
// else about the returned Result/Evidence is deliberately well-formed (valid IRN, correct
// Idempotency-Key echo when Submit is given one, non-negative latency) so this case trips L16
// alone.
type ignoresCancelledContextAdapter struct{ *refAdapter }

func (a *ignoresCancelledContextAdapter) Submit(_ context.Context, _ submission.Wire, idemKey string) (submission.Result, submission.Evidence) {
	hdr := http.Header{}
	if idemKey != "" {
		hdr.Set("Idempotency-Key", idemKey)
	}
	status := 200
	latency := 5
	return submission.Accepted{IRN: "IRN-IGNORES-CANCELLED-CTX"}, submission.Evidence{
		RequestHeaders:  hdr,
		ReachedWire:     true,
		HTTPStatus:      &status,
		LatencyMS:       &latency,
		ResponseHeaders: http.Header{},
	}
}

func (a *ignoresCancelledContextAdapter) Poll(_ context.Context, _ submission.Ref) (submission.Result, submission.Evidence) {
	status := 200
	latency := 5
	return submission.Accepted{IRN: "IRN-IGNORES-CANCELLED-CTX"}, submission.Evidence{
		ReachedWire:     true,
		HTTPStatus:      &status,
		LatencyMS:       &latency,
		ResponseHeaders: http.Header{},
	}
}

func newIgnoresCancelledContextAdapter() submission.Adapter {
	return &ignoresCancelledContextAdapter{refAdapter: newBaseRefAdapter()}
}

// redCases is the RED table: one entry per law demonstrating a non-conforming adapter.
// Deliberately EMPTY in this stage (M5-02-07 Stage 2) -- filling it with sixteen entries, one
// per law in AllLaws, is Stage 3 (the executor)'s sole deliverable. See this story's Technical
// Design for the full per-law spec (test name, given/when/then, and the two disjointness rules
// this table must respect: L06 short-circuits before L07-L10 are ever evaluated; L14 owns
// Poll's panic surface, L15 owns Name/Version/Transform/Submit's -- neither may fire the
// other).
var redCases = []redCase{
	{
		lawID:       "L01",
		description: "Name() returns the empty string",
		newAdapter:  newEmptyNameAdapter,
		expected:    lawSet("L01"),
	},
	{
		lawID:       "L01",
		description: "Version() returns the empty string",
		newAdapter:  newEmptyVersionAdapter,
		expected:    lawSet("L01"),
	},
	{
		lawID:       "L02",
		description: "Version() returns a new value on every call",
		newAdapter:  newDriftingVersionAdapter,
		expected:    lawSet("L02"),
	},
	{
		lawID:       "L03",
		description: "Transform appends a sequence counter, breaking determinism",
		newAdapter:  newNonDeterministicTransformAdapter,
		expected:    lawSet("L03"),
	},
	{
		lawID:       "L04",
		description: "Transform writes through Canonical.Supplier.TIN",
		newAdapter:  newMutatingTransformAdapter,
		expected:    lawSet("L04"),
		cleanup:     restoreL04Corpus,
	},
	{
		lawID:       "L05",
		description: "Transform returns a non-empty Wire alongside a non-nil error",
		newAdapter:  newMismatchedWireErrorAdapter(true),
		expected:    lawSet("L05"),
	},
	{
		lawID:       "L05",
		description: "Transform returns an empty Wire alongside a nil error",
		newAdapter:  newMismatchedWireErrorAdapter(false),
		expected:    lawSet("L05"),
	},
	{
		lawID:       "L06",
		description: "Submit returns a nil Result",
		newAdapter:  newRefFactory(nil, wellFormedEvidence()),
		expected:    lawSet("L06"),
	},
	{
		lawID:       "L06",
		description: "Submit returns the pointer variant &Accepted{...}",
		newAdapter:  newRefFactory(&submission.Accepted{IRN: "x"}, wellFormedEvidence()),
		expected:    lawSet("L06"),
	},
	{
		lawID:       "L07",
		description: "Submit returns Accepted with an empty IRN",
		newAdapter:  newRefFactory(submission.Accepted{IRN: ""}, wellFormedEvidence()),
		expected:    lawSet("L07"),
	},
	{
		lawID:       "L07",
		description: "Submit returns Accepted with a blank (whitespace-only) IRN",
		newAdapter:  newRefFactory(submission.Accepted{IRN: "   "}, wellFormedEvidence()),
		expected:    lawSet("L07"),
	},
	{
		lawID:       "L08",
		description: "Submit returns Rejected with no Reasons",
		newAdapter:  newRefFactory(submission.Rejected{}, wellFormedEvidence()),
		expected:    lawSet("L08"),
	},
	{
		lawID:       "L09",
		description: "Submit returns Pending with an empty Ref",
		newAdapter:  newRefFactory(submission.Pending{PollAfter: time.Now().Add(time.Hour), Ref: ""}, wellFormedEvidence()),
		expected:    lawSet("L09"),
	},
	{
		lawID:       "L10",
		description: "Submit returns Retryable with a nil Err",
		newAdapter:  newRefFactory(submission.Retryable{}, wellFormedEvidence()),
		expected:    lawSet("L10"),
	},
	{
		lawID:       "L11",
		description: "Evidence has ReachedWire false but a non-nil HTTPStatus",
		newAdapter: newRefFactory(submission.Accepted{IRN: "IRN-L11"}, submission.Evidence{
			ReachedWire: false,
			HTTPStatus:  intPtr(200),
		}),
		expected: lawSet("L11"),
	},
	{
		lawID:       "L12",
		description: "Evidence has a negative LatencyMS",
		newAdapter: newRefFactory(submission.Accepted{IRN: "IRN-L12"}, submission.Evidence{
			ReachedWire: true,
			HTTPStatus:  intPtr(200),
			LatencyMS:   intPtr(-1),
		}),
		expected: lawSet("L12"),
	},
	{
		lawID:       "L13",
		description: "Submit echoes a minted Idempotency-Key instead of the one it was called with",
		newAdapter:  newMintedIdemKeyAdapter,
		expected:    lawSet("L13"),
	},
	{
		lawID:       "L14",
		description: "Poll panics on a Ref it never issued",
		newAdapter:  newPanicsOnUnissuedRefAdapter,
		expected:    lawSet("L14"),
	},
	{
		lawID:       "L15",
		description: "Submit panics unconditionally",
		newAdapter:  newPanicsInSubmitAdapter,
		expected:    lawSet("L15"),
	},
	{
		lawID:       "L16",
		description: "Submit and Poll ignore an already-cancelled context",
		newAdapter:  newIgnoresCancelledContextAdapter,
		expected:    lawSet("L16"),
	},
}

// TestContractSuite_RejectsNonConformingAdapters (AC-1, AC-2) runs every entry in redCases
// through RunAdapterContract with a fresh lawRecorder and asserts the recorded law-id set
// equals that case's expected set EXACTLY -- set equality, not containment (AC-2): a
// non-conforming adapter that additionally trips an extra, unrelated law is itself a spec
// defect and must fail this test, not pass it loosely.
//
// Passes vacuously right now (Stage 2): redCases is empty, so this loop runs zero subtests.
// That is expected -- it is TestAllLaws_EveryLawHasADemonstratedRed below, not this test, that
// carries this stage's RED. Stage 3 fills the table; from that point on, this test is what
// actually exercises every case.
func TestContractSuite_RejectsNonConformingAdapters(t *testing.T) {
	for _, tc := range redCases {
		tc := tc
		t.Run(tc.lawID+"/"+tc.description, func(t *testing.T) {
			if tc.cleanup != nil {
				t.Cleanup(tc.cleanup)
			}

			rec := &lawRecorder{}
			RunAdapterContract(rec, tc.newAdapter)
			got := rec.lawIDs()

			for id := range tc.expected {
				if !got[id] {
					t.Errorf("case %s (%s): expected law %s to be recorded, but it was not -- "+
						"recorded set was %v, want exactly %v",
						tc.lawID, tc.description, id, sortedLawIDs(got), sortedLawIDs(tc.expected))
				}
			}
			for id := range got {
				if !tc.expected[id] {
					t.Errorf("case %s (%s): law %s was recorded but not expected (set equality, "+
						"not containment -- AC-2) -- recorded set was %v, want exactly %v",
						tc.lawID, tc.description, id, sortedLawIDs(got), sortedLawIDs(tc.expected))
				}
			}
		})
	}
}

// TestAllLaws_EveryLawHasADemonstratedRed (AC-4, AC-7 -- Core AC-5's executable form): every law
// id in AllLaws must appear in at least one redCases entry's expected set. FAILS right now
// (Stage 2): redCases is empty, so nothing is demonstrated, and the loop below names every one
// of the sixteen ids as orphaned -- one t.Errorf per missing id (mirrors
// TestAllLaws_IdsAreUniqueAndUsed's own per-id loop, contract_test.go:637-658), never one
// combined message that could silently print an empty (and therefore misleadingly reassuring)
// list.
func TestAllLaws_EveryLawHasADemonstratedRed(t *testing.T) {
	demonstrated := make(map[string]bool)
	for _, tc := range redCases {
		for id := range tc.expected {
			demonstrated[id] = true
		}
	}

	for _, id := range AllLaws {
		if !demonstrated[id] {
			t.Errorf("law %s in AllLaws has no demonstrated RED: no redCases entry's expected "+
				"set includes it (Core AC-5: every law enforced must be demonstrated failing "+
				"against a deliberately non-conforming adapter)", id)
		}
	}
}

// The sixteen tests below are the individually-named per-law RED specs the parent story's Test
// Specs table calls for, alongside the table-driven TestContractSuite_RejectsNonConformingAdapters
// above -- both are required, neither substitutes for the other. Each reuses the exact same
// factory function(s) redCases uses, so there is one definition of each non-conforming adapter,
// not two drifting copies.

// TestContractSuite_RejectsEmptyName (AC-1, L01): an adapter whose Name() returns "", and
// separately one whose Version() returns "", each record exactly {L01}.
func TestContractSuite_RejectsEmptyName(t *testing.T) {
	assertExactLawIDs(t, "Name() empty", recordedLawIDs(newEmptyNameAdapter), lawSet("L01"))
	assertExactLawIDs(t, "Version() empty", recordedLawIDs(newEmptyVersionAdapter), lawSet("L01"))
}

// TestContractSuite_RejectsDriftingIdentity (AC-1, L02): an adapter whose Version() returns a
// new value on every call records exactly {L02}.
func TestContractSuite_RejectsDriftingIdentity(t *testing.T) {
	assertExactLawIDs(t, "drifting Version()", recordedLawIDs(newDriftingVersionAdapter), lawSet("L02"))
}

// TestContractSuite_RejectsNonDeterministicTransform (AC-1, L03): an adapter whose Transform
// appends a counter to its otherwise-pure output records exactly {L03}.
func TestContractSuite_RejectsNonDeterministicTransform(t *testing.T) {
	assertExactLawIDs(t, "non-deterministic Transform", recordedLawIDs(newNonDeterministicTransformAdapter), lawSet("L03"))
}

// TestContractSuite_RejectsInputMutatingTransform (AC-1, L04): an adapter whose Transform
// writes through Canonical.Supplier.TIN records exactly {L04}. Registers restoreL04Corpus via
// t.Cleanup itself (this test does not go through TestContractSuite_RejectsNonConformingAdapters'
// subtest loop, which is what registers redCases' own L04 entry's cleanup), so the corpus is
// pristine again before any later test in the binary runs.
func TestContractSuite_RejectsInputMutatingTransform(t *testing.T) {
	t.Cleanup(restoreL04Corpus)
	assertExactLawIDs(t, "mutating Transform", recordedLawIDs(newMutatingTransformAdapter), lawSet("L04"))
}

// TestContractSuite_RejectsMismatchedWireErrorPair (AC-1, AC-4, L05): two independent cases --
// a non-empty Wire returned alongside a non-nil error, and an empty Wire returned alongside a
// nil error -- each record exactly {L05}.
func TestContractSuite_RejectsMismatchedWireErrorPair(t *testing.T) {
	assertExactLawIDs(t, "non-empty Wire with error",
		recordedLawIDs(newMismatchedWireErrorAdapter(true)), lawSet("L05"))
	assertExactLawIDs(t, "empty Wire with nil error",
		recordedLawIDs(newMismatchedWireErrorAdapter(false)), lawSet("L05"))
}

// TestContractSuite_RejectsNilResult (AC-1, AC-3, L06): a nil Result, and separately the
// pointer variant &Accepted{...} (isResult() has a value receiver, so *Accepted satisfies
// Result too and falls to CheckResult's default arm exactly like nil), each record exactly
// {L06} -- and, per disjointness rule 2, neither case may also record any of L07-L10, proving
// CheckResult's short-circuit fires for both.
func TestContractSuite_RejectsNilResult(t *testing.T) {
	cases := []struct {
		name       string
		newAdapter func() submission.Adapter
	}{
		{"nil Result", newRefFactory(nil, wellFormedEvidence())},
		{"pointer Accepted Result", newRefFactory(&submission.Accepted{IRN: "x"}, wellFormedEvidence())},
	}
	for _, tc := range cases {
		got := recordedLawIDs(tc.newAdapter)
		assertExactLawIDs(t, tc.name, got, lawSet("L06"))
		for _, id := range []string{"L07", "L08", "L09", "L10"} {
			if got[id] {
				t.Errorf("%s: law %s was recorded, want CheckResult's short-circuit on a nil/"+
					"non-value Result to skip L07-L10 entirely once L06 has already failed", tc.name, id)
			}
		}
	}
}

// TestContractSuite_RejectsAcceptedWithoutIRN (AC-1, L07): Accepted with an empty IRN, and
// separately Accepted with a blank (whitespace-only) IRN, each record exactly {L07}.
func TestContractSuite_RejectsAcceptedWithoutIRN(t *testing.T) {
	assertExactLawIDs(t, "empty IRN",
		recordedLawIDs(newRefFactory(submission.Accepted{IRN: ""}, wellFormedEvidence())), lawSet("L07"))
	assertExactLawIDs(t, "blank IRN",
		recordedLawIDs(newRefFactory(submission.Accepted{IRN: "   "}, wellFormedEvidence())), lawSet("L07"))
}

// TestContractSuite_RejectsEmptyRejection (AC-1, L08): Rejected with no Reasons records exactly
// {L08}.
func TestContractSuite_RejectsEmptyRejection(t *testing.T) {
	assertExactLawIDs(t, "empty Rejected.Reasons",
		recordedLawIDs(newRefFactory(submission.Rejected{}, wellFormedEvidence())), lawSet("L08"))
}

// TestContractSuite_RejectsPendingWithoutRef (AC-1, L09): Pending with a valid PollAfter but an
// empty Ref records exactly {L09}.
func TestContractSuite_RejectsPendingWithoutRef(t *testing.T) {
	pending := submission.Pending{PollAfter: time.Now().Add(time.Hour), Ref: ""}
	assertExactLawIDs(t, "empty Pending.Ref",
		recordedLawIDs(newRefFactory(pending, wellFormedEvidence())), lawSet("L09"))
}

// TestContractSuite_RejectsRetryableWithNilError (AC-1, L10): Retryable with a nil Err records
// exactly {L10}.
func TestContractSuite_RejectsRetryableWithNilError(t *testing.T) {
	assertExactLawIDs(t, "nil Retryable.Err",
		recordedLawIDs(newRefFactory(submission.Retryable{}, wellFormedEvidence())), lawSet("L10"))
}

// TestContractSuite_RejectsResponseWithoutReachedWire (AC-1, L11): Evidence with ReachedWire
// false but a non-nil HTTPStatus records exactly {L11}.
func TestContractSuite_RejectsResponseWithoutReachedWire(t *testing.T) {
	ev := submission.Evidence{ReachedWire: false, HTTPStatus: intPtr(200)}
	assertExactLawIDs(t, "ReachedWire false with HTTPStatus set",
		recordedLawIDs(newRefFactory(submission.Accepted{IRN: "IRN-L11"}, ev)), lawSet("L11"))
}

// TestContractSuite_RejectsNegativeLatency (AC-1, L12): Evidence with LatencyMS -1 records
// exactly {L12}.
func TestContractSuite_RejectsNegativeLatency(t *testing.T) {
	ev := submission.Evidence{ReachedWire: true, HTTPStatus: intPtr(200), LatencyMS: intPtr(-1)}
	assertExactLawIDs(t, "negative LatencyMS",
		recordedLawIDs(newRefFactory(submission.Accepted{IRN: "IRN-L12"}, ev)), lawSet("L12"))
}

// TestContractSuite_RejectsMintedIdempotencyKey (AC-1, L13): an adapter that echoes a minted
// Idempotency-Key instead of the one passed to Submit records exactly {L13}.
func TestContractSuite_RejectsMintedIdempotencyKey(t *testing.T) {
	assertExactLawIDs(t, "minted Idempotency-Key",
		recordedLawIDs(newMintedIdemKeyAdapter), lawSet("L13"))
}

// TestContractSuite_RejectsPollOnUnissuedRef (AC-1, AC-3, AC-6, L14): an adapter well-formed
// for the Ref it issued, but which panics on a Ref it never issued, records exactly {L14} --
// never {L15}, proving the L14/L15 panic-surface partition holds -- and the run completes
// (the panic is caught by RunAdapterContract's own recover(), never escaping to abort the test
// binary).
func TestContractSuite_RejectsPollOnUnissuedRef(t *testing.T) {
	assertExactLawIDs(t, "Poll panics on an unissued ref",
		recordedLawIDs(newPanicsOnUnissuedRefAdapter), lawSet("L14"))
}

// TestContractSuite_RejectsPanickingAdapter (AC-3, AC-6, L15): an adapter whose Submit panics
// unconditionally records exactly {L15} -- never {L14} -- and the run completes.
func TestContractSuite_RejectsPanickingAdapter(t *testing.T) {
	assertExactLawIDs(t, "Submit panics unconditionally",
		recordedLawIDs(newPanicsInSubmitAdapter), lawSet("L15"))
}

// TestContractSuite_RejectsVerdictOnCancelledContext (AC-1, L16): an adapter that ignores ctx
// and returns a well-formed Accepted regardless of cancellation records exactly {L16}.
func TestContractSuite_RejectsVerdictOnCancelledContext(t *testing.T) {
	assertExactLawIDs(t, "ignores cancelled context",
		recordedLawIDs(newIgnoresCancelledContextAdapter), lawSet("L16"))
}
