// docadapter_test.go: the adapters driven over scripted document and object-store fakes.
package docadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// wtCloser counts closes so a leak (0) and a double close (2) are both visible.
type wtCloser struct {
	io.Reader
	closes int
}

func (c *wtCloser) Close() error { c.closes++; return nil }

// wtErrReader fails on the first Read, the shape a truncated object stream has.
type wtErrReader struct{ err error }

func (r wtErrReader) Read([]byte) (int, error) { return 0, r.err }

// wtScript is one scripted document.Service.Open outcome plus what the closure passed it.
type wtScript struct {
	doc document.Document
	obj document.Object
	err error

	calls int
	ctx   context.Context
	id    string
	rng   string
}

func (s *wtScript) open(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error) {
	s.calls++
	s.ctx, s.id, s.rng = ctx, id, rangeHeader
	return s.doc, s.obj, s.err
}

// TestOpener_CapsAtDocumentSizeCeiling: AC #4. The cap bounds the READ, never
// obj.Size -- Object.Size is what the store reports, and on a 206 it is the range length.
func TestOpener_CapsAtDocumentSizeCeiling(t *testing.T) {
	// documents.size_bytes CHECKs <= this, so a document of exactly this length is legal.
	const ceiling = 15728640
	if MaxDocumentBytes != ceiling {
		t.Fatalf("MaxDocumentBytes = %d, want %d (the documents.size_bytes CHECK)", MaxDocumentBytes, ceiling)
	}

	t.Run("at the ceiling reads whole", func(t *testing.T) {
		body := &wtCloser{Reader: bytes.NewReader(make([]byte, ceiling))}
		s := &wtScript{obj: document.Object{Body: body, Size: ceiling}}
		doc, err := Opener(s.open)(context.Background(), "doc")
		if err != nil {
			t.Fatalf("a document of exactly %d bytes is legal and must read whole, got %v", ceiling, err)
		}
		if len(doc.Bytes) != ceiling {
			t.Errorf("read %d bytes, want %d", len(doc.Bytes), ceiling)
		}
		if body.closes != 1 {
			t.Errorf("body closed %d time(s), want exactly 1", body.closes)
		}
	})

	t.Run("one byte over is refused even when Size lies", func(t *testing.T) {
		body := &wtCloser{Reader: bytes.NewReader(make([]byte, ceiling+1))}
		// Size deliberately understates the stream: a cap on obj.Size alone would let a lying
		// or ranged store push unbounded bytes into memory.
		s := &wtScript{obj: document.Object{Body: body, Size: 3}}
		doc, err := Opener(s.open)(context.Background(), "doc")
		if err == nil {
			t.Fatalf("a %d-byte body was accepted, and %d bytes came back presented as success", ceiling+1, len(doc.Bytes))
		}
		if len(doc.Bytes) != 0 {
			t.Errorf("the refused document still carries %d bytes; the error path must return the zero Document", len(doc.Bytes))
		}
		if body.closes != 1 {
			t.Errorf("body closed %d time(s), want exactly 1", body.closes)
		}
	})
}

// TestOpener_ClosesBodyOnReadError: AC #4. Exactly once -- a defer plus an explicit
// Close in the error branch is a double close, which only the count catches.
func TestOpener_ClosesBodyOnReadError(t *testing.T) {
	boom := errors.New("boom")
	body := &wtCloser{Reader: wtErrReader{err: boom}}
	s := &wtScript{obj: document.Object{Body: body, Size: 3}}

	_, err := Opener(s.open)(context.Background(), "doc")
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the read error", err)
	}
	if body.closes != 1 {
		t.Errorf("body closed %d time(s), want exactly 1", body.closes)
	}
}

// TestOpener_ClosesBodyWhenOpenErrors: the leak two callers in this repo still have
// -- registering the defer after the error return drops a body handed back WITH an error.
func TestOpener_ClosesBodyWhenOpenErrors(t *testing.T) {
	boom := errors.New("open failed")
	body := &wtCloser{Reader: bytes.NewReader([]byte("abc"))}
	s := &wtScript{obj: document.Object{Body: body, Size: 3}, err: boom}

	_, err := Opener(s.open)(context.Background(), "doc")
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the open error", err)
	}
	if body.closes != 1 {
		t.Errorf("body closed %d time(s) after an open that returned BOTH a body and an error, want exactly 1", body.closes)
	}
}

// TestOpener_RejectsANilBody: io.ReadAll over a nil ReadCloser panics, and an empty
// document would otherwise be extracted as if it were the real one.
func TestOpener_RejectsANilBody(t *testing.T) {
	s := &wtScript{obj: document.Object{Body: nil, Size: 0}}

	doc, err := Opener(s.open)(context.Background(), "doc-42")
	if err == nil {
		t.Fatalf("a nil body was accepted and yielded %+v", doc)
	}
	if !strings.Contains(err.Error(), "doc-42") {
		t.Errorf("err = %q, want the document id in it", err)
	}
	if len(doc.Bytes) != 0 || doc.ContentType != "" {
		t.Errorf("the refused document is %+v, want the zero Document", doc)
	}
}

// TestOpener_PassesEmptyRangeHeader: a stray range would extract a fragment and
// report success.
func TestOpener_PassesEmptyRangeHeader(t *testing.T) {
	body := &wtCloser{Reader: bytes.NewReader([]byte("abc"))}
	s := &wtScript{obj: document.Object{Body: body, Size: 3}}

	if _, err := Opener(s.open)(context.Background(), "doc-7"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("the document service was opened %d time(s), want 1", s.calls)
	}
	if s.rng != "" {
		t.Errorf("rangeHeader = %q, want the empty string", s.rng)
	}
	if s.id != "doc-7" {
		t.Errorf("document id = %q, want %q", s.id, "doc-7")
	}
}

// TestOpener_CarriesDeclaredContentType: nil means unknown, not a missing field.
func TestOpener_CarriesDeclaredContentType(t *testing.T) {
	pdf := "application/pdf"
	for _, tc := range []struct {
		name     string
		declared *string
		want     string
	}{
		{"nil becomes empty", nil, ""},
		{"set is carried verbatim", &pdf, pdf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &wtCloser{Reader: bytes.NewReader([]byte("abc"))}
			s := &wtScript{
				doc: document.Document{DeclaredContentType: tc.declared},
				obj: document.Object{Body: body, Size: 3},
			}
			doc, err := Opener(s.open)(context.Background(), "doc")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if doc.ContentType != tc.want {
				t.Errorf("ContentType = %q, want %q", doc.ContentType, tc.want)
			}
			if string(doc.Bytes) != "abc" {
				t.Errorf("Bytes = %q, want %q", doc.Bytes, "abc")
			}
		})
	}
}

// TestOpener_ForwardsContextVerbatim: the worker has already put the job tenant on
// the ctx, and document.Service.Open resolves its tenant from that identity. A closure that
// wrapped ctx in a second identity would read another tenant rows, silently.
func TestOpener_ForwardsContextVerbatim(t *testing.T) {
	type ctxKey struct{}
	body := &wtCloser{Reader: bytes.NewReader([]byte("abc"))}
	s := &wtScript{obj: document.Object{Body: body, Size: 3}}

	ctx := context.WithValue(context.Background(), ctxKey{}, "tenant-marker")
	if _, err := Opener(s.open)(ctx, "doc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("the document service was opened %d time(s), want 1", s.calls)
	}
	if s.ctx == nil {
		t.Fatal("the document service got a nil context")
	}
	if got := s.ctx.Value(ctxKey{}); got != "tenant-marker" {
		t.Errorf("the caller context value did not survive: got %v", got)
	}
	if s.ctx != ctx {
		t.Errorf("the context was wrapped before it reached the document service (%T); the job tenant identity is on the caller context, and wrapping it overwrites the tenant RLS scopes the row lookup by", s.ctx)
	}
}

// psObjects is PageSink's only collaborator: a fake document.ObjectStore that records the
// one Put and, crucially, the reader's position when it arrived.
type psObjects struct {
	calls  int
	key    string
	size   int64
	offset int64
	body   []byte
	err    error
}

var _ document.ObjectStore = (*psObjects)(nil)

func (o *psObjects) Put(_ context.Context, key string, body io.ReadSeeker, size int64) error {
	o.calls++
	o.key, o.size = key, size

	pos, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	o.offset = pos

	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	o.body = b
	return o.err
}

func (o *psObjects) Get(context.Context, string, string) (document.Object, error) {
	return document.Object{}, errors.New("PageSink never reads an object")
}

// TestPageSink_PutsToTheDocumentStore: the offset assertion is the load-bearing one.
// document.Service.Store rewinds before its own Put for the reason recorded at
// internal/document/service.go:45 -- Put transmits from the reader's CURRENT offset, so an
// already-read reader sends zero bytes under a declared length and the object lands empty.
func TestPageSink_PutsToTheDocumentStore(t *testing.T) {
	objects := &psObjects{}
	sink := PageSink(objects)

	const key = "tenants/3f2a1c88-0b6d-4e19-9f31-5c7a2d840e11/pages/" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/v1/p0001.png"
	body := []byte("\x89PNG\r\n\x1a\nextr-09 page one bytes")

	if err := sink(context.Background(), key, body); err != nil {
		t.Fatalf("PageSink: %v", err)
	}
	if objects.calls != 1 {
		t.Fatalf("the object store saw %d Put(s), want 1; every assertion below would read a zero value", objects.calls)
	}
	if objects.key != key {
		t.Errorf("Put was given key %q, want %q", objects.key, key)
	}
	if objects.size != int64(len(body)) {
		t.Errorf("Put was given size %d, want %d -- the declared length is what the store transmits", objects.size, len(body))
	}
	if objects.offset != 0 {
		t.Errorf("Put was handed a reader at offset %d, want 0: Put transmits from the reader's current offset, so a pre-read reader sends zero bytes under a declared length", objects.offset)
	}
	if !bytes.Equal(objects.body, body) {
		t.Errorf("Put read back %d byte(s), want the %d handed to the sink", len(objects.body), len(body))
	}

	objects.err = errors.New("the object store refused the PUT")
	if err := sink(context.Background(), key, body); !errors.Is(err, objects.err) {
		t.Errorf("PageSink returned %v over a failing store, want its error: a swallowed PUT failure would commit a page row naming nothing", err)
	}
}

const (
	// A key in the shape extraction_page_images_key_tenant_scoped admits. The adapter must hand
	// it to the store byte for byte.
	pgiKey = "tenants/11111111-1111-1111-1111-111111111111/pages/" +
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc/v1/p0002.png"
)

var pgiPNG = []byte("\x89PNG\r\n\x1a\nextr-11-03")

// pgiBody counts closes. Zero leaks the upstream connection and two is a double close, so the
// count is kept rather than a bool.
type pgiBody struct {
	r      io.Reader
	closes int
}

func (b *pgiBody) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *pgiBody) Close() error {
	b.closes++
	return nil
}

// pgiStore is a document.ObjectStore that records what Get was asked for. Put fails loudly: the
// page route never writes, and a Put reaching this fake would be a seam pointed the wrong way.
type pgiStore struct {
	calls    int
	gotKey   string
	gotRange string
	obj      document.Object
	err      error
}

func (s *pgiStore) Put(_ context.Context, _ string, _ io.ReadSeeker, _ int64) error {
	return errors.New("Put is not part of the page-image read seam")
}

func (s *pgiStore) Get(_ context.Context, key, rangeHeader string) (document.Object, error) {
	s.calls++
	s.gotKey, s.gotRange = key, rangeHeader
	return s.obj, s.err
}

// The adapter is the only thing standing between the reader's key and the bucket, so what it
// forwards IS the security claim: the exact key, and no range header. A range would let a
// caller-shaped value reach S3 through a route that accepts none.
func TestPageObjectReader_ReadsTheKeyItIsGiven(t *testing.T) {
	body := &pgiBody{r: strings.NewReader(string(pgiPNG))}
	store := &pgiStore{obj: document.Object{Body: body, Size: int64(len(pgiPNG))}}

	rc, size, err := PageObjectReader(store)(context.Background(), pgiKey)
	if err != nil {
		t.Fatalf("PageObjectReader(...)(ctx, %q): %v", pgiKey, err)
	}
	if rc == nil {
		t.Fatal("the adapter returned a nil reader with a nil error; the handler would stream nothing")
	}

	if store.calls != 1 {
		t.Errorf("ObjectStore.Get ran %d time(s), want 1", store.calls)
	}
	if store.gotKey != pgiKey {
		t.Errorf("ObjectStore.Get was asked for %q, want the reader's own key %q verbatim", store.gotKey, pgiKey)
	}
	if store.gotRange != "" {
		t.Errorf("ObjectStore.Get was handed range header %q, want none -- this route serves whole pages", store.gotRange)
	}
	if size != int64(len(pgiPNG)) {
		t.Errorf("size = %d, want the object's own %d", size, len(pgiPNG))
	}

	// Unread and unclosed on the way out: the handler streams the body and closes it. An
	// adapter that read it here would buffer a page per request.
	if body.closes != 0 {
		t.Errorf("the adapter closed the body %d time(s) before handing it over, want 0 -- the handler streams it", body.closes)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read the returned body: %v", err)
	}
	if string(got) != string(pgiPNG) {
		t.Errorf("the returned body read back as %q, want the object's own bytes %q", got, pgiPNG)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("close the returned body: %v", err)
	}
	if body.closes != 1 {
		t.Errorf("closing the returned reader closed the underlying body %d time(s), want exactly 1", body.closes)
	}
}

// AC 7 at the ObjectStore seam. Opener already guards the same two shapes
// : a Get that hands back both a body and an error still owes a
// close, and a Get that hands back neither must be an error rather than a nil reader the
// handler streams.
func TestPageObjectReader_ClosesBodyWhenGetErrors(t *testing.T) {
	t.Run("body AND error", func(t *testing.T) {
		body := &pgiBody{r: strings.NewReader(string(pgiPNG))}
		store := &pgiStore{
			obj: document.Object{Body: body, Size: int64(len(pgiPNG))},
			err: errors.New("s3: 503 slow down"),
		}

		rc, _, err := PageObjectReader(store)(context.Background(), pgiKey)

		if err == nil {
			t.Fatal("the adapter returned a nil error though the store failed")
		}
		if rc != nil {
			t.Errorf("the adapter returned a %T alongside its error, want nil -- the handler would stream a body it does not own", rc)
		}
		if body.closes != 1 {
			t.Errorf("the body was closed %d time(s) on the error path, want exactly 1 -- 0 leaks the upstream connection and 2 is a double close", body.closes)
		}
	})

	t.Run("no body, no error", func(t *testing.T) {
		store := &pgiStore{obj: document.Object{Size: 0}}

		rc, _, err := PageObjectReader(store)(context.Background(), pgiKey)

		if err == nil {
			t.Fatal("a Get that produced no body and no error was passed through as success; the handler would stream a nil reader")
		}
		if rc != nil {
			t.Errorf("the adapter returned a %T alongside its error, want nil", rc)
		}
		if !strings.Contains(err.Error(), pgiKey) {
			t.Errorf("err = %q, want it to name the key %q an operator has to look up", err.Error(), pgiKey)
		}
	})
}
