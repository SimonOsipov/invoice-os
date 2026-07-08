// The M2-09 adversarial exactly-once suite for the River outbox/worker spine. Where the
// M2-08 smoke test (worker_smoke_test.go) proves the happy paths, this file attacks the
// failure modes that break naive queues — transaction rollback, concurrent workers, poison
// jobs, and operator re-drive — against a real Postgres.
//
// HONEST FRAMING: River is at-least-once, NOT exactly-once. "Exactly once" in FiscalBridge
// is a composite of three legs, and this suite proves each:
//  1. Enqueue dedupe   — idempotency_keys UNIQUE(tenant_id, key) + ON CONFLICT in EnqueueTx
//     (a duplicate business key inserts neither key nor job).
//  2. Single delivery  — River's FOR UPDATE SKIP LOCKED + atomic available→running
//     (two clients on one queue each get a distinct job).
//  3. Idempotent handler — queue.OncePerJob: a handler that commits its effect then crashes
//     before River acks it re-applies the effect ZERO extra times on retry.
// Leg 3 is the one M2-08 lacked; without it a committed-then-crashed worker double-applies
// (rawWorker below demonstrates the hazard; idempotentWorker proves the fix closes it).
//
// Like the M2-07 RLS suite and the M2-08 smoke test, it reuses the Postgres-service-
// container + Makefile-bootstrap path (not testcontainers). It needs BOTH DATABASE_URL
// (invoice_app, to run jobs) and DATABASE_MIGRATION_URL (invoice_migrator, to create the
// test-only effects table), and SKIPS ITSELF when either is unset — so a bare
// `go test ./...` and the default CI `go` job stay green. It runs under the CI `queue` job
// or `make test-queue`. See docs/migrations.md §6, §8.
package submission_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// --- shared fixture (TestMain) -------------------------------------------------------

// effectsFixture holds the app pool (runs jobs as invoice_app) and the migrator pool
// (owns the test-only effects table). nil when the suite is not configured, so every case
// self-skips via requireEffects.
type effectsFixture struct {
	app *pgxpool.Pool // invoice_app: the NOBYPASSRLS runtime role the workers run as
	mig *pgxpool.Pool // invoice_migrator: owns/creates test_job_effects (the runtime-table idiom)
}

// fx is the shared fixture, nil when the suite is not configured.
var fx *effectsFixture

func TestMain(m *testing.M) { os.Exit(runEffects(m)) }

func runEffects(m *testing.M) int {
	ctx := context.Background()
	appURL := os.Getenv("DATABASE_URL")
	migURL := os.Getenv("DATABASE_MIGRATION_URL")
	if appURL == "" || migURL == "" {
		// Not configured: still run so every case self-skips (requireEffects). The M2-08
		// smoke cases in this package gate on DATABASE_URL alone and are unaffected.
		return m.Run()
	}

	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "effects suite: connect app pool:", err)
		return 1
	}
	mig, err := pgxpool.New(ctx, migURL)
	if err != nil {
		app.Close()
		fmt.Fprintln(os.Stderr, "effects suite: connect migrator pool:", err)
		return 1
	}
	// A URL that is set but unreachable / not migrated is a real failure (e.g.
	// `make test-queue` without `make dev-db`), not a skip.
	if err := app.Ping(ctx); err != nil {
		app.Close()
		mig.Close()
		fmt.Fprintln(os.Stderr, "effects suite: ping app DB (is it up, bootstrapped and migrated?):", err)
		return 1
	}
	if err := createEffectsTable(ctx, mig); err != nil {
		app.Close()
		mig.Close()
		fmt.Fprintln(os.Stderr, "effects suite:", err)
		return 1
	}

	fx = &effectsFixture{app: app, mig: mig}
	code := m.Run()

	_, _ = mig.Exec(ctx, `DROP TABLE IF EXISTS test_job_effects`)
	app.Close()
	mig.Close()
	return code
}

// createEffectsTable builds the test-only side-effect table as the migrator/owner. It is
// tenant-scoped FORCE-RLS like the M2-06 `tenants` template and the M2-07 rls_fixture — so
// the workers write it under the same RLS the real submission path will — and it is created
// at runtime, never a committed migration (M2-09 ships no schema change). CRUCIALLY it has
// NO UNIQUE constraint on job_id: a double-apply must be INSERTABLE and visible, or the
// at-least-once hazard (rawWorker) would be silently swallowed instead of observed.
func createEffectsTable(ctx context.Context, mig *pgxpool.Pool) error {
	stmts := []string{
		`DROP TABLE IF EXISTS test_job_effects`,
		`CREATE TABLE test_job_effects (
			id        uuid   PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id uuid   NOT NULL,
			job_id    bigint NOT NULL,
			note      text   NOT NULL
		)`,
		`ALTER TABLE test_job_effects ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE test_job_effects FORCE  ROW LEVEL SECURITY`,
		`CREATE POLICY tenant_isolation ON test_job_effects
			USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid)`,
		`GRANT SELECT, INSERT ON test_job_effects TO invoice_app`,
	}
	for _, s := range stmts {
		if _, err := mig.Exec(ctx, s); err != nil {
			return fmt.Errorf("create test_job_effects: %w", err)
		}
	}
	return nil
}

// requireEffects skips the calling test when the suite is not configured.
func requireEffects(t *testing.T) *effectsFixture {
	t.Helper()
	if fx == nil {
		t.Skip("exactly-once suite skipped: set DATABASE_URL and DATABASE_MIGRATION_URL " +
			"(or run `make test-queue`)")
	}
	return fx
}

// --- test job args + workers ---------------------------------------------------------

// rawArgs → rawWorker (NO idempotency guard). Records one effect per execution and,
// when CrashOnAttempt1 is set, returns an error AFTER committing the effect on attempt 1
// to simulate a worker that crashes before River acks it.
type rawArgs struct {
	TenantID        string `json:"tenant_id"`
	Note            string `json:"note"`
	CrashOnAttempt1 bool   `json:"crash_on_attempt_1"`
}

func (rawArgs) Kind() string     { return "test_effect_raw" }
func (a rawArgs) Tenant() string { return a.TenantID }

// guardedArgs → idempotentWorker (effect wrapped in queue.OncePerJob). Same shape as
// rawArgs; the only difference is the worker.
type guardedArgs struct {
	TenantID        string `json:"tenant_id"`
	Note            string `json:"note"`
	CrashOnAttempt1 bool   `json:"crash_on_attempt_1"`
}

func (guardedArgs) Kind() string     { return "test_effect_guarded" }
func (a guardedArgs) Tenant() string { return a.TenantID }

// poisonArgs → poisonWorker (always fails, records nothing). Drives the discard/DLQ paths.
type poisonArgs struct {
	TenantID string `json:"tenant_id"`
	Note     string `json:"note"`
}

func (poisonArgs) Kind() string     { return "test_effect_poison" }
func (a poisonArgs) Tenant() string { return a.TenantID }

// noTenantArgs deliberately does NOT implement queue.TenantScoped — it exists only to prove
// EnqueueTx fails closed on a non-implementer (AC #10).
type noTenantArgs struct {
	Note string `json:"note"`
}

func (noTenantArgs) Kind() string { return "test_no_tenant" }

// rawWorker appends one effect per execution with no guard: on a committed-then-crashed
// retry it double-applies. That is the hazard leg 3 exists to close.
type rawWorker struct {
	river.WorkerDefaults[rawArgs]
	pool *pgxpool.Pool
}

func (w *rawWorker) Work(ctx context.Context, job *river.Job[rawArgs]) error {
	if err := recordEffect(ctx, w.pool, job.Args.TenantID, job.ID, job.Args.Note); err != nil {
		return err
	}
	if job.Args.CrashOnAttempt1 && job.Attempt == 1 {
		return fmt.Errorf("rawWorker: simulated crash after commit, before ack (attempt %d)", job.Attempt)
	}
	return nil
}

// idempotentWorker wraps the SAME effect in queue.OncePerJob, inside one WithinTenantTx so
// the marker and the effect commit together. On a committed-then-crashed retry the marker is
// already present, so OncePerJob skips the effect and the job is acked without re-applying.
type idempotentWorker struct {
	river.WorkerDefaults[guardedArgs]
	pool *pgxpool.Pool
}

func (w *idempotentWorker) Work(ctx context.Context, job *river.Job[guardedArgs]) error {
	err := db.WithinTenantTx(ctx, w.pool, job.Args.TenantID, func(tx pgx.Tx) error {
		_, e := queue.OncePerJob(ctx, tx, job.Args.TenantID, job.ID, func() error {
			_, ie := tx.Exec(ctx,
				`INSERT INTO test_job_effects (tenant_id, job_id, note) VALUES ($1, $2, $3)`,
				job.Args.TenantID, job.ID, job.Args.Note)
			return ie
		})
		return e
	})
	if err != nil {
		return err // tx rolled back: marker + effect both gone, so a retry re-applies
	}
	// tx committed (marker + effect durable). Simulate crash-before-ack on attempt 1 so
	// River retries even though the effect already committed — the exact exactly-once window.
	if job.Args.CrashOnAttempt1 && job.Attempt == 1 {
		return fmt.Errorf("idempotentWorker: simulated crash after commit, before ack (attempt %d)", job.Attempt)
	}
	return nil
}

// poisonWorker always fails and never records an effect, so it walks attempt→retry→discard.
type poisonWorker struct {
	river.WorkerDefaults[poisonArgs]
}

func (poisonWorker) Work(_ context.Context, job *river.Job[poisonArgs]) error {
	return fmt.Errorf("poisonWorker: always fails (attempt %d/%d)", job.Attempt, job.MaxAttempts)
}

// --- acceptance cases ----------------------------------------------------------------

// AC #1: a rolled-back enqueue transaction leaves NONE of the three writes behind — the
// business-state row, the idempotency_keys row, and the river_job row all vanish together.
func TestExactlyOnce_OutboxThreeWayAtomicity(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	enq := newInsertClient(t, f.app)

	tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
	err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		// (1) a business-state write on the SAME tx (job_id 0 = a domain row, not a job effect)
		if _, e := tx.Exec(ctx,
			`INSERT INTO test_job_effects (tenant_id, job_id, note) VALUES ($1, 0, $2)`,
			tenant, note); e != nil {
			return e
		}
		// (2) the outbox enqueue: idempotency key + river_job, same tx
		if _, e := enq.EnqueueTx(ctx, tx, tenant, key, rawArgs{TenantID: tenant, Note: note}, nil); e != nil {
			return e
		}
		// (3) force the whole tx to roll back
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("WithinTenantTx err = %v, want errRollback", err)
	}

	if n := countEffects(t, f.app, tenant); n != 0 {
		t.Errorf("business-state rows after rollback = %d, want 0", n)
	}
	if n := countKeys(t, f.app, tenant, key); n != 0 {
		t.Errorf("idempotency_keys rows after rollback = %d, want 0", n)
	}
	if n := countJobs(t, f.app, rawArgs{}.Kind(), note); n != 0 {
		t.Errorf("river_job rows after rollback = %d, want 0", n)
	}
}

// AC #2: two concurrent EnqueueTx with the same (tenant, key) → exactly one river_job and
// one idempotency_keys row, and exactly one caller reports skipped=true (the loser).
func TestExactlyOnce_ConcurrentEnqueueSameKey(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	enq := newInsertClient(t, f.app)

	tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
	var wg sync.WaitGroup
	start := make(chan struct{})
	skipped := make([]bool, 2)
	errs := make([]error, 2)
	for i := range skipped {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release both goroutines together to maximise contention
			errs[i] = db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
				s, e := enq.EnqueueTx(ctx, tx, tenant, key,
					rawArgs{TenantID: tenant, Note: note}, nil)
				skipped[i] = s
				return e
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d EnqueueTx: %v", i, e)
		}
	}
	skips := 0
	for _, s := range skipped {
		if s {
			skips++
		}
	}
	if skips != 1 {
		t.Errorf("skipped=true count = %d, want exactly 1 (one winner, one loser)", skips)
	}
	if n := countJobs(t, f.app, rawArgs{}.Kind(), note); n != 1 {
		t.Errorf("river_job rows for concurrent same-key = %d, want exactly 1", n)
	}
	if n := countKeys(t, f.app, tenant, key); n != 1 {
		t.Errorf("idempotency_keys rows for concurrent same-key = %d, want exactly 1", n)
	}
}

// AC #3: two worker clients on one queue deliver each of N distinct jobs exactly once —
// River's SKIP LOCKED single-delivery. A double delivery would show as a job with 2 effects.
func TestExactlyOnce_ConcurrentWorkersSingleDelivery(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	const n = 20
	tenant := uuid.NewString()

	enq := newInsertClient(t, f.app)
	for i := 0; i < n; i++ {
		key, note := uuid.NewString(), uuid.NewString()
		if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
			_, e := enq.EnqueueTx(ctx, tx, tenant, key,
				rawArgs{TenantID: tenant, Note: note}, nil)
			return e
		}); err != nil {
			t.Fatalf("enqueue job %d: %v", i, err)
		}
	}

	// Two independent clients competing on the same default queue.
	c1 := newWorkerClient(t, f.app, func(w *river.Workers) { river.AddWorker(w, &rawWorker{pool: f.app}) })
	c2 := newWorkerClient(t, f.app, func(w *river.Workers) { river.AddWorker(w, &rawWorker{pool: f.app}) })
	startClient(t, ctx, c1)
	startClient(t, ctx, c2)

	waitForCompletedCount(t, f.app, rawArgs{}.Kind(), tenant, n, 30*time.Second)

	if got := countEffects(t, f.app, tenant); got != n {
		t.Errorf("total effects = %d, want %d (double delivery?)", got, n)
	}
	if maxc := maxEffectsPerJob(t, f.app, tenant); maxc > 1 {
		t.Errorf("a job applied its effect %d times, want 1 (SKIP LOCKED single-delivery broke)", maxc)
	}
}

// AC #4: the at-least-once hazard, documented. A raw handler that commits then crashes
// before ack applies the effect TWICE across the retry.
func TestExactlyOnce_RawHandlerAppliesTwice(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	c := newWorkerClient(t, f.app, func(w *river.Workers) { river.AddWorker(w, &rawWorker{pool: f.app}) })
	startClient(t, ctx, c)

	tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
	enqueueEffect(t, ctx, c, f.app, tenant, key,
		rawArgs{TenantID: tenant, Note: note, CrashOnAttempt1: true}, &river.InsertOpts{MaxAttempts: 3})

	id := jobID(t, f.app, rawArgs{}.Kind(), note)
	waitForJobState(t, f.app, id, string(rivertype.JobStateCompleted), 30*time.Second)

	if got := countEffects(t, f.app, tenant); got != 2 {
		t.Errorf("raw handler effect count = %d, want 2 (the at-least-once hazard)", got)
	}
}

// AC #5: the guard closes the window. An OncePerJob handler that commits, crashes, and is
// retried applies its effect EXACTLY ONCE — the retry is a no-op.
func TestExactlyOnce_IdempotentGuardAppliesOnce(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	c := newWorkerClient(t, f.app, func(w *river.Workers) { river.AddWorker(w, &idempotentWorker{pool: f.app}) })
	startClient(t, ctx, c)

	tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
	enqueueEffect(t, ctx, c, f.app, tenant, key,
		guardedArgs{TenantID: tenant, Note: note, CrashOnAttempt1: true}, &river.InsertOpts{MaxAttempts: 3})

	id := jobID(t, f.app, guardedArgs{}.Kind(), note)
	waitForJobState(t, f.app, id, string(rivertype.JobStateCompleted), 30*time.Second)

	if got := countEffects(t, f.app, tenant); got != 1 {
		t.Errorf("guarded handler effect count = %d, want 1 (retry must be a no-op)", got)
	}
	if got := countKeys(t, f.app, tenant, fmt.Sprintf("job:%d", id)); got != 1 {
		t.Errorf("OncePerJob marker count = %d, want 1", got)
	}
}

// AC #6 + #7: a poison job that always fails lands in `discarded` at max_attempts (attempts
// and recorded errors both == max_attempts, finalized_at set, no side effect) and STAYS
// discarded — under immediateRetry a self-revival would be instant, so it never happens.
func TestExactlyOnce_PoisonJobDiscardsAndStays(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	enq := newInsertClient(t, f.app)
	work := newWorkerClient(t, f.app, func(w *river.Workers) { river.AddWorker(w, &poisonWorker{}) })
	startClient(t, ctx, work)

	const maxAttempts = 3
	tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		_, e := enq.EnqueueTx(ctx, tx, tenant, key,
			poisonArgs{TenantID: tenant, Note: note}, &river.InsertOpts{MaxAttempts: maxAttempts})
		return e
	}); err != nil {
		t.Fatalf("enqueue poison job: %v", err)
	}
	id := jobID(t, f.app, poisonArgs{}.Kind(), note)
	waitForJobState(t, f.app, id, string(rivertype.JobStateDiscarded), 30*time.Second)

	// AC #6 — the full discard shape.
	var state string
	var attempt, nErrors int
	var finalized bool
	if err := f.app.QueryRow(ctx,
		`SELECT state, attempt, coalesce(array_length(errors, 1), 0), finalized_at IS NOT NULL
		 FROM river_job WHERE id = $1`, id).
		Scan(&state, &attempt, &nErrors, &finalized); err != nil {
		t.Fatalf("read discarded job: %v", err)
	}
	if state != string(rivertype.JobStateDiscarded) {
		t.Errorf("state = %q, want discarded", state)
	}
	if attempt != maxAttempts {
		t.Errorf("attempt = %d, want %d (max_attempts)", attempt, maxAttempts)
	}
	if nErrors != maxAttempts {
		t.Errorf("recorded errors = %d, want %d (one per attempt)", nErrors, maxAttempts)
	}
	if !finalized {
		t.Error("finalized_at is null, want set")
	}
	if got := countEffects(t, f.app, tenant); got != 0 {
		t.Errorf("poison applied a side effect %d times, want 0", got)
	}

	// AC #7 — stays discarded (no self-revival) across a settle window.
	requireStableState(t, f.app, id, string(rivertype.JobStateDiscarded), time.Second)
	if got := countEffects(t, f.app, tenant); got != 0 {
		t.Errorf("poison applied a side effect after settle = %d, want 0", got)
	}
}

// AC #8: re-driving a COMPLETED idempotent job (River JobRetry) runs the handler again —
// the attempt count climbs — but OncePerJob applies the effect no additional times.
func TestExactlyOnce_RedriveCompletedIdempotent(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	c := newWorkerClient(t, f.app, func(w *river.Workers) { river.AddWorker(w, &idempotentWorker{pool: f.app}) })
	startClient(t, ctx, c)

	tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
	enqueueEffect(t, ctx, c, f.app, tenant, key,
		guardedArgs{TenantID: tenant, Note: note}, nil) // succeeds on attempt 1
	id := jobID(t, f.app, guardedArgs{}.Kind(), note)
	waitForJobState(t, f.app, id, string(rivertype.JobStateCompleted), 20*time.Second)
	if got := countEffects(t, f.app, tenant); got != 1 {
		t.Fatalf("effect count before re-drive = %d, want 1", got)
	}
	before := jobAttempt(t, f.app, id)

	// Operator re-drive: JobRetry makes the completed job available to run again.
	if _, err := c.River().JobRetry(ctx, id); err != nil {
		t.Fatalf("JobRetry: %v", err)
	}
	waitForJob(t, f.app, id, 20*time.Second,
		func(state string, attempt int) bool {
			return state == string(rivertype.JobStateCompleted) && attempt > before
		}, "re-completed at a higher attempt")

	if got := countEffects(t, f.app, tenant); got != 1 {
		t.Errorf("effect count after re-drive = %d, want 1 (re-run must apply nothing new)", got)
	}
}

// AC #9: re-driving a DISCARDED poison job re-discards it (JobRetry bumps max_attempts by
// one on an exhausted job, so it runs once more and discards again) and applies no effect.
func TestExactlyOnce_RedriveDiscardedPoison(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	enq := newInsertClient(t, f.app)
	work := newWorkerClient(t, f.app, func(w *river.Workers) { river.AddWorker(w, &poisonWorker{}) })
	startClient(t, ctx, work)

	const maxAttempts = 2
	tenant, key, note := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		_, e := enq.EnqueueTx(ctx, tx, tenant, key,
			poisonArgs{TenantID: tenant, Note: note}, &river.InsertOpts{MaxAttempts: maxAttempts})
		return e
	}); err != nil {
		t.Fatalf("enqueue poison job: %v", err)
	}
	id := jobID(t, f.app, poisonArgs{}.Kind(), note)
	waitForJobState(t, f.app, id, string(rivertype.JobStateDiscarded), 30*time.Second)

	if _, err := work.River().JobRetry(ctx, id); err != nil {
		t.Fatalf("JobRetry: %v", err)
	}
	// Exhausted job: max_attempts → maxAttempts+1, so it runs once more and re-discards at
	// attempt maxAttempts+1.
	waitForJob(t, f.app, id, 30*time.Second,
		func(state string, attempt int) bool {
			return state == string(rivertype.JobStateDiscarded) && attempt == maxAttempts+1
		}, "re-discarded at attempt maxAttempts+1")

	if got := countEffects(t, f.app, tenant); got != 0 {
		t.Errorf("re-driven poison applied a side effect %d times, want 0", got)
	}
}

// AC #10: EnqueueTx fails closed on tenant divergence — a mismatched declared tenant AND a
// non-implementer of TenantScoped are both rejected, each writing neither key nor job.
func TestExactlyOnce_EnqueueRejectsTenantMismatch(t *testing.T) {
	f := requireEffects(t)
	ctx := context.Background()
	enq := newInsertClient(t, f.app)

	// (a) args declare tenant B while the outbox is scoped to tenant A.
	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	key, note := uuid.NewString(), uuid.NewString()
	err := db.WithinTenantTx(ctx, f.app, tenantA, func(tx pgx.Tx) error {
		_, e := enq.EnqueueTx(ctx, tx, tenantA, key, rawArgs{TenantID: tenantB, Note: note}, nil)
		return e
	})
	if err == nil {
		t.Error("EnqueueTx accepted a tenant-mismatched job, want rejection")
	}
	if n := countKeys(t, f.app, tenantA, key); n != 0 {
		t.Errorf("idempotency_keys rows after mismatch = %d, want 0", n)
	}
	if n := countJobs(t, f.app, rawArgs{}.Kind(), note); n != 0 {
		t.Errorf("river_job rows after mismatch = %d, want 0", n)
	}

	// (b) args that don't implement TenantScoped at all are rejected too.
	key2, note2 := uuid.NewString(), uuid.NewString()
	err = db.WithinTenantTx(ctx, f.app, tenantA, func(tx pgx.Tx) error {
		_, e := enq.EnqueueTx(ctx, tx, tenantA, key2, noTenantArgs{Note: note2}, nil)
		return e
	})
	if err == nil {
		t.Error("EnqueueTx accepted a non-TenantScoped job, want rejection")
	}
	if n := countKeys(t, f.app, tenantA, key2); n != 0 {
		t.Errorf("idempotency_keys rows after non-implementer = %d, want 0", n)
	}
	if n := countJobs(t, f.app, noTenantArgs{}.Kind(), note2); n != 0 {
		t.Errorf("river_job rows after non-implementer = %d, want 0", n)
	}
}

// --- helpers -------------------------------------------------------------------------

// newInsertClient builds an INSERT-ONLY client (no Queues/Workers): it never Starts, it
// only runs EnqueueTx.
func newInsertClient(t *testing.T, pool *pgxpool.Pool) *queue.Client {
	t.Helper()
	q, err := queue.New(pool, queue.Config{})
	if err != nil {
		t.Fatalf("build insert-only client: %v", err)
	}
	return q
}

// newWorkerClient builds a WORKING client on the default queue under the fast immediateRetry
// policy, registering whatever workers `register` adds.
func newWorkerClient(t *testing.T, pool *pgxpool.Pool, register func(*river.Workers)) *queue.Client {
	t.Helper()
	workers := river.NewWorkers()
	register(workers)
	q, err := queue.New(pool, queue.Config{
		Queues:      map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}},
		Workers:     workers,
		RetryPolicy: immediateRetry{},
	})
	if err != nil {
		t.Fatalf("build worker client: %v", err)
	}
	return q
}

// startClient starts a working client and registers its drained Stop via t.Cleanup.
func startClient(t *testing.T, ctx context.Context, q *queue.Client) {
	t.Helper()
	if err := q.Start(ctx); err != nil {
		t.Fatalf("start worker pool: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := q.Stop(stopCtx); err != nil {
			t.Errorf("stop worker pool: %v", err)
		}
	})
}

// enqueueEffect runs the transactional-outbox enqueue for one effect job and commits.
func enqueueEffect(t *testing.T, ctx context.Context, q *queue.Client, pool *pgxpool.Pool, tenant, key string, args river.JobArgs, opts *river.InsertOpts) {
	t.Helper()
	if err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
		_, e := q.EnqueueTx(ctx, tx, tenant, key, args, opts)
		return e
	}); err != nil {
		t.Fatalf("enqueue effect job: %v", err)
	}
}

// recordEffect writes one test_job_effects row inside a tenant-scoped tx (the RLS path the
// real submission worker will use).
func recordEffect(ctx context.Context, pool *pgxpool.Pool, tenant string, jobID int64, note string) error {
	return db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO test_job_effects (tenant_id, job_id, note) VALUES ($1, $2, $3)`,
			tenant, jobID, note)
		return e
	})
}

// countEffects counts this tenant's test_job_effects rows (FORCE RLS, so the count runs
// inside the tenant's context to see its own rows).
func countEffects(t *testing.T, pool *pgxpool.Pool, tenant string) int {
	t.Helper()
	var n int
	if err := db.WithinTenantTx(context.Background(), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM test_job_effects WHERE tenant_id = $1`, tenant).Scan(&n)
	}); err != nil {
		t.Fatalf("count test_job_effects: %v", err)
	}
	return n
}

// maxEffectsPerJob returns the largest number of effect rows any single job produced for the
// tenant — >1 means a job ran (or applied) more than once.
func maxEffectsPerJob(t *testing.T, pool *pgxpool.Pool, tenant string) int {
	t.Helper()
	var maxc int
	if err := db.WithinTenantTx(context.Background(), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT coalesce(max(c), 0) FROM (
				SELECT count(*) AS c FROM test_job_effects WHERE tenant_id = $1 GROUP BY job_id
			) s`, tenant).Scan(&maxc)
	}); err != nil {
		t.Fatalf("max effects per job: %v", err)
	}
	return maxc
}

// jobAttempt reads a job's current attempt count.
func jobAttempt(t *testing.T, pool *pgxpool.Pool, id int64) int {
	t.Helper()
	var a int
	if err := pool.QueryRow(context.Background(),
		`SELECT attempt FROM river_job WHERE id = $1`, id).Scan(&a); err != nil {
		t.Fatalf("read job %d attempt: %v", id, err)
	}
	return a
}

// waitForCompletedCount polls until at least want jobs of a kind+tenant are `completed`.
func waitForCompletedCount(t *testing.T, pool *pgxpool.Pool, kind, tenant string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var n int
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM river_job
			 WHERE kind = $1 AND args->>'tenant_id' = $2 AND state = 'completed'`,
			kind, tenant).Scan(&n); err != nil {
			t.Fatalf("count completed jobs: %v", err)
		}
		if n >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("completed jobs for tenant %s = %d after %s, want >= %d", tenant, n, timeout, want)
}

// waitForJob polls a job's (state, attempt) until pred holds or timeout elapses.
func waitForJob(t *testing.T, pool *pgxpool.Pool, id int64, timeout time.Duration, pred func(state string, attempt int) bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var state string
	var attempt int
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(context.Background(),
			`SELECT state, attempt FROM river_job WHERE id = $1`, id).Scan(&state, &attempt); err != nil {
			t.Fatalf("read job %d: %v", id, err)
		}
		if pred(state, attempt) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %d (state=%q attempt=%d) never satisfied %q within %s", id, state, attempt, desc, timeout)
}

// requireStableState asserts a job's state stays `want` for the whole window — proof it does
// not self-revive.
func requireStableState(t *testing.T, pool *pgxpool.Pool, id int64, want string, dur time.Duration) {
	t.Helper()
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		var state string
		if err := pool.QueryRow(context.Background(),
			`SELECT state FROM river_job WHERE id = $1`, id).Scan(&state); err != nil {
			t.Fatalf("read job %d state: %v", id, err)
		}
		if state != want {
			t.Fatalf("job %d state = %q during stability window, want stable %q (self-revived?)", id, state, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
