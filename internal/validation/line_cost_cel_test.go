// De-risks the line-cost-non-negative CEL expression seeded by the
// 20260715120000_line_rules migration: runs the EXACT expr string through the
// production celEvaluator (pure Go, no DB) so a syntax slip that would 500
// every validation is caught here rather than only in the DB-backed golden/
// e2e suites. Keep the expr below byte-identical to the migration's params.
package validation

import (
	"encoding/json"
	"testing"
)

// lineCostExpr mirrors migrations/20260715120000_line_rules.sql verbatim.
const lineCostExpr = `!has(invoice.line_items) || invoice.line_items.all(x, !has(x.unit_price) || type(x.unit_price) != double || x.unit_price >= 0.0)`

func lineCostRule() Rule {
	params, _ := json.Marshal(map[string]string{"expr": lineCostExpr})
	return Rule{
		Key:      "line-cost-non-negative",
		Type:     TypeCEL,
		Params:   json.RawMessage(params),
		Severity: "error",
		Message:  "Line item cost must be zero or positive.",
	}
}

func lineCostPayload(items ...map[string]any) Payload {
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return Payload{"invoice": map[string]any{"line_items": list}}
}

func TestLineCostCEL(t *testing.T) {
	e := celEvaluator{}
	r := lineCostRule()

	t.Run("all non-negative costs pass", func(t *testing.T) {
		p := lineCostPayload(
			map[string]any{"id": "1", "unit_price": 100.0},
			map[string]any{"id": "2", "unit_price": 0.0},
		)
		v, err := mustEval(t, e, p, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error (expr must compile+run): %v", err)
		}
		if v != nil {
			t.Errorf("violation = %+v, want nil: 100 and 0 are both >= 0", v)
		}
	})

	t.Run("a negative cost fires", func(t *testing.T) {
		p := lineCostPayload(
			map[string]any{"id": "1", "unit_price": 100.0},
			map[string]any{"id": "2", "unit_price": -10.0},
		)
		v, err := mustEval(t, e, p, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("violation = nil, want non-nil: unit_price -10 is negative")
		}
	})

	t.Run("a non-numeric cost is skipped, NOT a 500", func(t *testing.T) {
		// The type() guard must let a string cost through without faulting the
		// >= comparison (payload-shape is another rule's concern).
		p := lineCostPayload(map[string]any{"id": "1", "unit_price": "abc"})
		v, err := mustEval(t, e, p, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v -- the type() guard must avoid a CEL comparison fault on a string cost", err)
		}
		if v != nil {
			t.Errorf("violation = %+v, want nil: a non-numeric cost is skipped by this rule", v)
		}
	})

	t.Run("no line_items -> passes", func(t *testing.T) {
		v, err := mustEval(t, e, Payload{"invoice": map[string]any{}}, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("violation = %+v, want nil: no line_items is not applicable", v)
		}
	})

	t.Run("a cost-less line -> passes (has() guard)", func(t *testing.T) {
		p := lineCostPayload(map[string]any{"id": "1", "description": "no cost"})
		v, err := mustEval(t, e, p, r)
		if err != nil {
			t.Fatalf("Eval() unexpected error: %v", err)
		}
		if v != nil {
			t.Errorf("violation = %+v, want nil: a line without unit_price is skipped", v)
		}
	})
}
