package main

import (
	"bytes"
	"go/ast"
	"go/types"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

const readinessAllowedOrigin = "https://app.example"

var readinessPaths = []string{"/readiness-check/mapping", "/readiness-check/run"}

// readinessStubs stands in for the worker: the run answers confirm, the press answers sent; every path is counted.
func readinessStubs(t *testing.T) (worker *url.URL, calls func(path string) int) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]int{}
	w := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path]++
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/internal/public-checks/confirm" {
			_, _ = rw.Write([]byte(`{"outcome":"sent"}`))
			return
		}
		_, _ = rw.Write([]byte(`{"status":"confirm"}`))
	}))
	t.Cleanup(w.Close)
	wu, _ := url.Parse(w.URL)
	return wu, func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return seen[path]
	}
}

// The landing's sentence is read from its source: the gateway takes no other.
func landingReadinessConsent(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../frontend/landing/src/readinessConsent.ts")
	if err != nil {
		t.Fatalf("read the landing sentence: %v", err)
	}
	m := regexp.MustCompile(`(?m)^export const READINESS_CONSENT_TEXT =\s*'([^'\\\n]+)'`).FindSubmatch(b)
	if m == nil {
		t.Fatal("readinessConsent.ts: no exported single-quoted READINESS_CONSENT_TEXT literal")
	}
	return string(m[1])
}

func readinessRunRequest(t *testing.T, xff string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("email", "a@x.co")
	_ = mw.WriteField("consent_text", landingReadinessConsent(t))
	fw, _ := mw.CreateFormFile("file", "inv.csv")
	_, _ = fw.Write([]byte("a,b\n1,2\n"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/readiness-check/run", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", readinessAllowedOrigin)
	req.Header.Set("X-Forwarded-For", xff)
	return req
}

func TestGatewayRoutes_ReadinessCheckRoutesAreCORSWrapped(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	wu, _ := readinessStubs(t)
	rt := readinessCheckHandlers(wu, "gw-token", slog.New(slog.DiscardHandler), nil)
	withCORS := gateway.CORS([]string{readinessAllowedOrigin})
	mux := http.NewServeMux()
	mux.Handle("POST /readiness-check/mapping", withCORS(rt.Mapping))
	mux.Handle("OPTIONS /readiness-check/mapping", withCORS(rt.Mapping))
	mux.Handle("POST /readiness-check/run", withCORS(rt.Run))
	mux.Handle("OPTIONS /readiness-check/run", withCORS(rt.Run))
	for _, p := range readinessPaths {
		rec := preflight(mux, p, readinessAllowedOrigin)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != readinessAllowedOrigin {
			t.Errorf("preflight %s = %d, grant %q, want 204 with the grant", p, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, readinessRunRequest(t, "203.0.113.7, 34.1.2.3"))
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != readinessAllowedOrigin {
		t.Fatalf("POST run = %d, grant %q, want 200 with the grant\n%s", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"), rec.Body)
	}

	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	sites, stmts := mainRoutes(t, src)
	recv := ""
	for _, s := range stmts {
		if as, ok := s.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if call, ok := as.Rhs[0].(*ast.CallExpr); ok && types.ExprString(call.Fun) == "readinessCheckHandlers" {
				recv = types.ExprString(as.Lhs[0])
			}
		}
	}
	if recv == "" {
		t.Fatal("main has no top-level `x := readinessCheckHandlers(...)`")
	}
	for _, m := range []string{"POST", "OPTIONS"} {
		for p, field := range map[string]string{"/readiness-check/mapping": "Mapping", "/readiness-check/run": "Run"} {
			s := sitesFor(sites, m+" "+p)
			if want := "withCORS(" + recv + "." + field + ")"; len(s) != 1 || !s[0].topLevel || s[0].handler != want {
				t.Errorf("%s %s = %+v, want one top-level %s", m, p, s, want)
			}
		}
	}
}

// The confirm page is a same-origin form on the gateway: both methods are registered, and no CORS wrapper adds a grant.
func TestReadinessRoutes_ConfirmIsRegisteredWithoutCORS(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	wu, calls := readinessStubs(t)
	rt := readinessCheckHandlers(wu, "gw-token", slog.New(slog.DiscardHandler), nil)
	if rt.Confirm == nil {
		t.Fatal("readinessCheckHandlers returned no Confirm handler")
	}

	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	sites, stmts := mainRoutes(t, src)
	recv := ""
	for _, s := range stmts {
		if as, ok := s.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if call, ok := as.Rhs[0].(*ast.CallExpr); ok && types.ExprString(call.Fun) == "readinessCheckHandlers" {
				recv = types.ExprString(as.Lhs[0])
			}
		}
	}
	if recv == "" {
		t.Fatal("main has no top-level `x := readinessCheckHandlers(...)`")
	}
	for _, m := range []string{"GET", "POST"} {
		s := sitesFor(sites, m+" /readiness-check/confirm")
		if want := recv + ".Confirm"; len(s) != 1 || !s[0].topLevel || s[0].handler != want {
			t.Errorf("%s /readiness-check/confirm = %+v, want one top-level %s with no CORS wrapper", m, s, want)
		}
	}
	if s := sitesFor(sites, "OPTIONS /readiness-check/confirm"); len(s) != 0 {
		t.Errorf("OPTIONS /readiness-check/confirm = %+v, want no preflight route", s)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /readiness-check/confirm", rt.Confirm)
	mux.Handle("POST /readiness-check/confirm", rt.Confirm)
	target := "/readiness-check/confirm?t=" + strings.Repeat("A", 43)
	for _, tc := range []struct {
		method, want string
		sent         int
	}{{http.MethodGet, "Get my results", 0}, {http.MethodPost, "Done.", 1}} {
		req := httptest.NewRequest(tc.method, target, nil)
		req.Header.Set("Origin", readinessAllowedOrigin)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s = %d, want 200 with %q\n%s", tc.method, rec.Code, tc.want, rec.Body)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want none", tc.method, got)
		}
		if n := calls("/internal/public-checks/confirm"); n != tc.sent {
			t.Errorf("after %s: worker confirm calls = %d, want %d", tc.method, n, tc.sent)
		}
	}
}

// Production and nightly count the per-IP limit; a preview logs and admits.
func TestReadinessCheckPosture_EnforcesOffAPreview(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		xffs      []string
		want429   bool
	}{
		{"production", "production", []string{"203.0.113.7, 34.1.2.3"}, true},
		{"nightly", "nightly", []string{"203.0.113.7"}, true},
		{"ascomply-pr-1", "ascomply-pr-1", []string{"203.0.113.7"}, false},
		{"production IPv6 one /64", "production", []string{"2001:db8:1:2::1, 34.1.2.3", "2001:db8:1:2:ffff::9, 34.1.2.3"}, true},
		{"production IPv6 two /64s", "production", []string{"2001:db8:1:2::1, 34.1.2.3", "2001:db8:1:3::1, 34.1.2.3"}, false},
		{"production mapped IPv4", "production", []string{"::ffff:203.0.113.7, 34.1.2.3", "203.0.113.7, 34.1.2.3"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEPLOY_ENV", tc.env)
			wu, _ := readinessStubs(t)
			rt := readinessCheckHandlers(wu, "gw-token", slog.New(slog.DiscardHandler), nil)
			var last int
			for i := 0; i < 11; i++ {
				rec := httptest.NewRecorder()
				rt.Run.ServeHTTP(rec, readinessRunRequest(t, tc.xffs[i%len(tc.xffs)]))
				last = rec.Code
				if i < 10 && last != http.StatusOK {
					t.Fatalf("run %d = %d, want 200", i+1, last)
				}
			}
			if want := map[bool]int{true: http.StatusTooManyRequests, false: http.StatusOK}[tc.want429]; last != want {
				t.Fatalf("11th run = %d, want %d", last, want)
			}
		})
	}
}

func TestGatewayBinary_DoesNotLinkGoPdfium(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "github.com/klippa-app/go-pdfium") {
		t.Fatal("cmd/gateway links go-pdfium")
	}
}
