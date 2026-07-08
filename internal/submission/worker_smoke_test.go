// Env-gated integration smoke test for the M2-08 job spine: the transactional-outbox
// enqueue helper + a real River worker pool against a real Postgres. It proves the
// HAPPY paths only (enqueue → worker runs → job completes; duplicate business key → one
// job; a job that exhausts MaxAttempts → discarded). The ADVERSARIAL exactly-once /
// re-drive proof is M2-09 — deliberately not here.
//
// Like the M2-07 RLS suite, it reuses the Postgres-service-container + Makefile-bootstrap
// path (not testcontainers): it connects as invoice_app via DATABASE_URL and SKIPS ITSELF
// when that is unset, so a bare `go test ./...` and the default CI `go` job stay green
// without a database. It runs only under the CI `queue` job or `make test-queue`, which
// bootstrap the roles, migrate (creating River's tables + idempotency_keys), and set
// DATABASE_URL to the invoice_app URL. See docs/migrations.md §6, §8.
package submission_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// errRollback is the sentinel a WithinTenantTx callback returns to force a rollback,
// exercising the outbox's atomicity (a rolled-back tx must leave neither key nor job).
var errRollback = errors.New("smoke: intentional rollback")

// requireDB connects as invoice_app or skips. A URL that is set but unreachable / not
// migrated is a real failure (e.g. `make test-queue` without `make dev-db`), not a skip.
func requireDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("queue smoke test skipped: set DATABASE_URL to the invoice_app URL of a " +
			"migrated Postgres (or run `make test-queue`)")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect app pool: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping app DB (is it up, bootstrapped and migrated?): %v", err)
	}
	return pool
}

// failArgs is a test-only job whose worker always errors, so it exhausts MaxAttempts and
// lands in River's `discarded` (dead-letter) state — the DLQ path of AC #5.
type failArgs struct {
	Note string `json:"note"`
}

func (failArgs) Kind() string { return "submission_demo_fail" }

type failWorker struct {
	river.WorkerDefaults[failArgs]
}

func (failWorker) Work(_ context.Context, job *river.Job[failArgs]) error {
	return fmt.Errorf("smoke: always fail (attempt %d/%d)", job.Attempt, job.MaxAttempts)
}

// immediateRetry schedules every retry for now, so a failing job walks attempt→retry→
// discard in seconds instead of River's default exponential backoff (~weeks). It is the
// RetryPolicy knob queue.Config exposes for exactly this.
type immediateRetry struct{}

func (immediateRetry) NextRetry(*rivertype.JobRow) time.Time { return time.Now() }

// newSmokeClient builds a WORKING client with the production demo worker plus the
// test-only fail worker, under the fast retry policy.
func newSmokeClient(t *testing.T, pool *pgxpool.Pool) *queue.Client {
	t.Helper()
	workers := submission.Workers(pool, nil)
	river.AddWorker(workers, &failWorker{})
	q, err := queue.New(pool, queue.Config{
		Queues:      map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 5}},
		Workers:     workers,
		RetryPolicy: immediateRetry{},
	})
	if err != nil {
		t.Fatalf("build queue client: %v", err)
	}
	return q
}

// TestQueueSmoke_Outbox exercises the transactional outbox without a running worker: it
// only cares that the enqueue's writes commit or roll back atomically (AC #3) and that a
// duplicate business key yields exactly one job (AC #4).
func TestQueueSmoke_Outbox(t *testing.T) {
	pool := requireDB(t)
	defer pool.Close()
	ctx := context.Background()
	q := newSmokeClient(t, pool) // never Start()ed: EnqueueTx is an insert-only path

	t.Run("rollback leaves neither key nor job", func(t *testing.T) {
		tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
		err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			if _, e := q.EnqueueTx(ctx, tx, tenant, key,
				submission.DemoArgs{TenantID: tenant, Note: note}, nil); e != nil {
				return e
			}
			return errRollback // force the whole tx to roll back
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("WithinTenantTx err = %v, want errRollback", err)
		}
		if n := countJobs(t, pool, submission.DemoArgs{}.Kind(), note); n != 0 {
			t.Errorf("river_job rows after rollback = %d, want 0", n)
		}
		if n := countKeys(t, pool, tenant, key); n != 0 {
			t.Errorf("idempotency_keys rows after rollback = %d, want 0", n)
		}
	})

	t.Run("commit leaves both key and job", func(t *testing.T) {
		tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
		skipped := enqueueDemo(t, ctx, q, pool, tenant, key, note)
		if skipped {
			t.Fatal("first enqueue reported skipped, want inserted")
		}
		if n := countJobs(t, pool, submission.DemoArgs{}.Kind(), note); n != 1 {
			t.Errorf("river_job rows after commit = %d, want 1", n)
		}
		if n := countKeys(t, pool, tenant, key); n != 1 {
			t.Errorf("idempotency_keys rows after commit = %d, want 1", n)
		}
	})

	t.Run("empty business key is rejected", func(t *testing.T) {
		tenant := uuid.NewString()
		err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			_, e := q.EnqueueTx(ctx, tx, tenant, "",
				submission.DemoArgs{TenantID: tenant, Note: uuid.NewString()}, nil)
			return e
		})
		if err == nil {
			t.Fatal("EnqueueTx with an empty key should return an error")
		}
	})

	t.Run("duplicate business key produces exactly one job", func(t *testing.T) {
		tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
		if skipped := enqueueDemo(t, ctx, q, pool, tenant, key, note); skipped {
			t.Fatal("first enqueue skipped, want inserted")
		}
		// Same (tenant, key): the authoritative idempotency_keys UNIQUE dedupes it.
		if skipped := enqueueDemo(t, ctx, q, pool, tenant, key, note); !skipped {
			t.Fatal("second enqueue of the same key was not skipped")
		}
		if n := countJobs(t, pool, submission.DemoArgs{}.Kind(), note); n != 1 {
			t.Errorf("river_job rows for duplicate key = %d, want exactly 1", n)
		}
	})
}

// TestQueueSmoke_Worker starts the pool once and proves a demo job runs to completion
// (AC #8 / the worker-role WithinTenantTx path, AC #7) and that an always-failing job
// exhausts MaxAttempts into the discarded state (AC #5).
func TestQueueSmoke_Worker(t *testing.T) {
	pool := requireDB(t)
	defer pool.Close()
	ctx := context.Background()
	q := newSmokeClient(t, pool)

	if err := q.Start(ctx); err != nil {
		t.Fatalf("start worker pool: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := q.Stop(stopCtx); err != nil {
			t.Errorf("stop worker pool: %v", err)
		}
	}()

	t.Run("demo job runs to completion", func(t *testing.T) {
		tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
		enqueueDemo(t, ctx, q, pool, tenant, key, note)
		id := jobID(t, pool, submission.DemoArgs{}.Kind(), note)
		waitForJobState(t, pool, id, string(rivertype.JobStateCompleted), 20*time.Second)
	})

	t.Run("exhausted job lands in discarded", func(t *testing.T) {
		tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
		// MaxAttempts=2 so we actually observe a retry (attempt 1 -> immediate retry ->
		// attempt 2) before the discard, not just an instant give-up.
		err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			_, e := q.EnqueueTx(ctx, tx, tenant, key,
				failArgs{Note: note}, &river.InsertOpts{MaxAttempts: 2})
			return e
		})
		if err != nil {
			t.Fatalf("enqueue fail job: %v", err)
		}
		id := jobID(t, pool, failArgs{}.Kind(), note)
		waitForJobState(t, pool, id, string(rivertype.JobStateDiscarded), 30*time.Second)
	})
}

// --- helpers -------------------------------------------------------------------------

// enqueueDemo runs the transactional-outbox enqueue for a demo job and commits.
func enqueueDemo(t *testing.T, ctx context.Context, q *queue.Client, pool *pgxpool.Pool, tenant, key, note string) (skipped bool) {
	t.Helper()
	err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
		s, e := q.EnqueueTx(ctx, tx, tenant, key,
			submission.DemoArgs{TenantID: tenant, Note: note}, nil)
		skipped = s
		return e
	})
	if err != nil {
		t.Fatalf("enqueue demo: %v", err)
	}
	return skipped
}

// countJobs counts river_job rows for a kind + note. river_job has no RLS (it is
// cross-tenant infrastructure), so the app role sees every row.
func countJobs(t *testing.T, pool *pgxpool.Pool, kind, note string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'note' = $2`,
		kind, note).Scan(&n); err != nil {
		t.Fatalf("count river_job: %v", err)
	}
	return n
}

// countKeys counts idempotency_keys rows for a tenant + key. That table IS tenant data
// (FORCE RLS), so the count must run inside the tenant's context to see its own rows.
func countKeys(t *testing.T, pool *pgxpool.Pool, tenant, key string) int {
	t.Helper()
	var n int
	err := db.WithinTenantTx(context.Background(), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count idempotency_keys: %v", err)
	}
	return n
}

// jobID returns the single river_job id for a kind + note (fails if not exactly one).
func jobID(t *testing.T, pool *pgxpool.Pool, kind, note string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM river_job WHERE kind = $1 AND args->>'note' = $2`,
		kind, note).Scan(&id); err != nil {
		t.Fatalf("look up river_job id (kind=%s note=%s): %v", kind, note, err)
	}
	return id
}

// waitForJobState polls a job's state until it reaches want or timeout elapses.
func waitForJobState(t *testing.T, pool *pgxpool.Pool, id int64, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var state string
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(context.Background(),
			`SELECT state FROM river_job WHERE id = $1`, id).Scan(&state); err != nil {
			t.Fatalf("read job %d state: %v", id, err)
		}
		if state == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %d state = %q after %s, want %q", id, state, timeout, want)
}
