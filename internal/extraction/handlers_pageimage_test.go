// handlers_pageimage_test.go: the status/header/body contract of
// GET /v1/extractions/{id}/pages/{n}, driven with httptest and two spy seams. No database --
// this file must never call stRequire, the package's one sanctioned skip site, because
// scripts/ci/rls-test-gate.sh fails a step on any skip. It shares handlers_test.go's hnd*
// harness and handlers_detail_test.go's dtl* wire messages: the three extraction routes answer
// refusals with one envelope, and a second copy of it here could drift.
//
// Helpers use a pim* prefix; hnd dtl rvd rda up rd st wk dc fx mx px pr ps pt pd pb pe rx de rp
// are taken.
package extraction_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go/ast"
	"go/parser"
	"go/token"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness --------------------------------------------------------------------------------

const (
	// The two wildcard names in "GET /v1/extractions/{id}/pages/{n}". The mux binds them in
	// production.
	pimIDKey   = "id"
	pimPageKey = "n"

	// The one wire message this route owns. The malformed-id 400 and the 404 are asserted
	// through handlers_detail_test.go's constants instead: both routes name the same path
	// parameter, and a second copy here would let the two drift apart while this file stayed
	// green.
	pimMsgBadPage = "page must be a positive integer"

	// The three response headers AC 4 fixes. Content-Disposition is deliberately absent from
	// this list: it is asserted as an absent HEADER, not as an empty value.
	pimContentType  = "image/png"
	pimNosniff      = "nosniff"
	pimCacheControl = "private, no-store"

	// A key in the shape extraction_page_images_key_tenant_scoped admits. It is the reader's
	// output and the object store's input, so the assertions read it verbatim on both sides.
	pimStorageKey = "tenants/11111111-1111-1111-1111-111111111111/pages/" +
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc/v1/p0002.png"

	// ls internal/extraction/*.go | grep -v _test | wc -l -> 26 at 201d7169. A floor, not an
	// equality: the point is that the walk resolved a real directory.
	pimMinPackageFiles = 26

	pimHandlerName = "PageImageHandler"
)

// pimPNG is a body no other fixture in this package could produce: the eight-byte PNG signature
// plus a marker. Byte equality against it is what says the object reached the wire unaltered.
var pimPNG = append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("extr-11-03")...)

// pimKeySpy is the injected reader seam. calls counts invocations, which is the only way to
// assert that a refusal was raised BEFORE the reader ran rather than merely that it returned
// the right code.
type pimKeySpy struct {
	gotJobID string
	gotPage  int
	calls    int
	key      string
	err      error
}

func (s *pimKeySpy) pageImageKey(_ context.Context, jobID string, page int) (string, error) {
	s.gotJobID, s.gotPage = jobID, page
	s.calls++
	return s.key, s.err
}

// pimBody counts closes rather than recording a bool: zero closes leaks the upstream connection
// and two is a double close, and both are defects a bool reports as "not one".
type pimBody struct {
	r      io.Reader
	closes int
}

func (b *pimBody) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *pimBody) Close() error {
	b.closes++
	return nil
}

// pimObjectSpy is the injected PageObject seam. A nil body is returned as a nil interface, not
// as a typed nil: the second is non-nil to a caller testing `body != nil` and would hide the
// guard this file exists to demand.
type pimObjectSpy struct {
	gotKey string
	calls  int
	body   *pimBody
	size   int64
	err    error
}

func (s *pimObjectSpy) get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	s.gotKey = key
	s.calls++
	if s.body == nil {
		return nil, s.size, s.err
	}
	return s.body, s.size, s.err
}

func pimObject(b []byte) *pimObjectSpy {
	return &pimObjectSpy{body: &pimBody{r: bytes.NewReader(b)}, size: int64(len(b))}
}

// pimServe drives the handler once. rawID and rawPage are what the mux would have bound to {id}
// and {n}; a nil identity means the request carries none.
func pimServe(t *testing.T, key *pimKeySpy, obj *pimObjectSpy, rawID, rawPage string, id *auth.Identity, log *slog.Logger) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	extraction.PageImageHandler(key.pageImageKey, obj.get, log)(w, pimRequest(rawID, rawPage, id))
	return w
}

// pimRequest builds the request the mux would have dispatched.
func pimRequest(rawID, rawPage string, id *auth.Identity) *http.Request {
	r := httptest.NewRequest(http.MethodGet,
		"/v1/extractions/"+url.PathEscape(rawID)+"/pages/"+url.PathEscape(rawPage), nil)
	r.SetPathValue(pimIDKey, rawID)
	r.SetPathValue(pimPageKey, rawPage)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	return r
}

// pimGet is the authenticated request every case but the 401 uses.
func pimGet(t *testing.T, key *pimKeySpy, obj *pimObjectSpy, rawID, rawPage string) *httptest.ResponseRecorder {
	t.Helper()
	return pimServe(t, key, obj, rawID, rawPage, &hndIdentity, nil)
}

// --- AC 4: the headers a browser needs to paint the bytes -----------------------------------

// The whole point of the subtask. document.DownloadHandler fixes application/octet-stream plus
// an attachment disposition (internal/document/handlers.go:70-72), which no browser paints, so
// the three headers below AND the absent fourth are the deliverable.
func TestExtractionPageImageHandler_ServesImagePngWithoutADisposition(t *testing.T) {
	key := &pimKeySpy{key: pimStorageKey}
	obj := pimObject(pimPNG)

	w := pimGet(t, key, obj, hndJobID, "2")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", w.Code, w.Body.String())
	}
	if got := w.Body.Bytes(); !bytes.Equal(got, pimPNG) {
		t.Errorf("body = %q, want the object's own bytes %q", got, pimPNG)
	}

	// The key comes off the RLS-visible row and reaches object storage verbatim: no
	// caller-supplied text may be spliced into it.
	if key.calls != 1 || key.gotJobID != hndJobID || key.gotPage != 2 {
		t.Errorf("the reader ran %d time(s) for job %q page %d, want 1 for %q page 2",
			key.calls, key.gotJobID, key.gotPage, hndJobID)
	}
	if obj.calls != 1 || obj.gotKey != pimStorageKey {
		t.Errorf("the object store ran %d time(s) for key %q, want 1 for the reader's own key %q",
			obj.calls, obj.gotKey, pimStorageKey)
	}

	h := w.Header()
	headers := []struct{ name, want string }{
		{"Content-Type", pimContentType},
		{"X-Content-Type-Options", pimNosniff},
		{"Cache-Control", pimCacheControl},
	}
	if len(headers) == 0 {
		t.Fatal("the header table is empty, so this test examined nothing")
	}
	for _, hdr := range headers {
		if got := h.Get(hdr.name); got != hdr.want {
			t.Errorf("%s = %q, want %q", hdr.name, got, hdr.want)
		}
	}

	// Absent, not blank. A browser refuses to paint an attachment whatever its value, and an
	// empty Content-Disposition is still a Content-Disposition on the wire.
	if v := h.Values("Content-Disposition"); len(v) != 0 {
		t.Errorf("Content-Disposition = %v, want no such header at all -- an attachment is never painted as an image", v)
	}

	if obj.body.closes != 1 {
		t.Errorf("the object body was closed %d time(s) on the 200 path, want exactly 1 (0 leaks the upstream connection, 2 is a double close)", obj.body.closes)
	}
}

// --- AC 5: identity first, then the two path values -----------------------------------------

// Both path values are malformed in the first two cases, so a handler that parsed first would
// answer 400 and tell an unauthenticated caller which parameter it read.
func TestExtractionPageImageHandler_UnauthenticatedIs401BeforeParsing(t *testing.T) {
	cases := []struct{ name, rawID, rawPage string }{
		{"bad page", hndJobID, "abc"},
		{"bad id", "nope", "1"},
		{"both bad", "nope", "-1"},
		{"both well formed", hndJobID, "1"},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := &pimKeySpy{key: pimStorageKey}
			obj := pimObject(pimPNG)
			log, buf := hndLogger()

			w := pimServe(t, key, obj, tc.rawID, tc.rawPage, nil, log)

			hndAssert(t, w, http.StatusUnauthorized, hndErrBody(t, hndMsgUnauthorized))
			if key.calls != 0 {
				t.Errorf("the reader ran %d time(s) on an unauthenticated request, want 0", key.calls)
			}
			if obj.calls != 0 {
				t.Errorf("the object store ran %d time(s) on an unauthenticated request, want 0", obj.calls)
			}
			if lines := hndLogLines(buf.String()); len(lines) != 0 {
				t.Errorf("a 401 refusal must not log as an error: %q", buf.String())
			}
		})
	}
}

// n is the 1-based page_number. Anything that is not a positive integer is refused before the
// reader runs, so a caller cannot probe the inventory with a malformed page.
func TestExtractionPageImageHandler_NonPositivePageIs400(t *testing.T) {
	if pimMsgBadPage == dtlMsgMalformed {
		t.Fatal("the page and id 400 messages are the same string; one of them names the wrong parameter")
	}

	cases := []struct{ name, rawPage string }{
		{"zero", "0"},
		{"negative", "-1"},
		{"text", "abc"},
		{"empty", ""},
		{"fractional", "1.5"},
		{"leading space", " 1"},
		{"beyond int64", "99999999999999999999"},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := &pimKeySpy{key: pimStorageKey}
			obj := pimObject(pimPNG)
			log, buf := hndLogger()

			w := pimServe(t, key, obj, hndJobID, tc.rawPage, &hndIdentity, log)

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, pimMsgBadPage))
			if key.calls != 0 {
				t.Errorf("the reader ran %d time(s) on page %q, want 0", key.calls, tc.rawPage)
			}
			if obj.calls != 0 {
				t.Errorf("the object store ran %d time(s) on page %q, want 0", obj.calls, tc.rawPage)
			}
			if lines := hndLogLines(buf.String()); len(lines) != 0 {
				t.Errorf("a 400 refusal must not log as an error: %q", buf.String())
			}
		})
	}
}

// AC 5's other half, which the story's Test Specs table omits. The message is the detail
// route's, asserted through its constant: both routes bind the same {id}, and one route
// naming the parameter differently would tell a caller the wrong field.
func TestExtractionPageImageHandler_MalformedJobIdIs400(t *testing.T) {
	cases := []struct{ name, rawID string }{
		{"plain text", "not-a-uuid"},
		{"empty", ""},
		{"too short", "3f1a2b3c"},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := &pimKeySpy{key: pimStorageKey}
			obj := pimObject(pimPNG)
			log, buf := hndLogger()

			w := pimServe(t, key, obj, tc.rawID, "1", &hndIdentity, log)

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, dtlMsgMalformed))
			if key.calls != 0 {
				t.Errorf("the reader ran %d time(s) on a malformed id, want 0", key.calls)
			}
			if obj.calls != 0 {
				t.Errorf("the object store ran %d time(s) on a malformed id, want 0", obj.calls)
			}
			if lines := hndLogLines(buf.String()); len(lines) != 0 {
				t.Errorf("a 400 refusal must not log as an error: %q", buf.String())
			}
		})
	}
}

// --- AC 6: an absent page and another tenant's job are one answer ---------------------------

// Not a 200 with an empty body: an empty 200 tells a caller the job exists and the page does
// not, which is exactly the distinction a refused read must not draw. The wrapped case is not
// decoration -- the reader may wrap ErrNotFound, and an err == comparison would silently stop
// matching.
func TestExtractionPageImageHandler_UnknownPageIs404(t *testing.T) {
	const wrapText = "extraction: page image key: "
	cases := []struct {
		name string
		err  error
	}{
		{"bare sentinel", extraction.ErrNotFound},
		{"wrapped sentinel", fmt.Errorf("%s%w", wrapText, extraction.ErrNotFound)},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := &pimKeySpy{err: tc.err}
			obj := pimObject(pimPNG)
			log, buf := hndLogger()

			w := pimServe(t, key, obj, hndJobID, "99", &hndIdentity, log)

			hndAssert(t, w, http.StatusNotFound, hndErrBody(t, dtlMsgNotFound))
			if key.calls != 1 {
				t.Errorf("the reader ran %d time(s), want 1", key.calls)
			}
			if obj.calls != 0 {
				t.Errorf("the object store ran %d time(s) after a refused key lookup, want 0 -- a refused read must reach no bucket", obj.calls)
			}
			if strings.Contains(w.Body.String(), wrapText) {
				t.Errorf("the 404 body carries the reader's own text: %q", w.Body.String())
			}
			if lines := hndLogLines(buf.String()); len(lines) != 0 {
				t.Errorf("a 404 refusal must not log as an error: %q", buf.String())
			}
		})
	}
}

// Every status code this route can answer with, not only the 200: EXTR-11-01's suite stayed
// green while the reader returned a populated struct after a transaction that never committed,
// and EXTR-11-02's stayed green while the audit could fail open. statusForErr is shared with
// two other routes, so the point here is that this handler routes THROUGH it rather than
// growing a fourth map.
func TestExtractionPageImageHandler_ReaderRefusalsUseTheSharedStatusMap(t *testing.T) {
	const internalText = "dial tcp 10.0.0.7:5432: connection refused"
	cases := []struct {
		name     string
		err      error
		status   int
		body     string
		logLines int
	}{
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, hndMsgUnauthorized, 0},
		{"suspended member", db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage, 0},
		{"unknown", errors.New(internalText), http.StatusInternalServerError, hndMsgInternal, 1},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := &pimKeySpy{err: tc.err}
			obj := pimObject(pimPNG)
			log, buf := hndLogger()

			w := pimServe(t, key, obj, hndJobID, "1", &hndIdentity, log)

			hndAssert(t, w, tc.status, hndErrBody(t, tc.body))
			if obj.calls != 0 {
				t.Errorf("the object store ran %d time(s) after a refused key lookup, want 0", obj.calls)
			}
			if strings.Contains(w.Body.String(), internalText) {
				t.Errorf("the refusal body leaks the internal error: %q", w.Body.String())
			}

			lines := hndLogLines(buf.String())
			if len(lines) != tc.logLines {
				t.Fatalf("the refusal emitted %d log line(s), want %d: %q", len(lines), tc.logLines, buf.String())
			}
			if tc.logLines == 0 {
				return
			}
			// The 500's other half: an operator alerts on the line, so asserting only that the
			// body is clean would pass on a handler that logged nothing at all.
			if !strings.Contains(lines[0], internalText) {
				t.Errorf("the 500 log line carries no internal error, so the operator has nothing to debug with: %q", lines[0])
			}
			if !strings.Contains(lines[0], `"level":"ERROR"`) {
				t.Errorf("the 500 did not log at level ERROR, which is what an operator alerts on: %q", lines[0])
			}
		})
	}
}

// --- AC 7: the object body is closed exactly once, on every path ----------------------------

// Three shapes the PageObject seam can hand back. The middle one is the trap the shipped
// newDocumentOpener already guards for (cmd/submission/main.go:195-199): a store that returns
// both a body and an error still owes a close, and a handler that registers its defer after the
// error branch leaks one connection per failed page.
func TestExtractionPageImageHandler_ClosesBodyWhenTheStoreErrors(t *testing.T) {
	cases := []struct {
		name       string
		withBody   bool
		err        error
		wantStatus int
		wantCloses int
	}{
		{"body, no error", true, nil, http.StatusOK, 1},
		{"body AND error", true, errors.New("s3: 503 slow down"), http.StatusInternalServerError, 1},
		{"error, no body", false, errors.New("s3: 503 slow down"), http.StatusInternalServerError, 0},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := &pimKeySpy{key: pimStorageKey}
			obj := &pimObjectSpy{err: tc.err}
			if tc.withBody {
				obj.body = &pimBody{r: bytes.NewReader(pimPNG)}
				obj.size = int64(len(pimPNG))
			}
			log, _ := hndLogger()

			w := pimServe(t, key, obj, hndJobID, "2", &hndIdentity, log)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body=%q)", w.Code, tc.wantStatus, w.Body.String())
			}
			if obj.calls != 1 {
				t.Errorf("the object store ran %d time(s), want 1", obj.calls)
			}
			if tc.withBody && obj.body.closes != tc.wantCloses {
				t.Errorf("the object body was closed %d time(s), want exactly %d -- 0 leaks the upstream connection and 2 is a double close",
					obj.body.closes, tc.wantCloses)
			}
			if tc.wantStatus == http.StatusOK {
				return
			}
			// A failed fetch must not ship the object's bytes under a 500, and must not ship a
			// 500 envelope with the bytes appended either.
			if got := w.Body.String(); got != hndErrBody(t, hndMsgInternal) {
				t.Errorf("body = %q, want exactly the 500 envelope %q", got, hndErrBody(t, hndMsgInternal))
			}
		})
	}
}

// --- AC 9: the handler streams ---------------------------------------------------------------

// pimStreamWriter is a ResponseWriter that announces its first Write. httptest.ResponseRecorder
// cannot serve here: reading its buffer while the handler goroutine writes to it is a data
// race, and the whole oracle is what the writer has received while the reader is still blocked.
type pimStreamWriter struct {
	hdr   http.Header
	mu    sync.Mutex
	buf   bytes.Buffer
	code  int
	first chan struct{}
	once  sync.Once
}

func newPimStreamWriter() *pimStreamWriter {
	return &pimStreamWriter{hdr: http.Header{}, first: make(chan struct{})}
}

func (w *pimStreamWriter) Header() http.Header { return w.hdr }

func (w *pimStreamWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.code == 0 {
		w.code = code
	}
}

func (w *pimStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.code == 0 {
		w.code = http.StatusOK
	}
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	w.once.Do(func() { close(w.first) })
	return n, err
}

func (w *pimStreamWriter) status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.code
}

func (w *pimStreamWriter) bytesSoFar() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// pimBlockingBody hands back its first chunk immediately and then blocks until release is
// closed. A handler that buffers the object never reaches the ResponseWriter at all, so the
// arrival of that first chunk IS the streaming oracle. Every field is touched by the handler
// goroutine only; the test goroutine reads closes after the handler has returned.
type pimBlockingBody struct {
	first, rest []byte
	sentFirst   bool
	sentRest    bool
	release     chan struct{}
	closes      int
}

func (b *pimBlockingBody) Read(p []byte) (int, error) {
	if !b.sentFirst {
		b.sentFirst = true
		return copy(p, b.first), nil
	}
	<-b.release
	if !b.sentRest {
		b.sentRest = true
		return copy(p, b.rest), nil
	}
	return 0, io.EOF
}

func (b *pimBlockingBody) Close() error {
	b.closes++
	return nil
}

// The behavioural half of AC 9, and the one that can actually fail: a page is ~113 KiB and a
// document may hold 800, so an io.ReadAll of the object holds the whole render in memory and
// answers nothing until the last byte arrives.
func TestExtractionPageImageHandler_StreamsRatherThanBuffers(t *testing.T) {
	const wait = 5 * time.Second

	body := &pimBlockingBody{
		first:   pimPNG,
		rest:    []byte("-tail"),
		release: make(chan struct{}),
	}
	obj := &pimObjectSpy{body: nil, size: int64(len(pimPNG) + 5)}
	// The spy's own body field is a *pimBody, so this case hands the blocking reader through a
	// closure of its own rather than widening the shared spy.
	get := func(_ context.Context, key string) (io.ReadCloser, int64, error) {
		obj.gotKey = key
		obj.calls++
		return body, obj.size, nil
	}
	key := &pimKeySpy{key: pimStorageKey}
	w := newPimStreamWriter()

	done := make(chan struct{})
	go func() {
		defer close(done)
		extraction.PageImageHandler(key.pageImageKey, get, nil)(w, pimRequest(hndJobID, "2", &hndIdentity))
	}()

	select {
	case <-w.first:
	case <-done:
		t.Fatalf("the handler returned before the object reader was released; it wrote %q and cannot have streamed", w.bytesSoFar())
	case <-time.After(wait):
		close(body.release)
		t.Fatalf("no byte reached the ResponseWriter within %s while the object reader was still blocked -- the handler buffered the page instead of streaming it", wait)
	}

	// The first chunk is on the wire before the object finished arriving. That is the claim.
	if got := w.bytesSoFar(); !bytes.Equal(got, pimPNG) {
		t.Errorf("the writer held %q before the reader was released, want the first chunk %q", got, pimPNG)
	}
	if w.status() != http.StatusOK {
		t.Errorf("status = %d at first byte, want 200", w.status())
	}

	close(body.release)
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatalf("the handler did not return within %s of the object reader being released", wait)
	}

	if got, want := w.bytesSoFar(), append(append([]byte(nil), pimPNG...), []byte("-tail")...); !bytes.Equal(got, want) {
		t.Errorf("body = %q, want the whole object %q", got, want)
	}
	if body.closes != 1 {
		t.Errorf("the streamed body was closed %d time(s), want exactly 1", body.closes)
	}
	if obj.calls != 1 || obj.gotKey != pimStorageKey {
		t.Errorf("the object store ran %d time(s) for key %q, want 1 for %q", obj.calls, obj.gotKey, pimStorageKey)
	}
}

// pimNonTestFiles returns this package's non-test .go file names, floored: a walk over a path
// that stopped resolving reports a clean package too.
func pimNonTestFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the internal/extraction package directory: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	if len(out) < pimMinPackageFiles {
		t.Fatalf("the walk found %d non-test .go file(s) in internal/extraction, want at least %d (26 measured at 201d7169) -- a scan over a path that stopped resolving reports all-clear",
			len(out), pimMinPackageFiles)
	}
	sort.Strings(out)
	return out
}

// pimReadAllCalls returns the position of every io.ReadAll call under n.
func pimReadAllCalls(fset *token.FileSet, n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ReadAll" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "io" {
			return true
		}
		out = append(out, fset.Position(call.Pos()).String())
		return true
	})
	return out
}

// The cheap half of AC 9, and an absence proof, so it carries two needles. The planted one is
// the PageImageHandler declaration itself: a scan over a renamed or moved file must fail rather
// than report a clean handler. The population one is the io.ReadAll the package already carries
// in docling.go, which proves the matcher fires at all.
func TestExtractionPageImageHandler_NamesNoReadAll(t *testing.T) {
	fset := token.NewFileSet()

	var (
		decl      *ast.FuncDecl
		declFile  string
		everyHit  []string
		fileCount int
	)
	for _, name := range pimNonTestFiles(t) {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v -- a file the scan cannot read is a file it reports clean on", name, err)
		}
		fileCount++
		everyHit = append(everyHit, pimReadAllCalls(fset, f)...)
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != pimHandlerName || fn.Body == nil {
				continue
			}
			if decl != nil {
				t.Fatalf("%s and %s both declare func %s(...); the scan below cannot say which one is served", declFile, name, pimHandlerName)
			}
			decl, declFile = fn, name
		}
	}
	if fileCount < pimMinPackageFiles {
		t.Fatalf("parsed %d file(s), want at least %d", fileCount, pimMinPackageFiles)
	}
	if decl == nil {
		t.Fatalf("no non-test file in internal/extraction declares func %s(...); the io.ReadAll absence below would pass over a renamed or moved file", pimHandlerName)
	}
	if len(everyHit) == 0 {
		t.Fatalf("the matcher found no io.ReadAll anywhere in internal/extraction, though docling.go carries one -- a matcher that finds nothing reports a streaming handler too")
	}

	if hits := pimReadAllCalls(fset, decl); len(hits) != 0 {
		t.Errorf("%s's %s calls io.ReadAll at %v -- a rendered page is ~113 KiB and a document may hold 800, so the handler must stream rather than buffer one",
			declFile, pimHandlerName, hits)
	}
}
