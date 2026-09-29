package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	argsTIN = "20746318-0001"
	// An unquoted value survives ScrubText, so it still shows args attached after the scrub.
	argsSeq    = "918273645001"
	panicTrace = "goroutine 9 [running]:\nexample.work()"
)

func jobRow(attempt, maxAttempts int) *rivertype.JobRow {
	return &rivertype.JobRow{
		ID:          7,
		Kind:        "k",
		Queue:       "default",
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
		EncodedArgs: []byte(`{"tenant_id":"` + argsTIN + `","seq":` + argsSeq + `}`),
	}
}

func assertJobEvent(t *testing.T, e *sentry.Event, fingerprint string, attempt, maxAttempts int) {
	t.Helper()
	if want := []string{"river", fingerprint, "k"}; !slices.Equal(e.Fingerprint, want) {
		t.Errorf("fingerprint = %q, want %q", e.Fingerprint, want)
	}
	if got := e.Tags["job_kind"]; got != "k" {
		t.Errorf("tag job_kind = %q, want k", got)
	}
	rc := e.Contexts["river"]
	for key, want := range map[string]string{
		"attempt":      fmt.Sprint(attempt),
		"max_attempts": fmt.Sprint(maxAttempts),
		"queue":        "default",
	} {
		if got := fmt.Sprint(rc[key]); got != want {
			t.Errorf("context river.%s = %s, want %s", key, got, want)
		}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if !strings.Contains(string(raw), `"job_kind"`) {
		t.Fatalf("event JSON lacks the job_kind tag, so the marker checks below prove nothing: %s", raw)
	}
	for _, marker := range []string{argsTIN, argsSeq} {
		if strings.Contains(string(raw), marker) {
			t.Errorf("event JSON holds the job's encoded args (%s): %s", marker, raw)
		}
	}
}

func TestErrorReporter_FinalAttemptOpensOneIssue(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "submission")

	if res := (errorReporter{}).HandleError(context.Background(), jobRow(8, 8), errors.New("adapter: 503")); res != nil {
		t.Errorf("HandleError result = %+v, want nil", res)
	}

	e := rec.One(t, want)
	if !slices.ContainsFunc(e.Exception, func(ex sentry.Exception) bool { return ex.Value == "adapter: 503" }) {
		t.Errorf("exception values = %+v, want one of %q", e.Exception, "adapter: 503")
	}
	assertJobEvent(t, e, "discarded", 8, 8)
}

func TestErrorReporter_RetryingAttemptOpensNothing(t *testing.T) {
	t.Run("retrying attempts", func(t *testing.T) {
		_, rec, want := sentrytest.Boot(t, "submission")
		for _, attempt := range []int{1, 7} {
			if res := (errorReporter{}).HandleError(context.Background(), jobRow(attempt, 8), errors.New("adapter: 503")); res != nil {
				t.Errorf("attempt %d/8: result = %+v, want nil", attempt, res)
			}
			rec.None(t)
		}

		(errorReporter{}).HandleError(context.Background(), jobRow(8, 8), errors.New("adapter: 503"))
		rec.One(t, want)
	})

	t.Run("first attempt is the last", func(t *testing.T) {
		_, rec, want := sentrytest.Boot(t, "submission")
		(errorReporter{}).HandleError(context.Background(), jobRow(1, 1), errors.New("adapter: 503"))
		rec.One(t, want)
	})
}

func TestErrorReporter_FinalPanicOpensOneIssue(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "submission")

	if res := (errorReporter{}).HandlePanic(context.Background(), jobRow(8, 8), "boom", panicTrace); res != nil {
		t.Errorf("HandlePanic result = %+v, want nil", res)
	}

	e := rec.One(t, want)
	if e.Level != sentry.LevelFatal {
		t.Errorf("level = %q, want fatal", e.Level)
	}
	if trace, _ := e.Contexts["river"]["trace"].(string); !strings.Contains(trace, "example.work") {
		t.Errorf("context river.trace = %q, want the River trace", trace)
	}
	assertJobEvent(t, e, "panic", 8, 8)
}

func TestErrorReporter_RetryingPanicOpensNothing(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "submission")
	for _, attempt := range []int{1, 7} {
		if res := (errorReporter{}).HandlePanic(context.Background(), jobRow(attempt, 8), "boom", panicTrace); res != nil {
			t.Errorf("attempt %d/8: result = %+v, want nil", attempt, res)
		}
		rec.None(t)
	}

	(errorReporter{}).HandlePanic(context.Background(), jobRow(8, 8), "boom", panicTrace)
	rec.One(t, want)
}

func TestErrorReporter_SentryOffIsNoop(t *testing.T) {
	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(nil)
	t.Cleanup(func() { hub.BindClient(prev) })

	if res := (errorReporter{}).HandleError(context.Background(), jobRow(8, 8), errors.New("adapter: 503")); res != nil {
		t.Errorf("HandleError result = %+v, want nil", res)
	}
	if res := (errorReporter{}).HandlePanic(context.Background(), jobRow(8, 8), "boom", panicTrace); res != nil {
		t.Errorf("HandlePanic result = %+v, want nil", res)
	}
}
