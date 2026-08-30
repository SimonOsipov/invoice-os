// document_source_rows_test.go: EXTR-06-05 (task-765) -- Core AC 2 (source_rows stays NULL)
// and Core AC 3 (the document path can't reach the spreadsheet builder). SR-01..SR-04 are
// skipped as duplicate coverage:
//
//	AC #1 -- TestServiceImportDocument_WrittenInvoiceSourceRowsIsNullNotEmptyArray
//	         (document_service_adversarial_test.go:242) already reads back through the real
//	         ImportDocument via the superuser pool.
//	AC #2 -- TestStoreCreate_NilSourceRowsIsNull / _EmptyNonNilSourceRowsRejected /
//	         _SourceRowBelowTwoRejected (internal/invoice/source_rows_test.go:109/139/203)
//	         already assert NULL-accepted and 23514/invoices_source_rows_are_sheet_rows for
//	         '{}' and '{1}'.
//
// SR-05/SR-06 scan only document.go: handlers_document.go does not exist on this branch yet
// (EXTR-06-06 creates it) -- that subtask's own suite owes handlers_document.go the same
// banned-identifier scan once the file exists.
package importer

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// --- SR-05 / SR-06: document.go names no spreadsheet-only helper (Core AC 3) ---------------

// TestImporterDocumentGo_NamesNoSpreadsheetOnlyHelpers covers both specs off one AST walk:
// SR-06 is the control needle (invoice.CreateInput must be found, or the absence below reads
// clean for the wrong reason) plus a population floor (exactly the files this subtask scans);
// SR-05 is the absence of the seven spreadsheet-only names.
func TestImporterDocumentGo_NamesNoSpreadsheetOnlyHelpers(t *testing.T) {
	root := sxDepsRepoRoot(t)
	targets := []string{"document.go"} // handlers_document.go joins this list in EXTR-06-06

	banned := map[string]bool{
		"buildCreateInput":          true,
		"sheetRow":                  true,
		"sheetRows":                 true,
		"invoiceGroup":              true,
		"headerConflictField":       true,
		"bestEffortBadNumericField": true,
		"resolveMapping":            true,
	}

	fset := token.NewFileSet()
	var parsedFiles []string
	offenders := map[string][]string{}
	needleFound := false

	for _, name := range targets {
		path := filepath.Join(root, "internal/importer", name)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsedFiles = append(parsedFiles, name)

		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && banned[id.Name] {
				offenders[name] = append(offenders[name], id.Name)
			}
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "invoice" && sel.Sel.Name == "CreateInput" {
					needleFound = true
				}
			}
			return true
		})
	}

	if !needleFound {
		t.Fatal("control needle failed: invoice.CreateInput not found in document.go -- the scan below would be vacuous")
	}
	if len(parsedFiles) != 1 {
		t.Fatalf("parsed %d file(s), want 1 -- the walk did not scan the intended target list", len(parsedFiles))
	}
	if len(offenders) > 0 {
		for name, ids := range offenders {
			t.Errorf("%s names %v: the document path must never reach a spreadsheet-only helper", name, ids)
		}
	}
}

// --- SR-07: ImportDocument over a zero-field extraction (Core AC 4) ------------------------

// TestServiceImportDocument_ZeroFieldExtractionQuarantinesNoInvoiceNumberNoPanic: a succeeded
// extraction_jobs row with literally zero extraction_field_results rows is DB-legal -- no
// CHECK ties state='succeeded' to a field-result count. documentCreateInput never indexes
// ex.Fields, so this is green from the start (mutation-confirmed non-vacuous, see git log for
// this commit's proof).
func TestServiceImportDocument_ZeroFieldExtractionQuarantinesNoInvoiceNumberNoPanic(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SR-07 tenant")
	entityID := seedEntity(t, super, tenantID, "SR-07 entity")
	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, map[string]*string{})

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v, want nil -- a mapper RowError must never surface as a returned error (D-9)", err)
	}
	if res.Status != "completed" || res.ReadyInvoices != 0 || res.QuarantinedInvoices != 1 {
		t.Errorf("BatchResult = %+v, want completed/ready=0/quarantined=1", res)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(res.Errors))
	}
	if res.Errors[0].Field != "invoice_number" {
		t.Errorf("Errors[0].Field = %q, want %q", res.Errors[0].Field, "invoice_number")
	}
	if res.Errors[0].Message != "invoice_number is missing or blank" {
		t.Errorf("Errors[0].Message = %q, want the shipped documentCreateInput message", res.Errors[0].Message)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices for entity = %d, want 0 -- a zero-field extraction must never write a garbage invoice", got)
	}
}

// --- SR-08: documentCreateInput over nil/empty Fields does not panic -----------------------

// TestDocumentCreateInput_NilAndEmptyFieldsDoNotPanic: no indexing exists in
// documentCreateInput (a map lookup, never ex.Fields[i]), so this is green from the start too
// -- same mutation-confirmed root cause as SR-07.
func TestDocumentCreateInput_NilAndEmptyFieldsDoNotPanic(t *testing.T) {
	cases := []struct {
		name   string
		fields []extractedField
	}{
		{name: "nilFields", fields: nil},
		{name: "emptyFields", fields: []extractedField{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rowErr := documentCreateInput("entity-1", "doc-1", SettledExtraction{Fields: tc.fields})
			if rowErr == nil {
				t.Fatal("rowErr = nil, want a structural RowError -- invoice_number is absent from an empty Fields set")
			}
			if rowErr.Field != "invoice_number" {
				t.Errorf("rowErr.Field = %q, want %q", rowErr.Field, "invoice_number")
			}
			if !reflect.DeepEqual(got, invoice.CreateInput{}) {
				t.Errorf("CreateInput = %+v, want the zero value", got)
			}
		})
	}
}
