// sweep.go implements the sweeper's reap-or-skip decision for a Railway
// PR environment (M4-23-06, task-150): given the environment's name,
// whether Railway reports it as ephemeral, and the PR state resolved from
// `gh pr view`, decide whether to delete it.
//
// ShouldReap requires POSITIVE evidence of a closed or merged PR before
// reaping -- every other state (open, unknown, unrecognised), a
// non-ephemeral environment, or a name that doesn't parse as a PR
// environment is a skip. ParsePRState is the adapter from `gh pr view`'s
// raw output (stdout + exit code) to a PRState, and fails to
// PRStateUnknown on anything short of a well-formed, uppercase
// OPEN/CLOSED/MERGED payload at exit code 0 -- see task-150's
// Implementation Plan for the full guard-ordering rationale.
package main

import "encoding/json"

// PRState is the pull request state as resolved from `gh pr view`.
// PRStateUnknown is deliberately the zero value (see
// TestPRStateUnknownIsZeroValue in sweep_test.go) so an uninitialised or
// forgotten PRState fails safe -- "we could not determine the PR's
// state" is a first-class value, not decoded as Open by accident.
type PRState int

const (
	PRStateUnknown PRState = iota // zero value -- must stay first, see task-150
	PRStateOpen
	PRStateClosed
	PRStateMerged
)

// String implements fmt.Stringer for PRState, for logging. An
// unrecognised value renders as "unknown" rather than a number: on this
// path every non-declared state is treated as unknown anyway, so the log
// line should not suggest otherwise.
func (s PRState) String() string {
	switch s {
	case PRStateOpen:
		return "open"
	case PRStateClosed:
		return "closed"
	case PRStateMerged:
		return "merged"
	default:
		return "unknown"
	}
}

// ReapReason is the human-readable reason ShouldReap returns alongside
// its decision, for the sweeper's log line. It is a named string type
// (not bare string) so a reason can be compared by equality against the
// constants below rather than by matching prose.
type ReapReason string

const (
	ReasonPRClosed          ReapReason = "pr-closed"
	ReasonPRMerged          ReapReason = "pr-merged"
	ReasonPROpen            ReapReason = "pr-open"
	ReasonPRStateUnknown    ReapReason = "pr-state-unknown"
	ReasonUnrecognisedState ReapReason = "pr-state-unrecognised"
	ReasonNotAPREnvironment ReapReason = "not-a-pr-environment"
	ReasonNotEphemeral      ReapReason = "not-ephemeral"
)

// ShouldReap decides whether the Railway environment envName should be
// deleted. It is pure -- no network, no environment reads, no globals --
// so every fail-safe branch is directly unit-testable even though none
// can be reliably provoked against live Railway.
// The guard ORDER below is load-bearing, not stylistic. isEphemeral goes
// first because it is the only guard that does not depend on the name, so
// it is the one that survives a rename; the name parse goes second. That
// separation is what makes AC-5's two "development" cases skip via two
// DIFFERENT guards -- which is what "independently" means, and what
// TestShouldNeverReapDevelopment asserts by requiring the reasons to
// differ. There is deliberately NO literal "development" denylist here:
// "development" cannot parse as pr-<N>, and a RENAMED development is by
// definition no longer called "development", so a name denylist would
// only ever fire where guard 2 already covers it. See task-150 for the
// five real layers that do guard it.
func ShouldReap(envName string, isEphemeral bool, state PRState) (bool, ReapReason) {
	if !isEphemeral {
		return false, ReasonNotEphemeral
	}
	if _, ok := ParsePR(envName); !ok {
		// The PR number itself is discarded -- the caller already parsed it
		// to call gh. Re-parsing keeps ShouldReap self-contained and lets it
		// refuse a name its caller mis-parsed.
		return false, ReasonNotAPREnvironment
	}
	switch state {
	case PRStateClosed:
		return true, ReasonPRClosed
	case PRStateMerged:
		return true, ReasonPRMerged
	case PRStateOpen:
		return false, ReasonPROpen
	case PRStateUnknown:
		return false, ReasonPRStateUnknown
	default:
		// Mandatory: there must be no path that falls through to a reap.
		return false, ReasonUnrecognisedState
	}
}

// ParsePRState maps `gh pr view --json state`'s raw output -- its stdout
// and exit code -- to a PRState. It fails to PRStateUnknown on anything
// short of a well-formed, uppercase OPEN/CLOSED/MERGED payload at exit
// code 0: a non-zero exit code wins over reap-looking stdout, always.
// The uppercase match is case-SENSITIVE by design. gh emits uppercase
// (measured 2026-07-19). If gh ever changed case, or someone swapped in
// `gh api` (REST returns lowercase open/closed and has no merged state at
// all), the failure direction is "never reap" -- visible in the daily log
// rather than a deletion.
func ParsePRState(stdout string, ghExitCode int) PRState {
	// A non-zero exit wins over reap-looking stdout, always -- stdout is
	// not even inspected.
	if ghExitCode != 0 {
		return PRStateUnknown
	}
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return PRStateUnknown
	}
	switch payload.State {
	case "OPEN":
		return PRStateOpen
	case "CLOSED":
		return PRStateClosed
	case "MERGED":
		return PRStateMerged
	default:
		// Includes "" -- the `{}` case, where the key is absent entirely.
		return PRStateUnknown
	}
}
