//go:build mockissuer

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestTaggedGatewayRegistersMintRoutesOutsideProduction(t *testing.T) {
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	withCORS := gateway.CORS([]string{"https://app.ascomply.test"})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First, so a red positive below cannot hide it.
	t.Run("refusals", func(t *testing.T) {
		for _, c := range []struct{ env, flag string }{
			{"production", "true"},
			{" Production ", "true"},
			{"development", "false"},
		} {
			jwks, login := mockIssuerRoutes(c.env, c.flag, withCORS, logger)
			if jwks != nil || login != nil {
				t.Errorf("mockIssuerRoutes(%q, %q) = (jwks %v, login %v), want (nil, nil)", c.env, c.flag, jwks, login)
			}
		}
	})

	jwks, login := mockIssuerRoutes("development", "true", withCORS, logger)
	if jwks == nil || login == nil {
		t.Fatalf(`mockIssuerRoutes("development", "true") = (jwks %v, login %v), want both handlers: a tagged build must serve the mint routes outside production`, jwks, login)
	}

	rec := httptest.NewRecorder()
	jwks.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET jwks = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("decode jwks %q: %v", rec.Body.String(), err)
	}
	if len(set.Keys) != 1 || set.Keys[0].Kty != "EC" {
		t.Errorf("jwks keys = %+v, want exactly one EC key", set.Keys)
	}

	rec = httptest.NewRecorder()
	login.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /auth/login {} = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil {
		t.Fatalf("decode login %q: %v", rec.Body.String(), err)
	}
	if tok.AccessToken == "" {
		t.Errorf("login body %s carries no access_token", rec.Body.String())
	}
}

// Only the exact flag "true" enables, and no spelling of production does; an unset
// ENVIRONMENT (go test, local runs) stays enabled. Case matters for the flag only.
func TestTaggedMockIssuerRoutesValueDomain(t *testing.T) {
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	withCORS := gateway.CORS([]string{"https://app.ascomply.test"})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, c := range []struct{ env, flag string }{
		{"PRODUCTION", "true"},
		{"production\n", "true"},
		{"\tproduction", "true"},
		{"development", ""},
		{"development", "TRUE"},
		{"development", "True"},
		{"development", "1"},
		{"development", "yes"},
		{"development", " true"},
		{"development", "true "},
	} {
		if jwks, login := mockIssuerRoutes(c.env, c.flag, withCORS, logger); jwks != nil || login != nil {
			t.Errorf("mockIssuerRoutes(%q, %q) = (jwks %v, login %v), want (nil, nil)", c.env, c.flag, jwks, login)
		}
	}

	for _, env := range []string{"", "Development", "pr-261"} {
		if jwks, login := mockIssuerRoutes(env, "true", withCORS, logger); jwks == nil || login == nil {
			t.Errorf(`mockIssuerRoutes(%q, "true") = (jwks %v, login %v), want both handlers`, env, jwks, login)
		}
	}
}

// Both routes come from one issuer stamped with AUTH_ISSUER, and login keeps its CORS
// layer.
func TestTaggedMockIssuerRoutesKeepTheirWiring(t *testing.T) {
	const origin = "https://app.ascomply.test"
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwks, login := mockIssuerRoutes("development", "true", gateway.CORS([]string{origin}), logger)
	if jwks == nil || login == nil {
		t.Fatalf(`mockIssuerRoutes("development", "true") = (jwks %v, login %v), want both handlers`, jwks, login)
	}

	t.Run("minted token verifies against the served JWKS", func(t *testing.T) {
		srv := httptest.NewServer(jwks)
		t.Cleanup(srv.Close)
		verifier, err := auth.NewVerifier(auth.Config{Issuer: mountTestIssuer, JWKSURL: srv.URL, Logger: logger})
		if err != nil {
			t.Fatalf("verifier: %v", err)
		}
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"tenant_id":"tenant-a"}`)))
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
			t.Fatalf("login = %d %s, want an access_token (decode err %v)", rec.Code, rec.Body.String(), err)
		}
		id, err := verifier.Verify(t.Context(), tok.AccessToken)
		if err != nil {
			t.Fatalf("the minted token does not verify against the served JWKS for issuer %s: %v", mountTestIssuer, err)
		}
		if id.TenantID != "tenant-a" {
			t.Errorf("verified tenant = %q, want tenant-a", id.TenantID)
		}
	})

	t.Run("login answers the CORS preflight", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodOptions, "/auth/login", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, r)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); rec.Code != http.StatusNoContent || got != origin {
			t.Errorf("preflight = %d with Access-Control-Allow-Origin %q, want 204 and %q", rec.Code, got, origin)
		}
	})

	t.Run("login grants the trace headers", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodOptions, "/auth/login", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "content-type, sentry-trace, baggage, traceparent")
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("preflight = %d, want 204", rec.Code)
		}
		got := allowHeaderSet(rec.Header())
		for _, tok := range []string{"content-type", "sentry-trace", "baggage"} {
			if !got[tok] {
				t.Errorf("granted token set = %v, missing %q", got, tok)
			}
		}
		if got["traceparent"] {
			t.Errorf("granted token set = %v, must not grant traceparent", got)
		}
	})

	t.Run("login withholds the trace headers from a disallowed origin", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodOptions, "/auth/login", nil)
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "content-type, sentry-trace, baggage")
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("preflight = %d, want 204", rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "" {
			t.Errorf("Access-Control-Allow-Headers = %q, want none", got)
		}
	})

	t.Run("an empty body mints whatever RAILWAY_ENVIRONMENT_NAME says", func(t *testing.T) {
		t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
		_, hosted := mockIssuerRoutes("development", "true", gateway.CORS([]string{origin}), logger)
		if hosted == nil {
			t.Fatal("mockIssuerRoutes returned no login handler")
		}
		rec := httptest.NewRecorder()
		hosted.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("")))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST empty body under RAILWAY_ENVIRONMENT_NAME=production = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
			t.Errorf("login body %q carries no access_token (decode err %v)", rec.Body.String(), err)
		}
	})
}

// The mint serves any identity wherever MockIssuerEnabled is set; the Railway environment name must not gate it.
func TestTaggedMockLoginIgnoresRailwayEnvironmentName(t *testing.T) {
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, login := mockIssuerRoutes("preview", "true", gateway.CORS([]string{"https://app.ascomply.test"}), logger)
	if login == nil {
		t.Fatal(`mockIssuerRoutes("preview", "true") returned no login handler`)
	}
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST {} under RAILWAY_ENVIRONMENT_NAME=production = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// A DSN that cannot connect gives 502 for a valid body; a bad body is refused before any connection.
func TestTaggedMockStaffRouteWiresTheGrant(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	const dsnHost = "staff-dsn-marker.invalid"
	h := mockStaffRoute("postgres://u@"+dsnHost+":5432/db", logger)
	post := func(body string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/mock/staff", strings.NewReader(body)))
		return rec
	}
	// Messages from gateway.MockStaffHandler (Design § API contracts).
	const (
		unavailable = `{"error":"staff grant unavailable"}`
		invalid     = `{"error":"invalid request body"}`
	)

	for _, c := range []struct {
		name, body string
		code       int
		want       string
	}{
		{"valid body, unreachable database", `{"user_id":"` + uuid.NewString() + `"}`, http.StatusBadGateway, unavailable},
		{"malformed body", `{`, http.StatusBadRequest, invalid},
	} {
		rec := post(c.body)
		if rec.Code != c.code {
			t.Errorf("%s: POST = %d (body %s), want %d", c.name, rec.Code, rec.Body.String(), c.code)
			continue
		}
		if got := strings.TrimSpace(rec.Body.String()); got != c.want {
			t.Errorf("%s: body = %s, want %s", c.name, got, c.want)
		}
	}
	// The failed connection names the DSN host, so the route is bound to the DSN it was given.
	if !strings.Contains(logs.String(), dsnHost) {
		t.Errorf("log %q does not name the DSN host %q", logs.String(), dsnHost)
	}
}

// Same shape as TestTaggedMockStaffRouteWiresTheGrant: an unreachable DSN gives 502 for a valid body.
func TestTaggedMockMemberRouteWiresTheGrant(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	const dsnHost = "member-dsn-marker.invalid"
	h := mockMemberRoute("postgres://u@"+dsnHost+":5432/db", logger)
	post := func(body string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/mock/member", strings.NewReader(body)))
		return rec
	}
	valid := `{"user_id":"` + uuid.NewString() + `","tenant_id":"` + uuid.NewString() +
		`","role":"admin","display_name":"Ada Okafor","email":"ada@example.test"}`
	// Messages from gateway.MockMemberHandler (Design § API contract).
	const (
		unavailable = `{"error":"membership grant unavailable"}`
		invalid     = `{"error":"invalid request body"}`
	)

	for _, c := range []struct {
		name, body string
		code       int
		want       string
	}{
		{"valid body, unreachable database", valid, http.StatusBadGateway, unavailable},
		{"malformed body", `{`, http.StatusBadRequest, invalid},
	} {
		rec := post(c.body)
		if rec.Code != c.code {
			t.Errorf("%s: POST = %d (body %s), want %d", c.name, rec.Code, rec.Body.String(), c.code)
			continue
		}
		if got := strings.TrimSpace(rec.Body.String()); got != c.want {
			t.Errorf("%s: body = %s, want %s", c.name, got, c.want)
		}
	}
	// The failed connection names the DSN host, so the route is bound to the DSN it was given.
	if !strings.Contains(logs.String(), dsnHost) {
		t.Errorf("log %q does not name the DSN host %q", logs.String(), dsnHost)
	}
}
