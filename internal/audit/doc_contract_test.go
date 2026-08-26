// AUDIT-11-08 AC-5 (source): the read contract must record the write-side rule as a
// derivation, not just assert the key exists -- naming the generated column that
// enumerates which writers it binds, and the scan test that keeps the claim honest.
// No DB needed: this reads docs/, like scoped_test.go reads migrations.FS.
package audit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const docContractPath = "docs/audit-log-read-contract.md"

// docContractScanTest is the AUDIT-11-06 scan (internal/platform/db/audit_number_scan_test.go)
// that enforces every invoice-scoped writer carries invoice_number. Named here, not imported:
// it lives in a different package and asserting by name is the point of AC-5.
const docContractScanTest = "TestRLS_EveryInvoiceScopedWriterCarriesTheNumber"

func docContractRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root")
	}
	return root
}

func TestAuditDoc_ReadContractRecordsTheWriteSideRule(t *testing.T) {
	root := docContractRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(docContractPath)))
	if err != nil {
		t.Fatalf("read %s: %v", docContractPath, err)
	}
	doc := string(raw)

	if !strings.Contains(doc, "invoice_number") {
		t.Errorf("%s never names invoice_number -- the write-side rule has nothing to point at", docContractPath)
	}
	if !strings.Contains(doc, "audit_log.invoice_id") {
		t.Errorf("%s does not name audit_log.invoice_id -- the write-side rule must name the generated column, not just assert the key by fiat", docContractPath)
	}
	if !strings.Contains(strings.ToLower(doc), "enumerat") {
		t.Errorf("%s never says the writer set is ENUMERATED by the generated column -- AC-5 wants the column named as the enumeration source, not merely mentioned elsewhere in the page", docContractPath)
	}
	if !strings.Contains(doc, docContractScanTest) {
		t.Errorf("%s does not name %s -- a rule with no test named against it decays the moment the scan is renamed or deleted", docContractPath, docContractScanTest)
	}
}
