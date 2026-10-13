package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func listRule(target, params string, codes ...string) Rule {
	r := Rule{
		Key: "list-rule", Type: TypeEnum, Target: target, Params: json.RawMessage(params),
		Severity: "error", Message: "bad code", Scope: "document", Enabled: true,
	}
	if codes != nil {
		r.Codes = CodeSet{}
		for _, c := range codes {
			r.Codes[c] = struct{}{}
		}
	}
	return r
}

func evalListRule(t *testing.T, r Rule, invoice map[string]any) (Result, error) {
	t.Helper()
	return NewDefaultEngine().Evaluate(Payload{"invoice": invoice}, RuleSet{Version: 1, Rules: []Rule{r}})
}

func lgaRule() Rule {
	return listRule("supplier.postal_address.lga", `{"list":"lgas"}`, "NG-AB-ANO", "NG-AB-ASO", "NG-LA-IKJ")
}

func lgaInvoice(v any) map[string]any {
	return map[string]any{"supplier": map[string]any{"postal_address": map[string]any{"lga": v}}}
}

func mustEvalList(t *testing.T, r Rule, inv map[string]any) Result {
	t.Helper()
	res, err := evalListRule(t, r, inv)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}

func wantOneViolation(t *testing.T, res Result, expected, actual string) {
	t.Helper()
	if len(res.Violations) != 1 {
		t.Fatalf("violations = %d, want 1: %+v", len(res.Violations), res.Violations)
	}
	v := res.Violations[0]
	if v.Expected == nil || *v.Expected != expected || v.Actual == nil || *v.Actual != actual {
		t.Errorf("violation = %+v, want expected %q actual %q", v, expected, actual)
	}
}

func TestEnumList_CodeInListPasses(t *testing.T) {
	if res := mustEvalList(t, lgaRule(), lgaInvoice("NG-AB-ASO")); len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none", res.Violations)
	}
}

func TestEnumList_CodeNotInListViolates(t *testing.T) {
	for _, v := range []string{"NG-AB-XXX", "ng-ab-aso", "NG-AB-ASO "} {
		t.Run(v, func(t *testing.T) {
			wantOneViolation(t, mustEvalList(t, lgaRule(), lgaInvoice(v)), "NRS list: lgas", v)
		})
	}
}

func TestEnumList_NonStringValueViolates(t *testing.T) {
	wantOneViolation(t, mustEvalList(t, lgaRule(), lgaInvoice(float64(566))), "NRS list: lgas", "566")
}

func TestEnumList_AbsentValuePasses(t *testing.T) {
	for name, inv := range map[string]map[string]any{"absent": {}, "null": lgaInvoice(nil)} {
		t.Run(name, func(t *testing.T) {
			if res := mustEvalList(t, lgaRule(), inv); len(res.Violations) != 0 {
				t.Errorf("violations = %+v, want none", res.Violations)
			}
		})
	}
}

func TestEnumList_ListNotLoadedFailsLoud(t *testing.T) {
	for name, codes := range map[string]CodeSet{"nil": nil, "empty": {}} {
		for inv, payload := range map[string]map[string]any{"present": lgaInvoice("NG-AB-ANO"), "absent": {}} {
			t.Run(name+"/"+inv, func(t *testing.T) {
				r := lgaRule()
				r.Codes = codes
				res, err := evalListRule(t, r, payload)
				if err == nil || !strings.Contains(err.Error(), "lgas") || !strings.Contains(err.Error(), "not loaded") {
					t.Fatalf("err = %v, want one naming lgas and 'not loaded'", err)
				}
				if res.RuleSetVersion != 0 || res.Violations != nil {
					t.Errorf("result = %+v, want zero", res)
				}
			})
		}
	}
}

func TestEnumList_ValuesAndListIsConfigError(t *testing.T) {
	r := listRule("supplier.postal_address.lga", `{"values":["NG-AB-ANO"],"list":"lgas"}`, "NG-AB-ANO")
	for name, inv := range map[string]map[string]any{"present": lgaInvoice("NG-AB-ANO"), "absent": {}} {
		t.Run(name, func(t *testing.T) {
			if _, err := evalListRule(t, r, inv); err == nil || !strings.Contains(err.Error(), "values and list") {
				t.Fatalf("err = %v, want 'values and list'", err)
			}
		})
	}
}

func TestEnumList_BlankListIsConfigError(t *testing.T) {
	r := listRule("supplier.postal_address.lga", `{"list":""}`, "NG-AB-ANO")
	for name, inv := range map[string]map[string]any{"present": lgaInvoice("NG-AB-ANO"), "absent": {}} {
		t.Run(name, func(t *testing.T) {
			if _, err := evalListRule(t, r, inv); err == nil {
				t.Fatal("err = nil, want a config error")
			}
		})
	}
}

func TestEnumList_BadItemsIsConfigError(t *testing.T) {
	invs := map[string]map[string]any{
		"present": {"line_items": []any{map[string]any{"hsn_code": "A"}}},
		"absent":  {},
	}
	for pname, params := range map[string]string{
		"items-with-values": `{"values":["A"],"items":"line_items"}`,
		"blank-items":       `{"list":"hs-codes","items":""}`,
	} {
		for iname, inv := range invs {
			t.Run(pname+"/"+iname, func(t *testing.T) {
				if _, err := evalListRule(t, listRule("hsn_code", params, "A"), inv); err == nil {
					t.Fatal("err = nil, want a config error")
				}
			})
		}
	}
}

func lineRule() Rule {
	return listRule("hsn_code", `{"list":"hs-codes","items":"line_items"}`, "0101.21", "0101.29")
}

func lines(hsn ...any) map[string]any {
	items := []any{}
	for _, h := range hsn {
		items = append(items, map[string]any{"hsn_code": h})
	}
	return map[string]any{"line_items": items}
}

func TestEnumList_LineCodesInListPass(t *testing.T) {
	if res := mustEvalList(t, lineRule(), lines("0101.21", "0101.29")); len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none", res.Violations)
	}
}

func TestEnumList_OneBadLineViolatesOnce(t *testing.T) {
	res := mustEvalList(t, lineRule(), lines("0101.21", "9999.99"))
	wantOneViolation(t, res, "NRS list: hs-codes", "9999.99")
	wantPaths(t, res.Violations, "line_items[2].hsn_code")
}

func TestEnumList_EveryBadLineIsReported(t *testing.T) {
	res := mustEvalList(t, lineRule(), lines("9999.98", "0101.21", "9999.99"))
	wantPaths(t, res.Violations, "line_items[1].hsn_code", "line_items[3].hsn_code")
	for i, want := range []string{"9999.98", "9999.99"} {
		if a := res.Violations[i].Actual; a == nil || *a != want {
			t.Errorf("violation %d actual = %v, want %q", i, a, want)
		}
	}
}

func TestEnumList_EveryOffenderIsReported(t *testing.T) {
	codes := make([]any, 150)
	for i := range codes {
		codes[i] = "9999.99"
	}
	res := mustEvalList(t, lineRule(), lines(codes...))
	if len(res.Violations) != 150 {
		t.Fatalf("violations = %d, want 150 (no per-rule cap)", len(res.Violations))
	}
	got := linePaths(res.Violations)
	want := make([]string, 150)
	for i := range want {
		want[i] = fmt.Sprintf("line_items[%d].hsn_code", i+1)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %q, want string order %q", got, want)
	}
}

func TestEnumList_NoLinesPasses(t *testing.T) {
	for name, inv := range map[string]map[string]any{
		"absent": {}, "null": {"line_items": nil}, "empty": {"line_items": []any{}}, "string": {"line_items": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			if res := mustEvalList(t, lineRule(), inv); len(res.Violations) != 0 {
				t.Errorf("violations = %+v, want none", res.Violations)
			}
		})
	}
}

func TestEnumList_LineWithoutFieldPasses(t *testing.T) {
	inv := map[string]any{"line_items": []any{map[string]any{}, map[string]any{"hsn_code": nil}}}
	if res := mustEvalList(t, lineRule(), inv); len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none", res.Violations)
	}
}

func TestEnumList_NonObjectLineViolates(t *testing.T) {
	res := mustEvalList(t, lineRule(), map[string]any{"line_items": []any{"0101.21"}})
	wantOneViolation(t, res, "NRS list: hs-codes", "0101.21")
	wantPaths(t, res.Violations, "line_items[1]")
}

func TestEnumList_ItemsReachTaxSubtotals(t *testing.T) {
	r := listRule("tax_category", `{"list":"tax-categories","items":"tax_subtotals"}`, "STANDARD_VAT", "ZERO_VAT")
	subtotals := func(cats ...string) map[string]any {
		items := []any{}
		for _, c := range cats {
			items = append(items, map[string]any{"tax_category": c})
		}
		return map[string]any{"tax_subtotals": items}
	}
	if res := mustEvalList(t, r, subtotals("STANDARD_VAT", "ZERO_VAT")); len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none", res.Violations)
	}
	res := mustEvalList(t, r, subtotals("STANDARD_VAT", "BOGUS"))
	wantOneViolation(t, res, "NRS list: tax-categories", "BOGUS")
	wantPaths(t, res.Violations, "tax_subtotals[2].tax_category")
}

func TestEnumList_InlineValuesUnchanged(t *testing.T) {
	r := listRule("currency", `{"values":["NGN"]}`)
	wantOneViolation(t, mustEvalList(t, r, map[string]any{"currency": "USD"}), "NGN", "USD")
}

func batchWithLoadErr(t *testing.T, loadErr error) (int, string, string) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	load := func(context.Context) (RuleSet, error) { return RuleSet{}, loadErr }
	rec := doBatchWithLog(t, load, log)
	return rec.Code, rec.Body.String(), buf.String()
}

func TestBatch_MissingCodeListAnswers503(t *testing.T) {
	code, body, logs := batchWithLoadErr(t, fmt.Errorf("%w: lgas", ErrCodeListMissing))
	if code != 503 || !strings.Contains(body, "no active rule-set") {
		t.Errorf("got %d %q, want 503 no active rule-set", code, body)
	}
	if strings.Count(logs, "\n") != 1 || !strings.Contains(logs, "load rule-set") || !strings.Contains(logs, "lgas") {
		t.Errorf("log = %q, want one line naming load rule-set and lgas", logs)
	}
}

func TestBatch_NoActiveRuleSetStaysUnlogged(t *testing.T) {
	code, _, logs := batchWithLoadErr(t, ErrNoActiveRuleSet)
	if code != 503 || logs != "" {
		t.Errorf("got %d, log %q; want 503 and no log", code, logs)
	}
}

func doBatchWithLog(t *testing.T, load func(context.Context) (RuleSet, error), log *slog.Logger) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(`{"invoices":[{"ref":"a","invoice":{}}]}`))
	rec := httptest.NewRecorder()
	BatchValidateHandler(allDates(load), NewDefaultEngine(), nil, log).ServeHTTP(rec, r)
	return rec
}

func TestEnumList_MixedObjectAndNonObjectLines(t *testing.T) {
	res := mustEvalList(t, lineRule(), map[string]any{"line_items": []any{
		"0101.21", map[string]any{"hsn_code": "9999.99"}, map[string]any{"hsn_code": "0101.21"}, 7.0,
	}})
	wantPaths(t, res.Violations, "line_items[1]", "line_items[2].hsn_code", "line_items[4]")
}

func TestEnumList_EvalReturnsFirstOfEvalLines(t *testing.T) {
	p := Payload{"invoice": lines("9999.98", "9999.99")}
	v, err := enumEval{}.Eval(p, lineRule())
	if err != nil || v == nil {
		t.Fatalf("Eval = %v, %v; want first violation", v, err)
	}
	if v.Path != "line_items[1].hsn_code" {
		t.Errorf("Eval path = %q, want line_items[1].hsn_code", v.Path)
	}
}
