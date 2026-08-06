// task-411 (BUG-05-02) QA Mode B adversarial coverage ON TOP OF the 7 red
// specs in revalidate_test.go. Reuses the dbTestPools/seedTenant/seedEntity/
// seedInvoiceWithViolations/seedRuleSetVersionID/mustCount/auditCount/
// snapshotInvoiceGateState/assertGateSnapshotUnchanged/pgCode harness from
// store_test.go/apply_validation_test.go (same package).
package invoice

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestDemoteRevalidatedTx_CrossTenantIDReturnsErrNotFound: invoices IS
// tenant-owned, so RLS must refuse a cross-tenant call the same way it
// refuses one for MarkSubmittedTx (system_actor_adversarial_test.go) --
// ErrNotFound, never a leak. Exercised through the invoice_app pool, never
// superuser (super is used only to seed/verify out of band).
func TestDemoteRevalidatedTx_CrossTenantIDReturnsErrNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantA := seedTenant(t, super, "ADV-XTEN tenant A")
	tenantB := seedTenant(t, super, "ADV-XTEN tenant B")
	entityA := seedEntity(t, super, tenantA, "ADV-XTEN entity")
	invID := seedInvoiceWithViolations(t, super, tenantA, entityA, "ADV-XTEN", "validated", "[]")
	versionID := seedRuleSetVersionID(t, super)

	before := snapshotInvoiceGateState(t, super, invID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	beforeTransitioned := auditCount(t, app, tenantA, "invoice.transitioned")
	beforeValidated := auditCount(t, app, tenantA, "invoice.validated")

	vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "should never be stamped"}}
	err := db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, invID, tenantB, vs, versionID)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DemoteRevalidatedTx(cross-tenant id): err = %v, want ErrNotFound (never a leak)", err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "ADV-XTEN")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantA, "invoice.transitioned"); n != beforeTransitioned {
		t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d", n, beforeTransitioned)
	}
	if n := auditCount(t, app, tenantA, "invoice.validated"); n != beforeValidated {
		t.Errorf("audit_log invoice.validated rows = %d, want unchanged %d", n, beforeValidated)
	}
}

// TestDemoteRevalidatedTx_MismatchedActorTenantRefusedByRLS: the tx's GUC is
// tenantA (owns the row, so it's visible/lockable), but the tenantID param
// is tenantB -- RevalidateActor(tenantB).TenantID lands in the history
// INSERT's tenant_id column, refused by invoice_status_history's tenant_isolation
// WITH CHECK (42501), same mechanism as TestMarkFailedTx_HistoryTenantScopedByRLS's
// mismatched-tenant subtest (system_actor_test.go:167).
//
// Unlike AC-6's phantom-ruleSetVersionID mechanism (which fails at the FIRST
// write), this fails at transitionTx's history INSERT -- AFTER the violations/
// rule_set_version_id UPDATE and the status UPDATE have both already
// succeeded in the SAME tx. Proves a later statement's failure rolls back an
// earlier statement's write, not just "nothing was ever written".
func TestDemoteRevalidatedTx_MismatchedActorTenantRefusedByRLS(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantA := seedTenant(t, super, "ADV-MISMATCH tenant A")
	tenantB := seedTenant(t, super, "ADV-MISMATCH tenant B")
	entityA := seedEntity(t, super, tenantA, "ADV-MISMATCH entity")
	originalViolations := `[{"rule_key":"vat-standard-rate","severity":"warning","message":"pre-existing"}]`
	invID := seedInvoiceWithViolations(t, super, tenantA, entityA, "ADV-MISMATCH", "validated", originalViolations)
	versionID := seedRuleSetVersionID(t, super)

	before := snapshotInvoiceGateState(t, super, invID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)

	vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required"}}
	err := db.WithinTenantTx(ctx, app, tenantA, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, invID, tenantB, vs, versionID)
		return err
	})
	if err == nil {
		t.Fatal("DemoteRevalidatedTx with a mismatched Actor.TenantID succeeded, want an RLS violation (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("DemoteRevalidatedTx with a mismatched Actor.TenantID: pgCode = %q, want 42501 (insufficient_privilege / RLS WITH CHECK): %v", code, err)
	}

	// The violations/rule_set_version_id stamp (step 4) and the status write
	// (inside transitionTx, before the refused history INSERT) both ran
	// successfully earlier in this SAME tx -- this proves they rolled back too.
	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "ADV-MISMATCH")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
}

// TestDemoteRevalidatedTx_ConcurrentCallsSerializeToOneDemotion: two
// concurrently-open transactions racing DemoteRevalidatedTx on the SAME
// validated invoice -- unlike calling it twice sequentially (which would
// pass even without a real lock), this exercises the FOR UPDATE row lock:
// the losing goroutine blocks until the winner commits, then observes
// status already == draft and takes the idempotent no-op branch. Modelled on
// TestMarkSubmittedTx_ConcurrentCallsSerializeToOneHistoryRow
// (system_actor_adversarial_test.go). Also the adversarial pin for the
// step-2 status guard: transitionTx itself does not re-check `current`
// against the row (store.go:1477, UPDATE has no WHERE status=... clause) --
// this guard is the SOLE defense, and a future refactor that removes it
// would make every racing goroutine see status=validated simultaneously and
// each attempt a real transition, producing more than one history row.
func TestDemoteRevalidatedTx_ConcurrentCallsSerializeToOneDemotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-RACE tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-RACE entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "ADV-RACE", "validated", "[]")
	versionID := seedRuleSetVersionID(t, super)
	vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required"}}

	const n = 4
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
				_, err := store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, vs, versionID)
				return err
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent DemoteRevalidatedTx[%d]: err = %v, want nil (winner demotes, losers take the idempotent no-op)", i, err)
		}
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("status after concurrent DemoteRevalidatedTx = %q, want %q", status, StatusDraft)
	}
	if hn := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'draft'`, invID); hn != 1 {
		t.Errorf("history rows (to_status=draft) = %d, want exactly 1 (FOR UPDATE serialized the race)", hn)
	}
	if an := auditCount(t, app, tenantID, "invoice.validated"); an != 1 {
		t.Errorf("audit_log invoice.validated rows = %d, want exactly 1", an)
	}
}

// TestDemoteRevalidatedTx_NonexistentIDReturnsErrNotFound: mirrors
// TestMarkSubmittedTx_NonexistentIDReturnsErrNotFound -- step 1's SELECT ...
// FOR UPDATE 0-rows on a well-formed but nonexistent id, mapped to
// ErrNotFound. Untested by the 7 red specs (all seed a real invoice first).
func TestDemoteRevalidatedTx_NonexistentIDReturnsErrNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-404 tenant")
	versionID := seedRuleSetVersionID(t, super)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, "00000000-0000-0000-0000-000000000000", tenantID, nil, versionID)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DemoteRevalidatedTx(nonexistent id): err = %v, want ErrNotFound", err)
	}
}

// TestDemoteRevalidatedTx_MalformedIDReturnsErrValidation: a non-UUID id
// trips 22P02 (invalid_text_representation) on the FOR UPDATE SELECT itself,
// mapped to ErrValidation -- the same handling markTerminalTx/Store.Transition
// share (system_actor_adversarial_test.go's SA-ADV-3). Untested by the 7 red
// specs.
func TestDemoteRevalidatedTx_MalformedIDReturnsErrValidation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-MALFORMED tenant")
	versionID := seedRuleSetVersionID(t, super)

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, "not-a-uuid", tenantID, nil, versionID)
		return err
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("DemoteRevalidatedTx(malformed id): err = %v, want ErrValidation", err)
	}
}

// TestDemoteRevalidatedTx_LargeViolationSetStoresAllEntries: a
// several-hundred-entry violation set stores every entry verbatim -- proves
// nothing along the write path (marshal, jsonb column, RETURNING scan)
// truncates or drops entries.
func TestDemoteRevalidatedTx_LargeViolationSetStoresAllEntries(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-LARGE tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-LARGE entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "ADV-LARGE", "validated", "[]")
	versionID := seedRuleSetVersionID(t, super)

	const want = 500
	vs := make([]Violation, want)
	for i := range vs {
		vs[i] = Violation{RuleKey: "bulk-rule", Severity: "warning", Message: "bulk violation", Path: "line_items"}
	}
	vs[want-1].Severity = "error" // ensure a real blocking violation is among them

	var got Invoice
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		got, err = store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, vs, versionID)
		return err
	})
	if err != nil {
		t.Fatalf("DemoteRevalidatedTx (large violation set): %v (want nil)", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("returned Invoice.Status = %q, want %q", got.Status, StatusDraft)
	}

	stored := readViolations(t, super, invID)
	if len(stored) != want {
		t.Fatalf("stored violation count = %d, want %d", len(stored), want)
	}
	if stored[want-1].Severity != "error" {
		t.Errorf("stored violations[%d].Severity = %q, want %q -- the last entry, want no truncation from the tail", want-1, stored[want-1].Severity, "error")
	}
}
