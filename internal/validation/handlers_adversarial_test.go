// M4-04-03 (task-109) -- Stage 4 (QA Verify, Mode B) adversarial coverage,
// added on top of the executor's green suite without modifying any existing
// test. store_adversarial_test.go proves the G3 guard holds at the STORE
// layer (loadActiveRuleSetTx / LoadActiveRuleSet / LoadActiveRuleSetGlobal).
// This file proves it survives end-to-end through the REAL HTTP handlers,
// wired to the REAL Store and REAL Engine over the live DB -- i.e. that
// nothing between the store and the wire response (statusForErr, the
// handler's own error branch) accidentally swallows ErrEmptyRuleSet back
// into a 200. Both surfaces this story ships are covered:
//
//   - ValidateHandler + Store.LoadActiveRuleSet: the UNCHANGED single-invoice
//     POST /v1/validate path (the M3-09 playground contract) -- proving the
//     "existing behaviour is unchanged" AC (#9) does not silently mean
//     "existing fail-open is unchanged" now that this subtask's refactor
//     routes it through the shared loadActiveRuleSetTx.
//   - BatchValidateHandler + Store.LoadActiveRuleSetGlobal: the NEW
//     tenant-free batch path -- the disaster case named directly in the
//     story: a silently-empty rule-set masquerading as "every invoice is
//     compliant" for an entire batch at once, not just one invoice.
package validation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestValidate_ActiveVersionZeroRules503NotCleanPass (G3, single-invoice
// surface): with a REAL active rule_set_versions row carrying ZERO rules,
// POST /v1/validate (ValidateHandler + the real Store.LoadActiveRuleSet +
// the real Engine) must answer 503 -- NEVER a clean 200 with
// violations: []. A 200 here would be indistinguishable, on the wire, from
// "this invoice is genuinely compliant."
func TestValidate_ActiveVersionZeroRules503NotCleanPass(t *testing.T) {
	super, app := dbTestPools(t)

	seedVersion(t, super, true) // zero rules: no seedRule call

	store := NewStore(app)
	eng := NewDefaultEngine()
	loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
		rs, err := store.LoadActiveRuleSet(ctx)
		if err != nil {
			return Result{}, err
		}
		return eng.Evaluate(p, rs)
	}

	tenantID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID}
	r := httptest.NewRequest("POST", "/v1/validate", strings.NewReader(`{"invoice":{}}`))
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	ValidateHandler(loadAndEval, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the active rule-set carries zero rules (body=%s) -- "+
			"a 200 here means an empty invoice against an unreadable rule-set was reported CLEAN, "+
			"the exact G3 silent fail-open the [tenant-free-ruleset-load] guard exists to prevent",
			rec.Code, rec.Body.String())
	}
}

// TestBatchValidate_ActiveVersionZeroRules503NotCleanPass (G3, batch
// surface): the same scenario through BatchValidateHandler +
// Store.LoadActiveRuleSetGlobal -- a batch of otherwise-invalid invoices
// (empty {} bodies, which would fire every `required` rule against the
// real v2 rule-set, per VB-13) must still answer 503 for the WHOLE batch,
// never a 200 reporting every item's violations: [] (an entire batch
// masquerading as compliant is worse than one invoice: it is exactly the
// mass-fail-open a compliance gate exists to prevent).
func TestBatchValidate_ActiveVersionZeroRules503NotCleanPass(t *testing.T) {
	super, app := dbTestPools(t)

	seedVersion(t, super, true) // zero rules

	store := NewStore(app)
	eng := NewDefaultEngine()

	reqBody := `{"invoices":[{"ref":"a","invoice":{}},{"ref":"b","invoice":{}}]}`
	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	BatchValidateHandler(store.LoadActiveRuleSetGlobal, eng, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusServiceUnavailable {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &raw)
		if _, hasResults := raw["results"]; hasResults {
			t.Fatalf("status = %d (want 503) AND the response carries a \"results\" key (body=%s) -- "+
				"a batch validated against a rule-set with zero readable rules must never report "+
				"per-item results at all, let alone violations:[] for every item", rec.Code, rec.Body.String())
		}
		t.Fatalf("status = %d, want 503 when the active rule-set carries zero rules (body=%s)",
			rec.Code, rec.Body.String())
	}
}
