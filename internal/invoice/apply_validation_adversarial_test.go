// M4-04-05 (task-112): QA Mode B adversarial coverage ON TOP OF GATE-01..18
// (apply_validation_test.go), written during the post-implementation verify
// pass. Reuses the dbTestPools/seedTenant/seedEntity/seedRuleSetVersionID/
// contentFingerprint/snapshotInvoiceGateState/auditPayload harness from
// store_test.go / apply_validation_test.go / adversarial_test.go (same
// package). apply_validation_test.go itself is NOT modified.
//
// These close three gaps GATE-01..18 leave open:
//
//  1. The "outcome" audit vocabulary ("promoted"/"blocked") that
//     Store.ApplyValidation stamps into invoice.validated's payload is
//     asserted by ZERO of the shipped GATE tests -- M4-07's dashboard will
//     consume this field, so an unpinned vocabulary drifts silently.
//  2. GATE-13 exercises actor-CHECK atomicity only on the PROMOTING (clean)
//     path, where the failure is caught by transitionTx's OWN
//     "invoice.transitioned" audit write (step 5) -- confirmed live: moving
//     ApplyValidation's OWN "invoice.validated" audit write (step 6) into a
//     separate transaction still left GATE-13 green, because step 5's write
//     independently poisons the same tx first. The BLOCKED (non-promoting)
//     path has no step-5 write at all, so it is the only scenario that
//     isolates step 6's own atomicity -- and nothing in GATE-01..18 exercises
//     it.
//  3. Steps 2 (status re-check) and 3 (fingerprint re-check) are ordered
//     status-before-fingerprint, and the Stage-1 architecture validation
//     calls that ordering load-bearing -- but GATE-17 (the only concurrency
//     spec) cannot discriminate it: confirmed live by running GATE-17 against
//     a swapped order and observing it stay green (no content edit occurs in
//     that scenario, so the fingerprint matches for every loser regardless of
//     which check runs first). A scenario where the invoice is BOTH promoted
//     out of draft AND content-edited is required to tell the orderings
//     apart, and none of GATE-01..18 constructs one.
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// applyValidationOutcomePayload is the subset of invoice.validated's payload
// this file asserts on.
type applyValidationOutcomePayload struct {
	Outcome        string `json:"outcome"`
	ViolationCount int    `json:"violation_count"`
}

// --- outcome vocabulary: "promoted" / "blocked" -----------------------------

// A clean evaluation promotes, and invoice.validated's payload must carry
// outcome == "promoted" (not "validated", which collides with the Status
// value of the same name -- the reasoning task-112's Implementation Notes
// record for choosing this vocabulary).
func TestApplyValidation_AuditOutcomePromotedOnCleanEvaluation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-OUTCOME-01 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-OUTCOME-01 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ADV-OUTCOME-01"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv)

	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, versionID, fp); err != nil {
		t.Fatalf("ApplyValidation (clean): want success, got: %v", err)
	}

	raw := auditPayload(t, app, tenantID, "invoice.validated")
	var got applyValidationOutcomePayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal invoice.validated payload %s: %v", raw, err)
	}
	if got.Outcome != "promoted" {
		t.Errorf("invoice.validated payload outcome = %q, want %q (raw payload: %s)", got.Outcome, "promoted", raw)
	}
	if got.ViolationCount != 0 {
		t.Errorf("invoice.validated payload violation_count = %d, want 0", got.ViolationCount)
	}
}

// A blocking (severity:error) violation stays draft, and invoice.validated's
// payload must carry outcome == "blocked" -- disjoint from both Status values
// ("validated") and from the violation-count axis (asserted separately
// below: a warning-only invoice is "promoted" WITH violations, so "blocked"
// cannot be inferred merely from violation_count > 0).
func TestApplyValidation_AuditOutcomeBlockedOnErrorViolation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-OUTCOME-02 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-OUTCOME-02 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ADV-OUTCOME-02"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv)

	violations := []Violation{{RuleKey: "vat-standard-rate", Severity: "error", Message: "VAT rate mismatch"}}
	if _, err := store.ApplyValidation(c, inv.ID, violations, versionID, fp); err != nil {
		t.Fatalf("ApplyValidation (one error violation): want success, got: %v", err)
	}

	raw := auditPayload(t, app, tenantID, "invoice.validated")
	var got applyValidationOutcomePayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal invoice.validated payload %s: %v", raw, err)
	}
	if got.Outcome != "blocked" {
		t.Errorf("invoice.validated payload outcome = %q, want %q (raw payload: %s)", got.Outcome, "blocked", raw)
	}
	if got.ViolationCount != 1 {
		t.Errorf("invoice.validated payload violation_count = %d, want 1", got.ViolationCount)
	}
}

// The pinned case the story's own reasoning calls out explicitly: a
// warning-only set PROMOTES (no error severity blocks it) while still
// carrying violations -- so outcome must be "promoted" even though
// violation_count > 0. This is what proves outcome is on the gate-verdict
// axis, deliberately NOT the violation-count axis.
func TestApplyValidation_AuditOutcomePromotedWithWarningOnlyViolations(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-OUTCOME-03 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-OUTCOME-03 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ADV-OUTCOME-03"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv)

	violations := []Violation{
		{RuleKey: "supplier-tin-format", Severity: "warning", Message: "TIN format looks unusual"},
		{RuleKey: "buyer-tin-format", Severity: "info", Message: "TIN not yet verified"},
	}
	got, err := store.ApplyValidation(c, inv.ID, violations, versionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (warning+info only): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Fatalf("ApplyValidation returned status = %q, want %q (precondition: this case must promote)", got.Status, StatusValidated)
	}

	raw := auditPayload(t, app, tenantID, "invoice.validated")
	var payload applyValidationOutcomePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal invoice.validated payload %s: %v", raw, err)
	}
	if payload.Outcome != "promoted" {
		t.Errorf("invoice.validated payload outcome = %q, want %q -- a warning/info-only invoice PROMOTES despite carrying violations (raw payload: %s)", payload.Outcome, "promoted", raw)
	}
	if payload.ViolationCount != 2 {
		t.Errorf("invoice.validated payload violation_count = %d, want 2 (outcome must not be inferable from this field)", payload.ViolationCount)
	}
}

// --- GATE-13 variant: atomicity on the BLOCKED (non-promoting) path --------

// GATE-13's shipped scenario uses a CLEAN (promoting) evaluation, so the
// actor-CHECK failure it exercises is caught by transitionTx's OWN
// "invoice.transitioned" audit write (step 5), which runs inside the SAME
// tx ahead of ApplyValidation's own "invoice.validated" write (step 6).
// Verified live during this QA pass: moving step 6's audit write into a
// SEPARATE db.WithinRequestTenantTx call left GATE-13 green regardless,
// because step 5's write already poisons the shared connection first for
// the clean/promoting case.
//
// The BLOCKED path has no step 5 (no promotion, no transitionTx call, no
// "invoice.transitioned" write) -- so it is the only scenario that isolates
// step 6's OWN atomicity. Confirmed live: the same "audit write in a
// separate tx" mutation that GATE-13 could not catch DOES leak the
// violations/rule_set_version_id stamp on this path (status stays draft
// either way, since there is no promotion to roll back, but the stamp
// written in step 4 survives the step-6 failure when step 6 is not on the
// same tx) -- exactly the kind of false/stale verdict this subtask must
// never allow to stick.
func TestApplyValidation_BlockedPathLongActorRollsBackWholeTx(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-GATE13B tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-GATE13B entity")
	cNormal := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(cNormal, CreateInput{EntityID: entityID, InvoiceNumber: "ADV-GATE13B"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	versionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv)

	longSubject := strings.Repeat("a", 256)
	cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: longSubject, Role: "authenticated", TenantID: tenantID})

	before := snapshotInvoiceGateState(t, super, inv.ID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeValidated := auditCount(t, app, tenantID, "invoice.validated")

	// A BLOCKING violation -- so ApplyValidation never reaches step 5
	// (transitionTx / the "invoice.transitioned" write). Only step 4 (the
	// violations/rule_set_version_id UPDATE) and step 6 (this call's OWN
	// "invoice.validated" audit write) are in play.
	violations := []Violation{{RuleKey: "vat-standard-rate", Severity: "error", Message: "blocking"}}
	_, err = store.ApplyValidation(cCrafted, inv.ID, violations, versionID, fp)
	if err == nil {
		t.Fatal("ApplyValidation (blocked path) with a 256-char actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("ApplyValidation (blocked path) with a 256-char actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "ADV-GATE13B")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.validated"); n != beforeValidated {
		t.Errorf("audit_log invoice.validated rows = %d, want unchanged %d", n, beforeValidated)
	}
}

// --- status-vs-fingerprint ordering discriminator ---------------------------

// GATE-17 (the only concurrency spec) cannot discriminate the status-before-
// fingerprint ordering: confirmed live during this QA pass by running it
// against a swapped check order and observing it stay green -- no content
// edit occurs in that race, so every loser's fingerprint still matches
// regardless of which check runs first.
//
// This constructs the scenario that DOES discriminate it: an invoice that is
// BOTH promoted out of draft (Transition -> validated) AND content-edited
// (Store.Update) after the caller's fingerprint was taken, so a stale
// evaluatedFingerprint is presented against a row for which BOTH the status
// re-check and the fingerprint re-check would independently fire.
// Status-before-fingerprint (as shipped) must yield ErrNotDraft; confirmed
// live that swapping the order flips this test's result to
// ErrStaleValidation, proving the assertion below is a real discriminator
// and not a vacuous pass.
func TestApplyValidation_StatusCheckPrecedesFingerprintCheckWhenBothStale(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "ADV-ORDER tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-ORDER entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ADV-ORDER"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	staleFP := contentFingerprint(inv) // taken BEFORE both the promotion and the edit below
	versionID := seedRuleSetVersionID(t, super)

	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition -> validated: %v", err)
	}
	newVAT := "42.00"
	if _, err := store.Update(c, inv.ID, UpdateInput{VAT: &newVAT}); err != nil {
		t.Fatalf("Update (content edit after promotion): %v", err)
	}

	_, err = store.ApplyValidation(c, inv.ID, []Violation{}, versionID, staleFP)
	if !errors.Is(err, ErrNotDraft) {
		t.Fatalf("ApplyValidation(promoted AND edited, stale fingerprint) err = %v, want ErrNotDraft "+
			"(status-check must win over fingerprint-check when both apply)", err)
	}
	if errors.Is(err, ErrStaleValidation) {
		t.Fatal("err wraps BOTH ErrNotDraft and ErrStaleValidation -- the two sentinels must be mutually exclusive per call")
	}
}
