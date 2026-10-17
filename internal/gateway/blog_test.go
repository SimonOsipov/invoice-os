package gateway

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const blogTestToken = "gw-token-for-blog-tests"

var blogCoverBytes = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0xff, 0xd9}

// blogPaths holds one valid request path per public route.
var blogPaths = []string{"/blog/categories", "/blog/posts", "/blog/posts/abc", "/blog/covers/abc/17"}

type blogCall struct {
	method, path, rawQuery string
	header                 http.Header
}

// blogUpstream stands in for notifications and records every call it receives.
type blogUpstream struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []blogCall
	answer http.HandlerFunc
}

func newBlogUpstream(t *testing.T) *blogUpstream {
	t.Helper()
	u := &blogUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.calls = append(u.calls, blogCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()})
		answer := u.answer
		u.mu.Unlock()
		if answer != nil {
			answer(w, r)
			return
		}
		blogDefaultAnswer(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func blogDefaultAnswer(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/internal/blog/covers/") {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(blogCoverBytes)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (u *blogUpstream) setAnswer(f http.HandlerFunc) {
	u.mu.Lock()
	u.answer = f
	u.mu.Unlock()
}

func (u *blogUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *blogUpstream) last(t *testing.T) blogCall {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.calls) == 0 {
		t.Fatal("the upstream saw no call")
	}
	return u.calls[len(u.calls)-1]
}

func blogAnswers(status int, contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

type blogCfg struct {
	unenforced   bool
	max, maxKeys int // 0: BlogReadPerIP, BlogReadMaxKeys
	client       *http.Client
}

type blogRig struct {
	up       *blogUpstream
	routes   BlogRoutes
	clk      *testClock
	throttle *SignInThrottle
	logs     *logStore
	mux      *http.ServeMux
}

func newBlogRig(t *testing.T, cfg blogCfg) *blogRig {
	t.Helper()
	up := newBlogUpstream(t)
	if cfg.max == 0 {
		cfg.max = BlogReadPerIP
	}
	if cfg.maxKeys == 0 {
		cfg.maxKeys = BlogReadMaxKeys
	}
	if cfg.client == nil {
		cfg.client = up.srv.Client()
	}
	base, err := url.Parse(up.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	clk := newTestClock()
	th := NewSignInThrottle("blog-read", cfg.max, cfg.maxKeys, BlogReadWindow, clk.Now)
	log, logs := newCaptureLog()
	routes := BlogHandlers(base, cfg.client, blogTestToken, th, !cfg.unenforced, log)
	// No method in the patterns: the handler itself must refuse a method that is not a read.
	mux := http.NewServeMux()
	mux.Handle("/blog/categories", routes.Categories)
	mux.Handle("/blog/posts", routes.Posts)
	mux.Handle("/blog/posts/{slug}", routes.Post)
	mux.Handle("/blog/covers/{id}/{version}", routes.Cover)
	return &blogRig{up: up, routes: routes, clk: clk, throttle: th, logs: logs, mux: mux}
}

const blogIP = "203.0.113.7:4000"

func (r *blogRig) do(method, path, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func (r *blogRig) get(path, remote string) *httptest.ResponseRecorder {
	return r.do(http.MethodGet, path, remote)
}

// direct calls h with raw path values, as the mux would set them after decoding.
func (r *blogRig) direct(h http.Handler, remote string, kv ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/blog/direct", nil)
	req.RemoteAddr = remote
	for i := 0; i < len(kv); i += 2 {
		req.SetPathValue(kv[i], kv[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// spend sends n GETs from remote, spread over the four routes, and requires each to be forwarded.
func (r *blogRig) spend(t *testing.T, remote string, n int) {
	t.Helper()
	for i := range n {
		if rec := r.get(blogPaths[i%len(blogPaths)], remote); rec.Code != http.StatusOK {
			t.Fatalf("request %d of %d from %s = %d, want 200: %s", i+1, n, remote, rec.Code, rec.Body.String())
		}
	}
}

func (r *blogRig) warns(msg string) []logRecord {
	var out []logRecord
	for _, rec := range r.logs.all() {
		if rec.level >= slog.LevelWarn && strings.HasPrefix(rec.text, msg) {
			out = append(out, rec)
		}
	}
	return out
}

func requireJSONError(t *testing.T, rec *httptest.ResponseRecorder, code int, msg string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d: %s", rec.Code, code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !reflect.DeepEqual(body, map[string]string{"error": msg}) {
		t.Errorf("body = %q, want {\"error\":%q}", rec.Body.String(), msg)
	}
}

func TestBlogRead_ForwardsEachRouteToItsInternalPath(t *testing.T) {
	want := map[string]string{
		"/blog/categories":    "/internal/blog/categories",
		"/blog/posts":         "/internal/blog/posts",
		"/blog/posts/abc":     "/internal/blog/posts/abc",
		"/blog/covers/abc/17": "/internal/blog/covers/abc/17",
	}
	if len(want) != len(blogPaths) {
		t.Fatalf("table has %d paths, want %d", len(want), len(blogPaths))
	}
	for _, path := range blogPaths {
		t.Run(path, func(t *testing.T) {
			rig := newBlogRig(t, blogCfg{})
			if rec := rig.get(path, blogIP); rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200: %s", path, rec.Code, rec.Body.String())
			}
			if n := rig.up.count(); n != 1 {
				t.Fatalf("upstream calls = %d, want 1", n)
			}
			c := rig.up.last(t)
			if c.method != http.MethodGet || c.path != want[path] {
				t.Errorf("upstream saw %s %s, want GET %s", c.method, c.path, want[path])
			}
			if got := c.header.Get("X-Gateway-Token"); got != blogTestToken {
				t.Errorf("X-Gateway-Token = %q, want the gateway token", got)
			}
		})
	}
}

func TestBlogRead_ForwardsNoClientHeaderOrQuery(t *testing.T) {
	sent := map[string]string{
		"Authorization":   "Bearer client-token",
		"Cookie":          "sid=secret",
		"X-User-ID":       "7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f",
		"X-Tenant-ID":     "tenant-1",
		"X-User-Staff":    "true",
		"X-Real-IP":       "198.51.100.77",
		"X-Forwarded-For": "198.51.100.77",
		"Origin":          "https://blog.example",
		"Referer":         "https://blog.example/post",
		"X-Gateway-Token": "client-forged-token",
	}
	allowed := []string{"X-Gateway-Token", "User-Agent", "Accept-Encoding", "Accept"}
	for _, path := range blogPaths {
		t.Run(path, func(t *testing.T) {
			rig := newBlogRig(t, blogCfg{})
			req := httptest.NewRequest(http.MethodGet, path+"?x=1&y=%00", nil)
			req.RemoteAddr = blogIP
			for k, v := range sent {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			rig.mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			c := rig.up.last(t)
			if c.rawQuery != "" {
				t.Errorf("upstream query = %q, want none", c.rawQuery)
			}
			if got := c.header.Get("X-Gateway-Token"); got != blogTestToken {
				t.Errorf("X-Gateway-Token = %q, want the gateway token, not the client's", got)
			}
			for name, vals := range c.header {
				if !slices.Contains(allowed, name) {
					t.Errorf("upstream saw header %s = %v, want only %v", name, vals, allowed)
				}
			}
		})
	}
}

func TestBlogRead_PassesTheBodyAndContentTypeThrough(t *testing.T) {
	const jsonBody = `{"posts":[{"id":"abc","title":"Hello"}]}`
	for _, path := range []string{"/blog/categories", "/blog/posts", "/blog/posts/abc"} {
		t.Run(path, func(t *testing.T) {
			rig := newBlogRig(t, blogCfg{})
			rig.up.setAnswer(blogAnswers(http.StatusOK, "application/json; charset=utf-8", jsonBody))
			rec := rig.get(path, blogIP)
			if rec.Code != http.StatusOK || rec.Body.String() != jsonBody {
				t.Fatalf("GET = %d %q, want 200 %q", rec.Code, rec.Body.String(), jsonBody)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want the upstream's", got)
			}
		})
	}
	t.Run("cover bytes", func(t *testing.T) {
		rig := newBlogRig(t, blogCfg{})
		rig.up.setAnswer(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(blogCoverBytes)
		})
		rec := rig.get("/blog/covers/abc/17", blogIP)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), blogCoverBytes) {
			t.Fatalf("GET cover = %d %x, want 200 %x", rec.Code, rec.Body.Bytes(), blogCoverBytes)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/png" {
			t.Errorf("Content-Type = %q, want image/png", got)
		}
	})
}

func TestBlogRead_ShortCacheControlOnJSONAnswers(t *testing.T) {
	const want = "public, max-age=60"
	for _, path := range []string{"/blog/categories", "/blog/posts", "/blog/posts/abc"} {
		t.Run("200 "+path, func(t *testing.T) {
			rig := newBlogRig(t, blogCfg{})
			rec := rig.get(path, blogIP)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != want {
				t.Errorf("Cache-Control = %q, want %q", got, want)
			}
		})
	}
	t.Run("404 /blog/posts/x", func(t *testing.T) {
		rig := newBlogRig(t, blogCfg{})
		rig.up.setAnswer(blogAnswers(http.StatusNotFound, "application/json", `{"error":"not found"}`))
		rec := rig.get("/blog/posts/x", blogIP)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET = %d, want 404", rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != want || strings.Contains(got, "no-store") {
			t.Errorf("Cache-Control = %q, want %q", got, want)
		}
	})
}

func TestBlogRead_CoverCacheControlIsImmutable(t *testing.T) {
	rig := newBlogRig(t, blogCfg{})
	rec := rig.get("/blog/covers/abc/17", blogIP)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET cover = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want the immutable year", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

type blogAnswer struct {
	code   int
	header http.Header
	body   string
}

func snapshotBlog(rec *httptest.ResponseRecorder) blogAnswer {
	h := rec.Header().Clone()
	h.Del("Date")
	return blogAnswer{rec.Code, h, rec.Body.String()}
}

// blogMalformed lists path values the gateway must refuse before any upstream call: the
// canonical decimal version and the slug shape are the only forms served (the URL is the cache key).
var blogMalformed = []struct{ name, path string }{
	{"slug Bad_Slug", "/blog/posts/Bad_Slug"},
	{"slug of 81 chars", "/blog/posts/" + strings.Repeat("a", 81)},
	{"slug upper case", "/blog/posts/ABC"},
	{"slug non-ASCII", "/blog/posts/%C3%A9"},
	{"slug trailing newline", "/blog/posts/abc%0A"},
	{"slug encoded slash", "/blog/posts/a%2Fb"},
	{"slug NUL", "/blog/posts/%00"},
	{"slug invalid UTF-8", "/blog/posts/%ff"},
	{"cover id Bad_Id", "/blog/covers/Bad_Id/17"},
	{"cover id NUL", "/blog/covers/%00/17"},
	{"cover id invalid UTF-8", "/blog/covers/%ff/17"},
	{"version 0", "/blog/covers/abc/0"},
	{"version x", "/blog/covers/abc/x"},
	{"version 01", "/blog/covers/abc/01"},
	{"version 007", "/blog/covers/abc/007"},
	{"version of 20 digits", "/blog/covers/abc/12345678901234567890"},
	{"version +5 encoded", "/blog/covers/abc/%2B5"},
	{"version +5 literal", "/blog/covers/abc/+5"},
	{"version -0", "/blog/covers/abc/-0"},
	{"version -5", "/blog/covers/abc/-5"},
	{"version leading space", "/blog/covers/abc/%205"},
	{"version trailing space", "/blog/covers/abc/5%20"},
	{"version NUL", "/blog/covers/abc/%00"},
	{"version invalid UTF-8", "/blog/covers/abc/%ff"},
	{"version trailing newline", "/blog/covers/abc/5%0A"},
}

func TestBlogRead_EveryNotFoundIsIdentical(t *testing.T) {
	type tc struct {
		name, path string
		reaches    bool
		answer     http.HandlerFunc
	}
	cases := []tc{
		{"upstream 404 with a body", "/blog/posts/abc", true, blogAnswers(http.StatusNotFound, "application/json", `{"error":"no such post"}`)},
		{"upstream 404 with no body", "/blog/posts/abc", true, blogAnswers(http.StatusNotFound, "", "")},
		{"upstream 404 on a cover", "/blog/covers/abc/17", true, blogAnswers(http.StatusNotFound, "text/plain", "stale version")},
	}
	for _, m := range blogMalformed {
		cases = append(cases, tc{name: m.name, path: m.path})
	}

	var ref blogAnswer
	for i, c := range cases {
		rig := newBlogRig(t, blogCfg{})
		if c.answer != nil {
			rig.up.setAnswer(c.answer)
		}
		rec := rig.get(c.path, blogIP)
		got := snapshotBlog(rec)
		if want := map[bool]int{true: 1, false: 0}[c.reaches]; rig.up.count() != want {
			t.Errorf("%s: upstream calls = %d, want %d", c.name, rig.up.count(), want)
		}
		if i == 0 {
			ref = got
			if got.code != http.StatusNotFound {
				t.Fatalf("%s: status = %d, want 404: %s", c.name, got.code, got.body)
			}
			var body map[string]string
			if err := json.Unmarshal([]byte(got.body), &body); err != nil || !reflect.DeepEqual(body, map[string]string{"error": "not found"}) {
				t.Errorf("uniform 404 body = %q, want {\"error\":\"not found\"}", got.body)
			}
			for h, want := range map[string]string{
				"Content-Type":           "application/json",
				"Cache-Control":          "public, max-age=60",
				"X-Content-Type-Options": "nosniff",
			} {
				if g := got.header.Get(h); g != want {
					t.Errorf("uniform 404 %s = %q, want %q", h, g, want)
				}
			}
			if !slices.Contains(got.header.Values("Vary"), "Origin") {
				t.Errorf("uniform 404 Vary = %v, want Origin", got.header.Values("Vary"))
			}
			continue
		}
		if !reflect.DeepEqual(got, ref) {
			t.Errorf("%s: answer %+v differs from the upstream-404 answer %+v", c.name, got, ref)
		}
	}
}

func TestBlogRead_MalformedPathValuesNeverReachUpstream(t *testing.T) {
	rig := newBlogRig(t, blogCfg{})
	for _, m := range blogMalformed {
		if rec := rig.get(m.path, blogIP); rec.Code != http.StatusNotFound {
			t.Errorf("%s: GET %s = %d, want 404", m.name, m.path, rec.Code)
		}
	}

	// Raw values the mux cannot deliver (a dot segment is cleaned away before routing).
	for _, v := range []string{"..", ".", "", "a/b", "\x00", "\xff", "abc\n", " abc", "-abc ", strings.Repeat("a", 81), "a_b"} {
		if rec := rig.direct(rig.routes.Post, blogIP, "slug", v); rec.Code != http.StatusNotFound {
			t.Errorf("slug %q = %d, want 404", v, rec.Code)
		}
		if rec := rig.direct(rig.routes.Cover, blogIP, "id", v, "version", "17"); rec.Code != http.StatusNotFound {
			t.Errorf("cover id %q = %d, want 404", v, rec.Code)
		}
	}
	for _, v := range []string{"0", "01", "007", "", "+5", "-0", "-5", " 5", "5 ", "5\n", "1e3", "0x10", "12345678901234567890", "\x00", "\xff", "٣"} {
		if rec := rig.direct(rig.routes.Cover, blogIP, "id", "abc", "version", v); rec.Code != http.StatusNotFound {
			t.Errorf("version %q = %d, want 404", v, rec.Code)
		}
	}
	if n := rig.up.count(); n != 0 {
		t.Fatalf("upstream calls = %d after malformed values, want 0", n)
	}

	// Positive pair: the canonical forms are forwarded.
	for _, p := range []string{"/blog/posts/" + strings.Repeat("a", 80), "/blog/posts/a-1", "/blog/covers/abc/1", "/blog/covers/abc/1760000000123456"} {
		if rec := rig.get(p, blogIP); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, rec.Code)
		}
	}
	if n := rig.up.count(); n != 4 {
		t.Errorf("upstream calls = %d after four canonical requests, want 4", n)
	}
}

func TestBlogRead_UpstreamFailureIs502(t *testing.T) {
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the gateway followed an upstream redirect")
	}))
	defer trap.Close()

	type tc struct {
		name, path string
		status     string // the status attribute of the WARN
		setup      func(t *testing.T, rig *blogRig)
		cfg        blogCfg
	}
	answer := func(status int) func(*testing.T, *blogRig) {
		return func(_ *testing.T, rig *blogRig) {
			rig.up.setAnswer(blogAnswers(status, "application/json", `{"error":"x"}`))
		}
	}
	cases := []tc{
		{name: "500 on categories", path: "/blog/categories", status: "500", setup: answer(500)},
		{name: "500 on posts", path: "/blog/posts", status: "500", setup: answer(500)},
		{name: "500 on a post", path: "/blog/posts/abc", status: "500", setup: answer(500)},
		{name: "500 on a cover", path: "/blog/covers/abc/17", status: "500", setup: answer(500)},
		{name: "401 on a post", path: "/blog/posts/abc", status: "401", setup: answer(401)},
		{name: "204 on a post", path: "/blog/posts/abc", status: "204", setup: answer(204)},
		{name: "404 on the category list", path: "/blog/categories", status: "404", setup: answer(404)},
		{name: "404 on the post list", path: "/blog/posts", status: "404", setup: answer(404)},
		{name: "302 on posts", path: "/blog/posts", status: "302", setup: func(_ *testing.T, rig *blogRig) {
			rig.up.setAnswer(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, trap.URL, http.StatusFound) })
		}},
		{name: "302 on a cover", path: "/blog/covers/abc/17", status: "302", setup: func(_ *testing.T, rig *blogRig) {
			rig.up.setAnswer(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, trap.URL, http.StatusFound) })
		}},
		{name: "upstream closed", path: "/blog/posts", status: "0", setup: func(_ *testing.T, rig *blogRig) { rig.up.srv.Close() }},
		{name: "upstream slower than the client timeout", path: "/blog/posts", status: "0",
			cfg: blogCfg{client: &http.Client{Timeout: 50 * time.Millisecond}},
			setup: func(_ *testing.T, rig *blogRig) {
				rig.up.setAnswer(func(w http.ResponseWriter, r *http.Request) {
					select {
					case <-time.After(200 * time.Millisecond):
					case <-r.Context().Done():
					}
				})
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newBlogRig(t, c.cfg)
			c.setup(t, rig)
			rec := rig.get(c.path, blogIP)
			requireJSONError(t, rec, http.StatusBadGateway, "blog is unavailable")
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			ws := rig.warns("blog-read: upstream failed")
			if len(ws) != 1 {
				t.Fatalf("upstream-failed WARNs = %d (%v), want 1", len(ws), ws)
			}
			if got := ws[0].attrs["status"]; got != c.status {
				t.Errorf("WARN status = %q, want %q", got, c.status)
			}
			if all := rig.logs.all(); len(all) != 1 {
				t.Errorf("log lines = %d, want only the one WARN: %v", len(all), all)
			}
		})
	}
}

func TestBlogRead_BodyOverTheCapIs502(t *testing.T) {
	big := func(n int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(bytes.Repeat([]byte("a"), n))
		}
	}
	for _, path := range []string{"/blog/posts", "/blog/covers/abc/17"} {
		rig := newBlogRig(t, blogCfg{})
		rig.up.setAnswer(big(maxBlogBodyBytes + 1))
		requireJSONError(t, rig.get(path, blogIP), http.StatusBadGateway, "blog is unavailable")
	}

	// Positive pair: a body of exactly the cap is served whole.
	rig := newBlogRig(t, blogCfg{})
	rig.up.setAnswer(big(maxBlogBodyBytes))
	rec := rig.get("/blog/posts", blogIP)
	if rec.Code != http.StatusOK || rec.Body.Len() != maxBlogBodyBytes {
		t.Errorf("body of exactly the cap = %d with %d bytes, want 200 with %d", rec.Code, rec.Body.Len(), maxBlogBodyBytes)
	}
}

func TestBlogRead_VaryOriginWithoutAnOrigin(t *testing.T) {
	for _, path := range []string{"/blog/categories", "/blog/posts", "/blog/posts/abc"} {
		rig := newBlogRig(t, blogCfg{})
		rec := rig.get(path, blogIP)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		n := 0
		for _, v := range rec.Header().Values("Vary") {
			for tok := range strings.SplitSeq(v, ",") {
				if strings.EqualFold(strings.TrimSpace(tok), "Origin") {
					n++
				}
			}
		}
		if n != 1 {
			t.Errorf("GET %s: Vary %v names Origin %d times, want once", path, rec.Header().Values("Vary"), n)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
	}
}

func TestBlogRead_PerIPLimitSpansTheFourRoutes(t *testing.T) {
	rig := newBlogRig(t, blogCfg{})
	rig.spend(t, blogIP, BlogReadPerIP)
	if n := rig.up.count(); n != BlogReadPerIP {
		t.Fatalf("upstream calls = %d, want %d", n, BlogReadPerIP)
	}
	for _, path := range blogPaths {
		rec := rig.get(path, blogIP)
		requireJSONError(t, rec, http.StatusTooManyRequests, "too many requests")
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, got)
		}
	}
	if n := rig.up.count(); n != BlogReadPerIP {
		t.Errorf("upstream calls = %d after the refusals, want %d", n, BlogReadPerIP)
	}
}

func TestBlogRead_OtherIPAndNextWindowPass(t *testing.T) {
	rig := newBlogRig(t, blogCfg{})
	rig.spend(t, blogIP, BlogReadPerIP)
	if rec := rig.get("/blog/posts", blogIP); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the budget = %d, want 429", rec.Code)
	}
	if rec := rig.get("/blog/posts", "198.51.100.9:4000"); rec.Code != http.StatusOK {
		t.Errorf("another IP = %d, want 200", rec.Code)
	}
	rig.clk.Advance(BlogReadWindow - time.Nanosecond)
	if rec := rig.get("/blog/posts", blogIP); rec.Code != http.StatusTooManyRequests {
		t.Errorf("1ns before the window ends = %d, want 429", rec.Code)
	}
	rig.clk.Advance(time.Nanosecond)
	if rec := rig.get("/blog/posts", blogIP); rec.Code != http.StatusOK {
		t.Errorf("at the window end = %d, want 200", rec.Code)
	}
}

func TestBlogRead_IPv6SubnetSharesOneBudget(t *testing.T) {
	rig := newBlogRig(t, blogCfg{})
	rig.spend(t, "[2001:db8::1]:4000", BlogReadPerIP)
	if rec := rig.get("/blog/posts", "[2001:db8::2]:4000"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("same /64 = %d, want 429", rec.Code)
	}
	if rec := rig.get("/blog/posts", "[2001:db8:0:1::1]:4000"); rec.Code != http.StatusOK {
		t.Errorf("another /64 = %d, want 200", rec.Code)
	}
}

func TestBlogRead_LimitIsSpentBeforeThePathCheck(t *testing.T) {
	rig := newBlogRig(t, blogCfg{})
	rig.spend(t, blogIP, BlogReadPerIP)
	for _, p := range []string{"/blog/posts/Bad_Slug", "/blog/covers/abc/0", "/blog/covers/abc/%2B5", "/blog/posts/%00"} {
		if rec := rig.get(p, blogIP); rec.Code != http.StatusTooManyRequests {
			t.Errorf("GET %s over the budget = %d, want 429, not 404", p, rec.Code)
		}
	}

	// A malformed request spends budget: a scanner cannot probe for free.
	const other = "198.51.100.9:4000"
	for range BlogReadPerIP {
		if rec := rig.get("/blog/posts/Bad_Slug", other); rec.Code != http.StatusNotFound {
			t.Fatalf("malformed within the budget = %d, want 404", rec.Code)
		}
	}
	if rec := rig.get("/blog/posts", other); rec.Code != http.StatusTooManyRequests {
		t.Errorf("valid request after %d malformed ones = %d, want 429", BlogReadPerIP, rec.Code)
	}
}

func TestBlogRead_UnenforcedLimitLogsAndForwards(t *testing.T) {
	rig := newBlogRig(t, blogCfg{unenforced: true})
	rig.spend(t, blogIP, BlogReadPerIP)
	for range 3 {
		if rec := rig.get("/blog/posts", blogIP); rec.Code != http.StatusOK {
			t.Fatalf("over the budget, unenforced = %d, want 200", rec.Code)
		}
	}
	if n := rig.up.count(); n != BlogReadPerIP+3 {
		t.Errorf("upstream calls = %d, want %d", n, BlogReadPerIP+3)
	}
	ws := rig.warns("blog-read: limit reached")
	if len(ws) != 1 {
		t.Fatalf("limit WARNs = %d, want 1 (once a minute)", len(ws))
	}
	for k, want := range map[string]string{"enforced": "false", "limit": "ip", "key_source": "remote_addr"} {
		if got := ws[0].attrs[k]; got != want {
			t.Errorf("WARN %s = %q, want %q", k, got, want)
		}
	}
	for _, rec := range rig.logs.all() {
		if strings.Contains(rec.text, "203.0.113.7") {
			t.Errorf("a log line holds the client IP: %s", rec.text)
		}
	}
}

func TestBlogRead_FullMapAdmitsNewVisitors(t *testing.T) {
	rig := newBlogRig(t, blogCfg{max: 3, maxKeys: 2})
	const a, b, c = "203.0.113.7:4000", "198.51.100.9:4000", "192.0.2.33:4000"
	rig.spend(t, a, 3)
	rig.spend(t, b, 1)
	for i := range 5 {
		if rec := rig.get("/blog/posts", c); rec.Code != http.StatusOK {
			t.Fatalf("new visitor %d on a full map = %d, want 200", i+1, rec.Code)
		}
	}
	if rec := rig.get("/blog/posts", a); rec.Code != http.StatusTooManyRequests {
		t.Errorf("held visitor over its budget on a full map = %d, want 429", rec.Code)
	}
	if rec := rig.get("/blog/posts", b); rec.Code != http.StatusOK {
		t.Errorf("held visitor within its budget on a full map = %d, want 200", rec.Code)
	}
}

func TestBlogRead_LimitWarnIsAtMostOncePerMinute(t *testing.T) {
	const msg = "blog-read: limit reached"
	rig := newBlogRig(t, blogCfg{max: 3})
	rig.spend(t, blogIP, 3)
	if rec := rig.get("/blog/posts", blogIP); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("first refusal = %d, want 429", rec.Code)
	}
	if n := len(rig.warns(msg)); n != 1 {
		t.Fatalf("WARNs after the first refusal = %d, want 1", n)
	}
	for i := range 50 {
		if rec := rig.get(blogPaths[i%len(blogPaths)], blogIP); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("refusal %d = %d, want 429", i+2, rec.Code)
		}
	}
	if n := len(rig.warns(msg)); n != 1 {
		t.Errorf("WARNs after 51 refusals in one minute = %d, want 1", n)
	}
	rig.clk.Advance(59 * time.Second)
	rig.get("/blog/posts", blogIP)
	if n := len(rig.warns(msg)); n != 1 {
		t.Errorf("WARNs 59s after the first = %d, want 1", n)
	}
	rig.clk.Advance(time.Second)
	rig.get("/blog/posts", blogIP)
	if n := len(rig.warns(msg)); n != 2 {
		t.Errorf("WARNs 60s after the first = %d, want 2", n)
	}
}

func TestBlogRead_OnlyGetAndHeadAreReads(t *testing.T) {
	rig := newBlogRig(t, blogCfg{})
	for _, path := range blogPaths {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
			rec := rig.do(m, path, blogIP)
			requireJSONError(t, rec, http.StatusMethodNotAllowed, "method not allowed")
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("%s %s: Allow = %q, want \"GET, HEAD\"", m, path, got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s %s: Cache-Control = %q, want no-store", m, path, got)
			}
		}
	}
	if n := rig.up.count(); n != 0 {
		t.Fatalf("upstream calls = %d after non-reads, want 0", n)
	}

	// The 20 refusals spent nothing: the full budget is still there.
	rig.spend(t, blogIP, BlogReadPerIP)
	if rec := rig.get("/blog/posts", blogIP); rec.Code != http.StatusTooManyRequests {
		t.Errorf("request %d = %d, want 429", BlogReadPerIP+1, rec.Code)
	}
}

func TestBlogRead_HeadIsARead(t *testing.T) {
	rig := newBlogRig(t, blogCfg{})
	rec := rig.do(http.MethodHead, "/blog/posts", blogIP)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD /blog/posts = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("HEAD Cache-Control = %q, want public, max-age=60", got)
	}
	if n := rig.up.count(); n > 1 {
		t.Errorf("upstream calls = %d for one HEAD, want at most 1", n)
	}
}
