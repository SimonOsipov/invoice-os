// M5-04-03 (task-229): tests for internal/submission's InvoicePort
// (invoice_port.go) and its *invoice.Store implementation
// (submission_port.go). Written BEFORE the real bodies exist (RED against
// submission_port.go's Stage 2.5 stubs, which all return
// errPortNotImplemented). Reuses the dbTestPools/seedTenant/seedEntity/
// seedInvoice/seedRuleSetVersionID harness from store_test.go/
// apply_validation_test.go (same package).
//
// T03-1 has no dedicated test function here: it is the compile-time
// assertion `var _ submission.InvoicePort = (*Store)(nil)` at
// submission_port.go:27 — satisfied (green) the moment this file and
// invoice_port.go both compile, independent of whether the method bodies
// are real or stubbed. Forcing a red-to-green Test wrapper around a
// property that is either always-true-by-construction or a build failure
// would be theater, not a spec.
//
// Spec-to-test map (task-229 Test Specs table T03-1..T03-6):
//
//	T03-1 satisfied structurally, submission_port.go:27 (see above)
//	T03-2 TestInvoicePort_GetTxMatchesStoreGet
//	T03-3 TestInvoicePort_CanonicalOrdersLinesWithNoRequestIdentity
//	T03-4 TestInvoicePort_HasFiscalOutcomeReadsIrnColumnOnly
//	T03-5 TestRLS_InvoicePortCrossTenantNotFound
//	T03-6 internal/submission/deps_test.go, UNMODIFIED (confirmed green
//	      both before and after this subtask's changes — no invoice import
//	      was added to internal/submission)
package invoice

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestInvoicePort_GetTxMatchesStoreGet (T03-2): Store.Get and the new
// tx-scoped getTx must return byte-identical Invoice values for the SAME
// id — the regression proof that extracting getTx out of Store.Get (Stage
// 3) changes nothing observable. Seeds an invoice with THREE line items
// out of line_no order (so ordering is actually exercised, not vacuously
// true for a 0/1-line invoice) and a stamped, non-nil RuleSetVersionID (so
// the correlated rule_set_versions subselect is exercised too) — mirrors
// TestStoreGet_HydratesLineItemsOrdered + TestStoreGet_PopulatesRuleSetVersion
// (store_test.go) combined onto one invoice.
//
// RED today: getTx is a Stage 2.5 stub returning (Invoice{},
// errPortNotImplemented) (submission_port.go) — this fails on that
// sentinel error, not a compile error. Store.Get itself is untouched and
// already correct (03's own current, shipped code), so got1 succeeds;
// got2 (via getTx) is what fails.
func TestInvoicePort_GetTxMatchesStoreGet(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-2 tenant")
	entityID := seedEntity(t, super, tenantID, "T03-2 entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "T03-2")

	// Seed 3 line_items OUT of line_no order -- an unordered read would
	// fail the ordering assertion below (mirrors
	// TestStoreGet_HydratesLineItemsOrdered).
	for _, lineNo := range []int{3, 1, 2} {
		if _, err := super.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no, description) VALUES ($1, $2, $3, $4)`,
			tenantID, invoiceID, lineNo, fmt.Sprintf("T03-2 line %d", lineNo),
		); err != nil {
			t.Fatalf("seed line_items (line_no=%d): %v", lineNo, err)
		}
	}

	// Stamp a real rule_set_version_id so the correlated subselect is
	// exercised -- a nil RuleSetVersionID would leave half of this test's
	// coverage claim vacuous.
	rsvID := seedRuleSetVersionID(t, super)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, rsvID, invoiceID,
	); err != nil {
		t.Fatalf("stamp rule_set_version_id: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got1, err := store.Get(c, invoiceID)
	if err != nil {
		t.Fatalf("Store.Get: %v", err)
	}
	if len(got1.LineItems) != 3 {
		t.Fatalf("Store.Get: len(LineItems) = %d, want 3 (seed produced a vacuous ordering check otherwise)", len(got1.LineItems))
	}
	if got1.RuleSetVersion == nil {
		t.Fatalf("Store.Get: RuleSetVersion = nil, want non-nil (seed stamped rule_set_version_id; a nil result would make T03-2's subselect-coverage claim vacuous)")
	}

	var got2 Invoice
	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		got2, err = getTx(ctx, tx, invoiceID)
		return err
	})
	if err != nil {
		t.Fatalf("getTx: %v (want nil -- getTx must be Store.Get's tx-scoped body, not a stub)", err)
	}

	if !reflect.DeepEqual(got1, got2) {
		t.Errorf("Store.Get and getTx returned different Invoice values for the same id:\nStore.Get = %+v\ngetTx     = %+v", got1, got2)
	}
	for i, want := range []int{1, 2, 3} {
		if got2.LineItems[i].LineNo != want {
			t.Errorf("getTx: LineItems[%d].LineNo = %d, want %d (ordered by line_no)", i, got2.LineItems[i].LineNo, want)
		}
	}
}

// TestInvoicePort_CanonicalOrdersLinesWithNoRequestIdentity (T03-3): the
// worker calls Canonical inside its OWN db.WithinTenantTx(ctx, app,
// tenantID, ...) with a bare context.Background() -- no JWT, no
// auth.Identity in ctx at all (the worker path, mirrors 02's own T02-3
// "no identity in context" idiom, system_actor_test.go:45-47). Seeds 3
// line_items out of line_no order so an unordered read would fail the
// ordering assertion, exactly as T03-2 above.
//
// RED today: Canonical's Stage 2.5 stub (submission_port.go) returns
// (submission.Canonical{}, errPortNotImplemented) -- this fails on the
// sentinel err, not a compile error.
func TestInvoicePort_CanonicalOrdersLinesWithNoRequestIdentity(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background() // deliberately no auth.WithIdentity -- the worker path

	tenantID := seedTenant(t, super, "T03-3 tenant")
	entityID := seedEntity(t, super, tenantID, "T03-3 entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "T03-3")

	for _, lineNo := range []int{3, 1, 2} {
		if _, err := super.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no, description) VALUES ($1, $2, $3, $4)`,
			tenantID, invoiceID, lineNo, fmt.Sprintf("T03-3 line %d", lineNo),
		); err != nil {
			t.Fatalf("seed line_items (line_no=%d): %v", lineNo, err)
		}
	}

	var port submission.InvoicePort = NewStore(app)

	var got submission.Canonical
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		got, err = port.Canonical(ctx, tx, invoiceID)
		return err
	})
	if err != nil {
		t.Fatalf("port.Canonical (no request identity): %v (want nil)", err)
	}

	// The hazard submission_canonical.go's header records: a non-hydrated
	// invoice silently maps to ZERO lines, and the pure mapper cannot
	// detect it -- assert a non-zero count explicitly, or this test would
	// pass against a broken implementation that fed Canonical a
	// List-shaped (LineItems-nil) invoice instead of a getTx-hydrated one.
	if len(got.Lines) != 3 {
		t.Fatalf("port.Canonical: len(Lines) = %d, want 3 (a hydrated invoice must carry its line items)", len(got.Lines))
	}
	for i, want := range []int{1, 2, 3} {
		if got.Lines[i].LineNo != want {
			t.Errorf("port.Canonical: Lines[%d].LineNo = %d, want %d (ordered by line_no)", i, got.Lines[i].LineNo, want)
		}
	}
	if got.InvoiceID != invoiceID {
		t.Errorf("port.Canonical: InvoiceID = %q, want %q", got.InvoiceID, invoiceID)
	}
}

// TestInvoicePort_HasFiscalOutcomeReadsIrnColumnOnly (T03-4): false for a
// fresh invoice; true once a superuser sets irn; STILL false when only
// csid is set -- HasFiscalOutcome reads invoices.irn exclusively, never
// csid. No CHECK correlates the two columns
// (migrations/20260722083015_invoices_fiscal_outcome.sql), so this is a
// real semantic pin, not an accident.
//
// RED today: the Stage 2.5 stub always returns (false,
// errPortNotImplemented) -- this fails the "true after irn is set" case's
// err check (want nil, got the sentinel), not a compile error.
func TestInvoicePort_HasFiscalOutcomeReadsIrnColumnOnly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T03-4 tenant")
	entityID := seedEntity(t, super, tenantID, "T03-4 entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "T03-4")

	var port submission.InvoicePort = NewStore(app)

	hasOutcome := func() (bool, error) {
		var got bool
		err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			var err error
			got, err = port.HasFiscalOutcome(ctx, tx, invoiceID)
			return err
		})
		return got, err
	}

	// Case 1: fresh invoice, irn and csid both NULL -> false, nil.
	got, err := hasOutcome()
	if err != nil {
		t.Fatalf("HasFiscalOutcome (fresh invoice): %v (want nil)", err)
	}
	if got {
		t.Errorf("HasFiscalOutcome (fresh invoice) = true, want false (irn is NULL)")
	}

	// Case 2: irn set by a superuser (stands in for the eventual poll-result
	// writer) -> true, nil.
	if _, err := super.Exec(ctx, `UPDATE invoices SET irn = $1 WHERE id = $2`, "IRN-T03-4", invoiceID); err != nil {
		t.Fatalf("stamp irn: %v", err)
	}
	got, err = hasOutcome()
	if err != nil {
		t.Fatalf("HasFiscalOutcome (irn set): %v (want nil)", err)
	}
	if !got {
		t.Errorf("HasFiscalOutcome (irn set) = false, want true")
	}

	// Case 3: clear irn, set ONLY csid -> STILL false -- no CHECK
	// correlates the two columns, and HasFiscalOutcome must read irn
	// exclusively.
	if _, err := super.Exec(ctx, `UPDATE invoices SET irn = NULL, csid = $1 WHERE id = $2`, "CSID-T03-4", invoiceID); err != nil {
		t.Fatalf("clear irn, stamp csid: %v", err)
	}
	got, err = hasOutcome()
	if err != nil {
		t.Fatalf("HasFiscalOutcome (csid-only): %v (want nil)", err)
	}
	if got {
		t.Errorf("HasFiscalOutcome (csid-only) = true, want false (only irn should count, never csid)")
	}
}

// TestRLS_InvoicePortCrossTenantNotFound (T03-5, closes AC#4's "0-rows
// cross-tenant" clause -- the orphan T03-4 alone does not cover, Stage-2
// validation 2026-07-23): tenant A's invoice, read inside tenant B's
// db.WithinTenantTx, must fail closed on BOTH port methods --
// port.Canonical never returns a hydrated invoice (not-found, the same
// class Store.Get already maps pgx.ErrNoRows to), and
// port.HasFiscalOutcome returns (false, nil) -- never true, never an
// error. Stamps tenant A's invoice with a real irn first, so a leak on
// HasFiscalOutcome would be observable as `true`, not merely a
// coincidental zero-value false.
//
// RED today: both stub bodies return errPortNotImplemented
// unconditionally, so Canonical's err fails the errors.Is(err,
// ErrNotFound) assertion (wrong error, not "no error"), and
// HasFiscalOutcome's err fails "want nil" -- neither is a compile error.
func TestRLS_InvoicePortCrossTenantNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "T03-5 tenant A")
	tenantB := seedTenant(t, super, "T03-5 tenant B")
	entityA := seedEntity(t, super, tenantA, "T03-5 entity A")
	invoiceA := seedInvoice(t, super, tenantA, entityA, "T03-5")

	// Stamp a real irn so a cross-tenant leak of HasFiscalOutcome would
	// surface as `true`, not a coincidental false.
	if _, err := super.Exec(ctx, `UPDATE invoices SET irn = $1 WHERE id = $2`, "IRN-T03-5", invoiceA); err != nil {
		t.Fatalf("stamp irn on tenant A's invoice: %v", err)
	}

	var port submission.InvoicePort = NewStore(app)

	// Canonical, from inside tenant B's tx, on tenant A's invoice id: RLS
	// 0-rows -> not-found, never a hydrated invoice.
	err := db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		got, err := port.Canonical(ctx, tx, invoiceA)
		if err == nil {
			t.Errorf("port.Canonical (tenant B tx, tenant A's invoice id) = %+v, want ErrNotFound (RLS must 0-row this, not hydrate it)", got)
		} else if !errors.Is(err, ErrNotFound) {
			t.Errorf("port.Canonical (tenant B tx, tenant A's invoice id) err = %v, want ErrNotFound", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant B, Canonical check): %v", err)
	}

	// HasFiscalOutcome, same cross-tenant id: (false, nil) -- never true,
	// never an error.
	err = db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		got, err := port.HasFiscalOutcome(ctx, tx, invoiceA)
		if err != nil {
			t.Errorf("port.HasFiscalOutcome (tenant B tx, tenant A's invoice id) err = %v, want nil", err)
		}
		if got {
			t.Errorf("port.HasFiscalOutcome (tenant B tx, tenant A's invoice id) = true, want false (RLS must 0-row this)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant B, HasFiscalOutcome check): %v", err)
	}
}
