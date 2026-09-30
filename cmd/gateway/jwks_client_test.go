package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func TestNewJWKSClient_TracesAndTimesOut(t *testing.T) {
	sentrytest.Boot(t, "gateway")
	stub := sentrytest.NewHeaderStub(t, nil)
	c := newJWKSClient()
	if c.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", c.Timeout)
	}
	get := func(ctx context.Context) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, stub.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
	}

	tx := sentry.StartTransaction(context.Background(), "GET /x")
	get(tx.Context())
	tx.Finish()
	traceID, _ := stub.Only(t, 1).Trace(t)
	if traceID != tx.TraceID.String() {
		t.Errorf("sentry-trace trace id = %s, want the span's %s", traceID, tx.TraceID.String())
	}

	get(context.Background())
	stub.Only(t, 2).AssertUntraced(t)
}
