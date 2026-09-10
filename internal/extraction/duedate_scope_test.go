// duedate_scope_test.go: EXTR-25-04 AC-4.8 and AC-4.11 -- due_date must reach no non-test
// source outside anchor.go, and the doc it falsifies must gain the qualifier that says so.
package extraction_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	ddNeedle        = "due_date"
	ddControlNeedle = "issue_date" // known present in non-test source; proves the scan finds hits at all
)

var ddScanExts = []string{".go", ".sql", ".ts", ".tsx"}

func ddScanEligible(path string) bool {
	ext := filepath.Ext(path)
	if !slices.Contains(ddScanExts, ext) {
		return false
	}
	if ext == ".go" && strings.HasSuffix(path, "_test.go") {
		return false
	}
	return true
}

// AC-4.8. An *ast.Ident walk (reachability_test.go's own mechanism) cannot see "due_date" as a
// STRING LITERAL, which is exactly this row's own mutation shape, so this reads raw bytes
// instead. Covers frontend/ (the existing walker skips it); skips node_modules and docling
// goldens, which are data, not source.
func TestExtraction_NoDueDateFieldExists(t *testing.T) {
	root := rxRepoRoot(t)

	var scanned int
	var hits []string
	var sawControl bool

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch filepath.Base(path) {
			case ".git", ".claude", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".docling.json") || !ddScanEligible(path) {
			return nil
		}

		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		text := string(b)
		if strings.Contains(text, ddControlNeedle) {
			sawControl = true
		}
		if strings.Contains(text, ddNeedle) {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if scanned < 200 {
		t.Fatalf("scanned %d file(s), want at least 200; the walk is reading the wrong tree", scanned)
	}
	if !sawControl {
		t.Fatalf("the control needle %q was found in no scanned file; a scan that finds nothing is indistinguishable from a broken walk", ddControlNeedle)
	}

	slices.Sort(hits)
	want := []string{"internal/extraction/anchor.go"}
	if !slices.Equal(hits, want) {
		t.Errorf("%q appears in %v, want exactly %v -- nothing outside the lexicon entry may carry a due date", ddNeedle, hits, want)
	}
}

// ddQualifierMarkers are the terms the added qualifier must carry: the AC's own language names
// the new entry a "rule-less owning-phrase entry".
var ddQualifierMarkers = []string{"rule-less", "owning phrase", "owning-phrase"}

func ddHasQualifier(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range ddQualifierMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// AC-4.11. Neither FingerprintVersion nor BoxlessFingerprintVersion moves, and both cited
// bump-rule passages in docs/extraction-corpus.md gain the qualifier distinguishing a widened
// existing pattern (bump) from a new rule-less owning-phrase entry (no bump). Each passage is
// graded on its own, so one qualifier cannot cover for the other.
func TestAnchorLexicon_TheBumpRuleDistinguishesANewEntryFromAWidenedPattern(t *testing.T) {
	if extraction.FingerprintVersion != "v3" {
		t.Errorf("FingerprintVersion = %q, want %q -- EXTR-26 widened six shared-lexicon patterns and stepped this lever; the next bump must be as deliberate", extraction.FingerprintVersion, "v3")
	}
	if extraction.BoxlessFingerprintVersion != "b3" {
		t.Errorf("BoxlessFingerprintVersion = %q, want %q -- anchorLabelMatchers feeds both producers, so the widening stepped this lever with the geometric one", extraction.BoxlessFingerprintVersion, "b3")
	}

	root := rxRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "docs", "extraction-corpus.md"))
	if err != nil {
		t.Fatalf("read docs/extraction-corpus.md: %v", err)
	}
	for _, locator := range ddBumpPassageLocators {
		para, err := ddParagraphContaining(string(b), locator)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		if !ddHasQualifier(para) {
			t.Errorf("the docs/extraction-corpus.md passage owning %q carries no new-entry/widened-pattern qualifier (one of %v):\n%s", locator, ddQualifierMarkers, para)
		}
	}
}

// ddBumpPassageLocators name the two doc passages that state a lexicon change's fingerprint-bump
// cost. Keyed on a sentence each passage owns, not a line number: the ranges this test used to
// carry rotted the first time a paragraph was inserted above them, and three other passages in the
// same file say "owning phrase", so a drifted range can pass on the wrong text. Neither locator
// contains a qualifier marker, so matching one proves nothing on its own.
var ddBumpPassageLocators = []string{
	"EXTR-22 closed the last reach limit by widening an EXISTING anchor pattern",
	"It is also the expensive lever: WIDENING AN EXISTING",
	// The third passage. It stated the bump rule over the whole lexicon and so contradicted
	// this story's own entry; it was uncaught because only the first two were ever cited.
	"an operator who bumps\nit and expects every stored rule gone is wrong",
}

// ddParagraphContaining returns the blank-line-delimited block holding locator. Exactly one
// block must hold it: zero means the passage was rewritten and this test needs re-pointing, more
// than one means the locator stopped identifying a single passage.
func ddParagraphContaining(doc, locator string) (string, error) {
	var found []string
	for _, para := range strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n\n") {
		if strings.Contains(para, locator) {
			found = append(found, para)
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("docs/extraction-corpus.md holds %d passage(s) containing %q, want exactly 1 -- re-point this test at the passage that replaced it", len(found), locator)
	}
	return found[0], nil
}
