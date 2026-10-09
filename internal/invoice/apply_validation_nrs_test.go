package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// An NRS edit between the fingerprint and ApplyValidation is stale and writes nothing.
func TestApplyValidation_EditingAnNRSFieldMakesTheVerdictStale(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "ENGI-02-04 stale tenant")
	entityID := seedEntity(t, super, tenantID, "ENGI-02-04 stale entity")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	versionID := seedRuleSetVersionID(t, super)

	edits := map[string]func(id string, lineID string) error{
		"header": func(id, _ string) error {
			_, err := store.Update(c, id, UpdateInput{InvoiceKind: strPtr("381")})
			return err
		},
		"party": func(id, _ string) error {
			_, err := store.Update(c, id, UpdateInput{SupplierState: strPtr("NG-LA")})
			return err
		},
		"line": func(id, lineID string) error {
			_, err := store.Edit(c, id, EditInput{LineItems: &[]LineItemInput{
				{ID: &lineID, Description: strPtr("Widget"), TaxCategory: strPtr("ZERO_RATED")},
			}})
			return err
		},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			inv, err := store.Create(c, CreateInput{
				EntityID: entityID, InvoiceNumber: "ENGI-02-04-" + name,
				LineItems: []LineItemInput{{Description: strPtr("Widget"), TaxCategory: strPtr("STANDARD_VAT")}},
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			staleFP := contentFingerprint(inv, inv.LineItems)
			if err := edit(inv.ID, inv.LineItems[0].ID); err != nil {
				t.Fatalf("edit: %v", err)
			}

			before := snapshotInvoiceGateState(t, super, inv.ID)
			beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
			if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, staleFP); !errors.Is(err, ErrStaleValidation) {
				t.Fatalf("ApplyValidation err = %v, want ErrStaleValidation", err)
			}
			assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), name)
			if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
				t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
			}
		})
	}
}

func TestApplyValidation_FreshFingerprintAfterAnNRSEditSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "ENGI-02-04 fresh tenant")
	entityID := seedEntity(t, super, tenantID, "ENGI-02-04 fresh entity")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ENGI-02-04-fresh"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := store.Update(c, inv.ID, UpdateInput{BuyerLGA: strPtr("Ikeja")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	freshFP := contentFingerprint(updated, updated.LineItems)

	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, seedRuleSetVersionID(t, super), freshFP); err != nil {
		t.Fatalf("ApplyValidation with a fresh fingerprint: %v", err)
	}
}
