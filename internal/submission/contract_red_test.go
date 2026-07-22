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
	"sort"
	"testing"

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

// redCases is the RED table: one entry per law demonstrating a non-conforming adapter.
// Deliberately EMPTY in this stage (M5-02-07 Stage 2) -- filling it with sixteen entries, one
// per law in AllLaws, is Stage 3 (the executor)'s sole deliverable. See this story's Technical
// Design for the full per-law spec (test name, given/when/then, and the two disjointness rules
// this table must respect: L06 short-circuits before L07-L10 are ever evaluated; L14 owns
// Poll's panic surface, L15 owns Name/Version/Transform/Submit's -- neither may fire the
// other).
var redCases = []redCase{}

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
