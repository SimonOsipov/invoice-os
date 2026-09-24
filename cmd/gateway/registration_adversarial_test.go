package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestMustParseSiteURL_AcceptsAbsoluteHTTP(t *testing.T) {
	for _, raw := range []string{"https://www.ascomply.com", "http://localhost:5173", "https://site.example/app/"} {
		var buf bytes.Buffer
		u := mustParseSiteURL(raw, slog.New(slog.NewJSONHandler(&buf, nil)))
		if u == nil || u.String() != raw {
			t.Errorf("mustParseSiteURL(%q) = %v, want %q", raw, u, raw)
		}
		if buf.Len() != 0 {
			t.Errorf("mustParseSiteURL(%q) logged %s, want nothing", raw, buf.String())
		}
	}
}

// D4: unset is allowed and logged once, at WARN, naming the variable.
func TestMustParseSiteURL_UnsetIsNilAndLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	if u := mustParseSiteURL("", slog.New(slog.NewJSONHandler(&buf, nil))); u != nil {
		t.Fatalf("unset = %v, want nil", u)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"level":"WARN"`) || !strings.Contains(lines[0], "AUTH_SITE_URL") {
		t.Errorf("log = %q, want one WARN line naming AUTH_SITE_URL", buf.String())
	}
}

const siteURLChildEnv = "GATEWAY_TEST_SITEURL_CHILD"

func runSiteURLChild(t *testing.T, raw string) (int, string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMustParseSiteURLFatalsOnAMalformedValue$", "-test.count=1")
	cmd.Env = append(os.Environ(), siteURLChildEnv+"=1", "AUTH_SITE_URL="+raw)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	}
	t.Fatalf("run child: %v", err)
	return -1, ""
}

func TestMustParseSiteURLFatalsOnAMalformedValue(t *testing.T) {
	if os.Getenv(siteURLChildEnv) == "1" {
		mustParseSiteURL(os.Getenv("AUTH_SITE_URL"), slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		os.Exit(0)
	}

	// Positive pair: unset and a valid URL boot, so exit 1 below is the parse failing.
	for _, raw := range []string{"", "https://www.ascomply.com"} {
		if code, out := runSiteURLChild(t, raw); code != 0 {
			t.Errorf("AUTH_SITE_URL=%q: exit %d, want 0 (log %q)", raw, code, out)
		}
	}

	for name, raw := range map[string]string{
		"relative path":  "/landing",
		"no scheme":      "www.ascomply.com",
		"ftp":            "ftp://site.example",
		"javascript":     "javascript:alert(1)",
		"no host":        "https://",
		"no host a path": "https:///landing",
		"parse error":    "http://%zz",
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runSiteURLChild(t, raw)
			if code != 1 {
				t.Errorf("exit %d, want 1 (log %q)", code, out)
			}
			if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, "AUTH_SITE_URL") {
				t.Errorf("log %q does not name AUTH_SITE_URL at ERROR", out)
			}
		})
	}
}

// main parses AUTH_SITE_URL before db.Provision and hands that value to registrationHandlers.
func TestGatewayMainWiresAuthSiteURLIntoRegistration(t *testing.T) {
	_, body := parseMain(t)

	siteVar, parseAt, provisionAt, wiredArg := "", -1, -1, ""
	for i, st := range body.List {
		if as, ok := st.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if call, ok := isCallTo(as.Rhs[0], "", "mustParseSiteURL"); ok && len(call.Args) == 2 {
				if env, ok := isCallTo(call.Args[0], "os", "Getenv"); ok && len(env.Args) == 1 && isStringLit(env.Args[0], "AUTH_SITE_URL") {
					siteVar, parseAt = types.ExprString(as.Lhs[0]), i
				}
			}
			if call, ok := isCallTo(as.Rhs[0], "", "registrationHandlers"); ok && len(call.Args) == 3 {
				wiredArg = types.ExprString(call.Args[1])
			}
		}
		ast.Inspect(st, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok {
				if _, ok := isCallTo(e, "db", "Provision"); ok && provisionAt < 0 {
					provisionAt = i
				}
			}
			return true
		})
	}
	if siteVar == "" {
		t.Fatal(`main has no top-level x := mustParseSiteURL(os.Getenv("AUTH_SITE_URL"), ...)`)
	}
	if provisionAt < 0 || parseAt >= provisionAt {
		t.Errorf("mustParseSiteURL at statement %d, db.Provision at %d, want the parse first", parseAt, provisionAt)
	}
	if wiredArg != siteVar {
		t.Errorf("registrationHandlers site argument = %q, want %q", wiredArg, siteVar)
	}
}

// The client is 10 s and never follows a redirect. No seam exposes it, so its literal is read.
func TestRegistrationClientTimeoutAndNoFollow(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if d, ok := d.(*ast.FuncDecl); ok && d.Name.Name == "registrationHandlers" {
			fn = d
		}
	}
	if fn == nil {
		t.Fatal("main.go declares no registrationHandlers")
	}
	var clients []map[string]string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok || types.ExprString(cl.Type) != "http.Client" {
			return true
		}
		fields := map[string]string{}
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			fields[types.ExprString(kv.Key)] = types.ExprString(kv.Value)
			// ExprString elides a func literal's body; record what it returns instead.
			if lit, ok := kv.Value.(*ast.FuncLit); ok {
				var rets []string
				ast.Inspect(lit.Body, func(n ast.Node) bool {
					if r, ok := n.(*ast.ReturnStmt); ok {
						for _, x := range r.Results {
							rets = append(rets, "return "+types.ExprString(x))
						}
					}
					return true
				})
				fields[types.ExprString(kv.Key)] = strings.Join(rets, "; ")
			}
		}
		clients = append(clients, fields)
		return true
	})
	if len(clients) != 1 {
		t.Fatalf("registrationHandlers builds %d http.Client literals, want 1", len(clients))
	}
	if got := clients[0]["Timeout"]; got != "10 * time.Second" {
		t.Errorf("Timeout = %q, want 10 * time.Second", got)
	}
	if got := clients[0]["CheckRedirect"]; got != "return http.ErrUseLastResponse" {
		t.Errorf("CheckRedirect returns %q, want only http.ErrUseLastResponse", got)
	}
}

// A GoTrue 3xx is an answer, not a hop: following it could turn a refusal into a 200.
func TestRegistrationHandlers_DoNotFollowGoTrueRedirects(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/landed" {
			_, _ = w.Write([]byte(`{"access_token":"at"}`))
			return
		}
		http.Redirect(w, r, "/landed", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	authURL, _ := url.Parse(srv.URL)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, slog.New(slog.DiscardHandler))

	if rec := serveRegistration(reg.Register, http.MethodPost, "/auth/register", `{"email":"new@corp.example","password":"Corr3ct-Horse"}`); rec.Code != http.StatusBadGateway {
		t.Errorf("Register = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	rec := serveRegistration(reg.Verify, http.MethodGet, "/auth/verify?token=T&type=signup", "")
	if loc := rec.Header().Get("Location"); loc != "https://site.example/?verify=failed" {
		t.Errorf("Verify Location = %q, want https://site.example/?verify=failed", loc)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"POST /signup", "POST /verify"}; !slices.Equal(calls, want) {
		t.Errorf("GoTrue saw %v, want %v", calls, want)
	}
}

// The mux answers a wrong method with 405 before either handler runs. main's patterns are
// pinned by TestRegistrationRoutesRegisteredUnconditionally.
func TestRegistrationRoutes_WrongMethodIs405(t *testing.T) {
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	mux.Handle("POST /auth/register", reg.Register)
	mux.Handle("GET /auth/verify", reg.Verify)

	// Positive pair: the right methods reach the handlers.
	if rec := serveRegistration(mux, http.MethodPost, "/auth/register", `{"email":"a@corp.example","password":"p"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("POST /auth/register = %d, want 202", rec.Code)
	}
	if rec := serveRegistration(mux, http.MethodGet, "/auth/verify?token=T&type=signup", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /auth/verify = %d, want 303", rec.Code)
	}
	before := len(calls())

	for _, c := range []struct{ method, target string }{
		{http.MethodGet, "/auth/register"},
		{http.MethodHead, "/auth/register"},
		{http.MethodPut, "/auth/register"},
		{http.MethodOptions, "/auth/register"},
		{http.MethodPost, "/auth/verify?token=T&type=signup"},
		{http.MethodPut, "/auth/verify?token=T&type=signup"},
		{http.MethodOptions, "/auth/verify?token=T&type=signup"},
	} {
		if rec := serveRegistration(mux, c.method, c.target, ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.target, rec.Code)
		}
	}
	if got := calls(); len(got) != before {
		t.Errorf("a wrong method reached GoTrue: %v", got[before:])
	}
}

// ServeMux lets "GET /auth/verify" serve HEAD, so the handler itself must refuse a link-scanner prefetch.
func TestVerify_HeadIsRefusedWithoutConsumingTheToken(t *testing.T) {
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	mux.Handle("GET /auth/verify", reg.Verify)

	rec := serveRegistration(mux, http.MethodHead, "/auth/verify?token=T&type=signup", "")

	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Errorf("HEAD /auth/verify = %d Allow %q, want 405 Allow GET", rec.Code, rec.Header().Get("Allow"))
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("HEAD /auth/verify reached GoTrue %v; a prefetch consumed the token", got)
	}
}
