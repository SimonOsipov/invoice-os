// aitext_internal_test.go: AIR-01-01's page-check rules and scoring helpers, driving the RED
// specs in aitext_rules_internal_test.go green. Pure logic only, no network, no test skips.
package extraction

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// aitGoldenPages replays one committed Docling golden through the real DoclingReader --
// production's own reader, not a hand-rolled decode of the fixture JSON.
func aitGoldenPages(t *testing.T, name string) []TokenPage {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", name+".docling.json"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	r, err := NewDoclingReader(srv.URL)
	if err != nil {
		t.Fatalf("NewDoclingReader(%q): %v", srv.URL, err)
	}

	var pages []TokenPage
	if _, err := r.Read(t.Context(), Document{ContentType: "application/pdf"}, CollectTokens(&pages)); err != nil {
		t.Fatalf("replay golden %s: %v", name, err)
	}
	return pages
}

// --- page-check rules (Core AC 5, 6) -------------------------------------------------------

// aitRuleWholeToken is today's production rule: some token carries the value whole, or as the
// remainder after a label (tokenCarries does both). Box is that token's own line region.
func aitRuleWholeToken(field, raw string, pages []TokenPage) (bool, Region) {
	shape, ok := tier1Shape(field)
	if !ok {
		return false, Region{}
	}
	want := shape.Normalize(raw)
	if len(want) == 0 {
		return false, Region{}
	}
	for _, p := range pages {
		for _, tok := range p.Tokens {
			if tokenCarries(shape, want, tok.Text) {
				return true, tok.Region
			}
		}
	}
	return false, Region{}
}

// aitRuleInLine broadens rule A to any run of consecutive words inside one token's own text --
// Docling's "line". Box stays the whole line: Docling returns no sub-line box.
func aitRuleInLine(field, raw string, pages []TokenPage) (bool, Region) {
	if found, box := aitRuleWholeToken(field, raw, pages); found {
		return true, box
	}
	shape, ok := tier1Shape(field)
	if !ok {
		return false, Region{}
	}
	want := shape.Normalize(raw)
	if len(want) == 0 {
		return false, Region{}
	}
	for _, p := range pages {
		for _, tok := range p.Tokens {
			words := strings.Fields(tok.Text)
			for i := range words {
				for j := i + 1; j <= len(words); j++ {
					run := strings.Join(words[i:j], " ")
					if tokenCarries(shape, want, run) {
						return true, tok.Region
					}
				}
			}
		}
	}
	return false, Region{}
}

// aitRuleJoinedRow broadens rule B to a run of words spanning several tokens on one row.
// Box is the union of the line cells the matched words came from, never the whole row: on a
// two-column row that would drag in the neighbouring column.
func aitRuleJoinedRow(field, raw string, pages []TokenPage) (bool, Region) {
	if found, box := aitRuleInLine(field, raw, pages); found {
		return true, box
	}
	shape, ok := tier1Shape(field)
	if !ok {
		return false, Region{}
	}
	want := shape.Normalize(raw)
	if len(want) == 0 {
		return false, Region{}
	}

	type wordSpan struct {
		text string
		tok  int // index into the row slice
	}

	for _, p := range pages {
		for _, row := range aitGroupRows(p.Tokens) {
			if len(row) < 2 {
				continue // a lone token is already covered by aitRuleInLine
			}
			var words []wordSpan
			for ti, tk := range row {
				for _, w := range strings.Fields(tk.Text) {
					words = append(words, wordSpan{text: w, tok: ti})
				}
			}
			for i := range words {
				for j := i + 1; j <= len(words); j++ {
					if words[i].tok == words[j-1].tok {
						continue // a same-token run was already tried by aitRuleInLine
					}
					parts := make([]string, 0, j-i)
					for _, w := range words[i:j] {
						parts = append(parts, w.text)
					}
					if !tokenCarries(shape, want, strings.Join(parts, " ")) {
						continue
					}
					box := row[words[i].tok].Region
					for k := words[i].tok + 1; k <= words[j-1].tok; k++ {
						box = unionRegion(box, row[k].Region)
					}
					return true, box
				}
			}
		}
	}
	return false, Region{}
}

// aitGroupRows clusters same-page tokens into rows: two tokens share a row when their vertical
// extents overlap by at least half the smaller height. Sorted by X0 within each row.
func aitGroupRows(tokens []Token) [][]Token {
	n := len(tokens)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if aitSameRow(tokens[i].Region, tokens[j].Region) {
				ri, rj := find(i), find(j)
				if ri != rj {
					parent[ri] = rj
				}
			}
		}
	}

	groups := map[int][]Token{}
	for i, tk := range tokens {
		r := find(i)
		groups[r] = append(groups[r], tk)
	}
	rows := make([][]Token, 0, len(groups))
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool { return g[i].Region.X0 < g[j].Region.X0 })
		rows = append(rows, g)
	}
	return rows
}

func aitSameRow(a, b Region) bool {
	minH := math.Min(a.Y1-a.Y0, b.Y1-b.Y0)
	if minH <= 0 {
		return false
	}
	top, bottom := math.Max(a.Y0, b.Y0), math.Min(a.Y1, b.Y1)
	if bottom <= top {
		return false
	}
	return bottom-top >= 0.5*minH
}

// aitRuleSubstringControl is the earlier harness's loose fallback (aimtGrounded's substring
// arm) -- never a page-check candidate, kept only to prove the adversarial fixtures can fail a
// loose rule.
func aitRuleSubstringControl(field, raw string, pages []TokenPage) (bool, Region) {
	readings := aitReadings(field, raw)
	if len(readings) == 0 {
		readings = []string{aitLoose(raw)}
	}
	for _, p := range pages {
		for _, tok := range p.Tokens {
			loose := aitLoose(tok.Text)
			for _, r := range readings {
				if len(r) >= 3 && strings.Contains(loose, aitLoose(r)) {
					return true, Region{}
				}
			}
		}
	}
	return false, Region{}
}

func aitLoose(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// aitReadings normalises raw under field's shape, falling back to its loosened text when the
// shape refuses it (a blank raw has no reading at all).
func aitReadings(field, raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if shape, ok := tier1Shape(field); ok {
		if r := shape.Normalize(raw); len(r) > 0 {
			return r
		}
	}
	return []string{aitLoose(raw)}
}

// --- classification and agreement (Core AC 4) -----------------------------------------------

// aitClassify scores one answer against the key's acceptable readings: right, wrong or blank.
func aitClassify(field string, answer *string, key []string) string {
	blank := answer == nil || strings.TrimSpace(*answer) == ""
	if blank {
		if len(key) == 0 {
			return "right"
		}
		return "blank"
	}
	answerReadings := aitReadings(field, *answer)
	for _, k := range key {
		if hasCommonReading(answerReadings, aitReadings(field, k)) {
			return "right"
		}
	}
	return "wrong"
}

func hasCommonReading(a, b []string) bool {
	for _, r := range a {
		if containsString(b, r) {
			return true
		}
	}
	return false
}

// aitAgreement classifies the AI/engine pair into the five classes that cover every cell with
// no overlap.
func aitAgreement(field string, ai, engine *string) string {
	aiBlank := ai == nil || strings.TrimSpace(*ai) == ""
	engineBlank := engine == nil || strings.TrimSpace(*engine) == ""

	switch {
	case aiBlank && engineBlank:
		return "neither"
	case aiBlank:
		return "engine_only"
	case engineBlank:
		return "ai_only"
	}
	if hasCommonReading(aitReadings(field, *ai), aitReadings(field, *engine)) {
		return "agree"
	}
	return "disagree"
}

// aitRuleCounts runs rule over cells, returning (found, foundOf, accepted, acceptedOf): found is
// how many AI-right cells the rule locates on the page, accepted is how many AI-wrong cells it
// wrongly locates. A blank answer enters neither count.
func aitRuleCounts(rule func(field, raw string, pages []TokenPage) (bool, Region), cells []ruleCountCell) (found, foundOf, accepted, acceptedOf int) {
	for _, c := range cells {
		switch c.Verdict {
		case "right":
			foundOf++
			if ok, _ := rule(c.Field, c.Answer, c.Pages); ok {
				found++
			}
		case "wrong":
			acceptedOf++
			if ok, _ := rule(c.Field, c.Answer, c.Pages); ok {
				accepted++
			}
		}
	}
	return
}

// --- scoring against the confirmed key (Core AC 3, 8) -----------------------------------------

type aitScoreResult struct {
	Cells       []cellFixture
	Unconfirmed []fieldRef
	Textless    []string
	NotRead     []notScoredEntry
}

// aitKeyField is one field's acceptable readings and whether the user confirmed them.
type aitKeyField struct {
	Values    []string
	Confirmed bool
}

// aitKeyDoc is one document's key row.
type aitKeyDoc struct {
	Set    string
	Source string
	Fields map[string]aitKeyField
}

// aitKey is the loaded, confirmation-gated answer key.
type aitKey struct {
	Docs map[string]aitKeyDoc
}

// aitScoreConfirmed lists which (document, field) cells are eligible to score: a textless
// document (Core AC 8) or a not-read one scores nothing, and an unconfirmed field is listed but
// not scored (Core AC 3). answers is accepted for the caller's future classification pass; this
// stage only decides eligibility.
func aitScoreConfirmed(key aitKey, dumps []docDump, answers []docAnswer, notScored []notScoredEntry) aitScoreResult {
	_ = answers
	result := aitScoreResult{NotRead: append([]notScoredEntry(nil), notScored...)}

	for _, d := range dumps {
		if d.TextChars == 0 {
			result.Textless = append(result.Textless, d.File)
			continue
		}
		doc, ok := key.Docs[d.File]
		if !ok {
			continue
		}
		fields := make([]string, 0, len(doc.Fields))
		for f := range doc.Fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, field := range fields {
			if doc.Fields[field].Confirmed {
				result.Cells = append(result.Cells, cellFixture{File: d.File, Field: field})
			} else {
				result.Unconfirmed = append(result.Unconfirmed, fieldRef{File: d.File, Field: field})
			}
		}
	}
	return result
}

// --- percentiles and rule selection (Core AC 6, 7) --------------------------------------------

// aitPercentile is nearest-rank over a sorted copy of vals, the EXTR-00 aggregator's definition.
func aitPercentile(vals []float64) (p50, p90 float64, ok bool) {
	if len(vals) == 0 {
		return 0, 0, false
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	return s[len(s)/2], s[int(float64(len(s))*0.9)], true
}

// aitPickRule applies the fixed selection criterion (D-A14): eligible, no more wrong-accepted
// than rule A, most found, simplest on a tie.
func aitPickRule(stats []ruleStat) string {
	order := map[string]int{"A": 0, "B": 1, "C": 2}
	var aAccepted int
	for _, s := range stats {
		if s.Name == "A" {
			aAccepted = s.Accepted
		}
	}

	eligible := make([]ruleStat, 0, len(stats))
	for _, s := range stats {
		if s.Eligible && s.Accepted == aAccepted {
			eligible = append(eligible, s)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].Found != eligible[j].Found {
			return eligible[i].Found > eligible[j].Found
		}
		return order[eligible[i].Name] < order[eligible[j].Name]
	})
	if len(eligible) == 0 {
		return ""
	}
	return eligible[0].Name
}

// aitPromptLines renders one prompt line per Docling token, in reader order -- never re-sorted
// by y, because TEXT_INTRO's "one visual row" is only approximate (D-A10).
func aitPromptLines(pages []TokenPage) []promptLine {
	var lines []promptLine
	for _, p := range pages {
		for _, tok := range p.Tokens {
			lines = append(lines, promptLine{
				Page:     p.Number,
				Y:        tok.Region.Y0,
				Segments: []promptSegment{{X: tok.Region.X0, Text: tok.Text}},
			})
		}
	}
	return lines
}

// --- key and answer loading (Core AC 2, 3) ----------------------------------------------------

type aitKeyFieldJSON struct {
	Values    []string `json:"values"`
	Confirmed bool     `json:"confirmed"`
}

type aitKeyDocJSON struct {
	Set    string                     `json:"set"`
	Source string                     `json:"source"`
	Fields map[string]aitKeyFieldJSON `json:"fields"`
}

type aitConfirmationJSON struct {
	Source     string `json:"source"`
	AnswerFile string `json:"answer_file"`
	Recorded   string `json:"recorded"`
}

// aitLoadKey refuses a key with no confirmation marker or an empty answer_file (Core AC 3):
// nothing is scored against a key that cannot show it was confirmed.
func aitLoadKey(path string) (aitKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return aitKey{}, fmt.Errorf("read key %s: %w", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return aitKey{}, fmt.Errorf("parse key %s: %w", path, err)
	}

	confRaw, ok := top["confirmation"]
	if !ok {
		return aitKey{}, fmt.Errorf("key %s carries no confirmation marker", path)
	}
	var conf aitConfirmationJSON
	if err := json.Unmarshal(confRaw, &conf); err != nil {
		return aitKey{}, fmt.Errorf("key %s: parse confirmation: %w", path, err)
	}
	if strings.TrimSpace(conf.AnswerFile) == "" {
		return aitKey{}, fmt.Errorf("key %s: confirmation carries no answer_file", path)
	}
	delete(top, "confirmation")

	docs := make(map[string]aitKeyDoc, len(top))
	for file, docRaw := range top {
		var d aitKeyDocJSON
		if err := json.Unmarshal(docRaw, &d); err != nil {
			return aitKey{}, fmt.Errorf("key %s: parse %s: %w", path, file, err)
		}
		fields := make(map[string]aitKeyField, len(d.Fields))
		for name, f := range d.Fields {
			fields[name] = aitKeyField{Values: f.Values, Confirmed: f.Confirmed}
		}
		docs[file] = aitKeyDoc{Set: d.Set, Source: d.Source, Fields: fields}
	}
	return aitKey{Docs: docs}, nil
}

// aitCheckCorpusRows refuses a key whose corpus rows (source: expectByLayout) disagree with the
// committed corpus_key.json, or omit a layout it holds -- Core AC 2's "no retyping" guarantee.
func aitCheckCorpusRows(key aitKey, corpusKeyPath string) error {
	raw, err := os.ReadFile(corpusKeyPath)
	if err != nil {
		return fmt.Errorf("read corpus key %s: %w", corpusKeyPath, err)
	}
	var rows map[string]map[string]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		return fmt.Errorf("parse corpus key %s: %w", corpusKeyPath, err)
	}

	files := make([]string, 0, len(key.Docs))
	for file, doc := range key.Docs {
		if doc.Source == "expectByLayout" {
			files = append(files, file)
		}
	}
	sort.Strings(files)

	for _, file := range files {
		doc := key.Docs[file]
		row, ok := rows[file]
		if !ok {
			return fmt.Errorf("corpus key %s carries no row for %s, a corpus layout the answer key holds", corpusKeyPath, file)
		}
		fields := make([]string, 0, len(doc.Fields))
		for f := range doc.Fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, field := range fields {
			want, ok := row[field]
			if !ok || !containsString(doc.Fields[field].Values, want) {
				return fmt.Errorf("corpus key %s: %s field %s disagrees with the answer key (committed %q, key %v)", corpusKeyPath, file, field, want, doc.Fields[field].Values)
			}
		}
	}
	return nil
}

type aitAnswerRecordJSON struct {
	File   string            `json:"file"`
	Run    int               `json:"run"`
	Model  string            `json:"model"`
	Fields map[string]string `json:"fields"`
	Error  string            `json:"error"`
}

// aitLoadAnswers returns the good records off one JSONL file, and counts the errored or
// fieldless ones separately. A line that is not JSON at all is a hard failure, naming the line.
func aitLoadAnswers(path string) ([]docAnswer, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read answers %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	var good []docAnswer
	errCount := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec aitAnswerRecordJSON
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, 0, fmt.Errorf("answers %s: line %d: %w", path, i+1, err)
		}
		if rec.Error != "" || rec.Fields == nil {
			errCount++
			continue
		}
		good = append(good, docAnswer{File: rec.File, Run: rec.Run, Fields: rec.Fields})
	}
	return good, errCount, nil
}
