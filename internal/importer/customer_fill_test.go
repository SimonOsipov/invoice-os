package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	cfTIN     = "12345678-0042"
	cfFailTIN = "99999999-0001"
)

func cfRecordName(t *testing.T, super *pgxpool.Pool, tenantID, tin string) (name *string, n int) {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT name FROM customers WHERE tenant_id = $1 AND tin = $2`, tenantID, tin)
	if err != nil {
		t.Fatalf("read customers: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		n++
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan customers: %v", err)
		}
	}
	return name, n
}

// cfFailFill makes a customers INSERT for (tenantID, tin) raise, as the migrator; dropped on cleanup.
func cfFailFill(t *testing.T, tenantID, tin string) {
	t.Helper()
	url := os.Getenv("DATABASE_MIGRATION_URL")
	if url == "" {
		t.Skip("fill-failure test skipped: set DATABASE_MIGRATION_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect migrator: %v", err)
	}
	t.Cleanup(pool.Close)
	name := "cf_fill_fail_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	for _, q := range []string{
		fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $f$
			BEGIN
				IF NEW.tenant_id = '%s'::uuid AND NEW.tin = '%s' THEN RAISE EXCEPTION 'cf fill refused'; END IF;
				RETURN NEW;
			END $f$`, name, tenantID, tin),
		fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON customers FOR EACH ROW EXECUTE FUNCTION %s()`, name, name),
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("install fill-failure trigger: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON customers`, name))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, name))
	})
}

func cfRow(number, tin, buyer string) []string {
	return mkRow(number, "2026-01-10", tin, buyer, "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00")
}

func TestServiceImport_FillsTheDirectory(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "CF-IMP tenant")
	entityID := seedEntity(t, super, tenantID, "CF-IMP entity")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	rows := [][]string{cfRow("CF-IMP-1", cfTIN, "Importer Buyer"), cfRow("CF-IMP-2", cfTIN, "Importer Buyer")}
	res, err := newTestService(app).Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, false, "")
	if err != nil || res.ReadyInvoices != 2 {
		t.Fatalf("Import: ready=%d err=%v, want 2 ready", res.ReadyInvoices, err)
	}
	name, n := cfRecordName(t, super, tenantID, cfTIN)
	if n != 1 || name == nil || *name != "Importer Buyer" {
		t.Errorf("records for %s = %d (name %v), want 1 named Importer Buyer", cfTIN, n, name)
	}

	const dryTIN = "12345678-0099"
	dry := [][]string{cfRow("CF-IMP-3", dryTIN, "Dry Buyer")}
	if _, err := newTestServiceWithGate(app, &fakeGate{}).Import(c, entityID, "", "", 1, stdMapping, stdHeader, dry, true, ""); err != nil {
		t.Fatalf("Import dry-run: %v", err)
	}
	if _, n := cfRecordName(t, super, tenantID, dryTIN); n != 0 {
		t.Errorf("records for %s after a dry-run = %d, want 0", dryTIN, n)
	}
}

func TestServiceImportDocument_FillsTheDirectory(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "CF-DOC tenant")
	entityID := seedEntity(t, super, tenantID, "CF-DOC entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("CF-DOC-INV"))

	if _, err := newTestService(app).ImportDocument(sxIdentity(context.Background(), tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	name, n := cfRecordName(t, super, tenantID, "87654321-0001")
	if n != 1 || name == nil || *name != "Clean Buyer Ltd" {
		t.Errorf("records for the document's buyer TIN = %d (name %v), want 1 named Clean Buyer Ltd", n, name)
	}
}

func TestServiceImport_FillFailureAbortsTheRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "CF-ABORT tenant")
	entityID := seedEntity(t, super, tenantID, "CF-ABORT entity")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	cfFailFill(t, tenantID, cfFailTIN)

	rows := [][]string{cfRow("CF-AB-1", cfTIN, "Fine Buyer"), cfRow("CF-AB-2", cfFailTIN, "Failing Buyer")}
	_, err := newTestService(app).Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, false, "")
	if err == nil {
		t.Fatal("Import with a failing fill returned nil, want the operational error")
	}
	if errors.Is(err, invoice.ErrValidation) || errors.Is(err, invoice.ErrDuplicateNumber) {
		t.Errorf("Import err = %v, want a non-domain error (the run aborts, no quarantine)", err)
	}
	if _, status, _, _, _ := docBatchRowByEntity(t, super, entityID); status != "failed" {
		t.Errorf("batch status = %q, want failed", status)
	}
	if got := countInvoicesByNumber(t, super, entityID, "CF-AB-2"); got != 0 {
		t.Errorf("invoice CF-AB-2 stored = %d, want 0 (its save rolled back)", got)
	}
	if _, n := cfRecordName(t, super, tenantID, cfFailTIN); n != 0 {
		t.Errorf("records for the failing TIN = %d, want 0", n)
	}
}
