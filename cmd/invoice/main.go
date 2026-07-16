// Command invoice is the 03 Invoice context service. It serves the platform
// kit's /healthz + /readyz plus the /v1/invoices... CRUD + guarded-transition
// routes (M4-02): manual create, read/list, and the single guarded
// transitions endpoint — all resolved under RLS via internal/invoice.Store.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func main() {
	app, err := platform.New("invoice")
	if err != nil {
		log.Fatalf("invoice: startup: %v", err)
	}

	// The invoice_app (NOBYPASSRLS) connection pool. DATABASE_URL is required — an
	// invoice service that cannot reach its database is misconfigured, not
	// degraded. pgxpool.New is lazy (it connects on first use), so an unreachable
	// DB surfaces via /readyz rather than blocking startup.
	pool, err := db.NewPool(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("invoice: db pool: %v", err)
	}
	defer pool.Close()

	// Readiness: the app-role pool can round-trip to Postgres. Liveness (/healthz)
	// stays up regardless; /readyz flips to 503 while the DB is unreachable.
	app.Ready("database", func(ctx context.Context) error { return pool.Ping(ctx) })

	// Stub endpoint from the M2-04 skeleton — kept as the trivial reachability probe
	// the gateway's proxy tests exercise (/api/invoice/v1/ping). Real endpoints
	// live alongside it.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"invoice","status":"ok"}`))
	})

	// /v1/invoices... — the invoice CRUD + guarded-transition surface, resolved
	// under RLS. Reached via the gateway as /api/invoice/v1/invoices... (the
	// prefix is stripped upstream).
	store := invoice.NewStore(pool)
	app.Mux.HandleFunc("POST /v1/invoices", invoice.CreateHandler(store.Create, app.Logger))
	app.Mux.HandleFunc("GET /v1/invoices/{id}", invoice.GetHandler(store.Get, app.Logger))
	app.Mux.HandleFunc("GET /v1/invoices", invoice.ListHandler(store.List, app.Logger))
	app.Mux.HandleFunc("POST /v1/invoices/{id}/transitions", invoice.TransitionHandler(store.Transition, app.Logger))

	// /v1/imports -- the bulk CSV/XLSX import surface (M4-03), reusing the SAME
	// *invoice.Store instance above so an import's Create calls run through the
	// identical invoice-write path as the manual endpoints. Reached via the
	// gateway as /api/invoice/v1/imports (same mux, same middleware chain, so
	// identity/tenant are already in context).
	impStore := importer.NewStore(pool)
	impSvc := importer.NewService(impStore, store)
	app.Mux.HandleFunc("POST /v1/imports", importer.CreateHandler(impSvc.Import, app.Logger))

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("invoice: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("invoice: %s is required", key)
	}
	return v
}
