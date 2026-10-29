package queue

import (
	"context"
	"encoding/json"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
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
// Only the tenant id is read from EncodedArgs; the rest and Errors stay out (the filter does not scrub raw JSON).
func finalAttemptHub(job *rivertype.JobRow, class, trace string) *sentry.Hub {
	if job.Attempt < job.MaxAttempts {
		return nil
	}
	hub := sentry.CurrentHub()
	if !hub.Client().IsEnabled() {
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
	// River passes the executor's ctx, not the attempt's; metadata is the only link to its trace.
	var meta struct {
		Trace string `json:"sentry_trace"`
	}
	if json.Unmarshal(job.Metadata, &meta) == nil && meta.Trace != "" {
		_ = sentry.ContinueTrace(hub, meta.Trace, "") // sets the scope's trace; the option is unused
	}
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetFingerprint([]string{"river", class, job.Kind})
		scope.SetTag("job_kind", job.Kind)
		if id := tenantOf(job); id != "" {
			scope.SetTag("tenant_id", id)
		}
		scope.SetContext("river", rc)
	})
	return hub
}

// tenantOf returns the job's tenant id, or "" when the args carry none or it is not a UUID.
// The Sentry filter passes a tenant_id tag as is, so the UUID parse is the only guard on the value.
func tenantOf(job *rivertype.JobRow) string {
	var a struct {
		TenantID string `json:"tenant_id"`
	}
	if json.Unmarshal(job.EncodedArgs, &a) != nil {
		return ""
	}
	id, err := uuid.Parse(a.TenantID)
	if err != nil {
		return ""
	}
	return id.String()
}
