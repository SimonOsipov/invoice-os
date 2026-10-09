package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const dupIDsExpr = "!has(invoice.line_items) || invoice.line_items.all(x, !has(x.id) || invoice.line_items.filter(y, has(y.id) && y.id == x.id).size() <= 1)"

func celRule(key, target, expr string) Rule {
	params, _ := json.Marshal(map[string]string{"expr": expr})
	return Rule{Key: key, Type: TypeCEL, Target: target, Scope: "document", Enabled: true,
		Severity: "error", Message: key + " msg", Params: params}
}

func linesPayload(lines ...map[string]any) Payload {
	els := make([]any, len(lines))
	for i, l := range lines {
		els[i] = l
	}
	return Payload{"invoice": map[string]any{"line_items": els}}
}

func price(v float64) map[string]any { return map[string]any{"unit_price": v} }

func linePaths(vs []Violation) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Path)
	}
	return out
}

func mustLines(t *testing.T, p Payload, r Rule) []Violation {
	t.Helper()
	vs, err := celEvaluator{}.evalLines(p, r)
	if err != nil {
		t.Fatalf("evalLines: %v", err)
	}
	return vs
}

func wantPaths(t *testing.T, vs []Violation, want ...string) {
	t.Helper()
	if got := linePaths(vs); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestLinePath_Shapes(t *testing.T) {
	raw, err := os.ReadFile("testdata/line_paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		List  string `json:"list"`
		N     int    `json:"n"`
		Field string `json:"field"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("empty fixture")
	}
	for _, r := range rows {
		if got := linePath(r.List, r.N, r.Field); got != r.Path {
			t.Errorf("linePath(%q,%d,%q) = %q, want %q", r.List, r.N, r.Field, got, r.Path)
		}
	}
}

type fakeLineEval struct {
	vs  []Violation
	err error
}

func (f fakeLineEval) Eval(Payload, Rule) (*Violation, error) {
	panic("engine must prefer evalLines")
}
func (f fakeLineEval) evalLines(Payload, Rule) ([]Violation, error) { return f.vs, f.err }

func TestEngine_LineEvaluatorContributesEveryViolation(t *testing.T) {
	reg := map[RuleType]Evaluator{
		"plain": fakeEval{v: &Violation{RuleKey: "a", Path: "x"}},
		"lines": fakeLineEval{vs: []Violation{{RuleKey: "b", Path: "x[2]"}, {RuleKey: "b", Path: "x[1]"}}},
	}
	e := NewEngine(reg, newFakeGuard(true, nil))
	rs := RuleSet{Version: 1, Rules: []Rule{
		{Key: "b", Type: "lines", Scope: "document", Enabled: true},
		{Key: "a", Type: "plain", Scope: "document", Enabled: true},
	}}
	res, err := e.Evaluate(Payload{}, rs)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range res.Violations {
		got = append(got, v.RuleKey+" "+v.Path)
	}
	want := []string{"a x", "b x[1]", "b x[2]"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEngine_LineEvaluatorErrorFailsLoud(t *testing.T) {
	reg := map[RuleType]Evaluator{"lines": fakeLineEval{vs: []Violation{{RuleKey: "b"}}, err: errors.New("boom")}}
	e := NewEngine(reg, newFakeGuard(true, nil))
	res, err := e.Evaluate(Payload{}, RuleSet{Rules: []Rule{{Key: "b", Type: "lines", Scope: "document", Enabled: true}}})
	if err == nil || res.Violations != nil || res.RuleSetVersion != 0 {
		t.Fatalf("want Result{} and error, got %+v, %v", res, err)
	}
}

func TestLineCostCEL_TwoBadLinesTwoViolations(t *testing.T) {
	vs := mustLines(t, linesPayload(price(-1.0), price(5.0), price(-2.0)), celRule("line-cost", "line_items", lineCostExpr))
	wantPaths(t, vs, "line_items[1]", "line_items[3]")
	for _, v := range vs {
		if v.Message != "line-cost msg" || v.Severity != "error" || v.RuleKey != "line-cost" || v.Expected != nil || v.Actual != nil {
			t.Fatalf("violation = %+v", v)
		}
	}
}

func TestLineCostCEL_OneLineInvoice(t *testing.T) {
	wantPaths(t, mustLines(t, linesPayload(price(-1.0)), celRule("k", "line_items", lineCostExpr)), "line_items[1]")
}

func TestLineCostCEL_EveryLineFails(t *testing.T) {
	vs := mustLines(t, linesPayload(price(-1.0), price(-2.0), price(-3.0)), celRule("k", "line_items", lineCostExpr))
	wantPaths(t, vs, "line_items[1]", "line_items[2]", "line_items[3]")
}

func TestCEL_ShapeWithoutHasPrefix(t *testing.T) {
	r := celRule("k", "line_items", "invoice.line_items.all(x, x.unit_price >= 0.0)")
	wantPaths(t, mustLines(t, linesPayload(price(1.0), price(-1.0)), r), "line_items[2]")
}

func TestCEL_PassingLinesNoViolation(t *testing.T) {
	vs := mustLines(t, linesPayload(price(1.0), price(0.0)), celRule("k", "line_items", lineCostExpr))
	if len(vs) != 0 {
		t.Fatalf("got %v", vs)
	}
}

func TestCEL_DuplicateIdsStayBare(t *testing.T) {
	p := linesPayload(map[string]any{"id": "1"}, map[string]any{"id": "1"})
	wantPaths(t, mustLines(t, p, celRule("k", "line_items", dupIDsExpr)), "line_items")
}

func TestCEL_PositionalRuleStaysBare(t *testing.T) {
	r := celRule("k", "line_items", "invoice.line_items[0].unit_price >= 0.0")
	wantPaths(t, mustLines(t, linesPayload(price(-1.0), price(5.0), price(-2.0)), r), "line_items")
}

func TestCEL_ParityRuleStaysBare(t *testing.T) {
	r := celRule("k", "line_items", "invoice.line_items.size() % 2 == 0")
	wantPaths(t, mustLines(t, linesPayload(price(1.0), price(1.0), price(1.0)), r), "line_items")
}

func TestCEL_TargetMismatchStaysBare(t *testing.T) {
	wantPaths(t, mustLines(t, linesPayload(price(-1.0)), celRule("k", "subtotal", lineCostExpr)), "subtotal")
}

func TestCEL_EmptyTargetStaysBare(t *testing.T) {
	wantPaths(t, mustLines(t, linesPayload(price(-1.0)), celRule("k", "", lineCostExpr)), "")
}

func TestCEL_ScalarTargetStaysBare(t *testing.T) {
	p := Payload{"invoice": map[string]any{"total": -5.0}}
	wantPaths(t, mustLines(t, p, celRule("k", "total", "invoice.total > 0")), "total")
}

func TestCEL_AbsentOrEmptyListStaysBare(t *testing.T) {
	r := celRule("k", "line_items", "has(invoice.line_items) && invoice.line_items.size() > 0")
	wantPaths(t, mustLines(t, Payload{"invoice": map[string]any{}}, r), "line_items")
	wantPaths(t, mustLines(t, Payload{"invoice": map[string]any{"line_items": []any{}}}, r), "line_items")
}

func TestCEL_BrokenExprStillFailsLoud(t *testing.T) {
	vs, err := celEvaluator{}.evalLines(linesPayload(price(1.0)), celRule("k", "line_items", "invoice.line_items.all(x,"))
	if err == nil || len(vs) != 0 {
		t.Fatalf("want error and no violation, got %v, %v", vs, err)
	}
}

func TestCEL_RuntimeErrorStillFailsLoud(t *testing.T) {
	rs := RuleSet{Rules: []Rule{celRule("k", "line_items", "invoice.line_items.all(x, x.unit_price >= 0.0)")}}
	res, err := NewDefaultEngine().Evaluate(linesPayload(price(1.0), map[string]any{}), rs)
	if err == nil || !strings.Contains(err.Error(), "unit_price") || len(res.Violations) != 0 {
		t.Fatalf("got %+v, %v", res, err)
	}
}

func TestCEL_ErroringLineIsNotBlamed(t *testing.T) {
	r := celRule("k", "line_items", "invoice.line_items.all(x, x.unit_price >= 0.0)")
	wantPaths(t, mustLines(t, linesPayload(price(-1.0), map[string]any{}), r), "line_items[1]")
}

func TestCEL_EveryFailingLineIsNamed(t *testing.T) {
	lines := make([]map[string]any, 150)
	for i := range lines {
		lines[i] = price(-1.0)
	}
	rs := RuleSet{Rules: []Rule{celRule("k", "line_items", lineCostExpr)}}
	res, err := NewDefaultEngine().Evaluate(linesPayload(lines...), rs)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 150)
	for i := range want {
		want[i] = fmt.Sprintf("line_items[%d]", i+1)
	}
	sort.Strings(want)
	wantPaths(t, res.Violations, want...)
	if want[0] == "line_items[1]" {
		t.Fatalf("expected string order, not numeric: %q", want[:3])
	}
}

func TestLineCostCEL_TenLinesStringOrder(t *testing.T) {
	lines := make([]map[string]any, 10)
	for i := range lines {
		lines[i] = price(1.0)
	}
	lines[1], lines[9] = price(-1.0), price(-1.0)
	cur := Rule{Key: "currency-allowed", Type: TypeEnum, Target: "currency", Scope: "document", Enabled: true,
		Severity: "error", Message: "cur", Params: json.RawMessage(`{"values":["NGN"]}`)}
	rs := RuleSet{Rules: []Rule{celRule("line-cost-non-negative", "line_items", lineCostExpr), cur}}
	p := linesPayload(lines...)
	p["invoice"].(map[string]any)["currency"] = "USD"
	res, err := NewDefaultEngine().Evaluate(p, rs)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range res.Violations {
		got = append(got, v.RuleKey+" "+v.Path)
	}
	want := []string{"currency-allowed currency", "line-cost-non-negative line_items[10]", "line-cost-non-negative line_items[2]"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCEL_AttributionLeavesPayloadUntouched(t *testing.T) {
	p := linesPayload(price(-1.0), price(5.0))
	before := fmt.Sprintf("%#v", p)
	snapshot := linesPayload(price(-1.0), price(5.0))
	mustLines(t, p, celRule("k", "line_items", lineCostExpr))
	if after := fmt.Sprintf("%#v", p); after != before || !reflect.DeepEqual(p, snapshot) {
		t.Fatalf("payload mutated: %s", after)
	}
}

func TestCEL_NearMissShapesStayBare(t *testing.T) {
	p := Payload{"invoice": map[string]any{"min": 0.0, "other": 1.0, "line_items": []any{price(-1.0), price(5.0)}}}
	for name, expr := range map[string]string{
		"guard names another list": "!has(invoice.other) || invoice.line_items.all(x, x.unit_price >= 0.0)",
		"exists instead of all":    "invoice.line_items.exists(x, x.unit_price >= 100.0)",
		"body reads invoice":       "invoice.line_items.all(x, x.unit_price >= invoice.min)",
		"extra conjunct":           "invoice.line_items.all(x, x.unit_price >= 0.0) && invoice.min > 5.0",
	} {
		t.Run(name, func(t *testing.T) {
			wantPaths(t, mustLines(t, p, celRule("k", "line_items", expr)), "line_items")
		})
	}
}

func TestCEL_OtherListAndShadowedVariable(t *testing.T) {
	p := Payload{"invoice": map[string]any{
		"tax_subtotals": []any{map[string]any{"amount": 1.0}, map[string]any{"amount": -1.0}},
		"line_items":    []any{price(-1.0), price(2.0)},
	}}
	wantPaths(t, mustLines(t, p, celRule("k", "tax_subtotals", "invoice.tax_subtotals.all(s, s.amount >= 0.0)")), "tax_subtotals[2]")
	wantPaths(t, mustLines(t, p, celRule("k", "line_items", "invoice.line_items.all(invoice, invoice.unit_price >= 0.0)")), "line_items[1]")
}

func TestCEL_EvalReturnsFirstLineViolation(t *testing.T) {
	v, err := celEvaluator{}.Eval(linesPayload(price(1.0), price(-1.0), price(-2.0)), celRule("k", "line_items", lineCostExpr))
	if err != nil || v == nil || v.Path != "line_items[2]" {
		t.Fatalf("got %+v, %v", v, err)
	}
}
