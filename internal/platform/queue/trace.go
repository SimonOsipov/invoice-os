package queue

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/getsentry/sentry-go"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// sentryTraceKey is the job metadata key that carries the enqueuing span's sentry-trace.
const sentryTraceKey = "sentry_trace"

// jobTracing carries the enqueuing span's trace into a job's metadata and opens one
// transaction per attempt. It reports no issue: errorReporter does.
// ceiling: an unparseable sentry_trace starts a new trace; add a counter if lost links ever matter
type jobTracing struct {
	river.MiddlewareDefaults
}

var (
	_ rivertype.JobInsertMiddleware = &jobTracing{}
	_ rivertype.WorkerMiddleware    = &jobTracing{}
)

func (*jobTracing) InsertMany(ctx context.Context, manyParams []*rivertype.JobInsertParams, doInner func(context.Context) ([]*rivertype.JobInsertResult, error)) ([]*rivertype.JobInsertResult, error) {
	span := sentry.SpanFromContext(ctx)
	if span == nil {
		return doInner(ctx)
	}
	trace := span.ToSentryTrace()
	for _, p := range manyParams {
		meta := map[string]json.RawMessage{}
		if len(p.Metadata) > 0 {
			if err := json.Unmarshal(p.Metadata, &meta); err != nil || meta == nil {
				continue
			}
		}
		raw, err := json.Marshal(trace)
		if err != nil {
			continue
		}
		meta[sentryTraceKey] = raw
		if b, err := json.Marshal(meta); err == nil {
			p.Metadata = b
		}
	}
	return doInner(ctx)
}

// ceiling: one transaction per job attempt; sample job transactions apart above ~1M spans/month on Sentry Stats
func (*jobTracing) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) (err error) {
	hub := sentry.CurrentHub()
	if hub.Client() == nil {
		return doInner(ctx)
	}
	hub = hub.Clone()
	ctx = sentry.SetHubOnContext(ctx, hub)

	var meta struct {
		Trace string `json:"sentry_trace"`
	}
	_ = json.Unmarshal(job.Metadata, &meta)

	span := sentry.StartTransaction(ctx, job.Kind,
		sentry.ContinueTrace(hub, meta.Trace, ""),
		sentry.WithOpName("queue.process"),
		sentry.WithTransactionSource(sentry.SourceTask),
	)
	span.SetTag("job_kind", job.Kind)
	span.SetTag("queue", job.Queue)
	span.SetData("river.attempt", job.Attempt)
	span.SetData("river.max_attempts", job.MaxAttempts)
	// A panic skips the status line below and still finishes the transaction; it is not recovered.
	span.Status = sentry.SpanStatusInternalError
	defer span.Finish()

	err = doInner(span.Context())
	var snooze *rivertype.JobSnoozeError
	if err == nil || errors.As(err, &snooze) {
		span.Status = sentry.SpanStatusOK
	}
	return err
}
