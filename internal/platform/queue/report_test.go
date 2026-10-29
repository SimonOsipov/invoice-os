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
	errorsSeq  = "918273645002"
	tenantUUID = "3f2c1a9e-0b7d-4c55-9a1e-6d2f8b7a4c10"
	panicTrace = "goroutine 9 [running]:\nexample.work()"
)

func jobRow(attempt, maxAttempts int) *rivertype.JobRow {
	row := jobRowArgs(attempt, maxAttempts, nil)
	row.EncodedArgs = []byte(`{"tenant_id":"` + tenantUUID + `","buyer_tin":"` + argsTIN + `","seq":` + argsSeq + `}`)
	return row
}

func jobRowArgs(attempt, maxAttempts int, args []byte) *rivertype.JobRow {
	return &rivertype.JobRow{
		ID:          7,
		Kind:        "k",
		Queue:       "default",
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
		EncodedArgs: args,
		Errors:      []rivertype.AttemptError{{Attempt: 1, Error: "adapter: 503 for seq " + errorsSeq}},
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
	for _, marker := range []string{argsTIN, argsSeq, errorsSeq} {
		if strings.Contains(string(raw), marker) {
			t.Errorf("event JSON holds the job's encoded args or earlier errors (%s): %s", marker, raw)
		}
	}
	if got := e.Tags["tenant_id"]; got != tenantUUID {
		t.Errorf("tag tenant_id = %q, want %q", got, tenantUUID)
	}
	untagged := *e
	untagged.Tags = nil
	rest, err := json.Marshal(&untagged)
	if err != nil {
		t.Fatalf("marshal event without tags: %v", err)
	}
	if strings.Contains(string(rest), tenantUUID) {
		t.Errorf("tenant id appears outside Tags: %s", rest)
	}
}

// finalEvent reports a final-attempt error for a job with the given args and returns the one event.
func finalEvent(t *testing.T, args []byte) *sentry.Event {
	t.Helper()
	_, rec, want := sentrytest.Boot(t, "submission")
	(errorReporter{}).HandleError(context.Background(), jobRowArgs(8, 8, args), errors.New("adapter: 503"))
	return rec.One(t, want)
}

func TestErrorReporter_FinalAttemptTagsTheJobsTenant(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		e := finalEvent(t, []byte(`{"tenant_id":"`+tenantUUID+`"}`))
		if e.Tags["tenant_id"] != tenantUUID || e.Tags["job_kind"] != "k" {
			t.Errorf("tags = %v, want tenant_id %s and job_kind k", e.Tags, tenantUUID)
		}
	})
	t.Run("panic", func(t *testing.T) {
		_, rec, want := sentrytest.Boot(t, "submission")
		(errorReporter{}).HandlePanic(context.Background(), jobRowArgs(8, 8, []byte(`{"tenant_id":"`+tenantUUID+`"}`)), "boom", panicTrace)
		e := rec.One(t, want)
		if e.Tags["tenant_id"] != tenantUUID || e.Tags["job_kind"] != "k" {
			t.Errorf("tags = %v, want tenant_id %s and job_kind k", e.Tags, tenantUUID)
		}
	})
}

func TestErrorReporter_TenantTagIsCanonical(t *testing.T) {
	e := finalEvent(t, []byte(`{"tenant_id":"`+strings.ToUpper(tenantUUID)+`"}`))
	if got := e.Tags["tenant_id"]; got != tenantUUID {
		t.Errorf("tag tenant_id = %q, want %q", got, tenantUUID)
	}
}

func TestErrorReporter_NoTenantMeansNoTenantTag(t *testing.T) {
	for name, args := range map[string][]byte{
		"empty object": []byte(`{}`),
		"other field":  []byte(`{"invoice_id":"x"}`),
		"empty string": []byte(`{"tenant_id":""}`),
		"non-string":   []byte(`{"tenant_id":7}`),
		"nil args":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			e := finalEvent(t, args)
			if _, ok := e.Tags["tenant_id"]; ok {
				t.Errorf("tags = %v, want no tenant_id", e.Tags)
			}
			if e.Tags["job_kind"] != "k" {
				t.Errorf("tag job_kind = %q, want k", e.Tags["job_kind"])
			}
		})
	}
}

func TestErrorReporter_NonUUIDTenantIsNotTagged(t *testing.T) {
	for name, args := range map[string][]byte{
		"tin-shaped": []byte(`{"tenant_id":"` + argsTIN + `"}`),
		"free text":  []byte(`{"tenant_id":"not-a-uuid"}`),
		"bad json":   []byte(`{bad json`),
	} {
		t.Run(name, func(t *testing.T) {
			e := finalEvent(t, args)
			if _, ok := e.Tags["tenant_id"]; ok {
				t.Errorf("tags = %v, want no tenant_id", e.Tags)
			}
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			for _, leak := range []string{argsTIN, "not-a-uuid"} {
				if strings.Contains(string(raw), leak) {
					t.Errorf("event JSON holds the args value %q: %s", leak, raw)
				}
			}
		})
	}
}

// uuid.Parse accepts braced, urn and unhyphenated forms; the tag stays canonical. Nested keys are not the job's tenant; the last duplicate wins (encoding/json).
func TestErrorReporter_TenantTagFormsAndPlacement(t *testing.T) {
	u := tenantUUID
	for name, tc := range map[string]struct{ args, want string }{
		"braced":        {`{"tenant_id":"{` + strings.ToUpper(u) + `}"}`, u},
		"urn":           {`{"tenant_id":"urn:uuid:` + u + `"}`, u},
		"unhyphenated":  {`{"tenant_id":"` + strings.ReplaceAll(u, "-", "") + `"}`, u},
		"duplicate key": {`{"tenant_id":"` + argsTIN + `","tenant_id":"` + u + `"}`, u},
		"nested only":   {`{"inner":{"tenant_id":"` + u + `"}}`, ""},
		"padded":        {`{"tenant_id":" ` + u + `"}`, ""},
		"null":          {`{"tenant_id":null}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := finalEvent(t, []byte(tc.args))
			got, ok := e.Tags["tenant_id"]
			if tc.want == "" {
				if ok {
					t.Errorf("tags = %v, want no tenant_id", e.Tags)
				}
			} else if got != tc.want {
				t.Errorf("tag tenant_id = %q, want %q", got, tc.want)
			}
			if e.Tags["job_kind"] != "k" {
				t.Errorf("tag job_kind = %q, want k", e.Tags["job_kind"])
			}
		})
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

	// River discards at Attempt >= MaxAttempts; a rescued job or a lowered max lands past it.
	t.Run("attempt past the max counts", func(t *testing.T) {
		_, rec, want := sentrytest.Boot(t, "submission")
		(errorReporter{}).HandleError(context.Background(), jobRow(9, 8), errors.New("adapter: 503"))
		assertJobEvent(t, rec.One(t, want), "discarded", 9, 8)
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

func TestFinalAttemptHub_NilWhenOffAndSetWhenOn(t *testing.T) {
	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(nil)
	t.Cleanup(func() { hub.BindClient(prev) })
	if h := finalAttemptHub(jobRow(8, 8), "error", ""); h != nil {
		t.Error("finalAttemptHub returned a hub while Sentry is off, want nil")
	}

	sentrytest.Boot(t, "submission")
	if h := finalAttemptHub(jobRow(8, 8), "error", ""); h == nil {
		t.Error("finalAttemptHub returned nil with an enabled client, want a hub")
	}
	if h := finalAttemptHub(jobRow(7, 8), "error", ""); h != nil {
		t.Error("finalAttemptHub returned a hub for a retrying attempt, want nil")
	}
}
