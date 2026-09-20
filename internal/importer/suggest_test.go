// suggest_test.go: AIR-07-01 Mode A red specs for the mapping prompt, its schema and the
// guard. suggest.go is declaration-only (every function returns its zero value; mappingSystem/
// mappingIntro/mappingRowFmt/sampleRows are stub values), so most assertions below fail
// honestly against that stub. A few sub-assertions inside a multi-assertion test coincide with
// the stub's output (e.g. "unplaced" checks against an always-nil guardPlacements) -- the test
// as a whole still fails on its other assertion(s); see the handback report for the full list.
package importer

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// sgFakeAnswer calls mappingSchema through a real *ai.Client in fake mode and returns the
// answer, the call error and the logged call's outcome. Precedent: worker_db_test.go:4322
// (client construction) and :4383-4394 (outcome parsed from the JSON log line).
func sgFakeAnswer(t *testing.T, text string) (map[string]any, error, string) {
	t.Helper()
	t.Setenv(ai.EnvFake, "true")
	t.Setenv(ai.EnvKey, "")
	var buf bytes.Buffer
	c, err := ai.FromEnv(slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("ai.FromEnv: %v", err)
	}
	ans, callErr := c.Call(t.Context(), ai.Request{
		Purpose: ai.PurposeSpreadsheet, System: mappingSystem, Text: text,
		SchemaName: mappingSchemaName, Schema: mappingSchema,
	})
	return ans, callErr, sgOutcome(t, buf.String())
}

// sgOutcome reads the one "ai call" log line's outcome field.
func sgOutcome(t *testing.T, log string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if m["msg"] == "ai call" {
			outcome, _ := m["outcome"].(string)
			return outcome
		}
	}
	t.Fatal(`no "ai call" log line found`)
	return ""
}

// --- AC-1: the mapping prompt -------------------------------------------------------------

// T01 (row 1). Precedent: TestAIPrompt_MatchesTheMeasuredHarness, aireading_internal_test.go:91.
func TestMappingPrompt_MatchesTheMeasuredHarness(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tools", "aimodeltest", "csvrun.py"))
	if err != nil {
		t.Fatalf("read csvrun.py: %v", err)
	}
	text := string(src)

	m := regexp.MustCompile(`(?s)\nSYSTEM = """(.*?)"""`).FindStringSubmatch(text)
	if m == nil {
		t.Fatal("SYSTEM literal not found in csvrun.py")
	}
	if mappingSystem != m[1] {
		t.Errorf("mappingSystem does not equal csvrun.py's SYSTEM byte for byte (got len %d, want len %d)", len(mappingSystem), len(m[1]))
	}

	// intro sits inside user_text, indented four spaces (:61) -- the line-start anchor used
	// for SYSTEM does not transfer; anchor on the indentation instead.
	m = regexp.MustCompile(`(?m)^\s+intro = "(.*?)"$`).FindStringSubmatch(text)
	if m == nil {
		t.Fatal("intro literal not found in csvrun.py")
	}
	if mappingIntro != m[1] {
		t.Errorf("mappingIntro does not equal csvrun.py's intro byte for byte (got %q, want %q)", mappingIntro, m[1])
	}

	rowLit := `f"Row {i}: {csv_line(r)}"`
	if !strings.Contains(text, rowLit) {
		t.Fatalf("csvrun.py no longer contains %q -- the fixed Python-to-Go map below is stale", rowLit)
	}
	pyToGo := strings.NewReplacer("{i}", "%d", "{csv_line(r)}", "%s").Replace
	unwrap := strings.TrimSuffix(strings.TrimPrefix(rowLit, `f"`), `"`)
	if got := pyToGo(unwrap); got != mappingRowFmt {
		t.Errorf("mapped Python literal %q = %q, want mappingRowFmt %q", rowLit, got, mappingRowFmt)
	}
}

// T02 (row 2). AC-3's real oracle: a real *ai.Client in fake mode must accept mappingSchema
// with no refused outcome, answering all 14 properties as nil.
func TestMappingSchema_IsAcceptedByTheAIClient(t *testing.T) {
	ans, err, outcome := sgFakeAnswer(t, "Row 1: a,b")
	if err != nil {
		t.Fatalf("Call: %v (outcome %q)", err, outcome)
	}
	if outcome == "refused" {
		t.Fatalf("outcome = %q, want anything but refused", outcome)
	}
	if outcome != "fake" {
		t.Errorf("outcome = %q, want %q", outcome, "fake")
	}
	if len(ans) != 14 {
		t.Fatalf("answer has %d key(s), want 14: %v", len(ans), ans)
	}
	want := append(append([]string{}, mappingFields...), "header_row", "date_format", "decimal_separator")
	for _, k := range want {
		v, ok := ans[k]
		if !ok {
			t.Errorf("answer missing key %q", k)
			continue
		}
		if v != nil {
			t.Errorf("answer[%q] = %v, want nil (blank answer)", k, v)
		}
	}
}

// T03 (row 3).
func TestMappingPromptText_NumbersRowsFromOneAndQuotesCSV(t *testing.T) {
	rows := [][]string{{"a", "b,c"}, {"d", ""}}
	want := mappingIntro + "\nRow 1: a,\"b,c\"\nRow 2: d,"
	if got := mappingPromptText(rows); got != want {
		t.Errorf("mappingPromptText(%v) = %q, want %q", rows, got, want)
	}
	if got := mappingPromptText(nil); got != mappingIntro {
		t.Errorf("mappingPromptText(nil) = %q, want mappingIntro %q", got, mappingIntro)
	}
}

// T04 (row 4).
func TestSampleRows_MatchesTheMeasuredHarness(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tools", "aimodeltest", "csvrun.py"))
	if err != nil {
		t.Fatalf("read csvrun.py: %v", err)
	}
	m := regexp.MustCompile(`(?m)^SAMPLE_ROWS = (\d+)$`).FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("SAMPLE_ROWS literal not found in csvrun.py")
	}
	want, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse SAMPLE_ROWS: %v", err)
	}
	if sampleRows != want {
		t.Errorf("sampleRows = %d, want %d (csvrun.py's SAMPLE_ROWS)", sampleRows, want)
	}
}

// T05 (row 5).
func TestSuggestWindow_ShortFileYieldsEveryRecordAndNoPadding(t *testing.T) {
	header := []string{"A", "B"}
	rows := [][]string{{"1", "2"}, {"3", "4"}}
	got := suggestWindow(header, rows)
	if len(got) != 3 {
		t.Fatalf("suggestWindow returned %d entries, want 3: %v", len(got), got)
	}
	if !slices.Equal(got[0], header) {
		t.Errorf("suggestWindow[0] = %v, want header %v", got[0], header)
	}
	for i, r := range got {
		if r == nil {
			t.Errorf("suggestWindow[%d] is nil, want a record", i)
		}
	}
}

// T06 (row 6).
func TestSuggestWindow_EmptyDecodeYieldsNoRows(t *testing.T) {
	if got := suggestWindow(nil, nil); got != nil {
		t.Errorf("suggestWindow(nil, nil) = %v, want nil", got)
	}
	if got := suggestWindow([]string{}, [][]string{{"1"}}); got != nil {
		t.Errorf("suggestWindow([]string{}, rows) = %v, want nil", got)
	}
	// A non-blank header_row must still respect an upper bound of 0.
	if got := guardHeaderRow(map[string]any{"header_row": json.Number("3")}, 0); got != defaultHeaderRow {
		t.Errorf("guardHeaderRow(header_row=3, windowLen=0) = %d, want defaultHeaderRow %d", got, defaultHeaderRow)
	}
}

// T07 (row 7).
func TestSuggestWindow_LongFileIsCappedAtWindowRows(t *testing.T) {
	header := []string{"H"}
	rows := make([][]string, 39)
	for i := range rows {
		rows[i] = []string{strconv.Itoa(i)}
	}
	got := suggestWindow(header, rows)
	if len(got) != windowRows {
		t.Fatalf("suggestWindow returned %d entries, want windowRows=%d", len(got), windowRows)
	}
	if !slices.Equal(got[0], header) {
		t.Errorf("suggestWindow[0] = %v, want header %v", got[0], header)
	}
	if !slices.Equal(got[1], rows[0]) {
		t.Errorf("suggestWindow[1] = %v, want rows[0] %v", got[1], rows[0])
	}
}

// --- AC-4: the guard -----------------------------------------------------------------------

// T08 (row 8).
func TestGuardHeaderRow_EveryRowInTheWindowIsReachableAndOneBeyondIsNot(t *testing.T) {
	windowLen := windowRows
	if windowLen == 0 {
		t.Fatal("windowRows is 0 -- fixture cannot be built")
	}
	for want := 1; want <= windowLen; want++ {
		ans := map[string]any{"header_row": json.Number(strconv.Itoa(want))}
		if got := guardHeaderRow(ans, windowLen); got != want {
			t.Errorf("guardHeaderRow(header_row=%d, windowLen=%d) = %d, want %d", want, windowLen, got, want)
		}
	}
	beyond := map[string]any{"header_row": json.Number(strconv.Itoa(windowLen + 1))}
	if got := guardHeaderRow(beyond, windowLen); got != defaultHeaderRow {
		t.Errorf("guardHeaderRow(header_row=%d, windowLen=%d) = %d, want defaultHeaderRow %d", windowLen+1, windowLen, got, defaultHeaderRow)
	}
}

// T09 (row 9).
func TestGuardHeaderRow_MalformedValuesFallBackToOne(t *testing.T) {
	const windowLen = 10
	cases := []struct {
		name string
		ans  map[string]any
	}{
		{"absent", map[string]any{}},
		{"nil", map[string]any{"header_row": nil}},
		{"go string", map[string]any{"header_row": "3"}},
		{"go int", map[string]any{"header_row": int(3)}},
		{"go float64", map[string]any{"header_row": float64(3)}},
		{"zero", map[string]any{"header_row": json.Number("0")}},
		{"negative", map[string]any{"header_row": json.Number("-1")}},
		{"fractional", map[string]any{"header_row": json.Number("1.5")}},
		{"above window", map[string]any{"header_row": json.Number("11")}},
		{"int64 overflow", map[string]any{"header_row": json.Number("99999999999999999999")}},
		{"empty number", map[string]any{"header_row": json.Number("")}},
	}
	if len(cases) == 0 {
		t.Fatal("no cases -- fixture is empty")
	}
	for _, c := range cases {
		if got := guardHeaderRow(c.ans, windowLen); got != defaultHeaderRow {
			t.Errorf("%s: guardHeaderRow(%v, %d) = %d, want defaultHeaderRow %d", c.name, c.ans, windowLen, got, defaultHeaderRow)
		}
	}
	valid := map[string]any{"header_row": json.Number("3")}
	if got := guardHeaderRow(valid, windowLen); got != 3 {
		t.Errorf("guardHeaderRow(header_row=3, windowLen=%d) = %d, want 3", windowLen, got)
	}
}

// T10 (row 10). Proves its own premise: the real fake's blank answer carries a nil header_row.
func TestGuardHeaderRow_FakeBlankAnswerResolvesRowOne(t *testing.T) {
	ans, err, outcome := sgFakeAnswer(t, "Row 1: a,b")
	if err != nil {
		t.Fatalf("Call: %v (outcome %q)", err, outcome)
	}
	if outcome != "fake" {
		t.Fatalf("outcome = %q, want %q", outcome, "fake")
	}
	if ans["header_row"] != nil {
		t.Fatalf("ans[header_row] = %v, want nil (blank answer) -- test's premise is false", ans["header_row"])
	}
	if got := guardHeaderRow(ans, 10); got != defaultHeaderRow {
		t.Errorf("guardHeaderRow(blank answer, 10) = %d, want defaultHeaderRow %d", got, defaultHeaderRow)
	}
}

// T11 (row 11).
func TestGuardPlacements_FakeBlankAnswerPlacesNothing(t *testing.T) {
	ans, err, outcome := sgFakeAnswer(t, "Row 1: Total,Qty")
	if err != nil {
		t.Fatalf("Call: %v (outcome %q)", err, outcome)
	}
	if outcome != "fake" {
		t.Fatalf("outcome = %q, want %q", outcome, "fake")
	}
	placed := guardPlacements(ans, []string{"Total", "Qty"})
	if placed == nil {
		t.Errorf("guardPlacements returned a nil map, want non-nil (marshals to null, which §2 forbids)")
	}
	if len(placed) != 0 {
		t.Errorf("guardPlacements placed %v, want none", placed)
	}
}

// T12 (row 12, replaces the call-site variant -- see report). guardPlacements is fed the
// re-decoded header, never the window: two real Decode calls at different header rows on one
// file, one answer, opposite results.
func TestGuardPlacements_PlacesOnlyNamesPresentInTheHeaderItIsGiven(t *testing.T) {
	csvBytes := []byte("InvA,BuyA\nTitle Row\nInvB;BuyB\n1;2\n")

	header1, _, _, err := Decode(bytes.NewReader(csvBytes), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	header3, _, _, err := DecodeFrom(bytes.NewReader(csvBytes), "csv", 3)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	if len(header1) == 0 || len(header3) == 0 {
		t.Fatalf("fixture decoded to an empty header: header1=%v header3=%v", header1, header3)
	}
	if slices.Equal(header1, header3) {
		t.Fatalf("header1 %v equals header3 %v -- fixture cannot discriminate", header1, header3)
	}

	answer := map[string]any{"invoice_number": "InvA", "buyer_name": "BuyB"}

	got1 := guardPlacements(answer, header1)
	if got1["invoice_number"] != "InvA" {
		t.Errorf("guardPlacements(header1)[invoice_number] = %q, want %q", got1["invoice_number"], "InvA")
	}
	if _, ok := got1["buyer_name"]; ok {
		t.Errorf("guardPlacements(header1) placed buyer_name %q, header1 %v does not carry it", got1["buyer_name"], header1)
	}

	got3 := guardPlacements(answer, header3)
	if got3["buyer_name"] != "BuyB" {
		t.Errorf("guardPlacements(header3)[buyer_name] = %q, want %q", got3["buyer_name"], "BuyB")
	}
	if _, ok := got3["invoice_number"]; ok {
		t.Errorf("guardPlacements(header3) placed invoice_number %q, header3 %v does not carry it", got3["invoice_number"], header3)
	}
}

// T13 (row 13).
func TestGuardPlacements_ADuplicatedColumnUnplacesEveryClaimant(t *testing.T) {
	header := []string{"Amount", "Qty"}
	answer := map[string]any{"total": "Amount", "subtotal": "Amount", "line_quantity": "Qty"}
	got := guardPlacements(answer, header)
	if _, ok := got["total"]; ok {
		t.Errorf("guardPlacements placed total on a duplicated column: %v", got)
	}
	if _, ok := got["subtotal"]; ok {
		t.Errorf("guardPlacements placed subtotal on a duplicated column: %v", got)
	}
	if got["line_quantity"] != "Qty" {
		t.Errorf("guardPlacements[line_quantity] = %q, want %q", got["line_quantity"], "Qty")
	}
}

// T14 (row 14). NOTE: with guardPlacements fully stubbed (always empty), every assertion below
// is currently satisfied vacuously -- an empty result trivially matches "all three unplaced".
// This row's real discriminating power against row 13 (2-way vs 3-way duplicate) only exists
// once the guard is implemented; see mutations-AIR-07-01.jsonl row 14 for the replay proof.
func TestGuardPlacements_ThreeWayDuplicateUnplacesAllThree(t *testing.T) {
	header := []string{"Amount"}
	answer := map[string]any{"total": "Amount", "subtotal": "Amount", "vat": "Amount"}
	got := guardPlacements(answer, header)
	for _, f := range []string{"total", "subtotal", "vat"} {
		if _, ok := got[f]; ok {
			t.Errorf("guardPlacements placed %s on a three-way duplicated column: %v", f, got)
		}
	}
	if len(got) != 0 {
		t.Errorf("guardPlacements placed %v, want none", got)
	}
}

// T15 (row 15).
func TestGuardPlacements_ANonCanonicalKeyIsDropped(t *testing.T) {
	header := []string{"Total", "TIN"}
	answer := map[string]any{"totla": "Total", "supplier_tin": "TIN", "total": "Total"}
	got := guardPlacements(answer, header)
	if _, ok := got["totla"]; ok {
		t.Errorf("guardPlacements placed non-canonical key totla: %v", got)
	}
	if _, ok := got["supplier_tin"]; ok {
		t.Errorf("guardPlacements placed non-canonical key supplier_tin: %v", got)
	}
	if got["total"] != "Total" {
		t.Errorf("guardPlacements[total] = %q, want %q", got["total"], "Total")
	}
	if len(got) != 1 {
		t.Errorf("guardPlacements placed %d field(s), want exactly 1: %v", len(got), got)
	}
}

// T16 (row 16). A placement the guard passes must be one resolveMapping cannot reject.
func TestGuardPlacements_MatchesResolveMappingEquality(t *testing.T) {
	header := []string{" Total", "Total", "Inv No"}
	answer := map[string]any{"invoice_number": "Inv No", "total": "Total"}
	got := guardPlacements(answer, header)
	if got["invoice_number"] != "Inv No" {
		t.Errorf("guardPlacements[invoice_number] = %q, want %q", got["invoice_number"], "Inv No")
	}
	if got["total"] != "Total" {
		t.Errorf("guardPlacements[total] = %q, want %q", got["total"], "Total")
	}
	colIndex, err := resolveMapping(got, header)
	if err != nil {
		t.Fatalf("resolveMapping(%v, %v): %v", got, header, err)
	}
	if colIndex["total"] != 1 {
		t.Errorf("resolveMapping[total] = %d, want index 1 (the unpadded %q, not %q)", colIndex["total"], "Total", " Total")
	}
}

// T17 (row 17).
func TestGuardPlacements_BlankAndNonStringValuesAreUnplaced(t *testing.T) {
	header := []string{"", "  ", "Total"}
	answer := map[string]any{
		"invoice_number":   "",
		"issue_date":       "  ",
		"buyer_tin":        42,
		"buyer_name":       json.Number("7"),
		"currency":         nil,
		"subtotal":         []any{},
		"vat":              map[string]any{},
		"line_description": true,
		"total":            "Total",
	}
	got := guardPlacements(answer, header)
	for _, f := range []string{"invoice_number", "issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "line_description"} {
		if v, ok := got[f]; ok {
			t.Errorf("guardPlacements placed %s = %v, want unplaced", f, v)
		}
	}
	if got["total"] != "Total" {
		t.Errorf("guardPlacements[total] = %q, want %q (the control)", got["total"], "Total")
	}
}

// T18 (row 18). Distinct claim from T17: a blank header cell disqualifies only its own value.
func TestGuardPlacements_APartiallyBlankHeaderStillPlacesItsNamedColumns(t *testing.T) {
	header := []string{"", "", "Total"}
	answer := map[string]any{"total": "Total", "vat": ""}
	got := guardPlacements(answer, header)
	if got["total"] != "Total" {
		t.Errorf("guardPlacements[total] = %q, want %q", got["total"], "Total")
	}
	if _, ok := got["vat"]; ok {
		t.Errorf("guardPlacements placed vat on a blank header cell: %v", got["vat"])
	}
}

// T19 (row 19, AC-9's behavioural half; the source-scan half is
// TestDateFormatAndDecimalSeparator_AreReadNowhereOutsideSuggest below).
func TestGuardPlacements_IgnoresDateFormatAndDecimalSeparator(t *testing.T) {
	header := []string{"DD/MM/YYYY", ",", "Total"}
	answer := map[string]any{"date_format": "DD/MM/YYYY", "decimal_separator": ",", "total": "Total"}
	got := guardPlacements(answer, header)
	if len(got) != 1 {
		t.Fatalf("guardPlacements placed %d field(s), want exactly 1: %v", len(got), got)
	}
	if got["total"] != "Total" {
		t.Errorf("guardPlacements[total] = %q, want %q", got["total"], "Total")
	}
}

// T20 (row 20, NEW). Two columns sharing one header name is a shape the guard cannot and must
// not resolve -- the AI names a string, not an index. Rule 5 counts fields claiming a name,
// not columns sharing one, so the single claimant must still place.
func TestGuardPlacements_ADuplicateNameInTheFileItselfStillPlacesOnce(t *testing.T) {
	header := []string{"Total", "Total", "Inv"}
	answer := map[string]any{"invoice_number": "Inv", "total": "Total"}
	got := guardPlacements(answer, header)
	if got["total"] != "Total" {
		t.Errorf("guardPlacements[total] = %q, want %q -- two columns sharing one header name must still let a single field claim it", got["total"], "Total")
	}
	if got["invoice_number"] != "Inv" {
		t.Fatalf("guardPlacements[invoice_number] = %q, want %q", got["invoice_number"], "Inv")
	}
	colIndex, err := resolveMapping(got, header)
	if err != nil {
		t.Fatalf("resolveMapping(%v, %v): %v", got, header, err)
	}
	if colIndex["total"] != 0 {
		t.Errorf("resolveMapping[total] = %d, want index 0 (first match)", colIndex["total"])
	}
}

// T21 (row 21, NEW). Go's encoding/csv and Python's csv.writer(QUOTE_MINIMAL) agree except on
// a leading-space field and the exact field `\.`, both measured, not assumed.
func TestCSVLine_MatchesPythonExceptForALeadingSpaceField(t *testing.T) {
	cases := []struct {
		name string
		row  []string
		want string
	}{
		{"comma in a field", []string{"a", "b,c"}, `a,"b,c"`},
		{"trailing empty field", []string{"d", ""}, `d,`},
		{"trailing space unquoted", []string{"x ", "y"}, `x ,y`},
		{"doubled inner quotes", []string{`he said "hi"`}, `"he said ""hi"""`},
		{"embedded newline quoted", []string{"multi\nline"}, "\"multi\nline\""},
		{"embedded tab unquoted", []string{"tab\there"}, "tab\there"},
		{"empty record", []string{}, ""},
		{"nil record", nil, ""},
		// ceiling: Go's csv quotes a leading-space field, Python's does not; a suggestion is
		// lost, never mis-placed. Pinned at Go's measured value, not Python's `" Total",Total`.
		{"leading space field diverges from Python", []string{" Total", "Total"}, `" Total",Total`},
		// Measured divergence: Go quotes the exact field `\.`; Python does not.
		{"lone backslash-dot field diverges from Python", []string{`\.`}, `"\."`},
	}
	if len(cases) == 0 {
		t.Fatal("no cases -- fixture is empty")
	}
	for _, c := range cases {
		if got := csvLine(c.row); got != c.want {
			t.Errorf("%s: csvLine(%q) = %q, want %q", c.name, c.row, got, c.want)
		}
	}
}

// --- AC-9's source scan ----------------------------------------------------------------------

// sgScanSkip is excluded because these two files legitimately name date_format and
// decimal_separator (schema properties, guard fixtures).
var sgScanSkip = map[string]bool{
	"internal/importer/suggest.go":      true,
	"internal/importer/suggest_test.go": true,
}

var sgScanDirs = []string{"internal", "cmd", filepath.Join("frontend", "app", "src"), "e2e"}

// sgScanSkipDir prunes sibling worktrees, RALPH logs and vendored/build trees that would fail
// the scan for reasons unrelated to this repo's own source (static-scans-walk-sibling-worktrees).
var sgScanSkipDir = map[string]bool{".claude": true, ".ralph": true, "node_modules": true, ".git": true}

// This is a currently-green structural guard, not a redified behaviour test: nothing in the
// repo reads date_format or decimal_separator today (breaklist confirms TOTAL 0), so the scan
// has nothing to catch until a future change violates AC-9. It reds only as a regression check.
func TestDateFormatAndDecimalSeparator_AreReadNowhereOutsideSuggest(t *testing.T) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	needle := regexp.MustCompile(`date_format|decimal_separator`)

	var files int
	for _, dir := range sgScanDirs {
		start := filepath.Join(root, dir)
		if _, statErr := os.Stat(start); statErr != nil {
			continue
		}
		walkErr := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if sgScanSkipDir[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if sgScanSkip[rel] {
				return nil
			}
			files++
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if needle.Match(b) {
				t.Errorf("%s mentions date_format or decimal_separator -- AC-9 forbids any code path outside suggest.go/suggest_test.go from reading either key", rel)
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", dir, walkErr)
		}
	}
	if files < 300 {
		t.Fatalf("scan walked %d file(s), want at least 300 -- looks truncated, not a clean scan", files)
	}
}
