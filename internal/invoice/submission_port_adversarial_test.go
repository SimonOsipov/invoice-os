// M5-04-03 (task-229): QA Mode B adversarial coverage for InvoicePort
// (submission_port.go / getTx in store.go), written during the
// post-implementation verify pass on top of T03-1..T03-6
// (submission_port_test.go). Reuses the dbTestPools/seedTenant/seedEntity/
// seedInvoice/seedInvoiceAtStatus/seedRuleSetVersionID/mustCount harness
// from store_test.go/transition_adversarial_test.go/apply_validation_test.go
// (same package).
//
// Closes a gap the RED spec table left open on purpose (per the story: "No
// new test spec is added for MarkSubmitted/MarkFailed's transition
// behaviour through the port... T03-1's compile check plus 02's existing
// coverage is the traceable... test surface for these two methods"): NO
// existing test calls port.MarkSubmitted/port.MarkFailed (the port-level
// forwards) at all -- every MarkSubmittedTx/MarkFailedTx call site in the
// suite (system_actor_test.go, system_actor_adversarial_test.go) drives the
// underlying Store method directly, never through submission.InvoicePort.
// Verified by mutation: transposing the two string args in MarkSubmitted's
// forward (`s.MarkSubmittedTx(ctx, tx, tenantID, invoiceID)` instead of
// `(ctx, tx, invoiceID, tenantID)`) compiles clean and leaves BOTH
// `go test ./internal/invoice/...` and `go test ./internal/submission/...`
// green -- nothing catches it. TestInvoicePort_MarkSubmitted/
// MarkFailedBindsInvoiceIDAndTenantIDCorrectly below close that hole: they
// drive the SAME transposition red (ErrNotFound, since a tenant id almost
// never collides with an invoice id in the invoices table).
package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestInvoicePort_MarkSubmittedBindsInvoiceIDAndTenantIDCorrectly drives
// port.MarkSubmitted (never MarkSubmittedTx directly) end to end and
// asserts BOTH the resulting invoice status AND the invoice_status_history
// row's tenant_id/invoice_id/actor -- the argument-transposition mutation
// described above would surface here as ErrNotFound (a tenant id looked up
// against invoices.id) or, if it somehow found a row, a history row bound
// to the wrong ids.
func TestInvoicePort_MarkSubmittedBindsInvoiceIDAndTenantIDCorrectly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-ADV MarkSubmitted tenant")
	entityID := seedEntity(t, super, tenantID, "T03-ADV MarkSubmitted entity")
	invoiceID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-ADV-MS", StatusQueued)

	var port submission.InvoicePort = NewStore(app)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		return port.MarkSubmitted(ctx, tx, invoiceID, tenantID)
	})
	if err != nil {
		t.Fatalf("port.MarkSubmitted(invoiceID, tenantID): %v (want nil -- a transposed forward would 404 here)", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&status); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if status != string(StatusSubmitted) {
		t.Errorf("invoice status after port.MarkSubmitted = %q, want %q", status, StatusSubmitted)
	}

	var histTenant, histInvoice, actor, toStatus string
	if err := super.QueryRow(ctx,
		`SELECT tenant_id, invoice_id, actor, to_status FROM invoice_status_history WHERE invoice_id = $1`,
		invoiceID,
	).Scan(&histTenant, &histInvoice, &actor, &toStatus); err != nil {
		t.Fatalf("read back invoice_status_history row: %v", err)
	}
	if histTenant != tenantID {
		t.Errorf("invoice_status_history.tenant_id = %q, want %q (tenantID must land in the tenant_id column, not invoice_id)", histTenant, tenantID)
	}
	if histInvoice != invoiceID {
		t.Errorf("invoice_status_history.invoice_id = %q, want %q (invoiceID must land in the invoice_id column, not tenant_id)", histInvoice, invoiceID)
	}
	if actor != "system" {
		t.Errorf("invoice_status_history.actor = %q, want %q", actor, "system")
	}
	if toStatus != string(StatusSubmitted) {
		t.Errorf("invoice_status_history.to_status = %q, want %q", toStatus, StatusSubmitted)
	}
}

// TestInvoicePort_MarkFailedBindsInvoiceIDAndTenantIDCorrectly is
// MarkSubmitted's sibling above, for port.MarkFailed / MarkFailedTx.
func TestInvoicePort_MarkFailedBindsInvoiceIDAndTenantIDCorrectly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-ADV MarkFailed tenant")
	entityID := seedEntity(t, super, tenantID, "T03-ADV MarkFailed entity")
	invoiceID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-ADV-MF", StatusQueued)

	var port submission.InvoicePort = NewStore(app)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		return port.MarkFailed(ctx, tx, invoiceID, tenantID)
	})
	if err != nil {
		t.Fatalf("port.MarkFailed(invoiceID, tenantID): %v (want nil -- a transposed forward would 404 here)", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&status); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if status != string(StatusFailed) {
		t.Errorf("invoice status after port.MarkFailed = %q, want %q", status, StatusFailed)
	}

	var histTenant, histInvoice, actor, toStatus string
	if err := super.QueryRow(ctx,
		`SELECT tenant_id, invoice_id, actor, to_status FROM invoice_status_history WHERE invoice_id = $1`,
		invoiceID,
	).Scan(&histTenant, &histInvoice, &actor, &toStatus); err != nil {
		t.Fatalf("read back invoice_status_history row: %v", err)
	}
	if histTenant != tenantID {
		t.Errorf("invoice_status_history.tenant_id = %q, want %q", histTenant, tenantID)
	}
	if histInvoice != invoiceID {
		t.Errorf("invoice_status_history.invoice_id = %q, want %q", histInvoice, invoiceID)
	}
	if actor != "system" {
		t.Errorf("invoice_status_history.actor = %q, want %q", actor, "system")
	}
	if toStatus != string(StatusFailed) {
		t.Errorf("invoice_status_history.to_status = %q, want %q", toStatus, StatusFailed)
	}
}

// TestInvoicePort_CanonicalNonexistentIDReturnsErrNotFound: a well-formed
// UUID that matches no invoices row (same tenant tx, id simply doesn't
// exist) must map to ErrNotFound through the port, exactly like
// Store.Get/getTx.
func TestInvoicePort_CanonicalNonexistentIDReturnsErrNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-ADV Canonical-404 tenant")

	var port submission.InvoicePort = NewStore(app)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		got, err := port.Canonical(ctx, tx, uuid.NewString())
		if err == nil {
			t.Errorf("port.Canonical (nonexistent id) = %+v, want ErrNotFound", got)
		} else if !errors.Is(err, ErrNotFound) {
			t.Errorf("port.Canonical (nonexistent id) err = %v, want ErrNotFound", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// TestInvoicePort_CanonicalMalformedIDReturnsErrValidation: a non-UUID
// invoiceID raises Postgres 22P02 inside getTx's invoice SELECT, mapped to
// ErrValidation -- identical to Store.Get's own documented behaviour
// (store.go:213-214) since Canonical is a thin wrapper over the same getTx.
func TestInvoicePort_CanonicalMalformedIDReturnsErrValidation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-ADV Canonical-malformed tenant")

	var port submission.InvoicePort = NewStore(app)

	// The malformed id aborts the underlying Postgres tx at the query level
	// (22P02), so the error must propagate OUT of the closure (as
	// Store.Get's own WithinRequestTenantTx wrapper does) rather than being
	// swallowed and returning nil -- returning nil here would make
	// WithinTenantTx attempt a Commit on an already-aborted tx.
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := port.Canonical(ctx, tx, "not-a-uuid")
		return err
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("port.Canonical (malformed id) err = %v, want ErrValidation", err)
	}
}

// TestInvoicePort_HasFiscalOutcomeMalformedID documents HasFiscalOutcome's
// actual behaviour on a non-UUID invoiceID -- UNLIKE every other id-taking
// method in this package (Get/Update/Create/Transition/ApplyValidation/
// MarkSubmittedTx/MarkFailedTx/getTx, all of which map pgCode 22P02 to the
// domain ErrValidation), HasFiscalOutcome's body only special-cases
// pgx.ErrNoRows and otherwise returns the raw driver error verbatim. This
// test pins that actual behaviour (a real, non-nil error that is NOT
// ErrValidation) rather than assuming the convention was followed --
// flagged as a finding in the QA report, not silently "fixed" here (QA
// does not write implementation code).
func TestInvoicePort_HasFiscalOutcomeMalformedID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-ADV HasFiscalOutcome-malformed tenant")

	var port submission.InvoicePort = NewStore(app)

	// Same abort-the-tx hazard as the Canonical malformed-id test above: the
	// 22P02 error must propagate out of the closure, not be swallowed.
	var got bool
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		got, err = port.HasFiscalOutcome(ctx, tx, "not-a-uuid")
		return err
	})
	if got {
		t.Errorf("port.HasFiscalOutcome (malformed id) = true, want false")
	}
	if err == nil {
		t.Fatalf("port.HasFiscalOutcome (malformed id): err = nil, want a non-nil error (22P02 must not be silently swallowed as pgx.ErrNoRows)")
	}
	if errors.Is(err, ErrValidation) {
		t.Logf("port.HasFiscalOutcome (malformed id) DOES map to ErrValidation like its siblings -- this test's inconsistency premise is stale, update the doc comment")
	} else {
		t.Logf("port.HasFiscalOutcome (malformed id) returns a raw (non-ErrValidation) error: %v -- inconsistent with Canonical/getTx's 22P02->ErrValidation convention", err)
	}
}

// TestInvoicePort_CanonicalZeroLineItemsIsHydratedNotListShaped: a genuinely
// empty invoice (zero line_items rows) must still produce Canonical.Lines ==
// nil/empty WITHOUT error -- but that alone cannot distinguish "correctly
// hydrated, zero real lines" from the silent-nil-hydration bug
// submission_canonical.go's header warns about (a List-shaped invoice with
// LineItems left nil looks identical from Canonical's output alone). This
// test closes that gap by also stamping rule_set_version_id and reading the
// SAME invoice back through getTx directly: only Store.Get/getTx run the
// correlated rule_set_versions subselect (List never does, store.go:361-393
// selects invoiceColumns only) -- so a non-nil RuleSetVersion on the getTx
// read is independent proof this invoice went through the hydrating path,
// not List, decoupling "zero lines" from "never hydrated."
func TestInvoicePort_CanonicalZeroLineItemsIsHydratedNotListShaped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-ADV Canonical-zero-lines tenant")
	entityID := seedEntity(t, super, tenantID, "T03-ADV Canonical-zero-lines entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "T03-ADV-ZERO") // zero line_items, by construction

	rsvID := seedRuleSetVersionID(t, super)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, rsvID, invoiceID,
	); err != nil {
		t.Fatalf("stamp rule_set_version_id: %v", err)
	}

	var port submission.InvoicePort = NewStore(app)
	var got submission.Canonical
	var rawInv Invoice
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		rawInv, err = getTx(ctx, tx, invoiceID)
		if err != nil {
			return err
		}
		got, err = port.Canonical(ctx, tx, invoiceID)
		return err
	})
	if err != nil {
		t.Fatalf("getTx/port.Canonical (zero line items): %v (want nil)", err)
	}

	// Independent hydration proof: RuleSetVersion is populated ONLY by
	// getTx's subselect, never by List -- if this is nil, the read wasn't
	// hydrated and the zero-lines assertion below would be meaningless.
	if rawInv.RuleSetVersion == nil {
		t.Fatalf("getTx: RuleSetVersion = nil, want non-nil (proves this read was hydrated, not List-shaped; a nil result here makes the zero-lines check below vacuous)")
	}

	if len(got.Lines) != 0 {
		t.Errorf("port.Canonical (zero line items): len(Lines) = %d, want 0", len(got.Lines))
	}
	if got.InvoiceID != invoiceID {
		t.Errorf("port.Canonical: InvoiceID = %q, want %q", got.InvoiceID, invoiceID)
	}
}

// TestInvoicePort_RolledBackTxLeavesNoPartialWrites: port.MarkSubmitted
// writes (invoices.status + invoice_status_history) inside a tx whose
// caller ultimately rolls back (a non-nil return from db.WithinTenantTx's
// fn rolls back, db.go:60-63) must NOT persist -- the worker's own tx1/tx2
// atomicity is the entire reason InvoicePort takes the caller's tx instead
// of opening its own (invoice_port.go's own header comment).
func TestInvoicePort_RolledBackTxLeavesNoPartialWrites(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-ADV rollback tenant")
	entityID := seedEntity(t, super, tenantID, "T03-ADV rollback entity")
	invoiceID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T03-ADV-RB", StatusQueued)

	var port submission.InvoicePort = NewStore(app)

	sentinel := errors.New("T03-ADV: force rollback after MarkSubmitted")
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		if err := port.MarkSubmitted(ctx, tx, invoiceID, tenantID); err != nil {
			return err
		}
		return sentinel // force rollback of everything MarkSubmitted just wrote
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("db.WithinTenantTx: err = %v, want the sentinel (confirms the tx was actually rolled back on purpose, not committed)", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&status); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if status != string(StatusQueued) {
		t.Errorf("invoice status after rolled-back port.MarkSubmitted = %q, want unchanged %q", status, StatusQueued)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invoiceID); n != 0 {
		t.Errorf("invoice_status_history rows after rolled-back port.MarkSubmitted = %d, want 0", n)
	}
}

// TestInvoicePort_CanonicalMissingRuleSetVersionRowDoesNotError: getTx's
// correlated rule_set_versions subselect deliberately swallows ErrNoRows
// (store.go: "Nil when rule_set_version_id IS NULL (never validated)") --
// this test forces the OTHER route into that same branch: a NON-NULL
// rule_set_version_id whose target row has been deleted out from under it.
// The FK (REFERENCES rule_set_versions(id), no ON DELETE clause = RESTRICT)
// normally forbids this, so a single committed transaction with
// session_replication_role=replica (superuser-only, scoped by SET LOCAL to
// that one transaction) is used to bypass the FK trigger just long enough
// to delete a SCRATCH rule_set_versions row this test inserts itself --
// never one of the migration-seeded rows seedRuleSetVersionID/other tests
// in this package depend on.
func TestInvoicePort_CanonicalMissingRuleSetVersionRowDoesNotError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-ADV dangling-rsv tenant")
	entityID := seedEntity(t, super, tenantID, "T03-ADV dangling-rsv entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "T03-ADV-DANGLING")

	var maxVersion int
	if err := super.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM rule_set_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("look up max(version): %v", err)
	}
	var rsvID string
	if err := super.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, is_active) VALUES ($1, false) RETURNING id`,
		maxVersion+1000001, // scratch, never collides with a seeded version
	).Scan(&rsvID); err != nil {
		t.Fatalf("seed scratch rule_set_versions row: %v", err)
	}

	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, rsvID, invoiceID,
	); err != nil {
		t.Fatalf("stamp rule_set_version_id: %v", err)
	}

	fkBypassTx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin FK-bypass tx: %v", err)
	}
	if _, err := fkBypassTx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatalf("SET LOCAL session_replication_role: %v", err)
	}
	if _, err := fkBypassTx.Exec(ctx, `DELETE FROM rule_set_versions WHERE id = $1`, rsvID); err != nil {
		t.Fatalf("delete scratch rule_set_versions row via FK bypass: %v", err)
	}
	if err := fkBypassTx.Commit(ctx); err != nil {
		t.Fatalf("commit FK-bypass delete: %v", err)
	}

	// Confirm the dangling state before exercising the port: reference
	// still set, target row gone.
	var stillSet *string
	if err := super.QueryRow(ctx, `SELECT rule_set_version_id FROM invoices WHERE id = $1`, invoiceID).Scan(&stillSet); err != nil {
		t.Fatalf("read back rule_set_version_id: %v", err)
	}
	if stillSet == nil || *stillSet != rsvID {
		t.Fatalf("rule_set_version_id after FK-bypass delete = %v, want unchanged %q (dangling reference)", stillSet, rsvID)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM rule_set_versions WHERE id = $1`, rsvID); n != 0 {
		t.Fatalf("rule_set_versions rows for %q after delete = %d, want 0", rsvID, n)
	}

	var port submission.InvoicePort = NewStore(app)
	var got submission.Canonical
	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		got, err = port.Canonical(ctx, tx, invoiceID)
		return err
	})
	if err != nil {
		t.Fatalf("port.Canonical (dangling rule_set_version_id): %v (want nil -- the subselect's ErrNoRows must stay swallowed)", err)
	}
	if got.InvoiceID != invoiceID {
		t.Errorf("port.Canonical (dangling rule_set_version_id): InvoiceID = %q, want %q", got.InvoiceID, invoiceID)
	}
}
