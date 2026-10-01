package ai

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func TestAIClient_SendsNoTraceHeaders(t *testing.T) {
	sentrytest.Boot(t, "submission")
	stub := sentrytest.NewHeaderStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(validContent, "")))
	})
	c := realClient(t, stub.URL, time.Second)

	tx := sentry.StartTransaction(context.Background(), "POST /x")
	if _, err := c.Call(tx.Context(), baseReq()); err != nil {
		t.Fatalf("Call: %v", err)
	}
	tx.Finish()

	stub.Only(t, 1).AssertUntraced(t)
}
