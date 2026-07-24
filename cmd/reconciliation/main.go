// Command reconciliation is the M5-06 "who watches the watcher" sweep service. It boots
// the platform kit's /healthz + /readyz, then registers a Sweeper background worker that
// ticks Reconciler.SweepOnce on RECONCILE_INTERVAL — comparing invoices.status against
// submission_jobs.state per tenant, re-arming lost polls through the existing PollWorker
// path, and flagging every other drift as an append-only reconciliation.* audit record
// (internal/reconciliation).
package main

import (
	"context"
	"log"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/reconciliation"
)

func main() {
	app, err := platform.New("reconciliation")
	if err != nil {
		log.Fatalf("reconciliation: startup: %v", err)
	}

	ctx := context.Background()

	// Connect as the app role (invoice_app, NOBYPASSRLS) — never the migrator or
	// superuser (docs/migrations.md §1). This pool runs ReArmPoll's enqueue and every
	// audit write, each inside its own db.WithinTenantTx(AppPool, tenantID, ...).
	appDSN := os.Getenv("DATABASE_URL")
	if appDSN == "" {
		// pgx would otherwise build a config from ambient libpq env/defaults for an empty
		// DSN — fail fast so this service can only ever connect as its configured app role.
		log.Fatal("reconciliation: DATABASE_URL is required")
	}
	appPool, err := db.NewPool(ctx, appDSN)
	if err != nil {
		log.Fatalf("reconciliation: app db pool: %v", err)
	}
	defer appPool.Close()

	// Connect as the reader role (invoice_tenant_reader) — the ONLY role the
	// tenant_enumerate policy ORs in every row for (migrations/20260707122459_tenants_rls.sql:40).
	// Required, not optional: SweepOnce cannot enumerate tenants without it, so an
	// unconfigured reader DSN is a boot-time failure, not a degraded sweep.
	readerDSN := os.Getenv("DATABASE_READER_URL")
	if readerDSN == "" {
		log.Fatal("reconciliation: DATABASE_READER_URL is required")
	}
	readerPool, err := db.NewPool(ctx, readerDSN)
	if err != nil {
		log.Fatalf("reconciliation: reader db pool: %v", err)
	}
	defer readerPool.Close()

	// /readyz reflects BOTH DB dependencies the sweep carries: the app pool (every
	// ReArmPoll enqueue + audit write) and the reader pool (tenant enumeration). A
	// reader-role outage must surface as not-ready, not a silently-failing sweep.
	app.Ready("database", appPool.Ping)
	app.Ready("reader-database", readerPool.Ping)

	// Insert-only queue client: this service only ever enqueues submission_poll jobs via
	// ReArmPoll's transactional outbox, it never fetches or runs them, so it must NOT be
	// registered on the platform kit's background-worker lifecycle (queue.Client.Start is
	// for working clients only -- internal/platform/queue/queue.go's own doc comment).
	q, err := queue.New(appPool, queue.Config{})
	if err != nil {
		log.Fatalf("reconciliation: queue: %v", err)
	}

	// Malformed RECONCILE_* is a fatal boot error, mirroring submission.MockConfigFromEnv /
	// RateLimitConfigFromEnv's env-edge pattern.
	cfg, err := reconciliation.ConfigFromEnv()
	if err != nil {
		log.Fatalf("reconciliation: config: %v", err)
	}

	rec := &reconciliation.Reconciler{
		ReaderPool: readerPool,
		AppPool:    appPool,
		Queue:      q,
		Cfg:        cfg,
		Logger:     app.Logger,
	}

	// The Sweeper drives rec.SweepOnce on cfg.Interval, single-flight, and drains within
	// the platform shutdown window on SIGINT/SIGTERM (internal/reconciliation/sweeper.go).
	app.AddBackgroundWorker(reconciliation.NewSweeper(cfg.Interval, rec.SweepOnce))

	if err := app.Run(ctx); err != nil {
		log.Fatalf("reconciliation: %v", err)
	}
}
