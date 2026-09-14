// doc_test.go: docs/extraction-corpus.md's two end-to-end sections read as an oracle rather
// than as prose beside the number. This file holds the parsers and the phrase specs; the
// row-by-row half needs a live walk and lives in doc_db_test.go. No database.
package endtoend

import (
	"fmt"
	"maps"
	"path/filepath"
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

// AC-5.9. The doc's own miss count must equal len(eeRealMisses); a second copy that drifts from
// the pinned map is worse than no copy.
func TestCorpusDoc_RealMissCountMatchesEeRealMisses(t *testing.T) {
	section := eeDocSection(t, wildReadFile(t, eeDocFile), eeDocAccSection)

	re := regexp.MustCompile(`([0-9]+) real misses`)
	m := re.FindAllStringSubmatch(section, -1)
	if len(m) != 1 {
		t.Fatalf("%s's %q section carries %d \"N real misses\" phrase(s), want exactly 1", eeDocFile, eeDocAccSection, len(m))
	}
	got, err := strconv.Atoi(m[0][1])
	if err != nil {
		t.Fatalf("parse %q: %v", m[0][1], err)
	}
	if got != len(eeRealMisses) {
		t.Errorf("%s says %d real misses; eeRealMisses holds %d", eeDocFile, got, len(eeRealMisses))
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

// --- the as-printed siblings --------------------------------------------------

const (
	eeDocSiblingSection = "## The as-printed siblings"
	eeDocAddingSection  = "## Adding a layout"
	eeDocDivergenceSub  = "### What each divergence did"
	eeDocSourcesHome    = "Simon Vault/Projects/ASComply Africa/User Stories/EXTR/sources/"
	eeDocPairsEdit      = "A new `wild_*` arrangement declares a `wildPairs` row: its as-printed sibling, or an exemption with the reason."
	eeDocCollision      = "**The EXTR-33 collision.**"
	eeDocParitySpec     = "TestWildPairs_ATwinHitIsASiblingHitOrAnOwnedMiss"
	eeDocDistinctLabels = 26
)

// eeDocPairSources names each wildPairs twin's source document as the vault holds it.
var eeDocPairSources = map[string]string{
	wildTwoParty: "NG-Invoice-2-Rivers-Energy-boxed-header.pdf",
	wildRuled:    "NG-Invoice-4-Sahara-Telecoms-dense.pdf",
	wildStacked:  "NG-Invoice-3-Okonkwo-Advisory-minimal.pdf",
	wildRCNaira:  "NG-Invoice-1-Adeola-Steel-classic.pdf",
	wildScanned:  "NG-Invoice-5-Ibadan-Goods-scan.pdf",
}

// eeDocLabelCounts is the label tables' counted rows per sibling.
var eeDocLabelCounts = map[string]int{wildTwoPartyAsPrinted: 10, wildRuledAsPrinted: 8, wildStackedAsPrinted: 9}

// eeDocClass2Specs assert a paired twin's mechanism on its exact tokens and are not extended to
// its sibling; the corpus page's parity rule names each one.
var eeDocClass2Specs = []string{
	"TestWildLayouts_TheRuledTableReproducesACompetingTotal",
	"TestWildLayouts_TheRuledTableTotalStillTakesTheLineAmount",
	"TestWildLayouts_ALabelledCurrencyStillHeadsTheRuledTable",
	"TestRLS_EndToEndTheRuledLayoutReachesTheInvoiceUnderDocling",
	"TestLineItems_TheRuledWildFixtureNowMapsRateAndAmount",
	"TestEndToEnd_TheStackedBorderlessArrangementResolvesItsOffsetTotal",
	"TestWildLayouts_TheTwoPartyTINsBindToTheirOwnParty",
	"TestWildLayouts_TheTwoPartyBuyerNameIsTheName",
}

// eeDocVerdicts is the one verdict each divergence record carries and what else it must name.
var eeDocVerdicts = []struct {
	letter, verdict string
	needles         []string
}{
	{"a", "fixed by EXTR-26", []string{"EXTR-29", "`TOTAL DUE (NGN)`"}},
	{"b", "fixed by EXTR-24", []string{"8acf0879"}},
	{"c", "reproduces", []string{"EXTR-31", "TestWildLayouts_TheStackedSiblingReadsNoInvoiceNumber"}},
	{"d", "not reproduced", []string{"unmeasured", "TestWildLayouts_TheTwoPartySiblingDoesNotReproduceTheRegNoAsBuyerTIN"}},
}

var (
	eeDocRecordRE  = regexp.MustCompile(`(?m)^#### \(([a-z])\) `)
	eeDocVerdictRE = regexp.MustCompile(`(?m)^Verdict: \*\*([^*]+)\*\*\.$`)
	eeDocTINRE     = regexp.MustCompile(`\b[0-9]{8}-[0-9]{4}\b`)
	eeDocPairsHead = regexp.MustCompile(`(?m)^\| Lexicon-friendly \|`)
	eeDocStale     = []string{"EXTR-33 (`Planned`)", "has no EXTR-33 divergence", "The five `wild_*.pdf` arrangements"}
)

// eeDocSubsection is heading's body inside section, cut at the next "### ".
func eeDocSubsection(t *testing.T, section, heading string) string {
	t.Helper()
	marker := "\n" + heading + "\n"
	if n := strings.Count(section, marker); n != 1 {
		t.Fatalf("%q carries %d %q subsection(s), want exactly 1", eeDocSiblingSection, n, heading)
	}
	body := section[strings.Index(section, marker)+len(marker):]
	if j := strings.Index(body, "\n### "); j >= 0 {
		body = body[:j]
	}
	return body
}

// eeDocPairProblems reports each pair no table row names with its twin, source, and sibling or exemption.
func eeDocPairProblems(section string, pairs []wildPair, sources map[string]string) []string {
	var rows []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| ") {
			rows = append(rows, line)
		}
	}
	var problems []string
	for _, p := range pairs {
		member := p.exemption
		if p.sibling != "" {
			member = "`" + p.sibling + "`"
		}
		src := "`" + sources[p.twin] + "`"
		if !slices.ContainsFunc(rows, func(r string) bool {
			return strings.Contains(r, "`"+p.twin+"`") && strings.Contains(r, src) && strings.Contains(r, member)
		}) {
			problems = append(problems, fmt.Sprintf("%s: no table row names it with %s and %s", p.twin, src, member))
		}
	}
	return problems
}

// eeDocLabelProblems reports a label absent from the section or not exactly one token on its page.
func eeDocLabelProblems(section string, labels, pageTexts map[string][]string) []string {
	var problems []string
	for _, sib := range slices.Sorted(maps.Keys(labels)) {
		for _, l := range labels[sib] {
			if !strings.Contains(section, "`"+l+"`") {
				problems = append(problems, fmt.Sprintf("%s: label %q is not in %q", sib, l, eeDocSiblingSection))
			}
			n := 0
			for _, text := range pageTexts[sib] {
				if text == l {
					n++
				}
			}
			if n != 1 {
				problems = append(problems, fmt.Sprintf("%s: label %q is %d token(s) on the page, want exactly 1", sib, l, n))
			}
		}
	}
	return problems
}

// eeDocDivergenceProblems reports a record that is missing, repeated, or lacks its verdict or needles.
func eeDocDivergenceProblems(sub string) []string {
	var problems []string
	locs := eeDocRecordRE.FindAllStringSubmatchIndex(sub, -1)
	if len(locs) != len(eeDocVerdicts) {
		problems = append(problems, fmt.Sprintf("%d record(s), want %d", len(locs), len(eeDocVerdicts)))
	}
	bodies := map[string]string{}
	for i, loc := range locs {
		letter, end := sub[loc[2]:loc[3]], len(sub)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if _, dup := bodies[letter]; dup {
			problems = append(problems, fmt.Sprintf("record (%s) occurs more than once", letter))
		}
		bodies[letter] = sub[loc[0]:end]
	}
	for _, v := range eeDocVerdicts {
		body, ok := bodies[v.letter]
		if !ok {
			problems = append(problems, fmt.Sprintf("record (%s) is missing", v.letter))
			continue
		}
		if got := eeDocVerdictRE.FindAllStringSubmatch(body, -1); len(got) != 1 || got[0][1] != v.verdict {
			problems = append(problems, fmt.Sprintf("record (%s) carries verdict(s) %q, want exactly %q", v.letter, got, v.verdict))
		}
		for _, needle := range append([]string{"d36be21e", "70cc8dcd"}, v.needles...) {
			if !strings.Contains(body, needle) {
				problems = append(problems, fmt.Sprintf("record (%s) never names %s", v.letter, needle))
			}
		}
	}
	return problems
}

// eeDocStaleProblems reports each stale claim; whitespace is collapsed so a line break cannot hide one.
func eeDocStaleProblems(text string) []string {
	flat := strings.Join(strings.Fields(text), " ")
	var problems []string
	for _, s := range eeDocStale {
		if strings.Contains(flat, s) {
			problems = append(problems, fmt.Sprintf("still says %q", s))
		}
	}
	return problems
}

// eeDocMissingSpecs reports each name that no source declares as a func.
func eeDocMissingSpecs(names, sources []string) []string {
	var missing []string
	for _, n := range names {
		if !slices.ContainsFunc(sources, func(src string) bool { return strings.Contains(src, "func "+n+"(") }) {
			missing = append(missing, n)
		}
	}
	return missing
}

// Core AC-3, AC-4. Every wildPairs row sits on the pairs table beside its source, and the page
// says the sources are synthetic before it first says "verbatim".
func TestCorpusDoc_RecordsEveryAsPrintedPair(t *testing.T) {
	if len(wildPairs) < 5 || len(eeDocPairSources) != len(wildPairs) {
		t.Fatalf("%d pair(s) and %d source(s), want at least 5 and one source per pair", len(wildPairs), len(eeDocPairSources))
	}
	doc := wildReadFile(t, eeDocFile)
	section := eeDocSection(t, doc, eeDocSiblingSection)

	lower := strings.ToLower(doc)
	stmt, verbatim := strings.Index(lower, "synthetic mock-ups"), strings.Index(lower, "verbatim")
	if !strings.Contains(section, "synthetic mock-ups") || verbatim < 0 || stmt > verbatim {
		t.Errorf("%s says \"verbatim\" at byte %d and \"synthetic mock-ups\" at byte %d; the statement must come first, inside %q", eeDocFile, verbatim, stmt, eeDocSiblingSection)
	}
	for _, needle := range append([]string{eeDocSourcesHome}, slices.Sorted(maps.Values(eeDocPairSources))...) {
		if !strings.Contains(section, needle) {
			t.Errorf("%q never names %q", eeDocSiblingSection, needle)
		}
	}
	if !eeDocPairsHead.MatchString(section) {
		t.Errorf("%q has no table headed | Lexicon-friendly |", eeDocSiblingSection)
	}
	for _, p := range eeDocPairProblems(section, wildPairs, eeDocPairSources) {
		t.Error(p)
	}
	if adding := strings.Join(strings.Fields(eeDocSection(t, doc, eeDocAddingSection)), " "); !strings.Contains(adding, eeDocPairsEdit) {
		t.Errorf("%q does not carry the edit %q", eeDocAddingSection, eeDocPairsEdit)
	}
	tins := eeDocTINRE.FindAllString(section, -1)
	if len(tins) == 0 {
		t.Errorf("%q carries no TIN-shaped string; the reserved-block check reads nothing", eeDocSiblingSection)
	}
	for _, tin := range tins {
		if !strings.HasPrefix(tin, "99999999-") {
			t.Errorf("%q carries %s, outside the reserved 99999999- block", eeDocSiblingSection, tin)
		}
	}

	// Controls: one pair row removed, one exemption reason blanked, one source blanked.
	if got := eeDocPairProblems(strings.ReplaceAll(section, "`"+eeDocPairSources[wildStacked]+"`", ""), wildPairs, eeDocPairSources); len(got) != 1 || !strings.Contains(got[0], wildStacked) {
		t.Errorf("with the stacked pair's source blanked the checker reports %q, want exactly one problem naming %s", got, wildStacked)
	}
	var ruledRow string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| ") && strings.Contains(line, "`"+wildRuledAsPrinted+"`") && strings.Contains(line, eeDocPairSources[wildRuled]) {
			ruledRow = line
		}
	}
	if got := eeDocPairProblems(strings.Replace(section, ruledRow+"\n", "", 1), wildPairs, eeDocPairSources); len(got) != 1 || !strings.Contains(got[0], wildRuled) {
		t.Errorf("with the ruled pair's row removed the checker reports %q, want exactly one problem naming %s", got, wildRuled)
	}
	exemption := wildPairs[slices.IndexFunc(wildPairs, func(p wildPair) bool { return p.twin == wildScanned })].exemption
	if got := eeDocPairProblems(strings.ReplaceAll(section, exemption, ""), wildPairs, eeDocPairSources); len(got) != 1 || !strings.Contains(got[0], wildScanned) {
		t.Errorf("with the scanned exemption blanked the checker reports %q, want exactly one problem naming %s", got, wildScanned)
	}
}

// Core AC-3. Each sibling's printed labels are exactly one token each on its own page, and each
// appears on the corpus page.
func TestCorpusDoc_CarriesEachSiblingsPrintedLabels(t *testing.T) {
	pageTexts := map[string][]string{}
	distinct := map[string]bool{}
	for _, p := range wildPairs {
		if p.sibling == "" {
			continue
		}
		if got := len(wildAsPrintedLabels[p.sibling]); got != eeDocLabelCounts[p.sibling] {
			t.Errorf("wildAsPrintedLabels[%s] holds %d label(s), want %d", p.sibling, got, eeDocLabelCounts[p.sibling])
		}
		for _, l := range wildAsPrintedLabels[p.sibling] {
			distinct[l] = true
		}
		for _, text := range wildTokenTexts(wildPages(t, p.sibling)) {
			pageTexts[p.sibling] = append(pageTexts[p.sibling], strings.TrimSpace(text))
		}
	}
	if len(pageTexts) != len(eeDocLabelCounts) || len(wildAsPrintedLabels) != len(eeDocLabelCounts) || len(distinct) != eeDocDistinctLabels {
		t.Fatalf("%d sibling page(s), %d labelled sibling(s), %d distinct label(s); want %d, %d and %d", len(pageTexts), len(wildAsPrintedLabels), len(distinct), len(eeDocLabelCounts), len(eeDocLabelCounts), eeDocDistinctLabels)
	}

	section := eeDocSection(t, wildReadFile(t, eeDocFile), eeDocSiblingSection)
	for _, p := range eeDocLabelProblems(section, wildAsPrintedLabels, pageTexts) {
		t.Error(p)
	}

	// Controls: a planted label is absent from both; the untranscribed total is absent from the page.
	if got := eeDocLabelProblems(section, map[string][]string{wildTwoPartyAsPrinted: {"Planted Label"}}, pageTexts); len(got) != 2 {
		t.Errorf("a planted label reports %q, want two problems (section and page)", got)
	}
	if got := eeDocLabelProblems(section, map[string][]string{wildRuledAsPrinted: {"TOTAL DUE (NGN)"}}, pageTexts); len(got) != 1 || !strings.Contains(got[0], "0 token(s)") {
		t.Errorf("TOTAL DUE (NGN) on the ruled list reports %q, want exactly one problem: 0 token(s) on the page", got)
	}
}

// Core AC-6. One record per divergence carrying its verdict and both builds, and the stale
// collision claims are gone from the page.
func TestCorpusDoc_RecordsWhatEachDivergenceDid(t *testing.T) {
	if got := eeDocStaleProblems("its source has no\nEXTR-33 divergence at all"); len(got) != 1 {
		t.Fatalf("the stale scan reports %q on a claim broken across a line, want exactly one problem", got)
	}
	doc := wildReadFile(t, eeDocFile)
	for _, p := range eeDocStaleProblems(doc) {
		t.Errorf("%s %s", eeDocFile, p)
	}

	adding := eeDocSection(t, doc, eeDocAddingSection)
	start := strings.Index(adding, eeDocCollision)
	if start < 0 {
		t.Fatalf("%q carries no %s paragraph", eeDocAddingSection, eeDocCollision)
	}
	collision := adding[start:]
	if j := strings.Index(collision, "\n\n"); j >= 0 {
		collision = collision[:j]
	}
	for _, want := range []string{"`" + wildStackedAsPrinted + "`", "EXTR-29"} {
		if !strings.Contains(collision, want) {
			t.Errorf("%s never names %s", eeDocCollision, want)
		}
	}

	sub := eeDocSubsection(t, eeDocSection(t, doc, eeDocSiblingSection), eeDocDivergenceSub)
	for _, p := range eeDocDivergenceProblems(sub) {
		t.Error(p)
	}

	// Controls: (c) and (d) swap verdicts; (a) is recorded twice.
	swapped := strings.NewReplacer("Verdict: **reproduces**.", "Verdict: **not reproduced**.", "Verdict: **not reproduced**.", "Verdict: **reproduces**.").Replace(sub)
	if got := eeDocDivergenceProblems(swapped); len(got) != 2 || !strings.Contains(got[0], "(c)") || !strings.Contains(got[1], "(d)") {
		t.Errorf("swapped verdicts report %q, want one problem for (c) then one for (d)", got)
	}
	if got := eeDocDivergenceProblems(sub + "\n#### (a) planted\n"); !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, "(a) occurs more than once") }) {
		t.Errorf("a repeated record reports %q, want a problem naming (a) twice", got)
	}
}

// Core AC-5. The parity rule names the mechanical spec and every class-2 spec, and each one is
// still declared.
func TestCorpusDoc_StatesTheSiblingParityRule(t *testing.T) {
	var sources []string
	for _, glob := range []string{"*_test.go", "../*_test.go"} {
		paths, err := filepath.Glob(glob)
		if err != nil {
			t.Fatalf("glob %s: %v", glob, err)
		}
		for _, path := range paths {
			sources = append(sources, wildReadFile(t, path))
		}
	}
	if len(sources) < 50 {
		t.Fatalf("read %d _test.go file(s), want at least 50; a missing spec would read as declared nowhere for the wrong reason", len(sources))
	}
	names := append([]string{eeDocParitySpec}, eeDocClass2Specs...)
	for _, n := range eeDocMissingSpecs(names, sources) {
		t.Errorf("%s is named by the parity rule and declared by no _test.go under internal/extraction", n)
	}
	if got := eeDocMissingSpecs([]string{"TestWildLayouts_APlantedSpecNobodyDeclares"}, sources); len(got) != 1 {
		t.Errorf("a planted spec name reports %q, want it missing", got)
	}

	section := eeDocSection(t, wildReadFile(t, eeDocFile), eeDocSiblingSection)
	for _, n := range names {
		if !strings.Contains(section, "`"+n+"`") {
			t.Errorf("%q never names %s", eeDocSiblingSection, n)
		}
	}
}

var (
	eeDocInventoryRE = regexp.MustCompile(`^- \*\*(NG-[1-5]) `)
	eeDocSourceRE    = regexp.MustCompile(`^NG-[1-5]\b`)
)

// eeDocInventoryProblems reports a sibling label whose label-table row names no source inventory
// that prints it. Whitespace is collapsed because pdfium collapses a letter-spaced word gap.
func eeDocInventoryProblems(t *testing.T, section string, labels map[string][]string) []string {
	t.Helper()
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	inventories, src := map[string]string{}, ""
	for _, line := range strings.Split(eeDocSubsection(t, section, "### Source label inventories"), "\n") {
		if m := eeDocInventoryRE.FindStringSubmatch(line); m != nil {
			src = m[1]
		}
		if src != "" {
			inventories[src] += flat(line) + " "
		}
	}
	tables := eeDocSubsection(t, section, "### Label tables")
	var problems []string
	for _, sib := range slices.Sorted(maps.Keys(labels)) {
		marker := "**`" + sib + "`**"
		i := strings.Index(tables, marker)
		if i < 0 {
			problems = append(problems, fmt.Sprintf("%s: no label table", sib))
			continue
		}
		table := tables[i+len(marker):]
		if j := strings.Index(table, "\n**"); j >= 0 {
			table = table[:j]
		}
		for _, l := range labels[sib] {
			var sources []string
			for _, row := range strings.Split(table, "\n") {
				cells := strings.Split(row, " | ")
				if strings.HasPrefix(row, "| ") && len(cells) == 4 && (cells[2] == "`"+l+"`" || cells[2] == "same" && cells[1] == "`"+l+"`") {
					sources = append(sources, eeDocSourceRE.FindString(cells[3]))
				}
			}
			if len(sources) != 1 || sources[0] == "" {
				problems = append(problems, fmt.Sprintf("%s: label %q has label-table source(s) %q, want exactly one NG-n", sib, l, sources))
				continue
			}
			if !strings.Contains(inventories[sources[0]], "`"+flat(l)+"`") {
				problems = append(problems, fmt.Sprintf("%s: label %q is not in the %s inventory", sib, l, sources[0]))
			}
		}
	}
	return problems
}

// Core AC-3. Each sibling label's table row names its source, and that source's inventory prints it.
func TestCorpusDoc_EachPrintedLabelIsInItsSourceInventory(t *testing.T) {
	section := eeDocSection(t, wildReadFile(t, eeDocFile), eeDocSiblingSection)
	for _, p := range eeDocInventoryProblems(t, section, wildAsPrintedLabels) {
		t.Error(p)
	}

	// Controls: the inventory's `Taxable amount` (its first occurrence) renamed; the signature row's source moved.
	renamed := strings.Replace(section, "`Taxable amount`", "`Taxable Amount`", 1)
	if got := eeDocInventoryProblems(t, renamed, wildAsPrintedLabels); len(got) != 1 || !strings.Contains(got[0], "Taxable amount") {
		t.Errorf("with the inventory's Taxable amount renamed the checker reports %q, want exactly one problem naming it", got)
	}
	moved := strings.Replace(section, "| NG-5: ", "| NG-2: ", 1)
	if got := eeDocInventoryProblems(t, moved, wildAsPrintedLabels); len(got) != 1 || !strings.Contains(got[0], "Customer's Signature") {
		t.Errorf("with the signature row's source moved to NG-2 the checker reports %q, want exactly one problem naming it", got)
	}
}
