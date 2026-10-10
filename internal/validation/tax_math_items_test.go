// Unit coverage for taxMathEval's per-element mode (items/rate_by/rates):
// each derived tax subtotal is judged at its own category rate. Pure Go, no DB.
// Runs through NewDefaultEngine so the lineEvaluator wiring is observed too.
package validation

import (
	"encoding/json"
	"reflect"
	"testing"
)

const taxItemsParams = `{"items":"tax_subtotals","base":"taxable_amount","expected":"tax_amount","rate_by":"tax_category","rates":{"STANDARD_VAT":0.075,"ZERO_VAT":0,"EXEMPTED":0},"tolerance":0.005}`

func taxItemsRule(params string) Rule {
	return Rule{
		Key: "vat-standard-rate", Type: TypeTaxMath, Target: "tax_subtotals",
		Params: json.RawMessage(params), Severity: "error", Scope: "document", Enabled: true,
		Message: "Tax must equal the category rate of the taxable amount.",
	}
}

// taxItemsPayload wraps a raw JSON value as invoice.tax_subtotals.
func taxItemsPayload(t *testing.T, subtotals string) Payload {
	t.Helper()
	var p Payload
	if err := json.Unmarshal([]byte(`{"invoice":{"tax_subtotals":`+subtotals+`}}`), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func sub(cat string, taxable, tax string) string {
	return `{"tax_category":"` + cat + `","taxable_amount":` + taxable + `,"tax_amount":` + tax + `}`
}

func evalTaxItems(t *testing.T, p Payload, params string) ([]Violation, error) {
	t.Helper()
	res, err := NewDefaultEngine().Evaluate(p, RuleSet{Version: 5, Rules: []Rule{taxItemsRule(params)}})
	return res.Violations, err
}

func mustEvalTaxItems(t *testing.T, subtotals string) []Violation {
	t.Helper()
	vs, err := evalTaxItems(t, taxItemsPayload(t, subtotals), taxItemsParams)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return vs
}

func wantExpectedActual(t *testing.T, v Violation, expected, actual string) {
	t.Helper()
	if v.Expected == nil || *v.Expected != expected || v.Actual == nil || *v.Actual != actual {
		t.Errorf("Expected/Actual = %v/%v, want %q/%q", ptrStr(v.Expected), ptrStr(v.Actual), expected, actual)
	}
}

func ptrStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestTaxMathItems_EachCategoryAtItsRate(t *testing.T) {
	vs := mustEvalTaxItems(t, `[`+sub("STANDARD_VAT", "1000", "75")+`,`+sub("ZERO_VAT", "500", "0")+`,`+sub("EXEMPTED", "200", "0")+`]`)
	if len(vs) != 0 {
		t.Fatalf("violations = %+v, want none", vs)
	}
}

func TestTaxMathItems_WrongAmountNamesTheSubtotal(t *testing.T) {
	vs := mustEvalTaxItems(t, `[`+sub("STANDARD_VAT", "1000", "75")+`,`+sub("STANDARD_VAT", "1000", "70")+`]`)
	if len(vs) != 1 {
		t.Fatalf("violations = %+v, want exactly 1", vs)
	}
	if vs[0].Path != "tax_subtotals[2]" {
		t.Errorf("Path = %q, want tax_subtotals[2]", vs[0].Path)
	}
	wantExpectedActual(t, vs[0], "75", "70")
}

func TestTaxMathItems_ZeroRateWithTaxViolates(t *testing.T) {
	vs := mustEvalTaxItems(t, `[`+sub("ZERO_VAT", "500", "37.5")+`]`)
	if len(vs) != 1 {
		t.Fatalf("violations = %+v, want exactly 1", vs)
	}
	wantExpectedActual(t, vs[0], "0", "37.5")
}

// 1.00 x 0.075 vs 0.08 is exactly 0.005 in decimal; float64 maths would flag it.
func TestTaxMathItems_HalfCentBoundary(t *testing.T) {
	if vs := mustEvalTaxItems(t, `[`+sub("STANDARD_VAT", "1.00", "0.08")+`]`); len(vs) != 0 {
		t.Errorf("1.00/0.08: violations = %+v, want none (diff exactly 0.005, inclusive)", vs)
	}
	vs := mustEvalTaxItems(t, `[`+sub("STANDARD_VAT", "1.00", "0.081")+`]`)
	if len(vs) != 1 {
		t.Fatalf("1.00/0.081: violations = %+v, want exactly 1 (diff 0.006)", vs)
	}
	wantExpectedActual(t, vs[0], "0.075", "0.081")
}

func TestTaxMathItems_UnratedCategoryIsSkipped(t *testing.T) {
	// A control element proves the rule ran: it is the only violation.
	vs := mustEvalTaxItems(t, `[`+
		sub("STAMP_DUTY", "1000", "999")+`,`+
		`{"taxable_amount":1000,"tax_amount":999},`+
		`{"tax_category":7,"taxable_amount":1000,"tax_amount":999},`+
		sub("STANDARD_VAT", "1000", "70")+`]`)
	if len(vs) != 1 || vs[0].Path != "tax_subtotals[4]" {
		t.Fatalf("violations = %+v, want only tax_subtotals[4]", vs)
	}
}

func TestTaxMathItems_NonObjectElement(t *testing.T) {
	vs := mustEvalTaxItems(t, `["x"]`)
	if len(vs) != 1 || vs[0].Path != "tax_subtotals[1]" {
		t.Fatalf("violations = %+v, want one at tax_subtotals[1]", vs)
	}
}

func TestTaxMathItems_MissingOperandNamesTheField(t *testing.T) {
	vs := mustEvalTaxItems(t, `[{"tax_category":"STANDARD_VAT","taxable_amount":1000}]`)
	if len(vs) != 1 || vs[0].Path != "tax_subtotals[1].tax_amount" {
		t.Fatalf("absent tax_amount: violations = %+v, want one at tax_subtotals[1].tax_amount", vs)
	}
	if vs[0].Expected != nil || vs[0].Actual != nil {
		t.Errorf("Expected/Actual = %v/%v, want both nil", ptrStr(vs[0].Expected), ptrStr(vs[0].Actual))
	}

	vs = mustEvalTaxItems(t, `[{"tax_category":"STANDARD_VAT","taxable_amount":"abc","tax_amount":75}]`)
	if len(vs) != 1 || vs[0].Path != "tax_subtotals[1].taxable_amount" {
		t.Fatalf("non-numeric taxable_amount: violations = %+v, want one at tax_subtotals[1].taxable_amount", vs)
	}
}

func TestTaxMathItems_NoItemsPasses(t *testing.T) {
	// A control payload with a wrong subtotal proves the rule is live.
	if vs := mustEvalTaxItems(t, `[`+sub("STANDARD_VAT", "1000", "70")+`]`); len(vs) != 1 {
		t.Fatalf("control: violations = %+v, want exactly 1", vs)
	}
	for name, p := range map[string]Payload{
		"absent": {"invoice": map[string]any{}},
		"null":   taxItemsPayload(t, `null`),
		"string": taxItemsPayload(t, `"x"`),
		"empty":  taxItemsPayload(t, `[]`),
	} {
		vs, err := evalTaxItems(t, p, taxItemsParams)
		if err != nil || len(vs) != 0 {
			t.Errorf("%s: violations = %+v, err = %v, want none", name, vs, err)
		}
	}
}

// Each case is the valid config with exactly one fault, so none can pass on
// another fault's error. The control case proves the base config is accepted.
func TestTaxMathItems_ConfigFaultsFailLoud(t *testing.T) {
	const (
		items = `"items":"tax_subtotals"`
		ops   = `"base":"taxable_amount","expected":"tax_amount"`
		by    = `"rate_by":"tax_category"`
		rates = `"rates":{"STANDARD_VAT":0.075}`
		tol   = `"tolerance":0.005`
	)
	faults := map[string]string{
		"rate and rates":      `{` + items + `,` + ops + `,` + by + `,` + rates + `,"rate":0.075,` + tol + `}`,
		"neither rate nor":    `{` + items + `,` + ops + `,` + by + `,` + tol + `}`,
		"empty rates":         `{` + items + `,` + ops + `,` + by + `,"rates":{},` + tol + `}`,
		"non-numeric rate":    `{` + items + `,` + ops + `,` + by + `,"rates":{"A":"x"},` + tol + `}`,
		"rates without by":    `{` + items + `,` + ops + `,` + rates + `,` + tol + `}`,
		"blank rate_by":       `{` + items + `,` + ops + `,"rate_by":"",` + rates + `,` + tol + `}`,
		"rates without items": `{` + ops + `,` + by + `,` + rates + `,` + tol + `}`,
		"rate_by w/o items":   `{` + ops + `,` + by + `,"rate":0.075,` + tol + `}`,
		"blank items":         `{"items":"",` + ops + `,"rate":0.075,` + tol + `}`,
		"negative tolerance":  `{` + items + `,` + ops + `,` + by + `,` + rates + `,"tolerance":-1}`,
	}
	payloads := map[string]Payload{
		"items present": taxItemsPayload(t, `[`+sub("STANDARD_VAT", "1000", "75")+`]`),
		"items absent":  {"invoice": map[string]any{}},
	}

	valid := `{` + items + `,` + ops + `,` + by + `,` + rates + `,` + tol + `}`
	for pn, p := range payloads {
		if _, err := evalTaxItems(t, p, valid); err != nil {
			t.Errorf("control (%s): valid config rejected: %v", pn, err)
		}
	}
	for fn, params := range faults {
		for pn, p := range payloads {
			if _, err := evalTaxItems(t, p, params); err == nil {
				t.Errorf("%s / %s: err = nil, want a config error", fn, pn)
			}
		}
	}
}

func TestTaxMathItems_EveryBadElementReported(t *testing.T) {
	subtotals := `[` + sub("STANDARD_VAT", "1000", "1") + `,` + sub("ZERO_VAT", "500", "2") + `,` + sub("EXEMPTED", "200", "3") + `]`

	vs := mustEvalTaxItems(t, subtotals)
	var paths []string
	for _, v := range vs {
		paths = append(paths, v.Path)
	}
	if want := []string{"tax_subtotals[1]", "tax_subtotals[2]", "tax_subtotals[3]"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}

	first, err := taxMathEval{}.Eval(taxItemsPayload(t, subtotals), taxItemsRule(taxItemsParams))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if first == nil || first.Path != "tax_subtotals[1]" {
		t.Fatalf("Eval = %+v, want the tax_subtotals[1] violation", first)
	}
}
