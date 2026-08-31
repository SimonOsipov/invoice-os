// handlers_pageimage_qa_test.go: the five PageImageHandler claims the AC suite left with no
// oracle -- Content-Length, the non-positive size that guards it, a nil body with a nil error, a
// copy that fails after the 200, and the canonical uuid the route forwards.
//
// The truncation case runs over a REAL server: httptest.ResponseRecorder applies no transfer
// encoding and cannot see which framing the response went out under.
//
// Helpers use a pqa* prefix; hnd dtl rvd rda up rd st wk dc fx mx px pr ps pt pd pb pe rx de rp
// pim pgd are taken.
package extraction_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// pqaDeclared is a size no fixture body reaches, so a response carrying it has declared more
// than it can deliver.
const pqaDeclared = 4096

// pqaTruncating yields one chunk and then fails: an object store dropping a connection mid-page.
// closes is mutex-guarded because the handler goroutine writes it while the client still reads.
type pqaTruncating struct {
	chunk []byte
	sent  bool

	mu     sync.Mutex
	closes int
}

func (b *pqaTruncating) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.chunk), nil
	}
	return 0, errors.New("s3: connection reset mid-object")
}

func (b *pqaTruncating) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closes++
	return nil
}

func (b *pqaTruncating) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes
}

// pqaGet adapts a body and a declared size to the PageObject seam.
func pqaGet(body io.ReadCloser, size int64) extraction.PageObject {
	return func(context.Context, string) (io.ReadCloser, int64, error) { return body, size, nil }
}

// pqaServe runs h behind a real server on the route's own mux pattern, so {id} and {n} are bound
// by net/http. The returned channel closes when the handler returns: reading a spy the handler
// still owns is a race.
func pqaServe(t *testing.T, h http.HandlerFunc) (*httptest.Server, <-chan struct{}) {
	t.Helper()

	done := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/extractions/{id}/pages/{n}", func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		h(w, r)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), hndIdentity)))
	}))
	t.Cleanup(srv.Close)
	return srv, done
}

// pqaFetch returns the response, the bytes that arrived, and the error the client hit reading
// them. That error is the oracle.
func pqaFetch(t *testing.T, srv *httptest.Server) (*http.Response, []byte, error) {
	t.Helper()

	resp, err := srv.Client().Get(srv.URL + "/v1/extractions/" + hndJobID + "/pages/2")
	if err != nil {
		t.Fatalf("GET the page: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	got, readErr := io.ReadAll(resp.Body)
	return resp, got, readErr
}

// Content-Length picks the framing. Declared, net/http drops the connection on a short write and
// the client sees an unfinished response; undeclared, the truncated body is framed as the whole
// page -- AUDIT-08's truncated-ZIP escape, a successful 200 carrying a corrupt file. The control
// arm is what makes the first one falsifiable.
func TestExtractionPageImageHandler_ATruncatedObjectDoesNotArriveAsASuccessfulShortPng(t *testing.T) {
	t.Run("the shipped handler declares the length and the client sees the failure", func(t *testing.T) {
		body := &pqaTruncating{chunk: pimPNG}
		key := &pimKeySpy{key: pimStorageKey}
		log, buf := hndLogger()

		srv, done := pqaServe(t, extraction.PageImageHandler(key.pageImageKey, pqaGet(body, pqaDeclared), log))
		resp, got, readErr := pqaFetch(t, srv)

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the handler had not returned 5s after the client finished reading")
		}

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 -- the header is already on the wire when the object fails", resp.StatusCode)
		}
		if resp.ContentLength != pqaDeclared {
			t.Errorf("Content-Length = %d, want %d -- an undeclared length lets net/http frame the truncated body as the whole page",
				resp.ContentLength, pqaDeclared)
		}
		if readErr == nil {
			t.Errorf("the client read %d byte(s) of a %d-byte page with NO error: a mid-stream object failure arrived as a successful short PNG",
				len(got), pqaDeclared)
		}
		if int64(len(got)) >= pqaDeclared {
			t.Errorf("the client received %d byte(s), want fewer than the declared %d", len(got), pqaDeclared)
		}
		if n := body.closeCount(); n != 1 {
			t.Errorf("the object body was closed %d time(s) on the copy-failure path, want exactly 1", n)
		}

		// The operator's half: a page that failed mid-stream leaves a line naming the key.
		lines := hndLogLines(buf.String())
		if len(lines) != 1 {
			t.Fatalf("the copy failure emitted %d log line(s), want 1: %q", len(lines), buf.String())
		}
		if !strings.Contains(lines[0], pimStorageKey) {
			t.Errorf("the log line names no key, so an operator cannot find the object: %q", lines[0])
		}
	})

	t.Run("the same failure with no Content-Length is a silent success", func(t *testing.T) {
		body := &pqaTruncating{chunk: pimPNG}

		// The shipped handler minus the one header. Everything else is copied from it, so the
		// difference the arm above measures is that header and nothing else.
		srv, done := pqaServe(t, func(w http.ResponseWriter, _ *http.Request) {
			defer func() { _ = body.Close() }()
			h := w.Header()
			h.Set("Content-Type", "image/png")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Cache-Control", "private, no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, body)
		})
		resp, got, readErr := pqaFetch(t, srv)

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the control handler had not returned 5s after the client finished reading")
		}

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("control status = %d, want 200", resp.StatusCode)
		}
		if readErr != nil {
			t.Fatalf("the control arm surfaced the truncation as %v; then the arm above is not measuring Content-Length at all", readErr)
		}
		if !bytes.Equal(got, pimPNG) {
			t.Fatalf("the control arm delivered %q, want the truncated prefix %q", got, pimPNG)
		}
		if resp.ContentLength == pqaDeclared {
			t.Errorf("the control response still declared %d bytes; the two arms differ in something other than the header", pqaDeclared)
		}
	})
}

// The value is the object's own size, not whatever net/http would have back-filled.
func TestExtractionPageImageHandler_DeclaresTheObjectsOwnLength(t *testing.T) {
	key := &pimKeySpy{key: pimStorageKey}
	obj := pimObject(pimPNG)

	w := pimGet(t, key, obj, hndJobID, "2")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", w.Code, w.Body.String())
	}
	want := strconv.Itoa(len(pimPNG))
	if got := w.Header().Get("Content-Length"); got != want {
		t.Errorf("Content-Length = %q, want %q -- an undeclared length frames a truncated object as the whole page (TestExtractionPageImageHandler_ATruncatedObjectDoesNotArriveAsASuccessfulShortPng)",
			got, want)
	}
	if n := w.Body.Len(); strconv.Itoa(n) != want {
		t.Errorf("the handler wrote %d byte(s) under a Content-Length of %s", n, want)
	}
}

// The guard that lets the header be unconditional: a store reporting no length is refused before
// the 200, rather than served under a length of 0 -- a well-formed, empty, successful PNG.
func TestExtractionPageImageHandler_NonPositiveSizeIs500(t *testing.T) {
	cases := []struct {
		name string
		size int64
	}{
		{"zero", 0},
		{"negative", -1},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := &pimKeySpy{key: pimStorageKey}
			obj := &pimObjectSpy{body: &pimBody{r: bytes.NewReader(pimPNG)}, size: tc.size}
			log, buf := hndLogger()

			w := pimServe(t, key, obj, hndJobID, "2", &hndIdentity, log)

			hndAssert(t, w, http.StatusInternalServerError, hndErrBody(t, hndMsgInternal))
			if bytes.Contains(w.Body.Bytes(), pimPNG) {
				t.Errorf("the 500 body carries the object's bytes: %q", w.Body.String())
			}
			if got := w.Header().Get("Content-Type"); got == pimContentType {
				t.Errorf("Content-Type = %q on a refusal; a 500 envelope is not an image", got)
			}
			if obj.body.closes != 1 {
				t.Errorf("the object body was closed %d time(s), want exactly 1", obj.body.closes)
			}
			lines := hndLogLines(buf.String())
			if len(lines) != 1 {
				t.Fatalf("the refusal emitted %d log line(s), want 1: %q", len(lines), buf.String())
			}
			if !strings.Contains(lines[0], pimStorageKey) || !strings.Contains(lines[0], `"level":"ERROR"`) {
				t.Errorf("the 500 line must name the key at level ERROR: %q", lines[0])
			}
		})
	}
}

// No body and no error: the one seam shape that is not an error. newPageObjectReader refuses it
// at its own end, but PageObject is a func type any caller may supply and io.Copy over a nil
// ReadCloser panics, which reaches the client as a dropped connection with no log line.
func TestExtractionPageImageHandler_NoBodyAndNoErrorIs500(t *testing.T) {
	key := &pimKeySpy{key: pimStorageKey}
	obj := &pimObjectSpy{body: nil, size: int64(len(pimPNG))}
	log, buf := hndLogger()

	w := pimServe(t, key, obj, hndJobID, "2", &hndIdentity, log)

	hndAssert(t, w, http.StatusInternalServerError, hndErrBody(t, hndMsgInternal))
	if obj.calls != 1 {
		t.Errorf("the object store ran %d time(s), want 1", obj.calls)
	}
	lines := hndLogLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("the refusal emitted %d log line(s), want 1: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], pimStorageKey) {
		t.Errorf("the 500 line names no key: %q", lines[0])
	}
}

// AC 7's unnamed path: a failure AFTER the 200 is written. The three shapes
// TestExtractionPageImageHandler_ClosesBodyWhenTheStoreErrors covers all resolve before the copy
// starts, so a close added inside io.Copy's error branch is a second close none of them sees.
func TestExtractionPageImageHandler_ClosesOnceWhenTheCopyFails(t *testing.T) {
	body := &pqaTruncating{chunk: pimPNG}
	key := &pimKeySpy{key: pimStorageKey}
	log, buf := hndLogger()

	w := httptest.NewRecorder()
	extraction.PageImageHandler(key.pageImageKey, pqaGet(body, pqaDeclared), log)(
		w, pimRequest(hndJobID, "2", &hndIdentity))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- the header goes out before the copy begins", w.Code)
	}
	if n := body.closeCount(); n != 1 {
		t.Errorf("the object body was closed %d time(s) after a failed copy, want exactly 1 -- 0 leaks the upstream connection and 2 is a double close", n)
	}
	if got := w.Body.Bytes(); !bytes.Equal(got, pimPNG) {
		t.Errorf("the recorder holds %q, want the one chunk that arrived %q", got, pimPNG)
	}
	if lines := hndLogLines(buf.String()); len(lines) != 1 {
		t.Errorf("a failed copy emitted %d log line(s), want 1: %q", len(lines), buf.String())
	}
}

// "urn:uuid:<id>" is the one spelling uuid.Parse accepts and Postgres refuses with 22P02. The
// detail route asserts it (TestExtractionDetailHandler_UrnPrefixedUuidReachesTheReaderCanonicalised);
// this route carried the same claim in a comment only.
func TestExtractionPageImageHandler_UrnPrefixedUuidReachesTheReaderCanonicalised(t *testing.T) {
	want := uuid.MustParse(hndJobID).String()
	if want != hndJobID {
		t.Fatalf("the fixture id %q is not its own canonical form %q; this case asserts the wrong string", hndJobID, want)
	}
	value := "urn:uuid:" + hndJobID
	if value == want {
		t.Fatal("the urn value equals the canonical form, so this case proves nothing")
	}

	key := &pimKeySpy{key: pimStorageKey}
	obj := pimObject(pimPNG)

	w := pimGet(t, key, obj, value, "2")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", w.Code, w.Body.String())
	}
	if key.calls != 1 {
		t.Fatalf("the reader ran %d time(s), want 1", key.calls)
	}
	if key.gotJobID != want {
		t.Errorf("the reader received id %q, want the canonical %q; Postgres refuses anything else and the 500 would name a key that was never looked up", key.gotJobID, want)
	}
}
