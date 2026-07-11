// Command validation is the 04 Rules-as-Data Validation Engine service. It serves
// the platform kit's /healthz + /readyz plus the /v1/validate + /v1/rules/{key}
// routes (M3-04): POST /v1/validate runs a submitted invoice payload through the
// active published rule-set (all nine rule-type evaluators + the CEL guard) and
// answers every collected violation stamped with the rule-set version; PATCH
// /v1/rules/{key} is the M3-06 admin kill-switch that flips a rule's enabled bit
// and audits the flip — both resolved via internal/validation.Store over the
// invoice_app role.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

func main() {
	app, err := platform.New("validation")
	if err != nil {
		log.Fatalf("validation: startup: %v", err)
	}

	// The invoice_app (NOBYPASSRLS) connection pool. DATABASE_URL is required — a
	// validation service that cannot reach its database is misconfigured, not
	// degraded. pgxpool.New is lazy (it connects on first use), so an unreachable
	// DB surfaces via /readyz rather than blocking startup.
	pool, err := db.NewPool(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("validation: db pool: %v", err)
	}
	defer pool.Close()

	// Readiness: the app-role pool can round-trip to Postgres. Liveness (/healthz)
	// stays up regardless; /readyz flips to 503 while the DB is unreachable.
	app.Ready("database", func(ctx context.Context) error { return pool.Ping(ctx) })

	// Stub endpoint from the M2-04 skeleton — kept as the trivial reachability probe
	// the gateway's proxy tests exercise (/api/validation/v1/ping). Real endpoints
	// live alongside it.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"validation","status":"ok"}`))
	})

	// /v1/validate + /v1/rules/{key} — the validation engine surface. Reached via
	// the gateway as /api/validation/v1/... (the prefix is stripped upstream). The
	// store reads the active rule-set under RLS-threaded identity; the engine is
	// stateless (all nine evaluators + the CEL guard).
	store := validation.NewStore(pool)
	engine := validation.NewDefaultEngine()

	// The full request body ({"invoice": {...}}) is passed to engine.Evaluate
	// UNCHANGED — the engine's resolvePath roots at p["invoice"], so there is no
	// unwrap/re-wrap seam here (Decision N19; handlers.go payload contract).
	app.Mux.HandleFunc("POST /v1/validate", validation.ValidateHandler(
		func(ctx context.Context, p validation.Payload) (validation.Result, error) {
			rs, err := store.LoadActiveRuleSet(ctx)
			if err != nil {
				return validation.Result{}, err
			}
			return engine.Evaluate(p, rs)
		}, app.Logger))
	app.Mux.HandleFunc("PATCH /v1/rules/{key}", validation.ToggleHandler(store.ToggleRule, app.Logger))

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("validation: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("validation: %s is required", key)
	}
	return v
}
