// doc_test.go: docs/extraction-corpus.md's two end-to-end sections read as an oracle rather
// than as prose beside the number. This file holds the parsers and the phrase specs; the
// row-by-row half needs a live walk and lives in doc_db_test.go. No database.
package endtoend

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	eeDocFile = "../../../docs/extraction-corpus.md"

	eeDocAccSection  = "## End-to-end field accuracy"
	eeDocLineSection = "## Line-item outcome"
)

// The three row shapes. A layout name carries a dot, which [a-z_]+ cannot match, so the field
// scan and the layout scan are disjoint by construction; the layout scan is end-anchored, so a
// four-column line row cannot satisfy it either.
var (
	eeDocLayoutRowRE = regexp.MustCompile("(?m)^\\| `([a-z0-9_]+\\.pdf)` \\| ([0-9]+) \\| ([0-9]+) \\|\\s*$")
	eeDocFieldRowRE  = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| ([0-9]+) \\| ([0-9]+) \\|\\s*$")
	eeDocLineRowRE   = regexp.MustCompile("(?m)^\\| `([a-z0-9_]+\\.pdf)` \\| ([0-9]+) \\| ([0-9]+) \\| ([0-9]+) \\|\\s*$")
)

// eeDocSection is heading's body, cut at the next heading of the same level. Fatal on a missing,
// duplicated or empty section: a duplicated heading would silently slice only the first.
func eeDocSection(t *testing.T, doc, heading string) string {
	t.Helper()

	if n := strings.Count(doc, "\n"+heading); n != 1 {
		t.Fatalf("%s carries %d %q heading(s), want exactly 1; every scan below would read the wrong body", eeDocFile, n, heading)
	}
	body := doc[strings.Index(doc, heading)+len(heading):]
	if j := strings.Index(body, "\n## "); j >= 0 {
		body = body[:j]
	}
	if strings.TrimSpace(body) == "" {
		t.Fatalf("%s's %q section is empty", eeDocFile, heading)
	}
	return body
}

// eeDocAssertRowCounts is the disjointness control: the accuracy section's two tables must be
// read by two different scans, and neither may read a four-column line row.
func eeDocAssertRowCounts(t *testing.T, section string) {
	t.Helper()

	if n := len(eeDocLayoutRowRE.FindAllStringSubmatch(section, -1)); n != eeLayoutCount {
		t.Fatalf("the per-layout scan reads %d row(s) in %q, want %d; this scan is measuring the wrong table", n, eeDocAccSection, eeLayoutCount)
	}
	if n := len(eeDocFieldRowRE.FindAllStringSubmatch(section, -1)); n != len(writtenFields) {
		t.Fatalf("the per-field scan reads %d row(s) in %q, want %d", n, eeDocAccSection, len(writtenFields))
	}
	if n := len(eeDocLineRowRE.FindAllStringSubmatch(section, -1)); n != 0 {
		t.Fatalf("the four-column line scan reads %d row(s) in %q, want 0; the accuracy and line tables are not disjoint", n, eeDocAccSection)
	}
}

// --- the specs --------------------------------------------------------------

// AC-4. The headline, the rate and the procedure for moving them, rendered off the constants at
// test time so the doc cannot record a number the code does not hold.
func TestCorpusDoc_RecordsTheEndToEndProcedure(t *testing.T) {
	section := eeDocSection(t, wildReadFile(t, eeDocFile), eeDocAccSection)
	eeDocAssertRowCounts(t, section)

	for _, m := range eeDocFieldRowRE.FindAllStringSubmatch(section, -1) {
		if strings.Contains(m[1], ".pdf") {
			t.Errorf("the per-field scan matched the layout row %q; the two tables are not disjoint", m[1])
		}
	}
	for _, m := range eeDocLayoutRowRE.FindAllStringSubmatch(section, -1) {
		if slices.Contains(writtenFields, m[1]) {
			t.Errorf("the per-layout scan matched the field row %q; the two tables are not disjoint", m[1])
		}
	}

	if headline := fmt.Sprintf("%d of %d", eeCorpusHits, eeCorpusCells); !strings.Contains(section, headline) {
		t.Errorf("%s's %q section does not carry the headline %q", eeDocFile, eeDocAccSection, headline)
	}
	// Presence alone lets the right headline sit beside a wrong one. eeCorpusCells is this
	// section's only denominator, so every numerator over it must be the pinned hit count.
	overCells := regexp.MustCompile(fmt.Sprintf(`([0-9]+) of %d\b`, eeCorpusCells))
	found := overCells.FindAllStringSubmatch(section, -1)
	if len(found) == 0 {
		t.Errorf("the headline scan reads no %q phrase in %q; it would report clean on any number", fmt.Sprintf("N of %d", eeCorpusCells), eeDocAccSection)
	}
	for _, m := range found {
		if m[1] != strconv.Itoa(eeCorpusHits) {
			t.Errorf("%s's %q section publishes %q; eeCorpusHits is %d", eeDocFile, eeDocAccSection, m[0], eeCorpusHits)
		}
	}
	if rate := strconv.FormatFloat(eeCorpusFloor, 'f', 4, 64); !strings.Contains(section, rate) {
		t.Errorf("%s's %q section does not carry the rate %s", eeDocFile, eeDocAccSection, rate)
	}

	// Without these the tables are a snapshot, and nothing tells the next author that lowering
	// the constant is the one move a ratchet forbids.
	for _, phrase := range []string{"eeCorpusHits", "eeRealMisses", "TestRLS_EndToEnd", "only go up", "no honest oracle"} {
		if !strings.Contains(section, phrase) {
			t.Errorf("%s's %q section never mentions %q; the procedure for moving the figure is incomplete", eeDocFile, eeDocAccSection, phrase)
		}
	}
}

// eeDocLearnedSection is the section that describes the two tiers to a reader. It said "Tier-1
// stays generic" until EXTR-25-03 shipped a shape-only fallback rule and falsified it.
const eeDocLearnedSection = "## Learned rules"

// AC-3.12. The doc names the shipped fallback rule rather than calling Tier-1 wholly generic.
// A phrase assertion is only worth its green if the scan can miss, so the control needle below
// is a phrase the section certainly carries: without it, a renamed heading or a reworked section
// would report clean on any wording at all.
func TestCorpusDoc_TheLearnedRulesSectionNamesTheShippedFallback(t *testing.T) {
	section := eeDocSection(t, wildReadFile(t, eeDocFile), eeDocLearnedSection)

	const control = "tenant-specific tier"
	if !strings.Contains(section, control) {
		t.Fatalf("%s's %q section does not carry the control phrase %q; the assertions below would report clean on a section that no longer says anything", eeDocFile, eeDocLearnedSection, control)
	}

	const want = "t1.currency.sweep"
	if !strings.Contains(section, want) {
		t.Errorf("%s's %q section never names %q; Tier-1 ships one shape-only fallback and a reader is told otherwise", eeDocFile, eeDocLearnedSection, want)
	}
	const falsified = "Tier-1 stays generic"
	if strings.Contains(section, falsified) {
		t.Errorf("%s's %q section still says %q, which t1.currency.sweep falsified", eeDocFile, eeDocLearnedSection, falsified)
	}
}

// AC-5. The limit the figure cannot see, stated in the doc: it is monotone in both distance
// dials, so its non-vacuity rests on the mutilation controls and not on the dial window.
func TestCorpusDoc_RecordsTheDialBlindness(t *testing.T) {
	section := strings.ToLower(eeDocSection(t, wildReadFile(t, eeDocFile), eeDocAccSection))

	for _, needle := range []string{"monotone", "distance dial", "eeCutScore", "eeDecoyBaseHits"} {
		if !strings.Contains(section, strings.ToLower(needle)) {
			t.Errorf("%s's %q section never says %q; a widened dial can only raise this figure, and what bounds it is the mutilation control", eeDocFile, eeDocAccSection, needle)
		}
	}
}
