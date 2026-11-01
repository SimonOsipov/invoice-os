package main

import (
	"go/ast"
	"go/types"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

func TestBlogSubscribeRoutesRegisteredOutsideAPI(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	sites, stmts := mainRoutes(t, src)
	if s := sitesFor(sites, "POST /contacts/demo-request"); len(s) != 1 || !s[0].topLevel {
		t.Fatalf("control: POST /contacts/demo-request = %+v, want one top-level registration; the scan went blind", s)
	}

	recv, calls := "", 0
	for _, s := range stmts {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || types.ExprString(call.Fun) != "blogSubscribeHandlers" {
			continue
		}
		calls++
		recv = types.ExprString(as.Lhs[0])
		var args []string
		for _, a := range call.Args {
			args = append(args, types.ExprString(a))
		}
		// The pool argument is pinned by TestMain_PassesTheGatewayPoolToEverySharedState.
		want := []string{`routed["notifications"]`, "gatewayToken", "registerMinResponse", "app.Logger"}
		if len(args) != len(want)+1 || !reflect.DeepEqual(args[:len(want)], want) {
			t.Errorf("blogSubscribeHandlers args = %v, want %v and a pool", args, want)
		}
	}
	if calls != 1 {
		t.Fatalf("main calls blogSubscribeHandlers %d times at the top level, want once", calls)
	}

	want := map[string]string{
		"POST /blog/subscriptions":            "withCORS(" + recv + ".Subscribe)",
		"OPTIONS /blog/subscriptions":         "withCORS(" + recv + ".Subscribe)",
		"POST /blog/subscriptions/confirm":    "withCORS(" + recv + ".Confirm)",
		"OPTIONS /blog/subscriptions/confirm": "withCORS(" + recv + ".Confirm)",
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
	for _, s := range sites {
		if strings.Contains(s.pattern, "/blog/subscriptions") {
			if _, ok := want[s.pattern]; !ok {
				t.Errorf("unexpected subscription pattern %q", s.pattern)
			}
			if strings.Contains(s.pattern, "/api/") {
				t.Errorf("subscription pattern %q is under /api/", s.pattern)
			}
		}
	}
}

func TestBlogSubscribePreflightGrantsTheBlogOrigin(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	var calls atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(stub.Close)
	up, _ := url.Parse(stub.URL)
	sub := blogSubscribeHandlers(up, "gw-token", time.Millisecond, slog.New(slog.DiscardHandler), nil)
	withCORS := gateway.CORS([]string{blogAllowedOrigin})
	mux := http.NewServeMux()
	mux.Handle("POST /blog/subscriptions", withCORS(sub.Subscribe))
	mux.Handle("OPTIONS /blog/subscriptions", withCORS(sub.Subscribe))
	mux.Handle("POST /blog/subscriptions/confirm", withCORS(sub.Confirm))
	mux.Handle("OPTIONS /blog/subscriptions/confirm", withCORS(sub.Confirm))

	for _, p := range []string{"/blog/subscriptions", "/blog/subscriptions/confirm"} {
		rec := preflight(mux, p, blogAllowedOrigin)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != blogAllowedOrigin {
			t.Errorf("preflight %s from the blog origin = %d, grant %q, want 204 with the grant", p, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
		}
		rec = preflight(mux, p, "https://evil.example")
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("preflight %s from another origin carries a grant %q", p, got)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a preflight reached upstream %d times", n)
	}

	// Positive pair: the POST carries the grant too.
	rec := postJSON(mux, "/blog/subscriptions", blogAllowedOrigin, `{"email":"a@x.co","category_ids":["a"],"frequency":"weekly"}`)
	if rec.Code != http.StatusAccepted || calls.Load() != 1 {
		t.Fatalf("POST /blog/subscriptions = %d with %d upstream calls, want 202 with 1", rec.Code, calls.Load())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != blogAllowedOrigin {
		t.Errorf("POST answer lacks the blog origin grant")
	}

	// AC-6: a GET (a mail scanner's prefetch) to the confirm path confirms nothing.
	before := calls.Load()
	req := httptest.NewRequest(http.MethodGet, "/blog/subscriptions/confirm?token="+strings.Repeat("a", 43), nil)
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, req)
	if get.Code != http.StatusMethodNotAllowed || calls.Load() != before {
		t.Errorf("GET confirm = %d, upstream calls %d -> %d, want 405 and no call", get.Code, before, calls.Load())
	}
}

func TestBlogSubscribePreviewPostureAdmits(t *testing.T) {
	var calls atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(stub.Close)
	up, _ := url.Parse(stub.URL)
	send := func(env string, n int) int32 {
		t.Setenv("DEPLOY_ENV", env)
		calls.Store(0)
		sub := blogSubscribeHandlers(up, "gw-token", 0, slog.New(slog.DiscardHandler), nil)
		for range n {
			req := httptest.NewRequest(http.MethodPost, "/blog/subscriptions",
				strings.NewReader(`{"email":"p@x.co","category_ids":["a"],"frequency":"weekly"}`))
			sub.Subscribe.ServeHTTP(httptest.NewRecorder(), req)
		}
		return calls.Load()
	}
	if got := send("production", 5); got != 3 {
		t.Fatalf("hosted posture forwarded %d of 5, want 3 (the cap enforces)", got)
	}
	if got := send("nightly", 5); got != 3 {
		t.Errorf("nightly forwarded %d of 5, want 3 (the cap enforces)", got)
	}
	if got := send("ascomply-pr-1", 5); got != 5 {
		t.Errorf("PR fork forwarded %d of 5, want 5 (the cap logs and admits)", got)
	}
}
