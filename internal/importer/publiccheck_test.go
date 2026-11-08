package importer

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
)

const (
	supplierNameLine = "- supplier_name: the seller's (supplier's) name, the business that issued the invoice. Not the buyer's."
	supplierTINLine  = "- supplier_tin: the seller's (supplier's) Tax Identification Number. Not the buyer's TIN or an RC number."
)

func TestCheckMappingSystem_IsTheAppPromptPlusTwoSupplierLines(t *testing.T) {
	for _, line := range []string{supplierNameLine, supplierTINLine} {
		if !strings.Contains(checkMappingSystem, "\n"+line) {
			t.Fatalf("checkMappingSystem lacks %q", line)
		}
	}
	stripped := strings.Replace(checkMappingSystem, "\n"+supplierNameLine, "", 1)
	stripped = strings.Replace(stripped, "\n"+supplierTINLine, "", 1)
	if stripped != mappingSystem {
		t.Errorf("checkMappingSystem minus the two supplier lines differs from mappingSystem")
	}
	if strings.Contains(mappingSystem, "- supplier_") {
		t.Errorf("mappingSystem names a supplier field; the app prompt must not change")
	}
}

func TestCheckMappingSchema_RequiresTheImportKeysAndBothSupplierKeys(t *testing.T) {
	var got struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(checkMappingSchema, &got); err != nil {
		t.Fatal(err)
	}
	// The scan in TestDateFormatAndDecimalSeparator_* bars naming the tail keys here; take them from the app schema.
	var app struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(mappingSchema, &app); err != nil {
		t.Fatal(err)
	}
	keys := invoicefields.ImportKeys()
	want := slices.Concat(keys, []string{"supplier_name", "supplier_tin"}, app.Required[len(keys):])
	if len(app.Required[len(keys):]) != 3 {
		t.Fatalf("app schema tail = %v, want 3 keys", app.Required[len(keys):])
	}
	if !reflect.DeepEqual(got.Required, want) {
		t.Errorf("required = %v, want %v", got.Required, want)
	}
	if len(got.Properties) != len(want) {
		t.Errorf("properties = %d, want %d", len(got.Properties), len(want))
	}
	if len(invoicefields.ImportKeys()) != 36 {
		t.Errorf("import keys = %d, want 36", len(invoicefields.ImportKeys()))
	}
	if slices.Contains(mappingFields, "supplier_name") {
		t.Errorf("the app's mappingFields holds supplier_name")
	}
}

func supplierCSV() []byte {
	return []byte("Inv,Seller,Seller TIN,Total\nINV-1,Acme Ltd,12345678-0001,100.00\n")
}

func supplierAnswer() map[string]any {
	return map[string]any{
		"invoice_number": "Inv", "supplier_name": "Seller", "supplier_tin": "Seller TIN",
		"total": "Total", "header_row": json.Number("1"),
	}
}

func TestSuggestCheckMapping_PlacesSupplierNameAndTIN(t *testing.T) {
	f := &fakeSuggester{enabled: true, answer: supplierAnswer()}
	got, err := SuggestCheckMapping(t.Context(), f, supplierCSV(), "csv")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"invoice_number": "Inv", "supplier_name": "Seller", "supplier_tin": "Seller TIN", "total": "Total"}
	if !reflect.DeepEqual(got.Mapping, want) {
		t.Errorf("mapping = %v, want %v", got.Mapping, want)
	}
	if got.Source != "ai" {
		t.Errorf("source = %q, want ai", got.Source)
	}
}

func TestSuggestCheckMapping_SendsTheCheckPrompt(t *testing.T) {
	f := &fakeSuggester{enabled: true, answer: supplierAnswer()}
	if _, err := SuggestCheckMapping(t.Context(), f, supplierCSV(), "csv"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	req := f.calls[0]
	if req.System != checkMappingSystem || string(req.Schema) != string(checkMappingSchema) || req.Purpose != ai.PurposeSpreadsheet {
		t.Errorf("request = purpose %q, check system %v, check schema %v", req.Purpose, req.System == checkMappingSystem, string(req.Schema) == string(checkMappingSchema))
	}
}

type markCapture struct {
	fakeSuggester
	marked bool
}

func (m *markCapture) Call(ctx context.Context, req ai.Request) (map[string]any, error) {
	var got []airecord.Call
	airecord.Record(ctx, func(c airecord.Call) { got = append(got, c) }, nil, airecord.Call{Attempts: 1})
	m.marked = len(got) == 1 && got[0].PublicCheck
	return m.fakeSuggester.Call(ctx, req)
}

func TestSuggestCheckMapping_MarksItsAICallAsAPublicCheck(t *testing.T) {
	m := &markCapture{fakeSuggester: fakeSuggester{enabled: true, answer: supplierAnswer()}}
	if _, err := SuggestCheckMapping(t.Context(), m, supplierCSV(), "csv"); err != nil {
		t.Fatal(err)
	}
	if !m.marked {
		t.Errorf("the suggester's context carries no public-check mark")
	}
}

func TestSuggestCheckMapping_MovedHeaderRowReDecodes(t *testing.T) {
	f := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("3")}}
	got, err := SuggestCheckMapping(t.Context(), f, buildTitleRowsFixture(2), "csv")
	if err != nil {
		t.Fatal(err)
	}
	if got.HeaderRow != 3 || !reflect.DeepEqual(got.Columns, []string{"Inv No", "Total"}) || got.RowsTotal != 2 {
		t.Errorf("got header_row %d columns %v rows %d, want 3, [Inv No Total], 2", got.HeaderRow, got.Columns, got.RowsTotal)
	}
}

func TestSuggestCheckMapping_BlankHeaderAtTheAnsweredRowFallsBackToRowOne(t *testing.T) {
	file := []byte("Title\nMore\n\nInv No,Total\nINV-1,100\n")
	f := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("3")}}
	got, err := SuggestCheckMapping(t.Context(), f, file, "csv")
	if err != nil {
		t.Fatal(err)
	}
	if got.HeaderRow != 1 || !reflect.DeepEqual(got.Columns, []string{"Title"}) {
		t.Errorf("got header_row %d columns %v, want 1, [Title]", got.HeaderRow, got.Columns)
	}
}

func TestSuggestCheckMapping_OffSuggesterAnswersNone(t *testing.T) {
	var b strings.Builder
	b.WriteString("Inv,Total\n")
	for range 7 {
		b.WriteString("INV,1.00\n")
	}
	got, err := SuggestCheckMapping(t.Context(), nil, []byte(b.String()), "csv")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "none" || got.RowsTotal != 7 || len(got.SampleRows) != 5 || got.Mapping == nil || len(got.Mapping) != 0 || got.HeaderRow != 1 {
		t.Errorf("got %+v, want none, 7 rows, 5 samples, empty non-nil mapping", got)
	}
}

func TestSuggestCheckMapping_FailedSuggesterCallAnswersNone(t *testing.T) {
	f := &fakeSuggester{enabled: true, err: errors.New("boom")}
	got, err := SuggestCheckMapping(t.Context(), f, supplierCSV(), "csv")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "none" || got.Mapping == nil || len(got.Mapping) != 0 || !reflect.DeepEqual(got.Columns, []string{"Inv", "Seller", "Seller TIN", "Total"}) {
		t.Errorf("got %+v, want none, row-1 columns, empty mapping", got)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %d, want 1", len(f.calls))
	}
}

func TestSuggestCheckMapping_UndecodableFileIsAnError(t *testing.T) {
	f := &fakeSuggester{enabled: true, answer: supplierAnswer()}
	if _, err := SuggestCheckMapping(t.Context(), f, []byte("not a zip"), "xlsx"); err == nil {
		t.Fatal("want an error")
	}
	if len(f.calls) != 0 {
		t.Errorf("suggester called %d time(s), want 0", len(f.calls))
	}
}
