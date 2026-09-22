// jevseam_test.go: AC-1's two exported-shape clauses. package extraction_test because
// exportedness is only observable from outside the package (an internal test would pass just as
// well for a lowercase doclingPromptText).
package extraction_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// jsPromptIntro is aiTextIntro's own text (internal/extraction/aireading.go), duplicated here
// because an external test cannot reference the unexported constant it mirrors.
const jsPromptIntro = `The document text below was read by a PDF parser. Each line is one visual row on the page. y is the row's vertical position and x each text run's horizontal position, both from 0 to 1 measured from the top-left corner.`

func TestDoclingPromptText_IsReachableFromOutsideThePackage(t *testing.T) {
	pages := []extraction.TokenPage{
		{Number: 1, Tokens: []extraction.Token{
			{Text: "Invoice", Region: extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}},
			{Text: "INV-1", Region: extraction.Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}},
		}},
		{Number: 2, Tokens: []extraction.Token{
			{Text: "Total 100.00", Region: extraction.Region{Page: 2, X0: 0.50, Y0: 0.80, X1: 0.90, Y1: 0.82}},
		}},
	}

	got := extraction.DoclingPromptText(pages)

	want := jsPromptIntro + "\n\n" + strings.Join([]string{
		"--- page 1 ---",
		"y=0.100 | x=0.10 Invoice",
		"y=0.200 | x=0.10 INV-1",
		"--- page 2 ---",
		"y=0.800 | x=0.50 Total 100.00",
	}, "\n")

	if got != want {
		t.Errorf("DoclingPromptText mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// jsScanExtractionSources scans every .go file directly in internal/extraction (not recursive,
// so a sibling worktree or a .ralph/ log cannot be swept in) and reports which ones contain each
// needle. Modelled on internal/importer/filename_removed_test.go, except both needles are
// matched exactly: the unrenamed identifier is lowercase-ai only, and this package already has
// unrelated test names spelling out "AIPromptText" (capital AI) that a case-insensitive scan
// would self-match forever, rename or not.
func jsScanExtractionSources(t *testing.T, banned, control string) (bannedIn, controlIn []string) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/extraction: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(b)
		if strings.Contains(src, banned) {
			bannedIn = append(bannedIn, e.Name())
		}
		if strings.Contains(src, control) {
			controlIn = append(controlIn, e.Name())
		}
	}
	return bannedIn, controlIn
}

func TestAIPromptText_IsGoneFromThePackage(t *testing.T) {
	// Assembled so this file's own source cannot match the banned scan below.
	banned := "ai" + "PromptText"
	const control = "DoclingPromptText"

	bannedIn, controlIn := jsScanExtractionSources(t, banned, control)

	if len(controlIn) == 0 {
		t.Fatalf("the scan found %q in no file in internal/extraction -- the walk is broken, so the banned check below would pass vacuously", control)
	}
	if len(bannedIn) != 0 {
		t.Errorf("%q is still present in %v -- the rename to DoclingPromptText must remove every occurrence", banned, bannedIn)
	}
}
