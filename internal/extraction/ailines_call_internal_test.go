// ailines_call_internal_test.go: acceptance specs for the line-item call's prompt, schema,
// answer type and askAILines. Reuses tok/onePage/recordingAI from aitext_rules_internal_test.go
// and aireading_internal_test.go.
package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

func TestAILinesSystem_MatchesTheMeasuredHarness(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tools", "aimodeltest", "run.py"))
	if err != nil {
		t.Fatalf("read run.py: %v", err)
	}
	text := string(src)

	m := regexp.MustCompile(`(?s)\nLINE_RULES = """(.*?)"""`).FindStringSubmatch(text)
	if m == nil {
		t.Fatal("LINE_RULES literal not found in run.py")
	}
	const preamble = "You extract the line items of one invoice for a Nigerian e-invoicing system.\n\n"
	wantSystem := preamble + m[1]

	lineSystemLit := `LINE_SYSTEM = "` + strings.ReplaceAll(preamble, "\n", `\n`) + `" + LINE_RULES`
	if !strings.Contains(text, lineSystemLit) {
		t.Errorf("run.py no longer contains %q -- LINE_SYSTEM is no longer the fixed preamble plus LINE_RULES", lineSystemLit)
	}

	if aiLinesSystem != wantSystem {
		t.Errorf("aiLinesSystem does not equal run.py's LINE_SYSTEM byte for byte (got len %d, want len %d)", len(aiLinesSystem), len(wantSystem))
	}
}

func TestAILinesSchema_WrapsExactlyTheFiveLineRoles(t *testing.T) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(aiLinesSchema, &top); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if len(top) != 4 {
		t.Fatalf("schema has %d top-level key(s), want exactly 4 (type, additionalProperties, required, properties)", len(top))
	}
	for _, k := range []string{"type", "additionalProperties", "required", "properties"} {
		if _, ok := top[k]; !ok {
			t.Errorf("schema missing top-level key %q", k)
		}
	}

	var typ string
	if err := json.Unmarshal(top["type"], &typ); err != nil || typ != "object" {
		t.Errorf("schema type = %q (err %v), want %q", typ, err, "object")
	}
	var additionalProperties bool
	if err := json.Unmarshal(top["additionalProperties"], &additionalProperties); err != nil || additionalProperties {
		t.Errorf("schema additionalProperties = %v (err %v), want false", additionalProperties, err)
	}

	var required []string
	if err := json.Unmarshal(top["required"], &required); err != nil {
		t.Fatalf("unmarshal required: %v", err)
	}
	if !reflect.DeepEqual(required, []string{"line_items"}) {
		t.Errorf("required = %v, want [line_items]", required)
	}

	var props map[string]json.RawMessage
	if err := json.Unmarshal(top["properties"], &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("properties has %d key(s), want exactly 1 (line_items)", len(props))
	}
	lineItemsRaw, ok := props["line_items"]
	if !ok {
		t.Fatal("properties missing line_items")
	}

	var arr struct {
		Type  string          `json:"type"`
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(lineItemsRaw, &arr); err != nil {
		t.Fatalf("unmarshal line_items property: %v", err)
	}
	if arr.Type != "array" {
		t.Errorf("line_items.type = %q, want array", arr.Type)
	}

	var item map[string]json.RawMessage
	if err := json.Unmarshal(arr.Items, &item); err != nil {
		t.Fatalf("unmarshal items object: %v", err)
	}

	var itemRequired []string
	if err := json.Unmarshal(item["required"], &itemRequired); err != nil {
		t.Fatalf("unmarshal items.required: %v", err)
	}
	if !reflect.DeepEqual(itemRequired, LineRoles) {
		t.Errorf("items.required = %v, want extraction.LineRoles %v in order", itemRequired, LineRoles)
	}

	var itemAdditional bool
	if err := json.Unmarshal(item["additionalProperties"], &itemAdditional); err != nil || itemAdditional {
		t.Errorf("items.additionalProperties = %v (err %v), want false", itemAdditional, err)
	}

	var itemProps map[string]struct {
		Type []string `json:"type"`
	}
	if err := json.Unmarshal(item["properties"], &itemProps); err != nil {
		t.Fatalf("unmarshal items.properties: %v", err)
	}
	if len(itemProps) != len(LineRoles) {
		t.Fatalf("items.properties has %d key(s), want %d (one per LineRole, no other)", len(itemProps), len(LineRoles))
	}
	for _, role := range LineRoles {
		p, ok := itemProps[role]
		if !ok {
			t.Errorf("items.properties missing role %q", role)
			continue
		}
		if !reflect.DeepEqual(p.Type, []string{"string", "null"}) {
			t.Errorf("items.properties[%q].type = %v, want [string null]", role, p.Type)
		}
	}
}

func TestAskAILines_SendsOneLineItemCallWithTheText(t *testing.T) {
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.20, 0.12))
	stub := &recordingAI{enabled: true, answer: map[string]any{"line_items": []any{}}}

	askAILines(context.Background(), stub, pages)

	if len(stub.calls) != 1 {
		t.Fatalf("calls = %d, want exactly 1", len(stub.calls))
	}
	req := stub.calls[0]
	if req.Purpose != ai.PurposeLineItems {
		t.Errorf("Purpose = %q, want %q", req.Purpose, ai.PurposeLineItems)
	}
	if req.System != aiLinesSystem {
		t.Errorf("System = %q, want aiLinesSystem", req.System)
	}
	if string(req.Schema) != string(aiLinesSchema) {
		t.Errorf("Schema = %s, want aiLinesSchema %s", req.Schema, aiLinesSchema)
	}
	if req.SchemaName != "invoice_line_items" {
		t.Errorf("SchemaName = %q, want invoice_line_items", req.SchemaName)
	}
	if want := DoclingPromptText(pages); req.Text != want {
		t.Errorf("Text = %q, want DoclingPromptText(pages) = %q", req.Text, want)
	}
	if req.Pages != nil {
		t.Errorf("Pages = %v, want nil", req.Pages)
	}
	if req.FakeScope != "LINES" {
		t.Errorf("FakeScope = %q, want %q -- the line call is scoped so it cannot be steered by the header's own marker", req.FakeScope, "LINES")
	}
}

func TestAskAILines_TextEqualsAskAIsTextForTheSamePages(t *testing.T) {
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.20, 0.12))
	headerStub := &recordingAI{enabled: true, answer: map[string]any{}}
	lineStub := &recordingAI{enabled: true, answer: map[string]any{"line_items": []any{}}}

	askAI(context.Background(), headerStub, pages)
	askAILines(context.Background(), lineStub, pages)

	if len(headerStub.calls) != 1 || len(lineStub.calls) != 1 {
		t.Fatalf("calls = %d, %d, want 1 and 1", len(headerStub.calls), len(lineStub.calls))
	}
	if headerStub.calls[0].Text != lineStub.calls[0].Text {
		t.Errorf("askAILines Text = %q, want askAI's Text %q", lineStub.calls[0].Text, headerStub.calls[0].Text)
	}
}

func TestAskAILines_OffOrNilMakesNoCall(t *testing.T) {
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.20, 0.12))

	off := &recordingAI{enabled: false}
	if got, failed := askAILines(context.Background(), off, pages); got != nil || failed {
		t.Errorf("askAILines(off) = %v, %v, want nil, false", got, failed)
	}
	if len(off.calls) != 0 {
		t.Errorf("askAILines(off) calls = %d, want 0", len(off.calls))
	}

	if got, failed := askAILines(context.Background(), nil, pages); got != nil || failed {
		t.Errorf("askAILines(nil) = %v, %v, want nil, false", got, failed)
	}
}

func TestAskAILines_NoLineItemsKeyOrNullOrEmptyMeansNoAnswer(t *testing.T) {
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.20, 0.12))
	cases := map[string]map[string]any{
		"no key":     {},
		"json null":  {"line_items": nil},
		"empty list": {"line_items": []any{}},
	}
	for name, answer := range cases {
		stub := &recordingAI{enabled: true, answer: answer}
		got, failed := askAILines(context.Background(), stub, pages)
		if got != nil || failed {
			t.Errorf("%s: askAILines = %v, %v, want nil, false", name, got, failed)
		}
	}
}

func TestAskAILines_AMalformedElementOrCellIsAFailedCall(t *testing.T) {
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.20, 0.12))
	cases := map[string]map[string]any{
		"element not an object": {"line_items": []any{"not-an-object"}},
		"cell is a number":      {"line_items": []any{map[string]any{"description": json.Number("5")}}},
		"cell is a bool":        {"line_items": []any{map[string]any{"quantity": true}}},
		"one bad row among good rows": {"line_items": []any{
			map[string]any{"description": "Good row"},
			"bad row",
		}},
	}
	for name, answer := range cases {
		stub := &recordingAI{enabled: true, answer: answer}
		got, failed := askAILines(context.Background(), stub, pages)
		if got != nil || !failed {
			t.Errorf("%s: askAILines = %v, %v, want nil, true", name, got, failed)
		}
	}

	ok := &recordingAI{enabled: true, answer: map[string]any{"line_items": []any{
		map[string]any{"description": "Widget"},
	}}}
	if got, failed := askAILines(context.Background(), ok, pages); failed || len(got) != 1 {
		t.Errorf("control: askAILines = %v, %v, want 1 line, false", got, failed)
	}
}

func TestAskAILines_BlankAndAbsentCellsBothDecodeToNil(t *testing.T) {
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.20, 0.12))
	row := map[string]any{
		"description": "Widget",
		"unit_price":  "",
		"line_total":  "   ",
		"line_tax":    nil,
		// quantity: absent entirely.
	}
	stub := &recordingAI{enabled: true, answer: map[string]any{"line_items": []any{row}}}

	lines, failed := askAILines(context.Background(), stub, pages)
	if failed {
		t.Fatalf("failed = true, want false")
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want exactly 1", len(lines))
	}
	line := lines[0]
	if line.Description == nil || *line.Description != "Widget" {
		t.Errorf("Description = %v, want Widget", line.Description)
	}
	for name, got := range map[string]*string{
		"Quantity (absent)":      line.Quantity,
		"UnitPrice (blank)":      line.UnitPrice,
		"LineTotal (whitespace)": line.LineTotal,
		"LineTax (JSON null)":    line.LineTax,
	} {
		if got != nil {
			t.Errorf("%s = %q, want nil", name, *got)
		}
	}
}

// AC 6: askAI and askAILines run the same error values through aiCallHead's one failure
// policy -- the three exemptions (an ended caller context, ai.ErrOff) must match on both.
func TestAskAI_AskAILines_ShareOneFailurePolicy(t *testing.T) {
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.20, 0.12))
	headerAnswer := map[string]any{"total": "1935.00"}
	lineAnswer := map[string]any{"line_items": []any{}}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", ai.ErrUnavailable, true},
		{"refused", errors.New("ai: refused: HTTP 402"), true},
		{"canceled", context.Canceled, false},
		{"wrapped canceled", fmt.Errorf("ai: %w", context.Canceled), false},
		{"wrapped deadline", fmt.Errorf("ai: %w", context.DeadlineExceeded), false},
		{"off", ai.ErrOff, false},
	}
	for _, c := range cases {
		headerStub := &recordingAI{enabled: true, answer: headerAnswer, err: c.err}
		if _, failed := askAI(context.Background(), headerStub, pages); failed != c.want {
			t.Errorf("askAI(err=%v) failed = %v, want %v", c.err, failed, c.want)
		}
		lineStub := &recordingAI{enabled: true, answer: lineAnswer, err: c.err}
		if _, failed := askAILines(context.Background(), lineStub, pages); failed != c.want {
			t.Errorf("askAILines(err=%v) failed = %v, want %v", c.err, failed, c.want)
		}
	}
}
