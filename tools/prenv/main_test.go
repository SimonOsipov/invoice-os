// main_test.go pins the CLI contract documented in main.go's dispatch
// comment: exit 2 means "you called me wrong", exit 1 means "well-formed
// call, this isn't a PR environment name", exit 0 means success -- and
// stdout carries only the answer, never diagnostics. This contract is
// load-bearing for M4-23-07's sweeper, which will shell out to `prenv
// parse` and capture stdout as the PR number while relying on the exit
// code to distinguish "not a PR env, skip it" (1) from "I'm broken" (2).
//
// main() calls os.Exit, so it can't be driven in-process without killing
// the test binary -- these tests build the real prenv binary once (TestMain)
// and exec it, asserting on the real process exit code and the real
// stdout/stderr separation, which is exactly the boundary the sweeper will
// depend on.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "prenv-cli-test")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	binPath = filepath.Join(dir, "prenv")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		panic("building prenv for CLI tests failed: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

// runCLI execs the built prenv binary with args and returns its stdout,
// stderr, and exit code. Fails the test outright if the process couldn't
// even start (as opposed to exiting non-zero, which is a normal outcome
// here).
func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("failed to run prenv %v: %v", args, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// TestCLINameSuccess: `prenv name <pr>` on a well-formed positive PR number
// prints exactly the environment name plus a newline to stdout, nothing to
// stderr, and exits 0.
func TestCLINameSuccess(t *testing.T) {
	stdout, stderr, code := runCLI(t, "name", "42")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout != "pr-42\n" {
		t.Errorf("stdout = %q, want %q", stdout, "pr-42\n")
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

// TestCLIParseSuccess: `prenv parse <name>` on a well-formed PR environment
// name prints exactly the PR number plus a newline to stdout, nothing to
// stderr, and exits 0.
func TestCLIParseSuccess(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"pr-42", "42\n"},
		{"invoice-os-pr-42", "42\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, "parse", tc.name)
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

// TestCLICalledWrongExitsTwoWithCleanStdout covers every "you called me
// wrong" path the dispatch comment documents: missing subcommand, unknown
// subcommand, wrong arity for either subcommand, a non-numeric PR number,
// and a non-positive PR number. All must exit 2, and — the property that
// actually matters to a caller capturing stdout as data — stdout must stay
// completely empty; every diagnostic goes to stderr only.
func TestCLICalledWrongExitsTwoWithCleanStdout(t *testing.T) {
	cases := []struct {
		desc string
		args []string
	}{
		{"no subcommand", nil},
		{"unknown subcommand", []string{"bogus"}},
		{"name missing arg", []string{"name"}},
		{"name extra arg", []string{"name", "1", "2"}},
		{"name non-numeric", []string{"name", "abc"}},
		{"name zero", []string{"name", "0"}},
		{"name negative", []string{"name", "-5"}},
		{"parse missing arg", []string{"parse"}},
		{"parse extra arg", []string{"parse", "pr-1", "pr-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, tc.args...)
			if code != 2 {
				t.Errorf("args=%v: exit code = %d, want 2", tc.args, code)
			}
			if stdout != "" {
				t.Errorf("args=%v: stdout = %q, want empty (diagnostics must not leak into a captured answer)", tc.args, stdout)
			}
			if stderr == "" {
				t.Errorf("args=%v: stderr is empty, want a diagnostic message", tc.args)
			}
		})
	}
}

// TestCLIParseNotAPREnvironmentExitsOneWithCleanStdout covers the "well-
// formed call, negative answer" path: a syntactically fine `parse` call
// whose argument simply isn't a PR environment name (including the
// leading-zero lookalike from AC-3). Must exit 1, distinct from the exit-2
// "called wrong" cases above — that split is exactly what lets the sweeper
// tell "skip this, it's not a PR env" from "prenv itself is broken" without
// parsing stderr text. stdout must stay empty here too.
func TestCLIParseNotAPREnvironmentExitsOneWithCleanStdout(t *testing.T) {
	names := []string{"development", "production", "staging", "pr-01", ""}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, "parse", name)
			if code != 1 {
				t.Errorf("parse %q: exit code = %d, want 1", name, code)
			}
			if stdout != "" {
				t.Errorf("parse %q: stdout = %q, want empty", name, stdout)
			}
			if stderr == "" {
				t.Errorf("parse %q: stderr is empty, want a diagnostic message", name)
			}
		})
	}
}

// --- sweep-decide CLI contract (M4-23-06, task-150) ---
//
// STUB NOTICE (Stage 2.5 / Mode A): sweep-decide is not yet a recognised
// subcommand in main.go's dispatch switch -- every call below currently
// falls into the `default:` arm ("unknown subcommand ...", exit 2). The
// subcommand itself lands in Stage 3 (sweep.go's ShouldReap/ParsePRState
// go from panicking stubs to real bodies at the same time). See each
// test's own comment for how it reads today vs. once implemented.

// TestCLISweepDecideExitContract pins sweep-decide's exit/stdout
// contract from task-150: exit 0 (reap) with the reason on stdout for a
// closed/merged PR; exit 1 (skip) with the reason on stdout for an open
// PR. stderr is empty in both cases -- the deliberate divergence from
// `parse` (which writes to stderr on exit 1): here exit 1 is the common
// per-environment outcome (every open PR, every run), and stderr noise
// would bury real failures.
//
// Currently fails on every assertion (actual exit code 2, stdout empty,
// "unknown subcommand" on stderr) -- a legitimate RED via wrong exit
// code and wrong stdout, not a build error.
func TestCLISweepDecideExitContract(t *testing.T) {
	cases := []struct {
		name       string
		ghJSON     string
		wantCode   int
		wantStdout string
	}{
		{"closed-and-merged PR reaps", `{"state":"MERGED"}`, 0, "pr-merged\n"},
		{"open PR is skipped", `{"state":"OPEN"}`, 1, "pr-open\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, "sweep-decide", "pr-42", "true", "0", tc.ghJSON)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if stdout != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantStdout)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

// TestCLISweepDecideCalledWrongExitsTwoWithCleanStdout covers the
// "called wrong" path for sweep-decide: wrong arity, an is-ephemeral
// argument strconv.ParseBool can't parse, and a gh-exit-code argument
// strconv.Atoi can't parse. All must exit 2 with empty stdout -- the
// property the workflow's `if` structurally depends on to distinguish a
// mechanism failure from a real reap/skip answer.
//
// CAVEAT (untestable as a true RED right now): because sweep-decide
// isn't a recognised subcommand yet, EVERY case here already exits 2 via
// main.go's existing "unknown subcommand" default arm -- these
// assertions pass today, but for the wrong reason (the dispatcher's
// catch-all, not sweep-decide's own argument validation, which doesn't
// exist yet). This test cannot be made to genuinely fail red without
// either implementing argument validation now (out of scope for Mode A)
// or asserting on the exact diagnostic text (which the codebase's
// existing convention, TestCLICalledWrongExitsTwoWithCleanStdout above,
// deliberately does not do). It is included now because it is correct
// and becomes meaningful the moment Stage 3 adds sweep-decide -- but
// flagged here explicitly per the QA report rather than silently
// claimed as RED.
func TestCLISweepDecideCalledWrongExitsTwoWithCleanStdout(t *testing.T) {
	cases := []struct {
		desc string
		args []string
	}{
		{"missing all args", []string{"sweep-decide"}},
		{"missing one arg", []string{"sweep-decide", "pr-42", "true", "0"}},
		{"extra arg", []string{"sweep-decide", "pr-42", "true", "0", "{}", "extra"}},
		{"is-ephemeral empty", []string{"sweep-decide", "pr-42", "", "0", "{}"}},
		{"is-ephemeral not a bool", []string{"sweep-decide", "pr-42", "yes", "0", "{}"}},
		{"gh-exit-code empty", []string{"sweep-decide", "pr-42", "true", "", "{}"}},
		{"gh-exit-code not numeric", []string{"sweep-decide", "pr-42", "true", "abc", "{}"}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, tc.args...)
			if code != 2 {
				t.Errorf("args=%v: exit code = %d, want 2", tc.args, code)
			}
			if stdout != "" {
				t.Errorf("args=%v: stdout = %q, want empty", tc.args, stdout)
			}
			if stderr == "" {
				t.Errorf("args=%v: stderr is empty, want a diagnostic message", tc.args)
			}
		})
	}
}

// TestCLISweepDecideNeverReapsOnGhFailure asserts the fail-safe property
// at the exact process boundary the sweeper workflow crosses: a non-zero
// gh-exit-code must exit 1 (skip) regardless of what gh printed to
// stdout -- including reap-looking JSON, so a caller can never trigger a
// reap by racing gh's own exit code against its stdout. This is what
// directly answers task-150 AC-8's "fail-safe logic its real caller
// cannot reach is not fail-safe": it asserts the whole
// gh-exit-code -> ParsePRState -> ShouldReap -> CLI exit chain at the
// process boundary the sweeper actually crosses, not just the pure Go
// functions in isolation.
//
// Currently fails (actual exit code 2 via the "unknown subcommand" path)
// -- a legitimate RED, not a build error.
func TestCLISweepDecideNeverReapsOnGhFailure(t *testing.T) {
	cases := []struct {
		name   string
		ghJSON string
	}{
		{"empty stdout on gh failure", ""},
		{"reap-looking stdout on gh failure must not reap", `{"state":"MERGED"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, code := runCLI(t, "sweep-decide", "pr-42", "true", "1", tc.ghJSON)
			if code != 1 {
				t.Errorf("gh-exit-code=1, gh-json=%q: exit code = %d, want 1", tc.ghJSON, code)
			}
		})
	}
}
