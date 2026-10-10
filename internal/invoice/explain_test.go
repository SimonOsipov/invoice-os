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
