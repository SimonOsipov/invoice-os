//go:build mockissuer

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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

	t.Run("an empty body mints under a non-nightly DEPLOY_ENV", func(t *testing.T) {
		t.Setenv("DEPLOY_ENV", "production")
		_, hosted := mockIssuerRoutes("development", "true", gateway.CORS([]string{origin}), logger)
		if hosted == nil {
			t.Fatal("mockIssuerRoutes returned no login handler")
		}
		rec := httptest.NewRecorder()
		hosted.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("")))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST empty body under DEPLOY_ENV=production = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
			t.Errorf("login body %q carries no access_token (decode err %v)", rec.Body.String(), err)
		}
	})
}

// The mint serves any identity wherever MockIssuerEnabled is set; a non-nightly DEPLOY_ENV must not gate it.
func TestTaggedMockLoginIgnoresANonNightlyDeployEnv(t *testing.T) {
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	t.Setenv("DEPLOY_ENV", "production")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, login := mockIssuerRoutes("preview", "true", gateway.CORS([]string{"https://app.ascomply.test"}), logger)
	if login == nil {
		t.Fatal(`mockIssuerRoutes("preview", "true") returned no login handler`)
	}
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST {} under DEPLOY_ENV=production = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// An unreachable DSN gives 502 for a valid body; a bad body is refused before any connection.
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

// Same shape as TestTaggedMockMemberRouteWiresTheGrant: an unreachable DSN gives 502 for a valid body.
func TestTaggedMockInvitationTokenRouteWiresTheSetter(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	const dsnHost = "invitation-dsn-marker.invalid"
	h := mockInvitationTokenRoute("postgres://u@"+dsnHost+":5432/db", logger)
	post := func(body string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/mock/invitation-token", strings.NewReader(body)))
		return rec
	}
	// 43 base64url characters; msgInvalidBody's text is gateway.MockInvitationTokenHandler's 400 (Design § API contracts).
	valid := `{"tenant_id":"` + uuid.NewString() + `","invitation_id":"` + uuid.NewString() +
		`","token":"abcdefghijklmnopqrstuvwxyz-_ABCDEFGHIJKLMNO"}`
	const invalid = `{"error":"invalid request body"}`

	rec := post(`{`)
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != invalid {
		t.Errorf("malformed body: POST = %d (body %s), want 400 %s", rec.Code, rec.Body.String(), invalid)
	}
	rec = post(valid)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("valid body, unreachable database: POST = %d (body %s), want 502", rec.Code, rec.Body.String())
	}
	// The failed connection names the DSN host, so the route is bound to the DSN it was given.
	if !strings.Contains(logs.String(), dsnHost) {
		t.Errorf("log %q does not name the DSN host %q", logs.String(), dsnHost)
	}
	if strings.Contains(logs.String()+rec.Body.String(), "abcdefghijklmnopqrstuvwxyz-_") {
		t.Errorf("the log or the answer carries the token: %s %s", logs.String(), rec.Body.String())
	}
}

// Same shape as TestTaggedMockInvitationTokenRouteWiresTheSetter.
func TestTaggedMockDeletionLinkTokenRouteWiresTheSetter(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	const dsnHost = "deletion-link-dsn-marker.invalid"
	h := mockDeletionLinkTokenRoute("postgres://u@"+dsnHost+":5432/db", logger)
	post := func(body string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/mock/deletion-link-token", strings.NewReader(body)))
		return rec
	}
	// 43 base64url characters; msgInvalidBody's text is gateway.MockDeletionLinkTokenHandler's 400 (Design § API contracts).
	valid := `{"tenant_id":"` + uuid.NewString() + `","user_id":"` + uuid.NewString() +
		`","token":"abcdefghijklmnopqrstuvwxyz-_ABCDEFGHIJKLMNO"}`
	const invalid = `{"error":"invalid request body"}`

	rec := post(`{`)
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != invalid {
		t.Errorf("malformed body: POST = %d (body %s), want 400 %s", rec.Code, rec.Body.String(), invalid)
	}
	rec = post(valid)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("valid body, unreachable database: POST = %d (body %s), want 502", rec.Code, rec.Body.String())
	}
	// The failed connection names the DSN host, so the route is bound to the DSN it was given.
	if !strings.Contains(logs.String(), dsnHost) {
		t.Errorf("log %q does not name the DSN host %q", logs.String(), dsnHost)
	}
	if strings.Contains(logs.String()+rec.Body.String(), "abcdefghijklmnopqrstuvwxyz-_") {
		t.Errorf("the log or the answer carries the token: %s %s", logs.String(), rec.Body.String())
	}
}

func taggedJWK(t *testing.T, kid string) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e, err := k.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	pub := e.PublicKey().Bytes()
	enc := base64.RawURLEncoding.EncodeToString
	raw, err := json.Marshal([]map[string]any{{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "kid": kid,
		"key_ops": []string{"sign", "verify"},
		"x":       enc(pub[1:33]), "y": enc(pub[33:]), "d": enc(e.Bytes()),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Same shape as TestTaggedMockDeletionLinkTokenRouteWiresTheSetter.
func TestTaggedMockReadinessConfirmTokenRouteWiresTheSetter(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	const dsnHost = "readiness-confirm-dsn-marker.invalid"
	h := mockReadinessConfirmTokenRoute("postgres://u@"+dsnHost+":5432/db", logger)
	post := func(body string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/mock/readiness-confirm-token", strings.NewReader(body)))
		return rec
	}
	rec := post(`{`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: POST = %d (body %s), want 400", rec.Code, rec.Body.String())
	}
	rec = post(`{"email":"a@b.io","token":"abcdefghijklmnopqrstuvwxyz-_ABCDEFGHIJKLMNO"}`)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("valid body, unreachable database: POST = %d (body %s), want 502", rec.Code, rec.Body.String())
	}
	// The failed connection names the DSN host, so the route is bound to the DSN it was given.
	if !strings.Contains(logs.String(), dsnHost) {
		t.Errorf("log %q does not name the DSN host %q", logs.String(), dsnHost)
	}
	if strings.Contains(logs.String()+rec.Body.String(), "abcdefghijklmnopqrstuvwxyz-_") {
		t.Errorf("the log or the answer carries the token: %s %s", logs.String(), rec.Body.String())
	}
}

// With GATEWAY_MOCK_ISSUER_KEY set, JWKS and /auth/login both use that key's kid.
func TestTaggedMockIssuerUsesConfiguredKey(t *testing.T) {
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	t.Setenv("GATEWAY_MOCK_ISSUER_KEY", taggedJWK(t, "kid-configured"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwks, login := mockIssuerRoutes("development", "true", gateway.CORS([]string{"https://app.ascomply.test"}), logger)
	if jwks == nil || login == nil {
		t.Fatalf("mockIssuerRoutes = (jwks %v, login %v), want both handlers", jwks, login)
	}

	rec := httptest.NewRecorder()
	jwks.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("jwks = %q (decode err %v), want one key", rec.Body.String(), err)
	}
	if set.Keys[0].Kid != "kid-configured" {
		t.Errorf("jwks kid = %q, want kid-configured", set.Keys[0].Kid)
	}

	rec = httptest.NewRecorder()
	login.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{}`)))
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("login = %d %s, want an access_token", rec.Code, rec.Body.String())
	}
	hdr, _, _ := strings.Cut(tok.AccessToken, ".")
	b, err := base64.RawURLEncoding.DecodeString(hdr)
	if err != nil {
		t.Fatalf("decode token header: %v", err)
	}
	var h struct {
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(b, &h); err != nil || h.Kid != "kid-configured" {
		t.Errorf("token header %s, want kid kid-configured (decode err %v)", b, err)
	}
}

func servedKid(t *testing.T, iss *auth.MockIssuer) string {
	t.Helper()
	rec := httptest.NewRecorder()
	iss.JWKSHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("jwks = %q (decode err %v), want one key", rec.Body.String(), err)
	}
	return set.Keys[0].Kid
}

// Unset and blank both generate a key per build, as before.
func TestTaggedMockIssuerWithoutKeyGeneratesOne(t *testing.T) {
	for _, keyJSON := range []string{"", "  "} {
		a, err := newMockIssuer(mountTestIssuer, keyJSON)
		if err != nil {
			t.Fatalf("newMockIssuer(%q): %v", keyJSON, err)
		}
		b, err := newMockIssuer(mountTestIssuer, keyJSON)
		if err != nil {
			t.Fatalf("newMockIssuer(%q) again: %v", keyJSON, err)
		}
		if ka, kb := servedKid(t, a), servedKid(t, b); ka == kb {
			t.Errorf("newMockIssuer(%q) twice gave one kid %q, want two generated keys", keyJSON, ka)
		}
	}
}

func TestTaggedNewMockIssuerRejectsABadKey(t *testing.T) {
	const bad = `{"x":1}`
	_, err := newMockIssuer(mountTestIssuer, bad)
	if err == nil {
		t.Fatal("accepted a bad key")
	}
	if strings.Contains(err.Error(), bad) {
		t.Errorf("error quotes the key: %q", err.Error())
	}
}

const unauthorizedBody = `{"error":"unauthorized"}`

var gateToken = strings.Repeat("0a1b2c3d", 8)

type gateRoute struct {
	name, method, path, body, dsnHost string
	h                                 http.Handler
}

// gateRoutes builds the five mock routes the way main.go mounts them. The four DSN
// routes reach a database only when the gate lets a valid body through.
func gateRoutes(t *testing.T, logs *strings.Builder) []gateRoute {
	t.Helper()
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	logger := slog.New(slog.NewTextHandler(logs, nil))
	_, login := mockIssuerRoutes("development", "true", gateway.CORS([]string{"https://app.ascomply.test"}), logger)
	if login == nil {
		t.Fatal(`mockIssuerRoutes("development", "true") returned no login handler`)
	}
	dsn := func(route string) string { return "postgres://u@" + route + "-dsn-marker.invalid:5432/db" }
	const link = "abcdefghijklmnopqrstuvwxyz-_ABCDEFGHIJKLMNO"
	return []gateRoute{
		{"login", http.MethodPost, "/auth/login", `{}`, "", login},
		{"member", http.MethodPost, "/auth/mock/member", `{"user_id":"` + uuid.NewString() + `","tenant_id":"` + uuid.NewString() +
			`","role":"admin","display_name":"Ada Okafor","email":"ada@example.test"}`,
			"member-dsn-marker.invalid", mockMemberRoute(dsn("member"), logger)},
		{"invitation-token", http.MethodPost, "/auth/mock/invitation-token", `{"tenant_id":"` + uuid.NewString() +
			`","invitation_id":"` + uuid.NewString() + `","token":"` + link + `"}`,
			"invitation-token-dsn-marker.invalid", mockInvitationTokenRoute(dsn("invitation-token"), logger)},
		{"deletion-link-token", http.MethodPost, "/auth/mock/deletion-link-token", `{"tenant_id":"` + uuid.NewString() +
			`","user_id":"` + uuid.NewString() + `","token":"` + link + `"}`,
			"deletion-link-token-dsn-marker.invalid", mockDeletionLinkTokenRoute(dsn("deletion-link-token"), logger)},
		{"readiness-confirm-token", http.MethodPost, "/auth/mock/readiness-confirm-token", `{"email":"a@b.io","token":"` + link + `"}`,
			"readiness-confirm-token-dsn-marker.invalid", mockReadinessConfirmTokenRoute(dsn("readiness-confirm-token"), logger)},
	}
}

func serveGate(t *testing.T, r gateRoute, method, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, method, r.path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

// wantRefused asserts the gate's 401 and that no grant, setter or mint ran.
func wantRefused(t *testing.T, r gateRoute, rec *httptest.ResponseRecorder, logs *strings.Builder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("%s: %d (body %s), want 401", r.name, rec.Code, rec.Body.String())
		return
	}
	if got := strings.TrimSpace(rec.Body.String()); got != unauthorizedBody {
		t.Errorf("%s: body %s, want %s", r.name, got, unauthorizedBody)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("%s: Content-Type %q, want application/json", r.name, got)
	}
	if strings.Contains(rec.Body.String(), "access_token") {
		t.Errorf("%s: a refusal carries an access_token: %s", r.name, rec.Body.String())
	}
	if r.dsnHost != "" && strings.Contains(logs.String(), r.dsnHost) {
		t.Errorf("%s: the handler reached its database (log names %s): %s", r.name, r.dsnHost, logs.String())
	}
}

func TestTaggedMockRoutesRefuseWithoutTheToken(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "nightly")
	t.Setenv("GATEWAY_MOCK_ISSUER_TOKEN", gateToken)
	var logs strings.Builder
	routes := gateRoutes(t, &logs)
	if len(routes) != 5 {
		t.Fatalf("%d routes under test, want 5", len(routes))
	}
	for _, r := range routes {
		t.Run(r.name, func(t *testing.T) {
			wantRefused(t, r, serveGate(t, r, r.method, r.body, nil), &logs)
			wantRefused(t, r, serveGate(t, r, r.method, r.body, map[string]string{"X-Mock-Issuer-Token": ""}), &logs)
		})
	}
}

func TestTaggedMockRoutesRefuseAWrongToken(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "nightly")
	t.Setenv("GATEWAY_MOCK_ISSUER_TOKEN", gateToken)
	var logs strings.Builder
	routes := gateRoutes(t, &logs)
	if len(routes) != 5 {
		t.Fatalf("%d routes under test, want 5", len(routes))
	}
	wrong := map[string]map[string]string{
		"last character changed": {"X-Mock-Issuer-Token": gateToken[:len(gateToken)-1] + "e"},
		"one character longer":   {"X-Mock-Issuer-Token": gateToken + "0"},
		"upper case":             {"X-Mock-Issuer-Token": strings.ToUpper(gateToken)},
		"X-Gateway-Token":        {"X-Gateway-Token": gateToken},
		"Authorization Bearer":   {"Authorization": "Bearer " + gateToken},
	}
	for _, r := range routes {
		for name, hdr := range wrong {
			t.Run(r.name+"/"+name, func(t *testing.T) {
				wantRefused(t, r, serveGate(t, r, r.method, r.body, hdr), &logs)
			})
		}
	}
}

// An OPTIONS with no Origin passes gateway.CORS and would otherwise reach the minting handler.
func TestTaggedMockLoginRefusesAPreflightWithoutAnOrigin(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "nightly")
	t.Setenv("GATEWAY_MOCK_ISSUER_TOKEN", gateToken)
	var logs strings.Builder
	login := gateRoutes(t, &logs)[0]
	if login.name != "login" {
		t.Fatalf("first route is %q, want login", login.name)
	}
	wantRefused(t, login, serveGate(t, login, http.MethodOptions, "", nil), &logs)
}

func TestTaggedMockRoutesRefuseEverythingWhenTheTokenIsUnset(t *testing.T) {
	for _, state := range []string{"empty", "unset"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("DEPLOY_ENV", "nightly")
			t.Setenv("GATEWAY_MOCK_ISSUER_TOKEN", "")
			if state == "unset" {
				if err := os.Unsetenv("GATEWAY_MOCK_ISSUER_TOKEN"); err != nil {
					t.Fatal(err)
				}
			}
			var logs strings.Builder
			routes := gateRoutes(t, &logs)
			if len(routes) != 5 {
				t.Fatalf("%d routes under test, want 5", len(routes))
			}
			for _, r := range routes {
				t.Run(r.name, func(t *testing.T) {
					for _, hdr := range []map[string]string{nil, {"X-Mock-Issuer-Token": ""}, {"X-Mock-Issuer-Token": "anything"}} {
						wantRefused(t, r, serveGate(t, r, r.method, r.body, hdr), &logs)
					}
				})
			}
		})
	}
}

func TestTaggedMockIssuerWarnsWhenTheTokenIsUnset(t *testing.T) {
	boot := func(t *testing.T, token string) []string {
		t.Helper()
		t.Setenv("AUTH_ISSUER", mountTestIssuer)
		t.Setenv("DEPLOY_ENV", "nightly")
		t.Setenv("GATEWAY_MOCK_ISSUER_TOKEN", token)
		var logs strings.Builder
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		if _, login := mockIssuerRoutes("development", "true", gateway.CORS([]string{"https://app.ascomply.test"}), logger); login == nil {
			t.Fatal("mockIssuerRoutes returned no login handler")
		}
		return strings.Split(strings.TrimSpace(logs.String()), "\n")
	}

	t.Run("unset", func(t *testing.T) {
		lines := boot(t, "")
		if len(lines) != 1 || !strings.Contains(lines[0], "GATEWAY_MOCK_ISSUER_TOKEN") {
			t.Errorf("boot log %q, want one line naming GATEWAY_MOCK_ISSUER_TOKEN", lines)
		}
		if strings.Contains(strings.Join(lines, "\n"), "unauthenticated login is live") {
			t.Errorf("boot log %q says login is live while every route refuses", lines)
		}
	})

	t.Run("set", func(t *testing.T) {
		lines := boot(t, gateToken)
		joined := strings.Join(lines, "\n")
		if len(lines) != 1 || !strings.Contains(joined, "X-Mock-Issuer-Token") {
			t.Errorf("boot log %q, want one line naming X-Mock-Issuer-Token", lines)
		}
		if strings.Contains(joined, gateToken) {
			t.Errorf("boot log carries the token: %s", joined)
		}
		if strings.Contains(joined, "unauthenticated login is live") {
			t.Errorf("boot log %q says login is unauthenticated while the gate is on", lines)
		}
	})
}

func TestTaggedMockRoutesServeWithTheToken(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "nightly")
	t.Setenv("GATEWAY_MOCK_ISSUER_TOKEN", gateToken)
	var logs strings.Builder
	routes := gateRoutes(t, &logs)
	if len(routes) != 5 {
		t.Fatalf("%d routes under test, want 5", len(routes))
	}
	hdr := map[string]string{"X-Mock-Issuer-Token": gateToken}

	t.Run("login mints a token that verifies against the served JWKS", func(t *testing.T) {
		t.Setenv("AUTH_ISSUER", mountTestIssuer)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		jwks, login := mockIssuerRoutes("development", "true", gateway.CORS([]string{"https://app.ascomply.test"}), logger)
		if jwks == nil || login == nil {
			t.Fatal("mockIssuerRoutes returned no handlers")
		}
		srv := httptest.NewServer(jwks)
		t.Cleanup(srv.Close)
		verifier, err := auth.NewVerifier(auth.Config{Issuer: mountTestIssuer, JWKSURL: srv.URL, Logger: logger})
		if err != nil {
			t.Fatalf("verifier: %v", err)
		}
		r := gateRoute{name: "login", path: "/auth/login", h: login}
		rec := serveGate(t, r, http.MethodPost, `{}`, hdr)
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || rec.Code != http.StatusOK || tok.AccessToken == "" {
			t.Fatalf("login with the right header = %d %s, want 200 and an access_token (decode err %v)", rec.Code, rec.Body.String(), err)
		}
		if _, err := verifier.Verify(t.Context(), tok.AccessToken); err != nil {
			t.Errorf("the minted token does not verify against the served JWKS: %v", err)
		}
	})

	// A malformed body is refused by the handler itself, so the gate let the request in.
	for _, r := range routes[1:] {
		t.Run(r.name, func(t *testing.T) {
			rec := serveGate(t, r, r.method, `{`, hdr)
			if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusBadRequest || got != `{"error":"invalid request body"}` {
				t.Errorf("POST { with the right header = %d %s, want 400 invalid request body", rec.Code, got)
			}
		})
	}
}

func TestTaggedMockRoutesStayOpenOffTheNightly(t *testing.T) {
	for _, env := range []string{"pr-7", ""} {
		t.Run("DEPLOY_ENV="+env, func(t *testing.T) {
			t.Setenv("DEPLOY_ENV", env)
			t.Setenv("GATEWAY_MOCK_ISSUER_TOKEN", "")
			var logs strings.Builder
			routes := gateRoutes(t, &logs)
			if len(routes) != 5 {
				t.Fatalf("%d routes under test, want 5", len(routes))
			}
			rec := serveGate(t, routes[0], http.MethodPost, `{}`, nil)
			var tok struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || rec.Code != http.StatusOK || tok.AccessToken == "" {
				t.Errorf("POST /auth/login {} with no header = %d %s, want 200 and an access_token (decode err %v)", rec.Code, rec.Body.String(), err)
			}
			for _, r := range routes[1:] {
				rec := serveGate(t, r, r.method, `{`, nil)
				if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusBadRequest || got != `{"error":"invalid request body"}` {
					t.Errorf("%s: POST { with no header = %d %s, want 400 invalid request body", r.name, rec.Code, got)
				}
			}
			if !strings.Contains(logs.String(), "unauthenticated login is live") {
				t.Errorf("boot log %q lacks the unauthenticated-login warning", logs.String())
			}
		})
	}
}

// The verifier fetches JWKS with no header, and a browser preflight carries an Origin.
func TestTaggedMockGateLeavesJWKSAndPreflightOpen(t *testing.T) {
	const origin = "https://app.ascomply.test"
	t.Setenv("AUTH_ISSUER", mountTestIssuer)
	t.Setenv("DEPLOY_ENV", "nightly")
	t.Setenv("GATEWAY_MOCK_ISSUER_TOKEN", gateToken)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwks, login := mockIssuerRoutes("development", "true", gateway.CORS([]string{origin}), logger)
	if jwks == nil || login == nil {
		t.Fatalf("mockIssuerRoutes = (jwks %v, login %v), want both handlers", jwks, login)
	}

	rec := httptest.NewRecorder()
	jwks.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil || rec.Code != http.StatusOK || len(set.Keys) != 1 || set.Keys[0].Kty != "EC" {
		t.Errorf("GET jwks with no header = %d %s, want 200 with one EC key (decode err %v)", rec.Code, rec.Body.String(), err)
	}

	r := httptest.NewRequest(http.MethodOptions, "/auth/login", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	rec = httptest.NewRecorder()
	login.ServeHTTP(rec, r)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); rec.Code != http.StatusNoContent || got != origin {
		t.Errorf("preflight with an allowed Origin and no header = %d with Access-Control-Allow-Origin %q, want 204 and %q", rec.Code, got, origin)
	}
}
