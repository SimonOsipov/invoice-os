// Command portfolio is the 02 Portfolio context service. It serves the platform
// kit's /healthz + /readyz plus the /v1/entities... CRUD + lifecycle routes
// (M3-03): entity onboarding, read/list/filter, update, and offboard/onboard
// status transitions — all resolved under RLS via internal/portfolio.Store.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/portfolio"
)

func main() {
	app, err := platform.New("portfolio")
	if err != nil {
		log.Fatalf("portfolio: startup: %v", err)
	}

	// The invoice_app (NOBYPASSRLS) connection pool. DATABASE_URL is required — a
	// portfolio service that cannot reach its database is misconfigured, not
	// degraded. pgxpool.New is lazy (it connects on first use), so an unreachable
	// DB surfaces via /readyz rather than blocking startup.
	pool, err := db.NewPool(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("portfolio: db pool: %v", err)
	}
	defer pool.Close()

	// Readiness: the app-role pool can round-trip to Postgres. Liveness (/healthz)
	// stays up regardless; /readyz flips to 503 while the DB is unreachable.
	app.Ready("database", func(ctx context.Context) error { return pool.Ping(ctx) })

	// Stub endpoint from the M2-04 skeleton — kept as the trivial reachability probe
	// the gateway's proxy tests exercise (/api/portfolio/v1/ping). Real endpoints
	// live alongside it.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"portfolio","status":"ok"}`))
	})

	// /v1/entities... — the portfolio CRUD + lifecycle surface, resolved under
	// RLS. Reached via the gateway as /api/portfolio/v1/entities... (the prefix
	// is stripped upstream).
	store := portfolio.NewStore(pool)
	app.Mux.HandleFunc("POST /v1/entities", portfolio.CreateHandler(store.Create, app.Logger))
	app.Mux.HandleFunc("GET /v1/entities/{id}", portfolio.GetHandler(store.GetByID, app.Logger))
	app.Mux.HandleFunc("GET /v1/entities", portfolio.ListHandler(store.List, app.Logger))
	app.Mux.HandleFunc("PATCH /v1/entities/{id}", portfolio.UpdateHandler(store.Update, app.Logger))
	app.Mux.HandleFunc("POST /v1/entities/{id}/offboard", portfolio.OffboardHandler(
		func(ctx context.Context, id string) (portfolio.Entity, error) {
			return store.SetStatus(ctx, id, "archived")
		}, app.Logger))
	app.Mux.HandleFunc("POST /v1/entities/{id}/onboard", portfolio.OnboardHandler(
		func(ctx context.Context, id string) (portfolio.Entity, error) {
			return store.SetStatus(ctx, id, "active")
		}, app.Logger))

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("portfolio: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("portfolio: %s is required", key)
	}
	return v
}
