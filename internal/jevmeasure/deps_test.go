// deps_test.go: the two static import-fence guards -- no cmd package may
// depend on this measurement harness, and this harness imports only the
// standard library. Both are guards, vacuous by construction until the
// package compiles; see the mutation-ledger note on each.
package jevmeasure

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	jmModulePath  = "github.com/SimonOsipov/invoice-os"
	jmSelfPkg     = jmModulePath + "/internal/jevmeasure"
	jmEndtoendPkg = jmModulePath + "/internal/extraction/endtoend"
	jmControlPkg  = jmModulePath + "/internal/platform/ai"

	// Measured today (CHECK-01-07): 606 lines, 35 module-path packages. A
	// truncated or empty scan must not read as clean. D-9: raised from the
	// original 500/30 margin to close to the real measurement.
	jmMinCmdLines = 600
	jmMinCmdPkgs  = 34
	jmMinSelfDeps = 5

	// jmMinCmdTestLines/jmMinCmdTestPkgs: the -test closure (`go list -deps -test ./cmd/...`)
	// is strictly stronger than jmMinCmdLines/jmMinCmdPkgs -- it also sees a cmd/*_test.go
	// import the non-test scan cannot. Measured today: 617 lines, 38 packages.
	jmMinCmdTestLines = 550
	jmMinCmdTestPkgs  = 30
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

// AC-9 row, respecified (D-9): TestJevMeasure_NoCommandDependsOnIt already covers the build
// closure; this covers the -test closure, which a cmd/*_test.go importing the harness would
// slip past the build-only scan. Mutation ledger: import jevmeasure from any cmd test file --
// must red; from any cmd non-test file -- both this and TestJevMeasure_NoCommandDependsOnIt red.
func TestJevGuard_NoCommandImportsTheHarness(t *testing.T) {
	lines := jmGoList(t, "-deps", "-test", "./cmd/...")
	if len(lines) < jmMinCmdTestLines {
		t.Fatalf("go list -deps -test ./cmd/... returned %d line(s), want at least %d -- a truncated scan reports clean vacuously", len(lines), jmMinCmdTestLines)
	}

	modPkgs := map[string]bool{}
	var sawControl, sawSelf, sawEndtoend bool
	for _, raw := range lines {
		dep := jmDep(raw)
		if dep == jmControlPkg {
			sawControl = true
		}
		if dep == jmSelfPkg {
			sawSelf = true
		}
		if dep == jmEndtoendPkg {
			sawEndtoend = true
		}
		if dep == jmModulePath || strings.HasPrefix(dep, jmModulePath+"/") {
			modPkgs[dep] = true
		}
	}
	if !sawControl {
		t.Fatalf("control needle %s not found -- the scan itself is broken", jmControlPkg)
	}
	if len(modPkgs) < jmMinCmdTestPkgs {
		t.Fatalf("scan named %d module-path package(s), want at least %d -- looks truncated", len(modPkgs), jmMinCmdTestPkgs)
	}
	if sawSelf {
		t.Errorf("./cmd/...'s test closure depends on %s -- no product command may import the measurement harness", jmSelfPkg)
	}
	if sawEndtoend {
		t.Errorf("./cmd/...'s test closure depends on %s -- the endtoend measurement suite must not leak into a shipped command", jmEndtoendPkg)
	}
}

// jmFilesystemImports are the packages that would give jevmeasure a filesystem of its own.
// D-1 keeps the harness filesystem-free for a reason the two gated runs depend on: every
// artifact write goes through their own seam, so jvWrite's jvPaths audit and
// TestJevValue_TheGoHarnessOpensOnlyFixtureRootPaths' exactly-one-call-site source leg still
// see it. TestJevMeasure_ImportsOnlyTheStandardLibrary cannot catch this -- os IS stdlib.
var jmFilesystemImports = []string{"os", "os/exec", "path/filepath", "io/fs", "io/ioutil", "embed"}

// Mutation ledger: import "os" from any non-test file in this package -- must red.
func TestJevMeasure_OwnsNoFilesystem(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	banned := map[string]bool{}
	for _, p := range jmFilesystemImports {
		banned[p] = true
	}

	scanned, sawControl := 0, false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		scanned++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "encoding/json" {
				sawControl = true
			}
			if banned[path] {
				t.Errorf("%s imports %q -- jevmeasure must stay filesystem-free (D-1); the $JEV_OUT read/merge/write belongs to each gated run's own seam", filepath.Base(name), path)
			}
		}
	}
	if scanned < 4 {
		t.Fatalf("scanned %d non-test file(s) in this package, want at least 4 -- a walk that reads nothing reports clean vacuously", scanned)
	}
	if !sawControl {
		t.Fatalf("control needle encoding/json not found across %d file(s) -- the scan itself is broken", scanned)
	}
}
