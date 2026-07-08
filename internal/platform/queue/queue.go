// Package queue is the platform-kit wrapper over River (github.com/riverqueue/river),
// the Postgres-backed job queue that is FiscalBridge's async spine. It gives every
// service one way to build a River client and one way to enqueue: EnqueueTx, the
// transactional-outbox helper that writes a domain change, its idempotency-key row, and
// the River job in a SINGLE transaction so they commit or roll back together.
//
// Because the queue lives in the same Postgres as the domain data, an "enqueue" is just
// an INSERT in the caller's transaction — there is no second broker to dual-write to and
// get out of sync. The worker-role pattern (docs/migrations.md §8): a client connects as
// invoice_app; River's tables are cross-tenant infrastructure (no tenant_id, no RLS), and
// each job handler re-establishes tenant context with db.WithinTenantTx keyed by the job's
// tenant_id. This task builds the machinery + a happy-path smoke test; the adversarial
// exactly-once proof is M2-09.
package queue

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Config configures a River client built by New.
type Config struct {
	// Queues maps queue name -> settings. A non-empty map makes a WORKING client (it
	// fetches and runs jobs); leave it nil for an INSERT-ONLY client (enqueue only, e.g.
	// an API process). When set, Workers is required.
	Queues map[string]river.QueueConfig
	// Workers is the bundle of registered job workers. Required iff Queues is set.
	Workers *river.Workers
	// RetryPolicy overrides River's default exponential backoff (attempt^4 s, ~25 retries
	// over ~3 weeks). A job that exhausts its MaxAttempts under the active policy lands in
	// River's `discarded` state — River's built-in dead-letter (inspected/re-driven in
	// M4-02). nil keeps the default. Mainly a test seam for exercising the discard path fast.
	RetryPolicy river.ClientRetryPolicy
	// Logger is the structured logger River uses. nil falls back to slog's default.
	Logger *slog.Logger
}

// Client wraps a River client with the EnqueueTx outbox helper. It satisfies
// platform.BackgroundWorker (Start/Stop with matching signatures), so a working client
// registers straight onto the platform kit's lifecycle via App.AddBackgroundWorker.
type Client struct {
	river *river.Client[pgx.Tx]
}

// New builds a River client bound to pool, which MUST connect as invoice_app (the
// NOBYPASSRLS runtime role) — never the migrator or superuser (docs/migrations.md §1).
func New(pool *pgxpool.Pool, cfg Config) (*Client, error) {
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:      cfg.Queues,
		Workers:     cfg.Workers,
		RetryPolicy: cfg.RetryPolicy,
		Logger:      cfg.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("queue: new river client: %w", err)
	}
	return &Client{river: rc}, nil
}

// Start launches the worker pool (working clients only). It is non-blocking and, with
// Stop, satisfies platform.BackgroundWorker. An insert-only client never calls Start.
func (c *Client) Start(ctx context.Context) error { return c.river.Start(ctx) }

// Stop drains in-flight jobs, blocking until they finish or ctx expires. Satisfies
// platform.BackgroundWorker.
func (c *Client) Stop(ctx context.Context) error { return c.river.Stop(ctx) }

// River exposes the underlying client for the paths this thin wrapper deliberately does
// not re-abstract — non-transactional Insert, Subscribe, and rivertest in tests.
func (c *Client) River() *river.Client[pgx.Tx] { return c.river }

// EnqueueTx is the transactional-outbox enqueue: within the caller's transaction tx it
// records the business idempotency key and inserts the River job, so the two — together
// with whatever domain change the caller already made on tx — commit or roll back
// atomically. A rolled-back tx therefore leaves NEITHER the key nor the job behind.
//
// tx MUST be a tenant-scoped transaction from db.WithinTenantTx: idempotency_keys is
// written under RLS keyed by app.current_tenant, so tenantID must equal the tx's tenant
// (a mismatch fails the policy's WITH CHECK — a built-in safety net).
//
// Dedupe is authoritative and permanent via idempotency_keys' UNIQUE(tenant_id, key): a
// second EnqueueTx with the same (tenantID, key) inserts no key row and NO job, returning
// skipped=true. River's own UniqueOpts (set on the job args) is the complementary
// in-flight layer. A concurrent duplicate blocks on the unique index until the first tx
// resolves, so a key and its job always share one fate — the exactly-once property M2-09
// attacks adversarially.
func (c *Client) EnqueueTx(ctx context.Context, tx pgx.Tx, tenantID, key string, args river.JobArgs, opts *river.InsertOpts) (skipped bool, err error) {
	// A blank business key is a caller bug: ON CONFLICT DO NOTHING would collapse every
	// empty-key job for the tenant into one. Fail fast (idempotency_keys' CHECK rejects it
	// too — this just gives a clearer error before the write).
	if key == "" {
		return false, fmt.Errorf("queue: idempotency key is required")
	}
	// TODO(M2-09): also reject tenantID != the job args' tenant, so the outbox's atomic
	// tenant can't diverge from the tenant the worker runs the job under. Belongs with the
	// adversarial exactly-once suite (needs a tenant-aware job-args contract).
	ct, err := tx.Exec(ctx,
		`INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		tenantID, key)
	if err != nil {
		return false, fmt.Errorf("queue: record idempotency key: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return true, nil // duplicate business key: neither key nor job inserted
	}
	if _, err := c.river.InsertTx(ctx, tx, args, opts); err != nil {
		return false, fmt.Errorf("queue: insert job: %w", err)
	}
	return false, nil
}
