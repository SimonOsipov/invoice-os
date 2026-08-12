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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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
	_, err = store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
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
	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
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
		_, err = store.Edit(cCrafted, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
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
	// seedEntityWithTIN, not seedEntity (INVCR-01-17, C7 fix): Store.Create now
	// derives supplier_tin/supplier_name from the entity, so the entity itself
	// must carry the "SUP-TIN-1"/"Supplier Co" fixture values this test's Edit
	// call re-sends below as a no-op. "SUP-TIN-1" is not a 12-bare-digit shape,
	// so MBSSupplierTIN leaves it untouched -- the derived value equals this
	// placeholder byte-for-byte.
	entityID := seedEntityWithTIN(t, super, tenantID, "Supplier Co", "SUP-TIN-1")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "EDIT-04",
		// SupplierTIN/SupplierName intentionally OMITTED: Store.Create derives
		// them from the entity above regardless of what's sent (C7 fix).
		BuyerTIN:  strPtr("BUY-TIN-1"),
		BuyerName: strPtr("Buyer Co"),
		Currency:  strPtr("NGN"),
		Subtotal:  strPtr("100.00"),
		VAT:       strPtr("7.00"),
		Total:     strPtr("107.00"),
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
	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{
		SupplierTIN:  strPtr("SUP-TIN-1"),
		SupplierName: strPtr("Supplier Co"),
		BuyerTIN:     strPtr("BUY-TIN-1"),
		BuyerName:    strPtr("Buyer Co"),
		Currency:     strPtr("NGN"),
		Subtotal:     strPtr("100.00"),
		VAT:          strPtr("7.00"),
		Total:        strPtr("107.00"),
	}})
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

	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{Total: strPtr("100.0")}})
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

	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.00")}})
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

	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.00")}})
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

// TestStoreEdit_ClearsKeepMarks (INVCR-01-15, D6, task-291, AC #8 -- named
// TestEdit_ClearsKeepMarks in the Test Specs table; renamed to this file's own
// TestStoreEdit_ prefix for consistency with every other test here): a kept draft,
// PATCHed on a header field, must come back with all three kept_as_is_* columns NULL
// -- a content change invalidates the recorded reason, since the reason no longer
// describes the invoice as it now stands.
func TestStoreEdit_ClearsKeepMarks(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-KEEP-CLEAR tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-KEEP-CLEAR entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-KEEP-CLEAR", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET violations = $1::jsonb, kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'kept before this edit' WHERE id = $2`,
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`, inv.ID,
	); err != nil {
		t.Fatalf("seed kept-as-is triple: %v", err)
	}

	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.50")}})
	if err != nil {
		t.Fatalf("Edit (content change on a kept draft): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want unchanged %q (draft has nothing to demote from)", got.Status, StatusDraft)
	}
	if got.KeptAsIsAt != nil || got.KeptAsIsBy != nil || got.KeptAsIsReason != nil {
		t.Errorf("Edit return kept_as_is triple = (at=%v by=%v reason=%v), want all nil (a content change invalidates the recorded reason)",
			got.KeptAsIsAt, got.KeptAsIsBy, got.KeptAsIsReason)
	}

	var at, by, reason *string
	if err := super.QueryRow(ctx,
		`SELECT kept_as_is_at::text, kept_as_is_by, kept_as_is_reason FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&at, &by, &reason); err != nil {
		t.Fatalf("read back kept_as_is triple: %v", err)
	}
	if at != nil || by != nil || reason != nil {
		t.Errorf("stored kept_as_is triple after the edit = (at=%v by=%v reason=%v), want all NULL", at, by, reason)
	}
}

// TestStoreEdit_NoOpEditDoesNotClearKeepMarks (extra, task-291): the DB-authoritative
// no-op check (step 6) must win BEFORE the kept-marks clear -- a resent-unchanged PATCH
// is not a "content change" and must leave a kept invoice's reason exactly as it was,
// mirroring [A6]'s general "idempotence applies to draft" rule this file already pins
// for TestStoreEdit_DraftNoOpWritesNothing above.
func TestStoreEdit_NoOpEditDoesNotClearKeepMarks(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-KEEP-NOOP tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-KEEP-NOOP entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-KEEP-NOOP", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET violations = $1::jsonb, kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'still valid' WHERE id = $2`,
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`, inv.ID,
	); err != nil {
		t.Fatalf("seed kept-as-is triple: %v", err)
	}

	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.00")}}) // resent unchanged
	if err != nil {
		t.Fatalf("Edit (no-op resend on a kept draft): want success, got: %v", err)
	}
	if got.KeptAsIsAt == nil || got.KeptAsIsBy == nil || got.KeptAsIsReason == nil {
		t.Errorf("Edit (no-op) return kept_as_is triple = (at=%v by=%v reason=%v), want all still set (a no-op is not a content change)",
			got.KeptAsIsAt, got.KeptAsIsBy, got.KeptAsIsReason)
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

	if _, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{}}); !errors.Is(err, ErrValidation) {
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

	_, err = store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("not-a-number")}})
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
		if _, err := store.Edit(c, bogusID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.00")}}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Edit(nonexistent id) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("cross-tenant id", func(t *testing.T) {
		tenantA := seedTenant(t, super, "EDIT-10 tenant A")
		tenantB := seedTenant(t, super, "EDIT-10 tenant B")
		entityB := seedEntity(t, super, tenantB, "EDIT-10 B entity")
		invoiceB := seedInvoice(t, super, tenantB, entityB, "EDIT-10-B")

		cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

		if _, err := store.Edit(cA, invoiceB, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.00")}}); !errors.Is(err, ErrNotFound) {
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
	fp := contentFingerprint(inv, inv.LineItems)
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
	edited, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
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
	freshFP := contentFingerprint(edited, edited.LineItems)
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
	// seedEntityWithTIN, not seedEntity (INVCR-01-17, C7 fix): see EDIT-04's
	// identical note above -- the entity now supplies the "SUP-TIN-1"/
	// "Supplier Co" values the sibling-field assertions below expect.
	entityID := seedEntityWithTIN(t, super, tenantID, "Supplier Co", "SUP-TIN-1")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "EDIT-12",
		// SupplierTIN/SupplierName intentionally OMITTED: Store.Create derives
		// them from the entity above regardless of what's sent (C7 fix).
		BuyerTIN:  strPtr("BUY-TIN-1"),
		BuyerName: strPtr("Buyer Co"),
		Currency:  strPtr("NGN"),
		Subtotal:  strPtr("100.00"),
		VAT:       strPtr("7.00"),
		Total:     strPtr("107.00"),
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

	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr("New Buyer Co")}})
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

// --- M5-05-01 (task-237): the widened rejected leg of the fix loop --------------------
//
// Spec-to-test map (Test Specs table, M5-05-01 (task-237)):
//
//	AC#3 TestStoreEdit_RejectedContentChangeDemotesAndRetainsReasons (M5-09-02/task-255 rename)
//	AC#4 TestStoreEdit_RejectedNoOpKeepsStatusAndReasons
//	AC#5 TestStoreEdit_AcceptedStaysNotFixable
//	AC#3/#5 TestStoreEdit_RejectedLegContentAuditFailureRollsBackWholeEdit (M5-09-02/task-255 rename)
//
// seedInvoiceAtStatus is defined in transition_adversarial_test.go (same
// package). As of this commit, rejection_reasons still has no Go-side field
// on Invoice (invoiceColumns/scanInvoice deliberately does not project it,
// store.go) -- every assertion below reads it back with a raw `::text`
// SELECT, mirroring internal/platform/db/invoices_fiscal_rls_test.go's own
// convention. M5-05-02 (task-238) adds that field (RejectionReasons
// json.RawMessage) and projects it, but does NOT change any assertion in
// this file: these M5-05-01 (task-237) tests seed state directly via the superuser
// pool (bypassing Store.ApplyValidation/Store.Transition, the only real
// writers), and reading through Store.Edit's projection instead would only
// prove 02's read path, not this file's own write/clear behavior under
// test -- so the raw `::text` reads stay, unchanged, even after 02 lands.

// EDIT-13/AC#3 (M5-09-02/task-255: RE-BASELINED -- deliberately reverses
// M5-05's [reason-lifecycle] wipe-on-demotion, NOT a weakened test, see
// story Decision [rejection-history]): a content-changing Edit on a REJECTED
// invoice demotes it to draft and RETAINS rejection_reasons byte-identical
// to what was seeded, and writes exactly one (rejected,draft) history row
// plus one invoice.transitioned + one invoice.updated audit row, all in the
// SAME transaction -- mirrors TestStoreEdit_ValidatedContentChangeDemotes's
// shape for the widened leg.
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
func TestStoreEdit_RejectedContentChangeDemotesAndRetainsReasons(t *testing.T) {
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
	// jsonb's ::text output is not byte-identical to the literal above (it
	// normalizes whitespace) -- read back the DB's own rendering right after
	// the seed and compare against THAT, mirroring the byte-identity idiom
	// already used below for the failure-rollback siblings.
	var seededReasons string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID).Scan(&seededReasons); err != nil {
		t.Fatalf("read back seeded rejection_reasons: %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	newVAT := "9.50"
	got, err := store.Edit(c, invID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
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
	if reasons != seededReasons {
		t.Errorf("invoices.rejection_reasons after Edit = %q, want byte-identical to the seed %q (retained, not cleared)", reasons, seededReasons)
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

	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.00")}})
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

// EDIT-15/AC#5 (QA adversarial): after M5-05-01 (task-237) widens Store.Edit's fixable
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
	_, err := store.Edit(c, invID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
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

// TestStoreEdit_NonFixableStatesRejectedTable (INVED-01-03/task-264,
// INV-03-T5, widened): Store.Edit refuses ErrNotFixable, with nothing
// written, for each of queued/submitted/accepted/failed. Widens
// TestStoreEdit_NonFixableStateRejected's single queued case and
// TestStoreEdit_AcceptedStaysNotFixable's single accepted case with the two
// genuinely-uncovered non-fixable statuses, submitted and failed. This
// subtask's R0 step only adds stub canEdit/canRevalidate functions --
// Store.Edit's call site (store.go, the [A8] fixable-state guard) is left
// UNCHANGED until R2, so this exercises EXISTING, unmodified behaviour and is
// expected to pass immediately (not go red).
func TestStoreEdit_NonFixableStatesRejectedTable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	for _, status := range []Status{StatusQueued, StatusSubmitted, StatusAccepted, StatusFailed} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			tenantID := seedTenant(t, super, "INV-03-T5 "+string(status)+" tenant")
			entityID := seedEntity(t, super, tenantID, "INV-03-T5 entity")
			c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "INV-03-T5-"+string(status), status)

			var before Invoice
			if err := scanInvoice(super.QueryRow(ctx, `SELECT `+invoiceColumns+` FROM invoices WHERE id = $1`, invID), &before); err != nil {
				t.Fatalf("snapshot before: %v", err)
			}
			beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
			beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

			newVAT := "9.00"
			_, err := store.Edit(c, invID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
			if !errors.Is(err, ErrNotFixable) {
				t.Fatalf("Edit(%s invoice) err = %v, want ErrNotFixable", status, err)
			}

			var after Invoice
			if err := scanInvoice(super.QueryRow(ctx, `SELECT `+invoiceColumns+` FROM invoices WHERE id = $1`, invID), &after); err != nil {
				t.Fatalf("snapshot after: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("invoices row changed after refused Edit(%s): before %+v, after %+v, want byte-identical", status, before, after)
			}
			if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
				t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
			}
			if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
				t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d", n, beforeUpdated)
			}
		})
	}
}

// EDIT-16 (M5-09-02/task-255: RENAMED from TestStoreEdit_ClearingIsAtomicWithTheDemotion
// -- its original premise, that a rejection_reasons CLEAR rolls back
// atomically with the demotion, no longer applies: M5-09-02 removes that
// clear from Store.Edit's rejected leg entirely, so there is nothing left on
// this path to clear -- this re-baseline reverses the premise, it does not
// weaken the test). What survives is the rejected-leg mirror of
// TestStoreEdit_ContentAuditFailureRollsBackWholeEdit's injection shape,
// re-framed: a crafted caller Subject that fails the content-write audit
// CHECK at step 7 -- which precedes the demotion at step 8, in the SAME
// WithinRequestTenantTx -- rolls back the WHOLE edit: rejection_reasons is
// left BYTE-UNCHANGED and still populated, status stays rejected, no new
// history row, no new audit row. Proves retention holds across a
// rolled-back edit too, not just a successful one.
func TestStoreEdit_RejectedLegContentAuditFailureRollsBackWholeEdit(t *testing.T) {
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
		_, err := store.Edit(cCrafted, invID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
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
			t.Errorf("rejection_reasons after failed Edit = %q, want byte-unchanged %q (retention holds across a rolled-back edit too)", afterReasons, beforeReasons)
		}
		if afterReasons == "[]" {
			t.Errorf("rejection_reasons after failed Edit = %q, want still populated (nothing on this path ever touches rejection_reasons, successful or not)", afterReasons)
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

// --- M5-05-01 (task-237) (QA Mode B adversarial, Stage 4): additional coverage beyond
// the Test Specs table -- concurrency, a genuinely-after-the-clear failure,
// and cross-tenant refusal (the last already lives in
// cross_tenant_integration_test.go as TestRLS_InvoicesEditRejectedCrossTenantRefused).

// TestStoreEdit_ConcurrentEditsOnRejectedInvoiceSerializeToOneDemotion (QA
// Mode B adversarial): N concurrent Store.Edit calls against the SAME
// rejected invoice, each targeting a pairwise-DISTINCT VAT value (and none
// equal to the seeded original) -- mirrors
// TestApplyValidation_ConcurrentSerializesToOneWinner's shape (apply_validation_test.go),
// but Store.Edit's guard is looser than ApplyValidation's (draft, validated,
// AND rejected are all fixable), so unlike that sibling test there are no
// "loser" errors here -- every call succeeds, because after the FIRST
// winner's FOR UPDATE-serialized demotion the row is `draft`, which is
// STILL fixable for every subsequent caller.
//
// Because every target VAT is pairwise distinct (and distinct from the
// original), whichever order the FOR UPDUE lock serializes the N calls in,
// each call's own `before` snapshot (the previous winner's committed VAT, or
// the original for whoever goes first) always differs from its own target --
// so this is a DETERMINISTIC, order-independent assertion, not a race:
//   - exactly ONE (rejected,draft) demotion row -- only the very first
//     lock-holder observes before.Status == rejected; every later holder
//     observes draft and skips step 8 entirely
//   - exactly N invoice.updated audit rows -- every call writes a genuine
//     content change relative to ITS OWN before-snapshot
//   - final rejection_reasons stays byte-identical to the seed (M5-09-02:
//     retained, not cleared -- nothing on this path touches the column,
//     concurrently or otherwise)
//   - final status is draft
func TestStoreEdit_ConcurrentEditsOnRejectedInvoiceSerializeToOneDemotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-CONC tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-CONC entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "EDIT-CONC", StatusRejected)
	reasonsJSON := `[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, reasonsJSON, invID,
	); err != nil {
		t.Fatalf("seed rejection_reasons: %v", err)
	}
	var seededReasons string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID).Scan(&seededReasons); err != nil {
		t.Fatalf("read back seeded rejection_reasons: %v", err)
	}

	// Original seeded VAT is NULL (seedInvoiceAtStatus doesn't set one) --
	// every target below is non-nil and pairwise distinct, so each is a
	// genuine change relative to whatever came before it.
	targets := []string{"11.00", "22.00", "33.00", "44.00"}
	n := len(targets)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			vat := targets[i]
			_, errs[i] = store.Edit(c, invID, EditInput{UpdateInput: UpdateInput{VAT: &vat}})
		}()
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("concurrent Edit[%d] returned unexpected error: %v", i, e)
		}
	}

	var status, reasons string
	if err := super.QueryRow(ctx,
		`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &reasons); err != nil {
		t.Fatalf("read back status/rejection_reasons: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("status after concurrent Edits = %q, want %q", status, StatusDraft)
	}
	if reasons != seededReasons {
		t.Errorf("rejection_reasons after concurrent Edits = %q, want byte-identical to the seed %q (retained, never cleared)", reasons, seededReasons)
	}

	if hn := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'rejected' AND to_status = 'draft'`, invID,
	); hn != 1 {
		t.Errorf("invoice_status_history (rejected,draft) rows = %d, want exactly 1 (only the FOR UPDATE lock's first holder demotes)", hn)
	}
	if an := auditCount(t, app, tenantID, "invoice.updated"); an != n {
		t.Errorf("audit_log invoice.updated rows = %d, want %d (every one of the %d concurrent calls is a genuine content change relative to its own before-snapshot)", an, n, n)
	}
	if tn := auditCount(t, app, tenantID, "invoice.transitioned"); tn != 1 {
		t.Errorf("audit_log invoice.transitioned rows = %d, want exactly 1 (mirrors the single history demotion row)", tn)
	}
}

// TestStoreEdit_FailureAfterReasonsClearStillRollsBack was RETIRED by
// M5-09-02 (task-255): it existed to prove a rejection_reasons CLEAR inside
// Store.Edit's rejected-leg demotion rolled back atomically with a LATER
// (post-clear) legality failure. M5-09-02 removes that clear entirely --
// Store.Edit no longer touches rejection_reasons on any path -- so the
// premise this test was built to protect no longer exists. Its replacement,
// TestStoreTransition_AcceptedClearFailureStillRollsBack (transition_test.go),
// re-premises the identical atomicity shape onto the clear's new home: the
// conditional `rejection_reasons = '[]'` clause transitionTx's UPDATE gains
// for target == StatusAccepted.

// --- INVED-01-02 QA (Mode B): Store.Edit's shape is untouched by a LINED
// invoice -----------------------------------------------------------------

// TestStoreEdit_LinedInvoiceDemotesAndLeavesLinesUntouchedWithNilLineItems
// (QA adversarial, INVED-01-02 Part D/E; INVERTED by INVED-01-04, spec T22):
// no existing Store.Edit test exercises an invoice that actually HAS line
// items -- every one of them above is header-only. This closes that gap and
// pins two things at once:
//
//  1. INVERTED (T22, [edit-response-carries-lines]): Edit's RETURN now
//     carries the invoice's LineItems hydrated on EVERY success path,
//     including a header-only edit with `in.LineItems == nil` -- subtask
//     02's QA originally asserted the OPPOSITE (`got.LineItems != nil ->
//     error`), deferring the reversal to "subtask 04's territory". This IS
//     that territory: got.LineItems must be non-nil and byte-identical to
//     the 2 stored rows, because nothing on a header-only path (in.LineItems
//     == nil) ever touches them.
//  2. The line_items ROWS themselves are byte-unchanged after the edit --
//     Edit only reads lines (via hydrateLinesTx, for the fingerprint AND now
//     the response) when in.LineItems == nil; it never replaces them on
//     this path.
//
// The demotion itself (validated -> draft on a real header content change)
// still fires exactly as TestStoreEdit_ValidatedContentChangeDemotes proves
// for a header-only invoice -- this confirms lines-in-the-picture doesn't
// perturb that path (the preFP/afterFP comparison now also hashes
// beforeLines on both sides, so a real header change must still show up as
// "changed" despite the extra line content folded into both hashes).
func TestStoreEdit_LinedInvoiceDemotesAndLeavesLinesUntouchedWithNilLineItems(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "INV-02-EDIT-LINES tenant")
	entityID := seedEntity(t, super, tenantID, "INV-02-EDIT-LINES entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	priceA, priceB := "10.00", "20.00"
	inv, err := store.Create(c, CreateInput{
		EntityID: entityID, InvoiceNumber: "INV-02-EDIT-LINES", VAT: strPtr("7.00"),
		LineItems: []LineItemInput{
			{Description: &descA, UnitPrice: &priceA},
			{Description: &descB, UnitPrice: &priceB},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(inv.LineItems) != 2 {
		t.Fatalf("Create: len(LineItems) = %d, want 2", len(inv.LineItems))
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	// Snapshot the line_items rows verbatim before the edit.
	beforeLines := readLineItemsForTest(t, super, inv.ID)
	if len(beforeLines) != 2 {
		t.Fatalf("seeded line_items rows = %d, want 2", len(beforeLines))
	}

	newVAT := "9.50"
	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err != nil {
		t.Fatalf("Edit (content change on a lined, validated invoice): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted)", got.Status, StatusDraft)
	}
	// INVERTED by INVED-01-04 (T22, [edit-response-carries-lines]): was
	// `got.LineItems != nil -> error`. Store.Edit's return now carries
	// hydrated LineItems on every success path, header-only edits included.
	if got.LineItems == nil {
		t.Fatalf("Edit returned LineItems = nil, want the invoice's 2 lines hydrated on every success path, including this header-only edit ([edit-response-carries-lines])")
	}
	if !reflect.DeepEqual(got.LineItems, beforeLines) {
		t.Errorf("Edit returned LineItems = %+v, want byte-identical to the untouched stored rows %+v -- a header-only edit (in.LineItems == nil) must not perturb the lines", got.LineItems, beforeLines)
	}

	afterLines := readLineItemsForTest(t, super, inv.ID)
	if !reflect.DeepEqual(beforeLines, afterLines) {
		t.Errorf("line_items rows changed by Edit: before %+v, after %+v -- Store.Edit must not write lines", beforeLines, afterLines)
	}
}

// readLineItemsForTest reads one invoice's line_items ordered line_no ASC,
// as the superuser -- a DIFFERENT projection than lineItemColumns (fewer
// columns, no tenant_id needed) so this stays a read-back check independent
// of hydrateLinesTx/scanLineItem's own correctness.
func readLineItemsForTest(t *testing.T, super *pgxpool.Pool, invoiceID string) []LineItem {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id, line_no, description, quantity::text, unit_price::text, line_total::text, line_tax::text `+
			`FROM line_items WHERE invoice_id = $1 ORDER BY line_no ASC`, invoiceID,
	)
	if err != nil {
		t.Fatalf("read line_items: %v", err)
	}
	defer rows.Close()
	var out []LineItem
	for rows.Next() {
		var li LineItem
		if err := rows.Scan(&li.ID, &li.LineNo, &li.Description, &li.Quantity, &li.UnitPrice, &li.LineTotal, &li.LineTax); err != nil {
			t.Fatalf("scan line_items row: %v", err)
		}
		out = append(out, li)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate line_items: %v", err)
	}
	return out
}

// --- INVED-01-04 (task-265): RED specs for line-item mutation in Store.Edit
// -----------------------------------------------------------------------
//
// Written BEFORE replaceLinesTx/the widened Store.Edit body exist (RED
// against R0's store.go, which takes EditInput but ignores in.LineItems
// entirely): every assertion below fails on its OWN target value -- a lines-
// only edit silently becomes a no-op (nothing changed to hash against),
// got.LineItems stays nil, ApplyValidation's re-check goes stale -- never a
// compile error. A few specs (T9, T10, T14, T16) already pass at R0 as
// regression guards (existing guards that do not depend on line writes at
// all); that is expected and documented per-spec in the RALPH report, not a
// sign the spec is wrong.
//
// Spec-to-test map (Test Specs table, INVED-01-04 / task-265):
//
//	T1  TestStoreEdit_LineChangeOnValidatedDemotesAndAuditsLineItemsField
//	T2  TestStoreEdit_ReplaceLinesAppendsAndRenumbers
//	T3  TestStoreEdit_ReplaceLinesDropsAndRenumbers
//	T4  TestStoreEdit_NoOpReturnsFreshLineIDsNeverBeforeLines
//	T5  TestStoreEdit_NumericScaleOnlyLineResendIsNoOp
//	T6  TestStoreEdit_RejectedLineChangeDemotesRetainsReasons
//	T7  TestStoreEdit_EmptyLineItemsRemovesAllLinesGuardWidened
//	T8  -- no new test; the existing (T22-inverted)
//	    TestStoreEdit_LinedInvoiceDemotesAndLeavesLinesUntouchedWithNilLineItems's
//	    second arm IS T8 (nil LineItems leaves lines byte-identical incl ids)
//	T9  TestStoreEdit_LineChangeOnNonEditableStatusRefusedLinesUntouched
//	T10 -- no new test; TestStoreEdit_AllNilRejected already covers it
//	    (EditInput{UpdateInput{}} has LineItems == nil by zero value)
//	T11 TestStoreEdit_LineChangeContentAuditFailureRollsBackLineReplaceToo
//	T12 TestBatchSubmit_LineEditedInvoiceRefusedNotValidated (batch_submit_adversarial_test.go)
//	T13 TestStoreEdit_ConcurrentReplaceAllLastWriterWins
//	T14 TestStoreEdit_CrossTenantLineChangeRefusedLinesUntouched
//	T15 TestStoreEdit_DraftLineChangeNoDemotion
//	T16 TestStoreEdit_LineChangeNeverRewritesTotals
//	T17 TestStoreEdit_ReturnedLineItemsMatchStoredBothChangedAndNoOpPaths
//	T18 TestStoreEdit_DemoteThenRevalidateSucceedsWithLines
//	T19 TestTransition_LineEditedInvoiceIllegalDraftToQueued (transition_adversarial_test.go)
//	T20 TestStoreEdit_DemotionDerivedFromLegalTransitionsNotLiteral
//	T21 TestStoreEdit_MalformedLineNumericValidationErrorZeroRowsWritten
//	T22 TestStoreEdit_LinedInvoiceDemotesAndLeavesLinesUntouchedWithNilLineItems (inverted in place, above)

// seedLinedInvoiceAtStatus creates a real invoice WITH real line_items via
// Store.Create (over the app-role pool, so the write path runs exactly as
// production does) then, unless status is StatusDraft, force-writes
// invoices.status directly as the superuser -- mirroring
// seedInvoiceAtStatus's technique (transition_adversarial_test.go) for
// statuses Store.Transition cannot reach in a single hop, extended to a
// LINED invoice (no existing helper seeds lines at an arbitrary status).
// Returns Create's own return value (LineItems populated) with Status
// overwritten to match the forced value.
func seedLinedInvoiceAtStatus(t *testing.T, super *pgxpool.Pool, store *Store, c context.Context, entityID, number string, status Status, lines []LineItemInput) Invoice {
	t.Helper()
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: number, LineItems: lines})
	if err != nil {
		t.Fatalf("seed: Create(%s): %v", number, err)
	}
	if status != StatusDraft {
		if _, err := super.Exec(context.Background(),
			`UPDATE invoices SET status = $1 WHERE id = $2`, string(status), inv.ID,
		); err != nil {
			t.Fatalf("seed: force status of %s to %q: %v", number, status, err)
		}
		inv.Status = status
	}
	return inv
}

// auditFields returns the "fields" array of the newest audit_log row for
// tenantID+event -- mirrors auditActor's shape/RLS scoping (store_test.go).
// No existing helper asserts on the payload's fields key; INVED-01-04 needs
// it to prove the audit fields array literally contains "line_items" (T1/T7),
// as a concrete Go []string -- a payload that marshaled "fields":null (the
// nil-vs-empty-slice trap this repo already lost a CI gate to once, M4-16)
// fails json.Unmarshal into []string with a clear error, not a silent zero
// value, so that failure mode is caught too.
func auditFields(t *testing.T, pool *pgxpool.Pool, tenantID, event string) []string {
	t.Helper()
	ctx := context.Background()
	var raw json.RawMessage
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT payload->'fields' FROM audit_log WHERE event = $1 ORDER BY created_at DESC LIMIT 1`, event,
		).Scan(&raw)
	}); err != nil {
		t.Fatalf("read audit_log payload->'fields' for event %q: %v", event, err)
	}
	var fields []string
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal audit_log payload->'fields' %s into []string: %v", raw, err)
	}
	return fields
}

// T1: a line-only edit (no header fields) on a validated, lined invoice
// demotes it to draft exactly like a header change would -- one
// invoice.updated audit row whose fields array contains "line_items", one
// validated->draft history row, one invoice.transitioned audit row.
func TestStoreEdit_LineChangeOnValidatedDemotesAndAuditsLineItemsField(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T1 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T1 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	priceA, priceB := "10.00", "20.00"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T1", StatusValidated, []LineItemInput{
		{Description: &descA, UnitPrice: &priceA},
		{Description: &descB, UnitPrice: &priceB},
	})

	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	newDescA := "Widget v2"
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &newDescA, UnitPrice: &priceA},
		{Description: &descB, UnitPrice: &priceB},
	}})
	if err != nil {
		t.Fatalf("Edit (line-only change on a validated, lined invoice): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted, identical consequences to a header change)", got.Status, StatusDraft)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv.ID,
	); n != 1 {
		t.Errorf("invoice_status_history (validated,draft) rows = %d, want exactly 1", n)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned+1 {
		t.Errorf("audit_log invoice.transitioned rows = %d, want %d", n, beforeTransitioned+1)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("audit_log invoice.updated rows = %d, want %d", n, beforeUpdated+1)
	}
	if fields := auditFields(t, app, tenantID, "invoice.updated"); !reflect.DeepEqual(fields, []string{"line_items"}) {
		t.Errorf("invoice.updated audit fields = %v, want exactly [\"line_items\"] (a lines-only edit sent no header fields)", fields)
	}
}

// T2: replacing a validated invoice's 2 lines with 3 (the 2 originals plus
// one appended) demotes it and renumbers 1,2,3 by array position.
func TestStoreEdit_ReplaceLinesAppendsAndRenumbers(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T2 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T2 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	desc1, desc2, desc3 := "Line1", "Line2", "Line3-appended"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T2", StatusValidated, []LineItemInput{
		{Description: &desc1}, {Description: &desc2},
	})

	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &desc1}, {Description: &desc2}, {Description: &desc3},
	}})
	if err != nil {
		t.Fatalf("Edit (append a line): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want %q (demoted)", got.Status, StatusDraft)
	}

	rows := readLineItemsForTest(t, super, inv.ID)
	if len(rows) != 3 {
		t.Fatalf("line_items rows = %d, want 3", len(rows))
	}
	for i, r := range rows {
		if r.LineNo != i+1 {
			t.Errorf("rows[%d].LineNo = %d, want %d (contiguous 1..N by array position)", i, r.LineNo, i+1)
		}
	}
}

// T3: replacing a validated invoice's 3 lines with only lines 1 and 3
// demotes it, renumbers the survivors 1,2, and the dropped line's content is
// gone.
func TestStoreEdit_ReplaceLinesDropsAndRenumbers(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T3 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T3 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	desc1, desc2, desc3 := "Line1", "Line2-dropped", "Line3"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T3", StatusValidated, []LineItemInput{
		{Description: &desc1}, {Description: &desc2}, {Description: &desc3},
	})

	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &desc1}, {Description: &desc3},
	}})
	if err != nil {
		t.Fatalf("Edit (drop a line): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want %q (demoted)", got.Status, StatusDraft)
	}

	rows := readLineItemsForTest(t, super, inv.ID)
	if len(rows) != 2 {
		t.Fatalf("line_items rows = %d, want 2", len(rows))
	}
	if rows[0].LineNo != 1 || rows[1].LineNo != 2 {
		t.Errorf("line_no sequence = [%d,%d], want [1,2] (renumbered contiguously)", rows[0].LineNo, rows[1].LineNo)
	}
	for _, r := range rows {
		if r.Description != nil && *r.Description == desc2 {
			t.Errorf("dropped line content %q is still present after replace-all", desc2)
		}
	}
}

// T4: re-sending both lines byte-identically (no header fields) on a
// validated invoice is a content no-op (stays validated, no audit, no
// history) -- but replace-all still deletes+re-inserts, so the returned
// LineItems must carry the FRESH stored ids, matching a superuser read-back,
// never the pre-edit ids.
func TestStoreEdit_NoOpReturnsFreshLineIDsNeverBeforeLines(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T4 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T4 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	priceA, priceB := "10.00", "20.00"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T4", StatusValidated, []LineItemInput{
		{Description: &descA, UnitPrice: &priceA},
		{Description: &descB, UnitPrice: &priceB},
	})
	beforeLines := readLineItemsForTest(t, super, inv.ID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &descA, UnitPrice: &priceA},
		{Description: &descB, UnitPrice: &priceB},
	}})
	if err != nil {
		t.Fatalf("Edit (byte-identical line resend): want success (no-op), got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("Edit returned status = %q, want unchanged %q (content-identical resend is a no-op)", got.Status, StatusValidated)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d (no-op)", n, beforeUpdated)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d (no-op)", n, beforeHistory)
	}

	afterLines := readLineItemsForTest(t, super, inv.ID)
	if reflect.DeepEqual(beforeLines, afterLines) {
		t.Errorf("line_items rows after a content-identical resend = %+v, want CHURNED ids (replace-all deletes+re-inserts even on a no-op) -- identical to before %+v", afterLines, beforeLines)
	}
	if got.LineItems == nil {
		t.Fatal("Edit returned LineItems = nil, want the post-write (fresh-id) rows hydrated even on the no-op path")
	}
	if !reflect.DeepEqual(got.LineItems, afterLines) {
		t.Errorf("Edit returned LineItems = %+v, want byte-identical to the post-write stored rows %+v (never beforeLines)", got.LineItems, afterLines)
	}
}

// T5: re-sending a line whose unit_price scale differs but value is
// identical ("100.0" against a stored "100.00") is a genuine no-op, because
// numeric(14,2) normalizes the ::text read-back -- this only holds if the
// post-write hash (and the response) come from replaceLinesTx's RETURNING
// rows, never a slice synthesized from the raw input.
func TestStoreEdit_NumericScaleOnlyLineResendIsNoOp(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T5 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T5 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA := "Widget"
	price := "100.00"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T5", StatusValidated, []LineItemInput{
		{Description: &descA, UnitPrice: &price},
	})
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	rescaledPrice := "100.0"
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &descA, UnitPrice: &rescaledPrice},
	}})
	if err != nil {
		t.Fatalf("Edit (scale-only line resend): want success (no-op), got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf(`Edit returned status = %q, want unchanged %q -- "100.0" against stored "100.00" is a genuine no-op once numeric(14,2) normalizes it`, got.Status, StatusValidated)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d (false demotion)", n, beforeUpdated)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d (false demotion)", n, beforeHistory)
	}
	if len(got.LineItems) != 1 || got.LineItems[0].UnitPrice == nil || *got.LineItems[0].UnitPrice != "100.00" {
		t.Errorf(`Edit returned LineItems = %+v, want exactly one row with unit_price "100.00" -- the post-hash (and the response) must come from replaceLinesTx's RETURNING (DB-normalized), never a slice synthesized from the raw "100.0" input`, got.LineItems)
	}
}

// T6: a line change on a rejected, lined invoice demotes it to draft and
// RETAINS both rejection reasons (story Constraint, Core AC 6) -- identical
// retention behavior to the header-change path.
func TestStoreEdit_RejectedLineChangeDemotesRetainsReasons(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T6 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T6 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T6", StatusRejected, []LineItemInput{
		{Description: &descA}, {Description: &descB},
	})
	reasonsJSON := `[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"},` +
		`{"code":"CURRENCY_INVALID","message":"unrecognised currency code","path":"currency"}]`
	if _, err := super.Exec(ctx, `UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, reasonsJSON, inv.ID); err != nil {
		t.Fatalf("seed rejection_reasons: %v", err)
	}
	var seededReasons string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, inv.ID).Scan(&seededReasons); err != nil {
		t.Fatalf("read back seeded rejection_reasons: %v", err)
	}

	beforeHistory := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'rejected' AND to_status = 'draft'`, inv.ID,
	)

	newQty := "3"
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &descA, Quantity: &newQty}, {Description: &descB},
	}})
	if err != nil {
		t.Fatalf("Edit (line change on a rejected, lined invoice): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted)", got.Status, StatusDraft)
	}
	if string(got.RejectionReasons) != seededReasons {
		t.Errorf("Edit returned rejection_reasons = %s, want retained %s", got.RejectionReasons, seededReasons)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'rejected' AND to_status = 'draft'`, inv.ID,
	); n != beforeHistory+1 {
		t.Errorf("invoice_status_history (rejected,draft) rows = %d, want %d", n, beforeHistory+1)
	}
}

// T7: `line_items: []` (present-but-empty) on a validated invoice with no
// header fields removes every line -- also proving the all-nil guard was
// widened to admit a lines-only edit.
func TestStoreEdit_EmptyLineItemsRemovesAllLinesGuardWidened(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T7 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T7 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T7", StatusValidated, []LineItemInput{
		{Description: &descA}, {Description: &descB},
	})

	empty := []LineItemInput{}
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &empty})
	if err != nil {
		t.Fatalf("Edit (line_items: [], no header fields): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted)", got.Status, StatusDraft)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE invoice_id = $1`, inv.ID); n != 0 {
		t.Errorf("line_items rows = %d, want 0 (present-but-empty removes every line)", n)
	}
	if fields := auditFields(t, app, tenantID, "invoice.updated"); !reflect.DeepEqual(fields, []string{"line_items"}) {
		t.Errorf(`invoice.updated audit fields = %v, want exactly ["line_items"]`, fields)
	}
}

// T9: a line change on any non-editable status (queued/submitted/accepted/
// failed) is refused with ErrNotFixable and writes zero line rows -- the
// fixable-state guard runs before any content is touched, independent of
// whether the caller sent a header field, a line array, or both.
func TestStoreEdit_LineChangeOnNonEditableStatusRefusedLinesUntouched(t *testing.T) {
	for _, status := range []Status{StatusQueued, StatusSubmitted, StatusAccepted, StatusFailed} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			super, app := dbTestPools(t)
			ctx := context.Background()
			store := NewStore(app)

			tenantID := seedTenant(t, super, "EDIT-T9 tenant "+string(status))
			entityID := seedEntity(t, super, tenantID, "EDIT-T9 entity")
			c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

			descA := "Widget"
			inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T9-"+string(status), status, []LineItemInput{
				{Description: &descA},
			})
			beforeLines := readLineItemsForTest(t, super, inv.ID)
			beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
			beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

			newDesc := "Gadget"
			_, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{{Description: &newDesc}}})
			if !errors.Is(err, ErrNotFixable) {
				t.Fatalf("Edit(%s invoice, line change) err = %v, want ErrNotFixable", status, err)
			}

			afterLines := readLineItemsForTest(t, super, inv.ID)
			if !reflect.DeepEqual(beforeLines, afterLines) {
				t.Errorf("line_items rows changed by a refused Edit: before %+v, after %+v", beforeLines, afterLines)
			}
			if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
				t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
			}
			if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
				t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d", n, beforeUpdated)
			}
		})
	}
}

// T11: a crafted 256-char actor Subject that fails the content-write audit
// CHECK rolls back a LINES-ONLY edit's whole tx too -- proving the line
// replace is inside the same atomic unit as the header path already proves
// (TestStoreEdit_ContentAuditFailureRollsBackWholeEdit).
func TestStoreEdit_LineChangeContentAuditFailureRollsBackLineReplaceToo(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T11 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T11 entity")
	cNormal := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA := "Widget"
	inv := seedLinedInvoiceAtStatus(t, super, store, cNormal, entityID, "EDIT-T11", StatusValidated, []LineItemInput{
		{Description: &descA},
	})
	beforeLines := readLineItemsForTest(t, super, inv.ID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: strings.Repeat("a", 256), Role: "authenticated", TenantID: tenantID})
	newDesc := "Gadget"
	_, err := store.Edit(cCrafted, inv.ID, EditInput{LineItems: &[]LineItemInput{{Description: &newDesc}}})
	if err == nil {
		t.Fatal("Edit (lines-only, crafted 256-char actor) succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("Edit (lines-only, crafted actor): pgCode = %q, want 23514: %v", code, err)
	}

	var afterStatus string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&afterStatus); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(afterStatus) != StatusValidated {
		t.Errorf("status after failed Edit = %q, want unchanged %q", afterStatus, StatusValidated)
	}
	afterLines := readLineItemsForTest(t, super, inv.ID)
	if !reflect.DeepEqual(beforeLines, afterLines) {
		t.Errorf("line_items rows after failed Edit = %+v, want byte-unchanged (incl ids) %+v -- the line replace must roll back with the rest of the tx", afterLines, beforeLines)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
}

// T13: two concurrent Store.Edit calls against the SAME validated invoice,
// each replacing the full 2-line set with a different pair of descriptions,
// serialize on the invoices-row FOR UPDATE lock; both succeed; exactly ONE
// validated->draft history row is written (only the first lock-holder
// demotes; the second observes draft and has nothing left to demote); the
// final stored set is EXACTLY one editor's whole payload, never a mix of the
// two ([line-update-shape]: replace-all is last-writer-wins over the WHOLE
// set, it does not merge the way diffEditInput merges header fields).
func TestStoreEdit_ConcurrentReplaceAllLastWriterWins(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T13 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T13 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T13", StatusValidated, []LineItemInput{
		{Description: &descA}, {Description: &descB},
	})

	descA1, descB1 := "Widget (editor 1)", "Gadget (editor 1)"
	descA2, descB2 := "Widget (editor 2)", "Gadget (editor 2)"
	payload1 := []LineItemInput{{Description: &descA1}, {Description: &descB1}}
	payload2 := []LineItemInput{{Description: &descA2}, {Description: &descB2}}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = store.Edit(c, inv.ID, EditInput{LineItems: &payload1})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = store.Edit(c, inv.ID, EditInput{LineItems: &payload2})
	}()
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent Edit[%d] err = %v, want nil", i, e)
		}
	}

	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv.ID,
	); n != 1 {
		t.Errorf("invoice_status_history (validated,draft) rows = %d, want exactly 1 -- only the FOR UPDATE lock's first holder demotes", n)
	}

	rows := readLineItemsForTest(t, super, inv.ID)
	if len(rows) != 2 {
		t.Fatalf("line_items rows = %d, want 2", len(rows))
	}
	got0, got1 := "", ""
	if rows[0].Description != nil {
		got0 = *rows[0].Description
	}
	if rows[1].Description != nil {
		got1 = *rows[1].Description
	}
	switch {
	case got0 == descA1 && got1 == descB1:
	case got0 == descA2 && got1 == descB2:
	default:
		t.Errorf("final line_items descriptions = [%q, %q], want editor 1's whole payload [%q, %q] or editor 2's whole payload [%q, %q] -- never a mix of both (replace-all does not merge)",
			got0, got1, descA1, descB1, descA2, descB2)
	}
}

// T14: an edit carrying only a line change against a cross-tenant (or
// genuinely nonexistent) id resolves to ErrNotFound, same as the header
// path, and the target tenant's line rows are left byte-unchanged.
func TestStoreEdit_CrossTenantLineChangeRefusedLinesUntouched(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantB := seedTenant(t, super, "EDIT-T14 tenant B")
	entityB := seedEntity(t, super, tenantB, "EDIT-T14 B entity")
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})

	descA := "Widget"
	invB := seedLinedInvoiceAtStatus(t, super, store, cB, entityB, "EDIT-T14-B", StatusValidated, []LineItemInput{{Description: &descA}})
	beforeLines := readLineItemsForTest(t, super, invB.ID)

	tenantA := seedTenant(t, super, "EDIT-T14 tenant A")
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	newDesc := "Gadget"
	if _, err := store.Edit(cA, invB.ID, EditInput{LineItems: &[]LineItemInput{{Description: &newDesc}}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Edit(tenant B's invoice, line change) as tenant A err = %v, want ErrNotFound", err)
	}

	afterLines := readLineItemsForTest(t, super, invB.ID)
	if !reflect.DeepEqual(beforeLines, afterLines) {
		t.Errorf("tenant B's line_items rows changed by tenant A's refused cross-tenant Edit: before %+v, after %+v", beforeLines, afterLines)
	}
}

// T15: a lines-only edit on a draft, lined invoice leaves it draft -- there
// is nothing to demote from -- writing exactly one invoice.updated audit row
// and NO history/invoice.transitioned rows.
func TestStoreEdit_DraftLineChangeNoDemotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T15 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T15 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA := "Widget"
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-T15", LineItems: []LineItemInput{{Description: &descA}}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	newDesc := "Gadget"
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{{Description: &newDesc}}})
	if err != nil {
		t.Fatalf("Edit (draft, lines-only real change): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want unchanged %q", got.Status, StatusDraft)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d -- a draft invoice has nothing to demote from", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("audit_log invoice.updated rows = %d, want %d (+1 for the real lines-only change)", n, beforeUpdated+1)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
		t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d -- no demotion is possible from draft", n, beforeTransitioned)
	}
}

// T16 ([totals-ownership], negative AC): a line change never rewrites
// subtotal/vat/total -- those stay independently editable via the header
// path only.
func TestStoreEdit_LineChangeNeverRewritesTotals(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T16 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T16 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA := "Widget"
	inv, err := store.Create(c, CreateInput{
		EntityID: entityID, InvoiceNumber: "EDIT-T16",
		Subtotal: strPtr("100.00"), VAT: strPtr("7.00"), Total: strPtr("107.00"),
		LineItems: []LineItemInput{{Description: &descA}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	var beforeSubtotal, beforeVAT, beforeTotal string
	if err := super.QueryRow(ctx, `SELECT subtotal::text, vat::text, total::text FROM invoices WHERE id = $1`, inv.ID).
		Scan(&beforeSubtotal, &beforeVAT, &beforeTotal); err != nil {
		t.Fatalf("read back totals (before): %v", err)
	}

	newDesc := "Gadget"
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{{Description: &newDesc}}})
	if err != nil {
		t.Fatalf("Edit (line-only change): want success, got: %v", err)
	}
	if got.Subtotal == nil || *got.Subtotal != beforeSubtotal {
		t.Errorf("Edit returned Subtotal = %v, want unchanged %q", got.Subtotal, beforeSubtotal)
	}
	if got.VAT == nil || *got.VAT != beforeVAT {
		t.Errorf("Edit returned VAT = %v, want unchanged %q", got.VAT, beforeVAT)
	}
	if got.Total == nil || *got.Total != beforeTotal {
		t.Errorf("Edit returned Total = %v, want unchanged %q", got.Total, beforeTotal)
	}
}

// T17: Store.Edit's returned LineItems match a superuser read-back
// (different projection than lineItemColumns), ordered line_no ASC, on BOTH
// the changed path and the no-op path -- proving RETURNING == stored rather
// than some synthesized shape.
func TestStoreEdit_ReturnedLineItemsMatchStoredBothChangedAndNoOpPaths(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	t.Run("changed", func(t *testing.T) {
		tenantID := seedTenant(t, super, "EDIT-T17 tenant changed")
		entityID := seedEntity(t, super, tenantID, "EDIT-T17 entity")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		descA, descB := "Widget", "Gadget"
		inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T17-CHANGED", StatusValidated, []LineItemInput{
			{Description: &descA}, {Description: &descB},
		})

		newDescA := "Widget v2"
		got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
			{Description: &newDescA}, {Description: &descB},
		}})
		if err != nil {
			t.Fatalf("Edit (changed): want success, got: %v", err)
		}
		stored := readLineItemsForTest(t, super, inv.ID)
		if got.LineItems == nil {
			t.Fatal("Edit (changed) returned LineItems = nil, want the post-write rows hydrated")
		}
		if !reflect.DeepEqual(got.LineItems, stored) {
			t.Errorf("Edit (changed) returned LineItems = %+v, want byte-identical to the superuser read-back %+v", got.LineItems, stored)
		}
	})

	t.Run("no-op", func(t *testing.T) {
		tenantID := seedTenant(t, super, "EDIT-T17 tenant no-op")
		entityID := seedEntity(t, super, tenantID, "EDIT-T17 entity")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		descA, descB := "Widget", "Gadget"
		inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T17-NOOP", StatusValidated, []LineItemInput{
			{Description: &descA}, {Description: &descB},
		})

		got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
			{Description: &descA}, {Description: &descB},
		}})
		if err != nil {
			t.Fatalf("Edit (no-op): want success, got: %v", err)
		}
		stored := readLineItemsForTest(t, super, inv.ID)
		if got.LineItems == nil {
			t.Fatal("Edit (no-op) returned LineItems = nil, want the post-write rows hydrated even on the no-op path")
		}
		if !reflect.DeepEqual(got.LineItems, stored) {
			t.Errorf("Edit (no-op) returned LineItems = %+v, want byte-identical to the superuser read-back %+v", got.LineItems, stored)
		}
	})
}

// T18: the demote-then-revalidate loop closes end to end WITH lines in
// play, using store.ApplyValidation directly (NOT Gate.Validate, which needs
// a live HTTP endpoint this subtask does not stand up -- the edit_test.go
// idiom TestStoreEdit_DemoteThenRevalidateSucceeds already uses). The fresh
// fingerprint is computed straight off Edit's OWN return
// (contentFingerprint(edited, edited.LineItems)) -- proving
// [edit-response-carries-lines] is load-bearing, not decorative: if Edit's
// return does not carry the fresh lines, this fingerprint is computed over
// the WRONG (empty) line set and ApplyValidation's own in-tx re-check
// (which re-hydrates the REAL lines) goes stale.
func TestStoreEdit_DemoteThenRevalidateSucceedsWithLines(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T18 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T18 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA := "Widget"
	priceA := "10.00"
	inv, err := store.Create(c, CreateInput{
		EntityID: entityID, InvoiceNumber: "EDIT-T18",
		LineItems: []LineItemInput{{Description: &descA, UnitPrice: &priceA}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	staleVersionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)
	validated, err := store.ApplyValidation(c, inv.ID, []Violation{}, staleVersionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (seed): %v", err)
	}
	if validated.Status != StatusValidated {
		t.Fatalf("ApplyValidation (seed): status = %q, want validated (precondition)", validated.Status)
	}

	newDesc := "Widget v2"
	edited, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &newDesc, UnitPrice: &priceA},
	}})
	if err != nil {
		t.Fatalf("Edit (line change, demotion): want success, got: %v", err)
	}
	if edited.Status != StatusDraft {
		t.Fatalf("Edit: status = %q, want %q (demoted)", edited.Status, StatusDraft)
	}

	freshFP := contentFingerprint(edited, edited.LineItems)
	freshVersionID := seedRuleSetVersionID(t, super)
	revalidated, err := store.ApplyValidation(c, inv.ID, []Violation{}, freshVersionID, freshFP)
	if err != nil {
		t.Fatalf("ApplyValidation (re-validate after a lined edit, fingerprint taken off Edit's own return): want success, got: %v -- ErrStaleValidation here means Store.Edit's return did not carry the fresh lines ([edit-response-carries-lines])", err)
	}
	if revalidated.Status != StatusValidated {
		t.Errorf("ApplyValidation (re-validate): status = %q, want validated (promoted back)", revalidated.Status)
	}
}

// T20 ([D11]/AC #11): Store.Edit's demotion must be DERIVED from
// canTransition(before.Status, StatusDraft), never the hand-maintained
// `before.Status == StatusValidated || before.Status == StatusRejected`
// literal at store.go:749. Perturbs the package-level legalTransitions (via
// edgeTableWith, transition_test.go's helper) to add failed->draft, seeds a
// FAILED invoice WITH lines, and edits a line: with the derivation this
// commits AND demotes (draft, +1 failed->draft history row); with the
// retained literal it would commit and silently stay "failed" with NO
// history row -- the exact Core AC 2 violation this test exists to catch.
//
// Deliberately NO t.Parallel(): this mutates the package-level
// legalTransitions var, and this package has zero t.Parallel() calls
// anywhere (transition_test.go:801-805 documents the same invariant for
// TestCanEdit_TracksLegalTransitions's identical technique) -- adding
// t.Parallel() here (or anywhere in this package) would make this swap
// unsafe.
func TestStoreEdit_DemotionDerivedFromLegalTransitionsNotLiteral(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-T20 tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-T20 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA := "Widget"
	priceA := "10.00"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-T20", StatusFailed, []LineItemInput{
		{Description: &descA, UnitPrice: &priceA},
	})

	orig := legalTransitions
	t.Cleanup(func() { legalTransitions = orig })
	legalTransitions = edgeTableWith(orig, StatusFailed, StatusDraft)

	newDesc := "Widget v2"
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &newDesc, UnitPrice: &priceA},
	}})
	if err != nil {
		t.Fatalf("Edit (failed invoice, failed->draft now legal): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q -- demotion must be DERIVED from canTransition(before.Status, StatusDraft), not the hand-maintained validated/rejected literal (store.go:749)", got.Status, StatusDraft)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'failed' AND to_status = 'draft'`, inv.ID,
	); n != 1 {
		t.Errorf("invoice_status_history (failed,draft) rows = %d, want exactly 1 -- with the retained literal the write commits but the invoice silently stays 'failed' with NO history row (the exact silent Core AC 2 violation this test exists to catch)", n)
	}
}

// T21: a malformed line numeric behaves exactly like a malformed header
// numeric -- ErrValidation (not a raw 500) with zero line rows written on an
// editable status; on a NON-editable status the fixable-state guard still
// wins over the malformed content ([A8], mirrors
// TestStoreEdit_GuardBeforeContentValidation).
func TestStoreEdit_MalformedLineNumericValidationErrorZeroRowsWritten(t *testing.T) {
	t.Run("draft: malformed line unit_price -> ErrValidation, original line survives", func(t *testing.T) {
		super, app := dbTestPools(t)
		ctx := context.Background()
		store := NewStore(app)

		tenantID := seedTenant(t, super, "EDIT-T21 tenant draft")
		entityID := seedEntity(t, super, tenantID, "EDIT-T21 entity")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		descA := "Widget"
		inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-T21-DRAFT", LineItems: []LineItemInput{{Description: &descA}}})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

		badPrice := "not-a-number"
		_, err = store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{{Description: &descA, UnitPrice: &badPrice}}})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("Edit(draft, malformed line unit_price) err = %v, want ErrValidation", err)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE invoice_id = $1`, inv.ID); n != 1 {
			t.Errorf("line_items rows = %d, want unchanged 1 (the malformed replace-all rolled back, original line survives)", n)
		}
		if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated {
			t.Errorf("audit_log invoice.updated rows = %d, want unchanged %d", n, beforeUpdated)
		}
	})

	t.Run("queued: guard wins over malformed line content ([A8])", func(t *testing.T) {
		super, app := dbTestPools(t)
		ctx := context.Background()
		store := NewStore(app)

		tenantID := seedTenant(t, super, "EDIT-T21 tenant queued")
		entityID := seedEntity(t, super, tenantID, "EDIT-T21 entity")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "EDIT-T21-QUEUED"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
			t.Fatalf("pre-hop Transition(-> validated): %v", err)
		}
		if _, err := store.Transition(c, inv.ID, StatusQueued); err != nil {
			t.Fatalf("pre-hop Transition(-> queued): %v", err)
		}

		descA := "Widget"
		badPrice := "not-a-number"
		_, err = store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{{Description: &descA, UnitPrice: &badPrice}}})
		if !errors.Is(err, ErrNotFixable) {
			t.Fatalf("Edit(queued, malformed line unit_price) err = %v, want ErrNotFixable (guard wins over content validation)", err)
		}
		if errors.Is(err, ErrValidation) {
			t.Errorf("Edit(queued, malformed line unit_price) err = %v, must NOT also resolve as ErrValidation (guard must win outright)", err)
		}
	})
}

// --- INVED-01-04 QA (Mode B): adversarial coverage beyond the RED specs ---
// -----------------------------------------------------------------------
//
// The T1-T22 specs above are the architect's Test Specs table, authored RED
// before GREEN existed. These six close gaps QA identified while attacking
// Core AC 2 that the table didn't explicitly name: pure reordering (no
// add/remove), a large line set, all-NULL line numerics, a header+line
// change in ONE call (does the audit/history stay singular, not doubled?),
// lines concurrently removed out from under a header-only edit, and a
// direct fingerprint-level proof that emptying a line set is a genuine
// content change, not merely "trust the demotion happened".

// A1: reordering two EXISTING lines (no add/remove) is expressed purely as
// array order ([line-no-by-position]) -- line_no follows the NEW position,
// never the line's prior identity -- and still counts as a real content
// change (each line_no position now hashes a different description) so it
// demotes exactly like an append/drop would.
func TestStoreEdit_ReorderOnlyLineChangeDemotesAndRenumbersByPosition(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-QA-REORDER tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-QA-REORDER entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-QA-REORDER", StatusValidated, []LineItemInput{
		{Description: &descA}, {Description: &descB},
	})
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv.ID)

	// Swap: same two descriptions, reversed order -- no line added or removed.
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &descB}, {Description: &descA},
	}})
	if err != nil {
		t.Fatalf("Edit (reorder-only): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (a pure reorder is still a real content change per line_no position)", got.Status, StatusDraft)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv.ID); n != beforeHistory+1 {
		t.Errorf("invoice_status_history (validated,draft) rows = %d, want %d", n, beforeHistory+1)
	}

	stored := readLineItemsForTest(t, super, inv.ID)
	if len(stored) != 2 {
		t.Fatalf("line_items rows = %d, want 2", len(stored))
	}
	if stored[0].LineNo != 1 || stored[0].Description == nil || *stored[0].Description != descB {
		t.Errorf("line_no 1 = %+v, want description %q (the FIRST array entry, regardless of its prior line_no)", stored[0], descB)
	}
	if stored[1].LineNo != 2 || stored[1].Description == nil || *stored[1].Description != descA {
		t.Errorf("line_no 2 = %+v, want description %q (the SECOND array entry, regardless of its prior line_no)", stored[1], descA)
	}
}

// A2: a large line set (50 lines) persists in full, renumbered 1..N
// contiguously with no unique-constraint violation on the delete-then-
// reinsert -- the DELETE/INSERT separation ([line-update-shape] doc comment)
// holds regardless of set size, not just the 2-3 line fixtures the RED specs
// use.
func TestStoreEdit_LargeLineSetPersistsAllRenumbered1ToN(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-QA-LARGE tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-QA-LARGE entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descSeed := "Widget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-QA-LARGE", StatusValidated, []LineItemInput{
		{Description: &descSeed},
	})

	const n = 50
	lines := make([]LineItemInput, n)
	descs := make([]string, n)
	for i := 0; i < n; i++ {
		descs[i] = fmt.Sprintf("Line %d", i+1)
		lines[i] = LineItemInput{Description: &descs[i]}
	}
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &lines})
	if err != nil {
		t.Fatalf("Edit (50-line replace): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want %q (demoted)", got.Status, StatusDraft)
	}
	if len(got.LineItems) != n {
		t.Fatalf("Edit returned %d LineItems, want %d", len(got.LineItems), n)
	}

	stored := readLineItemsForTest(t, super, inv.ID)
	if len(stored) != n {
		t.Fatalf("line_items rows = %d, want %d", len(stored), n)
	}
	for i, li := range stored {
		if li.LineNo != i+1 {
			t.Errorf("stored[%d].LineNo = %d, want %d (contiguous from 1)", i, li.LineNo, i+1)
		}
		if li.Description == nil || *li.Description != descs[i] {
			t.Errorf("stored[%d].Description = %v, want %q", i, li.Description, descs[i])
		}
	}
}

// A3: a line with every numeric field NULL (only Description set) is
// store-invalid-faithfully persisted un-rejected -- no CHECK constraint on
// quantity/unit_price/line_total/line_tax (migrations/20260714105151_line_items.sql),
// mirroring Store.Create's existing NULL-numeric tolerance.
func TestStoreEdit_AllNullNumericLineFieldsPersist(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-QA-NULLNUM tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-QA-NULLNUM entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descSeed := "Widget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-QA-NULLNUM", StatusValidated, []LineItemInput{
		{Description: &descSeed},
	})

	newDesc := "All-null-numerics line"
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{
		{Description: &newDesc}, // Quantity/UnitPrice/LineTotal/LineTax left nil
	}})
	if err != nil {
		t.Fatalf("Edit (all-NULL-numerics line): want success (store-invalid-faithfully), got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want %q (demoted)", got.Status, StatusDraft)
	}
	if len(got.LineItems) != 1 {
		t.Fatalf("Edit returned %d LineItems, want 1", len(got.LineItems))
	}
	li := got.LineItems[0]
	if li.Quantity != nil || li.UnitPrice != nil || li.LineTotal != nil || li.LineTax != nil {
		t.Errorf("Edit returned line numerics = %+v, want all four NULL (store-invalid-faithfully, no CHECK constraint)", li)
	}

	stored := readLineItemsForTest(t, super, inv.ID)
	if len(stored) != 1 || stored[0].Quantity != nil || stored[0].UnitPrice != nil || stored[0].LineTotal != nil || stored[0].LineTax != nil {
		t.Errorf("stored line = %+v, want all four numerics NULL", stored)
	}
}

// A4: a header field AND a line change submitted in ONE Edit call produces
// exactly ONE invoice.updated audit row (fields containing BOTH "vat" and
// "line_items", not two separate rows) and exactly ONE validated->draft
// history row (not two) -- Store.Edit composes the header write, the line
// write, the single audit record and the single transitionTx call in one
// pass; nothing about combining the two inputs should double either side
// effect.
func TestStoreEdit_HeaderAndLineChangeInOneCallSingleAuditSingleHistory(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-QA-COMBINED tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-QA-COMBINED entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA := "Widget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-QA-COMBINED", StatusValidated, []LineItemInput{
		{Description: &descA},
	})
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv.ID)

	newVAT := "9.50"
	newDesc := "Gadget"
	got, err := store.Edit(c, inv.ID, EditInput{
		UpdateInput: UpdateInput{VAT: &newVAT},
		LineItems:   &[]LineItemInput{{Description: &newDesc}},
	})
	if err != nil {
		t.Fatalf("Edit (header+line combined): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want %q (demoted)", got.Status, StatusDraft)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("audit_log invoice.updated rows = %d, want %d (exactly ONE row for the combined edit, not two)", n, beforeUpdated+1)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv.ID); n != beforeHistory+1 {
		t.Errorf("invoice_status_history (validated,draft) rows = %d, want %d (exactly ONE demotion, not two)", n, beforeHistory+1)
	}
	fields := auditFields(t, app, tenantID, "invoice.updated")
	hasVAT, hasLines := false, false
	for _, f := range fields {
		if f == "vat" {
			hasVAT = true
		}
		if f == "line_items" {
			hasLines = true
		}
	}
	if !hasVAT || !hasLines {
		t.Errorf(`invoice.updated audit fields = %v, want to contain BOTH "vat" and "line_items"`, fields)
	}
}

// A5: an invoice's lines are removed by a direct (non-Edit) write between
// the invoice's creation and a later header-only Edit -- simulating a
// concurrently-completed prior deletion, e.g. another admin path or a
// migration, rather than a literal in-transaction race (the FOR UPDATE lock
// rules out a true concurrent line write during Edit's own tx, per
// [aggregate-lock]). Store.Edit must not panic or error on a lineless
// invoice: hydrateLinesTx simply reports zero rows, exactly as it would for
// an invoice that legitimately never had lines.
func TestStoreEdit_LinesRemovedOutOfBandThenHeaderOnlyEditSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-QA-OOB tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-QA-OOB entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA := "Widget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-QA-OOB", StatusValidated, []LineItemInput{
		{Description: &descA},
	})

	// Simulate an out-of-band prior removal of every line, bypassing Store.Edit
	// entirely -- superuser, outside any app transaction.
	if _, err := super.Exec(ctx, `DELETE FROM line_items WHERE invoice_id = $1`, inv.ID); err != nil {
		t.Fatalf("out-of-band delete: %v", err)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE invoice_id = $1`, inv.ID); n != 0 {
		t.Fatalf("precondition: line_items rows = %d, want 0", n)
	}

	newVAT := "9.50"
	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err != nil {
		t.Fatalf("Edit (header-only, on a now-lineless invoice): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want %q (demoted)", got.Status, StatusDraft)
	}
	if len(got.LineItems) != 0 {
		t.Errorf("Edit returned LineItems = %+v, want empty (the invoice genuinely has none)", got.LineItems)
	}
	if fields := auditFields(t, app, tenantID, "invoice.updated"); !reflect.DeepEqual(fields, []string{"vat"}) {
		t.Errorf(`invoice.updated audit fields = %v, want exactly ["vat"] (LineItems was nil in this call, so "line_items" must NOT appear)`, fields)
	}
}

// A6 (Part D): `line_items: []` on a lined, validated invoice with an
// UNCHANGED header genuinely changes the aggregate content fingerprint --
// not merely "the demotion happened, so presumably it did". This computes
// contentFingerprint directly, before and after, over the SAME (unchanged)
// header, proving the count marker (len(lines), INVED-01-02) is what makes
// zero lines distinguishable from the original two, independent of Edit's
// internal no-op check.
func TestStoreEdit_EmptyLineItemsFingerprintDiffersFromPreEdit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "EDIT-QA-FP tenant")
	entityID := seedEntity(t, super, tenantID, "EDIT-QA-FP entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "EDIT-QA-FP", StatusValidated, []LineItemInput{
		{Description: &descA}, {Description: &descB},
	})
	preLines := readLineItemsForTest(t, super, inv.ID)
	preFP := contentFingerprint(inv, preLines)

	empty := []LineItemInput{}
	got, err := store.Edit(c, inv.ID, EditInput{LineItems: &empty})
	if err != nil {
		t.Fatalf("Edit (line_items: []): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Fatalf("status = %q, want %q (demoted) -- precondition for the fingerprint comparison below to be meaningful", got.Status, StatusDraft)
	}

	// The header is byte-unchanged (`got` differs from `inv` only in
	// Status/LineItems); recompute the SAME fingerprint function directly over
	// got's header and its now-empty lines.
	postFP := contentFingerprint(got, got.LineItems)
	if postFP == preFP {
		t.Errorf("contentFingerprint unchanged (%s) after removing all lines -- the count marker must make zero lines distinct from the original two, independent of Edit's own no-op check", postFP)
	}
	if len(got.LineItems) != 0 {
		t.Errorf("got.LineItems = %+v, want empty", got.LineItems)
	}
}

// ============================================================================
// task-483 (APPR-06-07, Mode A): CancelLiveRunTx wired into Store.Edit's demotion
// branch -- specs written against store.go's CURRENT, unwired demotion (the
// canTransition block, store.go:1275-1279), so every assertion below fails for the
// reason noted on each test, never a compile error. This file never references
// internal/approval directly (see TestEdit_CancelRollsBackWithTheEdit's own header
// for why): approval_runs fixtures are seeded via seedApprovalRunFor/
// closeApprovalRunFor (apply_validation_arming_test.go), which write raw SQL, never
// through Store.Edit itself.
// ============================================================================

// cancelledPayload is invoice.approval_cancelled's payload shape (internal/approval,
// mirrored here since this file cannot import that package -- see the section
// header above).
type cancelledPayload struct {
	ID    string `json:"id"`
	RunID string `json:"run_id"`
}

// TestEdit_CancelsOpenRun (AC-2, AC-9): a validated invoice with one open run -->
// Store.Edit changes a content field --> invoice is draft, the run is cancelled with
// closed_by = the caller's subject and closed_at non-NULL, exactly one
// invoice.approval_cancelled audit row naming the run. Fails today: the run stays
// open -- nothing in Store.Edit touches approval_runs yet.
func TestEdit_CancelsOpenRun(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "CANCEL-01 tenant")
	entityID := seedEntity(t, super, tenantID, "CANCEL-01 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "CANCEL-01", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	policyID := seedApprovalPolicyFor(t, super, tenantID, "CANCEL-01 policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	runID := seedApprovalRunFor(t, super, tenantID, inv.ID, versionID) // defaults to open

	beforeCancelledAudit := auditCount(t, app, tenantID, "invoice.approval_cancelled")

	newVAT := "9.50"
	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err != nil {
		t.Fatalf("Edit (content change on validated invoice with an open run): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted)", got.Status, StatusDraft)
	}

	var state string
	var closedAt, closedBy *string
	if err := super.QueryRow(ctx,
		`SELECT state, closed_at::text, closed_by FROM approval_runs WHERE id = $1`, runID,
	).Scan(&state, &closedAt, &closedBy); err != nil {
		t.Fatalf("read back the run: %v", err)
	}
	if state != "cancelled" {
		t.Errorf("run state after Edit = %q, want %q", state, "cancelled")
	}
	if closedAt == nil {
		t.Errorf("run closed_at after Edit is NULL, want non-NULL")
	}
	if closedBy == nil || *closedBy != subject {
		t.Errorf("run closed_by after Edit = %v, want %q (the caller's subject)", closedBy, subject)
	}

	if n := auditCount(t, app, tenantID, "invoice.approval_cancelled"); n != beforeCancelledAudit+1 {
		t.Errorf("invoice.approval_cancelled audit rows = %d, want %d (exactly one new row)", n, beforeCancelledAudit+1)
	}
	raw := auditPayload(t, app, tenantID, "invoice.approval_cancelled")
	var payload cancelledPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal invoice.approval_cancelled payload %s: %v", raw, err)
	}
	if payload.ID != inv.ID {
		t.Errorf("audit payload id = %q, want %q", payload.ID, inv.ID)
	}
	if payload.RunID != runID {
		t.Errorf("audit payload run_id = %q, want %q", payload.RunID, runID)
	}
}

// TestEdit_NoOpEditCancelsNothing (AC-6): a validated invoice with one open run -->
// Store.Edit resubmits identical content (a no-op) --> invoice stays validated, the
// run stays open, no invoice.approval_cancelled audit row. Passes today by
// construction -- the no-op short-circuit at store.go:1219 returns before the
// demotion branch (where the cancel will live) is ever reached, the same
// "passes today, becomes a real guard once wired" shape as
// TestApplyValidation_NoActivePolicyLeavesTheTrailUnchanged
// (apply_validation_arming_test.go). The control for TestEdit_CancelsOpenRun above:
// goes red if a future change ever hooked the cancel ABOVE the no-op return.
func TestEdit_NoOpEditCancelsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "CANCEL-02 tenant")
	entityID := seedEntity(t, super, tenantID, "CANCEL-02 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "CANCEL-02", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	policyID := seedApprovalPolicyFor(t, super, tenantID, "CANCEL-02 policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	runID := seedApprovalRunFor(t, super, tenantID, inv.ID, versionID)

	beforeCancelledAudit := auditCount(t, app, tenantID, "invoice.approval_cancelled")

	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.00")}}) // resent unchanged
	if err != nil {
		t.Fatalf("Edit (no-op, VAT resent unchanged): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("Edit returned status = %q, want unchanged %q (no-op)", got.Status, StatusValidated)
	}

	var state string
	if err := super.QueryRow(ctx, `SELECT state FROM approval_runs WHERE id = $1`, runID).Scan(&state); err != nil {
		t.Fatalf("read back the run: %v", err)
	}
	if state != "open" {
		t.Errorf("run state after a no-op Edit = %q, want unchanged %q", state, "open")
	}
	if n := auditCount(t, app, tenantID, "invoice.approval_cancelled"); n != beforeCancelledAudit {
		t.Errorf("invoice.approval_cancelled audit rows = %d, want unchanged %d (a no-op cancels nothing)", n, beforeCancelledAudit)
	}
}

// TestEdit_CancelsApprovedRunNotOnlyOpen (AC-1, AC-2): a validated invoice whose only
// run is closed 'approved', zero steps, closed_by='system' (answer B's ordinary shape
// for an invoice that needed no sign-off, D37) --> Store.Edit changes a content field
// --> invoice is draft, that run is cancelled, closed_by is STILL 'system' and
// closed_at is unchanged (the COALESCE), exactly one cancellation audit row. Fails
// today: nothing cancels. Also fails against a `state = 'open'` predicate, which
// would leave the run 'approved' (D37).
func TestEdit_CancelsApprovedRunNotOnlyOpen(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "CANCEL-03 tenant")
	entityID := seedEntity(t, super, tenantID, "CANCEL-03 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "CANCEL-03", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	policyID := seedApprovalPolicyFor(t, super, tenantID, "CANCEL-03 policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	runID := seedApprovalRunFor(t, super, tenantID, inv.ID, versionID)
	closeApprovalRunFor(t, super, runID, "approved", "system")

	var seededClosedAt string
	if err := super.QueryRow(ctx, `SELECT closed_at::text FROM approval_runs WHERE id = $1`, runID).Scan(&seededClosedAt); err != nil {
		t.Fatalf("read back the seeded closed_at: %v", err)
	}

	beforeCancelledAudit := auditCount(t, app, tenantID, "invoice.approval_cancelled")

	newVAT := "9.50"
	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err != nil {
		t.Fatalf("Edit (content change on validated invoice with a closed 'approved' run): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q (demoted)", got.Status, StatusDraft)
	}

	var state, closedAt string
	var closedBy *string
	if err := super.QueryRow(ctx,
		`SELECT state, closed_at::text, closed_by FROM approval_runs WHERE id = $1`, runID,
	).Scan(&state, &closedAt, &closedBy); err != nil {
		t.Fatalf("read back the run: %v", err)
	}
	if state != "cancelled" {
		t.Errorf("run state after Edit = %q, want %q (D37: an approved run is live and must cancel too)", state, "cancelled")
	}
	if closedBy == nil || *closedBy != "system" {
		t.Errorf("run closed_by after Edit = %v, want unchanged %q (COALESCE preserves the original closure)", closedBy, "system")
	}
	if closedAt != seededClosedAt {
		t.Errorf("run closed_at after Edit = %q, want unchanged %q (COALESCE preserves the original closure)", closedAt, seededClosedAt)
	}

	if n := auditCount(t, app, tenantID, "invoice.approval_cancelled"); n != beforeCancelledAudit+1 {
		t.Errorf("invoice.approval_cancelled audit rows = %d, want %d (exactly one new row)", n, beforeCancelledAudit+1)
	}
}

// TestStoreEdit_DemoteThenRevalidateUnderActivePolicySucceeds (AC-4, AC-7, [FIX-3]):
// the story's headline behaviour, untested until this subtask. Modelled on the
// shipped TestStoreEdit_DemoteThenRevalidateSucceeds (above) with an ACTIVE
// one-approval-step policy added: create --> ApplyValidation arms an OPEN run -->
// Store.Edit changes VAT, demoting --> ApplyValidation with the fresh fingerprint
// SUCCEEDS (no 23505 on approval_runs_one_open) and arms a second run --> exactly two
// runs exist for the invoice: the first cancelled, the second open with one pending
// approval step and the post-edit content_fingerprint. Fails today: ApplyValidation
// returns the raw 23505 subtask 06 deliberately left uncaught -- the exact re-arm
// hazard this subtask closes.
func TestStoreEdit_DemoteThenRevalidateUnderActivePolicySucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID, entityID, _ := seedOneStepActivePolicyTenant(t, super, "DEMOTE-REVAL-ACTIVE")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DEMOTE-REVAL-ACTIVE", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	fp1 := contentFingerprint(inv, inv.LineItems)
	ruleSetVersionID1 := seedRuleSetVersionID(t, super)
	validated, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID1, fp1)
	if err != nil {
		t.Fatalf("ApplyValidation (seed, arms an open run under the active policy): %v", err)
	}
	if validated.Status != StatusValidated {
		t.Fatalf("ApplyValidation (seed): status = %q, want %q (precondition)", validated.Status, StatusValidated)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'open'`, inv.ID); n != 1 {
		t.Fatalf("precondition: open approval_runs rows for invoice = %d, want exactly 1 (ArmTx must have armed an open run)", n)
	}

	newVAT := "9.50"
	edited, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err != nil {
		t.Fatalf("Edit (content change, demotion): want success, got: %v", err)
	}
	if edited.Status != StatusDraft {
		t.Fatalf("Edit: status = %q, want %q (demoted)", edited.Status, StatusDraft)
	}

	freshFP := contentFingerprint(edited, edited.LineItems)
	ruleSetVersionID2 := seedRuleSetVersionID(t, super)
	revalidated, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID2, freshFP)
	if err != nil {
		t.Fatalf("ApplyValidation (re-validate after demotion): want success through the fix loop, got: %v "+
			"(want no 23505 on approval_runs_one_open -- Store.Edit's demotion must cancel the stale open run before ApplyValidation re-arms)", err)
	}
	if revalidated.Status != StatusValidated {
		t.Errorf("ApplyValidation (re-validate): status = %q, want %q (promoted back to green)", revalidated.Status, StatusValidated)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, inv.ID); n != 2 {
		t.Fatalf("approval_runs rows for invoice = %d, want exactly 2 (the cancelled first run, the fresh second run)", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state <> 'cancelled'`, inv.ID); n != 1 {
		t.Fatalf("non-cancelled approval_runs rows for invoice = %d, want exactly 1", n)
	}

	var liveRunID, liveState, liveFingerprint string
	if err := super.QueryRow(ctx,
		`SELECT id, state, content_fingerprint FROM approval_runs WHERE invoice_id = $1 AND state <> 'cancelled'`, inv.ID,
	).Scan(&liveRunID, &liveState, &liveFingerprint); err != nil {
		t.Fatalf("read the live run: %v", err)
	}
	if liveState != "open" {
		t.Errorf("live run state = %q, want %q", liveState, "open")
	}
	if liveFingerprint != freshFP {
		t.Errorf("live run content_fingerprint = %q, want the post-edit fingerprint %q", liveFingerprint, freshFP)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_run_steps WHERE run_id = $1 AND state = 'pending'`, liveRunID); n != 1 {
		t.Errorf("pending approval_run_steps rows for the live run = %d, want exactly 1", n)
	}
}

// TestEdit_CancelRollsBackWithTheEdit (AC-8): pins CancelLiveRunTx's D12 contract --
// "the cancel joins the caller's transaction rather than opening one of its own" --
// against the specific way an executor could get this wrong: hooking the cancel call
// EARLIER than the plan's insertion point (immediately after transitionTx, inside the
// demotion branch), or having it commit on its own connection instead of the passed
// tx. Reuses the crafted-actor injection TestStoreEdit_ContentAuditFailureRollsBackWholeEdit
// uses (above): an empty Subject fails invoice.updated's audit_log CHECK at step 7,
// aborting the WHOLE Store.Edit transaction before the demotion branch (step 8, where
// the cancel lives, the LAST statement in that transaction per the plan) is ever
// reached.
//
// This means: against the plan's own correctly-ordered implementation, this test
// passes BOTH today and after CancelLiveRunTx ships -- step 7 always fires first, so
// the cancel is never reached either way. It is the SAME "passes today by
// construction, becomes a real guard once wired" shape as TestEdit_NoOpEditCancelsNothing
// above and TestApplyValidation_NoActivePolicyLeavesTheTrailUnchanged
// (apply_validation_arming_test.go): a future change that moved the cancel call ABOVE
// step 7, or that let it survive step 7's abort by writing on its own connection
// rather than the caller's tx, would flip the run to 'cancelled' here and this test
// would catch it. No construction of "cancel succeeds, then a LATER statement fails"
// is reachable through Store.Edit itself: the plan places the cancel as the last
// statement in the demotion branch, and every write in one Edit call shares the SAME
// ctx-derived actor (callerID.Subject / actorFromContext(ctx) are the identical
// value), so no crafted Subject can pass step 7's audit_log CHECK and fail only
// later -- confirmed structurally in store.go, not merely asserted here. This is also
// why this test deliberately does NOT reference approval.CancelLiveRunTx directly:
// doing so would fail to compile today and take every other test in this package down
// with it.
func TestEdit_CancelRollsBackWithTheEdit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "CANCEL-05 tenant")
	entityID := seedEntity(t, super, tenantID, "CANCEL-05 entity")
	cNormal := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(cNormal, CreateInput{EntityID: entityID, InvoiceNumber: "CANCEL-05", VAT: strPtr("7.00")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(cNormal, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	policyID := seedApprovalPolicyFor(t, super, tenantID, "CANCEL-05 policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	runID := seedApprovalRunFor(t, super, tenantID, inv.ID, versionID)

	beforeCancelledAudit := auditCount(t, app, tenantID, "invoice.approval_cancelled")

	cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: "", Role: "authenticated", TenantID: tenantID})
	newVAT := "9.50"
	_, err = store.Edit(cCrafted, inv.ID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err == nil {
		t.Fatal("Edit with a crafted actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("Edit with a crafted actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusValidated {
		t.Errorf("status after failed Edit = %q, want unchanged %q", status, StatusValidated)
	}

	var state string
	if err := super.QueryRow(ctx, `SELECT state FROM approval_runs WHERE id = $1`, runID).Scan(&state); err != nil {
		t.Fatalf("read back the run: %v", err)
	}
	if state != "open" {
		t.Errorf("run state after the whole Edit transaction rolled back = %q, want unchanged %q", state, "open")
	}

	if n := auditCount(t, app, tenantID, "invoice.approval_cancelled"); n != beforeCancelledAudit {
		t.Errorf("invoice.approval_cancelled audit rows = %d, want unchanged %d (rolled back with everything else)", n, beforeCancelledAudit)
	}
}
