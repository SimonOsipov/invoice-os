package extraction

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func TestDoclingTrace_ReadPropagatesUnderASpan(t *testing.T) {
	sentrytest.Boot(t, "submission")
	stub := sentrytest.NewHeaderStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	r, err := NewDoclingReader(stub.URL)
	if err != nil {
		t.Fatal(err)
	}
	doc := Document{Bytes: []byte("%PDF-1.4"), ContentType: "application/pdf"}
	onPage := func(Page) error { return nil }

	tx := sentry.StartTransaction(context.Background(), "extract")
	if _, err := r.Read(tx.Context(), doc, onPage); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("Read under a span: err = %v, want the sidecar's 500", err)
	}
	tx.Finish()
	traceID, _ := stub.Only(t, 1).Trace(t)
	if traceID != tx.TraceID.String() {
		t.Errorf("sentry-trace trace id = %s, want the span's %s", traceID, tx.TraceID.String())
	}

	if _, err := r.Read(context.Background(), doc, onPage); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("Read with no span: err = %v, want the sidecar's 500", err)
	}
	stub.Only(t, 2).AssertUntraced(t)
}
