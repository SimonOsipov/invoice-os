// Command submission is the 05 Submission context service. M2-08: it boots the River
// worker pool (the async job spine) alongside the platform kit's /healthz + /readyz and
// drains in-flight jobs within the shutdown window on SIGINT/SIGTERM. The worker connects
// as the app role (invoice_app) and re-establishes tenant context per job — the
// worker-role pattern, docs/migrations.md §8. Real submission handlers arrive in M3.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func main() {
	app, err := platform.New("submission")
	if err != nil {
		log.Fatalf("submission: startup: %v", err)
	}

	ctx := context.Background()

	// Connect as the app role (invoice_app, NOBYPASSRLS) — never the migrator or
	// superuser (docs/migrations.md §1). The worker uses this pool both to operate River's
	// queue and, per job, to open tenant-scoped transactions via db.WithinTenantTx.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// pgx would otherwise build a config from ambient libpq env/defaults for an empty
		// DSN — fail fast so this service can only ever connect as its configured app role.
		log.Fatal("submission: DATABASE_URL is required")
	}
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		log.Fatalf("submission: db pool: %v", err)
	}
	defer pool.Close()

	// /readyz now reflects the DB dependency the worker carries.
	app.Ready("database", pool.Ping)

	// M5-02-04: resolve the configured adapter against the fail-closed production
	// allowlist before the queue starts. Fatal in production (or when APP_ADAPTER is
	// set but not selectable) -- never boot with an unauthorized adapter in prod. In
	// non-production with APP_ADAPTER unset, log a warning and continue with no
	// adapter, keeping the dev fleet's boot green.
	reg := submission.NewDefaultRegistry()
	appAdapter := os.Getenv("APP_ADAPTER")
	adapter, err := submission.Select(reg, app.Config.Environment, appAdapter)
	if err != nil {
		if submission.IsProduction(app.Config.Environment) || appAdapter != "" {
			log.Fatalf("submission: adapter: %v", err)
		}
		log.Printf("submission: adapter: %v (continuing with no adapter configured)", err)
	}
	_ = adapter // wired for M5-04's worker to consume; unused here is deliberate for this subtask.

	// Build the working River client (demo workers) and register it on the platform kit's
	// lifecycle, so it starts alongside /healthz and drains on shutdown (decision #3).
	q, err := queue.New(pool, queue.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: submission.Workers(pool, app.Logger),
		Logger:  app.Logger,
	})
	if err != nil {
		log.Fatalf("submission: queue: %v", err)
	}
	app.AddBackgroundWorker(q)

	// Stub endpoint — proves routing end to end; replaced by real endpoints in M3.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"submission","status":"ok"}`))
	})

	if err := app.Run(ctx); err != nil {
		log.Fatalf("submission: %v", err)
	}
}
