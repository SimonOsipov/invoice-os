package submission_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestSubmissionPackage_DoesNotImportInvoicePackage proves the
// [mapper-lives-in-03] direction: internal/submission must never import
// internal/invoice, even transitively -- M5-04 makes internal/invoice import
// internal/submission's job args, and the reverse edge would close that
// cycle. Mirrors internal/invoice/validator_test.go:452's
// TestValidatorClient_DoesNotImportValidationPackage, applied to the
// opposite package pair and direction.
//
// Unlike that precedent (which fails today because of a deliberate scaffold
// violation it exists to catch), this is a pure baseline/regression guard:
// internal/submission has never imported internal/invoice, and this
// subtask's mapper points the other way (internal/invoice imports
// internal/submission), so this test passes from the moment it is written
// and stays green. It is not a red-to-green spec for THIS subtask -- its
// job is to catch a future PR that accidentally reverses the direction.
func TestSubmissionPackage_DoesNotImportInvoicePackage(t *testing.T) {
	root := repoRootForDepsTest(t)
	cmd := exec.Command("go", "list", "-deps", "./internal/submission")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/submission: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "github.com/SimonOsipov/invoice-os/internal/invoice" {
			t.Errorf("internal/submission imports internal/invoice -- forbidden by [mapper-lives-in-03]: " +
				"the mapper lives in internal/invoice and imports internal/submission, never the reverse")
			return
		}
	}
}

// repoRootForDepsTest resolves the git worktree root. Duplicated (not
// imported) from internal/invoice/validator_test.go's
// repoRootForValidatorTest, because internal/submission must not import
// internal/invoice even in test code -- that import-direction rule is
// exactly what this file exists to enforce, so it cannot itself violate it.
func repoRootForDepsTest(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}
