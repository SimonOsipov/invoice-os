// Command invoice is the 03 Invoice context service. It serves the platform
// kit's /healthz + /readyz plus the /v1/invoices... CRUD + guarded-transition
// routes (M4-02): manual create, read/list, and the single guarded
// transitions endpoint — all resolved under RLS via internal/invoice.Store —
// plus the validate gate (M4-04), which evaluates an invoice against 04's
// active rule set and is the only route to the validated status.
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
	app.Mux.HandleFunc("PATCH /v1/invoices/{id}", invoice.EditHandler(store.Edit, app.Logger))

	// POST /v1/invoices/{id}/validate -- THE validate gate ([gate-endpoint],
	// M4-04): the ONLY route by which an invoice reaches validated, and the
	// on-demand re-validate endpoint. Reached via the gateway as
	// /api/invoice/v1/invoices/{id}/validate; the gateway forwards arbitrary
	// subpaths under its generic /api/ prefix, so this route needs no gateway
	// change.
	//
	// Both vars are REQUIRED ([env-wiring]) -- mustEnv log.Fatalf's on an unset
	// one, so an invoice service that cannot reach 04 fails fast at boot rather
	// than serving a surface that silently cannot validate. This service needs
	// its OWN copy of each: Railway vars are per-service, and the gateway's
	// VALIDATION_URL is not inherited here.
	//
	// VALIDATION_URL must carry NO trailing slash -- the client concatenates
	// "/v1/validate/batch" onto it (validator.go).
	validator := invoice.NewValidator(mustEnv("VALIDATION_URL"), mustEnv("S2S_TOKEN"), nil)
	gate := invoice.NewGate(store, validator)
	app.Mux.HandleFunc("POST /v1/invoices/{id}/validate", invoice.ValidateHandler(gate.Validate, app.Logger))

	// /v1/imports -- the bulk CSV/XLSX import surface (M4-03), reusing the SAME
	// *invoice.Store instance above so an import's Create calls run through the
	// identical invoice-write path as the manual endpoints. Reached via the
	// gateway as /api/invoice/v1/imports (same mux, same middleware chain, so
	// identity/tenant are already in context).
	// Reuses the SAME gate constructed above for the single-invoice validate
	// route: importer.NewService's third parameter is an importer-local
	// interface (task-114/M4-04-07's Stage-1 addendum F3) that *invoice.Gate
	// satisfies structurally (its Evaluate/ValidateBatch signatures match
	// exactly) -- no second gate, no adapter type, one gate driving both the
	// manual validate endpoint and the importer's batch pre-check
	// ([import-validates]/[dry-run-evaluates]).
	// /v1/imports/preview sits on the same mux and middleware chain but is
	// deliberately STATELESS ([preview-stateless]): it echoes back the header
	// and first few rows of the bytes just uploaded, touching no store, no
	// service and no entity, so it takes neither impSvc nor a logger.
	impStore := importer.NewStore(pool)
	impSvc := importer.NewService(impStore, store, gate)
	app.Mux.HandleFunc("POST /v1/imports", importer.CreateHandler(impSvc.Import, app.Logger))
	app.Mux.HandleFunc("POST /v1/imports/preview", importer.PreviewHandler())

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
