// deps_test.go: the static guards around the end-to-end suite -- both import fences, the
// single-skip-site rule, and the package count the gated CI step now has to schedule.
// No database.
package endtoend

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	eeModulePath    = "github.com/SimonOsipov/invoice-os"
	eeExtractionPkg = eeModulePath + "/internal/extraction"
	eeInvoicePkg    = eeModulePath + "/internal/invoice"
	eeDocumentPkg   = eeModulePath + "/internal/document"
	eePlatformPfx   = eeModulePath + "/internal/platform/"
	eeSelfPkg       = eeExtractionPkg + "/endtoend"

	// A truncated or empty `go list -deps` output must not read as a clean scan.
	eeMinDepsFloor = 20
	// Two test files today; a scan that reads fewer is reading the wrong directory.
	eeMinTestFiles = 2
	// One package before this subtask, two after.
	eeMinExtractionPkgs = 2
)

// eeRepoRoot pins `go list` to THIS worktree. A sibling /ralph worktree under
// .claude/worktrees/ would otherwise be scanned by a relative path.
func eeRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func eeGoList(t *testing.T, args ...string) []string {
	t.Helper()
	argv := append([]string{"list"}, args...)
	cmd := exec.CommandContext(t.Context(), "go", argv...)
	cmd.Dir = eeRepoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// eeDep strips the "<pkg> [<pkg>.test]" annotation `go list -deps -test` puts on one line.
func eeDep(raw string) string {
	dep, _, _ := strings.Cut(strings.TrimSpace(raw), " ")
	return dep
}

// AC-2. Adding a sibling subdirectory package must not widen internal/extraction's own dep
// set: only internal/platform/* is allowed, and internal/document is the edge the fence exists
// to stop.
func TestEndToEndPackage_DoesNotBreakTheExtractionFence(t *testing.T) {
	for _, scan := range []struct {
		name string
		args []string
	}{
		{"scan A", []string{"-deps", eeExtractionPkg}},
		{"scan B", []string{"-deps", "-test", eeExtractionPkg}},
	} {
		lines := eeGoList(t, scan.args...)
		if len(lines) < eeMinDepsFloor {
			t.Fatalf("%s: go list returned %d line(s), want at least %d -- the fence below would pass vacuously", scan.name, len(lines), eeMinDepsFloor)
		}

		// Control needles: a scan that names neither reports "clean" on an empty read.
		var sawSelf, sawContext bool
		for _, raw := range lines {
			switch eeDep(raw) {
			case eeExtractionPkg:
				sawSelf = true
			case "context":
				sawContext = true
			}
		}
		if !sawSelf || !sawContext {
			t.Fatalf("%s: sawSelf=%v sawContext=%v -- the scan itself is broken, so the fence proves nothing", scan.name, sawSelf, sawContext)
		}

		for _, raw := range lines {
			dep := eeDep(raw)
			switch dep {
			case "", eeExtractionPkg, eeExtractionPkg + "_test", eeExtractionPkg + ".test":
				continue
			}
			if dep != eeModulePath && !strings.HasPrefix(dep, eeModulePath+"/") {
				continue
			}
			if strings.HasPrefix(dep, eePlatformPfx) {
				continue
			}
			if dep == eeDocumentPkg {
				t.Errorf("%s: internal/extraction depends on %s -- content arrives via the OpenDocument func, and this edge drags the AWS SDK in with it", scan.name, dep)
				continue
			}
			t.Errorf("%s: internal/extraction depends on %s -- only internal/platform/* is allowed", scan.name, dep)
		}
	}
}

// AC-3. internal/importer must never import internal/extraction; that edge drags go-pdfium
// into cmd/invoice.
func TestEndToEndPackage_DoesNotBreakTheImporterFence(t *testing.T) {
	lines := eeGoList(t, "-deps", "./internal/importer")
	if len(lines) < eeMinDepsFloor {
		t.Fatalf("go list -deps ./internal/importer returned %d line(s), want at least %d -- the scan looks truncated, not a clean repo", len(lines), eeMinDepsFloor)
	}

	var sawControl, sawForbidden bool
	for _, raw := range lines {
		switch eeDep(raw) {
		case eeInvoicePkg:
			sawControl = true
		case eeExtractionPkg:
			sawForbidden = true
		}
	}
	if !sawControl {
		t.Fatalf("go list -deps ./internal/importer never named %s -- the scan is broken, so the forbidden-import check proves nothing", eeInvoicePkg)
	}
	if sawForbidden {
		t.Errorf("internal/importer imports %s -- forbidden: that edge drags go-pdfium into cmd/invoice", eeExtractionPkg)
	}
}

// AC-5. scripts/ci/rls-test-gate.sh fails a step on any skip, so eeRequire must stay this
// package's only t.Skip. Both needles are assembled from fragments so this file does not match
// its own scan.
func TestEndToEndPackage_HasExactlyOneSkipSite(t *testing.T) {
	// "." not a path: a test binary's CWD is its own package directory.
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob *_test.go: %v", err)
	}
	// The floor first: this scan asserts an ABSENCE, and zero files read reports clean.
	if len(names) < eeMinTestFiles {
		t.Fatalf("read %d test file(s) in internal/extraction/endtoend, want at least %d", len(names), eeMinTestFiles)
	}

	skipCall := regexp.MustCompile(`\bt\.Sk` + `ip(f|Now)?\(`)
	declRE := regexp.MustCompile(`func eeReq` + `uire\(`)

	skipSites := map[string]int{}
	var declares []string
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(raw)
		if n := len(skipCall.FindAllString(src, -1)); n > 0 {
			skipSites[name] = n
		}
		if declRE.MatchString(src) {
			declares = append(declares, name)
		}
	}

	// Control needle: the sanctioned site must be found, or the count below reads the same on
	// a package whose files stopped being parsed at all.
	if len(declares) != 1 || declares[0] != eeSkipFile {
		t.Fatalf("eeRequire is declared in %v, want exactly [%s]", declares, eeSkipFile)
	}
	if got := skipSites[eeSkipFile]; got != 1 {
		t.Errorf("%s holds %d t.Skip call(s), want exactly 1 (eeRequire)", eeSkipFile, got)
	}
	for name, n := range skipSites {
		if name != eeSkipFile {
			t.Errorf("%s holds %d t.Skip call(s); eeRequire is this package's only sanctioned skip site and the CI gate fails on a second", name, n)
		}
	}
}

const eeSkipFile = "harness_db_test.go"

// AC-7. ./internal/extraction/... is now two DB-backed packages under one glob sharing one
// Postgres -- the reason EXTR-21-08 adds -p 1 to the gated CI step.
func TestEndToEnd_TheGatedStepNeedsOnePackageAtATime(t *testing.T) {
	pkgs := eeGoList(t, "./internal/extraction/...")
	if len(pkgs) < eeMinExtractionPkgs {
		t.Fatalf("go list ./internal/extraction/... names %d package(s) (%v), want at least %d", len(pkgs), pkgs, eeMinExtractionPkgs)
	}
	var sawSelf bool
	for _, p := range pkgs {
		if strings.TrimSpace(p) == eeSelfPkg {
			sawSelf = true
		}
	}
	if !sawSelf {
		t.Fatalf("go list ./internal/extraction/... never named %s (%v) -- the glob no longer covers this suite, so the gated step does not run it", eeSelfPkg, pkgs)
	}
}
