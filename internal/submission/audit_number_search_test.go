// audit_number_search_test.go: AUDIT-11-05's rule-B half. audit_log.invoice_id is filled
// from payload->>'id' for the ten rule-A events and from payload->>'invoice_id' for the
// seven rule-B ones, and the reader's resolved arm compares that column -- so the payload
// spelling is the one structural fork in how a real writer's row reaches the search.
// internal/invoice covers rule A; this file covers rule B.
//
// Event-type floor is TWO, one per spelling. AUDIT-11-09 already proves all 17 invoice-scoped
// events reach the resolved arm, so driving every one through a real worker would add nothing.
//
// CF-30: the reader never reads the payload key, so the row comes back with AUDIT-11-03
// reverted. The assertion that the RETURNED payload carries invoice_number is the one that
// makes the writer load-bearing.
//
// Run: export the four DSNs and `go test ./internal/submission/ -p 1` (or `make test-queue`).
// A bare run with only DEV_DB_PORT set skips every case here and still prints ok (CF-6).
package submission_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// anSearchDriveAccepted is anDriveSubmitAccepted with the River job id exposed.
// queue.OncePerJob keys on that id and it is constant across a job's retries, so two drives
// in one tenant need distinct ids or the second one skips its whole closure.
func anSearchDriveAccepted(t *testing.T, f *effectsFixture, tenantID, invoiceID string, riverJobID int64) string {
	t.Helper()
	idemKey := anIdemKey(invoiceID)
	w := newTestWorker(f.app, newScriptedAdapter(anAccepted("SEARCH")))
	if err := w.Work(context.Background(), newSubmitJob(riverJobID, 1, 8, submission.SubmitArgs{
		TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey,
	})); err != nil {
		t.Fatalf("submit to Accepted (river job %d): %v", riverJobID, err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Fatalf("submission_jobs.state = %q, want %q -- the branch under test did not run", wj.state, "accepted")
	}
	return wj.id
}

// anSearchLimit is well above this fixture's row count, so HasMore stays false and the
// control assertion sees the whole result set.
const anSearchLimit = 50

// anSearchInvoiceIDKey is rule B's payload spelling, the half this file covers.
const anSearchInvoiceIDKey = "invoice_id"

// AC-1 (Core AC 4): a real SubmitWorker verdict is returned by the real reader for the
// invoice number, and the returned row carries that number.
func TestAuditNumber_SearchFindsASubmissionWorkersRow(t *testing.T) {
	f := requireExchangeDB(t)

	tenantID := seedTenant(t, f)
	defer cleanupTenant(t, f, tenantID)
	entityID := seedEntity(t, f, tenantID)
	invoiceID := seedInvoice(t, f, tenantID, entityID)
	// A second invoice driven through the same worker in the same tenant. Without it a
	// reader that ignored the search and returned every row in the tenant would pass.
	controlID := seedInvoice(t, f, tenantID, entityID)

	want := anMustInvoiceNumber(t, f, tenantID, invoiceID)
	control := anMustInvoiceNumber(t, f, tenantID, controlID)
	if want == "" {
		t.Fatalf("fixture invoice %s carries a blank invoice_number; a blank needle applies no filter", invoiceID)
	}
	if want == control || strings.Contains(control, want) {
		t.Fatalf("the control number %q is not distinguishable from the needle %q", control, want)
	}
	if strings.Contains(invoiceID, want) {
		t.Fatalf("the invoice number %q is a substring of the invoice id %s, so the generic payload arm could supply the match (09's uuid-prefix trap)", want, invoiceID)
	}

	jobID := anSearchDriveAccepted(t, f, tenantID, invoiceID, 8801)
	anSearchDriveAccepted(t, f, tenantID, controlID, 8802)

	const event = "submission.accepted"
	if strings.Contains(strings.ToLower(event), strings.ToLower(want)) {
		t.Fatalf("the needle %q occurs in the event name %q; the event arm would supply the match", want, event)
	}

	var ctx context.Context
	if err := db.WithinTenantTx(context.Background(), f.mig, tenantID, func(tx pgx.Tx) error {
		userID := uuid.NewString()
		_, e := tx.Exec(context.Background(),
			`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
			tenantID, userID, "preparer", "active")
		ctx = auth.WithIdentity(context.Background(), auth.Identity{
			Subject: userID, Role: "authenticated", TenantID: tenantID,
		})
		return e
	}); err != nil {
		t.Fatalf("seed membership via WithinTenantTx: %v", err)
	}
	got, err := audit.NewStore(f.app).List(ctx, audit.Filter{Q: want, Limit: anSearchLimit})
	if err != nil {
		t.Fatalf("audit.Store.List(q=%q): %v", want, err)
	}

	if len(got.Events) == 0 {
		t.Fatalf("q=%q returned no events at all (total=%d, log_is_empty=%v); the worker wrote a %s row for invoice %s and the real reader must find it",
			want, got.Total, got.LogIsEmpty, event, invoiceID)
	}
	if got.Total < 1 {
		t.Fatalf("q=%q reported total %d, want at least 1", want, got.Total)
	}
	if got.Page.HasMore {
		t.Fatalf("q=%q filled the %d-row page; the control assertion below would only see a prefix", want, anSearchLimit)
	}

	var target *audit.Event
	for i := range got.Events {
		e := &got.Events[i]
		p := anSearchPayload(t, e.Payload)
		if strings.Contains(string(e.Payload), controlID) {
			t.Errorf("q=%q returned a %s row naming the control invoice %s, whose number is %q -- the search is not narrowing to the resolved invoice; payload = %s",
				want, e.Event, controlID, control, e.Payload)
		}
		if e.Event == event && p[anSearchInvoiceIDKey] == invoiceID {
			target = e
		}
	}
	if target == nil {
		t.Fatalf("q=%q returned %d events (total=%d) but none is a %s for invoice %s", want, len(got.Events), got.Total, event, invoiceID)
	}

	p := anSearchPayload(t, target.Payload)
	if p["submission_job_id"] != jobID {
		t.Fatalf("%s: payload submission_job_id = %v, want %q -- this is not the row the worker wrote", event, p["submission_job_id"], jobID)
	}
	// Rule B's spelling, asserted so the two-spelling floor is mechanical rather than prose.
	if _, wrong := p["id"]; wrong {
		t.Errorf("%s: the returned payload carries id, rule A's spelling; internal/invoice owns that half; payload = %s", event, target.Payload)
	}
	// The needle reaches this row through the resolved arm alone: no other value in the
	// payload holds it, and the generic arm excludes the invoice_number key.
	for k, v := range p {
		s, isStr := v.(string)
		if k == auditNumberKey || !isStr {
			continue
		}
		if strings.Contains(strings.ToLower(s), strings.ToLower(want)) {
			t.Fatalf("%s: payload %q = %q contains the needle %q, so the generic value arm could supply the match", event, k, s, want)
		}
	}

	// The CF-30 assertion: the RETURNED payload carries the key. Only this goes red with
	// AUDIT-11-03 reverted.
	v, ok := p[auditNumberKey]
	if !ok {
		t.Fatalf("%s (SubmitWorker.Work, case Accepted): the returned payload has no %q key, so the row the reader hands back cannot say which invoice it is about; payload = %s, want %q",
			event, auditNumberKey, target.Payload, want)
	}
	if s, _ := v.(string); s != want {
		t.Errorf("%s (SubmitWorker.Work, case Accepted): the returned payload %q = %q, want %q (invoices.invoice_number read back)",
			event, auditNumberKey, s, want)
	}
}

func anSearchPayload(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal the returned payload %s: %v", raw, err)
	}
	return m
}
