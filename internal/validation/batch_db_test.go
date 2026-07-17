// M4-04-03 (task-109, Test-first: yes) -- Mode A RED DB-backed specs for
// BatchValidateHandler driven end-to-end against the REAL Store
// (LoadActiveRuleSetGlobal, currently the QA scaffold in
// store_qa_scaffold.go) and the REAL validation.Engine (NewDefaultEngine)
// over the live migrated DB (v2 active, 19 rules -- verified live, Stage-1
// addendum). No identity is ever placed in the request context here --
// that is the whole point of the tenant-free batch endpoint ([s2s-identity]).
//
// Run: `make dev-db` once, then with the per-role DSNs set directly (see
// dbTestPools in schema_test.go):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run 'TestBatch_' ./internal/validation/...
package validation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBatch_FullyValidInvoiceZeroViolations (VB-12, [batch-payload-rooting]'s
// discriminating test): a FULLY VALID invoice, evaluated against the REAL
// live v2 rule-set, must produce ZERO violations. Reuses
// seed_test.go's validInvoicePayload() -- TestSeed_DemoContract already
// proves, live, that this exact fixture fires zero violations against
// whatever rule-set is currently ACTIVE (activeSeedVersion = 2 today,
// per that file's doc comment), so this is not a hand-rolled fixture of
// unverified correctness.
//
// This is the ONE test that can catch a mis-rooted payload: a handler that
// passes it.Invoice to Engine.Evaluate UNWRAPPED (instead of re-rooting it
// as Payload{"invoice": it.Invoice}) makes resolvePath's p["invoice"]
// lookup fail for EVERY target, so every `required` rule fires -- even
// though the underlying data is fully valid. VB-13 (empty invoice) cannot
// catch this: a mis-rooted EMPTY invoice also resolves every path absent,
// so it fires every required rule regardless of whether rooting is correct.
func TestBatch_FullyValidInvoiceZeroViolations(t *testing.T) {
	_, app := dbTestPools(t)
	store := NewStore(app)
	eng := NewDefaultEngine()

	invoice := validInvoicePayload()["invoice"]
	reqBody, err := json.Marshal(map[string]any{
		"invoices": []map[string]any{{"ref": "clean", "invoice": invoice}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(string(reqBody)))
	rec := httptest.NewRecorder()
	BatchValidateHandler(store.LoadActiveRuleSetGlobal, eng, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body batchResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if len(body.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(body.Results))
	}
	if body.Results[0].Ref != "clean" {
		t.Errorf("results[0].ref = %q, want %q", body.Results[0].Ref, "clean")
	}
	if len(body.Results[0].Violations) != 0 {
		t.Errorf("a FULLY VALID invoice against the real v2 rule-set produced %d violations (want 0): %+v -- "+
			"[batch-payload-rooting]'s discriminating test: a mis-rooted payload resolves every target as "+
			"absent, which fires every `required` rule", len(body.Results[0].Violations), body.Results[0].Violations)
	}
}

// TestBatch_EmptyInvoiceFiresEveryRequired (VB-13): an empty invoice {}
// against the real v2 rule-set must fire every `required` rule -- proving
// the root resolves at all. This test does NOT discriminate mis-rooting
// (see VB-12's doc comment and [batch-payload-rooting]): it may legitimately
// PASS even against a mis-rooted handler, since a mis-rooted empty payload
// also resolves every target absent.
func TestBatch_EmptyInvoiceFiresEveryRequired(t *testing.T) {
	_, app := dbTestPools(t)
	store := NewStore(app)
	eng := NewDefaultEngine()

	reqBody, err := json.Marshal(map[string]any{
		"invoices": []map[string]any{{"ref": "empty", "invoice": map[string]any{}}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(string(reqBody)))
	rec := httptest.NewRecorder()
	BatchValidateHandler(store.LoadActiveRuleSetGlobal, eng, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body batchResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if len(body.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(body.Results))
	}
	// v2's live `required` rules -- 9 of the 19, verified live against the
	// local DB (Stage-1 addendum): currency, invoice_number, issue_date,
	// line_items, subtotal, supplier.name, supplier.tin, total, vat.
	wantKeys := []string{
		"currency-required", "invoice-number-required", "issue-date-required",
		"line-items-required", "subtotal-required", "supplier-name-required",
		"supplier-tin-required", "total-required", "vat-required",
	}
	gotKeys := map[string]bool{}
	for _, v := range body.Results[0].Violations {
		gotKeys[v.RuleKey] = true
	}
	for _, want := range wantKeys {
		if !gotKeys[want] {
			t.Errorf("empty invoice: missing expected violation %q -- proves the root resolves at all "+
				"(got violations: %+v)", want, body.Results[0].Violations)
		}
	}
}

// TestBatch_ResponseRuleSetVersionIDMatchesActiveRow (VB-17): the batch
// response's rule_set_version_id, resolved against rule_set_versions, must
// be the ACTIVE row's id, and its version must equal the reported int.
// Captured by QUERY (not hardcoded) -- rule_set_versions.id is DB-generated
// (gen_random_uuid()), so a literal uuid would only be correct against
// today's local dev DB, not CI or any other environment (same discipline
// as store_test.go's TestStore_LoadNoActiveErrors / seedVersion doc
// comments: "restoring by CAPTURED ID rather than by naming a version
// number is the point").
func TestBatch_ResponseRuleSetVersionIDMatchesActiveRow(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	eng := NewDefaultEngine()

	var wantID string
	var wantVersion int
	if err := super.QueryRow(context.Background(),
		`SELECT id, version FROM rule_set_versions WHERE is_active LIMIT 1`,
	).Scan(&wantID, &wantVersion); err != nil {
		t.Fatalf("read the active rule_set_versions row: %v", err)
	}

	reqBody, err := json.Marshal(map[string]any{
		"invoices": []map[string]any{{"ref": "x", "invoice": map[string]any{}}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(string(reqBody)))
	rec := httptest.NewRecorder()
	BatchValidateHandler(store.LoadActiveRuleSetGlobal, eng, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body batchResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body.RuleSetVersionID != wantID {
		t.Errorf("response rule_set_version_id = %q, want %q (the active rule_set_versions row's id)",
			body.RuleSetVersionID, wantID)
	}
	if body.RuleSetVersion != wantVersion {
		t.Errorf("response rule_set_version = %d, want %d", body.RuleSetVersion, wantVersion)
	}
}
