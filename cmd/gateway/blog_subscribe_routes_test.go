package main

import (
	"go/ast"
	"go/types"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
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
		if want := []string{`routed["notifications"]`, "gatewayToken", "registerMinResponse", "app.Logger"}; !reflect.DeepEqual(args, want) {
			t.Errorf("blogSubscribeHandlers args = %v, want %v", args, want)
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
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
	up, calls := blogUpstreamServer(t)
	sub := blogSubscribeHandlers(up, "gw-token", time.Millisecond, slog.New(slog.DiscardHandler))
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
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadGateway && rec.Code != http.StatusAccepted {
		t.Fatalf("POST /blog/subscriptions = %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != blogAllowedOrigin {
		t.Errorf("POST answer lacks the blog origin grant")
	}
}
