package invoice

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

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

// nrsSample is a storable sample for one NRS field name: a date, a time, a decimal or text.
func nrsSample(name string) any {
	switch name {
	case "DueDate", "TaxPointDate":
		d := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
		return &d
	case "IssueTime":
		return strPtr("14:30:00")
	case "TaxPercent":
		return strPtr("7.50")
	case "BaseQuantity":
		return strPtr("2.000")
	}
	return strPtr("v-" + name)
}

func nrsHeaderNames() []string {
	return []string{
		"InvoiceKind", "TaxCurrencyCode", "DueDate", "IssueTime", "TaxPointDate", "PaymentStatus",
		"SupplierEmail", "SupplierTelephone", "SupplierStreet", "SupplierCity", "SupplierPostalZone", "SupplierCountry", "SupplierState", "SupplierLGA",
		"BuyerEmail", "BuyerTelephone", "BuyerStreet", "BuyerCity", "BuyerPostalZone", "BuyerCountry", "BuyerState", "BuyerLGA",
	}
}

// Setting or clearing any one of the 22 header and party fields, or setting any one of the 9 line fields, after
// the fingerprint makes the verdict stale, and the refused write leaves the gate state and history alone.
func TestApplyValidation_EveryNRSFieldEditMakesTheVerdictStale(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "ENGI-02-04 every-field tenant")
	entityID := seedEntity(t, super, tenantID, "ENGI-02-04 every-field entity")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	versionID := seedRuleSetVersionID(t, super)

	assertStale := func(t *testing.T, inv Invoice, staleFP string) {
		t.Helper()
		before := snapshotInvoiceGateState(t, super, inv.ID)
		beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
		if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, staleFP); !errors.Is(err, ErrStaleValidation) {
			t.Fatalf("ApplyValidation err = %v, want ErrStaleValidation", err)
		}
		assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "stale")
		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
			t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
		}
	}

	headers := nrsHeaderNames()
	if len(headers) != 22 {
		t.Fatalf("%d header and party names, want 22", len(headers))
	}
	for _, name := range headers {
		t.Run("set/"+name, func(t *testing.T) {
			inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ENGI-02-04-set-" + name})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			staleFP := contentFingerprint(inv, inv.LineItems)
			var up UpdateInput
			reflect.ValueOf(&up).Elem().FieldByName(name).Set(reflect.ValueOf(nrsSample(name)))
			if _, err := store.Update(c, inv.ID, up); err != nil {
				t.Fatalf("Update: %v", err)
			}
			assertStale(t, inv, staleFP)
		})
		t.Run("clear/"+name, func(t *testing.T) {
			var in CreateInput
			in.EntityID, in.InvoiceNumber = entityID, "ENGI-02-04-clear-"+name
			reflect.ValueOf(&in).Elem().FieldByName(name).Set(reflect.ValueOf(nrsSample(name)))
			inv, err := store.Create(c, in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			staleFP := contentFingerprint(inv, inv.LineItems)
			var up UpdateInput
			field := reflect.ValueOf(&up).Elem().FieldByName(name)
			if field.Type() == reflect.TypeOf((*time.Time)(nil)) {
				field.Set(reflect.ValueOf(ClearDate))
			} else {
				field.Set(reflect.ValueOf(ClearText))
			}
			if _, err := store.Update(c, inv.ID, up); err != nil {
				t.Fatalf("Update: %v", err)
			}
			assertStale(t, inv, staleFP)
		})
	}

	lineNames := []string{"TaxCategory", "HSNCode", "ISICCode", "ProductCategory", "ServiceCategory", "SellersItemIdentification", "PriceUnit", "TaxPercent", "BaseQuantity"}
	for _, name := range lineNames {
		t.Run("line/"+name, func(t *testing.T) {
			inv, err := store.Create(c, CreateInput{
				EntityID: entityID, InvoiceNumber: "ENGI-02-04-line-" + name,
				LineItems: []LineItemInput{{Description: strPtr("Widget")}, {Description: strPtr("Gadget")}},
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			staleFP := contentFingerprint(inv, inv.LineItems)
			first, second := inv.LineItems[0].ID, inv.LineItems[1].ID
			edited := LineItemInput{ID: &second, Description: strPtr("Gadget")}
			reflect.ValueOf(&edited).Elem().FieldByName(name).Set(reflect.ValueOf(nrsSample(name)))
			if _, err := store.Edit(c, inv.ID, EditInput{LineItems: &[]LineItemInput{{ID: &first, Description: strPtr("Widget")}, edited}}); err != nil {
				t.Fatalf("Edit: %v", err)
			}
			assertStale(t, inv, staleFP)
		})
	}
}

// A fingerprint taken from the stored invoice with every NRS field set still matches the locked row:
// the stored forms (time, decimals, dates) do not make a fresh verdict look stale.
func TestApplyValidation_FingerprintOfAFullyPopulatedStoredInvoiceIsStable(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "ENGI-02-04 stable tenant")
	entityID := seedEntity(t, super, tenantID, "ENGI-02-04 stable entity")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	var in CreateInput
	in.EntityID, in.InvoiceNumber = entityID, "ENGI-02-04-stable"
	for _, name := range nrsHeaderNames() {
		reflect.ValueOf(&in).Elem().FieldByName(name).Set(reflect.ValueOf(nrsSample(name)))
	}
	in.IssueTime = strPtr("14:30")
	line := LineItemInput{Description: strPtr("Widget"), Quantity: strPtr("2"), UnitPrice: strPtr("10"), LineTotal: strPtr("20"), LineTax: strPtr("1.5")}
	for _, name := range []string{"TaxCategory", "HSNCode", "ISICCode", "ProductCategory", "ServiceCategory", "SellersItemIdentification", "PriceUnit", "TaxPercent", "BaseQuantity"} {
		reflect.ValueOf(&line).Elem().FieldByName(name).Set(reflect.ValueOf(nrsSample(name)))
	}
	line.TaxPercent, line.BaseQuantity = strPtr("7.5"), strPtr("1")
	in.LineItems = []LineItemInput{line}

	created, err := store.Create(c, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.Get(c, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if *got.IssueTime != "14:30:00" || *got.LineItems[0].TaxPercent != "7.50" || *got.LineItems[0].BaseQuantity != "1.000" {
		t.Fatalf("stored forms = %q %q %q, want normalised 14:30:00 7.50 1.000", *got.IssueTime, *got.LineItems[0].TaxPercent, *got.LineItems[0].BaseQuantity)
	}
	fp := contentFingerprint(got, got.LineItems)
	if fromCreate := contentFingerprint(created, created.LineItems); fromCreate != fp {
		t.Errorf("fingerprint from Create's result %s differs from Get's %s", fromCreate, fp)
	}
	if _, err := store.ApplyValidation(c, created.ID, []Violation{}, seedRuleSetVersionID(t, super), fp); err != nil {
		t.Fatalf("ApplyValidation with the stored invoice's fingerprint: %v", err)
	}
}
