package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type loadCall struct {
	dates  []string
	custom []Rule
}

// recordingLoader serves rs for every requested date and records each call.
func recordingLoader(rs RuleSet, calls *[]loadCall) func(context.Context, []string, []Rule) (map[string]RuleSet, error) {
	return func(_ context.Context, dates []string, custom []Rule) (map[string]RuleSet, error) {
		*calls = append(*calls, loadCall{append([]string(nil), dates...), append([]Rule(nil), custom...)})
		out := map[string]RuleSet{}
		for _, d := range dates {
			out[d] = rs
		}
		return out, nil
	}
}

// goldenFake is a two-rule golden set: buyer.tin format and invoice_number required.
func goldenFake() RuleSet {
	return RuleSet{ID: "g-id", Version: 7, Rules: []Rule{
		{Key: "buyer-tin-format", Type: TypeFormat, Target: "buyer.tin", Params: json.RawMessage(`{"pattern":"^[0-9]{8}-[0-9]{4}$"}`),
			Severity: "error", Message: "bad buyer tin", Scope: "document", Enabled: true},
		{Key: "invoice-number-required", Type: TypeRequired, Target: "invoice_number", Params: json.RawMessage(`{}`),
			Severity: "error", Message: "number needed", Scope: "document", Enabled: true},
	}}
}

// failingInvoice breaks both golden rules and has no buyer.email.
func failingInvoice() map[string]any {
	return map[string]any{"buyer": map[string]any{"tin": "abc"}}
}

func cwire(id, key string, typ RuleType, target, params string, sev Severity, msg string) map[string]any {
	return map[string]any{"id": id, "key": key, "type": typ, "target": target,
		"params": json.RawMessage(params), "severity": sev, "when": nil, "message": msg}
}

func requiredEmail(id, key string) map[string]any {
	return cwire(id, key, TypeRequired, "buyer.email", `{}`, "error", "email needed")
}

func runCustomBatch(t *testing.T, rs RuleSet, calls *[]loadCall, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return doBatchAt(t, recordingLoader(rs, calls), NewDefaultEngine(), nil, string(raw))
}

func item(ref string, sets ...string) map[string]any {
	it := map[string]any{"ref": ref, "invoice": failingInvoice()}
	if sets != nil {
		it["custom_rule_sets"] = sets
	}
	return it
}

func batchKeys(vs []Violation) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.RuleKey)
	}
	return out
}

func wantOK(t *testing.T, rec *httptest.ResponseRecorder) batchResponseBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	return decodeBatch(t, rec)
}

func TestBatch_CustomSetJoinsTheGoldenRules(t *testing.T) {
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices":         []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{requiredEmail("rev1", "custom:r1")}},
	}))
	got := body.Results[0]
	want := []string{"buyer-tin-format", "custom:r1", "invoice-number-required"}
	if !reflect.DeepEqual(batchKeys(got.Violations), want) {
		t.Errorf("violation keys = %v, want %v", batchKeys(got.Violations), want)
	}
	if !reflect.DeepEqual(got.CustomRuleIDs, []string{"rev1"}) {
		t.Errorf("custom_rule_ids = %v, want [rev1]", got.CustomRuleIDs)
	}
}

func TestBatch_ItemWithoutASetIsJudgedAsBefore(t *testing.T) {
	var calls []loadCall
	rec := runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices":         []any{item("a", "e1"), item("b")},
		"custom_rule_sets": map[string]any{"e1": []any{requiredEmail("rev1", "custom:r1")}},
	})
	body := wantOK(t, rec)
	base := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{"invoices": []any{item("b")}}))
	if !reflect.DeepEqual(body.Results[1].Violations, base.Results[0].Violations) {
		t.Errorf("item b violations = %+v, want the no-sets run's %+v", body.Results[1].Violations, base.Results[0].Violations)
	}
	if len(body.Results[1].CustomRuleIDs) != 0 || !strings.Contains(rec.Body.String(), `"custom_rule_ids":[]`) {
		t.Errorf("item b custom_rule_ids must be present and empty (body=%s)", rec.Body.String())
	}
}

func TestBatch_EachItemGetsOnlyItsOwnSet(t *testing.T) {
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices": []any{item("a", "e1"), item("b", "e2")},
		"custom_rule_sets": map[string]any{
			"e1": []any{requiredEmail("rev1", "custom:r1")},
			"e2": []any{cwire("rev2", "custom:r2", TypeRequired, "buyer.phone", `{}`, "warning", "phone needed")},
		},
	}))
	for i, c := range []struct{ key, id string }{{"custom:r1", "rev1"}, {"custom:r2", "rev2"}} {
		got := body.Results[i]
		if !reflect.DeepEqual(batchKeys(got.Violations), []string{"buyer-tin-format", c.key, "invoice-number-required"}) {
			t.Errorf("item %s keys = %v, want only %s", got.Ref, batchKeys(got.Violations), c.key)
		}
		if !reflect.DeepEqual(got.CustomRuleIDs, []string{c.id}) {
			t.Errorf("item %s custom_rule_ids = %v, want [%s]", got.Ref, got.CustomRuleIDs, c.id)
		}
	}
}

func TestBatch_ItemNamingTwoSetsGetsBoth(t *testing.T) {
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices": []any{item("both", "firm", "e1"), item("firm-only", "firm")},
		"custom_rule_sets": map[string]any{
			"firm": []any{cwire("rev-f", "custom:f", TypeRequired, "buyer.phone", `{}`, "error", "phone needed")},
			"e1":   []any{requiredEmail("rev1", "custom:r1")},
		},
	}))
	if !reflect.DeepEqual(body.Results[0].CustomRuleIDs, []string{"rev-f", "rev1"}) {
		t.Errorf("both: custom_rule_ids = %v, want [rev-f rev1]", body.Results[0].CustomRuleIDs)
	}
	if !reflect.DeepEqual(batchKeys(body.Results[0].Violations), []string{"buyer-tin-format", "custom:f", "custom:r1", "invoice-number-required"}) {
		t.Errorf("both: keys = %v", batchKeys(body.Results[0].Violations))
	}
	if !reflect.DeepEqual(body.Results[1].CustomRuleIDs, []string{"rev-f"}) ||
		!reflect.DeepEqual(batchKeys(body.Results[1].Violations), []string{"buyer-tin-format", "custom:f", "invoice-number-required"}) {
		t.Errorf("firm-only: got %v %v, want firm's rule only", batchKeys(body.Results[1].Violations), body.Results[1].CustomRuleIDs)
	}
}

func goldenOnly(vs []Violation) []Violation {
	out := []Violation{}
	for _, v := range vs {
		if !strings.HasPrefix(v.RuleKey, customKeyPrefix) {
			out = append(out, v)
		}
	}
	return out
}

// Needs the real active rule set, so it runs with the DB suite.
func TestBatch_CustomRulesNeverChangeGoldenViolations(t *testing.T) {
	_, app := dbTestPools(t)
	real, err := NewStore(app).LoadForDates(context.Background(), []string{activeSeedDate})
	if err != nil {
		t.Fatal(err)
	}
	rs := real[activeSeedDate]
	inv := badInvoicePayload()["invoice"].(map[string]any)
	one := func(sets map[string]any) []Violation {
		var calls []loadCall
		reqItem := map[string]any{"ref": "a", "invoice": inv}
		if sets != nil {
			reqItem["custom_rule_sets"] = []string{"s"}
		}
		body := wantOK(t, runCustomBatch(t, rs, &calls, map[string]any{"invoices": []any{reqItem}, "custom_rule_sets": sets}))
		return body.Results[0].Violations
	}
	baseline := one(nil)
	if len(baseline) < 2 {
		t.Fatalf("baseline has %d violations, want the invoice to fail at least two golden rules: %+v", len(baseline), baseline)
	}
	when := "invoice.currency == 'USD'"
	warnSameTarget := cwire("rev-a", "custom:a", TypeFormat, "supplier.tin", `{"pattern":"^ZZZ$"}`, "warning", "warn")
	falseWhen := cwire("rev-b", "custom:b", TypeRequired, "buyer.email", `{}`, "error", "email needed")
	falseWhen["when"] = when
	faulting := cwire("rev-c", "custom:c", TypeCEL, "supplier.tin", `{"expr":"invoice.missing.x == 1"}`, "error", "boom")
	for name, rule := range map[string]map[string]any{"warning on the same target": warnSameTarget, "false when": falseWhen, "fault": faulting} {
		t.Run(name, func(t *testing.T) {
			got := one(map[string]any{"s": []any{rule}})
			if !reflect.DeepEqual(goldenOnly(got), baseline) {
				t.Errorf("golden violations changed:\n got  %+v\n want %+v", goldenOnly(got), baseline)
			}
		})
	}
}

func TestBatch_InvalidCustomSetIs400WithNoLoad(t *testing.T) {
	ok := func() map[string]any { return requiredEmail("rev1", "custom:r1") }
	mod := func(k string, v any) map[string]any { r := ok(); r[k] = v; return r }
	cases := []struct {
		name  string
		items []any
		sets  map[string]any
	}{
		{"unknown set key", []any{item("a", "nope")}, map[string]any{"e1": []any{ok()}}},
		{"item names a set twice", []any{item("a", "e1", "e1")}, map[string]any{"e1": []any{ok()}}},
		{"rule id in two sets", []any{item("a", "firm")}, map[string]any{"firm": []any{ok()}, "e1": []any{mod("key", "custom:r2")}}},
		{"empty set", []any{item("a", "e1")}, map[string]any{"e1": []any{}}},
		{"golden-looking key", []any{item("a", "e1")}, map[string]any{"e1": []any{mod("key", "supplier-tin-required")}}},
		{"key without prefix", []any{item("a", "e1")}, map[string]any{"e1": []any{mod("key", "r1")}}},
		{"duplicate key", []any{item("a", "e1")}, map[string]any{"e1": []any{ok(), mod("id", "rev2")}}},
		{"severity info", []any{item("a", "e1")}, map[string]any{"e1": []any{mod("severity", "info")}}},
		{"unknown type", []any{item("a", "e1")}, map[string]any{"e1": []any{mod("type", "nope")}}},
		{"empty id", []any{item("a", "e1")}, map[string]any{"e1": []any{mod("id", "")}}},
		{"empty message", []any{item("a", "e1")}, map[string]any{"e1": []any{mod("message", "")}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []loadCall
			rec := runCustomBatch(t, goldenFake(), &calls, map[string]any{"invoices": c.items, "custom_rule_sets": c.sets})
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			if len(calls) != 0 {
				t.Errorf("loader called %d times, want 0", len(calls))
			}
		})
	}
}

func TestBatch_FaultingCustomRuleIsAViolation(t *testing.T) {
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices":         []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{cwire("rev1", "custom:r", TypeCEL, "buyer.email", `{"expr":"invoice.missing.x == 1"}`, "error", "own message")}},
	}))
	var custom []Violation
	for _, v := range body.Results[0].Violations {
		if v.RuleKey == "custom:r" {
			custom = append(custom, v)
		}
	}
	if len(custom) != 1 || custom[0].Severity != "error" || custom[0].Path != "buyer.email" || custom[0].Message != customFaultMessage {
		t.Fatalf("custom violations = %+v, want one error at buyer.email with customFaultMessage", custom)
	}
	if !reflect.DeepEqual(goldenOnly(body.Results[0].Violations), []Violation{
		{RuleKey: "buyer-tin-format", Severity: "error", Message: "bad buyer tin", Path: "buyer.tin", Expected: ptr("^[0-9]{8}-[0-9]{4}$")},
		{RuleKey: "invoice-number-required", Severity: "error", Message: "number needed", Path: "invoice_number"},
	}) {
		t.Errorf("golden violations lost: %+v", goldenOnly(body.Results[0].Violations))
	}
}

func TestBatch_FaultingGoldenRuleIsStill500(t *testing.T) {
	rs := goldenFake()
	rs.Rules[0].Params = json.RawMessage(`{"pattern":"("}`)
	var calls []loadCall
	rec := runCustomBatch(t, rs, &calls, map[string]any{
		"invoices":         []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{requiredEmail("rev1", "custom:r1")}},
	})
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "results") {
		t.Errorf("got %d %s, want 500 with no results", rec.Code, rec.Body.String())
	}
}

func TestBatch_CustomSeverityPassesThrough(t *testing.T) {
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices": []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{
			requiredEmail("rev1", "custom:e"),
			cwire("rev2", "custom:w", TypeRequired, "buyer.phone", `{}`, "warning", "phone needed"),
		}},
	}))
	got := map[string]Severity{}
	for _, v := range body.Results[0].Violations {
		got[v.RuleKey] = v.Severity
	}
	if got["custom:e"] != "error" || got["custom:w"] != "warning" {
		t.Errorf("severities = %v, want custom:e error and custom:w warning", got)
	}
}

func TestBatch_OneLoaderCallWithCustomRules(t *testing.T) {
	var calls []loadCall
	mk := func(ref, date string, sets ...string) map[string]any {
		return map[string]any{"ref": ref, "invoice": map[string]any{"issue_date": date}, "custom_rule_sets": sets}
	}
	wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices": []any{mk("a", "2027-01-01", "firm"), mk("b", "2027-02-01", "e1"), mk("c", "2027-03-01", "firm", "e1")},
		"custom_rule_sets": map[string]any{
			"firm": []any{requiredEmail("rev1", "custom:r1")},
			"e1":   []any{cwire("rev2", "custom:r2", TypeRequired, "buyer.phone", `{}`, "warning", "phone needed")},
		},
	}))
	if len(calls) != 1 {
		t.Fatalf("loader calls = %d, want 1", len(calls))
	}
	if len(calls[0].dates) != 3 {
		t.Errorf("dates = %v, want the 3 distinct dates", calls[0].dates)
	}
	if len(calls[0].custom) != 2 {
		t.Fatalf("custom rules = %d, want both sets flattened", len(calls[0].custom))
	}
	for _, r := range calls[0].custom {
		if !r.Enabled || r.Scope != "document" {
			t.Errorf("rule %s Enabled=%v Scope=%q, want true and document", r.Key, r.Enabled, r.Scope)
		}
	}
}

func TestBatch_GoldenAndCustomViolationsAreSorted(t *testing.T) {
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices": []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{
			cwire("rev-f", "custom:f", TypeRequired, "buyer.phone", `{}`, "error", "phone needed"),
			cwire("rev-0", "custom:0", TypeRequired, "buyer.email", `{}`, "error", "email needed"),
		}},
	}))
	want := []string{"buyer-tin-format", "custom:0", "custom:f", "invoice-number-required"}
	if !reflect.DeepEqual(batchKeys(body.Results[0].Violations), want) {
		t.Errorf("order = %v, want %v", batchKeys(body.Results[0].Violations), want)
	}
}

func TestBatch_FaultingCustomRuleKeepsItsWarningSeverityAndOtherRulesRun(t *testing.T) {
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices": []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{
			cwire("rev1", "custom:w", TypeCEL, "buyer.email", `{"expr":"invoice.missing.x == 1"}`, "warning", "own"),
			requiredEmail("rev2", "custom:ok"),
		}},
	}))
	vs := body.Results[0].Violations
	if got := byKey(vs, "custom:w"); len(got) != 1 || got[0].Severity != "warning" || got[0].Message != customFaultMessage {
		t.Errorf("faulting warning rule = %+v, want one warning with customFaultMessage", got)
	}
	if got := byKey(vs, "custom:ok"); len(got) != 1 || got[0].Message != "email needed" {
		t.Errorf("sibling rule = %+v, want judged as usual", got)
	}
}

func TestBatch_GoldenErrorAndCustomWarningOnTheSameTargetBothReport(t *testing.T) {
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices":         []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{cwire("rev1", "custom:n", TypeRequired, "invoice_number", `{}`, "warning", "warn")}},
	}))
	vs := body.Results[0].Violations
	g, c := byKey(vs, "invoice-number-required"), byKey(vs, "custom:n")
	if len(g) != 1 || g[0].Severity != "error" || len(c) != 1 || c[0].Severity != "warning" || c[0].Path != "invoice_number" {
		t.Errorf("golden %+v custom %+v, want both reported with their own severity", g, c)
	}
}

func TestBatch_ItemsNamingNoSetNeedNoSets(t *testing.T) {
	for name, sets := range map[string]any{"absent": nil, "empty map": map[string]any{}} {
		t.Run(name, func(t *testing.T) {
			var calls []loadCall
			body := map[string]any{"invoices": []any{item("a")}}
			if sets != nil {
				body["custom_rule_sets"] = sets
			}
			got := wantOK(t, runCustomBatch(t, goldenFake(), &calls, body))
			if len(got.Results) != 1 || len(got.Results[0].Violations) == 0 || len(got.Results[0].CustomRuleIDs) != 0 || len(calls) != 1 || len(calls[0].custom) != 0 {
				t.Errorf("results=%+v calls=%d, want golden violations, no custom ids, one load with no custom rules", got.Results, len(calls))
			}
		})
	}
}

func TestBatch_ItemNamingASetWithNoSetsMapIs400(t *testing.T) {
	var calls []loadCall
	rec := runCustomBatch(t, goldenFake(), &calls, map[string]any{"invoices": []any{item("a", "e1")}})
	if rec.Code != http.StatusBadRequest || len(calls) != 0 {
		t.Errorf("got %d with %d loads, want 400 and 0", rec.Code, len(calls))
	}
}

func TestBatch_UnusedInvalidSetIs400(t *testing.T) {
	var calls []loadCall
	rec := runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices":         []any{item("a")},
		"custom_rule_sets": map[string]any{"e1": []any{cwire("rev1", "custom:r", TypeRequired, "x", `{}`, "info", "m")}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an invalid set no item names", rec.Code)
	}
}

func TestBatch_DuplicateKeyAcrossSetsIs400WithNoLoad(t *testing.T) {
	var calls []loadCall
	rec := runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices": []any{item("a", "firm", "e1")},
		"custom_rule_sets": map[string]any{
			"firm": []any{requiredEmail("rev1", "custom:same")},
			"e1":   []any{requiredEmail("rev2", "custom:same")},
		},
	})
	if rec.Code != http.StatusBadRequest || len(calls) != 0 {
		t.Errorf("got %d with %d loader calls, want 400 and 0 (body=%s)", rec.Code, len(calls), rec.Body.String())
	}
}

func heavyRule(id, key string) map[string]any {
	return cwire(id, key, TypeCEL, "buyer.email", celParams(nested100(5)), "warning", "heavy")
}

func faultsFor(vs []Violation, key string) []Violation {
	var out []Violation
	for _, v := range vs {
		if v.RuleKey == key {
			out = append(out, v)
		}
	}
	return out
}

func withoutKey(vs []Violation, key string) []Violation {
	var out []Violation
	for _, v := range vs {
		if v.RuleKey != key {
			out = append(out, v)
		}
	}
	return out
}

func TestBatch_OverLimitCustomRuleFaultsAloneAndOthersRun(t *testing.T) {
	items := []any{item("a", "e1"), item("b", "e1")}
	var calls []loadCall
	start := time.Now()
	with := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices":         items,
		"custom_rule_sets": map[string]any{"e1": []any{heavyRule("rev1", "custom:heavy"), requiredEmail("rev2", "custom:email")}},
	}))
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("batch took %v, want under 10s", d)
	}
	without := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices":         items,
		"custom_rule_sets": map[string]any{"e1": []any{requiredEmail("rev2", "custom:email")}},
	}))
	for i, res := range with.Results {
		heavy := faultsFor(res.Violations, "custom:heavy")
		if len(heavy) != 1 || heavy[0].Message != customFaultMessage || heavy[0].Severity != "warning" {
			t.Errorf("item %d heavy violations = %+v, want one warning customFaultMessage", i, heavy)
		}
		if !reflect.DeepEqual(withoutKey(res.Violations, "custom:heavy"), without.Results[i].Violations) {
			t.Errorf("item %d other violations changed: %+v vs %+v", i, withoutKey(res.Violations, "custom:heavy"), without.Results[i].Violations)
		}
	}
}

func TestBatch_OverLimitCustomWhenFaultsAlone(t *testing.T) {
	gated := cwire("rev1", "custom:gated", TypeRequired, "buyer.email", `{}`, "error", "email needed")
	gated["when"] = nested100(3) + " && true"
	var calls []loadCall
	body := wantOK(t, runCustomBatch(t, goldenFake(), &calls, map[string]any{
		"invoices":         []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{gated, requiredEmail("rev2", "custom:email")}},
	}))
	vs := body.Results[0].Violations
	if f := faultsFor(vs, "custom:gated"); len(f) != 1 || f[0].Message != customFaultMessage {
		t.Errorf("gated violations = %+v, want one fault", f)
	}
	if f := faultsFor(vs, "custom:email"); len(f) != 1 || f[0].Message != "email needed" {
		t.Errorf("sibling violations = %+v, want its own violation", f)
	}
	if len(goldenOnly(vs)) != 2 {
		t.Errorf("golden violations = %+v, want 2", goldenOnly(vs))
	}
}

func logLine(logs, key string) string {
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, "custom rule fault") && strings.Contains(l, "rule_key="+key) {
			return l
		}
	}
	return ""
}

func TestBatch_CustomFaultLogNamesTheCostLimit(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	raw, _ := json.Marshal(map[string]any{
		"invoices": []any{item("a", "e1")},
		"custom_rule_sets": map[string]any{"e1": []any{heavyRule("rev1", "custom:heavy"),
			cwire("rev2", "custom:broken", TypeCEL, "buyer.email", `{"expr":"invoice.missing.x == 1"}`, "error", "m")}},
	})
	var calls []loadCall
	rec := httptest.NewRecorder()
	BatchValidateHandler(recordingLoader(goldenFake(), &calls), NewDefaultEngine(), nil, log).
		ServeHTTP(rec, httptest.NewRequest("POST", "/v1/validate/batch", bytes.NewReader(raw)))
	wantOK(t, rec)
	if l := logLine(buf.String(), "custom:heavy"); !strings.Contains(l, "fault=cost_limit") {
		t.Errorf("heavy log line = %q, want fault=cost_limit", l)
	}
	if l := logLine(buf.String(), "custom:broken"); !strings.Contains(l, "fault=error") {
		t.Errorf("broken log line = %q, want fault=error", l)
	}
}

func TestCustomSets_EvaluateReturnsContextError(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	eng := NewDefaultEngine()
	cs, err := newCustomSets(eng, map[string][]customRuleWire{"e1": {
		{ID: "r1", Key: "custom:c", Type: TypeCEL, Target: "buyer.email", Params: json.RawMessage(`{"expr":"invoice.missing.x == 1"}`), Severity: "error", Message: "m"},
		{ID: "r2", Key: "custom:r", Type: TypeRequired, Target: "buyer.email", Params: json.RawMessage(`{}`), Severity: "error", Message: "m"},
	}}, []batchItem{{Ref: "a", CustomRuleSets: []string{"e1"}}})
	if err != nil {
		t.Fatal(err)
	}
	vs, _, err := cs.evaluate(cancelledCtx(), eng, Payload{"invoice": failingInvoice()}, goldenFake(), []string{"e1"}, log)
	if !errors.Is(err, context.Canceled) || len(vs) != 0 {
		t.Errorf("vs=%+v err=%v, want Canceled and no violations", vs, err)
	}
	if strings.Contains(buf.String(), "custom rule fault") {
		t.Errorf("log = %q, want no fault line", buf.String())
	}
}

func TestBatch_ContextEndDuringCustomRulesStopsTheBatch(t *testing.T) {
	// 100 x 100 x 10 nested: cost 641,411, under the limit, about 0.1s per run
	slow := celParams(nestedAll(100, 100, 10))
	rule := cwire("rev1", "custom:slow", TypeCEL, "buyer.email", slow, "error", "slow")
	var w customRuleWire
	raw, _ := json.Marshal(rule)
	_ = json.Unmarshal(raw, &w)
	one := Rule{Key: w.Key, Type: w.Type, Target: w.Target, Params: w.Params, Severity: w.Severity, Message: w.Message, Scope: "document", Enabled: true}
	if _, err := NewDefaultEngine().EvaluateContext(limited(), emptyInvoice(), RuleSet{Rules: []Rule{one}}); err != nil {
		t.Fatalf("the slow rule must run under the limit: %v", err)
	}
	items := make([]any, 200)
	for i := range items {
		items[i] = item(fmt.Sprint(i), "e1")
	}
	body, _ := json.Marshal(map[string]any{"invoices": items, "custom_rule_sets": map[string]any{"e1": []any{rule}}})
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var calls []loadCall
	rec := httptest.NewRecorder()
	start := time.Now()
	BatchValidateHandler(recordingLoader(goldenFake(), &calls), NewDefaultEngine(), nil, log).
		ServeHTTP(rec, httptest.NewRequest("POST", "/v1/validate/batch", bytes.NewReader(body)).WithContext(ctx))
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("handler took %v, want under 5s", d)
	}
	if strings.Contains(rec.Body.String(), "results") {
		t.Errorf("body = %q, want no results", rec.Body.String())
	}
	if l := buf.String(); strings.Contains(l, "custom rule fault") || strings.Contains(l, "ERROR") {
		t.Errorf("log = %q, want no fault and no error line", l)
	}
}

func TestBatch_GoldenRuleAboveTheLimitIsStillJudged(t *testing.T) {
	lines := make([]any, 500)
	for i := range lines {
		lines[i] = map[string]any{"id": fmt.Sprint(i + 1)}
	}
	lines[499] = map[string]any{"id": "499"} // the last two share an id, so the all() runs to the end
	rs := RuleSet{ID: "g-id", Version: 7, Rules: []Rule{celRule("no-duplicate-line-items", "line_items", dupIDsExpr)}}
	var calls []loadCall
	rec := runCustomBatch(t, rs, &calls, map[string]any{
		"invoices": []any{map[string]any{"ref": "a", "invoice": map[string]any{"line_items": lines}}},
	})
	body := wantOK(t, rec)
	if got := batchKeys(body.Results[0].Violations); !reflect.DeepEqual(got, []string{"no-duplicate-line-items"}) {
		t.Errorf("violations = %v, want the duplicate-line violation", got)
	}
}
