// M4-02 PR review (CodeRabbit finding A): Get, Update, and Transition all run
// a `WHERE id = $1` query but, before this fix, only mapped pgx.ErrNoRows ->
// ErrNotFound -- a malformed (non-uuid) path id raises 22P02
// (invalid_text_representation) at Postgres, which fell through raw as an
// unmapped *pgconn.PgError -> HTTP 500. store.go now maps 22P02 ->
// ErrValidation in all three methods (mirroring Create's existing entity_id
// mapping, commit c324f4e). This test pins that fix and proves it does NOT
// regress the distinct cross-tenant/nonexistent-but-VALID-uuid case (still
// ErrNotFound via pgx.ErrNoRows) by leaving a real baseline invoice
// completely untouched throughout. Reuses the dbTestPools/seedTenant/
// seedEntity/seedInvoice/mustCount/auditCount harness from store_test.go
// (same package).
package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestStore_MalformedIDIsValidationError: a non-uuid id string passed to
// Get/Update/Transition maps to ErrValidation (22P02), not a raw
// *pgconn.PgError / HTTP 500; Update/Transition persist no writes, and a
// real baseline invoice for the same tenant is left completely unchanged by
// either failed call.
func TestStore_MalformedIDIsValidationError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "MALFORMED-ID tenant")
	entityID := seedEntity(t, super, tenantID, "MALFORMED-ID entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "MALFORMED-ID-baseline")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const malformed = "not-a-uuid"

	t.Run("Get", func(t *testing.T) {
		_, err := store.Get(c, malformed)
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("Get(malformed id) err = %v, want ErrValidation (22P02 invalid_text_representation)", err)
		}
	})

	t.Run("Update", func(t *testing.T) {
		const event = "invoice.updated"
		before := auditCount(t, app, tenantID, event)

		newTotal := "1.00"
		_, err := store.Update(c, malformed, UpdateInput{Total: &newTotal})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("Update(malformed id) err = %v, want ErrValidation (22P02 invalid_text_representation)", err)
		}

		if after := auditCount(t, app, tenantID, event); after != before {
			t.Errorf("audit_log rows for %s after malformed-id Update = %d, want unchanged %d", event, after, before)
		}
		var total *string
		if err := super.QueryRow(ctx, `SELECT total::text FROM invoices WHERE id = $1`, invoiceID).Scan(&total); err != nil {
			t.Fatalf("read back baseline invoice: %v", err)
		}
		if total != nil {
			t.Errorf("baseline invoice total after malformed-id Update = %v, want unchanged NULL", *total)
		}
	})

	t.Run("Transition", func(t *testing.T) {
		const event = "invoice.transitioned"
		beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invoiceID)
		beforeAudit := auditCount(t, app, tenantID, event)

		_, err := store.Transition(c, malformed, StatusValidated)
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("Transition(malformed id) err = %v, want ErrValidation (22P02 invalid_text_representation)", err)
		}

		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invoiceID); n != beforeHistory {
			t.Errorf("invoice_status_history rows for baseline invoice after malformed-id Transition = %d, want unchanged %d", n, beforeHistory)
		}
		if n := auditCount(t, app, tenantID, event); n != beforeAudit {
			t.Errorf("audit_log rows for %s after malformed-id Transition = %d, want unchanged %d", event, n, beforeAudit)
		}
		var status string
		if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&status); err != nil {
			t.Fatalf("read back baseline invoice status: %v", err)
		}
		if status != string(StatusDraft) {
			t.Errorf("baseline invoice status after malformed-id Transition = %q, want unchanged %q", status, StatusDraft)
		}
	})
}
