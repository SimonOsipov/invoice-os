// corpus_test.go: the golden corpus and what Tier-1 must produce from it. The expectations are
// a Go table rather than a JSON file so they review in the diff and register no second flag --
// fixtures_test.go:23 owns -update.
//
// The specs below guard the corpus itself: that every expectation names a committed file, that
// every committed layout is expected, and that the TINs stay in the free part of the reserved
// block. C-09 is the only one that fails on a partial corpus; the rest quantify over fxCorpus
// or corpusExpect and would pass over a subset.
package extraction_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// corpusPrefix marks a corpus layout inside the flat testdata/ directory. A subdirectory would
// sit outside TestFixtures_MatchTheirGenerator's non-directory .pdf count.
const corpusPrefix = "corpus_"

// corpusLayouts is the set C-09 pins, hard-coded rather than derived: derived from fxCorpus it
// could not see a missing layout.
var corpusLayouts = []string{
	"corpus_inline_labels.pdf",
	"corpus_split_labels.pdf",
	"corpus_stacked_labels.pdf",
	"corpus_two_column.pdf",
	"corpus_ambiguous_date.pdf",
	"corpus_totals_block.pdf",
}

// corpusTIN is any TIN-shaped run. Deliberately wider than the reserved block so C-07 can find
// a fixture that left it.
var corpusTIN = regexp.MustCompile(`[0-9]{8}-[0-9]{4}`)

// corpusFreeTINPrefix is the reserved block; corpusScriptedTINs are the mock's scripted
// outcomes (-0001..-0007) and its never-allocate pair (-0008, -0009), which no fixture may use
// -- internal/submission/mock_script.go:76-93.
const corpusFreeTINPrefix = "99999999-"

var corpusScriptedTINs = []string{
	"0001", "0002", "0003", "0004", "0005", "0006", "0007", "0008", "0009",
}

// corpusExpect is what Tier-1 must produce for each committed layout: a HeaderFields key, and
// every value that is a correct answer. Two values is D-10's ambiguous-date fork, not a choice.
// A field absent from fields is not asserted -- the corpus measures Tier-1's reach, and
// pretending an unreached field is a pass would inflate it. Values are the NORMALISED forms
// section 4 emits: amounts -?\d+(\.\d{1,2})?, dates 2006-01-02, currency upper-cased. Rank is
// not asserted here; EXTR-04-09 owns the match semantics and the floor.
var corpusExpect = []struct {
	file   string
	fields map[string][]string
}{
	{
		file: "corpus_inline_labels.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1001"},
			"issue_date":     {"2026-03-04"},
			"supplier_tin":   {"99999999-0101"},
			"supplier_name":  {"Adeyemi Trading Limited"},
			"buyer_tin":      {"99999999-0102"},
			"buyer_name":     {"Honeywell Group"},
			"currency":       {"NGN"},
			"subtotal":       {"1000.00"},
			"vat":            {"75.00"},
			"total":          {"1075.00"},
		},
	},
	{
		file: "corpus_split_labels.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1002"},
			"issue_date":     {"2026-04-15"},
			"supplier_tin":   {"99999999-0201"},
			"supplier_name":  {"Adeyemi Trading Limited"},
			"buyer_tin":      {"99999999-0202"},
			"buyer_name":     {"Honeywell Group"},
			"currency":       {"NGN"},
			"subtotal":       {"2000.00"},
			"vat":            {"150.00"},
			"total":          {"2150.00"},
		},
	},
	{
		file: "corpus_stacked_labels.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1003"},
			"issue_date":     {"2026-04-22"},
			"supplier_tin":   {"99999999-0301"},
			"supplier_name":  {"Adeyemi Trading Limited"},
			"buyer_tin":      {"99999999-0302"},
			"buyer_name":     {"Honeywell Group"},
			"total":          {"3225.00"},
		},
	},
	{
		file: "corpus_two_column.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1004"},
			"issue_date":     {"2026-05-06"},
			"supplier_tin":   {"99999999-0401"},
			"supplier_name":  {"Adeyemi Trading Limited"},
			"buyer_tin":      {"99999999-0402"},
			"buyer_name":     {"Honeywell Group"},
			"total":          {"6450.00"},
		},
	},
	{
		file: "corpus_ambiguous_date.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1005"},
			// 12/03/2026: both components <= 12 and no month name, so ShapeDate returns BOTH
			// normalisations and issue_date carries two candidates. This row is the fixture's
			// whole reason for existing, and why fields is [] and not a single string.
			"issue_date":    {"2026-03-12", "2026-12-03"},
			"supplier_tin":  {"99999999-0501"},
			"supplier_name": {"Adeyemi Trading Limited"},
			"total":         {"4300.00"},
		},
	},
	{
		file: "corpus_totals_block.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1006"},
			"supplier_tin":   {"99999999-0601"},
			"subtotal":       {"5000.00"},
			"vat":            {"375.00"},
			"total":          {"5375.00"},
		},
	},
}

// --- harness ----------------------------------------------------------------

// corpusFxNames is every name fxCorpus carries.
func corpusFxNames() map[string]bool {
	names := make(map[string]bool, len(fxCorpus))
	for _, f := range fxCorpus {
		names[f.name] = true
	}
	return names
}

// corpusRequireCommitted fails naming EVERY layout that is not on disk. Without it the first
// missing file fatals the read and hides the other five.
func corpusRequireCommitted(t *testing.T) {
	t.Helper()

	var missing []string
	for _, name := range corpusLayouts {
		if _, err := os.Stat(filepath.Join(fxDir, name)); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d corpus layout(s) not committed under %s/: %s -- append the fxCorpus entries and regenerate with `go test ./internal/extraction/ -run TestFixtures_MatchTheirGenerator -update`",
			len(missing), fxDir, strings.Join(missing, ", "))
	}
}

// --- the tests --------------------------------------------------------------

// C-03. An expectation over a file that is not there measures nothing.
func TestCorpus_EveryExpectationNamesACommittedFile(t *testing.T) {
	if len(corpusExpect) == 0 {
		t.Fatal("corpusExpect is empty; every assertion below would pass over nothing")
	}

	fx := corpusFxNames()
	for _, want := range corpusExpect {
		t.Run(want.file, func(t *testing.T) {
			if len(want.fields) == 0 {
				t.Errorf("%s expects no field at all; a row with an empty fields map asserts nothing", want.file)
			}
			if !strings.HasPrefix(want.file, corpusPrefix) {
				t.Errorf("%s is not %s-prefixed; the corpus layouts are named apart from the reader fixtures", want.file, corpusPrefix)
			}
			if _, err := os.Stat(filepath.Join(fxDir, want.file)); err != nil {
				t.Errorf("%s is expected but not committed under %s/: %v", want.file, fxDir, err)
			}
			if !fx[want.file] {
				t.Errorf("%s is expected but has no fxCorpus entry; nothing regenerates or byte-compares it", want.file)
			}
		})
	}
}

// C-04. A fixture with no expectation measures nothing either.
func TestCorpus_EveryLayoutIsExpected(t *testing.T) {
	rows := make(map[string]int, len(corpusExpect))
	for _, want := range corpusExpect {
		rows[want.file]++
	}

	var layouts []string
	for _, f := range fxCorpus {
		if strings.HasPrefix(f.name, corpusPrefix) {
			layouts = append(layouts, f.name)
		}
	}
	if len(layouts) == 0 {
		t.Fatalf("fxCorpus carries no %s-prefixed entry; the per-layout assertions below would pass over nothing", corpusPrefix)
	}

	for _, name := range layouts {
		if n := rows[name]; n != 1 {
			t.Errorf("%s has %d corpusExpect row(s), want exactly 1", name, n)
		}
	}
}

// C-05. A key outside HeaderFields is a field nothing downstream can write.
func TestCorpus_EveryExpectedFieldIsInTheVocabulary(t *testing.T) {
	vocab := make(map[string]bool, len(extraction.HeaderFields))
	for _, f := range extraction.HeaderFields {
		vocab[f] = true
	}
	if len(vocab) == 0 {
		t.Fatal("extraction.HeaderFields is empty; membership below would reject everything or assert nothing")
	}

	keys := 0
	for _, want := range corpusExpect {
		for field, values := range want.fields {
			keys++
			if !vocab[field] {
				t.Errorf("%s expects %q, which is not in extraction.HeaderFields %v", want.file, field, extraction.HeaderFields)
			}
			if len(values) == 0 {
				t.Errorf("%s expects %q with no value; an empty value list asserts nothing", want.file, field)
			}
		}
	}
	if keys == 0 {
		t.Fatal("corpusExpect names no field at all; the membership assertion ran over nothing")
	}
}

// C-06. A corpus pdfium cannot read measures nothing.
func TestCorpus_ReadsWithTheRealReader(t *testing.T) {
	corpusRequireCommitted(t)

	for _, name := range corpusLayouts {
		t.Run(name, func(t *testing.T) {
			pages, res := ptRead(t, name)
			if len(pages) < 1 {
				t.Fatalf("%s read as %d page(s), want at least 1", name, len(pages))
			}
			if res.PagesWithText < 1 {
				t.Errorf("%s reports %d page(s) with text; every corpus layout is native text", name, res.PagesWithText)
			}

			nonEmpty := 0
			for _, tok := range ptTokens(pages) {
				if strings.TrimSpace(tok.Text) != "" {
					nonEmpty++
				}
			}
			if nonEmpty < 1 {
				t.Errorf("%s yielded %d non-empty token(s), want at least 1", name, nonEmpty)
			}
		})
	}
}

// C-07. An absence scan that finds nothing is a regex or fixture regression, not a pass:
// corpus_two_column.pdf alone carries two TINs, so the floor is 2.
func TestCorpus_UsesOnlyFreeReservedTINs(t *testing.T) {
	corpusRequireCommitted(t)

	var hits []string
	for _, name := range corpusLayouts {
		pages, _ := ptRead(t, name)
		for _, tok := range ptTokens(pages) {
			for _, hit := range corpusTIN.FindAllString(tok.Text, -1) {
				hits = append(hits, hit)
				if !strings.HasPrefix(hit, corpusFreeTINPrefix) {
					t.Errorf("%s carries TIN %q, outside the reserved %s block -- it could collide with a real taxpayer", name, hit, corpusFreeTINPrefix)
					continue
				}
				for _, scripted := range corpusScriptedTINs {
					if strings.TrimPrefix(hit, corpusFreeTINPrefix) == scripted {
						t.Errorf("%s carries TIN %q, one of the mock adapter's scripted or never-allocate values (internal/submission/mock_script.go:76-93)", name, hit)
					}
				}
			}
		}
	}

	if len(hits) < 2 {
		t.Fatalf("found %d TIN-shaped token(s) across the corpus, want at least 2 (%v) -- a zero-hit scan asserts nothing about the block",
			len(hits), hits)
	}
}

// C-08. D-1's producer split: the same field arrives as one token from one producer and two
// from another, and the corpus must carry both. pdfium appends a trailing space to a rect that
// is followed by another on the same line, so the split label reads "Invoice No " -- compare
// trimmed or this fails on a correct fixture.
func TestCorpus_ExercisesBothTokenGranularities(t *testing.T) {
	corpusRequireCommitted(t)

	const (
		label       = "Invoice No"
		inlineFile  = "corpus_inline_labels.pdf"
		inlineValue = "INV-1001"
		splitFile   = "corpus_split_labels.pdf"
		splitValue  = "INV-1002"
	)

	read := func(name string) []string {
		pages, _ := ptRead(t, name)
		var texts []string
		for _, tok := range ptTokens(pages) {
			texts = append(texts, tok.Text)
		}
		if len(texts) == 0 {
			t.Fatalf("%s yielded no token; the granularity assertions over it would report nothing", name)
		}
		return texts
	}

	inline := read(inlineFile)
	joined, bare := false, false
	for _, text := range inline {
		if strings.Contains(text, label) && strings.Contains(text, inlineValue) {
			joined = true
		}
		if strings.TrimSpace(text) == label {
			bare = true
		}
	}
	if !joined {
		t.Errorf("%s has no token carrying both %q and %q; same_token has no target", inlineFile, label, inlineValue)
	}
	if bare {
		t.Errorf("%s carries %q as a token of its own; it is the INLINE layout and must not also be split", inlineFile, label)
	}

	split := read(splitFile)
	joined, bare = false, false
	for _, text := range split {
		if strings.Contains(text, label) && strings.Contains(text, splitValue) {
			joined = true
		}
		if strings.TrimSpace(text) == label {
			bare = true
		}
	}
	if !bare {
		t.Errorf("%s has no token equal to %q on its own; the right relation has no target", splitFile, label)
	}
	if joined {
		t.Errorf("%s carries %q and %q in one token; it is the SPLIT layout, so the two granularities are the same file twice", splitFile, label, splitValue)
	}
}

// C-09. The one spec a partial corpus fails: C-01..C-05 all quantify over fxCorpus or
// corpusExpect and pass over a subset.
func TestCorpus_HasAllSixNamedLayouts(t *testing.T) {
	const want = 6

	if len(corpusLayouts) != want {
		t.Fatalf("corpusLayouts names %d layout(s), want %d -- the hard-coded set is the floor and cannot be trimmed to fit fxCorpus", len(corpusLayouts), want)
	}
	named := make(map[string]bool, want)
	for _, name := range corpusLayouts {
		named[name] = true
	}

	got := make(map[string]bool)
	for _, f := range fxCorpus {
		if strings.HasPrefix(f.name, corpusPrefix) {
			got[f.name] = true
		}
	}
	if len(got) != want {
		t.Errorf("fxCorpus carries %d %s-prefixed entr(y/ies), want exactly %d", len(got), corpusPrefix, want)
	}

	for _, name := range corpusLayouts {
		if !got[name] {
			t.Errorf("fxCorpus has no entry for %s", name)
		}
	}
	for name := range got {
		if !named[name] {
			t.Errorf("fxCorpus carries %s, which is not one of the six named layouts", name)
		}
	}
}

// --- EXTR-14-09: the learned-rule fixture and the doc that owns it -------------------------

// E-11. C-07 quantifies over corpusLayouts, so learned_two_party.pdf sits outside its reserved-
// TIN scan. This closes that gap for the one fixture that is deliberately not a corpus layout.
// The >=2 floor is what stops a regex or fixture regression from reading as a clean pass.
func TestCorpus_TheLearnedRuleFixtureUsesOnlyFreeReservedTINs(t *testing.T) {
	pages, _ := ptRead(t, fxLearnedTwoParty)

	var hits []string
	for _, tok := range ptTokens(pages) {
		for _, hit := range corpusTIN.FindAllString(tok.Text, -1) {
			hits = append(hits, hit)
			if !strings.HasPrefix(hit, corpusFreeTINPrefix) {
				t.Errorf("%s carries TIN %q, outside the reserved %s block -- it could collide with a real taxpayer", fxLearnedTwoParty, hit, corpusFreeTINPrefix)
				continue
			}
			for _, scripted := range corpusScriptedTINs {
				if strings.TrimPrefix(hit, corpusFreeTINPrefix) == scripted {
					t.Errorf("%s carries TIN %q, one of the mock adapter's scripted or never-allocate values (internal/submission/mock_script.go:76-93)", fxLearnedTwoParty, hit)
				}
			}
		}
	}
	if len(hits) < 2 {
		t.Fatalf("found %d TIN-shaped token(s) in %s, want at least 2 (%v) -- a zero-hit scan asserts nothing about the block",
			len(hits), fxLearnedTwoParty, hits)
	}
}

// --- E-10: the learned-rules documentation ------------------------------------------------

const (
	cldSection         = "## Learned rules"
	cldAdding          = "## Adding a layout"
	cldMinSectionRunes = 400
	cldMinSubRunes     = 180
)

var cldWhitespaceRun = regexp.MustCompile(`\s+`)

// cldSubsections are the claims the "## Learned rules" section owes. Each needle is a phrase,
// never a bare number: "0.06" is a substring of "0.065", and a bare-number list reports a
// derivation present when only a summary line survived.
var cldSubsections = []struct {
	heading string
	needles []string
	why     string
}{
	{
		// D-2's cost, and the vocabulary every subsection below borrows. Nothing else in the
		// doc says what a b1: key is composed of.
		heading: cldIdentitiesHeading,
		needles: []string{
			// "fingerprintversion" is a substring of "boxlessfingerprintversion" and so can
			// never red on its own; arm 3 of TestCorpusDoc_NamesOneInvalidationRuleNotTwo
			// masks the boxless spelling and is the guard that actually pins it.
			"v1:", "b1:", "boxlessfingerprintversion", "fingerprintversion",
			"<label>:<placement>", "band", "only its own",
		},
		why: "an operator who bumps FingerprintVersion and expects every stored rule gone is wrong, and a reader who cannot compose a b1: key cannot predict which documents share one",
	},
	{
		heading: "How a rule is derived",
		needles: []string{
			"learnrule", "betteranchor", "same_token", "below", "rounded up",
			"0.35", "0.06", "regexp.quotemeta", "learned_two_party.pdf", "99999999-0702",
		},
		why: "a derivation nobody can reproduce is a rule nobody can predict",
	},
	{
		// The boxless needles are the ones that go false if that arm is ever dropped; the
		// five shipped ones all survive a text saying only pointed teaches.
		heading: "Which correction produces a rule",
		needles: []string{
			"typed", "undone", "zero rules", "anchors to nothing", "honest refusal",
			"boxless", "b1:", "layout_tokens", "learnboxlessrule",
			// What the boxless path derives, and the refusal that is not the no-hit one.
			"same_token", "ambiguous",
		},
		why: "the method and the layout namespace are both inputs; a reader who thinks only a gesture teaches will point at the wrong thing on a DOCX, and a reader who never learns which relation the boxless path derives cannot predict what it will do",
	},
	{
		heading: "Undo does not un-teach",
		needles: []string{
			"stays live", "append-only", "both rows remain", "ordering",
			"TestRLS_AnUndoDoesNotUnteachAndOnAV1LayoutOnlyAPointedCorrectionSupersedes",
		},
		why: "D-17 is the sharp edge of this feature, and a caveat nobody wrote down is a support call",
	},
	{
		heading: "A rule is scoped to one tenant and one layout fingerprint",
		needles: []string{"fingerprint", "never even loaded", "different tenant"},
		why:     "the two boundaries a reader will otherwise assume are advisory",
	},
	{
		heading: "When a learned rule misfires",
		needles: []string{
			"corpus owner", "corpus_two_column.pdf", "99999999-0401", "ambiguous",
			"second pointed correction", "fingerprintversion",
		},
		why: "a response path with no name on it is a response path nobody runs, and the two-column case is the canonical misfire",
	},
	{
		heading: "learned_two_party.pdf is not a corpus layout",
		needles: []string{"corpusexpect", "corpuslayouts", "corpustokenfloor", "extr-04"},
		why:     "the next author will otherwise add a corpusExpect row for it by reflex and drag the ratchet with it",
	},
}

// cldSubsection returns one "### " subsection's own text inside body, whitespace collapsed. A
// rune floor is what stops an absence check from passing over an emptied subsection.
func cldSubsection(t *testing.T, body, heading string) string {
	t.Helper()

	lines := strings.Split(body, "\n")
	var starts []int
	for i, line := range lines {
		if strings.HasPrefix(line, "### ") && strings.TrimSpace(strings.TrimPrefix(line, "### ")) == heading {
			starts = append(starts, i)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("%s holds %d subsection(s) headed %q under %s, want exactly 1 -- a renamed or duplicated heading leaves this oracle reading the wrong prose, or none",
			acDoc, len(starts), heading, cldSection)
	}

	end := len(lines)
	for i := starts[0] + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "### ") || strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	text := strings.TrimSpace(cldWhitespaceRun.ReplaceAllString(strings.Join(lines[starts[0]:end], " "), " "))
	if n := len([]rune(text)); n < cldMinSubRunes {
		t.Fatalf("%s subsection %q carries %d rune(s), want at least %d -- an absence check over an emptied subsection always passes",
			acDoc, heading, n, cldMinSubRunes)
	}
	return text
}

// E-10. docs/extraction-corpus.md is this subtask's deliverable, so it has an oracle. The scan
// is section-scoped: the doc names 0.06 and corpus_two_column.pdf in several places, and a
// whole-file check would report this section clean off another section's prose.
func TestCorpusDoc_RecordsHowALearnedRuleIsDerivedAndWhatUndoDoesNot(t *testing.T) {
	doc := acRepoFile(t, acDoc)

	body := acDocSectionText(t, doc, cldSection)
	if n := len([]rune(body)); n < cldMinSectionRunes {
		t.Fatalf("%s's %q section carries %d rune(s), want at least %d", acDoc, cldSection, n, cldMinSectionRunes)
	}
	if got := strings.Count(doc, "\n"+cldSection); got != 1 {
		t.Fatalf("%s carries %d %q heading(s), want exactly 1", acDoc, got, cldSection)
	}

	for _, s := range cldSubsections {
		text := strings.ToLower(cldSubsection(t, body, s.heading))
		for _, needle := range s.needles {
			if !strings.Contains(text, strings.ToLower(needle)) {
				t.Errorf("%s subsection %q never says %q -- %s", acDoc, s.heading, needle, s.why)
			}
		}
	}

	// The reflex guard: the next author reads "## Adding a layout", not this section.
	adding := strings.ToLower(acDocSectionText(t, doc, cldAdding))
	if !strings.Contains(adding, fxLearnedTwoParty) {
		t.Errorf("%s's %q section never names %s -- the next author adds a corpusExpect row for it by reflex", acDoc, cldAdding, fxLearnedTwoParty)
	}
}

// --- EXTR-19-09 / AC-6: the doc's two self-contradictions, in the scan's blind spot ---------

const (
	// cldBoxlessLever is the string FingerprintVersion is a substring of; it must be masked
	// before any FingerprintVersion scan, or every masked paragraph reads as a hit.
	cldBoxlessLever = "BoxlessFingerprintVersion"
	cldGeoLever     = "FingerprintVersion"
	cldLeverMask    = "\x00BOXLESS_LEVER\x00"
	cldLeverFloor   = 3
	cldPreambleMin  = 200
	// cldStalePreamble is the sentence that went false when EXTR-19-08's handler landed.
	cldStalePreamble = "a single pointed correction"
	// cldControlNeedle proves the scan read docs/extraction-corpus.md and not some other file.
	cldControlNeedle = "## Learned rules"
	// cldIdentitiesHeading is the subsection that owes the geometric lever by name.
	cldIdentitiesHeading = "The two layout identities"
)

// cldParagraphs splits doc on blank lines, then splits a markdown table into its own rows. A
// table carries no blank line, so a whole-block unit lets one compliant row cover every other
// row in the same table -- measured: adding a bare FingerprintVersion sentence to a sibling row
// left this scan green. :35 lives in such a table, so the row is the unit that matters.
func cldParagraphs(doc string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n\n") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(p), "|") {
			for _, row := range strings.Split(p, "\n") {
				if strings.TrimSpace(row) != "" {
					out = append(out, row)
				}
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// cldLearnedRulesPreamble is the text between "## Learned rules" and its first "### ". No
// needle can reach it: cldSubsection only reads under a "### " heading.
func cldLearnedRulesPreamble(t *testing.T, doc string) string {
	t.Helper()

	body := acDocSectionText(t, doc, cldSection)
	if i := strings.Index(body, "\n### "); i >= 0 {
		body = body[:i]
	}
	return strings.TrimSpace(cldWhitespaceRun.ReplaceAllString(body, " "))
}

// EXTR-19-09 / AC-6. Two sentences say a FingerprintVersion bump alone invalidates every
// stored rule, contradicting the section that names both levers; and the "## Learned rules"
// preamble still says a rule comes from a pointed correction, false since EXTR-19-08. Both
// sit outside every "### " subsection, so cldSubsection cannot reach either -- this reads the
// RAW file instead.
func TestCorpusDoc_NamesOneInvalidationRuleNotTwo(t *testing.T) {
	doc := acRepoFile(t, acDoc)

	// The found-needle control: prove the scan is reading the right file before asserting
	// anything about what the file does not say.
	if !strings.Contains(doc, cldControlNeedle) {
		t.Fatalf("%s carries no %q heading, so this scan is reading the wrong file and every absence below is an artefact", acDoc, cldControlNeedle)
	}

	// Arm 1 -- one invalidation rule, not two. Mask the longer lever first: it contains the
	// shorter one as a substring.
	masked := strings.ReplaceAll(doc, cldBoxlessLever, cldLeverMask)
	var named int
	for _, p := range cldParagraphs(masked) {
		if !strings.Contains(p, cldGeoLever) {
			continue
		}
		named++
		if strings.Contains(p, cldLeverMask) {
			continue
		}
		t.Errorf("%s names %s as an invalidation lever without %s:\n\n%s\n\nSince EXTR-19-02 the same anchorLabelMatchers feed both fingerprints, so a lexicon change needs both bumps; a paragraph naming one lever tells the reader a stale half-truth",
			acDoc, cldGeoLever, cldBoxlessLever, strings.TrimSpace(p))
	}
	if named < cldLeverFloor {
		t.Fatalf("%s names %s in %d paragraph(s), want at least %d — a scan finding nothing reports a clean file",
			acDoc, cldGeoLever, named, cldLeverFloor)
	}

	// Arm 2 -- the preamble, which no needle list can reach.
	preamble := cldLearnedRulesPreamble(t, doc)
	if n := len([]rune(preamble)); n < cldPreambleMin {
		t.Fatalf("%s's %q preamble carries %d rune(s), want at least %d — an absence check over an emptied preamble always passes",
			acDoc, cldSection, n, cldPreambleMin)
	}
	lower := strings.ToLower(preamble)
	if strings.Contains(lower, cldStalePreamble) {
		t.Errorf("%s's %q preamble still says %q; since EXTR-19-08 a typed correction on a b1: layout also derives a rule:\n\n%s",
			acDoc, cldSection, cldStalePreamble, preamble)
	}
	// Word-bounded, not Contains: "untyped" carries "typed" as a substring, so a plain
	// Contains reports the method named by prose that denies it.
	for _, method := range []string{"pointed", "typed"} {
		if !regexp.MustCompile(`\b` + method + `\b`).MatchString(lower) {
			t.Errorf("%s's %q preamble never names the %q method — the preamble is the first prose a reader meets and it owes both learning paths:\n\n%s",
				acDoc, cldSection, method, preamble)
		}
	}

	// Arm 3 -- the geometric lever, named in the subsection that owes it. The
	// "fingerprintversion" needle in cldSubsections cannot assert this: it is a substring of
	// "boxlessfingerprintversion", so the sibling needle satisfies it and it can never red.
	identities := cldSubsection(t, acDocSectionText(t, doc, cldSection), cldIdentitiesHeading)
	if !strings.Contains(strings.ToLower(strings.ReplaceAll(identities, cldBoxlessLever, cldLeverMask)), strings.ToLower(cldGeoLever)) {
		t.Errorf("%s's %q subsection names %s only inside %s — the operator who bumps the geometric lever is the reader this subsection exists for:\n\n%s",
			acDoc, cldIdentitiesHeading, cldGeoLever, cldBoxlessLever, identities)
	}
}
