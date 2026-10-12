package importer

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Buyer-type values are customers.buyer_type's CHECK set, migrations/20261011140051_buyer_directory.sql.
const btLookupRefused = "bt lookup refused"

const (
	btB2B = "B2B"
	btB2G = "B2G"
	btB2C = "B2C"
)

func btPtr(s string) *string { return &s }

// btRecord seeds a directory record as the superuser; a nil buyerType leaves buyer_type NULL.
func btRecord(t *testing.T, super *pgxpool.Pool, tenantID, tin string, buyerType *string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO customers (tenant_id, tin, name, buyer_type) VALUES ($1, $2, 'BT record', $3)`,
		tenantID, tin, buyerType); err != nil {
		t.Fatalf("seed customers (%s): %v", tin, err)
	}
}

// btDocument seeds a settled reading for a new document: no invoice_kind, buyer_tin = tin, and a number when given.
func btDocument(t *testing.T, super *pgxpool.Pool, tenantID, tin, number string) string {
	t.Helper()
	values := docNoNumberValues()
	if number != "" {
		values["invoice_number"] = sxPtr(number)
	}
	values["buyer_tin"] = sxPtr(tin)
	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, values)
	return documentID
}

// btKind reads the invoice_kind of the invoices citing documentID; it fails unless exactly one cites it.
func btKind(t *testing.T, super *pgxpool.Pool, documentID string) *string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT invoice_kind FROM invoices WHERE source_document_id = $1`, documentID)
	if err != nil {
		t.Fatalf("read invoice_kind: %v", err)
	}
	defer rows.Close()
	var kinds []*string
	for rows.Next() {
		var k *string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan invoice_kind: %v", err)
		}
		kinds = append(kinds, k)
	}
	if len(kinds) != 1 {
		t.Fatalf("invoices citing document %s = %d, want 1", documentID, len(kinds))
	}
	return kinds[0]
}

func btWantKind(t *testing.T, label string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s: invoice_kind = %v, want %s", label, btShow(got), want)
	}
}

func btWantNoKind(t *testing.T, label string, got *string) {
	t.Helper()
	if got != nil {
		t.Errorf("%s: invoice_kind = %q, want NULL", label, *got)
	}
}

func btShow(p *string) string {
	if p == nil {
		return "NULL"
	}
	return *p
}

// btImportDocument imports the document's reading as tenantID's member and returns the filed kind.
func btImportDocument(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID, documentID string) *string {
	t.Helper()
	if _, err := newTestService(app).ImportDocument(sxIdentity(context.Background(), tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	return btKind(t, super, documentID)
}

// btFailLookup makes a top-level SELECT on customers that reaches (tenantID, tin) raise, via a RESTRICTIVE policy
// installed as the superuser (the table owner is not needed); dropped on cleanup. The directory fill after Create
// is an INSERT and stays unaffected.
func btFailLookup(t *testing.T, pool *pgxpool.Pool, tenantID, tin string) (drop func()) {
	t.Helper()
	ctx := context.Background()
	name := "bt_lookup_fail_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	for _, q := range []string{
		fmt.Sprintf(`CREATE FUNCTION %s(t uuid, n text) RETURNS boolean LANGUAGE plpgsql AS $f$
			BEGIN
				IF t = '%s'::uuid AND n = '%s' AND current_query() ~* '^\s*select' THEN RAISE EXCEPTION '%s'; END IF;
				RETURN true;
			END $f$`, name, tenantID, tin, btLookupRefused),
		fmt.Sprintf(`CREATE POLICY %s ON customers AS RESTRICTIVE FOR SELECT USING (%s(tenant_id, tin))`, name, name),
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("install lookup-failure policy: %v", err)
		}
	}
	drop = func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP POLICY IF EXISTS %s ON customers`, name))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s(uuid, text)`, name))
	}
	t.Cleanup(drop)
	return drop
}

func TestRLS_ADocumentImportTakesTheBuyerTypeFromTheDirectory(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-01 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-01 entity")
	const tin = "11111111-0001"
	btRecord(t, super, tenantID, tin, btPtr(btB2G))
	documentID := btDocument(t, super, tenantID, tin, "BT-01-INV")

	btWantKind(t, "tenant A's own record", btImportDocument(t, super, app, tenantID, entityID, documentID), btB2G)
}

func TestRLS_ADocumentImportCopiesEachBuyerTypeAsIs(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-02 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-02 entity")
	cases := []struct{ tin, kind string }{
		{"11111111-0002", btB2B},
		{"11111111-0003", btB2G},
		{"11111111-0004", btB2C},
	}
	if len(cases) == 0 {
		t.Fatal("no buyer types to copy")
	}
	for i, c := range cases {
		btRecord(t, super, tenantID, c.tin, btPtr(c.kind))
		documentID := btDocument(t, super, tenantID, c.tin, fmt.Sprintf("BT-02-INV-%d", i))
		btWantKind(t, c.tin, btImportDocument(t, super, app, tenantID, entityID, documentID), c.kind)
	}
}

func TestRLS_ADocumentImportWithNoDirectoryRecordLeavesTheBuyerTypeMissing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-03 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-03 entity")

	// Control: a document whose TIN has a record is filled, so the fill is wired.
	const withRecord, noRecord = "11111111-0005", "11111111-0006"
	btRecord(t, super, tenantID, withRecord, btPtr(btB2B))
	control := btDocument(t, super, tenantID, withRecord, "BT-03-CTL")
	btWantKind(t, "control", btImportDocument(t, super, app, tenantID, entityID, control), btB2B)

	documentID := btDocument(t, super, tenantID, noRecord, "BT-03-INV")
	btWantNoKind(t, "no record for the TIN", btImportDocument(t, super, app, tenantID, entityID, documentID))
}

func TestRLS_ADocumentImportWithARecordWithoutABuyerTypeLeavesItMissing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-04 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-04 entity")

	const typed, untyped = "11111111-0007", "11111111-0008"
	btRecord(t, super, tenantID, typed, btPtr(btB2C))
	control := btDocument(t, super, tenantID, typed, "BT-04-CTL")
	btWantKind(t, "control", btImportDocument(t, super, app, tenantID, entityID, control), btB2C)

	btRecord(t, super, tenantID, untyped, nil)
	documentID := btDocument(t, super, tenantID, untyped, "BT-04-INV")
	btWantNoKind(t, "record with no buyer type", btImportDocument(t, super, app, tenantID, entityID, documentID))
}

func TestRLS_ADocumentImportNeverTakesAnotherTenantsBuyerType(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "BT-05 tenant A")
	tenantB := seedTenant(t, super, "BT-05 tenant B")
	entityA := seedEntity(t, super, tenantA, "BT-05 entity A")
	entityB := seedEntity(t, super, tenantB, "BT-05 entity B")
	const tin = "11111111-0009"
	btRecord(t, super, tenantB, tin, btPtr(btB2G))

	// Control: the same import as B is filled, so the record is reachable by its owner.
	docB := btDocument(t, super, tenantB, tin, "BT-05-B")
	btWantKind(t, "control, tenant B", btImportDocument(t, super, app, tenantB, entityB, docB), btB2G)

	docA := btDocument(t, super, tenantA, tin, "BT-05-A")
	btWantNoKind(t, "tenant A, record only in B", btImportDocument(t, super, app, tenantA, entityA, docA))
}

func TestRLS_ADocumentImportTakesItsOwnTenantsBuyerType(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "BT-06 tenant A")
	tenantB := seedTenant(t, super, "BT-06 tenant B")
	entityA := seedEntity(t, super, tenantA, "BT-06 entity A")
	const tin = "11111111-0010"
	btRecord(t, super, tenantA, tin, btPtr(btB2B))
	btRecord(t, super, tenantB, tin, btPtr(btB2G))

	documentID := btDocument(t, super, tenantA, tin, "BT-06-A")
	btWantKind(t, "tenant A with a record in both tenants", btImportDocument(t, super, app, tenantA, entityA, documentID), btB2B)
}

func TestRLS_ASuppliedNumberNeverTakesAnotherTenantsBuyerType(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "BT-07 tenant A")
	tenantB := seedTenant(t, super, "BT-07 tenant B")
	entityA := seedEntity(t, super, tenantA, "BT-07 entity A")
	entityB := seedEntity(t, super, tenantB, "BT-07 entity B")
	const tin = "11111111-0011"
	btRecord(t, super, tenantB, tin, btPtr(btB2C))
	svc := newTestService(app)

	// Control: the same supply as B is filled.
	docB := btDocument(t, super, tenantB, tin, "")
	if _, err := svc.SupplyInvoiceNumber(sxIdentity(context.Background(), tenantB), entityB, docB, "BT-07-B"); err != nil {
		t.Fatalf("SupplyInvoiceNumber as B: %v", err)
	}
	btWantKind(t, "control, tenant B", btKind(t, super, docB), btB2C)

	docA := btDocument(t, super, tenantA, tin, "")
	if _, err := svc.SupplyInvoiceNumber(sxIdentity(context.Background(), tenantA), entityA, docA, "BT-07-A"); err != nil {
		t.Fatalf("SupplyInvoiceNumber as A: %v", err)
	}
	btWantNoKind(t, "tenant A, record only in B", btKind(t, super, docA))
}

func TestRLS_ASuppliedNumberTakesTheBuyerTypeFromTheDirectory(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-08 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-08 entity")
	const tin = "11111111-0012"
	btRecord(t, super, tenantID, tin, btPtr(btB2B))
	documentID := btDocument(t, super, tenantID, tin, "")

	if _, err := newTestService(app).SupplyInvoiceNumber(sxIdentity(context.Background(), tenantID), entityID, documentID, "BT-08-INV"); err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v", err)
	}
	btWantKind(t, "supplied number", btKind(t, super, documentID), btB2B)
}

func TestRLS_ADocumentImportLookupFailureFailsTheBatch(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-09 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-09 entity")
	const tin = "11111111-0013"
	btRecord(t, super, tenantID, tin, btPtr(btB2B))
	documentID := btDocument(t, super, tenantID, tin, "BT-09-INV")
	btFailLookup(t, super, tenantID, tin)

	_, err := newTestService(app).ImportDocument(sxIdentity(context.Background(), tenantID), entityID, documentID)
	if err == nil || !strings.Contains(err.Error(), btLookupRefused) {
		t.Errorf("ImportDocument err = %v, want the lookup's error", err)
	}
	if _, status, _, _, _ := docBatchRowByEntity(t, super, entityID); status != "failed" {
		t.Errorf("batch status = %q, want failed", status)
	}
	if n := countInvoicesCitingDocument(t, super, documentID); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
}

func TestRLS_ASuppliedNumberLookupFailureFilesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-10 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-10 entity")
	const tin = "11111111-0014"
	btRecord(t, super, tenantID, tin, btPtr(btB2B))
	documentID := btDocument(t, super, tenantID, tin, "")
	btFailLookup(t, super, tenantID, tin)

	_, err := newTestService(app).SupplyInvoiceNumber(sxIdentity(context.Background(), tenantID), entityID, documentID, "BT-10-INV")
	if err == nil || !strings.Contains(err.Error(), btLookupRefused) {
		t.Errorf("SupplyInvoiceNumber err = %v, want the lookup's error", err)
	}
	if n := countInvoicesCitingDocument(t, super, documentID); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
}

func TestRLS_ABuyerTypeFillSurvivesWhitespaceAroundTheTINOnBothRoutes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-11 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-11 entity")
	const tin = "11111111-0015"
	btRecord(t, super, tenantID, tin, btPtr(btB2G))

	imported := btDocument(t, super, tenantID, " \t"+tin+"  ", "BT-11-IMP")
	btWantKind(t, "import, padded TIN", btImportDocument(t, super, app, tenantID, entityID, imported), btB2G)

	supplied := btDocument(t, super, tenantID, "  "+tin+" ", "")
	if _, err := newTestService(app).SupplyInvoiceNumber(sxIdentity(context.Background(), tenantID), entityID, supplied, "BT-11-SUP"); err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v", err)
	}
	btWantKind(t, "supplied number, padded TIN", btKind(t, super, supplied), btB2G)
}

// A tenant's own record with no buyer type must not fall through to another tenant's typed record.
func TestRLS_ARecordWithoutABuyerTypeNeverFallsThroughToAnotherTenantsOnBothRoutes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "BT-12 tenant A")
	tenantB := seedTenant(t, super, "BT-12 tenant B")
	entityA := seedEntity(t, super, tenantA, "BT-12 entity A")
	const tin = "11111111-0016"
	btRecord(t, super, tenantA, tin, nil)
	btRecord(t, super, tenantB, tin, btPtr(btB2G))

	imported := btDocument(t, super, tenantA, tin, "BT-12-IMP")
	btWantNoKind(t, "import", btImportDocument(t, super, app, tenantA, entityA, imported))

	supplied := btDocument(t, super, tenantA, tin, "")
	if _, err := newTestService(app).SupplyInvoiceNumber(sxIdentity(context.Background(), tenantA), entityA, supplied, "BT-12-SUP"); err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v", err)
	}
	btWantNoKind(t, "supplied number", btKind(t, super, supplied))
}

func TestRLS_ABuyerTypeFillNeverFillsALineTaxCategory(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-13 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-13 entity")
	const tin = "11111111-0017"
	btRecord(t, super, tenantID, tin, btPtr(btB2B))
	values := docCleanValues("BT-13-INV")
	values["buyer_tin"] = sxPtr(tin)
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].quantity"] = sxPtr("2")
	values["line_items[1].unit_price"] = sxPtr("500.00")
	values["line_items[1].line_total"] = sxPtr("1000.00")
	values["line_items[1].line_tax"] = sxPtr("75.00")
	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, values)

	btWantKind(t, "filled kind", btImportDocument(t, super, app, tenantID, entityID, documentID), btB2B)
	rows, err := super.Query(context.Background(),
		`SELECT l.tax_category FROM line_items l JOIN invoices i ON i.id = l.invoice_id WHERE i.source_document_id = $1`, documentID)
	if err != nil {
		t.Fatalf("read line tax categories: %v", err)
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var c *string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
		if c != nil {
			t.Errorf("line tax_category = %q, want NULL", *c)
		}
	}
	if n == 0 {
		t.Fatal("no line items filed, so tax_category was not observed")
	}
}

// A lookup failure files nothing, and once the fault clears the same document files normally.
func TestRLS_ASuppliedNumberRetryAfterALookupFailureFilesOneInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "BT-14 tenant")
	entityID := seedEntity(t, super, tenantID, "BT-14 entity")
	const tin = "11111111-0018"
	btRecord(t, super, tenantID, tin, btPtr(btB2C))
	documentID := btDocument(t, super, tenantID, tin, "")
	drop := btFailLookup(t, super, tenantID, tin)
	svc := newTestService(app)
	ctx := sxIdentity(context.Background(), tenantID)

	if _, err := svc.SupplyInvoiceNumber(ctx, entityID, documentID, "BT-14-INV"); err == nil {
		t.Fatal("SupplyInvoiceNumber with a failing lookup returned no error")
	}
	if n := countInvoicesCitingDocument(t, super, documentID); n != 0 {
		t.Fatalf("invoices after the failure = %d, want 0", n)
	}
	drop()
	if _, err := svc.SupplyInvoiceNumber(ctx, entityID, documentID, "BT-14-INV"); err != nil {
		t.Fatalf("retry after the fault cleared: %v", err)
	}
	btWantKind(t, "retry", btKind(t, super, documentID), btB2C)
}
