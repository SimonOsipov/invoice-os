package main

import (
	"context"
	"go/ast"
	"go/types"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

const blogAllowedOrigin = "https://blog.example"

// blogUpstreamServer answers every call 200 JSON and counts the calls.
func blogUpstreamServer(t *testing.T) (*url.URL, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u, &calls
}

// blogMux mounts the real blog handlers behind the CORS allow-list on the patterns main registers.
func blogMux(t *testing.T, log *slog.Logger) (*http.ServeMux, *atomic.Int32) {
	t.Helper()
	up, calls := blogUpstreamServer(t)
	blogRead := blogHandlers(up, "gw-token", log)
	withCORS := gateway.CORS([]string{blogAllowedOrigin})
	mux := http.NewServeMux()
	mux.Handle("GET /blog/categories", withCORS(blogRead.Categories))
	mux.Handle("OPTIONS /blog/categories", withCORS(blogRead.Categories))
	mux.Handle("GET /blog/posts", withCORS(blogRead.Posts))
	mux.Handle("OPTIONS /blog/posts", withCORS(blogRead.Posts))
	mux.Handle("GET /blog/posts/{slug}", withCORS(blogRead.Post))
	mux.Handle("OPTIONS /blog/posts/{slug}", withCORS(blogRead.Post))
	mux.Handle("GET /blog/covers/{id}/{version}", blogRead.Cover)
	mux.Handle("GET /blog/posts/", withCORS(blogRead.NotFound))
	mux.Handle("GET /blog/covers/", blogRead.NotFound)
	return mux, calls
}

func blogGet(h http.Handler, path, remoteAddr, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remoteAddr
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBlogRoutesRegisteredOutsideAPI(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if buildConstrained(src) {
		t.Fatal("main.go carries a build constraint; its routes are not in every build")
	}
	sites, stmts := mainRoutes(t, src)
	if s := sitesFor(sites, "POST /contacts/demo-request"); len(s) != 1 || !s[0].topLevel {
		t.Fatalf("control: POST /contacts/demo-request = %+v, want one top-level registration; the scan went blind", s)
	}

	recv, corsLocal := "", false
	for _, s := range stmts {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		switch types.ExprString(call.Fun) {
		case "gateway.CORS":
			corsLocal = corsLocal || types.ExprString(as.Lhs[0]) == "withCORS"
		case "blogHandlers":
			var args []string
			for _, a := range call.Args {
				args = append(args, types.ExprString(a))
			}
			if want := []string{`routed["notifications"]`, "gatewayToken", "app.Logger"}; !reflect.DeepEqual(args, want) {
				t.Errorf("blogHandlers args = %v, want %v", args, want)
			}
			if recv != "" {
				t.Error("main calls blogHandlers more than once at the top level")
			}
			recv = types.ExprString(as.Lhs[0])
		}
	}
	if !corsLocal {
		t.Fatal("main has no top-level `withCORS := gateway.CORS(...)`")
	}
	if recv == "" {
		t.Fatal("main has no top-level `x := blogHandlers(routed[\"notifications\"], gatewayToken, app.Logger)`")
	}

	want := map[string]string{
		"GET /blog/categories":            "withCORS(" + recv + ".Categories)",
		"OPTIONS /blog/categories":        "withCORS(" + recv + ".Categories)",
		"GET /blog/posts":                 "withCORS(" + recv + ".Posts)",
		"OPTIONS /blog/posts":             "withCORS(" + recv + ".Posts)",
		"GET /blog/posts/{slug}":          "withCORS(" + recv + ".Post)",
		"OPTIONS /blog/posts/{slug}":      "withCORS(" + recv + ".Post)",
		"GET /blog/covers/{id}/{version}": recv + ".Cover",
		"GET /blog/posts/":                "withCORS(" + recv + ".NotFound)",
		"GET /blog/covers/":               recv + ".NotFound",
	}
	for pattern, handler := range want {
		s := sitesFor(sites, pattern)
		if len(s) != 1 {
			t.Errorf("%s registered %d times, want once", pattern, len(s))
			continue
		}
		if !s[0].topLevel {
			t.Errorf("%s is registered under a condition; it must be in every build", pattern)
		}
		if s[0].handler != handler {
			t.Errorf("%s handler = %s, want %s", pattern, s[0].handler, handler)
		}
	}

	// These are the only blog routes: the cover has no preflight, nothing sits under /api/.
	n := 0
	for _, s := range sites {
		if strings.Contains(s.pattern, "/blog/subscriptions") {
			continue // inventoried by TestBlogSubscribeRoutesRegisteredOutsideAPI
		}
		if strings.Contains(s.pattern, "/blog") && !strings.Contains(s.pattern, "/v1/staff/") {
			n++
			if _, ok := want[s.pattern]; !ok {
				t.Errorf("unexpected blog pattern %q", s.pattern)
			}
		}
		if strings.Contains(s.pattern, "/api/") && strings.Contains(s.pattern, "blog") {
			t.Errorf("blog pattern %q is under /api/", s.pattern)
		}
	}
	if n != len(want) {
		t.Errorf("main registers %d blog patterns, want %d", n, len(want))
	}
}

func TestBlogRoutes_PreflightGrantsAnAllowedOrigin(t *testing.T) {
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
	mux, calls := blogMux(t, slog.New(slog.DiscardHandler))

	rec := preflight(mux, "/blog/posts", blogAllowedOrigin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight from the allowed origin = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != blogAllowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, blogAllowedOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodGet) {
		t.Errorf("Access-Control-Allow-Methods = %q, want GET granted", got)
	}

	rec = preflight(mux, "/blog/posts", "https://evil.example")
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight from a disallowed origin = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("preflight from a disallowed origin carries Access-Control-Allow-Origin = %q, want none", got)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a preflight reached the upstream %d times, want 0", n)
	}

	// Positive pair: the read itself carries the grant, on a hit and on the uniform 404.
	for _, p := range []string{"/blog/categories", "/blog/posts", "/blog/posts/abc"} {
		rec := blogGet(mux, p, "203.0.113.7:4000", blogAllowedOrigin)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200: %s", p, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != blogAllowedOrigin {
			t.Errorf("GET %s Access-Control-Allow-Origin = %q, want %q", p, got, blogAllowedOrigin)
		}
	}
	rec = blogGet(mux, "/blog/posts/Bad_Slug", "203.0.113.7:4000", blogAllowedOrigin)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Access-Control-Allow-Origin") != blogAllowedOrigin {
		t.Errorf("uniform 404 = %d with grant %q, want 404 readable from the blog origin", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

// blogLimitWarns keeps the enforced attribute of each limit WARN.
type blogLimitWarns struct {
	mu       *sync.Mutex
	enforced *[]string
}

func (h blogLimitWarns) Enabled(context.Context, slog.Level) bool { return true }
func (h blogLimitWarns) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h blogLimitWarns) WithGroup(string) slog.Handler            { return h }
func (h blogLimitWarns) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "blog-read: limit reached" {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "enforced" {
			h.mu.Lock()
			*h.enforced = append(*h.enforced, a.Value.String())
			h.mu.Unlock()
		}
		return true
	})
	return nil
}

func TestBlogHandlers_PostureTable(t *testing.T) {
	for _, c := range []struct {
		env     string
		refused bool
	}{
		{"pr-7", false},
		{"production", true},
		{"development", true},
		{"", true},
		{"pr-", true},
		{"PR-7", true},
	} {
		t.Run("env="+c.env, func(t *testing.T) {
			t.Setenv("RAILWAY_ENVIRONMENT_NAME", c.env)
			var mu sync.Mutex
			var enforced []string
			up, calls := blogUpstreamServer(t)
			routes := blogHandlers(up, "gw-token", slog.New(blogLimitWarns{&mu, &enforced}))
			for i := range gateway.BlogReadPerIP {
				if rec := blogGet(routes.Posts, "/blog/posts", "203.0.113.7:4000", ""); rec.Code != http.StatusOK {
					t.Fatalf("request %d within the budget = %d, want 200", i+1, rec.Code)
				}
			}
			rec := blogGet(routes.Posts, "/blog/posts", "203.0.113.7:4000", "")
			wantCode, wantCalls, wantEnforced := http.StatusOK, int32(gateway.BlogReadPerIP+1), "false"
			if c.refused {
				wantCode, wantCalls, wantEnforced = http.StatusTooManyRequests, int32(gateway.BlogReadPerIP), "true"
			}
			if rec.Code != wantCode {
				t.Errorf("request %d = %d, want %d", gateway.BlogReadPerIP+1, rec.Code, wantCode)
			}
			if n := calls.Load(); n != wantCalls {
				t.Errorf("upstream saw %d calls, want %d", n, wantCalls)
			}
			if !reflect.DeepEqual(enforced, []string{wantEnforced}) {
				t.Errorf("limit WARN enforced = %v, want [%s]", enforced, wantEnforced)
			}
		})
	}
}

func TestBlogRoutes_OptionsWithoutOriginIsNotARead(t *testing.T) {
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
	mux, calls := blogMux(t, slog.New(slog.DiscardHandler))

	for _, p := range []string{"/blog/categories", "/blog/posts", "/blog/posts/abc"} {
		req := httptest.NewRequest(http.MethodOptions, p, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("OPTIONS %s with no Origin = %d, want 405: %s", p, rec.Code, rec.Body.String())
		}
	}
	// The cover route has no preflight: a browser loads it as an image.
	for _, origin := range []string{"", blogAllowedOrigin} {
		req := httptest.NewRequest(http.MethodOptions, "/blog/covers/abc/17", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("OPTIONS cover with Origin %q = %d, want 405", origin, rec.Code)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(m, "/blog/posts", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /blog/posts = %d, want 405", m, rec.Code)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a non-read reached the upstream %d times, want 0", n)
	}

	// Positive pair: the GET is forwarded.
	if rec := blogGet(mux, "/blog/posts", "203.0.113.7:4000", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /blog/posts = %d, want 200", rec.Code)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("upstream saw %d calls after one GET, want 1", n)
	}
}

func TestBlogHandlers_HaveTheirOwnBucket(t *testing.T) {
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(goTrueAnswering(t, cwSession), site, 0, slog.New(slog.DiscardHandler), &demoRecSink{}, noPendingInvite)
	up, _ := blogUpstreamServer(t)
	routes := blogHandlers(up, "gw-token", slog.New(slog.DiscardHandler))

	const first, second = "203.0.113.7:4000", "198.51.100.9:4000"
	for range gateway.DemoRequestPerIP + 1 {
		demoPost(reg.DemoRequest, first)
	}
	if rec := demoPost(reg.DemoRequest, first); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("demo budget not spent: %d", rec.Code)
	}
	if rec := blogGet(routes.Posts, "/blog/posts", first, ""); rec.Code != http.StatusOK {
		t.Errorf("blog read after the demo budget is spent = %d, want 200", rec.Code)
	}

	for range gateway.BlogReadPerIP {
		blogGet(routes.Posts, "/blog/posts", second, "")
	}
	if rec := blogGet(routes.Posts, "/blog/posts", second, ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blog budget not spent: %d", rec.Code)
	}
	if !reg.RegisterPerIP.Reserve("198.51.100.9") {
		t.Error("spending the blog budget limited register")
	}
	if rec := demoPost(reg.DemoRequest, second); rec.Code != http.StatusAccepted {
		t.Errorf("demo request after the blog budget is spent = %d, want 202", rec.Code)
	}
}

func TestBlogRoutes_VaryOriginIsNamedOnceBehindCORS(t *testing.T) {
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
	mux, _ := blogMux(t, slog.New(slog.DiscardHandler))
	for _, p := range []string{"/blog/categories", "/blog/posts", "/blog/posts/abc", "/blog/posts/Bad_Slug"} {
		rec := blogGet(mux, p, "203.0.113.7:4000", blogAllowedOrigin)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != blogAllowedOrigin {
			t.Fatalf("GET %s: control Access-Control-Allow-Origin = %q, want the grant", p, got)
		}
		n := 0
		for _, v := range rec.Header().Values("Vary") {
			for _, tok := range strings.Split(v, ",") {
				if strings.EqualFold(strings.TrimSpace(tok), "Origin") {
					n++
				}
			}
		}
		if n != 1 {
			t.Errorf("GET %s: Vary %v names Origin %d times, want once", p, rec.Header().Values("Vary"), n)
		}
	}
}
