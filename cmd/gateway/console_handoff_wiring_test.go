package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	h := handoffHandlers(goTrueAnswering(t, `{}`), nil, gateway.NewSessionChecker(nil, nil, time.Now, log), log, nil, verifier)
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
