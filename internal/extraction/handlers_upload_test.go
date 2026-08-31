// handlers_upload_test.go: the status/body/ordering contract of POST /v1/documents, driven
// with httptest and injected store/enqueue spies. No database — this file must never call
// stRequire, the package's one sanctioned skip site, because scripts/ci/rls-test-gate.sh
// fails a step on any skip.
//
// Edge and negative cases live in handlers_upload_adversarial_test.go, which shares this
// file's harness; the two DB-backed claims live in handlers_upload_db_test.go.
//
// Helpers use an up* prefix; hnd rd st wk dc fx mx px pr ps pt pd pb pe rx de rp eq are taken.
package extraction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness --------------------------------------------------------------------------------

const (
	upRoute = "/v1/documents"

	// The refusal string this route owns. Spelled here rather than read off the production
	// constant: a test that recomputed it through the same symbol would agree with any value.
	upMsgRefusal      = "this file type cannot be read here"
	upMsgUnauthorized = "unauthorized"
	upMsgTooLarge     = "request body exceeds the upload size limit"

	upPDF  = "application/pdf"
	upPNG  = "image/png"
	upJPEG = "image/jpeg"
	upWebP = "image/webp"
	upDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	upXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

var upIdentity = auth.Identity{
	Subject:  "e5b10007-0000-4000-8000-000000000009",
	Role:     "authenticated",
	TenantID: "22222222-2222-2222-2222-222222222222",
}

// upSpy is the injected store and enqueue pair. order records the CALL SEQUENCE, which is the
// only way to prove classification happened before the store rather than merely that the
// status came out right.
//
// store echoes back what the handler handed it (id apart), mirroring what the real seam
// records. An assertion on the response's content_type therefore reads the value the handler
// CLASSIFIED, not a constant the spy chose.
type upSpy struct {
	order []string

	gotFilename    string
	gotContentType string
	gotSize        int64
	gotEnqueueID   string

	id       string
	reused   bool
	storeID  *string // overrides id when set, including with the empty string
	storeErr error
}

func newUpSpy() *upSpy { return &upSpy{id: uuid.NewString()} }

func (s *upSpy) store(_ context.Context, filename, contentType string, size int64, body io.ReadSeeker) (extraction.StoredDocument, error) {
	s.order = append(s.order, "store")
	s.gotFilename, s.gotContentType, s.gotSize = filename, contentType, size
	// Drained to EOF, as document.Service.Store drains it.
	if body != nil {
		_, _ = io.Copy(io.Discard, body)
	}
	if s.storeErr != nil {
		return extraction.StoredDocument{}, s.storeErr
	}
	id := s.id
	if s.storeID != nil {
		id = *s.storeID
	}
	return extraction.StoredDocument{
		ID:          id,
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   size,
		Reused:      s.reused,
	}, nil
}

func (s *upSpy) enqueue(_ context.Context, documentID string) (bool, error) {
	s.order = append(s.order, "enqueue")
	s.gotEnqueueID = documentID
	return false, nil
}

func (s *upSpy) calls(name string) int {
	n := 0
	for _, c := range s.order {
		if c == name {
			n++
		}
	}
	return n
}

// upBody builds a one-part multipart body. A blank partContentType leaves the part on
// multipart's own application/octet-stream default, which is what a browser sends for a file
// it cannot type. extra rides as additional form fields.
func upBody(t *testing.T, filename, partContentType string, content []byte, extra map[string]string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for k, v := range extra {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if filename != "" {
		var fw io.Writer
		var err error
		if partContentType != "" {
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
			h.Set("Content-Type", partContentType)
			fw, err = w.CreatePart(h)
		} else {
			fw, err = w.CreateFormFile("file", filename)
		}
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatalf("write file content: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// upServe drives the handler once. A nil id means no identity on the context.
func upServe(t *testing.T, spy *upSpy, id *auth.Identity, contentType string, body io.Reader) (*httptest.ResponseRecorder, []byte, map[string]json.RawMessage) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, upRoute, body)
	r.Header.Set("Content-Type", contentType)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	extraction.UploadHandler(spy.store, spy.enqueue, nil).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	decoded := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec, raw, decoded
}

// upPost is upServe for the common case: an authenticated caller sending one file part.
func upPost(t *testing.T, spy *upSpy, filename, partContentType string, content []byte) (*httptest.ResponseRecorder, []byte, map[string]json.RawMessage) {
	t.Helper()
	body, ct := upBody(t, filename, partContentType, content, nil)
	return upServe(t, spy, &upIdentity, ct, body)
}

// upString reads one string field out of the decoded body, failing when it is absent.
func upString(t *testing.T, decoded map[string]json.RawMessage, key string) string {
	t.Helper()
	raw, ok := decoded[key]
	if !ok {
		t.Fatalf("response carries no %q key; got keys %v", key, upKeys(decoded))
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode %q as a string from %s: %v", key, raw, err)
	}
	return s
}

func upBool(t *testing.T, decoded map[string]json.RawMessage, key string) bool {
	t.Helper()
	raw, ok := decoded[key]
	if !ok {
		t.Fatalf("response carries no %q key; got keys %v", key, upKeys(decoded))
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("decode %q as a bool from %s: %v", key, raw, err)
	}
	return b
}

func upKeys(decoded map[string]json.RawMessage) []string {
	out := make([]string, 0, len(decoded))
	for k := range decoded {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- AC-1: the accepted types --------------------------------------------------------------

// TestUploadHandler_PdfIs201WithDocumentId: the happy path. The body names a document, and it
// names NO extraction job -- no such value exists at response time (the Stage-1 finding), so
// its ABSENCE is the assertion.
func TestUploadHandler_PdfIs201WithDocumentId(t *testing.T) {
	spy := newUpSpy()
	rec, raw, decoded := upPost(t, spy, "native_invoice.pdf", upPDF, []byte("%PDF-1.7 fake"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for a pdf upload (body=%s)", rec.Code, raw)
	}
	if got := upString(t, decoded, "document_id"); got != spy.id {
		t.Errorf("document_id = %q, want the store seam's %q", got, spy.id)
	}
	if _, err := uuid.Parse(upString(t, decoded, "document_id")); err != nil {
		t.Errorf("document_id is not a well-formed uuid: %v", err)
	}
	if _, ok := decoded["extraction_job_id"]; ok {
		t.Errorf("body carries extraction_job_id; no job id exists at response time, so any value there is invented (body=%s)", raw)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if spy.calls("enqueue") != 1 {
		t.Errorf("enqueue ran %d time(s), want exactly 1 (order=%v)", spy.calls("enqueue"), spy.order)
	}
	if spy.gotSize != int64(len("%PDF-1.7 fake")) {
		t.Errorf("the store seam was handed size %d, want the part size %d", spy.gotSize, len("%PDF-1.7 fake"))
	}
}

// TestUploadHandler_AcceptsAllFiveDocumentTypes: every accepted type, twice -- once resolved
// from the extension with an untyped part, once resolved from a declared content type carrying
// a ;charset= suffix behind an extension the table does not know. The asserted content_type is
// the value the handler handed the store, so it reads the CLASSIFICATION, not the spy.
func TestUploadHandler_AcceptsAllFiveDocumentTypes(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		partType    string
		wantContent string
	}{
		{"pdf by extension", "scan.pdf", "", upPDF},
		{"png by extension", "scan.PNG", "", upPNG},
		{"jpg by extension", "scan.jpg", "", upJPEG},
		{"jpeg by extension", "scan.jpeg", "", upJPEG},
		{"webp by extension", "scan.webp", "", upWebP},
		{"docx by extension", "scan.docx", "", upDOCX},

		{"pdf by content type", "scan.bin", upPDF + "; charset=utf-8", upPDF},
		{"png by content type", "scan.bin", upPNG + "; charset=utf-8", upPNG},
		{"jpeg by content type", "scan.bin", upJPEG + "; charset=utf-8", upJPEG},
		{"webp by content type", "scan.bin", upWebP + "; charset=utf-8", upWebP},
		{"docx by content type", "scan.bin", upDOCX + "; charset=utf-8", upDOCX},
	}
	if len(cases) < 11 {
		t.Fatalf("the table holds %d case(s); the five types by extension and by content type need at least 11", len(cases))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := newUpSpy()
			rec, raw, decoded := upPost(t, spy, c.filename, c.partType, []byte("bytes"))

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 for %s/%q (body=%s)", rec.Code, c.filename, c.partType, raw)
			}
			if got := upString(t, decoded, "content_type"); got != c.wantContent {
				t.Errorf("content_type = %q, want the canonical %q", got, c.wantContent)
			}
			if spy.gotContentType != c.wantContent {
				t.Errorf("the store seam was handed content type %q, want the canonical %q -- the row would record the raw part header", spy.gotContentType, c.wantContent)
			}
			if spy.gotFilename != c.filename {
				t.Errorf("the store seam was handed filename %q, want the raw part filename %q -- Service.Store owns the sanitization", spy.gotFilename, c.filename)
			}
		})
	}
}

// --- AC-2: the refusal ------------------------------------------------------------------

// TestUploadHandler_SpreadsheetIs400WithTheRefusalMessage: a spreadsheet has its own route, so
// this one refuses it -- with the exact wire string, and before the store.
//
// The accepted control runs FIRST: "the store spy recorded nothing" also holds for a handler
// that never calls the store at all, and the control is what separates the two.
func TestUploadHandler_SpreadsheetIs400WithTheRefusalMessage(t *testing.T) {
	control := newUpSpy()
	rec, raw, _ := upPost(t, control, "native_invoice.pdf", upPDF, []byte("%PDF-1.7 fake"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("control: an accepted pdf returned %d, want 201 (body=%s); the store-never-ran assertions below would pass vacuously", rec.Code, raw)
	}
	if control.calls("store") != 1 {
		t.Fatalf("control: the store seam ran %d time(s) on an accepted pdf, want 1; the store-never-ran assertions below would pass vacuously", control.calls("store"))
	}

	cases := []struct {
		name     string
		filename string
		partType string
	}{
		{"csv by extension", "invoices.csv", "text/csv"},
		{"xlsx by extension", "invoices.xlsx", upXLSX},
		{"csv by content type", "invoices.bin", "text/csv"},
		{"xlsx by content type", "invoices.bin", upXLSX},
		{"text/plain by content type", "invoices.bin", "text/plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := newUpSpy()
			rec, raw, decoded := upPost(t, spy, c.filename, c.partType, []byte("a,b\n1,2\n"))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %s/%q (body=%s)", rec.Code, c.filename, c.partType, raw)
			}
			if got := upString(t, decoded, "error"); got != upMsgRefusal {
				t.Errorf("error = %q, want the refusal string %q", got, upMsgRefusal)
			}
			if spy.calls("store") != 0 {
				t.Errorf("the store seam ran %d time(s) on a refused upload, want 0 (order=%v)", spy.calls("store"), spy.order)
			}
			if _, ok := decoded["document_id"]; ok {
				t.Errorf("a refusal body names a document_id; nothing was stored, so there is none (body=%s)", raw)
			}
		})
	}
}

// TestUploadHandler_ClassifyPrecedesStore: the dead-store inversion, as an ORDER assertion.
// POST /v1/imports/preview stores at handlers.go:397 and classifies at :416; this route must
// do the reverse. Both spies ride one order slice, so a handler that skipped the store and
// still enqueued fails here rather than passing on a store count alone.
func TestUploadHandler_ClassifyPrecedesStore(t *testing.T) {
	accepted := newUpSpy()
	rec, raw, _ := upPost(t, accepted, "scan.pdf", upPDF, []byte("%PDF-1.7 fake"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("an accepted pdf returned %d, want 201 (body=%s); the empty-order assertion below would be vacuous", rec.Code, raw)
	}
	if got := strings.Join(accepted.order, ","); got != "store,enqueue" {
		t.Errorf("an accepted upload ran %q, want %q -- the enqueue must follow the store that names the document", got, "store,enqueue")
	}

	refused := newUpSpy()
	rec, raw, _ = upPost(t, refused, "archive.zip", "application/zip", []byte("PK\x03\x04"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unsupported .zip returned %d, want 400 (body=%s)", rec.Code, raw)
	}
	if len(refused.order) != 0 {
		t.Errorf("a refused upload ran %v, want neither seam -- classification must precede both", refused.order)
	}
}

// --- AC-3: identity, then the cap ----------------------------------------------------------

// TestUploadHandler_NoIdentityOversizedBodyIs401Not413 mirrors PRV-01
// (internal/importer/handlers_preview_test.go:129): a stranger's bytes are never read, so the
// identity check sits above http.MaxBytesReader.
func TestUploadHandler_NoIdentityOversizedBodyIs401Not413(t *testing.T) {
	spy := newUpSpy()
	body, ct := upBody(t, "scan.pdf", upPDF, bytes.Repeat([]byte("x"), 16<<20), nil)
	rec, raw, decoded := upServe(t, spy, nil, ct, body)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for no identity plus an oversized body, NOT 413 (body=%s)", rec.Code, raw)
	}
	if got := upString(t, decoded, "error"); got != upMsgUnauthorized {
		t.Errorf("error = %q, want %q", got, upMsgUnauthorized)
	}
	if len(spy.order) != 0 {
		t.Errorf("an unauthenticated upload ran %v, want neither seam", spy.order)
	}
}

// TestUploadHandler_OversizedBodyIs413: above maxMultipartMemory ParseMultipartForm spools to
// disk but keeps reading through the MaxBytesReader, so the 16 MiB case surfaces as
// *http.MaxBytesError and must be answered 413 -- never the generic "invalid multipart form"
// 400 arm.
func TestUploadHandler_OversizedBodyIs413(t *testing.T) {
	if extraction.MaxUploadBytesForTest != 15<<20 {
		t.Fatalf("maxUploadBytes = %d, want %d (15 MiB) -- documents.size_bytes CHECKs <= 15728640", extraction.MaxUploadBytesForTest, 15<<20)
	}

	spy := newUpSpy()
	rec, raw, decoded := upPost(t, spy, "scan.pdf", upPDF, bytes.Repeat([]byte("x"), 16<<20))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a 16 MiB body (body=%s)", rec.Code, raw)
	}
	if got := upString(t, decoded, "error"); got != upMsgTooLarge {
		t.Errorf("error = %q, want %q", got, upMsgTooLarge)
	}
	if len(spy.order) != 0 {
		t.Errorf("an oversized upload ran %v, want neither seam", spy.order)
	}
}

// --- AC-4: the suspended caller --------------------------------------------------------------

// TestUploadHandler_SuspendedMemberIs403: statusForErr already maps db.ErrNotActiveMember, so
// a suspended caller must not fall to the 500 default the way an unmapped error would.
func TestUploadHandler_SuspendedMemberIs403(t *testing.T) {
	spy := newUpSpy()
	spy.storeErr = db.ErrNotActiveMember
	rec, raw, decoded := upPost(t, spy, "scan.pdf", upPDF, []byte("%PDF-1.7 fake"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for db.ErrNotActiveMember, never 500 (body=%s)", rec.Code, raw)
	}
	if got := upString(t, decoded, "error"); got != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want db.NotActiveMemberMessage %q", got, db.NotActiveMemberMessage)
	}
	if spy.calls("enqueue") != 0 {
		t.Errorf("enqueue ran %d time(s) after a failed store, want 0 -- there is no document to extract", spy.calls("enqueue"))
	}
}

// --- AC-5: the reuse flag ------------------------------------------------------------------

// TestUploadHandler_ReusedFlagIsTheStoreSeamsVerdictNotDerived exercises BOTH arms: a handler
// that hardcoded false would pass a one-arm test.
func TestUploadHandler_ReusedFlagIsTheStoreSeamsVerdictNotDerived(t *testing.T) {
	for _, want := range []bool{false, true} {
		t.Run(fmt.Sprintf("reused=%t", want), func(t *testing.T) {
			spy := newUpSpy()
			spy.reused = want
			rec, raw, decoded := upPost(t, spy, "scan.pdf", upPDF, []byte("%PDF-1.7 fake"))

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
			}
			if got := upBool(t, decoded, "reused"); got != want {
				t.Errorf("reused = %t, want the store seam's verdict %t", got, want)
			}
		})
	}
}

// --- AC-6: the CORS fence (green from the start -- it reads shipped gateway source) ---------

var upCorsAllowMethodsRE = regexp.MustCompile(`corsAllowMethods\s*=\s*"([^"]*)"`)

// TestUploadHandler_PostIsAlreadyInCorsAllowMethods mirrors
// internal/importer/handlers_document_test.go:286-306: a NEW http method would need a
// corsAllowMethods edit no other test can see. POST already being there is what this reads.
func TestUploadHandler_PostIsAlreadyInCorsAllowMethods(t *testing.T) {
	raw, err := os.ReadFile("../gateway/cors.go")
	if err != nil {
		t.Fatalf("read ../gateway/cors.go: %v", err)
	}
	m := upCorsAllowMethodsRE.FindSubmatch(raw)
	if m == nil {
		t.Fatal("no corsAllowMethods constant in ../gateway/cors.go; the extraction lost its anchor")
	}
	methods := string(m[1])
	if strings.TrimSpace(methods) == "" {
		t.Fatal("corsAllowMethods read as empty; the check below would pass vacuously")
	}
	if !strings.Contains(methods, http.MethodPost) {
		t.Errorf("corsAllowMethods = %q, want it to already contain POST", methods)
	}
}

// --- AC-7: the exact field set ---------------------------------------------------------------

// TestUploadResponse_FieldSetIsExact reads the KEYS off a real 201, not the Go struct: an
// omitempty tag drops a false or a zero from the wire while leaving the field in place, and
// only the wire can see that. Both reuse arms run for exactly that reason.
func TestUploadResponse_FieldSetIsExact(t *testing.T) {
	want := []string{"content_type", "document_id", "filename", "reused", "size_bytes"}

	for _, reused := range []bool{false, true} {
		t.Run(fmt.Sprintf("reused=%t", reused), func(t *testing.T) {
			spy := newUpSpy()
			spy.reused = reused
			rec, raw, decoded := upPost(t, spy, "scan.pdf", upPDF, []byte("%PDF-1.7 fake"))

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
			}
			got := upKeys(decoded)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("201 body keys = %v, want exactly %v (body=%s)", got, want, raw)
			}
		})
	}
}

// --- the two seam edges QA found -------------------------------------------------------------

// TestUploadHandler_NeverEnqueuesABlankDocumentID: EnqueueExtraction accepts a blank document
// id silently and burns the tenant's "extract:" key permanently -- the dedupe behind it is not
// in-flight, so that key never comes back. The handler is the only place that can refuse it.
//
// The non-blank control runs first: "enqueue never ran" also holds for a handler that never
// enqueues at all.
func TestUploadHandler_NeverEnqueuesABlankDocumentID(t *testing.T) {
	control := newUpSpy()
	rec, raw, _ := upPost(t, control, "scan.pdf", upPDF, []byte("%PDF-1.7 fake"))
	if rec.Code != http.StatusCreated || control.calls("enqueue") != 1 {
		t.Fatalf("control: a stored pdf returned %d with %d enqueue call(s), want 201 and 1 (body=%s); the assertions below would pass vacuously",
			rec.Code, control.calls("enqueue"), raw)
	}

	blank := ""
	spy := newUpSpy()
	spy.storeID = &blank
	rec, raw, _ = upPost(t, spy, "scan.pdf", upPDF, []byte("%PDF-1.7 fake"))

	if spy.calls("enqueue") != 0 {
		t.Errorf("enqueue ran %d time(s) for a blank document id, want 0 -- it would burn the tenant's extract: key on nothing", spy.calls("enqueue"))
	}
	if rec.Code == http.StatusCreated {
		t.Errorf("status = 201 for a store that named no document; the caller cannot poll a blank document_id (body=%s)", raw)
	}
}

// TestUploadHandler_EnqueuesTheStoredIdNotACallerSuppliedOne: the enqueue seam performs no
// ownership check, so the only thing keeping a caller from queueing extraction over another
// tenant's document is that this route never reads a document id off the request. Pinned here
// so the gap stays deliberate.
func TestUploadHandler_EnqueuesTheStoredIdNotACallerSuppliedOne(t *testing.T) {
	foreign := uuid.NewString()
	spy := newUpSpy()
	body, ct := upBody(t, "scan.pdf", upPDF, []byte("%PDF-1.7 fake"), map[string]string{
		"document_id": foreign,
		"id":          foreign,
	})
	rec, raw, decoded := upServe(t, spy, &upIdentity, ct, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
	}
	if spy.calls("enqueue") != 1 {
		t.Fatalf("enqueue ran %d time(s), want exactly 1; the identity assertion below would read nothing", spy.calls("enqueue"))
	}
	if spy.gotEnqueueID != spy.id {
		t.Errorf("enqueue was handed %q, want the store seam's %q", spy.gotEnqueueID, spy.id)
	}
	if spy.gotEnqueueID == foreign {
		t.Errorf("enqueue was handed the caller-supplied document id %q; the seam does no ownership check, so this queues extraction over a row the caller may not own", foreign)
	}
	if got := upString(t, decoded, "document_id"); got != spy.id {
		t.Errorf("document_id = %q, want the store seam's %q", got, spy.id)
	}
}

// --- CLASSIFY-5 (EXTR-09-04, task-771, Test-first) --------------------------------------------

// The picker's table lives in TypeScript and this one lives in Go; nothing but this test
// makes them one table. It reads the SOURCE of both, because reading either through an
// exported symbol would agree with whatever value that symbol held.
const upPickerSource = "../../frontend/app/src/lib/importFlow.ts"

var (
	// Whole-line comments only, so a mid-line slash inside a content type survives.
	// importFlow.ts documents the expected literal in prose; stripping it keeps the
	// example below from being read as the table.
	upTSCommentRE = regexp.MustCompile(`(?m)^[ \t]*//.*$`)
	upTSTableRE   = regexp.MustCompile(`(?s)ACCEPTED_PICKED_TYPES[^=]*=\s*\[(.*?)\n\]`)
	upTSRowRE     = regexp.MustCompile(`(?s)\{\s*ext:\s*'([^']*)'\s*,\s*kind:\s*'([^']*)'\s*,\s*contentTypes:\s*\[([^\]]*)\]`)
	upTSStringRE  = regexp.MustCompile(`'([^']*)'`)
)

func upSortedKeys(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// TestUploadHandler_BrowserAndGoAcceptedTypeTablesAgree (CLASSIFY-5) pins the picker's
// document half to classify.go. Without it, "all six layers agree" is true the day it
// merges and unfalsifiable afterwards.
//
// Only the DOCUMENT half is compared, and that is the whole claim: Go holds the document
// types alone (spreadsheets have their own route), so the shared domain is the six document
// extensions. The TypeScript table also carries .csv/.xlsx, which is why a .csv declared
// application/pdf classifies as a spreadsheet in the browser and as a PDF here.
func TestUploadHandler_BrowserAndGoAcceptedTypeTablesAgree(t *testing.T) {
	raw, err := os.ReadFile(upPickerSource)
	if err != nil {
		t.Fatalf("read %s: %v", upPickerSource, err)
	}
	src := upTSCommentRE.ReplaceAll(raw, nil)

	m := upTSTableRE.FindSubmatch(src)
	if m == nil {
		t.Fatalf("no ACCEPTED_PICKED_TYPES = [ ... ] literal in %s; CLASSIFY-5 reads it as one\n"+
			"    { ext: %s, kind: %s, contentTypes: [%s] },\n"+
			"row per entry, closed by a newline and a bare ]", upPickerSource, "'.pdf'", "'document'", "'application/pdf'")
	}

	rows := upTSRowRE.FindAllSubmatch(m[1], -1)
	if len(rows) == 0 {
		t.Fatalf("the ACCEPTED_PICKED_TYPES literal in %s parsed to 0 rows; every assertion below would pass over nothing", upPickerSource)
	}

	tsExt := map[string]bool{}
	tsCT := map[string]bool{}
	kinds := map[string]int{}
	for _, r := range rows {
		ext, kind := string(r[1]), string(r[2])
		kinds[kind]++
		if kind != "document" {
			continue
		}
		tsExt[ext] = true
		for _, q := range upTSStringRE.FindAllSubmatch(r[3], -1) {
			tsCT[string(q[1])] = true
		}
	}

	// Floors. The spreadsheet count proves the regex read the WHOLE literal rather than a
	// leading fragment that happened to hold only document rows.
	if kinds["spreadsheet"] == 0 {
		t.Fatalf("parsed %d row(s) from %s and none of kind spreadsheet; the read landed on a fragment, not the table", len(rows), upPickerSource)
	}
	if len(tsExt) == 0 || len(tsCT) == 0 {
		t.Fatalf("the TypeScript document half parsed to %d extension(s) and %d content type(s); the comparison below would be vacuous", len(tsExt), len(tsCT))
	}

	goTable := extractionAcceptedTypes(t)
	if len(goTable) == 0 {
		t.Fatal("acceptedDocumentTypes parsed empty out of classify.go; the comparison below would be vacuous")
	}
	goExt := map[string]bool{}
	goCT := map[string]bool{}
	for ext, ct := range goTable {
		goExt[ext] = true
		goCT[ct] = true
	}

	if got, want := upSortedKeys(tsExt), upSortedKeys(goExt); got != want {
		t.Errorf("extension sets differ.\n  %s: %s\n  classify.go: %s", upPickerSource, got, want)
	}
	if got, want := upSortedKeys(tsCT), upSortedKeys(goCT); got != want {
		t.Errorf("content-type sets differ.\n  %s: %s\n  classify.go: %s", upPickerSource, got, want)
	}
}
