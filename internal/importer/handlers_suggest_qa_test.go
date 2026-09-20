// handlers_suggest_qa_test.go: QA coverage the Test Specs left open -- the re-decoded
// header the guard must read, the window bound on the AI's header_row, the source:"ai"
// arm a resolved row alone reaches, and a nil suggester.
package importer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestSuggestHandler_TheGuardReadsTheReDecodedHeaderNotTheWindowRow: decodeCSV sniffs the
// delimiter off line 1 and decodeCSVFrom sniffs it off the resolved row, so window[2] and
// DecodeFrom's header are two different splits of the same line. Passing the window row
// compiles and silently unplaces every field (§6 rule 3).
func TestSuggestHandler_TheGuardReadsTheReDecodedHeaderNotTheWindowRow(t *testing.T) {
	id := testIdentity()
	// Line 1 sniffs ';', so row 3 read under that delimiter is the single column
	// "Inv No,Total"; read from row 3 it sniffs ',' and is two columns.
	fixture := []byte("Title;A;B\nMore\nInv No,Total\nINV-1,100\nINV-2,200\n")
	open := newFakeDocOpen("data.csv", "text/csv", fixture)
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{
		"header_row": json.Number("3"), "invoice_number": "Inv No", "total": "Total",
	}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if want := []string{"Inv No", "Total"}; !reflect.DeepEqual(resp.Columns, want) {
		t.Fatalf("columns = %v, want %v -- the re-decode must re-sniff the delimiter at row 3", resp.Columns, want)
	}
	want := map[string]string{"invoice_number": "Inv No", "total": "Total"}
	if !reflect.DeepEqual(resp.Mapping, want) {
		t.Errorf("mapping = %v, want %v -- the guard was handed the window row, whose one column is %q", resp.Mapping, want, "Inv No,Total")
	}
	if resp.Source != "ai" {
		t.Errorf("source = %q, want %q", resp.Source, "ai")
	}
}

// TestSuggestHandler_AHeaderRowBeyondTheRowsSentFallsBackToRowOne: the window length is
// the handler's own argument to guardHeaderRow. Row 11 exists in the file but was never
// shown to the model, so answering it is a claim about rows it did not see.
// TestGuardHeaderRow_* pins every other unusable shape at the unit level.
func TestSuggestHandler_AHeaderRowBeyondTheRowsSentFallsBackToRowOne(t *testing.T) {
	id := testIdentity()
	var b strings.Builder
	b.WriteString("Col\n")
	for i := 2; i <= 12; i++ {
		fmt.Fprintf(&b, "Row%d\n", i)
	}
	open := newFakeDocOpen("data.csv", "text/csv", []byte(b.String()))
	// 12 physical rows; the window is windowRows (10), so row 11 is real but unseen.
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("11")}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if len(suggester.calls) != 1 {
		t.Fatalf("suggester calls = %d, want 1", len(suggester.calls))
	}
	if got := strings.Count(suggester.calls[0].Text, "\nRow "); got != windowRows {
		t.Fatalf("the prompt carries %d rows, want %d -- the fixture no longer bounds the window below row 11", got, windowRows)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.HeaderRow != defaultHeaderRow {
		t.Errorf("header_row = %d, want %d -- row 11 was never shown to the model", resp.HeaderRow, defaultHeaderRow)
	}
	if want := []string{"Col"}; !reflect.DeepEqual(resp.Columns, want) {
		t.Errorf("columns = %v, want %v", resp.Columns, want)
	}
}

// TestSuggestHandler_AResolvedRowWithNoPlacementsIsStillSourceAI: a title-row file the
// model read correctly but mapped nothing on is still an AI answer -- header_row is the
// other half of what it returns, and the SPA needs the row to open the file at.
func TestSuggestHandler_AResolvedRowWithNoPlacementsIsStillSourceAI(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", buildTitleRowsFixture(2))
	// A correct header_row and no placement the guard can keep.
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{
		"header_row": json.Number("3"), "invoice_number": "No Such Column",
	}}

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.HeaderRow != 3 {
		t.Fatalf("header_row = %d, want 3 -- the premise is a resolved row", resp.HeaderRow)
	}
	if len(resp.Mapping) != 0 {
		t.Fatalf("mapping = %v, want empty -- the premise is a guard-emptied answer", resp.Mapping)
	}
	if resp.Source != "ai" {
		t.Errorf("source = %q, want %q -- the model answered the header row, which is an answer", resp.Source, "ai")
	}
	if !strings.Contains(string(raw), `"mapping":{}`) {
		t.Errorf("raw body = %s, want \"mapping\":{}, never null", raw)
	}
}

// TestSuggestHandler_ANilSuggesterAnswersNone: MappingSuggester's contract is "nil is off".
// Only a nil interface reaches the first half of askMapping's guard.
func TestSuggestHandler_ANilSuggesterAnswersNone(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Inv No", "Total"}, [][]string{{"INV-1", "100"}}))

	rec, raw := doSuggestRequest(t, open.fn(), (&lookupSpy{}).fn(), nil, nil, &id, suggestReqBody(uuid.NewString(), open.doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.Source != "none" {
		t.Errorf("source = %q, want %q", resp.Source, "none")
	}
	if resp.HeaderRow != defaultHeaderRow {
		t.Errorf("header_row = %d, want %d", resp.HeaderRow, defaultHeaderRow)
	}
	if want := []string{"Inv No", "Total"}; !reflect.DeepEqual(resp.Columns, want) {
		t.Errorf("columns = %v, want %v", resp.Columns, want)
	}
}
