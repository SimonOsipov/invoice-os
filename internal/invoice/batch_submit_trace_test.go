package invoice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// A batch-submit request's own transaction is the parent the enqueued job's attempt continues.
func TestBatchSubmit_EnqueuedJobCarriesTheRequestTrace(t *testing.T) {
	super, pool := dbTestPools(t)
	app, rec, _ := sentrytest.Boot(t, "invoice")
	q := newInsertOnlyQueueClient(t, pool)
	tenantID := seedTenant(t, super, "batch trace tenant")
	entityID := seedEntity(t, super, tenantID, "batch trace entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BT-1", StatusValidated)
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM river_job WHERE args->>'invoice_id' = $1`, invID)
	})

	caller := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	submitter := NewSubmitter(NewStore(pool), q)
	const route = "POST /v1/invoices/submissions"
	app.Mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithIdentity(r.Context(), caller))
		BatchSubmitHandler(submitter.BatchSubmit, adminRoleStub, nil).ServeHTTP(w, r)
	})

	body := marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{invID}, IdempotencyKey: "bt-" + uuid.NewString()})
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/invoices/submissions", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", route, w.Code, w.Body)
	}

	var carried string
	if err := super.QueryRow(context.Background(),
		`SELECT coalesce(metadata->>'sentry_trace', '') FROM river_job
		  WHERE kind = 'submission_submit' AND args->>'invoice_id' = $1`, invID).Scan(&carried); err != nil {
		t.Fatalf("read the enqueued job's metadata: %v", err)
	}
	txs := rec.Transactions()
	if len(txs) != 1 || txs[0].Transaction != route {
		t.Fatalf("recorded %d transactions, want the one %q", len(txs), route)
	}
	ctxs, _ := txs[0].Contexts["trace"]
	wantTrace, _ := ctxs["trace_id"].(string)
	wantSpan, _ := ctxs["span_id"].(string)
	if wantTrace == "" || wantSpan == "" {
		t.Fatalf("request transaction has no trace context: %v", ctxs)
	}
	if want := wantTrace + "-" + wantSpan + "-1"; carried != want {
		t.Errorf("job metadata sentry_trace = %q, want the request transaction's %q", carried, want)
	}
}
