package invoice

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// csNote seeds a note as the superuser, so the test does not depend on Store.Create.
// A nil total stores NULL.
func csNote(t *testing.T, super *pgxpool.Pool, tenant, entity, number, docType, original, status string, total *string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id, status, total)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::numeric) RETURNING id`,
		tenant, entity, number, docType, original, status, total,
	).Scan(&id); err != nil {
		t.Fatalf("seed %s %s: %v", docType, number, err)
	}
	return id
}

func csAmount(s string) *string { return &s }

// csCredited calls CreditedSoFarTx inside a tenant transaction on the app pool.
func csCredited(t *testing.T, app *pgxpool.Pool, tenant, original, exclude string) string {
	t.Helper()
	ctx := context.Background()
	var got string
	if err := db.WithinTenantTx(ctx, app, tenant, func(tx pgx.Tx) error {
		var err error
		got, err = CreditedSoFarTx(ctx, tx, original, exclude)
		return err
	}); err != nil {
		t.Fatalf("CreditedSoFarTx: %v", err)
	}
	return got
}

func csWant(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("CreditedSoFarTx = %q, want %q", got, want)
	}
}

func TestCreditedSoFar_SumsTheCountedStatuses(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS sums")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	csNote(t, super, tenant, ent, "CS-A", "credit_note", o1, "accepted", csAmount("100.00"))
	csNote(t, super, tenant, ent, "CS-S", "credit_note", o1, "submitted", csAmount("20.00"))
	csNote(t, super, tenant, ent, "CS-Q", "credit_note", o1, "queued", csAmount("3.00"))
	csNote(t, super, tenant, ent, "CS-V", "credit_note", o1, "validated", csAmount("0.50"))

	csWant(t, csCredited(t, app, tenant, o1, ""), "123.50")
}

func TestCreditedSoFar_FiltersByTheOriginal(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS filter")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	o2 := seedInvoice(t, super, tenant, ent, "CS-O2")
	csNote(t, super, tenant, ent, "CS-N1", "credit_note", o1, "accepted", csAmount("100.00"))
	csNote(t, super, tenant, ent, "CS-N2", "credit_note", o2, "accepted", csAmount("7.00"))

	csWant(t, csCredited(t, app, tenant, o1, ""), "100.00")
	csWant(t, csCredited(t, app, tenant, o2, ""), "7.00")
}

func TestCreditedSoFar_SkipsDraftRejectedAndFailed(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS skip statuses")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	csNote(t, super, tenant, ent, "CS-A", "credit_note", o1, "accepted", csAmount("10.00"))
	csNote(t, super, tenant, ent, "CS-D", "credit_note", o1, "draft", csAmount("1.00"))
	csNote(t, super, tenant, ent, "CS-R", "credit_note", o1, "rejected", csAmount("2.00"))
	csNote(t, super, tenant, ent, "CS-F", "credit_note", o1, "failed", csAmount("4.00"))

	csWant(t, csCredited(t, app, tenant, o1, ""), "10.00")
}

func TestCreditedSoFar_SkipsDebitNotes(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS skip debit")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	csNote(t, super, tenant, ent, "CS-C", "credit_note", o1, "accepted", csAmount("10.00"))
	csNote(t, super, tenant, ent, "CS-D", "debit_note", o1, "accepted", csAmount("5.00"))

	csWant(t, csCredited(t, app, tenant, o1, ""), "10.00")
}

func TestCreditedSoFar_ReadsNothingFromAnotherTenant(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "CS tenant A")
	entA := seedEntity(t, super, tenantA, "A1")
	tenantB := seedTenant(t, super, "CS tenant B")
	o1 := seedInvoice(t, super, tenantA, entA, "CS-O1")
	csNote(t, super, tenantA, entA, "CS-A", "credit_note", o1, "accepted", csAmount("100.00"))

	csWant(t, csCredited(t, app, tenantA, o1, ""), "100.00")
	csWant(t, csCredited(t, app, tenantB, o1, ""), "0.00")
}

func TestCreditedSoFar_ZeroWhenNothingCounts(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS zero")
	ent := seedEntity(t, super, tenant, "E1")
	empty := seedInvoice(t, super, tenant, ent, "CS-EMPTY")
	nullTotal := seedInvoice(t, super, tenant, ent, "CS-NULL")
	csNote(t, super, tenant, ent, "CS-N", "credit_note", nullTotal, "accepted", nil)

	csWant(t, csCredited(t, app, tenant, empty, ""), "0.00")
	csWant(t, csCredited(t, app, tenant, nullTotal, ""), "0.00")
}

func TestCreditedSoFar_ExcludesTheNamedNote(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS exclude")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	o2 := seedInvoice(t, super, tenant, ent, "CS-O2")
	csNote(t, super, tenant, ent, "CS-A", "credit_note", o1, "accepted", csAmount("100.00"))
	n := csNote(t, super, tenant, ent, "CS-N", "credit_note", o1, "validated", csAmount("20.00"))
	m := csNote(t, super, tenant, ent, "CS-M", "credit_note", o2, "accepted", csAmount("7.00"))

	csWant(t, csCredited(t, app, tenant, o1, n), "100.00")
	csWant(t, csCredited(t, app, tenant, o1, ""), "120.00")
	csWant(t, csCredited(t, app, tenant, o1, m), "120.00")
}
