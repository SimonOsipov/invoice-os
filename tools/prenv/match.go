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
package main

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ErrNoMatch is returned when zero candidate names match prNumber.
var ErrNoMatch = errors.New("prenv: no environment name matches the given PR number")

// ErrAmbiguous is returned when more than one candidate name matches
// prNumber. The error returned by Match must name every candidate (wrap
// this sentinel with %w so errors.Is identity survives) so a human
// reading CI logs -- and the tests -- can assert on identity rather than
// string content, while still seeing which environments collided.
var ErrAmbiguous = errors.New("prenv: ambiguous match: more than one environment name matches the given PR number")

// shapePattern anchors both documented Railway PR-environment name shapes
// -- `pr-<N>` and `<repo>-pr-<N>` -- at both ends of the string (^...$),
// so no prefix or substring match is ever possible (AC-2). The `<repo>-`
// portion is deliberately unconstrained (any run of characters ending in
// a literal hyphen): Match has no way to know the "real" repo name, so
// ANY such prefix is, by design, the second documented shape (F5) --
// TestMatchRepoPrefixShapeIsUnconstrained_PinnedBehavior pins this.
//
// The digit run itself is captured with the plain [0-9]+ class (no
// no-leading-zero restriction here); Match instead requires the captured
// run to equal strconv.Itoa(prNumber) verbatim, which is what rejects a
// leading-zero lookalike like pr-01 for PR 1 without special-casing the
// character class.
var shapePattern = regexp.MustCompile(`^(?:.+-)?pr-([0-9]+)$`)

// Match resolves the single Railway ephemeral environment name in names
// that corresponds to prNumber. Matching is exact on the two documented
// shapes only -- `pr-<N>` and `<repo>-pr-<N>`, with <N> parsed as a whole
// number equal to prNumber -- never a prefix or substring test (AC-2).
//
// Returns (name, nil) for exactly one match, ("", ErrNoMatch) for zero
// matches, and ("", ErrAmbiguous) -- naming every candidate -- for more
// than one match (AC-3). Match performs no network calls, environment
// reads, or global state access: names is the only input (AC-1).
//
// Match does not deduplicate names: Railway environment names are unique
// within a project, so a duplicate entry in names would indicate a caller
// bug (e.g. concatenating two GraphQL pages), not a legitimate ambiguity
// -- and is reported as ErrAmbiguous like any other multi-candidate case
// rather than silently collapsed.
func Match(names []string, prNumber int) (string, error) {
	want := strconv.Itoa(prNumber)

	var matches []string
	for _, name := range names {
		submatch := shapePattern.FindStringSubmatch(name)
		if submatch == nil {
			continue
		}
		if submatch[1] != want {
			continue
		}
		matches = append(matches, name)
	}

	switch len(matches) {
	case 0:
		return "", ErrNoMatch
	case 1:
		return matches[0], nil
	default:
		// Sorted so the error message is deterministic regardless of the
		// order names arrived in (e.g. GraphQL result ordering is not a
		// guarantee Match should depend on for reproducible CI logs).
		sorted := append([]string(nil), matches...)
		sort.Strings(sorted)
		return "", fmt.Errorf("%w: %s", ErrAmbiguous, strings.Join(sorted, ", "))
	}
}
