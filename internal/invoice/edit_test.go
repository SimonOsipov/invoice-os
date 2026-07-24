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
//	QA/Mode-B    TestStoreEdit_PartialNonMoneyFieldChangeDemotes
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

// QA Mode-B adversarial: a validated invoice with SEVERAL fields set is
// edited on exactly ONE non-money field (buyer_name) -- proving two things
// no existing spec combines: (1) a text-typed column change (not a numeric
// one -- every other demotion spec here only ever changes VAT/Total) is
// enough to trip the fingerprint diff and demote, so contentFingerprint's
// sensitivity to all ten MBS-content columns (unit-proven in
// payload_fingerprint_test.go's TestContentFingerprint_
// EachOfTenContentColumnsIsSignificant) is actually WIRED UP through
// Store.Edit's real-change/demotion path, not just money fields; and (2)
// updateContentTx's dynamic SET clause touches ONLY the one field named in
// UpdateInput -- every sibling field (both the other text columns and the
// money columns) survives byte-unchanged, guarding against an off-by-one in
// the SET-clause/placeholder-index build silently clobbering an adjacent
// column.
func TestStoreEdit_PartialNonMoneyFieldChangeDemotes(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-12 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-12 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "EDIT-12",
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
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	got, err := store.Edit(c, inv.ID, UpdateInput{BuyerName: strPtr("New Buyer Co")})
	if err != nil {
		t.Fatalf("Edit (single non-money field change on validated): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (a non-money field change demotes too)", got.Status, StatusDraft)
	}

	// The one field named in UpdateInput changed...
	if got.BuyerName == nil || *got.BuyerName != "New Buyer Co" {
		t.Errorf("Edit: BuyerName = %v, want %q", got.BuyerName, "New Buyer Co")
	}
	// ...every sibling field, text AND numeric, survives byte-unchanged
	// (updateContentTx's dynamic SET clause must not have touched them).
	if got.SupplierTIN == nil || *got.SupplierTIN != "SUP-TIN-1" {
		t.Errorf("Edit: SupplierTIN = %v, want unchanged %q", got.SupplierTIN, "SUP-TIN-1")
	}
	if got.SupplierName == nil || *got.SupplierName != "Supplier Co" {
		t.Errorf("Edit: SupplierName = %v, want unchanged %q", got.SupplierName, "Supplier Co")
	}
	if got.BuyerTIN == nil || *got.BuyerTIN != "BUY-TIN-1" {
		t.Errorf("Edit: BuyerTIN = %v, want unchanged %q", got.BuyerTIN, "BUY-TIN-1")
	}
	if got.Currency == nil || *got.Currency != "NGN" {
		t.Errorf("Edit: Currency = %v, want unchanged %q", got.Currency, "NGN")
	}
	if got.Subtotal == nil || *got.Subtotal != "100.00" {
		t.Errorf("Edit: Subtotal = %v, want unchanged %q", got.Subtotal, "100.00")
	}
	if got.VAT == nil || *got.VAT != "7.00" {
		t.Errorf("Edit: VAT = %v, want unchanged %q", got.VAT, "7.00")
	}
	if got.Total == nil || *got.Total != "107.00" {
		t.Errorf("Edit: Total = %v, want unchanged %q", got.Total, "107.00")
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory+1 {
		t.Errorf("invoice_status_history rows = %d, want %d (exactly one new demotion row)", n, beforeHistory+1)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("audit_log invoice.updated rows = %d, want %d (exactly one new row)", n, beforeUpdated+1)
	}
	if a := auditActor(t, app, tenantID, "invoice.updated"); a != subject {
		t.Errorf("invoice.updated audit actor = %q, want %q", a, subject)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned+1 {
		t.Errorf("audit_log invoice.transitioned rows = %d, want %d (exactly one new row)", n, beforeTransitioned+1)
	}
}

// --- M5-05-01 (task-237): the widened rejected leg of the fix loop --------
//
// Spec-to-test map (Test Specs table, M5-05-01 / task-237):
//
//	AC#3 TestStoreEdit_RejectedContentChangeDemotesAndClearsReasons
//	AC#4 TestStoreEdit_RejectedNoOpKeepsStatusAndReasons
//	AC#5 TestStoreEdit_AcceptedStaysNotFixable
//	AC#3/#5 TestStoreEdit_ClearingIsAtomicWithTheDemotion
//
// seedInvoiceAtStatus is defined in transition_adversarial_test.go (same
// package). rejection_reasons has no Go-side field on Invoice (invoiceColumns/
// scanInvoice deliberately does not project it, store.go) -- every assertion
// below reads it back with a raw `::text` SELECT, mirroring
// internal/platform/db/invoices_fiscal_rls_test.go's own convention.

// EDIT-13/AC#3: a content-changing Edit on a REJECTED invoice demotes it to
// draft, clears rejection_reasons back to '[]', and writes exactly one
// (rejected,draft) history row plus one invoice.transitioned + one
// invoice.updated audit row, all in the SAME transaction -- mirrors
// TestStoreEdit_ValidatedContentChangeDemotes's shape for the widened leg.
//
// The history row's from_status is asserted explicitly against StatusRejected
// (never StatusValidated) -- transitionTx's `current` parameter is used BOTH
// for the legality check and for the from_status value it writes into
// invoice_status_history. A demotion branch that widened the outer guard to
// accept rejected but left the literal StatusValidated in the transitionTx
// call would still (wrongly) succeed here -- via canTransition(validated,
// draft), an edge that is ALREADY legal today -- and would write a FALSE
// history row claiming the invoice came from validated. This assertion is
// what catches that specific bug; a byte-value check on from_status, not
// merely "a history row exists".
func TestStoreEdit_RejectedContentChangeDemotesAndClearsReasons(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-13 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-13 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "EDIT-13", StatusRejected)
	reasonsJSON := `[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, reasonsJSON, invID,
	); err != nil {
		t.Fatalf("seed rejection_reasons: %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	newVAT := "9.50"
	got, err := store.Edit(c, invID, UpdateInput{VAT: &newVAT})
	if err != nil {
		t.Fatalf("Edit (content change on rejected invoice): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted)", got.Status, StatusDraft)
	}

	var dbStatus, reasons string
	if err := super.QueryRow(ctx,
		`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&dbStatus, &reasons); err != nil {
		t.Fatalf("read back status/rejection_reasons: %v", err)
	}
	if Status(dbStatus) != StatusDraft {
		t.Errorf("invoices.status after Edit = %q, want %q", dbStatus, StatusDraft)
	}
	if reasons != "[]" {
		t.Errorf("invoices.rejection_reasons after Edit = %q, want %q (cleared)", reasons, "[]")
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory+1 {
		t.Errorf("invoice_status_history rows = %d, want %d (exactly one new demotion row)", n, beforeHistory+1)
	}
	var fromStatus *string
	var toStatus, actor string
	if err := super.QueryRow(ctx,
		`SELECT from_status, to_status, actor FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`,
		invID,
	).Scan(&fromStatus, &toStatus, &actor); err != nil {
		t.Fatalf("read newest history row: %v", err)
	}
	if fromStatus == nil || Status(*fromStatus) != StatusRejected {
		t.Errorf("newest history from_status = %v, want %q (NOT %q -- transitionTx's `current` arg must come from before.Status, never a hardcoded validated literal)", fromStatus, StatusRejected, StatusValidated)
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

// EDIT-14/AC#4: a no-op edit (every field resent at its CURRENT value) on a
// REJECTED invoice leaves it rejected, with rejection_reasons untouched --
// the fingerprint short-circuit (step 6) must still win over the widened
// fixable-state guard, exactly as it already does for validated
// (TestStoreEdit_ValidatedNoOpStaysValidated). Reaches rejected via real
// Transition hops (submitted->rejected is ALREADY legal today), then
// force-seeds rejection_reasons directly -- nothing in internal/invoice
// writes that column yet.
func TestStoreEdit_RejectedNoOpKeepsStatusAndReasons(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-14 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-14 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-14", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, hop := range []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusRejected} {
		if _, err := store.Transition(c, inv.ID, hop); err != nil {
			t.Fatalf("pre-hop Transition(-> %s): %v", hop, err)
		}
	}
	reasonsJSON := `[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, reasonsJSON, inv.ID,
	); err != nil {
		t.Fatalf("seed rejection_reasons: %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	got, err := store.Edit(c, inv.ID, UpdateInput{VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Edit (no-op on rejected invoice): want success, got: %v", err)
	}
	if got.Status != StatusRejected {
		t.Errorf("Edit returned status = %q, want unchanged %q (no-op)", got.Status, StatusRejected)
	}

	var reasons string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, inv.ID).Scan(&reasons); err != nil {
		t.Fatalf("read back rejection_reasons: %v", err)
	}
	if reasons == "[]" {
		t.Errorf("rejection_reasons after no-op Edit = %q, want unchanged (still populated) -- a no-op must not clear it", reasons)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d (no-op writes no history)", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d (no-op writes no audit)", n, beforeUpdated)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
		t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d (no-op must not transition)", n, beforeTransitioned)
	}
}

// EDIT-15/AC#5 (QA adversarial): after M5-05-01 widens Store.Edit's fixable
// set to include rejected, an ACCEPTED invoice must still refuse with
// ErrNotFixable -- the widened path stops at rejected, it does not silently
// swallow the rest of the terminal/in-flight states. Passes vacuously today
// (accepted was already refused before the widening) -- it exists to catch a
// FUTURE over-widening regression, mirroring
// TestStoreEdit_NonFixableStateRejected's queued case for the sibling
// still-refused state this subtask must not touch.
func TestStoreEdit_AcceptedStaysNotFixable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-15 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-15 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "EDIT-15", StatusAccepted)

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

	newVAT := "9.99"
	_, err := store.Edit(c, invID, UpdateInput{VAT: &newVAT})
	if !errors.Is(err, ErrNotFixable) {
		t.Fatalf("Edit(accepted invoice) err = %v, want ErrNotFixable", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusAccepted {
		t.Errorf("invoices.status after refused Edit = %q, want unchanged %q", status, StatusAccepted)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d", n, beforeUpdated)
	}
}

// EDIT-16 (QA adversarial): a crafted caller Subject that fails the
// content-write audit CHECK at step 7 -- which precedes BOTH the
// rejection_reasons clear and the demotion at step 8, in the SAME
// WithinRequestTenantTx -- rolls back the WHOLE edit: rejection_reasons is
// left BYTE-UNCHANGED (never observably cleared to '[]'), status stays
// rejected, no new history row, no new audit row. Mirrors
// TestStoreEdit_ContentAuditFailureRollsBackWholeEdit's injection shape for
// the widened rejected leg -- proves the clear is not a separate, unguarded
// write that could survive a later rollback in the same transaction.
func TestStoreEdit_ClearingIsAtomicWithTheDemotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	run := func(t *testing.T, label, craftedSubject string) {
		tenantID := seedTenant(t, super, "EDIT-16 "+label+" tenant")
		entityID := seedEntity(t, super, tenantID, "EDIT-16 entity")

		invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "EDIT-16-"+label, StatusRejected)
		reasonsJSON := `[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"}]`
		if _, err := super.Exec(ctx,
			`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, reasonsJSON, invID,
		); err != nil {
			t.Fatalf("seed rejection_reasons: %v", err)
		}

		var beforeReasons string
		if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID).Scan(&beforeReasons); err != nil {
			t.Fatalf("read back rejection_reasons (before): %v", err)
		}
		beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
		beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
		beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

		cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: craftedSubject, Role: "authenticated", TenantID: tenantID})
		newVAT := "9.50"
		_, err := store.Edit(cCrafted, invID, UpdateInput{VAT: &newVAT})
		if err == nil {
			t.Fatal("Edit with a crafted actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
		}
		if code := pgCode(err); code != "23514" {
			t.Fatalf("Edit with a crafted actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
		}

		var afterStatus, afterReasons string
		if err := super.QueryRow(ctx,
			`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
		).Scan(&afterStatus, &afterReasons); err != nil {
			t.Fatalf("read back status/rejection_reasons (after): %v", err)
		}
		if Status(afterStatus) != StatusRejected {
			t.Errorf("status after failed Edit = %q, want unchanged %q", afterStatus, StatusRejected)
		}
		if afterReasons != beforeReasons {
			t.Errorf("rejection_reasons after failed Edit = %q, want byte-unchanged %q (the clear must roll back too)", afterReasons, beforeReasons)
		}
		if afterReasons == "[]" {
			t.Errorf("rejection_reasons after failed Edit = %q, a rolled-back edit must never observably clear it", afterReasons)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
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
