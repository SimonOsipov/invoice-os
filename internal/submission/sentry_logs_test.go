package submission

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// msgRecorder is a slog.Handler that keeps every message it is given, through With copies too.
type msgRecorder struct {
	mu   *sync.Mutex
	msgs *[]string
}

func newMsgRecorder() msgRecorder { return msgRecorder{mu: &sync.Mutex{}, msgs: &[]string{}} }

func (msgRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h msgRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.msgs = append(*h.msgs, r.Message)
	return nil
}

func (h msgRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h msgRecorder) WithGroup(string) slog.Handler      { return h }

func (h msgRecorder) saw(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(*h.msgs, msg)
}

type sentryLogsArgs struct{}

func (sentryLogsArgs) Kind() string { return "sentry_logs_noop" }

type sentryLogsWorker struct {
	river.WorkerDefaults[sentryLogsArgs]
}

func (sentryLogsWorker) Work(context.Context, *river.Job[sentryLogsArgs]) error { return nil }

// startStopRiver runs one River client on a queue of its own through a start and a stop.
func startStopRiver(t *testing.T, pool *pgxpool.Pool, logger *slog.Logger, waitFor msgRecorder) {
	t.Helper()
	workers := river.NewWorkers()
	river.AddWorker(workers, &sentryLogsWorker{})
	q, err := queue.New(pool, queue.Config{
		Queues:  map[string]river.QueueConfig{"sentry-logs-" + uuid.NewString(): {MaxWorkers: 1}},
		Workers: workers,
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("build queue client: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("start queue client: %v", err)
	}
	// River logs "started" from its own goroutine; give it a moment before the stop.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !waitFor.saw("River client started"); {
		time.Sleep(10 * time.Millisecond)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := q.Stop(ctx); err != nil {
		t.Fatalf("stop queue client: %v", err)
	}
}

func TestQueueLogs_NilLoggerUsesTheDefaultLogger(t *testing.T) {
	pool := vaRequireAppPool(t)
	rec := newMsgRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	startStopRiver(t, pool, nil, rec)

	if !rec.saw("River client started") {
		t.Errorf("default logger never saw %q; River kept its own logger", "River client started")
	}
}

func TestQueueLogs_ExplicitLoggerWins(t *testing.T) {
	pool := vaRequireAppPool(t)
	a, b := newMsgRecorder(), newMsgRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(a))
	t.Cleanup(func() { slog.SetDefault(prev) })

	startStopRiver(t, pool, slog.New(b), b)

	if !b.saw("River client started") {
		t.Error("Config.Logger never saw \"River client started\"")
	}
	if a.saw("River client started") {
		t.Error("the default logger saw \"River client started\" although Config.Logger was set")
	}
}
