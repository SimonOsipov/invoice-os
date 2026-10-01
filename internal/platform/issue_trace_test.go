package platform_test

import (
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const failPattern = "GET /v1/fail"

var failures = map[string]http.HandlerFunc{
	"5xx response": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
	"recovered panic": func(http.ResponseWriter, *http.Request) {
		panic("boom")
	},
	"CaptureError": func(w http.ResponseWriter, r *http.Request) {
		platform.CaptureError(r.Context(), http.ErrHandlerTimeout)
		w.WriteHeader(http.StatusOK)
	},
}

// issueTraces returns each issue's and each transaction's trace id, in send order.
func issueTraces(t *testing.T, rec *sentrytest.Recorder, want int) (issues, txs []string) {
	t.Helper()
	ie, te := rec.Events(), rec.Transactions()
	if len(ie) != want || len(te) != want {
		t.Fatalf("recorded %d issues and %d transactions, want %d of each", len(ie), len(te), want)
	}
	for _, e := range ie {
		issues = append(issues, sentrytest.TraceID(t, e))
	}
	for _, e := range te {
		txs = append(txs, sentrytest.TraceID(t, e))
	}
	return issues, txs
}

func TestIssue_CarriesTheRequestTransactionTraceID(t *testing.T) {
	if len(failures) == 0 {
		t.Fatal("no failure cases")
	}
	for name, h := range failures {
		t.Run(name, func(t *testing.T) {
			app, rec, _ := sentrytest.Boot(t, "svc")
			app.Mux.HandleFunc(failPattern, h)
			serve(app.Handler(), http.MethodGet, "/v1/fail")

			issues, txs := issueTraces(t, rec, 1)
			if issues[0] != txs[0] {
				t.Errorf("issue trace_id = %s, want the request transaction's %s", issues[0], txs[0])
			}
		})
	}
}

func TestIssue_TwoRequestsDoNotShareATraceID(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc(failPattern, failures["5xx response"])
	h := app.Handler()
	serve(h, http.MethodGet, "/v1/fail")
	serve(h, http.MethodGet, "/v1/fail")

	issues, txs := issueTraces(t, rec, 2)
	if txs[0] == txs[1] {
		t.Fatalf("both transactions share trace_id %s; the control needs two traces", txs[0])
	}
	if issues[0] == issues[1] {
		t.Errorf("both issues share trace_id %s, want one per request", issues[0])
	}
	for i := range issues {
		if issues[i] != txs[i] {
			t.Errorf("issue %d trace_id = %s, want its transaction's %s", i, issues[i], txs[i])
		}
	}
}
