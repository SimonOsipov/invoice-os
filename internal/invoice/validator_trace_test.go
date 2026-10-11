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
	body := fmt.Sprintf(`{"rule_set_version":%[1]d,"rule_set_version_id":"x","results":[{"ref":"inv-1","violations":[],"rule_set_version":%[1]d,"rule_set_version_id":"x"}]}`, cannedRuleSetVersion)
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
	call := stub.Only(t, 1)
	traceID, _ := call.Trace(t)
	if traceID != tx.TraceID.String() {
		t.Errorf("sentry-trace trace id = %s, want the span's %s", traceID, tx.TraceID.String())
	}
	if call.Header.Get("X-S2S-Token") != "tok" || call.Header.Get("Content-Type") != "application/json" {
		t.Errorf("traced call headers = %v, want the peer token and JSON content type kept", call.Header)
	}
	for _, h := range []string{"X-Tenant-ID", "X-User-ID", "X-User-Role"} {
		if v := call.Header.Values(h); len(v) != 0 {
			t.Errorf("validator sent %s %q, want no identity header", h, v)
		}
	}

	if _, err := v.Validate(context.Background(), items); err != nil {
		t.Fatalf("Validate with no span: %v", err)
	}
	stub.Only(t, 2).AssertUntraced(t)
}

func TestValidatorTrace_InjectedClientIsNotTraced(t *testing.T) {
	sentrytest.Boot(t, "invoice")
	body := fmt.Sprintf(`{"rule_set_version":%[1]d,"rule_set_version_id":"x","results":[{"ref":"inv-1","violations":[],"rule_set_version":%[1]d,"rule_set_version_id":"x"}]}`, cannedRuleSetVersion)
	stub := sentrytest.NewHeaderStub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	v := NewValidator(stub.URL, "tok", &http.Client{})

	tx := sentry.StartTransaction(context.Background(), "POST /x")
	if _, err := v.Validate(tx.Context(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tx.Finish()

	stub.Only(t, 1).AssertUntraced(t)
}
