// AUDIT-12-01 QA (task-698): adversarial coverage the four RED tests
// (516eec76) didn't add. All three routes now share statusForErr's fallback,
// so its edges need proving at the HTTP layer, not just the happy path each
// site's RED test already covers.
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
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestSheet_UnmappedErrorIsStill500 closes a coverage gap: Create and Preview
// already prove a genuinely unmapped error still 500s
// (TestImport_StorageUnreachableWritesNothing, TestPreview_StorageFailureIs500);
// Sheet had no equivalent -- TestSheetHandler_NilObjectBodyIs500 exercises a
// different 500 path (open succeeds, body is nil), never open()'s own default arm.
func TestSheet_UnmappedErrorIsStill500(t *testing.T) {
	id := testIdentity()
	boom := errors.New("importer: qa unmapped boom")
	open := newFakeDocOpen("data.csv", "text/csv", []byte("Inv No\nINV-1\n"))
	open.err = boom

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a genuinely unmapped error (body=%s)", rec.Code, raw)
	}
	var resp sheetErrorBody
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode error response %s: %v", raw, err)
	}
	if strings.Contains(resp.Error, boom.Error()) {
		t.Errorf("500 body leaks the internal error: %q", resp.Error)
	}
}

// --- wrapped db.ErrNotActiveMember still 403s (statusForErr uses errors.Is) -

func TestImport_WrappedNotActiveMemberIs403(t *testing.T) {
	id := testIdentity()
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run for a caller the seam refuses")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType, open := storedUpload(t, uuid.NewString(), string(mappingJSON), "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	open.err = fmt.Errorf("importer: open: %w", db.ErrNotActiveMember)
	rec, resp := doImportCreate(t, imp, open.fn(), &id, "", contentType, body)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a wrapped db.ErrNotActiveMember (body=%v)", rec.Code, resp)
	}
	if resp.Error != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want %q", resp.Error, db.NotActiveMemberMessage)
	}
}

func TestPreview_WrappedNotActiveMemberIs403(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	store.err = fmt.Errorf("importer: store: %w", db.ErrNotActiveMember)
	body, ct := buildMultipartBody(t, "", "", "data.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))

	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a wrapped db.ErrNotActiveMember (body=%s)", rec.Code, raw)
	}
	if resp.Error != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want %q (body=%s)", resp.Error, db.NotActiveMemberMessage, raw)
	}
}

func TestSheet_WrappedNotActiveMemberIs403(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", []byte("Inv No\nINV-1\n"))
	open.err = fmt.Errorf("importer: open: %w", db.ErrNotActiveMember)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a wrapped db.ErrNotActiveMember (body=%s)", rec.Code, raw)
	}
	var resp sheetErrorBody
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode error response %s: %v", raw, err)
	}
	if resp.Error != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want %q (body=%s)", resp.Error, db.NotActiveMemberMessage, raw)
	}
}

// --- the 403 path must not log as an error -----------------------------

// doXxx helpers below hardcode a nil logger, so these three build the request
// directly to inject a spy *slog.Logger (mirrors internal/invoice's
// slog.NewJSONHandler(&buf, nil) idiom).

func TestImport_403DoesNotLog(t *testing.T) {
	id := testIdentity()
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run for a caller the seam refuses")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType, open := storedUpload(t, uuid.NewString(), string(mappingJSON), "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	open.err = db.ErrNotActiveMember

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := httptest.NewRequest("POST", "/v1/imports", body)
	r.Header.Set("Content-Type", contentType)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	CreateHandler(imp, open.fn(), logger).ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if logged := buf.String(); logged != "" {
		t.Errorf("a 403 refusal must not log as an error: %s", logged)
	}
}

func TestPreview_403DoesNotLog(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	store.err = db.ErrNotActiveMember
	body, ct := buildMultipartBody(t, "", "", "data.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := httptest.NewRequest("POST", "/v1/imports/preview", body)
	r.Header.Set("Content-Type", ct)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	PreviewHandler(store.fn(), logger).ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if logged := buf.String(); logged != "" {
		t.Errorf("a 403 refusal must not log as an error: %s", logged)
	}
}

func TestSheet_403DoesNotLog(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", []byte("Inv No\nINV-1\n"))
	open.err = db.ErrNotActiveMember

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := httptest.NewRequest(http.MethodGet, "/v1/documents/"+open.doc.ID+"/sheet", nil)
	r.SetPathValue("id", open.doc.ID)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	SheetHandler(open.fn(), logger).ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if logged := buf.String(); logged != "" {
		t.Errorf("a 403 refusal must not log as an error: %s", logged)
	}
}

// --- the 403 body is byte-identical across all three routes ---------------

func TestThreeRoutes_403BodyIsByteIdentical(t *testing.T) {
	id := testIdentity()
	want, err := json.Marshal(map[string]string{"error": db.NotActiveMemberMessage})
	if err != nil {
		t.Fatalf("marshal expected body: %v", err)
	}
	want = append(want, '\n') // json.NewEncoder.Encode (writeJSON) appends one

	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run for a caller the seam refuses")
		return BatchResult{}, nil
	}
	createBody, createCT, createOpen := storedUpload(t, uuid.NewString(), string(mappingJSON), "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	createOpen.err = db.ErrNotActiveMember
	_, createRaw, _ := doImportUpload(t, imp, createOpen.fn(), &id, "", createCT, createBody)

	store := newFakeDocStore()
	store.err = db.ErrNotActiveMember
	previewBody, previewCT := buildMultipartBody(t, "", "", "data.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	_, previewRaw, _ := doPreviewUpload(t, store.fn(), &id, previewCT, previewBody)

	sheetOpen := newFakeDocOpen("data.csv", "text/csv", []byte("Inv No\nINV-1\n"))
	sheetOpen.err = db.ErrNotActiveMember
	_, sheetRaw := doSheetRequest(t, sheetOpen.fn(), &id, sheetOpen.doc.ID)

	bodies := map[string][]byte{"CreateHandler": createRaw, "PreviewHandler": previewRaw, "SheetHandler": sheetRaw}
	if len(bodies) != 3 {
		t.Fatalf("collected %d route bodies, want 3 -- an empty set would pass every check below vacuously", len(bodies))
	}
	for name, raw := range bodies {
		if !bytes.Equal(raw, want) {
			t.Errorf("%s body = %q, want %q byte-for-byte", name, raw, want)
		}
	}
}

// --- known gap, inherited unchanged: db.ErrNoTenant still 500s here --------

// TestThreeRoutes_ErrNoTenantIs500NotAuthNorForbidden documents a pre-existing
// gap this subtask inherits unchanged (QA finding F-3, task-698):
// importer.statusForErr has no db.ErrNoTenant arm at all (see
// not_active_member_403_test.go's header comment, AUDIT-10-03 site 13/14), so
// a caller who passes the top-of-handler identity check but whose tenant id
// fails db.WithinRequestTenantTxOpts's uuid.Parse still 500s here, unlike
// every other package's db.ErrNoTenant -> 401 (e.g.
// internal/archive/handlers.go:138, internal/tenancy/tenancy.go:160). Not
// fixed by this subtask: no AC names it, and AC-4 keeps AUDIT-12-01 to the
// existing mapper. This pins the CURRENT (wrong) status so a future fix shows
// as a visible diff here rather than an unnoticed behavior change.
func TestThreeRoutes_ErrNoTenantIs500NotAuthNorForbidden(t *testing.T) {
	id := testIdentity()
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}

	cases := []struct {
		name string
		run  func(t *testing.T) (*httptest.ResponseRecorder, []byte)
	}{
		{"CreateHandler", func(t *testing.T) (*httptest.ResponseRecorder, []byte) {
			imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
				t.Fatal("imp must not run for a caller the seam refuses")
				return BatchResult{}, nil
			}
			body, contentType, open := storedUpload(t, uuid.NewString(), string(mappingJSON), "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
			open.err = db.ErrNoTenant
			rec, raw, _ := doImportUpload(t, imp, open.fn(), &id, "", contentType, body)
			return rec, raw
		}},
		{"PreviewHandler", func(t *testing.T) (*httptest.ResponseRecorder, []byte) {
			store := newFakeDocStore()
			store.err = db.ErrNoTenant
			body, ct := buildMultipartBody(t, "", "", "data.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
			rec, raw, _ := doPreviewUpload(t, store.fn(), &id, ct, body)
			return rec, raw
		}},
		{"SheetHandler", func(t *testing.T) (*httptest.ResponseRecorder, []byte) {
			open := newFakeDocOpen("data.csv", "text/csv", []byte("Inv No\nINV-1\n"))
			open.err = db.ErrNoTenant
			return doSheetRequest(t, open.fn(), &id, open.doc.ID)
		}},
	}
	if len(cases) != 3 {
		t.Fatalf("%d case(s) registered, want 3 -- an empty table would pass vacuously", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, raw := tc.run(t)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 -- CURRENT (wrong) behavior; a fix landing 401 here should update this test, not be caught by it", rec.Code)
				_ = raw
			}
		})
	}
}
