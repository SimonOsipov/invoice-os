package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	jtTraceID = "0123456789abcdef0123456789abcdef"
	jtSpanID  = "fedcba9876543210"
)

func jtJob(kind, queue string, attempt, maxAttempts int, metadata string) *rivertype.JobRow {
	return &rivertype.JobRow{
		ID:          11,
		Kind:        kind,
		Queue:       queue,
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
		Metadata:    []byte(metadata),
	}
}

// jtInsert runs the middleware's insert side and returns each job's metadata as doInner saw it.
func jtInsert(t *testing.T, ctx context.Context, metadata ...string) (seen []map[string]any, innerCalls int) {
	t.Helper()
	var params []*rivertype.JobInsertParams
	for _, m := range metadata {
		params = append(params, &rivertype.JobInsertParams{Kind: "k", Metadata: []byte(m)})
	}
	_, err := (&jobTracing{}).InsertMany(ctx, params, func(context.Context) ([]*rivertype.JobInsertResult, error) {
		innerCalls++
		for i, p := range params {
			var m map[string]any
			if err := json.Unmarshal(p.Metadata, &m); err != nil {
				t.Fatalf("job %d metadata %q is not a JSON object: %v", i, p.Metadata, err)
			}
			seen = append(seen, m)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	return seen, innerCalls
}

// jtWire round-trips a transaction through JSON, the shape Sentry receives.
func jtWire(t *testing.T, e *sentry.Event) map[string]any {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return out
}

func jtTrace(t *testing.T, e *sentry.Event) map[string]any {
	t.Helper()
	ctxs, _ := jtWire(t, e)["contexts"].(map[string]any)
	tr, _ := ctxs["trace"].(map[string]any)
	if tr == nil {
		t.Fatalf("transaction %q has no contexts.trace", e.Transaction)
	}
	return tr
}

func jtTransactions(t *testing.T, rec *sentrytest.Recorder, want int) []*sentry.Event {
	t.Helper()
	got := rec.Transactions()
	if len(got) != want {
		var names []string
		for _, e := range got {
			names = append(names, e.Transaction)
		}
		t.Fatalf("recorded %d transactions, want %d: %q", len(got), want, names)
	}
	return got
}

func TestJobTracing_InsertWritesOnlySentryTrace(t *testing.T) {
	sentrytest.Boot(t, "submission")
	tx := sentry.StartTransaction(context.Background(), "POST /v1/probe")
	defer tx.Finish()

	// River hands the middleware `{}` for a job with no metadata of its own.
	seen, calls := jtInsert(t, tx.Context(), `{"k":"v"}`, `{}`)

	if calls != 1 {
		t.Fatalf("doInner called %d times, want 1", calls)
	}
	want := []map[string]any{
		{"k": "v", "sentry_trace": tx.ToSentryTrace()},
		{"sentry_trace": tx.ToSentryTrace()},
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("metadata reaching River = %v, want %v", seen, want)
	}
	for i, m := range seen {
		if _, ok := m["baggage"]; ok {
			t.Errorf("job %d metadata carries baggage: %v", i, m)
		}
	}
}

func TestJobTracing_InsertWithoutASpanChangesNothing(t *testing.T) {
	sentrytest.Boot(t, "submission")

	seen, calls := jtInsert(t, context.Background(), `{"k":"v"}`, `{}`)

	if calls != 1 {
		t.Fatalf("doInner called %d times, want 1", calls)
	}
	want := []map[string]any{{"k": "v"}, {}}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("metadata reaching River = %v, want %v", seen, want)
	}
}

func TestJobTracing_WorkContinuesTheMetadataTrace(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "submission")
	job := jtJob("submission_poll", "q", 2, 8, `{"sentry_trace":"`+jtTraceID+"-"+jtSpanID+`-1"}`)
	var calls int
	var inner *sentry.Span

	err := (&jobTracing{}).Work(context.Background(), job, func(ctx context.Context) error {
		calls++
		inner = sentry.SpanFromContext(ctx)
		return nil
	})

	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if calls != 1 {
		t.Fatalf("doInner called %d times, want 1", calls)
	}
	if inner == nil {
		t.Error("the worker's context holds no span")
	}
	e := jtTransactions(t, rec, 1)[0]
	if e.Transaction != "submission_poll" {
		t.Errorf("transaction name = %q, want the job kind", e.Transaction)
	}
	if e.TransactionInfo == nil || e.TransactionInfo.Source != sentry.SourceTask {
		t.Errorf("transaction source = %+v, want %q", e.TransactionInfo, sentry.SourceTask)
	}
	tr := jtTrace(t, e)
	for key, want := range map[string]any{
		"op":             "queue.process",
		"trace_id":       jtTraceID,
		"parent_span_id": jtSpanID,
		"status":         "ok",
	} {
		if tr[key] != want {
			t.Errorf("contexts.trace.%s = %v, want %v", key, tr[key], want)
		}
	}
	if e.Tags["job_kind"] != "submission_poll" || e.Tags["queue"] != "q" {
		t.Errorf("tags = %v, want job_kind submission_poll and queue q", e.Tags)
	}
	data, _ := tr["data"].(map[string]any)
	if data["river.attempt"] != float64(2) || data["river.max_attempts"] != float64(8) {
		t.Errorf("span data = %v, want river.attempt 2 and river.max_attempts 8", data)
	}
}

func TestJobTracing_MissingOrMalformedTraceStartsANewTrace(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "submission")
	for _, metadata := range []string{
		`{}`,
		`{"sentry_trace":"garbage"}`,
		`{"sentry_trace":"` + jtTraceID + "-" + jtSpanID + `-2"}`,
	} {
		err := (&jobTracing{}).Work(context.Background(), jtJob("submission_poll", "q", 1, 8, metadata),
			func(context.Context) error { return nil })
		if err != nil {
			t.Fatalf("Work with metadata %s: %v", metadata, err)
		}
	}

	got := jtTransactions(t, rec, 3)

	for i, e := range got {
		tr := jtTrace(t, e)
		id, _ := tr["trace_id"].(string)
		if len(id) != 32 || id == jtTraceID {
			t.Errorf("transaction %d trace_id = %q, want a fresh 32-hex id", i, id)
		}
		if parent, ok := tr["parent_span_id"]; ok {
			t.Errorf("transaction %d has parent_span_id %v, want none", i, parent)
		}
	}
}

// MarkerCred has no value pattern for scrubbing to mask, and the raw args carry it unquoted
// (scrubbing redacts quoted text), so a leak of it shows; the key sets pin the rest.
func TestJobTracing_NoArgsOrErrorTextLeaves(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "submission")
	job := jtJob("submission_poll", "q", 1, 8,
		`{"sentry_trace":"`+jtTraceID+"-"+jtSpanID+`-1","x":"`+sentrytest.MarkerCred+`-meta"}`)
	job.EncodedArgs = []byte(`{"buyer_tin":` + sentrytest.MarkerCred + `-args}`)
	inner := fmt.Errorf("adapter refused %s-err", sentrytest.MarkerCred)

	err := (&jobTracing{}).Work(context.Background(), job, func(context.Context) error { return inner })

	if err != inner {
		t.Errorf("Work returned %v, want the worker's own error", err)
	}
	e := jtTransactions(t, rec, 1)[0]
	tr := jtTrace(t, e)
	if tr["status"] != "internal_error" {
		t.Errorf("transaction status = %v, want internal_error", tr["status"])
	}
	data, _ := tr["data"].(map[string]any)
	if got := sortedKeys(data); fmt.Sprint(got) != "[river.attempt river.max_attempts]" {
		t.Errorf("span data keys = %v, want river.attempt and river.max_attempts", got)
	}
	if got := sortedKeys(e.Tags); fmt.Sprint(got) != "[job_kind queue]" {
		t.Errorf("tag keys = %v, want job_kind and queue", got)
	}
	if len(e.Spans) != 0 || e.Request != nil {
		t.Errorf("transaction carries %d spans or request %v, want none", len(e.Spans), e.Request)
	}
	sentrytest.AssertNoLeak(t, rec.Transactions())
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestJobTracing_SnoozeIsOkAndErrorIsInternal(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "submission")
	snooze, failure := river.JobSnooze(time.Minute), errors.New("x")
	var returned []error
	for _, inner := range []error{snooze, failure} {
		returned = append(returned, (&jobTracing{}).Work(context.Background(), jtJob("submission_poll", "q", 1, 8, `{}`),
			func(context.Context) error { return inner }))
	}

	got := jtTransactions(t, rec, 2)

	if returned[0] != snooze || returned[1] != failure {
		t.Errorf("Work returned %v, want the worker's own errors %v and %v", returned, snooze, failure)
	}
	for i, want := range []string{"ok", "internal_error"} {
		if st := jtTrace(t, got[i])["status"]; st != want {
			t.Errorf("transaction %d status = %v, want %s", i, st, want)
		}
	}
}

func TestJobTracing_PanicFinishesAndPropagates(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "submission")
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = (&jobTracing{}).Work(context.Background(), jtJob("submission_poll", "q", 1, 8, `{}`),
			func(context.Context) error { panic("boom") })
	}()

	if recovered != "boom" {
		t.Errorf("recovered %v, want the worker's panic value boom", recovered)
	}
	e := jtTransactions(t, rec, 1)[0]
	if st := jtTrace(t, e)["status"]; st != "internal_error" {
		t.Errorf("transaction status = %v, want internal_error", st)
	}
}

func TestJobTracing_NoClientPassesThrough(t *testing.T) {
	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(nil)
	t.Cleanup(func() { hub.BindClient(prev) })
	t.Setenv("SENTRY_DSN", "")

	seen, inserts := jtInsert(t, context.Background(), `{"k":"v"}`)
	var works int
	var span *sentry.Span
	err := (&jobTracing{}).Work(context.Background(), jtJob("submission_poll", "q", 1, 8, `{}`),
		func(ctx context.Context) error {
			works++
			span = sentry.SpanFromContext(ctx)
			return nil
		})

	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if inserts != 1 || works != 1 {
		t.Fatalf("doInner calls: insert %d, work %d; want 1 each", inserts, works)
	}
	if fmt.Sprint(seen) != fmt.Sprint([]map[string]any{{"k": "v"}}) {
		t.Errorf("metadata reaching River = %v, want only k", seen)
	}
	if span != nil {
		t.Error("the worker's context holds a span although no client is bound")
	}
}
