package submission_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

type sentryFailArgs struct{}

func (sentryFailArgs) Kind() string { return "sentry_test_fail" }

type sentryFailWorker struct {
	river.WorkerDefaults[sentryFailArgs]
}

func (sentryFailWorker) Work(context.Context, *river.Job[sentryFailArgs]) error {
	return errors.New("sentry test: always fail")
}

type sentryPanicArgs struct{}

func (sentryPanicArgs) Kind() string { return "sentry_test_panic" }

type sentryPanicWorker struct {
	river.WorkerDefaults[sentryPanicArgs]
}

func (sentryPanicWorker) Work(context.Context, *river.Job[sentryPanicArgs]) error {
	panic("sentry test: always panic")
}

type sentryCancelArgs struct{}

func (sentryCancelArgs) Kind() string { return "sentry_test_cancel" }

type sentryCancelWorker struct {
	river.WorkerDefaults[sentryCancelArgs]
}

func (sentryCancelWorker) Work(context.Context, *river.Job[sentryCancelArgs]) error {
	return river.JobCancel(errors.New("refused"))
}

type sentryFlakyArgs struct{}

func (sentryFlakyArgs) Kind() string { return "sentry_test_flaky" }

type sentryFlakyWorker struct {
	river.WorkerDefaults[sentryFlakyArgs]
}

func (sentryFlakyWorker) Work(_ context.Context, job *river.Job[sentryFlakyArgs]) error {
	if job.Attempt == 1 {
		return errors.New("sentry test: first attempt fails")
	}
	return nil
}

// sentryQueue runs the test workers on a queue of its own, so residue from other
// suites is never fetched and no other client fetches these jobs.
type sentryQueue struct {
	client *queue.Client
	name   string
	events <-chan *river.Event
}

func startSentryQueue(t *testing.T, pool *pgxpool.Pool) *sentryQueue {
	t.Helper()
	workers := river.NewWorkers()
	river.AddWorker(workers, &sentryFailWorker{})
	river.AddWorker(workers, &sentryPanicWorker{})
	river.AddWorker(workers, &sentryCancelWorker{})
	river.AddWorker(workers, &sentryFlakyWorker{})
	name := "sentry-" + uuid.NewString()
	q, err := queue.New(pool, queue.Config{
		Queues:      map[string]river.QueueConfig{name: {MaxWorkers: 2}},
		Workers:     workers,
		RetryPolicy: immediateRetry{},
	})
	if err != nil {
		t.Fatalf("build queue client: %v", err)
	}
	events, cancel := q.River().Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	t.Cleanup(cancel)
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("start worker pool: %v", err)
	}
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := q.Stop(ctx); err != nil {
			t.Errorf("stop worker pool: %v", err)
		}
	})
	return &sentryQueue{client: q, name: name, events: events}
}

// run inserts one job and waits until River reports it in state want. River
// calls ErrorHandler before it writes the state, so the capture has happened by then.
func (s *sentryQueue) run(t *testing.T, args river.JobArgs, maxAttempts int, want rivertype.JobState) *rivertype.JobRow {
	t.Helper()
	res, err := s.client.River().Insert(context.Background(), args, &river.InsertOpts{Queue: s.name, MaxAttempts: maxAttempts})
	if err != nil {
		t.Fatalf("insert %s job: %v", args.Kind(), err)
	}
	timeout := time.After(30 * time.Second)
	for {
		select {
		case ev := <-s.events:
			if ev.Job.ID == res.Job.ID && ev.Job.State == want {
				return ev.Job
			}
		case <-timeout:
			t.Fatalf("%s job %d did not reach %s in 30s", args.Kind(), res.Job.ID, want)
		}
	}
}

func eventsOfKind(rec *sentrytest.Recorder, kind string) []*sentry.Event {
	var out []*sentry.Event
	for _, e := range rec.Events() {
		if e.Tags["job_kind"] == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestQueueSentry_DiscardedJobOpensOneIssue(t *testing.T) {
	pool := requireDB(t)
	defer pool.Close()
	_, rec, want := sentrytest.Boot(t, "submission")
	q := startSentryQueue(t, pool)

	q.run(t, sentryFailArgs{}, 2, rivertype.JobStateDiscarded)

	got := eventsOfKind(rec, sentryFailArgs{}.Kind())
	if len(got) != 1 {
		t.Fatalf("events tagged job_kind=%s = %d, want 1", sentryFailArgs{}.Kind(), len(got))
	}
	sentrytest.AssertLabels(t, got[0], want)
	if fp := []string{"river", "discarded", sentryFailArgs{}.Kind()}; !slices.Equal(got[0].Fingerprint, fp) {
		t.Errorf("fingerprint = %q, want %q", got[0].Fingerprint, fp)
	}
}

func TestQueueSentry_PanickingJobOpensOneIssue(t *testing.T) {
	pool := requireDB(t)
	defer pool.Close()
	_, rec, want := sentrytest.Boot(t, "submission")
	q := startSentryQueue(t, pool)

	q.run(t, sentryPanicArgs{}, 2, rivertype.JobStateDiscarded)

	got := eventsOfKind(rec, sentryPanicArgs{}.Kind())
	if len(got) != 1 {
		t.Fatalf("events tagged job_kind=%s = %d, want 1 (a panic on every attempt opens one issue)", sentryPanicArgs{}.Kind(), len(got))
	}
	e := got[0]
	sentrytest.AssertLabels(t, e, want)
	if fp := []string{"river", "panic", sentryPanicArgs{}.Kind()}; !slices.Equal(e.Fingerprint, fp) {
		t.Errorf("fingerprint = %q, want %q", e.Fingerprint, fp)
	}
	if got := fmt.Sprint(e.Contexts["river"]["attempt"]); got != "2" {
		t.Errorf("context river.attempt = %s, want 2", got)
	}
}

func TestQueueSentry_CancelledJobOpensNothing(t *testing.T) {
	pool := requireDB(t)
	defer pool.Close()
	_, rec, want := sentrytest.Boot(t, "submission")
	q := startSentryQueue(t, pool)

	q.run(t, sentryCancelArgs{}, 3, rivertype.JobStateCancelled)
	if n := len(eventsOfKind(rec, sentryCancelArgs{}.Kind())); n != 0 {
		t.Errorf("events tagged job_kind=%s = %d, want 0", sentryCancelArgs{}.Kind(), n)
	}

	q.run(t, sentryFailArgs{}, 1, rivertype.JobStateDiscarded)
	if n := len(eventsOfKind(rec, sentryFailArgs{}.Kind())); n != 1 {
		t.Fatalf("positive control: events tagged job_kind=%s = %d, want 1", sentryFailArgs{}.Kind(), n)
	}
	rec.One(t, want)
}

func TestQueueSentry_RetriedThenSucceededOpensNothing(t *testing.T) {
	pool := requireDB(t)
	defer pool.Close()
	_, rec, want := sentrytest.Boot(t, "submission")
	q := startSentryQueue(t, pool)

	job := q.run(t, sentryFlakyArgs{}, 3, rivertype.JobStateCompleted)
	if job.Attempt != 2 || len(job.Errors) != 1 {
		t.Fatalf("flaky job completed on attempt %d with %d errors, want attempt 2 after one error", job.Attempt, len(job.Errors))
	}
	if n := len(eventsOfKind(rec, sentryFlakyArgs{}.Kind())); n != 0 {
		t.Errorf("events tagged job_kind=%s = %d, want 0", sentryFlakyArgs{}.Kind(), n)
	}

	q.run(t, sentryFailArgs{}, 1, rivertype.JobStateDiscarded)
	if n := len(eventsOfKind(rec, sentryFailArgs{}.Kind())); n != 1 {
		t.Fatalf("positive control: events tagged job_kind=%s = %d, want 1", sentryFailArgs{}.Kind(), n)
	}
	rec.One(t, want)
}
