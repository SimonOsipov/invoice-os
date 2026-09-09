// duedate_scope_test.go: EXTR-25-04 AC-4.8 and AC-4.11 -- due_date must reach no non-test
// source outside anchor.go, and the doc it falsifies must gain the qualifier that says so.
package extraction_test

import (
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
// the new entry a "rule-less owning-phrase entry", which is the distinguishing concept absent
// from both passages today.
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
// existing pattern (bump) from a new rule-less owning-phrase entry (no bump). Floor of 2
// matched passages out of 2 cited, so a rewritten doc cannot pass on zero.
func TestAnchorLexicon_TheBumpRuleDistinguishesANewEntryFromAWidenedPattern(t *testing.T) {
	if extraction.FingerprintVersion != "v2" {
		t.Errorf("FingerprintVersion = %q, want %q -- a rule-less lexicon entry must not bump it", extraction.FingerprintVersion, "v2")
	}
	if extraction.BoxlessFingerprintVersion != "b2" {
		t.Errorf("BoxlessFingerprintVersion = %q, want %q -- a rule-less lexicon entry must not bump it", extraction.BoxlessFingerprintVersion, "b2")
	}

	root := rxRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "docs", "extraction-corpus.md"))
	if err != nil {
		t.Fatalf("read docs/extraction-corpus.md: %v", err)
	}
	lines := strings.Split(string(b), "\n")

	passages := []struct{ from, to int }{
		{489, 492},
		{1006, 1013},
	}
	matched := 0
	for _, p := range passages {
		if p.to > len(lines) {
			t.Fatalf("docs/extraction-corpus.md has %d line(s), want at least %d for the %d-%d passage", len(lines), p.to, p.from, p.to)
		}
		text := strings.Join(lines[p.from-1:p.to], "\n")
		if ddHasQualifier(text) {
			matched++
		} else {
			t.Logf("docs/extraction-corpus.md:%d-%d carries no new-entry/widened-pattern qualifier yet:\n%s", p.from, p.to, text)
		}
	}
	if matched < 2 {
		t.Errorf("%d of 2 cited passages carry the new-entry/widened-pattern qualifier, want both -- a rewritten doc must not pass on zero", matched)
	}
}
