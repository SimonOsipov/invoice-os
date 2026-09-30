package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// River gives ErrorHandler the executor's outer context, not the attempt's, so the job row's
// metadata is the only link from a final-attempt issue to the attempt transaction's trace.
func TestJobIssue_CarriesTheAttemptTransactionTraceID(t *testing.T) {
	cases := map[string]func(job *rivertype.JobRow){
		"error": func(job *rivertype.JobRow) {
			boom := errors.New("adapter down")
			_ = (&jobTracing{}).Work(context.Background(), job, func(context.Context) error { return boom })
			errorReporter{}.HandleError(context.Background(), job, boom)
		},
		"panic": func(job *rivertype.JobRow) {
			func() {
				defer func() { _ = recover() }()
				_ = (&jobTracing{}).Work(context.Background(), job, func(context.Context) error { panic("boom") })
			}()
			errorReporter{}.HandlePanic(context.Background(), job, "boom", "trace")
		},
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			_, rec, _ := sentrytest.Boot(t, "submission")
			job := jtJob("submission_poll", "q", 8, 8, `{"sentry_trace":"`+jtTraceID+"-"+jtSpanID+`-1"}`)
			run(job)

			issues, txs := rec.Events(), rec.Transactions()
			if len(issues) != 1 || len(txs) != 1 {
				t.Fatalf("recorded %d issues and %d transactions, want 1 of each", len(issues), len(txs))
			}
			if got := sentrytest.TraceID(t, txs[0]); got != jtTraceID {
				t.Fatalf("attempt transaction trace_id = %s, want the enqueuing %s", got, jtTraceID)
			}
			if got := sentrytest.TraceID(t, issues[0]); got != jtTraceID {
				t.Errorf("issue trace_id = %s, want the attempt transaction's %s", got, jtTraceID)
			}
		})
	}
}
