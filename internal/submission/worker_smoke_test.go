// Env-gated integration smoke test for the M2-08 job spine: the transactional-outbox
// enqueue helper + a real River worker pool against a real Postgres. It proves the outbox's
// HAPPY paths (enqueue commits/rolls back atomically; duplicate business key → one job) and
// that a job that exhausts MaxAttempts → discarded. The ADVERSARIAL exactly-once / re-drive
// proof is M2-09 — deliberately not here.
//
// M5-04-05 deletes the M2-08 DemoArgs/DemoWorker scaffold this file originally exercised
// (worker.go); the outbox-only subtests below now use SubmitArgs instead, since EnqueueTx's
// own mechanics don't care which JobArgs implementation they carry. The one subtest that
// DID need a live, completing worker ("demo job runs to completion") is gone with its
// subject: newSmokeClient builds a bare SubmitWorker/PollWorker pair (Adapter/InvoicePort
// left zero-value -- neither's Work is exercised here, only failWorker's), registered via
// submission.Workers(sw, pw) alongside the test-only failWorker, so there is still no
// worker here that could carry a submission_submit job to completion (that needs a real
// Adapter/InvoicePort, which is cmd/submission's composition root, not this package's own
// tests).
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
	TenantID string `json:"tenant_id"`
	Note     string `json:"note"`
}

func (failArgs) Kind() string { return "submission_demo_fail" }

// Tenant satisfies queue.TenantScoped (EnqueueTx is fail-closed on non-implementers).
func (a failArgs) Tenant() string { return a.TenantID }

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

// newSmokeClient builds a WORKING client registering a bare SubmitWorker/PollWorker pair
// (submission.Workers(sw, pw) -- Adapter/InvoicePort/Limiter left zero-value, since neither
// worker's Work is exercised by this file, only failWorker's) plus the test-only fail
// worker, under the fast retry policy.
func newSmokeClient(t *testing.T, pool *pgxpool.Pool) *queue.Client {
	t.Helper()
	sw := &submission.SubmitWorker{Pool: pool}
	pw := &submission.PollWorker{Pool: pool}
	workers := submission.Workers(sw, pw)
	river.AddWorker(workers, &failWorker{})
	q, err := queue.New(pool, queue.Config{
		Queues:      map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 5}},
		Workers:     workers,
		RetryPolicy: immediateRetry{},
	})
	if err != nil {
		t.Fatalf("build queue client: %v", err)
	}
	sw.Queue, pw.Queue = q, q
	return q
}

// smokeOutboxQueue is a River queue name no client in this test binary ever configures
// (newSmokeClient/newWorkerClient only configure river.QueueDefault) -- without a dedicated
// queue, a later-started working client sharing this package's Postgres (e.g.
// TestQueueSmoke_Worker's) would fetch these stray `default`-queue rows anyway and process
// them with the zero-value SubmitWorker newSmokeClient registers (nil Adapter/InvoicePort),
// which would panic on Work rather than exercising the outbox mechanics this test wants.
const smokeOutboxQueue = "smoke-outbox-only"

// TestQueueSmoke_Outbox exercises the transactional outbox without a running worker: it
// only cares that the enqueue's writes commit or roll back atomically (AC #3) and that a
// duplicate business key yields exactly one job (AC #4). It uses the M2-09 insert-only
// client shape (newInsertClient, failure_modes_test.go) rather than newSmokeClient: River
// validates at InsertTx time that a job's Kind is registered in the client's Workers bundle
// whenever that bundle is non-nil, even for a client that is never Start()ed -- true of
// newSmokeClient's bundle now that it registers SubmitWorker/PollWorker (M5-04-08). An
// insert-only client (Workers left nil entirely) skips that check, matching every other
// pure-EnqueueTx test in this package (failure_modes_test.go's
// rawArgs/guardedArgs/poisonArgs/noTenantArgs, none of which are registered anywhere
// either).
func TestQueueSmoke_Outbox(t *testing.T) {
	pool := requireDB(t)
	defer pool.Close()
	ctx := context.Background()
	q := newInsertClient(t, pool) // insert-only: no Workers bundle, so no kind-registration check

	t.Run("rollback leaves neither key nor job", func(t *testing.T) {
		tenant, key, idemKey := uuid.NewString(), uuid.NewString(), uuid.NewString()
		err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			if _, e := q.EnqueueTx(ctx, tx, tenant, key,
				submission.SubmitArgs{TenantID: tenant, InvoiceID: uuid.NewString(), IdempotencyKey: idemKey},
				&river.InsertOpts{Queue: smokeOutboxQueue}); e != nil {
				return e
			}
			return errRollback // force the whole tx to roll back
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("WithinTenantTx err = %v, want errRollback", err)
		}
		if n := countSubmitJobs(t, pool, idemKey); n != 0 {
			t.Errorf("river_job rows after rollback = %d, want 0", n)
		}
		if n := countKeys(t, pool, tenant, key); n != 0 {
			t.Errorf("idempotency_keys rows after rollback = %d, want 0", n)
		}
	})

	t.Run("commit leaves both key and job", func(t *testing.T) {
		tenant, key, idemKey := uuid.NewString(), uuid.NewString(), uuid.NewString()
		skipped := enqueueSubmit(t, ctx, q, pool, tenant, key, idemKey)
		if skipped {
			t.Fatal("first enqueue reported skipped, want inserted")
		}
		if n := countSubmitJobs(t, pool, idemKey); n != 1 {
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
				submission.SubmitArgs{TenantID: tenant, InvoiceID: uuid.NewString(), IdempotencyKey: uuid.NewString()},
				&river.InsertOpts{Queue: smokeOutboxQueue})
			return e
		})
		if err == nil {
			t.Fatal("EnqueueTx with an empty key should return an error")
		}
	})

	t.Run("duplicate business key produces exactly one job", func(t *testing.T) {
		tenant, key, idemKey := uuid.NewString(), uuid.NewString(), uuid.NewString()
		if skipped := enqueueSubmit(t, ctx, q, pool, tenant, key, idemKey); skipped {
			t.Fatal("first enqueue skipped, want inserted")
		}
		// Same (tenant, key): the authoritative idempotency_keys UNIQUE dedupes it.
		if skipped := enqueueSubmit(t, ctx, q, pool, tenant, key, idemKey); !skipped {
			t.Fatal("second enqueue of the same key was not skipped")
		}
		if n := countSubmitJobs(t, pool, idemKey); n != 1 {
			t.Errorf("river_job rows for duplicate key = %d, want exactly 1", n)
		}
	})
}

// TestQueueSmoke_Worker starts the pool once and proves an always-failing job exhausts
// MaxAttempts into the discarded state (AC #5). It no longer also proves a job "runs to
// completion": that subtest exercised the now-deleted M2-08 DemoWorker, and this file never
// enqueues a real SubmitArgs/PollArgs job onto river.QueueDefault -- newSmokeClient's
// SubmitWorker/PollWorker are registered with nil Adapter/InvoicePort (see its own doc
// comment), so driving one to `completed` needs the real dependencies only cmd/submission's
// composition root builds.
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

	t.Run("exhausted job lands in discarded", func(t *testing.T) {
		tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
		// MaxAttempts=2 so we actually observe a retry (attempt 1 -> immediate retry ->
		// attempt 2) before the discard, not just an instant give-up.
		err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			_, e := q.EnqueueTx(ctx, tx, tenant, key,
				failArgs{TenantID: tenant, Note: note}, &river.InsertOpts{MaxAttempts: 2})
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

// enqueueSubmit runs the transactional-outbox enqueue for a submission_submit job and
// commits. invoiceID is filler (a fresh uuid): these outbox-only subtests exercise
// EnqueueTx's own mechanics, never SubmitWorker.Work, so it need not name a real invoice.
func enqueueSubmit(t *testing.T, ctx context.Context, q *queue.Client, pool *pgxpool.Pool, tenant, key, idemKey string) (skipped bool) {
	t.Helper()
	err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
		s, e := q.EnqueueTx(ctx, tx, tenant, key,
			submission.SubmitArgs{TenantID: tenant, InvoiceID: uuid.NewString(), IdempotencyKey: idemKey},
			&river.InsertOpts{Queue: smokeOutboxQueue})
		skipped = s
		return e
	})
	if err != nil {
		t.Fatalf("enqueue submit: %v", err)
	}
	return skipped
}

// countSubmitJobs counts river_job rows for SubmitArgs jobs whose own args carry idemKey as
// their idempotency_key field. SubmitArgs has no "note" field (unlike DemoArgs, failArgs,
// rawArgs, guardedArgs, poisonArgs below and in failure_modes_test.go, all of which do and
// are read back through the shared countJobs/jobID by that field) -- IdempotencyKey is the
// only test-controlled, per-call-unique string on SubmitArgs, so it doubles as this file's
// tracking value. river_job has no RLS (it is cross-tenant infrastructure), so the app role
// sees every row.
func countSubmitJobs(t *testing.T, pool *pgxpool.Pool, idemKey string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'idempotency_key' = $2`,
		submission.SubmitArgs{}.Kind(), idemKey).Scan(&n); err != nil {
		t.Fatalf("count river_job: %v", err)
	}
	return n
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
