// M4-02-01 (task-96): Stage-4 QA fix-loop regression coverage. QA found that
// Store.Create didn't honor its own [D10] contract / doc comment
// ("EntityID + InvoiceNumber required, non-empty"): an empty EntityID or
// InvoiceNumber persisted silently (invoice_number has no non-empty CHECK),
// and a malformed non-empty EntityID (fails uuid parse) surfaced a raw
// *pgconn.PgError (22P02) instead of ErrValidation. Both are fixed in
// store.go (a pre-tx empty-field guard + a 22P02 case in Create's pg-error
// switch); these tests pin that fix. Reuses the dbTestPools/seedTenant/
// seedEntity/mustCount/auditCount harness from store_test.go (same package).
package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestStoreCreate_RequiresEntityIDAndNumber: an empty EntityID or an empty
// InvoiceNumber is rejected as ErrValidation BEFORE any tx opens (mirrors
// Update's all-nil pre-tx guard); nothing persists for either case.
func TestStoreCreate_RequiresEntityIDAndNumber(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "VAL-01 tenant")
	entityID := seedEntity(t, super, tenantID, "VAL-01 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "invoice.created"

	t.Run("empty EntityID", func(t *testing.T) {
		before := auditCount(t, app, tenantID, event)

		_, err := store.Create(c, CreateInput{EntityID: "", InvoiceNumber: "VAL-01-A"})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("Create (empty EntityID) err = %v, want ErrValidation", err)
		}

		if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("invoices rows for tenant after rejected Create (empty EntityID) = %d, want 0", n)
		}
		if n := auditCount(t, app, tenantID, event); n != before {
			t.Errorf("audit_log rows for %s after rejected Create (empty EntityID) = %d, want unchanged %d", event, n, before)
		}
	})

	t.Run("empty InvoiceNumber", func(t *testing.T) {
		before := auditCount(t, app, tenantID, event)

		_, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: ""})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("Create (empty InvoiceNumber) err = %v, want ErrValidation", err)
		}

		if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("invoices rows for tenant after rejected Create (empty InvoiceNumber) = %d, want 0", n)
		}
		if n := auditCount(t, app, tenantID, event); n != before {
			t.Errorf("audit_log rows for %s after rejected Create (empty InvoiceNumber) = %d, want unchanged %d", event, n, before)
		}
	})
}

// TestStoreCreate_MalformedEntityIDIsValidationError: a non-empty but
// malformed EntityID (fails uuid parse at the DB, SQLSTATE 22P02
// invalid_text_representation) maps to ErrValidation, not a raw
// *pgconn.PgError -- entity_id is the only uuid-typed input param on Create
// (tenant is from ctx; import_batch_id is omitted, [D3]), so 22P02 here
// unambiguously means a bad entity_id.
func TestStoreCreate_MalformedEntityIDIsValidationError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "VAL-02 tenant")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "invoice.created"
	before := auditCount(t, app, tenantID, event)

	_, err := store.Create(c, CreateInput{EntityID: "not-a-uuid", InvoiceNumber: "VAL-02"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create (malformed EntityID) err = %v, want ErrValidation (22P02 invalid_text_representation)", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("invoices rows for tenant after rejected Create (malformed EntityID) = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("line_items rows for tenant after rejected Create (malformed EntityID) = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("invoice_status_history rows for tenant after rejected Create (malformed EntityID) = %d, want 0", n)
	}
	if n := auditCount(t, app, tenantID, event); n != before {
		t.Errorf("audit_log rows for %s after rejected Create (malformed EntityID) = %d, want unchanged %d", event, n, before)
	}
}
