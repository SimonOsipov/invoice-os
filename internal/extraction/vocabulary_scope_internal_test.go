// vocabulary_scope_internal_test.go: which spellings each lexicon entry admits, and which it
// must keep refusing. Package extraction: every spec reads a span through alSpan
// (anchor_internal_test.go) or calls anchorOutranked, both unexported.
package extraction

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// T-01.1: alSuffix lets a two-word party label take one of four enumerated tails between its
// words. Without it `bill\s*to` needs "bill" then whitespace then "to", and in "Billed to" the
// next char is "e" -- no match at all.
func TestAnchorLexicon_ATwoWordPartyLabelTakesAnEnumeratedSuffix(t *testing.T) {
	for _, c := range []struct {
		text string
		loc  []int
	}{
		{"Billed to", []int{0, 9}},
		{"Billed To:", []int{0, 9}},
		{"Billing to", []int{0, 10}},
		{"Bills to", []int{0, 8}},
		{"Delivered to", []int{0, 12}},
		{"Invoiced to", []int{0, 11}},
	} {
		loc := alSpan(c.text, "buyer_name")
		if loc == nil {
			t.Errorf("%q: buyer_name span = nil, want %v", c.text, c.loc)
			continue
		}
		if loc[0] != c.loc[0] || loc[1] != c.loc[1] {
			t.Errorf("%q: buyer_name span = %v, want %v", c.text, loc, c.loc)
		}
	}
}

// T-01.2: the suffix must not reach the four uninflected two-word entries it was never meant
// for: 0 of 16 spellings match at offset 0. It is the fence that reds on all 16 the moment
// someone generalises alSuffix into one of these patterns.
//
// Not `loc == nil`: total's bare "total" arm and issue_date's bare "date" arm already match the
// trailing noun of half these spellings at a non-zero offset, for a reason unrelated to
// alSuffix (.ralph/arch-26-01.md). Only "no match starts at offset 0" isolates the two-word arm.
func TestAnchorLexicon_TheSuffixDoesNotReachTheUninflectedEntries(t *testing.T) {
	tails := []string{"ed", "ing", "s", "d"}
	cases := []struct{ id, first, second string }{
		{"subtotal", "sub", "total"},
		{"total", "grand", "total"},
		{"total", "amount", "due"},
		{"issue_date", "invoice", "date"},
	}

	n := 0
	for _, c := range cases {
		for _, tail := range tails {
			n++
			text := c.first + tail + " " + c.second
			if loc := alSpan(text, c.id); loc != nil && loc[0] == 0 {
				t.Errorf("%s: %q matches at %v, want no match starting at offset 0 -- the two-word arm fired on a spelling alSuffix must not reach", c.id, text, loc)
			}
		}
	}
	if n != 16 {
		t.Fatalf("checked %d spelling(s), want 16", n)
	}
}

// T-01.3: the paired positive for T-01.1 -- without it, T-01.1 would pass against a pattern
// loosened to match everything.
func TestAnchorLexicon_TheOriginalPartySpellingsStillMatch(t *testing.T) {
	for _, text := range []string{"Bill To", "Sold to", "Invoice to", "Deliver to", "Buyer"} {
		if alSpan(text, "buyer_name") == nil {
			t.Errorf("%q: buyer_name span = nil, want a match", text)
		}
	}
	if loc := alSpan("Supplier", "buyer_name"); loc != nil {
		t.Errorf("%q: buyer_name span = %v, want no match", "Supplier", loc)
	}
}

// T-01.5: the loosened buyer_name/buyer_tin must still outrank the party-less bare_tin. Without
// the suffix buyer_tin does not match this token at all, leaving bare_tin unopposed.
func TestAnchorLexicon_TheLoosenedBuyerLabelStillOutranksTheBareTIN(t *testing.T) {
	const text = "Billed to TIN: 99999999-1302"

	// bare_tin's own \s* prefix consumes nothing at this offset, so the span keeps its leading
	// space -- width 4. "TIN" alone would be width 3 and is the wrong oracle here.
	bare := alSpan(text, "bare_tin")
	if want := []int{9, 13}; bare == nil || bare[0] != want[0] || bare[1] != want[1] {
		t.Fatalf("bare_tin span = %v, want %v", bare, want)
	}
	if !anchorOutranked(text, bare) {
		t.Errorf("anchorOutranked(%q, %v) = false, want true: buyer_tin must claim a strictly wider span", text, bare)
	}

	tin := alSpan(text, "buyer_tin")
	if want := []int{0, 13}; tin == nil || tin[0] != want[0] || tin[1] != want[1] {
		t.Fatalf("buyer_tin span = %v, want %v", tin, want)
	}
	if tinWidth, bareWidth := tin[1]-tin[0], bare[1]-bare[0]; tinWidth <= bareWidth {
		t.Errorf("buyer_tin width = %d, bare_tin width = %d, want buyer_tin strictly wider", tinWidth, bareWidth)
	}
}

// T-01.6: a characterisation pin, not red-first. Non-vacuity is
// structural rather than a length check: every row asserts a non-nil span, so the table reds if
// the matcher goes silent; "₦Total" starts at offset 3, so it reds under a `^` anchor; the nine
// trailing-decoration rows end before len(text), so they red under a `$` anchor; and slicing the
// raw text by loc reds if any normalisation pass runs ahead of FindStringIndex.
func TestAnchorLexicon_ADecoratedLabelStillNamesItsOwnField(t *testing.T) {
	for _, c := range []struct{ text, owner, want string }{
		{"Sub-total:", "subtotal", "Sub-total"},
		{"SUBTOTAL ₦", "subtotal", "SUBTOTAL"},
		{"Subtotal (NGN)", "subtotal", "Subtotal"},
		{"Subtotal %", "subtotal", "Subtotal"},
		{"CURRENCY (NGN)", "currency", "CURRENCY"},
		{"TIN #", "bare_tin", "TIN"},
		{"VAT @ 7.5%", "vat", "VAT"},
		{"TOTAL DUE (NGN)", "total", "TOTAL"},
		{"₦Total", "total", "Total"},
		{"GRAND TOTAL —", "total", "GRAND TOTAL"},
	} {
		loc := alSpan(c.text, c.owner)
		if loc == nil {
			t.Errorf("%q: %s span = nil, want a match", c.text, c.owner)
			continue
		}
		if got := c.text[loc[0]:loc[1]]; got != c.want {
			t.Errorf("%q: %s matched %q, want %q", c.text, c.owner, got, c.want)
		}
	}
}

// T-02.1: the three advisory arms, each claimed by exactly one entry. Before the widening all
// five spellings missed every entry.
func TestAnchorLexicon_TheAdvisoryWordsNameTheirOwnField(t *testing.T) {
	for _, c := range []struct{ text, owner string }{
		{"Taxable amount", "subtotal"},
		{"Amount payable", "total"},
		{"Amount payable (NGN)", "total"},
		{"Issued", "issue_date"},
		{"Issued:", "issue_date"},
	} {
		ids := alIDs(c.text)
		if len(ids) != 1 {
			t.Errorf("%q: claimed by %v, want exactly one entry", c.text, ids)
			continue
		}
		if ids[0] != c.owner {
			t.Errorf("%q: claimed by %q, want %q", c.text, ids[0], c.owner)
		}
	}
}

// T-02.2: the non-vacuity fence for T-02.1. The last two rows are what make
// Core AC-3 work: total has no bare amount arm, and subtotal's amount arm needs a leading net,
// so the two new arms can never cross into each other's field.
func TestAnchorLexicon_TheAdvisoryWordsDoNotReachTheirNeighbours(t *testing.T) {
	for _, c := range []struct{ text, id string }{
		{"Subtotal", "total"},
		{"Grand Total", "subtotal"},
		{"Dated", "issue_date"},
		{"Tax", "subtotal"},
		{"Taxable amount", "total"},
		{"Amount payable", "subtotal"},
	} {
		if loc := alSpan(c.text, c.id); loc != nil {
			t.Errorf("%q: %s span = %v, want no match", c.text, c.id, loc)
		}
	}
}

// T-03.1: the five leading spellings anchor supplier_name at offset 0.
func TestAnchorLexicon_FromNamesTheSupplierWhenItLeadsTheToken(t *testing.T) {
	for _, text := range []string{"From", "From:", "FROM", "  From", "From: Kaduna Advisory Partners"} {
		loc := alSpan(text, "supplier_name")
		if loc == nil {
			t.Errorf("%q: supplier_name span = nil, want a match starting at 0", text)
			continue
		}
		if loc[0] != 0 {
			t.Errorf("%q: supplier_name span = %v, want it to start at 0", text, loc)
		}
	}

	// The paired positive: the rewritten alternation must still carry its original arm, and
	// Buyer must still refuse it.
	for _, text := range []string{"Supplier", "Seller", "Vendor"} {
		if alSpan(text, "supplier_name") == nil {
			t.Errorf("%q: supplier_name span = nil, want a match", text)
		}
	}
	if loc := alSpan("Buyer", "supplier_name"); loc != nil {
		t.Errorf("%q: supplier_name span = %v, want no match", "Buyer", loc)
	}
}

// T-03.2: the terminator's whole justification. A LEADING from in ordinary prose must refuse:
// a bare `^\s*from\b` with no terminator accepts every one of the four below, fabricates a supplier
// name and loses buyer_tin (TestParty_ALeadingProseFromLeavesTheBuyerTINAlone). The mid-sentence
// rows below are the terminator's alone; the leading anchor's own rows are in
// TestAnchorLexicon_AMidSentenceFromWithATerminatorNamesNobody.
func TestAnchorLexicon_FromInProseNamesNobody(t *testing.T) {
	midSentence := []string{
		"Balance carried forward from previous invoice",
		"Amount due from customer",
		"Services rendered from 01 Aug to 31 Aug 2026",
		"Payment received from Honeywell Group",
		"Freight from Lagos",
	}
	leading := []string{
		"From Lagos to Abuja",
		"From 01 Aug 2026 to 31 Aug 2026",
		"From the above, the amount due is",
		"From our records",
	}
	for _, text := range append(append([]string{}, midSentence...), leading...) {
		if loc := alSpan(text, "supplier_name"); loc != nil {
			t.Errorf("%q: supplier_name span = %v, want no match", text, loc)
		}
	}
}

// T-03.5: the terminator's cost, characterised rather than left silent. The accepted trade: the
// alternative -- an unterminated leading anchor -- fabricates a supplier out of a route line
// and takes buyer_tin with it (TestParty_ALeadingProseFromLeavesTheBuyerTINAlone).
func TestAnchorLexicon_AnUncolonnedFromNamesNoSupplier(t *testing.T) {
	const text = "From Kaduna Advisory Partners"
	if loc := alSpan(text, "supplier_name"); loc != nil {
		t.Errorf("%q: supplier_name span = %v, want no match -- an uncolonned inline heading reads no supplier name", text, loc)
	}
}

// T-04.2: withholding_tax must strictly contain vat's span on every AC-2 spelling, not just the
// plain one. "WHT (Withholding Tax)" is the sharpest case: vat's bare "tax" arm matches inside
// the parenthesis, and the owner's span must still reach around it, not stop short.
func TestAnchorLexicon_TheWithholdingPhraseStrictlyContainsTheVATWord(t *testing.T) {
	for _, text := range []string{
		"Withholding tax 10%",
		"WHT (Withholding Tax)",
		"With-holding Tax",
		"WITHHOLDING TAX",
		"Withhold tax",
		"Withholding  Tax",
	} {
		// Non-vacuity: the containment claim below compares against nothing if vat itself missed.
		vat := alSpan(text, "vat")
		if vat == nil {
			t.Errorf("%q: vat span = nil, want a match", text)
			continue
		}
		if !anchorOutranked(text, vat) {
			t.Errorf("anchorOutranked(%q, %v) = false, want true: withholding_tax must claim a strictly wider span", text, vat)
		}
		owner := alSpan(text, "withholding_tax")
		if owner == nil {
			t.Errorf("%q: withholding_tax span = nil, want a match", text)
			continue
		}
		if !(owner[0] <= vat[0] && owner[1] >= vat[1] && owner[1]-owner[0] > vat[1]-vat[0]) {
			t.Errorf("%q: withholding_tax span %v does not strictly contain vat span %v", text, owner, vat)
		}
	}
}

// Both \b anchors survived mutation: dropping either left every spec green. The leading one is
// behavioural -- without it the phrase opens mid-word and suppresses a vat label the token really
// carries; the trailing one is match-level, like doc_title's "TAX INVOICES" reject.
func TestAnchorLexicon_TheWithholdingPhraseIsBoundedAtBothEnds(t *testing.T) {
	// The paired positives: the refusals below hold equally against a pattern broken to match
	// nothing. Both boundaries sit against a non-word char here, so both still match.
	for _, text := range []string{"WHT-Withholding tax", "Withholding tax: 318,742.00"} {
		if alSpan(text, "withholding_tax") == nil {
			t.Errorf("%q: withholding_tax span = nil, want a match", text)
		}
	}

	for _, c := range []struct{ text, why string }{
		{"Nonwithholding tax", "the phrase opened inside a word it does not own"},
		{"Withholding Taxable amount", "the phrase ran past its own tax into the taxable-amount label"},
	} {
		if loc := alSpan(c.text, "withholding_tax"); loc != nil {
			t.Errorf("withholding_tax claims %v on %q; %s", loc, c.text, c.why)
		}
	}

	// The consequence the leading boundary buys: that token's own vat label still anchors.
	const inner = "Nonwithholding tax"
	vat := alSpan(inner, "vat")
	if vat == nil {
		t.Fatalf("%q: vat span = nil; the suppression claim below has nothing to stand on", inner)
	}
	if anchorOutranked(inner, vat) {
		t.Errorf("anchorOutranked(%q, %v) = true, want false: a phrase opening mid-word suppressed a label it does not contain", inner, vat)
	}
}

// vsVATPatternAtEXTR22 is an independently typed transcription of the shipped vat entry's
// pattern, verified byte-identical against origin/main and HEAD at authoring time. EXTR-22 owns
// this pattern; this story adds an entry above it and writes nothing inside it.
const vsVATPatternAtEXTR22 = `(?i)\b(vat|v\.a\.t\.?|tax)\b`

// T-04.5: a guard, not a driver -- passes on arrival. Measured (.ralph/arch-26-04.md 9a): an
// ADDITIVE widening of vat (adding a |levy arm) passes the entire extraction and endtoend
// suites, so this is the only mechanical guard on that pattern. The reference literal is retyped
// above, never read back from anchorLexicon (a tautology) and never fetched via git at test time
// (CI clones at fetch-depth 1, so origin/main is unreachable there).
func TestAnchorLexicon_TheVATPatternIsUnchangedByThisStory(t *testing.T) {
	n := 0
	for _, e := range anchorLexicon {
		if e.ID != "vat" {
			continue
		}
		n++
		if e.Pattern != vsVATPatternAtEXTR22 {
			t.Errorf("vat pattern = %q, want %q -- EXTR-22 owns this pattern and this story does not write to it", e.Pattern, vsVATPatternAtEXTR22)
		}
	}
	// Non-vacuity: without this, the loop above passes vacuously if the entry is ever renamed
	// or removed.
	if n != 1 {
		t.Fatalf("anchorLexicon holds %d entr(y/ies) with ID %q, want exactly 1", n, "vat")
	}

	// Second witness. Measured: widening vat AND this constant together passes the whole package,
	// so the comparison above cannot tell a co-edit from no edit. reg_identifier is EXTR-22's
	// other site and carries the same alternation byte-identically; taking the needle from the
	// constant makes a co-edit red from a site it did not touch.
	alt := strings.TrimSuffix(strings.TrimPrefix(vsVATPatternAtEXTR22, `(?i)\b`), `\b`)
	if alt == vsVATPatternAtEXTR22 {
		t.Fatalf("%q does not open (?i)\\b and close \\b; the needle below is not the alternation", vsVATPatternAtEXTR22)
	}
	reg := ""
	for _, e := range anchorLexicon {
		if e.ID == "reg_identifier" {
			reg = e.Pattern
		}
	}
	if reg == "" {
		t.Fatalf("anchorLexicon holds no reg_identifier entry; the witness below reads nothing")
	}
	if !strings.Contains(reg, alt) {
		t.Errorf("reg_identifier pattern %q does not carry %q; EXTR-22's two sites carry one alternation, so one of them moved", reg, alt)
	}
}

// vsReadCorpusDoc reads docs/extraction-corpus.md. Package extraction (not _test), so it cannot
// reuse extraction_test's rxRepoRoot/acRepoFile; the test binary's working directory is its own
// package directory, same assumption those helpers make.
func vsReadCorpusDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "extraction-corpus.md"))
	if err != nil {
		t.Fatalf("read docs/extraction-corpus.md: %v", err)
	}
	return string(b)
}

// vsDocSection is heading's body up to the next "## " heading.
func vsDocSection(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("docs/extraction-corpus.md carries no %q section", heading)
	}
	body := doc[i+len(heading):]
	if j := strings.Index(body, "\n## "); j >= 0 {
		body = body[:j]
	}
	return body
}

// vsDocSubsection is heading's body up to the next "## " or "### " heading -- for a heading one
// level deeper than vsDocSection reads.
func vsDocSubsection(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("docs/extraction-corpus.md carries no %q subsection", heading)
	}
	body := doc[i+len(heading):]
	end := len(body)
	for _, marker := range []string{"\n## ", "\n### "} {
		if j := strings.Index(body, marker); j >= 0 && j < end {
			end = j
		}
	}
	return body[:end]
}

// vsFirstParagraph is text up to the first blank line -- markdown's own paragraph unit, the same
// split duedate_scope_test.go's ddParagraphContaining uses.
func vsFirstParagraph(text string) string {
	text = strings.TrimLeft(text, "\n")
	if i := strings.Index(text, "\n\n"); i >= 0 {
		return text[:i]
	}
	return text
}

// vsListedIDRE matches a backtick id immediately opening its own parenthetical example list --
// the shape every entry in "## Labels that own their token" already uses ("`party_ref`
// (`Customer No.`, ...)"). It is what tells "the entries this paragraph enumerates" apart from an
// id the same paragraph merely mentions in passing (`vat`'s bare `tax`, `issue_date`'s bare
// `date`), which carries no parenthetical of its own.
var vsListedIDRE = regexp.MustCompile("`([a-z_]+)`\\s*\\(")

// vsListedLexiconIDs is the deduplicated, sorted set of anchorLexicon ids vsListedIDRE finds in
// text.
func vsListedLexiconIDs(text string) []string {
	known := make(map[string]bool, len(anchorLexicon))
	for _, e := range anchorLexicon {
		known[e.ID] = true
	}
	seen := make(map[string]bool)
	var ids []string
	for _, m := range vsListedIDRE.FindAllStringSubmatch(text, -1) {
		if id := m[1]; known[id] && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// vsToleranceHeading is the literal the doc must carry; the executor writes exactly this string.
const vsToleranceHeading = "### A label may carry a verb inflection"

// T-07.1: the tolerance subsection under "## Labels that own their token" must carry the alSuffix
// literal and name exactly the anchorLexicon entries that carry it -- no more, no fewer.
func TestCorpusDoc_RecordsTheLabelTolerance(t *testing.T) {
	doc := vsReadCorpusDoc(t)
	sub := vsDocSubsection(t, doc, vsToleranceHeading)

	if !strings.Contains(sub, alSuffix) {
		t.Errorf("the %q subsection does not carry the literal alSuffix tail %q", vsToleranceHeading, alSuffix)
	}

	var derived []string
	for _, e := range anchorLexicon {
		if strings.Contains(e.Pattern, alSuffix) {
			derived = append(derived, e.ID)
		}
	}
	sort.Strings(derived)
	// Population floor: a table over an empty set passes vacuously.
	if len(derived) == 0 {
		t.Fatalf("no anchorLexicon entry carries alSuffix; the checks below would pass vacuously")
	}
	wantDerived := []string{"buyer_name", "buyer_tin"}
	if !slices.Equal(derived, wantDerived) {
		t.Fatalf("anchorLexicon entries carrying alSuffix = %v, want %v", derived, wantDerived)
	}

	named := vsListedLexiconIDs(sub)
	if !slices.Equal(named, derived) {
		t.Errorf("%q subsection lists %v as taking the suffix, want exactly %v", vsToleranceHeading, named, derived)
	}
}

// vsOwningPhraseHeading is the existing section T-07.2 reads and re-counts.
const vsOwningPhraseHeading = "## Labels that own their token"

var (
	// vsOpeningCountRE reads the section's opening "<N> entries in `anchorLexicon`" sentence.
	vsOpeningCountRE = regexp.MustCompile(`(?i)\b(\w+) entries in `)
	// vsNoneOfTheRE reads the section's later "None of the <n> carries a Tier-1 rule" sentence.
	vsNoneOfTheRE = regexp.MustCompile(`(?i)\bNone of the (\w+)\b`)
)

// vsNumberWords spells the small counts this section's prose plausibly carries.
var vsNumberWords = map[int]string{
	4: "four", 5: "five", 6: "six", 7: "seven", 8: "eight", 9: "nine", 10: "ten",
}

// T-07.2: every spelled count in "## Labels that own their token" -- the opening sentence and the
// later "None of the <n>" -- must equal len(t1OwningPhraseIDs) spelled in words, and the opening
// paragraph's enumerated ids must equal that declaration exactly.
func TestCorpusDoc_TheOwningPhraseCountMatchesTheDeclaration(t *testing.T) {
	doc := vsReadCorpusDoc(t)
	section := vsDocSection(t, doc, vsOwningPhraseHeading)
	opening := vsFirstParagraph(section)

	want, ok := vsNumberWords[len(t1OwningPhraseIDs)]
	if !ok {
		t.Fatalf("no spelled word for %d in vsNumberWords; extend it", len(t1OwningPhraseIDs))
	}

	openingCount := vsOpeningCountRE.FindAllStringSubmatch(section, -1)
	if len(openingCount) != 1 {
		t.Fatalf("%q section carries %d \"<n> entries in\" phrase(s), want exactly 1", vsOwningPhraseHeading, len(openingCount))
	}
	if got := strings.ToLower(openingCount[0][1]); got != want {
		t.Errorf("opening count = %q, want %q (len(t1OwningPhraseIDs) = %d)", openingCount[0][1], want, len(t1OwningPhraseIDs))
	}

	noneOfThe := vsNoneOfTheRE.FindAllStringSubmatch(section, -1)
	if len(noneOfThe) != 1 {
		t.Fatalf("%q section carries %d \"None of the <n>\" phrase(s), want exactly 1", vsOwningPhraseHeading, len(noneOfThe))
	}
	if got := strings.ToLower(noneOfThe[0][1]); got != want {
		t.Errorf("%s's \"None of the\" count = %q, want %q", vsOwningPhraseHeading, noneOfThe[0][1], want)
	}

	named := vsListedLexiconIDs(opening)
	// Found-needle control: if the parser is blind, party_ref would be absent even though the
	// doc text plainly backticks and enumerates it.
	if !slices.Contains(named, "party_ref") {
		t.Fatalf("party_ref not found among the opening paragraph's listed ids %v; the parser is blind", named)
	}

	wantIDs := append([]string(nil), t1OwningPhraseIDs...)
	sort.Strings(wantIDs)
	if !slices.Equal(named, wantIDs) {
		t.Errorf("%s's opening paragraph lists %v as owning phrases, want exactly %v (t1OwningPhraseIDs)", vsOwningPhraseHeading, named, wantIDs)
	}
}
