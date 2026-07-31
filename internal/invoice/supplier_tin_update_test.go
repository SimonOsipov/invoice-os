// INVCR-01-18 (task-303, C7 fix, EDIT path): closes the sibling gap
// task-293's own QA flagged (advisory finding, part 3/3 of that task's
// notes) -- Store.Create derives supplier_tin/supplier_name from the
// invoice's entity ([supplier-from-entity], INVCR-01-17), but Store.Update
// (and, sharing updateContentTx, Store.Edit -- the ONLY one of the two
// actually wired to HTTP, PATCH /v1/invoices/{id}) did not, so an operator
// could retype a bare-digit supplier_tin through the edit screen and
// reintroduce the exact defect C7 fixed on create.
//
// Mirrors supplier_tin_test.go's own fixtures/helpers (createEntityViaReal-
// PortfolioStore, c7FIRSTIN/c7CanonicalFIRSTIN/c7JTBTIN) rather than
// duplicating them -- same package, same file set, one entity->invoice
// boundary story split across the create and edit paths.
package invoice

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestStoreUpdate_SupplierTINAndNameDerivedFromEntityOverridingCaller
// (AC #1, #2): Store.Update overrides whatever supplier_tin/supplier_name
// the caller PATCHes in with the invoice's entity-derived values -- the
// exact mirror of TestStoreCreate_SupplierTINAndNameDerivedFromEntity-
// OverridingCaller (supplier_tin_test.go) for the Update path. The entity
// carries a REAL 12-bare-digit canonicalized FIRS tin (via the actual
// portfolio.Store.Create write path, not a raw-SQL fixture -- the same
// "test gap is the defect" discipline task-293's own header established),
// so a stored-then-restored round trip is the thing under test, not a
// value that happened to already be correctly shaped.
func TestStoreUpdate_SupplierTINAndNameDerivedFromEntityOverridingCaller(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "C7-EDIT-01 tenant")
	const entityName = "C7-EDIT-01 API-created Supplier Co"
	entityID, canonicalTIN := createEntityViaRealPortfolioStore(t, super, app, tenantID, entityName, c7FIRSTIN)
	if canonicalTIN != c7CanonicalFIRSTIN {
		t.Fatalf("premise broken: portfolio.Store.Create persisted tin %q, want %q", canonicalTIN, c7CanonicalFIRSTIN)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, gapiValidInvoiceInput(entityID, "C7-EDIT-01"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Precondition: Create already derived the correct value (task-293) --
	// this test is about Update, so pin that Create's own behaviour isn't
	// what's under test here.
	if inv.SupplierTIN == nil || *inv.SupplierTIN != c7FIRSTIN {
		t.Fatalf("precondition broken: Create's own supplier_tin = %v, want %q", inv.SupplierTIN, c7FIRSTIN)
	}

	// A PATCH deliberately submitting WRONG values -- proves Update
	// overrides the caller's wire body rather than trusting it, exactly as
	// Create does.
	updated, err := store.Update(c, inv.ID, UpdateInput{
		SupplierTIN:  strPtr("00000000-0000"),
		SupplierName: strPtr("Wrong Co (should never be persisted)"),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.SupplierTIN == nil || *updated.SupplierTIN != c7FIRSTIN {
		t.Errorf("Update returned supplier_tin = %v, want the entity's restored %q (NOT the caller's %q)",
			updated.SupplierTIN, c7FIRSTIN, "00000000-0000")
	}
	if updated.SupplierName == nil || *updated.SupplierName != entityName {
		t.Errorf("Update returned supplier_name = %v, want the entity's %q (NOT the caller's %q)",
			updated.SupplierName, entityName, "Wrong Co (should never be persisted)")
	}

	var supplierTIN, supplierName string
	if err := super.QueryRow(ctx,
		`SELECT supplier_tin, supplier_name FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&supplierTIN, &supplierName); err != nil {
		t.Fatalf("read back invoice: %v", err)
	}
	if supplierTIN != c7FIRSTIN {
		t.Errorf("supplier_tin read back = %q, want %q", supplierTIN, c7FIRSTIN)
	}
	if supplierName != entityName {
		t.Errorf("supplier_name read back = %q, want %q", supplierName, entityName)
	}

	// Close the loop, same as the create-path spec: the restored TIN must
	// pass supplier-tin-format on a REAL validate call, not just as a
	// stored-value assertion. The invoice is still draft (Update never
	// touches status), so it is Validate-eligible.
	srv := startInProcess04(t, app)
	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(store, validator)

	got, _, err := gate.Validate(c, inv.ID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Status != StatusValidated {
		var vs []Violation
		_ = json.Unmarshal(got.Violations, &vs)
		t.Errorf("status = %q, want %q -- violations: %+v", got.Status, StatusValidated, vs)
	}
	if string(got.Violations) != "[]" {
		t.Errorf("violations = %s, want [] (supplier-tin-format must NOT fire once the PATCH's bogus tin is overridden)", got.Violations)
	}
}

// TestStoreEdit_SupplierTINOverriddenOnRealPATCHPathClosesLoopOnGateValidate
// (AC #2, production path): PATCH /v1/invoices/{id} is wired to Store.Edit,
// NOT Store.Update (cmd/invoice/main.go) -- Store.Update itself is never
// reachable over HTTP. This is the spec that actually proves the operator-
// facing defect is closed: an Edit call (the real wire path) that submits a
// bare-12-digit supplier_tin still ends up with the entity-derived
// hyphenated value stored, and the invoice passes supplier-tin-format on a
// REAL Gate.Validate call.
func TestStoreEdit_SupplierTINOverriddenOnRealPATCHPathClosesLoopOnGateValidate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "C7-EDIT-02 tenant")
	const entityName = "C7-EDIT-02 API-created Supplier Co"
	entityID, canonicalTIN := createEntityViaRealPortfolioStore(t, super, app, tenantID, entityName, c7FIRSTIN)
	if canonicalTIN != c7CanonicalFIRSTIN {
		t.Fatalf("premise broken: portfolio.Store.Create persisted tin %q, want %q", canonicalTIN, c7CanonicalFIRSTIN)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, gapiValidInvoiceInput(entityID, "C7-EDIT-02"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The exact regression this subtask exists to close: an operator retypes
	// the bare-12-digit canonical form through the edit screen's (formerly
	// live) supplier_tin input.
	got, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{
		SupplierTIN:  strPtr(c7CanonicalFIRSTIN), // "100123450007" -- exactly what supplier-tin-format rejects
		SupplierName: strPtr("Operator-typed Co (should never be persisted)"),
	}})
	if err != nil {
		t.Fatalf("Edit (PATCH re-typing a bare-digit supplier_tin): %v", err)
	}
	if got.SupplierTIN == nil || *got.SupplierTIN != c7FIRSTIN {
		t.Errorf("Edit returned supplier_tin = %v, want the entity-derived, hyphen-restored %q (NOT the operator-typed bare %q)",
			got.SupplierTIN, c7FIRSTIN, c7CanonicalFIRSTIN)
	}
	if got.SupplierName == nil || *got.SupplierName != entityName {
		t.Errorf("Edit returned supplier_name = %v, want the entity's %q", got.SupplierName, entityName)
	}

	srv := startInProcess04(t, app)
	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(store, validator)

	valid, _, err := gate.Validate(c, inv.ID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if valid.Status != StatusValidated {
		var vs []Violation
		_ = json.Unmarshal(valid.Violations, &vs)
		t.Errorf("status = %q, want %q -- violations: %+v", valid.Status, StatusValidated, vs)
	}
	if string(valid.Violations) != "[]" {
		t.Errorf("violations = %s, want [] (the whole point of this fix)", valid.Violations)
	}
}

// TestStoreUpdate_JTBEntityTINPassesThroughUnchanged (AC #4): a 10-digit JTB
// entity TIN has no hyphen to restore on the Update path either -- mirrors
// TestStoreCreate_JTBEntityTINPassesThroughUnchanged.
func TestStoreUpdate_JTBEntityTINPassesThroughUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "C7-EDIT-03 tenant")
	entityID, canonicalTIN := createEntityViaRealPortfolioStore(t, super, app, tenantID, "C7-EDIT-03 JTB Supplier Co", c7JTBTIN)
	if canonicalTIN != c7JTBTIN {
		t.Fatalf("premise broken: JTB tin persisted as %q, want %q unchanged", canonicalTIN, c7JTBTIN)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, gapiValidInvoiceInput(entityID, "C7-EDIT-03"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := store.Update(c, inv.ID, UpdateInput{SupplierTIN: strPtr("this value must be ignored")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.SupplierTIN == nil || *updated.SupplierTIN != c7JTBTIN {
		t.Errorf("Update returned supplier_tin = %v, want the JTB tin %q UNCHANGED (no hyphen fabricated)", updated.SupplierTIN, c7JTBTIN)
	}
}

// TestStoreUpdate_BuyerTINNotNormalized (AC #3, scope fence): buyer_tin/
// buyer_name are the CALLER's data on the Update path too -- a bare
// 12-digit buyer TIN must persist byte-unchanged and still fire
// buyer-tin-format on validate. Mirrors TestStoreCreate_BuyerTINNotNormalized.
func TestStoreUpdate_BuyerTINNotNormalized(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "C7-EDIT-04 tenant")
	entityID, _ := createEntityViaRealPortfolioStore(t, super, app, tenantID, "C7-EDIT-04 Supplier Co", c7FIRSTIN)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, gapiValidInvoiceInput(entityID, "C7-EDIT-04"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const bareBuyerTIN = "876543210002" // 12 bare digits -- canonicalFIRSTIN's own shape
	updated, err := store.Update(c, inv.ID, UpdateInput{BuyerTIN: strPtr(bareBuyerTIN)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.BuyerTIN == nil || *updated.BuyerTIN != bareBuyerTIN {
		t.Errorf("Update returned buyer_tin = %v, want %q UNCHANGED -- buyer_tin must never be normalized", updated.BuyerTIN, bareBuyerTIN)
	}

	var buyerTIN string
	if err := super.QueryRow(ctx, `SELECT buyer_tin FROM invoices WHERE id = $1`, inv.ID).Scan(&buyerTIN); err != nil {
		t.Fatalf("read back invoice: %v", err)
	}
	if buyerTIN != bareBuyerTIN {
		t.Errorf("buyer_tin read back = %q, want %q", buyerTIN, bareBuyerTIN)
	}

	srv := startInProcess04(t, app)
	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(store, validator)

	got, _, err := gate.Validate(c, inv.ID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	var vs []Violation
	if err := json.Unmarshal(got.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", got.Violations, err)
	}
	if !hasViolation(vs, "buyer-tin-format") {
		t.Errorf("violations = %+v, want one naming buyer-tin-format -- buyer_tin is user-supplied and MUST still be able to violate", vs)
	}
}

// TestStoreEdit_PreExistingWrongSupplierTINGenuinelyChangesAndDemotesOnFirstPatch
// (AC #8, the "legitimate edge" half of the no-op trap): an invoice created
// BEFORE this fix existed (simulated here by writing supplier_tin directly,
// bypassing Store.Create's own derivation entirely) carries a supplier_tin
// that disagrees with what its entity would derive today. The FIRST PATCH
// after this fix ships -- even one that changes nothing else the caller can
// see -- genuinely changes the invoice's content fingerprint (the stored
// value differs from the newly-derived one) and correctly demotes
// validated->draft. This is NOT a false demotion: the content really did
// change, from a stale/wrong stored tin to the correct one. The sibling case
// (a no-op edit on an invoice whose stored value ALREADY agrees with its
// entity) is TestStoreEdit_ValidatedNoOpStaysValidated (EDIT-04,
// edit_test.go), deliberately left untouched by this subtask.
func TestStoreEdit_PreExistingWrongSupplierTINGenuinelyChangesAndDemotesOnFirstPatch(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "C7-EDIT-05 tenant")
	// The entity's OWN tin is already correctly hyphenated -- MBSSupplierTIN
	// leaves an already-hyphenated value untouched (it only matches a bare
	// 12-digit canonical form), so today's derivation would produce exactly
	// this value.
	const entityTIN = "20012345-0009"
	const entityName = "C7-EDIT-05 Legacy Supplier Co"
	entityID := seedEntityWithTIN(t, super, tenantID, entityName, entityTIN)

	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	// Simulate a pre-C7-fix invoice: raw-inserted (bypassing Store.Create's
	// derivation entirely, exactly as a real pre-fix row would have been
	// written), with a supplier_tin that DISAGREES with the entity's own tin
	// above -- the bare-digit shape task-293/task-303 exist to eliminate --
	// plus the rest of a minimal MBS-content set and buyer_name left as a
	// deliberately UNCHANGED field the Edit call below re-sends verbatim.
	inv := seedInvoiceAtStatus(t, super, tenantID, entityID, "C7-EDIT-05", StatusValidated)
	const staleBuyerName = "Stale-but-unchanged Buyer Co"
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET supplier_tin = 'WRONG-STALE-TIN', supplier_name = 'Stale Supplier Name',
		 buyer_name = $1 WHERE id = $2`,
		staleBuyerName, inv,
	); err != nil {
		t.Fatalf("seed pre-fix stale supplier_tin: %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv)
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	// The caller changes NOTHING visible: buyer_name is re-sent with its
	// CURRENT (unchanged) value. The only thing that actually changes is the
	// silent entity-derived supplier correction.
	got, err := store.Edit(c, inv, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr(staleBuyerName)}})
	if err != nil {
		t.Fatalf("Edit (buyer_name resent unchanged, on a pre-fix-stale supplier_tin invoice): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Edit returned status = %q, want %q -- the entity-derived correction is a REAL content change, so this must demote", got.Status, StatusDraft)
	}
	if got.SupplierTIN == nil || *got.SupplierTIN != entityTIN {
		t.Errorf("Edit returned supplier_tin = %v, want the entity-derived %q (the stale stored value must be self-corrected)", got.SupplierTIN, entityTIN)
	}
	if got.SupplierName == nil || *got.SupplierName != entityName {
		t.Errorf("Edit returned supplier_name = %v, want the entity's %q", got.SupplierName, entityName)
	}

	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv,
	); n != 1 {
		t.Errorf("invoice_status_history (validated,draft) rows = %d, want exactly 1 (genuine demotion)", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv); n != beforeHistory+1 {
		t.Errorf("invoice_status_history rows = %d, want %d", n, beforeHistory+1)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned+1 {
		t.Errorf("audit_log invoice.transitioned rows = %d, want %d", n, beforeTransitioned+1)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("audit_log invoice.updated rows = %d, want %d", n, beforeUpdated+1)
	}

	// The audit trail must NAME the silent correction, not just apply it
	// (product-advisor review, 2026-07-31, TestStoreUpdate_AuditFieldsOmit-
	// SupplierWhenUnchangedButNameItWhenCorrected pins the same mechanism at
	// Store.Update directly): buyer_name was the only field the caller
	// "submitted" a value for, but the entity-derived supplier correction is
	// a REAL stored-value change too, so both must appear alongside it.
	fields := auditFields(t, app, tenantID, "invoice.updated")
	hasBuyerName, hasSupplierTIN, hasSupplierName := false, false, false
	for _, f := range fields {
		switch f {
		case "buyer_name":
			hasBuyerName = true
		case "supplier_tin":
			hasSupplierTIN = true
		case "supplier_name":
			hasSupplierName = true
		}
	}
	if !hasBuyerName || !hasSupplierTIN || !hasSupplierName {
		t.Errorf(`invoice.updated audit fields = %v, want "buyer_name", "supplier_tin" AND "supplier_name" all present`, fields)
	}
}

// TestStoreUpdate_AuditFieldsOmitSupplierWhenUnchangedButNameItWhenCorrected
// (product-advisor review, 2026-07-31): the entity-derived override is
// written to SQL unconditionally, but only EARNS a "supplier_tin"/
// "supplier_name" entry in changedFields -- and therefore in the
// invoice.updated audit payload, which carries fields ONLY, no from/to
// snapshot -- when the derived value actually DIFFERS from what is already
// stored. Two subtests pin both halves:
//
//   - "unchanged": an ordinary PATCH against an already-correctly-derived
//     invoice must NOT claim a supplier change it didn't make -- breaking
//     this would falsely claim the caller asked to change supplier identity
//     on EVERY edit, contradicting the pre-existing "fields lists what
//     genuinely changed" contract several edit_test.go specs pin
//     byte-for-byte (e.g.
//     TestStoreEdit_LinesRemovedOutOfBandThenHeaderOnlyEditSucceeds's fields
//     == ["vat"], TestStoreEdit_EmptyLineItemsRemovesAllLinesGuardWidened's
//     fields == ["line_items"]).
//   - "corrected": a stale/wrong stored value (the AC #8 "genuine content
//     change" case, TestStoreEdit_PreExistingWrongSupplierTINGenuinely-
//     ChangesAndDemotesOnFirstPatch's own scenario, exercised here directly
//     against Store.Update) MUST be named in the audit trail -- a silent
//     correction with zero trace of what changed would be a worse gap than
//     the fields array being merely incomplete.
func TestStoreUpdate_AuditFieldsOmitSupplierWhenUnchangedButNameItWhenCorrected(t *testing.T) {
	t.Run("unchanged", func(t *testing.T) {
		super, app := dbTestPools(t)
		ctx := context.Background()

		tenantID := seedTenant(t, super, "C7-EDIT-06 tenant")
		entityID, _ := createEntityViaRealPortfolioStore(t, super, app, tenantID, "C7-EDIT-06 Supplier Co", c7FIRSTIN)

		store := NewStore(app)
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		inv, err := store.Create(c, gapiValidInvoiceInput(entityID, "C7-EDIT-06"))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if _, err := store.Update(c, inv.ID, UpdateInput{Total: strPtr("999.99")}); err != nil {
			t.Fatalf("Update (Total only): %v", err)
		}

		fields := auditFields(t, app, tenantID, "invoice.updated")
		if !reflect.DeepEqual(fields, []string{"total"}) {
			t.Errorf(`invoice.updated audit fields = %v, want exactly ["total"] (supplier_tin/supplier_name are `+
				`always re-derived and WRITTEN, but must not appear here when the derived value equals what was `+
				`already stored)`, fields)
		}
	})

	t.Run("corrected", func(t *testing.T) {
		super, app := dbTestPools(t)
		ctx := context.Background()

		tenantID := seedTenant(t, super, "C7-EDIT-07 tenant")
		const entityTIN = "30012345-0008"
		const entityName = "C7-EDIT-07 Legacy Supplier Co"
		entityID := seedEntityWithTIN(t, super, tenantID, entityName, entityTIN)

		store := NewStore(app)
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		// A pre-fix-shaped invoice: raw-inserted with a supplier_tin/name that
		// DISAGREE with the entity above (simulating a stale stored value), same
		// idiom as TestStoreEdit_PreExistingWrongSupplierTINGenuinelyChangesAnd-
		// DemotesOnFirstPatch, but at Store.Update directly (no status/demotion
		// concern here -- this is purely about the audit "fields" array).
		inv := seedInvoiceAtStatus(t, super, tenantID, entityID, "C7-EDIT-07", StatusDraft)
		if _, err := super.Exec(ctx,
			`UPDATE invoices SET supplier_tin = 'WRONG-STALE-TIN', supplier_name = 'Stale Supplier Name' WHERE id = $1`, inv,
		); err != nil {
			t.Fatalf("seed pre-fix stale supplier_tin: %v", err)
		}

		if _, err := store.Update(c, inv, UpdateInput{Total: strPtr("999.99")}); err != nil {
			t.Fatalf("Update (Total only, on a stale-supplier invoice): %v", err)
		}

		fields := auditFields(t, app, tenantID, "invoice.updated")
		hasTotal, hasSupplierTIN, hasSupplierName := false, false, false
		for _, f := range fields {
			switch f {
			case "total":
				hasTotal = true
			case "supplier_tin":
				hasSupplierTIN = true
			case "supplier_name":
				hasSupplierName = true
			}
		}
		if !hasTotal || !hasSupplierTIN || !hasSupplierName {
			t.Errorf(`invoice.updated audit fields = %v, want "total", "supplier_tin" AND "supplier_name" all `+
				`present -- the entity-derived correction is a REAL change and must be named, not silently applied`, fields)
		}
		if len(fields) != 3 {
			t.Errorf(`invoice.updated audit fields = %v, want exactly 3 entries (no unrelated field silently added)`, fields)
		}
	})
}
