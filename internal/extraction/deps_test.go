// deps_test.go: the import fence. internal/extraction reaches stored content only
// through the OpenDocument func, never by importing internal/document.
package extraction_test

import (
	"os/exec"
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

// assertFenced flags every in-module dependency outside this package and
// internal/platform/*. It matches on the full module path: the stdlib ships
// packages literally named internal/abi, internal/platform and internal/goroot,
// so a bare "internal/" prefix test fires on Go's own runtime internals.
func assertFenced(t *testing.T, scan string, lines []string) {
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
