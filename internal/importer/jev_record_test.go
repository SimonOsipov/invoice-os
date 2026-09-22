// jev_record_test.go: CHECK-01-06 Flash Lite half -- records one JSON line per layout to
// mapping_answers.jsonl by sending each layout's window (suggestWindow/mappingPromptText, via
// the shipped askMapping) through the real ai.Client. Gated on OPENROUTER_API_KEY; the gate
// exists for determinism of the recorded artifact, not spend -- a full 48-layout run costs
// about one US cent (.ralph/arch/CHECK-01-06.md D-7). AC-8's reader lives here too, not in
// jev_mapping_test.go, which is CHECK-01-07's file (D-5).
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// jrLayout is layouts.json's id field -- the only one this half reads. rows never reaches
// the recorder: it opens the CSV and decodes it instead (D-1).
type jrLayout struct {
	ID string `json:"id"`
}

// jrAnswer is one mapping_answers.jsonl line. Answer is the raw model answer, untouched --
// CHECK-01-07 runs guardHeaderRow/guardPlacements over it, so no coercion belongs here.
type jrAnswer struct {
	Layout string         `json:"layout"`
	Answer map[string]any `json:"answer"`
	Model  string         `json:"model"`
}

func jrLoadLayouts(t *testing.T, dir string) []jrLayout {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "layouts.json"))
	if err != nil {
		t.Fatalf("read layouts.json: %v", err)
	}
	var layouts []jrLayout
	if err := json.Unmarshal(b, &layouts); err != nil {
		t.Fatalf("unmarshal layouts.json: %v", err)
	}
	if len(layouts) == 0 {
		t.Fatalf("layouts.json holds no layouts")
	}
	return layouts
}

// jrWindow is production's exact path: decode the CSV, then suggestWindow. Never
// layouts.json's rows, which Go's encoding/csv drops a blank record from on the titled
// layouts (D-1) -- a rows-based window would send a prompt production never sends.
func jrWindow(t *testing.T, dir, id string) [][]string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "csv", id+".csv"))
	if err != nil {
		t.Fatalf("open csv for %s: %v", id, err)
	}
	defer func() { _ = f.Close() }()
	header, rows, _, err := Decode(f, "csv")
	if err != nil {
		t.Fatalf("Decode csv for %s: %v", id, err)
	}
	return suggestWindow(header, rows)
}

// jrRecorded returns the set of layout ids already in path. An absent file is an empty set,
// not an error -- AC-6's resume.
func jrRecorded(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]bool{}
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var a jrAnswer
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Fatalf("unmarshal %s line %q: %v", path, line, err)
		}
		seen[a.Layout] = true
	}
	return seen
}

// jrRecord asks s for every not-yet-recorded layout and appends one line per answer to
// dir/mapping_answers.jsonl. askMapping collapses off, a failed call and a refused envelope
// into one nil answer; a nil writes no line, so the next run retries it (R-9) rather than
// CHECK-01-07 scoring a transport failure as an empty model answer.
func jrRecord(t *testing.T, s MappingSuggester, dir string) int {
	t.Helper()
	layouts := jrLoadLayouts(t, dir)
	path := filepath.Join(dir, "mapping_answers.jsonl")
	recorded := jrRecorded(t, path)

	written := 0
	for _, l := range layouts {
		if recorded[l.ID] {
			continue
		}
		window := jrWindow(t, dir, l.ID)
		ans := askMapping(t.Context(), s, window)
		if ans == nil {
			t.Logf("%s: no answer, not recorded", l.ID)
			continue
		}
		line, err := json.Marshal(jrAnswer{Layout: l.ID, Answer: ans, Model: ai.Model})
		if err != nil {
			t.Fatalf("marshal answer for %s: %v", l.ID, err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			t.Fatalf("append to %s: %v", path, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close %s: %v", path, err)
		}
		written++
	}
	return written
}

// jrReadAnswers reads path's lines, erroring on the first layout id absent from known rather
// than skipping it silently (AC-8). Returns an error, not a t.Fatalf, so both legs of
// TestJevRecord_AnUnknownLayoutIdFailsTheReader can observe the outcome.
func jrReadAnswers(path string, known map[string]bool) ([]jrAnswer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []jrAnswer
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		var a jrAnswer
		if err := dec.Decode(&a); err != nil {
			return nil, fmt.Errorf("jrReadAnswers: unmarshal line %q: %w", line, err)
		}
		if !known[a.Layout] {
			return nil, fmt.Errorf("jrReadAnswers: layout %q is not in layouts.json", a.Layout)
		}
		out = append(out, a)
	}
	return out, nil
}

// jrGateLogs mirrors jev_value_test.go's jvGateLogs: the seam TestJevRecord_UnsetKeyRecords
// Nothing reads to tell key-missing from out-missing apart. Reset per subtest with
// jrResetGateLogs.
var jrGateLogs []string

func jrResetGateLogs() { jrGateLogs = nil }

func jrGateLog(t *testing.T, msg string) {
	t.Helper()
	jrGateLogs = append(jrGateLogs, msg)
	t.Log(msg)
}

// jrGateReason names which env var jrGatedRun found missing -- jvGateReason's shape.
func jrGateReason(key, out string) string {
	switch {
	case key == "" && out == "":
		return fmt.Sprintf("%s and JEV_OUT unset: no client built, no call", ai.EnvKey)
	case key == "":
		return fmt.Sprintf("%s unset: no client built, no call", ai.EnvKey)
	default:
		return "JEV_OUT unset: no client built, no call"
	}
}

// jrGatedRun reads OPENROUTER_API_KEY and JEV_OUT directly -- never Client.Enabled(), which
// is key != "" || fake and would open under AI_FAKE with no key (D-2). Either empty logs one
// line naming which and returns false without building a client; a t.Skip here fails CI
// outright (rls-test-gate.sh has no -run filter over ./internal/importer/...).
func jrGatedRun(t *testing.T, s MappingSuggester) bool {
	t.Helper()
	key := os.Getenv(ai.EnvKey)
	out := os.Getenv("JEV_OUT")
	if key == "" || out == "" {
		jrGateLog(t, jrGateReason(key, out))
		return false
	}
	if s == nil {
		c, err := ai.FromEnv(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		if err != nil {
			t.Fatalf("ai.FromEnv: %v", err)
		}
		s = c
	}
	t.Logf("live run: model %s", ai.Model) // named so an operator sees what will be sent
	jrRecord(t, s, out)
	return true
}

// TestJevRecord_Record is the recorder's one operator entry point. CI sets neither env var,
// so this always declines there.
func TestJevRecord_Record(t *testing.T) {
	jrGatedRun(t, nil)
}

// --- test fixtures ---------------------------------------------------------------------

func jrWriteLayoutsIDs(t *testing.T, dir string, ids ...string) {
	t.Helper()
	layouts := make([]jrLayout, len(ids))
	for i, id := range ids {
		layouts[i] = jrLayout{ID: id}
	}
	b, err := json.Marshal(layouts)
	if err != nil {
		t.Fatalf("marshal layouts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "layouts.json"), b, 0o644); err != nil {
		t.Fatalf("write layouts.json: %v", err)
	}
}

func jrWriteCSV(t *testing.T, dir, id, content string) {
	t.Helper()
	csvDir := filepath.Join(dir, "csv")
	if err := os.MkdirAll(csvDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", csvDir, err)
	}
	if err := os.WriteFile(filepath.Join(csvDir, id+".csv"), []byte(content), 0o644); err != nil {
		t.Fatalf("write csv for %s: %v", id, err)
	}
}

// jrPlainCSVText is a minimal, untitled CSV: header on row 1, production's default path.
func jrPlainCSVText() string {
	return "Invoice No,Total\nINV-1,100\n"
}

// jrTitledCSVText mirrors csvgen.py's title block: title line(s), a BLANK line, then the
// header -- the shape Go's encoding/csv silently drops a record for (D-1). n data rows fill
// the window when n >= windowRows-1.
func jrTitledCSVText(n int) string {
	var b strings.Builder
	b.WriteString("Sample Export TITLEMARK\n")
	b.WriteString("\n") // the blank line encoding/csv skips
	b.WriteString("Inv No,Total\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "INV-%d,%d\n", i, i*100)
	}
	return b.String()
}

// jrFindCall returns the one call in calls whose Text contains marker, failing unless
// exactly one matches -- so a fixture holding several layouts can still bind an assertion to
// one specific recorded call.
func jrFindCall(t *testing.T, calls []ai.Request, marker string) ai.Request {
	t.Helper()
	var found []ai.Request
	for _, c := range calls {
		if strings.Contains(c.Text, marker) {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d call(s) whose text contains %q, want exactly 1", len(found), marker)
	}
	return found[0]
}

// --- AC-4 --------------------------------------------------------------------------------

// jrFatalSuggester fails the test if ever called: the key gate must have stopped the run
// before any suggester is reached.
type jrFatalSuggester struct{ t *testing.T }

func (f jrFatalSuggester) Enabled() bool { return true }

func (f jrFatalSuggester) Call(_ context.Context, req ai.Request) (map[string]any, error) {
	f.t.Fatalf("jrFatalSuggester.Call invoked with text %q -- the key gate should have stopped this", req.Text)
	return nil, nil
}

func TestJevRecord_UnsetKeyRecordsNothing(t *testing.T) {
	negative := func(t *testing.T, key, out string, wantKeyNamed, wantOutNamed bool) {
		t.Helper()
		t.Setenv(ai.EnvKey, key)
		t.Setenv("JEV_OUT", out)
		jrResetGateLogs()
		if got := jrGatedRun(t, jrFatalSuggester{t: t}); got {
			t.Errorf("jrGatedRun(key=%q, out=%q) = true, want false", key, out)
		}
		if len(jrGateLogs) != 1 {
			t.Fatalf("jrGatedRun(key=%q, out=%q) logged %d line(s), want exactly 1", key, out, len(jrGateLogs))
		}
		msg := jrGateLogs[0]
		if strings.Contains(msg, ai.EnvKey) != wantKeyNamed {
			t.Errorf("logged %q; %s named = %v, want %v", msg, ai.EnvKey, strings.Contains(msg, ai.EnvKey), wantKeyNamed)
		}
		if strings.Contains(msg, "JEV_OUT") != wantOutNamed {
			t.Errorf("logged %q; JEV_OUT named = %v, want %v", msg, strings.Contains(msg, "JEV_OUT"), wantOutNamed)
		}
	}
	t.Run("neither set", func(t *testing.T) { negative(t, "", "", true, true) })
	t.Run("key only", func(t *testing.T) { negative(t, "not-a-real-key", "", false, true) })
	t.Run("out only", func(t *testing.T) { negative(t, "", t.TempDir(), true, false) })

	// Positive control: without it, a jrGatedRun that always returns false would pass every
	// leg above.
	t.Run("both set: positive control", func(t *testing.T) {
		dir := t.TempDir()
		jrWriteLayoutsIDs(t, dir, "sw_zoho")
		jrWriteCSV(t, dir, "sw_zoho", jrPlainCSVText())
		t.Setenv(ai.EnvKey, "not-a-real-key")
		t.Setenv("JEV_OUT", dir)
		jrResetGateLogs()
		rec := &fakeSuggester{enabled: true, answer: map[string]any{"invoice_number": "Inv"}}
		if got := jrGatedRun(t, rec); !got {
			t.Errorf("jrGatedRun = false, want true")
		}
		if len(rec.calls) != 1 {
			t.Errorf("suggester called %d time(s), want 1", len(rec.calls))
		}
		if len(jrGateLogs) != 0 {
			t.Errorf("logged %d line(s), want 0 -- the decline line must be absent on the positive control", len(jrGateLogs))
		}
	})
}

// --- AC-5 --------------------------------------------------------------------------------

// TestJevRecord_SendsTheShippedWindow: the titled fixture's blank title-block line is
// exactly the shape encoding/csv drops a record for -- the scenario D-1 exists to guard.
func TestJevRecord_SendsTheShippedWindow(t *testing.T) {
	dir := t.TempDir()
	jrWriteLayoutsIDs(t, dir, "plain_01", "titled_01")
	jrWriteCSV(t, dir, "plain_01", jrPlainCSVText())
	jrWriteCSV(t, dir, "titled_01", jrTitledCSVText(windowRows-1))

	rec := &fakeSuggester{enabled: true, answer: map[string]any{"invoice_number": "Inv"}}
	if n := jrRecord(t, rec, dir); n != 2 {
		t.Fatalf("jrRecord wrote %d line(s), want 2", n)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("suggester received %d call(s), want 2", len(rec.calls))
	}

	titled := jrFindCall(t, rec.calls, "TITLEMARK")

	f, err := os.Open(filepath.Join(dir, "csv", "titled_01.csv"))
	if err != nil {
		t.Fatalf("open titled_01.csv: %v", err)
	}
	defer func() { _ = f.Close() }()
	header, rows, _, err := Decode(f, "csv")
	if err != nil {
		t.Fatalf("Decode titled_01.csv: %v", err)
	}
	wantText := mappingPromptText(suggestWindow(header, rows))
	if titled.Text != wantText {
		t.Errorf("req.Text = %q, want %q", titled.Text, wantText)
	}
	if titled.System != mappingSystem {
		t.Errorf("req.System does not equal mappingSystem")
	}
	if titled.SchemaName != mappingSchemaName {
		t.Errorf("req.SchemaName = %q, want %q", titled.SchemaName, mappingSchemaName)
	}
	if titled.Purpose != ai.PurposeSpreadsheet {
		t.Errorf("req.Purpose = %q, want %q", titled.Purpose, ai.PurposeSpreadsheet)
	}
	if len(titled.Schema) == 0 {
		t.Errorf("req.Schema is empty")
	}
	if titled.Pages != nil {
		t.Errorf("req.Pages = %v, want nil", titled.Pages)
	}

	// Floor: every corpus layout fills the window exactly (§1.5); without this, a mutation
	// inside suggestWindow moves both sides of the equality above and stays green.
	if got := strings.Count(titled.Text, "\nRow "); got != windowRows {
		t.Errorf("prompt carries %d row line(s), want %d (windowRows)", got, windowRows)
	}
}

// TestJevRecord_ALongFileIsCappedAtTheWindow pins the RECORDER's own path through the cap,
// not suggestWindow's (TestSuggestWindow_LongFileIsCappedAtWindowRows already covers that).
func TestJevRecord_ALongFileIsCappedAtTheWindow(t *testing.T) {
	dir := t.TempDir()
	const dataRows = 40
	var b strings.Builder
	b.WriteString("Col\n")
	for i := 1; i <= dataRows; i++ {
		fmt.Fprintf(&b, "R%d\n", i)
	}
	if got := strings.Count(b.String(), "\n") - 1; got != dataRows {
		t.Fatalf("fixture built %d data row(s), want %d -- fixture did not shrink silently", got, dataRows)
	}
	jrWriteLayoutsIDs(t, dir, "long_01")
	jrWriteCSV(t, dir, "long_01", b.String())

	rec := &fakeSuggester{enabled: true, answer: map[string]any{"invoice_number": "R1"}}
	if n := jrRecord(t, rec, dir); n != 1 {
		t.Fatalf("jrRecord wrote %d line(s), want 1", n)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("suggester received %d call(s), want 1", len(rec.calls))
	}
	if got := strings.Count(rec.calls[0].Text, "\nRow "); got != windowRows {
		t.Errorf("prompt carries %d row line(s), want %d (windowRows)", got, windowRows)
	}
}

// --- AC-6 --------------------------------------------------------------------------------

// TestJevRecord_AppendsRatherThanTruncates: AC-6's first clause.
func TestJevRecord_AppendsRatherThanTruncates(t *testing.T) {
	dir := t.TempDir()
	jrWriteLayoutsIDs(t, dir, "mix_01", "mix_02")
	jrWriteCSV(t, dir, "mix_01", jrPlainCSVText())
	jrWriteCSV(t, dir, "mix_02", jrPlainCSVText())

	path := filepath.Join(dir, "mapping_answers.jsonl")
	preWritten := `{"layout":"mix_01","answer":{"invoice_number":"Inv No","header_row":1},"model":"pinned-model"}` + "\n"
	if err := os.WriteFile(path, []byte(preWritten), 0o644); err != nil {
		t.Fatalf("pre-write %s: %v", path, err)
	}

	rec := &fakeSuggester{enabled: true, answer: map[string]any{"invoice_number": "Inv No"}}
	if n := jrRecord(t, rec, dir); n != 1 {
		t.Fatalf("jrRecord wrote %d line(s), want 1 (mix_01 is already recorded)", n)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("file holds %d line(s), want 2: %v", len(lines), lines)
	}
	if lines[0]+"\n" != preWritten {
		t.Errorf("line 1 = %q, want the pre-written line byte-identical: %q", lines[0], preWritten)
	}
}

// TestJevRecord_ARecordedLayoutIsNotAskedAgain: AC-6's second clause -- "resumes without
// re-spending". An append-only recorder that re-asks every layout on each run passes
// TestJevRecord_AppendsRatherThanTruncates above and still fails this one.
func TestJevRecord_ARecordedLayoutIsNotAskedAgain(t *testing.T) {
	dir := t.TempDir()
	jrWriteLayoutsIDs(t, dir, "sw_zoho", "mix_01")
	jrWriteCSV(t, dir, "sw_zoho", jrPlainCSVText())
	jrWriteCSV(t, dir, "mix_01", "Inv,Total\nA,1\nB,2\n")

	path := filepath.Join(dir, "mapping_answers.jsonl")
	preWritten := `{"layout":"sw_zoho","answer":{"invoice_number":"Invoice No","header_row":1},"model":"pinned-model"}` + "\n"
	if err := os.WriteFile(path, []byte(preWritten), 0o644); err != nil {
		t.Fatalf("pre-write %s: %v", path, err)
	}

	rec := &fakeSuggester{enabled: true, answer: map[string]any{"invoice_number": "Inv"}}
	if n := jrRecord(t, rec, dir); n != 1 {
		t.Fatalf("jrRecord wrote %d line(s), want 1", n)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("suggester was called %d time(s), want exactly 1 -- sw_zoho must not be re-asked", len(rec.calls))
	}

	f, err := os.Open(filepath.Join(dir, "csv", "mix_01.csv"))
	if err != nil {
		t.Fatalf("open mix_01.csv: %v", err)
	}
	defer func() { _ = f.Close() }()
	header, rows, _, err := Decode(f, "csv")
	if err != nil {
		t.Fatalf("Decode mix_01.csv: %v", err)
	}
	wantText := mappingPromptText(suggestWindow(header, rows))
	if rec.calls[0].Text != wantText {
		t.Errorf("the one call's Text is not mix_01's window -- sw_zoho was asked instead")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := strings.Count(string(b), "\n"); got != 2 {
		t.Errorf("file holds %d line(s), want 2", got)
	}
}

// --- AC-7 --------------------------------------------------------------------------------

func TestJevRecord_EachLineCarriesIdAnswerAndModel(t *testing.T) {
	dir := t.TempDir()
	jrWriteLayoutsIDs(t, dir, "sw_zoho")
	jrWriteCSV(t, dir, "sw_zoho", jrPlainCSVText())

	answer := map[string]any{
		"invoice_number": "Invoice Number",
		"header_row":     json.Number("1"),
	}
	rec := &fakeSuggester{enabled: true, answer: answer}
	if n := jrRecord(t, rec, dir); n != 1 {
		t.Fatalf("jrRecord wrote %d line(s), want 1", n)
	}

	b, err := os.ReadFile(filepath.Join(dir, "mapping_answers.jsonl"))
	if err != nil {
		t.Fatalf("read mapping_answers.jsonl: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(b)))
	dec.UseNumber() // matches guardHeaderRow's json.Number expectation, so the round trip is honest
	var line jrAnswer
	if err := dec.Decode(&line); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if line.Layout != "sw_zoho" {
		t.Errorf("line.Layout = %q, want %q", line.Layout, "sw_zoho")
	}
	if line.Model != ai.Model {
		t.Errorf("line.Model = %q, want ai.Model %q", line.Model, ai.Model)
	}
	if !reflect.DeepEqual(line.Answer, answer) {
		t.Errorf("line.Answer = %v, want the raw suggester answer %v (not guarded/coerced)", line.Answer, answer)
	}
	if _, ok := line.Answer["header_row"]; !ok {
		t.Errorf("line.Answer has no header_row key -- guardHeaderRow must not have run here")
	}
}

// --- AC-8 --------------------------------------------------------------------------------

func TestJevRecord_AnUnknownLayoutIdFailsTheReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mapping_answers.jsonl")

	t.Run("negative: an id absent from the known set fails the read", func(t *testing.T) {
		line := `{"layout":"mix_99","answer":{"invoice_number":"Inv"},"model":"m"}` + "\n"
		if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		known := map[string]bool{"mix_01": true, "sw_zoho": true}
		if _, err := jrReadAnswers(path, known); err == nil {
			t.Fatal("jrReadAnswers returned nil error, want one naming mix_99")
		} else if !strings.Contains(err.Error(), "mix_99") {
			t.Errorf("error %q does not name mix_99", err)
		}
	})

	// Floor / control: a reader that fails on every line would also pass the negative leg
	// above.
	t.Run("positive: two known ids read back cleanly", func(t *testing.T) {
		lines := "{\"layout\":\"mix_01\",\"answer\":{\"invoice_number\":\"A\"},\"model\":\"m\"}\n" +
			"{\"layout\":\"sw_zoho\",\"answer\":{\"invoice_number\":\"B\"},\"model\":\"m\"}\n"
		if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		known := map[string]bool{"mix_01": true, "sw_zoho": true}
		got, err := jrReadAnswers(path, known)
		if err != nil {
			t.Fatalf("jrReadAnswers: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("read %d row(s), want 2", len(got))
		}
		ids := []string{got[0].Layout, got[1].Layout}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"mix_01", "sw_zoho"}) {
			t.Errorf("ids = %v, want [mix_01 sw_zoho]", ids)
		}
	})
}

// --- safety: the one real client sits behind the key gate ----------------------------------

// jrEnvKeySite is one function in internal/importer's own *_test.go files whose body calls
// ai.FromEnv. Two pre-existing sites (suggest_test.go's sgFakeAnswer,
// handlers_suggest_test.go's envelope test) explicitly blank OPENROUTER_API_KEY first and
// build a fake-mode client that can never reach a vendor; jrGatedRun is the only site that
// reads the real key.
type jrEnvKeySite struct {
	file     string
	funcName string
	gated    bool // reads os.Getenv(ai.EnvKey) BEFORE calling ai.FromEnv, in the same function
	blanked  bool // sets ai.EnvKey to "" before ai.FromEnv, in the same function
}

func jrCallName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name + "." + x.Sel.Name
		}
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// jrScanFromEnvSites parses every *_test.go under dir with go/parser and returns one entry
// per top-level function whose body calls ai.FromEnv.
func jrScanFromEnvSites(t *testing.T, dir string) []jrEnvKeySite {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var sites []jrEnvKeySite
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var fromEnvPos, getenvPos token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch jrCallName(call.Fun) {
				case "ai.FromEnv":
					if fromEnvPos == token.NoPos {
						fromEnvPos = call.Pos()
					}
				case "os.Getenv":
					if len(call.Args) == 1 && jrCallName(call.Args[0]) == "ai.EnvKey" && getenvPos == token.NoPos {
						getenvPos = call.Pos()
					}
				}
				return true
			})
			if fromEnvPos == token.NoPos {
				continue
			}
			start := fset.Position(fn.Pos()).Offset
			end := fset.Position(fn.End()).Offset
			body := string(src[start:end])
			sites = append(sites, jrEnvKeySite{
				file:     e.Name(),
				funcName: fn.Name.Name,
				gated:    getenvPos != token.NoPos && getenvPos < fromEnvPos,
				blanked:  strings.Contains(body, `Setenv(ai.EnvKey, "")`),
			})
		}
	}
	if scanned < 3 {
		t.Fatalf("scanned %d _test.go file(s) in %s, want at least 3 -- the walk looks truncated", scanned, dir)
	}
	return sites
}

// TestJevRecord_TheOnlyRealClientSitsBehindTheKeyGate is the row the plan's nine specified
// rows leave unchecked: that no path in this package can build a live, key-backed ai.Client
// outside jrGatedRun. A literal "exactly one ai.FromEnv call site" count is false as written
// -- two pre-existing fake-mode test helpers already call it -- so sites are classified by
// whether they blank the key (provably safe) or read it for real before building the client
// (the one site this test pins).
func TestJevRecord_TheOnlyRealClientSitsBehindTheKeyGate(t *testing.T) {
	sites := jrScanFromEnvSites(t, ".")
	if len(sites) < 3 {
		t.Fatalf("found %d ai.FromEnv call site(s), want at least 3 (two existing fake-mode helpers plus jrGatedRun) -- the scan is not finding real matches", len(sites))
	}

	var gated, unaccounted []jrEnvKeySite
	for _, s := range sites {
		switch {
		case s.gated:
			gated = append(gated, s)
		case s.blanked:
			// Safe: a fake-mode helper that clears the key first can never see a real one.
		default:
			unaccounted = append(unaccounted, s)
		}
	}
	if len(unaccounted) != 0 {
		t.Fatalf("ai.FromEnv called without reading os.Getenv(ai.EnvKey) first and without blanking the key: %v -- every call site must be gated or provably fake", unaccounted)
	}
	if len(gated) != 1 {
		t.Fatalf("found %d call site(s) reading os.Getenv(ai.EnvKey) before ai.FromEnv, want exactly 1: %v", len(gated), gated)
	}
	if gated[0].file != "jev_record_test.go" || gated[0].funcName != "jrGatedRun" {
		t.Fatalf("the gated ai.FromEnv call site is %s:%s, want jev_record_test.go:jrGatedRun", gated[0].file, gated[0].funcName)
	}
}
