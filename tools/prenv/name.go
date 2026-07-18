package main

import (
	"regexp"
	"strconv"
)

// envNamePattern anchors both Railway PR-environment name shapes at both
// ends: bare "pr-<N>" and repo-qualified "<repo>-pr-<N>". The <repo>-
// prefix is deliberately unconstrained (see
// TestParsePRPrefixIsUnconstrained_PinnedBehavior in name_test.go) --
// ParsePR has no way to know the "real" repo name, so any string ending
// in -pr-<N> is, by design, the second documented shape.
//
// The digit run itself is captured with the plain [0-9]+ class (no
// no-leading-zero restriction here); ParsePR instead re-renders the
// captured digits with strconv.Itoa and requires an exact match, which is
// what rejects a leading-zero lookalike like pr-01 without complicating
// the character class (technique carried over verbatim from the deleted
// match.go's shapePattern).
//
// Deliberate asymmetry (AC-9): internal/platform/db/bootstrap.go:39's
// prEnvironmentPattern uses this same shape and therefore ACCEPTS
// "pr-01", while ParsePR rejects it. Unreachable via the automated path
// (Name never emits a leading zero) and it fails safe in both directions,
// so no code change follows from it.
var envNamePattern = regexp.MustCompile(`^(?:.+-)?pr-([0-9]+)$`)

// Name returns the Railway PR-environment name for pr: "pr-<pr>". It is
// the only place in the repo that constructs an environment name (AC-1).
//
// Name is deliberately total and unvalidating: Name(-1) returns "pr--1"
// rather than an error. The trust boundary is argv -- main rejects pr < 1
// with exit 2 -- not this pure function, whose only in-process caller
// passes github.event.number (>= 1 by construction). The round trip with
// ParsePR therefore holds for pr >= 1 only, by design.
func Name(pr int) string {
	return "pr-" + strconv.Itoa(pr)
}

// ParsePR reports the PR number encoded in a Railway PR-environment name,
// accepting only the two documented shapes anchored at both ends -- never
// a prefix or substring match (AC-2) -- and rejecting non-canonical digit
// runs such as a leading zero (AC-3). Returns (0, false) for any name that
// is not a PR-environment name in either shape (AC-4).
func ParsePR(name string) (int, bool) {
	submatch := envNamePattern.FindStringSubmatch(name)
	if submatch == nil {
		return 0, false
	}
	// Reachable, not defensive: [0-9]+ is unbounded, so a digit run long
	// enough to overflow int makes Atoi return ErrRange.
	n, err := strconv.Atoi(submatch[1])
	if err != nil {
		return 0, false
	}
	// Parse-then-re-render: the inverse of the deleted match.go's
	// `submatch[1] != want` check. Requiring the captured digits to equal
	// their own canonical rendering is what rejects "pr-01".
	if strconv.Itoa(n) != submatch[1] {
		return 0, false
	}
	return n, true
}
