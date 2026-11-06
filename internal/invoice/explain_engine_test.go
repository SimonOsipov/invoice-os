package invoice_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

type explainStub struct{ answer map[string]any }

func (s explainStub) Enabled() bool { return true }
func (s explainStub) Call(context.Context, ai.Request) (map[string]any, error) {
	return s.answer, nil
}

func lineCostViolations(t *testing.T, inv invoice.Invoice) []invoice.Violation {
	t.Helper()
	rule := ruleLineCostNonNegative()
	rule.Target = "line_items"
	res, err := validation.NewDefaultEngine().Evaluate(rooted(t, inv), validation.RuleSet{Rules: []validation.Rule{rule}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	raw, err := json.Marshal(res.Violations)
	if err != nil {
		t.Fatal(err)
	}
	var vs []invoice.Violation
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatal(err)
	}
	return vs
}

func TestExplain_BareLineFixClearsTheRule(t *testing.T) {
	lines := func(up, lt string, i int) invoice.LineItem {
		return invoice.LineItem{ID: "l", LineNo: i, UnitPrice: ptr(up), LineTotal: ptr(lt)}
	}
	build := func() invoice.Invoice {
		return invoice.Invoice{ID: "inv-1", LineItems: []invoice.LineItem{
			lines("105.00", "105.00", 1), lines("-5.00", "-5.00", 2), lines("0.00", "0.00", 3),
		}}
	}
	cases := []struct {
		name       string
		answer     map[string]any
		wantNoFix  bool
		wantNoLine bool
	}{
		{"model answers line_total", map[string]any{"explanation": "Line 2 is negative.", "fix_field": "line_total", "fix_value": "5.00"}, false, false},
		{"model answers null field", map[string]any{"explanation": "Line 2 is negative.", "fix_field": nil, "fix_value": "5.00"}, false, false},
		{"model repeats the value", map[string]any{"explanation": "Line 2 is negative.", "fix_field": "unit_price", "fix_value": "-5.00"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := build()
			vs := lineCostViolations(t, inv)
			if len(vs) != 1 || vs[0].Path != "line_items[2]" {
				t.Fatalf("verdict = %+v, want one violation at line_items[2]", vs)
			}
			raw, _ := json.Marshal(vs)
			inv.Violations = raw
			get := func(context.Context, string) (invoice.Invoice, error) { return inv, nil }
			got, err := invoice.NewExplainer(get, explainStub{tc.answer}).Explain(context.Background(), "inv-1",
				invoice.ViolationKey{RuleKey: "line-cost-non-negative", Path: "line_items[2]"})
			if err != nil || got.Status != "ok" || got.Explanation == nil {
				t.Fatalf("Explain = (%+v, %v), want ok with the explanation kept", got, err)
			}
			if tc.wantNoFix {
				if got.Fix != nil {
					t.Fatalf("fix = %+v, want nil", got.Fix)
				}
				return
			}
			if got.Fix == nil || got.Fix.Line == nil {
				t.Fatalf("fix = %+v, want a line fix", got.Fix)
			}
			if got.Fix.Field != "unit_price" {
				t.Errorf("fix.Field = %q, want unit_price", got.Fix.Field)
			}
			after := build()
			line := &after.LineItems[*got.Fix.Line-1]
			switch got.Fix.Field {
			case "unit_price":
				line.UnitPrice = ptr(got.Fix.Value)
			case "line_total":
				line.LineTotal = ptr(got.Fix.Value)
			}
			for _, v := range lineCostViolations(t, after) {
				if v.Path == "line_items[2]" {
					t.Errorf("%s still fires at line_items[2] after the fix %+v", v.RuleKey, got.Fix)
				}
			}
		})
	}
}
