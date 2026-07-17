// Command validation is the 04 Rules-as-Data Validation Engine service. It serves
// the platform kit's /healthz + /readyz plus the /v1/validate + /v1/rules/{key}
// + /v1/validate/batch routes (M3-04, M4-04): POST /v1/validate runs a submitted invoice payload through the
// active published rule-set (all nine rule-type evaluators + the CEL guard) and
// answers every collected violation stamped with the rule-set version; PATCH
// /v1/rules/{key} is the M3-06 admin kill-switch that flips a rule's enabled bit
// and audits the flip — both resolved via internal/validation.Store over the
// invoice_app role. POST /v1/validate/batch (M4-04-03) is the tenant-free peer
// surface 03 submits batches to: peer-authenticated via S2S_TOKEN, carrying no
// identity, loading the rule-set once per batch.
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

	// POST /v1/validate/batch — the tenant-free peer surface 03 (submission)
	// calls to validate a whole batch in one request (M4-04-03). Unlike the two
	// routes above, it carries NO identity: it is authenticated as a fleet PEER
	// via the shared S2S_TOKEN ([s2s-peer-auth]) and reads no tenant, because
	// rule evaluation is a pure function of (payload, active global rule-set)
	// and there is no tenant-scoped data behind it ([s2s-identity]). Hence
	// LoadActiveRuleSetGlobal rather than LoadActiveRuleSet: the tenant-wrapped
	// loader returns db.ErrNoTenant with no identity in context, so an
	// identity-less peer call structurally cannot use it.
	//
	// S2S_TOKEN is required via mustEnv: an unset var log.Fatalf's at boot
	// rather than starting this endpoint with an empty token that would admit
	// every caller. The var is set in the deploy env (M4-04-08, [env-wiring]).
	// The stateless engine is reused; the rule-set is loaded once per batch,
	// inside the handler.
	app.Mux.Handle("POST /v1/validate/batch", validation.S2SMiddleware(mustEnv("S2S_TOKEN"))(
		validation.BatchValidateHandler(store.LoadActiveRuleSetGlobal, engine, app.Logger)))

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
