// main_test.go builds the prenv binary once and execs it: exit 2 means a malformed call, with
// a diagnostic on stderr and nothing on stdout.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	shimTrampoline = filepath.Join(dir, "shim-trampoline")
	if err := os.WriteFile(shimTrampoline, []byte("#!/bin/sh\nexec /bin/sh \"$0.body\" \"$@\"\n"), 0o555); err != nil {
		panic(err)
	}
	warmShimTrampoline(dir)

	os.Exit(m.Run())
}

// warmShimTrampoline execs the trampoline once so the macOS first-exec delay lands before the parallel tests.
func warmShimTrampoline(dir string) {
	if runtime.GOOS != "darwin" {
		return
	}
	warm := filepath.Join(dir, "warm")
	if err := os.WriteFile(warm+".body", nil, 0o644); err != nil {
		panic(err)
	}
	if err := os.Symlink(shimTrampoline, warm); err != nil {
		panic(err)
	}
	if err := exec.Command(warm).Run(); err != nil {
		panic("warming the shim trampoline failed: " + err.Error())
	}
	os.Remove(warm)
	os.Remove(warm + ".body")
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

// TestCLICalledWrongExitsTwoWithCleanStdout covers every "you called me
// wrong" path the dispatch comment documents: missing subcommand, unknown
// subcommand, wrong arity for either subcommand, a non-numeric PR number,
// and a non-positive PR number. All must exit 2, and — the property that
// actually matters to a caller capturing stdout as data — stdout must stay
// completely empty; every diagnostic goes to stderr only.
func TestCLICalledWrongExitsTwoWithCleanStdout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		args []string
	}{
		{"no subcommand", nil},
		{"unknown subcommand", []string{"bogus"}},
		{"jwk-es256 extra arg", []string{"jwk-es256", "x"}},
		{"jwk-check extra arg", []string{"jwk-check", "x"}},
		{"mail-logo-check extra arg", []string{"mail-logo-check", "x"}},
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

func runToFiles(t *testing.T, cmd *exec.Cmd, merge bool) (stdout, stderr string, err error) {
	t.Helper()
	dir := t.TempDir()
	outF, ferr := os.Create(filepath.Join(dir, "stdout"))
	if ferr != nil {
		t.Fatal(ferr)
	}
	defer outF.Close()
	errF := outF
	if !merge {
		if errF, ferr = os.Create(filepath.Join(dir, "stderr")); ferr != nil {
			t.Fatal(ferr)
		}
		defer errF.Close()
	}
	cmd.Stdout, cmd.Stderr = outF, errF
	err = cmd.Run()
	o, rerr := os.ReadFile(outF.Name())
	if rerr != nil {
		t.Fatal(rerr)
	}
	stdout = string(o)
	if !merge {
		e, rerr := os.ReadFile(errF.Name())
		if rerr != nil {
			t.Fatal(rerr)
		}
		stderr = string(e)
	}
	return stdout, stderr, err
}
