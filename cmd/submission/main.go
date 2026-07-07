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
	pool, err := db.NewPool(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("submission: db pool: %v", err)
	}
	defer pool.Close()

	// /readyz now reflects the DB dependency the worker carries.
	app.Ready("database", pool.Ping)

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
