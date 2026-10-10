package invoice

import (
	"encoding/base64"
	"encoding/json"
	"go/format"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

func explainHarness(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "aimodeltest", "explainrun.py"))
	if err != nil {
		t.Fatalf("read explainrun.py: %v", err)
	}
	return string(b)
}

func explainPyString(t *testing.T, src, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)\n` + name + ` = """(.*?)"""`).FindStringSubmatch(src)
	if m == nil || m[1] == "" {
		t.Fatalf("%s triple-quoted literal not found or empty in explainrun.py", name)
	}
	return m[1]
}

func explainPyLine(t *testing.T, src, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + name + ` = "([^"]+)"$`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("%s line not found in explainrun.py", name)
	}
	return m[1]
}

func TestExplainSchema_PassesTheClientSchemaCheck(t *testing.T) {
	t.Setenv(ai.EnvFake, "true")
	t.Setenv(ai.EnvKey, "")
	client, err := ai.FromEnv(nil)
	if err != nil {
		t.Fatalf("ai.FromEnv: %v", err)
	}
	payload := `{"explanation":"x","fix_field":null,"fix_value":null}`
	answer, err := client.Call(t.Context(), ai.Request{
		Purpose:    ai.PurposeExplain,
		System:     explainSystem,
		Text:       "AIFAKE-EXPLAIN-ANSWER-" + base64.RawURLEncoding.EncodeToString([]byte(payload)),
		FakeScope:  "EXPLAIN",
		SchemaName: explainSchemaName,
		Schema:     explainSchema,
	})
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	want := map[string]any{"explanation": "x", "fix_field": nil, "fix_value": nil}
	if !reflect.DeepEqual(answer, want) {
		t.Errorf("answer = %v, want %v", answer, want)
	}
}

func TestExplainPrompt_MatchesTheMeasuredHarness(t *testing.T) {
	src := explainHarness(t)
	if got := explainPyString(t, src, "SYSTEM"); got != explainSystem {
		t.Errorf("explainrun.py SYSTEM differs from explainSystem:\n got %q\nwant %q", got, explainSystem)
	}
	if got := explainPyLine(t, src, "VIOLATION_INTRO"); got != explainViolationIntro {
		t.Errorf("VIOLATION_INTRO = %q, want %q", got, explainViolationIntro)
	}
	if got := explainPyLine(t, src, "INVOICE_INTRO"); got != explainInvoiceIntro {
		t.Errorf("INVOICE_INTRO = %q, want %q", got, explainInvoiceIntro)
	}
	m := regexp.MustCompile(`"response_format": \{"type": "json_schema", "json_schema": \{"name": "([^"]+)"`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("response_format line not found in explainrun.py")
	}
	if m[1] != explainSchemaName {
		t.Errorf("response_format name = %q, want %q", m[1], explainSchemaName)
	}
}

func TestExplainSchema_KeysMatchTheHarness(t *testing.T) {
	m := regexp.MustCompile(`"required": \[([^\]]*)\]`).FindStringSubmatch(explainHarness(t))
	if m == nil {
		t.Fatal("SCHEMA required list not found in explainrun.py")
	}
	var py []string
	for _, k := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		py = append(py, k[1])
	}
	var parsed struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(explainSchema, &parsed); err != nil {
		t.Fatalf("explainSchema: %v", err)
	}
	if len(py) == 0 || !reflect.DeepEqual(py, parsed.Required) {
		t.Errorf("harness required = %v, explainSchema required = %v", py, parsed.Required)
	}

	lit := regexp.MustCompile(`(?s)\nSCHEMA = (\{.*?\})\n\n\ndef `).FindStringSubmatch(explainHarness(t))
	if lit == nil {
		t.Fatal("SCHEMA literal not found in explainrun.py")
	}
	var pySchema, goSchema any
	if err := json.Unmarshal([]byte(strings.NewReplacer("False", "false", "True", "true").Replace(lit[1])), &pySchema); err != nil {
		t.Fatalf("harness SCHEMA: %v", err)
	}
	if err := json.Unmarshal(explainSchema, &goSchema); err != nil {
		t.Fatalf("explainSchema: %v", err)
	}
	if !reflect.DeepEqual(pySchema, goSchema) {
		t.Errorf("harness SCHEMA = %v, explainSchema = %v", pySchema, goSchema)
	}
}

func TestExplainPromptText_CarriesTheViolationAndPayload(t *testing.T) {
	exp, act := "75.00", "70.00"
	v := Violation{RuleKey: "vat-standard-rate", Severity: "error", Message: "VAT must be 7.5% of subtotal.", Path: "vat", Expected: &exp, Actual: &act}
	got := explainPromptText(v, map[string]any{"vat": 70, "subtotal": 1000})
	for _, want := range []string{
		`The violation:` + "\n" + `{"rule_key":"vat-standard-rate"`,
		`"message":"VAT must be 7.5% of subtotal."`,
		`"path":"vat"`,
		`"expected":"75.00"`,
		`"actual":"70.00"`,
		"}\n\nThe invoice, as the rule engine read it:\n{",
		`"vat":70`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text lacks %q:\n%s", want, got)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("text ends with a newline: %q", got)
	}
}

func TestExplainPromptText_NoHTMLEscaping(t *testing.T) {
	payload := map[string]any{"line_items": []any{map[string]any{"description": "A & B <x>"}}}
	got := explainPromptText(Violation{RuleKey: "r", Message: "A & B <x>"}, payload)
	if strings.Count(got, "A & B <x>") != 2 {
		t.Errorf("want message and description unescaped:\n%s", got)
	}
	if strings.Contains(got, `\u0026`) || strings.Contains(got, `\u003c`) {
		t.Errorf("text carries an HTML escape:\n%s", got)
	}
}

func TestExplainPromptText_OmitsAbsentExpected(t *testing.T) {
	got := explainPromptText(Violation{RuleKey: "buyer-tin-required", Message: "Buyer TIN is required.", Path: "buyer.tin"}, map[string]any{})
	if strings.Contains(got, `"expected"`) || strings.Contains(got, `"actual"`) {
		t.Errorf("text carries an absent expected or actual:\n%s", got)
	}
}

func TestExplainSystem_SurvivesGofmtAndCarriesNoCurledQuote(t *testing.T) {
	src, err := os.ReadFile("explain.go")
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(src)
	if err != nil {
		t.Fatalf("format.Source: %v", err)
	}
	if string(formatted) != string(src) {
		t.Error("explain.go is not gofmt-clean")
	}
	if !strings.Contains(string(src), "const explainSystem = `") {
		t.Error("explainSystem is not a backtick raw string constant")
	}
	if strings.ContainsAny(explainSystem, "\u201c\u201d\u2018\u2019") {
		t.Error("explainSystem carries a curled quote")
	}
}

func TestExplainTarget_HeaderPaths(t *testing.T) {
	for path, want := range map[string]string{
		"issue_date": "issue_date", "supplier.tin": "supplier_tin", "supplier.name": "supplier_name",
		"buyer.tin": "buyer_tin", "buyer.name": "buyer_name", "currency": "currency",
		"subtotal": "subtotal", "vat": "vat", "total": "total",
	} {
		field, line, ok := explainTarget(path, 2, "")
		if !ok || field != want || line != 0 {
			t.Errorf("%s: got (%q, %d, %v), want (%q, 0, true)", path, field, line, ok, want)
		}
	}
}

func TestExplainTarget_NonEditHeaderHasNoFix(t *testing.T) {
	for _, path := range []string{"invoice_number", "due_date", "buyer.state"} {
		if _, _, ok := explainTarget(path, 2, ""); ok {
			t.Errorf("%s: want no target", path)
		}
	}
}

func TestExplainTarget_LineFieldPath(t *testing.T) {
	if f, n, ok := explainTarget("line_items[2].unit_price", 3, ""); !ok || f != "unit_price" || n != 2 {
		t.Errorf("got (%q, %d, %v)", f, n, ok)
	}
	if _, _, ok := explainTarget("line_items[2].hsn_code", 3, "unit_price"); ok {
		t.Error("hsn_code is not an Edit line field")
	}
}

func TestExplainTarget_BareLineUsesModelField(t *testing.T) {
	if f, n, ok := explainTarget("line_items[2]", 3, "unit_price"); !ok || f != "unit_price" || n != 2 {
		t.Errorf("got (%q, %d, %v)", f, n, ok)
	}
	for _, model := range []string{"hsn_code", "", "buyer_tin"} {
		if _, _, ok := explainTarget("line_items[2]", 3, model); ok {
			t.Errorf("model field %q: want no target", model)
		}
	}
}

func TestExplainTarget_ModelCannotMoveAHeaderFix(t *testing.T) {
	if f, _, ok := explainTarget("vat", 2, "subtotal"); !ok || f != "vat" {
		t.Errorf("got (%q, %v), want vat", f, ok)
	}
}

func TestExplainTarget_OutOfRangeAndUnmappable(t *testing.T) {
	for _, path := range []string{"line_items[4]", "line_items[0]", "line_items[0].unit_price", "line_items[01].unit_price", "line_items", "tax_subtotals[1].tax_category", "line_items[1].", ""} {
		if _, _, ok := explainTarget(path, 3, "unit_price"); ok {
			t.Errorf("%q: want no target", path)
		}
	}
}

func TestParseLinePath_ReadsTheEngineFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "validation", "testdata", "line_paths.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		List  string `json:"list"`
		N     int    `json:"n"`
		Field string `json:"field"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	var lineRows, otherRows int
	for _, r := range rows {
		n, field, ok := parseLinePath(r.Path)
		if r.List == "line_items" {
			lineRows++
			if !ok || n != r.N || field != r.Field {
				t.Errorf("%s: got (%d, %q, %v), want (%d, %q, true)", r.Path, n, field, ok, r.N, r.Field)
			}
		} else {
			otherRows++
			if ok {
				t.Errorf("%s: want refused", r.Path)
			}
		}
	}
	if lineRows == 0 || otherRows == 0 {
		t.Fatalf("fixture has %d line rows and %d other rows", lineRows, otherRows)
	}
}

func explainAnswer(expl any, field, value any) map[string]any {
	return map[string]any{"explanation": expl, "fix_field": field, "fix_value": value}
}

func explainPayload() map[string]any {
	return map[string]any{
		"currency":   "USD",
		"issue_date": "2026-09-30",
		"supplier":   map[string]any{"tin": "12345678-0001"},
		"line_items": []any{
			map[string]any{"unit_price": json.Number("10.00")},
			map[string]any{"unit_price": json.Number("-5.00"), "description": "Toner"},
		},
	}
}

func TestGuardExplanation_LongOrLinkedIsDropped(t *testing.T) {
	v := Violation{Message: "m", Path: "currency"}
	for text, want := range map[string]string{
		strings.Repeat("a", 1201): "unavailable",
		strings.Repeat("a", 1200): "ok",
		"see https://x.example":   "unavailable",
		"visit www.x.example":     "unavailable",
		strings.Repeat("é", 1200): "ok",
	} {
		if got := guardExplanation(v, explainPayload(), explainAnswer(text, nil, nil)); got.Status != want {
			t.Errorf("%.20q (%d runes): status %s, want %s", text, len([]rune(text)), got.Status, want)
		}
	}
}

func TestGuardExplanation_MoneyValueMustBeDecimal(t *testing.T) {
	v := Violation{Message: "m", Path: "line_items[2].unit_price"}
	for value, wantFix := range map[string]bool{"5.00": true, "-5": false, "5,00": false, "1e3": false, "five": false, "": false} {
		got := guardExplanation(v, explainPayload(), explainAnswer("Why.", "unit_price", value))
		if (got.Fix != nil) != wantFix || got.Explanation == nil {
			t.Errorf("value %q: fix %v, explanation %v", value, got.Fix, got.Explanation)
		}
	}
	// "-5" is a valid decimal but equals the stored -5.00, so it also gives no fix
	if got := guardExplanation(v, explainPayload(), explainAnswer("Why.", "unit_price", "-6")); got.Fix == nil {
		t.Error("-6 is a valid decimal: want a fix")
	}
}

func TestGuardExplanation_DateValueMustBeISO(t *testing.T) {
	v := Violation{Message: "m", Path: "issue_date"}
	for value, wantFix := range map[string]bool{"2026-10-11": true, "11/10/2026": false, "2026-13-01": false} {
		got := guardExplanation(v, explainPayload(), explainAnswer("Why.", "issue_date", value))
		if (got.Fix != nil) != wantFix || got.Explanation == nil {
			t.Errorf("value %q: fix %v", value, got.Fix)
		}
	}
}

func TestGuardExplanation_TextValueBounds(t *testing.T) {
	v := Violation{Message: "m", Path: "buyer.name"}
	for value, wantFix := range map[string]bool{"  ": false, strings.Repeat("a", 201): false, strings.Repeat("a", 200): true, "Acme Ltd": true} {
		got := guardExplanation(v, explainPayload(), explainAnswer("Why.", "name", value))
		if (got.Fix != nil) != wantFix {
			t.Errorf("value %.12q (%d): fix %v", value, len(value), got.Fix)
		}
	}
}

func TestGuardExplanation_EqualValueIsNoFix(t *testing.T) {
	v := Violation{Message: "m", Path: "line_items[2].unit_price"}
	if got := guardExplanation(v, explainPayload(), explainAnswer("Why.", "unit_price", "-5")); got.Fix != nil {
		t.Errorf("fix %v, want none", got.Fix)
	}
	v = Violation{Message: "m", Path: "currency"}
	if got := guardExplanation(v, explainPayload(), explainAnswer("Why.", "currency", "USD")); got.Fix != nil {
		t.Errorf("text equal: fix %v, want none", got.Fix)
	}
}

func TestGuardExplanation_FixCarriesCurrentAndLabel(t *testing.T) {
	v := Violation{Message: "m", Path: "line_items[2]"}
	got := guardExplanation(v, explainPayload(), explainAnswer("Why.", "unit_price", "5.00"))
	b, _ := json.Marshal(got.Fix)
	want := `{"field":"unit_price","label":"Unit price","line":2,"current":"-5.00","value":"5.00"}`
	if string(b) != want {
		t.Errorf("fix %s, want %s", b, want)
	}
	v = Violation{Message: "m", Path: "currency"}
	got = guardExplanation(v, explainPayload(), explainAnswer("Why.", "currency", "NGN"))
	b, _ = json.Marshal(got.Fix)
	want = `{"field":"currency","label":"Currency","line":null,"current":"USD","value":"NGN"}`
	if string(b) != want {
		t.Errorf("fix %s, want %s", b, want)
	}
}

func TestGuardExplanation_AbsentCurrentIsNull(t *testing.T) {
	v := Violation{Message: "m", Path: "buyer.tin"}
	got := guardExplanation(v, explainPayload(), explainAnswer("Why.", "tin", "12345678-0001"))
	if got.Fix == nil || got.Fix.Current != nil || got.Fix.Field != "buyer_tin" {
		t.Errorf("fix %+v, want buyer_tin with null current", got.Fix)
	}
}

func TestGuardExplanation_BlankIsUnavailable(t *testing.T) {
	for _, expl := range []any{nil, "", "  "} {
		got := guardExplanation(Violation{Path: "currency"}, explainPayload(), explainAnswer(expl, "currency", "NGN"))
		if got.Status != "unavailable" || got.Explanation != nil || got.Fix != nil {
			t.Errorf("%v: got %+v", expl, got)
		}
	}
}

func TestGuardExplanation_UncitedRegulationIsDropped(t *testing.T) {
	v := Violation{Message: "Unit price must not be negative.", Path: "line_items[2]"}
	for _, text := range []string{"This breaks the Finance Act.", "See Section 12.", "A penalty applies.", "The tax law says so.", "A 2025 directive requires it.", "Per clause 4."} {
		if got := guardExplanation(v, explainPayload(), explainAnswer(text, nil, nil)); got.Status != "unavailable" {
			t.Errorf("%q: status %s", text, got.Status)
		}
	}
	if got := guardExplanation(v, explainPayload(), explainAnswer("Fill in the HSN code.", nil, nil)); got.Status != "ok" {
		t.Errorf("HSN code: status %s", got.Status)
	}
}

func TestGuardExplanation_CitationInTheMessagePasses(t *testing.T) {
	v := Violation{Message: "VAT Act requires 7.5%.", Path: "vat"}
	if got := guardExplanation(v, explainPayload(), explainAnswer("The VAT Act sets the rate.", nil, nil)); got.Status != "ok" {
		t.Errorf("status %s", got.Status)
	}
}

func TestGuardExplanation_WordBoundaries(t *testing.T) {
	v := Violation{Message: "m", Path: "vat"}
	if got := guardExplanation(v, explainPayload(), explainAnswer("the exact amount reacts", nil, nil)); got.Status != "ok" {
		t.Errorf("status %s", got.Status)
	}
}

func TestExplainResult_MarshalsNullsNotOmitted(t *testing.T) {
	b, err := json.Marshal(explainUnavailable())
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"status":"unavailable","explanation":null,"fix":null}`; string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}

// want_field in the cases is the model's name for the field ("tin"), so a header case matches its party suffix.
func TestExplainCases_WantedFixesPassTheTargetGuard(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "aimodeltest", "explain_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID        string `json:"id"`
		Violation struct {
			Path string `json:"path"`
		} `json:"violation"`
		Invoice struct {
			LineItems []json.RawMessage `json:"line_items"`
		} `json:"invoice"`
		WantField *string `json:"want_field"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		if c.WantField == nil {
			continue
		}
		field, _, ok := explainTarget(c.Violation.Path, len(c.Invoice.LineItems), *c.WantField)
		// want_field is the name the model returns: the last path segment for a header path.
		_, last, _ := strings.Cut(headerMBSPath(field), ".")
		if last == "" {
			last = field
		}
		if !ok || last != *c.WantField || headerMBSPath(field) != c.Violation.Path && !strings.HasPrefix(c.Violation.Path, "line_items") {
			t.Errorf("%s: want_field %q, target (%q, %v)", c.ID, *c.WantField, field, ok)
		}
	}
}
