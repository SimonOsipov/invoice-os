package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/customrule"
)

// echoServer answers a clean, total batch response. mutate edits each result before it is
// written; the decoded request is kept for the caller.
func echoServer(t *testing.T, got *validateBatchRequest, mutate func(i int, req validateBatchRequest, r *validateBatchItemResult)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req validateBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got != nil {
			*got = req
		}
		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			ids := []string{}
			for _, name := range it.CustomRuleSets {
				for _, cr := range req.CustomRuleSets[name] {
					ids = append(ids, cr.ID)
				}
			}
			results[i] = validateBatchItemResult{Ref: it.Ref, Violations: []Violation{}, CustomRuleIDs: ids,
				RuleSetVersion: cannedRuleSetVersion, RuleSetVersionID: "00000000-0000-0000-0000-000000000000"}
			if mutate != nil {
				mutate(i, req, &results[i])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(validateBatchResponse{RuleSetVersion: cannedRuleSetVersion,
			RuleSetVersionID: "00000000-0000-0000-0000-000000000000", Results: results})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func noCustomRules(context.Context, []string) (CustomRuleSets, error) { return CustomRuleSets{}, nil }

func ruleAt(revID string, entity *string) customrule.Rule {
	return customrule.Rule{ID: "rule-" + revID, Key: "custom:rule-" + revID, RevisionID: revID, EntityID: entity,
		Type: "required", Target: "buyer.email", Params: json.RawMessage(`{}`), Severity: "error", Message: "m " + revID}
}

func TestGate_SendsFirmAndOwnClientRulesOnly(t *testing.T) {
	a, b := "aaaaaaaa-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"
	sets := CustomRuleSets{
		Firm:     []customrule.Rule{ruleAt("F", nil)},
		ByEntity: map[string][]customrule.Rule{a: {ruleAt("RA", &a)}, b: {ruleAt("RB", &b)}},
	}
	var req validateBatchRequest
	srv := echoServer(t, &req, nil)
	gate := NewGate(nil, NewValidator(srv.URL, gapiS2SToken, nil))
	var asked []string
	gate.customRules = func(_ context.Context, ids []string) (CustomRuleSets, error) {
		asked = ids
		return sets, nil
	}

	res, err := gate.Evaluate(context.Background(), []EvalItem{
		{Ref: "1", Invoice: Invoice{EntityID: a}}, {Ref: "2", Invoice: Invoice{EntityID: b}}, {Ref: "3", Invoice: Invoice{EntityID: a}},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !reflect.DeepEqual(asked, []string{a, b}) {
		t.Errorf("rules read for %v, want the distinct entities [%s %s] in one call", asked, a, b)
	}
	ids := func(name string) []string {
		var out []string
		for _, r := range req.CustomRuleSets[name] {
			out = append(out, r.ID)
		}
		return out
	}
	if len(req.CustomRuleSets) != 3 || !reflect.DeepEqual(ids("firm"), []string{"F"}) ||
		!reflect.DeepEqual(ids(a), []string{"RA"}) || !reflect.DeepEqual(ids(b), []string{"RB"}) {
		t.Errorf("sets = %+v, want firm={F}, A={RA}, B={RB}, each rule once", req.CustomRuleSets)
	}
	want := map[string][]string{"1": {"firm", a}, "2": {"firm", b}, "3": {"firm", a}}
	for _, it := range req.Invoices {
		if !reflect.DeepEqual(it.CustomRuleSets, want[it.Ref]) {
			t.Errorf("item %s names %v, want %v", it.Ref, it.CustomRuleSets, want[it.Ref])
		}
	}
	if got := res.CustomByRef["2"]; !reflect.DeepEqual(got, []string{"F", "RB"}) {
		t.Errorf("CustomByRef[2] = %v, want [F RB]", got)
	}
}

func TestGate_NoRulesSendsNoSets(t *testing.T) {
	var req validateBatchRequest
	srv := echoServer(t, &req, nil)
	gate := NewGate(nil, NewValidator(srv.URL, gapiS2SToken, nil))
	gate.customRules = noCustomRules
	res, err := gate.Evaluate(context.Background(), []EvalItem{{Ref: "1", Invoice: Invoice{EntityID: "e"}}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(req.CustomRuleSets) != 0 || len(req.Invoices[0].CustomRuleSets) != 0 {
		t.Errorf("request carried sets %v / %v, want none", req.CustomRuleSets, req.Invoices[0].CustomRuleSets)
	}
	if ids, ok := res.CustomByRef["1"]; !ok || ids == nil || len(ids) != 0 {
		t.Errorf("CustomByRef[1] = %#v, want a present empty list", ids)
	}
}

func TestValidator_CustomEchoMismatchIsUpstream(t *testing.T) {
	sets := map[string][]CustomRule{"s": {{ID: "r1"}, {ID: "r2"}}}
	named := []ValidateItem{{Ref: "x", Invoice: map[string]any{}, CustomRuleSets: []string{"s"}}}
	unnamed := []ValidateItem{{Ref: "x", Invoice: map[string]any{}}}
	cases := []struct {
		name   string
		items  []ValidateItem
		mutate func(i int, req validateBatchRequest, r *validateBatchItemResult)
	}{
		{"field absent", named, func(_ int, _ validateBatchRequest, r *validateBatchItemResult) { r.CustomRuleIDs = nil }},
		{"extra id", named, func(_ int, _ validateBatchRequest, r *validateBatchItemResult) {
			r.CustomRuleIDs = []string{"r1", "r2", "r3"}
		}},
		{"missing id", named, func(_ int, _ validateBatchRequest, r *validateBatchItemResult) { r.CustomRuleIDs = []string{"r1"} }},
		{"repeated id", named, func(_ int, _ validateBatchRequest, r *validateBatchItemResult) {
			r.CustomRuleIDs = []string{"r1", "r1"}
		}},
		{"ids for an item that named none", unnamed, func(_ int, _ validateBatchRequest, r *validateBatchItemResult) {
			r.CustomRuleIDs = []string{"r1"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := echoServer(t, nil, c.mutate)
			_, err := NewValidator(srv.URL, gapiS2SToken, nil).ValidateCustom(context.Background(), c.items, sets)
			if !errors.Is(err, ErrUpstream) {
				t.Errorf("err = %v, want ErrUpstream", err)
			}
		})
	}
	t.Run("matching ids in any order pass", func(t *testing.T) {
		srv := echoServer(t, nil, func(_ int, _ validateBatchRequest, r *validateBatchItemResult) {
			r.CustomRuleIDs = []string{"r2", "r1"}
		})
		res, err := NewValidator(srv.URL, gapiS2SToken, nil).ValidateCustom(context.Background(), named, sets)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		got := append([]string(nil), res.CustomByRef["x"]...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, []string{"r1", "r2"}) {
			t.Errorf("CustomByRef = %v", got)
		}
	})
}

func TestValidator_OldServiceStillJudgesItemsWithoutRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Invoices []struct {
				Ref string `json:"ref"`
			} `json:"invoices"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// The pre-ENGI-14 shape: no custom_rule_ids field.
		type oldItem struct {
			Ref              string      `json:"ref"`
			Violations       []Violation `json:"violations"`
			RuleSetVersion   int         `json:"rule_set_version"`
			RuleSetVersionID string      `json:"rule_set_version_id"`
		}
		old := struct {
			RuleSetVersion   int       `json:"rule_set_version"`
			RuleSetVersionID string    `json:"rule_set_version_id"`
			Results          []oldItem `json:"results"`
		}{RuleSetVersion: cannedRuleSetVersion, RuleSetVersionID: "v"}
		for _, it := range req.Invoices {
			old.Results = append(old.Results, oldItem{Ref: it.Ref, Violations: []Violation{},
				RuleSetVersion: cannedRuleSetVersion, RuleSetVersionID: "v"})
		}
		_ = json.NewEncoder(w).Encode(old)
	}))
	t.Cleanup(srv.Close)

	items := []ValidateItem{{Ref: "a", Invoice: map[string]any{}}, {Ref: "b", Invoice: map[string]any{}}}
	res, err := NewValidator(srv.URL, gapiS2SToken, nil).Validate(context.Background(), items)
	if err != nil {
		t.Fatalf("Validate against a pre-ENGI-14 response: %v", err)
	}
	for _, ref := range []string{"a", "b"} {
		if ids, ok := res.CustomByRef[ref]; !ok || len(ids) != 0 {
			t.Errorf("CustomByRef[%s] = %#v, want present and empty", ref, ids)
		}
	}
}
