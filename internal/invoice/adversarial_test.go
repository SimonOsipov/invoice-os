// M4-02-01 (task-96): QA adversarial/edge/negative coverage ON TOP OF
// INV-STORE-01..13 + TestRLS_InvoicesStoreChildWritesTenantScoped
// (store_test.go / cross_tenant_integration_test.go), written during the
// Mode B (post-implementation) verify pass. These target gaps the AC tests
// don't close: zero line_items, audit payload content, Update's status
// immutability, Update against a truly non-existent id, numeric(14,2)/(14,3)
// boundary-precision round-tripping (no float drift), and List's tie-break
// determinism when created_at collides. Reuses the dbTestPools/seedTenant/
// seedEntity/seedInvoice/mustCount/auditCount harness from store_test.go
// (same package).
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// auditPayload returns the jsonb payload of the most recent audit_log row for
// tenantID+event, mirroring store_test.go's auditActor helper.
func auditPayload(t *testing.T, pool *pgxpool.Pool, tenantID, event string) json.RawMessage {
	t.Helper()
	ctx := context.Background()
	var payload json.RawMessage
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT payload FROM audit_log WHERE event = $1 ORDER BY created_at DESC LIMIT 1`, event,
		).Scan(&payload)
	}); err != nil {
		t.Fatalf("read audit_log payload: %v", err)
	}
	return payload
}

// Adversarial-1: Create with ZERO line_items succeeds (an invoice may have no
// lines yet) -- no line_items rows are (incorrectly) manufactured, and the
// returned Invoice carries no LineItems.
func TestStoreCreate_ZeroLineItemsSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-01 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-01 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ADV-01"})
	if err != nil {
		t.Fatalf("Create (zero line items): want success, got: %v", err)
	}
	if len(inv.LineItems) != 0 {
		t.Errorf("Create (zero line items): returned LineItems = %v, want empty", inv.LineItems)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE invoice_id = $1`, inv.ID); n != 0 {
		t.Errorf("line_items rows for a zero-line Create = %d, want 0", n)
	}
}

// Adversarial-2: the "invoice.created" audit row has the expected event key
// and a non-empty payload carrying the created invoice's id, on the SAME
// tenant as the invoice. INV-STORE-03 only asserts the row COUNT and actor,
// not the payload content -- a Create that wrote an empty {} payload (or the
// wrong invoice id) would still pass INV-STORE-03.
func TestStoreCreate_AuditPayloadCarriesInvoiceID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-02 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-02 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ADV-02"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	payload := auditPayload(t, app, tenantID, "invoice.created")
	if len(payload) == 0 || string(payload) == "{}" {
		t.Fatalf("invoice.created audit payload = %s, want non-empty (carrying at least the invoice id)", payload)
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal audit payload %s: %v", payload, err)
	}
	if decoded.ID != inv.ID {
		t.Errorf("invoice.created audit payload id = %q, want %q", decoded.ID, inv.ID)
	}

	// The invoice row and its audit row are both present, on the SAME
	// tenant -- the "iff" atomicity direction for a SUCCESSFUL Create
	// (the failure direction is INV-STORE-07's assertNothingPersisted).
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE id = $1 AND tenant_id = $2`, inv.ID, tenantID); n != 1 {
		t.Errorf("invoices rows for the created id under its own tenant = %d, want 1", n)
	}
}

// Adversarial-3: Update never changes status, even though it dynamically
// builds its SET clause from UpdateInput's fields. Regression insurance
// against a future field accidentally wired into that dynamic SET builder.
func TestStoreUpdate_NeverChangesStatus(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-03 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-03 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ADV-03"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.Status != StatusDraft {
		t.Fatalf("Create: status = %q, want %q (precondition)", inv.Status, StatusDraft)
	}

	newTotal := "42.00"
	updated, err := store.Update(c, inv.ID, UpdateInput{Total: &newTotal})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != StatusDraft {
		t.Errorf("Update: returned status = %q, want unchanged %q", updated.Status, StatusDraft)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if status != string(StatusDraft) {
		t.Errorf("invoices.status after Update = %q, want unchanged %q", status, StatusDraft)
	}
}

// Adversarial-4: Update against a truly non-existent id (never seeded, not
// merely cross-tenant) resolves to ErrNotFound and writes no audit row.
// INV-STORE-12 only exercises the cross-tenant-but-EXISTING case.
func TestStoreUpdate_NonExistentIDNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-04 tenant")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "invoice.updated"
	before := auditCount(t, app, tenantID, event)

	bogusID := uuid.NewString()
	newTotal := "1.00"
	if _, err := store.Update(c, bogusID, UpdateInput{Total: &newTotal}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(non-existent id) err = %v, want ErrNotFound", err)
	}

	if after := auditCount(t, app, tenantID, event); after != before {
		t.Errorf("audit_log rows for %s after Update(non-existent id) = %d, want unchanged %d", event, after, before)
	}
}

// Adversarial-5: numeric(14,2)/(14,3) boundary-precision values round-trip
// EXACTLY through the ::text cast -- both on invoices (subtotal/vat/total,
// numeric(14,2)) and line_items (quantity numeric(14,3); unit_price/
// line_total/line_tax numeric(14,2)). INV-STORE-05 only exercises a small
// negative value; this exercises the full 14-significant-digit precision
// ceiling, guarding against any accidental float64/pgtype.Numeric
// intermediate representation that would lose precision.
func TestStoreCreate_NumericPrecisionRoundTripsExactly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-05 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-05 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	subtotal := "123456789012.34" // 12 int digits + 2 decimal = 14 sig digits, numeric(14,2) ceiling
	vat := "0.01"
	total := "123456789012.35"
	quantity := "12345678901.123" // 11 int digits + 3 decimal = 14 sig digits, numeric(14,3) ceiling
	unitPrice := "123456789012.34"
	lineTotal := "123456789012.34"
	lineTax := "0.00"

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "ADV-05",
		Subtotal:      &subtotal,
		VAT:           &vat,
		Total:         &total,
		LineItems: []LineItemInput{{
			Quantity:  &quantity,
			UnitPrice: &unitPrice,
			LineTotal: &lineTotal,
			LineTax:   &lineTax,
		}},
	})
	if err != nil {
		t.Fatalf("Create (boundary-precision numerics): %v", err)
	}

	checkStr := func(label string, got *string, want string) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %v, want %q (exact round-trip, no float drift)", label, got, want)
		}
	}
	checkStr("Subtotal", inv.Subtotal, subtotal)
	checkStr("VAT", inv.VAT, vat)
	checkStr("Total", inv.Total, total)
	if len(inv.LineItems) != 1 {
		t.Fatalf("len(LineItems) = %d, want 1", len(inv.LineItems))
	}
	li := inv.LineItems[0]
	checkStr("LineItems[0].Quantity", li.Quantity, quantity)
	checkStr("LineItems[0].UnitPrice", li.UnitPrice, unitPrice)
	checkStr("LineItems[0].LineTotal", li.LineTotal, lineTotal)
	checkStr("LineItems[0].LineTax", li.LineTax, lineTax)

	// Re-read independently (bypassing Store.Get, which shares scanInvoice
	// with Create) to rule out a RETURNING-clause-only coincidence.
	var subtotalDB, vatDB, totalDB string
	if err := super.QueryRow(ctx,
		`SELECT subtotal::text, vat::text, total::text FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&subtotalDB, &vatDB, &totalDB); err != nil {
		t.Fatalf("read back invoice numerics: %v", err)
	}
	if subtotalDB != subtotal || vatDB != vat || totalDB != total {
		t.Errorf("invoices numeric read-back = (%q,%q,%q), want (%q,%q,%q)", subtotalDB, vatDB, totalDB, subtotal, vat, total)
	}

	var qtyDB, upDB, ltDB, taxDB string
	if err := super.QueryRow(ctx,
		`SELECT quantity::text, unit_price::text, line_total::text, line_tax::text FROM line_items WHERE invoice_id = $1`, inv.ID,
	).Scan(&qtyDB, &upDB, &ltDB, &taxDB); err != nil {
		t.Fatalf("read back line_items numerics: %v", err)
	}
	if qtyDB != quantity || upDB != unitPrice || ltDB != lineTotal || taxDB != lineTax {
		t.Errorf("line_items numeric read-back = (%q,%q,%q,%q), want (%q,%q,%q,%q)",
			qtyDB, upDB, ltDB, taxDB, quantity, unitPrice, lineTotal, lineTax)
	}
}

// Adversarial-6: List's ORDER BY created_at DESC, id DESC tie-breaks
// deterministically when created_at COLLIDES across rows -- INV-STORE-10
// never forces a collision (real Create calls get distinct now() values), so
// it can't observe whether the id DESC tie-break clause is actually present
// vs. accidentally dropped.
func TestStoreList_TieBreaksByIDOnCollidingCreatedAt(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-06 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-06 entity")

	sameTS := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 3; i++ {
		var id string
		if err := super.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, created_at) VALUES ($1, $2, $3, $4) RETURNING id`,
			tenantID, entityID, uuid.NewString(), sameTS,
		).Scan(&id); err != nil {
			t.Fatalf("seed invoice %d with colliding created_at: %v", i, err)
		}
		ids = append(ids, id)
		t.Cleanup(func(id string) func() {
			return func() { _, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id) }
		}(id))
	}

	// Expected order: id DESC (the ORDER BY clause's tie-break for equal
	// created_at). Canonical lowercase UUID text ordering matches Postgres's
	// uuid byte-value ordering (hyphens sit at identical fixed positions).
	wantOrder := make([]string, len(ids))
	copy(wantOrder, ids)
	for i := 0; i < len(wantOrder); i++ {
		for j := i + 1; j < len(wantOrder); j++ {
			if wantOrder[j] > wantOrder[i] {
				wantOrder[i], wantOrder[j] = wantOrder[j], wantOrder[i]
			}
		}
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("List total = %d, want 3", total)
	}
	if len(items) != 3 {
		t.Fatalf("List len = %d, want 3", len(items))
	}
	for i, want := range wantOrder {
		if items[i].ID != want {
			t.Errorf("List order[%d] = %q, want %q (id DESC tie-break on colliding created_at)", i, items[i].ID, want)
		}
	}
}
