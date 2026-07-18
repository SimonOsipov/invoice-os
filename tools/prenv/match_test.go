// match_test.go is the QA Mode A (RED) acceptance-test stage for task-132
// (M4-21-08, Test-first: yes): tests for Match (match.go), transcribed
// from the story's Test Specs table, authored BEFORE the real anchored-
// shape / numeric-compare logic exists. Match is currently a
// compile-only stub that always returns ("", ErrNoMatch) (see match.go's
// STUB NOTICE), so every test below that expects a non-empty resolved
// name, ErrAmbiguous, or one specific candidate to win over another is
// RED until M4-21-08's implementation lands.
//
// These are pure unit tests: no network, no database, no os.Getenv, no
// filesystem -- just in-memory []string literals -- so they run in the
// existing Go CI job unconditionally (AC-5), with no new test runner or
// config. That purity is also what lets this file construct ambiguous /
// adversarial candidate lists a live Railway environment could never be
// coaxed into producing on purpose (Decision [matcher-in-go], QA F5).
//
// Coverage -- Test Specs table (task-132):
//  1. TestMatchShortShape                    -- ["pr-1"], N=1 -> "pr-1".
//  2. TestMatchDoesNotPrefixMatchLongerNumber -- ["pr-1","pr-15"], N=1 -> "pr-1" only (the load-bearing false-positive guard).
//  3. TestMatchRepoPrefixedShape              -- ["invoice-os-pr-1"], N=1 -> matches.
//  4. TestMatchIgnoresUnrelatedNames           -- ["development","production","pr-7"]: N=7 -> "pr-7"; N=1 -> ErrNoMatch.
//  5. TestMatchEmptyListIsExplicitNoMatch      -- [] (and nil), N=1 -> ErrNoMatch, not ("", nil).
//  6. TestMatchAmbiguousIsExplicitError        -- ["pr-1","invoice-os-pr-1"], N=1 -> ErrAmbiguous, message names both.
//  7. TestMatchRejectsNumericLookalikes        -- ["pr-01","pr-1x","xpr-1","pr-1-old"], N=1 -> ErrNoMatch.
//
// Coverage -- additional boundary/adversarial cases folded into this RED
// stage (this subtask exists specifically to make exactly these cases
// testable per F5, so they belong in the AC-derived set here, not
// deferred to a later pass):
//  8. TestMatchRejectsShapeLookalikesWithoutNumericSuffix -- "pr-abc", "pr-" (non-numeric / empty suffix) -> ErrNoMatch.
//  9. TestMatchBoundaryHigherNumberMatchesItself -- N=15 against ["pr-1","pr-15"] -> "pr-15" only (mirror of #2: proves a numeric compare, not a hardcoded reject-15).
//  10. TestMatchLargePRNumber -- N=999999 against ["pr-999999"] -> matches; no fixed digit-width assumption.
//  11. TestMatchWhitespaceNeverMatches -- leading/trailing whitespace around an otherwise-valid name -> ErrNoMatch (anchored, not trimmed).
//  12. TestMatchCaseSensitiveShapeRejected -- ["PR-1"] (uppercase prefix) -> ErrNoMatch; not either documented shape.
//  13. TestMatchRepoPrefixShapeIsUnconstrained_PinnedBehavior -- ["production-pr-1"], N=1 -> matches: the <repo>-pr-<N> shape has an unconstrained prefix by design (F5); pinned as a deliberate, visible decision.
package main

import (
	"errors"
	"strings"
	"testing"
)

// matchCase is one row of a Match acceptance table: names/prNumber in,
// either a specific winning name (wantErr == nil) or a specific sentinel
// error (wantName == "") out.
type matchCase struct {
	name     string
	names    []string
	prNumber int
	wantName string
	wantErr  error // nil means "expect success"; checked via errors.Is otherwise.
}

func runMatchCases(t *testing.T, cases []matchCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Match(tc.names, tc.prNumber)

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Match(%v, %d) unexpected error: %v", tc.names, tc.prNumber, err)
				}
				if got != tc.wantName {
					t.Errorf("Match(%v, %d) = %q, want %q", tc.names, tc.prNumber, got, tc.wantName)
				}
				return
			}

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Match(%v, %d) err = %v, want errors.Is(err, %v)", tc.names, tc.prNumber, err, tc.wantErr)
			}
			if got != "" {
				t.Errorf("Match(%v, %d) = %q, want empty string on error", tc.names, tc.prNumber, got)
			}
		})
	}
}

// TestMatchShortShape: AC-2. The plain pr-<N> shape with a single
// candidate matches directly.
func TestMatchShortShape(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "single pr-1 candidate", names: []string{"pr-1"}, prNumber: 1, wantName: "pr-1"},
	})
}

// TestMatchDoesNotPrefixMatchLongerNumber: AC-2, the load-bearing test.
// pr-15 must never be treated as a prefix match for PR 1 -- naive
// matching (e.g. strings.HasPrefix(name, "pr-1")) would incorrectly
// resolve PR 1 against pr-15, and CI would then deploy to and reset
// ANOTHER PR's environment -- cross-PR corruption that presents as
// flakiness, the exact risk named in the story's Risks table (QA F5).
func TestMatchDoesNotPrefixMatchLongerNumber(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "pr-1 and pr-15 both present, N=1 resolves only pr-1", names: []string{"pr-1", "pr-15"}, prNumber: 1, wantName: "pr-1"},
	})
}

// TestMatchRepoPrefixedShape: AC-2. The <repo>-pr-<N> shape matches with
// a single candidate.
func TestMatchRepoPrefixedShape(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "repo-prefixed shape, single candidate", names: []string{"invoice-os-pr-1"}, prNumber: 1, wantName: "invoice-os-pr-1"},
	})
}

// TestMatchIgnoresUnrelatedNames: AC-2. Unrelated Railway environment
// names (development, production) never participate in matching, whether
// or not a real match exists among the rest of the list.
func TestMatchIgnoresUnrelatedNames(t *testing.T) {
	names := []string{"development", "production", "pr-7"}
	runMatchCases(t, []matchCase{
		{name: "N=7 matches pr-7, unrelated names ignored", names: names, prNumber: 7, wantName: "pr-7"},
		{name: "N=1 has no candidate among unrelated names", names: names, prNumber: 1, wantErr: ErrNoMatch},
	})
}

// TestMatchEmptyListIsExplicitNoMatch: AC-3. Zero candidates (an empty
// list, or a nil slice -- the plausible shape of a GraphQL response
// carrying no environments) must return the explicit ErrNoMatch sentinel,
// not a silent ("", nil).
func TestMatchEmptyListIsExplicitNoMatch(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "empty candidate list", names: []string{}, prNumber: 1, wantErr: ErrNoMatch},
		{name: "nil candidate list", names: nil, prNumber: 1, wantErr: ErrNoMatch},
	})
}

// TestMatchAmbiguousIsExplicitError: AC-3. Two names matching the SAME PR
// number (a genuine Railway naming collision -- both documented shapes
// present at once) must return ErrAmbiguous, not silently pick one, and
// the message must NAME every candidate so a human reading CI logs can
// tell which environments collided.
func TestMatchAmbiguousIsExplicitError(t *testing.T) {
	names := []string{"pr-1", "invoice-os-pr-1"}
	got, err := Match(names, 1)

	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Match(%v, 1) err = %v, want errors.Is(err, ErrAmbiguous)", names, err)
	}
	if got != "" {
		t.Errorf("Match(%v, 1) = %q, want empty string on ErrAmbiguous", names, got)
	}
	msg := err.Error()
	for _, candidate := range names {
		if !strings.Contains(msg, candidate) {
			t.Errorf("Match(%v, 1) error message %q does not name candidate %q", names, msg, candidate)
		}
	}
}

// TestMatchRejectsNumericLookalikes: AC-2. Strings that resemble the
// documented shapes but differ in the digit run (leading zero, trailing
// letter, wrong anchoring, trailing suffix) must never match -- pr-01 is
// a different whole number than 1, and none of the others is either
// documented shape at all.
func TestMatchRejectsNumericLookalikes(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "leading zero, trailing letter, unanchored prefix, trailing suffix", names: []string{"pr-01", "pr-1x", "xpr-1", "pr-1-old"}, prNumber: 1, wantErr: ErrNoMatch},
	})
}

// TestMatchRejectsShapeLookalikesWithoutNumericSuffix: additional shape
// lookalikes beyond the Test Specs table's numeric-suffix cases -- a
// non-numeric suffix (pr-abc) and a missing suffix entirely (pr-) are
// neither documented shape (the <N> capture must be a non-empty whole
// number), so both must be rejected the same way as the numeric
// lookalikes above.
func TestMatchRejectsShapeLookalikesWithoutNumericSuffix(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "non-numeric suffix and empty suffix", names: []string{"pr-abc", "pr-"}, prNumber: 1, wantErr: ErrNoMatch},
	})
}

// TestMatchBoundaryHigherNumberMatchesItself: mirror of
// TestMatchDoesNotPrefixMatchLongerNumber -- the numeric compare must be
// symmetric. Matching pr-15 for N=15 (and NOT pr-1) proves an anchored
// digit-run + numeric-compare approach, not a hardcoded "reject 15"
// special case that happens to make the N=1 test pass.
func TestMatchBoundaryHigherNumberMatchesItself(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "N=15 resolves only pr-15, not pr-1", names: []string{"pr-1", "pr-15"}, prNumber: 15, wantName: "pr-15"},
	})
}

// TestMatchLargePRNumber: no fixed digit-width assumption -- a large,
// realistic-future PR number must still match via the numeric compare,
// not a regexp accidentally anchored to a narrow digit count.
func TestMatchLargePRNumber(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "6-digit PR number", names: []string{"pr-999999"}, prNumber: 999999, wantName: "pr-999999"},
	})
}

// TestMatchWhitespaceNeverMatches: AC-2's "exact ... no prefix or
// substring matching anywhere" extends to whitespace -- a name is either
// the exact anchored shape or it is not a candidate at all. The GraphQL
// response is caller-controlled input, not something Match should trust
// implicitly by trimming it before comparing.
func TestMatchWhitespaceNeverMatches(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "leading space before pr-1", names: []string{" pr-1"}, prNumber: 1, wantErr: ErrNoMatch},
		{name: "trailing space after pr-1", names: []string{"pr-1 "}, prNumber: 1, wantErr: ErrNoMatch},
		{name: "trailing newline after pr-1", names: []string{"pr-1\n"}, prNumber: 1, wantErr: ErrNoMatch},
	})
}

// TestMatchCaseSensitiveShapeRejected: the two documented shapes use a
// lowercase "pr-" literal (Railway's actual naming, per F5) -- an
// uppercase-prefixed lookalike is not either documented shape and must be
// rejected, not case-folded into a match.
func TestMatchCaseSensitiveShapeRejected(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "uppercase PR-1 prefix is not the documented lowercase shape", names: []string{"PR-1"}, prNumber: 1, wantErr: ErrNoMatch},
	})
}

// TestMatchRepoPrefixShapeIsUnconstrained_PinnedBehavior: the
// <repo>-pr-<N> shape has an intentionally unconstrained prefix (F5) --
// Match has no way to know what the "real" repo name is, so ANY string
// ending in -pr-<N> is, by design, the second documented shape. This is
// an inherent property the story's Risks table already calls out (e.g.
// an environment or repo literally named production-pr-1), not a bug in
// this function. Pinning it here makes it a deliberate, visible decision:
// a future change to reject such names is a visible diff against this
// test, not a silent behavior change either way.
func TestMatchRepoPrefixShapeIsUnconstrained_PinnedBehavior(t *testing.T) {
	runMatchCases(t, []matchCase{
		{name: "production-pr-1 matches the repo-prefixed shape (unconstrained prefix, by design)", names: []string{"production-pr-1"}, prNumber: 1, wantName: "production-pr-1"},
	})
}
