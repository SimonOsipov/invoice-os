// M4-04-05 (task-112): tests for internal/invoice's Store.ApplyValidation --
// the same-tx TOCTOU-safe gate that stamps violations/rule_set_version_id and
// (on a clean result) promotes draft->validated via the transitionTx
// extraction -- written BEFORE the real implementation exists (RED against
// apply_validation_qa_scaffold.go's not-implemented stub: ApplyValidation
// currently always returns errApplyValidationNotImplemented, so every
// assertion below fails for that reason or a specific errors.Is/pgCode
// mismatch, not a compile error). Reuses the dbTestPools/seedTenant/
// seedEntity/mustCount/auditCount/auditActor harness from store_test.go (same
// package); contentFingerprint is payload.go's own unexported helper, called
// directly since this file lives in the same package.
//
// Spec-to-test map (Test Specs table, task-112 / M4-04-05):
//
//	GATE-01 TestApplyValidation_CleanEvaluationPromotesAndStampsVersion
//	GATE-02 TestApplyValidation_CleanEvaluationWritesBothAuditRows
//	GATE-03 TestApplyValidation_ErrorViolationStaysDraftNoHistoryRow
//	GATE-04 TestApplyValidation_WarningInfoOnlyPromotes
//	GATE-05 TestApplyValidation_MixedErrorWarningStaysDraftStoresBoth
//	GATE-09 TestApplyValidation_ValidatedInvoiceRefused
//	GATE-10 TestApplyValidation_QueuedInvoiceRefused
//	GATE-11 TestApplyValidation_StaleFingerprintRefused
//	GATE-12 TestApplyValidation_FreshFingerprintAfterUpdateSucceeds
//	GATE-13 TestApplyValidation_LongActorRollsBackWholeTx
//	GATE-14 TestApplyValidation_CrossTenantNotFound
//	GATE-15 TestApplyValidation_MalformedIDRejected
//	GATE-16 TestApplyValidation_UnseededRuleSetVersionIDRefused
//	GATE-17 TestApplyValidation_ConcurrentSerializesToOneWinner
//	GATE-18 TestApplyValidation_NilViolationsNormalizeToEmptyArrayNeverNull
//
// GATE-00 is the shipped internal/invoice transition suite
// (transition_test.go + transition_adversarial_test.go), untouched by this
// story -- it is task-112's regression gate for the transitionTx extraction,
// not a new spec authored here.
//
// Run: `make test-rls` (or `make test-audit`), or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- shared GATE-* harness additions ---------------------------------------

// seedRuleSetVersionID returns a real, existing rule_set_versions id -- any
// row satisfies the invoices.rule_set_version_id FK, so this deliberately
// does NOT filter by version number or is_active (RS-V2-14 scope: this
// package is not allow-listed for a literal `version = N` pin). Mirrors
// internal/platform/db/invoices_rls_test.go's INV-RLS-14
// (`SELECT id FROM rule_set_versions LIMIT 1`) verbatim.
func seedRuleSetVersionID(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM rule_set_versions LIMIT 1`,
	).Scan(&id); err != nil {
		t.Fatalf("look up a valid rule_set_versions id (is the M3-05/M4-04-01 seed applied?): %v", err)
	}
	return id
}

// invoiceGateSnapshot is the three columns ApplyValidation's write step
// touches (status, violations, rule_set_version_id) -- used by the
// error-path GATE specs to prove "nothing written" with a real before/after
// comparison rather than a single point-in-time read.
type invoiceGateSnapshot struct {
	status           string
	violationsText   string
	ruleSetVersionID *string
}

func snapshotInvoiceGateState(t *testing.T, super *pgxpool.Pool, id string) invoiceGateSnapshot {
	t.Helper()
	var s invoiceGateSnapshot
	if err := super.QueryRow(context.Background(),
		`SELECT status, violations::text, rule_set_version_id::text FROM invoices WHERE id = $1`, id,
	).Scan(&s.status, &s.violationsText, &s.ruleSetVersionID); err != nil {
		t.Fatalf("snapshot invoice %s: %v", id, err)
	}
	return s
}

func assertGateSnapshotUnchanged(t *testing.T, before, after invoiceGateSnapshot, label string) {
	t.Helper()
	if before.status != after.status {
		t.Errorf("%s: status changed %q -> %q, want unchanged", label, before.status, after.status)
	}
	if before.violationsText != after.violationsText {
		t.Errorf("%s: violations changed %q -> %q, want unchanged", label, before.violationsText, after.violationsText)
	}
	beforeV, afterV := "<nil>", "<nil>"
	if before.ruleSetVersionID != nil {
		beforeV = *before.ruleSetVersionID
	}
	if after.ruleSetVersionID != nil {
		afterV = *after.ruleSetVersionID
	}
	if beforeV != afterV {
		t.Errorf("%s: rule_set_version_id changed %q -> %q, want unchanged", label, beforeV, afterV)
	}
}

// readViolations decodes the invoices.violations jsonb column back into
// []Violation. Comparing the DECODED slice (not the raw ::text) is
// deliberate: jsonb does not preserve object key insertion order on
// round-trip, so a raw string comparison against json.Marshal's own field
// order would be a false negative waiting to happen. An empty array has no
// such ambiguity, so GATE-01/18 assert violations::text == "[]" directly
// instead (a stronger, more literal check for exactly that shape).
func readViolations(t *testing.T, super *pgxpool.Pool, id string) []Violation {
	t.Helper()
	var raw []byte
	if err := super.QueryRow(context.Background(),
		`SELECT violations FROM invoices WHERE id = $1`, id,
	).Scan(&raw); err != nil {
		t.Fatalf("read back violations for %s: %v", id, err)
	}
	var vs []Violation
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatalf("unmarshal stored violations for %s (%s): %v", id, raw, err)
	}
	return vs
}

// --- GATE-01/02: clean evaluation on a draft ------------------------------

// GATE-01: a clean evaluation ([]Violation{}) on a draft promotes to
// validated, stores violations as jsonb [] (never null), stamps
// rule_set_version_id, and writes exactly one draft->validated history row.
func TestApplyValidation_CleanEvaluationPromotesAndStampsVersion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-01 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-01 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-01"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	got, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (clean): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("ApplyValidation returned status = %q, want %q", got.Status, StatusValidated)
	}

	snap := snapshotInvoiceGateState(t, super, inv.ID)
	if snap.status != string(StatusValidated) {
		t.Errorf("invoices.status = %q, want %q", snap.status, StatusValidated)
	}
	if snap.violationsText != "[]" {
		t.Errorf("invoices.violations::text = %q, want %q (jsonb [], never null)", snap.violationsText, "[]")
	}
	if snap.ruleSetVersionID == nil || *snap.ruleSetVersionID != versionID {
		t.Errorf("invoices.rule_set_version_id = %v, want %q", snap.ruleSetVersionID, versionID)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory+1 {
		t.Errorf("invoice_status_history rows = %d, want %d (exactly one new row)", n, beforeHistory+1)
	}
	var fromStatus *string
	var toStatus string
	if err := super.QueryRow(ctx,
		`SELECT from_status, to_status FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`,
		inv.ID,
	).Scan(&fromStatus, &toStatus); err != nil {
		t.Fatalf("read newest history row: %v", err)
	}
	if fromStatus == nil || Status(*fromStatus) != StatusDraft {
		t.Errorf("newest history from_status = %v, want %q", fromStatus, StatusDraft)
	}
	if Status(toStatus) != StatusValidated {
		t.Errorf("newest history to_status = %q, want %q", toStatus, StatusValidated)
	}
}

// TestApplyValidation_ClearsKeepMarksOnPromotion (INVCR-01-15, D6, task-291, AC #7): a
// kept draft, fixed and re-evaluated clean, must promote to validated with all three
// kept_as_is_* columns NULL -- and, critically, with NO 23514. The
// invoices_kept_as_is_draft_only CHECK FORCES this: transitionTx's promoting UPDATE
// sets status='validated' in the SAME statement that must null the marks, or the
// write itself violates the CHECK it is trying to satisfy (kept_as_is_at set + status
// no longer draft). A forgotten clear is therefore a loud CI red here, never a stale
// KEPT badge surviving into validated.
func TestApplyValidation_ClearsKeepMarksOnPromotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "KEEP-GATE-CLEAR tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-GATE-CLEAR entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "KEEP-GATE-CLEAR"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The fingerprint is taken BEFORE the raw seed below -- contentFingerprint hashes
	// only header+lines (payload.go), never violations/kept_as_is_*, so seeding those
	// two afterward cannot stale it.
	fp := contentFingerprint(inv, inv.LineItems)

	// Force-seed a "kept, blocking" draft directly (bypassing Store.KeepAsIs, which
	// is kept_as_is_test.go's own concern) -- an operator kept this invoice earlier,
	// and it is now being re-validated clean (e.g. after an upstream fix).
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET violations = $1::jsonb, kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'was kept before the fix' WHERE id = $2`,
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`, inv.ID,
	); err != nil {
		t.Fatalf("seed kept-as-is triple: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)

	got, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (clean, promoting a previously-kept invoice): want success with NO 23514, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("ApplyValidation returned status = %q, want %q", got.Status, StatusValidated)
	}
	if got.KeptAsIsAt != nil || got.KeptAsIsBy != nil || got.KeptAsIsReason != nil {
		t.Errorf("ApplyValidation return kept_as_is triple = (at=%v by=%v reason=%v), want all nil (cleared on promotion)",
			got.KeptAsIsAt, got.KeptAsIsBy, got.KeptAsIsReason)
	}

	var at, by, reason *string
	if err := super.QueryRow(ctx,
		`SELECT kept_as_is_at::text, kept_as_is_by, kept_as_is_reason FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&at, &by, &reason); err != nil {
		t.Fatalf("read back kept_as_is triple: %v", err)
	}
	if at != nil || by != nil || reason != nil {
		t.Errorf("stored kept_as_is triple after promotion = (at=%v by=%v reason=%v), want all NULL", at, by, reason)
	}
}

// GATE-02: after a clean ApplyValidation, audit_log has BOTH
// invoice.validated and invoice.transitioned rows for the tenant, actor ==
// the caller's Subject on both -- the same-tx pairing Core AC #2 requires.
func TestApplyValidation_CleanEvaluationWritesBothAuditRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-02 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-02 entity")
	subject := memberSubject
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-02"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	beforeValidated := auditCount(t, app, tenantID, "invoice.validated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, fp); err != nil {
		t.Fatalf("ApplyValidation (clean): want success, got: %v", err)
	}

	if n := auditCount(t, app, tenantID, "invoice.validated"); n != beforeValidated+1 {
		t.Errorf("audit_log invoice.validated rows = %d, want %d (exactly one new row)", n, beforeValidated+1)
	}
	if a := auditActor(t, app, tenantID, "invoice.validated"); a != subject {
		t.Errorf("invoice.validated audit actor = %q, want %q", a, subject)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned+1 {
		t.Errorf("audit_log invoice.transitioned rows = %d, want %d (exactly one new row)", n, beforeTransitioned+1)
	}
	if a := auditActor(t, app, tenantID, "invoice.transitioned"); a != subject {
		t.Errorf("invoice.transitioned audit actor = %q, want %q", a, subject)
	}
}

// --- GATE-03/04/05: violation severities ------------------------------------

// GATE-03: a single severity:error violation keeps the invoice draft,
// stores the violation and stamps rule_set_version_id, writes NO history
// row -- and is still a normal (nil-error) return to the caller, since a
// validation FAILURE is a legitimate outcome, not a store error.
func TestApplyValidation_ErrorViolationStaysDraftNoHistoryRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-03 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-03 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-03"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	violations := []Violation{{RuleKey: "vat-standard-rate", Severity: "error", Message: "VAT rate mismatch"}}
	got, err := store.ApplyValidation(c, inv.ID, violations, versionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (one error violation): want success (a blocking verdict is still a normal call), got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("ApplyValidation returned status = %q, want unchanged %q (an error-severity violation blocks promotion)", got.Status, StatusDraft)
	}

	snap := snapshotInvoiceGateState(t, super, inv.ID)
	if snap.status != string(StatusDraft) {
		t.Errorf("invoices.status = %q, want unchanged %q", snap.status, StatusDraft)
	}
	if snap.ruleSetVersionID == nil || *snap.ruleSetVersionID != versionID {
		t.Errorf("invoices.rule_set_version_id = %v, want stamped %q (version is stamped even on a blocking result)", snap.ruleSetVersionID, versionID)
	}
	if got := readViolations(t, super, inv.ID); !reflect.DeepEqual(got, violations) {
		t.Errorf("stored violations = %+v, want %+v", got, violations)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d (no promotion, no new row)", n, beforeHistory)
	}
}

// GATE-04: warning/info-only violations promote to validated and are
// stored (a non-error severity never blocks).
func TestApplyValidation_WarningInfoOnlyPromotes(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-04 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-04 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-04"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	violations := []Violation{
		{RuleKey: "supplier-tin-format", Severity: "warning", Message: "TIN format looks unusual"},
		{RuleKey: "buyer-tin-format", Severity: "info", Message: "TIN not yet verified"},
	}
	got, err := store.ApplyValidation(c, inv.ID, violations, versionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (warning+info only): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("ApplyValidation returned status = %q, want %q (no error-severity violation)", got.Status, StatusValidated)
	}

	snap := snapshotInvoiceGateState(t, super, inv.ID)
	if snap.status != string(StatusValidated) {
		t.Errorf("invoices.status = %q, want %q", snap.status, StatusValidated)
	}
	if got := readViolations(t, super, inv.ID); !reflect.DeepEqual(got, violations) {
		t.Errorf("stored violations = %+v, want %+v", got, violations)
	}
}

// GATE-05: a mixed error+warning set stays draft AND stores BOTH violations
// -- collect-all is preserved end to end, not just at the evaluator.
func TestApplyValidation_MixedErrorWarningStaysDraftStoresBoth(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-05 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-05 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-05"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	violations := []Violation{
		{RuleKey: "vat-standard-rate", Severity: "error", Message: "VAT rate mismatch"},
		{RuleKey: "supplier-tin-format", Severity: "warning", Message: "TIN format looks unusual"},
	}
	got, err := store.ApplyValidation(c, inv.ID, violations, versionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (mixed error+warning): want success, got: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("ApplyValidation returned status = %q, want unchanged %q (one error-severity violation is enough to block)", got.Status, StatusDraft)
	}

	storedViolations := readViolations(t, super, inv.ID)
	if len(storedViolations) != 2 {
		t.Fatalf("stored violations count = %d, want 2 (collect-all: BOTH the error and the warning persist)", len(storedViolations))
	}
	if !reflect.DeepEqual(storedViolations, violations) {
		t.Errorf("stored violations = %+v, want %+v", storedViolations, violations)
	}
}

// --- GATE-09/10: scope is draft-only ----------------------------------------

// GATE-09: a validated invoice refuses ApplyValidation with ErrNotDraft;
// nothing changes.
func TestApplyValidation_ValidatedInvoiceRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-09 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-09 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-09"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)
	before := snapshotInvoiceGateState(t, super, inv.ID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, fp); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("ApplyValidation(validated invoice) err = %v, want ErrNotDraft", err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "GATE-09")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
}

// GATE-10: a queued invoice refuses ApplyValidation with ErrNotDraft.
func TestApplyValidation_QueuedInvoiceRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-10 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-10 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-10"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusQueued); err != nil {
		t.Fatalf("pre-hop Transition(-> queued): %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)
	before := snapshotInvoiceGateState(t, super, inv.ID)

	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, fp); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("ApplyValidation(queued invoice) err = %v, want ErrNotDraft", err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "GATE-10")
}

// TestApplyValidation_NonDraftStatusesRefusedTable (INVED-01-03/task-264,
// INV-03-T6, widened): an invoice seeded at each of the 6 non-draft statuses
// refuses Store.ApplyValidation with ErrNotDraft -- NEVER ErrStaleValidation,
// proving the status re-check (store.go:1038) fires BEFORE the fingerprint
// re-check (store.go:1052) even though the fingerprint passed in here is the
// invoice's TRUE, freshly-computed one (so a wrongly-ordered implementation
// that checked the fingerprint first would not have anything to blame the
// rejection on). Snapshot (status/violations/rule_set_version_id) and history
// are unchanged; no invoice.validated audit row is written. Overlaps GATE-09
// (validated) and GATE-10 (queued) above by design -- this table does not
// replace them, it adds the genuinely-uncovered submitted/accepted/rejected/
// failed cases.
func TestApplyValidation_NonDraftStatusesRefusedTable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	for _, status := range []Status{
		StatusValidated, StatusQueued, StatusSubmitted,
		StatusAccepted, StatusRejected, StatusFailed,
	} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			tenantID := seedTenant(t, super, "INV-03-T6 "+string(status)+" tenant")
			entityID := seedEntity(t, super, tenantID, "INV-03-T6 entity")
			c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "INV-03-T6-"+string(status), status)

			inv, err := store.Get(c, invID)
			if err != nil {
				t.Fatalf("Get (to compute the invoice's true fingerprint): %v", err)
			}
			versionID := seedRuleSetVersionID(t, super)
			fp := contentFingerprint(inv, inv.LineItems)

			before := snapshotInvoiceGateState(t, super, invID)
			beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
			beforeValidated := auditCount(t, app, tenantID, "invoice.validated")

			if _, err := store.ApplyValidation(c, invID, []Violation{}, versionID, fp); !errors.Is(err, ErrNotDraft) {
				t.Fatalf("ApplyValidation(%s invoice) err = %v, want ErrNotDraft (never ErrStaleValidation -- the status check must precede the fingerprint check)", status, err)
			}

			assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invID), "INV-03-T6 "+string(status))
			if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
				t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
			}
			if n := auditCount(t, app, tenantID, "invoice.validated"); n != beforeValidated {
				t.Errorf("audit_log invoice.validated rows = %d, want unchanged %d", n, beforeValidated)
			}
		})
	}
}

// --- GATE-11/12: TOCTOU content staleness -----------------------------------

// GATE-11: content edited (via Store.Update) between when the fingerprint
// was taken and when ApplyValidation runs -> ErrStaleValidation, with
// nothing written -- the status re-check alone would NOT catch this (status
// stays draft across an Update).
func TestApplyValidation_StaleFingerprintRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-11 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-11 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-11"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	staleFP := contentFingerprint(inv, inv.LineItems) // taken BEFORE the Update below

	newVAT := "42.00"
	if _, err := store.Update(c, inv.ID, UpdateInput{VAT: &newVAT}); err != nil {
		t.Fatalf("Update (content edit between fingerprint and apply): %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	before := snapshotInvoiceGateState(t, super, inv.ID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, staleFP); !errors.Is(err, ErrStaleValidation) {
		t.Fatalf("ApplyValidation(stale fingerprint) err = %v, want ErrStaleValidation", err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "GATE-11")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
}

// GATE-12: a fingerprint taken AFTER a prior content edit (i.e. genuinely
// current) is NOT stale -- ApplyValidation succeeds. This proves the
// staleness check does not false-positive merely because the invoice was
// edited at SOME point in its history; only a mismatch AT APPLY TIME
// matters.
func TestApplyValidation_FreshFingerprintAfterUpdateSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-12 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-12 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-12"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newVAT := "42.00"
	updated, err := store.Update(c, inv.ID, UpdateInput{VAT: &newVAT})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	freshFP := contentFingerprint(updated, updated.LineItems) // taken AFTER the edit -- current

	versionID := seedRuleSetVersionID(t, super)

	got, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, freshFP)
	if err != nil {
		t.Fatalf("ApplyValidation (fresh, non-stale fingerprint): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("ApplyValidation returned status = %q, want %q", got.Status, StatusValidated)
	}
}

// --- GATE-13: same-tx atomicity ---------------------------------------------

// GATE-13: an actor (Subject) 256 chars long passes invoice_status_history's
// char_length(actor)>0 CHECK but fails audit_log's char_length(actor)<=255
// CHECK (23514) -- the whole ApplyValidation transaction rolls back: status
// stays draft, violations stay [], rule_set_version_id stays NULL. Mirrors
// TestTransition_AtomicityRollsBackOnActorCheckFailure's 256-char sub-case
// (transition_test.go) and TestStoreCreate_AtomicityRollsBackOnLaterInTxFailure's
// (store_test.go).
func TestApplyValidation_LongActorRollsBackWholeTx(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-13 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-13 entity")
	cNormal := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(cNormal, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-13"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	longSubject := strings.Repeat("a", 256)
	cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: longSubject, Role: "authenticated", TenantID: tenantID})

	before := snapshotInvoiceGateState(t, super, inv.ID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeValidated := auditCount(t, app, tenantID, "invoice.validated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	_, err = store.ApplyValidation(cCrafted, inv.ID, []Violation{}, versionID, fp)
	if err == nil {
		t.Fatal("ApplyValidation with a 256-char actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("ApplyValidation with a 256-char actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "GATE-13")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d (the whole tx rolled back)", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.validated"); n != beforeValidated {
		t.Errorf("audit_log invoice.validated rows = %d, want unchanged %d", n, beforeValidated)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
		t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d", n, beforeTransitioned)
	}
}

// --- GATE-14/15/16: id/version validation -----------------------------------

// GATE-14: another tenant's invoice id resolves to ErrNotFound (RLS
// 0-rows); nothing written to that row.
func TestApplyValidation_CrossTenantNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantA := seedTenant(t, super, "GATE-14 tenant A")
	tenantB := seedTenant(t, super, "GATE-14 tenant B")
	entityB := seedEntity(t, super, tenantB, "GATE-14 B entity")
	invoiceB := seedInvoice(t, super, tenantB, entityB, "GATE-14-B")

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA})

	before := snapshotInvoiceGateState(t, super, invoiceB)

	// The fingerprint/version values are irrelevant here: RLS makes the row
	// invisible under tenant A's GUC, so the SELECT ... FOR UPDATE 0-rows
	// before either the status or the fingerprint check ever runs.
	if _, err := store.ApplyValidation(cA, invoiceB, []Violation{}, uuid.NewString(), "irrelevant"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ApplyValidation(tenant B's invoice) as tenant A err = %v, want ErrNotFound", err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, invoiceB), "GATE-14")
}

// GATE-15: a malformed (non-uuid) id raises 22P02, mapped to ErrValidation
// -- mirrors Get/Update/Create/Transition.
func TestApplyValidation_MalformedIDRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-15 tenant")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	if _, err := store.ApplyValidation(c, "not-a-uuid", []Violation{}, uuid.NewString(), "irrelevant"); !errors.Is(err, ErrValidation) {
		t.Fatalf("ApplyValidation(malformed id) err = %v, want ErrValidation (22P02 invalid_text_representation)", err)
	}
}

// GATE-16: a random, unseeded uuid as ruleSetVersionID is refused by the FK
// (23503) -- 04 cannot make 03 stamp a phantom rule-set version; nothing
// committed.
func TestApplyValidation_UnseededRuleSetVersionIDRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-16 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-16 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-16"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fp := contentFingerprint(inv, inv.LineItems)
	bogusVersionID := uuid.NewString()

	before := snapshotInvoiceGateState(t, super, inv.ID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	_, err = store.ApplyValidation(c, inv.ID, []Violation{}, bogusVersionID, fp)
	if err == nil {
		t.Fatal("ApplyValidation with an unseeded rule_set_version_id succeeded, want a foreign_key_violation (SQLSTATE 23503)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("ApplyValidation with an unseeded rule_set_version_id: pgCode = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "GATE-16")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
}

// --- GATE-17: concurrency ----------------------------------------------------

// GATE-17: N concurrent ApplyValidation calls on the SAME draft invoice --
// SELECT ... FOR UPDATE serializes them; exactly one wins (promotes to
// validated); every loser observes the now-validated row under READ
// COMMITTED and resolves to ErrNotDraft (NOT ErrStaleValidation: the
// winner's write touches only status/violations/rule_set_version_id, none
// of which are in the content fingerprint, so a loser's fingerprint still
// matches -- only the status re-check catches it. This is why the status
// check must run BEFORE the fingerprint check). Exactly one
// invoice_status_history row (to_status=validated) exists afterward. N=6,
// mirroring TestTransition_ConcurrentSameEdgeSerializesToOneWinner
// (transition_test.go, INV-SM-06).
func TestApplyValidation_ConcurrentSerializesToOneWinner(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-17 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-17 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-17"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = store.ApplyValidation(c, inv.ID, []Violation{}, versionID, fp)
		}(i)
	}
	wg.Wait()

	successes, notDrafts, other := 0, 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			successes++
		case errors.Is(e, ErrNotDraft):
			notDrafts++
		default:
			other++
			t.Errorf("concurrent ApplyValidation returned unexpected error: %v", e)
		}
	}
	if successes != 1 {
		t.Errorf("concurrent ApplyValidation successes = %d, want exactly 1", successes)
	}
	if notDrafts != n-1 {
		t.Errorf("concurrent ApplyValidation ErrNotDraft count = %d, want %d (losers observe validated after FOR UPDATE releases)", notDrafts, n-1)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusValidated {
		t.Errorf("status after concurrent ApplyValidation = %q, want %q", status, StatusValidated)
	}
	if hn := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'validated'`, inv.ID); hn != 1 {
		t.Errorf("invoice_status_history rows (to_status=validated) = %d, want exactly 1 (FOR UPDATE serialized the race)", hn)
	}
}

// --- GATE-18: the violations-write null bug ---------------------------------

// GATE-18 (AC#14, Stage-1 F2 / orchestrator-strengthened): a clean
// ApplyValidation passing a NIL []Violation (not []Violation{}) must store
// jsonb [] -- exactly, byte for byte -- never SQL NULL and never the JSON
// literal null.
//
// This is deliberately NOT a "no 23502" check. json.Marshal(nil
// []Violation) returns []byte("null") -- a NON-nil 4-byte slice holding the
// JSON scalar `null`. Binding that to `violations jsonb NOT NULL` SUCCEEDS
// (jsonb null is a valid JSON value, not SQL NULL), so no 23502 ever fires
// -- an implementation that binds json.Marshal(vs) directly, without first
// normalizing a nil vs to []Violation{}, ships violations='null'::jsonb and
// a "no 23502" assertion would report GREEN on exactly that bug. Asserting
// the STORED VALUE is what catches it.
func TestApplyValidation_NilViolationsNormalizeToEmptyArrayNeverNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GATE-18 tenant")
	entityID := seedEntity(t, super, tenantID, "GATE-18 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GATE-18"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	var nilViolations []Violation // deliberately nil, not []Violation{}

	got, err := store.ApplyValidation(c, inv.ID, nilViolations, versionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (nil violations slice): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("ApplyValidation returned status = %q, want %q (a nil/empty violation set has no error-severity entries)", got.Status, StatusValidated)
	}

	var violationsText string
	if err := super.QueryRow(ctx, `SELECT violations::text FROM invoices WHERE id = $1`, inv.ID).Scan(&violationsText); err != nil {
		t.Fatalf("read back violations: %v", err)
	}
	if violationsText != "[]" {
		t.Fatalf("invoices.violations::text = %q, want exactly %q -- 'null' is the SPECIFIC bug this gate exists to catch: "+
			"json.Marshal(nil []Violation) produces the JSON literal null (a NON-nil []byte), which binds successfully to "+
			"`jsonb NOT NULL` (no 23502 -- the column is NOT violated) and lands violations='null'::jsonb "+
			"[Stage-1 F2, AC#14, violations-write]", violationsText, "[]")
	}
}

// --- INVED-01-02: line hydration through the real gate (INV-02-T10/T11) ---

// TestApplyValidation_LinedDraftValidatesWithoutStaleFingerprint (INV-02-T10):
// a stored draft invoice with >=2 line items, validated end to end through
// the REAL gate (Store.Get -> Evaluate -> 04 in-process -> Store.
// ApplyValidation), must reach a real verdict -- never ErrStaleValidation.
// This is the "outage detector" for INVED-01-02's single highest-risk
// defect: if Store.ApplyValidation's locked-row fingerprint ever stopped
// hydrating its lines (missing, or reading off the pool instead of the tx --
// see hydrateLinesTx's own doc), EVERY validate call on a lined invoice
// would fail this exact spec with ErrStaleValidation, because Gate.
// Validate's evaluated fingerprint (over a Get-hydrated invoice) would no
// longer match the locked row's line-less one. Reuses gate_test.go's
// startInProcess04/gapiValidInvoiceInput/gapiS2SToken (same package).
func TestApplyValidation_LinedDraftValidatesWithoutStaleFingerprint(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "INV-02-T10 tenant")
	// seedEntityWithTIN, not seedEntity (INVCR-01-17, C7 fix): Store.Create
	// now derives supplier_tin/supplier_name from the entity, so the entity
	// must carry gapiValidInvoiceInput's own "12345678-0001"/"Acme Ltd" for
	// this fixture to still produce zero violations (a bare seedEntity has no
	// tin, which now overrides gapiValidInvoiceInput's SupplierTIN with nil
	// and fires supplier-tin-required).
	entityID := seedEntityWithTIN(t, super, tenantID, "Acme Ltd", "12345678-0001")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	// gapiValidInvoiceInput (gate_test.go) carries exactly 2 line items --
	// the ">=2 line items" this spec calls for -- and is independently
	// verified (PAY-18) to produce zero violations against the real v2 rule
	// set.
	inv, err := store.Create(c, gapiValidInvoiceInput(entityID, "INV-02-T10"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(inv.LineItems) < 2 {
		t.Fatalf("test fixture bug: Create returned %d line items, want >= 2", len(inv.LineItems))
	}

	srv := startInProcess04(t, app)
	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(store, validator)

	got, _, err := gate.Validate(c, inv.ID)
	if errors.Is(err, ErrStaleValidation) {
		t.Fatalf("Gate.Validate on a lined draft returned ErrStaleValidation -- ApplyValidation's locked-row "+
			"fingerprint must be taken over hydrated lines, or EVERY validate on a lined invoice fails this "+
			"way [INV-02-T10]: %v", err)
	}
	if err != nil {
		t.Fatalf("Gate.Validate: want a clean promotion, got err: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("status = %q, want %q -- gapiValidInvoiceInput is independently verified (PAY-18) to produce "+
			"zero violations against the real v2 rule set [INV-02-T10]", got.Status, StatusValidated)
	}
	var vs []Violation
	if err := json.Unmarshal(got.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", got.Violations, err)
	}
	if len(vs) != 0 {
		t.Errorf("violations = %+v, want none [INV-02-T10]", vs)
	}
	if got.RuleSetVersionID == nil {
		t.Error("rule_set_version_id not stamped -- want a real verdict, not a no-op [INV-02-T10]")
	}
}

// TestApplyValidation_LinedInvoiceStillTripsStaleGuard (INV-02-T11): the
// SAME TOCTOU guard TestApplyValidation_StaleFingerprintRefused (GATE-11)
// proves for a line-less invoice, replayed on one that HAS line items -- an
// edit committed between the pre-edit fingerprint and the ApplyValidation
// call must still be caught as ErrStaleValidation, with nothing written.
// Confirms hydrateLinesTx's tx-scoped read inside ApplyValidation does not
// weaken [toctou-staleness] now that lines are part of what "changed" means.
func TestApplyValidation_LinedInvoiceStillTripsStaleGuard(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "INV-02-T11 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-02-T11 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	descA, descB := "Widget", "Gadget"
	inv, err := store.Create(c, CreateInput{
		EntityID: entityID, InvoiceNumber: "INV-02-T11",
		LineItems: []LineItemInput{
			{Description: &descA, Quantity: strPtr("2"), UnitPrice: strPtr("100.00")},
			{Description: &descB, Quantity: strPtr("1"), UnitPrice: strPtr("50.00")},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(inv.LineItems) != 2 {
		t.Fatalf("test fixture bug: Create returned %d line items, want 2", len(inv.LineItems))
	}
	staleFP := contentFingerprint(inv, inv.LineItems) // taken BEFORE the Update below

	newVAT := "42.00"
	if _, err := store.Update(c, inv.ID, UpdateInput{VAT: &newVAT}); err != nil {
		t.Fatalf("Update (content edit between fingerprint and apply): %v", err)
	}

	versionID := seedRuleSetVersionID(t, super)
	before := snapshotInvoiceGateState(t, super, inv.ID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, staleFP); !errors.Is(err, ErrStaleValidation) {
		t.Fatalf("ApplyValidation(stale fingerprint, lined invoice) err = %v, want ErrStaleValidation [INV-02-T11]", err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "INV-02-T11")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d [INV-02-T11]", n, beforeHistory)
	}
}
