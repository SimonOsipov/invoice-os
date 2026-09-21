// ailines_score_internal_test.go: AIR-08-05's Stage E for line items -- the pure aggregator
// aliBuildSummary (AC-2..6), the dump.json/answers.lines.jsonl/answers.jsonl loaders, and the
// env-gated driver TestAIText_ScoreLines. Specs live in ailines_score_rules_internal_test.go.
package extraction

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// aliReaderEngine names the engine's own reading in the reports below; the two AI call shapes
// use their run.py purpose as their reader name.
const (
	aliReaderEngine    = "engine"
	aliPurposeHeader   = "header"
	aliPurposeCombined = "combined"
	aliPurposeLines    = "lines"
)

// aliReaders is the report's reader order: the engine first, then the two AI call shapes.
var aliReaders = []string{aliReaderEngine, aliPurposeCombined, aliPurposeLines}

const (
	aliCallGood      = "good"
	aliCallRefused   = "refused"
	aliCallTransport = "transport_error"
	aliCallBlank     = "blank"
)

// --- input value types -----------------------------------------------------------------

// aliLineAnswer is one good line-item record: one reader's rows for one document and run.
type aliLineAnswer struct {
	File    string
	Model   string
	Purpose string
	Run     int
	Rows    []DocLine
}

// aliCallMeta is one record of any purpose, good or not. The cost/latency arms and the
// good/refused/transport/blank census both read it.
type aliCallMeta struct {
	File     string
	Model    string
	Purpose  string
	Run      int
	Outcome  string
	Cost     float64
	LatencyS *float64
}

// --- output shapes (line_cells.json / line_summary.json) -------------------------------

type aliCellJSON struct {
	File        string `json:"file"`
	Set         string `json:"set"`
	Reader      string `json:"reader"`
	Model       string `json:"model"`
	Run         int    `json:"run"`
	KeyRow      int    `json:"key_row"`
	ReaderRow   int    `json:"reader_row"`
	Pass        int    `json:"pass"`
	Outcome     string `json:"outcome"`
	Role        string `json:"role"`
	KeyHasValue bool   `json:"key_has_value"`
	Verdict     string `json:"verdict"`
}

type aliAlignRunJSON struct {
	Model      string         `json:"model"`
	Run        int            `json:"run"`
	ReaderRows int            `json:"reader_rows"`
	Found      int            `json:"found"`
	Misaligned int            `json:"misaligned"`
	Invented   int            `json:"invented"`
	Dropped    int            `json:"dropped"`
	PassPairs  map[string]int `json:"pass_pairs"`
}

type aliReaderDocJSON struct {
	Runs              []aliAlignRunJSON `json:"runs"`
	DistinctRowCounts int               `json:"distinct_row_counts"`
}

type aliDocJSON struct {
	Set     string                      `json:"set"`
	KeyRows int                         `json:"key_rows"`
	Readers map[string]aliReaderDocJSON `json:"readers"`
}

// aliRoleRowJSON keeps value cells and correct nulls in two halves that are never added.
type aliRoleRowJSON struct {
	Reader       string `json:"reader"`
	Role         string `json:"role"`
	ValueRight   int    `json:"value_right"`
	ValueWrong   int    `json:"value_wrong"`
	ValueMissing int    `json:"value_missing"`
	NullRight    int    `json:"null_right"`
	NullWrong    int    `json:"null_wrong"`
	NullMissing  int    `json:"null_missing"`
}

// aliDenomRowJSON is the denominator the headline divides by.
type aliDenomRowJSON struct {
	Reader     string `json:"reader"`
	Alignments int    `json:"alignments"`
	ValueCells int    `json:"value_cells"`
	NullCells  int    `json:"null_cells"`
}

type aliDeltaRowJSON struct {
	Reader     string `json:"reader"`
	Role       string `json:"role"`
	Cells      string `json:"cells"`
	BothRight  int    `json:"both_right"`
	AIOnly     int    `json:"ai_only"`
	EngineOnly int    `json:"engine_only"`
	BothWrong  int    `json:"both_wrong"`
}

type aliArmJSON struct {
	Calls      int     `json:"calls"`
	TotalCost  float64 `json:"total_cost"`
	CostP50    float64 `json:"cost_p50"`
	CostP90    float64 `json:"cost_p90"`
	LatencyP50 float64 `json:"latency_p50_s"`
	LatencyP90 float64 `json:"latency_p90_s"`
}

type aliArmSumJSON struct {
	Total float64 `json:"total"`
	P50   float64 `json:"p50"`
	P90   float64 `json:"p90"`
}

type aliArmsJSON struct {
	SameCall                  aliArmJSON    `json:"same_call"`
	SeparateHeader            aliArmJSON    `json:"separate_header"`
	SeparateLines             aliArmJSON    `json:"separate_lines"`
	SeparateCost              aliArmSumJSON `json:"separate_cost"`
	SeparateLatencySequential aliArmSumJSON `json:"separate_latency_sequential_s"`
	SeparateLatencyParallel   aliArmSumJSON `json:"separate_latency_parallel_s"`
	UnpairedLegs              int           `json:"unpaired_legs"`
}

type aliCensusRowJSON struct {
	Purpose        string `json:"purpose"`
	Good           int    `json:"good"`
	Refused        int    `json:"refused"`
	TransportError int    `json:"transport_error"`
	Blank          int    `json:"blank"`
	MissingRuns    int    `json:"missing_runs"`
	MaxRun         int    `json:"max_run"`
}

type aliSummaryJSON struct {
	Models              []string              `json:"models"`
	KeyTotals           aliKeyTotalsResult    `json:"key_totals"`
	Documents           map[string]aliDocJSON `json:"documents"`
	Roles               []aliRoleRowJSON      `json:"roles"`
	Denominators        []aliDenomRowJSON     `json:"denominators"`
	Delta               []aliDeltaRowJSON     `json:"delta"`
	DeltaUnpairedRows   int                   `json:"delta_unpaired_rows"`
	Arms                aliArmsJSON           `json:"arms"`
	Census              []aliCensusRowJSON    `json:"census"`
	Unconfirmed         []string              `json:"unconfirmed"`
	Textless            []string              `json:"textless"`
	TextlessWithKeyRows []string              `json:"textless_with_key_rows"`
	NotRead             []notScoredEntry      `json:"not_read"`
	Cells               int                   `json:"cells"`
}

// --- floor helper (AC-7) -----------------------------------------------------------------

func aliFloorProblem(n int, what string) string {
	if n == 0 {
		return fmt.Sprintf("no %s to assert over", what)
	}
	return ""
}

func aliFloor(t *testing.T, n int, what string) {
	t.Helper()
	if p := aliFloorProblem(n, what); p != "" {
		t.Fatal(p)
	}
}

// --- ordering helpers ----------------------------------------------------------------------

func aliReaderIndex(reader string) int {
	for i, r := range aliReaders {
		if r == reader {
			return i
		}
	}
	return len(aliReaders)
}

func aliRoleIndex(role string) int {
	for i, r := range LineRoles {
		if r == role {
			return i
		}
	}
	return len(LineRoles)
}

// aliCellsHalf is AC-3/AC-4's value-vs-null split, keyed off the KEY cell alone.
func aliCellsHalf(key *string) string {
	if aliKeyHasValue(key) {
		return "value"
	}
	return "null"
}

// --- the pure aggregator (AC 2-6) ---------------------------------------------------------

// aliRunInput is one reader's one run: the rows it returned for one document.
type aliRunInput struct {
	Run   int
	Model string
	Rows  []DocLine
}

// aliRunsFor collects one reader's runs for one document, ascending by run. The engine reads
// once (Run 0, Model ""); combined and lines get one run per good answer record.
func aliRunsFor(reader, file string, engine map[string][]DocLine, answers []aliLineAnswer) []aliRunInput {
	if reader == aliReaderEngine {
		return []aliRunInput{{Run: 0, Model: "", Rows: engine[file]}}
	}
	var runs []aliRunInput
	for _, a := range answers {
		if a.File == file && a.Purpose == reader {
			runs = append(runs, aliRunInput{Run: a.Run, Model: a.Model, Rows: a.Rows})
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Run < runs[j].Run })
	return runs
}

// aliRunKey addresses one (document, reader, run) alignment's pairs, for the delta step.
type aliRunKey struct {
	File   string
	Reader string
	Run    int
}

func aliAddRoleVerdict(agg map[string]map[string]*aliRoleRowJSON, reader, role string, keyHasValue bool, verdict string) {
	if agg[reader] == nil {
		agg[reader] = map[string]*aliRoleRowJSON{}
	}
	row := agg[reader][role]
	if row == nil {
		row = &aliRoleRowJSON{Reader: reader, Role: role}
		agg[reader][role] = row
	}
	switch {
	case keyHasValue && verdict == aliRight:
		row.ValueRight++
	case keyHasValue && verdict == aliWrong:
		row.ValueWrong++
	case keyHasValue:
		row.ValueMissing++
	case !keyHasValue && verdict == aliRight:
		row.NullRight++
	case !keyHasValue && verdict == aliWrong:
		row.NullWrong++
	default:
		row.NullMissing++
	}
}

func aliBumpDenom(agg map[string]*aliDenomRowJSON, reader string, keyHasValue bool) {
	row := agg[reader]
	if row == nil {
		row = &aliDenomRowJSON{Reader: reader}
		agg[reader] = row
	}
	if keyHasValue {
		row.ValueCells++
	} else {
		row.NullCells++
	}
}

func aliDeltaBump(agg map[string]*aliDeltaRowJSON, reader, role, half string) *aliDeltaRowJSON {
	k := reader + "|" + role + "|" + half
	d := agg[k]
	if d == nil {
		d = &aliDeltaRowJSON{Reader: reader, Role: role, Cells: half}
		agg[k] = d
	}
	return d
}

// aliBuildSummary is the whole of AC 2-6. Pure: no file, no variable, no clock, no network, so
// every AC it carries is gradable with nothing set.
func aliBuildSummary(
	key aliKey,
	dumps []docDump,
	engine map[string][]DocLine,
	answers []aliLineAnswer,
	calls []aliCallMeta,
	notScored []notScoredEntry,
) (aliSummaryJSON, []aliCellJSON) {
	sum := aliSummaryJSON{
		KeyTotals: aliKeyTotals(key),
		Documents: map[string]aliDocJSON{},
		NotRead:   append([]notScoredEntry(nil), notScored...),
	}
	sort.Slice(sum.NotRead, func(i, j int) bool { return sum.NotRead[i].File < sum.NotRead[j].File })

	// --- 1. document partition (AC-6) ---
	var scoredFiles []string
	for _, dump := range dumps {
		if dump.TextChars == 0 {
			sum.Textless = append(sum.Textless, dump.File)
			if doc, ok := key.Docs[dump.File]; ok && len(doc.Rows) > 0 {
				sum.TextlessWithKeyRows = append(sum.TextlessWithKeyRows, dump.File)
			}
			continue
		}
		doc, ok := key.Docs[dump.File]
		if !ok {
			continue // aliCheckKeyCoverage refuses this case before the driver ever calls in
		}
		if !doc.Confirmed {
			sum.Unconfirmed = append(sum.Unconfirmed, dump.File)
			continue
		}
		sum.Documents[dump.File] = aliDocJSON{Set: dump.Set, KeyRows: len(doc.Rows), Readers: map[string]aliReaderDocJSON{}}
		scoredFiles = append(scoredFiles, dump.File)
	}
	sort.Strings(sum.Textless)
	sort.Strings(sum.TextlessWithKeyRows)
	sort.Strings(sum.Unconfirmed)
	sort.Strings(scoredFiles)

	// --- 2 & 3. per reader, per run alignment, and cells (AC-2, AC-3) ---
	var cells []aliCellJSON
	roleAgg := map[string]map[string]*aliRoleRowJSON{}
	denomAgg := map[string]*aliDenomRowJSON{}
	pairsByRun := map[aliRunKey]map[int]aliPair{}

	for _, file := range scoredFiles {
		doc := key.Docs[file]
		docJSON := sum.Documents[file]
		for _, reader := range aliReaders {
			runs := aliRunsFor(reader, file, engine, answers)

			var runRows []aliAlignRunJSON
			counts := map[int]bool{}
			for _, run := range runs {
				al := aliAlign(run.Rows, doc.Rows)
				counts[len(run.Rows)] = true

				if denomAgg[reader] == nil {
					denomAgg[reader] = &aliDenomRowJSON{Reader: reader}
				}
				denomAgg[reader].Alignments++

				passPairs := map[string]int{}
				for _, p := range al.Pairs {
					passPairs[strconv.Itoa(p.Pass)]++
				}
				runRows = append(runRows, aliAlignRunJSON{
					Model: run.Model, Run: run.Run, ReaderRows: len(run.Rows),
					Found: al.Found(), Misaligned: al.Misaligned(),
					Invented: len(al.Invented), Dropped: len(al.Dropped), PassPairs: passPairs,
				})

				kp := map[int]aliPair{}
				for _, p := range al.Pairs {
					kp[p.Key] = p
					keyRow := doc.Rows[p.Key-1]
					readerRow := run.Rows[p.Answer-1]
					for _, role := range LineRoles {
						keyCell := keyRow.Cell(role)
						verdict := aliClassifyCell(role, readerRow.Cell(role), keyCell)
						hasValue := aliKeyHasValue(keyCell)
						cells = append(cells, aliCellJSON{
							File: file, Set: docJSON.Set, Reader: reader, Model: run.Model, Run: run.Run,
							KeyRow: p.Key, ReaderRow: p.Answer, Pass: p.Pass, Outcome: p.Outcome,
							Role: role, KeyHasValue: hasValue, Verdict: verdict,
						})
						aliAddRoleVerdict(roleAgg, reader, role, hasValue, verdict)
						aliBumpDenom(denomAgg, reader, hasValue)
					}
				}
				pairsByRun[aliRunKey{File: file, Reader: reader, Run: run.Run}] = kp

				// A dropped key row's cells never reach aliClassifyCell as nil: that call
				// returns aliRight for a null key cell, awarding credit for a row the reader
				// never returned (a bias that flatters the engine, which drops 18 rows to the
				// AI's 0). Every one of a dropped row's five cells is missing, value or null.
				for _, keyPos := range al.Dropped {
					keyRow := doc.Rows[keyPos-1]
					for _, role := range LineRoles {
						keyCell := keyRow.Cell(role)
						verdict := aliMissing
						hasValue := aliKeyHasValue(keyCell)
						cells = append(cells, aliCellJSON{
							File: file, Set: docJSON.Set, Reader: reader, Model: run.Model, Run: run.Run,
							KeyRow: keyPos, ReaderRow: 0, Pass: 0, Outcome: "dropped",
							Role: role, KeyHasValue: hasValue, Verdict: verdict,
						})
						aliAddRoleVerdict(roleAgg, reader, role, hasValue, verdict)
						aliBumpDenom(denomAgg, reader, hasValue)
					}
				}
				// An invented reader row has no key row to score against and is already
				// counted at row level (al.Invented) -- it emits no cell.
			}
			docJSON.Readers[reader] = aliReaderDocJSON{Runs: runRows, DistinctRowCounts: len(counts)}
		}
		sum.Documents[file] = docJSON
	}

	for _, reader := range aliReaders {
		for _, role := range LineRoles {
			if row := roleAgg[reader][role]; row != nil {
				sum.Roles = append(sum.Roles, *row)
			}
		}
	}
	for _, reader := range aliReaders {
		if row := denomAgg[reader]; row != nil {
			sum.Denominators = append(sum.Denominators, *row)
		}
	}

	// --- 4. delta (AC-4) ---
	deltaAgg := map[string]*aliDeltaRowJSON{}
	for _, file := range scoredFiles {
		doc := key.Docs[file]
		enginePairs := pairsByRun[aliRunKey{File: file, Reader: aliReaderEngine, Run: 0}]
		for _, reader := range []string{aliPurposeCombined, aliPurposeLines} {
			for _, a := range answers {
				if a.File != file || a.Purpose != reader {
					continue
				}
				aiPairs := pairsByRun[aliRunKey{File: file, Reader: reader, Run: a.Run}]
				keyRows := make([]int, 0, len(aiPairs))
				for k := range aiPairs {
					keyRows = append(keyRows, k)
				}
				sort.Ints(keyRows)
				for _, keyRow := range keyRows {
					aiPair := aiPairs[keyRow]
					enginePair, hasEngine := enginePairs[keyRow]
					if !hasEngine {
						sum.DeltaUnpairedRows++
						continue
					}
					keyLine := doc.Rows[keyRow-1]
					aiRow := a.Rows[aiPair.Answer-1]
					engineRow := engine[file][enginePair.Answer-1]
					for _, role := range LineRoles {
						keyCell := keyLine.Cell(role)
						aiCorrect := aliClassifyCell(role, aiRow.Cell(role), keyCell) == aliRight
						engineCorrect := aliClassifyCell(role, engineRow.Cell(role), keyCell) == aliRight
						d := aliDeltaBump(deltaAgg, reader, role, aliCellsHalf(keyCell))
						switch {
						case aiCorrect && engineCorrect:
							d.BothRight++
						case aiCorrect:
							d.AIOnly++
						case engineCorrect:
							d.EngineOnly++
						default:
							d.BothWrong++
						}
					}
				}
			}
		}
	}
	for _, reader := range []string{aliPurposeCombined, aliPurposeLines} {
		for _, role := range LineRoles {
			for _, half := range []string{"value", "null"} {
				if d := deltaAgg[reader+"|"+role+"|"+half]; d != nil {
					sum.Delta = append(sum.Delta, *d)
				}
			}
		}
	}

	// --- 5 & 6. arms and census (AC-5, AC-6) ---
	sum.Arms = aliBuildArms(calls)
	sum.Census = aliBuildCensus(calls, len(scoredFiles))

	// --- 7. models (the unasked-for guard the plan keeps) ---
	modelSet := map[string]bool{}
	for _, c := range calls {
		if c.Model != "" {
			modelSet[c.Model] = true
		}
	}
	for m := range modelSet {
		sum.Models = append(sum.Models, m)
	}
	sort.Strings(sum.Models)

	sort.Slice(cells, func(i, j int) bool {
		a, b := cells[i], cells[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if ai, bi := aliReaderIndex(a.Reader), aliReaderIndex(b.Reader); ai != bi {
			return ai < bi
		}
		if a.Run != b.Run {
			return a.Run < b.Run
		}
		if a.KeyRow != b.KeyRow {
			return a.KeyRow < b.KeyRow
		}
		return aliRoleIndex(a.Role) < aliRoleIndex(b.Role)
	})
	sum.Cells = len(cells)

	return sum, cells
}

// --- arms (AC-5) -----------------------------------------------------------------------

type aliJoinKey struct {
	File  string
	Model string
	Run   int
}

func aliFilterPurpose(calls []aliCallMeta, purpose string) []aliCallMeta {
	var out []aliCallMeta
	for _, c := range calls {
		if c.Purpose == purpose {
			out = append(out, c)
		}
	}
	return out
}

func aliArmFromCalls(calls []aliCallMeta) aliArmJSON {
	var arm aliArmJSON
	var goodCosts, goodLatencies []float64
	for _, c := range calls {
		arm.Calls++
		arm.TotalCost += c.Cost
		if c.Outcome != aliCallGood {
			continue
		}
		goodCosts = append(goodCosts, c.Cost)
		if c.LatencyS != nil {
			goodLatencies = append(goodLatencies, *c.LatencyS)
		}
	}
	arm.CostP50, arm.CostP90, _ = aitPercentile(goodCosts)
	arm.LatencyP50, arm.LatencyP90, _ = aitPercentile(goodLatencies)
	return arm
}

func aliArmSum(vals []float64) aliArmSumJSON {
	var s aliArmSumJSON
	for _, v := range vals {
		s.Total += v
	}
	s.P50, s.P90, _ = aitPercentile(vals)
	return s
}

// aliBuildArms is AC-5: the combined arm unsummed, the two separate-call legs unsummed, then
// the header/lines legs joined on (file, model, run) for the sequential/parallel view.
func aliBuildArms(calls []aliCallMeta) aliArmsJSON {
	var arms aliArmsJSON
	arms.SameCall = aliArmFromCalls(aliFilterPurpose(calls, aliPurposeCombined))

	headerCalls := aliFilterPurpose(calls, aliPurposeHeader)
	linesCalls := aliFilterPurpose(calls, aliPurposeLines)
	arms.SeparateHeader = aliArmFromCalls(headerCalls)
	arms.SeparateLines = aliArmFromCalls(linesCalls)

	headerByKey := map[aliJoinKey]aliCallMeta{}
	for _, c := range headerCalls {
		headerByKey[aliJoinKey{c.File, c.Model, c.Run}] = c
	}
	linesByKey := map[aliJoinKey]aliCallMeta{}
	for _, c := range linesCalls {
		linesByKey[aliJoinKey{c.File, c.Model, c.Run}] = c
	}

	seen := map[aliJoinKey]bool{}
	keys := make([]aliJoinKey, 0, len(headerByKey)+len(linesByKey))
	for k := range headerByKey {
		keys = append(keys, k)
		seen[k] = true
	}
	for k := range linesByKey {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].File != keys[j].File {
			return keys[i].File < keys[j].File
		}
		if keys[i].Model != keys[j].Model {
			return keys[i].Model < keys[j].Model
		}
		return keys[i].Run < keys[j].Run
	})

	var costs, sequentials, parallels []float64
	for _, k := range keys {
		h, hasH := headerByKey[k]
		l, hasL := linesByKey[k]
		if !hasH || !hasL {
			arms.UnpairedLegs++
			continue
		}
		costs = append(costs, h.Cost+l.Cost)
		if h.LatencyS != nil && l.LatencyS != nil {
			sequential := *h.LatencyS + *l.LatencyS
			parallel := math.Max(*h.LatencyS, *l.LatencyS)
			sequentials = append(sequentials, sequential)
			parallels = append(parallels, parallel)
		}
	}
	arms.SeparateCost = aliArmSum(costs)
	arms.SeparateLatencySequential = aliArmSum(sequentials)
	arms.SeparateLatencyParallel = aliArmSum(parallels)
	return arms
}

// --- census (AC-6) ---------------------------------------------------------------------

type aliFileRun struct {
	File string
	Run  int
}

// aliBuildCensus is AC-6's per-purpose call ledger. MaxRun is pooled across every purpose (the
// harness runs the same RUNS count everywhere), so a purpose missing its top run entirely (no
// record at all, not even a bad one) still reports it missing instead of shrinking its own ceiling.
func aliBuildCensus(calls []aliCallMeta, scoredDocs int) []aliCensusRowJSON {
	maxRun := 0
	for _, c := range calls {
		if c.Run > maxRun {
			maxRun = c.Run
		}
	}

	rows := make([]aliCensusRowJSON, 0, 3)
	for _, purpose := range []string{aliPurposeHeader, aliPurposeCombined, aliPurposeLines} {
		row := aliCensusRowJSON{Purpose: purpose, MaxRun: maxRun}
		seen := map[aliFileRun]bool{}
		for _, c := range calls {
			if c.Purpose != purpose {
				continue
			}
			switch c.Outcome {
			case aliCallGood:
				row.Good++
			case aliCallRefused:
				row.Refused++
			case aliCallTransport:
				row.TransportError++
			case aliCallBlank:
				row.Blank++
			}
			seen[aliFileRun{File: c.File, Run: c.Run}] = true
		}
		// ceiling: a run tier that fails for every document leaves maxRun short and
		// undercounts, same caveat as aitBuildSummary's own MissingRuns.
		missing := scoredDocs*maxRun - len(seen)
		if missing < 0 {
			missing = 0
		}
		row.MissingRuns = missing
		rows = append(rows, row)
	}
	return rows
}

// --- loaders ---------------------------------------------------------------------------

// aliEngineRows turns dump.json's engine_lines into positional rows. Index is carried through
// unchanged so a gap left by a row the engine read no cell of stays visible; aliAlign pairs on
// slice position and content, never on Index.
func aliEngineRows(entries []aliEngineLineJSON) []DocLine {
	out := make([]DocLine, 0, len(entries))
	for _, e := range entries {
		line := DocLine{Index: e.Index}
		for role, v := range e.Roles {
			switch role {
			case LineRoleDescription:
				line.Description = v.Value
			case LineRoleQuantity:
				line.Quantity = v.Value
			case LineRoleUnitPrice:
				line.UnitPrice = v.Value
			case LineRoleLineTotal:
				line.LineTotal = v.Value
			case LineRoleLineTax:
				line.LineTax = v.Value
			}
		}
		out = append(out, line)
	}
	return out
}

// aliReadDumps loads every dumped document off files.txt: the docDump rows the summary
// partitions on, and the engine's line rows per file. It does not rebuild TokenPages -- only
// AIR-01's page-check rules need those.
func aliReadDumps(t *testing.T, out string) ([]docDump, map[string][]DocLine) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(out, "files.txt"))
	if err != nil {
		t.Fatalf("read files.txt: %v", err)
	}

	var dumps []docDump
	engineByFile := map[string][]DocLine{}
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

		engineFields := map[string]string{}
		for field, e := range dj.Engine {
			if e.Value != nil {
				engineFields[field] = *e.Value
			}
		}
		dumps = append(dumps, docDump{File: dj.File, Set: dj.Set, TextChars: dj.TextChars, Engine: engineFields})
		engineByFile[dj.File] = aliEngineRows(dj.EngineLines)
	}
	return dumps, engineByFile
}

// aliLineRecordJSON decodes one answers.jsonl or answers.lines.jsonl record. Lines is a pointer,
// exactly as aliKeyDocJSON.Rows is, so a real zero-row answer ("lines": []) stays distinguishable
// from an absent lines key. There is no fields member: a lines record carries none, and a
// combined record's header answers are captured by run.py but scored by nothing here.
type aliLineRecordJSON struct {
	File     string                `json:"file"`
	Model    string                `json:"model"`
	Purpose  string                `json:"purpose"`
	Run      int                   `json:"run"`
	Refused  bool                  `json:"refused"`
	Error    string                `json:"error"`
	Cost     float64               `json:"cost"`
	LatencyS *float64              `json:"latency_s"`
	Lines    *[]map[string]*string `json:"lines"`
}

// aliCallOutcome classifies one decoded record. A header record carries no lines key by
// construction (run.py only fills "lines" for lines/combined purposes), so the blank check only
// ever applies to a line-bearing purpose -- else every good header call would misclassify blank.
func aliCallOutcome(rec aliLineRecordJSON) string {
	if rec.Refused {
		return aliCallRefused
	}
	if rec.Error != "" {
		return aliCallTransport
	}
	if rec.Purpose != aliPurposeHeader && rec.Lines == nil {
		return aliCallBlank
	}
	return aliCallGood
}

// aliDecodeLineRows converts one good record's raw role maps into DocLines, in printed order.
// An unknown role fails loudly, mirroring aliLoadKey's own ladder.
func aliDecodeLineRows(rows []map[string]*string) ([]DocLine, error) {
	out := make([]DocLine, 0, len(rows))
	for i, rowMap := range rows {
		line := DocLine{Index: i + 1}
		for role, v := range rowMap {
			if !containsString(LineRoles, role) {
				return nil, fmt.Errorf("row %d carries role %q, which is not one of LineRoles", i+1, role)
			}
			switch role {
			case LineRoleDescription:
				line.Description = v
			case LineRoleQuantity:
				line.Quantity = v
			case LineRoleUnitPrice:
				line.UnitPrice = v
			case LineRoleLineTotal:
				line.LineTotal = v
			case LineRoleLineTax:
				line.LineTax = v
			}
		}
		out = append(out, line)
	}
	return out, nil
}

// aliLoadLineAnswers reads answers.lines.jsonl: run.py sends only the lines and combined
// purposes there. A record of any other purpose is an operator error (mixed-up files) and fails
// loudly, naming the line.
func aliLoadLineAnswers(path string) ([]aliLineAnswer, []aliCallMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read line answers %s: %w", path, err)
	}
	var answers []aliLineAnswer
	var calls []aliCallMeta
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec aliLineRecordJSON
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, nil, fmt.Errorf("line answers %s: line %d: %w", path, i+1, err)
		}
		if rec.Purpose != aliPurposeLines && rec.Purpose != aliPurposeCombined {
			return nil, nil, fmt.Errorf("line answers %s: line %d: purpose %q is neither %q nor %q",
				path, i+1, rec.Purpose, aliPurposeLines, aliPurposeCombined)
		}
		outcome := aliCallOutcome(rec)
		calls = append(calls, aliCallMeta{
			File: rec.File, Model: rec.Model, Purpose: rec.Purpose, Run: rec.Run,
			Outcome: outcome, Cost: rec.Cost, LatencyS: rec.LatencyS,
		})
		if outcome != aliCallGood {
			continue
		}
		rows, err := aliDecodeLineRows(*rec.Lines)
		if err != nil {
			return nil, nil, fmt.Errorf("line answers %s: line %d: %w", path, i+1, err)
		}
		answers = append(answers, aliLineAnswer{File: rec.File, Model: rec.Model, Purpose: rec.Purpose, Run: rec.Run, Rows: rows})
	}
	return answers, calls, nil
}

// aliLoadHeaderCalls reads answers.jsonl for the separate arm's header leg only. A record
// carrying no purpose key is a header record, matching run.py's own done_keys() default.
func aliLoadHeaderCalls(path string) ([]aliCallMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read header answers %s: %w", path, err)
	}
	var calls []aliCallMeta
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec aliLineRecordJSON
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("header answers %s: line %d: %w", path, i+1, err)
		}
		if rec.Purpose == "" {
			rec.Purpose = aliPurposeHeader
		}
		calls = append(calls, aliCallMeta{
			File: rec.File, Model: rec.Model, Purpose: rec.Purpose, Run: rec.Run,
			Outcome: aliCallOutcome(rec), Cost: rec.Cost, LatencyS: rec.LatencyS,
		})
	}
	return calls, nil
}

// --- the driver --------------------------------------------------------------------------

// TestAIText_ScoreLines is Stage E for line items: aliLoadKey, then the dumps/textless union,
// then aliCheckKeyCoverage before either answers file is opened, then the two loaders, then
// aliBuildSummary, in that order.
func TestAIText_ScoreLines(t *testing.T) {
	out := os.Getenv("AIMT_OUT")
	manifestPath := os.Getenv("AIMT_MANIFEST")
	lineAnswersPath := os.Getenv("AIMT_LINE_ANSWERS")
	headerAnswersPath := os.Getenv("AIMT_ANSWERS")
	keyPath := os.Getenv("AIMT_LINE_KEY")
	if out == "" || manifestPath == "" || lineAnswersPath == "" || headerAnswersPath == "" || keyPath == "" {
		t.Log("AIMT_OUT/AIMT_MANIFEST/AIMT_LINE_ANSWERS/AIMT_ANSWERS/AIMT_LINE_KEY unset: no read, no call")
		return
	}

	key, err := aliLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	manifest := aitReadManifest(t, manifestPath)
	notScored := make([]notScoredEntry, 0, len(manifest.NotScored))
	for _, e := range manifest.NotScored {
		notScored = append(notScored, notScoredEntry{File: e.File, Reason: e.Reason})
	}

	dumps, engineRows := aliReadDumps(t, out)
	dumps = append(dumps, aitReadTextlessDumps(t, out)...)
	aliCheckKeyCoverage(t, key, dumps, notScored)

	lineAnswers, lineCalls, err := aliLoadLineAnswers(lineAnswersPath)
	if err != nil {
		t.Fatalf("aliLoadLineAnswers: %v", err)
	}
	headerCalls, err := aliLoadHeaderCalls(headerAnswersPath)
	if err != nil {
		t.Fatalf("aliLoadHeaderCalls: %v", err)
	}
	calls := append(append([]aliCallMeta(nil), headerCalls...), lineCalls...)

	summary, cells := aliBuildSummary(key, dumps, engineRows, lineAnswers, calls, notScored)
	if len(summary.Models) > 1 {
		t.Fatalf("answers carry %d model(s) %v, want at most 1 -- pooling would silently produce a wrong headline", len(summary.Models), summary.Models)
	}
	if len(cells) == 0 {
		t.Fatal("aliBuildSummary produced 0 cells; a zero-cell write reads exactly like a clean run")
	}

	if err := aitWriteJSON(filepath.Join(out, "line_cells.json"), cells); err != nil {
		t.Fatalf("write line_cells.json: %v", err)
	}
	if err := aitWriteJSON(filepath.Join(out, "line_summary.json"), summary); err != nil {
		t.Fatalf("write line_summary.json: %v", err)
	}
	t.Logf("scored %d line cell(s); %d unconfirmed; %d textless; %d not read; census %v",
		len(cells), len(summary.Unconfirmed), len(summary.Textless), len(summary.NotRead), summary.Census)
}
