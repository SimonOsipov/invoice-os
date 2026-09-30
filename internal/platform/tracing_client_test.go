package platform_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func getVia(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: platform.TraceTransport(nil)}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	resp.Body.Close()
}

func TestTraceTransport_PropagatesUnderASpan(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	stub := sentrytest.NewHeaderStub(t, nil)
	tx := sentry.StartTransaction(context.Background(), "GET /x")

	getVia(t, tx.Context(), stub.URL+"/x")
	tx.Finish()

	traceID, parentID := stub.Only(t, 1).Trace(t)
	if traceID != tx.TraceID.String() {
		t.Errorf("sentry-trace trace id = %s, want the transaction's %s", traceID, tx.TraceID.String())
	}
	txs := rec.Transactions()
	if len(txs) != 1 {
		t.Fatalf("sent %d transactions, want 1", len(txs))
	}
	spans := sentrytest.ClientSpans(txs[0])
	if len(spans) != 1 {
		t.Fatalf("transaction has %d http.client spans, want 1", len(spans))
	}
	if got := spans[0].SpanID.String(); parentID != got {
		t.Errorf("sentry-trace parent = %s, want the http.client span %s", parentID, got)
	}
}

func TestTraceTransport_NoSpanNoHeader(t *testing.T) {
	_, _, _ = sentrytest.Boot(t, "invoice")
	stub := sentrytest.NewHeaderStub(t, nil)

	getVia(t, context.Background(), stub.URL+"/x")
	stub.Only(t, 1).AssertUntraced(t)

	// Positive control on the same stub: a span in the context is propagated.
	tx := sentry.StartTransaction(context.Background(), "GET /x")
	getVia(t, tx.Context(), stub.URL+"/x")
	tx.Finish()
	if traceID, _ := stub.Only(t, 2).Trace(t); traceID != tx.TraceID.String() {
		t.Errorf("traced call: trace id = %s, want %s", traceID, tx.TraceID.String())
	}
}
