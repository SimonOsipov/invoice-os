package main

import "regexp"

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
var envNamePattern = regexp.MustCompile(`^(?:.+-)?pr-([0-9]+)$`)

// STUB NOTICE: Name and ParsePR are compile-only stubs for the Stage 2.5
// (QA Mode A / RED) test-spec pass of M4-23-01 (task-154). Both panic
// unconditionally -- the real logic (bare string construction for Name;
// anchored-match, overflow-checked, parse-then-re-render for ParsePR)
// lands in Stage 3 (Executor). Every name_test.go case that expects a
// non-panicking result is RED until then.

// Name returns the Railway PR-environment name for pr: "pr-<pr>". It is
// the only place in the repo that constructs an environment name (AC-1).
func Name(pr int) string {
	panic("not implemented")
}

// ParsePR reports the PR number encoded in a Railway PR-environment name,
// accepting only the two documented shapes anchored at both ends -- never
// a prefix or substring match (AC-2) -- and rejecting non-canonical digit
// runs such as a leading zero (AC-3). Returns (0, false) for any name that
// is not a PR-environment name in either shape (AC-4).
func ParsePR(name string) (int, bool) {
	panic("not implemented")
}
