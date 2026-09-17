// aitext_harness_internal_test.go: AIR-01-02's env-gated drivers -- Stage B (TestAIText_Dump)
// and Stage E (TestAIText_Score) wire 01's pure helpers to files. Each driver checks its own
// variables first and returns with one log line when any is unset, opening no file and no
// connection (Deviation Justification: this gate returns, it never asks the test runner to
// skip).
package extraction

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- manifest.json -----------------------------------------------------------------------

type aitManifestDocJSON struct {
	File    string `json:"file"`
	Set     string `json:"set"`
	Docling string `json:"docling"`
}

type aitNotScoredJSON struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type aitManifestJSON struct {
	Docs      []aitManifestDocJSON `json:"docs"`
	NotScored []aitNotScoredJSON   `json:"not_scored"`
}

func aitReadManifest(t *testing.T, path string) aitManifestJSON {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest %s: %v", path, err)
	}
	var m aitManifestJSON
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest %s: %v", path, err)
	}
	return m
}

// --- dump.json / textless.json wire shapes ------------------------------------------------

type aitBoxJSON struct {
	Page int     `json:"page"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
}

type aitDumpTokenJSON struct {
	Text string     `json:"text"`
	Box  aitBoxJSON `json:"box"`
}

type aitDumpSegmentJSON struct {
	X    float64 `json:"x"`
	Text string  `json:"text"`
}

type aitDumpLineJSON struct {
	Y        float64              `json:"y"`
	Segments []aitDumpSegmentJSON `json:"segments"`
}

type aitDumpPageJSON struct {
	Number int                `json:"number"`
	Tokens []aitDumpTokenJSON `json:"tokens"`
	Lines  []aitDumpLineJSON  `json:"lines"`
}

// aitDumpEngineJSON is one header field's engine reading. Value is nil for a missing field
// (docDump.Engine then carries no key at all -- its own "absent key = missing" contract).
type aitDumpEngineJSON struct {
	Value  *string `json:"value"`
	Reason string  `json:"reason"`
}

type aitDumpJSON struct {
	File      string                       `json:"file"`
	Set       string                       `json:"set"`
	TextChars int                          `json:"text_chars"`
	Pages     []aitDumpPageJSON            `json:"pages"`
	Engine    map[string]aitDumpEngineJSON `json:"engine"`
}

type aitTextlessJSON struct {
	File string `json:"file"`
	Set  string `json:"set"`
}

// --- Stage B: TestAIText_Dump --------------------------------------------------------------

// aitReadDoclingFile replays one Docling JSON file (a live Stage A read, or a committed
// golden) through the real DoclingReader and the worker's own readText call (worker.go:68,
// the call worker.go:187 makes) -- never a hand-rolled decode of the wire JSON.
func aitReadDoclingFile(t *testing.T, path string) ([]Page, []TokenPage, PageResult) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read docling json %s: %v", path, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	r, err := NewDoclingReader(srv.URL)
	if err != nil {
		t.Fatalf("NewDoclingReader(%q): %v", srv.URL, err)
	}
	pages, tokens, res, err := readText(t.Context(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText %s: %v", path, err)
	}
	return pages, tokens, res
}

// aitStem drops a file's extension -- out/docs/<stem>/dump.json, matching run.py's own
// os.path.splitext(basename(path)).
func aitStem(file string) string {
	return strings.TrimSuffix(file, filepath.Ext(file))
}

func aitWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// TestAIText_Dump is Stage B: for each manifest document, read its Docling JSON, run the
// worker's default arm (no learned rules -- a fresh layout has none) and write dump.json.
// A TextChars == 0 document is recorded in textless.json and never dumped (Core AC 8).
func TestAIText_Dump(t *testing.T) {
	out := os.Getenv("AIMT_OUT")
	manifestPath := os.Getenv("AIMT_MANIFEST")
	if out == "" || manifestPath == "" {
		t.Log("AIMT_OUT/AIMT_MANIFEST unset: no read, no call")
		return
	}

	manifest := aitReadManifest(t, manifestPath)
	if err := os.MkdirAll(filepath.Join(out, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}

	dumped := []string{}
	textless := []aitTextlessJSON{}
	for _, doc := range manifest.Docs {
		pages, tokens, res := aitReadDoclingFile(t, doc.Docling)
		if res.TextChars == 0 {
			textless = append(textless, aitTextlessJSON{File: doc.File, Set: doc.Set})
			continue
		}

		results := Reconcile(Input{
			Candidates: Resolve(tokens, RuleSet{Tier1: Tier1Rules}),
			Lines:      LineItems(pages),
			Entity:     Entity{},
			Pages:      tokens,
		})
		engine := map[string]aitDumpEngineJSON{}
		for _, r := range results {
			if !containsString(HeaderFields, r.Name) {
				continue // Reconcile also returns line-item rows; dump.json's engine is headers only
			}
			engine[r.Name] = aitDumpEngineJSON{Value: r.Value, Reason: string(r.Reason)}
		}

		lines := aitPromptLines(tokens)
		byPage := map[int][]promptLine{}
		for _, l := range lines {
			byPage[l.Page] = append(byPage[l.Page], l)
		}
		pagesJSON := make([]aitDumpPageJSON, 0, len(tokens))
		for _, p := range tokens {
			tokJSON := make([]aitDumpTokenJSON, 0, len(p.Tokens))
			for _, tk := range p.Tokens {
				tokJSON = append(tokJSON, aitDumpTokenJSON{Text: tk.Text, Box: aitBoxJSON{
					Page: tk.Region.Page, X0: tk.Region.X0, Y0: tk.Region.Y0, X1: tk.Region.X1, Y1: tk.Region.Y1,
				}})
			}
			lineJSON := make([]aitDumpLineJSON, 0, len(byPage[p.Number]))
			for _, l := range byPage[p.Number] {
				segs := make([]aitDumpSegmentJSON, 0, len(l.Segments))
				for _, s := range l.Segments {
					segs = append(segs, aitDumpSegmentJSON{X: s.X, Text: s.Text})
				}
				lineJSON = append(lineJSON, aitDumpLineJSON{Y: l.Y, Segments: segs})
			}
			pagesJSON = append(pagesJSON, aitDumpPageJSON{Number: p.Number, Tokens: tokJSON, Lines: lineJSON})
		}

		dj := aitDumpJSON{File: doc.File, Set: doc.Set, TextChars: res.TextChars, Pages: pagesJSON, Engine: engine}
		dir := filepath.Join(out, "docs", aitStem(doc.File))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := aitWriteJSON(filepath.Join(dir, "dump.json"), dj); err != nil {
			t.Fatalf("write dump for %s: %v", doc.File, err)
		}
		dumped = append(dumped, doc.File)
	}

	if err := os.WriteFile(filepath.Join(out, "files.txt"), []byte(strings.Join(dumped, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write files.txt: %v", err)
	}
	if err := aitWriteJSON(filepath.Join(out, "textless.json"), textless); err != nil {
		t.Fatalf("write textless.json: %v", err)
	}
	t.Logf("dumped %d document(s), %d textless", len(dumped), len(textless))
}

// --- Stage E: TestAIText_Score --------------------------------------------------------------

// aitReadDumps loads every dumped document off files.txt, returning the docDump rows
// aitScoreConfirmed reads plus the reconstructed TokenPages the page-check rules run against.
func aitReadDumps(t *testing.T, out string) ([]docDump, map[string][]TokenPage) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(out, "files.txt"))
	if err != nil {
		t.Fatalf("read files.txt: %v", err)
	}

	var dumps []docDump
	tokensByFile := map[string][]TokenPage{}
	for _, file := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		path := filepath.Join(out, "docs", aitStem(file), "dump.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var dj aitDumpJSON
		if err := json.Unmarshal(raw, &dj); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		engine := map[string]string{}
		for field, e := range dj.Engine {
			if e.Value != nil {
				engine[field] = *e.Value
			}
		}
		dumps = append(dumps, docDump{File: dj.File, Set: dj.Set, TextChars: dj.TextChars, Engine: engine})

		pages := make([]TokenPage, 0, len(dj.Pages))
		for _, p := range dj.Pages {
			toks := make([]Token, 0, len(p.Tokens))
			for _, tk := range p.Tokens {
				toks = append(toks, Token{Text: tk.Text, Region: Region{
					Page: tk.Box.Page, X0: tk.Box.X0, Y0: tk.Box.Y0, X1: tk.Box.X1, Y1: tk.Box.Y1,
				}})
			}
			pages = append(pages, TokenPage{Number: p.Number, Tokens: toks})
		}
		tokensByFile[dj.File] = pages
	}
	return dumps, tokensByFile
}

func aitReadTextlessDumps(t *testing.T, out string) []docDump {
	t.Helper()
	path := filepath.Join(out, "textless.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var entries []aitTextlessJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	dumps := make([]docDump, 0, len(entries))
	for _, e := range entries {
		dumps = append(dumps, docDump{File: e.File, Set: e.Set, TextChars: 0})
	}
	return dumps
}

// aitCheckKeyCoversScoredDumps fails before a cell is written when a non-textless dump has no
// key row: aitScoreConfirmed's own loop drops that document with no trace at all -- not a
// cell, not unconfirmed, not textless, not not_read (Stage 1 validation). A textless dump
// needs no row (it is listed regardless of the key), and a not_scored document is never
// dumped, so neither belongs in this check. Counts only, never a file name (Data handling
// boundaries).
func aitCheckKeyCoversScoredDumps(t *testing.T, key aitKey, dumps []docDump) {
	t.Helper()
	var scored, missing int
	for _, d := range dumps {
		if d.TextChars == 0 {
			continue
		}
		scored++
		if _, ok := key.Docs[d.File]; !ok {
			missing++
		}
	}
	if missing > 0 {
		t.Fatalf("%d scored document(s) have no key row; aitScoreConfirmed would drop them silently", missing)
	}
	t.Logf("key covers every scored dump: %d document(s)", scored)
}

// aitAnswerMetaJSON re-decodes one answers.jsonl line for cost and latency --
// aitAnswerRecordJSON (01) carries neither, and this file must not add fields to it.
type aitAnswerMetaJSON struct {
	Run      int               `json:"run"`
	Error    string            `json:"error"`
	Fields   map[string]string `json:"fields"`
	Cost     float64           `json:"cost"`
	LatencyS *float64          `json:"latency_s"`
}

// aitReadAnswerMeta returns per-call cost and latency over the good records, the total spend
// over every record, and the highest run number seen -- run.py's intended RUNS, since no
// AIMT_RUNS variable exists. aitLoadAnswers has already validated every line as JSON by the
// time this runs, so a decode failure here cannot occur in practice.
func aitReadAnswerMeta(t *testing.T, path string) (costs, latencies []float64, totalCost float64, maxRun int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read answers %s: %v", path, err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec aitAnswerMetaJSON
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Run > maxRun {
			maxRun = rec.Run
		}
		totalCost += rec.Cost
		if rec.Error == "" && rec.Fields != nil {
			costs = append(costs, rec.Cost)
			if rec.LatencyS != nil {
				latencies = append(latencies, *rec.LatencyS)
			}
		}
	}
	return costs, latencies, totalCost, maxRun
}

type aitSummaryFieldJSON struct {
	AIRight     int `json:"ai_right"`
	AIWrong     int `json:"ai_wrong"`
	AIBlank     int `json:"ai_blank"`
	EngineRight int `json:"engine_right"`
	EngineWrong int `json:"engine_wrong"`
	EngineBlank int `json:"engine_blank"`
	Agree       int `json:"agree"`
	Disagree    int `json:"disagree"`
	AIOnly      int `json:"ai_only"`
	// Remainder is engine_only + neither (D-A16): every row sums to its cell count.
	Remainder int `json:"remainder"`
}

type aitSummaryRuleJSON struct {
	Found         int  `json:"found"`
	FoundOf       int  `json:"found_of"`
	Accepted      int  `json:"accepted"`
	AcceptedOf    int  `json:"accepted_of"`
	AdversarialOK bool `json:"adversarial_ok"`
}

type aitSummaryJSON struct {
	Cells       int                                       `json:"cells"`
	Errors      int                                       `json:"errors"`
	Unconfirmed []fieldRef                                `json:"unconfirmed"`
	Textless    []string                                  `json:"textless"`
	NotRead     []notScoredEntry                          `json:"not_read"`
	Fields      map[string]map[string]aitSummaryFieldJSON `json:"fields_by_set"`
	Rules       map[string]aitSummaryRuleJSON             `json:"rules"`
	PickedRule  string                                    `json:"picked_rule"`
	CostP50     float64                                   `json:"cost_p50"`
	CostP90     float64                                   `json:"cost_p90"`
	LatencyP50  float64                                   `json:"latency_p50_s"`
	LatencyP90  float64                                   `json:"latency_p90_s"`
	TotalCost   float64                                   `json:"total_cost"`
	MissingRuns int                                       `json:"missing_runs"`
}

// aitAdversarialPass replays the golden that carries the dropped-digit TIN and rejects rule
// unless it also rejects the truncated amount and every aitInventedValues entry (Core AC 5).
func aitAdversarialPass(t *testing.T, rule func(field, raw string, pages []TokenPage) (bool, Region)) bool {
	t.Helper()
	pages := aitGoldenPages(t, "wild_scanned_no_number")
	if found, _ := rule("buyer_tin", "9999999-1202", pages); found {
		return false
	}
	if found, _ := rule("total", "935.00", pages); found {
		return false
	}
	for field, value := range aitInventedValues {
		if found, _ := rule(field, value, pages); found {
			return false
		}
	}
	return true
}

// aitBuildSummary aggregates Core AC 4 (per set, per field), AC 5 (per-rule found/accepted and
// adversarial results) and AC 7 (cost/latency percentiles) on top of 01's pure helpers.
func aitBuildSummary(t *testing.T, key aitKey, dumps []docDump, tokensByFile map[string][]TokenPage,
	answers []docAnswer, answerErrors int, result aitScoreResult,
	costs, latencies []float64, totalCost float64, maxRun int,
) aitSummaryJSON {
	t.Helper()

	fields := map[string]map[string]aitSummaryFieldJSON{}
	var ruleCells []ruleCountCell
	textDocs := 0

	for _, d := range dumps {
		if d.TextChars == 0 {
			continue
		}
		textDocs++
		doc, ok := key.Docs[d.File]
		if !ok {
			continue
		}
		pages := tokensByFile[d.File]
		for field, kf := range doc.Fields {
			if !kf.Confirmed {
				continue
			}
			var engineVal *string
			if v, ok := d.Engine[field]; ok {
				engineVal = &v
			}
			if fields[d.Set] == nil {
				fields[d.Set] = map[string]aitSummaryFieldJSON{}
			}
			stat := fields[d.Set][field]
			switch aitClassify(field, engineVal, kf.Values) {
			case "right":
				stat.EngineRight++
			case "wrong":
				stat.EngineWrong++
			case "blank":
				stat.EngineBlank++
			}

			for _, a := range answers {
				if a.File != d.File {
					continue
				}
				var aiVal *string
				raw, has := a.Fields[field]
				if has {
					aiVal = &raw
				}
				verdict := aitClassify(field, aiVal, kf.Values)
				switch verdict {
				case "right":
					stat.AIRight++
				case "wrong":
					stat.AIWrong++
				case "blank":
					stat.AIBlank++
				}
				switch aitAgreement(field, aiVal, engineVal) {
				case "agree":
					stat.Agree++
				case "disagree":
					stat.Disagree++
				case "ai_only":
					stat.AIOnly++
				default: // engine_only, neither
					stat.Remainder++
				}
				// "right" also covers a blank answer against an empty key (aitClassify); the
				// rule-found denominator wants a non-blank ANSWER TEXT, not just a non-"blank"
				// verdict, or an empty raw with nothing to search for inflates found_of.
				if verdict != "blank" && strings.TrimSpace(raw) != "" {
					ruleCells = append(ruleCells, ruleCountCell{Field: field, Answer: raw, Verdict: verdict, Pages: pages})
				}
			}
			fields[d.Set][field] = stat
		}
	}

	rules := map[string]aitSummaryRuleJSON{}
	var stats []ruleStat
	for _, r := range []struct {
		name string
		fn   func(field, raw string, pages []TokenPage) (bool, Region)
	}{
		{"A", aitRuleWholeToken}, {"B", aitRuleInLine}, {"C", aitRuleJoinedRow},
	} {
		found, foundOf, accepted, acceptedOf := aitRuleCounts(r.fn, ruleCells)
		ok := aitAdversarialPass(t, r.fn)
		rules[r.name] = aitSummaryRuleJSON{Found: found, FoundOf: foundOf, Accepted: accepted, AcceptedOf: acceptedOf, AdversarialOK: ok}
		stats = append(stats, ruleStat{Name: r.name, Found: found, Accepted: accepted, Eligible: ok})
	}
	foundCtl, foundOfCtl, acceptedCtl, acceptedOfCtl := aitRuleCounts(aitRuleSubstringControl, ruleCells)
	rules["control"] = aitSummaryRuleJSON{
		Found: foundCtl, FoundOf: foundOfCtl, Accepted: acceptedCtl, AcceptedOf: acceptedOfCtl,
		AdversarialOK: aitAdversarialPass(t, aitRuleSubstringControl),
	}

	costP50, costP90, _ := aitPercentile(costs)
	latP50, latP90, _ := aitPercentile(latencies)

	// ceiling: maxRun is the highest run number seen anywhere, so a run tier that failed for
	// EVERY document (no record at all, not even an errored one) undercounts; revisit if
	// run.py's own per-file attempt count needs recording alongside answers.jsonl.
	missingRuns := textDocs*maxRun - len(answers)
	if missingRuns < 0 {
		missingRuns = 0
	}

	return aitSummaryJSON{
		Cells:       len(result.Cells),
		Errors:      answerErrors,
		Unconfirmed: result.Unconfirmed,
		Textless:    result.Textless,
		NotRead:     result.NotRead,
		Fields:      fields,
		Rules:       rules,
		PickedRule:  aitPickRule(stats),
		CostP50:     costP50,
		CostP90:     costP90,
		LatencyP50:  latP50,
		LatencyP90:  latP90,
		TotalCost:   totalCost,
		MissingRuns: missingRuns,
	}
}

// TestAIText_Score is Stage E: aitLoadKey, then aitCheckCorpusRows, then aitLoadAnswers, then
// aitScoreConfirmed, in that order -- any error fails before a cell is written.
func TestAIText_Score(t *testing.T) {
	out := os.Getenv("AIMT_OUT")
	manifestPath := os.Getenv("AIMT_MANIFEST")
	answersPath := os.Getenv("AIMT_ANSWERS")
	keyPath := os.Getenv("AIMT_KEY")
	corpusKeyPath := os.Getenv("AIMT_CORPUS_KEY")
	if out == "" || manifestPath == "" || answersPath == "" || keyPath == "" || corpusKeyPath == "" {
		t.Log("AIMT_OUT/AIMT_MANIFEST/AIMT_ANSWERS/AIMT_KEY/AIMT_CORPUS_KEY unset: no read, no call")
		return
	}

	key, err := aitLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aitLoadKey: %v", err)
	}
	if err := aitCheckCorpusRows(key, corpusKeyPath); err != nil {
		t.Fatalf("aitCheckCorpusRows: %v", err)
	}
	answers, answerErrors, err := aitLoadAnswers(answersPath)
	if err != nil {
		t.Fatalf("aitLoadAnswers: %v", err)
	}

	manifest := aitReadManifest(t, manifestPath)
	notScored := make([]notScoredEntry, 0, len(manifest.NotScored))
	for _, e := range manifest.NotScored {
		notScored = append(notScored, notScoredEntry{File: e.File, Reason: e.Reason})
	}

	dumps, tokensByFile := aitReadDumps(t, out)
	dumps = append(dumps, aitReadTextlessDumps(t, out)...)
	aitCheckKeyCoversScoredDumps(t, key, dumps)

	result := aitScoreConfirmed(key, dumps, answers, notScored)
	if err := aitWriteJSON(filepath.Join(out, "cells.json"), result.Cells); err != nil {
		t.Fatalf("write cells.json: %v", err)
	}

	costs, latencies, totalCost, maxRun := aitReadAnswerMeta(t, answersPath)
	summary := aitBuildSummary(t, key, dumps, tokensByFile, answers, answerErrors, result, costs, latencies, totalCost, maxRun)
	if err := aitWriteJSON(filepath.Join(out, "summary.json"), summary); err != nil {
		t.Fatalf("write summary.json: %v", err)
	}
	t.Logf("scored %d cell(s); %d unconfirmed; %d textless; %d not read; %d answer error(s)",
		len(result.Cells), len(result.Unconfirmed), len(result.Textless), len(result.NotRead), answerErrors)
}
