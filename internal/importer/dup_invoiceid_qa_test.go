// dup_invoiceid_qa_test.go: QA Mode B adversarial coverage on top of
// task-404 (BUG-08-01) -- the plan's specs prove the id resolves and
// round-trips; these prove it never resolves the WRONG id across a tenant
// or entity boundary, and survives multi-row/unusual-input persistence.
package importer

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestStoreExistingNumbers_CrossTenantSameNumberNeverLeaksForeignID: tenant
// A and tenant B each store their OWN invoice under the identical number, so
// a leaking query has a wrong-but-plausible id to return instead of just an
// empty result. ExistingNumbers now hands back an id, not a bool -- this is
// the failure mode a boolean map could never produce (RLS is the product).
func TestStoreExistingNumbers_CrossTenantSameNumberNeverLeaksForeignID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "BUG-08-01 x-tenant A")
	tenantB := seedTenant(t, super, "BUG-08-01 x-tenant B")
	entityA := seedEntity(t, super, tenantA, "BUG-08-01 x-tenant A entity")
	entityB := seedEntity(t, super, tenantB, "BUG-08-01 x-tenant B entity")

	idA := seedInvoice(t, super, tenantA, entityA, "INV-XTENANT-COLLIDE")
	idB := seedInvoice(t, super, tenantB, entityB, "INV-XTENANT-COLLIDE")

	store := NewStore(app)
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})

	got, err := store.ExistingNumbers(cB, entityB, []string{"INV-XTENANT-COLLIDE"})
	if err != nil {
		t.Fatalf("ExistingNumbers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ExistingNumbers len = %d, want 1: %v", len(got), got)
	}
	id, ok := got["INV-XTENANT-COLLIDE"]
	if !ok || id != idB {
		t.Fatalf("ExistingNumbers[...] = (%q, ok=%v), want (%q, true) -- tenant B's own row", id, ok, idB)
	}
	if id == idA {
		t.Fatalf("ExistingNumbers[...] returned tenant A's id %q -- cross-tenant leak", idA)
	}
}

// TestStoreExistingNumbers_EntityScopedWithinTenantNeverLeaksSiblingID:
// two entities under the SAME tenant (so RLS's tenant_id pin lets both rows
// through) each store their own invoice under the identical number. Only
// the entity_id filter in the query can tell them apart -- proves that
// filter, not merely RLS, resolves the right id.
func TestStoreExistingNumbers_EntityScopedWithinTenantNeverLeaksSiblingID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-08-01 entity-scope tenant")
	entityA := seedEntity(t, super, tenantID, "BUG-08-01 entity-scope A")
	entityB := seedEntity(t, super, tenantID, "BUG-08-01 entity-scope B")

	idA := seedInvoice(t, super, tenantID, entityA, "INV-ENTITY-COLLIDE")
	idB := seedInvoice(t, super, tenantID, entityB, "INV-ENTITY-COLLIDE")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	gotA, err := store.ExistingNumbers(c, entityA, []string{"INV-ENTITY-COLLIDE"})
	if err != nil {
		t.Fatalf("ExistingNumbers(entityA): %v", err)
	}
	if id, ok := gotA["INV-ENTITY-COLLIDE"]; !ok || id != idA || id == idB {
		t.Fatalf("ExistingNumbers(entityA)[...] = (%q, ok=%v), want (%q, true), never entity B's %q", id, ok, idA, idB)
	}

	gotB, err := store.ExistingNumbers(c, entityB, []string{"INV-ENTITY-COLLIDE"})
	if err != nil {
		t.Fatalf("ExistingNumbers(entityB): %v", err)
	}
	if id, ok := gotB["INV-ENTITY-COLLIDE"]; !ok || id != idB || id == idA {
		t.Fatalf("ExistingNumbers(entityB)[...] = (%q, ok=%v), want (%q, true), never entity A's %q", id, ok, idB, idA)
	}

	seedInvoice(t, super, tenantID, entityA, "INV-ENTITY-ONLY-A")
	onlyA, err := store.ExistingNumbers(c, entityB, []string{"INV-ENTITY-ONLY-A"})
	if err != nil {
		t.Fatalf("ExistingNumbers(entityB, A-only number): %v", err)
	}
	if _, ok := onlyA["INV-ENTITY-ONLY-A"]; ok {
		t.Errorf("ExistingNumbers(entityB)[%q] present, want absent -- belongs to sibling entity A", "INV-ENTITY-ONLY-A")
	}
}

// TestImport_StoreDuplicate_MultiRowGroupCarriesSingleCorrectInvoiceID: one
// invoice number spread over two sheet rows (one group, Rows=[2,3]) that
// collides with a stored invoice -- the single resulting errors[] entry
// must carry that invoice's id. Distinct from CountersUnchanged, which pins
// the counts but never reads InvoiceID.
func TestImport_StoreDuplicate_MultiRowGroupCarriesSingleCorrectInvoiceID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-08-01 multirow tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-08-01 multirow entity")
	wantID := seedInvoice(t, super, tenantID, entityID, "INV-MULTIROW")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-MULTIROW", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"), // sheet 2
		mkRow("INV-MULTIROW", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item2", "1", "10.00"), // sheet 3
	}

	res, err := svc.Import(c, entityID, "", "", stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
	}
	re := res.Errors[0]
	if !intSliceEqual(re.Rows, []int{2, 3}) {
		t.Fatalf("Rows = %v, want [2 3] -- fixture assumption broken", re.Rows)
	}
	if re.InvoiceID != wantID {
		t.Errorf("InvoiceID = %q, want %q -- the multi-row group's single entry must carry the collided invoice's id", re.InvoiceID, wantID)
	}
}

// TestImport_StoreDuplicate_MultipleGroupsEachCarryOwnDistinctInvoiceID:
// three DIFFERENT stored numbers, each its own single-row group -- every
// errors[] entry must carry ITS OWN group's id, never a sibling group's
// (guards a shared-loop-variable or first-match-wins bug).
func TestImport_StoreDuplicate_MultipleGroupsEachCarryOwnDistinctInvoiceID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-08-01 multi-group tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-08-01 multi-group entity")
	idA := seedInvoice(t, super, tenantID, entityID, "INV-MULTI-A")
	idB := seedInvoice(t, super, tenantID, entityID, "INV-MULTI-B")
	idC := seedInvoice(t, super, tenantID, entityID, "INV-MULTI-C")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-MULTI-A", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "ItemA", "1", "10.00"), // sheet 2
		mkRow("INV-MULTI-B", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "ItemB", "1", "20.00"), // sheet 3
		mkRow("INV-MULTI-C", "2026-01-12", "T3", "B3", "NGN", "30.00", "3.00", "33.00", "ItemC", "1", "30.00"), // sheet 4
	}

	res, err := svc.Import(c, entityID, "", "", stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Errors) != 3 {
		t.Fatalf("len(Errors) = %d, want 3: %+v", len(res.Errors), res.Errors)
	}

	want := map[int]string{2: idA, 3: idB, 4: idC}
	seen := map[int]bool{}
	for _, re := range res.Errors {
		if len(re.Rows) != 1 {
			t.Fatalf("entry Rows = %v, want a single-row group", re.Rows)
		}
		row := re.Rows[0]
		wantID, ok := want[row]
		if !ok {
			t.Fatalf("unexpected sheet row %d in errors", row)
		}
		seen[row] = true
		if re.InvoiceID != wantID {
			t.Errorf("row %d: InvoiceID = %q, want %q -- each group must carry its OWN resolved id, not a sibling's", row, re.InvoiceID, wantID)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("saw rows %v, want all of {2,3,4}", seen)
	}
}

// TestImport_StoreDuplicate_UnusualInvoiceNumberSurvivesRoundTrip: a very
// long number and a unicode number each resolve their own id and survive
// Finalize -> GetBatch's jsonb round trip -- the id itself is always a
// plain UUID, so this stresses the invoice_number the id is keyed off, not
// the id's own encoding.
func TestImport_StoreDuplicate_UnusualInvoiceNumberSurvivesRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		number string
	}{
		{"very long", "INV-" + strings.Repeat("X", 500)},
		{"unicode", "INV-ΤΙΜΟΛΟΓΙΟ-Ω-发票-2026"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			ctx := context.Background()

			tenantID := seedTenant(t, super, "BUG-08-01 unusual tenant "+tc.name)
			entityID := seedEntity(t, super, tenantID, "BUG-08-01 unusual entity "+tc.name)
			wantID := seedInvoice(t, super, tenantID, entityID, tc.number)

			svc := newTestService(app)
			c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

			rows := [][]string{
				mkRow(tc.number, "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"), // sheet 2
			}

			res, err := svc.Import(c, entityID, "", "", stdMapping, stdHeader, rows, false)
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if len(res.Errors) != 1 {
				t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
			}
			if res.Errors[0].InvoiceID != wantID {
				t.Fatalf("InvoiceID = %q, want %q", res.Errors[0].InvoiceID, wantID)
			}

			store := NewStore(app)
			got, err := store.GetBatch(c, res.ID)
			if err != nil {
				t.Fatalf("GetBatch: %v", err)
			}
			if len(got.Errors) != 1 || got.Errors[0].InvoiceID != wantID {
				t.Fatalf("persisted Errors = %+v, want one entry with InvoiceID %q -- must survive Finalize -> GetBatch jsonb round trip", got.Errors, wantID)
			}
		})
	}
}
