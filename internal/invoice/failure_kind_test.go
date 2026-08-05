// failure_kind_test.go: invoices.failure_kind, the reason a submission
// landed in status='failed'. Written RED, before the column exists -- see
// the BUG-06-01 story / task-383 Test Specs table.
package invoice

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
