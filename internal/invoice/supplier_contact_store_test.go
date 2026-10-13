package invoice

import (
	"context"
	"testing"
	"time"
)

func setEntityContact(t *testing.T, f nrsFixture, email string, full bool) {
	t.Helper()
	q := `UPDATE business_entities SET email = $2 WHERE id = $1`
	args := []any{f.entityID, email}
	if full {
		q = `UPDATE business_entities SET email = $2, telephone = '+2348000000001', street = '1 Marina', city = 'Ikeja',
		       postal_zone = '100001', country = 'NG', state = 'NG-LA', lga = 'NG-LA-001' WHERE id = $1`
	}
	if _, err := f.super.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("set entity contact: %v", err)
	}
}

func contactOf(inv Invoice) [8]string {
	v := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	return [8]string{v(inv.SupplierEmail), v(inv.SupplierTelephone), v(inv.SupplierStreet), v(inv.SupplierCity),
		v(inv.SupplierPostalZone), v(inv.SupplierCountry), v(inv.SupplierState), v(inv.SupplierLGA)}
}

func TestStoreCreate_SupplierContactDefaultsFromTheCompany(t *testing.T) {
	f := newNRSFixture(t, "SC-01")
	setEntityContact(t, f, "co@x.ng", true)
	want := [8]string{"co@x.ng", "+2348000000001", "1 Marina", "Ikeja", "100001", "NG", "NG-LA", "NG-LA-001"}

	created := f.create(t, "SC-1", CreateInput{})
	if got := contactOf(created); got != want {
		t.Errorf("created contact = %v, want %v", got, want)
	}
	if got := contactOf(f.get(t, created.ID)); got != want {
		t.Errorf("stored contact = %v, want %v", got, want)
	}
}

func TestStoreCreate_CallerSupplierContactWins(t *testing.T) {
	f := newNRSFixture(t, "SC-02")
	setEntityContact(t, f, "co@x.ng", true)

	inv := f.create(t, "SC-2", CreateInput{SupplierEmail: strPtr("caller@x.ng"), SupplierCity: strPtr("")})
	got := contactOf(f.get(t, inv.ID))
	if got[0] != "caller@x.ng" || got[3] != "" || got[2] != "1 Marina" {
		t.Errorf("contact = %v, want caller email, sent-empty city, company street", got)
	}
}

func TestStoreEdit_AppliesNoSupplierContactDefault(t *testing.T) {
	f := newNRSFixture(t, "SC-06")
	inv := f.create(t, "SC-6", CreateInput{})
	if inv.SupplierEmail != nil {
		t.Fatalf("precondition: SupplierEmail = %v, want nil", inv.SupplierEmail)
	}
	setEntityContact(t, f, "late@x.ng", false)

	due := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{DueDate: &due}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got := f.get(t, inv.ID).SupplierEmail; got != nil {
		t.Errorf("SupplierEmail after edit = %q, want nil", *got)
	}
}

func TestStoreCreate_SupplierContactBlankPaddedAndSentEmpty(t *testing.T) {
	f := newNRSFixture(t, "SC-03")
	if _, err := f.super.Exec(context.Background(),
		`UPDATE business_entities SET email = '  co@x.ng ', telephone = '   ', city = 'Ikeja' WHERE id = $1`, f.entityID); err != nil {
		t.Fatalf("set contact: %v", err)
	}
	inv := f.get(t, f.create(t, "SC-3", CreateInput{SupplierStreet: strPtr("")}).ID)
	got := contactOf(inv)
	if got[0] != "co@x.ng" || got[1] != "<nil>" || got[2] != "" || got[3] != "Ikeja" || got[7] != "<nil>" {
		t.Errorf("contact = %v, want trimmed email, nil blank phone, sent-empty street, city, nil lga", got)
	}
	if inv.SupplierName == nil || *inv.SupplierName == "" {
		t.Errorf("SupplierName = %v, want the entity name", inv.SupplierName)
	}
}
