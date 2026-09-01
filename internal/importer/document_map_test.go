// document_map_test.go: specs for documentCreateInput (EXTR-06-02, task-762). Pure Go, no DB.
// Authored RED in Mode A against a stub, now green against the real mapper (commit a93a9518);
// QA added adversarial coverage below the MAP-11 spec section (task-762 Mode B).
//
// Spec-to-test map (Test Specs table, EXTR-06-02 / task-762):
//
//	MAP-01 TestDocumentCreateInput_FullTenFieldExtractionMapsEveryColumn
//	MAP-02 TestDocumentCreateInput_SourceRowsAlwaysNil
//	MAP-03 TestDocumentCreateInput_SupplierFieldsNeverSetEvenWhenExtractionCarriesThem
//	MAP-04 TestDocumentCreateInput_UnknownFieldNamesDropped
//	MAP-05 TestDocumentCreateInput_NullInvoiceNumberReturnsStructuralRowError
//	MAP-06 TestDocumentCreateInput_WhitespaceOnlyInvoiceNumberTreatedAsAbsent
//	MAP-07 TestDocumentCreateInput_IssueDateParsing
//	MAP-08 TestDocumentCreateInput_MoneyFieldsPassThroughVerbatim
//	MAP-09 TestDocumentCreateInput_AmbiguousInvoiceNumberWithDecidedValueStillWritten
//	MAP-10 TestDocumentCreateInput_LineItemsNilNoAppendInBody
//	MAP-11 TestDocumentCreateInput_MapperFieldNamesMatchesHeaderFieldsInOrder
//
// Every spec below carries at least one POSITIVE expected-value assertion (not just an
// absent-field check a zero-value stub would satisfy vacuously) -- mutation-confirmed
// (task-762 QA pass) to still fail on VALUE, for the AC each spec covers, not just on a
// compile or panic error.
package importer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

func mpPtr(s string) *string         { return &s }
func timePtr(t time.Time) *time.Time { return &t }

// --- MAP-01: full ten-field extraction maps every column --------------------------------

func TestDocumentCreateInput_FullTenFieldExtractionMapsEveryColumn(t *testing.T) {
	ex := SettledExtraction{
		JobID: "job-map-01",
		Fields: []extractedField{
			{Name: "invoice_number", Value: mpPtr("INV-100")},
			{Name: "issue_date", Value: mpPtr("2026-07-01")},
			{Name: "supplier_tin", Value: mpPtr("SHOULD-NOT-APPEAR-TIN")},
			{Name: "supplier_name", Value: mpPtr("SHOULD-NOT-APPEAR-NAME")},
			{Name: "buyer_tin", Value: mpPtr("BUYER-TIN-1")},
			{Name: "buyer_name", Value: mpPtr("Buyer Co")},
			{Name: "currency", Value: mpPtr("USD")},
			{Name: "subtotal", Value: mpPtr("100.00")},
			{Name: "vat", Value: mpPtr("7.50")},
			{Name: "total", Value: mpPtr("107.50")},
		},
	}

	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.EntityID != "entity-1" {
		t.Errorf("EntityID = %q, want %q", got.EntityID, "entity-1")
	}
	if got.InvoiceNumber != "INV-100" {
		t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, "INV-100")
	}
	wantDate := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if got.IssueDate == nil || !got.IssueDate.Equal(wantDate) {
		t.Errorf("IssueDate = %v, want %v", got.IssueDate, wantDate)
	}
	if got.SupplierTIN != nil {
		t.Errorf("SupplierTIN = %q, want nil (Q11: store overwrites it)", *got.SupplierTIN)
	}
	if got.SupplierName != nil {
		t.Errorf("SupplierName = %q, want nil (Q11: store overwrites it)", *got.SupplierName)
	}
	if got.BuyerTIN == nil || *got.BuyerTIN != "BUYER-TIN-1" {
		t.Errorf("BuyerTIN = %v, want %q", got.BuyerTIN, "BUYER-TIN-1")
	}
	if got.BuyerName == nil || *got.BuyerName != "Buyer Co" {
		t.Errorf("BuyerName = %v, want %q", got.BuyerName, "Buyer Co")
	}
	if got.Currency == nil || *got.Currency != "USD" {
		t.Errorf("Currency = %v, want %q", got.Currency, "USD")
	}
	if got.Subtotal == nil || *got.Subtotal != "100.00" {
		t.Errorf("Subtotal = %v, want %q", got.Subtotal, "100.00")
	}
	if got.VAT == nil || *got.VAT != "7.50" {
		t.Errorf("VAT = %v, want %q", got.VAT, "7.50")
	}
	if got.Total == nil || *got.Total != "107.50" {
		t.Errorf("Total = %v, want %q", got.Total, "107.50")
	}
	if got.SourceDocumentID == nil || *got.SourceDocumentID != "doc-1" {
		t.Errorf("SourceDocumentID = %v, want %q", got.SourceDocumentID, "doc-1")
	}
	if got.SourceRows != nil {
		t.Errorf("SourceRows = %v, want nil", got.SourceRows)
	}
	if got.LineItems != nil {
		t.Errorf("LineItems = %v, want nil", got.LineItems)
	}
	if got.ImportBatchID != nil {
		t.Errorf("ImportBatchID = %v, want nil -- the mapper never sees a batch id (D-17)", got.ImportBatchID)
	}
}

// --- MAP-02: SourceRows nil regardless of what Fields carries ---------------------------

func TestDocumentCreateInput_SourceRowsAlwaysNil(t *testing.T) {
	cases := []struct {
		name          string
		fields        []extractedField
		wantInvNumber string
	}{
		{
			name:          "onlyRequiredField",
			fields:        []extractedField{{Name: "invoice_number", Value: mpPtr("INV-200")}},
			wantInvNumber: "INV-200",
		},
		{
			name: "allTenFields",
			fields: []extractedField{
				{Name: "invoice_number", Value: mpPtr("INV-201")},
				{Name: "issue_date", Value: mpPtr("2026-01-15")},
				{Name: "supplier_tin", Value: mpPtr("SUP-TIN")},
				{Name: "supplier_name", Value: mpPtr("Supplier Co")},
				{Name: "buyer_tin", Value: mpPtr("BUY-TIN")},
				{Name: "buyer_name", Value: mpPtr("Buyer Co")},
				{Name: "currency", Value: mpPtr("EUR")},
				{Name: "subtotal", Value: mpPtr("10.00")},
				{Name: "vat", Value: mpPtr("1.00")},
				{Name: "total", Value: mpPtr("11.00")},
			},
			wantInvNumber: "INV-201",
		},
		{
			// extractedField (document.go) carries no page/region metadata -- SettledExtraction's
			// query never selects region_json (EXTR-06-01) -- so this case substitutes the
			// richest input this type CAN express (a flagged field with a decided value) for
			// the original Test Specs wording "fields with regions on page 1".
			name:          "decoratedFieldWithReason",
			fields:        []extractedField{{Name: "invoice_number", Value: mpPtr("INV-202"), Reason: mpPtr("ambiguous")}},
			wantInvNumber: "INV-202",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rowErr := documentCreateInput("entity-1", "doc-1", SettledExtraction{Fields: tc.fields})
			if rowErr != nil {
				t.Fatalf("rowErr = %+v, want nil", rowErr)
			}
			// Positive companion: proves the mapper actually ran the real mapping for this
			// fixture, so the SourceRows-nil check below isn't vacuously true of a stub too.
			if got.InvoiceNumber != tc.wantInvNumber {
				t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, tc.wantInvNumber)
			}
			if got.SourceRows != nil {
				t.Errorf("SourceRows = %v, want nil", got.SourceRows)
			}
		})
	}
}

// --- MAP-03: supplier fields never set, even when the extraction carries them -----------

func TestDocumentCreateInput_SupplierFieldsNeverSetEvenWhenExtractionCarriesThem(t *testing.T) {
	ex := SettledExtraction{
		Fields: []extractedField{
			{Name: "invoice_number", Value: mpPtr("INV-300")},
			{Name: "supplier_tin", Value: mpPtr("SUP-TIN-999")},
			{Name: "supplier_name", Value: mpPtr("Supplier Co Ltd")},
		},
	}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.InvoiceNumber != "INV-300" {
		t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, "INV-300")
	}
	if got.SupplierTIN != nil {
		t.Errorf("SupplierTIN = %q, want nil -- Store.Create overwrites it from the entity on every write (Q11)", *got.SupplierTIN)
	}
	if got.SupplierName != nil {
		t.Errorf("SupplierName = %q, want nil -- Store.Create overwrites it from the entity on every write (Q11)", *got.SupplierName)
	}
}

// --- MAP-04: unknown field names dropped -------------------------------------------------

// invoice_date/total_amount are ILLUSTRATIVE off-vocabulary names, not the mock extractor's
// current output: EXTR-12-01 renamed its fields onto the real vocabulary (issue_date, total).
// They stay unmapped either way, alongside two synthetic ones (line_items,
// line_items[0].line_total). internal/importer cannot import internal/extraction
// (document_deps_test.go), so every value here is copied, not referenced.
func TestDocumentCreateInput_UnknownFieldNamesDropped(t *testing.T) {
	ex := SettledExtraction{
		Fields: []extractedField{
			{Name: "invoice_number", Value: mpPtr("MOCK-INV-0001")},
			{Name: "invoice_date", Value: mpPtr("2026-01-01")},        // unknown: not "issue_date"
			{Name: "total_amount", Value: mpPtr("1000.00")},           // unknown: not "total"
			{Name: "supplier_tin", Value: mpPtr("MOCK-TIN-SUPPLIER")}, // known, but see MAP-03/Q11
			{Name: "buyer_tin", Value: mpPtr("MOCK-TIN-BUYER")},
			{Name: "line_items", Value: mpPtr("garbage")},
			{Name: "line_items[0].line_total", Value: mpPtr("999.99")},
		},
	}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.InvoiceNumber != "MOCK-INV-0001" {
		t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, "MOCK-INV-0001")
	}
	if got.IssueDate != nil {
		t.Errorf("IssueDate = %v, want nil -- \"invoice_date\" is not \"issue_date\", must be dropped, not aliased", got.IssueDate)
	}
	if got.Total != nil {
		t.Errorf("Total = %v, want nil -- \"total_amount\" is not \"total\", must be dropped, not aliased", got.Total)
	}
	if got.BuyerTIN == nil || *got.BuyerTIN != "MOCK-TIN-BUYER" {
		t.Errorf("BuyerTIN = %v, want %q (buyer_tin IS a known field)", got.BuyerTIN, "MOCK-TIN-BUYER")
	}
	if got.SupplierTIN != nil {
		t.Errorf("SupplierTIN = %v, want nil (Q11 -- unset regardless of \"known\"/\"unknown\")", got.SupplierTIN)
	}
	if got.LineItems != nil {
		t.Errorf("LineItems = %v, want nil -- line_items/line_items[0].line_total are not header fields", got.LineItems)
	}
}

// --- MAP-05: NULL invoice_number returns a structural RowError --------------------------

func TestDocumentCreateInput_NullInvoiceNumberReturnsStructuralRowError(t *testing.T) {
	ex := SettledExtraction{
		Fields: []extractedField{
			{Name: "invoice_number", Value: nil, Reason: mpPtr("unreadable")},
			{Name: "total", Value: mpPtr("50.00")},
		},
	}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr == nil {
		t.Fatal("rowErr = nil, want a structural RowError")
	}
	if rowErr.Field != "invoice_number" {
		t.Errorf("rowErr.Field = %q, want %q", rowErr.Field, "invoice_number")
	}
	if rowErr.RuleKey != "" {
		t.Errorf("rowErr.RuleKey = %q, want \"\" -- a missing invoice_number is structural, not a rule violation", rowErr.RuleKey)
	}
	if !reflect.DeepEqual(got, invoice.CreateInput{}) {
		t.Errorf("CreateInput = %+v, want the zero value", got)
	}
}

// --- MAP-06: whitespace-only invoice_number treated as absent ---------------------------

func TestDocumentCreateInput_WhitespaceOnlyInvoiceNumberTreatedAsAbsent(t *testing.T) {
	ex := SettledExtraction{
		Fields: []extractedField{
			{Name: "invoice_number", Value: mpPtr("   ")},
		},
	}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr == nil {
		t.Fatal("rowErr = nil, want a structural RowError for a whitespace-only invoice_number")
	}
	if rowErr.Field != "invoice_number" {
		t.Errorf("rowErr.Field = %q, want %q", rowErr.Field, "invoice_number")
	}
	if rowErr.RuleKey != "" {
		t.Errorf("rowErr.RuleKey = %q, want \"\"", rowErr.RuleKey)
	}
	if !reflect.DeepEqual(got, invoice.CreateInput{}) {
		t.Errorf("CreateInput = %+v, want the zero value", got)
	}
}

// --- MAP-07: issue_date parsing -----------------------------------------------------------

func TestDocumentCreateInput_IssueDateParsing(t *testing.T) {
	cases := []struct {
		name      string
		dateField *extractedField // nil = field absent entirely
		wantErr   bool
		wantDate  *time.Time
	}{
		{
			name:      "malformed",
			dateField: &extractedField{Name: "issue_date", Value: mpPtr("2026-13-45")},
			wantErr:   true,
		},
		{
			name:      "valid",
			dateField: &extractedField{Name: "issue_date", Value: mpPtr("2026-07-01")},
			wantDate:  timePtr(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)),
		},
		{
			name:      "absent",
			dateField: nil,
			wantDate:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := []extractedField{{Name: "invoice_number", Value: mpPtr("INV-DATE-1")}}
			if tc.dateField != nil {
				fields = append(fields, *tc.dateField)
			}
			got, rowErr := documentCreateInput("entity-1", "doc-1", SettledExtraction{Fields: fields})
			if tc.wantErr {
				if rowErr == nil {
					t.Fatal("rowErr = nil, want a RowError on issue_date")
				}
				if rowErr.Field != "issue_date" {
					t.Errorf("rowErr.Field = %q, want %q", rowErr.Field, "issue_date")
				}
				return
			}
			if rowErr != nil {
				t.Fatalf("rowErr = %+v, want nil", rowErr)
			}
			// Positive companion, run for BOTH "valid" and "absent": proves the mapper ran
			// the real mapping, so the date-specific check below can't pass vacuously.
			if got.InvoiceNumber != "INV-DATE-1" {
				t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, "INV-DATE-1")
			}
			if tc.wantDate == nil {
				if got.IssueDate != nil {
					t.Errorf("IssueDate = %v, want nil", got.IssueDate)
				}
				return
			}
			if got.IssueDate == nil || !got.IssueDate.Equal(*tc.wantDate) {
				t.Errorf("IssueDate = %v, want %v", got.IssueDate, *tc.wantDate)
			}
		})
	}
}

// --- MAP-08: money fields pass through verbatim -------------------------------------------

func TestDocumentCreateInput_MoneyFieldsPassThroughVerbatim(t *testing.T) {
	cases := []struct {
		name                 string
		subtotal, vat, total string
	}{
		{name: "typical", subtotal: "100.00", vat: "7.50", total: "107.50"},
		{name: "negative", subtotal: "-1.00", vat: "-1.00", total: "-1.00"},
		{name: "zero", subtotal: "0.00", vat: "0.00", total: "0.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := SettledExtraction{Fields: []extractedField{
				{Name: "invoice_number", Value: mpPtr("INV-MONEY-1")},
				{Name: "subtotal", Value: mpPtr(tc.subtotal)},
				{Name: "vat", Value: mpPtr(tc.vat)},
				{Name: "total", Value: mpPtr(tc.total)},
			}}
			got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
			if rowErr != nil {
				t.Fatalf("rowErr = %+v, want nil", rowErr)
			}
			if got.Subtotal == nil || *got.Subtotal != tc.subtotal {
				t.Errorf("Subtotal = %v, want %q", got.Subtotal, tc.subtotal)
			}
			if got.VAT == nil || *got.VAT != tc.vat {
				t.Errorf("VAT = %v, want %q", got.VAT, tc.vat)
			}
			if got.Total == nil || *got.Total != tc.total {
				t.Errorf("Total = %v, want %q", got.Total, tc.total)
			}
		})
	}
}

// --- MAP-09: ambiguous invoice_number with a decided value is still written -------------

func TestDocumentCreateInput_AmbiguousInvoiceNumberWithDecidedValueStillWritten(t *testing.T) {
	ex := SettledExtraction{Fields: []extractedField{
		{Name: "invoice_number", Value: mpPtr("INV-AMBIG-1"), Reason: mpPtr("ambiguous")},
	}}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil -- an ambiguous field with a decided value is written; only a NULL value contributes nothing", rowErr)
	}
	if got.InvoiceNumber != "INV-AMBIG-1" {
		t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, "INV-AMBIG-1")
	}
}

// --- MAP-10: LineItems nil, no append() anywhere in documentCreateInput's body ----------

func TestDocumentCreateInput_LineItemsNilNoAppendInBody(t *testing.T) {
	ex := SettledExtraction{Fields: []extractedField{
		{Name: "invoice_number", Value: mpPtr("INV-LINE-1")},
		{Name: "subtotal", Value: mpPtr("10.00")},
	}}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	// Positive companion: proves the real mapping ran, so LineItems==nil below can't pass
	// vacuously against a stub that never touches Fields at all.
	if got.InvoiceNumber != "INV-LINE-1" {
		t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, "INV-LINE-1")
	}
	if got.LineItems != nil {
		t.Errorf("LineItems = %v, want nil -- documentCreateInput never builds line items", got.LineItems)
	}

	// AST half: no `append(` call anywhere inside documentCreateInput's body. Scoped to that
	// one func, not the whole file -- SettledExtraction (same file) legitimately appends to
	// ex.Fields, and scanning the whole file would false-positive on that.
	root := sxDepsRepoRoot(t) // reused from document_deps_test.go, same package
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "internal/importer/document.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse document.go: %v", err)
	}

	var mapperFn, settledFn *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fd.Name.Name {
		case "documentCreateInput":
			mapperFn = fd
		case "SettledExtraction":
			settledFn = fd
		}
	}
	if mapperFn == nil {
		t.Fatal("documentCreateInput func decl not found in document.go -- the scan below would be vacuous")
	}
	if settledFn == nil || !mpBodyCallsAppend(settledFn) {
		t.Fatal("control needle failed: SettledExtraction no longer calls append -- the append-detector can no longer find a planted hit, so the absence check below means nothing")
	}
	if mpBodyCallsAppend(mapperFn) {
		t.Error("documentCreateInput's body calls append -- it must never build a line item slice; LineItems stays nil")
	}
}

// mpBodyCallsAppend reports whether fn's body contains a call to the builtin append.
func mpBodyCallsAppend(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "append" {
			found = true
		}
		return true
	})
	return found
}

// --- MAP-11: the vocabulary drift guard ---------------------------------------------------

// TestDocumentCreateInput_MapperFieldNamesMatchesHeaderFieldsInOrder guards against
// internal/importer growing its own copy of extraction.HeaderFields (required because
// document_deps_test.go / SX-09 forbids importing internal/extraction, so nothing compiler-
// links the two lists) that silently drifts from the real vocabulary.
func TestDocumentCreateInput_MapperFieldNamesMatchesHeaderFieldsInOrder(t *testing.T) {
	root := sxDepsRepoRoot(t)

	mapperNames := mpStringSliceVar(t, filepath.Join(root, "internal/importer/document.go"), "mapperFieldNames")
	extractionNames := mpStringSliceVar(t, filepath.Join(root, "internal/extraction/vocabulary.go"), "HeaderFields")

	// Floor + control needle: an absence scan that silently found zero names on BOTH sides
	// would report "equal" for the wrong reason. Prove each side actually parsed something.
	if len(extractionNames) == 0 {
		t.Fatal("parsed 0 names from internal/extraction/vocabulary.go's HeaderFields -- the parser side of this guard is broken, so the comparison below would be vacuous")
	}
	if len(mapperNames) == 0 {
		t.Fatal("parsed 0 names from internal/importer/document.go's mapperFieldNames -- the mapper's own copy of the vocabulary does not exist yet (expected RED for task-762 Mode A)")
	}
	if !slices.Equal(mapperNames, extractionNames) {
		t.Errorf("mapperFieldNames = %v, want %v (element-for-element, in order, matching extraction.HeaderFields)", mapperNames, extractionNames)
	}
}

// --- Adversarial coverage (QA, task-762 Mode B) ------------------------------------------

// TestDocumentCreateInput_DuplicateFieldNameLastWriteWins: two rank-0 rows for the same
// field_name are legal (EXTR-05 D-4 -- SettledExtraction never dedupes). The mapper builds
// its values map by iterating ex.Fields in order, so the LAST matching entry wins; determinism
// end-to-end rests on SettledExtraction's `ORDER BY created_at, id` (SX-05-FIX), not on
// anything here -- this test pins the mapper's own half of that contract.
func TestDocumentCreateInput_DuplicateFieldNameLastWriteWins(t *testing.T) {
	ex := SettledExtraction{Fields: []extractedField{
		{Name: "invoice_number", Value: mpPtr("INV-FIRST")},
		{Name: "invoice_number", Value: mpPtr("INV-LAST")},
	}}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.InvoiceNumber != "INV-LAST" {
		t.Errorf("InvoiceNumber = %q, want %q (last entry in Fields order wins)", got.InvoiceNumber, "INV-LAST")
	}
}

// TestDocumentCreateInput_EmptyStringSubtotalPassesThroughVerbatim: AC #7 is byte-verbatim
// passthrough, and the mapper's only "absent" test is `!= nil` (extractedField.Value), never
// `!= ""` -- so a non-NULL empty string is a decided value, not a missing one, and it must
// flow through unmodified. (Store.Create binds it $N::text::numeric; Postgres rejects "" at
// write time -- invoice/store.go:229 -- a store-level concern, not the mapper's.)
func TestDocumentCreateInput_EmptyStringSubtotalPassesThroughVerbatim(t *testing.T) {
	ex := SettledExtraction{Fields: []extractedField{
		{Name: "invoice_number", Value: mpPtr("INV-EMPTY-1")},
		{Name: "subtotal", Value: mpPtr("")},
	}}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.Subtotal == nil || *got.Subtotal != "" {
		t.Errorf("Subtotal = %v, want a non-nil pointer to \"\" -- empty-string is a decided value, not NULL", got.Subtotal)
	}
}

// TestDocumentCreateInput_NullIssueDateWithMissingReasonLeavesDateNil: a NULL issue_date
// value (Value: nil) flagged with a "missing" reason must leave IssueDate nil and produce no
// RowError -- parseIssueDate is never called on a NULL value in the first place (only on a
// non-nil *string), so the "missing" reason carries no special handling of its own.
func TestDocumentCreateInput_NullIssueDateWithMissingReasonLeavesDateNil(t *testing.T) {
	ex := SettledExtraction{Fields: []extractedField{
		{Name: "invoice_number", Value: mpPtr("INV-NULLDATE-1")},
		{Name: "issue_date", Value: nil, Reason: mpPtr("missing")},
	}}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.InvoiceNumber != "INV-NULLDATE-1" {
		t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, "INV-NULLDATE-1")
	}
	if got.IssueDate != nil {
		t.Errorf("IssueDate = %v, want nil", got.IssueDate)
	}
}

// TestDocumentCreateInput_OnlyUnknownFieldNamesReturnsInvoiceNumberRowError: an extraction
// carrying nothing but unknown field_names must fall through to the same structural
// invoice_number RowError as a genuinely empty extraction -- not a silently-empty
// invoice.CreateInput{} with a nil error.
func TestDocumentCreateInput_OnlyUnknownFieldNamesReturnsInvoiceNumberRowError(t *testing.T) {
	ex := SettledExtraction{Fields: []extractedField{
		{Name: "invoice_date", Value: mpPtr("2026-01-01")},
		{Name: "total_amount", Value: mpPtr("500.00")},
	}}
	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr == nil {
		t.Fatal("rowErr = nil, want a structural RowError on invoice_number")
	}
	if rowErr.Field != "invoice_number" {
		t.Errorf("rowErr.Field = %q, want %q", rowErr.Field, "invoice_number")
	}
	if !reflect.DeepEqual(got, invoice.CreateInput{}) {
		t.Errorf("CreateInput = %+v, want the zero value", got)
	}
}

// TestDocumentCreateInput_SourceDocumentIDIsDocumentIDNotJobID: documentID and ex.JobID are
// deliberately distinct values here -- if the mapper ever wired SourceDocumentID to the job
// id (an adjacent string of the same shape) instead of the documentID argument, this is the
// only test that would catch it.
func TestDocumentCreateInput_SourceDocumentIDIsDocumentIDNotJobID(t *testing.T) {
	ex := SettledExtraction{
		JobID:  "job-should-not-appear",
		Fields: []extractedField{{Name: "invoice_number", Value: mpPtr("INV-SRC-1")}},
	}
	got, rowErr := documentCreateInput("entity-1", "doc-distinct-id", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.SourceDocumentID == nil || *got.SourceDocumentID != "doc-distinct-id" {
		t.Errorf("SourceDocumentID = %v, want %q", got.SourceDocumentID, "doc-distinct-id")
	}
}

// mpStringSliceVar parses path and returns the string-literal elements of the top-level
// `var name = []string{...}` declaration, or nil when no such var exists.
func mpStringSliceVar(t *testing.T, path, name string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != name || len(vs.Values) != 1 {
				continue
			}
			cl, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			var out []string
			for _, elt := range cl.Elts {
				lit, ok := elt.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s literal %s: %v", name, lit.Value, err)
				}
				out = append(out, s)
			}
			return out
		}
	}
	return nil
}

// --- EXTR-12-01: the mock's default result, mapped ---------------------------------------

// AC-6/AC-8, and CONFIRMATORY, not red-first: document_deps_test.go fences this package off from
// internal/extraction, so the field set below is hand-copied and asserting it would pass whatever
// the mock actually emits. The oracle for the rename is
// internal/extraction's TestMockExtractor_DefaultResultNamesAreOnTheVocabulary; MAP-11 links
// HeaderFields to mapperFieldNames, closing the chain to the columns asserted here.
//
// What this adds over MAP-01: the mock's PARTIAL field set, where three readings are flagged and
// two carry no value at all, still maps every decided value and leaves the absent ones NULL.
func TestDocumentCreateInput_MockDefaultProducesTheStatedInvoice(t *testing.T) {
	ex := SettledExtraction{
		JobID: "job-extr-12-01",
		Fields: []extractedField{
			{Name: "invoice_number", Value: mpPtr("MOCK-INV-0001")},
			{Name: "issue_date", Value: mpPtr("2026-01-01"), Reason: mpPtr("ambiguous")},
			{Name: "total", Value: mpPtr("1000.00"), Reason: mpPtr("inconsistent")},
			{Name: "subtotal", Value: mpPtr("950.00"), Reason: mpPtr("inconsistent")},
			{Name: "supplier_tin", Value: mpPtr("MOCK-TIN-SUPPLIER-ALT"), Reason: mpPtr("inconsistent")},
			{Name: "buyer_tin", Value: nil, Reason: mpPtr("missing")},
			{Name: "vat", Value: nil, Reason: mpPtr("unreadable")},
		},
	}

	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}

	docID := "doc-1"
	want := invoice.CreateInput{
		EntityID:      "entity-1",
		InvoiceNumber: "MOCK-INV-0001",
		IssueDate:     timePtr(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Subtotal:      mpPtr("950.00"),
		Total:         mpPtr("1000.00"),
		// Q11: Store.Create overwrites the supplier from the entity, so the mapper leaves both
		// unset however loudly the extraction carries them.
		SourceDocumentID: &docID,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CreateInput = %+v, want %+v", got, want)
	}

	// Named again below DeepEqual: these are the two the rename repairs, and a DeepEqual failure
	// on any other field would otherwise bury which one moved.
	if got.IssueDate == nil {
		t.Error("IssueDate = nil -- the mock's issue_date is on the vocabulary now and must map")
	} else if !got.IssueDate.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("IssueDate = %v, want 2026-01-01", got.IssueDate)
	}
	if got.Total == nil || *got.Total != "1000.00" {
		t.Errorf("Total = %v, want %q -- the mock's total is on the vocabulary now and must map", got.Total, "1000.00")
	}
	if got.Subtotal == nil || *got.Subtotal != "950.00" {
		t.Errorf("Subtotal = %v, want %q", got.Subtotal, "950.00")
	}
	// An unreadable field carries no reading, so it maps to NULL rather than to an empty string.
	if got.VAT != nil {
		t.Errorf("VAT = %q, want nil -- the mock's vat is unreadable and carries no value", *got.VAT)
	}
	if got.BuyerTIN != nil {
		t.Errorf("BuyerTIN = %q, want nil -- the mock's buyer_tin is missing", *got.BuyerTIN)
	}
}
