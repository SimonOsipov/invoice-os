package blogweb

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type rig struct {
	h     http.Handler
	g     *fakeGateway
	c     *clock
	root  string
	app   []byte
	shell []byte
}

func newRig(t *testing.T) *rig {
	t.Helper()
	g, c := newGateway(t), newClock()
	g.cats = []Category{{ID: "a", Name: "Alpha"}, {ID: "empty", Name: "Empty"}}
	g.posts = []Post{{ID: "p", CategoryID: "a", PublishDate: "2026-10-01", Title: "Hello post", Summary: "S"}}
	root := t.TempDir()
	shell := []byte(realShellHTML(t))
	app := bytes.Repeat([]byte("console.log(1);\n"), 128) // 2 KiB
	must := func(name string, b []byte) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("index.html", shell)
	must("assets/app.js", app)
	must("assets/app.js.map", []byte("{}"))
	must("favicon.ico", []byte("ico"))
	must("build.txt", []byte("abc123"))
	u, _ := url.Parse(g.URL)
	h, err := New(Config{Root: root, Gateway: u, Client: &http.Client{Timeout: 300 * time.Millisecond}, Now: c.now, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{h, g, c, root, app, shell}
}

func (r *rig) do(method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://blog.example/", nil)
	req.URL.Path = target
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func TestServer_VisiblePostAnswers200WithItsPage(t *testing.T) {
	w := newRig(t).do("GET", "/p")
	h := w.Header()
	if w.Code != 200 || h.Get("Content-Type") != "text/html; charset=utf-8" || h.Get("Cache-Control") != "no-cache" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("%d %v", w.Code, h)
	}
	has(t, w.Body.String(), "<title>Hello post · ASComply blog</title>")
	has(t, w.Body.String(), `<script type="module"`)
}

func TestServer_TrailingSlashServesTheSamePageAndCanonical(t *testing.T) {
	r := newRig(t)
	for path, canon := range map[string]string{"/p/": "https://blog.example/p", "/category/a/": "https://blog.example/category/a"} {
		w := r.do("GET", path)
		if w.Code != 200 {
			t.Errorf("%s: %d", path, w.Code)
		}
		has(t, w.Body.String(), `<link rel="canonical" href="`+canon+`">`)
	}
}

func TestServer_HomeCategoryAndSubscribeAnswer200(t *testing.T) {
	r := newRig(t)
	for _, p := range []string{"/", "/category/a", "/category/empty", "/subscribe", "/subscribe/confirm"} {
		if w := r.do("GET", p); w.Code != 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if !strings.Contains(r.do("GET", "/subscribe/confirm").Body.String(), `content="noindex"`) {
		t.Error("/subscribe/confirm lacks noindex")
	}
	if strings.Contains(r.do("GET", "/subscribe").Body.String(), "noindex") {
		t.Error("/subscribe carries noindex")
	}
}

func TestServer_HiddenUnknownAndMalformedAre404(t *testing.T) {
	r := newRig(t)
	var first string
	for _, p := range []string{"/draft-1", "/future-1", "/nope", "/category/nope", "/Bad_Slug", "/a/b/c", "/category/", "//p", "/../go.mod", "/p\x00"} {
		w := r.do("GET", p)
		if w.Code != 404 || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%q: %d %v", p, w.Code, w.Header())
		}
		has(t, w.Body.String(), "<h1>Not found</h1>")
		if first == "" {
			first = w.Body.String()
		} else if w.Body.String() != first {
			t.Errorf("%q: body differs", p)
		}
	}
}

func TestServer_SlugLengthBoundary(t *testing.T) {
	r := newRig(t)
	long := strings.Repeat("a", 80)
	r.g.set(func() { r.g.posts = []Post{{ID: long, CategoryID: "a", PublishDate: "2026-10-01"}} })
	if w := r.do("GET", "/"+long); w.Code != 200 {
		t.Errorf("80: %d", w.Code)
	}
	if w := r.do("GET", "/"+long+"a"); w.Code != 404 {
		t.Errorf("81: %d", w.Code)
	}
}

func TestServer_EmptySnapshotStillAnswersHome(t *testing.T) {
	r := newRig(t)
	r.g.set(func() { r.g.cats, r.g.posts = []Category{}, []Post{} })
	w := r.do("GET", "/")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	has(t, w.Body.String(), "<h1>"+homeHeading+"</h1>")
	var sm parsedSitemap
	if err := xml.Unmarshal(r.do("GET", "/sitemap.xml").Body.Bytes(), &sm); err != nil || len(sm.URLs) != 1 {
		t.Errorf("sitemap %+v %v", sm, err)
	}
}

func TestServer_SitemapAndRobotsAnswerWithTheirTypes(t *testing.T) {
	r := newRig(t)
	w := r.do("GET", "/sitemap.xml")
	var sm parsedSitemap
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/xml; charset=utf-8" || w.Header().Get("Cache-Control") != "no-cache" ||
		xml.Unmarshal(w.Body.Bytes(), &sm) != nil {
		t.Errorf("sitemap %d %v", w.Code, w.Header())
	}
	w = r.do("GET", "/robots.txt")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("robots %d %v", w.Code, w.Header())
	}
	has(t, w.Body.String(), "Sitemap: https://blog.example/sitemap.xml")
}

func TestServer_RobotsNeedsNoUpstream(t *testing.T) {
	r := newRig(t)
	r.g.set(func() { r.g.status = 500 })
	if w := r.do("GET", "/robots.txt"); w.Code != 200 {
		t.Errorf("%d", w.Code)
	}
	if n := len(r.g.lastPath); n != 0 {
		t.Errorf("upstream called %d times", n)
	}
}

func TestServer_UpstreamFailureIs503WithTheUnmodifiedShell(t *testing.T) {
	r := newRig(t)
	r.g.set(func() { r.g.status = 500 })
	for _, p := range []string{"/p", "/", "/sitemap.xml"} {
		w := r.do("GET", p)
		if w.Code != 503 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Retry-After") != "60" {
			t.Errorf("%s: %d %v", p, w.Code, w.Header())
		}
		if p != "/sitemap.xml" && !bytes.Equal(w.Body.Bytes(), r.shell) {
			t.Errorf("%s: body is not the shell", p)
		}
	}
}

func TestServer_APublishShowsAfterMaxAge(t *testing.T) {
	r := newRig(t)
	if w := r.do("GET", "/new"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	r.g.set(func() { r.g.posts = append(r.g.posts, Post{ID: "new", CategoryID: "a", PublishDate: "2026-10-02"}) })
	r.c.add(60 * time.Second)
	if w := r.do("GET", "/new"); w.Code != 200 {
		t.Errorf("/new: %d", w.Code)
	}
	has(t, r.do("GET", "/sitemap.xml").Body.String(), "https://blog.example/new</loc>")
}

func TestServer_HealthAndMapParityWithCaddy(t *testing.T) {
	r := newRig(t)
	if w := r.do("GET", "/health"); w.Code != 200 || w.Body.String() != "OK" {
		t.Errorf("health %d %q", w.Code, w.Body)
	}
	w := r.do("GET", "/assets/app.js.map")
	if w.Code != 404 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Body.Len() != 0 {
		t.Errorf("map %d %v %q", w.Code, w.Header(), w.Body)
	}
}

func TestServer_ExistingFileWinsOverDocumentRoutes(t *testing.T) {
	r := newRig(t)
	for p, want := range map[string][]byte{"/assets/app.js": r.app, "/favicon.ico": []byte("ico"), "/build.txt": []byte("abc123")} {
		w := r.do("GET", p)
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if len(r.g.lastPath) != 0 {
		t.Error("upstream called")
	}
}

func TestServer_IndexHtmlRedirectsAndDirectoriesAreNotFiles(t *testing.T) {
	r := newRig(t)
	if w := r.do("GET", "/index.html"); w.Code != 301 || w.Header().Get("Location") != "./" {
		t.Errorf("index.html: %d %v", w.Code, w.Header())
	}
	for _, p := range []string{"/assets/", "/assets", "/../go.mod"} {
		w := r.do("GET", p)
		if w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
		has(t, w.Body.String(), "<h1>Not found</h1>")
	}
}

func TestServer_GzipsAssets(t *testing.T) {
	r := newRig(t)
	w := r.do("GET", "/assets/app.js", "Accept-Encoding", "gzip")
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("headers %v", w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, r.app) {
		t.Error("gunzipped body differs")
	}
}

func TestServer_OnlyGetAndHead(t *testing.T) {
	r := newRig(t)
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		for _, p := range []string{"/", "/assets/app.js"} {
			w := r.do(m, p)
			if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s: %d %v", m, p, w.Code, w.Header())
			}
		}
	}
	get, head := r.do("GET", "/p"), r.do("HEAD", "/p")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Errorf("HEAD: %d %d bytes", head.Code, head.Body.Len())
	}
	for _, k := range []string{"Content-Type", "Cache-Control", "X-Content-Type-Options"} {
		if get.Header().Get(k) != head.Header().Get(k) {
			t.Errorf("%s: GET %q HEAD %q", k, get.Header().Get(k), head.Header().Get(k))
		}
	}
}

func TestNew_RefusesABrokenShell(t *testing.T) {
	u, _ := url.Parse("https://api.example")
	for name, index := range map[string]*string{
		"two roots": ptr(`<title>t</title><div id="root"></div><div id="root"></div>`),
		"missing":   nil,
	} {
		root := t.TempDir()
		if index != nil {
			_ = os.WriteFile(filepath.Join(root, "index.html"), []byte(*index), 0o644)
		}
		if _, err := New(Config{Root: root, Gateway: u, Client: &http.Client{}, Now: time.Now, Log: slog.Default()}); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}

func ptr(s string) *string { return &s }

func (r *rig) doRaw(target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "https://blog.example"+target, nil)
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func TestServer_EncodedPathsNeverReachOutsideRootOrShadowRoutes(t *testing.T) {
	r := newRig(t)
	secret := filepath.Join(r.root, "..", "secret.txt")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })
	for _, p := range []string{
		"/%2e%2e/secret.txt", "/..%2fsecret.txt", "/assets/%2e%2e/%2e%2e/secret.txt",
		"/assets/..%2f..%2fsecret.txt", "/%2e%2e%2fsecret.txt", "/p%00", "/%00",
		"/%70%2f", "/p%2fx", "/category%2fa", "/%43ategory/a",
	} {
		w := r.doRaw(p)
		if strings.Contains(w.Body.String(), "TOPSECRET") {
			t.Errorf("%s: served the file outside Root", p)
		}
		if w.Code != 404 && w.Code != 400 && w.Code != 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	for _, p := range []string{"/%2e%2e/secret.txt", "/p%00", "/p%2fx", "/%43ategory/a"} {
		if w := r.doRaw(p); w.Code != 404 {
			t.Errorf("%s: %d, want the not-found page", p, w.Code)
		}
	}
	// "%70" decodes to "p": the same page as /p
	if w := r.doRaw("/%70"); w.Code != 200 {
		t.Errorf("/%%70: %d", w.Code)
	}
}

func TestServer_ConcurrentRequestsAfterExpiryFetchOnce(t *testing.T) {
	r := newRig(t)
	if w := r.do("GET", "/p"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	base := len(r.g.lastPath)
	r.g.set(func() { r.g.posts = append(r.g.posts, Post{ID: "late", CategoryID: "a", PublishDate: "2026-10-02"}) })
	r.c.add(60 * time.Second)
	var wg sync.WaitGroup
	codes := make([]int, 24)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = r.do("GET", "/late").Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Errorf("request %d: %d", i, c)
		}
	}
	if n := len(r.g.lastPath) - base; n != 2 {
		t.Errorf("upstream calls during one refresh = %d, want 2", n)
	}
}

func TestServer_FailureBackoffAnswers503WithoutFetchingAndRecovers(t *testing.T) {
	r := newRig(t)
	r.g.set(func() { r.g.status = 500 })
	if w := r.do("GET", "/p"); w.Code != 503 {
		t.Fatal(w.Code)
	}
	after := len(r.g.lastPath)
	r.g.set(func() { r.g.status = 0 })
	r.c.add(4 * time.Second)
	for _, p := range []string{"/p", "/", "/sitemap.xml"} {
		if w := r.do("GET", p); w.Code != 503 {
			t.Errorf("%s inside back-off: %d", p, w.Code)
		}
	}
	if len(r.g.lastPath) != after {
		t.Error("fetched inside the back-off window")
	}
	r.c.add(time.Second)
	if w := r.do("GET", "/p"); w.Code != 200 {
		t.Errorf("after back-off: %d", w.Code)
	}
}

func TestServer_HeadOn503And404And503Sitemap(t *testing.T) {
	r := newRig(t)
	if w := r.do("HEAD", "/nope"); w.Code != 404 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("HEAD 404: %d %d %v", w.Code, w.Body.Len(), w.Header())
	}
	if w := r.do("HEAD", "/sitemap.xml"); w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD sitemap: %d %d", w.Code, w.Body.Len())
	}
	r.g.set(func() { r.g.status = 500 })
	r.c.add(time.Hour)
	for _, p := range []string{"/p", "/sitemap.xml"} {
		w := r.do("HEAD", p)
		if w.Code != 503 || w.Body.Len() != 0 || w.Header().Get("Retry-After") != "60" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("HEAD %s: %d %d %v", p, w.Code, w.Body.Len(), w.Header())
		}
	}
}

func TestServer_HostIsEscapedInEveryGeneratedDocument(t *testing.T) {
	r := newRig(t)
	const host = `evil"><script>alert(1)</script>`
	for _, p := range []string{"/", "/p", "/nope", "/sitemap.xml", "/robots.txt"} {
		req := httptest.NewRequest("GET", "https://blog.example/", nil)
		req.URL.Path, req.Host = p, host
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		if w.Code != 200 && w.Code != 404 {
			t.Fatalf("%s: %d", p, w.Code)
		}
		if p != "/robots.txt" && strings.Contains(w.Body.String(), "<script>alert(1)") {
			t.Errorf("%s: Host written unescaped", p)
		}
	}
}

func TestServer_SubscribeTrailingSlashAndOverlongIDsAndStaticHeaders(t *testing.T) {
	r := newRig(t)
	for p, canon := range map[string]string{"/subscribe/": "/subscribe", "/subscribe/confirm/": "/subscribe/confirm"} {
		w := r.do("GET", p)
		if w.Code != 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
		has(t, w.Body.String(), `<link rel="canonical" href="https://blog.example`+canon+`">`)
	}
	long := strings.Repeat("a", 81)
	r.g.set(func() {
		r.g.cats = append(r.g.cats, Category{ID: long, Name: "Long"})
		r.g.posts = []Post{{ID: long, CategoryID: "a", PublishDate: "2026-10-01"}}
	})
	r.c.add(time.Minute)
	for _, p := range []string{"/" + long, "/category/" + long} {
		if w := r.do("GET", p); w.Code != 404 {
			t.Errorf("%s: %d, want 404 for an id over 80 characters", p[:12], w.Code)
		}
	}
	for _, p := range []string{"/assets/app.js", "/favicon.ico", "/build.txt"} {
		if cc := r.do("GET", p).Header().Get("Cache-Control"); cc != "" {
			t.Errorf("%s: Cache-Control %q, Caddy sent none", p, cc)
		}
	}
}

func TestServer_AClientThatLeavesDuringARefreshDoesNotTakeTheSiteDown(t *testing.T) {
	r := newRig(t)
	r.g.set(func() { r.g.hang = 100 * time.Millisecond })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "https://blog.example/p", nil).WithContext(ctx)
	r.h.ServeHTTP(httptest.NewRecorder(), req)
	r.g.set(func() { r.g.hang = 0 })
	r.c.add(time.Second)
	if w := r.do("GET", "/p"); w.Code != 200 {
		t.Errorf("next visitor after a client left mid-refresh: %d, want 200", w.Code)
	}
}

func TestServer_AWaiterThatLeavesReturnsAtOnceAndLeavesTheSharedStateAlone(t *testing.T) {
	r := newRig(t)
	r.g.set(func() {
		r.g.hang, r.g.catCC, r.g.postCC = 100*time.Millisecond, "public, max-age=3", "public, max-age=3"
	})
	var wg sync.WaitGroup
	var holder int
	wg.Add(1)
	go func() { defer wg.Done(); holder = r.do("GET", "/p").Code }()
	time.Sleep(30 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	r.h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://blog.example/p", nil).WithContext(ctx))
	if d := time.Since(start); d > 120*time.Millisecond {
		t.Errorf("a cancelled waiter took %v; the refresh holds the lock for about 200ms", d)
	}
	wg.Wait()
	if holder != 200 {
		t.Errorf("the request that held the lock: %d", holder)
	}
	r.g.set(func() { r.g.hang = 0 })
	r.c.add(4 * time.Second)
	if w := r.do("GET", "/p"); w.Code != 200 {
		t.Errorf("after the snapshot went stale: %d, a leaving waiter must not set a back-off", w.Code)
	}
}
