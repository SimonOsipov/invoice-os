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

func checkMap(extra ...string) map[string]string {
	m := map[string]string{"invoice_number": "Inv", "total": "Total"}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

func checkNumbers(s CheckSheet) []string {
	var out []string
	for _, i := range s.Invoices {
		out = append(out, i.Number)
	}
	return out
}

func TestCheckSpreadsheet_ChecksTheFirstTwoInvoicesInFileOrder(t *testing.T) {
	csv := "Inv,Total\nA,1\nB,2\nA,1\nC,4\nD,5\n"
	got, err := CheckSpreadsheet(t.Context(), []byte(csv), "csv", 1, checkMap(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(checkNumbers(got), []string{"A", "B"}) || got.RowsTotal != 5 || got.InvoicesTotal != 4 {
		t.Fatalf("got %v rows %d invoices %d, want [A B] 5 4", checkNumbers(got), got.RowsTotal, got.InvoicesTotal)
	}
	if n := len(got.Invoices[0].Invoice.LineItems); n != 2 {
		t.Errorf("A has %d lines, want 2", n)
	}
}

func TestCheckSpreadsheet_LimitAboveTheInvoiceCountReturnsEveryInvoice(t *testing.T) {
	csv := "Inv,Total\nA,1\nB,2\nC,3\n"
	for limit, want := range map[int]int{2: 2, 5: 3} {
		got, err := CheckSpreadsheet(t.Context(), []byte(csv), "csv", 1, checkMap(), limit)
		if err != nil || len(got.Invoices) != want {
			t.Errorf("limit %d: %d invoices, err %v, want %d", limit, len(got.Invoices), err, want)
		}
	}
}

func TestCheckSpreadsheet_ReadsSupplierNameVerbatimAndRestoresABareTINHyphen(t *testing.T) {
	csv := "Inv,Total,Seller,TIN\nA,1,Acme Ltd,123456780001\nB,2,Beta,12345678-0001\nC,3,Gamma,1234567890\n"
	got, err := CheckSpreadsheet(t.Context(), []byte(csv), "csv", 1, checkMap("supplier_name", "Seller", "supplier_tin", "TIN"), 5)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"Acme Ltd", "Beta", "Gamma"}
	tins := []string{"12345678-0001", "12345678-0001", "1234567890"}
	for i, ci := range got.Invoices {
		inv := ci.Invoice
		if inv == nil || inv.SupplierName == nil || *inv.SupplierName != names[i] || inv.SupplierTIN == nil || *inv.SupplierTIN != tins[i] {
			t.Errorf("invoice %d: %+v, want %s / %s", i, inv, names[i], tins[i])
		}
	}
}

func TestCheckSpreadsheet_BlankOrUnmappedSupplierIsAbsent(t *testing.T) {
	cases := map[string]struct {
		csv string
		m   map[string]string
	}{
		"blank":    {"Inv,Total,Seller,TIN\nA,1,,\n", checkMap("supplier_name", "Seller", "supplier_tin", "TIN")},
		"unmapped": {"Inv,Total,Seller,TIN\nA,1,Acme,123456780001\n", checkMap()},
	}
	for name, c := range cases {
		got, err := CheckSpreadsheet(t.Context(), []byte(c.csv), "csv", 1, c.m, 2)
		if err != nil || len(got.Invoices) != 1 || got.Invoices[0].Invoice == nil {
			t.Fatalf("%s: %+v err %v", name, got, err)
		}
		if inv := got.Invoices[0].Invoice; inv.SupplierName != nil || inv.SupplierTIN != nil {
			t.Errorf("%s: supplier name %v tin %v, want both nil", name, inv.SupplierName, inv.SupplierTIN)
		}
	}
}

func TestCheckSpreadsheet_AQuarantinedInvoiceCarriesItsProblem(t *testing.T) {
	csv := "Inv,Total,Date\nA,1,31/02/2026\nB,2,2026-01-02\nC,3,2026-01-03\n"
	got, err := CheckSpreadsheet(t.Context(), []byte(csv), "csv", 1, checkMap("issue_date", "Date"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(checkNumbers(got), []string{"A", "B"}) {
		t.Fatalf("got %v, want [A B]", checkNumbers(got))
	}
	a, b := got.Invoices[0], got.Invoices[1]
	if a.Invoice != nil || a.Problem == nil || a.Problem.Field != "issue_date" || !slices.Equal(a.Problem.Rows, []int{2}) {
		t.Errorf("A = %+v, want a Problem on issue_date row 2 and no Invoice", a)
	}
	if b.Invoice == nil || b.Problem != nil {
		t.Errorf("B = %+v, want ready", b)
	}
}

func TestCheckSpreadsheet_BlankNumberRowsAreNotInvoices(t *testing.T) {
	csv := "Inv,Total\n,1\nA,2\n,3\nB,4\nC,5\n"
	got, err := CheckSpreadsheet(t.Context(), []byte(csv), "csv", 1, checkMap(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(checkNumbers(got), []string{"A", "B"}) || got.RowsTotal != 5 || got.InvoicesTotal != 3 {
		t.Errorf("got %v rows %d invoices %d, want [A B] 5 3", checkNumbers(got), got.RowsTotal, got.InvoicesTotal)
	}
}

func TestCheckSpreadsheet_RefusesAMappingWithoutInvoiceNumber(t *testing.T) {
	_, err := CheckSpreadsheet(t.Context(), []byte("Inv,Total\nA,1\n"), "csv", 1, map[string]string{"total": "Total"}, 2)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

func TestCheckSpreadsheet_RefusesSupplierEmailAsAKey(t *testing.T) {
	_, err := CheckSpreadsheet(t.Context(), []byte("Inv,Total\nA,1\n"), "csv", 1, checkMap("supplier_email", "Total"), 2)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "supplier_email") {
		t.Errorf("err = %v, want ErrValidation naming supplier_email", err)
	}
}

func TestResolveMapping_AppStillRefusesSupplierName(t *testing.T) {
	_, err := resolveMapping(map[string]string{"invoice_number": "Inv", "supplier_name": "Seller"}, []string{"Inv", "Seller"})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

func TestCheckSpreadsheet_HonoursAHeaderRowAboveOne(t *testing.T) {
	csv := "Quarterly report,\nInv,Total\nA,1\nB,2\n"
	got, err := CheckSpreadsheet(t.Context(), []byte(csv), "csv", 2, checkMap(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(checkNumbers(got), []string{"A", "B"}) || got.RowsTotal != 2 {
		t.Errorf("got %v rows %d, want [A B] 2", checkNumbers(got), got.RowsTotal)
	}
}

func TestCheckSpreadsheet_HeaderRowZeroMeansOne(t *testing.T) {
	csv := []byte("Inv,Total\nA,1\nB,2\n")
	zero, err0 := CheckSpreadsheet(t.Context(), csv, "csv", 0, checkMap(), 2)
	one, err1 := CheckSpreadsheet(t.Context(), csv, "csv", 1, checkMap(), 2)
	if err0 != nil || err1 != nil || !reflect.DeepEqual(zero, one) {
		t.Errorf("zero %+v (%v) != one %+v (%v)", zero, err0, one, err1)
	}
}

func TestCheckSpreadsheet_HeaderOnlyFileHasNoInvoices(t *testing.T) {
	got, err := CheckSpreadsheet(t.Context(), []byte("Inv,Total\n"), "csv", 1, checkMap(), 2)
	if err != nil || len(got.Invoices) != 0 || got.RowsTotal != 0 || got.InvoicesTotal != 0 {
		t.Errorf("got %+v err %v, want empty", got, err)
	}
}

func pcf(name, v string) CheckField { return CheckField{Name: name, Value: &v} }

func TestCheckDocumentInvoice_TakesSupplierFromTheReading(t *testing.T) {
	inv, rowErr := CheckDocumentInvoice([]CheckField{
		pcf("invoice_number", "INV-1"), pcf("supplier_name", " Acme "), pcf("supplier_tin", "12345678-0001"),
	})
	if rowErr != nil {
		t.Fatalf("rowErr = %+v", rowErr)
	}
	if inv.InvoiceNumber != "INV-1" || inv.SupplierName == nil || *inv.SupplierName != "Acme" ||
		inv.SupplierTIN == nil || *inv.SupplierTIN != "12345678-0001" {
		t.Errorf("number %q, name %v, tin %v", inv.InvoiceNumber, inv.SupplierName, inv.SupplierTIN)
	}
}

func TestCheckDocumentInvoice_RestoresABareSupplierTINHyphenAndReadsTheLines(t *testing.T) {
	inv, rowErr := CheckDocumentInvoice([]CheckField{
		pcf("invoice_number", "INV-1"), pcf("supplier_tin", "123456780001"),
		pcf("line_items[2].description", "Widget"), pcf("line_items[2].quantity", "2"), pcf("line_items[2].unit_price", "5.00"),
		pcf("line_items[1].description", "Gadget"), pcf("line_items[1].quantity", "1"), pcf("line_items[1].unit_price", "3.00"),
	})
	if rowErr != nil {
		t.Fatalf("rowErr = %+v", rowErr)
	}
	if inv.SupplierTIN == nil || *inv.SupplierTIN != "12345678-0001" {
		t.Errorf("SupplierTIN = %v, want 12345678-0001", inv.SupplierTIN)
	}
	if len(inv.LineItems) != 2 || inv.LineItems[0].Description == nil || *inv.LineItems[0].Description != "Gadget" ||
		*inv.LineItems[1].Description != "Widget" {
		t.Errorf("LineItems = %+v, want Gadget then Widget", inv.LineItems)
	}
}

func TestCheckDocumentInvoice_BlankSupplierIsAbsent(t *testing.T) {
	inv, rowErr := CheckDocumentInvoice([]CheckField{pcf("invoice_number", "INV-1"), pcf("supplier_name", "  ")})
	if rowErr != nil {
		t.Fatalf("rowErr = %+v", rowErr)
	}
	if inv.SupplierName != nil || inv.SupplierTIN != nil {
		t.Errorf("name %v, tin %v; want both nil", inv.SupplierName, inv.SupplierTIN)
	}
}

func TestCheckDocumentInvoice_NoInvoiceNumberIsAProblem(t *testing.T) {
	inv, rowErr := CheckDocumentInvoice([]CheckField{pcf("supplier_name", "Acme")})
	if inv != nil || rowErr == nil || rowErr.Field != "invoice_number" {
		t.Fatalf("inv %v, rowErr %+v; want nil and a RowError on invoice_number", inv, rowErr)
	}
}

func TestCheckDocumentInvoice_PoorScanIsAProblemWithThePoorScanSentence(t *testing.T) {
	reason := "unreadable"
	inv, rowErr := CheckDocumentInvoice([]CheckField{{Name: "document_text_layer", Reason: &reason}})
	if inv != nil || rowErr == nil || rowErr.Message != poorScanMessage {
		t.Fatalf("inv %v, rowErr %+v; want nil and poorScanMessage", inv, rowErr)
	}
}
