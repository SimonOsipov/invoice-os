// handlers_test.go: the DownloadHandler specs, authored before handlers.go
// exists. Mostly closure-injected; TestRLS_Download* is DB-backed and needs the
// same DSN pair document_test.go documents.
package document_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- harness ----------------------------------------------------------------

type openFn func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error)

func testIdentity() auth.Identity {
	return auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
}

// doDownload drives the handler through a real ServeHTTP. SetPathValue stands in
// for the ServeMux pattern, so the handler's r.PathValue("id") resolves.
func doDownload(t *testing.T, open openFn, id *auth.Identity, docID, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/documents/"+docID, nil)
	r.SetPathValue("id", docID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	if rangeHeader != "" {
		r.Header.Set("Range", rangeHeader)
	}
	rec := httptest.NewRecorder()
	document.DownloadHandler(open, nil).ServeHTTP(rec, r)
	return rec
}

// countingBody is an object body that records its Close calls.
type countingBody struct {
	io.Reader
	closes int
}

func (b *countingBody) Close() error { b.closes++; return nil }

var errMidCopy = errors.New("object stream broke mid-copy")

// errAfterFirstChunk fails only after a byte has been written, i.e. after the
// 200 is already committed.
type errAfterFirstChunk struct {
	chunk []byte
	sent  bool
}

func (r *errAfterFirstChunk) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.chunk), nil
	}
	return 0, errMidCopy
}

// blockingBody hands back one chunk and then blocks: an implementation that
// buffers the object before writing never reaches the response writer.
type blockingBody struct {
	first   []byte
	sent    bool
	release chan struct{}
	closes  int
}

func (b *blockingBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.first), nil
	}
	<-b.release
	return 0, io.EOF
}

func (b *blockingBody) Close() error { b.closes++; return nil }

// firstWriteRecorder publishes the first Write so a test can observe the
// response mid-flight; a plain recorder only ever shows the finished body.
type firstWriteRecorder struct {
	*httptest.ResponseRecorder
	first chan []byte
}

func (w *firstWriteRecorder) Write(p []byte) (int, error) {
	select {
	case w.first <- bytes.Clone(p):
	default:
	}
	return w.ResponseRecorder.Write(p)
}

func nopObject(payload []byte) document.Object {
	return document.Object{Body: io.NopCloser(bytes.NewReader(payload)), Size: int64(len(payload))}
}

func namedDoc(name string) document.Document {
	return document.Document{ID: uuid.NewString(), StorageKey: "tenants/x/deadbeef", Filename: &name}
}

// --- AC-1: identity first ---------------------------------------------------

func TestDownloadHandler_NoIdentityIs401(t *testing.T) {
	open := func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error) {
		t.Error("open must not run without an identity — an unauthenticated request touches neither the database nor object storage")
		return document.Document{}, document.Object{}, nil
	}

	rec := doDownload(t, open, nil, uuid.NewString(), "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Result().Header.Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition on a 401 = %q, want unset — the byte-bearing headers must not leak onto an error body", got)
	}
}

// --- AC-2: malformed id is 400 before the store ------------------------------

func TestDownloadHandler_MalformedIDIs400(t *testing.T) {
	id := testIdentity()
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		t.Errorf("open must not run for a malformed id, got %q", docID)
		return document.Document{}, document.Object{}, nil
	}

	rec := doDownload(t, open, &id, "abc", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	// The store's own 22P02 mapping would put "document: validation" on the wire.
	if strings.Contains(rec.Body.String(), "document: validation") {
		t.Errorf("400 body = %s, want no package-internal error string", rec.Body.String())
	}
}

// --- AC-3: no existence oracle ----------------------------------------------

// Rendering only. Both legs of the real property are the SAME error value at
// this boundary, so byte-equality here is true by construction — that is what
// TestRLS_DownloadCrossTenantIs404AndIdenticalToUnknown exists for.
func TestDownloadHandler_NotFoundRendersA404WithoutObjectHeaders(t *testing.T) {
	id := testIdentity()
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		return document.Document{}, document.Object{}, fmt.Errorf("document: get k: %w", document.ErrNotFound)
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	h := rec.Result().Header
	if got := h.Get("Content-Type"); got == "application/octet-stream" {
		t.Errorf("Content-Type on a 404 = %q, want the error envelope's type", got)
	}
	for _, k := range []string{"Content-Disposition", "Accept-Ranges"} {
		if got := h.Get(k); got != "" {
			t.Errorf("%s on a 404 = %q, want unset", k, got)
		}
	}
}

// Three legs, one test: the positive leg is the only thing separating a real
// implementation from one that always 404s, and the byte-equality of the other
// two — not the status — is what proves there is no existence oracle.
func TestRLS_DownloadCrossTenantIs404AndIdenticalToUnknown(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenant1 := seedTenant(t, super, "DOC-DL-RLS tenant 1")
	tenant2 := seedTenant(t, super, "DOC-DL-RLS tenant 2")
	store := document.NewStore(app)
	c1 := identity(ctx, tenant1, uuid.NewString())
	c2 := identity(ctx, tenant2, uuid.NewString())

	doc1, _, err := store.Upsert(c1, docFixture(tenant1, "download-rls-1", 11))
	if err != nil {
		t.Fatalf("Upsert (tenant 1): %v", err)
	}
	doc2, _, err := store.Upsert(c2, docFixture(tenant2, "download-rls-2", 11))
	if err != nil {
		t.Fatalf("Upsert (tenant 2): %v", err)
	}

	payload := []byte("tenant-1 document bytes")
	objs := &fakeObjects{getObject: nopObject(payload)}
	svc := document.NewService(store, objs)
	id1 := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenant1}

	own := doDownload(t, svc.Open, &id1, doc1.ID, "")
	if own.Code != http.StatusOK {
		t.Fatalf("own-tenant download status = %d, want 200 (body=%s)", own.Code, own.Body.String())
	}
	if !bytes.Equal(own.Body.Bytes(), payload) {
		t.Errorf("own-tenant body = %q, want %q", own.Body.Bytes(), payload)
	}

	cross := doDownload(t, svc.Open, &id1, doc2.ID, "")
	if cross.Code != http.StatusNotFound {
		t.Errorf("cross-tenant download status = %d, want 404 (body=%s)", cross.Code, cross.Body.String())
	}

	unknown := doDownload(t, svc.Open, &id1, uuid.NewString(), "")
	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown-id download status = %d, want 404 (body=%s)", unknown.Code, unknown.Body.String())
	}

	if !bytes.Equal(cross.Body.Bytes(), unknown.Body.Bytes()) {
		t.Errorf("cross-tenant body = %s, unknown-id body = %s — want byte-identical (no existence oracle)",
			cross.Body.String(), unknown.Body.String())
	}
	// An RLS-invisible row must not even reach object storage.
	if len(objs.getKeys) != 1 || objs.getKeys[0] != doc1.StorageKey {
		t.Errorf("object-store GETs = %v, want exactly [%q] — a refused row must yield no object call at all",
			objs.getKeys, doc1.StorageKey)
	}
}

// --- AC-4: an audit failure yields no document bytes ------------------------

// The store has no audit-failure sentinel, so this can only be a generic error.
// "No bytes" is the AC's claim about the OBJECT, not about response length: a
// 500 still carries the package's error envelope.
func TestDownloadHandler_AuditFailureYieldsNoBytes(t *testing.T) {
	id := testIdentity()
	auditErr := errors.New("audit: insert audit_log row")
	sentinel := []byte("SECRET-DOCUMENT-BYTES")

	t.Run("zero object", func(t *testing.T) {
		open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
			return document.Document{}, document.Object{}, auditErr
		}
		rec := doDownload(t, open, &id, uuid.NewString(), "")

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), auditErr.Error()) {
			t.Errorf("500 body = %s, want a generic message, not the internal error", rec.Body.String())
		}
		h := rec.Result().Header
		if got := h.Get("Content-Type"); got == "application/octet-stream" {
			t.Errorf("Content-Type on a 500 = %q, want the error envelope's type", got)
		}
		for _, k := range []string{"Content-Disposition", "Accept-Ranges"} {
			if got := h.Get(k); got != "" {
				t.Errorf("%s on a 500 = %q, want unset", k, got)
			}
		}
	})

	// Deliberately unrealistic — Service.Open returns a zero Object on error. It
	// is the only shape that makes "no bytes" discriminating rather than vacuous.
	t.Run("populated object alongside the error", func(t *testing.T) {
		body := &countingBody{Reader: bytes.NewReader(sentinel)}
		open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
			return namedDoc("x.pdf"), document.Object{Body: body, Size: int64(len(sentinel))}, auditErr
		}
		rec := doDownload(t, open, &id, uuid.NewString(), "")

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
		}
		if bytes.Contains(rec.Body.Bytes(), sentinel) {
			t.Errorf("500 body carries the object bytes %q — an unaudited read must produce none", sentinel)
		}
	})
}

// --- AC-5: streamed, never buffered -----------------------------------------

func TestDownloadHandler_StreamsWithoutBuffering(t *testing.T) {
	id := testIdentity()
	first := []byte("first-chunk")
	body := &blockingBody{first: first, release: make(chan struct{})}
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		return namedDoc("big.pdf"), document.Object{Body: body}, nil
	}

	docID := uuid.NewString()
	r := httptest.NewRequest("GET", "/v1/documents/"+docID, nil)
	r.SetPathValue("id", docID)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := &firstWriteRecorder{ResponseRecorder: httptest.NewRecorder(), first: make(chan []byte, 1)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		document.DownloadHandler(open, nil).ServeHTTP(rec, r)
	}()

	select {
	case got := <-rec.first:
		if !bytes.Equal(got, first) {
			t.Errorf("first write = %q, want the object's first chunk %q", got, first)
		}
	case <-time.After(5 * time.Second):
		close(body.release)
		<-done
		t.Fatal("nothing reached the response writer while the object body was still open — the handler buffers the object before writing")
	}

	close(body.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the object body finished")
	}

	if got := rec.Body.Bytes(); !bytes.Equal(got, first) {
		t.Errorf("response body = %q, want %q", got, first)
	}
	if body.closes != 1 {
		t.Errorf("object body Close calls = %d, want 1", body.closes)
	}
}

func TestDownloadHandler_ClosesBodyOnEveryPath(t *testing.T) {
	id := testIdentity()
	payload := []byte("document bytes")

	cases := []struct {
		name     string
		body     io.Reader
		obj      func(io.ReadCloser) document.Object
		rangeHdr string
		wantCode int
	}{
		{
			name:     "success",
			body:     bytes.NewReader(payload),
			obj:      func(rc io.ReadCloser) document.Object { return document.Object{Body: rc, Size: int64(len(payload))} },
			wantCode: http.StatusOK,
		},
		{
			name: "mid-copy error",
			body: &errAfterFirstChunk{chunk: payload},
			obj:  func(rc io.ReadCloser) document.Object { return document.Object{Body: rc, Size: int64(len(payload))} },
			// The 200 is already committed when the read fails; the handler can
			// only close and log, never restatus.
			wantCode: http.StatusOK,
		},
		{
			name: "range",
			body: bytes.NewReader(payload[:5]),
			obj: func(rc io.ReadCloser) document.Object {
				return document.Object{Body: rc, Size: 5, ContentRange: "bytes 0-4/14", Partial: true}
			},
			rangeHdr: "bytes=0-4",
			wantCode: http.StatusPartialContent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cb := &countingBody{Reader: tc.body}
			open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
				return namedDoc("a.pdf"), tc.obj(cb), nil
			}
			rec := doDownload(t, open, &id, uuid.NewString(), tc.rangeHdr)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body=%q)", rec.Code, tc.wantCode, rec.Body.Bytes())
			}
			if cb.closes != 1 {
				t.Errorf("object body Close calls = %d, want 1", cb.closes)
			}
		})
	}
}

// --- AC-6: the fixed security headers ---------------------------------------

func TestDownloadHandler_SecurityHeaders(t *testing.T) {
	id := testIdentity()
	payload := []byte("pdf-bytes")
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		return namedDoc("invoice.pdf"), nopObject(payload), nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.Bytes())
	}
	h := rec.Result().Header
	want := map[string]string{
		"Content-Type":           "application/octet-stream",
		"X-Content-Type-Options": "nosniff",
		"Accept-Ranges":          "bytes",
		"Content-Length":         "9",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := h.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want it to start with attachment (never inline)", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Errorf("body = %q, want %q", rec.Body.Bytes(), payload)
	}
}

// The declared type is caller content; echoing it defeats the nosniff mitigation.
func TestDownloadHandler_NeverEchoesDeclaredContentType(t *testing.T) {
	id := testIdentity()
	declared := "text/html"
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		doc := namedDoc("payload.html")
		doc.DeclaredContentType = &declared
		return doc, nopObject([]byte("<script>alert(1)</script>")), nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "")

	if got := rec.Result().Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	for k, vs := range rec.Result().Header {
		for _, v := range vs {
			if strings.Contains(v, declared) {
				t.Errorf("header %s = %q carries the declared type %q", k, v, declared)
			}
		}
	}
}

// SanitizeFilename does not strip the double quote and passes non-ASCII through,
// so the stored name can still break a hand-built quoted-string.
func TestDownloadHandler_DispositionFilenameIsCoerced(t *testing.T) {
	id := testIdentity()

	for _, name := range []string{
		`evil".pdf`,
		"ev\"il\n/etc/passwd.pdf",
		"a\r\nX-Injected: 1.pdf",
	} {
		t.Run(name, func(t *testing.T) {
			open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
				return namedDoc(name), nopObject([]byte("x")), nil
			}
			rec := doDownload(t, open, &id, uuid.NewString(), "")

			got := rec.Result().Header.Get("Content-Disposition")
			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("Content-Disposition = %q, want a single line with no raw CR/LF", got)
			}
			mt, params, err := mime.ParseMediaType(got)
			if err != nil {
				t.Fatalf("Content-Disposition = %q is unparseable: %v", got, err)
			}
			if mt != "attachment" {
				t.Errorf("disposition type = %q, want attachment", mt)
			}
			if params["filename"] != name {
				t.Errorf("filename param = %q, want the stored name %q", params["filename"], name)
			}
		})
	}
}

func TestDownloadHandler_DispositionEncodesNonASCIIFilename(t *testing.T) {
	id := testIdentity()
	name := "фактура — naïve.pdf"
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		return namedDoc(name), nopObject([]byte("x")), nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "")

	got := rec.Result().Header.Get("Content-Disposition")
	for i := 0; i < len(got); i++ {
		if got[i] > 0x7f {
			t.Fatalf("Content-Disposition = %q carries a raw non-ASCII byte at %d; a header value must be ASCII", got, i)
		}
	}
	mt, params, err := mime.ParseMediaType(got)
	if err != nil {
		t.Fatalf("Content-Disposition = %q is unparseable: %v", got, err)
	}
	if mt != "attachment" || params["filename"] != name {
		t.Errorf("Content-Disposition = %q decodes to (%q, %q), want (attachment, %q)", got, mt, params["filename"], name)
	}
}

// A nil filename must not render filename="" — an empty param is worse than none.
func TestDownloadHandler_NilFilenameStillSetsAttachment(t *testing.T) {
	id := testIdentity()
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		doc := document.Document{ID: uuid.NewString(), StorageKey: "tenants/x/deadbeef"}
		return doc, nopObject([]byte("x")), nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "")

	got := rec.Result().Header.Get("Content-Disposition")
	if got == "" {
		t.Fatal("Content-Disposition is unset for a nil filename, want attachment")
	}
	if strings.Contains(got, `filename=""`) {
		t.Fatalf("Content-Disposition = %q, want the filename param omitted rather than empty", got)
	}
	mt, params, err := mime.ParseMediaType(got)
	if err != nil {
		t.Fatalf("Content-Disposition = %q is unparseable: %v", got, err)
	}
	if mt != "attachment" {
		t.Errorf("disposition type = %q, want attachment", mt)
	}
	if v, ok := params["filename"]; ok && v == "" {
		t.Error("Content-Disposition carries an empty filename param, want it omitted")
	}
}

// --- AC-7: ranges are the object store's business ---------------------------

func TestDownloadHandler_RangeReturns206WithContentRange(t *testing.T) {
	id := testIdentity()
	partial := []byte("0123456789")
	var gotRange string
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		gotRange = rangeHeader
		doc := namedDoc("big.pdf")
		doc.SizeBytes = 1000 // the full object; must NOT become Content-Length here
		return doc, document.Object{
			Body:         io.NopCloser(bytes.NewReader(partial)),
			Size:         int64(len(partial)),
			ContentRange: "bytes 0-9/1000",
			Partial:      true,
		}, nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "bytes=0-9")

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 (body=%q)", rec.Code, rec.Body.Bytes())
	}
	if gotRange != "bytes=0-9" {
		t.Errorf("open received range %q, want the inbound header verbatim %q", gotRange, "bytes=0-9")
	}
	h := rec.Result().Header
	if got := h.Get("Content-Range"); got != "bytes 0-9/1000" {
		t.Errorf("Content-Range = %q, want the object store's %q mirrored", got, "bytes 0-9/1000")
	}
	// Object.Size is the BODY length on a 206; Document.SizeBytes would over-declare.
	if got := h.Get("Content-Length"); got != "10" {
		t.Errorf("Content-Length = %q, want 10 (the range length, not the object's 1000)", got)
	}
	if got := h.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), partial) {
		t.Errorf("body = %q, want %q", rec.Body.Bytes(), partial)
	}
}

// The SDK sets a non-nil pointer to "" for a blank upstream Content-Range, so
// Partial can arrive true with the header empty. A 200 there would present the
// range bytes as the whole object and silently truncate the download.
func TestDownloadHandler_PartialWithBlankContentRangeIs500(t *testing.T) {
	id := testIdentity()
	partial := []byte("0123456789")

	cb := &countingBody{Reader: bytes.NewReader(partial)}
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		doc := namedDoc("big.pdf")
		doc.SizeBytes = 1000
		return doc, document.Object{Body: cb, Size: int64(len(partial)), Partial: true}, nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "bytes=0-9")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — %d partial bytes must not go out as a complete 200 (body=%q)",
			rec.Code, len(partial), rec.Body.Bytes())
	}
	if bytes.Contains(rec.Body.Bytes(), partial) {
		t.Errorf("500 body carries the object bytes %q — an upstream fault must yield none", partial)
	}
	h := rec.Result().Header
	if got := h.Get("Content-Type"); got == "application/octet-stream" {
		t.Errorf("Content-Type on a 500 = %q, want the error envelope's type", got)
	}
	for _, k := range []string{"Content-Disposition", "Accept-Ranges", "Content-Length", "Content-Range"} {
		if got := h.Get(k); got != "" {
			t.Errorf("%s on a 500 = %q, want unset", k, got)
		}
	}
	if cb.closes != 1 {
		t.Errorf("object body Close calls = %d, want 1", cb.closes)
	}
}

func TestDownloadHandler_UnsatisfiableRangeReturns416(t *testing.T) {
	id := testIdentity()
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		return document.Document{}, document.Object{}, fmt.Errorf("document: get k: %w", document.ErrRangeNotSatisfiable)
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "bytes=9999-")

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416 (body=%s)", rec.Code, rec.Body.String())
	}
	h := rec.Result().Header
	for _, k := range []string{"Content-Disposition", "Accept-Ranges"} {
		if got := h.Get(k); got != "" {
			t.Errorf("%s on a 416 = %q, want unset", k, got)
		}
	}
}

func TestDownloadHandler_NoRangeHeaderReturns200(t *testing.T) {
	id := testIdentity()
	payload := []byte("whole object")
	gotRange := "unset"
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		gotRange = rangeHeader
		return namedDoc("a.pdf"), nopObject(payload), nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.Bytes())
	}
	if gotRange != "" {
		t.Errorf("open received range %q, want the empty string when the request carries no Range", gotRange)
	}
	if got := rec.Result().Header.Get("Content-Range"); got != "" {
		t.Errorf("Content-Range on a 200 = %q, want unset", got)
	}
}

// A nil ContentLength arrives as Size == 0; declaring that over a non-empty body
// truncates the download.
func TestDownloadHandler_UnknownSizeDeclaresNoZeroContentLength(t *testing.T) {
	id := testIdentity()
	payload := []byte("bytes of unknown declared length")
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		return namedDoc("a.pdf"), document.Object{Body: io.NopCloser(bytes.NewReader(payload))}, nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.Bytes())
	}
	if got := rec.Result().Header.Get("Content-Length"); got == "0" {
		t.Errorf("Content-Length = %q over a %d-byte body — an unknown size must be left undeclared", got, len(payload))
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Errorf("body = %q, want %q", rec.Body.Bytes(), payload)
	}
}

// --- AC-8: the request carries only the opaque id ---------------------------

func TestDownloadHandler_KeyIsNeverReadFromTheRequest(t *testing.T) {
	const injected = "tenants/other-tenant/deadbeef"

	t.Run("query and header are ignored", func(t *testing.T) {
		id := testIdentity()
		docID := uuid.NewString()
		var gotID, gotRange string
		open := func(ctx context.Context, openID, rangeHeader string) (document.Document, document.Object, error) {
			gotID, gotRange = openID, rangeHeader
			return namedDoc("a.pdf"), nopObject([]byte("x")), nil
		}

		r := httptest.NewRequest("GET", "/v1/documents/"+docID+"?key="+injected+"&storage_key="+injected, nil)
		r.SetPathValue("id", docID)
		r.Header.Set("X-Storage-Key", injected)
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		rec := httptest.NewRecorder()
		document.DownloadHandler(open, nil).ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.Bytes())
		}
		if gotID != docID {
			t.Errorf("open received id %q, want the parsed uuid %q", gotID, docID)
		}
		if strings.Contains(gotID, injected) || strings.Contains(gotRange, injected) {
			t.Errorf("open received the request-supplied key (id=%q range=%q); the key comes only off the RLS-visible row", gotID, gotRange)
		}
	})

	t.Run("traversal-shaped id is rejected", func(t *testing.T) {
		id := testIdentity()
		open := func(ctx context.Context, openID, rangeHeader string) (document.Document, document.Object, error) {
			t.Errorf("open must not run for a traversal-shaped id, got %q", openID)
			return document.Document{}, document.Object{}, nil
		}
		rec := doDownload(t, open, &id, "../../"+injected, "")

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
	})
}

// --- structural pin ---------------------------------------------------------

// The handler takes the open func, not the *Service, so failure modes stay
// injectable — the repo's existing handler shape.
var _ func(func(context.Context, string, string) (document.Document, document.Object, error), *slog.Logger) http.HandlerFunc = document.DownloadHandler
