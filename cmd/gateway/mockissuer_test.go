//go:build mockissuer

package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestTaggedGatewayRegistersMintRoutesOutsideProduction(t *testing.T) {
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	// Empty is local posture, the only one where an empty body mints.
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "")
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
		t.Fatalf("POST /auth/login {} = %d, want 200 under local posture (body %s)", rec.Code, rec.Body.String())
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

// Both routes come from one issuer stamped with AUTH_ISSUER, login keeps its CORS
// layer, and posture is read from RAILWAY_ENVIRONMENT_NAME.
func TestTaggedMockIssuerRoutesKeepTheirWiring(t *testing.T) {
	const origin = "https://app.ascomply.test"
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "")
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

	t.Run("hosted posture refuses an empty body", func(t *testing.T) {
		t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
		_, hosted := mockIssuerRoutes("development", "true", gateway.CORS([]string{origin}), logger)
		if hosted == nil {
			t.Fatal("mockIssuerRoutes returned no login handler")
		}
		rec := httptest.NewRecorder()
		hosted.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{}`)))
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST {} under RAILWAY_ENVIRONMENT_NAME=production = %d, want 403 (body %s)", rec.Code, rec.Body.String())
		}
	})
}
