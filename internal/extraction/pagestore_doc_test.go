// pagestore_doc_test.go: docs/page-image-storage.md is a deliverable, so it has an oracle.
// Modelled on internal/audit/doc_contract_test.go: read the doc, locate each section by its
// own heading, and fatal on a section too short to carry the claim it is checked for — an
// absence check over an emptied section always passes.
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
	pdDocPath         = "docs/page-image-storage.md"
	pdMinSectionRunes = 200
	pdMinSections     = 4
)

var pdWhitespaceRun = regexp.MustCompile(`\s+`)

// pdSections are the four claims the doc owes, each with the needles that make it a record
// rather than a gesture.
var pdSections = []struct {
	heading string
	needles []string
	why     string
}{
	{
		heading: "Render profile",
		needles: []string{"150", "grayscale", "PNG"},
		why:     "a canvas that cannot tell what resolution or colour model it is scaling has to guess",
	},
	{
		heading: "The key scheme",
		needles: []string{"tenants/", "/pages/", "/v1/", ".png", "content hash", "server-derived"},
		why:     "the key is the object-storage access-control boundary, so its template and where each segment comes from are the record",
	},
	{
		heading: "The page cap",
		needles: []string{"800", "600", "480", "300", "1,600", "safety factor"},
		why:     "a cap with no derivation is a number someone will raise without redoing the arithmetic",
	},
	{
		heading: "Retention",
		needles: []string{"never deleted by the app", "demo tenant", "docs/demo-reset.md"},
		why:     "the purge deletes the rows that made the objects findable and leaves the objects; an unenumerated retention claim is an unowned one",
	},
}

func pdRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root; the read below would resolve to nothing")
	}
	return root
}

// pdSection returns one "## " section's own text, heading included, whitespace collapsed. Only
// that section is read: the doc names 150 and 800 in more than one place, and a whole-file
// check would report every section clean off another section's prose.
func pdSection(t *testing.T, doc, heading string) string {
	t.Helper()

	lines := strings.Split(doc, "\n")
	var starts []int
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") && strings.TrimSpace(strings.TrimPrefix(line, "## ")) == heading {
			starts = append(starts, i)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("%s holds %d section(s) headed %q, want exactly 1 -- a renamed, deleted or duplicated heading leaves this oracle reading the wrong prose, or none", pdDocPath, len(starts), heading)
	}

	end := len(lines)
	for i := starts[0] + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	body := strings.TrimSpace(pdWhitespaceRun.ReplaceAllString(strings.Join(lines[starts[0]:end], " "), " "))
	if n := len([]rune(body)); n < pdMinSectionRunes {
		t.Fatalf("%s section %q carries %d rune(s), want at least %d -- an absence check over an emptied section always passes", pdDocPath, heading, n, pdMinSectionRunes)
	}
	return body
}

func TestPageStorageDocRecordsTheProfileAndTheCap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pdRepoRoot(t), filepath.FromSlash(pdDocPath)))
	if err != nil {
		t.Fatalf("read %s: %v -- the doc is this subtask's deliverable, so an absent file is the failure and not a skip", pdDocPath, err)
	}
	doc := string(raw)

	if n := strings.Count(doc, "\n## "); n < pdMinSections {
		t.Fatalf("%s carries %d level-two heading(s), want at least %d", pdDocPath, n, pdMinSections)
	}

	for _, s := range pdSections {
		body := pdSection(t, doc, s.heading)
		lower := strings.ToLower(body)
		for _, needle := range s.needles {
			if !strings.Contains(lower, strings.ToLower(needle)) {
				t.Errorf("%s section %q never says %q -- %s", pdDocPath, s.heading, needle, s.why)
			}
		}
	}

	// The cap is a derivation, not an assertion: the arithmetic has to be legible enough that
	// raising it means redoing it. maxPages and the 300 ms per-page budget are pinned in
	// internal/extraction/pdfium.go, which this section must agree with.
	capSection := pdSection(t, doc, "The page cap")
	if !strings.Contains(capSection, "maxPages") {
		t.Errorf("%s section %q does not name maxPages -- the constant the number lives in, and the only route from the prose to the code", pdDocPath, "The page cap")
	}
	if !strings.Contains(strings.ToLower(capSection), "never measured") {
		t.Errorf("%s section %q does not record that the per-page PUT cost was never measured -- that unmeasured term is the entire reason for the safety factor, and a derivation that hides it reads as arithmetic over known quantities", pdDocPath, "The page cap")
	}
}
