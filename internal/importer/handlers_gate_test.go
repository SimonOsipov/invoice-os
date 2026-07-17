// task-114 / M4-04-07 -- Mode A RED specs for IMPV-14/15: POST /v1/imports'
// HTTP response body carries the additive rule-outcome fields
// ([import-report-shape]), on both the real (201) and dry-run (200) paths.
// Complements service_gate_test.go's IMPV-01..13/16 (the Service layer);
// these two test ONLY handlers.go's response-construction wiring, driven by
// a REAL Service + REAL in-process 04 (reusing service_gate_test.go's
// startInProcess04ForImporter/newTestServiceWithGate) over one VAT-wrong
// invoice -- gate CORRECTNESS itself is already covered at the service
// layer (IMPV-01/02/06); these two only check the HTTP LAYER serializes
// whatever BatchResult carries.
//
// Spec-to-test map:
//
//	IMPV-14 TestCreateHandler_RealResponseCarriesNewGateFields
//	IMPV-15 TestCreateHandler_DryRunResponseCarriesNewGateFieldsNoIDStatus
//
// RED against service.go's QA Mode-A structural scaffold: importResponse
// (handlers.go) does not yet serialize BatchResult's four new fields at
// all, and BatchResult's own new fields stay at their Go zero value
// regardless (Import() never populates them) -- so importBatchBodyGate
// below decodes them to their zero value on every run, failing the
// non-zero-value assertions below for a REAL reason, never a compile error.
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -v -run 'TestCreateHandler_.*NewGateFields' ./internal/importer/...
package importer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// importBatchBodyGate is handlers_test.go's importBatchBody extended with
// M4-04-07's additive fields -- declared as its OWN type here (not by
// editing importBatchBody in handlers_test.go) so that file stays
// untouched; json.Unmarshal only needs one decode target per test, and
// IMPV-14/15 are the only handlers_test.go-adjacent specs that need these
// fields.
type importBatchBodyGate struct {
	ID                     string              `json:"id"`
	Status                 string              `json:"status"`
	RuleSetVersion         *int                `json:"rule_set_version"`
	InvoicesClean          int                 `json:"invoices_clean"`
	InvoicesWithViolations int                 `json:"invoices_with_violations"`
	InvoiceViolations      []InvoiceViolations `json:"invoice_violations"`
	Error                  string              `json:"error"`
}

// impvHandlerRows is one clean-ready, VAT-wrong CSV upload -- reuses
// IMPV-04's numbers (fires ONLY vat-standard-rate against the real v2 rule
// set), against a REAL gate so the response reflects genuine gate output.
func impvHandlerRows() [][]string {
	return [][]string{
		mkRow("IMPV14-VATWRONG", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "1.00", "268.75", "Item1", "2", "100.00"),
		mkRow("IMPV14-VATWRONG", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "1.00", "268.75", "Item2", "1", "50.00"),
	}
}

// doImportCreateGate is doImportCreate's (handlers_test.go) twin, decoding
// into importBatchBodyGate instead of importBatchBody.
func doImportCreateGate(t *testing.T, imp importFunc, id *auth.Identity, query, contentType string, body io.Reader) (*httptest.ResponseRecorder, importBatchBodyGate) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports"+query, body)
	r.Header.Set("Content-Type", contentType)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CreateHandler(imp, nil).ServeHTTP(rec, r)

	var resp importBatchBodyGate
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

// --- IMPV-14: real import -----------------------------------------------

// TestCreateHandler_RealResponseCarriesNewGateFields (IMPV-14): POST
// /v1/imports (real) -> 201; body carries rule_set_version,
// invoices_clean, invoices_with_violations, invoice_violations.
func TestCreateHandler_RealResponseCarriesNewGateFields(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "IMPV-14 tenant")
	entityID := seedEntity(t, super, tenantID, "IMPV-14 entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	realGate := invoice.NewGate(invoice.NewStore(app), validator)
	svc := newTestServiceWithGate(app, realGate)

	mappingJSON, err := json.Marshal(stdMapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), "data.csv", "", csvBody(t, stdHeader, impvHandlerRows()))
	rec, resp := doImportCreateGate(t, svc.Import, &id, "", contentType, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.RuleSetVersion == nil {
		t.Error("RuleSetVersion = nil, want a pointer to 2 -- the batch WAS evaluated")
	} else if *resp.RuleSetVersion != 2 {
		t.Errorf("*RuleSetVersion = %d, want 2", *resp.RuleSetVersion)
	}
	if resp.InvoicesWithViolations != 1 {
		t.Errorf("InvoicesWithViolations = %d, want 1 (IMPV14-VATWRONG)", resp.InvoicesWithViolations)
	}
	if len(resp.InvoiceViolations) != 1 {
		t.Errorf("InvoiceViolations = %+v, want exactly 1 entry", resp.InvoiceViolations)
	}
}

// --- IMPV-15: dry-run -----------------------------------------------------

// TestCreateHandler_DryRunResponseCarriesNewGateFieldsNoIDStatus (IMPV-15):
// POST /v1/imports?dry_run=true -> 200; the same new fields; no id/status
// (M4-03 shape preserved).
func TestCreateHandler_DryRunResponseCarriesNewGateFieldsNoIDStatus(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "IMPV-15 tenant")
	entityID := seedEntity(t, super, tenantID, "IMPV-15 entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	realGate := invoice.NewGate(invoice.NewStore(app), validator)
	svc := newTestServiceWithGate(app, realGate)

	mappingJSON, err := json.Marshal(stdMapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), "data.csv", "", csvBody(t, stdHeader, impvHandlerRows()))
	rec, resp := doImportCreateGate(t, svc.Import, &id, "?dry_run=true", contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for ?dry_run=true (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.ID != "" || resp.Status != "" {
		t.Errorf("dry-run (ID=%q Status=%q), want both empty (M4-03 shape preserved)", resp.ID, resp.Status)
	}
	if resp.RuleSetVersion == nil {
		t.Error("RuleSetVersion = nil, want a pointer to 2 -- the batch WAS evaluated")
	} else if *resp.RuleSetVersion != 2 {
		t.Errorf("*RuleSetVersion = %d, want 2", *resp.RuleSetVersion)
	}
	if resp.InvoicesWithViolations != 1 {
		t.Errorf("InvoicesWithViolations = %d, want 1 (IMPV14-VATWRONG)", resp.InvoicesWithViolations)
	}
	if len(resp.InvoiceViolations) != 1 {
		t.Errorf("InvoiceViolations = %+v, want exactly 1 entry", resp.InvoiceViolations)
	}

	ctx := context.Background()
	var batchCount, invoiceCount int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, entityID).Scan(&batchCount); err != nil {
		t.Fatalf("count import_batches: %v", err)
	}
	if err := super.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE entity_id = $1`, entityID).Scan(&invoiceCount); err != nil {
		t.Fatalf("count invoices: %v", err)
	}
	if batchCount != 0 || invoiceCount != 0 {
		t.Errorf("dry-run wrote rows (import_batches=%d invoices=%d), want (0,0)", batchCount, invoiceCount)
	}
}
