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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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

// ============================================================================
// task-412 (BUG-05-03) Stage 2.5 RED: AC-1..AC-10 for RevalidateActive, run
// against the not-implemented stub in revalidate.go (returns a sentinel
// error) so every assertion below fails for its own reason, never a compile
// error or nil panic. AC-11's CLI specs live in
// tools/revalidate-invoices/main_test.go instead (plain Go, no DB env).
//
// Reuses this file's own AC-1..AC-6 harness (dbTestPools/seedTenant/
// seedEntity/seedInvoiceAtStatus/seedInvoiceWithViolations/
// seedRuleSetVersionID/mustCount/auditCount/readViolations/
// snapshotInvoiceGateState/assertGateSnapshotUnchanged) plus store_test.go's
// strPtr and gate_test.go's hasViolation (same package, no redeclaration).
//
// The fake 04 below evaluates buyer.tin (and, for AC-8 only, line_items)
// presence straight off the wire payload MBSPayload produced -- never a
// second copy of that mapping -- mirroring gate_test.go's own
// TestGate_EvaluateCallsValidatorExactlyOnceWithAllItems fake-server idiom.
// No test here needs a real VALIDATION_URL env var or the real rule engine
// (task-412's Stage 1 validation, part 3/3): an httptest.Server passed
// straight into NewValidator(srv.URL, ...) is the shipped pattern.
// ============================================================================

// revalidateS2SToken is an arbitrary in-process peer secret for this file's
// fake validator servers -- never read from env, scoped to these tests only.
const revalidateS2SToken = "revalidate-qa-test-s2s-token"

// tinValidatorServer is a fake 04 that blocks ONLY on a missing buyer TIN --
// the one rule BUG-05 exists to close. calls counts every batch POST it
// receives, so AC-9 can prove the network was never reached.
type tinValidatorServer struct {
	*httptest.Server
	calls int
}

// newTINValidatorServer decodes the wire request with validator.go's own
// (unexported, same-package) validateBatchRequest.
func newTINValidatorServer(t *testing.T, ruleSetVersionID string) *tinValidatorServer {
	t.Helper()
	s := &tinValidatorServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		body, _ := io.ReadAll(r.Body)
		var req validateBatchRequest
		_ = json.Unmarshal(body, &req)

		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			results[i] = validateBatchItemResult{Ref: it.Ref, Violations: tinOnlyViolations(it.Invoice)}
		}
		writeValidateResponse(t, w, ruleSetVersionID, results)
	}))
	t.Cleanup(s.Server.Close)
	return s
}

// tinOnlyViolations blocks iff the wire payload's buyer.tin is absent or
// empty -- decodes payload.go's party() shape, never a second copy of it.
func tinOnlyViolations(inv map[string]any) []Violation {
	buyer, _ := inv["buyer"].(map[string]any)
	tin, _ := buyer["tin"].(string)
	if tin == "" {
		return []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required", Path: "buyer.tin"}}
	}
	return []Violation{}
}

// newLineItemsAwareValidatorServer additionally blocks on an absent
// line_items key -- AC-8's own oracle for "Store.Get was used, not
// Store.List" (gate.go's header: List leaves LineItems nil, which the
// payload mapper cannot distinguish from a genuinely line-less invoice).
func newLineItemsAwareValidatorServer(t *testing.T, ruleSetVersionID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req validateBatchRequest
		_ = json.Unmarshal(body, &req)

		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			vs := tinOnlyViolations(it.Invoice)
			if _, ok := it.Invoice["line_items"]; !ok {
				vs = append(vs, Violation{RuleKey: "line-items-required", Severity: "error", Message: "At least one line item is required", Path: "line_items"})
			}
			results[i] = validateBatchItemResult{Ref: it.Ref, Violations: vs}
		}
		writeValidateResponse(t, w, ruleSetVersionID, results)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newOutageValidatorServer always answers 503 -- validator.go maps this to
// ErrNoActiveRuleSet, never a laundered clean verdict (AC-10).
func newOutageValidatorServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeValidateResponse marshals a clean, TOTAL batch response -- never a raw
// JSON string literal (RS-V2-14 / F7 pin-detector scope note, gate_test.go).
func writeValidateResponse(t *testing.T, w http.ResponseWriter, ruleSetVersionID string, results []validateBatchItemResult) {
	t.Helper()
	b, err := json.Marshal(validateBatchResponse{RuleSetVersion: cannedRuleSetVersion, RuleSetVersionID: ruleSetVersionID, Results: results})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// setBuyerTIN force-writes buyer_tin directly (raw SQL, superuser) -- the
// one column tinOnlyViolations reads to decide the verdict.
func setBuyerTIN(t *testing.T, super *pgxpool.Pool, id, tin string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET buyer_tin = $1 WHERE id = $2`, tin, id,
	); err != nil {
		t.Fatalf("force-set buyer_tin: %v", err)
	}
}

// readInvoiceStatus reads invoices.status directly (raw SQL, superuser).
func readInvoiceStatus(t *testing.T, super *pgxpool.Pool, id string) Status {
	t.Helper()
	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read back status for %s: %v", id, err)
	}
	return Status(status)
}

// auditCountForInvoiceEvent counts audit_log rows for tenantID+event whose
// payload names invoiceID specifically -- unlike auditCount (tenant-wide),
// this survives a run that also writes the same event for a SIBLING invoice
// in the same tenant (AC-2's clean-invoice-in-a-mixed-tenant case).
func auditCountForInvoiceEvent(t *testing.T, pool *pgxpool.Pool, tenantID, event, invoiceID string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE event = $1 AND payload->>'id' = $2`, event, invoiceID,
		).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log for invoice %s: %v", invoiceID, err)
	}
	return n
}

// notesContain reports whether any note in notes contains substr.
func notesContain(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

// readAllTenantIDs runs SELECT id FROM tenants ORDER BY id over pool --
// exactly the query reconciliation's enumerateTenants runs (sweep.go:185).
func readAllTenantIDs(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		t.Fatalf("enumerate tenants: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan tenant id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("enumerate tenants rows: %v", err)
	}
	return ids
}

// sameIDSet reports whether a and b contain the same ids, order-independent.
func sameIDSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}

// containsID reports whether id is present in ids.
func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// --- AC-1: only status='validated' is ever touched --------------------------

// TestRevalidateActive_TouchesOnlyValidated (AC-1): one invoice seeded in
// each of the 7 statuses, all TIN-less (buyer_tin left NULL by
// seedInvoiceAtStatus) -- a real run demotes only the validated one; the
// other six are byte-identical after (status, violations, history count).
func TestRevalidateActive_TouchesOnlyValidated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC1 tenant")
	entityID := seedEntity(t, super, tenantID, "AC1 entity")

	statuses := []Status{StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted, StatusRejected, StatusFailed}
	ids := make(map[Status]string, len(statuses))
	before := make(map[Status]invoiceGateSnapshot, len(statuses))
	beforeHistory := make(map[Status]int, len(statuses))
	for _, status := range statuses {
		id := seedInvoiceAtStatus(t, super, tenantID, entityID, "AC1-"+string(status), status)
		ids[status] = id
		before[status] = snapshotInvoiceGateState(t, super, id)
		beforeHistory[status] = mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, id)
	}

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Demoted != 1 {
		t.Errorf("Demoted = %d, want 1 (only the validated invoice)", res.Demoted)
	}

	if got := readInvoiceStatus(t, super, ids[StatusValidated]); got != StatusDraft {
		t.Errorf("the validated invoice's status = %q, want %q (demoted)", got, StatusDraft)
	}
	if vs := readViolations(t, super, ids[StatusValidated]); !hasViolation(vs, "buyer-tin-required") {
		t.Errorf("the demoted invoice's violations = %+v, want one naming buyer-tin-required", vs)
	}

	for _, status := range statuses {
		if status == StatusValidated {
			continue
		}
		id := ids[status]
		assertGateSnapshotUnchanged(t, before[status], snapshotInvoiceGateState(t, super, id), "AC1-"+string(status))
		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, id); n != beforeHistory[status] {
			t.Errorf("%s invoice_status_history rows = %d, want unchanged %d", status, n, beforeHistory[status])
		}
	}
}

// --- AC-2: demotes only the now-failing invoice ------------------------------

// TestRevalidateActive_DemotesOnlyTheNowFailing (AC-2): two validated
// invoices, one TIN-less and one carrying a well-formed buyer TIN -- a real
// run demotes the TIN-less one (buyer-tin-required stored) and leaves the
// clean one completely untouched, including its ORIGINAL rule_set_version_id
// stamp from before this run.
func TestRevalidateActive_DemotesOnlyTheNowFailing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC2 tenant")
	entityID := seedEntity(t, super, tenantID, "AC2 entity")

	tinLessID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC2-TINLESS", "validated", "[]")

	cleanID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC2-CLEAN", "validated", "[]")
	setBuyerTIN(t, super, cleanID, "87654321-0002")
	originalVersionID := seedRuleSetVersionID(t, super)
	if _, err := super.Exec(ctx, `UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, originalVersionID, cleanID); err != nil {
		t.Fatalf("force-stamp original rule_set_version_id: %v", err)
	}
	cleanBefore := snapshotInvoiceGateState(t, super, cleanID)
	cleanBeforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, cleanID)

	srv := newTINValidatorServer(t, seedRuleSetVersionID(t, super))
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Demoted != 1 {
		t.Errorf("Demoted = %d, want 1 (only the TIN-less invoice)", res.Demoted)
	}

	if got := readInvoiceStatus(t, super, tinLessID); got != StatusDraft {
		t.Errorf("TIN-less invoice status = %q, want %q", got, StatusDraft)
	}
	if vs := readViolations(t, super, tinLessID); !hasViolation(vs, "buyer-tin-required") {
		t.Errorf("TIN-less invoice violations = %+v, want one naming buyer-tin-required", vs)
	}

	assertGateSnapshotUnchanged(t, cleanBefore, snapshotInvoiceGateState(t, super, cleanID), "AC2-clean")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, cleanID); n != cleanBeforeHistory {
		t.Errorf("clean invoice invoice_status_history rows = %d, want unchanged %d", n, cleanBeforeHistory)
	}
	if n := auditCountForInvoiceEvent(t, app, tenantID, "invoice.transitioned", cleanID); n != 0 {
		t.Errorf("clean invoice audit_log invoice.transitioned rows = %d, want 0", n)
	}
	if n := auditCountForInvoiceEvent(t, app, tenantID, "invoice.validated", cleanID); n != 0 {
		t.Errorf("clean invoice audit_log invoice.validated rows = %d, want 0", n)
	}
}

// --- AC-3: --all-tenants covers every enumerated tenant ----------------------

// TestRevalidateAllTenants_CoversEveryEnumeratedTenant (AC-3): the
// reader-pool enumeration (no GUC set, the tenant_enumerate policy) must
// return literally every tenant row -- proven by set-equality against a raw
// superuser SELECT id FROM tenants, never a partial/scoped view. Coverage is
// then demonstrated by running RevalidateActive over the two tenants this
// test owns, exactly the operation the CLI's --all-tenants loop performs per
// enumerated element (sweep.go:169-176/185's precedent).
//
// Deliberately NOT driven over the FULL enumerated set: that includes real
// dev-seed tenants (task-412's Stage 1 validation: "4 tenants seeded
// locally"), and writing to their data as a side effect of this test would
// corrupt shared dev-DB state for every other suite.
func TestRevalidateAllTenants_CoversEveryEnumeratedTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	readerURL := os.Getenv("DATABASE_READER_URL")
	if readerURL == "" {
		t.Skip("AC-3 needs DATABASE_READER_URL (the invoice_tenant_reader DSN) set")
	}
	reader, err := db.NewPool(ctx, readerURL)
	if err != nil {
		t.Fatalf("open reader pool: %v", err)
	}
	t.Cleanup(reader.Close)

	tenantA := seedTenant(t, super, "AC3 tenant A")
	entityA := seedEntity(t, super, tenantA, "AC3 entity A")
	invA := seedInvoiceWithViolations(t, super, tenantA, entityA, "AC3-A", "validated", "[]")

	tenantB := seedTenant(t, super, "AC3 tenant B")
	entityB := seedEntity(t, super, tenantB, "AC3 entity B")
	invB := seedInvoiceWithViolations(t, super, tenantB, entityB, "AC3-B", "validated", "[]")

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	readerIDs := readAllTenantIDs(t, reader)
	superIDs := readAllTenantIDs(t, super)
	if !sameIDSet(readerIDs, superIDs) {
		t.Fatalf("reader-pool enumeration = %v, want the same set as a superuser SELECT id FROM tenants = %v", readerIDs, superIDs)
	}
	if !containsID(readerIDs, tenantA) || !containsID(readerIDs, tenantB) {
		t.Fatalf("reader-pool enumeration = %v, want both seeded tenants %s and %s present", readerIDs, tenantA, tenantB)
	}

	for _, tenantID := range []string{tenantA, tenantB} {
		if _, err := RevalidateActive(ctx, app, store, gate, tenantID, false); err != nil {
			t.Fatalf("RevalidateActive(%s): %v (want nil)", tenantID, err)
		}
	}

	for _, id := range []string{invA, invB} {
		if got := readInvoiceStatus(t, super, id); got != StatusDraft {
			t.Errorf("invoice %s status = %q, want %q (demoted)", id, got, StatusDraft)
		}
	}
}

// --- AC-4: --verify semantics (dry-run bracketing a real run) --------------

// TestRevalidateVerify_ExitsNonZeroWhileAnyRemains (AC-4): --verify's own
// mechanism is a full no-write re-scan (task-412's Implementation Notes:
// "--VERIFY IS FULL RE-EVALUATION, not a stored-data query"), i.e. exactly
// RevalidateActive(dryRun=true). A TIN-less validated invoice reports a
// non-zero yield naming the invoice id (what the CLI's --verify turns into a
// non-zero exit); after a real run, the same dry-run re-scan reports clean.
func TestRevalidateVerify_ExitsNonZeroWhileAnyRemains(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC4 tenant")
	entityID := seedEntity(t, super, tenantID, "AC4 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC4-INV", "validated", "[]")

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	verify, err := RevalidateActive(ctx, app, store, gate, tenantID, true)
	if err != nil {
		t.Fatalf("RevalidateActive (verify pass 1, dry-run): %v (want nil)", err)
	}
	if verify.Demoted == 0 {
		t.Fatal("verify pass 1: Demoted = 0, want non-zero -- a TIN-less validated invoice must be reported as still failing")
	}
	if !notesContain(verify.Notes, invID) {
		t.Errorf("verify pass 1: Notes = %v, want an entry naming invoice %s", verify.Notes, invID)
	}

	if _, err := RevalidateActive(ctx, app, store, gate, tenantID, false); err != nil {
		t.Fatalf("RevalidateActive (real run): %v (want nil)", err)
	}

	verify2, err := RevalidateActive(ctx, app, store, gate, tenantID, true)
	if err != nil {
		t.Fatalf("RevalidateActive (verify pass 2, dry-run): %v (want nil)", err)
	}
	if verify2.Demoted != 0 {
		t.Errorf("verify pass 2: Demoted = %d, want 0 -- the real run already cleared it", verify2.Demoted)
	}
}

// --- AC-5: restartable -------------------------------------------------------

// TestRevalidateActive_SecondRunIsANoOp (AC-5): after a real run, running
// again examines nothing (the demoted invoice is no longer validated),
// demotes nothing, and writes no new history/audit rows.
func TestRevalidateActive_SecondRunIsANoOp(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC5 tenant")
	entityID := seedEntity(t, super, tenantID, "AC5 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC5-INV", "validated", "[]")

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	first, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive (first run): %v (want nil)", err)
	}
	if first.Demoted != 1 {
		t.Fatalf("first run Demoted = %d, want 1", first.Demoted)
	}

	afterFirstHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	afterFirstTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")
	afterFirstValidated := auditCount(t, app, tenantID, "invoice.validated")

	second, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive (second run): %v (want nil)", err)
	}
	if second.Examined != 0 {
		t.Errorf("second run Examined = %d, want 0 (the invoice is draft now, no longer validated)", second.Examined)
	}
	if second.Demoted != 0 {
		t.Errorf("second run Demoted = %d, want 0", second.Demoted)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != afterFirstHistory {
		t.Errorf("invoice_status_history rows after second run = %d, want unchanged %d", n, afterFirstHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != afterFirstTransitioned {
		t.Errorf("audit_log invoice.transitioned rows after second run = %d, want unchanged %d", n, afterFirstTransitioned)
	}
	if n := auditCount(t, app, tenantID, "invoice.validated"); n != afterFirstValidated {
		t.Errorf("audit_log invoice.validated rows after second run = %d, want unchanged %d", n, afterFirstValidated)
	}
}

// --- AC-6: dry-run writes nothing but reports the yield ---------------------

// TestRevalidateActive_DryRunWritesNothingButReportsTheYield (AC-6): a
// TIN-less validated invoice under dryRun=true reports Demoted==1 while the
// row itself stays completely untouched.
func TestRevalidateActive_DryRunWritesNothingButReportsTheYield(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC6-DRYRUN tenant")
	entityID := seedEntity(t, super, tenantID, "AC6-DRYRUN entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC6-DRYRUN-INV", "validated", "[]")

	before := snapshotInvoiceGateState(t, super, invID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, true)
	if err != nil {
		t.Fatalf("RevalidateActive (dry-run): %v (want nil)", err)
	}
	if res.Demoted != 1 {
		t.Errorf("Demoted = %d, want 1 (the yield a real run would produce)", res.Demoted)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "AC6-dryrun")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
}

// --- AC-7: reports Examined/Demoted/Clean per run ----------------------------

// TestRevalidateActive_ReportsExaminedAndDemoted (AC-7): 3 validated
// invoices (2 TIN-less, 1 clean) plus 2 non-validated -- the non-validated
// pair is never examined at all.
func TestRevalidateActive_ReportsExaminedAndDemoted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC7 tenant")
	entityID := seedEntity(t, super, tenantID, "AC7 entity")

	seedInvoiceWithViolations(t, super, tenantID, entityID, "AC7-FAIL-1", "validated", "[]")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "AC7-FAIL-2", "validated", "[]")
	cleanID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC7-CLEAN", "validated", "[]")
	setBuyerTIN(t, super, cleanID, "87654321-0002")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "AC7-DRAFT", StatusDraft)
	seedInvoiceAtStatus(t, super, tenantID, entityID, "AC7-QUEUED", StatusQueued)

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Examined != 3 {
		t.Errorf("Examined = %d, want 3 (only the validated invoices)", res.Examined)
	}
	if res.Demoted != 2 {
		t.Errorf("Demoted = %d, want 2", res.Demoted)
	}
	if res.Clean != 1 {
		t.Errorf("Clean = %d, want 1", res.Clean)
	}
}

// --- AC-8: evaluates hydrated line items (Get, never List) ------------------

// TestRevalidateActive_EvaluatesHydratedLineItems (AC-8): a validated
// invoice with real line items and a good buyer TIN must NOT be demoted --
// proving the pass rehydrates via Store.Get (never Store.List, which leaves
// LineItems nil and would make line-items-required fire on every invoice,
// gate.go's own header).
func TestRevalidateActive_EvaluatesHydratedLineItems(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC8 tenant")
	entityID := seedEntity(t, super, tenantID, "AC8 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID: entityID, InvoiceNumber: "AC8-INV",
		BuyerTIN: strPtr("87654321-0002"), BuyerName: strPtr("Beta Ltd"),
		LineItems: []LineItemInput{
			{Quantity: strPtr("2"), UnitPrice: strPtr("100.00"), LineTotal: strPtr("200.00")},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := super.Exec(ctx, `UPDATE invoices SET status = 'validated' WHERE id = $1`, inv.ID); err != nil {
		t.Fatalf("force-seed status to validated: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	srv := newLineItemsAwareValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Demoted != 0 {
		t.Errorf("Demoted = %d, want 0 -- line-items-required must not fire on a line-bearing invoice (a List-not-Get regression)", res.Demoted)
	}
	if got := readInvoiceStatus(t, super, inv.ID); got != StatusValidated {
		t.Errorf("status = %q, want unchanged %q", got, StatusValidated)
	}
}

// --- AC-9: refuses a privileged role before any invoice is read -------------

// TestRevalidateActive_RefusesPrivilegedRole (AC-9): a pool connected as the
// superuser (BYPASSRLS) is refused before the invoice list query ever runs,
// and the validator is never even reached.
func TestRevalidateActive_RefusesPrivilegedRole(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(super)

	tenantID := seedTenant(t, super, "AC9 tenant")
	entityID := seedEntity(t, super, tenantID, "AC9 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC9-INV", "validated", "[]")
	before := snapshotInvoiceGateState(t, super, invID)

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	_, err := RevalidateActive(ctx, super, store, gate, tenantID, false)
	if !errors.Is(err, ErrRevalidatePrivilegedRole) {
		t.Errorf("err = %v, want ErrRevalidatePrivilegedRole", err)
	}
	if srv.calls != 0 {
		t.Errorf("validator (04) received %d requests, want 0 -- the privileged-role refusal must happen before any invoice is read or evaluated", srv.calls)
	}
	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "AC9")
}

// --- AC-10: an upstream outage aborts the tenant and writes nothing --------

// TestRevalidateActive_UpstreamOutageAbortsAndWritesNothing (AC-10): a
// validator stub returning 503 (ErrNoActiveRuleSet, validator.go) must
// propagate raw and demote nothing -- an outage is never a verdict.
func TestRevalidateActive_UpstreamOutageAbortsAndWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "AC10 tenant")
	entityID := seedEntity(t, super, tenantID, "AC10 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "AC10-INV", "validated", "[]")
	before := snapshotInvoiceGateState(t, super, invID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")
	beforeValidated := auditCount(t, app, tenantID, "invoice.validated")

	srv := newOutageValidatorServer(t)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	_, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Errorf("err = %v, want ErrNoActiveRuleSet (raw, from validator.go's 503 mapping)", err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "AC10")
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

// ============================================================================
// task-412 (BUG-05-03) QA: adversarial coverage beyond AC-1..AC-10, plus one
// gap-close for AC-9 that mutation-testing found. Mutation-verified: each
// test below was confirmed to redden against the specific break it names.
// ============================================================================

// TestRevalidateActive_RefusesPrivilegedRoleBeforeAnyInvoiceIDIsRead (AC-9
// gap-close): the shipped AC-9 spec only proves the refusal precedes the
// validator call and any write -- moving refuseRevalidatePrivilegedRole to
// AFTER validatedInvoiceIDs does not redden it. A malformed tenantID pins the
// missing ordering: db.WithinTenantTx validates the uuid BEFORE issuing any
// statement (db.go's own ErrNoTenant fail-closed guard), so if the id-read
// ran first this test would observe a "list validated invoices" wrapping
// error instead of ErrRevalidatePrivilegedRole.
func TestRevalidateActive_RefusesPrivilegedRoleBeforeAnyInvoiceIDIsRead(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(super)

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	_, err := RevalidateActive(ctx, super, store, gate, "not-a-uuid", false)
	if !errors.Is(err, ErrRevalidatePrivilegedRole) {
		t.Errorf("err = %v, want ErrRevalidatePrivilegedRole (the privileged-role check must run before the id-read's own tenant-id validation, not merely before the write)", err)
	}
}

// TestRevalidateActive_NeverTouchesAnotherTenantsInvoices: two tenants each
// hold an equally TIN-less validated invoice; running RevalidateActive for
// tenant A only must leave tenant B's invoice byte-identical. Uses the app
// (invoice_app) pool -- the same RLS-scoped pool as every other spec in this
// file, never super -- so this proves RLS isolation, not a hand-written
// tenant_id filter.
func TestRevalidateActive_NeverTouchesAnotherTenantsInvoices(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantA := seedTenant(t, super, "XTENANT tenant A")
	entityA := seedEntity(t, super, tenantA, "XTENANT entity A")
	seedInvoiceWithViolations(t, super, tenantA, entityA, "XTENANT-A", "validated", "[]")

	tenantB := seedTenant(t, super, "XTENANT tenant B")
	entityB := seedEntity(t, super, tenantB, "XTENANT entity B")
	invB := seedInvoiceWithViolations(t, super, tenantB, entityB, "XTENANT-B", "validated", "[]")
	beforeB := snapshotInvoiceGateState(t, super, invB)
	beforeBHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invB)

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantA, false)
	if err != nil {
		t.Fatalf("RevalidateActive(tenantA): %v (want nil)", err)
	}
	if res.Examined != 1 || res.Demoted != 1 {
		t.Fatalf("res = %+v, want Examined=1 Demoted=1 (only tenant A's own invoice)", res)
	}

	assertGateSnapshotUnchanged(t, beforeB, snapshotInvoiceGateState(t, super, invB), "XTENANT-B")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invB); n != beforeBHistory {
		t.Errorf("tenant B invoice_status_history rows = %d, want unchanged %d", n, beforeBHistory)
	}
	if n := auditCountForInvoiceEvent(t, app, tenantB, "invoice.transitioned", invB); n != 0 {
		t.Errorf("tenant B audit_log invoice.transitioned rows for its invoice = %d, want 0", n)
	}
}

// TestRevalidateActive_TenantWithNoValidatedInvoicesIsACleanNoOp: a tenant
// holding only non-validated invoices examines nothing, demotes nothing, and
// the validator is never called (an empty id set produces zero chunks).
func TestRevalidateActive_TenantWithNoValidatedInvoicesIsACleanNoOp(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ZERO tenant")
	entityID := seedEntity(t, super, tenantID, "ZERO entity")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "ZERO-DRAFT", StatusDraft)
	seedInvoiceAtStatus(t, super, tenantID, entityID, "ZERO-QUEUED", StatusQueued)

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Examined != 0 || res.Demoted != 0 || res.Clean != 0 || res.Skipped != 0 || len(res.Notes) != 0 {
		t.Errorf("res = %+v, want the zero value (nothing validated to examine)", res)
	}
	if srv.calls != 0 {
		t.Errorf("validator received %d call(s), want 0 -- an empty id set must never round-trip", srv.calls)
	}
}

// TestRevalidateActive_WarningSeverityDoesNotDemote: a violation with
// severity="warning" (never "error") must not block -- HasBlockingViolation's
// own contract, pinned end-to-end through RevalidateActive rather than only
// at the store.go unit level.
func TestRevalidateActive_WarningSeverityDoesNotDemote(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "WARN tenant")
	entityID := seedEntity(t, super, tenantID, "WARN entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "WARN-INV", "validated", "[]")
	before := snapshotInvoiceGateState(t, super, invID)

	versionID := seedRuleSetVersionID(t, super)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req validateBatchRequest
		_ = json.Unmarshal(body, &req)
		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			results[i] = validateBatchItemResult{Ref: it.Ref, Violations: []Violation{
				{RuleKey: "vat-standard-rate", Severity: "warning", Message: "advisory, never blocks"},
			}}
		}
		writeValidateResponse(t, w, versionID, results)
	}))
	t.Cleanup(srv.Close)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Demoted != 0 {
		t.Errorf("Demoted = %d, want 0 -- only severity=error blocks", res.Demoted)
	}
	if res.Clean != 1 {
		t.Errorf("Clean = %d, want 1", res.Clean)
	}
	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "WARN")
}

// TestRevalidateActive_SkippedCountsARaceLoss: an invoice leaves 'validated'
// between the id-list read and demoteRevalidated's own write-lock -- the fake
// 04 force-writes the invoice to draft (simulating a concurrent actor) the
// moment it is asked to evaluate it, then still reports a blocking violation.
// Skipped, not Demoted, must count it, and the row must stay exactly as the
// race left it (draft, no rule_set_version_id stamp from this run).
func TestRevalidateActive_SkippedCountsARaceLoss(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SKIP tenant")
	entityID := seedEntity(t, super, tenantID, "SKIP entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "SKIP-INV", "validated", "[]")

	versionID := seedRuleSetVersionID(t, super)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := super.Exec(context.Background(), `UPDATE invoices SET status = 'draft' WHERE id = $1`, invID); err != nil {
			t.Fatalf("force-race invoice to draft: %v", err)
		}
		body, _ := io.ReadAll(r.Body)
		var req validateBatchRequest
		_ = json.Unmarshal(body, &req)
		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			results[i] = validateBatchItemResult{Ref: it.Ref, Violations: tinOnlyViolations(it.Invoice)}
		}
		writeValidateResponse(t, w, versionID, results)
	}))
	t.Cleanup(srv.Close)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (the invoice raced out of validated before the write lock)", res.Skipped)
	}
	if res.Demoted != 0 {
		t.Errorf("Demoted = %d, want 0", res.Demoted)
	}
	if got := readInvoiceStatus(t, super, invID); got != StatusDraft {
		t.Errorf("status = %q, want draft (from the race, not a demotion this run performed)", got)
	}
}

// TestRevalidateActive_VerifyUpstreamOutageFailsLoudNotClean: --verify is
// RevalidateActive(dryRun=true); an upstream outage during that pass must
// still propagate raw, never settle to a false "0 examined, 0 demoted"
// result that a caller could mistake for "everything is clean".
func TestRevalidateActive_VerifyUpstreamOutageFailsLoudNotClean(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "VERIFY-OUTAGE tenant")
	entityID := seedEntity(t, super, tenantID, "VERIFY-OUTAGE entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "VERIFY-OUTAGE-INV", "validated", "[]")

	srv := newOutageValidatorServer(t)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, true)
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Errorf("err = %v, want ErrNoActiveRuleSet", err)
	}
	if res.Examined != 0 || res.Demoted != 0 {
		t.Errorf("res = %+v, want the zero value -- an outage must never masquerade as a clean verify", res)
	}
}

// TestRevalidateActive_ChunksAtExactlyRevalidateChunkSizeBoundary and
// TestRevalidateActive_ChunksOneOverBoundaryIntoTwoCalls pin
// revalidateChunkSize's value at LITERAL 200 (never the revalidateChunkSize
// symbol itself, which would make the seed count and the assertion drift
// together under any mutation of the constant and silently pass) via the
// fake 04's own call counter -- mutation-verified unpinned before this pair
// (both 1 and 10000 passed all 14 shipped specs). All invoices are clean (a
// good buyer TIN) so no writes cloud the call count. A guard confirms
// revalidateChunkSize itself is still 200, so these two tests fail loudly
// (not silently pass wrong) if the constant is ever deliberately changed.
func TestRevalidateActive_ChunksAtExactlyRevalidateChunkSizeBoundary(t *testing.T) {
	if revalidateChunkSize != 200 {
		t.Fatalf("revalidateChunkSize = %d, want 200 -- update this test's literal boundary too", revalidateChunkSize)
	}
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	const n = 200
	tenantID := seedTenant(t, super, "CHUNK tenant")
	entityID := seedEntity(t, super, tenantID, "CHUNK entity")
	for i := 0; i < n; i++ {
		id := seedInvoiceWithViolations(t, super, tenantID, entityID, fmt.Sprintf("CHUNK-%d", i), "validated", "[]")
		setBuyerTIN(t, super, id, "87654321-0002")
	}

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Examined != n {
		t.Errorf("Examined = %d, want %d", res.Examined, n)
	}
	if srv.calls != 1 {
		t.Errorf("validator received %d batch call(s), want exactly 1 for %d invoices at the chunk boundary", srv.calls, n)
	}
}

func TestRevalidateActive_ChunksOneOverBoundaryIntoTwoCalls(t *testing.T) {
	if revalidateChunkSize != 200 {
		t.Fatalf("revalidateChunkSize = %d, want 200 -- update this test's literal boundary too", revalidateChunkSize)
	}
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	const n = 201
	tenantID := seedTenant(t, super, "CHUNK+1 tenant")
	entityID := seedEntity(t, super, tenantID, "CHUNK+1 entity")
	for i := 0; i < n; i++ {
		id := seedInvoiceWithViolations(t, super, tenantID, entityID, fmt.Sprintf("CHUNK1-%d", i), "validated", "[]")
		setBuyerTIN(t, super, id, "87654321-0002")
	}

	versionID := seedRuleSetVersionID(t, super)
	srv := newTINValidatorServer(t, versionID)
	validator := NewValidator(srv.URL, revalidateS2SToken, nil)
	gate := NewGate(store, validator)

	res, err := RevalidateActive(ctx, app, store, gate, tenantID, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v (want nil)", err)
	}
	if res.Examined != n {
		t.Errorf("Examined = %d, want %d", res.Examined, n)
	}
	if srv.calls != 2 {
		t.Errorf("validator received %d batch call(s), want exactly 2 for %d invoices (ceil over the 200 boundary)", srv.calls, n)
	}
}

// --- task-483 (APPR-06-07, Mode A): DemoteRevalidatedTx cancels the live run too ---

// TestRevalidate_CancelsThenRearms (AC-7): a validated invoice with one open run
// (armed under an active policy) --> DemoteRevalidatedTx demotes it --> the run is
// cancelled with closed_by = 'revalidate-rule-set' (RevalidateActor's fixed literal,
// actor.go) --> then ApplyValidation re-promotes it (DemoteRevalidatedTx touches no
// content column, so the SAME pre-demotion fingerprint still matches) --> exactly one
// cancelled run and one open run, the new run carrying the SAME content_fingerprint.
// Fails today: the first run stays open and the re-arm hits 23505 on
// approval_runs_one_open.
func TestRevalidate_CancelsThenRearms(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID, entityID, _ := seedOneStepActivePolicyTenant(t, super, "REVAL-CANCEL-REARM")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "REVAL-CANCEL-REARM"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fp := contentFingerprint(inv, inv.LineItems)
	ruleSetVersionID1 := seedRuleSetVersionID(t, super)
	validated, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID1, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (seed, arms an open run): %v", err)
	}
	if validated.Status != StatusValidated {
		t.Fatalf("ApplyValidation (seed): status = %q, want %q (precondition)", validated.Status, StatusValidated)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'open'`, inv.ID); n != 1 {
		t.Fatalf("precondition: open approval_runs rows for invoice = %d, want exactly 1", n)
	}

	vs := []Violation{{RuleKey: "buyer-tin-required", Severity: "error", Message: "Buyer TIN is required"}}
	ruleSetVersionID2 := seedRuleSetVersionID(t, super)
	var demoted Invoice
	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var err error
		demoted, err = store.DemoteRevalidatedTx(ctx, tx, inv.ID, tenantID, vs, ruleSetVersionID2)
		return err
	})
	if err != nil {
		t.Fatalf("DemoteRevalidatedTx: %v (want nil)", err)
	}
	if demoted.Status != StatusDraft {
		t.Fatalf("DemoteRevalidatedTx: status = %q, want %q (demoted)", demoted.Status, StatusDraft)
	}

	// DemoteRevalidatedTx stamps violations/rule_set_version_id only -- none of the
	// ten MBS content columns -- so the invoice's content is unchanged and the SAME
	// pre-demotion fingerprint still matches the just-demoted row.
	ruleSetVersionID3 := seedRuleSetVersionID(t, super)
	revalidated, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID3, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (re-promote after DemoteRevalidatedTx): want success, got: %v "+
			"(want no 23505 on approval_runs_one_open -- DemoteRevalidatedTx must cancel the stale open run first)", err)
	}
	if revalidated.Status != StatusValidated {
		t.Errorf("ApplyValidation (re-promote): status = %q, want %q", revalidated.Status, StatusValidated)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'cancelled'`, inv.ID); n != 1 {
		t.Errorf("cancelled approval_runs rows for invoice = %d, want exactly 1", n)
	}
	var cancelledClosedBy string
	if err := super.QueryRow(ctx,
		`SELECT closed_by FROM approval_runs WHERE invoice_id = $1 AND state = 'cancelled'`, inv.ID,
	).Scan(&cancelledClosedBy); err != nil {
		t.Fatalf("read the cancelled run: %v", err)
	}
	if cancelledClosedBy != "revalidate-rule-set" {
		t.Errorf("cancelled run closed_by = %q, want %q (RevalidateActor's fixed literal)", cancelledClosedBy, "revalidate-rule-set")
	}

	var openFingerprint string
	if err := super.QueryRow(ctx,
		`SELECT content_fingerprint FROM approval_runs WHERE invoice_id = $1 AND state = 'open'`, inv.ID,
	).Scan(&openFingerprint); err != nil {
		t.Fatalf("read the fresh open run: %v", err)
	}
	if openFingerprint != fp {
		t.Errorf("fresh open run content_fingerprint = %q, want %q (unchanged by DemoteRevalidatedTx)", openFingerprint, fp)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, inv.ID); n != 2 {
		t.Errorf("approval_runs rows for invoice = %d, want exactly 2 (one cancelled, one open)", n)
	}
}
