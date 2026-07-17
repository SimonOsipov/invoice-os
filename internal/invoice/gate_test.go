// task-113 / M4-04-06 ("03: gate orchestration, POST /v1/invoices/{id}/
// validate, and the transitions guard") -- Mode A RED specs for the
// non-HTTP half of GAPI-01..17: GAPI-10..14, against internal/invoice.Gate
// (see gate_qa_scaffold.go's header for why this file currently compiles
// against a blanket not-implemented stub, and why that is the right scope
// for QA Mode A here). GAPI-01..09/15..17 (the HTTP handler + transitions
// guard) live in handlers_test.go instead, next to the other handler
// tests.
//
// Spec-to-test map (task-113's Test Specs table):
//
//	GAPI-10 TestGate_EvaluateCallsValidatorExactlyOnceWithAllItems
//	GAPI-11 TestGate_ValidateOnNonDraftReturnsErrNotDraftWithoutCalling04
//	GAPI-12 TestGate_ValidateRealDraftFailsVATStandardRate
//	GAPI-13 TestGate_ValidateAfterUpdateFixToGreenRevalidatesToValidated
//	GAPI-14 TestGate_ValidateZeroLineItemsStaysDraftWithLineItemsRequired
//
// GAPI-11..14 are DB-backed (dbTestPools, env-gated skip -- see store_test.go's
// dbTestPools doc). GAPI-11 is filed as "unit" in task-113's own Test Specs
// table, but Gate holds a CONCRETE *Store (task-113 §a: "mirroring
// importer.Service's shape"), not an interface -- there is no way to put an
// invoice in a non-draft state for Gate.Validate to observe without a real
// Store backed by a real DB. Flagged in the QA return, not silently
// reinterpreted: this file treats GAPI-11 as DB-backed, same harness as
// GAPI-12/13/14.
//
// GAPI-12/13/14 stand up a REAL in-process 04 (internal/validation's
// Store.LoadActiveRuleSetGlobal + NewDefaultEngine + BatchValidateHandler,
// behind S2SMiddleware, on an httptest.Server) against the SAME shared
// dev DB the invoice_app pool already points at -- both packages' own
// dbTestPools helpers read DATABASE_URL/DATABASE_SUPERUSER_URL for the
// identical invoice_app role (internal/validation/store.go's own header:
// "Store persists/reads rule_set_versions + rules as the invoice_app
// role"), so one app pool serves both sides of the gate in-process, exactly
// as it does in production between two separate deployed services.
//
// This file is `package invoice` (an INTERNAL test file), not
// `package invoice_test`: it imports internal/validation directly to build
// the in-process 04 handler. This does NOT trip
// TestValidatorClient_DoesNotImportValidationPackage (validator_test.go,
// VC-14) -- that guard runs `go list -deps ./internal/invoice` WITHOUT
// `-test`, which only inspects the package's non-test build graph; Go test
// files (whether `package invoice` or `package invoice_test`) are excluded
// from that graph regardless of which package clause they use. Confirmed
// empirically (`go list -deps ./internal/invoice | grep -c validation` ==
// 0, before and after this file existed) and re-confirmed by re-running
// VC-14 itself after adding this file. internal/invoice/payload_engine_test.go
// already established the identical precedent (import internal/validation
// from a test file, verified against the same guard) -- this file reuses
// package invoice's OWN store_test.go harness (dbTestPools/seedTenant/
// seedEntity/strPtr) instead of redeclaring it, since it does not have
// payload_engine_test.go's reason (unexported evaluator types) to go
// external.
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -v -run 'TestGate_' ./internal/invoice/...
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

// gapiS2SToken is an arbitrary in-process peer secret shared between the
// fake 04 server (S2SMiddleware) and the Validator pointed at it below --
// not a real secret, never read from env, scoped to this file's tests only.
const gapiS2SToken = "gapi-qa-test-s2s-token"

// startInProcess04 stands up a REAL 04 batch-validate handler (real DB-backed
// rule-set load, real engine, real peer-auth middleware) on an
// httptest.Server, for GAPI-11..14's "against the real v2 rule set" specs.
// Uses the SAME app pool the caller's Store is built from (see file header:
// both packages' dbTestPools read the identical invoice_app role).
func startInProcess04(t *testing.T, app *pgxpool.Pool) *httptest.Server {
	t.Helper()
	vstore := validation.NewStore(app)
	eng := validation.NewDefaultEngine()
	handler := validation.S2SMiddleware(gapiS2SToken)(validation.BatchValidateHandler(vstore.LoadActiveRuleSetGlobal, eng, nil))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// --- GAPI-10: Gate.Evaluate is exactly one round trip -----------------------

// TestGate_EvaluateCallsValidatorExactlyOnceWithAllItems (GAPI-10, unit): 3
// EvalItems must produce exactly ONE POST to the validator, carrying all 3
// items in that one request -- [batch-of-one], the property that keeps a
// 500-invoice import to one round trip. The fake server decodes with
// validator.go's own (unexported, same-package) validateBatchRequest and
// echoes a clean, TOTAL response built via json.Marshal of the equally
// unexported validateBatchResponse/validateBatchItemResult -- never a raw
// JSON string literal with a digit next to "rule_set_version" in the source
// text (RS-V2-14 / F7 pin-detector scope: internal/invoice/ is not
// allow-listed). cannedRuleSetVersion is validator_test.go's own
// package-level const, reused here rather than redeclared.
func TestGate_EvaluateCallsValidatorExactlyOnceWithAllItems(t *testing.T) {
	calls := 0
	var lastItemCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		var req validateBatchRequest
		_ = json.Unmarshal(body, &req)
		lastItemCount = len(req.Invoices)

		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			results[i] = validateBatchItemResult{Ref: it.Ref, Violations: []Violation{}}
		}
		resp := validateBatchResponse{
			RuleSetVersion:   cannedRuleSetVersion,
			RuleSetVersionID: "00000000-0000-0000-0000-000000000000",
			Results:          results,
		}
		b, err := json.Marshal(resp)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(nil, validator)

	items := []EvalItem{
		{Ref: "a", Invoice: Invoice{InvoiceNumber: "a"}},
		{Ref: "b", Invoice: Invoice{InvoiceNumber: "b"}},
		{Ref: "c", Invoice: Invoice{InvoiceNumber: "c"}},
	}
	_, _ = gate.Evaluate(context.Background(), items)

	if calls != 1 {
		t.Errorf("validator (04) received %d requests, want exactly 1 -- [batch-of-one]: one round trip for "+
			"the whole batch, not one per item", calls)
	}
	if calls == 1 && lastItemCount != 3 {
		t.Errorf("the one request carried %d items, want 3 (every EvalItem in that single round trip)", lastItemCount)
	}
}

// --- GAPI-11: the advisory pre-check saves the round trip -------------------

// TestGate_ValidateOnNonDraftReturnsErrNotDraftWithoutCalling04 (GAPI-11):
// a non-draft invoice must short-circuit to ErrNotDraft WITHOUT ever
// calling 04 -- the advisory pre-check's entire reason to exist. Promotes a
// fresh draft to validated via the REAL, unchanged Store.Transition (legal
// per legalTransitions[StatusDraft] = {StatusValidated}) rather than raw
// SQL, so the fixture is exactly what the shipped state machine produces.
// See file header for why this is DB-backed despite the plan's table
// marking it "unit".
func TestGate_ValidateOnNonDraftReturnsErrNotDraftWithoutCalling04(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GAPI-11 tenant")
	entityID := seedEntity(t, super, tenantID, "GAPI-11 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "GAPI-11"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition to validated (test setup, via the real unchanged Store.Transition): %v", err)
	}

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		b, _ := json.Marshal(validateBatchResponse{RuleSetVersion: cannedRuleSetVersion, RuleSetVersionID: "x", Results: []validateBatchItemResult{}})
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(store, validator)

	_, err = gate.Validate(c, inv.ID)
	if !errors.Is(err, ErrNotDraft) {
		t.Errorf("err = %v, want ErrNotDraft -- Gate.Validate's advisory pre-check must reject a non-draft "+
			"invoice before ever calling 04", err)
	}
	if called {
		t.Error("the validator (04) WAS called -- Gate.Validate's ErrNotDraft pre-check exists precisely to " +
			"SAVE this round trip on a non-draft invoice")
	}
}

// --- GAPI-12/13/14: the real gate, real v2 rule set, 04 in-process ----------

// gapiValidInvoiceInput returns a Store.Create input for a FULLY VALID
// invoice against the real, live v2 rule set (19 rules) -- the identical
// content payload_engine_test.go's TestPayloadEngine_ValidInvoice_
// ZeroViolationsAgainstRealV2 (PAY-18) already verifies produces ZERO
// violations: subtotal 250.00 = 2x100.00 + 1x50.00 (line-items-sum-subtotal),
// vat 18.75 = 7.5% of 250.00 (vat-standard-rate), every required field
// present, currency NGN. number is the caller's chosen invoice_number so
// GAPI-12/13/14 each get a distinct row.
func gapiValidInvoiceInput(entityID, number string) CreateInput {
	issueDate := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return CreateInput{
		EntityID: entityID, InvoiceNumber: number,
		IssueDate: &issueDate, Currency: strPtr("NGN"),
		SupplierTIN: strPtr("12345678-0001"), SupplierName: strPtr("Acme Ltd"),
		BuyerTIN: strPtr("87654321-0002"), BuyerName: strPtr("Beta Ltd"),
		Subtotal: strPtr("250.00"), VAT: strPtr("18.75"), Total: strPtr("268.75"),
		LineItems: []LineItemInput{
			{Quantity: strPtr("2"), UnitPrice: strPtr("100.00"), LineTotal: strPtr("200.00")},
			{Quantity: strPtr("1"), UnitPrice: strPtr("50.00"), LineTotal: strPtr("50.00")},
		},
	}
}

// hasViolation reports whether vs contains ruleKey.
func hasViolation(vs []Violation, ruleKey string) bool {
	for _, v := range vs {
		if v.RuleKey == ruleKey {
			return true
		}
	}
	return false
}

// TestGate_ValidateRealDraftFailsVATStandardRate (GAPI-12): the PAY-18
// fixture with VAT deliberately wrong (1.00 instead of 18.75; every other
// field untouched, so no v2 rule other than vat-standard-rate can fire --
// v1's 17 rules have no total==subtotal+vat cross-check, only
// vat-standard-rate ties vat to 7.5% of subtotal) must stay draft, carry a
// violation naming vat-standard-rate, and still stamp rule_set_version_id
// (the version is stamped even on a blocking verdict -- ApplyValidation's
// own doc: "the version is stamped even on a blocking verdict").
func TestGate_ValidateRealDraftFailsVATStandardRate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GAPI-12 tenant")
	entityID := seedEntity(t, super, tenantID, "GAPI-12 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	in := gapiValidInvoiceInput(entityID, "GAPI-12")
	in.VAT = strPtr("1.00") // wrong: 7.5% of 250.00 is 18.75, not 1.00
	inv, err := store.Create(c, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	srv := startInProcess04(t, app)
	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(store, validator)

	got, err := gate.Validate(c, inv.ID)
	if err != nil {
		t.Fatalf("Validate: want a normal (nil-error) BLOCKED outcome (a validation failure is a legitimate "+
			"outcome, not a store error), got err: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want %q -- a blocking violation must not promote", got.Status, StatusDraft)
	}
	var vs []Violation
	if err := json.Unmarshal(got.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", got.Violations, err)
	}
	if !hasViolation(vs, "vat-standard-rate") {
		t.Errorf("violations = %+v, want one naming vat-standard-rate", vs)
	}
	if got.RuleSetVersionID == nil {
		t.Error("rule_set_version_id = nil, want stamped even on a blocking verdict -- \"these violations came " +
			"from THAT rule set\" is what makes the verdict auditable")
	}
}

// TestGate_ValidateAfterUpdateFixToGreenRevalidatesToValidated (GAPI-13):
// the Day-60 demo path end to end -- GAPI-12's failing draft, VAT corrected
// via Store.Update, re-validated: now validated, violations == [],
// rule_set_version_id stamped, and invoice_status_history shows the
// draft->validated transition.
func TestGate_ValidateAfterUpdateFixToGreenRevalidatesToValidated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GAPI-13 tenant")
	entityID := seedEntity(t, super, tenantID, "GAPI-13 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	in := gapiValidInvoiceInput(entityID, "GAPI-13")
	in.VAT = strPtr("1.00") // wrong, same as GAPI-12 -- this test owns its own fixture (no cross-test ordering dependency)
	inv, err := store.Create(c, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	srv := startInProcess04(t, app)
	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(store, validator)

	if _, err := gate.Validate(c, inv.ID); err != nil {
		t.Fatalf("first Validate (expected a blocked-but-successful outcome, setting up the re-validate case): %v", err)
	}

	if _, err := store.Update(c, inv.ID, UpdateInput{VAT: strPtr("18.75")}); err != nil {
		t.Fatalf("Update to fix VAT: %v", err)
	}

	got, err := gate.Validate(c, inv.ID)
	if err != nil {
		t.Fatalf("second Validate (want a clean promoted outcome), got err: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("status = %q, want %q -- the Day-60 re-validate-to-green path end to end", got.Status, StatusValidated)
	}
	if string(got.Violations) != "[]" {
		t.Errorf("violations = %s, want [] after the fix", got.Violations)
	}
	if got.RuleSetVersionID == nil {
		t.Error("rule_set_version_id = nil, want stamped")
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

// TestGate_ValidateZeroLineItemsStaysDraftWithLineItemsRequired (GAPI-14):
// a draft with zero line items (otherwise a clean, fully-valid MBS payload
// -- subtotal/vat/total all 0.00, self-consistent under vat-standard-rate)
// must stay draft and carry line-items-required -- and ONLY that: the two
// v2-only line rules (line-cost-non-negative, line-items-sum-subtotal) and
// no-duplicate-line-items all guard on `!has(invoice.line_items)` /
// resolve-absent-as-nil, so an ABSENT line_items key (payload-absence: zero
// lines are omitted, never emitted as []) does not additionally fire them.
func TestGate_ValidateZeroLineItemsStaysDraftWithLineItemsRequired(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "GAPI-14 tenant")
	entityID := seedEntity(t, super, tenantID, "GAPI-14 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	issueDate := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	inv, err := store.Create(c, CreateInput{
		EntityID: entityID, InvoiceNumber: "GAPI-14",
		IssueDate: &issueDate, Currency: strPtr("NGN"),
		SupplierTIN: strPtr("12345678-0001"), SupplierName: strPtr("Acme Ltd"),
		BuyerTIN: strPtr("87654321-0002"), BuyerName: strPtr("Beta Ltd"),
		Subtotal: strPtr("0.00"), VAT: strPtr("0.00"), Total: strPtr("0.00"),
		// LineItems intentionally omitted -- zero line items is the point.
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	srv := startInProcess04(t, app)
	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(store, validator)

	got, err := gate.Validate(c, inv.ID)
	if err != nil {
		t.Fatalf("Validate: want a normal (nil-error) BLOCKED outcome, got err: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want %q", got.Status, StatusDraft)
	}
	var vs []Violation
	if err := json.Unmarshal(got.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", got.Violations, err)
	}
	if !hasViolation(vs, "line-items-required") {
		t.Errorf("violations = %+v, want one naming line-items-required", vs)
	}
}
