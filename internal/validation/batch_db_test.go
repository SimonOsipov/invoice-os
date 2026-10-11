// M4-04-03 (task-109, Test-first: yes) -- Mode A RED DB-backed specs for
// BatchValidateHandler driven end-to-end against the REAL Store
// (LoadForDates) and the REAL validation.Engine (NewDefaultEngine)
// over the live migrated DB (v4 judges the 2026-dated fixtures, 20 rules). No identity is ever placed
// in the request context here -- that is the whole point of the tenant-free
// batch endpoint ([s2s-identity]).
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
	"time"
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
	BatchValidateHandler(store.LoadForDates, eng, nil, nil).ServeHTTP(rec, r)

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
	BatchValidateHandler(store.LoadForDates, eng, fixedNow(activeSeedNow), nil).ServeHTTP(rec, r)

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
	// A subset of v4's 10 `required` rules -- membership only, so a future
	// publish that adds another one does not redden this.
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
// as store_test.go's seedVersion doc
// comments: "restoring by CAPTURED ID rather than by naming a version
// number is the point").
func TestBatch_ResponseRuleSetVersionIDMatchesActiveRow(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	eng := NewDefaultEngine()

	var wantID string
	var wantVersion int
	if err := super.QueryRow(context.Background(),
		`SELECT id, version FROM rule_set_versions WHERE id = rule_set_version_for($1::date)`, activeSeedDate,
	).Scan(&wantID, &wantVersion); err != nil {
		t.Fatalf("read the rule_set_versions row in force on %s: %v", activeSeedDate, err)
	}

	reqBody, err := json.Marshal(map[string]any{
		"invoices": []map[string]any{{"ref": "x", "invoice": map[string]any{}}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(string(reqBody)))
	rec := httptest.NewRecorder()
	BatchValidateHandler(store.LoadForDates, eng, fixedNow(activeSeedNow), nil).ServeHTTP(rec, r)

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

func TestBatch_DB_TwoVersionsInOneBatch(t *testing.T) {
	super, app := dbTestPools(t)
	aID, _ := dateFixture(t, super, "3001-01-01", "t-a")
	bID, _ := dateFixture(t, super, "3001-06-01", "t-b")

	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(`{"invoices":[
		{"ref":"a","invoice":{"issue_date":"3001-02-01"}},
		{"ref":"b","invoice":{"issue_date":"3001-07-01"}}]}`))
	rec := httptest.NewRecorder()
	BatchValidateHandler(NewStore(app).LoadForDates, NewDefaultEngine(), nil, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	body := decodeBatch(t, rec)
	if body.Results[0].RuleSetVersionID != aID || body.Results[1].RuleSetVersionID != bID {
		t.Errorf("item stamps = %s, %s; want %s, %s",
			body.Results[0].RuleSetVersionID, body.Results[1].RuleSetVersionID, aID, bID)
	}
}

func TestBatch_DB_UndatedInvoiceIgnoresAScheduledVersion(t *testing.T) {
	super, app := dbTestPools(t)
	seedV5Lists(t, super)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	fixtureID, _ := dateFixture(t, super, tomorrow, "t-tomorrow")
	var todayID string
	var todayVersion int
	if err := super.QueryRow(context.Background(),
		`SELECT id, version FROM rule_set_versions WHERE id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)`).Scan(&todayID, &todayVersion); err != nil {
		t.Fatalf("read the version in force today: %v", err)
	}

	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(`{"invoices":[{"ref":"a","invoice":{}}]}`))
	rec := httptest.NewRecorder()
	BatchValidateHandler(NewStore(app).LoadForDates, NewDefaultEngine(), nil, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeBatch(t, rec).Results[0]
	if got.RuleSetVersion != todayVersion || got.RuleSetVersionID != todayID || got.RuleSetVersionID == fixtureID {
		t.Errorf("undated item stamp = v%d %s, want today's v%d %s and not the scheduled fixture %s",
			got.RuleSetVersion, got.RuleSetVersionID, todayVersion, todayID, fixtureID)
	}
}

// Year 0 parses in Go but Postgres rejects it at d::date; the item is judged as undated and the
// rest of the batch keeps its own version.
func TestBatch_DB_YearZeroIssueDateIsJudgedAsUndated(t *testing.T) {
	super, app := dbTestPools(t)
	seedV5Lists(t, super)
	bID, _ := dateFixture(t, super, "3001-06-01", "t-yz")
	var todayID string
	if err := super.QueryRow(context.Background(),
		`SELECT rule_set_version_for((now() AT TIME ZONE 'UTC')::date)`).Scan(&todayID); err != nil {
		t.Fatalf("read the version in force today: %v", err)
	}

	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(`{"invoices":[
		{"ref":"bad","invoice":{"issue_date":"0000-01-01"}},
		{"ref":"ok","invoice":{"issue_date":"3001-07-01"}}]}`))
	rec := httptest.NewRecorder()
	BatchValidateHandler(NewStore(app).LoadForDates, NewDefaultEngine(), nil, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := decodeBatch(t, rec)
	if body.Results[0].RuleSetVersionID != todayID || body.Results[1].RuleSetVersionID != bID {
		t.Errorf("item stamps = %s, %s; want %s (today), %s",
			body.Results[0].RuleSetVersionID, body.Results[1].RuleSetVersionID, todayID, bID)
	}
}

// One batch stamps each item with the version in force on its own issue date, across the v4/v5 boundary.
func TestBatch_DB_V5StampsInvoicesFromItsStartDate(t *testing.T) {
	super, app := dbTestPools(t)
	seedV5Lists(t, super)

	dayBefore := v5Invoice("B2B", []map[string]any{v5Line("1", "STANDARD_VAT", 1000, 7.5, 75)})
	invoiceOf(dayBefore)["issue_date"] = "2026-12-31"
	// supplier.street is required by v5 only: the same invoice is clean under v4 and fails under v5.
	noStreet := func(date string) map[string]any {
		p := v5Invoice("B2B", []map[string]any{v5Line("1", "STANDARD_VAT", 1000, 7.5, 75)})
		inv := invoiceOf(p)
		inv["issue_date"] = date
		delete(v5PartyOf(inv, "supplier"), "street")
		return inv
	}
	reqBody, err := json.Marshal(map[string]any{"invoices": []map[string]any{
		{"ref": "v4", "invoice": invoiceOf(dayBefore)},
		{"ref": "v5", "invoice": invoiceOf(v5B2BPayload())},
		{"ref": "v4-lax", "invoice": noStreet("2026-12-31")},
		{"ref": "v5-strict", "invoice": noStreet(v5Start)},
	}})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	BatchValidateHandler(NewStore(app).LoadForDates, NewDefaultEngine(), nil, nil).
		ServeHTTP(rec, httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(string(reqBody))))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := decodeBatch(t, rec)

	for i, c := range []struct {
		date    string
		version int
	}{{"2026-12-31", 4}, {v5Start, 5}, {"2026-12-31", 4}, {v5Start, 5}} {
		var wantID string
		if err := super.QueryRow(context.Background(),
			`SELECT id FROM rule_set_versions WHERE version = $1 AND id = rule_set_version_for($2::date)`, c.version, c.date).Scan(&wantID); err != nil {
			t.Fatalf("v%d is not in force on %s: %v", c.version, c.date, err)
		}
		got := body.Results[i]
		if got.RuleSetVersion != c.version || got.RuleSetVersionID != wantID {
			t.Errorf("item %s stamp = v%d %s, want v%d %s", got.Ref, got.RuleSetVersion, got.RuleSetVersionID, c.version, wantID)
		}
		wantViolations := 0
		if got.Ref == "v5-strict" {
			wantViolations = 1
		}
		if len(got.Violations) != wantViolations {
			t.Errorf("item %s has violations %+v, want %d", got.Ref, got.Violations, wantViolations)
		} else if wantViolations == 1 && got.Violations[0].RuleKey != "supplier-street-required" {
			t.Errorf("item %s violation = %s, want supplier-street-required", got.Ref, got.Violations[0].RuleKey)
		}
	}
}
