package invoice

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func TestValidatorTrace_PropagatesToValidation(t *testing.T) {
	sentrytest.Boot(t, "invoice")
	body := fmt.Sprintf(`{"rule_set_version":%d,"rule_set_version_id":"x","results":[{"ref":"inv-1","violations":[]}]}`, cannedRuleSetVersion)
	stub := sentrytest.NewHeaderStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	v := NewValidator(stub.URL, "tok", nil)
	items := []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}}

	tx := sentry.StartTransaction(context.Background(), "POST /x")
	if _, err := v.Validate(tx.Context(), items); err != nil {
		t.Fatalf("Validate under a span: %v", err)
	}
	tx.Finish()
	traceID, _ := stub.Only(t, 1).Trace(t)
	if traceID != tx.TraceID.String() {
		t.Errorf("sentry-trace trace id = %s, want the span's %s", traceID, tx.TraceID.String())
	}

	if _, err := v.Validate(context.Background(), items); err != nil {
		t.Fatalf("Validate with no span: %v", err)
	}
	stub.Only(t, 2).AssertUntraced(t)
}
