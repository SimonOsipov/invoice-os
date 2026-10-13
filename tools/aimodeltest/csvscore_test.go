// Pins csvscore.py on hand-built fixtures: the per-field means and the guardPlacements rules 3-5.
package aimodeltest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type csScore struct {
	Runs   int                           `json:"runs"`
	Fields map[string]map[string]float64 `json:"fields"`
	Total  map[string]float64            `json:"total"`
}

type csRec struct {
	Layout  string         `json:"layout"`
	Variant string         `json:"variant"`
	Model   string         `json:"model"`
	Run     int            `json:"run"`
	Answer  map[string]any `json:"answer,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// csScoreRun writes the fixture into t.TempDir() and runs csvscore.py on it.
func csScoreRun(t *testing.T, layouts []cgLayout, recs []csRec, args ...string) csScore {
	t.Helper()
	cgPython(t)
	dir := t.TempDir()
	lb, err := json.Marshal(layouts)
	if err != nil {
		t.Fatal(err)
	}
	var ab strings.Builder
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		ab.Write(b)
		ab.WriteByte('\n')
	}
	lp, ap := filepath.Join(dir, "layouts.json"), filepath.Join(dir, "csv_answers.jsonl")
	if err := os.WriteFile(lp, lb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ap, []byte(ab.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), "python3", append([]string{"-I", "csvscore.py", lp, ap}, args...)...).Output()
	if err != nil {
		t.Fatalf("csvscore.py: %v", err)
	}
	var got csScore
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return got
}

func csLayout(id string, columns []string, key map[string][]*string) cgLayout {
	return cgLayout{ID: id, Columns: columns, HeaderRow: 1, Key: key}
}

func csRow(layout string, run int, answer map[string]any) csRec {
	return csRec{Layout: layout, Variant: "rows", Model: "m", Run: run, Answer: answer}
}

func csWant(t *testing.T, got map[string]float64, correct, missed, bad float64, what string) {
	t.Helper()
	if got["correct"] != correct || got["missed"] != missed || got["bad"] != bad {
		t.Errorf("%s = %v, want correct %v missed %v bad %v", what, got, correct, missed, bad)
	}
}

func TestCsvscore_ScoresCorrectMissedAndBad(t *testing.T) {
	nul := []*string{nil}
	layouts := []cgLayout{
		csLayout("a", []string{"Invoice No", "Total", "Tax"}, map[string][]*string{
			"invoice_number": {cgStr("Invoice No")}, "total": {cgStr("Total")}, "vat": nul, "line_tax": nul}),
		csLayout("b", []string{"Inv", "Amt"}, map[string][]*string{
			"invoice_number": {cgStr("Inv")}, "total": {cgStr("Amt")}, "vat": nul, "line_tax": nul}),
	}
	recs := []csRec{
		// "bogus" claims Total: rule 3 drops it, else it would null total.
		csRow("a", 1, map[string]any{"invoice_number": "Invoice No", "total": "Total", "vat": nil, "line_tax": nil, "bogus": "Total"}),
		csRow("a", 2, map[string]any{"invoice_number": nil, "total": "Tax", "vat": nil, "line_tax": nil}),
		csRow("b", 1, map[string]any{"invoice_number": "Inv", "total": nil, "vat": "Amt", "line_tax": nil}),
		csRow("b", 2, map[string]any{"invoice_number": "Inv", "total": "Amt", "vat": nil, "line_tax": nil}),
		{Layout: "a", Variant: "headers", Model: "m", Run: 1, Answer: map[string]any{"invoice_number": "Tax"}},
		{Layout: "a", Variant: "rows", Model: "m", Run: 3, Error: "HTTP 500"},
	}
	got := csScoreRun(t, layouts, recs, "--variant", "rows")
	if got.Runs != 4 {
		t.Errorf("runs = %d, want 4 (the headers variant and the errored call are out)", got.Runs)
	}
	csWant(t, got.Fields["invoice_number"], 0.75, 0.25, 0, "invoice_number")
	csWant(t, got.Fields["total"], 0.5, 0.25, 0.25, "total")
	csWant(t, got.Fields["vat"], 0, 0, 0.25, "vat")
	csWant(t, got.Fields["line_tax"], 0, 0, 0, "line_tax")
	csWant(t, got.Total, 1.25, 0.5, 0.5, "total row")
}

func TestCsvscore_ADuplicateClaimNullsBothFields(t *testing.T) {
	layouts := []cgLayout{csLayout("a", []string{"Invoice No", "Tax"}, map[string][]*string{
		"vat": {cgStr("Tax")}, "line_tax": {nil}})}
	got := csScoreRun(t, layouts, []csRec{csRow("a", 1, map[string]any{"vat": "Tax", "line_tax": "Tax"})})
	csWant(t, got.Fields["vat"], 0, 1, 0, "vat")
	csWant(t, got.Fields["line_tax"], 0, 0, 0, "line_tax")
}

func TestCsvscore_AHeaderNotInTheColumnsIsNull(t *testing.T) {
	layouts := []cgLayout{csLayout("a", []string{"Total"}, map[string][]*string{"total": {cgStr("Total")}})}
	got := csScoreRun(t, layouts, []csRec{csRow("a", 1, map[string]any{"total": "Totl"})})
	csWant(t, got.Fields["total"], 0, 1, 0, "total")
}

func TestCsvscore_FieldsFilterLimitsTheTotal(t *testing.T) {
	layouts := []cgLayout{csLayout("a", []string{"Invoice No", "Total", "Tax"}, map[string][]*string{
		"invoice_number": {cgStr("Invoice No")}, "total": {cgStr("Total")}, "vat": {cgStr("Tax")}})}
	got := csScoreRun(t, layouts, []csRec{csRow("a", 1, map[string]any{"invoice_number": "Invoice No", "total": "Total", "vat": "Tax"})},
		"--fields", "invoice_number,total")
	if len(got.Fields) != 2 {
		t.Errorf("scored fields = %v, want invoice_number and total only", got.Fields)
	}
	csWant(t, got.Total, 2, 0, 0, "total row")
}
