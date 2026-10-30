package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// ncAccepted turns a seeded invoice into an accepted original with an IRN, total and issue date.
func ncAccepted(t *testing.T, super *pgxpool.Pool, id, irn, total string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = 'accepted', irn = $2, total = $3::numeric, issue_date = '2026-03-04' WHERE id = $1`,
		id, irn, total); err != nil {
		t.Fatalf("make %s accepted: %v", id, err)
	}
}

func ncRead(t *testing.T, app *pgxpool.Pool, tenant, original, exclude string, lock bool) originalFacts {
	t.Helper()
	ctx := context.Background()
	var got originalFacts
	if err := db.WithinTenantTx(ctx, app, tenant, func(tx pgx.Tx) error {
		var err error
		got, err = readOriginalFactsTx(ctx, tx, &original, exclude, lock)
		return err
	}); err != nil {
		t.Fatalf("readOriginalFactsTx: %v", err)
	}
	return got
}

func TestReadOriginalFacts_ExcludesTheNamedNoteAndFiltersTheOriginal(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "NC read")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "NC-O1")
	o2 := seedInvoice(t, super, tenant, ent, "NC-O2")
	ncAccepted(t, super, o1, "IRN-O1", "100.00")
	csNote(t, super, tenant, ent, "NC-A", "credit_note", o1, "accepted", csAmount("30.00"))
	n := csNote(t, super, tenant, ent, "NC-N", "credit_note", o1, "validated", csAmount("20.00"))
	csNote(t, super, tenant, ent, "NC-M", "credit_note", o2, "accepted", csAmount("7.00"))

	got := ncRead(t, app, tenant, o1, n, false)
	if !got.Found || got.Number != "NC-O1" || got.DocumentType != submission.CommercialInvoice ||
		got.Status != StatusAccepted {
		t.Errorf("facts = %+v", got)
	}
	if got.IRN == nil || *got.IRN != "IRN-O1" {
		t.Errorf("IRN = %v, want IRN-O1", got.IRN)
	}
	if got.Total == nil || *got.Total != "100.00" {
		t.Errorf("Total = %v, want 100.00", got.Total)
	}
	if got.IssueDate == nil || got.IssueDate.Format("2006-01-02") != "2026-03-04" {
		t.Errorf("IssueDate = %v, want 2026-03-04", got.IssueDate)
	}
	if got.CreditedBefore != "30.00" {
		t.Errorf("CreditedBefore excluding N = %q, want 30.00", got.CreditedBefore)
	}
	if all := ncRead(t, app, tenant, o1, "", false); all.CreditedBefore != "50.00" {
		t.Errorf("CreditedBefore excluding none = %q, want 50.00", all.CreditedBefore)
	}
}

func TestReadOriginalFacts_AnotherTenantReadsAsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "NC tenant A")
	entA := seedEntity(t, super, tenantA, "A1")
	tenantB := seedTenant(t, super, "NC tenant B")
	o1 := seedInvoice(t, super, tenantA, entA, "NC-O1")
	ncAccepted(t, super, o1, "IRN-O1", "100.00")

	if !ncRead(t, app, tenantA, o1, "", false).Found {
		t.Fatal("tenant A does not see its own original")
	}
	if got := ncRead(t, app, tenantB, o1, "", false); got.Found {
		t.Errorf("tenant B read tenant A's original: %+v", got)
	}
}

// ncNowait runs SELECT ... FOR UPDATE NOWAIT on a second connection and returns its error.
func ncNowait(app *pgxpool.Pool, tenant, id string) error {
	return db.WithinTenantTx(context.Background(), app, tenant, func(tx pgx.Tx) error {
		var got string
		return tx.QueryRow(context.Background(), `SELECT id FROM invoices WHERE id = $1 FOR UPDATE NOWAIT`, id).Scan(&got)
	})
}

func TestReadOriginalFacts_LockTrueTakesTheRowLock(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "NC lock")
	ent := seedEntity(t, super, tenant, "E1")
	o1 := seedInvoice(t, super, tenant, ent, "NC-O1")
	ncAccepted(t, super, o1, "IRN-O1", "100.00")
	ctx := context.Background()

	var during error
	if err := db.WithinTenantTx(ctx, app, tenant, func(tx pgx.Tx) error {
		if f, err := readOriginalFactsTx(ctx, tx, &o1, "", true); err != nil || !f.Found {
			t.Errorf("locking read = %+v, %v", f, err)
		}
		during = ncNowait(app, tenant, o1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(during, &pgErr) || pgErr.Code != "55P03" {
		t.Errorf("NOWAIT while locked = %v, want 55P03", during)
	}
	if err := ncNowait(app, tenant, o1); err != nil {
		t.Errorf("NOWAIT after commit: %v, want success", err)
	}

	if err := db.WithinTenantTx(ctx, app, tenant, func(tx pgx.Tx) error {
		if f, err := readOriginalFactsTx(ctx, tx, &o1, "", false); err != nil || !f.Found {
			t.Errorf("plain read = %+v, %v", f, err)
		}
		if err := ncNowait(app, tenant, o1); err != nil {
			t.Errorf("NOWAIT beside a lock=false read: %v, want success", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
