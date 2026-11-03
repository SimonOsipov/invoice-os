package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const missingVarCopy = " is not set. Add it under Settings → Secrets and variables → Actions → Variables.\n"

// runRequireVars runs the script with exactly env set (plus PATH) and returns its streams and exit code.
func runRequireVars(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "scripts", "ci", "require-vars.sh")}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	stdout, stderr, err := runToFiles(t, cmd, false)
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running require-vars.sh: %v", err)
		}
		code = ee.ExitCode()
	}
	return stdout, stderr, code
}

func TestRequireVars_AllSetPrintsNothing(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runRequireVars(t, []string{"A=x", "B=y"}, "A", "B")
	if code != 0 || stdout != "" || stderr != "" {
		t.Errorf("code=%d stdout=%q stderr=%q, want 0 and empty streams", code, stdout, stderr)
	}
}

func TestRequireVars_NamesEachMissingVariable(t *testing.T) {
	t.Parallel()
	stdout, _, code := runRequireVars(t, []string{"A=x", "B="}, "A", "B")
	if code != 1 {
		t.Errorf("code=%d, want 1", code)
	}
	if want := "::error::B" + missingVarCopy; stdout != want {
		t.Errorf("stdout=%q, want %q", stdout, want)
	}
}

func TestRequireVars_TwoMissingInArgumentOrder(t *testing.T) {
	t.Parallel()
	stdout, _, code := runRequireVars(t, nil, "B", "A")
	if code != 1 {
		t.Errorf("code=%d, want 1", code)
	}
	if want := "::error::B" + missingVarCopy + "::error::A" + missingVarCopy; stdout != want {
		t.Errorf("stdout=%q, want %q", stdout, want)
	}
}

func TestRequireVars_NoArgumentIsAWrongCall(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runRequireVars(t, nil)
	if code != 2 || stdout != "" || stderr != "usage: require-vars.sh NAME...\n" {
		t.Errorf("code=%d stdout=%q stderr=%q, want 2, empty stdout, the usage line", code, stdout, stderr)
	}
}

func TestRequireVars_WhitespaceOnlyIsMissing(t *testing.T) {
	t.Parallel()
	stdout, _, code := runRequireVars(t, []string{"A= \t "}, "A")
	if code != 1 {
		t.Errorf("code=%d, want 1", code)
	}
	if want := "::error::A" + missingVarCopy; stdout != want {
		t.Errorf("stdout=%q, want %q", stdout, want)
	}
}
