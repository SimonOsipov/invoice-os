// deps_test.go: the import fence. internal/extraction reaches stored content only
// through the OpenDocument func, never by importing internal/document.
package extraction_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	modulePath    = "github.com/SimonOsipov/invoice-os"
	extractionPkg = modulePath + "/internal/extraction"
	documentPkg   = modulePath + "/internal/document"
	platformPfx   = modulePath + "/internal/platform/"
)

// goListDeps shells go list in this package's directory. "." not a path: a test
// binary's CWD is its own package directory.
func goListDeps(t *testing.T, extra ...string) []string {
	t.Helper()

	argv := append([]string{"list", "-deps"}, extra...)
	argv = append(argv, ".")
	out, err := exec.CommandContext(t.Context(), "go", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// fenceT is the narrow t assertFenced needs. *testing.T satisfies it; so does the recorder
// in extractor_edge_test.go that proves the fence reports anything at all.
type fenceT interface {
	Helper()
	Errorf(format string, args ...any)
}

// assertFenced flags every in-module dependency outside this package and
// internal/platform/*. It matches on the full module path: the stdlib ships
// packages literally named internal/abi, internal/platform and internal/goroot,
// so a bare "internal/" prefix test fires on Go's own runtime internals.
func assertFenced(t fenceT, scan string, lines []string) {
	t.Helper()

	for _, raw := range lines {
		// go list -deps -test annotates one line as "<pkg>_test [<pkg>.test]".
		dep, _, _ := strings.Cut(strings.TrimSpace(raw), " ")

		switch dep {
		case "", extractionPkg, extractionPkg + "_test", extractionPkg + ".test":
			continue
		}
		if dep != modulePath && !strings.HasPrefix(dep, modulePath+"/") {
			continue
		}
		if strings.HasPrefix(dep, platformPfx) {
			continue
		}
		if dep == documentPkg {
			t.Errorf("%s: internal/extraction depends on %s -- content arrives via the OpenDocument func, and this edge would drag the AWS SDK in with it", scan, dep)
			continue
		}
		t.Errorf("%s: internal/extraction depends on %s -- only internal/platform/* is allowed, and internal/document is the edge this fence exists to stop", scan, dep)
	}
}

func TestExtractionPackage_DoesNotImportDocumentPackage(t *testing.T) {
	scanA := goListDeps(t)
	if len(scanA) < 2 {
		t.Fatalf("scan A: go list -deps returned %d lines; the fence below would pass vacuously", len(scanA))
	}
	var sawSelf, sawContext bool
	for _, line := range scanA {
		switch strings.TrimSpace(line) {
		case extractionPkg:
			sawSelf = true
		case "context":
			sawContext = true
		}
	}
	if !sawSelf {
		t.Fatalf("scan A: %s absent from its own dep list; the fence below would pass vacuously", extractionPkg)
	}
	if !sawContext {
		t.Fatalf("scan A: context absent from the dep list; the fence below would pass vacuously")
	}
	assertFenced(t, "scan A", scanA)

	// Scan B: go list -deps EXCLUDES test imports, so scan A alone cannot see a
	// fixture test dragging internal/document back in.
	scanB := goListDeps(t, "-test")
	if len(scanB) < 2 {
		t.Fatalf("scan B: go list -deps -test returned %d lines; the fence below would pass vacuously", len(scanB))
	}
	assertFenced(t, "scan B", scanB)
}

// EXTR-15-01 FK-11 (AC-11). scripts/ci/rls-test-gate.sh fails a step on any skipped or
// zero-ran suite, so stRequire is this package's one sanctioned skip site. A story that adds
// a second one reds here rather than on the gate.
func TestExtractionPackage_HasExactlyOneSkipSite(t *testing.T) {
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob *_test.go: %v", err)
	}
	// The floor first: this scan asserts an ABSENCE, and zero files read reports clean.
	// 91 measured at aaee0c3d.
	if len(names) < 30 {
		t.Fatalf("read %d test file(s) in internal/extraction, want at least 30 (91 measured)", len(names))
	}

	// Both needles are assembled from fragments so this file does not match its own scan.
	skipCall := regexp.MustCompile(`\bt\.Sk` + `ip(f|Now)?\(`)
	declRE := regexp.MustCompile(`func stReq` + `uire\(`)

	skipSites := map[string]int{}
	declares := []string{}
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

	// The control needle: the one sanctioned site must still be found, or the count below
	// reads the same on a package whose test files stopped being parsed at all.
	if len(declares) != 1 || declares[0] != "store_db_test.go" {
		t.Fatalf("stRequire is declared in %v, want exactly [store_db_test.go]", declares)
	}
	if got := skipSites["store_db_test.go"]; got != 1 {
		t.Errorf("store_db_test.go holds %d t.Skip call(s), want exactly 1 (stRequire)", got)
	}
	for name, n := range skipSites {
		if name != "store_db_test.go" {
			t.Errorf("%s holds %d t.Skip call(s); stRequire is this package's only sanctioned skip site and the CI gate fails on a second", name, n)
		}
	}
}
