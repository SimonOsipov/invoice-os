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
