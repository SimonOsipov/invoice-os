// sweep_test.go holds the acceptance tests for ShouldReap and
// ParsePRState (sweep.go), transcribed from the architect's validated
// Test Specs table for task-150 (M4-23-06, Test-first: yes) and authored
// against panicking stubs BEFORE the implementation exists, per that
// table's numbering (specs 1-15; specs 16-18, the sweep-decide CLI
// contract, live in main_test.go instead, per the table's own File
// column).
//
// These are pure unit tests: no network, no filesystem, no os.Getenv --
// just in-memory values -- so they run in the existing Go CI job
// unconditionally, with no new test runner or config.
//
// Coverage -- task-150's validated Test Specs table (12 inherited: 8
// unchanged, 4 corrected; 6 added):
//  1. TestShouldReapClosedPR                          -- AC-1, unchanged.
//  2. TestShouldReapMergedPR                          -- AC-1, unchanged.
//  3. TestShouldNotReapOpenPR                         -- AC-2, unchanged.
//  4. TestShouldNotReapUnknownStateFailsSafe          -- AC-2, unchanged.
//  5. TestShouldNotReapUnrecognisedStateFailsSafe     -- AC-2, unchanged; PRState(99) also pins PRState as an int-kind type (a non-int type wouldn't compile here).
//  6. TestShouldNotReapUnparseableName                -- AC-3, unchanged.
//  7. TestShouldNotReapNonEphemeral                   -- AC-4, unchanged.
//  8. TestShouldNeverReapDevelopment                  -- AC-5, corrected: asserts the two reasons DIFFER, not just false twice -- that's what evidences two independent guards.
//  9. TestShouldReapReasonIsExactPerBranch             -- AC-6, corrected: exact reason per branch via a wantReason column, not just non-emptiness (which a single shared reason string would also satisfy).
//  10. TestParsePRStateMapsMalformedOutputToUnknown    -- AC-8, corrected: both ParsePRState args pinned, rc=0 so this tests the parser rather than the exit-code short-circuit.
//  11. TestParsePRStateMapsGhFailureToUnknown          -- AC-8, corrected: both args pinned; includes the reap-looking-stdout-at-rc=1 case (proves rc wins) and the measured nonexistent-PR shape.
//  12. TestParsePRStateMapsRealGhPayloads              -- AC-8, corrected: both args pinned; trailing-newline variants ($(...)  strips it, a file/pipe capture would not).
//  13. TestPRStateUnknownIsZeroValue                   -- AC-2, added: without this, reordering the iota block would silently make the zero value Open or Closed.
//  14. TestShouldReapIsTotalOverEveryStateAndFlag       -- AC-1/AC-2, added: closed-world cross-product -- true in EXACTLY 2 of the 36 cells, not just 2 positive examples.
//  15. TestParsePRStateNeverYieldsAReapableDecision     -- AC-8, added: composes ParsePRState -> ShouldReap. Nothing in the inherited 12 did this, and a fail-safe branch its real caller can never reach is not fail-safe.
//
// NOTE on spec 13 (TestPRStateUnknownIsZeroValue): this test calls no
// function -- it only checks the const block's declared order, so it
// passed even against the pre-implementation stubs. That is intentional:
// it is a structural regression guard against a future reorder of the
// iota block, not a test of behavior.
package main

import (
	"fmt"
	"testing"
)

// TestShouldReapClosedPR: AC-1. A closed PR's environment is reaped.
func TestShouldReapClosedPR(t *testing.T) {
	reap, reason := ShouldReap("pr-42", true, PRStateClosed)
	if !reap {
		t.Errorf(`ShouldReap("pr-42", true, PRStateClosed) reap = false, want true`)
	}
	if reason != ReasonPRClosed {
		t.Errorf(`ShouldReap("pr-42", true, PRStateClosed) reason = %q, want %q`, reason, ReasonPRClosed)
	}
}

// TestShouldReapMergedPR: AC-1. A merged PR's environment is reaped.
func TestShouldReapMergedPR(t *testing.T) {
	reap, reason := ShouldReap("pr-42", true, PRStateMerged)
	if !reap {
		t.Errorf(`ShouldReap("pr-42", true, PRStateMerged) reap = false, want true`)
	}
	if reason != ReasonPRMerged {
		t.Errorf(`ShouldReap("pr-42", true, PRStateMerged) reason = %q, want %q`, reason, ReasonPRMerged)
	}
}

// TestShouldNotReapOpenPR: AC-2. An open PR's environment is never
// reaped -- only positive evidence of closed/merged reaps.
func TestShouldNotReapOpenPR(t *testing.T) {
	reap, reason := ShouldReap("pr-42", true, PRStateOpen)
	if reap {
		t.Errorf(`ShouldReap("pr-42", true, PRStateOpen) reap = true, want false`)
	}
	if reason != ReasonPROpen {
		t.Errorf(`ShouldReap("pr-42", true, PRStateOpen) reason = %q, want %q`, reason, ReasonPROpen)
	}
}

// TestShouldNotReapUnknownStateFailsSafe: AC-2. "We could not determine
// the PR's state" must never be treated as evidence to delete.
func TestShouldNotReapUnknownStateFailsSafe(t *testing.T) {
	reap, reason := ShouldReap("pr-42", true, PRStateUnknown)
	if reap {
		t.Errorf(`ShouldReap("pr-42", true, PRStateUnknown) reap = true, want false`)
	}
	if reason != ReasonPRStateUnknown {
		t.Errorf(`ShouldReap("pr-42", true, PRStateUnknown) reason = %q, want %q`, reason, ReasonPRStateUnknown)
	}
}

// TestShouldNotReapUnrecognisedStateFailsSafe: AC-2. A state outside the
// four declared values (e.g. a future addition ShouldReap's switch
// doesn't know about yet) must fall into a default arm that skips, never
// fall through to a reap. PRState(99) only compiles because PRState is
// an integer-kind type -- this spec also pins that constraint.
func TestShouldNotReapUnrecognisedStateFailsSafe(t *testing.T) {
	reap, reason := ShouldReap("pr-42", true, PRState(99))
	if reap {
		t.Errorf(`ShouldReap("pr-42", true, PRState(99)) reap = true, want false`)
	}
	if reason != ReasonUnrecognisedState {
		t.Errorf(`ShouldReap("pr-42", true, PRState(99)) reason = %q, want %q`, reason, ReasonUnrecognisedState)
	}
}

// TestShouldNotReapUnparseableName: AC-3. A name that doesn't parse as
// pr-<N> is never reaped, regardless of PR state.
func TestShouldNotReapUnparseableName(t *testing.T) {
	reap, reason := ShouldReap("my-scratch-env", true, PRStateClosed)
	if reap {
		t.Errorf(`ShouldReap("my-scratch-env", true, PRStateClosed) reap = true, want false`)
	}
	if reason != ReasonNotAPREnvironment {
		t.Errorf(`ShouldReap("my-scratch-env", true, PRStateClosed) reason = %q, want %q`, reason, ReasonNotAPREnvironment)
	}
}

// TestShouldNotReapNonEphemeral: AC-4. isEphemeral=false is a hard skip
// regardless of everything else.
func TestShouldNotReapNonEphemeral(t *testing.T) {
	reap, reason := ShouldReap("pr-42", false, PRStateClosed)
	if reap {
		t.Errorf(`ShouldReap("pr-42", false, PRStateClosed) reap = true, want false`)
	}
	if reason != ReasonNotEphemeral {
		t.Errorf(`ShouldReap("pr-42", false, PRStateClosed) reason = %q, want %q`, reason, ReasonNotEphemeral)
	}
}

// TestShouldNeverReapDevelopment: AC-5, corrected. "development" must
// never be reaped, via TWO INDEPENDENT guards -- the name-parse guard
// (development never parses as pr-<N>) and the ephemeral guard
// (development is never ephemeral). Asserting `false` for both cases,
// without more, would not distinguish "one guard checked twice" from
// "two guards" -- the reasons must differ.
func TestShouldNeverReapDevelopment(t *testing.T) {
	reap1, reason1 := ShouldReap("development", true, PRStateClosed)
	if reap1 {
		t.Errorf(`ShouldReap("development", true, PRStateClosed) reap = true, want false`)
	}
	if reason1 != ReasonNotAPREnvironment {
		t.Errorf(`ShouldReap("development", true, PRStateClosed) reason = %q, want %q`, reason1, ReasonNotAPREnvironment)
	}

	reap2, reason2 := ShouldReap("development", false, PRStateMerged)
	if reap2 {
		t.Errorf(`ShouldReap("development", false, PRStateMerged) reap = true, want false`)
	}
	if reason2 != ReasonNotEphemeral {
		t.Errorf(`ShouldReap("development", false, PRStateMerged) reason = %q, want %q`, reason2, ReasonNotEphemeral)
	}

	if reason1 == reason2 {
		t.Errorf(`ShouldReap skipped "development" via the same reason both times (%q) -- want two different guards per AC-5's "independently"`, reason1)
	}
}

// TestShouldReapReasonIsExactPerBranch: AC-6, corrected. Every branch's
// reason is checked against the SPECIFIC constant it should return, not
// merely checked for non-emptiness -- a single shared non-empty string
// returned from every branch would pass a non-emptiness-only check and
// defeat AC-6's actual purpose (an operator being able to tell which
// branch fired from the log line alone).
func TestShouldReapReasonIsExactPerBranch(t *testing.T) {
	cases := []struct {
		name        string
		envName     string
		isEphemeral bool
		state       PRState
		wantReap    bool
		wantReason  ReapReason
	}{
		{"closed", "pr-42", true, PRStateClosed, true, ReasonPRClosed},
		{"merged", "pr-42", true, PRStateMerged, true, ReasonPRMerged},
		{"open", "pr-42", true, PRStateOpen, false, ReasonPROpen},
		{"unknown state", "pr-42", true, PRStateUnknown, false, ReasonPRStateUnknown},
		{"unrecognised state", "pr-42", true, PRState(99), false, ReasonUnrecognisedState},
		{"unparseable name", "my-scratch-env", true, PRStateClosed, false, ReasonNotAPREnvironment},
		{"non-ephemeral", "pr-42", false, PRStateClosed, false, ReasonNotEphemeral},
		{"development via name guard", "development", true, PRStateClosed, false, ReasonNotAPREnvironment},
		{"development via ephemeral guard", "development", false, PRStateMerged, false, ReasonNotEphemeral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotReap, gotReason := ShouldReap(tc.envName, tc.isEphemeral, tc.state)
			if gotReap != tc.wantReap {
				t.Errorf("ShouldReap(%q, %v, state=%d) reap = %v, want %v", tc.envName, tc.isEphemeral, int(tc.state), gotReap, tc.wantReap)
			}
			if gotReason != tc.wantReason {
				t.Errorf("ShouldReap(%q, %v, state=%d) reason = %q, want %q", tc.envName, tc.isEphemeral, int(tc.state), gotReason, tc.wantReason)
			}
			if gotReason == "" {
				t.Errorf("ShouldReap(%q, %v, state=%d) returned an empty reason", tc.envName, tc.isEphemeral, int(tc.state))
			}
		})
	}
}

// TestParsePRStateMapsMalformedOutputToUnknown: AC-8, corrected. Both
// ParsePRState arguments are pinned (rc=0), so this exercises the JSON
// parsing itself, not the exit-code short-circuit covered separately by
// TestParsePRStateMapsGhFailureToUnknown.
func TestParsePRStateMapsMalformedOutputToUnknown(t *testing.T) {
	stdouts := []string{"", "not json", "{}", `{"state":"WEIRD"}`}
	for i, stdout := range stdouts {
		t.Run(fmt.Sprintf("case_%d_%q", i, stdout), func(t *testing.T) {
			got := ParsePRState(stdout, 0)
			if got != PRStateUnknown {
				t.Errorf("ParsePRState(%q, 0) = %d, want PRStateUnknown (%d)", stdout, int(got), int(PRStateUnknown))
			}
		})
	}
}

// TestParsePRStateMapsGhFailureToUnknown: AC-8, corrected. A non-zero gh
// exit code maps to Unknown WITHOUT inspecting stdout at all -- the
// reap-looking-stdout-at-rc=1 case is what proves the exit code wins
// over reap-looking content rather than merely being consistent with it.
func TestParsePRStateMapsGhFailureToUnknown(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		rc     int
	}{
		{"empty stdout, rc=1", "", 1},
		{"reap-looking stdout, rc=1 -- exit code must still win", `{"state":"MERGED"}`, 1},
		{"empty stdout, rc=127 (command not found)", "", 127},
		{"measured nonexistent-PR shape, rc=1", "GraphQL: Could not resolve to a PullRequest with the number of 99999", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParsePRState(tc.stdout, tc.rc)
			if got != PRStateUnknown {
				t.Errorf("ParsePRState(%q, %d) = %d, want PRStateUnknown (%d)", tc.stdout, tc.rc, int(got), int(PRStateUnknown))
			}
		})
	}
}

// TestParsePRStateMapsRealGhPayloads: AC-8, corrected. The measured,
// real `gh pr view --json state` shapes at rc=0, including trailing-
// newline variants -- $(...) strips a trailing newline, but a
// file/pipe capture would not, so both forms must parse identically.
// Case-sensitive by design: gh emits uppercase.
func TestParsePRStateMapsRealGhPayloads(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   PRState
	}{
		{"OPEN", `{"state":"OPEN"}`, PRStateOpen},
		{"CLOSED", `{"state":"CLOSED"}`, PRStateClosed},
		{"MERGED", `{"state":"MERGED"}`, PRStateMerged},
		{"OPEN with trailing newline", "{\"state\":\"OPEN\"}\n", PRStateOpen},
		{"CLOSED with trailing newline", "{\"state\":\"CLOSED\"}\n", PRStateClosed},
		{"MERGED with trailing newline", "{\"state\":\"MERGED\"}\n", PRStateMerged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParsePRState(tc.stdout, 0)
			if got != tc.want {
				t.Errorf("ParsePRState(%q, 0) = %d, want %d", tc.stdout, int(got), int(tc.want))
			}
		})
	}
}

// TestParsePRStateIsCaseSensitiveOnClosedAndMerged: AC-8, QA-added
// (post-implementation coverage hole). The inherited specs only probe
// case-sensitivity on the OPEN payload (see
// TestParsePRStateNeverYieldsAReapableDecision's `{"state":"open"}`
// case) -- but PRStateOpen never reaps either way, so a regression that
// made the match case-INSENSITIVE would sail through every existing
// test while accepting `{"state":"closed"}` / `{"state":"merged"}` as
// genuine reap evidence. Confirmed by mutation: switching
// ParsePRState's match to strings.ToUpper(payload.State) killed nothing
// in this file. This test pins the two branches that actually matter --
// the ones that authorize a delete -- directly, both at the parser and
// composed through ShouldReap.
func TestParsePRStateIsCaseSensitiveOnClosedAndMerged(t *testing.T) {
	cases := []string{
		`{"state":"closed"}`,
		`{"state":"Closed"}`,
		`{"state":"merged"}`,
		`{"state":"Merged"}`,
		`{"state":"MeRgEd"}`,
	}
	for _, stdout := range cases {
		t.Run(stdout, func(t *testing.T) {
			state := ParsePRState(stdout, 0)
			if state != PRStateUnknown {
				t.Errorf("ParsePRState(%q, 0) = %d, want PRStateUnknown (%d) -- lowercase/mixed-case must not be accepted", stdout, int(state), int(PRStateUnknown))
			}
			reap, reason := ShouldReap("pr-42", true, state)
			if reap {
				t.Errorf(`ShouldReap("pr-42", true, ParsePRState(%q, 0)) reaped -- a non-uppercase state must never authorize a delete`, stdout)
			}
			if reason != ReasonPRStateUnknown {
				t.Errorf(`ShouldReap("pr-42", true, ParsePRState(%q, 0)) reason = %q, want %q`, stdout, reason, ReasonPRStateUnknown)
			}
		})
	}
}

// TestPRStateUnknownIsZeroValue: AC-2, added. Without this pin, a future
// reorder of the iota block would silently make an uninitialised/
// forgotten PRState decode as Open or Closed instead of failing safe.
// See the file-level NOTE: this test calls no stubbed function, so it
// passes immediately even in this pre-implementation commit -- it is
// guarding the const declaration order, which is already correct.
func TestPRStateUnknownIsZeroValue(t *testing.T) {
	var s PRState
	if s != PRStateUnknown {
		t.Errorf("zero-value PRState = %d, want PRStateUnknown (%d)", int(s), int(PRStateUnknown))
	}
}

// TestShouldReapIsTotalOverEveryStateAndFlag: AC-1/AC-2, added. Turns
// "reap is true only for Closed/Merged" from two positive examples into
// a closed-world assertion: across the full cross-product of every
// declared PRState plus two unrecognised values, both ephemeral flags,
// and three representative names, `true` must occur in EXACTLY 2 cells
// -- ("pr-42", true, Closed) and ("pr-42", true, Merged) -- and every
// other cell must be false with a non-empty reason.
func TestShouldReapIsTotalOverEveryStateAndFlag(t *testing.T) {
	states := []PRState{PRStateUnknown, PRStateOpen, PRStateClosed, PRStateMerged, PRState(99), PRState(-1)}
	flags := []bool{true, false}
	names := []string{"pr-42", "development", "my-scratch-env"}

	trueCount := 0
	for _, name := range names {
		for _, flag := range flags {
			for _, state := range states {
				reap, reason := ShouldReap(name, flag, state)
				if reason == "" {
					t.Errorf("ShouldReap(%q, %v, state=%d) returned an empty reason", name, flag, int(state))
				}
				wantReap := name == "pr-42" && flag && (state == PRStateClosed || state == PRStateMerged)
				if reap != wantReap {
					t.Errorf("ShouldReap(%q, %v, state=%d) = %v, want %v", name, flag, int(state), reap, wantReap)
				}
				if reap {
					trueCount++
				}
			}
		}
	}
	if trueCount != 2 {
		t.Errorf("ShouldReap returned true in %d cells across the %dx%dx%d cross-product, want exactly 2", trueCount, len(names), len(flags), len(states))
	}
}

// TestParsePRStateNeverYieldsAReapableDecision: AC-8, added. Composes
// ParsePRState -> ShouldReap, which nothing in the inherited 12 specs
// did -- a fail-safe branch its real caller can never reach is not
// fail-safe. Runs a hostile stdout corpus (malformed JSON, an
// unrecognised state, a lowercase payload -- the parser is
// case-sensitive by design -- and well-formed payloads) across every gh
// exit code the workflow can observe, and asserts ShouldReap only ever
// reaps for the one combination that is actually well-formed evidence:
// a literal CLOSED or MERGED payload at rc=0.
func TestParsePRStateNeverYieldsAReapableDecision(t *testing.T) {
	hostileStdouts := []string{
		"",
		"not json",
		"{}",
		`{"state":"WEIRD"}`,
		`{"state":"open"}`, // lowercase -- must not be accepted
		`{"state":"OPEN"}`,
		`{"state":"CLOSED"}`,
		`{"state":"MERGED"}`,
		"GraphQL: Could not resolve to a PullRequest with the number of 99999",
	}
	rcs := []int{0, 1, 127}

	for _, stdout := range hostileStdouts {
		for _, rc := range rcs {
			t.Run(fmt.Sprintf("stdout=%q,rc=%d", stdout, rc), func(t *testing.T) {
				state := ParsePRState(stdout, rc)
				reap, reason := ShouldReap("pr-42", true, state)

				wellFormedReapable := rc == 0 && (stdout == `{"state":"CLOSED"}` || stdout == `{"state":"MERGED"}`)
				if reap != wellFormedReapable {
					t.Errorf("ShouldReap(\"pr-42\", true, ParsePRState(%q, %d)) = (%v, %q), want reap=%v", stdout, rc, reap, reason, wellFormedReapable)
				}
			})
		}
	}
}
