package main

import (
	"os"
	"strings"
	"testing"
)

// GKE-08 AC8: each /healthz/fleet call wakes every scaled-to-zero service, so only a gateway-token holder may make it.
func TestFleetRouteNeedsTheGatewayToken(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	want := `app.Mux.Handle("GET /healthz/fleet", platform.RequireToken(platform.HeaderGatewayToken, gatewayToken)(fleetHandler))`
	if n := strings.Count(s, `"GET /healthz/fleet"`); n != 1 || !strings.Contains(s, want) {
		t.Errorf("main.go registers GET /healthz/fleet %d times, want once as %s", n, want)
	}
}

// GKE-08: GoTrue is ingress-internal; the session check reaches it only with an ID token.
func TestSessionCheckerCarriesTheIDToken(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	want := `gateway.NewSessionChecker(probed["auth"], &http.Client{Timeout: gateway.SessionCheckTimeout, Transport: platform.IDTokenTransport(nil)}`
	if !strings.Contains(string(src), want) {
		t.Errorf("main.go builds the session checker without %s", want)
	}
}
