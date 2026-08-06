// task-411 (BUG-05-02) Stage 2.5 RED: DemoteRevalidatedTx specs, run against
// the not-implemented stub in revalidate.go so every assertion below fails
// for its own reason, never a compile error. Reuses the dbTestPools/
// seedTenant/seedEntity/seedInvoiceAtStatus/seedInvoiceWithViolations/
// seedRuleSetVersionID/mustCount/auditCount/auditPayload/readViolations/
// snapshotInvoiceGateState/assertGateSnapshotUnchanged/pgCode harness from
// store_test.go/apply_validation_test.go/transition_adversarial_test.go
// (same package).
//
// AC-6's mechanism is a phantom rule_set_version_id (never seeded into
// rule_set_versions), not the original 256-char-actor design:
// RevalidateActor's Subject is a fixed literal no caller input can
// lengthen -- see task-411's Implementation Notes [ac6-forcing-mechanism].
//
// AC-7 (canRevalidate/canSubmit/legalTransitions unchanged) needs no new
// test here: TestCanRevalidate_AgreesWithThePromotionEdge already exists at
// transition_test.go:840 and stays green untouched.
package invoice

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// demoteRevalidatedPayload is invoice.validated's payload shape for this
// method's "demoted" outcome -- deliberately NOT applyValidationOutcomePayload
// (apply_validation_adversarial_test.go), whose fields differ.
type demoteRevalidatedPayload struct {
	ID               string `json:"id"`
	RuleSetVersionID string `json:"rule_set_version_id"`
	Outcome          string `json:"outcome"`
	ViolationCount   int    `json:"violation_count"`
}

// transitionedPayload is invoice.transitioned's payload shape (transitionTx,
// store.go), used here to confirm the demotion's own from/to.
type transitionedPayload struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

// TestDemoteRevalidated_StampsAndDemotes (AC-1): a validated invoice given a
// blocking violation set ends as draft, with those violations and the new
// rule_set_version_id stored.
func TestDemoteRevalidated_StampsAndDemotes(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC1 tenant")
	entityID := seedEntity(t, super, tenantID, "AC1 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC1-INV", "validated", "[]")
	versionID := seedRuleSetVersionID(t, super)
	vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required", Path: "buyer_tin"}}

	var got Invoice
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		got, err = store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, vs, versionID)
		return err
	})
	if err != nil {
		t.Fatalf("DemoteRevalidatedTx: %v (want nil)", err)
	}

	if got.Status != StatusDraft {
		t.Errorf("returned Invoice.Status = %q, want %q", got.Status, StatusDraft)
	}
	if got.RuleSetVersionID == nil || *got.RuleSetVersionID != versionID {
		t.Errorf("returned Invoice.RuleSetVersionID = %v, want %q", got.RuleSetVersionID, versionID)
	}

	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("invoices.status = %q, want %q", status, StatusDraft)
	}
	if storedVs := readViolations(t, super, invID); !reflect.DeepEqual(storedVs, vs) {
		t.Errorf("stored violations = %+v, want %+v", storedVs, vs)
	}
}

// TestDemoteRevalidated_WritesOneHistoryRow (AC-2): exactly one
// invoice_status_history row is appended (validated->draft,
// actor='revalidate-rule-set').
func TestDemoteRevalidated_WritesOneHistoryRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC2 tenant")
	entityID := seedEntity(t, super, tenantID, "AC2 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC2-INV", "validated", "[]")
	versionID := seedRuleSetVersionID(t, super)
	vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required"}}

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, vs, versionID)
		return err
	})
	if err != nil {
		t.Fatalf("DemoteRevalidatedTx: %v (want nil)", err)
	}

	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft' AND actor = 'revalidate-rule-set'`,
		invID,
	); n != 1 {
		t.Errorf("invoice_status_history rows (validated->draft, actor=revalidate-rule-set) = %d, want 1", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != 1 {
		t.Errorf("total invoice_status_history rows for invoice = %d, want exactly 1", n)
	}
}

// TestDemoteRevalidated_WritesBothAuditRows (AC-3): invoice.transitioned
// {from,to} and invoice.validated {outcome:"demoted", violation_count} both
// land in the same tx.
func TestDemoteRevalidated_WritesBothAuditRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC3 tenant")
	entityID := seedEntity(t, super, tenantID, "AC3 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC3-INV", "validated", "[]")
	versionID := seedRuleSetVersionID(t, super)
	vs := []Violation{
		{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required"},
		{RuleKey: "vat-standard-rate", Severity: "warning", Message: "advisory"},
	}

	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")
	beforeValidated := auditCount(t, app, tenantID, "invoice.validated")

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, vs, versionID)
		return err
	})
	if err != nil {
		t.Fatalf("DemoteRevalidatedTx: %v (want nil)", err)
	}

	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned+1 {
		t.Errorf("audit_log invoice.transitioned rows = %d, want %d (+1)", n, beforeTransitioned+1)
	}
	transitionedRaw := auditPayload(t, app, tenantID, "invoice.transitioned")
	var transitioned transitionedPayload
	if err := json.Unmarshal(transitionedRaw, &transitioned); err != nil {
		t.Fatalf("unmarshal invoice.transitioned payload %s: %v", transitionedRaw, err)
	}
	if transitioned.From != "validated" || transitioned.To != "draft" {
		t.Errorf("invoice.transitioned payload = %+v, want from=validated to=draft", transitioned)
	}

	if n := auditCount(t, app, tenantID, "invoice.validated"); n != beforeValidated+1 {
		t.Errorf("audit_log invoice.validated rows = %d, want %d (+1)", n, beforeValidated+1)
	}
	validatedRaw := auditPayload(t, app, tenantID, "invoice.validated")
	var validated demoteRevalidatedPayload
	if err := json.Unmarshal(validatedRaw, &validated); err != nil {
		t.Fatalf("unmarshal invoice.validated payload %s: %v", validatedRaw, err)
	}
	if validated.Outcome != "demoted" {
		t.Errorf("invoice.validated payload outcome = %q, want %q", validated.Outcome, "demoted")
	}
	if validated.ViolationCount != len(vs) {
		t.Errorf("invoice.validated payload violation_count = %d, want %d", validated.ViolationCount, len(vs))
	}
	if validated.RuleSetVersionID != versionID {
		t.Errorf("invoice.validated payload rule_set_version_id = %q, want %q", validated.RuleSetVersionID, versionID)
	}
}

// TestDemoteRevalidated_NoOpsWhenNoLongerValidated (AC-4): called on an
// invoice that is no longer validated, it writes nothing and returns the
// locked row unchanged.
func TestDemoteRevalidated_NoOpsWhenNoLongerValidated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	for _, status := range []Status{StatusDraft, StatusQueued, StatusAccepted} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			tenantID := seedTenant(t, super, "AC4 tenant "+string(status))
			entityID := seedEntity(t, super, tenantID, "AC4 entity")
			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AC4-"+string(status), status)
			versionID := seedRuleSetVersionID(t, super)
			vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "should never be stamped"}}

			before := snapshotInvoiceGateState(t, super, invID)
			beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
			beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")
			beforeValidated := auditCount(t, app, tenantID, "invoice.validated")

			var got Invoice
			err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
				var err error
				got, err = store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, vs, versionID)
				return err
			})
			if err != nil {
				t.Fatalf("DemoteRevalidatedTx on a %s invoice: %v (want nil, a no-op)", status, err)
			}
			if got.Status != status {
				t.Errorf("returned Invoice.Status = %q, want unchanged %q", got.Status, status)
			}

			assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "AC4-"+string(status))
			if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
				t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
			}
			if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
				t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d", n, beforeTransitioned)
			}
			if n := auditCount(t, app, tenantID, "invoice.validated"); n != beforeValidated {
				t.Errorf("audit_log invoice.validated rows = %d, want unchanged %d", n, beforeValidated)
			}
		})
	}
}

// TestDemoteRevalidated_IsIdempotentOnReplay (AC-4): calling it twice with
// the same arguments demotes once; the second call is the no-op.
func TestDemoteRevalidated_IsIdempotentOnReplay(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC4-REPLAY tenant")
	entityID := seedEntity(t, super, tenantID, "AC4-REPLAY entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC4-REPLAY", "validated", "[]")
	versionID := seedRuleSetVersionID(t, super)
	vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required"}}

	call := func() (Invoice, error) {
		var got Invoice
		err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			var err error
			got, err = store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, vs, versionID)
			return err
		})
		return got, err
	}

	first, err := call()
	if err != nil {
		t.Fatalf("first DemoteRevalidatedTx call: %v (want nil)", err)
	}
	if first.Status != StatusDraft {
		t.Fatalf("first call returned status = %q, want %q", first.Status, StatusDraft)
	}

	second, err := call()
	if err != nil {
		t.Fatalf("second (replayed) DemoteRevalidatedTx call: %v (want nil, a no-op)", err)
	}
	if second.Status != StatusDraft {
		t.Errorf("second call returned status = %q, want unchanged %q", second.Status, StatusDraft)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != 1 {
		t.Errorf("invoice_status_history rows after two calls = %d, want exactly 1 (second call must be a no-op)", n)
	}
}

// TestDemoteRevalidated_NilViolationsStoreEmptyArray (AC-5): a nil violation
// slice stores the literal [], never 'null'::jsonb ([violations-write],
// store.go:1623-1633).
func TestDemoteRevalidated_NilViolationsStoreEmptyArray(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC5 tenant")
	entityID := seedEntity(t, super, tenantID, "AC5 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC5-INV", "validated", "[]")
	versionID := seedRuleSetVersionID(t, super)

	var nilViolations []Violation // deliberately nil, not []Violation{}

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, nilViolations, versionID)
		return err
	})
	if err != nil {
		t.Fatalf("DemoteRevalidatedTx (nil violations): %v (want nil)", err)
	}

	var violationsText string
	if err := super.QueryRow(context.Background(), `SELECT violations::text FROM invoices WHERE id = $1`, invID).Scan(&violationsText); err != nil {
		t.Fatalf("read back violations: %v", err)
	}
	if violationsText != "[]" {
		t.Fatalf("invoices.violations::text = %q, want exactly %q ('null'::jsonb is the specific write-time bug this guards)", violationsText, "[]")
	}
}

// TestDemoteRevalidated_AtomicityRollsBackOnWriteFailure (AC-6): a failure in
// the demotion's writes rolls back the status change and the violations
// stamp together. Forced via a phantom rule_set_version_id (a syntactically
// valid uuid never seeded into rule_set_versions), which trips the FK on
// invoices.rule_set_version_id -- SQLSTATE 23503. Precedent:
// TestRLS_InvoicesRuleSetVersionFK (internal/platform/db/invoices_rls_test.go:551),
// store.go:1620-1621 documents the same scenario for ApplyValidation. Not
// the story's original 256-char-actor design: RevalidateActor's Subject is a
// fixed literal no caller input can lengthen (see task-411's Implementation
// Notes [ac6-forcing-mechanism]).
func TestDemoteRevalidated_AtomicityRollsBackOnWriteFailure(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC6 tenant")
	entityID := seedEntity(t, super, tenantID, "AC6 entity")
	originalViolations := `[{"rule_key":"vat-standard-rate","severity":"warning","message":"pre-existing"}]`
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC6-INV", "validated", originalViolations)

	phantomVersionID := uuid.NewString() // syntactically valid, never seeded into rule_set_versions
	vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required"}}

	before := snapshotInvoiceGateState(t, super, invID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")
	beforeValidated := auditCount(t, app, tenantID, "invoice.validated")

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, invID, tenantID, vs, phantomVersionID)
		return err
	})
	if err == nil {
		t.Fatal("DemoteRevalidatedTx with a phantom rule_set_version_id succeeded, want a foreign_key_violation (SQLSTATE 23503)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("DemoteRevalidatedTx with a phantom rule_set_version_id: pgCode = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "AC6")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
		t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d", n, beforeTransitioned)
	}
	if n := auditCount(t, app, tenantID, "invoice.validated"); n != beforeValidated {
		t.Errorf("audit_log invoice.validated rows = %d, want unchanged %d", n, beforeValidated)
	}
}
