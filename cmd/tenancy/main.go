// Command tenancy is the 01 Tenancy context service. It serves the platform kit's
// /healthz + /readyz plus GET /v1/me — the first endpoint that reads real data:
// it resolves the gateway-injected caller to their tenant through an RLS-scoped
// query (M2-13, the mock-login round trip's server side).
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/tenancy"
)

func main() {
	app, err := platform.New("tenancy")
	if err != nil {
		log.Fatalf("tenancy: startup: %v", err)
	}

	// The invoice_app (NOBYPASSRLS) connection pool. DATABASE_URL is required — a
	// tenancy service that cannot reach its database is misconfigured, not degraded.
	// pgxpool.New is lazy (it connects on first use), so an unreachable DB surfaces
	// via /readyz rather than blocking startup.
	pool, err := db.NewPool(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("tenancy: db pool: %v", err)
	}
	defer pool.Close()

	// Readiness: the app-role pool can round-trip to Postgres. Liveness (/healthz)
	// stays up regardless; /readyz flips to 503 while the DB is unreachable.
	app.Ready("database", func(ctx context.Context) error { return pool.Ping(ctx) })

	// Stub endpoint from the M2-04 skeleton — kept as the trivial reachability probe
	// the gateway's proxy tests exercise (/api/tenancy/v1/ping). Real endpoints live
	// alongside it.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"tenancy","status":"ok"}`))
	})

	// GET /v1/me — the caller's tenant + user, resolved under RLS. Reached via the
	// gateway as /api/tenancy/v1/me (the prefix is stripped upstream).
	store := tenancy.NewStore(pool)
	app.Mux.HandleFunc("GET /v1/me", tenancy.MeHandler(store.CurrentTenant, app.Logger))

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("tenancy: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("tenancy: %s is required", key)
	}
	return v
}
