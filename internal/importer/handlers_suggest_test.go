// handlers_suggest_test.go: Test Specs rows 1-4, 8-12, 15-17, 20, 23 for
// SuggestMappingHandler -- pure/httptest, fake-driven. DB-backed rows live in
// handlers_suggest_db_test.go; the error ladder lives in
// handlers_suggest_adversarial_test.go.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// fakeSuggester is a MappingSuggester double: records every Call and answers a fixed
// (answer, err) pair REGARDLESS of enabled -- so a guard that skips the Enabled() check
// is still observable via the call count.
type fakeSuggester struct {
	enabled bool
	answer  map[string]any
	err     error
	calls   []ai.Request
}

func (f *fakeSuggester) Enabled() bool { return f.enabled }

func (f *fakeSuggester) Call(ctx context.Context, req ai.Request) (map[string]any, error) {
	f.calls = append(f.calls, req)
	return f.answer, f.err
}

func suggestReqBody(entityID, documentID string) string {
	return fmt.Sprintf(`{"entity_id":%q,"document_id":%q}`, entityID, documentID)
}

func doSuggestRequest(t *testing.T, open openSpec, lookup func(ctx context.Context, entityID string, header []string) (*SavedMapping, error), suggester MappingSuggester, log *slog.Logger, id *auth.Identity, body string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports/suggest-mapping", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	SuggestMappingHandler(open, lookup, suggester, log).ServeHTTP(rec, r)
	return rec, rec.Body.Bytes()
}

func mustDecodeSuggest(t *testing.T, raw []byte) suggestMappingResponse {
	t.Helper()
	var resp suggestMappingResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode suggest response %s: %v", raw, err)
	}
	return resp
}

// buildTitleRowsFixture: 2 title rows, a header on row 3, then dataRows numbered rows.
func buildTitleRowsFixture(dataRows int) []byte {
	var b strings.Builder
	b.WriteString("Title\nMore\nInv No,Total\n")
	for i := 1; i <= dataRows; i++ {
		fmt.Fprintf(&b, "INV-%d,%d.00\n", i, 100+i)
	}
	return []byte(b.String())
}

// --- row 1 -------------------------------------------------------------------------

func TestSuggestHandler_SendsTheFirstTenRowsUnheadered(t *testing.T) {
	id := testIdentity()
	rows := make([][]string, 29)
	for i := 2; i <= 30; i++ {
		rows[i-2] = []string{strconv.Itoa(i)}
	}
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"1"}, rows))
	suggester := &fakeSuggester{enabled: true}

	_, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))

	if len(suggester.calls) == 0 {
		t.Fatalf("suggester recorded 0 calls, want 1 (body=%s)", raw)
	}
	got := suggester.calls[0]
	if got.Purpose != ai.PurposeSpreadsheet {
		t.Errorf("Purpose = %q, want %q", got.Purpose, ai.PurposeSpreadsheet)
	}
	wantLines := make([]string, 10)
	for i := 1; i <= 10; i++ {
		wantLines[i-1] = fmt.Sprintf("Row %d: %d", i, i)
	}
	wantText := mappingIntro + "\n" + strings.Join(wantLines, "\n")
	if got.Text != wantText {
		t.Errorf("Text = %q, want %q", got.Text, wantText)
	}
}

// --- row 2 -------------------------------------------------------------------------

func TestSuggestHandler_ShortFileSendsEveryRowItHas(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"1"}, [][]string{{"2"}, {"3"}}))
	suggester := &fakeSuggester{enabled: true}

	_, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))

	if len(suggester.calls) == 0 {
		t.Fatalf("suggester recorded 0 calls, want 1 (body=%s)", raw)
	}
	wantText := mappingIntro + "\nRow 1: 1\nRow 2: 2\nRow 3: 3"
	if got := suggester.calls[0].Text; got != wantText {
		t.Errorf("Text = %q, want %q -- a 3-row file must not pad to 10", got, wantText)
	}
}

// --- row 3 -------------------------------------------------------------------------

func TestSuggestHandler_ReDecodesAtTheAnsweredHeaderRow(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", buildTitleRowsFixture(2))
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("3")}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.HeaderRow != 3 {
		t.Errorf("header_row = %d, want 3", resp.HeaderRow)
	}
	want := []string{"Inv No", "Total"}
	if !reflect.DeepEqual(resp.Columns, want) {
		t.Errorf("columns = %v, want %v -- either the re-decode did not run at row 3, or obj.Body was read twice", resp.Columns, want)
	}
	if resp.RowsTotal != 2 {
		t.Errorf("rows_total = %d, want 2", resp.RowsTotal)
	}
}

// --- row 4 -------------------------------------------------------------------------

func TestSuggestHandler_SampleRowsStartBelowTheResolvedHeader(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", buildTitleRowsFixture(7))
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("3")}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.RowsTotal != 7 {
		t.Fatalf("rows_total = %d, want 7", resp.RowsTotal)
	}
	if len(resp.SampleRows) != 5 {
		t.Fatalf("len(sample_rows) = %d, want 5 (maxSampleRows), body=%s", len(resp.SampleRows), raw)
	}
	if want := []string{"INV-1", "101.00"}; !reflect.DeepEqual(resp.SampleRows[0], want) {
		t.Errorf("sample_rows[0] = %v, want %v (the file's row 4, right below the resolved header)", resp.SampleRows[0], want)
	}
}

// --- row 8 -------------------------------------------------------------------------

func TestSuggestHandler_OffAnswersNoneWithRowOne(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Inv No", "Total"}, [][]string{{"INV-1", "100"}}))
	suggester := &fakeSuggester{enabled: false, answer: map[string]any{"invoice_number": "Inv No", "total": "Total"}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.Source != "none" {
		t.Errorf("source = %q, want %q", resp.Source, "none")
	}
	if resp.HeaderRow != 1 {
		t.Errorf("header_row = %d, want 1", resp.HeaderRow)
	}
	if len(resp.Mapping) != 0 {
		t.Errorf("mapping = %v, want empty", resp.Mapping)
	}
	if want := []string{"Inv No", "Total"}; !reflect.DeepEqual(resp.Columns, want) {
		t.Errorf("columns = %v, want %v", resp.Columns, want)
	}
	if len(suggester.calls) != 0 {
		t.Errorf("suggester calls = %d, want 0 -- an off suggester must never be called", len(suggester.calls))
	}
}

// --- row 9 -------------------------------------------------------------------------

// TestSuggestHandler_UnavailableAnswersNoneNotFiveHundred uses the REAL ai.Client in
// fake mode: result.outcome is unexported, so the buffered log line is the only oracle
// that the envelope validated (never "refused") before the fake's error path ran.
func TestSuggestHandler_UnavailableAnswersNoneNotFiveHundred(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Note"}, [][]string{{"AIFAKE-UNAVAILABLE"}}))

	t.Setenv(ai.EnvFake, "true")
	t.Setenv(ai.EnvKey, "")
	var buf bytes.Buffer
	client, err := ai.FromEnv(slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("ai.FromEnv: %v", err)
	}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), client, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.Source != "none" {
		t.Errorf("source = %q, want %q", resp.Source, "none")
	}

	var calls []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if m["msg"] == "ai call" {
			calls = append(calls, m)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("the logger recorded %d %q line(s), want exactly 1: %v (body=%s)", len(calls), "ai call", calls, raw)
	}
	// Fake mode's own outcome is "fake" for every marker including AIFAKE-UNAVAILABLE
	// (TestCall_OffAndFakeSetTheOutcomeAndAttempts's fake_unavailable case) --
	// "unavailable" only happens against a real network. What this asserts is that
	// the envelope validated (never "refused") and the call's error still produced
	// source:"none" rather than a 500.
	if calls[0]["outcome"] != "fake" {
		t.Errorf("outcome = %v, want %q", calls[0]["outcome"], "fake")
	}
	if calls[0]["outcome"] == "refused" {
		t.Error("outcome = refused -- the envelope must validate (Purpose, Text, non-empty SchemaName)")
	}
}

// --- row 10 ------------------------------------------------------------------------

func TestSuggestHandler_ASchemaBreakingAnswerAnswersNone(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Inv No", "Total"}, [][]string{{"INV-1", "100"}}))
	// A suggester returning BOTH a populated map and a non-nil error: askMapping must
	// never trust the map when the error is set, whatever double calls it that way.
	suggester := &fakeSuggester{
		enabled: true,
		answer:  map[string]any{"invoice_number": "Inv No"},
		err:     errors.New("ai: response failed schema check"),
	}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.Source != "none" {
		t.Errorf("source = %q, want %q", resp.Source, "none")
	}
	if v, ok := resp.Mapping["invoice_number"]; ok {
		t.Errorf("mapping[invoice_number] = %q, want absent -- an errored call's answer must never be used", v)
	}
}

// --- row 11 ------------------------------------------------------------------------

func TestSuggestHandler_AGuardEmptiedAnswerAnswersNone(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Total"}, [][]string{{"100"}}))
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{
		"invoice_number": "Total", "total": "Total",
	}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.Source != "none" {
		t.Errorf("source = %q, want %q -- the duplicate-column claim unplaces both fields", resp.Source, "none")
	}
	if len(resp.Mapping) != 0 {
		t.Errorf("mapping = %v, want empty", resp.Mapping)
	}
}

// --- row 12 ------------------------------------------------------------------------

func TestSuggestHandler_CallsTheAIExactlyOncePerRequest(t *testing.T) {
	id := testIdentity()
	rows := make([][]string, 29)
	for i := 2; i <= 30; i++ {
		rows[i-2] = []string{strconv.Itoa(i)}
	}
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"1"}, rows))
	suggester := &fakeSuggester{enabled: true}

	_, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))

	if len(suggester.calls) != 1 {
		t.Errorf("suggester calls = %d, want exactly 1 (body=%s)", len(suggester.calls), raw)
	}
}

// --- row 15 ------------------------------------------------------------------------

func TestSuggestHandler_EmptyFileAnswersEmptyArraysNotNull(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("empty.csv", "text/csv", nil)
	suggester := &fakeSuggester{enabled: true}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"columns":[]`)) {
		t.Errorf(`raw body = %s, want "columns":[], never null`, raw)
	}
	if !bytes.Contains(raw, []byte(`"sample_rows":[]`)) {
		t.Errorf(`raw body = %s, want "sample_rows":[], never null`, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.RowsTotal != 0 {
		t.Errorf("rows_total = %d, want 0", resp.RowsTotal)
	}
	if resp.Source != "none" {
		t.Errorf("source = %q, want %q", resp.Source, "none")
	}
	if len(suggester.calls) != 0 {
		t.Errorf("suggester calls = %d, want 0 -- an empty window has nothing to show the model", len(suggester.calls))
	}
}

// --- row 16 ------------------------------------------------------------------------

func TestSuggestHandler_ABlankHeaderAtTheAnsweredRowFallsBackToRowOne(t *testing.T) {
	id := testIdentity()
	fixture := []byte("Title\nMore\n\nInv No,Total\nINV-1,100\n")
	open := newFakeDocOpen("data.csv", "text/csv", fixture)
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("3")}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.HeaderRow != 1 {
		t.Errorf("header_row = %d, want 1 -- row 3 is blank, must fall back", resp.HeaderRow)
	}
	if want := []string{"Title"}; !reflect.DeepEqual(resp.Columns, want) {
		t.Errorf("columns = %v, want %v (row 1's)", resp.Columns, want)
	}
}

// --- row 17 ------------------------------------------------------------------------

func TestSuggestHandler_ABlankXLSXHeaderRowFallsBackToRowOneToo(t *testing.T) {
	id := testIdentity()
	fixture := xlsxOf(t, [][]string{{"Title"}, {"More"}, nil, {"Inv No", "Total"}, {"INV-1", "100"}})
	open := newFakeDocOpen("data.xlsx", xlsxContentType, fixture)
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("3")}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.HeaderRow != 1 {
		t.Errorf("header_row = %d, want 1 -- row 3 is an excelize gap row, must fall back like CSV does", resp.HeaderRow)
	}
}

// --- row 20 ------------------------------------------------------------------------

func TestSuggestHandler_AGapRowIsAnEmptyArrayNotNull(t *testing.T) {
	id := testIdentity()
	fixture := xlsxOf(t, [][]string{{"Col"}, {"Row2"}, nil, {"Row4"}})
	open := newFakeDocOpen("data.xlsx", xlsxContentType, fixture)
	suggester := &fakeSuggester{enabled: true}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"sample_rows":[["Row2"],[],["Row4"]]`)) {
		t.Errorf("raw body = %s, want a [] gap row inside sample_rows, never null", raw)
	}
	if bytes.Contains(raw, []byte(",null")) || bytes.Contains(raw, []byte("null]")) {
		t.Errorf("raw body = %s, contains a null element", raw)
	}
}

// --- row 23 ------------------------------------------------------------------------

func TestSuggestHandler_AnUndecodableTailAtTheAnsweredRowFallsBackToRowOne(t *testing.T) {
	id := testIdentity()
	fixture := []byte(`a,b
"multi
line",c
d,e
`)
	open := newFakeDocOpen("data.csv", "text/csv", fixture)
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("3")}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, never 400 -- the AI's bad row choice is never the caller's fault (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.HeaderRow != 1 {
		t.Errorf("header_row = %d, want 1 -- row 3's offset opens mid-quote and cannot re-parse", resp.HeaderRow)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(resp.Columns, want) {
		t.Errorf("columns = %v, want %v", resp.Columns, want)
	}
	if resp.Source != "none" {
		t.Errorf("source = %q, want %q", resp.Source, "none")
	}
}
