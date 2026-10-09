// M4-04-03 (task-109, Test-first: yes) -- Mode A RED specs for
// BatchValidateHandler's own contract (VB-01, VB-02, VB-07..VB-11): the
// in-process Test Specs, driven with an injected stub loader closure (no
// DB) plus the REAL validation.Engine (NewDefaultEngine -- Evaluate itself
// is already-correct, already-shipped code; only the batch WIRING around it
// is new for this subtask).
//
// These specs are written against BatchValidateHandler as the story's plan
// specifies it (single load per batch, ≤5,000 items, ≤16 MiB body, one
// engine/config fault fails the WHOLE batch, the payload re-rooted as
// Payload{"invoice": it.Invoice} before Evaluate) -- see
// batch_qa_scaffold.go's file header for exactly which of those the current
// (QA Mode-A, temporary) scaffold gets wrong on purpose, and therefore which
// of these RED today.
//
// S2S auth (VB-03..06) is a separate concern, covered by s2s_test.go --
// these tests call BatchValidateHandler directly, unwrapped, matching the
// Test Specs table's "in-process" type for VB-01/02/07..11.
package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// batchResponseBody decodes BatchValidateHandler's success body
// ({"rule_set_version":...,"rule_set_version_id":...,"results":[...]}) or
// its flat error envelope ({"error":...}) -- both read through the same
// struct since Go's decoder leaves absent fields at their zero value.
// Shared with batch_db_test.go (same package).
type batchResponseBody struct {
	RuleSetVersion   int    `json:"rule_set_version"`
	RuleSetVersionID string `json:"rule_set_version_id"`
	Results          []struct {
		Ref              string      `json:"ref"`
		Violations       []Violation `json:"violations"`
		RuleSetVersion   int         `json:"rule_set_version"`
		RuleSetVersionID string      `json:"rule_set_version_id"`
	} `json:"results"`
	Error string `json:"error"`
}

// doBatch issues a POST /v1/validate/batch request with a raw JSON body
// straight through BatchValidateHandler (no S2SMiddleware wrapper -- see
// file header). Shared with batch_db_test.go.
func doBatch(t *testing.T, loadRuleSet func(ctx context.Context) (RuleSet, error), eng *Engine, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	return doBatchAt(t, allDates(loadRuleSet), eng, nil, rawBody)
}

// allDates serves the one rule-set the load func returns for every requested date.
func allDates(load func(ctx context.Context) (RuleSet, error)) func(ctx context.Context, dates []string) (map[string]RuleSet, error) {
	return func(ctx context.Context, dates []string) (map[string]RuleSet, error) {
		rs, err := load(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[string]RuleSet, len(dates))
		for _, d := range dates {
			out[d] = rs
		}
		return out, nil
	}
}

func doBatchAt(t *testing.T, load func(ctx context.Context, dates []string) (map[string]RuleSet, error), eng *Engine, now func() time.Time, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(rawBody))
	rec := httptest.NewRecorder()
	BatchValidateHandler(load, eng, now, nil).ServeHTTP(rec, r)
	return rec
}

// TestBatch_HappyPathOrderedResults (VB-01): 3 invoices (1 clean, 2 dirty)
// against a single "total is required" rule -> 200; 3 results in REQUEST
// order; each ref echoed; the clean one has violations: [].
func TestBatch_HappyPathOrderedResults(t *testing.T) {
	rule := Rule{
		Key: "total-required", Type: TypeRequired, Target: "total",
		Params: json.RawMessage(`{}`), Severity: "error", Message: "total is required",
		Scope: "document", Enabled: true,
	}
	loader := func(ctx context.Context) (RuleSet, error) {
		return RuleSet{ID: "fixture-id", Version: 42, Rules: []Rule{rule}}, nil
	}
	eng := NewDefaultEngine()

	reqBody := `{"invoices":[
		{"ref":"clean","invoice":{"total":100}},
		{"ref":"dirty1","invoice":{}},
		{"ref":"dirty2","invoice":{"total":""}}
	]}`
	rec := doBatch(t, loader, eng, reqBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body batchResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body.RuleSetVersion != 42 {
		t.Errorf("rule_set_version = %d, want 42", body.RuleSetVersion)
	}
	if len(body.Results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(body.Results))
	}
	wantRefs := []string{"clean", "dirty1", "dirty2"}
	for i, want := range wantRefs {
		if body.Results[i].Ref != want {
			t.Errorf("results[%d].ref = %q, want %q (echoed in REQUEST order)", i, body.Results[i].Ref, want)
		}
	}
	if len(body.Results[0].Violations) != 0 {
		t.Errorf("results[0] (clean, total=100 present) violations = %+v, want none -- "+
			"[batch-payload-rooting]: a mis-rooted payload resolves every target as absent, "+
			"firing `required` even on a clean invoice", body.Results[0].Violations)
	}
	if len(body.Results[1].Violations) == 0 {
		t.Error("results[1] (dirty1, total absent) violations = [], want at least one (total-required)")
	}
	if len(body.Results[2].Violations) == 0 {
		t.Error("results[2] (dirty2, total blank) violations = [], want at least one (total-required)")
	}
}

// TestBatch_SingleRuleSetLoadPerBatch (VB-02): a batch of 3 invoices -> the
// injected loader is called EXACTLY ONCE, however many invoices the batch
// carries (a counting fake).
func TestBatch_SingleRuleSetLoadPerBatch(t *testing.T) {
	calls := 0
	loader := func(ctx context.Context) (RuleSet, error) {
		calls++
		return RuleSet{ID: "fixture-id", Version: 42}, nil
	}
	eng := NewDefaultEngine()

	reqBody := `{"invoices":[{"ref":"a","invoice":{}},{"ref":"b","invoice":{}},{"ref":"c","invoice":{}}]}`
	rec := doBatch(t, loader, eng, reqBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Errorf("loader called %d times for a 3-item batch, want exactly 1 -- ONE rule-set load per "+
			"batch, however many invoices it carries [VB-02] (the entire point of the batch endpoint)", calls)
	}
}

// TestBatch_EmptyInvoicesList400 (VB-07): {"invoices":[]} -> 400.
func TestBatch_EmptyInvoicesList400(t *testing.T) {
	loader := func(ctx context.Context) (RuleSet, error) { return RuleSet{ID: "fixture-id", Version: 42}, nil }
	eng := NewDefaultEngine()

	rec := doBatch(t, loader, eng, `{"invoices":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an empty invoices list (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestBatch_OverCapItems400 (VB-08): 5,001 invoices -> 400.
func TestBatch_OverCapItems400(t *testing.T) {
	items := make([]map[string]any, 5001)
	for i := range items {
		items[i] = map[string]any{"ref": fmt.Sprintf("r%d", i), "invoice": map[string]any{}}
	}
	reqBody, err := json.Marshal(map[string]any{"invoices": items})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	loader := func(ctx context.Context) (RuleSet, error) { return RuleSet{ID: "fixture-id", Version: 42}, nil }
	eng := NewDefaultEngine()

	rec := doBatch(t, loader, eng, string(reqBody))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for 5,001 invoices (over the 5,000 cap; body=%s)", rec.Code, rec.Body.String())
	}
}

// TestBatch_OversizedBody413 (VB-09): a body over 16 MiB -> 413. Stage-1
// addendum G1: statusForErr has no 413 branch, so this must come from an
// explicit errors.As(*http.MaxBytesError) check at the decode site,
// ordered AFTER the S2S 401 (s2s_test.go's VB-03) but BEFORE the generic
// 400 -- see internal/importer/handlers.go:112-120 for the shipped house
// pattern. Padding lives inside ONE item's invoice (not extra items), so
// this test exercises the BYTE cap only, independent of VB-08's item cap.
func TestBatch_OversizedBody413(t *testing.T) {
	pad := strings.Repeat("x", 17<<20) // > 16 MiB
	reqBody := fmt.Sprintf(`{"invoices":[{"ref":"r1","invoice":{"note":%q}}]}`, pad)
	loader := func(ctx context.Context) (RuleSet, error) { return RuleSet{ID: "fixture-id", Version: 42}, nil }
	eng := NewDefaultEngine()

	rec := doBatch(t, loader, eng, reqBody)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 for a body over 16 MiB", rec.Code)
	}
}

// TestBatch_NoActiveRuleSet503 (VB-10): the loader returning
// ErrNoActiveRuleSet -> 503 "no active rule-set" (reusing statusForErr).
func TestBatch_NoActiveRuleSet503(t *testing.T) {
	loader := func(ctx context.Context) (RuleSet, error) { return RuleSet{}, ErrNoActiveRuleSet }
	eng := NewDefaultEngine()

	rec := doBatch(t, loader, eng, `{"invoices":[{"ref":"a","invoice":{}}]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the loader returns ErrNoActiveRuleSet (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestBatch_ConfigFault500NoPartialResults (VB-11): a rule with an unknown
// type (a config fault -- Decision N15, real Engine.Evaluate behavior,
// engine.go's `ev, ok := e.registry[rule.Type]`) on a batch of 2 otherwise-
// valid invoices -> 500 for the WHOLE batch, no partial "results" key in
// the response ([batch-fault-semantics]).
func TestBatch_ConfigFault500NoPartialResults(t *testing.T) {
	badRule := Rule{
		Key: "bogus", Type: RuleType("not-a-real-type"), Params: json.RawMessage(`{}`),
		Severity: "error", Message: "x", Scope: "document", Enabled: true,
	}
	loader := func(ctx context.Context) (RuleSet, error) {
		return RuleSet{ID: "fixture-id", Version: 42, Rules: []Rule{badRule}}, nil
	}
	eng := NewDefaultEngine()

	reqBody := `{"invoices":[{"ref":"a","invoice":{"total":1}},{"ref":"b","invoice":{"total":2}}]}`
	rec := doBatch(t, loader, eng, reqBody)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for an unknown rule type (config fault; body=%s)", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if _, ok := raw["results"]; ok {
		t.Error(`500 response carries a "results" key -- a config fault must fail the WHOLE batch, ` +
			`no partial results [batch-fault-semantics]`)
	}
}

// datesLoader serves sets by date, records each call's dates, and fails on a
// date it has no set for.
func datesLoader(calls *[][]string, sets map[string]RuleSet) func(context.Context, []string) (map[string]RuleSet, error) {
	return func(_ context.Context, dates []string) (map[string]RuleSet, error) {
		*calls = append(*calls, append([]string(nil), dates...))
		out := map[string]RuleSet{}
		for _, d := range dates {
			rs, ok := sets[d]
			if !ok {
				return nil, fmt.Errorf("no set for %s", d)
			}
			out[d] = rs
		}
		return out, nil
	}
}

func fixedNow(s string) func() time.Time {
	return func() time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			panic(err)
		}
		return ts
	}
}

func decodeBatch(t *testing.T, rec *httptest.ResponseRecorder) batchResponseBody {
	t.Helper()
	var body batchResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return body
}

func buyerRequired() Rule {
	return Rule{
		Key: "buyer-required", Type: TypeRequired, Target: "buyer.name",
		Params: json.RawMessage(`{}`), Severity: "error", Message: "buyer.name is required",
		Scope: "document", Enabled: true,
	}
}

func TestBatch_EachItemJudgedByTheVersionForItsIssueDate(t *testing.T) {
	var calls [][]string
	load := datesLoader(&calls, map[string]RuleSet{
		"3001-02-01": {ID: "id-a", Version: 5, Rules: []Rule{buyerRequired()}},
		"3001-07-01": {ID: "id-b", Version: 6, Rules: []Rule{{
			Key: "total-required", Type: TypeRequired, Target: "total",
			Params: json.RawMessage(`{}`), Severity: "error", Message: "x", Scope: "document", Enabled: true,
		}}},
	})
	rec := doBatchAt(t, load, NewDefaultEngine(), nil, `{"invoices":[
		{"ref":"one","invoice":{"total":1,"issue_date":"3001-02-01"}},
		{"ref":"two","invoice":{"total":1,"issue_date":"3001-07-01"}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	body := decodeBatch(t, rec)
	r1, r2 := body.Results[0], body.Results[1]
	if len(r1.Violations) != 1 || r1.Violations[0].RuleKey != "buyer-required" || r1.RuleSetVersion != 5 || r1.RuleSetVersionID != "id-a" {
		t.Errorf("item 1 = %+v, want set A's violation and stamp v5/id-a", r1)
	}
	if len(r2.Violations) != 0 || r2.RuleSetVersion != 6 || r2.RuleSetVersionID != "id-b" {
		t.Errorf("item 2 = %+v, want no violations and set B's stamp v6/id-b", r2)
	}
}

func TestBatch_UndatedInvoiceIsJudgedByTheVersionInForceToday(t *testing.T) {
	var calls [][]string
	load := datesLoader(&calls, map[string]RuleSet{"2027-02-01": {ID: "id-t", Version: 9}})
	rec := doBatchAt(t, load, NewDefaultEngine(), fixedNow("2027-02-01T10:00:00Z"), `{"invoices":[{"ref":"a","invoice":{}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != "2027-02-01" {
		t.Errorf("loader calls = %v, want one call with [2027-02-01]", calls)
	}
	if got := decodeBatch(t, rec).Results[0]; got.RuleSetVersion != 9 || got.RuleSetVersionID != "id-t" {
		t.Errorf("item stamp = v%d %q, want v9 id-t", got.RuleSetVersion, got.RuleSetVersionID)
	}
}

func TestBatch_MalformedIssueDateFallsBackToToday(t *testing.T) {
	var calls [][]string
	load := datesLoader(&calls, map[string]RuleSet{"2027-03-01": {ID: "id-t", Version: 9}})
	rec := doBatchAt(t, load, NewDefaultEngine(), fixedNow("2027-03-01T10:00:00Z"), `{"invoices":[
		{"ref":"a","invoice":{"issue_date":"15/01/2027"}},
		{"ref":"b","invoice":{"issue_date":20270115}},
		{"ref":"c","invoice":{"issue_date":""}},
		{"ref":"d","invoice":{"issue_date":null}},
		{"ref":"e","invoice":{"issue_date":"2027-1-5"}},
		{"ref":"f","invoice":{"issue_date":"2027-02-30"}},
		{"ref":"g","invoice":{"issue_date":" 2027-01-15"}},
		{"ref":"h","invoice":{"issue_date":"2027-01-15T00:00:00Z"}},
		{"ref":"i","invoice":{"issue_date":{"y":2027}}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if n := len(decodeBatch(t, rec).Results); n != 9 {
		t.Fatalf("results = %d, want 9", n)
	}
	if len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != "2027-03-01" {
		t.Errorf("loader calls = %v, want one call with only today", calls)
	}
}

func TestBatch_TodayIsTheUTCDate(t *testing.T) {
	var calls [][]string
	load := datesLoader(&calls, map[string]RuleSet{"2026-12-31": {ID: "id-t", Version: 9}})
	rec := doBatchAt(t, load, NewDefaultEngine(), fixedNow("2027-01-01T00:30:00+01:00"), `{"invoices":[{"ref":"a","invoice":{}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 || calls[0][0] != "2026-12-31" {
		t.Errorf("loader calls = %v, want [2026-12-31]", calls)
	}
}

func TestBatch_OneLoaderCallPerBatch(t *testing.T) {
	var calls [][]string
	load := datesLoader(&calls, map[string]RuleSet{
		"3001-01-01": {ID: "a", Version: 1}, "3001-02-01": {ID: "b", Version: 2}, "2027-05-05": {ID: "t", Version: 3},
	})
	rec := doBatchAt(t, load, NewDefaultEngine(), fixedNow("2027-05-05T01:00:00Z"), `{"invoices":[
		{"ref":"1","invoice":{"issue_date":"3001-01-01"}},
		{"ref":"2","invoice":{"issue_date":"3001-02-01"}},
		{"ref":"3","invoice":{"issue_date":"3001-01-01"}},
		{"ref":"4","invoice":{}},
		{"ref":"5","invoice":{"issue_date":"3001-02-01"}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 {
		t.Fatalf("loader called %d times, want 1", len(calls))
	}
	got := map[string]bool{}
	for _, d := range calls[0] {
		got[d] = true
	}
	if len(calls[0]) != 3 || !got["3001-01-01"] || !got["3001-02-01"] || !got["2027-05-05"] {
		t.Errorf("dates = %v, want exactly the 3 distinct dates", calls[0])
	}
}

func TestBatch_LoaderOmittingADateIs500(t *testing.T) {
	load := func(_ context.Context, dates []string) (map[string]RuleSet, error) {
		return map[string]RuleSet{dates[0]: {ID: "a", Version: 1}}, nil
	}
	rec := doBatchAt(t, load, NewDefaultEngine(), nil, `{"invoices":[
		{"ref":"1","invoice":{"issue_date":"3001-01-01"}},{"ref":"2","invoice":{"issue_date":"3001-02-01"}}]}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("got %d %s, want 500 internal server error", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "results") {
		t.Errorf("body %s carries results, want no partial results", rec.Body.String())
	}
}

func TestBatch_TopLevelStampIsTheLowestVersion(t *testing.T) {
	var calls [][]string
	load := datesLoader(&calls, map[string]RuleSet{
		"3001-01-01": {ID: "id-7", Version: 7}, "3001-02-01": {ID: "id-5", Version: 5}, "3001-03-01": {ID: "id-9", Version: 9},
	})
	rec := doBatchAt(t, load, NewDefaultEngine(), nil, `{"invoices":[
		{"ref":"1","invoice":{"issue_date":"3001-01-01"}},
		{"ref":"2","invoice":{"issue_date":"3001-02-01"}},
		{"ref":"3","invoice":{"issue_date":"3001-03-01"}}]}`)
	body := decodeBatch(t, rec)
	if body.RuleSetVersion != 5 || body.RuleSetVersionID != "id-5" {
		t.Errorf("top-level stamp = v%d %q, want v5 id-5", body.RuleSetVersion, body.RuleSetVersionID)
	}
}
