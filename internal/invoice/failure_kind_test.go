// failure_kind_test.go: invoices.failure_kind, the reason a submission
// landed in status='failed'. Written RED, before the column exists -- see
// the BUG-06-01 story / task-383 Test Specs table.
package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
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

// --- BUG-06-02 (task-384) -- Mode A RED specs for the submission->invoice
// seam: MarkFailedTx/Store.MarkFailed gain a trailing submission.FailureKind
// arg, written in the SAME tx as the status write via markTerminalTx's
// outcome callback. Written BEFORE submission.FailureKind or the widened
// signatures exist -- every reference to submission.FailureKind* below and
// every 5-arg MarkFailedTx call is a compile error until the executor lands
// it (Stage 3), not a runtime assertion failure. See task-384's Test Specs
// table for the AC mapping.

// TestMarkFailedTx_StampsKindInSameTx (AC-1): a queued invoice ->
// MarkFailedTx(..., FailurePayloadNotBuilt) inside one WithinTenantTx ->
// after commit, status='failed' AND failure_kind='payload_not_built'.
func TestMarkFailedTx_StampsKindInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "BUG-06-02 stamps-kind tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-02 stamps-kind entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-02-STAMP", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID, submission.FailurePayloadNotBuilt)
		return err
	})
	if err != nil {
		t.Fatalf("MarkFailedTx(queued->failed, FailurePayloadNotBuilt): %v, want nil", err)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusFailed {
		t.Errorf("invoice status = %q, want %q", status, StatusFailed)
	}
	got := failureKindOf(t, super, invID)
	if got == nil || *got != string(submission.FailurePayloadNotBuilt) {
		t.Errorf("failure_kind = %v, want %q", got, submission.FailurePayloadNotBuilt)
	}
}

// TestMarkFailedTx_KindRollsBackWithAnIllegalTransition (AC-1, the
// Constraint's real content): a draft invoice has no legal edge to failed ->
// MarkFailedTx(..., FailureNeverAcknowledged) -> ErrIllegalTransition, and
// the kind write rolls back with it -- failure_kind is still NULL, because
// the outcome callback and transitionTx share the same tx (markTerminalTx).
func TestMarkFailedTx_KindRollsBackWithAnIllegalTransition(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "BUG-06-02 rollback-illegal tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-02 rollback-illegal entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-02-ILLEGAL", StatusDraft)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID, submission.FailureNeverAcknowledged)
		return err
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("MarkFailedTx(draft->failed): err = %v, want ErrIllegalTransition", err)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("status after illegal MarkFailedTx = %q, want unchanged %q", status, StatusDraft)
	}
	if !failureKindIsNull(t, super, invID) {
		t.Error("failure_kind IS NULL = false after the aborted illegal transition, want true (kind write must roll back together with the refused transition)")
	}
}

// TestMarkFailedTx_ReplayDoesNotRewriteStoredKind (AC-1): an invoice already
// failed with failure_kind='never_acknowledged' -> MarkFailedTx(...,
// FailureAcknowledgedNoVerdict) -> returns nil (idempotent no-op), the
// stored kind is untouched, and no new invoice_status_history row lands --
// the outcome callback only runs AFTER markTerminalTx's idempotent
// short-circuit.
func TestMarkFailedTx_ReplayDoesNotRewriteStoredKind(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "BUG-06-02 replay tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-02 replay entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-02-REPLAY", StatusFailed)
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET failure_kind = $1 WHERE id = $2`, string(submission.FailureNeverAcknowledged), invID,
	); err != nil {
		t.Fatalf("seed failure_kind: %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID, submission.FailureAcknowledgedNoVerdict)
		return err
	})
	if err != nil {
		t.Fatalf("MarkFailedTx (already failed, idempotent replay): %v, want nil", err)
	}

	got := failureKindOf(t, super, invID)
	if got == nil || *got != string(submission.FailureNeverAcknowledged) {
		t.Errorf("failure_kind after replay = %v, want unchanged %q (idempotent no-op must not rewrite the stored kind)", got, submission.FailureNeverAcknowledged)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("history rows after idempotent replay = %d, want unchanged %d", n, beforeHistory)
	}
}

// TestMarkFailedTx_EveryKindConstantRoundTrips (AC-2): each of the three
// FailureKind constants round-trips through a fresh queued invoice, guarding
// against Go-const <-> DB-CHECK drift.
func TestMarkFailedTx_EveryKindConstantRoundTrips(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "BUG-06-02 roundtrip tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-02 roundtrip entity")

	for _, kind := range []submission.FailureKind{
		submission.FailurePayloadNotBuilt,
		submission.FailureNeverAcknowledged,
		submission.FailureAcknowledgedNoVerdict,
	} {
		t.Run(string(kind), func(t *testing.T) {
			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-02-RT-"+string(kind), StatusQueued)

			err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
				_, err := store.MarkFailedTx(ctx, tx, invID, tenantID, kind)
				return err
			})
			if err != nil {
				t.Fatalf("MarkFailedTx(queued->failed, %q): %v, want nil", kind, err)
			}

			got := failureKindOf(t, super, invID)
			if got == nil || *got != string(kind) {
				t.Errorf("failure_kind = %v, want %q", got, kind)
			}
		})
	}
}

// TestMarkFailedTx_BlankKindRefused (AC-6, [no second encoding of
// "unknown"]): the kind binds RAW, no NULLIF -- a blank kind must hit the
// invoices.failure_kind CHECK (23514), not silently land as SQL NULL.
func TestMarkFailedTx_BlankKindRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "BUG-06-02 blank-kind tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-02 blank-kind entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-02-BLANK", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID, submission.FailureKind(""))
		return err
	})
	if err == nil {
		t.Fatal("MarkFailedTx with a blank kind succeeded, want a CHECK violation (SQLSTATE 23514 -- bound raw, no NULLIF)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("MarkFailedTx with a blank kind: pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusQueued {
		t.Errorf("invoice status after refused blank-kind write = %q, want unchanged %q", status, StatusQueued)
	}
	if !failureKindIsNull(t, super, invID) {
		t.Error("failure_kind IS NULL = false after the refused write, want true (commits nothing)")
	}
}

// TestStoreMarkFailed_ForwardsKindThroughThePort (AC-1): *Store used as
// submission.InvoicePort -- MarkFailed(..., FailureAcknowledgedNoVerdict) ->
// stored failure_kind='acknowledged_no_verdict', proving the port forward
// (submission_port.go) carries the kind through, not just MarkFailedTx
// directly.
func TestStoreMarkFailed_ForwardsKindThroughThePort(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-06-02 port-forward tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-02 port-forward entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-02-PORT", StatusQueued)

	var port submission.InvoicePort = NewStore(app)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		return port.MarkFailed(ctx, tx, invID, tenantID, submission.FailureAcknowledgedNoVerdict)
	})
	if err != nil {
		t.Fatalf("port.MarkFailed(invID, tenantID, FailureAcknowledgedNoVerdict): %v, want nil", err)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusFailed {
		t.Errorf("invoice status after port.MarkFailed = %q, want %q", status, StatusFailed)
	}
	got := failureKindOf(t, super, invID)
	if got == nil || *got != string(submission.FailureAcknowledgedNoVerdict) {
		t.Errorf("failure_kind = %v, want %q", got, submission.FailureAcknowledgedNoVerdict)
	}
}
