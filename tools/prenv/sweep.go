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
//
// STUB NOTICE (M4-23-06, Stage 2.5 / Mode A): only the type declarations,
// the const blocks, and the function signatures exist below -- every
// function body panics. sweep_test.go (and the sweep-decide cases added
// to main_test.go) were authored against these stubs, before the real
// implementation exists, so they are RED for the right reason (an
// unimplemented panic or a wrong-answer/wrong-exit-code assertion, never
// a build error). The real bodies land in Stage 3; this notice and the
// panics disappear with them.
package main

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

// String implements fmt.Stringer for PRState, for logging.
func (s PRState) String() string {
	panic("not implemented")
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
func ShouldReap(envName string, isEphemeral bool, state PRState) (bool, ReapReason) {
	panic("not implemented")
}

// ParsePRState maps `gh pr view --json state`'s raw output -- its stdout
// and exit code -- to a PRState. It fails to PRStateUnknown on anything
// short of a well-formed, uppercase OPEN/CLOSED/MERGED payload at exit
// code 0: a non-zero exit code wins over reap-looking stdout, always.
func ParsePRState(stdout string, ghExitCode int) PRState {
	panic("not implemented")
}
