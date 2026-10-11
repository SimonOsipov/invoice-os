package importer

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const scEmail = "co@x.ng"

// seedEntityWithContact seeds an entity holding the 8 NRS contact columns.
func seedEntityWithContact(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO business_entities (tenant_id, name, tin, email, telephone, street, city, postal_zone, country, state, lga)
		 VALUES ($1, 'SC entity', '1234567890', $2, '+2348000000001', '1 Marina', 'Ikeja', '100001', 'NG', 'NG-LA', 'NG-LA-001')
		 RETURNING id`, tenantID, scEmail,
	).Scan(&id); err != nil {
		t.Fatalf("seed entity with contact: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

func storedSupplierContact(t *testing.T, super *pgxpool.Pool, invoiceID string) (email, street, lga *string) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT supplier_email, supplier_street, supplier_lga FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&email, &street, &lga); err != nil {
		t.Fatalf("read supplier contact: %v", err)
	}
	return email, street, lga
}

func TestServiceImport_SupplierContactFromTheCompany(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "SC-IMP tenant")
	entityID := seedEntityWithContact(t, super, tenantID)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("SC-1", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
		mkRow("SC-2", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}
	res, err := newTestService(app).Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, false, "")
	if err != nil || res.ReadyInvoices != 2 {
		t.Fatalf("Import: ready=%d err=%v, want 2 ready", res.ReadyInvoices, err)
	}
	for _, n := range []string{"SC-1", "SC-2"} {
		email, street, lga := storedSupplierContact(t, super, invoiceIDByNumber(t, super, entityID, n))
		if email == nil || *email != scEmail || street == nil || *street != "1 Marina" || lga == nil || *lga != "NG-LA-001" {
			t.Errorf("%s contact = %v/%v/%v, want the company values", n, email, street, lga)
		}
	}
}

func TestServiceImport_DryRunCarriesSupplierContact(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "SC-DRY tenant")
	entityID := seedEntityWithContact(t, super, tenantID)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	g := &fakeGate{}
	rows := [][]string{mkRow("SC-D1", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00")}
	if _, err := newTestServiceWithGate(app, g).Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, true, ""); err != nil {
		t.Fatalf("Import dry-run: %v", err)
	}
	if len(g.evaluateItems) != 1 {
		t.Fatalf("Evaluate items = %d, want 1", len(g.evaluateItems))
	}
	if e := g.evaluateItems[0].Invoice.SupplierEmail; e == nil || *e != scEmail {
		t.Errorf("dry-run SupplierEmail = %v, want %q", e, scEmail)
	}
}

func TestServiceImportDocument_SupplierContactFromTheCompany(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "SC-DOC tenant")
	entityID := seedEntityWithContact(t, super, tenantID)
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("SC-DOC-INV"))

	if _, err := newTestService(app).ImportDocument(sxIdentity(context.Background(), tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	_, street, _ := storedSupplierContact(t, super, invoiceIDByNumber(t, super, entityID, "SC-DOC-INV"))
	if street == nil || *street != "1 Marina" {
		t.Errorf("supplier_street = %v, want the company street", street)
	}
}
