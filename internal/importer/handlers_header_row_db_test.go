// handlers_header_row_db_test.go: T13, the DB-backed header_row spec --
// CreateHandler over the real Service.Import, CSV and XLSX alike.
package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// writeXLSXRow sets values across columns A, B, ... on one sheet row.
func writeXLSXRow(t *testing.T, f *excelize.File, sheet string, row int, values []string) {
	t.Helper()
	for c, v := range values {
		cell, err := excelize.CoordinatesToCellName(c+1, row)
		if err != nil {
			t.Fatalf("cell name: %v", err)
		}
		mustSetCellValue(t, f, sheet, cell, v)
	}
}

func TestRLS_ImportFromRowThreeNumbersCSVAndXLSXAlike(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	h := CreateHandler(svc.Import, docSvc.Open, NewStore(app).SaveMapping, nil)

	rowA := mkRow("INV-A", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget A", "1", "100.00")
	rowBlank := mkRow("", "2026-01-10", "TIN-A", "Buyer A", "NGN", "300.00", "30.00", "330.00", "Widget X", "1", "100.00")
	rowB := mkRow("INV-B", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Gadget B", "1", "100.00")

	type sourceRows struct {
		invoiceA []int
		invoiceB []int
	}

	run := func(t *testing.T, filename string, content []byte) sourceRows {
		tenantID := seedTenant(t, super, "header-row three "+filename)
		entityID := seedEntity(t, super, tenantID, "header-row three entity "+filename)
		id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

		doc := storeDocumentAs(t, docSvc, tenantID, filename, "", content)
		body, ct := buildImportForm(t, entityID, mustMappingJSON(t, stdMapping), doc.ID, importPart{field: "header_row", content: []byte("3")})
		r := httptest.NewRequest("POST", "/v1/imports", body)
		r.Header.Set("Content-Type", ct)
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		raw := rec.Body.Bytes()
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d, want 201 (body=%s)", filename, rec.Code, raw)
		}
		var resp importBatchBody
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("%s: decode response %s: %v", filename, raw, err)
		}
		if resp.RowsTotal != 3 {
			t.Errorf("%s: rows_total = %d, want 3", filename, resp.RowsTotal)
		}
		found := false
		for _, e := range resp.Errors {
			if e.Row == 5 && e.Message == "blank invoice number: row cannot be grouped" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: errors = %+v, want a row-5 blank invoice number entry", filename, resp.Errors)
		}

		var headerRow int
		if err := super.QueryRow(context.Background(),
			`SELECT header_row FROM import_batches WHERE id = $1`, resp.ID,
		).Scan(&headerRow); err != nil {
			t.Fatalf("%s: read header_row: %v", filename, err)
		}
		if headerRow != 3 {
			t.Errorf("%s: header_row = %d, want 3", filename, headerRow)
		}

		return sourceRows{
			invoiceA: sourceRowsOf(t, super, invoiceIDByNumber(t, super, entityID, "INV-A")),
			invoiceB: sourceRowsOf(t, super, invoiceIDByNumber(t, super, entityID, "INV-B")),
		}
	}

	var csvRows, xlsxRows sourceRows

	t.Run("csv", func(t *testing.T) {
		content := append([]byte("Sales Register - March 2026\n\n"), csvBody(t, stdHeader, [][]string{rowA, rowBlank, rowB})...)
		csvRows = run(t, "r.csv", content)
		if !intSliceEqual(csvRows.invoiceA, []int{4}) {
			t.Errorf("csv INV-A source_rows = %v, want [4]", csvRows.invoiceA)
		}
		if !intSliceEqual(csvRows.invoiceB, []int{6}) {
			t.Errorf("csv INV-B source_rows = %v, want [6]", csvRows.invoiceB)
		}
	})

	t.Run("xlsx", func(t *testing.T) {
		fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
			mustSetCellValue(t, f, sheet, "A1", "Sales Register - March 2026")
			if err := f.SetRowHeight(sheet, 2, 15); err != nil {
				t.Fatalf("set row height: %v", err)
			}
			writeXLSXRow(t, f, sheet, 3, stdHeader)
			writeXLSXRow(t, f, sheet, 4, rowA)
			writeXLSXRow(t, f, sheet, 5, rowBlank)
			writeXLSXRow(t, f, sheet, 6, rowB)
		})
		xlsxRows = run(t, "r.xlsx", fixture)
		if !intSliceEqual(xlsxRows.invoiceA, []int{4}) {
			t.Errorf("xlsx INV-A source_rows = %v, want [4]", xlsxRows.invoiceA)
		}
		if !intSliceEqual(xlsxRows.invoiceB, []int{6}) {
			t.Errorf("xlsx INV-B source_rows = %v, want [6]", xlsxRows.invoiceB)
		}
	})

	if !reflect.DeepEqual(csvRows, xlsxRows) {
		t.Errorf("csv result %+v != xlsx result %+v", csvRows, xlsxRows)
	}
}
