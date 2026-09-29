package queue

import (
	"context"

	"github.com/getsentry/sentry-go"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// errorReporter is River's ErrorHandler: it reports the attempt that ends a job.
// It returns nil, so River's retry schedule and final state are unchanged.
// ceiling: one event per discarded job; rate-cap if an adapter outage ever burns the quota
// ceiling: rescuer discards skip ErrorHandler; add a discarded-job check if a stuck job ever goes unseen
type errorReporter struct{}

var _ river.ErrorHandler = errorReporter{}

func (errorReporter) HandleError(_ context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	if hub := finalAttemptHub(job, "discarded", ""); hub != nil {
		hub.CaptureException(err)
	}
	return nil
}

func (errorReporter) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicVal any, trace string) *river.ErrorHandlerResult {
	if hub := finalAttemptHub(job, "panic", trace); hub != nil {
		hub.RecoverWithContext(ctx, panicVal)
	}
	return nil
}

// finalAttemptHub returns a scoped hub when this attempt ends the job and Sentry is on, else nil.
// EncodedArgs and Errors stay out: the filter does not scrub raw JSON.
func finalAttemptHub(job *rivertype.JobRow, class, trace string) *sentry.Hub {
	if job.Attempt < job.MaxAttempts {
		return nil
	}
	hub := sentry.CurrentHub()
	if hub.Client() == nil {
		return nil
	}
	hub = hub.Clone()
	rc := sentry.Context{
		"attempt":      job.Attempt,
		"max_attempts": job.MaxAttempts,
		"queue":        job.Queue,
	}
	if trace != "" {
		rc["trace"] = trace
	}
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetFingerprint([]string{"river", class, job.Kind})
		scope.SetTag("job_kind", job.Kind)
		scope.SetContext("river", rc)
	})
	return hub
}
