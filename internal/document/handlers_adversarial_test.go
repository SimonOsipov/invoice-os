// handlers_adversarial_test.go: the DownloadHandler edges the AC-derived specs
// leave open — error-body leakage, hostile stored filenames, a zero-byte object,
// audit-per-request under concurrency, and the route mount in cmd/invoice.
package document_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- error bodies name nothing about storage --------------------------------

// classifyGet wraps every object-store failure with the storage key, and the key
// carries the tenant id and the content hash. A handler that put err.Error() on
// the wire would hand an unauthorised caller the tenant's own object layout.
func TestDownloadHandler_ErrorBodiesLeakNoStorageKeyOrBucket(t *testing.T) {
	const (
		tenant = "6f1d5b9c-0f3a-4a1e-9d2b-7c8e5a4b3c2d"
		hash   = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
		bucket = "source-documents-6asblvno"
	)
	key := "tenants/" + tenant + "/" + hash
	secrets := []string{key, tenant, hash, bucket}

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"404", fmt.Errorf("document: get %s: %w", key, document.ErrNotFound), http.StatusNotFound},
		{"416", fmt.Errorf("document: get %s: %w", key, document.ErrRangeNotSatisfiable), http.StatusRequestedRangeNotSatisfiable},
		{"400", fmt.Errorf("document: get %s: %w", key, document.ErrValidation), http.StatusBadRequest},
		{"500", fmt.Errorf("operation error S3: GetObject, https response error, https://%s.t3.storageapi.dev/%s: 503",
			bucket, key), http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
				return document.Document{}, document.Object{}, tc.err
			}

			rec := doDownload(t, open, &id, uuid.NewString(), "")

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.want, rec.Body.String())
			}
			// Anti-vacuity: the injected error really does carry the strings.
			for _, s := range secrets {
				if tc.name != "500" && s == bucket {
					continue
				}
				if !strings.Contains(tc.err.Error(), s) {
					t.Fatalf("test setup: injected error %q does not contain %q, so the assertions below prove nothing",
						tc.err, s)
				}
			}
			body := rec.Body.String()
			for _, s := range secrets {
				if strings.Contains(body, s) {
					t.Errorf("%d body = %s leaks %q — an error body names no key, tenant, hash or bucket",
						tc.want, body, s)
				}
			}
			for k, vs := range rec.Result().Header {
				for _, v := range vs {
					for _, s := range secrets {
						if strings.Contains(v, s) {
							t.Errorf("header %s = %q leaks %q", k, v, s)
						}
					}
				}
			}
		})
	}
}

// --- hostile stored filenames ------------------------------------------------

// SanitizeFilename runs at store time, so a row written before it existed — or by
// any other writer — can still hold these. The disposition must stay a parseable
// single ASCII line that says attachment, never empty (an empty header lets the
// browser render inline, which is the whole mitigation gone).
func TestDownloadHandler_HostileStoredFilenamesStillRenderAnAttachment(t *testing.T) {
	names := map[string]string{
		"pure control bytes": "\x01\x02\x03\x7f",
		"embedded NUL":       "a\x00b.pdf",
		"invalid utf-8":      string([]byte{0xff, 0xfe, 'A', '.', 'p', 'd', 'f'}),
		"only quotes":        `""""`,
		"only a space":       " ",
		"parameter break":    `a;b="c";d`,
		"255 runes ascii":    strings.Repeat("a", 251) + ".pdf",
		"255 runes cyrillic": strings.Repeat("ф", 251) + ".pdf",
	}

	for label, name := range names {
		t.Run(label, func(t *testing.T) {
			id := testIdentity()
			open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
				return namedDoc(name), nopObject([]byte("x")), nil
			}

			rec := doDownload(t, open, &id, uuid.NewString(), "")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.Bytes())
			}
			got := rec.Result().Header.Get("Content-Disposition")
			if got == "" {
				t.Fatal("Content-Disposition is empty — an unset disposition renders inline")
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("Content-Disposition = %q spans lines", got)
			}
			for i := 0; i < len(got); i++ {
				if got[i] > 0x7f || got[i] < 0x20 {
					t.Fatalf("Content-Disposition = %q carries a raw non-printable/non-ASCII byte at %d", got, i)
				}
			}
			mt, params, err := mime.ParseMediaType(got)
			if err != nil {
				t.Fatalf("Content-Disposition = %q is unparseable: %v", got, err)
			}
			if mt != "attachment" {
				t.Fatalf("disposition type = %q, want attachment", mt)
			}
			if params["filename"] != name {
				t.Errorf("filename param = %q, want the stored name %q", params["filename"], name)
			}
		})
	}
}

// --- a zero-byte object ------------------------------------------------------

// A zero-byte source document is storable (TestServiceStore_ZeroByteDocumentIsStored),
// so it is downloadable. Size 0 must not be mistaken for the unknown-size case in a
// way that costs the response its headers.
func TestDownloadHandler_ZeroByteObjectIsA200WithTheFullHeaderSet(t *testing.T) {
	id := testIdentity()
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		return namedDoc("empty.pdf"), document.Object{Body: io.NopCloser(bytes.NewReader(nil)), Size: 0}, nil
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.Bytes())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.Bytes())
	}
	h := rec.Result().Header
	for k, want := range map[string]string{
		"Content-Type":           "application/octet-stream",
		"X-Content-Type-Options": "nosniff",
		"Accept-Ranges":          "bytes",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if got := h.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want attachment", got)
	}
	if got := h.Get("Content-Range"); got != "" {
		t.Errorf("Content-Range = %q on a non-range 200, want unset", got)
	}
}

// A range against a zero-byte object is unsatisfiable at the store; the handler
// must forward it and answer 416 rather than a bodiless 200.
func TestDownloadHandler_RangeOnAZeroByteObjectIs416(t *testing.T) {
	id := testIdentity()
	var gotRange string
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		gotRange = rangeHeader
		return document.Document{}, document.Object{},
			fmt.Errorf("document: get k: %w", document.ErrRangeNotSatisfiable)
	}

	rec := doDownload(t, open, &id, uuid.NewString(), "bytes=0-0")

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotRange != "bytes=0-0" {
		t.Errorf("open received range %q, want the inbound header verbatim", gotRange)
	}
}

// --- one audit row per request, under concurrency ---------------------------

// concurrentObjects hands every Get its own reader, so N downloads of one
// document cannot share a stream.
type concurrentObjects struct {
	mu      sync.Mutex
	payload []byte
	keys    []string
}

func (c *concurrentObjects) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	return errors.New("concurrentObjects: Put unused")
}

func (c *concurrentObjects) Get(ctx context.Context, key, rangeHeader string) (document.Object, error) {
	c.mu.Lock()
	c.keys = append(c.keys, key)
	c.mu.Unlock()
	return document.Object{Body: io.NopCloser(bytes.NewReader(c.payload)), Size: int64(len(c.payload))}, nil
}

// Core AC 6 is per-READ, not per-document: N concurrent downloads of one document
// owe exactly N document.read rows and N intact bodies. A handler that memoised
// the open result, or a store that folded the audit into the row lookup's
// once-per-document path, would under-count here and nowhere else.
func TestRLS_DownloadConcurrentRequestsAuditExactlyOncePerRequest(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-DL concurrent read audit")
	store := document.NewStore(app)
	subject := uuid.NewString()
	doc, _, err := store.Upsert(identity(ctx, tenantID, subject), docFixture(tenantID, "download-concurrent", 23))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	payload := []byte("tenant document bytes\n")
	objs := &concurrentObjects{payload: payload}
	svc := document.NewService(store, objs)
	id := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}

	if n := auditCount(t, app, tenantID, "document.read"); n != 0 {
		t.Fatalf("test setup: %d document.read rows before any download, want 0", n)
	}

	const n = 8
	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("GET", "/v1/documents/"+doc.ID, nil)
			r.SetPathValue("id", doc.ID)
			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			rec := httptest.NewRecorder()
			document.DownloadHandler(svc.Open, nil).ServeHTTP(rec, r)
			recs[i] = rec
		}()
	}
	wg.Wait()

	for i, rec := range recs {
		if rec.Code != http.StatusOK {
			t.Errorf("download %d status = %d, want 200 (body=%s)", i, rec.Code, rec.Body.String())
			continue
		}
		if !bytes.Equal(rec.Body.Bytes(), payload) {
			t.Errorf("download %d body = %q, want %q — concurrent requests shared a stream", i, rec.Body.Bytes(), payload)
		}
	}
	if got := auditCount(t, app, tenantID, "document.read"); got != n {
		t.Errorf("document.read audit rows after %d concurrent downloads = %d, want %d — reads are audited per request", n, got, n)
	}
	if got := auditCitingCount(t, app, tenantID, "document.read", doc.ID); got != n {
		t.Errorf("document.read rows citing %s = %d, want %d", doc.ID, got, n)
	}
	if len(objs.keys) != n {
		t.Errorf("object-store GETs = %d, want %d", len(objs.keys), n)
	}
}

// --- the route is mounted ----------------------------------------------------

// repoRoot resolves the module root from this package's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// mainRoutePattern returns the ServeMux pattern cmd/invoice/main.go registers for
// the document download, plus the handler expression it registers under it.
func mainRoutePattern(t *testing.T) (pattern string, handler *ast.CallExpr, file *ast.File) {
	t.Helper()
	path := filepath.Join(repoRoot(t), "cmd", "invoice", "main.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var seen int
	ast.Inspect(f, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		seen++
		p := strings.Trim(lit.Value, `"`)
		if !strings.Contains(p, "/v1/documents") {
			return true
		}
		// The download route is the one on the document ITSELF, not a
		// sub-resource of it (.../{id}/sheet). Selected by path depth rather
		// than by anything the callers below assert, so their pattern and
		// handler checks stay non-vacuous. Method is deliberately not filtered:
		// a second verb on this same path SHOULD trip the guard below.
		path := p
		if i := strings.LastIndex(p, " "); i >= 0 {
			path = p[i+1:]
		}
		if len(strings.Split(path, "/")) != 4 { // "", "v1", "documents", "{id}"
			return true
		}
		if pattern != "" {
			t.Errorf("cmd/invoice/main.go registers more than one /v1/documents/{id} route (%q and %q)", pattern, p)
		}
		pattern = p
		handler, _ = call.Args[1].(*ast.CallExpr)
		return true
	})

	if seen == 0 {
		t.Fatal("no HandleFunc call found in cmd/invoice/main.go — the scan matched nothing, so every assertion is vacuous")
	}
	if pattern == "" {
		t.Fatal("cmd/invoice/main.go registers no /v1/documents/{id} route — the download handler is unreachable in production")
	}
	return pattern, handler, f
}

func selectorName(e ast.Expr) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name + "." + sel.Sel.Name
}

// The handler is only real if main.go mounts it. Nothing else in the tree pins
// that: cmd/invoice is package main, its wiring lives inside func main, and no
// e2e spec names /v1/documents.
func TestDownload_RouteIsMountedInCmdInvoice(t *testing.T) {
	pattern, handler, _ := mainRoutePattern(t)

	if want := "GET /v1/documents/{id}"; pattern != want {
		t.Errorf("mounted pattern = %q, want %q", pattern, want)
	}
	if handler == nil {
		t.Fatalf("the %q handler argument is not a call expression", pattern)
	}
	if got := selectorName(handler.Fun); got != "document.DownloadHandler" {
		t.Errorf("%q is served by %q, want document.DownloadHandler", pattern, got)
	}
	if len(handler.Args) == 0 || selectorName(handler.Args[0]) == "" ||
		!strings.HasSuffix(selectorName(handler.Args[0]), ".Open") {
		t.Errorf("DownloadHandler's open argument is not an .Open method value; the route would not resolve a stored key")
	}
}

// C9's regression: main.go used to build the S3 store only to throw it away
// (`if _, err := document.NewS3Store(...)`), which left nothing to serve bytes
// from. The result must be bound and must reach document.NewService.
func TestDownload_S3StoreIsWiredIntoTheServiceNotDiscarded(t *testing.T) {
	_, _, f := mainRoutePattern(t)

	var storeIdent string
	var newServiceArgs []string
	ast.Inspect(f, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if ok {
			for _, rhs := range assign.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok || selectorName(call.Fun) != "document.NewS3Store" {
					continue
				}
				if len(assign.Lhs) == 0 {
					continue
				}
				if id, ok := assign.Lhs[0].(*ast.Ident); ok {
					storeIdent = id.Name
				}
			}
			return true
		}
		call, ok := node.(*ast.CallExpr)
		if ok && selectorName(call.Fun) == "document.NewService" {
			for _, a := range call.Args {
				if id, ok := a.(*ast.Ident); ok {
					newServiceArgs = append(newServiceArgs, id.Name)
					continue
				}
				newServiceArgs = append(newServiceArgs, selectorName(a))
			}
		}
		return true
	})

	if storeIdent == "" || storeIdent == "_" {
		t.Fatalf("document.NewS3Store's result is bound to %q — an unbound store serves no bytes", storeIdent)
	}
	if len(newServiceArgs) == 0 {
		t.Fatal("cmd/invoice/main.go never calls document.NewService — nothing composes the row store with object storage")
	}
	found := false
	for _, a := range newServiceArgs {
		if a == storeIdent {
			found = true
		}
	}
	if !found {
		t.Errorf("document.NewService(%v) does not take the S3 store %q", newServiceArgs, storeIdent)
	}
}

// The pattern string is only correct if a real ServeMux routes a real request
// through it AND binds the path value under the name the handler reads. A
// {docID}/{id} mismatch would 400 every download in production and no unit test
// that calls SetPathValue itself could see it.
func TestDownload_MountedPatternRoutesAndBindsTheIDPathValue(t *testing.T) {
	pattern, _, _ := mainRoutePattern(t)

	var gotID string
	open := func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
		gotID = docID
		return namedDoc("a.pdf"), nopObject([]byte("bytes")), nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc(pattern, document.DownloadHandler(open, nil))

	docID := uuid.NewString()
	r := httptest.NewRequest("GET", "/v1/documents/"+docID, nil)
	r = r.WithContext(auth.WithIdentity(r.Context(), testIdentity()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status through the mounted pattern %q = %d, want 200 (body=%s)", pattern, rec.Code, rec.Body.String())
	}
	if gotID != docID {
		t.Errorf("handler resolved id %q, want %q — the pattern's path value is not named id", gotID, docID)
	}
	if got := rec.Body.String(); got != "bytes" {
		t.Errorf("body = %q, want %q", got, "bytes")
	}

	// A bare collection path must not fall into the {id} route.
	bare := httptest.NewRequest("GET", "/v1/documents/", nil)
	bare = bare.WithContext(auth.WithIdentity(bare.Context(), testIdentity()))
	bareRec := httptest.NewRecorder()
	mux.ServeHTTP(bareRec, bare)
	if bareRec.Code == http.StatusOK {
		t.Errorf("GET /v1/documents/ = 200 through the {id} pattern, want no match")
	}
}
