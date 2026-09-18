// aireading_internal_test.go: acceptance specs for AIR-03-01's request, prompt and per-value
// checks (a)-(d). Reuses tok/onePage/containsAny/containsString/findToken/aitGoldenPages/
// aitInventedValues from aitext_rules_internal_test.go and aitext_internal_test.go.
package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// recordingAI is a fake AIReader that records every Call and answers with a fixed map/error.
type recordingAI struct {
	enabled bool
	answer  map[string]any
	err     error
	calls   []ai.Request
}

func (r *recordingAI) Enabled() bool { return r.enabled }

func (r *recordingAI) Call(ctx context.Context, req ai.Request) (map[string]any, error) {
	r.calls = append(r.calls, req)
	return r.answer, r.err
}

func TestAISchema_AsksForExactlyTheHeaderFields(t *testing.T) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(aiFieldSchema, &top); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if len(top) != 4 {
		t.Fatalf("schema has %d top-level keys, want exactly 4 (type, additionalProperties, required, properties)", len(top))
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
	if !reflect.DeepEqual(required, HeaderFields) {
		t.Errorf("required = %v, want %v in order", required, HeaderFields)
	}

	var props map[string]struct {
		Type []string `json:"type"`
	}
	if err := json.Unmarshal(top["properties"], &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	if len(props) != len(HeaderFields) {
		t.Fatalf("properties has %d keys, want %d (one per header field, no other)", len(props), len(HeaderFields))
	}
	for _, f := range HeaderFields {
		p, ok := props[f]
		if !ok {
			t.Errorf("properties missing header field %q", f)
			continue
		}
		if !reflect.DeepEqual(p.Type, []string{"string", "null"}) {
			t.Errorf("properties[%q].type = %v, want [string null]", f, p.Type)
		}
		if strings.Contains(f, "line") {
			t.Errorf("header field %q names a line-item key, which the schema must never carry", f)
		}
	}
}

func TestAIPrompt_MatchesTheMeasuredHarness(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tools", "aimodeltest", "run.py"))
	if err != nil {
		t.Fatalf("read run.py: %v", err)
	}
	text := string(src)

	m := regexp.MustCompile(`(?s)\nSYSTEM = """(.*?)"""`).FindStringSubmatch(text)
	if m == nil {
		t.Fatal("SYSTEM literal not found in run.py")
	}
	if aiSystem != m[1] {
		t.Errorf("aiSystem does not equal run.py's SYSTEM byte for byte (got len %d, want len %d)", len(aiSystem), len(m[1]))
	}

	m = regexp.MustCompile(`(?s)\nTEXT_INTRO = """(.*?)"""`).FindStringSubmatch(text)
	if m == nil {
		t.Fatal("TEXT_INTRO literal not found in run.py")
	}
	if aiTextIntro != m[1] {
		t.Errorf("aiTextIntro does not equal run.py's TEXT_INTRO byte for byte (got len %d, want len %d)", len(aiTextIntro), len(m[1]))
	}

	pageLit := `f"--- page {p['number']} ---"`
	rowLit := `f"y={ln['y']:.3f} | {runs}"`
	runLit := `f'x={s["x"]:.2f} {s["text"]}'`
	for _, lit := range []string{pageLit, rowLit, runLit, `TEXT_INTRO + "\n\n" + doc_text(dump)`} {
		if !strings.Contains(text, lit) {
			t.Errorf("run.py no longer contains %q -- the fixed Python-to-Go map below is stale", lit)
		}
	}

	// The fixed map (Stage 1 correction C2), applied to each Python literal's bare template,
	// must equal the Go renderer's own named constant.
	pyToGo := strings.NewReplacer(
		"{p['number']}", "%d",
		"{ln['y']:.3f}", "%.3f",
		"{runs}", "%s",
		`{s["x"]:.2f}`, "%.2f",
		`{s["text"]}`, "%s",
	).Replace
	unwrap := func(lit string) string {
		lit = strings.TrimPrefix(lit, `f"`)
		lit = strings.TrimPrefix(lit, `f'`)
		lit = strings.TrimSuffix(lit, `"`)
		lit = strings.TrimSuffix(lit, `'`)
		return lit
	}
	for _, c := range []struct{ lit, want string }{
		{pageLit, aiPromptPageFmt},
		{rowLit, aiPromptRowFmt},
		{runLit, aiPromptRunFmt},
	} {
		if got := pyToGo(unwrap(c.lit)); got != c.want {
			t.Errorf("mapped Python literal %q = %q, want the Go constant %q", c.lit, got, c.want)
		}
	}
}

func TestAIPromptText_OneLinePerDoclingTokenInReaderOrder(t *testing.T) {
	pages := []TokenPage{
		{Number: 1, Tokens: []Token{
			tok("B", 1, 0.10, 0.400, 0.20, 0.42), // lower on the page, listed first: out of y order
			tok("A", 1, 0.20, 0.100, 0.30, 0.12),
		}},
		{Number: 2, Tokens: []Token{
			tok("C", 2, 0.05, 0.200, 0.15, 0.22),
		}},
	}
	want := aiTextIntro + "\n\n" +
		"--- page 1 ---\n" +
		"y=0.400 | x=0.10 B\n" +
		"y=0.100 | x=0.20 A\n" +
		"--- page 2 ---\n" +
		"y=0.200 | x=0.05 C"
	if got := aiPromptText(pages); got != want {
		t.Errorf("aiPromptText = %q, want %q", got, want)
	}
}

func TestAskAI_SendsOneDocumentCallWithTheText(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))
	stub := &recordingAI{enabled: true, answer: map[string]any{}}

	askAI(context.Background(), stub, pages)

	if len(stub.calls) != 1 {
		t.Fatalf("calls = %d, want exactly 1", len(stub.calls))
	}
	req := stub.calls[0]
	if req.Purpose != ai.PurposeDocument {
		t.Errorf("Purpose = %q, want %q", req.Purpose, ai.PurposeDocument)
	}
	if req.SchemaName != "invoice_fields" {
		t.Errorf("SchemaName = %q, want invoice_fields", req.SchemaName)
	}
	if want := aiPromptText(pages); req.Text != want {
		t.Errorf("Text = %q, want aiPromptText(pages) = %q", req.Text, want)
	}
	if req.Pages != nil {
		t.Errorf("Pages = %v, want nil", req.Pages)
	}
	if req.FakeHint != "" {
		t.Errorf("FakeHint = %q, want empty", req.FakeHint)
	}
}

func TestAskAI_OffOrNilMakesNoCall(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))

	off := &recordingAI{enabled: false}
	if got := askAI(context.Background(), off, pages); got != nil {
		t.Errorf("askAI(off) = %v, want nil", got)
	}
	if len(off.calls) != 0 {
		t.Errorf("askAI(off) calls = %d, want 0", len(off.calls))
	}

	if got := askAI(context.Background(), nil, pages); got != nil {
		t.Errorf("askAI(nil) = %v, want nil", got)
	}
}

func TestAskAI_AnErrorLeavesNoAnswer(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))
	for _, e := range []error{ai.ErrUnavailable, ai.ErrOff, errors.New("x")} {
		stub := &recordingAI{enabled: true, answer: map[string]any{"total": "1935.00"}, err: e}
		if got := askAI(context.Background(), stub, pages); got != nil {
			t.Errorf("askAI(err=%v) = %v, want nil", e, got)
		}
	}
}

func TestAskAI_KeepsOnlyNonBlankStrings(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))
	stub := &recordingAI{enabled: true, answer: map[string]any{
		"invoice_number": nil,
		"buyer_name":     "  ",
		"total":          "1935.00",
		"vat":            json.Number("5"),
		"extra":          "x",
	}}
	got := askAI(context.Background(), stub, pages)
	want := map[string]string{"total": "1935.00"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("askAI(mixed answer) = %v, want %v", got, want)
	}
}

func TestAICheck_FormatCheckRefusesTheDroppedDigitTIN(t *testing.T) {
	full := tok("TIN: 99999999-1202", 1, 0.10, 0.10, 0.40, 0.12)
	dropped := tok("Ref 9999999-1202", 1, 0.10, 0.20, 0.40, 0.22)
	pages := onePage(1, full, dropped)

	if _, ok := checkAI("buyer_tin", "9999999-1202", pages); ok {
		t.Error("checkAI accepted the dropped-digit TIN")
	}
	reading, ok := checkAI("buyer_tin", "99999999-1202", pages)
	if !ok {
		t.Fatal("checkAI refused the control TIN 99999999-1202")
	}
	if reading.Region == nil || *reading.Region != full.Region {
		t.Errorf("region = %v, want the first token's region %v", reading.Region, full.Region)
	}
}

func TestAICheck_PageCheckRefusesAValueNotPrinted(t *testing.T) {
	pages := aitGoldenPages(t, "wild_scanned_no_number")
	if len(aitInventedValues) == 0 {
		t.Fatal("no invented values to check")
	}
	for field, value := range aitInventedValues {
		if containsAny(pages, value) {
			t.Fatalf("invented %s value %q occurs on the golden page -- it is not invented", field, value)
		}
		if _, ok := checkAI(field, value, pages); ok {
			t.Errorf("checkAI accepted the invented %s value %q, not printed anywhere on the page", field, value)
		}
	}
}

func TestAICheck_FindsAValueInsideOneLine(t *testing.T) {
	line := tok("Ref 7781 dated 2026-06-11 terms 30 days", 1, 0.10, 0.40, 0.60, 0.42)
	pages := onePage(1, line)

	reading, ok := checkAI("issue_date", "2026-06-11", pages)
	if !ok {
		t.Fatal("checkAI did not find the date buried inside the line")
	}
	if reading.Region == nil || *reading.Region != line.Region {
		t.Errorf("region = %v, want the whole line's region %v", reading.Region, line.Region)
	}
}

func TestAICheck_DoesNotJoinNeighbouringLines(t *testing.T) {
	honeywell := tok("Honeywell", 1, 0.10, 0.300, 0.19, 0.312)
	group := tok("Group", 1, 0.20, 0.301, 0.26, 0.313)
	pages := onePage(1, honeywell, group)

	if _, ok := checkAI("buyer_name", "Honeywell Group", pages); ok {
		t.Error("checkAI joined two neighbouring tokens across a token boundary it must not cross")
	}
}

func TestAICheck_ComparesWithWhitespaceCollapsed(t *testing.T) {
	spacedToken := onePage(1, tok("ACME  TRADING   LTD", 1, 0.10, 0.10, 0.50, 0.12))
	if _, ok := checkAI("buyer_name", "ACME TRADING LTD", spacedToken); !ok {
		t.Error("checkAI did not collapse the printed token's repeated spaces")
	}

	spacedAnswer := onePage(1, tok("ACME TRADING LTD", 1, 0.10, 0.10, 0.50, 0.12))
	if _, ok := checkAI("buyer_name", "ACME  TRADING   LTD", spacedAnswer); !ok {
		t.Error("checkAI did not collapse the AI answer's repeated spaces")
	}
}

func TestAICheck_HasNoSubstringFallback(t *testing.T) {
	nameToken := onePage(1, tok("HONEYWELL GROUPS", 1, 0.10, 0.10, 0.50, 0.12))
	if _, ok := checkAI("buyer_name", "HONEYWELL GROUP", nameToken); ok {
		t.Error("checkAI accepted a substring match against a longer printed name")
	}

	amountToken := onePage(1, tok("1,935.00", 1, 0.10, 0.20, 0.30, 0.22))
	if _, ok := checkAI("total", "935.00", amountToken); ok {
		t.Error("checkAI accepted a substring match against a longer printed amount")
	}
}

func TestAICheck_PaymentLabelOnTheSameLineFails(t *testing.T) {
	for _, line := range []string{
		"Account Name: ZENITH HOLDINGS LIMITED",
		"ACCOUNT NO ZENITH HOLDINGS LIMITED",
		"account number: ZENITH HOLDINGS LIMITED",
		"Bank ZENITH HOLDINGS LIMITED",
	} {
		pages := onePage(1, tok(line, 1, 0.10, 0.10, 0.60, 0.12))
		if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", pages); ok {
			t.Errorf("checkAI(buyer_name) accepted a value on the payment line %q", line)
		}
		if _, ok := checkAI("supplier_name", "ZENITH HOLDINGS LIMITED", pages); ok {
			t.Errorf("checkAI(supplier_name) accepted a value on the payment line %q", line)
		}
	}

	control := onePage(1, tok("Bill To: ZENITH HOLDINGS LIMITED", 1, 0.10, 0.30, 0.60, 0.32))
	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", control); !ok {
		t.Error("checkAI refused the control line, which carries no payment label")
	}
}

func TestAICheck_PaymentLabelOnTheLineBeforeFails(t *testing.T) {
	label := tok("ACCOUNT NAME", 1, 0.10, 0.10, 0.30, 0.12)
	value := tok("ZENITH HOLDINGS LIMITED", 1, 0.10, 0.20, 0.50, 0.22)
	pages := onePage(1, label, value)

	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", pages); ok {
		t.Error("checkAI accepted a value whose own line starts with nothing, when the line before carries a payment label")
	}
}

func TestAICheck_LineBeforeCountsOnlyWhenTheValueStartsItsLine(t *testing.T) {
	label := tok("ACCOUNT NAME", 1, 0.10, 0.10, 0.30, 0.12)
	value := tok("Bill To: ZENITH HOLDINGS LIMITED", 1, 0.10, 0.20, 0.60, 0.22)
	pages := onePage(1, label, value)

	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", pages); !ok {
		t.Error("checkAI refused a value that does not start its own line, even though the previous line carries a payment label")
	}
}

func TestAICheck_APaymentLabelBeforeAnyOccurrenceFails(t *testing.T) {
	top := tok("ZENITH HOLDINGS LIMITED", 1, 0.10, 0.10, 0.60, 0.12)
	label := tok("Account Name:", 1, 0.10, 0.30, 0.30, 0.32)
	again := tok("ZENITH HOLDINGS LIMITED", 1, 0.10, 0.40, 0.60, 0.42)
	pages := onePage(1, top, label, again)

	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", pages); ok {
		t.Error("checkAI accepted buyer_name even though one occurrence sits right after a payment label")
	}
}

func TestAICheck_PaymentLabelsApplyToPartyNamesOnly(t *testing.T) {
	pages := onePage(1, tok("Bank TIN: 12345678-0001", 1, 0.10, 0.10, 0.50, 0.12))
	if _, ok := checkAI("buyer_tin", "12345678-0001", pages); !ok {
		t.Error("checkAI refused a buyer_tin behind the word Bank -- check (c) applies to party names only")
	}
}

func TestAICheck_InvoiceNumberLabelLetsAnAllDigitNumberPass(t *testing.T) {
	cases := []struct {
		name  string
		pages []TokenPage
	}{
		{"label and value share the line, colon form", onePage(1, tok("Invoice Number: 20417", 1, 0.10, 0.10, 0.40, 0.12))},
		{"label and value share the line, abbreviated with a dot", onePage(1, tok("Invoice No. 20417", 1, 0.10, 0.10, 0.40, 0.12))},
		{"label and value are separate tokens", onePage(1,
			tok("Invoice No", 1, 0.10, 0.10, 0.25, 0.12),
			tok("20417", 1, 0.30, 0.10, 0.40, 0.12),
		)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reading, ok := checkAI("invoice_number", "20417", c.pages)
			if !ok {
				t.Fatal("checkAI refused an all-digit invoice number behind an invoice-number label")
			}
			line := findToken(t, c.pages, "20417")
			if reading.Region == nil || *reading.Region != line.Region {
				t.Errorf("region = %v, want the line holding 20417 (%v)", reading.Region, line.Region)
			}
		})
	}
}

func TestAICheck_AllDigitNumberWithoutTheLabelStaysRefused(t *testing.T) {
	cases := []struct {
		name  string
		pages []TokenPage
	}{
		{"a Ref label, not an invoice-number label", onePage(1, tok("Ref: 20417", 1, 0.10, 0.10, 0.30, 0.12))},
		{"the digits alone, nothing in front", onePage(1, tok("20417", 1, 0.10, 0.10, 0.20, 0.12))},
		{"a payment-account label, not an invoice-number label", onePage(1, tok("Account No: 20417", 1, 0.10, 0.10, 0.35, 0.12))},
		{"a different label on the line before", onePage(1,
			tok("Total", 1, 0.10, 0.10, 0.20, 0.12),
			tok("20417", 1, 0.10, 0.20, 0.20, 0.22),
		)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := checkAI("invoice_number", "20417", c.pages); ok {
				t.Error("checkAI accepted an all-digit invoice number with no invoice-number label in front of any occurrence")
			}
		})
	}
}

func TestAICheck_TheExceptionIsForAllDigitNumbersOnly(t *testing.T) {
	amount := onePage(1, tok("Invoice No: 1500.00", 1, 0.10, 0.10, 0.40, 0.12))
	if _, ok := checkAI("invoice_number", "1500.00", amount); ok {
		t.Error("checkAI accepted an amount-shaped value under the invoice-number exception")
	}

	date := onePage(1, tok("Invoice No: 2026-06-11", 1, 0.10, 0.20, 0.40, 0.22))
	if _, ok := checkAI("invoice_number", "2026-06-11", date); ok {
		t.Error("checkAI accepted a date-shaped value under the invoice-number exception")
	}
}

func TestAICheck_ATwoReadingDateFailsTheFormatCheck(t *testing.T) {
	pages := onePage(1, tok("Date 03/12/2026", 1, 0.10, 0.10, 0.40, 0.12))
	if _, ok := checkAI("issue_date", "03/12/2026", pages); ok {
		t.Error("checkAI accepted a numeric date with two valid day-first/month-first readings")
	}
}

func TestAICheck_TheBoxIsTheWholeLine(t *testing.T) {
	wide := tok("Ref 7781 dated 2026-06-11 terms 30 days", 1, 0.10, 0.40, 0.60, 0.42)
	reading, ok := checkAI("issue_date", "2026-06-11", onePage(1, wide))
	if !ok {
		t.Fatal("checkAI did not find the date inside the wide token")
	}
	if reading.Region == nil || *reading.Region != wide.Region {
		t.Errorf("region = %v, want the whole line's region %v", reading.Region, wide.Region)
	}

	unusable := Token{Text: "2026-06-11", Region: Region{Page: 0}}
	readingU, okU := checkAI("issue_date", "2026-06-11", onePage(1, unusable))
	if !okU {
		t.Fatal("checkAI refused a value whose token has an unusable box -- the box is not a gate on the format/page checks")
	}
	if readingU.Region != nil {
		t.Errorf("region = %v, want nil for an unusable box", readingU.Region)
	}
}
