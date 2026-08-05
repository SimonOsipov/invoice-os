// failure_kind_test.go: invoices.failure_kind, the reason a submission
// landed in status='failed'. Written RED, before the column exists -- see
// the BUG-06-01 story / task-383 Test Specs table.
package invoice

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// failureKindOf reads the column directly via the superuser pool: it is NOT
// on Invoice/invoiceColumns for this subtask (BUG-06-02 wires that up).
func failureKindOf(t *testing.T, super *pgxpool.Pool, invoiceID string) *string {
	t.Helper()
	var out *string
	if err := super.QueryRow(context.Background(),
		`SELECT failure_kind FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&out); err != nil {
		t.Fatalf("read invoices.failure_kind: %v", err)
	}
	return out
}

// failureKindIsNull mirrors source_rows_test.go's sourceRowsIsNull: asserts
// the boolean via SQL rather than a nil-pointer scan.
func failureKindIsNull(t *testing.T, super *pgxpool.Pool, invoiceID string) bool {
	t.Helper()
	var isNull bool
	if err := super.QueryRow(context.Background(),
		`SELECT failure_kind IS NULL FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&isNull); err != nil {
		t.Fatalf("read invoices.failure_kind IS NULL: %v", err)
	}
	return isNull
}

// TestFailureKind_DefaultsToNull (AC-1): a freshly created invoice carries
// no failure_kind until something sets one.
func TestFailureKind_DefaultsToNull(t *testing.T) {
	super, _ := dbTestPools(t)

	tenantID := seedTenant(t, super, "BUG-06-01 defaults-null tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-01 defaults-null entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "BUG-06-01-DEFAULT")

	if !failureKindIsNull(t, super, invoiceID) {
		t.Error("failure_kind IS NULL = false, want true for a freshly created invoice")
	}
	if got := failureKindOf(t, super, invoiceID); got != nil {
		t.Errorf("failure_kind = %q, want nil", *got)
	}
}

// TestFailureKind_AcceptsTheThreeKinds (AC-2): each of the three defined
// kinds is a legal value.
func TestFailureKind_AcceptsTheThreeKinds(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-06-01 accepts-three tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-01 accepts-three entity")

	for _, kind := range []string{"payload_not_built", "never_acknowledged", "acknowledged_no_verdict"} {
		t.Run(kind, func(t *testing.T) {
			invoiceID := seedInvoice(t, super, tenantID, entityID, "BUG-06-01-KIND-"+kind)
			if _, err := super.Exec(ctx,
				`UPDATE invoices SET failure_kind = $1 WHERE id = $2`, kind, invoiceID,
			); err != nil {
				t.Fatalf("UPDATE failure_kind = %q: %v", kind, err)
			}
			got := failureKindOf(t, super, invoiceID)
			if got == nil || *got != kind {
				t.Errorf("failure_kind = %v, want %q", got, kind)
			}
		})
	}
}

// TestFailureKind_RefusesUnknownValue (AC-2): anything outside the three
// defined kinds -- including the removed "APP-side rejection" idea -- must
// hit the CHECK, not silently persist.
func TestFailureKind_RefusesUnknownValue(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-06-01 refuses-unknown tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-01 refuses-unknown entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "BUG-06-01-UNKNOWN")

	_, err := super.Exec(ctx,
		`UPDATE invoices SET failure_kind = $1 WHERE id = $2`, "app_rejected", invoiceID,
	)
	if pgCode(err) != "23514" {
		t.Fatalf("UPDATE failure_kind = 'app_rejected' err = %v (code %q), want 23514", err, pgCode(err))
	}
	if !failureKindIsNull(t, super, invoiceID) {
		t.Error("failure_kind IS NULL = false after a refused UPDATE, want true (row unchanged)")
	}
}

// TestFailureKind_RefusesEmptyString (AC-2): a blank must not become a
// second encoding of "unknown".
func TestFailureKind_RefusesEmptyString(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-06-01 refuses-empty tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-01 refuses-empty entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "BUG-06-01-EMPTY")

	_, err := super.Exec(ctx,
		`UPDATE invoices SET failure_kind = $1 WHERE id = $2`, "", invoiceID,
	)
	if pgCode(err) != "23514" {
		t.Fatalf("UPDATE failure_kind = '' err = %v (code %q), want 23514", err, pgCode(err))
	}
	if !failureKindIsNull(t, super, invoiceID) {
		t.Error("failure_kind IS NULL = false after a refused UPDATE, want true (row unchanged)")
	}
}

// TestFailureKind_LegacyFailedRowIsReadable (AC-7): a pre-migration/legacy
// failed invoice carries no kind and must remain readable -- no
// status-correlating CHECK exists to refuse it.
func TestFailureKind_LegacyFailedRowIsReadable(t *testing.T) {
	super, _ := dbTestPools(t)

	tenantID := seedTenant(t, super, "BUG-06-01 legacy-failed tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-01 legacy-failed entity")
	invoiceID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-01-LEGACY-FAILED", StatusFailed)

	if !failureKindIsNull(t, super, invoiceID) {
		t.Error("failure_kind IS NULL = false, want true for a legacy failed row")
	}
}

// TestFailureKind_RefusesCaseVariant (AC-2, adversarial): the CHECK is a
// literal string match -- an uppercase variant of a legal kind is a
// different value and must be refused, not silently accepted.
func TestFailureKind_RefusesCaseVariant(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-06-01 refuses-case tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-01 refuses-case entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "BUG-06-01-CASE")

	_, err := super.Exec(ctx,
		`UPDATE invoices SET failure_kind = $1 WHERE id = $2`, "PAYLOAD_NOT_BUILT", invoiceID,
	)
	if pgCode(err) != "23514" {
		t.Fatalf("UPDATE failure_kind = 'PAYLOAD_NOT_BUILT' err = %v (code %q), want 23514", err, pgCode(err))
	}
	if !failureKindIsNull(t, super, invoiceID) {
		t.Error("failure_kind IS NULL = false after a refused UPDATE, want true (row unchanged)")
	}
}

// TestFailureKind_RefusesWhitespacePadded (AC-2, adversarial): the CHECK is
// an exact IN-list match -- surrounding whitespace is not trimmed, so a
// padded variant of a legal kind is still refused.
func TestFailureKind_RefusesWhitespacePadded(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-06-01 refuses-padded tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-01 refuses-padded entity")

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"leading_space", " payload_not_built"},
		{"trailing_space", "payload_not_built "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoiceID := seedInvoice(t, super, tenantID, entityID, "BUG-06-01-PAD-"+tc.name)
			_, err := super.Exec(ctx,
				`UPDATE invoices SET failure_kind = $1 WHERE id = $2`, tc.value, invoiceID,
			)
			if pgCode(err) != "23514" {
				t.Fatalf("UPDATE failure_kind = %q err = %v (code %q), want 23514", tc.value, err, pgCode(err))
			}
			if !failureKindIsNull(t, super, invoiceID) {
				t.Error("failure_kind IS NULL = false after a refused UPDATE, want true (row unchanged)")
			}
		})
	}
}

// TestFailureKind_CanBeClearedBackToNull (AC-3, adversarial): a kind already
// set is not sticky -- clearing it back to NULL must not trip the CHECK
// (IS NULL is the constraint's own escape clause).
func TestFailureKind_CanBeClearedBackToNull(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-06-01 clear-null tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-01 clear-null entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "BUG-06-01-CLEAR")

	if _, err := super.Exec(ctx,
		`UPDATE invoices SET failure_kind = 'never_acknowledged' WHERE id = $1`, invoiceID,
	); err != nil {
		t.Fatalf("seed failure_kind = 'never_acknowledged': %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET failure_kind = NULL WHERE id = $1`, invoiceID,
	); err != nil {
		t.Fatalf("UPDATE failure_kind = NULL: %v", err)
	}
	if !failureKindIsNull(t, super, invoiceID) {
		t.Error("failure_kind IS NULL = false after clearing, want true")
	}
}

// TestFailureKind_TenantScoped (AC-1/AC-2, adversarial): the migration
// added no new GRANT or policy, on the claim that the table-level GRANT and
// the row-scoped tenant_isolation policy are column-agnostic. Proves that
// empirically under the invoice_app role: own-tenant read/write succeeds,
// the CHECK still fires under invoice_app (not just superuser), and a
// cross-tenant row stays invisible to both SELECT and UPDATE.
func TestFailureKind_TenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "BUG-06-01 tenant-scope A")
	tenantB := seedTenant(t, super, "BUG-06-01 tenant-scope B")
	entityA := seedEntity(t, super, tenantA, "BUG-06-01 tenant-scope A Corp")
	invoiceA := seedInvoice(t, super, tenantA, entityA, "BUG-06-01-SCOPE-A")

	// Own tenant: invoice_app can read and write failure_kind on its own row.
	err := db.WithinTenantTx(ctx, app, tenantA, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE invoices SET failure_kind = 'never_acknowledged' WHERE id = $1`, invoiceA); e != nil {
			return e
		}
		var got *string
		if e := tx.QueryRow(ctx, `SELECT failure_kind FROM invoices WHERE id = $1`, invoiceA).Scan(&got); e != nil {
			return e
		}
		if got == nil || *got != "never_acknowledged" {
			t.Errorf("failure_kind under own tenant = %v, want %q", got, "never_acknowledged")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("own-tenant read/write: %v", err)
	}

	// Own tenant: the CHECK still fires under invoice_app, not just superuser.
	err = db.WithinTenantTx(ctx, app, tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE invoices SET failure_kind = 'app_rejected' WHERE id = $1`, invoiceA)
		return e
	})
	if pgCode(err) != "23514" {
		t.Fatalf("invoice_app UPDATE failure_kind = 'app_rejected' err = %v (code %q), want 23514", err, pgCode(err))
	}

	// Cross tenant: under tenant B's GUC, invoice A is invisible -- SELECT
	// returns zero rows and an UPDATE affects zero rows (no error, no leak).
	err = db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE id = $1`, invoiceA).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Errorf("invoice A visible under tenant B's GUC: count = %d, want 0", n)
		}
		tag, e := tx.Exec(ctx, `UPDATE invoices SET failure_kind = 'payload_not_built' WHERE id = $1`, invoiceA)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 0 {
			t.Errorf("cross-tenant UPDATE affected %d rows, want 0", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cross-tenant read/write: %v", err)
	}
}
