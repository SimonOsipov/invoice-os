// document_deps_test.go: SX-09 (EXTR-06-01, task-761) -- internal/importer must never import
// internal/extraction; that edge would drag github.com/klippa-app/go-pdfium into cmd/invoice
// (System Design, "Why internal/importer and not internal/extraction"). Template:
// internal/submission/deps_test.go.
package importer

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	sxForbiddenImport = "github.com/SimonOsipov/invoice-os/internal/extraction"
	sxControlImport   = "github.com/SimonOsipov/invoice-os/internal/invoice"
	// sxMinDepsFloor: a truncated or empty `go list -deps` output must not read as a clean
	// scan. internal/importer already pulls in internal/invoice, internal/document,
	// internal/platform/*, pgx and the Go stdlib -- comfortably past this floor today.
	sxMinDepsFloor = 20
)

// sxDepsRepoRoot resolves the worktree root `go list` must run from. Duplicated (not
// imported) from internal/submission/deps_test.go's repoRootForDepsTest -- different test
// package, unrelated fence.
func sxDepsRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// SX-09: the control needle (sxControlImport) and population floor (sxMinDepsFloor) guard
// against a scan that silently sees nothing and passes vacuously.
func TestImporterPackage_DoesNotImportExtractionPackage(t *testing.T) {
	root := sxDepsRepoRoot(t)
	cmd := exec.Command("go", "list", "-deps", "./internal/importer")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/importer: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < sxMinDepsFloor {
		t.Fatalf("go list -deps ./internal/importer returned %d lines, want at least %d -- scan looks truncated, not a clean repo", len(lines), sxMinDepsFloor)
	}

	var sawControl, sawForbidden bool
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case sxControlImport:
			sawControl = true
		case sxForbiddenImport:
			sawForbidden = true
		}
	}
	if !sawControl {
		t.Fatalf("go list -deps ./internal/importer never named %s -- the scan itself is broken, so the forbidden-import check below proves nothing", sxControlImport)
	}
	if sawForbidden {
		t.Errorf("internal/importer imports %s -- forbidden: that edge drags go-pdfium into cmd/invoice", sxForbiddenImport)
	}
}
