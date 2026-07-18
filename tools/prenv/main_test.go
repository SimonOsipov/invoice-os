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
