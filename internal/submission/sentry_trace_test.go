package submission_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const traceSpanOp = "db.sql.query"

// traceTestPool opens a db.NewPool pool on DATABASE_URL. requireDB owns the skip and the ping.
func traceTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	requireDB(t).Close()
	pool, err := db.NewPool(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("db.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type traceNoopArgs struct{}

func (traceNoopArgs) Kind() string { return "sentry_trace_noop" }

type traceNoopWorker struct {
	river.WorkerDefaults[traceNoopArgs]
}

func (traceNoopWorker) Work(context.Context, *river.Job[traceNoopArgs]) error { return nil }

func traceGet(app *platform.App, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// dbSpans returns the transaction's db.sql.query children in the order they finished.
func dbSpans(ev *sentry.Event) []*sentry.Span {
	var out []*sentry.Span
	for _, s := range ev.Spans {
		if s.Op == traceSpanOp {
			out = append(out, s)
		}
	}
	return out
}

func oneTransaction(t *testing.T, rec *sentrytest.Recorder, name string) *sentry.Event {
	t.Helper()
	txs := rec.Transactions()
	if len(txs) != 1 {
		t.Fatalf("sent %d transactions, want 1", len(txs))
	}
	if txs[0].Transaction != name {
		t.Fatalf("transaction name = %q, want %q", txs[0].Transaction, name)
	}
	return txs[0]
}

func TestTraceSentry_TenantTxSpansOnePerStatement(t *testing.T) {
	pool := traceTestPool(t)
	app, rec, _ := sentrytest.Boot(t, "submission")
	app.Mux.HandleFunc("GET /v1/count", func(w http.ResponseWriter, r *http.Request) {
		err := db.WithinTenantTx(r.Context(), pool, uuid.NewString(), func(tx pgx.Tx) error {
			var n int
			return tx.QueryRow(r.Context(), "SELECT 1").Scan(&n)
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	if w := traceGet(app, "/v1/count"); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/count = %d: %s", w.Code, w.Body)
	}

	spans := dbSpans(oneTransaction(t, rec, "GET /v1/count"))
	want := []string{"begin", "SELECT set_config('app.current_tenant', $1, true)", "SELECT 1", "commit"}
	var got []string
	for _, s := range spans {
		got = append(got, s.Description)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("db.sql.query descriptions = %q, want %q", got, want)
	}
}

// pollCounter counts River's fetch queries per queue, then delegates to the pool's own tracer.
type pollCounter struct {
	inner pgx.QueryTracer
	mu    sync.Mutex
	polls map[string]int
}

func (c *pollCounter) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SKIP LOCKED") {
		args := fmt.Sprint(data.Args)
		c.mu.Lock()
		for q := range c.polls {
			if strings.Contains(args, q) {
				c.polls[q]++
			}
		}
		c.mu.Unlock()
	}
	if c.inner == nil {
		return ctx
	}
	return c.inner.TraceQueryStart(ctx, conn, data)
}

func (c *pollCounter) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if c.inner != nil {
		c.inner.TraceQueryEnd(ctx, conn, data)
	}
}

func (c *pollCounter) min() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := -1
	for _, n := range c.polls {
		if m < 0 || n < m {
			m = n
		}
	}
	return m
}

func TestTraceSentry_RiverPollingOpensNoTransaction(t *testing.T) {
	base := traceTestPool(t)
	cfg := base.Config()
	queues := []string{uuid.NewString() + "-a", uuid.NewString() + "-b"}
	counter := &pollCounter{inner: cfg.ConnConfig.Tracer, polls: map[string]int{queues[0]: 0, queues[1]: 0}}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open counting pool: %v", err)
	}
	t.Cleanup(pool.Close)
	// River registers each queue in river_queue and never removes it.
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM river_queue WHERE name = ANY($1)", queues); err != nil {
			t.Errorf("delete river_queue rows: %v", err)
		}
	})

	app, rec, _ := sentrytest.Boot(t, "submission")
	workers := river.NewWorkers()
	river.AddWorker(workers, &traceNoopWorker{})
	q, err := queue.New(pool, queue.Config{
		Queues:  map[string]river.QueueConfig{queues[0]: {MaxWorkers: 1}, queues[1]: {MaxWorkers: 1}},
		Workers: workers,
	})
	if err != nil {
		t.Fatalf("build queue client: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("start queue client: %v", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Errorf("stop queue client: %v", err)
		}
	}
	t.Cleanup(stop)

	// Wait until each queue has been fetched three times, so the assertion sees real polling.
	for deadline := time.Now().Add(30 * time.Second); counter.min() < 3; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the slowest queue was polled %d times in 30s, want at least 3", counter.min())
		}
	}
	stop()

	if got := rec.Transactions(); len(got) != 0 {
		t.Fatalf("River polling sent %d transactions, want none: first is %q", len(got), got[0].Transaction)
	}

	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		if _, err := pool.Exec(r.Context(), "SELECT 1"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	if w := traceGet(app, "/v1/ping"); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/ping = %d: %s", w.Code, w.Body)
	}
	ev := oneTransaction(t, rec, "GET /v1/ping")
	spans := dbSpans(ev)
	if len(spans) != 1 || spans[0].Description != "SELECT 1" {
		t.Errorf("GET /v1/ping db.sql.query spans = %d, want one with description %q", len(spans), "SELECT 1")
	}
}

func TestTraceSentry_QueryArgumentsAndErrorsDoNotLeak(t *testing.T) {
	pool := traceTestPool(t)
	app, rec, _ := sentrytest.Boot(t, "submission")

	// Postgres %q-quotes a bad value, and the span filter redacts quoted text. A plpgsql
	// RAISE puts the marker in the message unquoted, so only that error can show a leak.
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	t.Cleanup(conn.Release)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP FUNCTION IF EXISTS pg_temp.leak(text)") })
	if _, err := conn.Exec(context.Background(),
		`CREATE FUNCTION pg_temp.leak(text) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'leak %', $1; END $$`); err != nil {
		t.Fatalf("create pg_temp.leak: %v", err)
	}

	var quotedErr, rawErr error
	app.Mux.HandleFunc("GET /v1/leak", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, err := pool.Exec(ctx, "SELECT $1::text", sentrytest.MarkerCred); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, quotedErr = pool.Exec(ctx, "SELECT $1::int", sentrytest.MarkerCred)
		_, rawErr = conn.Exec(ctx, "SELECT pg_temp.leak($1)", sentrytest.MarkerCred)
	})

	if w := traceGet(app, "/v1/leak"); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/leak = %d: %s", w.Code, w.Body)
	}
	for name, err := range map[string]error{"quoted": quotedErr, "unquoted": rawErr} {
		if err == nil || !strings.Contains(err.Error(), sentrytest.MarkerCred) {
			t.Fatalf("%s error = %v; it must hold the marker or the row cannot fail", name, err)
		}
	}

	spans := dbSpans(oneTransaction(t, rec, "GET /v1/leak"))
	if len(spans) != 3 {
		t.Fatalf("db.sql.query spans = %d, want 3", len(spans))
	}
	if spans[0].Status == sentry.SpanStatusInternalError {
		t.Error("the successful query set status internal_error")
	}
	for i, s := range spans[1:] {
		if s.Status != sentry.SpanStatusInternalError {
			t.Errorf("failed query %d status = %v, want internal_error", i+1, s.Status)
		}
	}
	sentrytest.AssertNoLeak(t, rec.Transactions())
}

func spanDescriptions(spans []*sentry.Span) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Description)
	}
	return out
}

func TestTraceSentry_PgxCallPathsEachFinishTheirOwnSpan(t *testing.T) {
	pool := traceTestPool(t)
	_, rec, _ := sentrytest.Boot(t, "submission")
	parent := sentry.StartTransaction(context.Background(), "GET /v1/paths")
	ctx := parent.Context()
	var n int

	if _, err := pool.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	rows, err := pool.Query(ctx, "SELECT 2")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for rows.Next() {
	}
	rows.Close()
	rows.Close()
	if err := pool.QueryRow(ctx, "SELECT 3").Scan(&n); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT 4 WHERE false").Scan(&n); err != pgx.ErrNoRows {
		t.Fatalf("no-row QueryRow error = %v, want ErrNoRows", err)
	}
	if err := pool.QueryRow(ctx, "SELECT 1/0").Scan(&n); err == nil {
		t.Fatal("SELECT 1/0 succeeded")
	}
	slow, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = pool.Exec(slow, "SELECT pg_sleep(5)")
	cancel()
	if err == nil {
		t.Fatal("a cancelled query succeeded")
	}
	// Batches are outside the tracer's ceiling: no span, and no other span is finished for them.
	b := &pgx.Batch{}
	b.Queue("SELECT 5")
	b.Queue("SELECT 6")
	br := pool.SendBatch(ctx, b)
	if err := br.Close(); err != nil {
		t.Fatalf("batch: %v", err)
	}

	if !parent.EndTime.IsZero() {
		t.Fatal("a query finished the parent span")
	}
	if got := rec.Transactions(); len(got) != 0 {
		t.Fatalf("queries sent %d transactions before the parent finished", len(got))
	}
	parent.Finish()

	spans := dbSpans(oneTransaction(t, rec, "GET /v1/paths"))
	want := []string{"SELECT 1", "SELECT 2", "SELECT 3", "SELECT 4 WHERE false", "SELECT 1/0", "SELECT pg_sleep(5)"}
	if fmt.Sprint(spanDescriptions(spans)) != fmt.Sprint(want) {
		t.Fatalf("db.sql.query descriptions = %q, want %q", spanDescriptions(spans), want)
	}
	failed := map[string]bool{"SELECT 1/0": true, "SELECT pg_sleep(5)": true}
	for _, s := range spans {
		if s.EndTime.IsZero() {
			t.Errorf("span %q was never finished", s.Description)
		}
		if got := s.Status == sentry.SpanStatusInternalError; got != failed[s.Description] {
			t.Errorf("span %q internal_error = %v, want %v", s.Description, got, failed[s.Description])
		}
	}
}

func TestTraceSentry_NestedTxSpansOnePerStatement(t *testing.T) {
	pool := traceTestPool(t)
	_, rec, _ := sentrytest.Boot(t, "submission")
	parent := sentry.StartTransaction(context.Background(), "GET /v1/nested")
	ctx := parent.Context()

	err := db.WithinTenantTx(ctx, pool, uuid.NewString(), func(tx pgx.Tx) error {
		inner, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := inner.Exec(ctx, "SELECT 1"); err != nil {
			return err
		}
		if err := inner.Rollback(ctx); err != nil {
			return err
		}
		inner, err = tx.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := inner.Exec(ctx, "SELECT 2"); err != nil {
			return err
		}
		return inner.Commit(ctx)
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
	parent.Finish()

	spans := dbSpans(oneTransaction(t, rec, "GET /v1/nested"))
	want := []string{
		"begin", "SELECT set_config('app.current_tenant', $1, true)",
		"savepoint sp_1", "SELECT 1", "rollback to savepoint sp_1",
		"savepoint sp_2", "SELECT 2", "release savepoint sp_2",
		"commit",
	}
	if fmt.Sprint(spanDescriptions(spans)) != fmt.Sprint(want) {
		t.Errorf("db.sql.query descriptions = %q, want %q", spanDescriptions(spans), want)
	}
	for _, s := range spans {
		if s.EndTime.IsZero() {
			t.Errorf("span %q was never finished", s.Description)
		}
	}
}

func TestTraceSentry_UnclosedRowsNeverReopenASentTransaction(t *testing.T) {
	pool := traceTestPool(t)
	_, rec, _ := sentrytest.Boot(t, "submission")
	parent := sentry.StartTransaction(context.Background(), "GET /v1/unclosed")

	if _, err := pool.Exec(parent.Context(), "SELECT 1"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	rows, err := pool.Query(parent.Context(), "SELECT 2")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	parent.Finish()
	rows.Close()

	spans := dbSpans(oneTransaction(t, rec, "GET /v1/unclosed"))
	if len(spans) != 1 || spans[0].Description != "SELECT 1" {
		t.Errorf("db.sql.query spans = %q, want only the finished SELECT 1", spanDescriptions(spans))
	}
	sentrytest.AssertNoLeak(t, rec.Transactions())
}
