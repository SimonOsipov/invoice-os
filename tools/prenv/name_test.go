// name_test.go holds the acceptance tests for Name and ParsePR
// (name.go), transcribed from the architect's validated Test Specs table
// for task-154 (M4-23-01, Test-first: yes) and authored against
// panicking stubs BEFORE the implementation existed. They were RED on
// commit 0547547 and went green with M4-23-01's implementation; the
// stubs and their STUB NOTICE are gone.
//
// These are pure unit tests: no network, no filesystem, no os.Getenv --
// just in-memory strings and ints -- so they run in the existing Go CI
// job unconditionally, with no new test runner or config.
//
// Coverage -- Test Specs table + Implementation Plan §4 (task-154), 12
// specs:
//  1. TestNameProducesBareShape                             -- AC-1: Name(n) == "pr-<n>" over n in {1,9,42,12345}.
//  2. TestParsePRAcceptsBareShape                            -- AC-2: "pr-42" -> (42, true).
//  3. TestParsePRAcceptsRepoQualifiedShape                   -- AC-2: "invoice-os-pr-42" -> (42, true).
//  4. TestParsePRIsAnchoredNotSubstring                      -- AC-2, reworded per plan (Match's "want PR" param did not survive the rewrite): a valid pr-<N> CONTAINED in a longer string must never parse.
//  5. TestParsePRRejectsLeadingZeroLookalike                 -- AC-3: "pr-01" -> (0, false).
//  6. TestParsePRRejectsNonPREnvironmentNames                -- AC-4, extended table of non-PR / malformed names -> (0, false).
//  7. TestNameParsePRRoundTrip                                -- AC-5: ParsePR(Name(n)) == (n, true), n >= 1 only (Name's domain).
//  8. TestNameSatisfiesGatewayBootstrapAllowlistShape        -- AC-8: Name(n) matches bootstrap.go's allowlist shape (tripwire, not a live coupling).
//  9. TestParsePRPrefixIsUnconstrained_PinnedBehavior        -- AC-2: the <repo>-pr-<N> prefix is unconstrained by design; pinned (carried forward from the deleted match_test.go).
//  10. TestParsePRRejectsOversizedDigitRun                    -- AC-3: a digit run that overflows int (strconv.Atoi ErrRange) is rejected; not dead code.
//  11. TestNameDomainIsPositivePRNumbers_PinnedBehavior       -- AC-1/AC-5: Name(-1) == "pr--1", which ParsePR then rejects -- Name is total and unvalidating, the trust boundary is the CLI (argv), not this function.
//  12. TestParsePRZeroPinnedBehavior                          -- AC-3: ParsePR("pr-0") -> (0, true); PR 0 doesn't exist, pinned because both downstream paths fail safe.
//
// Deliberately NOT tested here (per Implementation Plan §4, "ACs with no
// test spec"): AC-6 (purity) is a structural property of the import list,
// enforced by review, not a test; AC-7 ("nothing references Match") is
// enforced by the compiler now that match.go is deleted -- a surviving
// reference becomes a build failure, not a test failure; AC-9 is a
// comment-only requirement. Inventing tests for these would be ceremony,
// not coverage.
package main

import (
	"fmt"
	"regexp"
	"testing"
)

// TestNameProducesBareShape: AC-1. Name(n) returns exactly "pr-<n>" for a
// range of PR numbers, not just the single example (42) from the spec
// table.
func TestNameProducesBareShape(t *testing.T) {
	cases := []struct {
		pr   int
		want string
	}{
		{1, "pr-1"},
		{9, "pr-9"},
		{42, "pr-42"},
		{12345, "pr-12345"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := Name(tc.pr)
			if got != tc.want {
				t.Errorf("Name(%d) = %q, want %q", tc.pr, got, tc.want)
			}
		})
	}
}

// TestParsePRAcceptsBareShape: AC-2. The plain pr-<N> shape parses.
func TestParsePRAcceptsBareShape(t *testing.T) {
	n, ok := ParsePR("pr-42")
	if !ok || n != 42 {
		t.Errorf("ParsePR(%q) = (%d, %v), want (42, true)", "pr-42", n, ok)
	}
}

// TestParsePRAcceptsRepoQualifiedShape: AC-2. The <repo>-pr-<N> shape
// parses.
func TestParsePRAcceptsRepoQualifiedShape(t *testing.T) {
	n, ok := ParsePR("invoice-os-pr-42")
	if !ok || n != 42 {
		t.Errorf("ParsePR(%q) = (%d, %v), want (42, true)", "invoice-os-pr-42", n, ok)
	}
}

// TestParsePRIsAnchoredNotSubstring: AC-2, reworded from the story's Test
// Specs table per the architect's Implementation Plan §4 -- the original
// spec ("pr-15", want PR 1 -> (15, true), never (1, ...)) targeted
// Match's "want PR" parameter, which did not survive the ParsePR
// rewrite; asserting got != 1 there would be trivially implied by
// got == 15 and test nothing.
//
// ParsePR's actual anchoring hazard is different: a name that CONTAINS a
// valid pr-<N> shape as a substring must never parse, because the blast
// radius here is worse than Match's -- a mis-parse doesn't deploy to the
// wrong environment, it deletes one (the sweeper feeds ParsePR's result
// straight into a teardown decision).
func TestParsePRIsAnchoredNotSubstring(t *testing.T) {
	names := []string{"pr-15-old", "xpr-15", "my-pr-15-backup", "-pr-42"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			n, ok := ParsePR(name)
			if ok {
				t.Errorf("ParsePR(%q) = (%d, true), want (0, false): a pr-<N> substring must not parse", name, n)
			}
			if n != 0 {
				t.Errorf("ParsePR(%q) returned n=%d on failure, want 0", name, n)
			}
		})
	}
}

// TestParsePRRejectsLeadingZeroLookalike: AC-3. "pr-01" is a different
// whole number than 1 (its canonical name is "pr-1") -- ParsePR must
// reject it outright rather than parse it as 1.
func TestParsePRRejectsLeadingZeroLookalike(t *testing.T) {
	n, ok := ParsePR("pr-01")
	if ok {
		t.Errorf("ParsePR(%q) = (%d, true), want (0, false)", "pr-01", n)
	}
	if n != 0 {
		t.Errorf("ParsePR(%q) returned n=%d on failure, want 0", "pr-01", n)
	}
}

// TestParsePRRejectsNonPREnvironmentNames: AC-4. Every non-PR /
// malformed environment name returns (0, false), including the malformed
// table added by the architect: case ("PR-42"), a trailing newline that
// Go's $ anchor does not match before (a shell $(...) capture or
// jq-piped value can carry one -- "pr-42\n"), and stray punctuation
// ("pr-+42", "pr-4_2", "pr--1").
func TestParsePRRejectsNonPREnvironmentNames(t *testing.T) {
	names := []string{
		"development",
		"production",
		"staging",
		"pr-",
		"pr-abc",
		"pr-42x",
		"pr-42 ",
		"",
		"PR-42",
		"pr-42\n",
		"pr-+42",
		"pr-4_2",
		"pr--1",
	}
	for i, name := range names {
		t.Run(fmt.Sprintf("case_%d_%q", i, name), func(t *testing.T) {
			n, ok := ParsePR(name)
			if ok {
				t.Errorf("ParsePR(%q) = (%d, true), want (0, false)", name, n)
			}
			if n != 0 {
				t.Errorf("ParsePR(%q) returned n=%d on failure, want 0", name, n)
			}
		})
	}
}

// TestNameParsePRRoundTrip: AC-5. ParsePR(Name(n)) == (n, true) across a
// range of PR numbers, including a small number, a two-digit boundary, a
// realistic mid-size number, and large 6-digit numbers with no fixed
// digit-width assumption. The property holds for n >= 1 only -- that is
// Name's domain (see TestNameDomainIsPositivePRNumbers_PinnedBehavior).
func TestNameParsePRRoundTrip(t *testing.T) {
	ns := []int{1, 9, 10, 42, 99, 12345, 100000, 999999}
	for _, n := range ns {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			got, ok := ParsePR(Name(n))
			if !ok || got != n {
				t.Errorf("ParsePR(Name(%d)) = (%d, %v), want (%d, true)", n, got, ok, n)
			}
		})
	}
}

// TestNameSatisfiesGatewayBootstrapAllowlistShape: AC-8, a tripwire, not
// a live coupling.
//
// Duplicated literal: internal/platform/db/bootstrap.go:39's
// prEnvironmentPattern (`^(?:.+-)?pr-[0-9]+$`) is unexported, lives in a
// different package (internal/platform/db), and tools/prenv is package
// main -- so it cannot be imported here regardless. This test compiles a
// second copy of the same shape and checks Name's output against it, so
// bootstrap.go:39 and this test are the two sites a future edit to
// either pattern needs to keep in sync (grep both).
//
// The coupling this guards is NOT what an earlier draft of this story
// claimed. The regex gates the ENVIRONMENT variable, not the Railway
// environment name: cmd/gateway/main.go:52 passes
// os.Getenv("ENVIRONMENT") into db.Provision's Environment field, and
// provisionableEnvironment (internal/platform/db/bootstrap.go:47)
// short-circuits on the literal "development" before prEnvironmentPattern
// ever runs. ENVIRONMENT is set nowhere in this repo today, so every
// fork inherits "development" -- this test does not reflect a real
// coupling as of M4-23-01. It exists purely as a tripwire: if anyone
// later sets ENVIRONMENT=pr-<N>, a Name() shape that stopped matching
// this allowlist pattern would be caught here, instead of silently
// disabling bootstrap+seed with no error and no log line.
func TestNameSatisfiesGatewayBootstrapAllowlistShape(t *testing.T) {
	bootstrapAllowlist := regexp.MustCompile(`^(?:.+-)?pr-[0-9]+$`)

	for _, pr := range []int{1, 42, 12345} {
		name := Name(pr)
		if !bootstrapAllowlist.MatchString(name) {
			t.Errorf("Name(%d) = %q does not match bootstrap.go:39's allowlist pattern %s", pr, name, bootstrapAllowlist.String())
		}
	}
}

// TestParsePRPrefixIsUnconstrained_PinnedBehavior: AC-2. The
// <repo>-pr-<N> shape has an intentionally unconstrained prefix --
// ParsePR has no way to know what the "real" repo name is, so ANY string
// ending in -pr-<N> is, by design, the second documented shape. This was
// pinned in the deleted match_test.go's
// TestMatchRepoPrefixShapeIsUnconstrained_PinnedBehavior and is carried
// forward here so a future change to reject such names is a visible diff
// against this test, not a silent behavior change either way.
func TestParsePRPrefixIsUnconstrained_PinnedBehavior(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"production-pr-42", 42},
		{"pr-1-pr-15", 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParsePR(tc.name)
			if !ok || got != tc.want {
				t.Errorf("ParsePR(%q) = (%d, %v), want (%d, true)", tc.name, got, ok, tc.want)
			}
		})
	}
}

// TestParsePRRejectsOversizedDigitRun: AC-3. envNamePattern's digit class
// ([0-9]+) is unbounded, so a digit run long enough to overflow int makes
// strconv.Atoi return ErrRange -- this branch is reachable and must be
// treated as a rejection like any other malformed name, not left as
// unhandled dead code.
func TestParsePRRejectsOversizedDigitRun(t *testing.T) {
	name := "pr-99999999999999999999"
	n, ok := ParsePR(name)
	if ok {
		t.Errorf("ParsePR(%q) = (%d, true), want (0, false)", name, n)
	}
	if n != 0 {
		t.Errorf("ParsePR(%q) returned n=%d on failure, want 0", name, n)
	}
}

// TestNameDomainIsPositivePRNumbers_PinnedBehavior: AC-1/AC-5. Name is
// deliberately total and unvalidating -- the trust boundary is argv (the
// CLI rejects pr < 1 with exit 2), not this pure function. Name(-1) still
// produces a string ("pr--1"), and ParsePR then rejects that string
// because "-1" fails the [0-9]+ digit class -- pinning that the round
// trip holds for n >= 1 only, by design, not by accident.
func TestNameDomainIsPositivePRNumbers_PinnedBehavior(t *testing.T) {
	got := Name(-1)
	if got != "pr--1" {
		t.Fatalf("Name(-1) = %q, want %q", got, "pr--1")
	}

	n, ok := ParsePR(got)
	if ok {
		t.Errorf("ParsePR(%q) = (%d, true), want (0, false)", got, n)
	}
	if n != 0 {
		t.Errorf("ParsePR(%q) returned n=%d on failure, want 0", got, n)
	}
}

// TestParsePRZeroPinnedBehavior: AC-3. PR 0 does not exist, so this is
// unreachable via the real automated path (GitHub PR numbers start at 1)
// -- pinned anyway because a reviewer should not have to re-derive that
// both downstream paths fail safe: gh pr view 0 fails -> PRStateUnknown
// -> no reap.
func TestParsePRZeroPinnedBehavior(t *testing.T) {
	n, ok := ParsePR("pr-0")
	if !ok || n != 0 {
		t.Errorf("ParsePR(%q) = (%d, %v), want (0, true)", "pr-0", n, ok)
	}
}
