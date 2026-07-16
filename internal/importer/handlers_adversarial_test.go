// M4-03-05 (task-106) QA pass: adversarial handler-contract coverage added
// on top of the executor's IMP-API-01..07 suite (handlers_test.go). These
// pin specific CreateHandler contract guarantees the Test Specs table
// didn't spell out as its own row, but that the Implementation Plan's flow
// description implies:
//
//   - identity-first-401 must run BEFORE the upload cap -- an
//     oversized-AND-unauthenticated request is 401, never 413.
//   - detectFormat's actual filename/content-type resolution rules (case-
//     insensitive extension match; a generic/unrecognized content-type on an
//     unknown extension 400s; excelize.OpenReader failing on non-zip bytes
//     surfaces as 400, never a panic/500; an unknown extension paired with an
//     explicit "text/plain" Content-Type actually falls back to csv today --
//     pinned here as observed behavior, not a spec requirement).
//   - a request with no "file" part 400s rather than panicking on
//     r.FormFile's error.
//   - dry_run is an EXACT "true"/"false" string match (or absent, which is
//     also a real import) -- anything else, e.g. "1"/"TRUE"/a typo, 400s
//     rather than silently falling through to a real (persisting) import
//     (CodeRabbit finding, M4-03 PR review: dry_run must not fail open).
//   - the response envelope's delimiter/encoding fields are populated (non-
//     null) for a real, non-comma-delimited CSV upload.
//   - a mapping with no invoice_number key 400s end-to-end through the real
//     Service (resolveMapping's ErrValidation, before any DB write).
//
// This is NOT M4-15's exhaustive malformed-upload catalogue -- one targeted
// case per contract guarantee, per M4-03-05's own Implementation Plan note
// ("Exhaustive malformed/oversized/wrong-encoding edge tests are M4-15;
// here, one smoke per status class").
package importer

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- identity-before-upload-cap ordering ------------------------------------

// TestCreateHandler_NoIdentityOversizedBody401NotTooLarge: a request that is
// BOTH unauthenticated AND over the 10 MiB upload cap must 401, not 413 --
// identity-first-401 (auth.IdentityFromContext) runs before
// http.MaxBytesReader/ParseMultipartForm ever touch the body, per
// CreateHandler's own doc comment ("identity-first-401 (IMP-API-01) ->
// upload-cap"). imp fataling if called also proves neither check nor cap
// enforcement reaches the service layer.
func TestCreateHandler_NoIdentityOversizedBody401NotTooLarge(t *testing.T) {
	imp := func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run without an identity, even for an oversized body")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	oversized := bytes.Repeat([]byte("x"), 10<<20+1024) // > 10 MiB
	body, contentType := buildMultipartBody(t, uuid.NewString(), string(mappingJSON), "data.csv", "", oversized)
	rec, resp := doImportCreate(t, imp, nil, "", contentType, body)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an oversized body with no identity (identity-first-401 must precede the upload cap; body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- format detection: bad/unrecognized input, no DB write ------------------

// TestCreateHandler_FormatDetection_BadInputNoDBWrite400: two cases that must
// 400 (never panic, never 500, never reach imp) before any decode/import is
// attempted: an .xlsx-named file whose bytes are not a valid zip/xlsx
// (excelize.OpenReader errors inside Decode), and an unrecognized extension
// paired with the generic Content-Type multipart.CreateFormFile always sets
// ("application/octet-stream") when the test harness doesn't override it.
func TestCreateHandler_FormatDetection_BadInputNoDBWrite400(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		contentType string
		content     []byte
	}{
		{
			name:        "xlsx extension but bytes are not a valid zip/xlsx",
			filename:    "broken.xlsx",
			contentType: "",
			content:     []byte("this is definitely not a zip file"),
		},
		{
			name:        "unknown extension with the default octet-stream content-type",
			filename:    "data.xyz",
			contentType: "",
			content:     []byte("Inv No\nINV-1\n"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
			imp := func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
				t.Fatal("imp must not run when the uploaded file can't be recognized/decoded")
				return BatchResult{}, nil
			}
			mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
			if err != nil {
				t.Fatalf("marshal mapping: %v", err)
			}
			body, contentType := buildMultipartBody(t, uuid.NewString(), string(mappingJSON), tc.filename, tc.contentType, tc.content)
			rec, resp := doImportCreate(t, imp, &id, "", contentType, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (no panic, no 500; body=%s)", rec.Code, rec.Body.String())
			}
			if resp.Error == "" {
				t.Error("expected a non-empty error message in the body")
			}
		})
	}
}

// --- format detection: real (DB-backed) success paths, pinning actual
//     detectFormat behavior ---------------------------------------------------

// TestCreateHandler_FormatDetection_ActualMapping201: two real, DB-backed
// imports that pin detectFormat's actual resolution rules: an uppercase
// ".CSV" extension is matched case-insensitively (strings.ToLower), and an
// unrecognized extension whose part carries an explicit "text/plain"
// Content-Type currently falls back to the csv path (detectFormat's
// content-type switch lists "text/csv" and "text/plain" together) -- pinned
// here as OBSERVED behavior, not a spec requirement one way or the other.
func TestCreateHandler_FormatDetection_ActualMapping201(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		contentType string
	}{
		{name: "uppercase .CSV extension is detected case-insensitively as csv", filename: "DATA.CSV", contentType: ""},
		{name: "unknown extension + explicit text/plain content-type falls back to csv", filename: "data.dat", contentType: "text/plain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			svc := NewService(NewStore(app), invoice.NewStore(app))

			tenantID := seedTenant(t, super, "format-detection tenant")
			entityID := seedEntity(t, super, tenantID, "format-detection entity")
			id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

			header := []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}
			rows := [][]string{{"FMT-" + uuid.NewString(), "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"}}
			mapping := map[string]string{
				"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
				"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
			}
			mappingJSON, err := json.Marshal(mapping)
			if err != nil {
				t.Fatalf("marshal mapping: %v", err)
			}
			body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), tc.filename, tc.contentType, csvBody(t, header, rows))
			rec, resp := doImportCreate(t, svc.Import, &id, "", contentType, body)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
			}
			if resp.Format != "csv" {
				t.Errorf("format = %q, want %q", resp.Format, "csv")
			}
		})
	}
}

// --- missing file part -------------------------------------------------------

// TestCreateHandler_MissingFilePart400: entity_id + mapping present, but no
// "file" part at all must 400 (r.FormFile's error path), never panic/500.
func TestCreateHandler_MissingFilePart400(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	imp := func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run when the file part is missing")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType := buildMultipartBody(t, uuid.NewString(), string(mappingJSON), "", "", nil)
	rec, resp := doImportCreate(t, imp, &id, "", contentType, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when the file part is absent (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- dry_run query param: only "true"/"false"/absent are recognized --------

// TestCreateHandler_DryRunQueryParamVariants: "?dry_run=false" is a REAL
// (persisting) import -- explicit "false" means the caller opted OUT of a
// dry run; "?dry_run=1" and "?dry_run=TRUE" (wrong case) must both 400 with
// NO batch persisted -- CreateHandler's dry_run parsing now recognizes only
// absent/"true"/"false" (CodeRabbit finding, M4-03 PR review: dry_run must
// not fail open -- a typo or non-canonical value must never silently fall
// through to a real import).
func TestCreateHandler_DryRunQueryParamVariants(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{name: "dry_run=false is a real import", query: "?dry_run=false", wantStatus: http.StatusCreated},
		{name: "dry_run=1 is rejected, not silently real", query: "?dry_run=1", wantStatus: http.StatusBadRequest},
		{name: "dry_run=TRUE (wrong case) is rejected, not silently real", query: "?dry_run=TRUE", wantStatus: http.StatusBadRequest},
		{name: "dry_run=treu (typo) is rejected, not silently real", query: "?dry_run=treu", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			svc := NewService(NewStore(app), invoice.NewStore(app))

			tenantID := seedTenant(t, super, "dry-run-variant tenant")
			entityID := seedEntity(t, super, tenantID, "dry-run-variant entity")
			id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

			header := []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}
			rows := [][]string{{"DRV-" + uuid.NewString(), "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"}}
			mapping := map[string]string{
				"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
				"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
			}
			mappingJSON, err := json.Marshal(mapping)
			if err != nil {
				t.Fatalf("marshal mapping: %v", err)
			}
			body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
			rec, resp := doImportCreate(t, svc.Import, &id, tc.query, contentType, body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			ctx := context.Background()
			var batchCount int
			if err := super.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, entityID).Scan(&batchCount); err != nil {
				t.Fatalf("count import_batches: %v", err)
			}

			switch tc.wantStatus {
			case http.StatusCreated:
				if resp.ID == "" {
					t.Error("expected a non-empty id (a real import must persist a batch)")
				}
				if batchCount != 1 {
					t.Errorf("import_batches rows for entity = %d, want 1 (dry_run=false persists)", batchCount)
				}
			case http.StatusBadRequest:
				if resp.Error == "" {
					t.Error("expected a non-empty error message in the body")
				}
				if batchCount != 0 {
					t.Errorf("import_batches rows for entity = %d, want 0 (a rejected dry_run value must never persist)", batchCount)
				}
			}
		})
	}
}

// --- response envelope: delimiter/encoding populated for a real,
//     non-comma CSV upload ----------------------------------------------------

// semicolonCSVBody mirrors csvBody but writes ';'-delimited records instead
// of ','-delimited ones, so Decode's delimiter sniffing actually picks ';'
// -- for pinning the response envelope's delimiter/encoding fields on a real
// CSV upload (contrasting with IMP-API-07's xlsx case, where both are null).
func semicolonCSVBody(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	if err := w.Write(header); err != nil {
		t.Fatalf("write csv header: %v", err)
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			t.Fatalf("write csv row: %v", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatalf("flush csv writer: %v", err)
	}
	return buf.Bytes()
}

// TestCreateHandler_SemicolonCSVEnvelope: a real ';'-delimited CSV import's
// response must carry format=="csv" and non-null delimiter (";")/encoding
// fields -- the *string null-vs-present behavior in the OTHER direction from
// IMP-API-07 (xlsx: both null).
func TestCreateHandler_SemicolonCSVEnvelope(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app))

	tenantID := seedTenant(t, super, "semicolon-csv tenant")
	entityID := seedEntity(t, super, tenantID, "semicolon-csv entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}
	rows := [][]string{{"SEMI-" + uuid.NewString(), "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"}}
	mapping := map[string]string{
		"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
		"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
	}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), "data.csv", "", semicolonCSVBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, &id, "", contentType, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Format != "csv" {
		t.Errorf("format = %q, want %q", resp.Format, "csv")
	}
	if resp.Delimiter == nil || *resp.Delimiter != ";" {
		t.Errorf("delimiter = %v, want non-nil \";\"", resp.Delimiter)
	}
	if resp.Encoding == nil || *resp.Encoding == "" {
		t.Errorf("encoding = %v, want a non-nil, non-empty value", resp.Encoding)
	}
}

// --- mapping missing invoice_number: end-to-end through the real Service ----

// TestCreateHandler_MappingMissingInvoiceNumber400: valid JSON mapping that
// simply omits the required invoice_number key must 400 end-to-end through
// the real Service -- resolveMapping's ErrValidation (service.go) surfaces
// via statusForErr as 400, before any DB write (so no entity even needs to
// be seeded: resolveMapping runs before any Store call).
func TestCreateHandler_MappingMissingInvoiceNumber400(t *testing.T) {
	_, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app))

	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}

	header := []string{"Inv No", "Buyer"}
	rows := [][]string{{"MAP-1", "Acme Ltd"}}
	mapping := map[string]string{"buyer_name": "Buyer"} // no invoice_number key
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType := buildMultipartBody(t, uuid.NewString(), string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, &id, "", contentType, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when mapping has no invoice_number key (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}
