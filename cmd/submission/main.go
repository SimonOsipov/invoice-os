// Command submission is the 05 Submission context service. M2-08 booted the River worker
// pool (the async job spine) alongside the platform kit's /healthz + /readyz and drains
// in-flight jobs within the shutdown window on SIGINT/SIGTERM. The worker connects as the
// app role (invoice_app) and re-establishes tenant context per job — the worker-role
// pattern, docs/migrations.md §8. M5-04 wires the real handlers onto that spine:
// SubmitWorker drives the tx1 / adapter / tx2 submit flow and PollWorker follows a
// deferred verdict the same way (internal/submission/worker.go) — both registered via
// submission.Workers(sw, pw) below.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
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
	// allowlist before the queue starts.
	//
	// M5-03-05: the mock's latency baseline is read from the environment BEFORE the registry is
	// built, and a malformed value is fatal. The fatal is UNCONDITIONAL -- it fires even when
	// APP_ADAPTER is unset and the mock is never selected -- deliberately, matching how PORT
	// behaves: gating it on `appAdapter == "mock"` would defer the failure to the moment someone
	// flips the adapter on a fleet, the worst time to discover a typo.
	mockCfg, err := submission.MockConfigFromEnv()
	if err != nil {
		log.Fatalf("submission: adapter config: %v", err)
	}
	reg := submission.NewDefaultRegistry(mockCfg)
	appAdapter := os.Getenv("APP_ADAPTER")
	// M5-04-08 tightens this: a failed Select is fatal in EVERY environment, not just
	// production -- SubmitWorker/PollWorker below need a real Adapter to do anything, so
	// booting with none configured is no longer a viable "continue anyway" state for any
	// fleet, dev included.
	adapter, err := submission.Select(reg, app.Config.Environment, appAdapter)
	if err != nil {
		log.Fatalf("submission: adapter: %v", err)
	}

	rateLimit, err := submission.RateLimitConfigFromEnv()
	if err != nil {
		log.Fatalf("submission: rate limit config: %v", err)
	}
	limiter := submission.NewRateLimiter()
	invStore := invoice.NewStore(pool)

	// SubmitWorker/PollWorker are built with every field except Queue, which does not exist
	// yet -- queue.New (below) needs their bundle (submission.Workers(sw, pw)) to construct
	// the client in the first place. Queue is backfilled onto these same pointers once
	// queue.New returns, before the client is registered as a background worker.
	sw := &submission.SubmitWorker{
		Pool:        pool,
		Adapter:     adapter,
		InvoicePort: invStore,
		Limiter:     limiter,
		RateLimit:   rateLimit,
		Logger:      app.Logger,
	}
	pw := &submission.PollWorker{
		Pool:        pool,
		Adapter:     adapter,
		InvoicePort: invStore,
		Logger:      app.Logger,
	}

	// Build the working River client and register it on the platform kit's lifecycle, so it
	// starts alongside /healthz and drains on shutdown (decision #3).
	q, err := queue.New(pool, queue.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: submission.Workers(sw, pw),
		Logger:  app.Logger,
	})
	if err != nil {
		log.Fatalf("submission: queue: %v", err)
	}
	sw.Queue, pw.Queue = q, q
	app.AddBackgroundWorker(q)

	// Stub endpoint — proves routing end to end.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"submission","status":"ok"}`))
	})

	if err := app.Run(ctx); err != nil {
		log.Fatalf("submission: %v", err)
	}
}
