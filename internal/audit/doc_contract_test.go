// AUDIT-11-08 AC-5 (source): the read contract must record the write-side rule as a
// derivation, not just assert the key exists -- naming the generated column that
// enumerates which writers it binds, and the scan test that keeps the claim honest.
// No DB needed: this reads docs/, like scoped_test.go reads migrations.FS.
package audit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
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

// --- §11: the vocabulary counts ---------------------------------------------------------

const docContractVocabSection = "11"

var docContractIntRE = regexp.MustCompile(`\d+`)

// docContractTopSection returns one `## <number>.` section's heading tail and body, with
// whitespace collapsed so a line wrap cannot hide a phrase.
func docContractTopSection(t *testing.T, doc, number string) (heading, body string) {
	t.Helper()
	lines := strings.Split(doc, "\n")
	prefix := "## " + number + "."
	start := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if start >= 0 {
			t.Fatalf("%s holds more than one heading starting %q -- this oracle would read the wrong prose", docContractPath, prefix)
		}
		start = i
	}
	if start < 0 {
		t.Fatalf("%s holds no heading starting %q -- a renamed or deleted section leaves this oracle reading nothing", docContractPath, prefix)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	body = strings.TrimSpace(docContractWhitespaceRun.ReplaceAllString(strings.Join(lines[start+1:end], " "), " "))
	if n := len([]rune(body)); n < docContractMinSectionRunes {
		t.Fatalf("%s section %s carries %d rune(s), want at least %d -- every check below would read an emptied section", docContractPath, number, n, docContractMinSectionRunes)
	}
	return strings.TrimPrefix(lines[start], prefix), body
}

// docContractSpell renders 0-99 the way §11's prose does. Total over the range, so a derived
// count with no word fails loudly rather than matching nothing.
func docContractSpell(t *testing.T, n int) string {
	t.Helper()
	units := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
		"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen",
		"eighteen", "nineteen"}
	tens := []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	if n < 0 || n > 99 {
		t.Fatalf("count %d is outside the range this test can spell", n)
	}
	switch {
	case n < 20:
		return units[n]
	case n%10 == 0:
		return tens[n/10]
	default:
		return tens[n/10] + "-" + units[n%10]
	}
}

// §11's counts must be DERIVED, which is what §11 itself demands of its reader. The numerator
// comes from the generated column's own two dispatch lists, the denominator from the four
// rule-set fixtures. Heading digits and prose words are checked separately: EXTR-08-04 moved
// both by hand after the vocabulary grew, and nothing could have caught either.
func TestAuditDoc_ReadContractVocabularyCountsAreDerived(t *testing.T) {
	root := docContractRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(docContractPath)))
	if err != nil {
		t.Fatalf("read %s: %v", docContractPath, err)
	}

	idEvents, invoiceIDEvents := scopedEventListsFromExpression(t, scopedInvoiceIDMigrationUp(t))
	scoped := len(idEvents) + len(invoiceIDEvents)
	ruleD := triggerRuleDPayloads("00000000-0000-0000-0000-000000000000")
	vocabulary := len(triggerRuleAEvents) + len(triggerRuleBEvents) + len(triggerRuleCEvents) + len(ruleD)

	// Floors: an empty list would turn every comparison below into a claim about zero.
	if len(idEvents) == 0 || len(invoiceIDEvents) == 0 {
		t.Fatalf("the generated column's dispatch lists hold %d and %d events, want both non-empty",
			len(idEvents), len(invoiceIDEvents))
	}
	if vocabulary <= scoped {
		t.Fatalf("vocabulary %d is not larger than the scoped set %d -- the fixtures cannot be right", vocabulary, scoped)
	}

	heading, body := docContractTopSection(t, string(raw), docContractVocabSection)
	got := docContractIntRE.FindAllString(heading, -1)
	want := []string{strconv.Itoa(scoped), strconv.Itoa(vocabulary)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s section %s heading carries the numbers %v, want %v -- these are derived from the migration and the rule sets, never restated",
			docContractPath, docContractVocabSection, got, want)
	}

	// The prose says the same numbers in words, and says how each half of the numerator splits.
	for _, w := range []string{
		docContractSpell(t, scoped),
		docContractSpell(t, vocabulary),
		docContractSpell(t, len(idEvents)),
		docContractSpell(t, len(invoiceIDEvents)),
	} {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(w) + `\b`).MatchString(body) {
			t.Errorf("%s section %s never says %q -- its prose has drifted from the migration it claims to derive from",
				docContractPath, docContractVocabSection, w)
		}
	}
}
