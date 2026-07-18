// Package main implements prenv, a `go run`-able pure matcher that
// resolves the single Railway ephemeral PR environment name for a given
// PR number out of a list of candidate environment names (task-132,
// M4-21-08). Railway names a PR environment non-deterministically as
// either `pr-<N>` or `<repo>-pr-<N>` (Railway staff confirmed both
// shapes; not configurable), so resolve-env (M4-21-09) queries the
// environment names via GraphQL at runtime and hands them to Match rather
// than assuming a single naming convention.
//
// Match is carved out as a pure function -- no network, no environment
// reads, no globals; the candidate name list is an argument -- so the
// false-positive risk named in the story's Risks table (QA F5) is
// testable head-on: an unanchored/substring match would resolve PR 1
// against an environment named pr-15, and CI would then deploy to and
// reset the WRONG PR's environment (cross-PR corruption presenting as
// flakiness). A live Railway run only ever sees whatever environments
// happen to exist and can never deliberately exercise that ambiguity --
// purity is what makes it a unit-testable scenario instead.
//
// STUB NOTICE (QA Mode A / RALPH RED stage): Match below is a
// compile-only stub. It exists solely so match_test.go compiles and
// fails on its assertions (wrong returned name / wrong sentinel error)
// rather than "undefined: Match". The real anchored-regexp capture +
// strconv.Atoi numeric-compare logic is implemented by the executor in
// Stage 3 (M4-21-08) -- see the task's Implementation Plan for the exact
// approach.
package main

import "errors"

// ErrNoMatch is returned when zero candidate names match prNumber.
var ErrNoMatch = errors.New("prenv: no environment name matches the given PR number")

// ErrAmbiguous is returned when more than one candidate name matches
// prNumber. The error returned by Match must name every candidate (wrap
// this sentinel with %w so errors.Is identity survives) so a human
// reading CI logs -- and the tests -- can assert on identity rather than
// string content, while still seeing which environments collided.
var ErrAmbiguous = errors.New("prenv: ambiguous match: more than one environment name matches the given PR number")

// Match resolves the single Railway ephemeral environment name in names
// that corresponds to prNumber. Matching is exact on the two documented
// shapes only -- `pr-<N>` and `<repo>-pr-<N>`, with <N> parsed as a whole
// number equal to prNumber -- never a prefix or substring test (AC-2).
//
// Returns (name, nil) for exactly one match, ("", ErrNoMatch) for zero
// matches, and ("", ErrAmbiguous) -- naming every candidate -- for more
// than one match (AC-3). Match performs no network calls, environment
// reads, or global state access (AC-1).
//
// TODO: implemented by executor (M4-21-08 Stage 3). Currently a
// compile-only stub that always reports no match.
func Match(names []string, prNumber int) (string, error) {
	return "", ErrNoMatch
}
