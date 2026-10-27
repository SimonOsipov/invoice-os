package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// handoffHandlers must wrap ConsoleHandoff in the verifier and the staff gate, and share the exchange store.
func TestHandoffHandlers_ConsoleHandoffIsStaffOnlyAndRedeemsThroughExchange(t *testing.T) {
	const issuer = "https://issuer.test"
	mock, err := auth.NewMockIssuer(issuer)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(mock.JWKSHandler())
	t.Cleanup(jwks.Close)
	verifier, err := auth.NewVerifier(auth.Config{Issuer: issuer, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	h := handoffHandlers(goTrueAnswering(t, `{}`), nil, gateway.NewSessionChecker(nil, nil, time.Now, log), log, nil, verifier, nil, nil)
	staff, _ := mock.Mint(auth.MintOptions{Subject: "11111111-1111-1111-1111-111111111111", Staff: true})
	customer, _ := mock.Mint(auth.MintOptions{Subject: "11111111-1111-1111-1111-111111111111", TenantID: "tenant-a"})

	post := func(bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/auth/console-handoff", strings.NewReader(`{"state":"`+handoffState+`"}`))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		h.ConsoleHandoff.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no bearer = %d, want 401", rec.Code)
	}
	if rec := post(customer); rec.Code != http.StatusForbidden {
		t.Errorf("non-staff bearer = %d, want 403: %s", rec.Code, rec.Body.String())
	}

	rec := post(staff)
	if rec.Code != http.StatusOK {
		t.Fatalf("staff bearer = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var minted map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil || minted["code"] == "" {
		t.Fatalf("mint body = %s, want {\"code\":...}", rec.Body.String())
	}
	body, _ := json.Marshal(map[string]string{"code": minted["code"], "state": handoffState})
	rec = postJSON(h.Exchange, "/auth/exchange", handoffAllowedOrigin, string(body))
	var got map[string]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got) != 1 || got["access_token"] != staff {
		t.Errorf("exchange = %d %s, want exactly {\"access_token\": <the staff bearer>}", rec.Code, rec.Body.String())
	}
}

// handoffHandlers must put the session check between the verifier and the staff gate.
func TestHandoffHandlers_ConsoleHandoffRefusesARevokedSession(t *testing.T) {
	const issuer = "https://gotrue.wiring.test/auth/v1"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := func(n *big.Int) string { return base64.RawURLEncoding.EncodeToString(n.FillBytes(make([]byte, 32))) }
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"keys":[{"kty":"EC","crv":"P-256","alg":"ES256","use":"sig","kid":"k1","x":%q,"y":%q}]}`, b64(key.X), b64(key.Y))
	}))
	t.Cleanup(jwks.Close)
	verifier, err := auth.NewVerifier(auth.Config{Issuer: issuer, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatal(err)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": issuer, "sub": "11111111-1111-1111-1111-111111111111", "aud": "authenticated", "role": "authenticated",
		"session_id": "00a6daf2-8c5a-46d0-a9c5-cd9bf4fa2e08", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"app_metadata": map[string]any{"staff": true},
	})
	tok.Header["kid"] = "k1"
	staff, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}

	postWith := func(userStatus int, userBody string) *httptest.ResponseRecorder {
		gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(userStatus)
			_, _ = io.WriteString(w, userBody)
		}))
		defer gotrue.Close()
		authURL, _ := url.Parse(gotrue.URL)
		log := slog.New(slog.DiscardHandler)
		h := handoffHandlers(authURL, nil, gateway.NewSessionChecker(authURL, http.DefaultClient, time.Now, log), log, nil, verifier, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/auth/console-handoff", strings.NewReader(`{"state":"`+handoffState+`"}`))
		req.Header.Set("Authorization", "Bearer "+staff)
		rec := httptest.NewRecorder()
		h.ConsoleHandoff.ServeHTTP(rec, req)
		return rec
	}
	if rec := postWith(http.StatusForbidden, `{"code":403,"error_code":"session_not_found","msg":"gone"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("staff bearer, revoked session = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	// Control: the same token with a live session mints, so the 401 above is the session check's.
	if rec := postWith(http.StatusOK, `{"id":"11111111-1111-1111-1111-111111111111"}`); rec.Code != http.StatusOK {
		t.Errorf("control: staff bearer, live session = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
