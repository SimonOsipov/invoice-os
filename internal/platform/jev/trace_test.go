package jev

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func TestJevClient_SendsNoTraceHeaders(t *testing.T) {
	sentrytest.Boot(t, "submission")
	seen := &capture{}
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		reply(w, http.StatusOK, noulOK)
	})
	c := realClient(ts, time.Second, time.Millisecond)

	tx := sentry.StartTransaction(context.Background(), "POST /x")
	if _, err := c.Ask(tx.Context(), noulReq("state")); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	tx.Finish()

	sentrytest.Call{Header: seen.only(t).header}.AssertUntraced(t)
}
