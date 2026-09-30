package submission_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	jobProbeKind  = "sentry_trace_probe"
	jobFollowKind = "sentry_trace_followup"
)

type jobProbeArgs struct {
	TenantID string `json:"tenant_id"`
}

func (jobProbeArgs) Kind() string     { return jobProbeKind }
func (a jobProbeArgs) Tenant() string { return a.TenantID }

type jobFollowArgs struct {
	TenantID string `json:"tenant_id"`
}

func (jobFollowArgs) Kind() string     { return jobFollowKind }
func (a jobFollowArgs) Tenant() string { return a.TenantID }

// jobProbeWorker runs the test's own function as the probe job's body.
type jobProbeWorker struct {
	river.WorkerDefaults[jobProbeArgs]
	run func(context.Context) error
}

func (w *jobProbeWorker) Work(ctx context.Context, _ *river.Job[jobProbeArgs]) error {
	return w.run(ctx)
}

type jobFollowWorker struct {
	river.WorkerDefaults[jobFollowArgs]
}

func (*jobFollowWorker) Work(context.Context, *river.Job[jobFollowArgs]) error { return nil }

// jobPanicWorker panics in its own Work frame, which River's reported stack must still hold.
type jobPanicWorker struct {
	river.WorkerDefaults[jobProbeArgs]
}

func (*jobPanicWorker) Work(context.Context, *river.Job[jobProbeArgs]) error { panic("boom") }

func jobQueueNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = "sentry-job-trace-" + uuid.NewString()
	}
	return names
}

// jobQueues is a working River client on queues of its own, so no other suite fetches its jobs.
type jobQueues struct {
	client *queue.Client
	names  []string
	events <-chan *river.Event
	seen   []*river.Event
	halted bool
}

// newJobQueues builds the client without starting it and removes its queue and job rows at cleanup.
func newJobQueues(t *testing.T, pool *pgxpool.Pool, names []string, workers *river.Workers) *jobQueues {
	t.Helper()
	cfg := map[string]river.QueueConfig{}
	for _, n := range names {
		cfg[n] = river.QueueConfig{MaxWorkers: 2}
	}
	q, err := queue.New(pool, queue.Config{Queues: cfg, Workers: workers})
	if err != nil {
		t.Fatalf("build queue client: %v", err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{
			"DELETE FROM river_job WHERE queue = ANY($1)",
			"DELETE FROM river_queue WHERE name = ANY($1)",
		} {
			if _, err := pool.Exec(context.Background(), stmt, names); err != nil {
				t.Errorf("%s: %v", stmt, err)
			}
		}
	})
	events, cancel := q.River().Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	t.Cleanup(cancel)
	return &jobQueues{client: q, names: names, events: events}
}

func (q *jobQueues) start(t *testing.T) {
	t.Helper()
	if err := q.client.Start(context.Background()); err != nil {
		t.Fatalf("start queue client: %v", err)
	}
	t.Cleanup(func() { q.stop(t) })
}

// stop drains the client; a second call is a no-op.
func (q *jobQueues) stop(t *testing.T) {
	t.Helper()
	if q.halted {
		return
	}
	q.halted = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := q.client.Stop(ctx); err != nil {
		t.Errorf("stop queue client: %v", err)
	}
}

func (q *jobQueues) insert(t *testing.T, ctx context.Context, args river.JobArgs, maxAttempts int) *rivertype.JobInsertResult {
	t.Helper()
	res, err := q.client.River().Insert(ctx, args, &river.InsertOpts{Queue: q.names[0], MaxAttempts: maxAttempts})
	if err != nil {
		t.Fatalf("insert %s job: %v", args.Kind(), err)
	}
	return res
}

// wait blocks until a job of the kind is reported in the state. River writes the state after the
// middleware and the error handler have returned, so their Sentry output exists by then.
func (q *jobQueues) wait(t *testing.T, kind string, state rivertype.JobState) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		for _, ev := range q.seen {
			if ev.Job.Kind == kind && ev.Job.State == state {
				return
			}
		}
		select {
		case ev := <-q.events:
			q.seen = append(q.seen, ev)
		case <-deadline:
			t.Fatalf("no %s job reached %s in 30s", kind, state)
		}
	}
}

// jobTraceIDs returns the trace, span and parent span ids of a transaction as Sentry receives them.
func jobTraceIDs(t *testing.T, e *sentry.Event) (traceID, spanID, parentID, status string) {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}
	var wire struct {
		Contexts struct {
			Trace map[string]any `json:"trace"`
		} `json:"contexts"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode transaction: %v", err)
	}
	str := func(k string) string { s, _ := wire.Contexts.Trace[k].(string); return s }
	if str("trace_id") == "" {
		t.Fatalf("transaction %q has no contexts.trace.trace_id: %s", e.Transaction, raw)
	}
	return str("trace_id"), str("span_id"), str("parent_span_id"), str("status")
}

// jobTransaction returns the one transaction with the name, failing on none or several.
func jobTransaction(t *testing.T, txs []*sentry.Event, name string) *sentry.Event {
	t.Helper()
	var got []*sentry.Event
	var names []string
	for _, e := range txs {
		names = append(names, e.Transaction)
		if e.Transaction == name {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("recorded %d transactions named %q among %q, want 1", len(got), name, names)
	}
	return got[0]
}

func jobServe(app *platform.App, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

// enqueueInTenantTx inserts args through EnqueueTx in a tenant transaction. invoice_app cannot
// delete idempotency_keys, so each call uses a fresh tenant and key like the other queue suites.
func enqueueInTenantTx(ctx context.Context, q *queue.Client, pool *pgxpool.Pool, queueName string, args interface {
	river.JobArgs
	queue.TenantScoped
}) error {
	return db.WithinTenantTx(ctx, pool, args.Tenant(), func(tx pgx.Tx) error {
		_, err := q.EnqueueTx(ctx, tx, args.Tenant(), uuid.NewString(), args, &river.InsertOpts{Queue: queueName, MaxAttempts: 1})
		return err
	})
}

func TestJobTrace_AttemptContinuesTheRequestTraceAndPollingIsSilent(t *testing.T) {
	base := traceTestPool(t)
	names := jobQueueNames(2)
	cfg := base.Config()
	counter := &pollCounter{inner: cfg.ConnConfig.Tracer, polls: map[string]int{names[0]: 0, names[1]: 0}}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open counting pool: %v", err)
	}
	t.Cleanup(pool.Close)

	app, rec, _ := sentrytest.Boot(t, "submission")
	workers := river.NewWorkers()
	river.AddWorker(workers, &jobProbeWorker{run: func(ctx context.Context) error {
		_, err := pool.Exec(ctx, "SELECT 1")
		return err
	}})
	jq := newJobQueues(t, pool, names, workers)
	jq.start(t)
	app.Mux.HandleFunc("POST /v1/probe", func(w http.ResponseWriter, r *http.Request) {
		args := jobProbeArgs{TenantID: uuid.NewString()}
		if err := enqueueInTenantTx(r.Context(), jq.client, pool, names[0], args); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	if w := jobServe(app, http.MethodPost, "/v1/probe"); w.Code != http.StatusOK {
		t.Fatalf("POST /v1/probe = %d: %s", w.Code, w.Body)
	}
	jq.wait(t, jobProbeKind, rivertype.JobStateCompleted)
	// Each queue must be fetched three times, so the silence below is measured against real polling.
	for deadline := time.Now().Add(30 * time.Second); counter.min() < 3; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the slowest queue was polled %d times in 30s, want at least 3", counter.min())
		}
	}
	jq.stop(t)

	txs := rec.Transactions()
	if len(txs) != 2 {
		var got []string
		for _, e := range txs {
			got = append(got, e.Transaction)
		}
		t.Fatalf("recorded %d transactions %q, want the request and the job", len(txs), got)
	}
	req := jobTransaction(t, txs, "POST /v1/probe")
	job := jobTransaction(t, txs, jobProbeKind)
	reqTrace, reqSpan, _, _ := jobTraceIDs(t, req)
	jobTrace, _, jobParent, _ := jobTraceIDs(t, job)
	if jobTrace != reqTrace {
		t.Errorf("job trace id = %s, want the request's %s", jobTrace, reqTrace)
	}
	if jobParent != reqSpan {
		t.Errorf("job parent span id = %q, want the request transaction's span %s", jobParent, reqSpan)
	}
	if len(job.Spans) != 1 || job.Spans[0].Op != "db.sql.query" || job.Spans[0].Description != "SELECT 1" {
		t.Errorf("job spans = %v, want only the worker's db.sql.query SELECT 1", spanDescriptions(job.Spans))
	}
}

func TestJobTrace_FollowUpJobContinuesTheTrace(t *testing.T) {
	pool := traceTestPool(t)
	names := jobQueueNames(1)
	_, rec, _ := sentrytest.Boot(t, "submission")
	var jq *jobQueues
	workers := river.NewWorkers()
	river.AddWorker(workers, &jobProbeWorker{run: func(ctx context.Context) error {
		return enqueueInTenantTx(ctx, jq.client, pool, names[0], jobFollowArgs{TenantID: uuid.NewString()})
	}})
	river.AddWorker(workers, &jobFollowWorker{})
	jq = newJobQueues(t, pool, names, workers)
	jq.start(t)

	jq.insert(t, context.Background(), jobProbeArgs{TenantID: uuid.NewString()}, 1)
	jq.wait(t, jobProbeKind, rivertype.JobStateCompleted)
	jq.wait(t, jobFollowKind, rivertype.JobStateCompleted)

	txs := rec.Transactions()
	if len(txs) != 2 {
		t.Fatalf("recorded %d transactions, want the job and its follow-up", len(txs))
	}
	first := jobTransaction(t, txs, jobProbeKind)
	second := jobTransaction(t, txs, jobFollowKind)
	firstTrace, firstSpan, _, _ := jobTraceIDs(t, first)
	secondTrace, _, secondParent, _ := jobTraceIDs(t, second)
	if secondTrace != firstTrace {
		t.Errorf("follow-up trace id = %s, want the first job's %s", secondTrace, firstTrace)
	}
	if secondParent != firstSpan {
		t.Errorf("follow-up parent span id = %q, want the first job's transaction span %s", secondParent, firstSpan)
	}
}

func TestJobTrace_UntracedInsertIsItsOwnTrace(t *testing.T) {
	pool := traceTestPool(t)
	names := jobQueueNames(1)
	app, rec, _ := sentrytest.Boot(t, "submission")
	app.Mux.HandleFunc("GET /v1/ping", func(http.ResponseWriter, *http.Request) {})
	workers := river.NewWorkers()
	river.AddWorker(workers, &jobProbeWorker{run: func(context.Context) error { return nil }})
	jq := newJobQueues(t, pool, names, workers)
	jq.start(t)

	if w := jobServe(app, http.MethodGet, "/v1/ping"); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/ping = %d: %s", w.Code, w.Body)
	}
	res := jq.insert(t, context.Background(), jobProbeArgs{TenantID: uuid.NewString()}, 1)
	jq.wait(t, jobProbeKind, rivertype.JobStateCompleted)

	if bytes.Contains(res.Job.Metadata, []byte("sentry_trace")) {
		t.Errorf("an insert with no span wrote trace metadata: %s", res.Job.Metadata)
	}
	txs := rec.Transactions()
	if len(txs) != 2 {
		t.Fatalf("recorded %d transactions, want the request and the job", len(txs))
	}
	reqTrace, _, _, _ := jobTraceIDs(t, jobTransaction(t, txs, "GET /v1/ping"))
	jobTrace, _, jobParent, _ := jobTraceIDs(t, jobTransaction(t, txs, jobProbeKind))
	if jobTrace == reqTrace {
		t.Errorf("an untraced job joined the request's trace %s", reqTrace)
	}
	if jobParent != "" {
		t.Errorf("an untraced job has parent span %q, want none", jobParent)
	}
}

func TestJobTrace_FinalFailureIsOneIssueAndOneTransaction(t *testing.T) {
	pool := traceTestPool(t)
	names := jobQueueNames(1)
	_, rec, want := sentrytest.Boot(t, "submission")
	workers := river.NewWorkers()
	river.AddWorker(workers, &jobProbeWorker{run: func(context.Context) error { return errors.New("probe: always fail") }})
	jq := newJobQueues(t, pool, names, workers)
	jq.start(t)

	jq.insert(t, context.Background(), jobProbeArgs{TenantID: uuid.NewString()}, 1)
	jq.wait(t, jobProbeKind, rivertype.JobStateDiscarded)

	issue := rec.One(t, want)
	if fp := []string{"river", "discarded", jobProbeKind}; !slices.Equal(issue.Fingerprint, fp) {
		t.Errorf("issue fingerprint = %q, want %q", issue.Fingerprint, fp)
	}
	txs := rec.Transactions()
	if len(txs) != 1 {
		t.Fatalf("recorded %d transactions, want the one job attempt", len(txs))
	}
	jobTrace, _, _, status := jobTraceIDs(t, jobTransaction(t, txs, jobProbeKind))
	if status != "internal_error" {
		t.Errorf("job transaction status = %q, want internal_error", status)
	}
	if issueTrace, _ := issue.Contexts["trace"]["trace_id"].(string); issueTrace == jobTrace {
		t.Errorf("the issue shares the job transaction's trace %s; it must keep its own", jobTrace)
	}
}

func TestJobTrace_PanicKeepsRiverStackAndOneIssue(t *testing.T) {
	pool := traceTestPool(t)
	names := jobQueueNames(1)
	_, rec, want := sentrytest.Boot(t, "submission")
	workers := river.NewWorkers()
	river.AddWorker(workers, &jobPanicWorker{})
	jq := newJobQueues(t, pool, names, workers)
	jq.start(t)

	jq.insert(t, context.Background(), jobProbeArgs{TenantID: uuid.NewString()}, 1)
	jq.wait(t, jobProbeKind, rivertype.JobStateDiscarded)

	issue := rec.One(t, want)
	if fp := []string{"river", "panic", jobProbeKind}; !slices.Equal(issue.Fingerprint, fp) {
		t.Errorf("issue fingerprint = %q, want %q", issue.Fingerprint, fp)
	}
	if stack, _ := issue.Contexts["river"]["trace"].(string); !strings.Contains(stack, "(*jobPanicWorker).Work") {
		t.Errorf("context river.trace lacks the worker's Work frame: %q", stack)
	}
	txs := rec.Transactions()
	if len(txs) != 1 {
		t.Fatalf("recorded %d transactions, want the one panicking attempt", len(txs))
	}
	if _, _, _, status := jobTraceIDs(t, jobTransaction(t, txs, jobProbeKind)); status != "internal_error" {
		t.Errorf("job transaction status = %q, want internal_error", status)
	}
}
