package extraction_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// The docling call inside a River attempt is an http.client child of the job transaction, and the
// job continues the enqueuing request's trace.
func TestRLS_ExtractJobCallsDoclingUnderTheEnqueuingTrace(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "submission")
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	stub := sentrytest.NewHeaderStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	docling, err := extraction.NewDoclingReader(stub.URL)
	if err != nil {
		t.Fatal(err)
	}

	c := wkClient(t, wkWorkerText(t, wkOK(), wkNewOpener(), docling, &wkAuditRecorder{}), extraction.QueueName)
	req := sentry.StartTransaction(ctx, "POST /v1/documents")
	wkEnqueue(t, req.Context(), c, tenantID, documentID, nil)
	req.Finish()
	wkStart(t, c)

	var job *sentry.Event
	deadline := time.Now().Add(30 * time.Second)
	for job == nil && time.Now().Before(deadline) {
		for _, e := range rec.Transactions() {
			if len(sentrytest.ClientSpans(e)) > 0 {
				job = e
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if job == nil {
		t.Fatalf("no job transaction with an http.client span within 30s; stub saw %d calls", len(stub.Calls()))
	}
	spans := sentrytest.ClientSpans(job)
	if len(spans) == 0 {
		t.Fatal("job transaction has no http.client span")
	}

	calls := stub.Calls()
	if len(calls) == 0 {
		t.Fatal("docling stub was never called")
	}
	traceID, parentID := calls[0].Trace(t)
	if traceID != req.TraceID.String() {
		t.Errorf("docling sentry-trace trace id = %s, want the enqueuing request's %s", traceID, req.TraceID.String())
	}
	if got := sentrytest.TraceID(t, job); got != req.TraceID.String() {
		t.Errorf("job transaction trace id = %s, want the enqueuing request's %s", got, req.TraceID.String())
	}
	if parentID == req.SpanID.String() {
		t.Errorf("docling parent is the enqueuing request's own span %s, want an http.client span in the job", parentID)
	}
	found := false
	for _, s := range spans {
		found = found || s.SpanID.String() == parentID
	}
	if !found {
		t.Errorf("docling sentry-trace parent %s is not one of the job's http.client spans", parentID)
	}
}
