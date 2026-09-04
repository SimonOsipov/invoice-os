package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRuleKeyHasExactlyOneConstructionSite (task-404 AC-1) is a regression
// guard, not a RED spec: RuleKey: has exactly one non-test construction site
// today, inside storeDuplicateRowError. The browser classifies an errors[]
// entry as already-imported purely by RuleKey's presence -- a second
// construction site would silently mislabel some other row as
// already-imported. Scan technique copied from filename_removed_test.go.
func TestRuleKeyHasExactlyOneConstructionSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/importer: %v", err)
	}

	const needle = "RuleKey:"
	var sites []string
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, needle) {
				sites = append(sites, fmt.Sprintf("%s:%d", name, i+1))
			}
		}
	}

	if scanned == 0 {
		t.Fatal("scanned 0 non-test .go files in internal/importer -- the walk is broken, so the count below passed vacuously")
	}
	if len(sites) != 1 {
		t.Errorf("found %d RuleKey: construction site(s) %v, want exactly 1 -- the browser classifies an errors[] entry as already-imported purely by RuleKey's presence, so a second site would silently mislabel a row", len(sites), sites)
	}
}

// --- EXTR-15-05 PS-8 (AC-7) ---------------------------------------------------------------

// oldMapperMessage is the machine string EXTR-15-05 retires. It must survive nowhere in the
// package -- not in document.go, and not in a test still asserting it.
const oldMapperMessage = "invoice_number is missing or blank"

// TestOldMapperMessageIsGoneAndTheNewOnesAreLiterals is an absence check, so it carries its
// own controls: an absence proved over a walk that read nothing reads clean for the wrong
// reason. The walk control ("func documentCreateInput" must be found) and the two population
// floors run BEFORE the absence, and fail the test hard when they do not hold.
func TestOldMapperMessageIsGoneAndTheNewOnesAreLiterals(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/importer: %v", err)
	}

	var scanned, nonTest int
	var oldSites []string
	var ac2Refs int
	var prod strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		src := string(b)
		if !strings.HasSuffix(name, "_test.go") {
			nonTest++
			prod.WriteString(src)
		}
		for i, line := range strings.Split(src, "\n") {
			// The needle's own declaration is not an assertion of it.
			if strings.Contains(line, oldMapperMessage) && !strings.Contains(line, "oldMapperMessage") {
				oldSites = append(oldSites, fmt.Sprintf("%s:%d", name, i+1))
			}
			ac2Refs += strings.Count(line, "ac2Message(")
		}
	}

	// Controls. Every assertion below is vacuous without these.
	if scanned < 20 || nonTest < 5 {
		t.Fatalf("scanned %d .go file(s), %d of them non-test, in internal/importer -- want at least 20 and 5; the walk is broken", scanned, nonTest)
	}
	if !strings.Contains(prod.String(), "func documentCreateInput") {
		t.Fatal("the non-test corpus does not contain \"func documentCreateInput\" -- the walk read no production source, so the absence check below proves nothing")
	}

	if len(oldSites) != 0 {
		t.Errorf("the retired machine string %q still occurs at %v, want nowhere: it names a wire field and reads as an accusation about the tenant's data", oldMapperMessage, oldSites)
	}

	// The three retargeted call sites plus ac2Message's own definition. A retarget deleted
	// rather than moved drops this below 4.
	if ac2Refs < 4 {
		t.Errorf("ac2Message( appears %d time(s), want at least 4 -- its definition plus the three displaced assertions in document_dup_test.go, document_source_rows_test.go and document_service_db_test.go", ac2Refs)
	}

	// Presence needle, the other half of the pair: both sentences the mapper returns today must
	// exist as literals in production. This is what fails when the walk stops reading content,
	// and it holds on the shipped code, so the absence above cannot pass for the wrong reason.
	// goStringJoin folds "a" + "b" back into "ab" -- gofmt splits a long literal across lines.
	joined := goStringJoin.ReplaceAllString(prod.String(), "")
	ac1, ac2 := ac1Message(t), ac2Message(t)
	for _, msg := range []string{ac1, ac2} {
		if msg == "" {
			t.Fatal("the mapper returned an empty message; the search below would match every file")
		}
		if !strings.Contains(joined, msg) {
			t.Errorf("no production file in internal/importer contains the mapper's own message %q -- either the walk read nothing, or the sentence is assembled at runtime rather than written down", msg)
		}
	}
	if ac1 == ac2 {
		t.Errorf("the poor-scan and read-document branches return the same message %q; AC-1 and AC-2 are two different sentences", ac1)
	}
}

// goStringJoin matches the `" + "` a gofmt-wrapped Go string literal leaves between halves.
var goStringJoin = regexp.MustCompile(`"\s*\+\s*"`)
