// M4-04-06 (task-113), QA Mode B adversarial coverage ON TOP OF GAPI-01..17
// (gate_test.go / handlers_test.go), written during the post-implementation
// verify pass. Neither gate_test.go nor handlers_test.go is modified.
//
// Four gaps GAPI-01..17 leave open, all centered on the executor's own
// deviation beyond the plan -- Gate.Evaluate's empty-batch short-circuit
// (gate.go:102-105) -- and the hand-off gap the executor flagged for
// task-114 (no exported severity predicate for its dry-run):
//
//  1. TestGate_EvaluateEmptyBatchNoHTTPCallZeroValueResult /
//     TestGate_ValidateBatchEmptyInvoicesNoHTTPNoStoreCallZeroValueOutcome --
//     the short-circuit itself. No GAPI spec covers it and the executor
//     correctly authored no test for it under Stage 3's scope ("QA Mode B
//     authors adversarial/edge coverage, not the executor"). Both tests use
//     a NIL store: if the empty-batch path ever reached Store.ApplyValidation,
//     the call would nil-pointer-panic, which is a stronger guarantee than a
//     spy field that could itself have a bug.
//  2. TestGate_RealValidationService400sEmptyBatchMapsToErrUpstream -- the
//     chain the short-circuit's own doc comment claims ("04 answers a 400 to
//     an empty batch, which validator.go correctly maps to ErrUpstream") but
//     that no existing test exercises against the REAL 04 handler (only a
//     stub, in TestValidatorClient_400MapsToErrUpstream). Deliberately
//     bypasses Gate.Evaluate (which would itself short-circuit and never
//     make the call) to hit validator.Validate directly.
//  3. TestGate_ValidateBatchCallsValidatorExactlyOnceForMultipleInvoices --
//     GAPI-10 pins "one round trip" at the Gate.Evaluate layer with its own
//     counting fake; this is an INDEPENDENT counting fake at the
//     ValidateBatch layer (the one task-114's real import path actually
//     calls), the concrete mechanism behind the <60s import gate's whole
//     perf argument.
//  4. TestGate_ValidateBatchWarningOnlyInvoicePromotesAndCountsAsCleanNotWithViolations --
//     hasBlockingViolation, not len(violations) == 0, is what BatchOutcome's
//     Clean/WithViolations split is built on (gate.go's own doc: the naive
//     guess is WRONG). GATE-04 (apply_validation_test.go) pins the underlying
//     promotion semantic at the Store.ApplyValidation layer; nothing pins the
//     COUNT this story's own BatchOutcome derives from it -- the layer
//     task-114's IMPV-06 (dry-run must match the real run) actually depends
//     on. Pinning it here means the semantic cannot silently drift out from
//     under a consumer that does not exist yet.
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- 1. the empty-batch short-circuit --------------------------------------

// TestGate_EvaluateEmptyBatchNoHTTPCallZeroValueResult: Evaluate(ctx, nil)
// must NOT call the validator (04 would 400 an empty batch, which is exactly
// the outage-shaped error this short-circuit exists to avoid), must return a
// non-nil empty ByRef (a nil map is indistinguishable from "no entry" to a
// caller ranging over it -- the same absence-as-verdict hazard
// Validator.Validate's own totality check exists to prevent), and must
// return the ZERO value for RuleSetVersion/RuleSetVersionID -- nothing was
// evaluated, so there is no real version to report.
func TestGate_EvaluateEmptyBatchNoHTTPCallZeroValueResult(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(nil, validator) // nil store: Evaluate must never touch it either way

	res, err := gate.Evaluate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Evaluate(nil): want nil error, got %v", err)
	}
	if called {
		t.Error("the validator (04) WAS called for an empty batch -- the short-circuit exists precisely to avoid " +
			"a round trip 04 would answer with a 400")
	}
	if res.ByRef == nil {
		t.Error("ByRef = nil, want a non-nil empty map -- a nil map is indistinguishable from \"no violations for " +
			"this ref\" to a caller ranging over sent refs, the same hazard Validate's totality check guards " +
			"against on the non-empty path")
	}
	if len(res.ByRef) != 0 {
		t.Errorf("ByRef = %v, want empty", res.ByRef)
	}
	if res.RuleSetVersion != 0 || res.RuleSetVersionID != "" {
		t.Errorf("RuleSetVersion=%d RuleSetVersionID=%q, want the zero value -- nothing was evaluated, so there is "+
			"no real version to report", res.RuleSetVersion, res.RuleSetVersionID)
	}
}

// TestGate_ValidateBatchEmptyInvoicesNoHTTPNoStoreCallZeroValueOutcome: the
// reachable production scenario the short-circuit's own doc comment names --
// an import whose every row is quarantined creates zero invoices and hands
// ValidateBatch an empty slice. Must not call 04, must not touch the store
// (nil store: a real call would nil-pointer-panic), and -- the cardinal-sin
// check -- the short-circuit's zero-value RuleSetVersion/RuleSetVersionID
// must never appear keyed to an invoice id. ByID stays empty because the
// per-invoice loop never runs on an empty invs slice; this pins that
// invariant so a future refactor of ValidateBatch cannot silently start
// stamping BatchOutcome's zero-value version onto a synthesized entry.
func TestGate_ValidateBatchEmptyInvoicesNoHTTPNoStoreCallZeroValueOutcome(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	validator := NewValidator(srv.URL, gapiS2SToken, nil)
	gate := NewGate(nil, validator) // nil store: ApplyValidation must never run

	out, err := gate.ValidateBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("ValidateBatch(nil): want nil error (a quarantine-everything import has nothing to validate, "+
			"not an operational failure), got %v", err)
	}
	if called {
		t.Error("the validator (04) WAS called for an empty invs slice")
	}
	if out.Clean != 0 || out.WithViolations != 0 {
		t.Errorf("Clean=%d WithViolations=%d, want 0/0", out.Clean, out.WithViolations)
	}
	if len(out.ByID) != 0 {
		t.Errorf("ByID = %v, want empty -- no invoice id may carry the short-circuit's zero-value verdict", out.ByID)
	}
	if out.RuleSetVersion != 0 || out.RuleSetVersionID != "" {
		t.Errorf("RuleSetVersion=%d RuleSetVersionID=%q, want the zero value at the BATCH level -- this is only "+
			"safe because it never reaches ByID/an invoice; a future caller (task-114) that reports THIS field "+
			"verbatim as the import report's rule_set_version MUST special-case 0/\"\" as null, per "+
			"[import-report-shape] (\"rule_set_version is null when nothing was evaluated\") -- reporting the raw "+
			"zero value would be a false version stamp", out.RuleSetVersion, out.RuleSetVersionID)
	}
}

// --- 2. the real 04 really 400s an empty batch ------------------------------

// TestGate_RealValidationService400sEmptyBatchMapsToErrUpstream: verifies,
// against the REAL in-process 04 handler (not a stub), the chain
// Evaluate's short-circuit exists to avoid triggering: a genuinely empty
// batch gets a 400 from 04, and validator.go's closed status switch maps
// that 400 to ErrUpstream like any other non-200/503 status. Deliberately
// calls validator.Validate directly rather than through Gate.Evaluate, since
// Evaluate's own short-circuit would prevent the call from ever reaching 04.
func TestGate_RealValidationService400sEmptyBatchMapsToErrUpstream(t *testing.T) {
	_, app := dbTestPools(t)
	srv := startInProcess04(t, app)
	validator := NewValidator(srv.URL, gapiS2SToken, nil)

	_, err := validator.Validate(context.Background(), []ValidateItem{})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- confirms the real 04 (internal/validation's "+
			"BatchValidateHandler) 400s a genuinely empty batch, and validator.go maps that 400 to ErrUpstream "+
			"the same as any other non-200/503 status; this is the exact outage Evaluate's empty-batch "+
			"short-circuit exists to avoid manufacturing on a quarantine-everything import", err)
	}
}

// --- 3. one 04 call per ValidateBatch, independent counting fake -----------

// TestGate_ValidateBatchCallsValidatorExactlyOnceForMultipleInvoices: an
// INDEPENDENT counting fake (not a re-read of GAPI-10's own assertion) at
// the layer task-114's real import path actually calls -- ValidateBatch,
// not Gate.Evaluate directly. This is the concrete mechanism behind the
// <60s import gate's whole perf argument: a 500-invoice import must cost
// ONE HTTP round trip to 04, not 500.
func TestGate_ValidateBatchCallsValidatorExactlyOnceForMultipleInvoices(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	ruleSetVersionID := seedRuleSetVersionID(t, super)

	tenantID := seedTenant(t, super, "gate-qa one-call tenant")
	entityID := seedEntity(t, super, tenantID, "gate-qa one-call entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	var invs []Invoice
	for i := 0; i < 3; i++ {
		inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: fmt.Sprintf("gate-qa-one-call-%d", i)})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		invs = append(invs, inv)
	}

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
			RuleSetVersionID: ruleSetVersionID,
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
	gate := NewGate(store, validator)

	if _, err := gate.ValidateBatch(c, invs); err != nil {
		t.Fatalf("ValidateBatch: %v", err)
	}

	if calls != 1 {
		t.Errorf("validator (04) received %d requests for a 3-invoice ValidateBatch, want exactly 1 -- the <60s "+
			"import gate's whole perf argument is that a 500-invoice import costs ONE HTTP round trip, not 500",
			calls)
	}
	if calls == 1 && lastItemCount != 3 {
		t.Errorf("the one request carried %d items, want 3 (every invoice in that single round trip)", lastItemCount)
	}
}

// --- 4. warning-only promotes AND counts as Clean, at the Gate layer -------

// TestGate_ValidateBatchWarningOnlyInvoicePromotesAndCountsAsCleanNotWithViolations:
// a warning-only violation set must promote the invoice to validated (the
// underlying Store.ApplyValidation semantic GATE-04 already pins) AND must
// be counted in BatchOutcome.Clean, not WithViolations -- the naive
// len(violations) == 0 guess a consumer without hasBlockingViolation would
// reach for gets this wrong, precisely the hazard gate.go's own doc names
// for why the count is computed inside ValidateBatch rather than left to a
// caller (task-114) to re-derive. Pins the semantic at the layer task-114's
// IMPV-06 (dry-run must match the real run) actually depends on, so it
// cannot silently drift once that consumer exists.
func TestGate_ValidateBatchWarningOnlyInvoicePromotesAndCountsAsCleanNotWithViolations(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	ruleSetVersionID := seedRuleSetVersionID(t, super)

	tenantID := seedTenant(t, super, "gate-qa warning-only tenant")
	entityID := seedEntity(t, super, tenantID, "gate-qa warning-only entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "gate-qa-warning-only"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := validateBatchResponse{
			RuleSetVersion:   cannedRuleSetVersion,
			RuleSetVersionID: ruleSetVersionID,
			Results: []validateBatchItemResult{
				{Ref: inv.ID, Violations: []Violation{
					{RuleKey: "supplier-tin-format", Severity: "warning", Message: "TIN format looks unusual"},
				}},
			},
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
	gate := NewGate(store, validator)

	out, err := gate.ValidateBatch(c, []Invoice{inv})
	if err != nil {
		t.Fatalf("ValidateBatch: %v", err)
	}
	if out.Clean != 1 {
		t.Errorf("Clean = %d, want 1 -- a warning-only invoice PROMOTES, so it belongs in Clean, not "+
			"WithViolations (the naive len(violations)==0 guess would misplace it)", out.Clean)
	}
	if out.WithViolations != 0 {
		t.Errorf("WithViolations = %d, want 0", out.WithViolations)
	}
	vs := out.ByID[inv.ID]
	if len(vs) != 1 || vs[0].RuleKey != "supplier-tin-format" {
		t.Errorf("ByID[%s] = %+v, want the one warning violation preserved (collect-all survives even though "+
			"the invoice promoted)", inv.ID, vs)
	}

	got, err := store.Get(c, inv.ID)
	if err != nil {
		t.Fatalf("Get after ValidateBatch: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("status = %q, want %q -- a warning-only verdict must promote", got.Status, StatusValidated)
	}
}
