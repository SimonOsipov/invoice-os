package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// allowedOrigin is a pure unit-test fixture, not a discoverable per-environment target:
// CORS() takes its allow-list as a plain []string argument, wired from config at the
// gateway's call site, not read from this test. Since M4-21 every PR deploys to its own
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
