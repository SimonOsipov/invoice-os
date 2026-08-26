// AUDIT-11-08 AC-5 (source): the read contract must record the write-side rule as a
// derivation, not just assert the key exists -- naming the generated column that
// enumerates which writers it binds, and the scan test that keeps the claim honest.
// No DB needed: this reads docs/, like scoped_test.go reads migrations.FS.
package audit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const docContractPath = "docs/audit-log-read-contract.md"

// docContractScanTest is the AUDIT-11-06 scan (internal/platform/db/audit_number_scan_test.go)
// that enforces every invoice-scoped writer carries invoice_number. Named here, not imported:
// it lives in a different package and asserting by name is the point of AC-5.
const docContractScanTest = "TestRLS_EveryInvoiceScopedWriterCarriesTheNumber"

const (
	docContractSectionNumber   = "10.13"
	docContractMinSectionRunes = 200
)

var docContractWhitespaceRun = regexp.MustCompile(`\s+`)

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

// docContractSection returns §10.13's own text, heading included, whitespace
// collapsed to single spaces -- so every check below reads only that section, not
// the whole 600+-line document (§10.8/§10.12/§7 already say "invoice_number" and
// "audit_log.invoice_id", which made the old whole-file checks vacuous for §10.13).
func docContractSection(t *testing.T, doc string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	var starts []int
	for i, line := range lines {
		if !strings.HasPrefix(line, "### ") {
			continue
		}
		if fields := strings.Fields(strings.TrimPrefix(line, "### ")); len(fields) > 0 && fields[0] == docContractSectionNumber {
			starts = append(starts, i)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("%s holds %d heading(s) numbered %s, want exactly 1 -- a renamed, deleted or duplicated heading leaves this oracle reading the wrong prose, or none", docContractPath, len(starts), docContractSectionNumber)
	}
	end := len(lines)
	for i := starts[0] + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") || strings.HasPrefix(lines[i], "### ") {
			end = i
			break
		}
	}
	body := strings.TrimSpace(docContractWhitespaceRun.ReplaceAllString(strings.Join(lines[starts[0]:end], " "), " "))
	if n := len([]rune(body)); n < docContractMinSectionRunes {
		t.Fatalf("%s section %s carries %d rune(s), want at least %d -- an absence check over an emptied section always passes", docContractPath, docContractSectionNumber, n, docContractMinSectionRunes)
	}
	return body
}

// docContractHandMaintainedPhrases is the false claim this subtask rules out: the
// writer set is someone's manually kept list, not a derivation off the generated
// column. The correct heading itself says "not hand-maintained", so a bare
// substring match would fail on the correct doc too -- a hit immediately preceded
// by a negating word is read as the negation, not the claim.
var docContractHandMaintainedPhrases = []string{"by hand", "hand-maintained", "manually", "spreadsheet"}
var docContractNegators = []string{"not", "isn't", "is not", "never", "no longer"}

func docContractClaimsHandMaintained(section string) bool {
	lower := strings.ToLower(section)
	for _, phrase := range docContractHandMaintainedPhrases {
		for idx := 0; ; {
			i := strings.Index(lower[idx:], phrase)
			if i < 0 {
				break
			}
			pos := idx + i
			before := strings.TrimRight(lower[:pos], " ")
			negated := false
			for _, neg := range docContractNegators {
				if strings.HasSuffix(before, neg) {
					negated = true
					break
				}
			}
			if !negated {
				return true
			}
			idx = pos + len(phrase)
		}
	}
	return false
}

func TestAuditDoc_ReadContractRecordsTheWriteSideRule(t *testing.T) {
	root := docContractRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(docContractPath)))
	if err != nil {
		t.Fatalf("read %s: %v", docContractPath, err)
	}
	doc := string(raw)
	section := docContractSection(t, doc)
	lowerSection := strings.ToLower(section)

	if !strings.Contains(section, "invoice_number") {
		t.Errorf("%s section %s never names invoice_number -- the write-side rule has nothing to point at", docContractPath, docContractSectionNumber)
	}
	if !strings.Contains(section, "audit_log.invoice_id") {
		t.Errorf("%s section %s does not name audit_log.invoice_id -- the write-side rule must name the generated column, not just assert the key by fiat", docContractPath, docContractSectionNumber)
	}
	if !strings.Contains(lowerSection, "enumerat") {
		t.Errorf("%s section %s never says the writer set is ENUMERATED by the generated column -- AC-5 wants the column named as the enumeration source, not merely mentioned elsewhere in the page", docContractPath, docContractSectionNumber)
	}
	if !strings.Contains(section, docContractScanTest) {
		t.Errorf("%s section %s does not name %s -- a rule with no test named against it decays the moment the scan is renamed or deleted", docContractPath, docContractSectionNumber, docContractScanTest)
	}
	if docContractClaimsHandMaintained(section) {
		t.Errorf("%s section %s claims the writer set is hand-maintained -- the opposite of what AC-5 requires: it must be DERIVED from the generated column (§11)", docContractPath, docContractSectionNumber)
	}
}
