// Command gateway is the FiscalBridge API edge (M2-11). It verifies caller JWTs,
// injects the verified tenant/user/role context that downstream services and RLS
// depend on, and reverse-proxies each request to the owning context service. In
// dev/CI it also embeds a mock issuer (mint + JWKS) so a token can be minted and
// verified end to end with the exact code path used against Supabase GoTrue after
// M8 — the cutover is then a change to AUTH_ISSUER/AUTH_JWKS_URL, not to code.
package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// routedServices are the seven context services the gateway fronts, in wedge
// order. Each has a corresponding <NAME>_URL env var giving its base URL over
// Railway private networking (wired in M2-12). opsconsole joins at M7.
var routedServices = []string{
	"tenancy", "portfolio", "invoice", "validation",
	"submission", "dashboard", "notifications",
}

func main() {
	app, err := platform.New("gateway")
	if err != nil {
		log.Fatalf("gateway: startup: %v", err)
	}

	// Migrate before serving: the gateway is the fleet's single in-network
	// migrator (docs/migrations.md §2). It applies every pending migration as the
	// migrator role here — before app.Run starts the listener — so the schema is
	// fully migrated by the time /healthz first answers. That is the
	// schema-before-fleet barrier CI relies on when it health-gates the gateway
	// ahead of the seven context services (which never migrate). Fatal on failure:
	// a gateway that cannot migrate must never come up healthy.
	if err := db.MigrateUp(context.Background(), mustEnv("DATABASE_MIGRATION_URL"), migrations.FS); err != nil {
		log.Fatalf("gateway: migrate: %v", err)
	}

	verifier, err := auth.NewVerifier(auth.Config{
		Issuer:  mustEnv("AUTH_ISSUER"),
		JWKSURL: mustEnv("AUTH_JWKS_URL"),
		Logger:  app.Logger,
	})
	if err != nil {
		log.Fatalf("gateway: verifier: %v", err)
	}

	upstreams, err := loadUpstreams()
	if err != nil {
		log.Fatalf("gateway: upstreams: %v", err)
	}

	app.Mux.Handle(routePrefix, gateway.Handler(gateway.Options{
		Verifier:  verifier,
		Upstreams: upstreams,
		Logger:    app.Logger,
	}))

	// Dev/CI only: embed the mock issuer so a token can be minted (/auth/login)
	// and its JWKS served for the Verifier to fetch (AUTH_JWKS_URL points back at
	// this gateway). Refused in production regardless of the flag.
	if gateway.MockIssuerEnabled(app.Config.Environment, os.Getenv("GATEWAY_MOCK_ISSUER")) {
		issuer, err := auth.NewMockIssuer(mustEnv("AUTH_ISSUER"))
		if err != nil {
			log.Fatalf("gateway: mock issuer: %v", err)
		}
		app.Mux.Handle("GET /.well-known/jwks.json", issuer.JWKSHandler())
		app.Mux.HandleFunc("POST /auth/login", gateway.MockLoginHandler(issuer))
		app.Logger.Warn("mock issuer enabled — dev/CI only, never production")
	}

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("gateway: %v", err)
	}
}

// routePrefix must match the gateway package's mount point.
const routePrefix = "/api/"

// loadUpstreams reads each routed service's base URL from <NAME>_URL. A missing
// or invalid URL fails startup: a gateway that cannot reach a service it fronts
// must not come up half-wired.
func loadUpstreams() (map[string]*url.URL, error) {
	out := make(map[string]*url.URL, len(routedServices))
	for _, svc := range routedServices {
		key := strings.ToUpper(svc) + "_URL"
		raw := os.Getenv(key)
		if raw == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s=%q: %w", key, raw, err)
		}
		out[svc] = u
	}
	return out, nil
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("gateway: %s is required", key)
	}
	return v
}
