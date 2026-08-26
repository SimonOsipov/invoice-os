// Adversarial siblings of audit_number_test.go: the failure modes the
// acceptance set does not reach -- a hostile number, a second tenant, and
// concurrent writers. Run: `DEV_DB_PORT=5442 make test-invoice`.
package invoice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// auditNumberRowFor reads the audit_log row whose generated invoice_id is
// invID. Addressing by that column rather than by recency is what lets the
// concurrency test below tell two interleaved writers' rows apart.
func auditNumberRowFor(t *testing.T, app *pgxpool.Pool, tenantID, event, invID string) auditNumberRow {
	t.Helper()
	ctx := context.Background()
	var r auditNumberRow
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE event = $1 AND invoice_id = $2::uuid`, event, invID,
		).Scan(&r.rows); err != nil {
			return err
		}
		if r.rows == 0 {
			return nil
		}
		return tx.QueryRow(ctx,
			`SELECT payload, payload->>'`+auditNumberKey+`', invoice_id::text, entity_id::text
			   FROM audit_log WHERE event = $1 AND invoice_id = $2::uuid
			  ORDER BY created_at DESC, id DESC LIMIT 1`, event, invID,
		).Scan(&r.payload, &r.number, &r.invoiceID, &r.entityID)
	}); err != nil {
		t.Fatalf("read audit_log row for %q/%s: %v", event, invID, err)
	}
	return r
}

// countAuditByNumber counts, as tenantID, the audit rows carrying number under
// the new key -- the reader's free-text arm is a values-only jsonb scan, so
// this is the shape a cross-tenant search would take.
func countAuditByNumber(t *testing.T, app *pgxpool.Pool, tenantID, number string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE payload->>'`+auditNumberKey+`' = $1`, number,
		).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log by %s=%q as %s: %v", auditNumberKey, number, tenantID, err)
	}
	return n
}

// A number is caller-controlled text with no CHECK behind it. If any writer
// ever assembled its payload as a string instead of marshaling a map, a number
// shaped like JSON could add or REPLACE the "id" key -- and audit_log.invoice_id
// plus the entity trigger would then point the row at whichever invoice the
// caller named. TestAuditNumber_NumberIsVerbatimNeverDerived's fixture carries
// no JSON metacharacters and never asserts that "id" survived, so it cannot see
// this.
func TestAuditNumberAdversarial_AJSONShapedNumberCannotRewriteTheIdKey(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "AN-ADV injection tenant")
	entityID := seedEntity(t, super, tenantID, "AN-ADV injection entity")
	c, _ := auditNumberIdentity(tenantID)
	store := NewStore(app)

	decoy, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AN-ADV-DECOY"})
	if err != nil {
		t.Fatalf("Create decoy: %v", err)
	}

	// Closes the "id" string, re-opens it pointing at the decoy, then trails a
	// backslash, a quote, a tab, a newline and a 4-byte rune.
	hostile := `", "id": "` + decoy.ID + `", "z": "\" ` + "\t\n" + `🧾`

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: hostile})
	if err != nil {
		t.Fatalf("Create with a JSON-shaped number %q: %v", hostile, err)
	}
	if stored := mustInvoiceNumber(t, super, inv.ID); stored != hostile {
		t.Fatalf("invoices.invoice_number = %q, want %q -- the column mangled it, so the payload assertions would be meaningless", stored, hostile)
	}

	row := readAuditNumberRow(t, app, tenantID, "invoice.created")
	if row.rows != 2 {
		t.Fatalf("invoice.created rows = %d, want 2 (the decoy and the hostile one) -- the fixture did not drive both writers", row.rows)
	}

	if got := auditNumberKeys(t, row.payload); strings.Join(got, ",") != "id,"+auditNumberKey {
		t.Errorf("payload keys = [%s], want [id,%s] -- the number leaked out of its own value; payload = %s",
			strings.Join(got, ","), auditNumberKey, row.payload)
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(row.payload, &decoded); err != nil {
		t.Fatalf("unmarshal payload %s: %v", row.payload, err)
	}
	if decoded.ID != inv.ID {
		t.Errorf("payload id = %q, want %q (the decoy is %q)", decoded.ID, inv.ID, decoy.ID)
	}
	if row.invoiceID == nil || *row.invoiceID != inv.ID {
		t.Errorf("audit_log.invoice_id = %v, want %q (the decoy is %q)", row.invoiceID, inv.ID, decoy.ID)
	}
	if want := mustInvoiceEntityID(t, super, inv.ID); row.entityID == nil || *row.entityID != want {
		t.Errorf("audit_log.entity_id = %v, want %q", row.entityID, want)
	}
	if row.number == nil {
		t.Fatalf("payload->>'%s' is SQL NULL; payload = %s", auditNumberKey, row.payload)
	}
	if *row.number != hostile {
		t.Errorf("payload->>'%s' = %q, want %q byte-identically", auditNumberKey, *row.number, hostile)
	}
}

// This story is the first thing to put a human-facing invoice number in an
// audit payload, so "search the audit log for INV-2026-0042" becomes possible
// for the first time -- and with it the question of whether it reaches another
// firm's row. audit_log's tenant_isolation policy is the answer; both positive
// controls are here so a zero cannot come from an empty table or an absent key.
func TestAuditNumberAdversarial_ThePayloadNumberIsNotReadableAcrossTenants(t *testing.T) {
	super, app := dbTestPools(t)

	secret := "AN-ADV-SECRET-" + uuid.NewString()

	tenantA := seedTenant(t, super, "AN-ADV tenant A")
	entityA := seedEntity(t, super, tenantA, "AN-ADV entity A")
	ca, _ := auditNumberIdentity(tenantA)
	if _, err := NewStore(app).Create(ca, CreateInput{EntityID: entityA, InvoiceNumber: secret}); err != nil {
		t.Fatalf("Create in tenant A: %v", err)
	}

	tenantB := seedTenant(t, super, "AN-ADV tenant B")
	entityB := seedEntity(t, super, tenantB, "AN-ADV entity B")
	cb, _ := auditNumberIdentity(tenantB)
	if _, err := NewStore(app).Create(cb, CreateInput{EntityID: entityB, InvoiceNumber: "AN-ADV-B-OWN"}); err != nil {
		t.Fatalf("Create in tenant B: %v", err)
	}

	if n := countAuditByNumber(t, app, tenantA, secret); n != 1 {
		t.Fatalf("tenant A searching its OWN number %q matched %d rows, want 1 -- without this the refusal below could be vacuous", secret, n)
	}
	if n := countAuditByNumber(t, app, tenantB, "AN-ADV-B-OWN"); n != 1 {
		t.Fatalf("tenant B searching its OWN number matched %d rows, want 1 -- tenant B cannot see its own audit rows, so the refusal below would prove nothing", n)
	}
	if n := countAuditByNumber(t, app, tenantB, secret); n != 0 {
		t.Errorf("tenant B searching tenant A's number %q matched %d rows, want 0 -- the new key is a cross-tenant discovery channel", secret, n)
	}
}

// Every acceptance test drives ONE invoice per tenant, serially, so a number
// bound to the wrong request -- a package-level cache, a memoised lookup, a
// hoisted variable -- would still read back correctly there. Interleaved
// writers over distinct invoices are what make that visible.
func TestAuditNumberAdversarial_ConcurrentWritersNeverCrossNumbers(t *testing.T) {
	super, app := dbTestPools(t)

	const writers = 8

	tenantID := seedTenant(t, super, "AN-ADV concurrent tenant")
	entityID := seedEntity(t, super, tenantID, "AN-ADV concurrent entity")
	store := NewStore(app)

	ids := make([]string, writers)
	numbers := make([]string, writers)
	for i := range ids {
		numbers[i] = fmt.Sprintf("AN-ADV-CONC-%02d", i)
		ids[i] = seedInvoice(t, super, tenantID, entityID, numbers[i])
	}

	var wg sync.WaitGroup
	errs := make([]error, writers)
	wg.Add(writers)
	for i := range ids {
		go func(i int) {
			defer wg.Done()
			c, _ := auditNumberIdentity(tenantID)
			buyer := fmt.Sprintf("AN-ADV buyer %02d", i)
			_, errs[i] = store.Update(c, ids[i], UpdateInput{BuyerName: &buyer})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Update %d: %v", i, err)
		}
	}

	for i, id := range ids {
		row := auditNumberRowFor(t, app, tenantID, "invoice.updated", id)
		if row.rows != 1 {
			t.Fatalf("invoice %s (%s) has %d invoice.updated rows, want 1", numbers[i], id, row.rows)
		}
		if row.number == nil {
			t.Fatalf("invoice %s: payload->>'%s' is SQL NULL; payload = %s", numbers[i], auditNumberKey, row.payload)
		}
		if *row.number != numbers[i] {
			t.Errorf("invoice %s (%s): payload->>'%s' = %q, want %q -- a concurrent writer's number reached the wrong row",
				numbers[i], id, auditNumberKey, *row.number, numbers[i])
		}
	}
}
