// aitext_rules_internal_test.go: acceptance specs for AIR-01-01's page-check rules and scoring
// helpers in aitext_internal_test.go.
package extraction

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- test-local fixture builders (no plan helper lives here) ---------------------------

func tok(text string, page int, x0, y0, x1, y1 float64) Token {
	return Token{Text: text, Region: Region{Page: page, X0: x0, Y0: y0, X1: x1, Y1: y1}}
}

func onePage(page int, tokens ...Token) []TokenPage {
	return []TokenPage{{Number: page, Tokens: tokens}}
}

// findToken locates the one golden token carrying substr. Fatal, not silent: a rule test
// pinned to the wrong token would prove nothing.
func findToken(t *testing.T, pages []TokenPage, substr string) Token {
	t.Helper()
	for _, p := range pages {
		for _, tk := range p.Tokens {
			if strings.Contains(tk.Text, substr) {
				return tk
			}
		}
	}
	t.Fatalf("no golden token contains %q", substr)
	return Token{}
}

func containsAny(pages []TokenPage, substr string) bool {
	for _, p := range pages {
		for _, tk := range p.Tokens {
			if strings.Contains(tk.Text, substr) {
				return true
			}
		}
	}
	return false
}

// unionRegion is rule C's box: the smallest region covering both source cells.
func unionRegion(a, b Region) Region {
	return Region{
		Page: a.Page,
		X0:   math.Min(a.X0, b.X0),
		Y0:   math.Min(a.Y0, b.Y0),
		X1:   math.Max(a.X1, b.X1),
		Y1:   math.Max(a.Y1, b.Y1),
	}
}

func writeJSONFile(t *testing.T, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// ruleCountCell is one aitRuleCounts input row: an AI answer already classified against the
// key, plus the page it must be found on.
type ruleCountCell struct {
	Field   string
	Answer  string
	Verdict string // "right", "wrong" or "blank" -- aitClassify's own words
	Pages   []TokenPage
}

// ruleStat is one aitPickRule input row: one candidate rule's adversarial eligibility and its
// found/accepted counts over a run.
type ruleStat struct {
	Name     string
	Found    int
	Accepted int
	Eligible bool
}

// docDump is the slice element aitScoreConfirmed reads off Stage B's dump.json: one
// document's text_chars gate and its engine reading per field.
type docDump struct {
	File      string
	Set       string
	TextChars int
	Engine    map[string]string // field -> engine FieldResult.Value; absent key = missing
}

// docAnswer is one aitLoadAnswers-good record: one AI run's field readings for one document.
type docAnswer struct {
	File   string
	Run    int
	Fields map[string]string
}

// notScoredEntry is one manifest not_scored row: a document Stage A never sent to Docling.
type notScoredEntry struct {
	File   string
	Reason string
}

// fieldRef names one document x field cell, for the unconfirmed list.
type fieldRef struct {
	File  string
	Field string
}

// cellFixture is the row shape a scored cell is checked against: one document x run x field.
type cellFixture struct {
	File  string
	Run   int
	Field string
	AI    string // aitClassify verdict of that run's answer
}

type promptSegment struct {
	X    float64
	Text string
}

type promptLine struct {
	Page     int
	Y        float64
	Segments []promptSegment
}

func hasCellForField(cells []cellFixture, file, field string) bool {
	for _, c := range cells {
		if c.File == file && c.Field == field {
			return true
		}
	}
	return false
}

func fieldRefListed(list []fieldRef, file, field string) bool {
	for _, r := range list {
		if r.File == file && r.Field == field {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func notScoredContains(list []notScoredEntry, want notScoredEntry) bool {
	for _, e := range list {
		if e == want {
			return true
		}
	}
	return false
}

func notScoredHasFile(list []notScoredEntry, file string) bool {
	for _, e := range list {
		if e.File == file {
			return true
		}
	}
	return false
}

// --- page-check rules (AC 5, 6) ---------------------------------------------------------

func TestAIText_WholeTokenRuleFindsALabelledValue(t *testing.T) {
	pages := aitGoldenPages(t, "wild_scanned_no_number")
	want := findToken(t, pages, "99999999-1202")

	found, box := aitRuleWholeToken("buyer_tin", "99999999-1202", pages)
	if !found {
		t.Fatal("rule A did not find the labelled buyer_tin on the golden page")
	}
	if box != want.Region {
		t.Errorf("box = %+v, want the labelled token's own region %+v", box, want.Region)
	}
}

func TestAIText_InLineRuleFindsAValueInsideALine(t *testing.T) {
	line := tok("Ref 7781 dated 2026-06-11 terms 30 days", 1, 0.10, 0.40, 0.60, 0.42)
	pages := onePage(1, line)

	if foundA, _ := aitRuleWholeToken("issue_date", "2026-06-11", pages); foundA {
		t.Fatal("precondition failed: rule A already finds a value buried inside a longer line")
	}
	foundB, box := aitRuleInLine("issue_date", "2026-06-11", pages)
	if !foundB {
		t.Fatal("rule B did not find the date inside the line")
	}
	if box != line.Region {
		t.Errorf("box = %+v, want the whole line's region %+v", box, line.Region)
	}
}

func TestAIText_JoinedRowRuleJoinsNeighboursOnOneRow(t *testing.T) {
	honeywell := tok("Honeywell", 1, 0.10, 0.300, 0.19, 0.312)
	group := tok("Group", 1, 0.20, 0.301, 0.26, 0.313)
	pages := onePage(1, honeywell, group)

	if foundB, _ := aitRuleInLine("buyer_name", "Honeywell Group", pages); foundB {
		t.Fatal("precondition failed: rule B already joins across two separate tokens")
	}
	foundC, box := aitRuleJoinedRow("buyer_name", "Honeywell Group", pages)
	if !foundC {
		t.Fatal("rule C did not join the two neighbouring tokens on one row")
	}
	if want := unionRegion(honeywell.Region, group.Region); box != want {
		t.Errorf("box = %+v, want the union of both tokens' regions %+v", box, want)
	}
}

func TestAIText_JoinedRowRuleDoesNotJoinAcrossRows(t *testing.T) {
	honeywell := tok("Honeywell", 1, 0.10, 0.30, 0.19, 0.31)
	group := tok("Group", 1, 0.20, 0.40, 0.26, 0.41)
	pages := onePage(1, honeywell, group)

	if found, _ := aitRuleJoinedRow("buyer_name", "Honeywell Group", pages); found {
		t.Error("rule C joined two tokens that sit on different rows")
	}
}

func TestAIText_EveryRuleRejectsTheDroppedDigitTIN(t *testing.T) {
	pages := aitGoldenPages(t, "wild_scanned_no_number")

	if found, _ := aitRuleWholeToken("buyer_tin", "9999999-1202", pages); found {
		t.Error("rule A accepted the dropped-digit TIN")
	}
	if found, _ := aitRuleInLine("buyer_tin", "9999999-1202", pages); found {
		t.Error("rule B accepted the dropped-digit TIN")
	}
	if found, _ := aitRuleJoinedRow("buyer_tin", "9999999-1202", pages); found {
		t.Error("rule C accepted the dropped-digit TIN")
	}
	if found, _ := aitRuleSubstringControl("buyer_tin", "9999999-1202", pages); !found {
		t.Error("the control did not accept the dropped-digit TIN -- this fixture cannot then prove a loose rule fails")
	}
}

// aitInventedValues is one value per header field that occurs nowhere on the golden page --
// package-level so Stage E's summary driver (aitext_harness_internal_test.go) replays the
// same adversarial fixture instead of copying it (task-1074 Stage 1 validation).
var aitInventedValues = map[string]string{
	"invoice_number": "INV-9999",
	"issue_date":     "2026-01-31",
	"supplier_tin":   "12345678-0001",
	"supplier_name":  "GLOBACOM VENTURES LIMITED",
	"buyer_tin":      "87654321-0002",
	"buyer_name":     "INVENTED HOLDINGS LIMITED",
	"currency":       "USD",
	"subtotal":       "4321.00",
	"vat":            "222.00",
	"total":          "9999.00",
}

func TestAIText_EveryRuleRejectsAnInventedValue(t *testing.T) {
	pages := aitGoldenPages(t, "wild_scanned_no_number")
	if len(aitInventedValues) != len(HeaderFields) {
		t.Fatalf("invented values cover %d fields, want one per header field (%d)", len(aitInventedValues), len(HeaderFields))
	}

	if len(pages) == 0 || len(pages[0].Tokens) == 0 {
		t.Fatal("precondition failed: the golden replay produced no tokens, so every rejection below is vacuous")
	}

	for field, value := range aitInventedValues {
		// Precondition first: a rule that "rejects" a value already on the page proves nothing.
		if containsAny(pages, value) {
			t.Fatalf("invented %s value %q is not invented -- it occurs on the golden page", field, value)
		}
		// A shape-refused value is rejected by the guard, never by the page check this row pins.
		if shape, _ := tier1Shape(field); len(shape.Normalize(value)) == 0 {
			t.Fatalf("invented %s value %q is refused by its shape", field, value)
		}
		if found, _ := aitRuleWholeToken(field, value, pages); found {
			t.Errorf("rule A found the invented %s value %q", field, value)
		}
		if found, _ := aitRuleInLine(field, value, pages); found {
			t.Errorf("rule B found the invented %s value %q", field, value)
		}
		if found, _ := aitRuleJoinedRow(field, value, pages); found {
			t.Errorf("rule C found the invented %s value %q", field, value)
		}
	}
}

func TestAIText_EveryRuleRejectsATruncatedAmount(t *testing.T) {
	pages := aitGoldenPages(t, "wild_scanned_no_number")

	if found, _ := aitRuleWholeToken("total", "935.00", pages); found {
		t.Error("rule A accepted the truncated amount")
	}
	if found, _ := aitRuleInLine("total", "935.00", pages); found {
		t.Error("rule B accepted the truncated amount")
	}
	if found, _ := aitRuleJoinedRow("total", "935.00", pages); found {
		t.Error("rule C accepted the truncated amount")
	}
	if found, _ := aitRuleSubstringControl("total", "935.00", pages); !found {
		t.Error("the control did not accept the truncated amount -- this fixture cannot then prove a loose rule fails")
	}
}

func TestAIText_AShapeRefusedAnswerIsNeverFound(t *testing.T) {
	pages := aitGoldenPages(t, "wild_scanned_no_number")
	raw := "1,935.00 naira"
	if got := ShapeAmount.Normalize(raw); len(got) != 0 {
		t.Fatalf("precondition failed: ShapeAmount.Normalize(%q) = %v, want no reading", raw, got)
	}

	if found, _ := aitRuleWholeToken("total", raw, pages); found {
		t.Error("rule A found a shape-refused answer")
	}
	if found, _ := aitRuleInLine("total", raw, pages); found {
		t.Error("rule B found a shape-refused answer")
	}
	if found, _ := aitRuleJoinedRow("total", raw, pages); found {
		t.Error("rule C found a shape-refused answer")
	}
}

func TestAIText_InLineRuleAcceptsAPrintedPrefixOfAName(t *testing.T) {
	pages := aitGoldenPages(t, "wild_scanned_no_number")

	if found, _ := aitRuleWholeToken("buyer_name", "HONEYWELL", pages); found {
		t.Fatal("precondition failed: rule A already accepts a printed prefix of a name")
	}
	if found, _ := aitRuleInLine("buyer_name", "HONEYWELL", pages); !found {
		t.Error("rule B did not accept the printed prefix -- this pins a known false positive for the report to explain")
	}
}

func TestAIText_RulesNest(t *testing.T) {
	golden := aitGoldenPages(t, "wild_scanned_no_number")
	honeywellRow := onePage(1,
		tok("Honeywell", 1, 0.10, 0.300, 0.19, 0.312),
		tok("Group", 1, 0.20, 0.301, 0.26, 0.313),
	)
	inlineDate := onePage(1, tok("Ref 7781 dated 2026-06-11 terms 30 days", 1, 0.10, 0.40, 0.60, 0.42))

	cases := []struct {
		field, raw string
		pages      []TokenPage
	}{
		{"buyer_tin", "99999999-1202", golden},
		{"buyer_tin", "9999999-1202", golden},
		{"total", "935.00", golden},
		{"buyer_name", "HONEYWELL", golden},
		{"issue_date", "2026-06-11", inlineDate},
		{"buyer_name", "Honeywell Group", honeywellRow},
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		a, _ := aitRuleWholeToken(c.field, c.raw, c.pages)
		b, _ := aitRuleInLine(c.field, c.raw, c.pages)
		cc, _ := aitRuleJoinedRow(c.field, c.raw, c.pages)
		if a && !b {
			t.Errorf("%s %q: rule A found it but rule B did not -- B must be at least as broad", c.field, c.raw)
		}
		if b && !cc {
			t.Errorf("%s %q: rule B found it but rule C did not -- C must be at least as broad", c.field, c.raw)
		}
	}
}

// --- classification and agreement (AC 4) -------------------------------------------------

func TestAIText_ClassifiesAnAnswerAgainstTheKey(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	cases := []struct {
		name   string
		field  string
		answer *string
		key    []string
		want   string
	}{
		{"exact TIN match", "buyer_tin", strPtr("99999999-1202"), []string{"99999999-1202"}, "right"},
		{"dropped digit TIN", "buyer_tin", strPtr("9999999-1202"), []string{"99999999-1202"}, "wrong"},
		{"nil answer on a keyed field", "buyer_tin", nil, []string{"99999999-1202"}, "blank"},
		{"whitespace answer on a keyed field", "buyer_tin", strPtr("  "), []string{"99999999-1202"}, "blank"},
		{"nil answer on an empty key", "invoice_number", nil, []string{}, "right"},
		{"a value on an empty key", "invoice_number", strPtr("INV-1"), []string{}, "wrong"},
		{"an ambiguous date key, second reading", "issue_date", strPtr("2026-12-03"), []string{"2026-03-12", "2026-12-03"}, "right"},
		{"a comma amount against its digit key", "total", strPtr("1,935.00"), []string{"1935.00"}, "right"},
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		if got := aitClassify(c.field, c.answer, c.key); got != c.want {
			t.Errorf("%s: aitClassify(%s, %v, %v) = %q, want %q", c.name, c.field, c.answer, c.key, got, c.want)
		}
	}
}

func TestAIText_ClassifiesAgreement(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	cases := []struct {
		name       string
		ai, engine *string
		want       string
	}{
		{"comma vs bare digits", strPtr("1,935.00"), strPtr("1935.00"), "agree"},
		{"different amounts", strPtr("1800.00"), strPtr("1935.00"), "disagree"},
		{"only the AI read it", strPtr("1935.00"), nil, "ai_only"},
		{"only the engine read it", nil, strPtr("1935.00"), "engine_only"},
		{"neither read it", nil, nil, "neither"},
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		if got := aitAgreement("total", c.ai, c.engine); got != c.want {
			t.Errorf("%s: aitAgreement(total, %v, %v) = %q, want %q", c.name, c.ai, c.engine, got, c.want)
		}
	}
}

func TestAIText_CountsFoundAndAcceptedPerRule(t *testing.T) {
	rightPages := onePage(1,
		tok("Honeywell", 1, 0.10, 0.300, 0.19, 0.312),
		tok("Group", 1, 0.20, 0.301, 0.26, 0.313),
	)
	wrongPages := onePage(1, tok("9999.00", 1, 0.5, 0.5, 0.6, 0.52))
	blankPages := onePage(1, tok("VAT", 1, 0.1, 0.1, 0.2, 0.12))

	cells := []ruleCountCell{
		{Field: "buyer_name", Answer: "Honeywell Group", Verdict: "right", Pages: rightPages},
		{Field: "total", Answer: "9999.00", Verdict: "wrong", Pages: wrongPages},
		{Field: "vat", Answer: "", Verdict: "blank", Pages: blankPages},
	}
	if len(cells) == 0 {
		t.Fatal("no cells")
	}

	foundA, foundOfA, acceptedA, acceptedOfA := aitRuleCounts(aitRuleWholeToken, cells)
	if foundA != 0 || foundOfA != 1 {
		t.Errorf("rule A found = %d of %d, want 0 of 1", foundA, foundOfA)
	}
	if acceptedA != 1 || acceptedOfA != 1 {
		t.Errorf("rule A accepted = %d of %d, want 1 of 1", acceptedA, acceptedOfA)
	}

	foundC, foundOfC, acceptedC, acceptedOfC := aitRuleCounts(aitRuleJoinedRow, cells)
	if foundC != 1 || foundOfC != 1 {
		t.Errorf("rule C found = %d of %d, want 1 of 1", foundC, foundOfC)
	}
	if acceptedC != 1 || acceptedOfC != 1 {
		t.Errorf("rule C accepted = %d of %d, want 1 of 1", acceptedC, acceptedOfC)
	}
}

// --- scoring against the confirmed key (AC 3, 8) ------------------------------------------

func TestAIText_ScoresOnlyConfirmedKeyEntries(t *testing.T) {
	keyPath := writeJSONFile(t, "key.json", map[string]any{
		"confirmation": map[string]any{"source": "RALPH 0.6d", "answer_file": "key.answer.md", "recorded": "2026-09-17"},
		"doc.pdf": map[string]any{
			"set": "corpus", "source": "drafted",
			"fields": map[string]any{
				"total":          map[string]any{"values": []string{"1935.00"}, "confirmed": false},
				"invoice_number": map[string]any{"values": []string{"INV-1"}, "confirmed": true},
				"issue_date":     map[string]any{"values": []string{"2026-08-14"}, "confirmed": true},
			},
		},
	})
	key, err := aitLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aitLoadKey: %v", err)
	}
	dumps := []docDump{{File: "doc.pdf", Set: "corpus", TextChars: 100, Engine: map[string]string{
		"total": "1935.00", "invoice_number": "INV-1", "issue_date": "2026-08-14",
	}}}
	answers := []docAnswer{{File: "doc.pdf", Run: 1, Fields: map[string]string{
		"total": "1935.00", "invoice_number": "INV-1", "issue_date": "2026-08-14",
	}}}

	result := aitScoreConfirmed(key, dumps, answers, nil)

	if hasCellForField(result.Cells, "doc.pdf", "total") {
		t.Error("a cell was scored for the unconfirmed field total")
	}
	if !fieldRefListed(result.Unconfirmed, "doc.pdf", "total") {
		t.Errorf("total not listed under unconfirmed: %v", result.Unconfirmed)
	}
	for _, f := range []string{"invoice_number", "issue_date"} {
		if !hasCellForField(result.Cells, "doc.pdf", f) {
			t.Errorf("no cell for confirmed field %s", f)
		}
	}
}

func TestAIText_CountsATextlessDocumentWithoutScoringIt(t *testing.T) {
	keyPath := writeJSONFile(t, "key.json", map[string]any{
		"confirmation": map[string]any{"source": "RALPH 0.6d", "answer_file": "key.answer.md", "recorded": "2026-09-17"},
		"blank.pdf": map[string]any{
			"set": "corpus", "source": "drafted",
			"fields": map[string]any{
				"total": map[string]any{"values": []string{"1935.00"}, "confirmed": true},
			},
		},
	})
	key, err := aitLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aitLoadKey: %v", err)
	}
	dumps := []docDump{{File: "blank.pdf", Set: "corpus", TextChars: 0, Engine: map[string]string{}}}

	result := aitScoreConfirmed(key, dumps, nil, nil)
	if got := len(result.Cells); got != 0 {
		t.Errorf("cells = %d, want 0 for a textless document", got)
	}
	if !containsString(result.Textless, "blank.pdf") {
		t.Errorf("blank.pdf not listed under textless: %v", result.Textless)
	}
}

func TestAIText_ListsNotReadDocumentsApartFromTextless(t *testing.T) {
	keyPath := writeJSONFile(t, "key.json", map[string]any{
		"confirmation": map[string]any{"source": "RALPH 0.6d", "answer_file": "key.answer.md", "recorded": "2026-09-17"},
	})
	key, err := aitLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aitLoadKey: %v", err)
	}
	dumps := []docDump{{File: "textless.pdf", Set: "user", TextChars: 0, Engine: map[string]string{}}}
	notScored := []notScoredEntry{{File: "scan.pdf", Reason: "image-only, no committed Docling response"}}

	result := aitScoreConfirmed(key, dumps, nil, notScored)

	if got := len(result.Cells); got != 0 {
		t.Errorf("cells = %d, want 0", got)
	}
	if !notScoredContains(result.NotRead, notScored[0]) {
		t.Errorf("not_read = %v, want it to hold %+v", result.NotRead, notScored[0])
	}
	if !containsString(result.Textless, "textless.pdf") {
		t.Errorf("textless = %v, want it to hold textless.pdf", result.Textless)
	}
	if containsString(result.Textless, "scan.pdf") {
		t.Error("textless wrongly holds the not-read document scan.pdf")
	}
	if notScoredHasFile(result.NotRead, "textless.pdf") {
		t.Error("not_read wrongly holds the textless document")
	}
}

// --- percentiles and rule selection (AC 6, 7) ---------------------------------------------

func TestAIText_PercentilesUseNearestRank(t *testing.T) {
	if p50, p90, ok := aitPercentile([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}); !ok || p50 != 6 || p90 != 10 {
		t.Errorf("aitPercentile(1..10) = (%v, %v, %v), want (6, 10, true)", p50, p90, ok)
	}
	if p50, p90, ok := aitPercentile([]float64{4.2}); !ok || p50 != 4.2 || p90 != 4.2 {
		t.Errorf("aitPercentile([4.2]) = (%v, %v, %v), want (4.2, 4.2, true)", p50, p90, ok)
	}
	if _, _, ok := aitPercentile(nil); ok {
		t.Error("aitPercentile(nil) ok = true, want false")
	}
}

func TestAIText_PicksTheBroadestRuleThatAcceptsNoExtraWrongValue(t *testing.T) {
	allEligible := []ruleStat{
		{Name: "A", Found: 10, Accepted: 1, Eligible: true},
		{Name: "B", Found: 12, Accepted: 1, Eligible: true},
		{Name: "C", Found: 13, Accepted: 2, Eligible: true},
	}
	if got := aitPickRule(allEligible); got != "B" {
		t.Errorf("aitPickRule(all eligible) = %q, want B", got)
	}

	bFailed := []ruleStat{
		{Name: "A", Found: 10, Accepted: 1, Eligible: true},
		{Name: "B", Found: 12, Accepted: 1, Eligible: false},
		{Name: "C", Found: 13, Accepted: 2, Eligible: true},
	}
	if got := aitPickRule(bFailed); got != "A" {
		t.Errorf("aitPickRule(B adversarial-failed) = %q, want A", got)
	}

	tie := []ruleStat{
		{Name: "A", Found: 10, Accepted: 1, Eligible: true},
		{Name: "B", Found: 10, Accepted: 1, Eligible: true},
	}
	if got := aitPickRule(tie); got != "A" {
		t.Errorf("aitPickRule(tie) = %q, want the simpler rule A", got)
	}
}

// --- prompt rendering (AC 1) ---------------------------------------------------------------

func TestAIText_PromptLinesAreOneLinePerDoclingToken(t *testing.T) {
	pages := []TokenPage{
		{Number: 1, Tokens: []Token{
			tok("second", 1, 0.10, 0.5, 0.30, 0.52), // lower on the page, listed first: out of y order
			tok("first", 1, 0.15, 0.1, 0.35, 0.12),
		}},
		{Number: 2, Tokens: []Token{
			tok("third", 2, 0.20, 0.2, 0.40, 0.22),
		}},
	}

	lines := aitPromptLines(pages)
	want := []struct {
		page int
		text string
		y, x float64
	}{
		{1, "second", 0.5, 0.10},
		{1, "first", 0.1, 0.15},
		{2, "third", 0.2, 0.20},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %d, want %d (one per token, in reader order)", len(lines), len(want))
	}
	for i, w := range want {
		l := lines[i]
		if l.Page != w.page || l.Y != w.y {
			t.Errorf("line %d: page=%d y=%v, want page=%d y=%v -- reader order must not be re-sorted", i, l.Page, l.Y, w.page, w.y)
		}
		if len(l.Segments) != 1 || l.Segments[0].Text != w.text || l.Segments[0].X != w.x {
			t.Errorf("line %d segments = %+v, want one segment {x: %v, text: %q}", i, l.Segments, w.x, w.text)
		}
	}
}

// --- key and answer loading (AC 2, 3) -------------------------------------------------------

func TestAIText_RefusesAKeyWithoutAConfirmationMarker(t *testing.T) {
	noMarker := writeJSONFile(t, "key.json", map[string]any{
		"doc.pdf": map[string]any{"set": "corpus", "fields": map[string]any{}},
	})
	if _, err := aitLoadKey(noMarker); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Errorf("aitLoadKey(no confirmation) err = %v, want an error naming confirmation", err)
	}

	emptyAnswerFile := writeJSONFile(t, "key.json", map[string]any{
		"confirmation": map[string]any{"source": "RALPH 0.6d", "answer_file": "", "recorded": "2026-09-17"},
		"doc.pdf":      map[string]any{"set": "corpus", "fields": map[string]any{}},
	})
	if _, err := aitLoadKey(emptyAnswerFile); err == nil || !strings.Contains(err.Error(), "answer_file") {
		t.Errorf("aitLoadKey(empty answer_file) err = %v, want an error naming answer_file", err)
	}

	complete := writeJSONFile(t, "key.json", map[string]any{
		"confirmation": map[string]any{"source": "RALPH 0.6d", "answer_file": "key.answer.md", "recorded": "2026-09-17"},
		"doc.pdf":      map[string]any{"set": "corpus", "fields": map[string]any{}},
	})
	if _, err := aitLoadKey(complete); err != nil {
		t.Errorf("aitLoadKey(complete marker): %v, want nil", err)
	}
}

func TestAIText_KeyCorpusRowsEqualTheCommittedTable(t *testing.T) {
	corpusFields := func(total string) map[string]any {
		return map[string]any{
			"invoice_number": map[string]any{"values": []string{"INV-100"}, "confirmed": true},
			"issue_date":     map[string]any{"values": []string{"2026-03-01"}, "confirmed": true},
			"buyer_tin":      map[string]any{"values": []string{"11111111-0001"}, "confirmed": true},
			"buyer_name":     map[string]any{"values": []string{"BUYER LIMITED"}, "confirmed": true},
			"currency":       map[string]any{"values": []string{"NGN"}, "confirmed": true},
			"subtotal":       map[string]any{"values": []string{"900.00"}, "confirmed": true},
			"vat":            map[string]any{"values": []string{"67.50"}, "confirmed": true},
			"total":          map[string]any{"values": []string{total}, "confirmed": true},
		}
	}
	corpusKeyRow := func(total string) map[string]any {
		return map[string]any{
			"invoice_number": []string{"INV-100"}, "issue_date": []string{"2026-03-01"},
			"buyer_tin": []string{"11111111-0001"}, "buyer_name": []string{"BUYER LIMITED"},
			"currency": []string{"NGN"}, "subtotal": []string{"900.00"}, "vat": []string{"67.50"}, "total": []string{total},
		}
	}
	keyPath := writeJSONFile(t, "key.json", map[string]any{
		"confirmation":             map[string]any{"source": "RALPH 0.6d", "answer_file": "key.answer.md", "recorded": "2026-09-17"},
		"corpus_inline_labels.pdf": map[string]any{"set": "corpus", "source": "expectByLayout", "fields": corpusFields("967.50")},
		"corpus_totals_block.pdf":  map[string]any{"set": "corpus", "source": "expectByLayout", "fields": corpusFields("967.50")},
	})
	key, err := aitLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aitLoadKey: %v", err)
	}

	equalCorpusKey := writeJSONFile(t, "corpus_key.json", map[string]any{
		"corpus_inline_labels.pdf": corpusKeyRow("967.50"),
		"corpus_totals_block.pdf":  corpusKeyRow("967.50"),
	})
	if err := aitCheckCorpusRows(key, equalCorpusKey); err != nil {
		t.Errorf("aitCheckCorpusRows(equal rows): %v, want nil", err)
	}

	changedCorpusKey := writeJSONFile(t, "corpus_key.json", map[string]any{
		"corpus_inline_labels.pdf": corpusKeyRow("1.00"), // total disagrees with the key's 967.50
		"corpus_totals_block.pdf":  corpusKeyRow("967.50"),
	})
	if err := aitCheckCorpusRows(key, changedCorpusKey); err == nil ||
		!strings.Contains(err.Error(), "corpus_inline_labels.pdf") || !strings.Contains(err.Error(), "total") {
		t.Errorf("aitCheckCorpusRows(changed total) err = %v, want it to name corpus_inline_labels.pdf and total", err)
	}

	missingRowCorpusKey := writeJSONFile(t, "corpus_key.json", map[string]any{
		"corpus_totals_block.pdf": corpusKeyRow("967.50"),
	})
	if err := aitCheckCorpusRows(key, missingRowCorpusKey); err == nil ||
		!strings.Contains(err.Error(), "corpus_inline_labels.pdf") {
		t.Errorf("aitCheckCorpusRows(missing row) err = %v, want it to name corpus_inline_labels.pdf", err)
	}
}

func TestAIText_ErroredOrFieldlessAnswersNeverScore(t *testing.T) {
	good := `{"file":"doc.pdf","run":1,"model":"google/gemini-3.5-flash-lite","fields":{"total":"1935.00"}}`
	errored := `{"file":"doc.pdf","run":2,"model":"google/gemini-3.5-flash-lite","error":"rate_limited"}`
	nullFields := `{"file":"doc.pdf","run":3,"model":"google/gemini-3.5-flash-lite","fields":null}`
	truncated := `{"file":"doc.pdf","run":4,"fields":{"total":"1935.00"`

	fourLines := strings.Join([]string{good, errored, nullFields, truncated}, "\n") + "\n"
	path4 := filepath.Join(t.TempDir(), "answers.jsonl")
	if err := os.WriteFile(path4, []byte(fourLines), 0o644); err != nil {
		t.Fatalf("write answers.jsonl: %v", err)
	}
	if _, _, err := aitLoadAnswers(path4); err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("aitLoadAnswers(truncated 4th line) err = %v, want an error naming line 4", err)
	}

	threeLines := strings.Join([]string{good, errored, nullFields}, "\n") + "\n"
	path3 := filepath.Join(t.TempDir(), "answers.jsonl")
	if err := os.WriteFile(path3, []byte(threeLines), 0o644); err != nil {
		t.Fatalf("write answers.jsonl: %v", err)
	}
	goodRecords, errCount, err := aitLoadAnswers(path3)
	if err != nil {
		t.Fatalf("aitLoadAnswers(3 lines): %v, want nil", err)
	}
	if got := len(goodRecords); got != 1 {
		t.Fatalf("good records = %d, want 1", got)
	}
	if errCount != 2 {
		t.Errorf("errors = %d, want 2", errCount)
	}

	keyPath := writeJSONFile(t, "key.json", map[string]any{
		"confirmation": map[string]any{"source": "RALPH 0.6d", "answer_file": "key.answer.md", "recorded": "2026-09-17"},
		"doc.pdf": map[string]any{
			"set": "corpus", "source": "drafted",
			"fields": map[string]any{
				"total": map[string]any{"values": []string{"1935.00"}, "confirmed": true},
			},
		},
	})
	key, err := aitLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aitLoadKey: %v", err)
	}
	dumps := []docDump{{File: "doc.pdf", Set: "corpus", TextChars: 100, Engine: map[string]string{"total": "1935.00"}}}

	result := aitScoreConfirmed(key, dumps, goodRecords, nil)
	if !hasCellForField(result.Cells, "doc.pdf", "total") {
		t.Error("no cell for the one good record's field")
	}
	if got := len(result.Cells); got != 1 {
		t.Errorf("cells = %d, want exactly 1 (the good record only)", got)
	}
}

// --- QA defect pins (red until the helpers are fixed) ---------------------------------------

// D1: expectByLayout (endtoend/score_test.go) holds empty cells and a two-reading date; key.draft.json rows carry them as values lists.
func TestAIText_CorpusRowsAcceptTheCommittedTableShape(t *testing.T) {
	fields := map[string][]string{
		"invoice_number": {"INV-1005"}, "issue_date": {"2026-03-12", "2026-12-03"},
		"buyer_tin": {}, "buyer_name": {}, "currency": {"NGN"},
		"subtotal": {}, "vat": {}, "total": {"4300.00"},
	}
	keyFields := map[string]aitKeyField{}
	for f, v := range fields {
		keyFields[f] = aitKeyField{Values: v, Confirmed: true}
	}
	key := aitKey{Docs: map[string]aitKeyDoc{
		"corpus_ambiguous_date.pdf": {Set: "corpus", Source: "expectByLayout", Fields: keyFields},
	}}
	corpusKey := writeJSONFile(t, "corpus_key.json", map[string]any{"corpus_ambiguous_date.pdf": fields})

	if err := aitCheckCorpusRows(key, corpusKey); err != nil {
		t.Errorf("aitCheckCorpusRows(key equal to the committed row) = %v, want nil", err)
	}
}

// D1: a key row must hold every committed field (expectByLayout writes 8 per layout); a dropped one goes unscored.
func TestAIText_CorpusRowsRefuseAKeyThatDropsAField(t *testing.T) {
	committed := map[string][]string{
		"invoice_number": {"INV-1001"}, "issue_date": {"2026-03-04"},
		"buyer_tin": {"99999999-0102"}, "buyer_name": {"Honeywell Group"}, "currency": {"NGN"},
		"subtotal": {"1000.00"}, "vat": {"75.00"}, "total": {"1075.00"},
	}
	keyFields := map[string]aitKeyField{}
	for f, v := range committed {
		if f != "total" {
			keyFields[f] = aitKeyField{Values: v, Confirmed: true}
		}
	}
	key := aitKey{Docs: map[string]aitKeyDoc{
		"corpus_inline_labels.pdf": {Set: "corpus", Source: "expectByLayout", Fields: keyFields},
	}}
	corpusKey := writeJSONFile(t, "corpus_key.json", map[string]any{"corpus_inline_labels.pdf": committed})

	err := aitCheckCorpusRows(key, corpusKey)
	if err == nil || !strings.Contains(err.Error(), "corpus_inline_labels.pdf") || !strings.Contains(err.Error(), "total") {
		t.Errorf("aitCheckCorpusRows(key without total) err = %v, want an error naming corpus_inline_labels.pdf and total", err)
	}
}

// D2: task-1073 Stage 1 validation - row(t) is the tokens overlapping seed t itself, never a chain.
func TestAIText_JoinedRowRuleDoesNotChainOverlapsIntoOneRow(t *testing.T) {
	pages := onePage(1,
		tok("Honeywell", 1, 0.10, 0.300, 0.19, 0.310),
		tok("xx", 1, 0.80, 0.305, 0.85, 0.315),
		tok("yy", 1, 0.86, 0.310, 0.90, 0.320),
		tok("Group", 1, 0.20, 0.315, 0.26, 0.325),
	)
	if found, box := aitRuleJoinedRow("buyer_name", "Honeywell Group", pages); found {
		t.Errorf("rule C joined Honeywell and Group through a chain of overlaps (box %+v); neither overlaps the other", box)
	}
}

// D3: cells.json is one row per document x run x field (Stage E), built from good answer records, so
// Core AC 4 and D-A11 report per-run totals whose denominator is the good-record count.
func TestAIText_ScoresOneCellPerDocumentRunAndField(t *testing.T) {
	key := aitKey{Docs: map[string]aitKeyDoc{
		"doc.pdf": {Fields: map[string]aitKeyField{
			"total":          {Values: []string{"1935.00"}, Confirmed: true},
			"invoice_number": {Values: []string{"INV-1"}, Confirmed: true},
		}},
		"unanswered.pdf": {Fields: map[string]aitKeyField{
			"total": {Values: []string{"1.00"}, Confirmed: true},
		}},
	}}
	dumps := []docDump{{File: "doc.pdf", TextChars: 100}, {File: "unanswered.pdf", TextChars: 100}}
	answers := []docAnswer{
		{File: "doc.pdf", Run: 1, Fields: map[string]string{"total": "1,935.00", "invoice_number": "INV-1"}},
		{File: "doc.pdf", Run: 2, Fields: map[string]string{"total": "1800.00", "invoice_number": "INV-1"}},
		{File: "doc.pdf", Run: 3, Fields: map[string]string{"invoice_number": "INV-1"}},
	}

	result := aitScoreConfirmed(key, dumps, answers, nil)

	if got := len(result.Cells); got != 6 {
		t.Errorf("cells = %d, want 6 (3 good runs x 2 confirmed fields, none for a document with no answer)", got)
	}
	want := map[int]string{1: "right", 2: "wrong", 3: "blank"}
	for run, verdict := range want {
		n := 0
		for _, c := range result.Cells {
			if c.File == "doc.pdf" && c.Field == "total" && c.Run == run {
				n++
				if c.AI != verdict {
					t.Errorf("run %d total AI = %q, want %q", run, c.AI, verdict)
				}
			}
		}
		if n != 1 {
			t.Errorf("run %d total cells = %d, want 1", run, n)
		}
	}
}

// D4: D-A15 - a value the shape refuses is wrong, not blank, even when the key holds the same text.
func TestAIText_AShapeRefusedAnswerIsWrongEvenAgainstAnEqualKey(t *testing.T) {
	cases := []struct{ field, value string }{
		{"buyer_tin", "1234567-0001"},
		{"total", "1,935.00 naira"},
	}
	for _, c := range cases {
		if shape, _ := tier1Shape(c.field); len(shape.Normalize(c.value)) != 0 {
			t.Fatalf("precondition failed: %s shape accepts %q", c.field, c.value)
		}
		v := c.value
		if got := aitClassify(c.field, &v, []string{c.value}); got != "wrong" {
			t.Errorf("aitClassify(%s, %q, [%q]) = %q, want wrong", c.field, c.value, c.value, got)
		}
	}
}

// aitCorpusKeyFixture is one committed corpus row plus a key that transcribes it exactly.
func aitCorpusKeyFixture(t *testing.T) (map[string][]string, map[string]aitKeyField, string) {
	t.Helper()
	committed := map[string][]string{
		"invoice_number": {"INV-1005"}, "issue_date": {"2026-03-12", "2026-12-03"},
		"buyer_tin": {}, "buyer_name": {}, "currency": {"NGN"},
		"subtotal": {}, "vat": {}, "total": {"4300.00"},
	}
	keyFields := map[string]aitKeyField{}
	for f, v := range committed {
		keyFields[f] = aitKeyField{Values: v, Confirmed: true}
	}
	path := writeJSONFile(t, "corpus_key.json", map[string]any{
		"corpus_ambiguous_date.pdf": committed,
		"corpus_inline_labels.pdf":  committed,
	})
	return committed, keyFields, path
}

// D1: an extra reading on a committed cell would score an AI value the table never held.
func TestAIText_CorpusRowsRefuseAKeyWithAnExtraReading(t *testing.T) {
	_, keyFields, path := aitCorpusKeyFixture(t)
	widened := map[string]aitKeyField{}
	for f, v := range keyFields {
		widened[f] = v
	}
	widened["total"] = aitKeyField{Values: []string{"4300.00", "9999.00"}, Confirmed: true}
	key := aitKey{Docs: map[string]aitKeyDoc{
		"corpus_ambiguous_date.pdf": {Set: "corpus", Source: "expectByLayout", Fields: widened},
		"corpus_inline_labels.pdf":  {Set: "corpus", Source: "expectByLayout", Fields: keyFields},
	}}
	err := aitCheckCorpusRows(key, path)
	if err == nil || !strings.Contains(err.Error(), "corpus_ambiguous_date.pdf") || !strings.Contains(err.Error(), "total") {
		t.Errorf("aitCheckCorpusRows(extra total reading) err = %v, want an error naming corpus_ambiguous_date.pdf and total", err)
	}
}

// D1: D-A07 and Stage C - corpus rows carry the 8 written fields only, no supplier values.
func TestAIText_CorpusRowsRefuseAKeyThatAddsAField(t *testing.T) {
	_, keyFields, path := aitCorpusKeyFixture(t)
	added := map[string]aitKeyField{}
	for f, v := range keyFields {
		added[f] = v
	}
	added["supplier_name"] = aitKeyField{Values: []string{"INVENTED SUPPLIER LIMITED"}, Confirmed: true}
	key := aitKey{Docs: map[string]aitKeyDoc{
		"corpus_ambiguous_date.pdf": {Set: "corpus", Source: "expectByLayout", Fields: added},
		"corpus_inline_labels.pdf":  {Set: "corpus", Source: "expectByLayout", Fields: keyFields},
	}}
	err := aitCheckCorpusRows(key, path)
	if err == nil || !strings.Contains(err.Error(), "corpus_ambiguous_date.pdf") || !strings.Contains(err.Error(), "supplier_name") {
		t.Errorf("aitCheckCorpusRows(key adds supplier_name) err = %v, want an error naming corpus_ambiguous_date.pdf and supplier_name", err)
	}
}

// D1: Stage E step 2 and D-A08 - the key holds every committed corpus row; a dropped layout goes unscored.
func TestAIText_CorpusRowsRefuseAKeyThatOmitsACorpusLayout(t *testing.T) {
	_, keyFields, path := aitCorpusKeyFixture(t)
	key := aitKey{Docs: map[string]aitKeyDoc{
		"corpus_ambiguous_date.pdf": {Set: "corpus", Source: "expectByLayout", Fields: keyFields},
		"user_invoice.pdf":          {Set: "user", Source: "drafted", Fields: keyFields},
	}}
	err := aitCheckCorpusRows(key, path)
	if err == nil || !strings.Contains(err.Error(), "corpus_inline_labels.pdf") {
		t.Errorf("aitCheckCorpusRows(key omits corpus_inline_labels.pdf) err = %v, want an error naming that layout", err)
	}
}

// D1: a missing key field is not an empty one - dropping a committed [] cell loses a scored blank (D-A15).
func TestAIText_CorpusRowsRefuseAKeyThatDropsAnEmptyField(t *testing.T) {
	committed, keyFields, path := aitCorpusKeyFixture(t)
	if v, ok := committed["vat"]; !ok || len(v) != 0 {
		t.Fatalf("precondition failed: committed vat = %v, want present and empty", v)
	}
	dropped := map[string]aitKeyField{}
	for f, v := range keyFields {
		if f != "vat" {
			dropped[f] = v
		}
	}
	key := aitKey{Docs: map[string]aitKeyDoc{
		"corpus_ambiguous_date.pdf": {Set: "corpus", Source: "expectByLayout", Fields: dropped},
		"corpus_inline_labels.pdf":  {Set: "corpus", Source: "expectByLayout", Fields: keyFields},
	}}
	err := aitCheckCorpusRows(key, path)
	if err == nil || !strings.Contains(err.Error(), "corpus_ambiguous_date.pdf") || !strings.Contains(err.Error(), "vat") {
		t.Errorf("aitCheckCorpusRows(key drops empty vat) err = %v, want an error naming corpus_ambiguous_date.pdf and vat", err)
	}
}

// D1: D-A07 - a field outside the committed row is refused even when its value list is empty.
func TestAIText_CorpusRowsRefuseAKeyThatAddsAnEmptyField(t *testing.T) {
	_, keyFields, path := aitCorpusKeyFixture(t)
	added := map[string]aitKeyField{}
	for f, v := range keyFields {
		added[f] = v
	}
	added["supplier_tin"] = aitKeyField{Values: []string{}, Confirmed: true}
	key := aitKey{Docs: map[string]aitKeyDoc{
		"corpus_ambiguous_date.pdf": {Set: "corpus", Source: "expectByLayout", Fields: added},
		"corpus_inline_labels.pdf":  {Set: "corpus", Source: "expectByLayout", Fields: keyFields},
	}}
	err := aitCheckCorpusRows(key, path)
	if err == nil || !strings.Contains(err.Error(), "corpus_ambiguous_date.pdf") || !strings.Contains(err.Error(), "supplier_tin") {
		t.Errorf("aitCheckCorpusRows(key adds empty supplier_tin) err = %v, want an error naming corpus_ambiguous_date.pdf and supplier_tin", err)
	}
}

// D1: D-A08 - an omitted layout is refused even when every committed cell is empty.
func TestAIText_CorpusRowsRefuseAKeyThatOmitsAnAllEmptyLayout(t *testing.T) {
	_, keyFields, _ := aitCorpusKeyFixture(t)
	allEmpty := map[string][]string{
		"invoice_number": {}, "issue_date": {}, "buyer_tin": {}, "buyer_name": {},
		"currency": {}, "subtotal": {}, "vat": {}, "total": {},
	}
	path := writeJSONFile(t, "corpus_key_all_empty.json", map[string]any{
		"corpus_ambiguous_date.pdf": map[string][]string{
			"invoice_number": {"INV-1005"}, "issue_date": {"2026-03-12", "2026-12-03"},
			"buyer_tin": {}, "buyer_name": {}, "currency": {"NGN"},
			"subtotal": {}, "vat": {}, "total": {"4300.00"},
		},
		"corpus_blank_layout.pdf": allEmpty,
	})
	key := aitKey{Docs: map[string]aitKeyDoc{
		"corpus_ambiguous_date.pdf": {Set: "corpus", Source: "expectByLayout", Fields: keyFields},
	}}
	err := aitCheckCorpusRows(key, path)
	if err == nil || !strings.Contains(err.Error(), "corpus_blank_layout.pdf") {
		t.Errorf("aitCheckCorpusRows(key omits all-empty corpus_blank_layout.pdf) err = %v, want an error naming that layout", err)
	}
}
