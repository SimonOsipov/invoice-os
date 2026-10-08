// page_image_route_test.go: the cmd/submission half of EXTR-11-03 -- the object-store adapter
// the page route streams through. Nothing here serves a mux or opens a database (main() is not
// unit-testable, main_test.go:1-4): the adapter is driven over a fake ObjectStore. The route's
// registration is asserted in main_test.go's shipped ast.Inspect switch.
//
// Helpers use a pgi* prefix; dr ea wt ds are taken.
package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

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
func TestNewPageObjectReader_ReadsTheKeyItIsGiven(t *testing.T) {
	body := &pgiBody{r: strings.NewReader(string(pgiPNG))}
	store := &pgiStore{obj: document.Object{Body: body, Size: int64(len(pgiPNG))}}

	rc, size, err := newPageObjectReader(store)(context.Background(), pgiKey)
	if err != nil {
		t.Fatalf("newPageObjectReader(...)(ctx, %q): %v", pgiKey, err)
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

// AC 7 at the ObjectStore seam. newDocumentOpener already guards the same two shapes
// (cmd/submission/main.go:195-205): a Get that hands back both a body and an error still owes a
// close, and a Get that hands back neither must be an error rather than a nil reader the
// handler streams.
func TestNewPageObjectReader_ClosesBodyWhenGetErrors(t *testing.T) {
	t.Run("body AND error", func(t *testing.T) {
		body := &pgiBody{r: strings.NewReader(string(pgiPNG))}
		store := &pgiStore{
			obj: document.Object{Body: body, Size: int64(len(pgiPNG))},
			err: errors.New("s3: 503 slow down"),
		}

		rc, _, err := newPageObjectReader(store)(context.Background(), pgiKey)

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

		rc, _, err := newPageObjectReader(store)(context.Background(), pgiKey)

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
