// submit_worker_test.go: the DB-backed (T05-1..T05-14) plus one hybrid DB-backed/unit
// (T05-15) spec for M5-04-05's (task-230) SubmitWorker.Work -- the tx1 / adapter / tx2
// decision flow. Authored BEFORE worker.go's real Work body exists (RALPH Stage 2.5, Mode
// A): worker.go ships only the compiling SubmitArgs/SubmitWorker surface with Work stubbed
// to return errWorkerNotImplemented, so every case below fails on ITS OWN target assertion,
// never on a compile error, a connection failure, a fixture failure, or a skip.
//
// Package submission_test (external, matching every other DB-backed file in this package --
// exchange_db_test.go, ratelimit_db_test.go, poll_ref_db_test.go, worker_smoke_test.go,
// failure_modes_test.go -- SubmitWorker's exported fields and Work method need no internal
// access). Reuses the shared TestMain fixture (failure_modes_test.go:57, requireExchangeDB /
// effectsFixture) rather than declaring a second one. internal/submission may not import
// internal/invoice ([mapper-lives-in-03], deps_test.go), so this file defines its OWN
// seedTenant/seedEntity/seedInvoice helpers AND its own hand-rolled InvoicePort double
// (testInvoicePort below) rather than wiring in *invoice.Store -- both duplicate only the
// slice of real behaviour these specs exercise, following the exChain/dashboard/importer
// duplicate-never-import precedent the story's own Stage-2 architect validation names.
//
// ADAPTER DOUBLE. scriptedAdapter wraps refAdapter (reference_adapter_test.go -- M5-02-06's
// own "doubles as M5-04's test double" seam) rather than replacing it: every method not
// overridden here forwards straight to the embedded *refAdapter, so this stays exactly as
// lawful as refAdapter itself. What it ADDS, because refAdapter alone cannot: a per-call
// Result/Evidence SEQUENCE (refAdapter only offers one fixed pair, reused forever), a Submit
// call counter (AC#1's orphaned "called exactly once per attempt" half -- see the Stage-2
// architect validation on task-230), Transform failure injection (T05-7), and an optional
// onSubmit hook (T05-15's oracle).
//
// GATING. Every DB-backed case self-skips via requireExchangeDB when the suite is
// unconfigured, and ONLY then -- the same pair of env vars every other case in this package
// gates on. T05-15 gates the same way despite its "unit" label in the spec table: see its
// own doc comment for why a live probe against the row is the only way to observe a
// transaction boundary at all.
//
// Spec-to-test map (task-230's Test Specs table):
//
//	T05-1  TestSubmitWorker_JobRowCreatedOnceAndReusedAcrossAttempts
//	T05-2  TestSubmitWorker_AcceptedMovesInvoiceToSubmitted (+ AC#1's call-count orphan)
//	T05-3  TestSubmitWorker_AcceptedDoesNotSmuggleM505Scope
//	T05-4  TestSubmitWorker_RejectedDoesNotMoveInvoiceToRejected
//	T05-5  TestSubmitWorker_RetryableMidBudgetRetriesWithoutTouchingInvoice
//	T05-6  TestSubmitWorker_DeadLetterOnFinalAttempt
//	T05-7  TestSubmitWorker_TransformFailureIsTerminalAndCheap
//	T05-8  TestSubmitWorker_AlreadyClearedShortCircuitsViaSiblingJob
//	T05-9  TestSubmitWorker_AlreadyClearedHonoursStoredIRN
//	T05-10 TestSubmitWorker_RateLimitGateSnoozesWithoutBurningBudget
//	T05-11 TestSubmitWorker_AttemptNumberingSatisfiesBothColumns (+ AC#7's adapter-identity orphan)
//	T05-12 TestSubmitWorker_TerminalReplayIsANoOp
//	T05-13 TestSubmitWorker_EvidenceOutcomeTracksReachedWire
//	T05-14 TestRLS_SubmitWorkerCannotTouchAnotherTenant
//	T05-15 TestSubmitWorker_AdapterNotCalledUnderTransaction
//
// Local run: `DEV_DB_PORT=5433 make test-queue` from this worktree, or export DATABASE_URL /
// DATABASE_MIGRATION_URL and run `go test ./internal/submission/... -run 'SubmitWorker|RLS_SubmitWorker'`.
package submission_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// --- seeding (own, unexported, duplicated -- [mapper-lives-in-03]) --------------------

// seedTenant inserts one fresh tenant as the migrator, returning its id. Duplicated (not
// imported) from exchange_db_test.go's exChain: exChain's own combined chain also seeds a
// submission_jobs row, which does not fit here -- the WORKER itself must create that row
// for most of these specs.
func seedTenant(t *testing.T, f *effectsFixture) string {
	t.Helper()
	ctx := context.Background()
	tenantID := uuid.NewString()
	err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`,
			tenantID, "M5-04-05 worker "+tenantID[:8])
		return e
	})
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return tenantID
}

// seedEntity inserts one business_entities row for tenantID, returning its id.
func seedEntity(t *testing.T, f *effectsFixture, tenantID string) string {
	t.Helper()
	ctx := context.Background()
	entityID := uuid.NewString()
	err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
			entityID, tenantID, "Worker Corp")
		return e
	})
	if err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	return entityID
}

// seedInvoice inserts one invoices row already at status='queued' -- one hop upstream of
// the edge the worker itself owns ([worker-writes-status] only drives queued->submitted /
// queued->failed; a real batch-submit endpoint, M5-04-07, is what puts an invoice at queued
// in production) -- for tenantID/entityID, returning its id.
func seedInvoice(t *testing.T, f *effectsFixture, tenantID, entityID string) string {
	t.Helper()
	ctx := context.Background()
	invoiceID := uuid.NewString()
	err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status)
			 VALUES ($1, $2, $3, $4, 'queued')`,
			invoiceID, tenantID, entityID, "WRK-"+invoiceID[:8])
		return e
	})
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	return invoiceID
}

// seedQueuedInvoice composes seedTenant/seedEntity/seedInvoice for the common case: a fresh
// tenant with one invoice already at status='queued', ready for the worker's first attempt.
func seedQueuedInvoice(t *testing.T, f *effectsFixture) (tenantID, invoiceID string, cleanup func()) {
	t.Helper()
	tenantID = seedTenant(t, f)
	entityID := seedEntity(t, f, tenantID)
	invoiceID = seedInvoice(t, f, tenantID, entityID)
	return tenantID, invoiceID, func() { cleanupTenant(t, f, tenantID) }
}

// cleanupTenant deletes everything this file's fixtures attach to tenantID, deepest first --
// app_exchange, submission_jobs, invoices (whose own ON DELETE CASCADE also clears
// invoice_status_history), business_entities, tenants -- mirroring exChain's own cleanup
// ordering (exchange_db_test.go) since app_exchange -> submission_jobs and
// submission_jobs -> invoices are both ON DELETE RESTRICT.
func cleanupTenant(t *testing.T, f *effectsFixture, tenantID string) {
	t.Helper()
	err := db.WithinTenantTx(context.Background(), f.mig, tenantID, func(tx pgx.Tx) error {
		for _, q := range []string{
			`DELETE FROM app_exchange       WHERE tenant_id = $1`,
			`DELETE FROM submission_jobs    WHERE tenant_id = $1`,
			`DELETE FROM invoices           WHERE tenant_id = $1`,
			`DELETE FROM business_entities  WHERE tenant_id = $1`,
			`DELETE FROM tenants            WHERE id = $1`,
		} {
			if _, e := tx.Exec(context.Background(), q, tenantID); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Errorf("cleanup tenant %s: %v", tenantID, err)
	}
}

// seedSiblingJob inserts a submission_jobs row directly (bypassing the worker) at the given
// state, for tenantID/invoiceID under a DIFFERENT idempotency key than the job under test --
// T05-8's "a sibling job already cleared this invoice" fixture.
func seedSiblingJob(t *testing.T, f *effectsFixture, tenantID, invoiceID, state string) string {
	t.Helper()
	ctx := context.Background()
	jobID := uuid.NewString()
	err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key,
			                              adapter, adapter_version, state, attempts)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 1)`,
			jobID, tenantID, invoiceID, "sibling-"+jobID, "reference", "v1", state)
		return e
	})
	if err != nil {
		t.Fatalf("seed sibling submission_jobs row: %v", err)
	}
	return jobID
}

// seedTerminalJob inserts a submission_jobs row directly at state='accepted' with one
// existing app_exchange row and the matching invoice-side queued->submitted transition +
// history row -- mirroring what a real prior Accepted run (T05-2's own shape) would have
// left behind, so T05-12's "no SECOND row on replay" assertion means something against a
// non-empty baseline rather than an empty one.
func seedTerminalJob(t *testing.T, f *effectsFixture, tenantID, invoiceID, idemKey string) (jobID string) {
	t.Helper()
	ctx := context.Background()
	jobID = uuid.NewString()
	err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key,
			                              adapter, adapter_version, state, attempts)
			 VALUES ($1, $2, $3, $4, $5, $6, 'accepted', 1)`,
			jobID, tenantID, invoiceID, idemKey, "reference", "v1"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx,
			`INSERT INTO app_exchange (tenant_id, submission_job_id, invoice_id, operation,
			                           outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			tenantID, jobID, invoiceID, "reference", "v1"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE invoices SET status = 'submitted' WHERE id = $1`, invoiceID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
			 VALUES ($1, $2, 'queued', 'submitted', 'system')`,
			tenantID, invoiceID)
		return e
	})
	if err != nil {
		t.Fatalf("seed terminal submission_jobs fixture: %v", err)
	}
	return jobID
}

// setInvoiceIRN writes invoices.irn directly, as the migrator (this package has no
// superuser pool -- see exchange_db_test.go's header) -- T05-9's "already cleared, honoured
// via a stored IRN rather than a sibling job" fixture.
func setInvoiceIRN(t *testing.T, f *effectsFixture, tenantID, invoiceID, irn string) {
	t.Helper()
	err := db.WithinTenantTx(context.Background(), f.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), `UPDATE invoices SET irn = $1 WHERE id = $2`, irn, invoiceID)
		return e
	})
	if err != nil {
		t.Fatalf("set invoices.irn: %v", err)
	}
}

// --- read-back helpers (app role, tenant-scoped -- matches the real caller) -----------

// wjState is one submission_jobs row's columns these specs care about.
type wjState struct {
	id             string
	state          string
	attempts       int
	lastError      *string
	adapter        string
	adapterVersion string
}

// wjTry reads the submission_jobs row for (tenantID, idemKey), or ok=false if none exists.
func wjTry(t *testing.T, f *effectsFixture, tenantID, idemKey string) (wjState, bool) {
	t.Helper()
	var got wjState
	var found bool
	err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		e := tx.QueryRow(context.Background(),
			`SELECT id, state, attempts, last_error, adapter, adapter_version
			   FROM submission_jobs WHERE tenant_id = $1 AND idempotency_key = $2`,
			tenantID, idemKey).
			Scan(&got.id, &got.state, &got.attempts, &got.lastError, &got.adapter, &got.adapterVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		found = true
		return nil
	})
	if err != nil {
		t.Fatalf("read submission_jobs (tenant=%s key=%s): %v", tenantID, idemKey, err)
	}
	return got, found
}

// wjRequire reads the submission_jobs row and t.Fatalfs if it does not exist -- used by
// specs where the row's existence is a PRECONDITION for the rest of the assertions, not
// itself the thing under test (T05-1 and T05-14 assert existence/non-existence directly via
// wjCount/wjTry instead).
func wjRequire(t *testing.T, f *effectsFixture, tenantID, idemKey string) wjState {
	t.Helper()
	got, ok := wjTry(t, f, tenantID, idemKey)
	if !ok {
		t.Fatalf("no submission_jobs row for (tenant=%s key=%s) -- want one written by Work", tenantID, idemKey)
	}
	return got
}

// wjCount counts submission_jobs rows for (tenantID, idemKey). T05-1's own claim is about
// there being EXACTLY one row, which only a count (not a single-row Scan) can refute.
func wjCount(t *testing.T, f *effectsFixture, tenantID, idemKey string) int {
	t.Helper()
	var n int
	err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM submission_jobs WHERE tenant_id = $1 AND idempotency_key = $2`,
			tenantID, idemKey).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count submission_jobs (tenant=%s key=%s): %v", tenantID, idemKey, err)
	}
	return n
}

// wiRow is the invoices columns these specs care about.
type wiRow struct {
	status           string
	irn              *string
	rejectionReasons string // raw jsonb text
}

func wiRead(t *testing.T, f *effectsFixture, tenantID, invoiceID string) wiRow {
	t.Helper()
	var got wiRow
	err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status, irn, rejection_reasons::text FROM invoices WHERE id = $1`, invoiceID).
			Scan(&got.status, &got.irn, &got.rejectionReasons)
	})
	if err != nil {
		t.Fatalf("read invoices row (tenant=%s invoice=%s): %v", tenantID, invoiceID, err)
	}
	return got
}

// wihRow is one invoice_status_history row.
type wihRow struct {
	fromStatus *string
	toStatus   string
	actor      string
}

func wiHistory(t *testing.T, f *effectsFixture, tenantID, invoiceID string) []wihRow {
	t.Helper()
	var rows []wihRow
	err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		r, e := tx.Query(context.Background(),
			`SELECT from_status, to_status, actor FROM invoice_status_history
			  WHERE invoice_id = $1 ORDER BY changed_at`, invoiceID)
		if e != nil {
			return e
		}
		defer r.Close()
		for r.Next() {
			var row wihRow
			if e := r.Scan(&row.fromStatus, &row.toStatus, &row.actor); e != nil {
				return e
			}
			rows = append(rows, row)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatalf("read invoice_status_history (tenant=%s invoice=%s): %v", tenantID, invoiceID, err)
	}
	return rows
}

// wjExchangeRow is one app_exchange row's columns these specs care about.
type wjExchangeRow struct {
	outcome        string
	attempt        int
	adapter        string
	adapterVersion string
	requestHeaders string // raw jsonb text
	requestBody    *string
}

func wjExchanges(t *testing.T, f *effectsFixture, tenantID, jobID string) []wjExchangeRow {
	t.Helper()
	var rows []wjExchangeRow
	err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		r, e := tx.Query(context.Background(),
			`SELECT outcome, attempt, adapter, adapter_version, request_headers::text, request_body
			   FROM app_exchange WHERE submission_job_id = $1 ORDER BY occurred_at`, jobID)
		if e != nil {
			return e
		}
		defer r.Close()
		for r.Next() {
			var row wjExchangeRow
			if e := r.Scan(&row.outcome, &row.attempt, &row.adapter, &row.adapterVersion,
				&row.requestHeaders, &row.requestBody); e != nil {
				return e
			}
			rows = append(rows, row)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatalf("read app_exchange rows (tenant=%s job=%s): %v", tenantID, jobID, err)
	}
	return rows
}

// --- scripted adapter (thin wrapper around refAdapter) --------------------------------

// scriptedOutcome is one Submit call's programmed (Result, Evidence) pair.
type scriptedOutcome struct {
	result   submission.Result
	evidence submission.Evidence
}

// scriptedAdapter wraps refAdapter with exactly what these specs need beyond its single
// fixed pair -- see this file's header for the full rationale. Every method not overridden
// here forwards to the embedded *refAdapter.
type scriptedAdapter struct {
	*refAdapter

	mu           sync.Mutex
	submitCalls  int
	submitQueue  []scriptedOutcome
	transformErr error  // when set, Transform returns it instead of delegating
	onSubmit     func() // called synchronously, before returning, if non-nil
}

var _ submission.Adapter = (*scriptedAdapter)(nil)

// newScriptedAdapter builds one programmed with outcomes, consumed in call order; the last
// entry repeats once exhausted. Zero outcomes means Submit must never be called at all
// (T05-7, T05-8, T05-9, T05-10, T05-14): calling it anyway panics loudly rather than
// silently returning a zero Result.
func newScriptedAdapter(outcomes ...scriptedOutcome) *scriptedAdapter {
	return &scriptedAdapter{
		refAdapter:  &refAdapter{name: "reference", version: "v1"},
		submitQueue: outcomes,
	}
}

func (a *scriptedAdapter) Transform(ctx context.Context, c submission.Canonical) (submission.Wire, error) {
	if a.transformErr != nil {
		return nil, a.transformErr
	}
	return a.refAdapter.Transform(ctx, c)
}

func (a *scriptedAdapter) Submit(ctx context.Context, w submission.Wire, idemKey string) (submission.Result, submission.Evidence) {
	a.mu.Lock()
	if len(a.submitQueue) == 0 {
		a.mu.Unlock()
		panic("scriptedAdapter.Submit: called with an empty outcome queue -- the caller " +
			"must not have reached the wire (e.g. a transform failure, an already-cleared " +
			"short-circuit, or a rate-limit block) yet Submit fired anyway")
	}
	idx := a.submitCalls
	if idx >= len(a.submitQueue) {
		idx = len(a.submitQueue) - 1
	}
	a.submitCalls++
	hook := a.onSubmit
	outcome := a.submitQueue[idx]
	a.mu.Unlock()
	if hook != nil {
		hook()
	}
	return outcome.result, outcome.evidence
}

// calls reports how many times Submit has been invoked.
func (a *scriptedAdapter) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.submitCalls
}

// --- InvoicePort double (own, hand-rolled -- [mapper-lives-in-03]) --------------------

// testInvoicePort is a hand-rolled, package-local submission.InvoicePort double.
// internal/submission (and its external test package) may not import internal/invoice
// (deps_test.go / [mapper-lives-in-03]), so this duplicates only the SLICE of *invoice.
// Store's real behaviour these specs actually exercise -- the queued->submitted /
// queued->failed edges worker.go drives InvoicePort through, with the same idempotent-
// no-op shape invoice_port.go's own doc comment promises ("a redundant call on an
// already-[submitted|failed] invoice returns nil"). It is NOT a general invoice state
// machine: it recognises exactly the "queued" source state and panics via a returned error
// on anything else, which is deliberate -- if the worker starts asking this double to do
// more, that is a signal the double needs to grow, not a silent false pass.
type testInvoicePort struct{}

func (testInvoicePort) Canonical(ctx context.Context, tx pgx.Tx, invoiceID string) (submission.Canonical, error) {
	return submission.Canonical{InvoiceID: invoiceID}, nil
}

func (testInvoicePort) HasFiscalOutcome(ctx context.Context, tx pgx.Tx, invoiceID string) (bool, error) {
	var has bool
	err := tx.QueryRow(ctx, `SELECT irn IS NOT NULL FROM invoices WHERE id = $1`, invoiceID).Scan(&has)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return has, err
}

func (testInvoicePort) MarkSubmitted(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string) error {
	return tipMarkTerminal(ctx, tx, invoiceID, tenantID, "submitted")
}

func (testInvoicePort) MarkFailed(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string) error {
	return tipMarkTerminal(ctx, tx, invoiceID, tenantID, "failed")
}

var _ submission.InvoicePort = testInvoicePort{}

// tipMarkTerminal is MarkSubmitted/MarkFailed's shared tail: lock+read the current status,
// short-circuit as an idempotent no-op when already at target, otherwise require the source
// to be "queued" (the only edge this worker drives) and write the transition + one history
// row with actor 'system'.
func tipMarkTerminal(ctx context.Context, tx pgx.Tx, invoiceID, tenantID, target string) error {
	var current string
	if err := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1 FOR UPDATE`, invoiceID).Scan(&current); err != nil {
		return err
	}
	if current == target {
		return nil // idempotent no-op, mirrors invoice.Store.markTerminalTx
	}
	if current != "queued" {
		return fmt.Errorf("testInvoicePort: illegal transition %s -> %s", current, target)
	}
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status = $1 WHERE id = $2`, target, invoiceID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
		 VALUES ($1, $2, $3, $4, 'system')`,
		tenantID, invoiceID, current, target)
	return err
}

// --- job/worker construction -----------------------------------------------------------

// newTestWorker builds a SubmitWorker over pool/adapter with a fresh RateLimiter (never
// shared across tests) and the default limit high enough not to interfere with any spec
// other than T05-10, which overrides RateLimit itself.
func newTestWorker(pool *pgxpool.Pool, adapter submission.Adapter) *submission.SubmitWorker {
	return &submission.SubmitWorker{
		Pool:        pool,
		Adapter:     adapter,
		InvoicePort: testInvoicePort{},
		Limiter:     submission.NewRateLimiter(),
		RateLimit:   60,
	}
}

// newSubmitJob builds a *river.Job[SubmitArgs] the way River hands one to Work -- id is
// CONSTANT across simulated retries of "the same" job (river.Job.ID never changes across a
// job's own retries in reality); attempt/maxAttempts are what T05-5/T05-6/T05-11 vary.
func newSubmitJob(id int64, attempt, maxAttempts int, args submission.SubmitArgs) *river.Job[submission.SubmitArgs] {
	return &river.Job[submission.SubmitArgs]{
		JobRow: &rivertype.JobRow{ID: id, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   args,
	}
}

// --- T05-1 -------------------------------------------------------------------------------

func TestSubmitWorker_JobRowCreatedOnceAndReusedAcrossAttempts(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(
		scriptedOutcome{result: submission.Retryable{Err: errors.New("wsub: transient, attempt 1")}, evidence: submission.Evidence{ReachedWire: true}},
		scriptedOutcome{result: submission.Accepted{IRN: "NG-1", CSID: "CSID-1", QRPayload: "QR-1"}, evidence: submission.Evidence{ReachedWire: true}},
	)
	w := newTestWorker(f.app, adapter)
	args := submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey}

	_ = w.Work(ctx, newSubmitJob(1, 1, 8, args)) // attempt 1
	_ = w.Work(ctx, newSubmitJob(1, 2, 8, args)) // attempt 2, same job id

	if n := wjCount(t, f, tenantID, idemKey); n != 1 {
		t.Errorf("submission_jobs rows for (tenant, idempotency_key) after two attempts = %d, want exactly 1", n)
	}
}

// --- T05-2 -------------------------------------------------------------------------------

func TestSubmitWorker_AcceptedMovesInvoiceToSubmitted(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Accepted{IRN: "NG-1", CSID: "CSID-1", QRPayload: "QR-1"},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Errorf("Work on an Accepted outcome returned %v, want nil", err)
	}

	// AC#1's orphaned "called exactly once per attempt" half (Stage-2 architect validation).
	if got := adapter.calls(); got != 1 {
		t.Errorf("adapter.Submit call count = %d, want exactly 1", got)
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Errorf("job state = %q, want \"accepted\"", wj.state)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "submitted" {
		t.Errorf("invoice status = %q, want \"submitted\"", inv.status)
	}
	hist := wiHistory(t, f, tenantID, invoiceID)
	if len(hist) != 1 {
		t.Fatalf("invoice_status_history rows = %d, want exactly 1", len(hist))
	}
	if hist[0].fromStatus == nil || *hist[0].fromStatus != "queued" {
		t.Errorf("history from_status = %v, want \"queued\"", hist[0].fromStatus)
	}
	if hist[0].toStatus != "submitted" {
		t.Errorf("history to_status = %q, want \"submitted\"", hist[0].toStatus)
	}
	if hist[0].actor != "system" {
		t.Errorf("history actor = %q, want \"system\"", hist[0].actor)
	}
}

// --- T05-3 -------------------------------------------------------------------------------

func TestSubmitWorker_AcceptedDoesNotSmuggleM505Scope(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Accepted{IRN: "NG-1", CSID: "CSID-1", QRPayload: "QR-1"},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	// A t.Fatalf (not Errorf) here is load-bearing: without it, an unimplemented Work simply
	// leaves irn/rejection_reasons untouched and this test would PASS VACUOUSLY throughout
	// RALPH Stage 2.5 -- exactly the M5-05-scope smuggling bug this spec exists to catch,
	// undetected, if Work "fails" for an unrelated reason instead of succeeding and
	// (wrongly) writing the IRN.
	if err := w.Work(ctx, job); err != nil {
		t.Fatalf("Work on an Accepted outcome: %v", err)
	}

	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.irn != nil {
		t.Errorf("invoices.irn = %q, want NULL -- storing IRN is M5-05's job, not this worker's", *inv.irn)
	}
	if inv.rejectionReasons != "[]" {
		t.Errorf("invoices.rejection_reasons = %s, want \"[]\" -- this worker never writes it", inv.rejectionReasons)
	}
}

// --- T05-4 -------------------------------------------------------------------------------

func TestSubmitWorker_RejectedDoesNotMoveInvoiceToRejected(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Rejected{Reasons: []submission.Reason{{Code: "E1", Message: "bad TIN"}}},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Errorf("Work on a Rejected outcome returned %v, want nil", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "rejected" {
		t.Errorf("job state = %q, want \"rejected\"", wj.state)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "submitted" {
		t.Errorf("invoice status = %q, want \"submitted\" -- [reaching-the-app-means-a-verdict]: "+
			"Rejected still reached the APP", inv.status)
	}
}

// --- T05-5 -------------------------------------------------------------------------------

func TestSubmitWorker_RetryableMidBudgetRetriesWithoutTouchingInvoice(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Retryable{Err: errors.New("wsub: upstream 503, mid-budget")},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey}) // attempt 1 of 8

	err := w.Work(ctx, job)
	if err == nil {
		t.Fatal("Work on a mid-budget Retryable returned nil, want the original error so River retries")
	}
	var cancelErr *river.JobCancelError
	if errors.As(err, &cancelErr) {
		t.Error("Work returned a river.JobCancelError for a mid-budget Retryable -- that stops retries; want the plain error")
	}
	var snoozeErr *river.JobSnoozeError
	if errors.As(err, &snoozeErr) {
		t.Error("Work returned a river.JobSnoozeError for a mid-budget Retryable -- that is the rate-limit path's shape, not this one's")
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "queued" {
		t.Errorf("job state = %q, want \"queued\" (back for retry)", wj.state)
	}
	if wj.lastError == nil || *wj.lastError == "" {
		t.Error("job last_error is empty, want the Retryable's error message recorded")
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "queued" {
		t.Errorf("invoice status = %q, want unchanged \"queued\" -- [reaching-the-app-means-a-verdict]", inv.status)
	}
}

// --- T05-6 -------------------------------------------------------------------------------

func TestSubmitWorker_DeadLetterOnFinalAttempt(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Retryable{Err: errors.New("wsub: upstream 503, final attempt")},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 8, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey}) // job.Attempt == MaxAttempts

	err := w.Work(ctx, job)
	if err == nil {
		t.Error("Work on a Retryable final attempt returned nil, want a non-nil error so River discards the job")
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "dead_lettered" {
		t.Errorf("job state = %q, want \"dead_lettered\"", wj.state)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "failed" {
		t.Errorf("invoice status = %q, want \"failed\"", inv.status)
	}
}

// --- T05-7 -------------------------------------------------------------------------------

func TestSubmitWorker_TransformFailureIsTerminalAndCheap(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter() // empty queue: Submit must never fire
	adapter.transformErr = errors.New("wsub: cannot build wire from this invoice")
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	err := w.Work(ctx, job)
	if err == nil {
		t.Fatal("Work on a transform failure returned nil, want river.JobCancel so no retry occurs")
	}
	var cancelErr *river.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Errorf("Work error = %v, want it to satisfy errors.As(&river.JobCancelError{})", err)
	}
	if got := adapter.calls(); got != 0 {
		t.Errorf("adapter.Submit call count on a transform failure = %d, want 0 -- "+
			"[transform-yields-no-evidence]: Submit must never fire", got)
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "failed" {
		t.Errorf("job state = %q, want \"failed\"", wj.state)
	}
	if wj.attempts != 0 {
		t.Errorf("job attempts = %d, want unchanged 0 -- a transform failure must not consume the retry budget", wj.attempts)
	}
	exchanges := wjExchanges(t, f, tenantID, wj.id)
	if len(exchanges) != 1 {
		t.Fatalf("app_exchange rows = %d, want exactly 1", len(exchanges))
	}
	ex := exchanges[0]
	if ex.outcome != "transform_failed" {
		t.Errorf("outcome = %q, want \"transform_failed\"", ex.outcome)
	}
	if ex.requestHeaders != "{}" {
		t.Errorf("request_headers = %s, want \"{}\"", ex.requestHeaders)
	}
	if ex.requestBody != nil {
		t.Errorf("request_body = %q, want NULL", *ex.requestBody)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "failed" {
		t.Errorf("invoice status = %q, want \"failed\"", inv.status)
	}
}

// --- T05-8 -------------------------------------------------------------------------------

func TestSubmitWorker_AlreadyClearedShortCircuitsViaSiblingJob(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	seedSiblingJob(t, f, tenantID, invoiceID, "accepted")

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter() // Submit must never fire
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Errorf("Work on an already-cleared invoice returned %v, want nil", err)
	}
	if got := adapter.calls(); got != 0 {
		t.Errorf("adapter.Submit call count = %d, want 0 -- an already-cleared invoice must never reach Submit", got)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Errorf("this job's own state = %q, want \"accepted\"", wj.state)
	}
	if wj.attempts != 0 {
		t.Errorf("attempts = %d, want unchanged 0", wj.attempts)
	}
	exchanges := wjExchanges(t, f, tenantID, wj.id)
	if len(exchanges) != 1 || exchanges[0].outcome != "skipped_already_cleared" {
		t.Errorf("app_exchange rows = %+v, want exactly one skipped_already_cleared row", exchanges)
	}
}

// --- T05-9 -------------------------------------------------------------------------------

func TestSubmitWorker_AlreadyClearedHonoursStoredIRN(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	setInvoiceIRN(t, f, tenantID, invoiceID, "NG-STORED-1")

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter() // Submit must never fire
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Errorf("Work on an invoice with a stored irn returned %v, want nil", err)
	}
	if got := adapter.calls(); got != 0 {
		t.Errorf("adapter.Submit call count = %d, want 0", got)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Errorf("job state = %q, want \"accepted\"", wj.state)
	}
	exchanges := wjExchanges(t, f, tenantID, wj.id)
	if len(exchanges) != 1 || exchanges[0].outcome != "skipped_already_cleared" {
		t.Errorf("app_exchange rows = %+v, want exactly one skipped_already_cleared row", exchanges)
	}
}

// --- T05-10 ------------------------------------------------------------------------------

func TestSubmitWorker_RateLimitGateSnoozesWithoutBurningBudget(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter() // Submit must never fire: blocked before the wire
	w := newTestWorker(f.app, adapter)
	w.RateLimit = 0 // forces limiter.Allow to deny unconditionally and deterministically
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	err := w.Work(ctx, job)
	if err == nil {
		t.Fatal("Work under a denied rate-limit gate returned nil, want river.JobSnooze")
	}
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Errorf("Work error = %v, want it to satisfy errors.As(&river.JobSnoozeError{})", err)
	}
	if got := adapter.calls(); got != 0 {
		t.Errorf("adapter.Submit call count = %d, want 0", got)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "queued" {
		t.Errorf("job state = %q, want unchanged \"queued\"", wj.state)
	}
	if wj.attempts != 0 {
		t.Errorf("attempts = %d, want unchanged 0", wj.attempts)
	}
	exchanges := wjExchanges(t, f, tenantID, wj.id)
	if len(exchanges) != 1 || exchanges[0].outcome != "blocked_rate_limit" {
		t.Errorf("app_exchange rows = %+v, want exactly one blocked_rate_limit row", exchanges)
	}
}

// --- T05-11 ------------------------------------------------------------------------------

func TestSubmitWorker_AttemptNumberingSatisfiesBothColumns(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(
		scriptedOutcome{result: submission.Retryable{Err: errors.New("wsub: transient, attempt 1")}, evidence: submission.Evidence{ReachedWire: true}},
		scriptedOutcome{result: submission.Accepted{IRN: "NG-1", CSID: "CSID-1", QRPayload: "QR-1"}, evidence: submission.Evidence{ReachedWire: true}},
	)
	w := newTestWorker(f.app, adapter)
	args := submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey}

	if err := w.Work(ctx, newSubmitJob(1, 1, 8, args)); err == nil {
		t.Fatal("first (Retryable) attempt returned nil, want the original error")
	}
	if err := w.Work(ctx, newSubmitJob(1, 2, 8, args)); err != nil {
		t.Fatalf("second (Accepted) attempt: %v", err)
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.attempts != 2 {
		t.Errorf("submission_jobs.attempts = %d, want 2", wj.attempts)
	}
	if wj.adapter != "reference" || wj.adapterVersion != "v1" {
		t.Errorf("job adapter/adapter_version = %q/%q, want \"reference\"/\"v1\"", wj.adapter, wj.adapterVersion)
	}

	exchanges := wjExchanges(t, f, tenantID, wj.id)
	if len(exchanges) != 2 {
		t.Fatalf("app_exchange rows = %d, want exactly 2", len(exchanges))
	}
	if exchanges[0].attempt != 1 || exchanges[1].attempt != 2 {
		t.Errorf("app_exchange.attempt values = {%d,%d}, want exactly {1,2}", exchanges[0].attempt, exchanges[1].attempt)
	}
	// AC#7's orphaned "every recorded exchange carries the job's adapter/adapter_version"
	// half (Stage-2 architect validation): compared against the JOB's own stamped columns,
	// not a hardcoded literal, so this fails if the two ever drift apart.
	for i, ex := range exchanges {
		if ex.adapter != wj.adapter || ex.adapterVersion != wj.adapterVersion {
			t.Errorf("exchange[%d] adapter/adapter_version = %q/%q, want the job's own %q/%q",
				i, ex.adapter, ex.adapterVersion, wj.adapter, wj.adapterVersion)
		}
	}
}

// --- T05-12 ------------------------------------------------------------------------------

func TestSubmitWorker_TerminalReplayIsANoOp(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	jobID := seedTerminalJob(t, f, tenantID, invoiceID, idemKey)

	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Accepted{IRN: "NG-1", CSID: "CSID-1", QRPayload: "QR-1"},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Errorf("Work on an already-accepted job returned %v, want nil (a crash-replay must be a no-op)", err)
	}
	if got := adapter.calls(); got != 0 {
		t.Errorf("adapter.Submit call count on a terminal replay = %d, want 0 -- the terminal "+
			"short-circuit must return before ever reaching the adapter", got)
	}
	if n := exCountRows(t, f, tenantID, jobID); n != 1 {
		t.Errorf("app_exchange rows for job %s after replay = %d, want 1 (unchanged -- no second row)", jobID, n)
	}
	if hist := wiHistory(t, f, tenantID, invoiceID); len(hist) != 1 {
		t.Errorf("invoice_status_history rows after replay = %d, want 1 (unchanged -- no second row)", len(hist))
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Errorf("job state after replay = %q, want unchanged \"accepted\"", wj.state)
	}
	if wj.attempts != 1 {
		t.Errorf("job attempts after replay = %d, want unchanged 1", wj.attempts)
	}
}

// --- T05-13 ------------------------------------------------------------------------------

func TestSubmitWorker_EvidenceOutcomeTracksReachedWire(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()

	cases := []struct {
		name        string
		reachedWire bool
		wantOutcome string
	}{
		{"reached the wire", true, "sent"},
		{"never reached the wire", false, "connection_failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
			defer cleanup()

			idemKey := "req-" + uuid.NewString() + ":" + invoiceID
			adapter := newScriptedAdapter(scriptedOutcome{
				result:   submission.Accepted{IRN: "NG-1", CSID: "CSID-1", QRPayload: "QR-1"},
				evidence: submission.Evidence{ReachedWire: tc.reachedWire},
			})
			w := newTestWorker(f.app, adapter)
			job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

			if err := w.Work(ctx, job); err != nil {
				t.Fatalf("Work: %v", err)
			}
			wj := wjRequire(t, f, tenantID, idemKey)
			exchanges := wjExchanges(t, f, tenantID, wj.id)
			if len(exchanges) != 1 {
				t.Fatalf("app_exchange rows = %d, want 1", len(exchanges))
			}
			if exchanges[0].outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q (ReachedWire=%v)", exchanges[0].outcome, tc.wantOutcome, tc.reachedWire)
			}
		})
	}
}

// --- T05-14 ------------------------------------------------------------------------------

// TestRLS_SubmitWorkerCannotTouchAnotherTenant is, HONESTLY, STRUCTURALLY GREEN during
// RALPH Stage 2.5 -- flagging this explicitly rather than leaving it to be discovered as a
// silent, uncommented pass. T05-14's own claim ("fails closed -- no job row, no exchange
// row, no status change") is a pure NEGATIVE/absence invariant, and every one of the four
// assertions below is already satisfied by errWorkerNotImplemented's blanket, do-nothing
// stub: it returns non-nil (satisfying "want a fail-closed error"), never calls Submit,
// and writes nothing under any tenant -- exactly what a CORRECT fail-closed implementation
// also produces, via the composite FK (tenant_id, invoice_id) REFERENCES invoices
// (tenant_id, id) that submission_jobs itself carries (migrations/20260722085427_
// submission_jobs.sql) rejecting the cross-tenant INSERT before anything is written.
// There is no DB-observable difference between "an unimplemented stub returns immediately"
// and "a correct implementation attempted the write and was refused" -- both leave zero
// footprint -- so this spec cannot be forced into a genuine RED without asserting on
// Stage 3's not-yet-written, unknown-in-advance error SHAPE (which risks a test that is
// wrong, not just green, if Stage 3 legitimately takes a different valid path, e.g.
// checking InvoicePort.Canonical before ever attempting the INSERT).
//
// This is NOT license to treat "still green after Stage 3" as proof of correctness: Mode B
// (QA, post-implementation) MUST specifically re-audit this spec's meaningfulness -- e.g. by
// hand-verifying that a deliberately-broken Stage 3 (one that skips the tenant match check)
// would make this test fail -- rather than accept the green bar at face value, per
// [suite-proven-by-red].
func TestRLS_SubmitWorkerCannotTouchAnotherTenant(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantA, invoiceA, cleanupA := seedQueuedInvoice(t, f)
	defer cleanupA()
	tenantB := seedTenant(t, f)
	defer cleanupTenant(t, f, tenantB)

	idemKey := "req-" + uuid.NewString() + ":" + invoiceA
	adapter := newScriptedAdapter() // Submit must never fire
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantB, InvoiceID: invoiceA, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err == nil {
		t.Error("Work with tenant B naming tenant A's invoice returned nil, want a fail-closed error")
	}
	if got := adapter.calls(); got != 0 {
		t.Errorf("adapter.Submit call count = %d, want 0", got)
	}
	if n := wjCount(t, f, tenantB, idemKey); n != 0 {
		t.Errorf("submission_jobs rows under tenant B for the mismatched job = %d, want 0", n)
	}
	// Belt-and-suspenders: no leakage under tenant A's own scope either (a buggy
	// implementation could theoretically use InvoicePort.Canonical's tenant, not the job's
	// declared tenant, to open tx1).
	if n := wjCount(t, f, tenantA, idemKey); n != 0 {
		t.Errorf("submission_jobs rows under tenant A for the mismatched job = %d, want 0", n)
	}
	inv := wiRead(t, f, tenantA, invoiceA)
	if inv.status != "queued" {
		t.Errorf("tenant A's invoice status = %q, want unchanged \"queued\"", inv.status)
	}
}

// --- T05-15 ------------------------------------------------------------------------------

// TestSubmitWorker_AdapterNotCalledUnderTransaction closes AC#1's "never inside an open
// transaction" half. Adapter.Submit(ctx, w, idemKey) carries no tx/db handle at all
// ([adapters-are-db-free]), so there is nothing type-level to introspect -- the risk AC#1
// guards against is a CODE-STRUCTURE violation (Submit invoked from inside the closure
// passed to tx1's or tx2's db.WithinTenantTx), observable only via the database itself.
//
// THE ORACLE (Stage-2 architect validation's option (a)): the scripted adapter's onSubmit
// hook opens its OWN, independent connection (a separate db.WithinTenantTx on the same
// pool) and issues `SELECT id, state FROM submission_jobs ... FOR UPDATE NOWAIT` on the
// SAME job row the worker is processing, at the exact moment Submit fires. If the worker
// still held tx1's (or a wrongly-early tx2's) row lock, this probe would block; NOWAIT turns
// that into an immediate SQLSTATE 55P03 (lock_not_available) instead -- so a probe failure
// with that code is a genuine proof of nesting, not a flaky timing assumption. A probe
// SUCCESS additionally requires state='submitting' (tx1's write already durable), so the
// oracle proves both halves at once: no lock is held, AND tx1 has already committed --
// together exactly [adapters-are-db-free]'s "no transaction spans an adapter call".
//
// This IS DB-backed despite the spec table's "unit" label: a live probe against the row is
// the only way to observe a transaction boundary at all (a pure-unit fake adapter has
// nothing to inspect, since Submit's signature carries no tx). See task-230's Stage-2
// architect validation note on T05-15 for the alternatives considered.
func TestSubmitWorker_AdapterNotCalledUnderTransaction(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Accepted{IRN: "NG-1", CSID: "CSID-1", QRPayload: "QR-1"},
		evidence: submission.Evidence{ReachedWire: true},
	})

	var probeRan bool
	var probeErr error
	var probedState string
	adapter.onSubmit = func() {
		probeRan = true
		probeErr = db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
			var id string
			return tx.QueryRow(ctx,
				`SELECT id, state FROM submission_jobs
				  WHERE tenant_id = $1 AND idempotency_key = $2 FOR UPDATE NOWAIT`,
				tenantID, idemKey).Scan(&id, &probedState)
		})
	}

	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})
	_ = w.Work(ctx, job)

	// This IS the RED-phase failure today: Work is stubbed to errWorkerNotImplemented and
	// returns before ever reaching Adapter.Submit, so calls()==0, not 1.
	if got := adapter.calls(); got != 1 {
		t.Fatalf("adapter.Submit call count = %d, want exactly 1", got)
	}
	if !probeRan {
		t.Fatal("onSubmit hook never ran despite calls()==1 -- inconsistent scriptedAdapter state")
	}
	if probeErr != nil {
		if code := exPgCode(probeErr); code == "55P03" {
			t.Fatalf("independent-connection NOWAIT probe on the job row blocked (55P03 "+
				"lock_not_available) while Submit was executing -- the worker is holding a "+
				"transaction open across the adapter call: %v", probeErr)
		}
		t.Fatalf("independent-connection probe on the job row failed for an unexpected reason "+
			"while Submit was executing: %v", probeErr)
	}
	if probedState != "submitting" {
		t.Errorf("independent probe read submission_jobs.state = %q at the moment Submit "+
			"fired, want \"submitting\" -- tx1's write must already be committed and visible "+
			"before the adapter call, per the tx1 -> (no tx) -> tx2 sequence", probedState)
	}
}
