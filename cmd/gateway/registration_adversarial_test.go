package main

import (
	"bytes"
	"context"
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
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
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

// Unset is allowed and logged once, at WARN, naming the variable.
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
		"query":          "https://www.ascomply.com/?a=1",
		"fragment":       "https://www.ascomply.com/#top",
		"user info":      "https://user:pw@www.ascomply.com",
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runSiteURLChild(t, raw)
			if code != 1 {
				t.Errorf("exit %d, want 1 (log %q)", code, out)
			}
			if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, "AUTH_SITE_URL") {
				t.Errorf("log %q does not name AUTH_SITE_URL at ERROR", out)
			}
			// The log line reaches Sentry too; ScrubText has no rule for URL user info.
			if strings.Contains(out, raw) || strings.Contains(raw, "pw@") && strings.Contains(out, "pw") {
				t.Errorf("log %q carries the raw AUTH_SITE_URL value %q", out, raw)
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
			if call, ok := isCallTo(as.Rhs[0], "", "registrationHandlers"); ok && len(call.Args) == 6 {
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

// Each GoTrue client is 10 s and never follows a redirect. No seam exposes it, so its literal is read.
func TestRegistrationClientTimeoutAndNoFollow(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, name := range []string{"registrationHandlers", "resetPasswordHandler"} {
		var fn *ast.FuncDecl
		for _, d := range f.Decls {
			if d, ok := d.(*ast.FuncDecl); ok && d.Name.Name == name {
				fn = d
			}
		}
		if fn == nil {
			t.Errorf("main.go declares no %s", name)
			continue
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
			t.Errorf("%s builds %d http.Client literals, want 1", name, len(clients))
			continue
		}
		if got := clients[0]["Timeout"]; got != "10 * time.Second" {
			t.Errorf("%s Timeout = %q, want 10 * time.Second", name, got)
		}
		if got := clients[0]["CheckRedirect"]; got != "return http.ErrUseLastResponse" {
			t.Errorf("%s CheckRedirect returns %q, want only http.ErrUseLastResponse", name, got)
		}
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
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil, noPendingInvite)

	if rec := serveRegistration(reg.Register, http.MethodPost, "/auth/register", `{"email":"new@corp.example","password":"Corr3ct-Horse"}`); rec.Code != http.StatusBadGateway {
		t.Errorf("Register = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	rec := serveForm(reg.Verify, "/auth/verify", "token=T&type=signup")
	if loc := rec.Header().Get("Location"); loc != "https://site.example/?verify=failed" {
		t.Errorf("Verify Location = %q, want https://site.example/?verify=failed", loc)
	}
	mu.Lock()
	if want := []string{"POST /signup", "POST /verify"}; !slices.Equal(calls, want) {
		t.Errorf("GoTrue saw %v, want %v", calls, want)
	}
	before := len(calls)
	mu.Unlock()

	log := slog.New(slog.DiscardHandler)
	signIn := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	reset := resetPasswordHandler(authURL, site, gateway.NewSessionChecker(nil, nil, time.Now, log), signIn, log)
	rec = serveForm(reset, "/auth/reset-password", "token=T&type=recovery&password=new-password-1")
	if loc := rec.Header().Get("Location"); loc != "https://site.example/?reset=failed" {
		t.Errorf("reset form Location = %q, want https://site.example/?reset=failed", loc)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"POST /verify"}; !slices.Equal(calls[before:], want) {
		t.Errorf("the reset form made GoTrue calls %v, want only %v", calls[before:], want)
	}
}

// verifyMux mounts main's two /auth/verify patterns over the real handlers.
func verifyMux(t *testing.T, reg registration, site *url.URL) *http.ServeMux {
	t.Helper()
	page, err := gateway.VerifyPageHandler(site)
	if err != nil {
		t.Fatalf("VerifyPageHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /auth/verify", page)
	mux.Handle("POST /auth/verify", reg.Verify)
	return mux
}

// The mux answers a wrong method on register and verify with 405 before the handler runs; resend's
// handler answers its own. main's patterns are pinned by TestRegistrationRoutesRegisteredUnconditionally.
func TestRegistrationRoutes_WrongMethodIs405(t *testing.T) {
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil, noPendingInvite)
	mux := verifyMux(t, reg, site)
	mux.Handle("POST /auth/register", reg.Register)
	// No method in the pattern, so the handler's own method check answers.
	mux.Handle("/auth/resend-verification", reg.ResendVerification)
	mountResetRoutes(t, mux, reg, authURL, site)

	// Positive pair: the right methods reach the handlers.
	if rec := serveRegistration(mux, http.MethodPost, "/auth/register", `{"email":"a@corp.example","password":"p"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("POST /auth/register = %d, want 202", rec.Code)
	}
	if rec := serveRegistration(mux, http.MethodPost, "/auth/resend-verification", `{"email":"a@corp.example"}`); rec.Code != http.StatusAccepted {
		t.Errorf("POST /auth/resend-verification = %d, want 202", rec.Code)
	}
	if rec := serveRegistration(mux, http.MethodGet, "/auth/verify?token=T&type=signup", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /auth/verify = %d, want 200", rec.Code)
	}
	if rec := serveForm(mux, "/auth/verify", "token=T&type=signup"); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /auth/verify = %d, want 303", rec.Code)
	}
	if rec := serveRegistration(mux, http.MethodPost, "/auth/request-password-reset", `{"email":"a@corp.example"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("POST /auth/request-password-reset = %d, want 202", rec.Code)
	}
	if rec := serveRegistration(mux, http.MethodGet, "/auth/reset-password?token=T&type=recovery", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /auth/reset-password = %d, want 200", rec.Code)
	}
	if rec := serveForm(mux, "/auth/reset-password", "token=T&type=recovery&password=new-password-1"); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /auth/reset-password = %d, want 303", rec.Code)
	}
	if got := countCalls(calls(), "POST /verify"); got != 2 {
		t.Fatalf("the verify and reset forms made %d GoTrue /verify calls, want 2", got)
	}
	before := len(calls())

	for _, c := range []struct{ method, target string }{
		{http.MethodGet, "/auth/register"},
		{http.MethodHead, "/auth/register"},
		{http.MethodPut, "/auth/register"},
		{http.MethodGet, "/auth/resend-verification"},
		{http.MethodPut, "/auth/resend-verification"},
		{http.MethodDelete, "/auth/resend-verification"},
		{http.MethodPut, "/auth/verify?token=T&type=signup"},
		{http.MethodOptions, "/auth/verify?token=T&type=signup"},
		{http.MethodDelete, "/auth/verify?token=T&type=signup"},
		{http.MethodGet, "/auth/request-password-reset"},
		{http.MethodPut, "/auth/request-password-reset"},
		{http.MethodDelete, "/auth/request-password-reset"},
		{http.MethodPut, "/auth/reset-password?token=T&type=recovery"},
		{http.MethodDelete, "/auth/reset-password?token=T&type=recovery"},
	} {
		if rec := serveRegistration(mux, c.method, c.target, ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.target, rec.Code)
		}
	}
	if got := calls(); len(got) != before {
		t.Errorf("a wrong method reached GoTrue: %v", got[before:])
	}
}

// Opening the link, by GET or HEAD, spends nothing; only the POST of the page's own form reaches GoTrue.
func TestVerifyRoute_OpeningNeverReachesGoTrue(t *testing.T) {
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	mux := verifyMux(t, registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil, noPendingInvite), site)
	const link = "http://gateway.test/auth/verify?token=T&type=signup"

	var page string
	for _, method := range []string{http.MethodGet, http.MethodGet, http.MethodHead} {
		rec := serveRegistration(mux, method, link, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d, want 200", method, link, rec.Code)
		}
		if method == http.MethodGet {
			page = rec.Body.String()
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("opening the link reached GoTrue: %v", got)
	}

	action, values := pageForm(t, link, page)
	if values.Get("token") != "T" || values.Get("type") != "signup" {
		t.Fatalf("form values = %v, want token=T and type=signup", values)
	}
	rec := serveForm(mux, action, values.Encode())

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://site.example/?verified=1" {
		t.Errorf("click = %d Location %q, want 303 https://site.example/?verified=1", rec.Code, rec.Header().Get("Location"))
	}
	if got, want := calls(), []string{"POST /verify"}; !slices.Equal(got, want) {
		t.Errorf("GoTrue saw %v, want %v", got, want)
	}
}

// mountResetRoutes adds main's reset patterns, over the real handlers, to mux.
func mountResetRoutes(t *testing.T, mux *http.ServeMux, reg registration, authURL, site *url.URL) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	signIn := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	mux.Handle("POST /auth/request-password-reset", reg.RequestPasswordReset)
	mux.Handle("OPTIONS /auth/request-password-reset", reg.RequestPasswordReset)
	mux.Handle("GET /auth/reset-password", gateway.ResetPasswordPageHandler(site))
	mux.Handle("POST /auth/reset-password", resetPasswordHandler(authURL, site, gateway.NewSessionChecker(nil, nil, time.Now, log), signIn, log))
}

// Opening the reset link, by GET or HEAD, spends nothing; only the POST of the page's own form reaches GoTrue.
func TestResetPasswordRoute_OpeningNeverReachesGoTrue(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/verify" {
			_, _ = w.Write([]byte(`{"access_token":"at","user":{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","email":"ada@corp.example"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
	authURL, _ := url.Parse(srv.URL)
	site, _ := url.Parse("https://site.example")
	mux := http.NewServeMux()
	mountResetRoutes(t, mux, registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil, noPendingInvite), authURL, site)
	const link = "http://gateway.test/auth/reset-password?token=T&type=recovery"

	var page string
	for _, method := range []string{http.MethodGet, http.MethodGet, http.MethodHead} {
		rec := serveRegistration(mux, method, link, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d, want 200", method, link, rec.Code)
		}
		if method == http.MethodGet {
			page = rec.Body.String()
		}
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("opening the link reached GoTrue: %v", got)
	}

	action, values := pageForm(t, link, page)
	if values.Get("token") != "T" || values.Get("type") != "recovery" {
		t.Fatalf("form values = %v, want token=T and type=recovery", values)
	}
	values.Set("password", "new-password-1")
	rec := serveForm(mux, action, values.Encode())

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://site.example/?reset=1" {
		t.Errorf("submit = %d Location %q, want 303 https://site.example/?reset=1", rec.Code, rec.Header().Get("Location"))
	}
	if got, want := seen(), []string{"POST /verify", "PUT /user", "POST /logout"}; !slices.Equal(got, want) {
		t.Errorf("GoTrue saw %v, want %v", got, want)
	}
}

// Invitee registration and /auth/register spend one per-IP budget: main hands the register throttle on.
func TestGatewayMainSharesTheRegisterBudgetWithInvitations(t *testing.T) {
	_, body := parseMain(t)

	regVar, previewerVar, regAt, invAt := "", "", -1, -1
	var invCalls []*ast.CallExpr
	var previewerCall *ast.CallExpr
	for i, st := range body.List {
		if as, ok := st.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
			if call, ok := isCallTo(as.Rhs[0], "", "registrationHandlers"); ok && len(call.Args) == 6 && len(as.Lhs) == 1 {
				regVar, regAt = types.ExprString(as.Lhs[0]), i
			}
			if call, ok := isCallTo(as.Rhs[0], "gateway", "NewHTTPInvitationPreviewer"); ok && len(as.Lhs) == 1 {
				previewerVar, previewerCall = types.ExprString(as.Lhs[0]), call
			}
		}
		ast.Inspect(st, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if _, ok := isCallTo(call, "", "invitationHandlers"); ok {
					invCalls = append(invCalls, call)
					invAt = i
				}
			}
			return true
		})
	}
	if regVar == "" {
		t.Fatal("main has no top-level `x := registrationHandlers(...)` with 6 arguments")
	}
	if len(invCalls) != 1 {
		t.Fatalf("main calls invitationHandlers %d times, want exactly once", len(invCalls))
	}
	call := invCalls[0]
	if len(call.Args) != 6 {
		t.Fatalf("invitationHandlers call has %d arguments, want 6", len(call.Args))
	}
	if invAt <= regAt {
		t.Errorf("invitationHandlers is statement %d, registrationHandlers %d; the throttle is read before it exists", invAt, regAt)
	}
	for i, want := range map[int]string{
		0: `probed["auth"]`,
		1: "siteURL",
		2: "registerMinResponse",
		3: regVar + ".RegisterPerIP",
		5: "app.Logger",
	} {
		if got := types.ExprString(call.Args[i]); got != want {
			t.Errorf("invitationHandlers argument %d = %s, want %s", i+1, got, want)
		}
	}
	if previewerCall == nil {
		t.Fatal("main has no top-level `x := gateway.NewHTTPInvitationPreviewer(...)`")
	}
	if got := types.ExprString(call.Args[4]); got != previewerVar {
		t.Errorf("invitationHandlers previewer argument = %s, want %s", got, previewerVar)
	}
	if len(previewerCall.Args) != 3 || types.ExprString(previewerCall.Args[0]) != `routed["tenancy"]` || types.ExprString(previewerCall.Args[2]) != "gatewayToken" {
		t.Errorf("NewHTTPInvitationPreviewer args = %v, want routed[\"tenancy\"], a client and gatewayToken", previewerCall.Args)
	}
}

// main's patterns for the two invitation routes, mounted on a mux behind the gateway's CORS layer.
func TestInvitationRoutes_PreflightAnswersCORS(t *testing.T) {
	const origin = "https://app.example.test"
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	sites, _ := mainRoutes(t, src)
	var patterns []string
	for _, s := range sites {
		if strings.Contains(s.pattern, " /auth/invitation") {
			patterns = append(patterns, s.pattern)
		}
	}
	if len(patterns) != 4 {
		t.Fatalf("main registers %d invitation routes %v, want 4 (POST and OPTIONS of /auth/invitation and /auth/invitation/register)", len(patterns), patterns)
	}

	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	perIP := gateway.NewSignInThrottle("register", gateway.RegisterPerIP, gateway.RegisterMaxKeys, gateway.RegisterWindow, time.Now)
	preview := func(context.Context, string) (gateway.InvitationPreview, error) {
		return gateway.InvitationPreview{Workspace: "Obi Partners", Role: "reviewer", Email: "tunde@obi.test"}, nil
	}
	invitation, register := invitationHandlers(authURL, site, 0, perIP, preview, slog.New(slog.DiscardHandler))
	withCORS := gateway.CORS([]string{origin})
	mux := http.NewServeMux()
	for _, p := range patterns {
		if strings.HasSuffix(p, "/register") {
			mux.Handle(p, withCORS(register))
		} else {
			mux.Handle(p, withCORS(invitation))
		}
	}

	for _, path := range []string{"/auth/invitation", "/auth/invitation/register"} {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s = %d, want 204", path, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("OPTIONS %s Access-Control-Allow-Origin = %q, want %q", path, got, origin)
		}
		if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
			t.Errorf("OPTIONS %s Access-Control-Allow-Methods = %q, want POST granted", path, got)
		}
	}

	// Positive pair: the POST behind the same wrap answers with the grant and reaches its handler.
	req := httptest.NewRequest(http.MethodPost, "/auth/invitation", strings.NewReader(`{"token":"Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9T"}`))
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Errorf("POST /auth/invitation = %d with Access-Control-Allow-Origin %q, want 200 and %q: %s",
			rec.Code, rec.Header().Get("Access-Control-Allow-Origin"), origin, rec.Body.String())
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("a preflight or preview reached GoTrue: %v", got)
	}
}

// noPendingInvite is the lookup of a tenancy with no invites.
func noPendingInvite(context.Context, string) (bool, error) { return false, nil }

// Register asks tenancy about the address: main must hand registrationHandlers the HTTP lookup.
func TestGatewayMainWiresThePendingInviteLookupIntoRegistration(t *testing.T) {
	_, body := parseMain(t)

	lookupVar, regArg := "", ""
	var lookupCall *ast.CallExpr
	for _, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		if call, ok := isCallTo(as.Rhs[0], "gateway", "NewHTTPPendingInviteLookup"); ok {
			lookupVar, lookupCall = types.ExprString(as.Lhs[0]), call
		}
		if call, ok := isCallTo(as.Rhs[0], "", "registrationHandlers"); ok && len(call.Args) == 6 {
			regArg = types.ExprString(call.Args[5])
		}
	}
	if lookupCall == nil {
		t.Fatal("main has no top-level `x := gateway.NewHTTPPendingInviteLookup(...)`")
	}
	if len(lookupCall.Args) != 3 || types.ExprString(lookupCall.Args[0]) != `routed["tenancy"]` || types.ExprString(lookupCall.Args[2]) != "gatewayToken" {
		t.Errorf("NewHTTPPendingInviteLookup args = %v, want routed[\"tenancy\"], a client and gatewayToken", lookupCall.Args)
	}
	if regArg == "" {
		t.Fatal("main has no top-level `x := registrationHandlers(...)` with 6 arguments")
	}
	if regArg != lookupVar {
		t.Errorf("registrationHandlers sixth argument = %q, want %q", regArg, lookupVar)
	}
}

// Fail closed: with no lookup, Register must not reach GoTrue for a possibly invited address.
func TestRegistrationHandlers_NilPendingLookupIsNotConfigured(t *testing.T) {
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil, nil)

	rec := serveRegistration(reg.Register, http.MethodPost, "/auth/register", `{"email":"new@corp.example","password":"Corr3ct-Horse"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Register = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if want := `{"error":"registration is not configured"}`; strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("body = %s, want %s", rec.Body.String(), want)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("GoTrue saw %v, want no calls", got)
	}
}
