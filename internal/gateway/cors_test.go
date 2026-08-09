package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// allowedOrigin is a pure unit-test fixture, not a discoverable per-environment target:
// CORS() takes its allow-list as a plain []string argument, wired from config at the
// gateway's call site, not read from this test. Since M4-23 every PR deploys to its own
// ephemeral Railway environment with an unpredictable domain suffix, so this constant is
// deliberately a non-Railway literal — coupling it to a real (and inevitably stale) origin
// would give it environment meaning it doesn't have (Decision [cors-test-neutralized]).
const (
	allowedOrigin    = "https://app.example.test"
	disallowedOrigin = "https://evil.example.com"
)

// sentinel records whether the wrapped handler was reached, so tests can prove a
// preflight is short-circuited (never forwarded) while a real request passes through.
type sentinel struct{ reached bool }

func (s *sentinel) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.reached = true
	w.WriteHeader(http.StatusOK)
}

func TestCORSAllowedOriginGetsGrant(t *testing.T) {
	next := &sentinel{}
	h := CORS([]string{allowedOrigin})(next)

	r := httptest.NewRequest("GET", "/api/tenancy/v1/me", nil)
	r.Header.Set("Origin", allowedOrigin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if !next.reached {
		t.Fatal("a non-preflight GET must fall through to the wrapped handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin (the grant depends on the request Origin)", got)
	}
}

func TestCORSDisallowedOriginGetsNoGrant(t *testing.T) {
	next := &sentinel{}
	h := CORS([]string{allowedOrigin})(next)

	r := httptest.NewRequest("GET", "/api/tenancy/v1/me", nil)
	r.Header.Set("Origin", disallowedOrigin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if !next.reached {
		t.Fatal("a non-preflight request still passes through (the browser, not the gateway, blocks it)")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a non-listed origin", got)
	}
}

func TestCORSPreflightAnsweredWithGrant(t *testing.T) {
	next := &sentinel{}
	h := CORS([]string{allowedOrigin})(next)

	r := httptest.NewRequest("OPTIONS", "/api/tenancy/v1/me", nil)
	r.Header.Set("Origin", allowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if next.reached {
		t.Fatal("a preflight must be answered by the CORS layer, never forwarded to the wrapped handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("preflight Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != corsAllowMethods {
		t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, corsAllowMethods)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, corsAllowHeaders)
	}
}

// TestCORSPreflightGrantsPATCH proves the preflight for the portfolio entity edit
// (M3-08, the first PATCH caller from the browser) is granted: an OPTIONS with
// Access-Control-Request-Method: PATCH from an allowed origin must get PATCH back in
// Access-Control-Allow-Methods, or the browser blocks the follow-up PATCH.
func TestCORSPreflightGrantsPATCH(t *testing.T) {
	next := &sentinel{}
	h := CORS([]string{allowedOrigin})(next)

	r := httptest.NewRequest("OPTIONS", "/api/portfolio/v1/entities/00000000-0000-0000-0000-000000000001", nil)
	r.Header.Set("Origin", allowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "PATCH")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	got := rec.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(got, "PATCH") {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to contain PATCH", got)
	}
}

// TestCORSPreflightGrantsDELETE proves the preflight for the resolved-outside undo
// (DELETE /api/invoice/v1/invoices/{id}/resolved-outside) is granted: a caught bug where
// this method was missing from corsAllowMethods let the actual DELETE pass every backend
// test while failing client-side with net::ERR_FAILED, since the browser never sends a
// request the preflight doesn't grant.
func TestCORSPreflightGrantsDELETE(t *testing.T) {
	next := &sentinel{}
	h := CORS([]string{allowedOrigin})(next)

	r := httptest.NewRequest("OPTIONS", "/api/invoice/v1/invoices/00000000-0000-0000-0000-000000000001/resolved-outside", nil)
	r.Header.Set("Origin", allowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "DELETE")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	got := rec.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(got, "DELETE") {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to contain DELETE", got)
	}
}

// TestCORSPreflightGrantsPUT proves the preflight for the role-staffing round trip
// (PUT /api/invoice/v1/workflow-roles/{key}/members) is granted. This is the repo's
// first PUT from the browser, and the DELETE bug above is the precedent: every Go test
// and every e2e/api spec passes without the grant, because those callers send no Origin
// and so never preflight — only a browser does, and it blocks the PUT client-side.
//
// Asserted against the literal "PUT", not against corsAllowMethods: comparing the header
// to the constant that produced it (TestCORSPreflightAnsweredWithGrant) cannot detect a
// method missing from the constant.
func TestCORSPreflightGrantsPUT(t *testing.T) {
	next := &sentinel{}
	h := CORS([]string{allowedOrigin})(next)

	r := httptest.NewRequest("OPTIONS", "/api/invoice/v1/workflow-roles/tax-reviewer/members", nil)
	r.Header.Set("Origin", allowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "PUT")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	got := rec.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(got, "PUT") {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to contain PUT", got)
	}
}

// TestCORSPreflightBypassesAuth wires the CORS layer exactly as main does — OUTSIDE the
// JWT verifier — and proves a preflight to a protected /api route is answered 204, not
// 401. Without the outer CORS the same OPTIONS (no bearer) is a 401 from the verifier.
func TestCORSPreflightBypassesAuth(t *testing.T) {
	tg := setupGateway(t)
	h := CORS([]string{allowedOrigin})(tg.handler)

	r := httptest.NewRequest("OPTIONS", "/api/tenancy/v1/ping", nil)
	r.Header.Set("Origin", allowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight through the composed handler = %d, want 204 (must bypass the verifier)", rec.Code)
	}
	if tg.caps["tenancy"].hits != 0 {
		t.Error("preflight must not reach an upstream service")
	}
}

// TestCORSPreflightForStaffingReachesCORSThroughTheApiMount closes the last gap the
// PUT grant could hide in: TestCORSPreflightGrantsPUT and TestCORSPreflightBypassesAuth
// both call the composed handler directly, so neither proves the gateway's ServeMux
// ROUTES an OPTIONS to the /api/ pattern in the first place. That leg has already bitten
// this file once — /auth/login needed an explicit OPTIONS registration because a
// method-scoped pattern 405s a preflight instead of letting CORS answer it. Mounted here
// exactly as cmd/gateway/main.go does, with no Authorization header, as a browser sends it.
func TestCORSPreflightForStaffingReachesCORSThroughTheApiMount(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.Handle(routePrefix, CORS([]string{allowedOrigin})(tg.handler))

	const path = "/api/invoice/v1/workflow-roles/tax-reviewer/members"

	r := httptest.NewRequest("OPTIONS", path, nil)
	r.Header.Set("Origin", allowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "PUT")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("anonymous preflight = %d, want 204 (body %s) — the browser would see this instead of the PUT",
			rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "PUT") {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to contain PUT", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if tg.caps["invoice"].hits != 0 {
		t.Error("a preflight must not reach the invoice service")
	}

	// Non-vacuity: the 204 is the CORS grant, not the mount answering everything. The
	// same OPTIONS without an Origin is no preflight, falls through, and the verifier
	// 401s it — as does the real PUT the browser sends next.
	for _, c := range []struct{ name, method string }{
		{"Origin-less OPTIONS", "OPTIONS"},
		{"the follow-up PUT itself", "PUT"},
	} {
		t.Run(c.name+" is still 401 with no bearer", func(t *testing.T) {
			r := httptest.NewRequest(c.method, path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, r)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestGatewayApiMountIsCORSWrappedAndNotMethodScoped pins the two properties the test
// above assumes, at their real call site — every other test in this package composes CORS
// itself, so without this one both could be broken in the deployed fleet with the whole
// suite green. Dropping withCORS 401s every browser preflight; a method token on the
// pattern 405s it before CORS is reached.
func TestGatewayApiMountIsCORSWrappedAndNotMethodScoped(t *testing.T) {
	const path = "../../cmd/gateway/main.go"
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	type mount struct{ pattern, wrapper string }
	mounts := []mount{}
	ast.Inspect(f, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") || len(call.Args) < 2 {
			return true
		}
		m := mount{}
		switch pat := call.Args[0].(type) {
		case *ast.Ident: // the pattern is the routePrefix constant, not a literal
			if pat.Name != "routePrefix" {
				return true
			}
			m.pattern = routePrefix
		case *ast.BasicLit:
			if pat.Kind != token.STRING || !strings.Contains(pat.Value, routePrefix) {
				return true
			}
			m.pattern = strings.Trim(pat.Value, `"`)
		default:
			return true
		}
		if wrap, ok := call.Args[1].(*ast.CallExpr); ok {
			if id, ok := wrap.Fun.(*ast.Ident); ok {
				m.wrapper = id.Name
			}
		}
		mounts = append(mounts, m)
		return true
	})

	if len(mounts) != 1 {
		t.Fatalf("found %d /api/ mounts in %s (%v), want exactly 1 — the scan is out of date, so its assertions are vacuous",
			len(mounts), path, mounts)
	}
	if mounts[0].wrapper != "withCORS" {
		t.Errorf("the /api/ mount handler is wrapped in %q, want withCORS — without it every browser preflight is 401'd",
			mounts[0].wrapper)
	}
	if strings.ContainsAny(mounts[0].pattern, " ") {
		t.Errorf("the /api/ mount pattern is %q — a method token 405s every preflight", mounts[0].pattern)
	}
	// The cmd copy of routePrefix is only comment-linked to this package's; if they ever
	// diverge, the mount above stops being the path CORS actually guards.
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "routePrefix" || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			if got := strings.Trim(lit.Value, `"`); got != routePrefix {
				t.Errorf("cmd/gateway routePrefix = %q, want %q (this package's mount point)", got, routePrefix)
			}
		}
	}
}

// TestCORSPreflightDisallowedOriginNotForwarded proves a preflight from a non-listed
// origin is still short-circuited (204) — it never reaches the verifier to be 401'd —
// but receives no methods/headers grant, so the browser blocks the follow-up.
func TestCORSPreflightDisallowedOriginNotForwarded(t *testing.T) {
	next := &sentinel{}
	h := CORS([]string{allowedOrigin})(next)

	r := httptest.NewRequest("OPTIONS", "/api/tenancy/v1/me", nil)
	r.Header.Set("Origin", disallowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if next.reached {
		t.Fatal("a preflight must never be forwarded, even from a disallowed origin")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed-origin preflight granted Access-Control-Allow-Origin = %q, want empty", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("disallowed-origin preflight granted Access-Control-Allow-Methods = %q, want empty", got)
	}
}

// TestCORSNoOriginUntouched proves a request with no Origin header (same-origin or a
// server-to-server caller like the Verifier fetching JWKS) passes through with no CORS
// headers added and no preflight short-circuit.
func TestCORSNoOriginUntouched(t *testing.T) {
	next := &sentinel{}
	h := CORS([]string{allowedOrigin})(next)

	r := httptest.NewRequest("GET", "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if !next.reached {
		t.Fatal("a request with no Origin must pass through untouched")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty when no Origin is sent", got)
	}
}
