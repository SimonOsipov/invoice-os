// Package submission is the 05 Submission context: the async worker that will submit
// invoices to the FIRS/APP. M2-08 lands only the job-processing scaffold — a demo job that
// exercises the worker-role pattern end to end (a River pool + a per-job WithinTenantTx).
// Real submission handlers arrive in M3.
package submission

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// DemoArgs is the M2-08 demo job. Every ASComply job carries its tenant_id so the
// handler can re-establish tenant context from the payload (the worker-role pattern,
// docs/migrations.md §8) — River's queue tables have none. Real job args (e.g. an invoice
// id to submit) arrive in M3.
type DemoArgs struct {
	TenantID string `json:"tenant_id"`
	Note     string `json:"note"`
}

// Kind is River's stable job-type key, persisted on every row — never rename it.
func (DemoArgs) Kind() string { return "submission_demo" }

// Tenant satisfies queue.TenantScoped: the tenant this job runs its work under. EnqueueTx
// requires it and fails closed if it diverges from the outbox tenant (docs/migrations.md §8).
func (a DemoArgs) Tenant() string { return a.TenantID }

// InsertOpts gives every demo job River-layer uniqueness by args: the in-flight complement
// to the authoritative idempotency_keys dedupe (the two-layer idempotency of decision #2).
func (DemoArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	}
}

// DemoWorker runs DemoArgs jobs. It holds the app-role pool so its handler can open a
// tenant-scoped transaction — the worker connects as invoice_app and is subject to RLS
// exactly like the HTTP path (docs/migrations.md §8).
type DemoWorker struct {
	river.WorkerDefaults[DemoArgs]
	Pool   *pgxpool.Pool
	Logger *slog.Logger
}

// Work re-establishes the job's tenant context and runs its (here, trivial) tenant-scoped
// unit of work inside db.WithinTenantTx — the ONE sanctioned way to touch tenant data, so
// RLS isolates this job to job.Args.TenantID. River's own bookkeeping (fetch/complete)
// runs OUTSIDE this transaction, against its non-RLS infrastructure tables.
func (w *DemoWorker) Work(ctx context.Context, job *river.Job[DemoArgs]) error {
	return db.WithinTenantTx(ctx, w.Pool, job.Args.TenantID, func(tx pgx.Tx) error {
		// A real handler does the tenant's submission work here, on tx (every query
		// scoped to job.Args.TenantID by RLS). The demo just proves the context is
		// established and the job runs end to end as the app role.
		if w.Logger != nil {
			w.Logger.InfoContext(ctx, "demo job worked",
				slog.String("tenant_id", job.Args.TenantID),
				slog.Int64("job_id", job.ID))
		}
		return nil
	})
}

// Workers builds the River worker bundle for the submission service, wiring pool (and an
// optional logger) into each handler. cmd/submission hands the bundle to queue.New.
func Workers(pool *pgxpool.Pool, logger *slog.Logger) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, &DemoWorker{Pool: pool, Logger: logger})
	return workers
}
