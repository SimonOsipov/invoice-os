// Command validation is the 04 Rules-as-Data Validation Engine service. It serves
// the platform kit's /healthz + /readyz plus the /v1/rules/{key} +
// /v1/validate/batch routes. PATCH /v1/rules/{key} answers 401 without an
// identity, otherwise 403, and reaches no database. POST /v1/validate/batch
// is the tenant-free peer surface 03 submits batches to:
// peer-authenticated via S2S_TOKEN, carrying no identity, loading the rule-set
// versions for the batch's dates once per batch (LoadForDates).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/validation"
	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
)

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("validation")
	if err != nil {
		platform.Fatal(slog.Default(), "validation: startup: %v", err)
	}

	// The invoice_app (NOBYPASSRLS) connection pool. DATABASE_URL is required — a
	// validation service that cannot reach its database is misconfigured, not
	// degraded. pgxpool.New is lazy (it connects on first use), so an unreachable
	// DB surfaces via /readyz rather than blocking startup.
	pool, err := db.NewPool(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		platform.Fatal(app.Logger, "validation: db pool: %v", err)
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

	// The validation engine surface, reached via the gateway as
	// /api/validation/v1/... (the prefix is stripped upstream). The engine is
	// stateless; the store reads rules under the invoice_app role.
	store := validation.NewStore(pool)
	engine := validation.NewDefaultEngine()

	// 401 without an identity, otherwise 403; rules change only through the operator kill switch.
	app.Mux.HandleFunc("PATCH /v1/rules/{key}", validation.ToggleHandler())

	// POST /v1/validate/batch — the tenant-free peer surface 03 (submission)
	// calls to validate a whole batch in one request. It carries NO identity: it is
	// authenticated as a fleet PEER via the shared S2S_TOKEN ([s2s-peer-auth]) and reads no tenant, because
	// rule evaluation is a pure function of (payload, the rule-set version
	// in force on its issue date) and there is no tenant-scoped data behind it
	// ([s2s-identity]). Hence LoadForDates rather than LoadActiveRuleSet: the
	// tenant-wrapped loader returns db.ErrNoTenant with no identity in context, so an
	// identity-less peer call structurally cannot use it.
	//
	// S2S_TOKEN is required via mustEnv: an unset var makes platform.Fatal exit at boot
	// rather than starting this endpoint with an empty token that would admit
	// every caller. The var is set in the deploy env (M4-04-08, [env-wiring]).
	// The stateless engine is reused; LoadForDates runs once per batch, inside
	// the handler, and each item is judged by the version for its own date.
	app.Mux.Handle("POST /v1/validate/batch", validation.S2SMiddleware(mustEnv("S2S_TOKEN"))(
		validation.BatchValidateHandler(store.LoadForDates, engine, nil, app.Logger)))

	app.AddBackgroundWorker(codelist.NewWorker(codelist.SyncInterval,
		codelist.NewSyncer(pool, codelist.DefaultBaseURL, nil, app.Logger).SyncAll))

	app.RequireGateway(mustEnv("GATEWAY_TOKEN"), "POST /v1/validate/batch")

	if err := app.Run(context.Background()); err != nil {
		platform.Fatal(app.Logger, "validation: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		platform.Fatal(slog.Default(), "validation: %s is required", key)
	}
	return v
}
