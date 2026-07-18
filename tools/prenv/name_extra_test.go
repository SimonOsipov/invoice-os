// name_extra_test.go is QA's Mode B adversarial addition to name_test.go's
// 12 architect-authored acceptance tests (task-154, M4-23-01). These probe
// hazards the Test Specs table didn't enumerate: the round-trip property at
// its extremal boundary, adversarial shapes of the deliberately-unconstrained
// <repo>- prefix, and the known accept/reject asymmetry between this
// package's ParsePR and internal/platform/db/bootstrap.go's allowlist regex.
package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestNameParsePRRoundTripAtMaxInt: no n in Name's domain (n >= 1) causes
// ParsePR(Name(n)) to diverge -- checked at the one boundary where a gap
// could plausibly exist. strconv.Itoa/Atoi are true inverses across the
// full int range on a 64-bit platform (Atoi is ParseInt with bitSize=0,
// i.e. strconv.IntSize), so math.MaxInt is the extremal case: one past it
// isn't representable as an int argument to Name at all, and there is no
// smaller number that could exercise a different code path than the ones
// already covered by TestNameParsePRRoundTrip's 100000/999999 cases.
func TestNameParsePRRoundTripAtMaxInt(t *testing.T) {
	got, ok := ParsePR(Name(math.MaxInt))
	if !ok || got != math.MaxInt {
		t.Errorf("ParsePR(Name(math.MaxInt)) = (%d, %v), want (%d, true)", got, ok, math.MaxInt)
	}
}

// TestParsePRDoubleHyphenPrefixAccepted_PinnedBehavior: the <repo>- prefix
// grammar is `.+-` -- at least one arbitrary char followed by a literal
// hyphen. "--pr-42" satisfies that with prefix "-" + separator "-", so it
// parses as PR 42, while the single-hyphen "-pr-42" is already pinned
// (TestParsePRIsAnchoredNotSubstring) to reject, because there's no
// character left for the ".+" part once the separator hyphen is consumed.
// This non-obvious pair (one hyphen rejects, two accepts) is pinned
// separately so a future reader doesn't "fix" the double-hyphen case into
// a reject by tightening the character class without noticing the
// single-hyphen test would then need to change too.
func TestParsePRDoubleHyphenPrefixAccepted_PinnedBehavior(t *testing.T) {
	n, ok := ParsePR("--pr-42")
	if !ok || n != 42 {
		t.Errorf(`ParsePR("--pr-42") = (%d, %v), want (42, true)`, n, ok)
	}
}

// TestParsePRRejectsNewlineInsidePrefix: a newline embedded inside the
// <repo>- prefix (as opposed to trailing the whole name, already covered by
// name_test.go's "pr-42\n" case) fails closed. Go's `.` does not match `\n`
// without the (?s) flag, and envNamePattern doesn't set it, so a prefix
// straddling a newline can never satisfy `.+-` in one span and the overall
// anchored match fails.
func TestParsePRRejectsNewlineInsidePrefix(t *testing.T) {
	names := []string{"a\nb-pr-42", "repo\n-pr-42"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			n, ok := ParsePR(name)
			if ok {
				t.Errorf("ParsePR(%q) = (%d, true), want (0, false)", name, n)
			}
		})
	}
}

// TestParsePRUnicodePrefixAccepted_PinnedBehavior: the unconstrained prefix
// is not ASCII-only -- Go's regexp operates on decoded runes, so a
// multi-byte UTF-8 prefix is just more characters satisfying `.+`. Pinned
// because a Railway project/repo name is not guaranteed ASCII and ParsePR
// must not silently misbehave on one that isn't.
func TestParsePRUnicodePrefixAccepted_PinnedBehavior(t *testing.T) {
	n, ok := ParsePR("日本語-pr-42")
	if !ok || n != 42 {
		t.Errorf(`ParsePR("日本語-pr-42") = (%d, %v), want (42, true)`, n, ok)
	}
}

// TestParsePRUnboundedPrefixLength_PinnedBehavior: the <repo>- prefix has
// no length cap, by design (ParsePR cannot know the real repo name — see
// envNamePattern's doc comment in name.go). Exercised at a prefix far
// longer than any real Railway/GitHub name to confirm there's no hidden
// length guard, and — since Go's regexp package is RE2-based (linear time,
// no catastrophic backtracking) — that a long adversarial prefix is not a
// performance hazard for whatever eventually feeds ParsePR an environment
// name (e.g. the M4-23-07 sweeper iterating Railway's environment list).
func TestParsePRUnboundedPrefixLength_PinnedBehavior(t *testing.T) {
	prefix := strings.Repeat("a-", 2000) // 4000 chars, well past any real name
	name := prefix + "pr-42"
	n, ok := ParsePR(name)
	if !ok || n != 42 {
		t.Errorf("ParsePR(<4000-char prefix>-pr-42) = (%d, %v), want (42, true)", n, ok)
	}
}

// TestParsePRVsGatewayBootstrapAllowlistAsymmetry_PinnedLeadingZero pins
// AC-9's documented asymmetry with an executable assertion instead of only
// a comment: internal/platform/db/bootstrap.go:39's prEnvironmentPattern
// (`^(?:.+-)?pr-[0-9]+$`) ACCEPTS a leading-zero digit run like "pr-01",
// while this package's ParsePR (the parse-then-re-render check at
// tools/prenv/name.go:60) REJECTS it. Both patterns are duplicated string
// literals in packages that cannot import each other (tools/prenv is
// package main; internal/platform/db's prEnvironmentPattern is
// unexported) — see TestNameSatisfiesGatewayBootstrapAllowlistShape for
// the forward-direction tripwire (Name's output always satisfies the
// gateway's allowlist). This test pins the inverse: an input the gateway
// allowlist would accept that ParsePR refuses to parse. It fails safe
// (worst case a would-be PR environment named with a leading zero is
// invisible to prenv, not attributed to the wrong PR), but a future edit
// to either regex that closes or widens the gap should show up here, not
// get discovered by an incident.
func TestParsePRVsGatewayBootstrapAllowlistAsymmetry_PinnedLeadingZero(t *testing.T) {
	bootstrapAllowlist := regexp.MustCompile(`^(?:.+-)?pr-[0-9]+$`)

	for _, name := range []string{"pr-01", "pr-007"} {
		t.Run(name, func(t *testing.T) {
			if !bootstrapAllowlist.MatchString(name) {
				t.Fatalf("test invariant broken: bootstrap.go:39's allowlist no longer accepts %q -- update this test's premise", name)
			}
			n, ok := ParsePR(name)
			if ok {
				t.Errorf("ParsePR(%q) = (%d, true), want (0, false): the documented AC-9 asymmetry no longer holds", name, n)
			}
		})
	}
}

// sanity guard: fail loudly (not silently pass vacuously) if strconv ever
// changes such that Itoa/Atoi aren't inverses at MaxInt -- belt-and-braces
// for TestNameParsePRRoundTripAtMaxInt's premise.
func TestStrconvItoaAtoiInverseAtMaxInt_Sanity(t *testing.T) {
	s := strconv.Itoa(math.MaxInt)
	n, err := strconv.Atoi(s)
	if err != nil || n != math.MaxInt {
		t.Fatalf("strconv.Itoa/Atoi are not inverses at math.MaxInt: Itoa=%q, Atoi=(%d,%v)", s, n, err)
	}
}
