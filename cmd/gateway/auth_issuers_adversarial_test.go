package main

import (
	"errors"
	"go/ast"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const oneExtraIssuer = `[{"issuer":"https://extra.test/auth/v1","jwks_url":"https://extra.test/auth/v1/.well-known/jwks.json"}]`

// The published count is 1 + len(mustParseIssuers(...)); the scan in
// TestGatewayAuthIssuersCountsPrimaryPlusAdditional pins that expression in main.
func TestMustParseIssuersCountTable(t *testing.T) {
	for _, c := range []struct {
		name, raw, want string
	}{
		{"unset", "", "1"},
		{"whitespace", "  \n", "1"},
		{"empty array", "[]", "1"},
		{"one entry", oneExtraIssuer, "2"},
		{"two entries", `[{"issuer":"https://a.test","jwks_url":"https://a.test/jwks"},{"issuer":"https://b.test","jwks_url":"http://b.test/jwks"}]`, "3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := strconv.Itoa(1 + len(mustParseIssuers(c.raw))); got != c.want {
				t.Errorf("count for %q = %s, want %s", c.raw, got, c.want)
			}
		})
	}
}

const mustParseChildEnv = "GATEWAY_TEST_MUSTPARSE_CHILD"

// runMustParseChild runs mustParseIssuers(raw) in a child process and returns its exit code and log.
func runMustParseChild(t *testing.T, raw string) (int, string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMustParseIssuersFatalsOnAMalformedSet$", "-test.count=1")
	cmd.Env = append(os.Environ(), mustParseChildEnv+"=1", "AUTH_ADDITIONAL_ISSUERS="+raw)
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

func TestMustParseIssuersFatalsOnAMalformedSet(t *testing.T) {
	if os.Getenv(mustParseChildEnv) == "1" {
		mustParseIssuers(os.Getenv("AUTH_ADDITIONAL_ISSUERS"))
		os.Exit(0)
	}

	// Positive pair: a valid set boots, so exit 1 below is the parse failing.
	for _, raw := range []string{"", oneExtraIssuer} {
		if code, out := runMustParseChild(t, raw); code != 0 {
			t.Errorf("AUTH_ADDITIONAL_ISSUERS=%q: exit %d, want 0 (log %q)", raw, code, out)
		}
	}

	for name, raw := range map[string]string{
		"open bracket":   "[",
		"object":         `{"issuer":"https://a.test","jwks_url":"https://a.test/jwks"}`,
		"missing jwks":   `[{"issuer":"https://a.test"}]`,
		"non-http jwks":  `[{"issuer":"https://a.test","jwks_url":"ftp://a.test/jwks"}]`,
		"unknown field":  `[{"issuer":"https://a.test","jwks_url":"https://a.test/jwks","aud":"x"}]`,
		"trailing value": oneExtraIssuer + " []",
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runMustParseChild(t, raw)
			if code != 1 {
				t.Errorf("exit %d, want 1 (log %q)", code, out)
			}
			if !strings.Contains(out, "ERROR") || !strings.Contains(out, "AUTH_ADDITIONAL_ISSUERS") {
				t.Errorf("log %q does not name AUTH_ADDITIONAL_ISSUERS at ERROR", out)
			}
		})
	}
}

// ParseTrustedIssuers accepts a repeated issuer; NewVerifier is what refuses it.
func TestGatewayRefusesADuplicateAdditionalIssuer(t *testing.T) {
	const primary = "https://primary.test/auth/v1"
	for _, c := range []struct {
		name, raw, wantErr string
	}{
		{"distinct", oneExtraIssuer, ""},
		{"repeats primary", `[{"issuer":"` + primary + `","jwks_url":"https://other.test/jwks"}]`, "repeats issuer"},
		{"repeats itself", `[{"issuer":"https://a.test","jwks_url":"https://a.test/jwks"},{"issuer":"https://a.test","jwks_url":"https://b.test/jwks"}]`, "repeats issuer"},
	} {
		t.Run(c.name, func(t *testing.T) {
			additional := mustParseIssuers(c.raw)
			if len(additional) == 0 {
				t.Fatalf("mustParseIssuers(%q) returned no issuers", c.raw)
			}
			_, err := auth.NewVerifier(auth.Config{Issuer: primary, JWKSURL: "https://primary.test/jwks", Additional: additional})
			switch {
			case c.wantErr == "" && err != nil:
				t.Errorf("NewVerifier: %v, want success", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Errorf("NewVerifier err = %v, want it to contain %q", err, c.wantErr)
			}
		})
	}
}

// TestGatewayMainFatalsOnAVerifierError: the duplicate refusal above only stops
// boot if main's guard after auth.NewVerifier calls fatal.
func TestGatewayMainFatalsOnAVerifierError(t *testing.T) {
	_, body := parseMain(t)

	at, errName := -1, ""
	for i, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
			continue
		}
		if _, ok := isCallTo(as.Rhs[0], "auth", "NewVerifier"); !ok {
			continue
		}
		if id, ok := as.Lhs[1].(*ast.Ident); ok && id.Name != "_" {
			at, errName = i, id.Name
		}
	}
	if at < 0 || at+1 >= len(body.List) {
		t.Fatal("main never assigns auth.NewVerifier's error to a named local followed by a guard")
	}
	guard, ok := body.List[at+1].(*ast.IfStmt)
	if !ok {
		t.Fatalf("the statement after auth.NewVerifier is %T, want `if %s != nil`", body.List[at+1], errName)
	}
	bin, ok := guard.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		t.Fatalf("the guard after auth.NewVerifier is not `%s != nil`", errName)
	}
	if id, ok := bin.X.(*ast.Ident); !ok || id.Name != errName {
		t.Fatalf("the guard after auth.NewVerifier tests something other than %s", errName)
	}
	fatals := 0
	ast.Inspect(guard.Body, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			if _, ok := isCallTo(e, "", "fatal"); ok {
				fatals++
			}
		}
		return true
	})
	if fatals != 1 {
		t.Errorf("the `%s != nil` guard after auth.NewVerifier calls fatal %d time(s), want 1", errName, fatals)
	}
}

// No path under /api/auth reaches the auth upstream, while the fleet probe does.
func TestGatewayNeverProxiesAuthUnderAPI(t *testing.T) {
	issuer, err := auth.NewMockIssuer(mountTestIssuer)
	if err != nil {
		t.Fatalf("mock issuer: %v", err)
	}
	jwks := httptest.NewServer(issuer.JWKSHandler())
	t.Cleanup(jwks.Close)
	verifier, err := auth.NewVerifier(auth.Config{Issuer: mountTestIssuer, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	tok, err := issuer.Mint(auth.MintOptions{Subject: "11111111-1111-1111-1111-111111111111", Role: "authenticated", TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	var mu sync.Mutex
	var authHits []string
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHits = append(authHits, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(authSrv.Close)
	routedHit := false
	routedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		routedHit = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(routedSrv.Close)

	setUpstreamEnv(t, routedSrv.URL)
	t.Setenv("AUTH_URL", authSrv.URL)
	routed, probed, err := loadUpstreams()
	if err != nil {
		t.Fatalf("loadUpstreams: %v", err)
	}
	apiHandler, fleetHandler := gatewayHandlers(verifier, routed, probed, map[string]string{"auth": ".well-known/jwks.json"}, slog.Default())
	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.HandleFunc("GET /healthz/fleet", fleetHandler)

	do := func(method, path string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"email":"a@b.test","password":"x"}`))
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code
	}

	for _, p := range []struct{ method, path string }{
		{"GET", "/api/auth"},
		{"GET", "/api/auth/"},
		{"GET", "/api/auth/.well-known/jwks.json"},
		{"POST", "/api/auth/token?grant_type=password"},
		{"GET", "/api/auth/admin/users"},
		{"GET", "/api/AUTH/x"},
		{"GET", "/api/auth%2Fx"},
	} {
		if code := do(p.method, p.path); code >= 200 && code < 300 {
			t.Errorf("%s %s = %d, want a non-2xx refusal", p.method, p.path, code)
		}
	}

	// Controls: a routed service reaches its upstream, and the probe reaches auth.
	if code := do("GET", "/api/"+routedServices[0]+"/x"); code != http.StatusOK {
		t.Errorf("GET /api/%s/x = %d, want 200 from its upstream", routedServices[0], code)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz/fleet", nil))

	mu.Lock()
	defer mu.Unlock()
	if !routedHit {
		t.Error("the routed upstream was never reached; the refusals above prove nothing")
	}
	if len(authHits) == 0 {
		t.Fatal("the auth upstream saw no request at all; the fleet probe did not reach it")
	}
	if slices.ContainsFunc(authHits, func(p string) bool { return p != "/.well-known/jwks.json" }) {
		t.Errorf("the auth upstream was asked for %v, want only the fleet probe at /.well-known/jwks.json", authHits)
	}
}
