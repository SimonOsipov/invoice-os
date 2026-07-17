// M4-05-02 (task-121): tests for internal/invoice's Store.Edit -- the
// fix-loop orchestrator (System Design §4, M4-05 story) that composes a
// content write with a conditional validated->draft demotion in ONE
// db.WithinRequestTenantTx -- written BEFORE the real implementation exists
// (RED against store.go's not-implemented STUB: Edit currently always
// returns a distinct not-implemented error, never ErrNotFixable/ErrValidation/
// ErrNotFound/nil, so every assertion below fails on that mismatch or on the
// stub's non-nil error where success is wanted -- never a compile error).
// Reuses the dbTestPools/seedTenant/seedEntity/seedInvoice/mustCount/
// auditCount/auditActor/strPtr harness from store_test.go, seedInvoiceAtStatus
// from transition_adversarial_test.go, and seedRuleSetVersionID/readViolations
// from apply_validation_test.go (all same package).
//
// Spec-to-test map (Test Specs table, M4-05-02 story / task-121):
//
//	Core AC #1   TestStoreEdit_NonFixableStateRejected
//	Core AC #2   TestStoreEdit_ValidatedContentChangeDemotes
//	Core AC #2   TestStoreEdit_ContentAuditFailureRollsBackWholeEdit
//	Core AC #4   TestStoreEdit_DemoteThenRevalidateSucceeds
//	Core AC #3   TestStoreEdit_ValidatedNoOpStaysValidated
//	Core AC #3   TestStoreEdit_NumericScaleNoOp
//	Core AC #2/4 TestStoreEdit_DraftContentChangeNoDemotion
//	[A6]         TestStoreEdit_DraftNoOpWritesNothing
//	[A7]         TestStoreEdit_AllNilRejected
//	[A8]         TestStoreEdit_GuardBeforeContentValidation
//	existing     TestStoreEdit_NotFoundAndCrossTenant
//
// Run: `make test-rls` (or `make test-audit`), or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// EDIT-01: a non-fixable-state (queued) invoice refuses Edit with
// ErrNotFixable; nothing is written.
func TestStoreEdit_NonFixableStateRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-01 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-01 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-01"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusQueued); err != nil {
		t.Fatalf("pre-hop Transition(-> queued): %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

	newVAT := "9.99"
	_, err = store.Edit(c, inv.ID, UpdateInput{VAT: &newVAT})
	if !errors.Is(err, ErrNotFixable) {
		t.Fatalf("Edit(queued invoice) err = %v, want ErrNotFixable", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusQueued {
		t.Errorf("invoices.status after refused Edit = %q, want unchanged %q", status, StatusQueued)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d", n, beforeUpdated)
	}
}

// EDIT-02: a content-changing Edit on a validated invoice demotes it to
// draft, writing exactly one (validated,draft) history row and one
// invoice.transitioned + one invoice.updated audit, all in one tx.
func TestStoreEdit_ValidatedContentChangeDemotes(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-02 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-02 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-02", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	newVAT := "9.50"
	got, err := store.Edit(c, inv.ID, UpdateInput{VAT: &newVAT})
	if err != nil {
		t.Fatalf("Edit (content change on validated invoice): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted)", got.Status, StatusDraft)
	}

	var dbStatus string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&dbStatus); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(dbStatus) != StatusDraft {
		t.Errorf("invoices.status after Edit = %q, want %q", dbStatus, StatusDraft)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory+1 {
		t.Errorf("invoice_status_history rows = %d, want %d (exactly one new demotion row)", n, beforeHistory+1)
	}
	var fromStatus *string
	var toStatus, actor string
	if err := super.QueryRow(ctx,
		`SELECT from_status, to_status, actor FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`,
		inv.ID,
	).Scan(&fromStatus, &toStatus, &actor); err != nil {
		t.Fatalf("read newest history row: %v", err)
	}
	if fromStatus == nil || Status(*fromStatus) != StatusValidated {
		t.Errorf("newest history from_status = %v, want %q", fromStatus, StatusValidated)
	}
	if Status(toStatus) != StatusDraft {
		t.Errorf("newest history to_status = %q, want %q", toStatus, StatusDraft)
	}
	if actor != subject {
		t.Errorf("newest history actor = %q, want %q", actor, subject)
	}

	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned+1 {
		t.Errorf("audit_log invoice.transitioned rows = %d, want %d (exactly one new row)", n, beforeTransitioned+1)
	}
	if a := auditActor(t, app, tenantID, "invoice.transitioned"); a != subject {
		t.Errorf("invoice.transitioned audit actor = %q, want %q", a, subject)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("audit_log invoice.updated rows = %d, want %d (exactly one new row)", n, beforeUpdated+1)
	}
	if a := auditActor(t, app, tenantID, "invoice.updated"); a != subject {
		t.Errorf("invoice.updated audit actor = %q, want %q", a, subject)
	}
}

// EDIT-03: a crafted caller Subject (empty, or 256 chars) that fails the
// content-write audit CHECK at step 7 (invoice.updated, audit_log's
// char_length(actor) in [1,255]) -- which precedes the demotion in the SAME
// WithinRequestTenantTx -- rolls back the WHOLE edit: error propagates raw
// (SQLSTATE 23514), content byte-unchanged, status still validated, no new
// history row, no new audit row.
//
// This deliberately does NOT isolate the demotion write in isolation: no
// injection point isolated to the demotion exists, because audit_log's actor
// CHECK (char_length 1..255) is a STRICT SUPERSET of invoice_status_history's
// (char_length > 0, no upper bound) -- any actor bad enough to fail the
// demotion's history INSERT (empty) fails the earlier invoice.updated audit
// FIRST and aborts before transitionTx ever runs; a 256-char actor passes
// history's check but fails audit_log's upper bound at the SAME earlier step.
// The demotion write's OWN atomicity (a fault strictly inside transitionTx)
// is separately, independently proven by
// TestTransition_AtomicityRollsBackOnActorCheckFailure (transition_test.go:354),
// which Store.Edit's transitionTx call reuses verbatim.
func TestStoreEdit_ContentAuditFailureRollsBackWholeEdit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	run := func(t *testing.T, label, craftedSubject string) {
		tenantID := seedTenant(t, super, "EDIT-03 "+label+" tenant")
		entityID := seedEntity(t, super, tenantID, "EDIT-03 entity")
		cNormal := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		inv, err := store.Create(cNormal, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-03-" + label, VAT: strPtr("7.00")})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := store.Transition(cNormal, inv.ID, StatusValidated); err != nil {
			t.Fatalf("pre-hop Transition(-> validated): %v", err)
		}

		var beforeVAT string
		if err := super.QueryRow(ctx, `SELECT vat::text FROM invoices WHERE id = $1`, inv.ID).Scan(&beforeVAT); err != nil {
			t.Fatalf("read back vat (before): %v", err)
		}
		beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
		beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
		beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

		cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: craftedSubject, Role: "authenticated", TenantID: tenantID})
		newVAT := "9.50"
		_, err = store.Edit(cCrafted, inv.ID, UpdateInput{VAT: &newVAT})
		if err == nil {
			t.Fatal("Edit with a crafted actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
		}
		if code := pgCode(err); code != "23514" {
			t.Fatalf("Edit with a crafted actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
		}

		var afterVAT, afterStatus string
		if err := super.QueryRow(ctx, `SELECT vat::text, status FROM invoices WHERE id = $1`, inv.ID).Scan(&afterVAT, &afterStatus); err != nil {
			t.Fatalf("read back vat/status (after): %v", err)
		}
		if afterVAT != beforeVAT {
			t.Errorf("vat after failed Edit = %q, want byte-unchanged %q (whole tx rolled back)", afterVAT, beforeVAT)
		}
		if Status(afterStatus) != StatusValidated {
			t.Errorf("status after failed Edit = %q, want unchanged %q", afterStatus, StatusValidated)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
			t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
		}
		if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
			t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d", n, beforeUpdated)
		}
		if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
			t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d", n, beforeTransitioned)
		}
	}

	t.Run("empty actor fails audit_log CHECK (23514)", func(t *testing.T) {
		run(t, "empty", "")
	})
	t.Run("256-char actor fails audit_log CHECK (23514)", func(t *testing.T) {
		run(t, "256char", strings.Repeat("a", 256))
	})
}

// EDIT-04: a no-op edit (every field set to its CURRENT value) on a
// validated invoice leaves it validated -- no history row, no invoice.updated
// audit.
func TestStoreEdit_ValidatedNoOpStaysValidated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-04 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-04 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "EDIT-04",
		SupplierTIN:   strPtr("SUP-TIN-1"),
		SupplierName:  strPtr("Supplier Co"),
		BuyerTIN:      strPtr("BUY-TIN-1"),
		BuyerName:     strPtr("Buyer Co"),
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("100.00"),
		VAT:           strPtr("7.00"),
		Total:         strPtr("107.00"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

	// Every field re-sent with its CURRENT value (issue_date is left nil,
	// which is UpdateInput's own "leave unchanged" meaning -- trivially
	// "current" for a field that was never set).
	got, err := store.Edit(c, inv.ID, UpdateInput{
		SupplierTIN:  strPtr("SUP-TIN-1"),
		SupplierName: strPtr("Supplier Co"),
		BuyerTIN:     strPtr("BUY-TIN-1"),
		BuyerName:    strPtr("Buyer Co"),
		Currency:     strPtr("NGN"),
		Subtotal:     strPtr("100.00"),
		VAT:          strPtr("7.00"),
		Total:        strPtr("107.00"),
	})
	if err != nil {
		t.Fatalf("Edit (no-op, every field identical): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("Edit returned status = %q, want unchanged %q (no-op)", got.Status, StatusValidated)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d (no-op writes no history)", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d (no-op writes no audit)", n, beforeUpdated)
	}
}

// EDIT-05: a numeric-scale-only change ("100.00" -> "100.0", which
// numeric(14,2) normalizes to the SAME stored value) is a no-op -- the
// DB-authoritative fingerprint comparison (not a Go-side string compare)
// is what makes this a no-op rather than a false "changed".
func TestStoreEdit_NumericScaleNoOp(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-05 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-05 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-05", Total: strPtr("100.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

	got, err := store.Edit(c, inv.ID, UpdateInput{Total: strPtr("100.0")})
	if err != nil {
		t.Fatalf("Edit (numeric-scale no-op, \"100.00\"->\"100.0\"): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("Edit returned status = %q, want unchanged %q (scale-only is a no-op)", got.Status, StatusValidated)
	}
	if got.Total == nil || *got.Total != "100.00" {
		t.Errorf("Edit returned total = %v, want DB-normalized %q", got.Total, "100.00")
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d", n, beforeUpdated)
	}
}

// EDIT-06: a content-changing edit on a DRAFT invoice stays draft (nothing to
// demote FROM), writes exactly one invoice.updated audit, and no history row.
func TestStoreEdit_DraftContentChangeNoDemotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-06 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-06 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-06"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.Status != StatusDraft {
		t.Fatalf("Create: status = %q, want %q (precondition)", inv.Status, StatusDraft)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

	got, err := store.Edit(c, inv.ID, UpdateInput{VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Edit (content change on draft): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want unchanged %q (draft has nothing to demote from)", got.Status, StatusDraft)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d (draft edit writes no history)", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("audit_log invoice.updated rows = %d, want %d (exactly one new row)", n, beforeUpdated+1)
	}
	if a := auditActor(t, app, tenantID, "invoice.updated"); a != subject {
		t.Errorf("invoice.updated audit actor = %q, want %q", a, subject)
	}
}

// [A6]/EDIT-07: a no-op edit on a draft invoice writes NOTHING (no
// invoice.updated audit) -- idempotence applies to draft, not just validated.
func TestStoreEdit_DraftNoOpWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-07 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-07 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-07", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

	got, err := store.Edit(c, inv.ID, UpdateInput{VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Edit (no-op on draft): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q", got.Status, StatusDraft)
	}

	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d (no-op writes nothing)", n, beforeUpdated)
	}
}

// [A7]/EDIT-08: an all-nil UpdateInput is rejected as ErrValidation, mirroring
// Store.Update's own all-nil guard; nothing is written.
func TestStoreEdit_AllNilRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-08 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-08 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-08"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

	if _, err := store.Edit(c, inv.ID, UpdateInput{}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Edit(all-nil) err = %v, want ErrValidation", err)
	}

	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows after all-nil Edit = %d, want unchanged %d", n, beforeUpdated)
	}
}

// [A8]/EDIT-09: the fixable-state guard runs BEFORE content validation -- a
// queued invoice edited with a malformed numeric string still resolves to
// ErrNotFixable (409), not ErrValidation (400, which a 22P02 would otherwise
// map to).
func TestStoreEdit_GuardBeforeContentValidation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-09 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-09 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-09"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusQueued); err != nil {
		t.Fatalf("pre-hop Transition(-> queued): %v", err)
	}

	_, err = store.Edit(c, inv.ID, UpdateInput{VAT: strPtr("not-a-number")})
	if !errors.Is(err, ErrNotFixable) {
		t.Fatalf("Edit(queued, malformed numeric) err = %v, want ErrNotFixable (guard wins over 22P02)", err)
	}
	if errors.Is(err, ErrValidation) {
		t.Errorf("Edit(queued, malformed numeric) err = %v, must NOT also resolve as ErrValidation (guard must win outright)", err)
	}
}

// EDIT-10: Edit against a cross-tenant id, or a genuinely nonexistent id,
// resolves to ErrNotFound; nothing is written to the target row.
func TestStoreEdit_NotFoundAndCrossTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	t.Run("nonexistent id", func(t *testing.T) {
		tenantID := seedTenant(t, super, "EDIT-10 tenant")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		bogusID := uuid.NewString()
		if _, err := store.Edit(c, bogusID, UpdateInput{VAT: strPtr("7.00")}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Edit(nonexistent id) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("cross-tenant id", func(t *testing.T) {
		tenantA := seedTenant(t, super, "EDIT-10 tenant A")
		tenantB := seedTenant(t, super, "EDIT-10 tenant B")
		entityB := seedEntity(t, super, tenantB, "EDIT-10 B entity")
		invoiceB := seedInvoice(t, super, tenantB, entityB, "EDIT-10-B")

		cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

		if _, err := store.Edit(cA, invoiceB, UpdateInput{VAT: strPtr("7.00")}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Edit(tenant B's invoice) as tenant A err = %v, want ErrNotFound", err)
		}

		var vat *string
		if err := super.QueryRow(ctx, `SELECT vat::text FROM invoices WHERE id = $1`, invoiceB).Scan(&vat); err != nil {
			t.Fatalf("read back B's invoice: %v", err)
		}
		if vat != nil {
			t.Errorf("B's invoice vat after refused cross-tenant Edit = %v, want unchanged NULL", *vat)
		}
	})
}

// EDIT-11: a validated invoice carrying a real (non-blocking) violation set
// and a stamped rule_set_version_id is demoted by Store.Edit -- the demotion
// leaves that STALE stamp untouched -- and then Store.ApplyValidation
// re-runs, satisfies the still-draft-only gate, and re-stamps/promotes back
// to validated with a fresh clean verdict. Closes the loop End to end: the
// gate itself is completely unmodified by M4-05 ([A12]) -- only the edge and
// Store.Edit's demotion feed it a fresh draft to re-evaluate.
func TestStoreEdit_DemoteThenRevalidateSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-11 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-11 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-11", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	staleVersionID := seedRuleSetVersionID(t, super)
	staleViolations := []Violation{{RuleKey: "vat-standard-rate", Severity: "warning", Message: "VAT rate looks unusual"}}
	fp := contentFingerprint(inv)
	validated, err := store.ApplyValidation(c, inv.ID, staleViolations, staleVersionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (seed: warning-only promotes with violations stored): %v", err)
	}
	if validated.Status != StatusValidated {
		t.Fatalf("ApplyValidation (seed): status = %q, want %q (precondition)", validated.Status, StatusValidated)
	}

	// Store.Edit changes content -> demotes validated->draft, keeping the
	// STALE prior-validation stamp untouched.
	newVAT := "9.50"
	edited, err := store.Edit(c, inv.ID, UpdateInput{VAT: &newVAT})
	if err != nil {
		t.Fatalf("Edit (content change, demotion): want success, got: %v", err)
	}
	if edited.Status != StatusDraft {
		t.Fatalf("Edit: status = %q, want %q (demoted)", edited.Status, StatusDraft)
	}
	if edited.RuleSetVersionID == nil || *edited.RuleSetVersionID != staleVersionID {
		t.Errorf("Edit: rule_set_version_id = %v, want unchanged (stale) %q -- Edit must not touch it", edited.RuleSetVersionID, staleVersionID)
	}
	staleAfterEdit := readViolations(t, super, inv.ID)
	if len(staleAfterEdit) != 1 || staleAfterEdit[0].RuleKey != staleViolations[0].RuleKey || staleAfterEdit[0].Severity != staleViolations[0].Severity {
		t.Errorf("Edit: violations = %+v, want unchanged (stale) %+v -- Edit must not touch them", staleAfterEdit, staleViolations)
	}

	// Re-validate: a FRESH fingerprint (post-edit content) satisfies the
	// unchanged draft-only gate's status re-check (now draft) and content
	// re-check (fp matches the just-demoted row); a clean verdict re-stamps
	// and promotes back to validated.
	freshFP := contentFingerprint(edited)
	freshVersionID := seedRuleSetVersionID(t, super)
	revalidated, err := store.ApplyValidation(c, inv.ID, []Violation{}, freshVersionID, freshFP)
	if err != nil {
		t.Fatalf("ApplyValidation (re-validate after demotion): want success through the unchanged draft-only gate, got: %v", err)
	}
	if revalidated.Status != StatusValidated {
		t.Errorf("ApplyValidation (re-validate): status = %q, want %q (promoted back to green)", revalidated.Status, StatusValidated)
	}
	if revalidated.RuleSetVersionID == nil || *revalidated.RuleSetVersionID != freshVersionID {
		t.Errorf("ApplyValidation (re-validate): rule_set_version_id = %v, want re-stamped %q", revalidated.RuleSetVersionID, freshVersionID)
	}
	var freshViolationsText string
	if err := super.QueryRow(ctx, `SELECT violations::text FROM invoices WHERE id = $1`, inv.ID).Scan(&freshViolationsText); err != nil {
		t.Fatalf("read back violations: %v", err)
	}
	if freshViolationsText != "[]" {
		t.Errorf("violations after re-validate = %q, want %q (re-stamped clean, stale warning cleared)", freshViolationsText, "[]")
	}
}
