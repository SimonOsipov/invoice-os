// deps_test.go: the two static import-fence guards -- no cmd package may
// depend on this measurement harness, and this harness imports only the
// standard library. Both are guards, vacuous by construction until the
// package compiles; see the mutation-ledger note on each.
package jevmeasure

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	jmModulePath = "github.com/SimonOsipov/invoice-os"
	jmSelfPkg    = jmModulePath + "/internal/jevmeasure"
	jmControlPkg = jmModulePath + "/internal/platform/ai"

	// Measured today (b9cf1424): 606 lines, 35 module-path packages. A
	// truncated or empty scan must not read as clean.
	jmMinCmdLines = 500
	jmMinCmdPkgs  = 30
	jmMinSelfDeps = 5
)

func jmRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func jmGoList(t *testing.T, args ...string) []string {
	t.Helper()
	argv := append([]string{"list"}, args...)
	cmd := exec.CommandContext(t.Context(), "go", argv...)
	cmd.Dir = jmRepoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// jmDep strips the "<pkg> [<pkg>.test]" annotation `go list -deps -test` puts on one line.
func jmDep(raw string) string {
	dep, _, _ := strings.Cut(strings.TrimSpace(raw), " ")
	return dep
}

// AC-13. Vacuous by construction: nothing imports jevmeasure yet, so this
// passes the moment the package compiles, whatever cmd/... does. Mutation
// ledger: import jevmeasure from a cmd package -- must red.
func TestJevMeasure_NoCommandDependsOnIt(t *testing.T) {
	lines := jmGoList(t, "-deps", "./cmd/...")
	if len(lines) < jmMinCmdLines {
		t.Fatalf("go list -deps ./cmd/... returned %d line(s), want at least %d -- a truncated scan reports clean vacuously", len(lines), jmMinCmdLines)
	}

	modPkgs := map[string]bool{}
	var sawControl, sawSelf bool
	for _, raw := range lines {
		dep := jmDep(raw)
		if dep == jmControlPkg {
			sawControl = true
		}
		if dep == jmSelfPkg {
			sawSelf = true
		}
		if dep == jmModulePath || strings.HasPrefix(dep, jmModulePath+"/") {
			modPkgs[dep] = true
		}
	}
	if !sawControl {
		t.Fatalf("control needle %s not found -- the scan itself is broken", jmControlPkg)
	}
	if len(modPkgs) < jmMinCmdPkgs {
		t.Fatalf("scan named %d module-path package(s), want at least %d -- looks truncated", len(modPkgs), jmMinCmdPkgs)
	}
	if sawSelf {
		t.Errorf("./cmd/... depends on %s -- no product command may import the measurement harness", jmSelfPkg)
	}
}

// AC-14. Same guard shape as above -- what keeps the internal/importer fence
// irrelevant to a _test.go import. Mutation ledger: import internal/extraction
// from jevmeasure -- must red.
func TestJevMeasure_ImportsOnlyTheStandardLibrary(t *testing.T) {
	lines := jmGoList(t, "-deps", "./internal/jevmeasure")
	if len(lines) < jmMinSelfDeps {
		t.Fatalf("go list -deps ./internal/jevmeasure returned %d line(s), want at least %d", len(lines), jmMinSelfDeps)
	}

	var sawJSON, sawSelf bool
	for _, raw := range lines {
		dep := jmDep(raw)
		if dep == "encoding/json" {
			sawJSON = true
		}
		if dep == jmSelfPkg {
			sawSelf = true
			continue
		}
		if dep == jmModulePath || strings.HasPrefix(dep, jmModulePath+"/") {
			t.Errorf("internal/jevmeasure depends on %s -- stdlib only", dep)
		}
	}
	if !sawJSON {
		t.Fatalf("control needle encoding/json not found -- the scan itself is broken")
	}
	if !sawSelf {
		t.Fatalf("scan never named %s -- the scan itself is broken", jmSelfPkg)
	}
}
