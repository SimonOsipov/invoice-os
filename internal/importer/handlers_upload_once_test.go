// handlers_upload_once_test.go: the [upload-once] contract specs — preview
// stores the bytes and returns an id, import takes that id instead of a second
// upload. New file rather than an append to handlers_test.go /
// handlers_preview_test.go: those two carry frozen IMP-API-*/PRV-* spec maps
// and are among the 21 files the signature change forces the executor to
// adapt, so a shared file would make these specs indistinguishable from
// mechanical adaptation in review.
//
// Every case here is fake-driven httptest; the DB-backed half of the contract
// lives in upload_once_spine_test.go.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- the two new injected seams ---------------------------------------------

// storeSpec / openSpec are document.Service.Store / document.Service.Open's
// shapes. Declared here as test-side aliases only: the handlers are asserted
// structurally, so production stays free to name (or not name) these types.
type storeSpec = func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (document.Document, error)

type openSpec = func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error)

// errStoreBoom / errOpenBoom stand in for an unreachable object store. Their
// text is asserted ABSENT from every response body — a 500 must not leak.
var (
	errStoreBoom = errors.New("object storage unreachable: put")
	errOpenBoom  = errors.New("object storage unreachable: get")
)

// --- fakeDocStore -----------------------------------------------------------

type storeCall struct {
	filename    string
	contentType string
	size        int64
	body        []byte
}

// fakeDocStore records what PreviewHandler handed the store, and — critically —
// leaves the reader exactly where the real document.Service.Store leaves it: at
// EOF. See fn().
type fakeDocStore struct {
	calls []storeCall
	doc   document.Document
	err   error
}

func newFakeDocStore() *fakeDocStore {
	return &fakeDocStore{doc: document.Document{ID: uuid.NewString()}}
}

// fn mirrors document.Service.Store's reader handling byte for byte: hash pass
// to EOF, one rewind, then the PUT reads to EOF again. Nothing rewinds after
// that, so a handler that does not Seek(0) before Decode reads zero bytes (G1).
func (f *fakeDocStore) fn() storeSpec {
	return func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (document.Document, error) {
		b, err := io.ReadAll(body)
		if err != nil {
			return document.Document{}, err
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return document.Document{}, err
		}
		if _, err := io.Copy(io.Discard, body); err != nil {
			return document.Document{}, err
		}
		f.calls = append(f.calls, storeCall{filename: filename, contentType: contentType, size: size, body: b})
		if f.err != nil {
			return document.Document{}, f.err
		}
		return f.doc, nil
	}
}

// --- fakeDocOpen ------------------------------------------------------------

// countingCloser proves the handler closes the object body it was handed.
type countingCloser struct {
	io.Reader
	closes int
}

func (c *countingCloser) Close() error { c.closes++; return nil }

type fakeDocOpen struct {
	ids    []string
	ranges []string
	doc    document.Document
	body   *countingCloser
	err    error
}

func newFakeDocOpen(filename, contentType string, content []byte) *fakeDocOpen {
	d := document.Document{ID: uuid.NewString(), SizeBytes: int64(len(content))}
	if filename != "" {
		d.Filename = &filename
	}
	if contentType != "" {
		d.DeclaredContentType = &contentType
	}
	return &fakeDocOpen{doc: d, body: &countingCloser{Reader: bytes.NewReader(content)}}
}

func (f *fakeDocOpen) fn() openSpec {
	return func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error) {
		f.ids = append(f.ids, id)
		f.ranges = append(f.ranges, rangeHeader)
		if f.err != nil {
			return document.Document{}, document.Object{}, f.err
		}
		return f.doc, document.Object{Body: f.body, Size: f.doc.SizeBytes}, nil
	}
}

// --- request helpers --------------------------------------------------------

// previewUploadBody is previewResponse plus document_id and the shared error
// envelope. Kept separate from handlers_preview_test.go's previewBody, which
// pins the pre-[upload-once] shape.
type previewUploadBody struct {
	DocumentID string     `json:"document_id"`
	Format     string     `json:"format"`
	Delimiter  *string    `json:"delimiter"`
	Encoding   *string    `json:"encoding"`
	Columns    []string   `json:"columns"`
	SampleRows [][]string `json:"sample_rows"`
	RowsTotal  int        `json:"rows_total"`
	Error      string     `json:"error"`
}

func doPreviewUpload(t *testing.T, store storeSpec, id *auth.Identity, contentType string, body io.Reader) (*httptest.ResponseRecorder, []byte, previewUploadBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports/preview", body)
	r.Header.Set("Content-Type", contentType)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	PreviewHandler(store, nil).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	var resp previewUploadBody
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec, raw, resp
}

// importPart is one extra multipart part buildImportForm appends after the
// three contract fields — used to send the RETIRED "file" part and to pad a
// request past the whole-request cap.
type importPart struct {
	field    string
	filename string // non-empty makes it a file part
	content  []byte
}

// buildImportForm assembles the NEW POST /v1/imports body: three text fields,
// no file. A field whose value is "" is omitted entirely.
func buildImportForm(t *testing.T, entityID, mappingJSON, documentID string, extra ...importPart) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for _, f := range []struct{ name, value string }{
		{"entity_id", entityID}, {"mapping", mappingJSON}, {"document_id", documentID},
	} {
		if f.value == "" {
			continue
		}
		if err := w.WriteField(f.name, f.value); err != nil {
			t.Fatalf("write %s field: %v", f.name, err)
		}
	}

	for _, p := range extra {
		var fw io.Writer
		var err error
		if p.filename != "" {
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.field, p.filename))
			h.Set("Content-Type", "text/csv")
			fw, err = w.CreatePart(h)
		} else {
			fw, err = w.CreateFormField(p.field)
		}
		if err != nil {
			t.Fatalf("create %s part: %v", p.field, err)
		}
		if _, err := fw.Write(p.content); err != nil {
			t.Fatalf("write %s content: %v", p.field, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

func doImportUpload(t *testing.T, imp importFunc, open openSpec, id *auth.Identity, query, contentType string, body io.Reader) (*httptest.ResponseRecorder, []byte, importBatchBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports"+query, body)
	r.Header.Set("Content-Type", contentType)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CreateHandler(imp, open, nil).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	var resp importBatchBody
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec, raw, resp
}

// testIdentity is a throwaway authenticated caller.
func testIdentity() auth.Identity {
	return auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: uuid.NewString()}
}

// mustMappingJSON renders the canonical one-field mapping used by the fake-driven
// import cases, whose imp closure never inspects it.
func mustMappingJSON(t *testing.T, m map[string]string) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	return string(b)
}

// hasDocumentIDKey reports whether the raw body carries the key at all —
// a decoded empty string cannot distinguish "absent" from "".
func hasDocumentIDKey(raw []byte) bool {
	return bytes.Contains(raw, []byte(`"document_id"`))
}

// --- AC-1: the bytes are stored before anything can reject them -------------

// TestPreview_StoresBeforeDetectFormat: an upload whose format is unrecognized
// still lands in storage — the store call happens before detectFormat.
func TestPreview_StoresBeforeDetectFormat(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	content := []byte("%PDF-1.7\nnot a spreadsheet\n")
	body, ct := buildMultipartBody(t, "", "", "scan.pdf", "application/pdf", content)

	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unrecognized format (body=%s)", rec.Code, raw)
	}
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1 — the bytes must be stored BEFORE detectFormat rejects them", len(store.calls))
	}
	if !bytes.Equal(store.calls[0].body, content) {
		t.Errorf("stored bytes = %q, want the uploaded bytes %q", store.calls[0].body, content)
	}
	if resp.DocumentID != store.doc.ID {
		t.Errorf("document_id = %q, want %q — an unparseable file must be retrievable", resp.DocumentID, store.doc.ID)
	}
}

// TestPreview_StoresUndecodableFile: same for a file that clears detectFormat
// and then fails Decode (a .csv of NUL bytes — decode.go's control-byte gate).
func TestPreview_StoresUndecodableFile(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	content := bytes.Repeat([]byte{0x00, 0x01, 0x02}, 64)
	body, ct := buildMultipartBody(t, "", "", "corrupt.csv", "text/csv", content)

	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an undecodable file (body=%s)", rec.Code, raw)
	}
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1 — the bytes must be stored BEFORE Decode rejects them", len(store.calls))
	}
	if !bytes.Equal(store.calls[0].body, content) {
		t.Errorf("stored bytes = %q, want the uploaded bytes %q", store.calls[0].body, content)
	}
	if resp.DocumentID != store.doc.ID {
		t.Errorf("document_id = %q, want %q", resp.DocumentID, store.doc.ID)
	}
}

// TestPreview_StoresZeroByteFile: an empty file part is a storable document
// ([zero-byte-storable]), not a pre-store rejection.
func TestPreview_StoresZeroByteFile(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	body, ct := buildMultipartBody(t, "", "", "empty.csv", "text/csv", nil)

	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1 for a zero-byte upload (status=%d body=%s)", len(store.calls), rec.Code, raw)
	}
	if n := len(store.calls[0].body); n != 0 {
		t.Errorf("stored bytes = %d, want 0", n)
	}
	if store.calls[0].size != 0 {
		t.Errorf("declared size = %d, want 0", store.calls[0].size)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an empty csv decodes to zero rows (body=%s)", rec.Code, raw)
	}
	if resp.DocumentID != store.doc.ID {
		t.Errorf("document_id = %q, want %q", resp.DocumentID, store.doc.ID)
	}
}

// TestPreview_RewindsBetweenStoreAndDecode (G1): document.Service.Store leaves
// the reader at EOF. Without an explicit Seek(0) before Decode the preview
// silently decodes zero bytes — a 200 with an empty header and rows_total 0.
func TestPreview_RewindsBetweenStoreAndDecode(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	header := []string{"Inv No", "Date", "Total"}
	rows := [][]string{{"INV-1", "2026-01-15", "119.00"}, {"INV-2", "2026-01-16", "220.00"}, {"INV-3", "2026-01-17", "330.00"}}
	body, ct := buildMultipartBody(t, "", "", "data.csv", "text/csv", csvBody(t, header, rows))

	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if len(resp.Columns) != len(header) {
		t.Fatalf("columns = %v, want %v — a zero-length decode means the reader was never rewound after Store", resp.Columns, header)
	}
	for i := range header {
		if resp.Columns[i] != header[i] {
			t.Errorf("columns[%d] = %q, want %q", i, resp.Columns[i], header[i])
		}
	}
	if resp.RowsTotal != len(rows) {
		t.Errorf("rows_total = %d, want %d", resp.RowsTotal, len(rows))
	}
}

// TestPreview_StoreReceivesRawFilenameAndDeclaredSize: the handler hands the
// store the RAW multipart filename (Service.Store owns the sanitization, and
// two copies of a security coercion drift apart) and fh.Size as the declared
// size (G8 — the parameter is overridden downstream but is still contract).
func TestPreview_StoreReceivesRawFilenameAndDeclaredSize(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	const raw = "  branch lagos.csv  "
	content := csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}})
	body, ct := buildMultipartBody(t, "", "", raw, "text/csv", content)

	rec, rawBody, _ := doPreviewUpload(t, store.fn(), &id, ct, body)

	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1 (status=%d body=%s)", len(store.calls), rec.Code, rawBody)
	}
	got := store.calls[0]
	if got.filename != raw {
		t.Errorf("filename handed to the store = %q, want the raw part filename %q", got.filename, raw)
	}
	if got.contentType != "text/csv" {
		t.Errorf("content type handed to the store = %q, want %q", got.contentType, "text/csv")
	}
	if got.size != int64(len(content)) {
		t.Errorf("declared size = %d, want fh.Size %d", got.size, len(content))
	}
}

// --- AC-2: which bodies carry document_id ----------------------------------

// TestPreview_SuccessBodyCarriesDocumentID: 200 carries the stored row's id.
func TestPreview_SuccessBodyCarriesDocumentID(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	body, ct := buildMultipartBody(t, "", "", "data.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))

	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if resp.DocumentID != store.doc.ID {
		t.Fatalf("document_id = %q, want %q", resp.DocumentID, store.doc.ID)
	}
	if _, err := uuid.Parse(resp.DocumentID); err != nil {
		t.Errorf("document_id %q is not a uuid: %v", resp.DocumentID, err)
	}
}

// TestPreview_PostStoreErrorBodyCarriesDocumentID: exactly two 4xx paths sit
// after the store call, and both name the document so the stored bytes are
// genuinely retrievable ([error-body-carries-document-id]).
func TestPreview_PostStoreErrorBodyCarriesDocumentID(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		ct       string
		content  []byte
	}{
		{"unrecognized format", "scan.pdf", "application/pdf", []byte("%PDF-1.7\n")},
		{"undecodable", "corrupt.csv", "text/csv", bytes.Repeat([]byte{0x00}, 32)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			store := newFakeDocStore()
			body, ct := buildMultipartBody(t, "", "", tc.filename, tc.ct, tc.content)

			rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
			}
			if resp.Error == "" {
				t.Error("expected a non-empty error message alongside document_id")
			}
			if resp.DocumentID != store.doc.ID {
				t.Errorf("document_id = %q, want %q", resp.DocumentID, store.doc.ID)
			}
		})
	}
}

// TestPreview_PreStoreErrorBodyOmitsDocumentID: the four failures that happen
// before anything is stored carry the bare envelope — no document exists to
// name — and must not call the store at all.
func TestPreview_PreStoreErrorBodyOmitsDocumentID(t *testing.T) {
	csvContent := csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}})

	cases := []struct {
		name       string
		identity   bool
		want       int
		newRequest func(t *testing.T) (io.Reader, string)
	}{
		{"no identity", false, http.StatusUnauthorized, func(t *testing.T) (io.Reader, string) {
			return buildMultipartBody(t, "", "", "data.csv", "text/csv", csvContent)
		}},
		{"oversized body", true, http.StatusRequestEntityTooLarge, func(t *testing.T) (io.Reader, string) {
			return buildMultipartBody(t, "", "", "big.csv", "text/csv", bytes.Repeat([]byte("x"), maxUploadBytes+1024))
		}},
		{"malformed multipart", true, http.StatusBadRequest, func(t *testing.T) (io.Reader, string) {
			return strings.NewReader("this is not a multipart body"), "multipart/form-data; boundary=nope"
		}},
		{"missing file part", true, http.StatusBadRequest, func(t *testing.T) (io.Reader, string) {
			return buildMultipartBody(t, "", "", "", "", nil)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeDocStore()
			var id *auth.Identity
			if tc.identity {
				i := testIdentity()
				id = &i
			}
			body, ct := tc.newRequest(t)

			rec, raw, _ := doPreviewUpload(t, store.fn(), id, ct, body)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.want, raw)
			}
			if len(store.calls) != 0 {
				t.Errorf("store calls = %d, want 0 — nothing may be stored on a pre-store failure", len(store.calls))
			}
			if hasDocumentIDKey(raw) {
				t.Errorf("body carries a document_id key when nothing was stored: %s", raw)
			}
		})
	}
}

// TestPreview_StorageFailureIs500: preview now has a 500 path (which is why the
// factory gained a logger). It carries no document_id and must not echo the
// underlying error.
func TestPreview_StorageFailureIs500(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	store.err = errStoreBoom
	body, ct := buildMultipartBody(t, "", "", "data.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))

	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the object store is unreachable (body=%s)", rec.Code, raw)
	}
	if hasDocumentIDKey(raw) {
		t.Errorf("a failed store must not name a document: %s", raw)
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
	if strings.Contains(resp.Error, errStoreBoom.Error()) {
		t.Errorf("500 body leaks the internal error: %q", resp.Error)
	}
}

// --- AC-3: the import request's parts --------------------------------------

// TestImport_ReadsDocumentBytesAndBatchFilename: the happy path — the handler
// opens the document, decodes ITS bytes, and hands Service.Import the document
// id plus the filename taken from the document row (AC-7), not from the wire.
func TestImport_ReadsDocumentBytesAndBatchFilename(t *testing.T) {
	id := testIdentity()
	header := []string{"Inv No", "Total"}
	rows := [][]string{{"INV-1", "119.00"}, {"INV-2", "220.00"}}
	open := newFakeDocOpen("q.csv", "text/csv", csvBody(t, header, rows))

	var gotEntity, gotFilename, gotDocumentID string
	var gotHeader []string
	var gotRows [][]string
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
		gotEntity, gotFilename, gotDocumentID = entityID, filename, documentID
		gotHeader, gotRows = h, r
		return BatchResult{ID: uuid.NewString(), Status: "completed", RowsTotal: len(r), RowsValid: len(r)}, nil
	}

	entityID := uuid.NewString()
	body, ct := buildImportForm(t, entityID, mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID)
	rec, raw, resp := doImportUpload(t, imp, open.fn(), &id, "", ct, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
	}
	if len(open.ids) != 1 || open.ids[0] != open.doc.ID {
		t.Fatalf("open called with %v, want exactly [%s]", open.ids, open.doc.ID)
	}
	if gotEntity != entityID {
		t.Errorf("entity_id = %q, want %q", gotEntity, entityID)
	}
	if gotDocumentID != open.doc.ID {
		t.Errorf("documentID handed to Import = %q, want %q", gotDocumentID, open.doc.ID)
	}
	if gotFilename != "q.csv" {
		t.Errorf("filename handed to Import = %q, want the document row's %q", gotFilename, "q.csv")
	}
	if len(gotHeader) != len(header) || gotHeader[0] != header[0] {
		t.Errorf("header = %v, want %v — the document's bytes must be what gets decoded", gotHeader, header)
	}
	if len(gotRows) != len(rows) {
		t.Errorf("rows = %d, want %d", len(gotRows), len(rows))
	}
	if resp.RowsTotal != len(rows) {
		t.Errorf("rows_total = %d, want %d", resp.RowsTotal, len(rows))
	}
	if open.body.closes != 1 {
		t.Errorf("object body closed %d times, want 1 — an unclosed body leaks the upstream connection", open.body.closes)
	}
}

// TestImport_RequiresDocumentID: entity_id + mapping and nothing else is a 400
// that names the missing field; neither the document nor the service is touched.
func TestImport_RequiresDocumentID(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("q.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("Import must not run without a document_id")
		return BatchResult{}, nil
	}

	body, ct := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), "")
	rec, raw, resp := doImportUpload(t, imp, open.fn(), &id, "", ct, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if !strings.Contains(resp.Error, "document_id") {
		t.Errorf("error = %q, want it to name document_id", resp.Error)
	}
	if len(open.ids) != 0 {
		t.Errorf("open calls = %d, want 0", len(open.ids))
	}
}

// TestImport_RejectsFilePart: the hard switch. A request still carrying the
// retired "file" part fails loudly rather than being silently ignored.
func TestImport_RejectsFilePart(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("q.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("Import must not run for a request carrying a file part")
		return BatchResult{}, nil
	}

	body, ct := buildImportForm(t, uuid.NewString(),
		mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID,
		importPart{field: "file", filename: "data.csv", content: csvBody(t, []string{"Inv No"}, [][]string{{"INV-9"}})})
	rec, raw, resp := doImportUpload(t, imp, open.fn(), &id, "", ct, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a request still uploading a file (body=%s)", rec.Code, raw)
	}
	if !strings.Contains(resp.Error, "file") {
		t.Errorf("error = %q, want it to name the retired file part", resp.Error)
	}
}

// TestImport_MalformedDocumentIDIs400 (G2): a uuid.Parse guard sits ahead of
// the open call, so a malformed id never reaches the document store — which is
// what makes "store never called" assertable, and keeps the store's own 22P02
// mapping off the wire.
func TestImport_MalformedDocumentIDIs400(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("q.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("Import must not run for a malformed document_id")
		return BatchResult{}, nil
	}

	body, ct := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), "abc")
	rec, raw, resp := doImportUpload(t, imp, open.fn(), &id, "", ct, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if !strings.Contains(resp.Error, "uuid") {
		t.Errorf("error = %q, want the shared well-formed-uuid wording, not the store's own 22P02 mapping", resp.Error)
	}
	if len(open.ids) != 0 {
		t.Fatalf("open calls = %v, want none — the uuid guard runs first", open.ids)
	}
}

// TestImport_UnrecognizedDocumentFormatIs400: the document row's own name and
// declared type are what detectFormat sees; neither resolving is a 400.
func TestImport_UnrecognizedDocumentFormatIs400(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("scan.pdf", "application/pdf", []byte("%PDF-1.7\n"))
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("Import must not run for an unrecognized document format")
		return BatchResult{}, nil
	}

	body, ct := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID)
	rec, raw, resp := doImportUpload(t, imp, open.fn(), &id, "", ct, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
	if len(open.ids) != 1 {
		t.Errorf("open calls = %d, want 1 — the format decision is made on the DOCUMENT's name and type", len(open.ids))
	}
}

// TestImport_NilObjectBodyIs500: a non-conformant store can hand back a nil
// body alongside a nil error. Decoding that nil-dereferences inside
// io.ReadAll, so the read path is guarded the same way the Close is —
// internal/document's DownloadHandler guards both.
func TestImport_NilObjectBodyIs500(t *testing.T) {
	id := testIdentity()
	docID := uuid.NewString()
	filename, contentType := "q.csv", "text/csv"
	open := func(ctx context.Context, _, rangeHeader string) (document.Document, document.Object, error) {
		return document.Document{ID: docID, Filename: &filename, DeclaredContentType: &contentType},
			document.Object{}, nil
	}
	imp := func(ctx context.Context, entityID, fn, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("Import must not run for a document with no body")
		return BatchResult{}, nil
	}

	body, ct := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), docID)
	rec, raw, resp := doImportUpload(t, imp, open, &id, "", ct, body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, raw)
	}
	if resp.Error != "internal server error" {
		t.Errorf("error = %q, want the generic 500 body", resp.Error)
	}
}

// --- AC-8: one whole-request cap, 15 MiB, on both endpoints -----------------

// TestUploadCapIs15MiB pins the constant itself: 15 MiB binary, matching the
// documents.size_bytes CHECK (<= 15728640). The decimal reading would disagree
// with the database and a 413 probe cannot tell the two apart cheaply.
func TestUploadCapIs15MiB(t *testing.T) {
	if maxUploadBytes != 15<<20 {
		t.Errorf("maxUploadBytes = %d, want %d (15 MiB)", maxUploadBytes, 15<<20)
	}
	if maxUploadBytes == 15_000_000 {
		t.Error("maxUploadBytes is 15,000,000 decimal bytes — the migration's CHECK is 15728640")
	}
}

// TestMaxXLSXUnzipTracksUploadCap: the unzip bound is defined as a multiple of
// the upload cap, so raising the cap must not silently pin the old absolute.
func TestMaxXLSXUnzipTracksUploadCap(t *testing.T) {
	if want := int64(10 * maxUploadBytes); maxXLSXUnzipBytes != want {
		t.Errorf("maxXLSXUnzipBytes = %d, want %d (10 * maxUploadBytes)", maxXLSXUnzipBytes, want)
	}
}

// TestPreview_UploadCapIs15MiB: over the cap is 413 with nothing stored; a body
// over the RETIRED 10 MiB cap but under 15 MiB is not 413 and is stored.
func TestPreview_UploadCapIs15MiB(t *testing.T) {
	t.Run("over the cap", func(t *testing.T) {
		id := testIdentity()
		store := newFakeDocStore()
		body, ct := buildMultipartBody(t, "", "", "big.csv", "text/csv", bytes.Repeat([]byte("x"), maxUploadBytes+1024))

		rec, raw, _ := doPreviewUpload(t, store.fn(), &id, ct, body)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 (body=%s)", rec.Code, raw)
		}
		if len(store.calls) != 0 {
			t.Errorf("store calls = %d, want 0 — a 413 body is never read far enough to store", len(store.calls))
		}
	})

	t.Run("under the cap, over the retired 10 MiB one", func(t *testing.T) {
		id := testIdentity()
		store := newFakeDocStore()
		// NULs so Decode rejects it cheaply: this probe is about the cap, not
		// about parsing 11 MiB of csv.
		content := bytes.Repeat([]byte{0x00}, 11<<20)
		body, ct := buildMultipartBody(t, "", "", "eleven.csv", "text/csv", content)

		rec, raw, _ := doPreviewUpload(t, store.fn(), &id, ct, body)

		if rec.Code == http.StatusRequestEntityTooLarge {
			t.Fatalf("status = 413 for an 11 MiB body, want it accepted under the 15 MiB cap (body=%s)", raw)
		}
		if len(store.calls) != 1 {
			t.Fatalf("store calls = %d, want 1", len(store.calls))
		}
		if n := len(store.calls[0].body); n != len(content) {
			t.Errorf("stored %d bytes, want %d", n, len(content))
		}
	})
}

// TestImport_UploadCapIs15MiB: the same two probes on POST /v1/imports. The
// padding rides in an unrelated part — the cap bounds the WHOLE request, not
// one part.
func TestImport_UploadCapIs15MiB(t *testing.T) {
	newImp := func(t *testing.T) importFunc {
		return func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
			return BatchResult{ID: uuid.NewString(), Status: "completed", RowsTotal: len(r), RowsValid: len(r)}, nil
		}
	}

	t.Run("over the cap", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("q.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
		body, ct := buildImportForm(t, uuid.NewString(),
			mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID,
			importPart{field: "pad", filename: "pad.bin", content: bytes.Repeat([]byte("x"), maxUploadBytes+1024)})

		rec, raw, _ := doImportUpload(t, newImp(t), open.fn(), &id, "", ct, body)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 (body=%s)", rec.Code, raw)
		}
		if len(open.ids) != 0 {
			t.Errorf("open calls = %d, want 0 on a 413", len(open.ids))
		}
	})

	t.Run("under the cap, over the retired 10 MiB one", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("q.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
		body, ct := buildImportForm(t, uuid.NewString(),
			mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID,
			importPart{field: "pad", filename: "pad.bin", content: bytes.Repeat([]byte("x"), 11<<20)})

		rec, raw, _ := doImportUpload(t, newImp(t), open.fn(), &id, "", ct, body)

		if rec.Code == http.StatusRequestEntityTooLarge {
			t.Fatalf("status = 413 for an 11 MiB body, want it accepted under the 15 MiB cap (body=%s)", raw)
		}
		if len(open.ids) != 1 {
			t.Errorf("open calls = %d, want 1 — the request parsed fine under the cap", len(open.ids))
		}
	})
}

// --- AC-7: GET /v1/imports/{id} is untouched --------------------------------

// TestGetBatch_BodyOmitsDocumentID: import_batches gains a document_id column,
// but the batch's wire contract does not. Guard, not a RED spec.
func TestGetBatch_BodyOmitsDocumentID(t *testing.T) {
	id := testIdentity()
	name := "q.csv"
	get := func(ctx context.Context, batchID string) (Batch, error) {
		return Batch{ID: batchID, EntityID: uuid.NewString(), Filename: &name, Status: "completed"}, nil
	}

	r := httptest.NewRequest("GET", "/v1/imports/"+uuid.NewString(), nil)
	r.SetPathValue("id", uuid.NewString())
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	GetHandler(get, nil).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if hasDocumentIDKey(raw) {
		t.Errorf("GET /v1/imports/{id} body gained a document_id key: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"filename":"q.csv"`)) {
		t.Errorf("filename is no longer on the wire as before: %s", raw)
	}
}
