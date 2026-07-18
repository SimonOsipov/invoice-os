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

	dbsql "github.com/SimonOsipov/invoice-os/db"
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

	// Bootstrap (gated) -> migrate (unconditional) -> seed (gated), all fatal on
	// error and all complete before app.Run opens the listener, so a green
	// /healthz continues to mean "fully provisioned" (task-128). The gateway
	// remains the fleet's single in-network migrator (docs/migrations.md §2):
	// migrate is unconditional regardless of the guard below, exactly as before.
	//
	// The guard reads the RAW os.Getenv("ENVIRONMENT")/os.Getenv("GATEWAY_DB_BOOTSTRAP")
	// — never app.Config.Environment. internal/platform/config.go:44 substitutes
	// "development" for an unset ENVIRONMENT, which would silently re-open the
	// fail-open hole BootstrapEnabled's allowlist exists to close (QA F1). With
	// the guard off, none of DATABASE_SUPERUSER_URL / INVOICE_*_PASSWORD are
	// required — production boots without any of them set.
	if err := db.Provision(context.Background(), db.ProvisionConfig{
		Environment:   os.Getenv("ENVIRONMENT"),
		BootstrapFlag: os.Getenv("GATEWAY_DB_BOOTSTRAP"),
		SuperuserDSN:  os.Getenv("DATABASE_SUPERUSER_URL"),
		MigrationDSN:  mustEnv("DATABASE_MIGRATION_URL"),
		Passwords: db.RolePasswords{
			Migrator: os.Getenv("INVOICE_MIGRATOR_PASSWORD"),
			App:      os.Getenv("INVOICE_APP_PASSWORD"),
			Reader:   os.Getenv("INVOICE_TENANT_READER_PASSWORD"),
		},
		BootstrapFS:  dbsql.FS,
		MigrationsFS: migrations.FS,
		SeedFS:       dbsql.FS,
	}); err != nil {
		log.Fatalf("gateway: provision: %v", err)
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

	// CORS layer, composed OUTSIDE the JWT verifier: the app SPA and the gateway are
	// separate origins, so a browser preflight (OPTIONS, no bearer) must be answered
	// before the verifier would 401 it. Allowed origins come from CORS_ALLOWED_ORIGINS
	// (comma-separated); empty grants no browser origin (the production default).
	withCORS := gateway.CORS(strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ","))

	app.Mux.Handle(routePrefix, withCORS(gateway.Handler(gateway.Options{
		Verifier:  verifier,
		Upstreams: upstreams,
		Logger:    app.Logger,
	})))

	// Public fleet-health roll-up: the seven context services are private-network-only,
	// so this is how CI (and a future status page) see their health through the one public
	// backend surface. Outside /api/ and outside the verifier — operational, not tenant data.
	app.Mux.HandleFunc("GET /healthz/fleet", gateway.FleetHealthHandler(upstreams, app.Logger))

	// Dev/CI only: embed the mock issuer so a token can be minted (/auth/login)
	// and its JWKS served for the Verifier to fetch (AUTH_JWKS_URL points back at
	// this gateway). Refused in production regardless of the flag.
	if gateway.MockIssuerEnabled(app.Config.Environment, os.Getenv("GATEWAY_MOCK_ISSUER")) {
		issuer, err := auth.NewMockIssuer(mustEnv("AUTH_ISSUER"))
		if err != nil {
			log.Fatalf("gateway: mock issuer: %v", err)
		}
		app.Mux.Handle("GET /.well-known/jwks.json", issuer.JWKSHandler())
		// /auth/login is called cross-origin by the browser, so wrap it in the same CORS
		// layer. Register POST (the mint) and OPTIONS (the preflight CORS answers) — a
		// method-scoped POST route alone would 405 the preflight instead of letting CORS
		// handle it.
		login := withCORS(gateway.MockLoginHandler(issuer))
		app.Mux.Handle("POST /auth/login", login)
		app.Mux.Handle("OPTIONS /auth/login", login)
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
