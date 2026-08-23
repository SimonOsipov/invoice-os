// handlers_test.go: RED specs for AUDIT-05-08 (Mode A) -- DownloadHandler, the
// writeJSON/writeError/statusForErr seam and contentDisposition. handlers.go does not
// exist yet, so this file (and therefore the whole package archive test binary) does
// not compile until Stage 3 adds DownloadHandler, statusForErr and assembleFn -- the
// expected RED state (D-40/D-41/D-42/D-44/D-45 in the story pin the design).
// package archive (white-box), matching request_test.go/entity_db_test.go.
package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const testTenantID = "11111111-1111-1111-1111-111111111111"

// assembleSpy stands in for Store.Assemble as DownloadHandler consumes it
// (assembleFn). calls counts invocations so identity/parse-before-store ordering
// (AC-1, AC-2) is provable, not inferred from the status code alone. fn, when set,
// controls the (error, onStart, bytes-written) behavior a case needs; nil is a no-op
// success that writes nothing.
type assembleSpy struct {
	calls int
	fn    func(w io.Writer, onStart func(string)) error
}

func (s *assembleSpy) Assemble(_ context.Context, _ Request, w io.Writer, onStart func(filename string)) error {
	s.calls++
	if s.fn == nil {
		return nil
	}
	return s.fn(w, onStart)
}

func newTestRequest(t *testing.T, query url.Values, withIdentity bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/evidence-bundle?"+query.Encode(), nil)
	if withIdentity {
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: "system", TenantID: testTenantID}))
	}
	return r
}

func validQuery() url.Values {
	return url.Values{"entity_id": {validEntityID}, "from": {validFrom}, "to": {validTo}}
}

func decodeErrorBody(t *testing.T, body *bytes.Buffer) string {
	t.Helper()
	var doc struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&doc); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return doc.Error
}

// --- AC-1: identity before parsing ---------------------------------------------------

func TestBundleHandler_UnauthenticatedIs401BeforeParsing(t *testing.T) {
	spy := &assembleSpy{}
	h := DownloadHandler(spy.Assemble, slog.Default())

	// Malformed AND missing from/to: if parsing ran first this would 400, not 401.
	query := url.Values{"entity_id": {"not-a-uuid"}}
	r := newTestRequest(t, query, false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (identity must be checked before parsing)", rec.Code)
	}
	if spy.calls != 0 {
		t.Errorf("assemble called %d times, want 0 -- an unauthenticated caller must never reach the store", spy.calls)
	}
}

// --- AC-2: parsing before the store ---------------------------------------------------

func TestBundleHandler_MalformedParamIs400AndSkipsTheStore(t *testing.T) {
	spy := &assembleSpy{}
	h := DownloadHandler(spy.Assemble, slog.Default())

	query := url.Values{"entity_id": {validEntityID}, "from": {"not-a-time"}, "to": {validTo}}
	r := newTestRequest(t, query, true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	want := "from must be an RFC3339 timestamp"
	if got := decodeErrorBody(t, rec.Body); got != want {
		t.Errorf("error = %q, want the parser's own message %q", got, want)
	}
	if spy.calls != 0 {
		t.Errorf("assemble called %d times, want 0", spy.calls)
	}
}

// --- AC-3: Content-Type and Content-Disposition ---------------------------------------

func TestBundleHandler_SetsZipContentTypeAndDisposition(t *testing.T) {
	req, msg := parseRequest(validQuery())
	if msg != "" {
		t.Fatalf("test setup: parseRequest(validQuery()): %v", msg)
	}
	wantFilename := bundleFilename("Honeywell Group", req)
	// Control needle: pins the fixture to the story's own worked example (D-44), so this
	// test cannot pass by computing the same (possibly wrong) value on both sides.
	if want := "ASComply_evidence_Honeywell-Group_20260101_20260331.zip"; wantFilename != want {
		t.Fatalf("test setup: bundleFilename computed %q, want %q", wantFilename, want)
	}

	spy := &assembleSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart(wantFilename)
		_, err := w.Write([]byte("PK\x03\x04stub-zip-bytes"))
		return err
	}}
	h := DownloadHandler(spy.Assemble, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	wantDisposition := "attachment; filename=" + wantFilename
	got := rec.Header().Get("Content-Disposition")
	if got != wantDisposition {
		t.Errorf("Content-Disposition = %q, want %q (unquoted, D-44)", got, wantDisposition)
	}
	if strings.Contains(got, `"`) {
		t.Errorf("Content-Disposition = %q, must not be quoted for a token-safe filename (D-44)", got)
	}
}

// TestContentDisposition_QuotesWhenTheFallbackFilenameContainsAColon (D-44): the
// unquoted assertion above is contentDisposition's only coverage otherwise.
// bundleFilename falls back to Request.EntityID when the entity name has no
// alphanumeric character, and TestParseRequest_NonCanonicalUUIDFormsAcceptedRaw proves
// parseRequest keeps a urn:uuid: EntityID raw -- so the fallback filename can contain
// ':', a tspecial mime.FormatMediaType quotes.
func TestContentDisposition_QuotesWhenTheFallbackFilenameContainsAColon(t *testing.T) {
	r := Request{
		EntityID: "urn:uuid:a1b2c3d4-a1b2-c3d4-a1b2-c3d4a1b2c3d4",
		From:     mustParseRFC3339(t, validFrom),
		To:       mustParseRFC3339(t, validTo),
	}
	filename := bundleFilename("———", r) // no alphanumeric char: falls back to r.EntityID
	if !strings.Contains(filename, ":") {
		t.Fatalf("test setup: bundleFilename(non-alnum name) = %q, want it to contain ':' (the urn:uuid: fallback)", filename)
	}

	got := contentDisposition(filename)
	want := `attachment; filename="` + filename + `"`
	if got != want {
		t.Errorf("contentDisposition(%q) = %q, want %q (quoted -- a tspecial forces mime.FormatMediaType to quote, D-44)", filename, got, want)
	}
}

// --- AC-4: no Content-Length, framed by net/http instead -----------------------------

// TestBundleHandler_DeclaresNoContentLengthAndStillWrites replaces the story's vacuous
// AC-4 spec (D-42): against httptest.NewRecorder() a no-op handler ALSO has no
// Content-Length, so the absence assertion is paired with three controls proving the
// handler actually ran and streamed a real response.
func TestBundleHandler_DeclaresNoContentLengthAndStillWrites(t *testing.T) {
	spy := &assembleSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart("stub.zip")
		_, err := w.Write([]byte("PK\x03\x04stub-zip-bytes"))
		return err
	}}
	h := DownloadHandler(spy.Assemble, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want unset", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("control: Content-Type = %q, want application/zip -- the absence check above is meaningless if the handler never ran", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got == "" {
		t.Error("control: Content-Disposition is empty -- the absence check above is meaningless if the handler never ran")
	}
	if rec.Body.Len() == 0 {
		t.Error("control: response body is empty -- the absence check above is meaningless if the handler never ran")
	}
}

// TestBundleHandler_RealServerFramesTheBodyChunked (D-42): against a real
// httptest.NewServer, net/http itself decides the framing. Measured on this
// toolchain: a body of 2048 bytes or fewer gets a back-filled Content-Length; only
// above that does net/http switch to chunked. The stub body here is 3000 bytes so the
// test fails for the right reason, not because the fixture was too small.
func TestBundleHandler_RealServerFramesTheBodyChunked(t *testing.T) {
	stubBody := bytes.Repeat([]byte("Z"), 3000)
	spy := &assembleSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart("stub.zip")
		_, err := w.Write(stubBody)
		return err
	}}
	h := DownloadHandler(spy.Assemble, slog.Default())

	mux := http.NewServeMux()
	// Injects identity directly into the server-side request context: no JWT
	// middleware runs in this unit test, and the client can't transmit a context.
	mux.Handle("GET /v1/evidence-bundle", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithIdentity(r.Context(), auth.Identity{Subject: "system", TenantID: testTenantID})
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/v1/evidence-bundle?" + validQuery().Encode())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != len(stubBody) {
		t.Fatalf("control: response body is %d bytes, want %d -- the framing assertions below are meaningless if the handler didn't stream the full body", len(body), len(stubBody))
	}

	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Errorf("Content-Length header = %q, want absent", got)
	}
	if resp.ContentLength != -1 {
		t.Errorf("resp.ContentLength = %d, want -1 (unknown/streamed)", resp.ContentLength)
	}
	if len(resp.TransferEncoding) != 1 || resp.TransferEncoding[0] != "chunked" {
		t.Errorf("resp.TransferEncoding = %v, want [chunked]", resp.TransferEncoding)
	}
}

// --- AC-5: the shared error-mapping seam (D-40) ---------------------------------------

func TestStatusForErr_Table(t *testing.T) {
	cases := []struct {
		name           string
		err            error
		wantStatus     int
		wantMsg        string
		mustNotContain []string
	}{
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, "unauthorized", nil},
		{"entity not found", ErrEntityNotFound, http.StatusNotFound, "not found", nil},
		{
			"over cap", &TooManyInvoicesError{Count: 11000, Limit: 10000}, http.StatusBadRequest,
			"11000 invoices exceeds the bundle limit of 10000", []string{"archive:"},
		},
		{"unknown", errors.New("pq: relation x does not exist"), http.StatusInternalServerError, "internal server error", []string{"pq", "relation"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, msg := statusForErr(c.err)
			if status != c.wantStatus {
				t.Errorf("status = %d, want %d", status, c.wantStatus)
			}
			if msg != c.wantMsg {
				t.Errorf("msg = %q, want %q", msg, c.wantMsg)
			}
			for _, forbidden := range c.mustNotContain {
				if strings.Contains(msg, forbidden) {
					t.Errorf("msg %q leaks %q", msg, forbidden)
				}
			}
		})
	}
}

func TestBundleHandler_ErrNoTenantIs401(t *testing.T) {
	spy := &assembleSpy{fn: func(io.Writer, func(string)) error { return db.ErrNoTenant }}
	h := DownloadHandler(spy.Assemble, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorBody(t, rec.Body); got != "unauthorized" {
		t.Errorf("error = %q, want %q", got, "unauthorized")
	}
	if spy.calls != 1 {
		t.Errorf("assemble called %d times, want 1", spy.calls)
	}
}

func TestBundleHandler_UnknownEntityIs404(t *testing.T) {
	spy := &assembleSpy{fn: func(io.Writer, func(string)) error { return ErrEntityNotFound }}
	h := DownloadHandler(spy.Assemble, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), validEntityID) {
		t.Errorf("body %q names the entity id, want it absent", rec.Body.String())
	}
}

func TestBundleHandler_UnknownStoreErrorIs500WithNoInternals(t *testing.T) {
	spy := &assembleSpy{fn: func(io.Writer, func(string)) error {
		return errors.New("pq: relation x does not exist")
	}}
	h := DownloadHandler(spy.Assemble, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "pq") || strings.Contains(body, "relation") {
		t.Errorf("body %q leaks the internal error", body)
	}
}

// --- AC-6: over-cap 400 built from fields, never Error() (D-45) -----------------------

func TestBundleHandler_OverCapIs400NamingTheLimit(t *testing.T) {
	spy := &assembleSpy{fn: func(io.Writer, func(string)) error {
		return &TooManyInvoicesError{Count: 11000, Limit: 10000}
	}}
	h := DownloadHandler(spy.Assemble, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "11000") || !strings.Contains(body, "10000") {
		t.Errorf("body %q, want it to name both 11000 and 10000", body)
	}
	if strings.Contains(body, "archive:") {
		t.Errorf("body %q leaks the package-prefixed Error() string (D-45); statusForErr must use Count/Limit, never err.Error()", body)
	}
}

// --- D-41: headers are armed on the first byte, not before the store runs -------------

// TestBundleHandler_StoreErrorAfterFilenameButBeforeFirstByteIsStill500 discriminates
// against an eager-header implementation: onStart alone must not commit the response to
// a 200, or every 404/400/500 the store can still raise after selectEntity would ship as
// a 200 carrying a broken archive.
func TestBundleHandler_StoreErrorAfterFilenameButBeforeFirstByteIsStill500(t *testing.T) {
	spy := &assembleSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart("late-failure.zip") // filename known, but no byte ever reaches w
		return errors.New("boom: something failed after selectEntity")
	}}
	h := DownloadHandler(spy.Assemble, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (D-41: a failure before the first byte must still be an honest JSON error)", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want empty -- onStart alone must not arm the ZIP headers", got)
	}
}

// TestBundleHandler_ErrorAfterFirstByteKeeps200AndAppendsNoJSON is the mirror case:
// once bytes are on the wire the status line is already committed, so a later failure
// can only be logged, never turned into a JSON error appended to the stream.
func TestBundleHandler_ErrorAfterFirstByteKeeps200AndAppendsNoJSON(t *testing.T) {
	streamedBytes := []byte("PK\x03\x04-partial-bundle-bytes")
	spy := &assembleSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart("mid-stream-failure.zip")
		if _, err := w.Write(streamedBytes); err != nil {
			return err
		}
		return errors.New("boom: connection lost mid-stream")
	}}
	h := DownloadHandler(spy.Assemble, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (D-41: the status line is already committed once a byte has gone out; body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, streamedBytes) {
		t.Errorf("body = %q, want exactly the streamed bytes %q, no appended JSON", got, streamedBytes)
	}
	if strings.Contains(rec.Body.String(), `{"error"`) {
		t.Errorf("body %q contains an appended JSON error over an already-committed stream", rec.Body.String())
	}
}

// --- AUDIT-05-09: PreviewHandler shares DownloadHandler's error surface (D-40, D-52) --

// previewSpy stands in for Store.Preview as PreviewHandler consumes it (previewFn).
// Mirrors assembleSpy so one table can drive both handlers identically.
type previewSpy struct {
	calls int
	fn    func() (Preview, error)
}

func (s *previewSpy) Preview(_ context.Context, _ Request) (Preview, error) {
	s.calls++
	if s.fn == nil {
		return Preview{}, nil
	}
	return s.fn()
}

// TestPreviewAndDownload_ShareOneErrorSurface (D-40, D-52): both handlers call the same
// statusForErr, so the status/body mapping holds by construction -- asserting it alone
// proves nothing. Each row also asserts the store-call count on BOTH handlers, which
// does not hold by construction (each handler implements its own ordering).
func TestPreviewAndDownload_ShareOneErrorSurface(t *testing.T) {
	cases := []struct {
		name         string
		withIdentity bool
		query        url.Values
		storeErr     error // nil for the two pre-store rows -- the store must never be called
		wantStatus   int
		wantBody     string
		wantCalls    int
	}{
		{
			name:         "no identity",
			withIdentity: false,
			// Malformed entity_id too (D-52): without it a parse-first handler would
			// still 401, making the ordering assertion vacuous.
			query:      url.Values{"entity_id": {"not-a-uuid"}},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"error":"unauthorized"}` + "\n",
			wantCalls:  0,
		},
		{
			name:         "malformed param",
			withIdentity: true,
			query:        url.Values{"entity_id": {validEntityID}, "from": {"not-a-time"}, "to": {validTo}},
			wantStatus:   http.StatusBadRequest,
			wantBody:     `{"error":"from must be an RFC3339 timestamp"}` + "\n",
			wantCalls:    0,
		},
		{
			name:         "unknown entity",
			withIdentity: true,
			query:        validQuery(),
			storeErr:     ErrEntityNotFound,
			wantStatus:   http.StatusNotFound,
			wantBody:     `{"error":"not found"}` + "\n",
			wantCalls:    1,
		},
		{
			name:         "unknown store error",
			withIdentity: true,
			query:        validQuery(),
			storeErr:     errors.New("pq: relation x does not exist"),
			wantStatus:   http.StatusInternalServerError,
			wantBody:     `{"error":"internal server error"}` + "\n",
			wantCalls:    1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			aSpy := &assembleSpy{}
			if c.storeErr != nil {
				aSpy.fn = func(io.Writer, func(string)) error { return c.storeErr }
			}
			dh := DownloadHandler(aSpy.Assemble, slog.Default())
			dRec := httptest.NewRecorder()
			dh.ServeHTTP(dRec, newTestRequest(t, c.query, c.withIdentity))

			pSpy := &previewSpy{}
			if c.storeErr != nil {
				pSpy.fn = func() (Preview, error) { return Preview{}, c.storeErr }
			}
			ph := PreviewHandler(pSpy.Preview, slog.Default())
			pRec := httptest.NewRecorder()
			ph.ServeHTTP(pRec, newTestRequest(t, c.query, c.withIdentity))

			if dRec.Code != c.wantStatus {
				t.Errorf("download status = %d, want %d (body %s)", dRec.Code, c.wantStatus, dRec.Body.String())
			}
			if pRec.Code != c.wantStatus {
				t.Errorf("preview status = %d, want %d (body %s)", pRec.Code, c.wantStatus, pRec.Body.String())
			}
			if dRec.Body.String() != c.wantBody {
				t.Errorf("download body = %q, want %q", dRec.Body.String(), c.wantBody)
			}
			if pRec.Body.String() != c.wantBody {
				t.Errorf("preview body = %q, want %q", pRec.Body.String(), c.wantBody)
			}
			if !bytes.Equal(dRec.Body.Bytes(), pRec.Body.Bytes()) {
				t.Errorf("download and preview bodies differ byte-for-byte: %q vs %q", dRec.Body.String(), pRec.Body.String())
			}
			if aSpy.calls != c.wantCalls {
				t.Errorf("download store calls = %d, want %d", aSpy.calls, c.wantCalls)
			}
			if pSpy.calls != c.wantCalls {
				t.Errorf("preview store calls = %d, want %d", pSpy.calls, c.wantCalls)
			}
		})
	}
}

// TestPreviewHandler_Success200 is the control that the error table above is not
// PreviewHandler's only path (D-52).
func TestPreviewHandler_Success200(t *testing.T) {
	tin := "80000040-0001"
	want := Preview{
		Entity:    manifestEntity{ID: validEntityID, Name: "Success Co", TIN: &tin},
		Period:    manifestPeriod{From: validFrom, To: validTo, Bounds: "inclusive", Basis: "invoices.created_at"},
		Filename:  "ASComply_evidence_Success-Co_20260101_20260331.zip",
		Counts:    manifestCounts{Invoices: 2, StatusTransitions: 1, Submissions: 1, ExchangeAttempts: 1, BodyFiles: 1},
		OverLimit: false,
	}
	spy := &previewSpy{fn: func() (Preview, error) { return want, nil }}
	h := PreviewHandler(spy.Preview, slog.Default())
	r := newTestRequest(t, validQuery(), true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var got Preview
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded body = %+v, want %+v", got, want)
	}
	if spy.calls != 1 {
		t.Errorf("Preview called %d times, want 1", spy.calls)
	}
}
