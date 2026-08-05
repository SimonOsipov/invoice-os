// buyer_name on a reserved TIN doubles as an operator-facing label for what submitting it
// will do. A relabel that moves the "always accepts" name onto -0002 (whose real
// mockTriggerFor is Reject) would mislead every demo without failing any existing check --
// TestSeedOneBuyerNamePerBuyerTIN only requires ONE name per TIN, not the RIGHT one.
//
// Package `submission` (whitebox): mockTriggerFor and the TIN constants are unexported.
package submission

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// srtFirmTenantID/srtInHouseTenantID mirror db/seed.dev.sql's two demo tenants. Duplicated,
// not imported: internal/platform/db does not export them.
const (
	srtFirmTenantID    = "11111111-1111-1111-1111-111111111111"
	srtInHouseTenantID = "22222222-2222-2222-2222-222222222222"
)

// reservedTINLabelSubstring is the lowercase phrase buyer_name must contain for each real
// mockTriggerFor outcome -- checked against the actual function, not a second-guessed map of
// which TIN does what.
var reservedTINLabelSubstring = map[mockTrigger]string{
	mockTriggerAccept:      "accepts",
	mockTriggerReject:      "rejects",
	mockTriggerPending:     "defers",
	mockTriggerUnavailable: "unavailable",
	mockTriggerTimeout:     "times out",
}

// TestSeedReservedTINBuyerNameMatchesDerivedTrigger: for every seeded DEMO-2026-* row on a
// reserved TIN, buyer_name must describe what mockTriggerFor actually does with that TIN.
func TestSeedReservedTINBuyerNameMatchesDerivedTrigger(t *testing.T) {
	superDSN := sehRequireSuperuserDSN(t)
	pool, err := pgxpool.New(context.Background(), superDSN)
	if err != nil {
		t.Fatalf("open superuser pool: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT invoice_number, buyer_tin, coalesce(buyer_name, '') FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%' AND buyer_tin LIKE $2`,
		[]string{srtFirmTenantID, srtInHouseTenantID}, mockReservedPrefix+"%",
	)
	if err != nil {
		t.Fatalf("query reserved-TIN buyer_names: %v", err)
	}
	defer rows.Close()

	var checked int
	for rows.Next() {
		var number, tin, name string
		if err := rows.Scan(&number, &tin, &name); err != nil {
			t.Fatalf("scan reserved-TIN row: %v", err)
		}
		trigger := mockTriggerFor(tin)
		want, ok := reservedTINLabelSubstring[trigger]
		if !ok {
			t.Errorf("%s: buyer_tin=%s drives mockTriggerFor -> %q, an outcome this test has no expected label for", number, tin, trigger)
			continue
		}
		checked++
		if !strings.Contains(strings.ToLower(name), want) {
			t.Errorf("%s: buyer_tin=%s drives mockTriggerFor -> %q, but buyer_name = %q does not mention %q",
				number, tin, trigger, name, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate reserved-TIN rows: %v", err)
	}
	if checked == 0 {
		t.Fatal("zero reserved-TIN rows checked -- the LIKE filter or the label map is wrong")
	}
}
