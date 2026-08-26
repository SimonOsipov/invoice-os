// QA gap-fill for AUDIT-11-03: *Store.Number is the ONLY production
// implementation of InvoicePort.Number (cmd/submission/main.go wires invStore),
// and nothing exercised it. internal/submission's suite runs against
// testInvoicePort.Number, an independent copy of the same SQL, so mutating the
// real body to `return "", nil` or to read the wrong column left every test in
// both packages green.
package invoice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestInvoicePort_NumberReadsTheInvoiceNumberColumn pins the value, not the
// presence: a body returning "" or reading id/status compiles, satisfies the
// interface, and freezes a wrong string into every PollWorker audit row.
func TestInvoicePort_NumberReadsTheInvoiceNumberColumn(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "port-number tenant")
	entityID := seedEntity(t, super, tenantID, "port-number entity")

	var port submission.InvoicePort = NewStore(app)

	read := func(invoiceID string) (string, error) {
		var got string
		err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			var err error
			got, err = port.Number(ctx, tx, invoiceID)
			return err
		})
		return got, err
	}

	// Neither a uuid nor any other column's value, so reading id / status / a
	// literal is distinguishable from reading invoice_number.
	const want = "PN-VERBATIM-0001"
	invoiceID := seedInvoice(t, super, tenantID, entityID, want)

	got, err := read(invoiceID)
	if err != nil {
		t.Fatalf("Number: %v (want nil)", err)
	}
	if got != want {
		t.Fatalf("Number = %q, want %q read verbatim off invoices.invoice_number", got, want)
	}
	if got == invoiceID {
		t.Errorf("Number returned the invoice id -- the statement reads the wrong column")
	}

	// The column is the source of truth, so a later edit must be observable: a
	// cached, hardcoded or defaulted body passes the case above and fails here.
	const want2 = "PN-VERBATIM-0002"
	if _, err := super.Exec(ctx, `UPDATE invoices SET invoice_number = $1 WHERE id = $2`, want2, invoiceID); err != nil {
		t.Fatalf("update invoice_number: %v", err)
	}
	got, err = read(invoiceID)
	if err != nil {
		t.Fatalf("Number after update: %v (want nil)", err)
	}
	if got != want2 {
		t.Errorf("Number after update = %q, want %q -- the read is not hitting the column", got, want2)
	}

	// A number carrying JSON and SQL metacharacters must survive verbatim: it
	// travels into an immutable jsonb payload and cannot be repaired later.
	hostile := `PN-"'\{}[]:,/<>&%_-0003`
	if _, err := super.Exec(ctx, `UPDATE invoices SET invoice_number = $1 WHERE id = $2`, hostile, invoiceID); err != nil {
		t.Fatalf("update invoice_number to the hostile value: %v", err)
	}
	got, err = read(invoiceID)
	if err != nil {
		t.Fatalf("Number (hostile value): %v (want nil)", err)
	}
	if got != hostile {
		t.Errorf("Number (hostile value) = %q, want %q byte for byte", got, hostile)
	}
}

// TestRLS_InvoicePortNumberCrossTenantIsAnError is the sibling of
// TestRLS_InvoicePortCrossTenantNotFound for the new method, and pins the
// DELIBERATE asymmetry with HasFiscalOutcome: that one maps RLS 0-rows to
// (false, nil) because false is the honest answer, while Number must NOT map
// 0-rows to ("", nil) -- a blank is indistinguishable from "no number" once it
// is frozen into audit_log (AC-5, D-10).
func TestRLS_InvoicePortNumberCrossTenantIsAnError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "port-number tenant A")
	entityA := seedEntity(t, super, tenantA, "port-number entity A")
	invoiceA := seedInvoice(t, super, tenantA, entityA, "PN-TENANT-A-0001")
	tenantB := seedTenant(t, super, "port-number tenant B")

	var port submission.InvoicePort = NewStore(app)

	var got string
	err := db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		var err error
		got, err = port.Number(ctx, tx, invoiceA)
		return err
	})
	if err == nil {
		t.Fatalf("Number on tenant A's invoice inside tenant B's tx returned (%q, nil), want an error -- "+
			"a silent blank writes a permanently blank invoice_number into an immutable audit row", got)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Number cross-tenant error = %v, want one wrapping pgx.ErrNoRows", err)
	}
	if strings.Contains(got, "PN-TENANT-A") {
		t.Errorf("Number cross-tenant returned %q -- tenant A's number leaked across RLS", got)
	}

	// An id that exists nowhere fails the same closed way.
	err = db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		var err error
		got, err = port.Number(ctx, tx, "00000000-0000-0000-0000-000000000000")
		return err
	})
	if err == nil {
		t.Fatalf("Number on an absent id returned (%q, nil), want an error", got)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Number absent-id error = %v, want one wrapping pgx.ErrNoRows", err)
	}
}
