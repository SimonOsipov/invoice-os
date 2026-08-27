// INVCR-01-17 (task-293, C7 fix): the regression suite for the manual-create
// sibling of M4-04-08's bug (internal/importer/service_tin_test.go). The
// SAME defect class: portfolio.ValidateTIN canonicalizes an entity's TIN to
// 12 bare digits on write; the MBS rule supplier-tin-format demands
// NNNNNNNN-NNNN. The importer's entity -> invoice boundary already restores
// the hyphen ([supplier-from-entity]); POST /v1/invoices (Store.Create) did
// not, so an API-created entity's manually-created invoices FALSELY failed
// supplier-tin-format.
//
// WHY THIS FILE EXISTS, precisely mirroring service_tin_test.go's own
// header: the test gap IS the defect. Every pre-existing Store.Create spec
// seeds its entity via seedEntity/seedEntityWithTIN -- a raw SQL INSERT that
// bypasses portfolio.ValidateTIN entirely, so it is structurally blind to
// canonicalization. These specs go through the REAL portfolio.Store.Create,
// exactly as production's POST /api/portfolio/v1/entities does.
//
// package invoice (an INTERNAL test file, not invoice_test): importing
// internal/portfolio here does NOT add a production import edge --
// `go list -deps ./internal/invoice` (non-test only) proves it, the same
// precedent gate_test.go already established for internal/validation and
// service_tin_test.go established for internal/importer importing
// internal/portfolio.
package invoice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/portfolio"
)

// createEntityViaRealPortfolioStore creates ONE business_entities row through
// the REAL portfolio.Store.Create -- ValidateTIN, Luhn, canonicalization and
// all -- i.e. the exact path POST /api/portfolio/v1/entities takes in
// production. Mirrors internal/importer/service_tin_test.go's helper of the
// identical name and purpose. Returns the entity id and the CANONICAL tin as
// actually persisted.
func createEntityViaRealPortfolioStore(t *testing.T, super, app *pgxpool.Pool, tenantID, name, rawTIN string) (entityID, canonicalTIN string) {
	t.Helper()
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
	})
	ent, err := portfolio.NewStore(app).Create(ctx, portfolio.CreateInput{Name: name, TIN: rawTIN})
	if err != nil {
		t.Fatalf("portfolio.Store.Create(tin=%q): %v", rawTIN, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, ent.ID)
	})
	if ent.TIN == nil {
		t.Fatalf("portfolio.Store.Create returned a nil TIN for input %q", rawTIN)
	}
	return ent.ID, *ent.TIN
}

// c7FIRSTIN is a Luhn-valid hyphenated 8+4 FIRS TIN, so portfolio's
// ValidateTIN ACCEPTS it and canonicalizes it to "100123450007" -- 12 bare
// digits, exactly what supplier-tin-format rejects without the restore.
// Same fixture value as internal/importer/service_tin_test.go's
// tinFixFIRSTIN (that file's own drift guard already proves the round trip;
// no need to re-derive it here).
const c7FIRSTIN = "10012345-0007"

// c7CanonicalFIRSTIN is ValidateTIN(c7FIRSTIN)'s known output, pinned as a
// premise check the same way service_tin_test.go pins it.
const c7CanonicalFIRSTIN = "100123450007"

// c7JTBTIN is a Luhn-valid 10-digit JTB TIN -- no hyphen to restore. Same
// fixture value as service_tin_test.go's tinFixJTBTIN.
const c7JTBTIN = "1001230000"

// TestStoreCreate_SupplierTINAndNameDerivedFromEntityOverridingCaller
// (AC #1, #2, #6, #8): an invoice created for an entity whose stored tin is
// 12 bare digits is persisted with supplier_tin restored to NNNNNNNN-NNNN
// and supplier_name taken from the entity -- and this happens even though
// the CALLER sent different (wrong) values in the request, proving the
// override (not a 400) ruling AC #8 calls for. The restored TIN then passes
// supplier-tin-format on a real Validate call, closing the loop the
// importer's own TestServiceImport_APICreatedEntityCleanFileHasNoFalseTin...
// closes for the import path.
func TestStoreCreate_SupplierTINAndNameDerivedFromEntityOverridingCaller(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "C7-01 tenant")
	const entityName = "C7-01 API-created Supplier Co"
	entityID, canonicalTIN := createEntityViaRealPortfolioStore(t, super, app, tenantID, entityName, c7FIRSTIN)

	// Pin the premise: the entity really is stored hyphen-free, the exact
	// shape supplier-tin-format rejects (service_tin_test.go's own pattern).
	if canonicalTIN != c7CanonicalFIRSTIN {
		t.Fatalf("premise broken: portfolio.Store.Create persisted tin %q, want %q", canonicalTIN, c7CanonicalFIRSTIN)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	in := gapiValidInvoiceInput(entityID, "C7-01")
	// Deliberately WRONG values -- proves Store.Create overrides the caller's
	// wire body rather than trusting it (AC #8).
	in.SupplierTIN = strPtr("00000000-0000")
	in.SupplierName = strPtr("Wrong Co (should never be persisted)")

	inv, err := store.Create(c, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if inv.SupplierTIN == nil || *inv.SupplierTIN != c7FIRSTIN {
		t.Errorf("Create returned supplier_tin = %v, want the entity's restored %q (NOT the caller's %q)",
			inv.SupplierTIN, c7FIRSTIN, "00000000-0000")
	}
	if inv.SupplierName == nil || *inv.SupplierName != entityName {
		t.Errorf("Create returned supplier_name = %v, want the entity's %q (NOT the caller's %q)",
			inv.SupplierName, entityName, "Wrong Co (should never be persisted)")
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

	// Close the loop: the restored TIN must pass supplier-tin-format on a
	// REAL validate call against the active rule set -- the whole point of
	// the fix, not just a stored-value assertion.
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
		t.Errorf("violations = %s, want [] (supplier-tin-format must NOT fire once the hyphen is restored)", got.Violations)
	}
}

// TestStoreCreate_JTBEntityTINPassesThroughUnchanged (AC #3): a 10-digit
// JTB entity TIN has no hyphen to restore and must NOT be hyphenated into a
// fabricated 8+4 FIRS shape -- matches the importer's canonicalFIRSTIN guard
// (MBSSupplierTIN only matches exactly 12 bare digits). The resulting
// supplier-tin-format violation is GENUINE (a real JTB-vs-MBS mismatch), not
// a formatting bug -- mirrors
// TestServiceImport_APICreatedJTBEntityReportsGenuineTinFormatViolation on
// the import path.
func TestStoreCreate_JTBEntityTINPassesThroughUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "C7-02 tenant")
	entityID, canonicalTIN := createEntityViaRealPortfolioStore(t, super, app, tenantID, "C7-02 JTB Supplier Co", c7JTBTIN)

	if canonicalTIN != c7JTBTIN {
		t.Fatalf("premise broken: JTB tin persisted as %q, want %q unchanged", canonicalTIN, c7JTBTIN)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	in := gapiValidInvoiceInput(entityID, "C7-02")
	in.SupplierTIN = strPtr("this value must be ignored")

	inv, err := store.Create(c, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.SupplierTIN == nil || *inv.SupplierTIN != c7JTBTIN {
		t.Errorf("Create returned supplier_tin = %v, want the JTB tin %q UNCHANGED (no hyphen inserted)", inv.SupplierTIN, c7JTBTIN)
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
	if !hasViolation(vs, "supplier-tin-format") {
		t.Errorf("violations = %+v, want one naming supplier-tin-format -- a 10-digit JTB TIN "+
			"genuinely cannot satisfy the MBS 8+4 rule; fabricating a hyphen would hide a real signal", vs)
	}
}

// TestStoreCreate_NilEntityTINOverridesCallerSuppliedValue (AC #8, override
// edge case): a tin-less entity (seedEntity leaves tin NULL) still
// overrides the caller's supplied supplier_tin with nil -- Store.Create must
// not fall back to the caller's value just because the entity has nothing
// of its own. Mirrors the importer's nil-stays-nil contract
// (TestServiceImport_TinLessEntityCommitsWithNilSupplierTIN,
// TestMBSSupplierTIN_LeavesUncanonicalizedValuesAlone).
func TestStoreCreate_NilEntityTINOverridesCallerSuppliedValue(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "C7-04 tenant")
	const entityName = "C7-04 tin-less entity"
	entityID := seedEntity(t, super, tenantID, entityName)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "C7-04",
		SupplierTIN:   strPtr("99999999-9999"), // must be discarded, not stored
		SupplierName:  strPtr("Wrong Co"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.SupplierTIN != nil {
		t.Errorf("Create returned supplier_tin = %q, want nil (the entity has no tin of its own -- "+
			"the caller's value must NOT be used as a fallback)", *inv.SupplierTIN)
	}
	if inv.SupplierName == nil || *inv.SupplierName != entityName {
		t.Errorf("Create returned supplier_name = %v, want the entity's %q", inv.SupplierName, entityName)
	}

	var supplierTIN *string
	var supplierName string
	if err := super.QueryRow(ctx,
		`SELECT supplier_tin, supplier_name FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&supplierTIN, &supplierName); err != nil {
		t.Fatalf("read back invoice: %v", err)
	}
	if supplierTIN != nil {
		t.Errorf("supplier_tin read back = %q, want NULL", *supplierTIN)
	}
	if supplierName != entityName {
		t.Errorf("supplier_name read back = %q, want %q", supplierName, entityName)
	}
}

// TestStoreCreate_BuyerTINNotNormalized (AC #4): buyer_tin is the CALLER's
// data, never touched by the [supplier-from-entity] derivation -- a bare
// 12-digit buyer TIN (the EXACT shape that would be hyphen-restored if the
// buyer field were treated like the supplier field) must persist byte-
// unchanged and still fire buyer-tin-format on validate. Scope-fence check
// for AC #4/#7: "supplier fields ONLY".
func TestStoreCreate_BuyerTINNotNormalized(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "C7-03 tenant")
	entityID, _ := createEntityViaRealPortfolioStore(t, super, app, tenantID, "C7-03 Supplier Co", c7FIRSTIN)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	const bareBuyerTIN = "876543210002" // 12 bare digits -- canonicalFIRSTIN's own shape
	in := gapiValidInvoiceInput(entityID, "C7-03")
	in.BuyerTIN = strPtr(bareBuyerTIN)

	inv, err := store.Create(c, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.BuyerTIN == nil || *inv.BuyerTIN != bareBuyerTIN {
		t.Errorf("Create returned buyer_tin = %v, want %q UNCHANGED -- buyer_tin must never be normalized", inv.BuyerTIN, bareBuyerTIN)
	}

	var buyerTIN string
	if err := super.QueryRow(ctx,
		`SELECT buyer_tin FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&buyerTIN); err != nil {
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
		t.Errorf("violations = %+v, want one naming buyer-tin-format -- an unnormalized bare-digit "+
			"buyer TIN must still violate (buyer_tin is user-supplied and MUST still be able to violate)", vs)
	}
}
