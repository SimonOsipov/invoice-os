package invoice

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// csNote seeds a note as the superuser, so the test does not depend on Store.Create.
// A nil total stores NULL.
func csNote(t *testing.T, super *pgxpool.Pool, tenant, entity, number, docType, original, status string, total *string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id, status, total, currency)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, 'NGN') RETURNING id`,
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

func TestCreditedSoFar_CountsEachStatusByName(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS each status")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	// power-of-two amounts: any one status flipped gives a different sum
	amounts := map[Status]string{
		StatusDraft: "1.00", StatusValidated: "2.00", StatusQueued: "4.00", StatusSubmitted: "8.00",
		StatusAccepted: "16.00", StatusRejected: "32.00", StatusFailed: "64.00",
	}
	if len(amounts) != 7 {
		t.Fatalf("status table has %d entries, want 7", len(amounts))
	}
	for st, amt := range amounts {
		csNote(t, super, tenant, ent, "CS-"+string(st), "credit_note", o1, string(st), csAmount(amt))
	}

	csWant(t, csCredited(t, app, tenant, o1, ""), "30.00")
}

func TestCreditedSoFar_NullTotalDoesNotHideCountedNotes(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS null mix")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	csNote(t, super, tenant, ent, "CS-A", "credit_note", o1, "accepted", csAmount("10.00"))
	csNote(t, super, tenant, ent, "CS-N", "credit_note", o1, "submitted", nil)

	csWant(t, csCredited(t, app, tenant, o1, ""), "10.00")
}

func TestCreditedSoFar_SumsDecimalsExactlyAtScaleTwo(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS decimals")
	ent := seedEntity(t, super, tenant, "E1")
	small := seedInvoice(t, super, tenant, ent, "CS-SMALL")
	whole := seedInvoice(t, super, tenant, ent, "CS-WHOLE")
	big := seedInvoice(t, super, tenant, ent, "CS-BIG")
	csNote(t, super, tenant, ent, "CS-S1", "credit_note", small, "accepted", csAmount("0.10"))
	csNote(t, super, tenant, ent, "CS-S2", "credit_note", small, "accepted", csAmount("0.20"))
	csNote(t, super, tenant, ent, "CS-W1", "credit_note", whole, "accepted", csAmount("5"))
	csNote(t, super, tenant, ent, "CS-B1", "credit_note", big, "accepted", csAmount("99999999999.99"))
	csNote(t, super, tenant, ent, "CS-B2", "credit_note", big, "queued", csAmount("99999999999.99"))

	csWant(t, csCredited(t, app, tenant, small, ""), "0.30")
	csWant(t, csCredited(t, app, tenant, whole, ""), "5.00")
	csWant(t, csCredited(t, app, tenant, big, ""), "199999999999.98")
}

func TestCreditedSoFar_ExcludesOnlyTheNamedNoteNotItsAmount(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS exclude one")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	a := csNote(t, super, tenant, ent, "CS-A", "credit_note", o1, "accepted", csAmount("20.00"))
	csNote(t, super, tenant, ent, "CS-B", "credit_note", o1, "accepted", csAmount("20.00"))
	draft := csNote(t, super, tenant, ent, "CS-D", "credit_note", o1, "draft", csAmount("3.00"))
	debit := csNote(t, super, tenant, ent, "CS-DB", "debit_note", o1, "accepted", csAmount("9.00"))

	csWant(t, csCredited(t, app, tenant, o1, a), "20.00")
	csWant(t, csCredited(t, app, tenant, o1, draft), "40.00")
	csWant(t, csCredited(t, app, tenant, o1, debit), "40.00")
	csWant(t, csCredited(t, app, tenant, o1, ""), "40.00")
}

func TestCreditedSoFar_MalformedIDsReturnAnError(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS malformed")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	csNote(t, super, tenant, ent, "CS-A", "credit_note", o1, "accepted", csAmount("10.00"))

	ctx := context.Background()
	for _, tc := range []struct{ name, original, exclude string }{
		{"exclude", o1, "not-a-uuid"},
		{"original", "not-a-uuid", ""},
	} {
		got := "unset"
		err := db.WithinTenantTx(ctx, app, tenant, func(tx pgx.Tx) error {
			var e error
			got, e = CreditedSoFarTx(ctx, tx, tc.original, tc.exclude)
			return e
		})
		if err == nil {
			t.Errorf("%s: malformed id returned %q with no error", tc.name, got)
		}
	}
	csWant(t, csCredited(t, app, tenant, o1, ""), "10.00")
}

func TestCreditedSoFar_EachTenantSeesOnlyItsOwnNotes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "CS own A")
	entA := seedEntity(t, super, tenantA, "A1")
	tenantB := seedTenant(t, super, "CS own B")
	entB := seedEntity(t, super, tenantB, "B1")
	oA := seedInvoice(t, super, tenantA, entA, "CS-OA")
	oB := seedInvoice(t, super, tenantB, entB, "CS-OB")
	csNote(t, super, tenantA, entA, "CS-NA", "credit_note", oA, "accepted", csAmount("100.00"))
	csNote(t, super, tenantB, entB, "CS-NB", "credit_note", oB, "accepted", csAmount("7.00"))

	csWant(t, csCredited(t, app, tenantA, oA, ""), "100.00")
	csWant(t, csCredited(t, app, tenantB, oB, ""), "7.00")
	csWant(t, csCredited(t, app, tenantA, oB, ""), "0.00")
	csWant(t, csCredited(t, app, tenantB, oA, ""), "0.00")
}

// 101 near-max totals push the sum past 2^53 cents, where a float sum drifts.
func TestCreditedSoFar_SumStaysExactPastFloatPrecision(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS huge sum")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	if _, err := super.Exec(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id, status, total)
		 SELECT $1, $2, 'CS-BULK-' || g, 'credit_note', $3, 'accepted', 999999999999.99 FROM generate_series(1, 101) g`,
		tenant, ent, o1); err != nil {
		t.Fatalf("seed bulk: %v", err)
	}
	csNote(t, super, tenant, ent, "CS-ONE", "credit_note", o1, "accepted", csAmount("0.12"))

	csWant(t, csCredited(t, app, tenant, o1, ""), "100999999999999.11")
}

func csStamp(t *testing.T, super *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'r' WHERE id = $1`, id); err != nil {
		t.Fatalf("stamp %s: %v", id, err)
	}
}

func TestCreditedSoFar_ResolvedOutsideFailedNoteCounts(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS resolved outside")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	csNote(t, super, tenant, ent, "CS-A", "credit_note", o1, "accepted", csAmount("1.00"))
	stamped := csNote(t, super, tenant, ent, "CS-FS", "credit_note", o1, "failed", csAmount("2.00"))
	csStamp(t, super, stamped)
	csNote(t, super, tenant, ent, "CS-FN", "credit_note", o1, "failed", csAmount("4.00"))
	csStamp(t, super, csNote(t, super, tenant, ent, "CS-DS", "credit_note", o1, "draft", csAmount("8.00")))
	// invoices_kept_as_is_status forbids a stamp on a rejected note, so the unstamped row stands for it.
	csNote(t, super, tenant, ent, "CS-R", "credit_note", o1, "rejected", csAmount("16.00"))

	csWant(t, csCredited(t, app, tenant, o1, ""), "3.00")
	// A stamped failed note does not count itself when it is the note under check.
	csWant(t, csCredited(t, app, tenant, o1, stamped), "1.00")
}

func TestCreditedSoFar_ResolveAndUnresolveOutsideMoveTheSum(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS resolve cycle")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "CS-O1")
	csNote(t, super, tenant, ent, "CS-A", "credit_note", o1, "accepted", csAmount("1.00"))
	failed := csNote(t, super, tenant, ent, "CS-F", "credit_note", o1, "failed", csAmount("4.00"))

	approver := uuid.NewString()
	seedMembership(t, super, tenant, approver, "admin")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: approver, Role: "authenticated", TenantID: tenant})
	store := NewStore(app)

	csWant(t, csCredited(t, app, tenant, o1, ""), "1.00")
	if _, err := store.ResolveOutside(c, failed, "filed manually"); err != nil {
		t.Fatalf("ResolveOutside: %v", err)
	}
	csWant(t, csCredited(t, app, tenant, o1, ""), "5.00")
	if _, err := store.UnresolveOutside(c, failed); err != nil {
		t.Fatalf("UnresolveOutside: %v", err)
	}
	csWant(t, csCredited(t, app, tenant, o1, ""), "1.00")
}
