// failure_kind_test.go: invoices.failure_kind, the reason a submission
// landed in status='failed'. Written RED, before the column exists -- see
// the BUG-06-01 story / task-383 Test Specs table.
package invoice

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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

// --- QA gap-fill (task-384 verification): adversarial coverage the RED specs
// didn't cover -- a Go-legal-but-undeclared kind, concurrent MarkFailedTx on
// the same row, and a drift guard tying Valid()/the three constants to the
// DB CHECK's actual IN-list.

// TestMarkFailedTx_UnknownKindValidGoRefusedByDB (AC-6, adversarial):
// FailureKind is a bare string type -- Go's type system does not restrict it
// to the three declared constants. A constructible-but-undeclared value must
// still be refused by the DB CHECK (23514): Valid() and the type system are
// advisory, the CHECK is the real boundary.
func TestMarkFailedTx_UnknownKindValidGoRefusedByDB(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "BUG-06-02 unknown-kind tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-02 unknown-kind entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-02-UNKNOWN", StatusQueued)

	unknown := submission.FailureKind("app_rejected") // compiles fine; never a declared constant
	if unknown.Valid() {
		t.Fatalf("submission.FailureKind(%q).Valid() = true, want false (test premise broken)", unknown)
	}

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID, unknown)
		return err
	})
	if code := pgCode(err); code != "23514" {
		t.Fatalf("MarkFailedTx(%q): pgCode = %q, want 23514: %v", unknown, code, err)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusQueued {
		t.Errorf("status after refused unknown-kind write = %q, want unchanged %q", status, StatusQueued)
	}
	if !failureKindIsNull(t, super, invID) {
		t.Error("failure_kind IS NULL = false after refused write, want true")
	}
}

// TestMarkFailedTx_ConcurrentCallsOnSameInvoiceIdempotentlyConverge
// (adversarial, Core AC under real concurrency, not just sequential replay):
// N goroutines call MarkFailedTx on the SAME queued invoice at once.
// markTerminalTx's leading `SELECT ... FOR UPDATE` serializes them: exactly
// one goroutine finds the row still queued and runs the outcome write +
// transitionTx; every other goroutine's SELECT blocks until the winner
// commits, then observes status already 'failed' and takes the idempotent
// short-circuit -- no error, no second history row, no torn write.
func TestMarkFailedTx_ConcurrentCallsOnSameInvoiceIdempotentlyConverge(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "BUG-06-02 concurrent tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-06-02 concurrent entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "BUG-06-02-CONCURRENT", StatusQueued)

	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
				_, err := store.MarkFailedTx(ctx, tx, invID, tenantID, submission.FailureNeverAcknowledged)
				return err
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: MarkFailedTx err = %v, want nil", i, err)
		}
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusFailed {
		t.Errorf("status = %q, want %q", status, StatusFailed)
	}
	got := failureKindOf(t, super, invID)
	if got == nil || *got != string(submission.FailureNeverAcknowledged) {
		t.Errorf("failure_kind = %v, want %q", got, submission.FailureNeverAcknowledged)
	}
	if hn := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); hn != 1 {
		t.Errorf("invoice_status_history rows = %d, want exactly 1 (only the winner transitions)", hn)
	}
}

// TestFailureKindCheck_MatchesGoConstantsExactly (adversarial, drift guard):
// introspects invoices_failure_kind_check's actual IN-list from Postgres
// (pg_get_constraintdef) and compares it set-wise against the three
// submission.FailureKind constants. Catches a future engineer adding a Go
// constant with no matching migration, or a migration with no matching
// constant -- a mismatch either way is silent until this test runs it down.
func TestFailureKindCheck_MatchesGoConstantsExactly(t *testing.T) {
	super, _ := dbTestPools(t)

	var def string
	if err := super.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		 WHERE conrelid = 'invoices'::regclass AND contype = 'c' AND conname = 'invoices_failure_kind_check'`,
	).Scan(&def); err != nil {
		t.Fatalf("read invoices_failure_kind_check definition: %v", err)
	}

	dbValues := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([^']*)'::text`).FindAllStringSubmatch(def, -1) {
		dbValues[m[1]] = true
	}

	goValues := map[string]bool{
		string(submission.FailurePayloadNotBuilt):       true,
		string(submission.FailureNeverAcknowledged):     true,
		string(submission.FailureAcknowledgedNoVerdict): true,
	}

	if len(dbValues) != len(goValues) {
		t.Fatalf("CHECK IN-list has %d values %v, Go constants have %d %v -- drift", len(dbValues), dbValues, len(goValues), goValues)
	}
	for v := range goValues {
		if !dbValues[v] {
			t.Errorf("Go constant %q has no matching value in the DB CHECK IN-list %v", v, dbValues)
		}
	}
	for v := range dbValues {
		if !goValues[v] {
			t.Errorf("DB CHECK IN-list value %q has no matching Go constant", v)
		}
	}
}

// TestMarkFailedTx_CrossTenantIDReturnsErrNotFoundAndLeavesKindUntouched
// (adversarial): the row-invisible cross-tenant case MarkAcceptedTx
// (TestRLS_MarkAcceptedTxCrossTenantIsNotFound) and MarkSubmittedTx
// (TestMarkSubmittedTx_CrossTenantIDReturnsErrNotFound) both already cover,
// but MarkFailedTx itself did not: tx GUC AND actor are tenant B end to end,
// the invoice belongs to tenant A -- RLS 0-rows the initial FOR UPDATE read,
// so this is ErrNotFound, not 42501, and failure_kind stays NULL.
func TestMarkFailedTx_CrossTenantIDReturnsErrNotFoundAndLeavesKindUntouched(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantA := seedTenant(t, super, "BUG-06-02 cross-tenant tenant A")
	tenantB := seedTenant(t, super, "BUG-06-02 cross-tenant tenant B")
	entityA := seedEntity(t, super, tenantA, "BUG-06-02 cross-tenant entity")
	invID := seedInvoiceAtStatus(t, super, tenantA, entityA, "BUG-06-02-XTENANT", StatusQueued)

	err := db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantB, submission.FailureNeverAcknowledged)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkFailedTx (tenant B tx, tenant A's invoice): err = %v, want ErrNotFound", err)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusQueued {
		t.Errorf("status after cross-tenant-invisible MarkFailedTx = %q, want unchanged %q", status, StatusQueued)
	}
	if !failureKindIsNull(t, super, invID) {
		t.Error("failure_kind IS NULL = false after cross-tenant-invisible MarkFailedTx, want true")
	}
}

// --- BUG-06-04 (task-386) -- Mode A RED spec for the wire: the one test in
// this subtask that exercises the REAL invoiceColumns/scanInvoice SQL
// projection rather than a hand-built Invoice{} literal through an injected
// fake (see handlers_test.go for the marshal-shape tests). Written before
// Invoice.FailureKind exists -- a compile error until Stage 3 lands it.

// TestFailureKind_ProjectionRoundTripsThroughStoreGet (AC-3): guards
// invoiceColumns/scanInvoice's positional scan for the newly-appended
// failure_kind column, mirroring TestKeptAsIs_ProjectionRoundTrips
// (kept_as_is_test.go) exactly for the same reason -- a swap between two
// adjacent *string columns silently scans one's value into the other's
// field.
//
// failure_kind, kept_as_is_by and kept_as_is_reason are three ADJACENT
// *string columns at the tail of both lists, so all three get distinct,
// unmistakable values here: a positional reorder of any pair (e.g.
// invoiceColumns appending failure_kind correctly but scanInvoice's
// Scan(...) targets landing in the wrong order) puts a value in the wrong
// field and fails that field's own equality check, even though every column
// still scans into SOME *string field without error.
//
// Seeded at StatusDraft, not StatusFailed: invoices_kept_as_is_draft_only
// (20260731100000_invoices_kept_as_is.sql) requires kept_as_is_at IS NULL
// whenever status != 'draft', and invoices_kept_as_is_complete requires
// kept_as_is_at/by/reason all-null or all-non-null together -- so
// kept_as_is_by/reason can only be non-null (needed to give them their own
// distinct values) on a draft row. failure_kind carries no
// status-correlating CHECK (20260805075045_invoices_failure_kind.sql), so
// this is schema-legal.
func TestFailureKind_ProjectionRoundTripsThroughStoreGet(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "FK-PROJ tenant")
	entityID := seedEntity(t, super, tenantID, "FK-PROJ entity")
	invID := seedInvoice(t, super, tenantID, entityID, "FK-PROJ-001")

	const (
		wantFailureKind    = "acknowledged_no_verdict"
		wantKeptAsIsBy     = "FK-SWAP-actor-marker"
		wantKeptAsIsReason = "FK-SWAP-reason-marker: buyer confirmed intentional"
	)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = $1, kept_as_is_reason = $2, failure_kind = $3 WHERE id = $4`,
		wantKeptAsIsBy, wantKeptAsIsReason, wantFailureKind, invID,
	); err != nil {
		t.Fatalf("force-seed kept_as_is_*/failure_kind: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	got, err := store.Get(c, invID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.FailureKind == nil || *got.FailureKind != wantFailureKind {
		t.Errorf("Get.FailureKind = %v, want a pointer to %q", got.FailureKind, wantFailureKind)
	}
	if got.KeptAsIsBy == nil || *got.KeptAsIsBy != wantKeptAsIsBy {
		t.Errorf("Get.KeptAsIsBy = %v, want a pointer to %q", got.KeptAsIsBy, wantKeptAsIsBy)
	}
	if got.KeptAsIsReason == nil || *got.KeptAsIsReason != wantKeptAsIsReason {
		t.Errorf("Get.KeptAsIsReason = %v, want a pointer to %q", got.KeptAsIsReason, wantKeptAsIsReason)
	}
}
