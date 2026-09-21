// Specs for AIR-08-05's Stage E line-item scoring: aliBuildSummary, aliBuildArms,
// aliBuildCensus, the three loaders, and the env-gated driver. Helpers live in
// ailines_score_internal_test.go. Fixtures below are synthetic (ALPHA/BRAVO/WIDGET); none is a
// real document name.
package extraction

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// --- fixture builders --------------------------------------------------------------------

func aliLat(v float64) *float64 { return &v }

// aliDoc looks up one file's aliDocJSON entry, Fatal if absent.
func aliDoc(t *testing.T, sum aliSummaryJSON, file string) aliDocJSON {
	t.Helper()
	d, ok := sum.Documents[file]
	if !ok {
		t.Fatalf("no Documents entry for %q", file)
	}
	return d
}

// aliReaderRow looks up one reader's aliReaderDocJSON entry within a document, Fatal if absent.
func aliReaderRow(t *testing.T, doc aliDocJSON, reader string) aliReaderDocJSON {
	t.Helper()
	r, ok := doc.Readers[reader]
	if !ok {
		t.Fatalf("no Readers entry for %q", reader)
	}
	return r
}

// aliCensusRow looks up one purpose's aliCensusRowJSON row, Fatal if absent.
func aliCensusRow(t *testing.T, rows []aliCensusRowJSON, purpose string) aliCensusRowJSON {
	t.Helper()
	for _, r := range rows {
		if r.Purpose == purpose {
			return r
		}
	}
	t.Fatalf("no census row for purpose %q", purpose)
	return aliCensusRowJSON{}
}

// F-A: the only fixture that forces all three alignment passes to fire.
func aliFAKey() []DocLine {
	return []DocLine{
		aliTitleLine(1, "ALPHA", "100.00"),
		aliTitleLine(2, "BRAVO", "200.00"),
		aliTitleLine(3, "CHARLIE", "300.00"),
	}
}

func aliFAAnswer() []DocLine {
	return []DocLine{
		aliTitleLine(1, "ALPHA", "100.00"),
		aliTitleLine(2, "BRAVQ", "200.00"),
		aliTitleLine(3, "CHARLEY", "350.00"),
	}
}

// F-B: the engine drops row 2, the AI reader returns all three -- the only fixture that
// exercises DeltaUnpairedRows.
func aliFBKey() []DocLine {
	return []DocLine{
		aliTitleLine(1, "ALPHA", "100.00"),
		aliTitleLine(2, "BRAVO", "200.00"),
		aliTitleLine(3, "CHARLIE", "300.00"),
	}
}

func aliFBEngineRows() []DocLine {
	return []DocLine{
		aliTitleLine(1, "ALPHA", "100.00"),
		aliTitleLine(2, "CHARLIE", "300.00"),
	}
}

func aliFBAIRows() []DocLine {
	return []DocLine{
		aliTitleLine(1, "ALPHA", "100.00"),
		aliTitleLine(2, "BRAVO", "200.00"),
		aliTitleLine(3, "CHARLIE", "300.00"),
	}
}

// F-C: the engine misses unit_price, the AI reads it -- the Zoho shape reproduced synthetically.
// line_tax is absent on every row (aliMenuLine never sets it), so both readers null-agree there.
func aliFCKey() []DocLine {
	return []DocLine{aliMenuLine(1, "WIDGET", "2", "1000.00", "2000.00")}
}

func aliFCEngineRows() []DocLine {
	row := aliMenuLine(1, "WIDGET", "2", "1000.00", "2000.00")
	row.UnitPrice = nil
	return []DocLine{row}
}

func aliFCAIRows() []DocLine {
	return []DocLine{aliMenuLine(1, "WIDGET", "2", "1000.00", "2000.00")}
}

// F-D: runs 1 and 2 return 3 rows, run 3 returns 4 (an extra totals-band row).
func aliFDKey() []DocLine {
	return []DocLine{
		aliTitleLine(1, "ALPHA", "100.00"),
		aliTitleLine(2, "BRAVO", "200.00"),
		aliTitleLine(3, "CHARLIE", "300.00"),
	}
}

func aliFDRun(n int) []DocLine {
	rows := []DocLine{
		aliTitleLine(1, "ALPHA", "100.00"),
		aliTitleLine(2, "BRAVO", "200.00"),
		aliTitleLine(3, "CHARLIE", "300.00"),
	}
	if n == 3 {
		rows = append(rows, aliTitleLine(4, "TOTAL", "600.00"))
	}
	return rows
}

// F-E: cost/latency arms. Latencies (4.0s/9.0s) and combined costs (0.0040/0.0060) are
// deliberately unequal -- a tie cannot discriminate a sum from a max, or x=c from x+=c.
func aliFECalls() []aliCallMeta {
	return []aliCallMeta{
		{File: "fe.pdf", Model: "m", Purpose: aliPurposeHeader, Run: 1, Outcome: aliCallGood, Cost: 0.0010, LatencyS: aliLat(4.0)},
		{File: "fe.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Outcome: aliCallGood, Cost: 0.0030, LatencyS: aliLat(9.0)},
		{File: "fe.pdf", Model: "m", Purpose: aliPurposeCombined, Run: 1, Outcome: aliCallGood, Cost: 0.0040, LatencyS: aliLat(11.0)},
		{File: "fe.pdf", Model: "m", Purpose: aliPurposeHeader, Run: 2, Outcome: aliCallGood, Cost: 0.0020, LatencyS: aliLat(5.0)},
		{File: "fe.pdf", Model: "m", Purpose: aliPurposeCombined, Run: 2, Outcome: aliCallGood, Cost: 0.0060, LatencyS: aliLat(12.0)},
	}
}

// F-F: census. lines run1 good, run2 refused, run3 transport error. combined run1 transport
// error, run2 blank (no lines key, no error), run3 absent entirely.
func aliFFCalls() []aliCallMeta {
	return []aliCallMeta{
		{File: "ff.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Outcome: aliCallGood, Cost: 0.001, LatencyS: aliLat(1)},
		{File: "ff.pdf", Model: "m", Purpose: aliPurposeLines, Run: 2, Outcome: aliCallRefused, Cost: 0.001, LatencyS: aliLat(1)},
		{File: "ff.pdf", Model: "m", Purpose: aliPurposeLines, Run: 3, Outcome: aliCallTransport},
		{File: "ff.pdf", Model: "m", Purpose: aliPurposeCombined, Run: 1, Outcome: aliCallTransport},
		{File: "ff.pdf", Model: "m", Purpose: aliPurposeCombined, Run: 2, Outcome: aliCallBlank, Cost: 0.001, LatencyS: aliLat(1)},
	}
}

// F-G: partition. X confirmed/2 rows/text, Y unconfirmed/2 rows/text, Z confirmed/1 row/textless.
func aliFGKey() aliKey {
	return aliKey{Docs: map[string]aliKeyDoc{
		"fg-x.pdf": {Confirmed: true, Rows: []DocLine{aliTitleLine(1, "ALPHA", "1.00"), aliTitleLine(2, "BRAVO", "2.00")}},
		"fg-y.pdf": {Confirmed: false, Rows: []DocLine{aliTitleLine(1, "ALPHA", "1.00"), aliTitleLine(2, "BRAVO", "2.00")}},
		"fg-z.pdf": {Confirmed: true, Rows: []DocLine{aliTitleLine(1, "ALPHA", "1.00")}},
	}}
}

func aliFGDumps() []docDump {
	return []docDump{
		{File: "fg-x.pdf", Set: "corpus", TextChars: 500},
		{File: "fg-y.pdf", Set: "corpus", TextChars: 500},
		{File: "fg-z.pdf", Set: "corpus", TextChars: 0},
	}
}

func aliFGNotScored() []notScoredEntry {
	return []notScoredEntry{{File: "fg-w.pdf", Reason: "unreadable"}}
}

// --- T1, T2: the driver's env gate (AC-1), graded by a source scan -- mutation replay runs an
// env-gated body with nothing set, so no mutation inside it can ever fail under execution.

func aliScoreDriverSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("ailines_score_internal_test.go")
	if err != nil {
		t.Fatalf("read ailines_score_internal_test.go: %v", err)
	}
	return string(b)
}

func TestAliScoreLines_TheDriverReadsExactlyItsFiveVariables(t *testing.T) {
	src := aliScoreDriverSource(t)
	aliFloor(t, len(src), "driver source byte(s)")

	want := []string{
		`os.Getenv("AIMT_OUT")`, `os.Getenv("AIMT_MANIFEST")`, `os.Getenv("AIMT_LINE_ANSWERS")`,
		`os.Getenv("AIMT_ANSWERS")`, `os.Getenv("AIMT_LINE_KEY")`,
	}
	for _, w := range want {
		if !strings.Contains(src, w) {
			t.Errorf("driver source is missing %q", w)
		}
	}
}

func TestAliScoreLines_TheUnsetGateLogsOnceAndReturns(t *testing.T) {
	src := aliScoreDriverSource(t)
	aliFloor(t, len(src), "driver source byte(s)")

	gate := `if out == "" || manifestPath == "" || lineAnswersPath == "" || headerAnswersPath == "" || keyPath == "" {`
	idx := strings.Index(src, gate)
	if idx < 0 {
		t.Fatalf("driver source is missing the five-clause gate line %q", gate)
	}
	for _, name := range []string{"out", "manifestPath", "lineAnswersPath", "headerAnswersPath", "keyPath"} {
		if !strings.Contains(gate, name) {
			t.Errorf("gate line does not test %q", name)
		}
	}

	rest := src[idx+len(gate):]
	logIdx := strings.Index(rest, "t.Log(")
	returnIdx := strings.Index(rest, "return")
	if logIdx < 0 || returnIdx < 0 || logIdx > returnIdx {
		t.Error("gate body must call t.Log(...) then return, in that order")
	}
	if strings.Contains(rest[:max(returnIdx, 0)], "t.Skip") {
		t.Error("gate body must never call t.Skip")
	}
}

// --- T3-T5: per reader, per run (AC-2) ----------------------------------------------------

func TestAliBuildSummary_EachReaderGetsItsOwnRowOutcomesPerDocument(t *testing.T) {
	key := aliKey{Docs: map[string]aliKeyDoc{"fb.pdf": {Confirmed: true, Rows: aliFBKey()}}}
	dumps := []docDump{{File: "fb.pdf", Set: "corpus", TextChars: 500}}
	engine := map[string][]DocLine{"fb.pdf": aliFBEngineRows()}
	answers := []aliLineAnswer{
		{File: "fb.pdf", Model: "m", Purpose: aliPurposeCombined, Run: 1, Rows: aliFBAIRows()},
		{File: "fb.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Rows: aliFBAIRows()},
	}
	calls := []aliCallMeta{
		{File: "fb.pdf", Model: "m", Purpose: aliPurposeCombined, Run: 1, Outcome: aliCallGood},
		{File: "fb.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Outcome: aliCallGood},
	}
	sum, _ := aliBuildSummary(key, dumps, engine, answers, calls, nil)

	doc := aliDoc(t, sum, "fb.pdf")
	aliFloor(t, len(doc.Readers), "reader(s)")
	if len(doc.Readers) != 3 {
		t.Fatalf("len(doc.Readers) = %d, want 3 (engine, combined, lines)", len(doc.Readers))
	}
	for _, reader := range []string{aliReaderEngine, aliPurposeCombined, aliPurposeLines} {
		rr := aliReaderRow(t, doc, reader)
		aliFloor(t, len(rr.Runs), "run(s)")
	}

	engineRow := aliReaderRow(t, doc, aliReaderEngine)
	if got := engineRow.Runs[0].Found; got != 2 {
		t.Errorf("engine Found = %d, want 2", got)
	}
	if got := engineRow.Runs[0].Dropped; got != 1 {
		t.Errorf("engine Dropped = %d, want 1", got)
	}

	linesRow := aliReaderRow(t, doc, aliPurposeLines)
	if got := linesRow.Runs[0].Found; got != 3 {
		t.Errorf("lines Found = %d, want 3", got)
	}
}

func TestAliBuildSummary_ThePassThatMadeEachPairIsReported(t *testing.T) {
	key := aliKey{Docs: map[string]aliKeyDoc{"fa.pdf": {Confirmed: true, Rows: aliFAKey()}}}
	dumps := []docDump{{File: "fa.pdf", Set: "corpus", TextChars: 500}}
	engine := map[string][]DocLine{}
	answers := []aliLineAnswer{{File: "fa.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Rows: aliFAAnswer()}}
	calls := []aliCallMeta{{File: "fa.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Outcome: aliCallGood}}
	sum, _ := aliBuildSummary(key, dumps, engine, answers, calls, nil)

	doc := aliDoc(t, sum, "fa.pdf")
	linesRow := aliReaderRow(t, doc, aliPurposeLines)
	aliFloor(t, len(linesRow.Runs), "run(s)")
	run := linesRow.Runs[0]
	aliFloor(t, len(run.PassPairs), "pass pair(s)")

	if run.Found != 2 || run.Misaligned != 1 {
		t.Errorf("Found=%d Misaligned=%d, want 2 and 1", run.Found, run.Misaligned)
	}
	want := map[string]int{"1": 1, "2": 1, "3": 1}
	if len(run.PassPairs) != len(want) {
		t.Fatalf("PassPairs = %v, want %v", run.PassPairs, want)
	}
	for k, v := range want {
		if run.PassPairs[k] != v {
			t.Errorf("PassPairs[%q] = %d, want %d", k, run.PassPairs[k], v)
		}
	}
}

func TestAliBuildSummary_ThreeRunsDisagreeingOnRowCountAreReportedNotAveraged(t *testing.T) {
	key := aliKey{Docs: map[string]aliKeyDoc{"fd.pdf": {Confirmed: true, Rows: aliFDKey()}}}
	dumps := []docDump{{File: "fd.pdf", Set: "corpus", TextChars: 500}}
	engine := map[string][]DocLine{}
	var answers []aliLineAnswer
	var calls []aliCallMeta
	for run := 1; run <= 3; run++ {
		answers = append(answers, aliLineAnswer{File: "fd.pdf", Model: "m", Purpose: aliPurposeLines, Run: run, Rows: aliFDRun(run)})
		calls = append(calls, aliCallMeta{File: "fd.pdf", Model: "m", Purpose: aliPurposeLines, Run: run, Outcome: aliCallGood})
	}
	sum, _ := aliBuildSummary(key, dumps, engine, answers, calls, nil)

	doc := aliDoc(t, sum, "fd.pdf")
	linesRow := aliReaderRow(t, doc, aliPurposeLines)
	aliFloor(t, len(linesRow.Runs), "run(s)")
	if len(linesRow.Runs) != 3 {
		t.Fatalf("len(Runs) = %d, want 3", len(linesRow.Runs))
	}
	if linesRow.DistinctRowCounts != 2 {
		t.Errorf("DistinctRowCounts = %d, want 2 (3, 3, 4 -- never averaged to 3.33)", linesRow.DistinctRowCounts)
	}
}

func TestAliBuildSummary_ModelsListsTheDistinctModelsSeenInCalls(t *testing.T) {
	calls := []aliCallMeta{
		{File: "x.pdf", Model: "gpt", Purpose: aliPurposeHeader, Run: 1, Outcome: aliCallGood},
		{File: "x.pdf", Model: "gemini", Purpose: aliPurposeLines, Run: 1, Outcome: aliCallGood},
		{File: "x.pdf", Model: "gemini", Purpose: aliPurposeLines, Run: 2, Outcome: aliCallGood},
	}
	sum, _ := aliBuildSummary(aliKey{Docs: map[string]aliKeyDoc{}}, nil, map[string][]DocLine{}, nil, calls, nil)
	aliFloor(t, len(sum.Models), "model(s)")
	if len(sum.Models) != 2 {
		t.Fatalf("Models = %v, want 2 distinct models, not 3 (no dedup) or 1 (overwritten)", sum.Models)
	}
	if sum.Models[0] != "gemini" || sum.Models[1] != "gpt" {
		t.Errorf("Models = %v, want sorted [gemini gpt]", sum.Models)
	}
}

// The driver's own refusal on more than one model can only run under a hand-run env (F-7's
// admission), so it is graded by source scan, the same tool T1/T2 use for AC-1's driver body.
func TestAliScoreLines_TheDriverRefusesMoreThanOneModel(t *testing.T) {
	src := aliScoreDriverSource(t)
	aliFloor(t, len(src), "driver source byte(s)")

	guard := `if len(summary.Models) > 1 {`
	idx := strings.Index(src, guard)
	if idx < 0 {
		t.Fatalf("driver source is missing the multi-model refusal guard %q", guard)
	}
	rest := src[idx+len(guard):]
	fatalIdx := strings.Index(rest, "t.Fatalf(")
	closeIdx := strings.Index(rest, "\n\t}")
	if fatalIdx < 0 || closeIdx < 0 || fatalIdx > closeIdx {
		t.Error("multi-model guard body must call t.Fatalf before the block closes")
	}
	if !strings.Contains(rest, "pooling would silently produce a wrong headline") {
		t.Error("multi-model guard's Fatalf message is missing or was edited away from its own guard")
	}
}

// --- T6-T8: role and denominator counting (AC-3) ------------------------------------------

func aliFCSummary(t *testing.T) aliSummaryJSON {
	t.Helper()
	key := aliKey{Docs: map[string]aliKeyDoc{"fc.pdf": {Confirmed: true, Rows: aliFCKey()}}}
	dumps := []docDump{{File: "fc.pdf", Set: "corpus", TextChars: 500}}
	engine := map[string][]DocLine{"fc.pdf": aliFCEngineRows()}
	answers := []aliLineAnswer{{File: "fc.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Rows: aliFCAIRows()}}
	calls := []aliCallMeta{{File: "fc.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Outcome: aliCallGood}}
	sum, _ := aliBuildSummary(key, dumps, engine, answers, calls, nil)
	return sum
}

func TestAliBuildSummary_PerRoleValueCellsAndCorrectNullsAreCountedSeparately(t *testing.T) {
	sum := aliFCSummary(t)
	aliFloor(t, len(sum.Roles), "role row(s)")

	var lines, engine *aliRoleRowJSON
	for i := range sum.Roles {
		r := &sum.Roles[i]
		if r.Role != LineRoleUnitPrice {
			continue
		}
		switch r.Reader {
		case aliPurposeLines:
			lines = r
		case aliReaderEngine:
			engine = r
		}
	}
	if lines == nil || engine == nil {
		t.Fatalf("missing unit_price role row(s) for lines/engine: %v", sum.Roles)
	}
	if lines.ValueRight != 1 {
		t.Errorf("lines unit_price ValueRight = %d, want 1", lines.ValueRight)
	}
	if engine.ValueMissing != 1 {
		t.Errorf("engine unit_price ValueMissing = %d, want 1", engine.ValueMissing)
	}

	var linesTax *aliRoleRowJSON
	for i := range sum.Roles {
		if sum.Roles[i].Role == LineRoleLineTax && sum.Roles[i].Reader == aliPurposeLines {
			linesTax = &sum.Roles[i]
		}
	}
	if linesTax == nil {
		t.Fatal("missing line_tax role row for lines")
	}
	if linesTax.NullRight != 1 {
		t.Errorf("lines line_tax NullRight = %d, want 1", linesTax.NullRight)
	}
}

func TestAliBuildSummary_ADroppedKeyRowsCellsAreMissingNotCorrectNulls(t *testing.T) {
	key := aliKey{Docs: map[string]aliKeyDoc{"fb.pdf": {Confirmed: true, Rows: aliFBKey()}}}
	dumps := []docDump{{File: "fb.pdf", Set: "corpus", TextChars: 500}}
	engine := map[string][]DocLine{"fb.pdf": aliFBEngineRows()}
	sum, cells := aliBuildSummary(key, dumps, engine, nil, nil, nil)
	aliFloor(t, len(cells), "cell(s)")

	var droppedTax *aliCellJSON
	for i := range cells {
		c := &cells[i]
		if c.Reader == aliReaderEngine && c.Outcome == "dropped" && c.Role == LineRoleLineTax && c.KeyRow == 2 {
			droppedTax = c
		}
	}
	if droppedTax == nil {
		t.Fatalf("no dropped line_tax cell for key row 2: %v", cells)
	}
	if droppedTax.Verdict != aliMissing {
		t.Errorf("dropped row's line_tax verdict = %q, want %q (never a correct null)", droppedTax.Verdict, aliMissing)
	}
	_ = sum
}

func TestAliBuildSummary_TheRoleCountersPartitionTheDenominators(t *testing.T) {
	sumA := aliFCSummary(t)
	key := aliKey{Docs: map[string]aliKeyDoc{"fa.pdf": {Confirmed: true, Rows: aliFAKey()}}}
	dumps := []docDump{{File: "fa.pdf", Set: "corpus", TextChars: 500}}
	answers := []aliLineAnswer{{File: "fa.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Rows: aliFAAnswer()}}
	calls := []aliCallMeta{{File: "fa.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Outcome: aliCallGood}}
	sumB, _ := aliBuildSummary(key, dumps, map[string][]DocLine{}, answers, calls, nil)

	for _, sum := range []aliSummaryJSON{sumA, sumB} {
		aliFloor(t, len(sum.Denominators), "denominator row(s)")
		for _, denom := range sum.Denominators {
			var valueSum, nullSum int
			for _, role := range sum.Roles {
				if role.Reader != denom.Reader {
					continue
				}
				valueSum += role.ValueRight + role.ValueWrong + role.ValueMissing
				nullSum += role.NullRight + role.NullWrong + role.NullMissing
			}
			if valueSum != denom.ValueCells {
				t.Errorf("%s: role value counters sum to %d, denom.ValueCells = %d", denom.Reader, valueSum, denom.ValueCells)
			}
			if nullSum != denom.NullCells {
				t.Errorf("%s: role null counters sum to %d, denom.NullCells = %d", denom.Reader, nullSum, denom.NullCells)
			}
		}
	}
}

// --- T9-T11: delta (AC-4) ------------------------------------------------------------------

func TestAliBuildSummary_AUnitPriceOnlyTheAIReadsIsAnAIOnlyTransition(t *testing.T) {
	sum := aliFCSummary(t)
	aliFloor(t, len(sum.Delta), "delta row(s)")

	var unitPrice *aliDeltaRowJSON
	for i := range sum.Delta {
		d := &sum.Delta[i]
		if d.Reader == aliPurposeLines && d.Role == LineRoleUnitPrice && d.Cells == "value" {
			unitPrice = d
		}
	}
	if unitPrice == nil {
		t.Fatalf("no lines/unit_price/value delta row: %v", sum.Delta)
	}
	if unitPrice.AIOnly != 1 {
		t.Errorf("unit_price AIOnly = %d, want 1", unitPrice.AIOnly)
	}
}

func TestAliBuildSummary_TheFourTransitionsPartitionEveryPairedRoleCell(t *testing.T) {
	sum := aliFCSummary(t)
	aliFloor(t, len(sum.Delta), "delta row(s)")
	for _, d := range sum.Delta {
		total := d.BothRight + d.AIOnly + d.EngineOnly + d.BothWrong
		if total == 0 {
			t.Errorf("%s/%s/%s: all four transitions are zero", d.Reader, d.Role, d.Cells)
		}
	}
}

func TestAliBuildSummary_AKeyRowOnlyOneReaderPairedIsCountedNotDropped(t *testing.T) {
	key := aliKey{Docs: map[string]aliKeyDoc{"fb.pdf": {Confirmed: true, Rows: aliFBKey()}}}
	dumps := []docDump{{File: "fb.pdf", Set: "corpus", TextChars: 500}}
	engine := map[string][]DocLine{"fb.pdf": aliFBEngineRows()}
	answers := []aliLineAnswer{{File: "fb.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Rows: aliFBAIRows()}}
	calls := []aliCallMeta{{File: "fb.pdf", Model: "m", Purpose: aliPurposeLines, Run: 1, Outcome: aliCallGood}}
	sum, _ := aliBuildSummary(key, dumps, engine, answers, calls, nil)

	aliFloor(t, sum.DeltaUnpairedRows, "unpaired delta row(s)")
	if sum.DeltaUnpairedRows != 1 {
		t.Errorf("DeltaUnpairedRows = %d, want 1", sum.DeltaUnpairedRows)
	}
}

// --- T12-T14: arms (AC-5) ------------------------------------------------------------------

func TestAliBuildSummary_TheSameCallArmIsReportedUnsummed(t *testing.T) {
	arms := aliBuildArms(aliFECalls())
	aliFloor(t, arms.SameCall.Calls, "same-call call(s)")
	if arms.SameCall.Calls != 2 {
		t.Errorf("SameCall.Calls = %d, want 2", arms.SameCall.Calls)
	}
	if diff := arms.SameCall.TotalCost - 0.0100; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("SameCall.TotalCost = %v, want 0.0100", arms.SameCall.TotalCost)
	}
}

func TestAliBuildSummary_TheSeparateArmsLatencyIsBothSummedAndMaxed(t *testing.T) {
	arms := aliBuildArms(aliFECalls())
	aliFloor(t, arms.SameCall.Calls, "same-call call(s)")
	if diff := arms.SeparateLatencySequential.Total - 13.0; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("SeparateLatencySequential.Total = %v, want 13.0", arms.SeparateLatencySequential.Total)
	}
	if diff := arms.SeparateLatencyParallel.Total - 9.0; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("SeparateLatencyParallel.Total = %v, want 9.0", arms.SeparateLatencyParallel.Total)
	}
}

func TestAliBuildSummary_AnUnpairedSeparateLegIsCountedNotDropped(t *testing.T) {
	arms := aliBuildArms(aliFECalls())
	aliFloor(t, arms.SameCall.Calls, "same-call call(s)")
	if arms.UnpairedLegs != 1 {
		t.Errorf("UnpairedLegs = %d, want 1", arms.UnpairedLegs)
	}
}

// --- T15-T22: census and the record loaders (AC-6) ------------------------------------------

func TestAliCallOutcome_ARefusalABlankAndATransportErrorAreThreeOutcomes(t *testing.T) {
	emptyRows := []map[string]*string{}
	cases := []struct {
		name string
		rec  aliLineRecordJSON
		want string
	}{
		{"refused", aliLineRecordJSON{Purpose: aliPurposeLines, Refused: true, Error: "refused: x"}, aliCallRefused},
		{"transport error", aliLineRecordJSON{Purpose: aliPurposeLines, Error: "HTTP 500: x"}, aliCallTransport},
		{"blank", aliLineRecordJSON{Purpose: aliPurposeLines}, aliCallBlank},
		{"good", aliLineRecordJSON{Purpose: aliPurposeLines, Lines: &emptyRows}, aliCallGood},
	}
	aliFloor(t, len(cases), "case(s)")
	for _, c := range cases {
		if got := aliCallOutcome(c.rec); got != c.want {
			t.Errorf("%s: aliCallOutcome = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAliCallOutcome_AnEmptyRowListIsAGoodAnswerNotABlank(t *testing.T) {
	emptyRows := []map[string]*string{}
	good := aliLineRecordJSON{Purpose: aliPurposeCombined, Lines: &emptyRows}
	blank := aliLineRecordJSON{Purpose: aliPurposeCombined}
	if got := aliCallOutcome(good); got != aliCallGood {
		t.Errorf("empty lines list: aliCallOutcome = %q, want %q", got, aliCallGood)
	}
	if got := aliCallOutcome(blank); got != aliCallBlank {
		t.Errorf("absent lines key: aliCallOutcome = %q, want %q", got, aliCallBlank)
	}
}

func TestAliBuildSummary_AnUnconfirmedDocumentIsListedAndScoresNothing(t *testing.T) {
	sum, _ := aliBuildSummary(aliFGKey(), aliFGDumps(), map[string][]DocLine{}, nil, nil, aliFGNotScored())
	aliFloor(t, len(sum.Documents), "document(s)")

	if _, ok := sum.Documents["fg-y.pdf"]; ok {
		t.Error("unconfirmed document fg-y.pdf must not appear in Documents")
	}
	if len(sum.Unconfirmed) != 1 || sum.Unconfirmed[0] != "fg-y.pdf" {
		t.Errorf("Unconfirmed = %v, want [fg-y.pdf]", sum.Unconfirmed)
	}
	if _, ok := sum.Documents["fg-x.pdf"]; !ok {
		t.Error("confirmed document fg-x.pdf must appear in Documents")
	}
}

func TestAliBuildSummary_ATextlessDocumentIsListedAndItsKeyRowsAreNamed(t *testing.T) {
	sum, _ := aliBuildSummary(aliFGKey(), aliFGDumps(), map[string][]DocLine{}, nil, nil, aliFGNotScored())
	aliFloor(t, len(sum.Textless), "textless document(s)")

	if len(sum.Textless) != 1 || sum.Textless[0] != "fg-z.pdf" {
		t.Errorf("Textless = %v, want [fg-z.pdf]", sum.Textless)
	}
	if len(sum.TextlessWithKeyRows) != 1 || sum.TextlessWithKeyRows[0] != "fg-z.pdf" {
		t.Errorf("TextlessWithKeyRows = %v, want [fg-z.pdf] (fg-z.pdf is confirmed with 1 key row)", sum.TextlessWithKeyRows)
	}
}

func TestAliBuildSummary_AMissingRunIsCountedNeverSilentlyDropped(t *testing.T) {
	rows := aliBuildCensus(aliFFCalls(), 1)
	aliFloor(t, len(rows), "census row(s)")
	combined := aliCensusRow(t, rows, aliPurposeCombined)
	if combined.MissingRuns != 1 {
		t.Errorf("combined.MissingRuns = %d, want 1", combined.MissingRuns)
	}
}

func TestAliBuildSummary_ARefusalIsCountedApartFromATransportError(t *testing.T) {
	rows := aliBuildCensus(aliFFCalls(), 1)
	aliFloor(t, len(rows), "census row(s)")
	lines := aliCensusRow(t, rows, aliPurposeLines)
	if lines.Refused != 1 {
		t.Errorf("lines.Refused = %d, want 1", lines.Refused)
	}
	if lines.TransportError != 1 {
		t.Errorf("lines.TransportError = %d, want 1", lines.TransportError)
	}
}

func TestAliLoadLineAnswers_ARecordOfAnUnknownPurposeFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "answers.lines.jsonl")
	line := `{"file":"x.pdf","model":"m","purpose":"header","run":1}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := aliLoadLineAnswers(path); err == nil {
		t.Fatal("aliLoadLineAnswers accepted a record whose purpose is neither lines nor combined")
	} else if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error = %q, want it to name line 1", err.Error())
	}
}

func TestAliLoadLineAnswers_ACombinedRecordWithNoFieldsKeyStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "answers.lines.jsonl")
	line := `{"file":"x.pdf","model":"m","purpose":"combined","run":1,"lines":[{"description":"ALPHA","line_total":"1.00"}]}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	answers, calls, err := aliLoadLineAnswers(path)
	if err != nil {
		t.Fatalf("aliLoadLineAnswers: %v", err)
	}
	aliFloor(t, len(answers), "answer(s)")
	aliFloor(t, len(calls), "call(s)")
	if answers[0].Purpose != aliPurposeCombined {
		t.Errorf("Purpose = %q, want %q", answers[0].Purpose, aliPurposeCombined)
	}
	if answers[0].Rows[0].Description == nil || *answers[0].Rows[0].Description != "ALPHA" {
		t.Error("combined record's row was not decoded")
	}
}

// --- T23: aliEngineRows (AC-2) ---------------------------------------------------------------

func TestAliEngineRows_AGapInTheIndexesLeavesNoPlaceholderRow(t *testing.T) {
	entries := []aliEngineLineJSON{
		{Index: 1, Roles: map[string]aitDumpEngineJSON{LineRoleDescription: {Value: aliStr("ALPHA")}}},
		{Index: 3, Roles: map[string]aitDumpEngineJSON{LineRoleDescription: {Value: aliStr("CHARLIE")}}},
	}
	rows := aliEngineRows(entries)
	aliFloor(t, len(rows), "row(s)")
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 -- a gap must not insert a placeholder", len(rows))
	}
	if rows[0].Index != 1 || rows[1].Index != 3 {
		t.Errorf("Index = [%d,%d], want [1,3]", rows[0].Index, rows[1].Index)
	}
	if rows[0].Description == nil || *rows[0].Description != "ALPHA" {
		t.Error("rows[0].Description was not carried through")
	}
}

// --- T24, T25: the floor helper and its own static coverage (AC-7) ---------------------------

func TestAliFloorProblem_ZeroIsAProblemAndOneIsNot(t *testing.T) {
	if p := aliFloorProblem(0, "widget(s)"); p == "" {
		t.Error("aliFloorProblem(0, ...) returned empty, want a non-empty message")
	}
	if p := aliFloorProblem(1, "widget(s)"); p != "" {
		t.Errorf("aliFloorProblem(1, ...) = %q, want empty", p)
	}
}

// TestAliScoreSpecs_EveryLoopOverASummaryCollectionHasAFloorAboveIt scans this file's own test
// function bodies: any that ranges over a sum. field or over cells must have an aliFloor( call
// at an earlier byte offset in that same body. Needles are assembled from fragments so this
// scan does not match its own source.
func TestAliScoreSpecs_EveryLoopOverASummaryCollectionHasAFloorAboveIt(t *testing.T) {
	raw, err := os.ReadFile("ailines_score_rules_internal_test.go")
	if err != nil {
		t.Fatalf("read ailines_score_rules_internal_test.go: %v", err)
	}
	src := string(raw)

	parts := strings.Split(src, "\n"+"func Test")
	aliFloor(t, len(parts)-1, "test function(s) in the specs file")

	rangeNeedle := "range " + "sum."
	cellsNeedle := "range " + "cells"
	floorNeedle := "aliFloor" + "("

	var offenders []string
	for _, part := range parts[1:] {
		nameEnd := strings.IndexAny(part, "( ")
		if nameEnd < 0 {
			continue
		}
		name := "Test" + part[:nameEnd]

		rangeIdx := strings.Index(part, rangeNeedle)
		cellsIdx := strings.Index(part, cellsNeedle)
		loopIdx := -1
		switch {
		case rangeIdx < 0 && cellsIdx < 0:
			continue
		case rangeIdx < 0:
			loopIdx = cellsIdx
		case cellsIdx < 0:
			loopIdx = rangeIdx
		case rangeIdx < cellsIdx:
			loopIdx = rangeIdx
		default:
			loopIdx = cellsIdx
		}

		floorIdx := strings.Index(part, floorNeedle)
		if floorIdx < 0 || floorIdx > loopIdx {
			offenders = append(offenders, name)
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("function(s) loop over a summary collection with no aliFloor call above the loop: %v", offenders)
	}
}

// --- T26: the engine baseline on the real dumps (env-gated evidence, never a mutation target) -

func TestAliScoreLines_TheEngineBaselineOnTheRealDumps(t *testing.T) {
	out := os.Getenv("AIMT_OUT")
	keyPath := os.Getenv("AIMT_LINE_KEY")
	if out == "" || keyPath == "" {
		t.Log("AIMT_OUT/AIMT_LINE_KEY unset: no read")
		return
	}

	key, err := aliLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	dumps, engine := aliReadDumps(t, out)
	dumps = append(dumps, aitReadTextlessDumps(t, out)...)
	aliFloor(t, len(dumps), "dump(s)")
	aliFloor(t, len(key.Docs), "key document(s)")

	var found, misaligned, dropped, invented, entries, keyRows int
	perDoc := map[string]int{}
	for _, d := range dumps {
		if d.TextChars == 0 {
			continue
		}
		doc, ok := key.Docs[d.File]
		if !ok || !doc.Confirmed {
			continue
		}
		rows := engine[d.File]
		entries += len(rows)
		keyRows += len(doc.Rows)
		perDoc[d.File] = len(rows)
		al := aliAlign(rows, doc.Rows)
		found += al.Found()
		misaligned += al.Misaligned()
		dropped += len(al.Dropped)
		invented += len(al.Invented)
	}

	// Measured, not asserted: the two partition invariants only. 32 found is a ceiling derived
	// by assuming every same-count document aligns cleanly, never a pinned expectation.
	if got := found + misaligned + dropped; got != keyRows {
		t.Errorf("found+misaligned+dropped = %d, want %d (key rows)", got, keyRows)
	}
	if got := found + misaligned + invented; got != entries {
		t.Errorf("found+misaligned+invented = %d, want %d (engine entries)", got, entries)
	}
	t.Logf("engine baseline: found=%d misaligned=%d dropped=%d invented=%d entries=%d keyRows=%d per-doc=%v",
		found, misaligned, dropped, invented, entries, keyRows, perDoc)
}
