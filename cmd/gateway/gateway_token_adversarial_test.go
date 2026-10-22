package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

type recordedCall struct {
	path   string
	header http.Header
}

// recordingStub answers like GoTrue and like a context service's /healthz, and records every request.
func recordingStub(t *testing.T) (*url.URL, func() []recordedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, recordedCall{r.URL.Path, r.Header.Clone()})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"` + handoffAccessToken("user-r") + `","refresh_token":"ref-r"}`))
		case "/logout":
			w.WriteHeader(http.StatusNoContent)
		case "/healthz":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub url: %v", err)
	}
	return u, func() []recordedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedCall(nil), calls...)
	}
}

// Core AC-6: the gateway's own routes talk to GoTrue and probe the fleet as themselves, never as the gateway.
func TestGatewayOwnRoutesSendNoGatewayToken(t *testing.T) {
	const token = "gw-own-routes-token"
	stub, calls := recordingStub(t)
	setUpstreamEnv(t, stub.String())
	routed, probed, err := loadUpstreams()
	if err != nil {
		t.Fatalf("loadUpstreams: %v", err)
	}
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
	bearer, err := issuer.Mint(auth.MintOptions{Subject: "11111111-1111-1111-1111-111111111111", Role: "authenticated", TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	log := slog.New(slog.DiscardHandler)
	api, fleet := gatewayHandlers(verifier, nilURLSessions(), routed, probed, map[string]string{"auth": ".well-known/jwks.json"}, log, token)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(probed["auth"], site, 0, log, nil, noPendingInvite)
	hand := handoffMux(t, probed["auth"], true)

	serveRegistration(reg.Register, http.MethodPost, "/auth/register", `{"email":"new@corp.example","password":"Corr3ct-Horse"}`)
	serveForm(handoffVerify(probed["auth"], site, nil), "/auth/verify", "type=signup&token=abc")
	postJSON(hand, "/auth/sign-in", handoffAllowedOrigin, signInJSON("a@corp.example"))
	postJSON(hand, "/auth/refresh", handoffAllowedOrigin, `{"refresh_token":"ref-presented"}`)
	postJSON(hand, "/auth/sign-out", handoffAllowedOrigin, `{"refresh_token":"ref-presented"}`)
	fleet(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz/fleet", nil))

	// Control: the proxy on the same stub does carry the token, so the recorder can see it.
	r := httptest.NewRequest(http.MethodGet, "/api/tenancy/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("control GET /api/tenancy/v1/me = %d, want 200", rec.Code)
	}

	byPath := map[string][]recordedCall{}
	for _, c := range calls() {
		byPath[c.path] = append(byPath[c.path], c)
	}
	for _, p := range []string{"/signup", "/verify", "/token", "/logout", "/healthz"} {
		if len(byPath[p]) == 0 {
			t.Errorf("the stub saw no call on %s, so that route is unproven (saw %v)", p, keys(byPath))
		}
		for _, c := range byPath[p] {
			for _, h := range []string{"X-Gateway-Token", "X-S2S-Token", "X-Tenant-ID", "X-User-ID"} {
				if got := c.header.Values(h); len(got) != 0 {
					t.Errorf("%s call carries %s = %q, want none", p, h, got)
				}
			}
		}
	}
	ctl := byPath["/v1/me"]
	if len(ctl) != 1 || ctl[0].header.Get("X-Gateway-Token") != token {
		t.Fatalf("control call = %+v, want one call carrying X-Gateway-Token %q", ctl, token)
	}
}

func keys(m map[string][]recordedCall) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
