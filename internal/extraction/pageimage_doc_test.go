// pageimage_doc_test.go: AC 3. docs/page-image-storage.md's "What this does not cover" section
// is a deliverable of THIS subtask, because the pixels stop being unreadable in it. Shares
// pagestore_doc_test.go's pd* section reader, which fatals on a renamed, duplicated or emptied
// heading -- that fatal is this oracle's planted needle, so a deleted section fails rather than
// passing.
//
// Helpers use a pgd* prefix.
package extraction_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pgdSection = "What this does not cover"

// pgdFalsified are the clauses the section carries TODAY (at 201d7169) and that this subtask
// makes false. Each is matched case-insensitively against the whitespace-collapsed section.
var pgdFalsified = []struct{ clause, why string }{
	{
		clause: "the inventory is read; the pixels are not",
		why:    "the pixels become readable in this subtask -- GET /v1/extractions/{id}/pages/{n} streams them",
	},
	{
		clause: "nothing serves a page image",
		why:    "this subtask's route serves one; only the screen half stays owed, and it is EXTR-11-05's",
	},
	{
		clause: "the bytes route is extr-11-03's",
		why:    "the bytes route is shipped here, so a forward reference to it is a claim about the past",
	},
	{
		clause: "leaves this package only through",
		why:    "storage_key now also leaves the package through (*Reader).PageImageKey, so PageStore is no longer the only exit",
	},
}

// pgdHistoric are EXTR-01's original clauses. They were already rewritten by EXTR-11-01, so
// these assertions are vacuous today and are kept only so the old sentence cannot come back.
// They carry none of this test's red.
var pgdHistoric = []string{
	"no read path exists yet",
	"written and never read",
	"no store method selects one",
}

// pgdNames are what the rewritten section must say instead: the three seams that now read these
// rows. "(*Reader).Detail" is present today and is therefore the section-parse control -- if it
// is missing, the reader below is looking at the wrong prose.
var pgdNames = []string{
	"(*Reader).Detail",
	"(*Reader).PageImageKey",
	"GET /v1/extractions/{id}/pages/{n}",
}

// pgdStillTrue is the half of the old sentence that survives: no screen displays a page image
// until EXTR-11-05 draws the canvas. AC 3 says keep whatever is still true, so a rewrite that
// deletes the section wholesale is not the deliverable either.
var pgdStillTrue = []string{"EXTR-11-05"}

func TestPageImageStorageDoc_NoLongerClaimsThePixelsAreUnread(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pdRepoRoot(t), filepath.FromSlash(pdDocPath)))
	if err != nil {
		t.Fatalf("read %s: %v -- the doc is this subtask's deliverable, so an absent file is the failure and not a skip", pdDocPath, err)
	}

	body := pdSection(t, string(raw), pgdSection)
	lower := strings.ToLower(body)

	if len(pgdFalsified) == 0 || len(pgdNames) == 0 || len(pgdStillTrue) == 0 {
		t.Fatal("a clause table is empty, so this test examined nothing")
	}

	for _, c := range pgdFalsified {
		if strings.Contains(lower, c.clause) {
			t.Errorf("%s section %q still says %q -- %s", pdDocPath, pgdSection, c.clause, c.why)
		}
	}
	for _, clause := range pgdHistoric {
		if strings.Contains(lower, clause) {
			t.Errorf("%s section %q says %q again; EXTR-11-01 already retired that sentence", pdDocPath, pgdSection, clause)
		}
	}
	for _, name := range pgdNames {
		if !strings.Contains(lower, strings.ToLower(name)) {
			t.Errorf("%s section %q never names %q -- AC 3 wants the replacement to state what now reads these rows, not merely to delete what is no longer true", pdDocPath, pgdSection, name)
		}
	}
	for _, name := range pgdStillTrue {
		if !strings.Contains(body, name) {
			t.Errorf("%s section %q no longer names %q -- no screen displays a page image until that subtask, and AC 3 keeps whatever is still true", pdDocPath, pgdSection, name)
		}
	}
}
