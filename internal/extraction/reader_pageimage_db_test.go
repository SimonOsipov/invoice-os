// reader_pageimage_db_test.go: EXTR-11-03's DB-backed suite. It drives (*Reader).PageImageKey
// through the real handler, because the claim is end to end: a refused key lookup must reach no
// bucket, and a served one must hand object storage the key off the RLS-visible row and nothing
// else. Shares store_db_test.go's harness and reader_db_test.go's rdTenant/rdSeedJob fixtures,
// and seeds every row as the superuser for the same reason they do.
//
// Helpers use an rpi* prefix.
package extraction_test

import (
	"context"
	"go/ast"
	"go/token"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rpiObjectSpy records every key that reaches object storage. A refusal that still called Get
// has already leaked the fact that a row exists, whatever status it answered with.
type rpiObjectSpy struct {
	mu   sync.Mutex
	keys []string
	body []byte
}

func (s *rpiObjectSpy) get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	return io.NopCloser(strings.NewReader(string(s.body))), int64(len(s.body)), nil
}

func (s *rpiObjectSpy) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys...)
}

// rpiServe drives the real handler over the real reader, with the caller's own request context
// so RLS sees the identity rdTenant seeded.
func rpiServe(t *testing.T, r *extraction.Reader, obj *rpiObjectSpy, reqCtx context.Context, jobID, page string) *httptest.ResponseRecorder {
	t.Helper()
	req := pimRequest(jobID, page, nil).WithContext(reqCtx)
	w := httptest.NewRecorder()
	extraction.PageImageHandler(r.PageImageKey, obj.get, nil)(w, req)
	return w
}

// AC 6. Both directions: A reads its own page and the key that reaches the bucket is A's own
// row, and B asking for A's job is refused with the same 404 an absent page returns -- having
// touched no object at all. Without B's own successful read the refusal would also be what a
// reader that refuses everyone produces.
func TestRLS_ExtractionPageImageCrossTenantRefused(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	ctxB, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)

	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedPage(t, ctx, tenantA, docA, 2, 1275, 1651)
	rvdSeedPage(t, ctx, tenantB, docB, 1, 900, 1200)
	rvdSeedPage(t, ctx, tenantB, docB, 2, 900, 1200)

	wantA := rvdPageKey(tenantA, 2)
	wantB := rvdPageKey(tenantB, 2)
	if wantA == wantB {
		t.Fatalf("the two tenants derive the same page key %q, so the assertions below cannot tell whose row was read", wantA)
	}

	t.Run("A reads its own page", func(t *testing.T) {
		obj := &rpiObjectSpy{body: pimPNG}
		w := rpiServe(t, r, obj, ctxA, jobA, "2")

		if w.Code != 200 {
			t.Fatalf("A reading its own job %s page 2: status = %d, want 200 (body=%q)", jobA, w.Code, w.Body.String())
		}
		if got := w.Body.String(); got != string(pimPNG) {
			t.Errorf("body = %q, want the object's bytes %q", got, pimPNG)
		}
		if got := obj.seen(); len(got) != 1 || got[0] != wantA {
			t.Errorf("object storage was asked for %v, want exactly [%q] -- the key comes off the RLS-visible row, never from the request", got, wantA)
		}
	})

	t.Run("B is refused A's page", func(t *testing.T) {
		obj := &rpiObjectSpy{body: pimPNG}
		w := rpiServe(t, r, obj, ctxB, jobA, "2")

		hndAssert(t, w, 404, hndErrBody(t, dtlMsgNotFound))
		if got := obj.seen(); len(got) != 0 {
			t.Errorf("object storage was asked for %v on a cross-tenant read, want nothing -- a refused read must reach no bucket", got)
		}
	})

	t.Run("B reads its own page", func(t *testing.T) {
		obj := &rpiObjectSpy{body: pimPNG}
		w := rpiServe(t, r, obj, ctxB, jobB, "2")

		if w.Code != 200 {
			t.Fatalf("B reading its own job %s page 2: status = %d, want 200 (body=%q) -- without this the refusal above is also what a reader that refuses everyone produces", jobB, w.Code, w.Body.String())
		}
		if got := obj.seen(); len(got) != 1 || got[0] != wantB {
			t.Errorf("object storage was asked for %v, want exactly [%q]", got, wantB)
		}
	})
}

// AC 6's other half, over a real inventory rather than an injected sentinel: page 99 of a
// one-page document is a 404 with the refusal envelope, not a 200 carrying nothing. The page-1
// case beside it is the control -- an inventory that cannot be read at all answers 404 too.
func TestRLS_ExtractionPageImageUnknownPageIs404(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)

	t.Run("page 1 is served", func(t *testing.T) {
		obj := &rpiObjectSpy{body: pimPNG}
		w := rpiServe(t, r, obj, ctxA, jobA, "1")

		if w.Code != 200 {
			t.Fatalf("page 1 of a one-page document: status = %d, want 200 (body=%q) -- the 404 below proves nothing over an unreadable fixture", w.Code, w.Body.String())
		}
		if got := obj.seen(); len(got) != 1 || got[0] != rvdPageKey(tenantA, 1) {
			t.Errorf("object storage was asked for %v, want exactly [%q]", got, rvdPageKey(tenantA, 1))
		}
	})

	t.Run("page 99 is refused", func(t *testing.T) {
		obj := &rpiObjectSpy{body: pimPNG}
		w := rpiServe(t, r, obj, ctxA, jobA, "99")

		hndAssert(t, w, 404, hndErrBody(t, dtlMsgNotFound))
		if got := obj.seen(); len(got) != 0 {
			t.Errorf("object storage was asked for %v for a page the inventory does not hold, want nothing", got)
		}
	})
}

// The fork answer, asserted as a property. One screen open writes ONE document.read row, from
// DetailHandler; a twenty-page document must not turn that into twenty-one. The Detail call
// beside it is the vacuity guard: zero rows from a recorder that never worked would read
// exactly the same.
func TestRLS_ExtractionPageImageWritesNoAuditRow(t *testing.T) {
	ctx := t.Context()
	rec := &rdaRecorder{}
	r := rdaReader(t, rec)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	rdaPurge(t, tenantA)
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedPage(t, ctx, tenantA, docA, 2, 1275, 1651)

	if before := rdAuditCount(t, ctx, tenantA); before != 0 {
		t.Fatalf("tenant %s already holds %d audit row(s) before any read, so the delta below is not one", tenantA, before)
	}

	for _, page := range []string{"1", "2", "1"} {
		obj := &rpiObjectSpy{body: pimPNG}
		if w := rpiServe(t, r, obj, ctxA, jobA, page); w.Code != 200 {
			t.Fatalf("serving page %s: status = %d, want 200 (body=%q)", page, w.Code, w.Body.String())
		}
	}

	if n := rdAuditCount(t, ctx, tenantA); n != 0 {
		t.Errorf("three page fetches left %d audit row(s) for tenant %s, want 0 -- one screen open owes one document.read row, not one per page", n, tenantA)
	}
	if len(rec.calls) != 0 {
		t.Errorf("the recorder was called %d time(s) on the page route, want 0", len(rec.calls))
	}

	// The guard: the same reader DOES audit when the screen opens, so the zero above is a
	// decision rather than a recorder that was never wired.
	if _, err := r.Detail(ctxA, jobA); err != nil {
		t.Fatalf("Detail on the same reader: %v", err)
	}
	if n := rdAuditCount(t, ctx, tenantA); n != 1 {
		t.Fatalf("one Detail call left %d audit row(s), want 1 -- the zero above cannot distinguish a deliberate omission from a broken recorder", n)
	}
}

// The key is SELECTed, never rebuilt. PageKey(tenantID, contentHash, page) would reproduce the
// same string from Go without ever touching an RLS-scoped row, and every behavioural case above
// would still pass on the day the content hash became reachable from a request.
//
// The scan reads reader.go and only reader.go, which pins the query's FILE as well as its
// shape. That is deliberate: TestRLS_ExtractionDetailDocumentJoinNamesNoTenantId is the guard
// that no reader query names tenant_id itself, and it too reads only reader.go -- so a page
// query written in a new file would leave the tenant boundary unguarded while both scans stayed
// green.
func TestRLS_ExtractionPageImageKeySelectsTheStoredKey(t *testing.T) {
	f, fset := mxParse(t, rdReaderSource)

	var sqlLits, keyQueries int
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		unq, err := strconv.Unquote(bl.Value)
		if err != nil || !strings.Contains(unq, "SELECT") {
			return true
		}
		sqlLits++
		if strings.Contains(unq, "storage_key") && strings.Contains(unq, "extraction_page_images") {
			keyQueries++
			if !strings.Contains(unq, "page_number") {
				t.Errorf("%s: the storage_key query does not filter on page_number; the route addresses a page by number", fset.Position(bl.Pos()))
			}
		}
		return true
	})

	if sqlLits == 0 {
		t.Fatalf("%s holds no SQL string literal, so the count below reads nothing", rdReaderSource)
	}
	if keyQueries != 1 {
		t.Errorf("%s holds %d SELECT naming both storage_key and extraction_page_images, want exactly 1 -- the key must come off the row, never from PageKey(tenantID, contentHash, page) rebuilt in Go", rdReaderSource, keyQueries)
	}
}
