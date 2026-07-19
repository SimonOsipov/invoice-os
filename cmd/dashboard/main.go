// Command dashboard is the 06 Dashboard context service. It serves the
// platform kit's /healthz + /readyz plus the /v1/rollup portfolio-summary
// read (M4-07) -- resolved under RLS via internal/dashboard.Store.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/dashboard"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func main() {
	app, err := platform.New("dashboard")
	if err != nil {
		log.Fatalf("dashboard: startup: %v", err)
	}

	// The invoice_app (NOBYPASSRLS) connection pool. DATABASE_URL is required — a
	// dashboard service that cannot reach its database is misconfigured, not
	// degraded. pgxpool.New is lazy (it connects on first use), so an unreachable
	// DB surfaces via /readyz rather than blocking startup.
	pool, err := db.NewPool(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("dashboard: db pool: %v", err)
	}
	defer pool.Close()

	// Readiness: the app-role pool can round-trip to Postgres. Liveness (/healthz)
	// stays up regardless; /readyz flips to 503 while the DB is unreachable.
	app.Ready("database", func(ctx context.Context) error { return pool.Ping(ctx) })

	// Stub endpoint from the M2-04 skeleton — kept as the trivial reachability probe
	// the gateway's proxy tests exercise (/api/dashboard/v1/ping). Real endpoints
	// live alongside it.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"dashboard","status":"ok"}`))
	})

	// /v1/rollup — the portfolio-summary read, resolved under RLS. Reached via
	// the gateway as /api/dashboard/v1/rollup (the prefix is stripped upstream).
	store := dashboard.NewStore(pool)
	app.Mux.HandleFunc("GET /v1/rollup", dashboard.RollupHandler(store.Rollup, app.Logger))

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("dashboard: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("dashboard: %s is required", key)
	}
	return v
}
