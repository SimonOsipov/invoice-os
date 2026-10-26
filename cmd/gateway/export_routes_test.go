package main

import (
	"go/ast"
	"go/types"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// func main boots platform.New and cannot run in a test, so a scan reads its export-route wiring.
// Both routes share one store with handoffHandlers; the code route sits behind CORS, verifier and session check;
// the form route has no CORS and gets the /api/ handler.
func TestExportRoutesWiring(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	sites, stmts := mainRoutes(t, src)
	// Control: a known CORS-wrapped registration, so the scan can tell wrapped from bare.
	if s := sitesFor(sites, "POST /auth/console-handoff"); len(s) != 1 || s[0].handler != "withCORS(h.ConsoleHandoff)" {
		t.Fatalf("control: POST /auth/console-handoff = %+v, want withCORS(h.ConsoleHandoff); the scan went blind", s)
	}

	bound := map[string]ast.Expr{} // local name -> its right-hand side
	var storeName, apiName, handoffStoreArg string
	for _, s := range stmts {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		switch types.ExprString(call.Fun) {
		case "handoffStore":
			storeName = types.ExprString(as.Lhs[0])
		case "gatewayHandlers":
			apiName = types.ExprString(as.Lhs[0])
		case "handoffHandlers":
			if len(call.Args) == 7 {
				handoffStoreArg = types.ExprString(call.Args[6])
			}
		}
		if len(as.Lhs) == 1 {
			bound[types.ExprString(as.Lhs[0])] = as.Rhs[0]
		}
	}
	if storeName == "" || apiName == "" {
		t.Fatalf("main binds no `store := handoffStore(...)` (%q) or `api, _ := gatewayHandlers(...)` (%q)", storeName, apiName)
	}
	if handoffStoreArg != storeName {
		t.Errorf("handoffHandlers gets store %q, want the one export routes share: %q", handoffStoreArg, storeName)
	}

	one := func(pattern string) routeSite {
		s := sitesFor(sites, pattern)
		if len(s) != 1 || !s[0].topLevel {
			t.Fatalf("%s sites = %+v, want exactly one top-level registration", pattern, s)
		}
		return s[0]
	}

	// withCORS(<local>), local = verifier.Middleware(sessions.Middleware(ExportCodeHandler(store, ...))).
	for _, pattern := range []string{"POST /auth/export-code", "OPTIONS /auth/export-code"} {
		h := one(pattern).handler
		if !strings.HasPrefix(h, "withCORS(") || !strings.HasSuffix(h, ")") {
			t.Fatalf("%s handler = %s, want withCORS(<export code chain>)", pattern, h)
		}
		local := strings.TrimSuffix(strings.TrimPrefix(h, "withCORS("), ")")
		rhs, ok := bound[local]
		if !ok {
			t.Fatalf("%s handler local %q is not bound at the top level of main", pattern, local)
		}
		v, ok := rhs.(*ast.CallExpr)
		if !ok || types.ExprString(v.Fun) != "verifier.Middleware" || len(v.Args) != 1 {
			t.Fatalf("%s chain = %s, want verifier.Middleware(...) outermost", pattern, types.ExprString(rhs))
		}
		sess, ok := v.Args[0].(*ast.CallExpr)
		if !ok || types.ExprString(sess.Fun) != "sessions.Middleware" || len(sess.Args) != 1 {
			t.Fatalf("%s chain = %s, want sessions.Middleware(...) directly inside the verifier", pattern, types.ExprString(rhs))
		}
		mint, ok := sess.Args[0].(*ast.CallExpr)
		if !ok || types.ExprString(mint.Fun) != "gateway.ExportCodeHandler" || len(mint.Args) < 1 || types.ExprString(mint.Args[0]) != storeName {
			t.Fatalf("%s chain = %s, want gateway.ExportCodeHandler(%s, ...) innermost", pattern, types.ExprString(rhs), storeName)
		}
	}

	dl := one("POST /auth/export-download")
	if want := "gateway.ExportDownloadHandler(" + storeName + ", " + apiName + ", app.Logger)"; dl.handler != want {
		t.Errorf("POST /auth/export-download handler = %s, want %s (no CORS wrap)", dl.handler, want)
	}
	if got := sitesFor(sites, "OPTIONS /auth/export-download"); len(got) != 0 {
		t.Errorf("OPTIONS /auth/export-download is registered %+v; a form navigation needs no preflight", got)
	}
}

// main hands handoffStore's result straight to the export handlers; a nil Handoffs panics them on the first request.
func TestHandoffStore_WithoutAPoolIsUsableByTheExportRoutes(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	store := handoffStore(nil, log)
	if store == nil {
		t.Fatal("handoffStore(nil) = nil; main passes it to ExportCodeHandler and ExportDownloadHandler, which then panic (auth.md: an unset DATABASE_GATEWAY_URL keeps the in-process store)")
	}
	form := url.Values{"code": {"x"}, "state": {handoffState}}
	r := httptest.NewRequest(http.MethodPost, "/auth/export-download", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	gateway.ExportDownloadHandler(store, http.NotFoundHandler(), log).ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("export-download on a poolless store = %d, want 400", rec.Code)
	}
}
