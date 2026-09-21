// aireading_internal_test.go: acceptance specs for AIR-03-01's request, prompt and per-value
// checks (a)-(d). Reuses tok/onePage/containsAny/containsString/findToken/aitGoldenPages/
// aitInventedValues from aitext_rules_internal_test.go and aitext_internal_test.go.
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

	// AIR-05-02 T01: the image message's intro, double-quoted (not triple) in run.py.
	m = regexp.MustCompile(`\nIMAGE_INTRO = "(.*?)"\n`).FindStringSubmatch(text)
	if m == nil {
		t.Fatal("IMAGE_INTRO literal not found in run.py")
	}
	if aiImageIntro != m[1] {
		t.Errorf("aiImageIntro does not equal run.py's IMAGE_INTRO byte for byte (got len %d, want len %d)", len(aiImageIntro), len(m[1]))
	}
	if imagePartsLit := `parts = [{"type": "text", "text": IMAGE_INTRO}]`; !strings.Contains(text, imagePartsLit) {
		t.Errorf("run.py no longer contains %q -- the image message no longer leads with the intro", imagePartsLit)
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
	if req.System != aiSystem {
		t.Errorf("System = %q, want aiSystem", req.System)
	}
	if string(req.Schema) != string(aiFieldSchema) {
		t.Errorf("Schema = %s, want aiFieldSchema %s", req.Schema, aiFieldSchema)
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
	if req.FakeScope != "" {
		t.Errorf("FakeScope = %q, want empty -- the header call is unscoped", req.FakeScope)
	}
}

func TestAskAI_OffOrNilMakesNoCall(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))

	off := &recordingAI{enabled: false}
	if got, _ := askAI(context.Background(), off, pages); got != nil {
		t.Errorf("askAI(off) = %v, want nil", got)
	}
	if len(off.calls) != 0 {
		t.Errorf("askAI(off) calls = %d, want 0", len(off.calls))
	}

	if got, _ := askAI(context.Background(), nil, pages); got != nil {
		t.Errorf("askAI(nil) = %v, want nil", got)
	}

	on := &recordingAI{enabled: true, answer: map[string]any{"total": "1935.00"}}
	if got, _ := askAI(context.Background(), on, pages); len(on.calls) != 1 || got["total"] != "1935.00" {
		t.Errorf("control: askAI(on) calls = %d, answer = %v; want 1 call and total 1935.00", len(on.calls), got)
	}
}

func TestAskAI_AnErrorLeavesNoAnswer(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))
	for _, e := range []error{ai.ErrUnavailable, ai.ErrOff, errors.New("x")} {
		stub := &recordingAI{enabled: true, answer: map[string]any{"total": "1935.00"}, err: e}
		if got, _ := askAI(context.Background(), stub, pages); got != nil {
			t.Errorf("askAI(err=%v) = %v, want nil", e, got)
		}
	}

	ok := &recordingAI{enabled: true, answer: map[string]any{"total": "1935.00"}}
	if got, _ := askAI(context.Background(), ok, pages); got["total"] != "1935.00" {
		t.Errorf("control: askAI(no error) = %v, want total 1935.00", got)
	}
}

// T01 (AIR-04-01): askAI's second return is aiFailed(err) -- every Call error reports true
// except a cancelled/expired caller context and ai.ErrOff (BQ1, AC-6).
func TestAskAI_ReportsEveryFailureExceptAnEndedContextOrOff(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))
	answer := map[string]any{"total": "1935.00"}

	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", ai.ErrUnavailable, true},
		{"fake unavailable", fmt.Errorf("%w (fake)", ai.ErrUnavailable), true},
		{"refused", errors.New("ai: refused: HTTP 402"), true},
		{"invalid request", errors.New("ai: invalid request: x"), true},
		{"fake answer", errors.New("ai: fake answer: x"), true},
		{"canceled", context.Canceled, false},
		{"wrapped canceled", fmt.Errorf("ai: %w", context.Canceled), false},
		{"wrapped deadline", fmt.Errorf("ai: %w", context.DeadlineExceeded), false},
		{"off", ai.ErrOff, false},
	} {
		stub := &recordingAI{enabled: true, answer: answer, err: c.err}
		got, failed := askAI(context.Background(), stub, pages)
		if got != nil {
			t.Errorf("%s: answer = %v, want nil", c.name, got)
		}
		if failed != c.want {
			t.Errorf("%s: flag = %v, want %v", c.name, failed, c.want)
		}
		if len(stub.calls) != 1 {
			t.Errorf("%s: calls = %d, want exactly 1", c.name, len(stub.calls))
		}
	}

	ok := &recordingAI{enabled: true, answer: answer}
	gotOK, failedOK := askAI(context.Background(), ok, pages)
	if failedOK || gotOK["total"] != "1935.00" {
		t.Errorf("control: askAI(no error) = %v, %v; want {total: 1935.00}, false", gotOK, failedOK)
	}
}

// aiFailed follows the wrapped cause, not the message; an ended context wins a joined error.
func TestAIFailed_FollowsTheWrappedCause(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wrapped off", fmt.Errorf("ai: %w", ai.ErrOff), false},
		{"bare deadline", context.DeadlineExceeded, false},
		{"doubly wrapped unavailable", fmt.Errorf("worker: %w", fmt.Errorf("%w (fake)", ai.ErrUnavailable)), true},
		{"spent budget naming a deadline", fmt.Errorf("%w (last: %v)", ai.ErrUnavailable, context.DeadlineExceeded), true},
		{"joined unavailable and canceled", errors.Join(ai.ErrUnavailable, context.Canceled), false},
		{"joined unavailable and a plain error", errors.Join(ai.ErrUnavailable, errors.New("x")), true},
	}
	for _, c := range cases {
		if got := aiFailed(c.err); got != c.want {
			t.Errorf("%s: aiFailed(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

// T02 (AIR-04-01) GUARD: the disabled/nil guard runs before any Call, so it can never report
// failed even when the stub is primed with ErrUnavailable.
func TestAskAI_OffOrNilIsNeverUnavailable(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))

	off := &recordingAI{enabled: false, err: ai.ErrUnavailable}
	got, failed := askAI(context.Background(), off, pages)
	if got != nil || failed {
		t.Errorf("askAI(off) = %v, %v; want nil, false", got, failed)
	}
	if len(off.calls) != 0 {
		t.Errorf("askAI(off) calls = %d, want 0", len(off.calls))
	}

	got, failed = askAI(context.Background(), nil, pages)
	if got != nil || failed {
		t.Errorf("askAI(nil) = %v, %v; want nil, false", got, failed)
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
	got, _ := askAI(context.Background(), stub, pages)
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

	// Control: the page's own values pass, so the refusals above are not a blind page.
	for field, value := range map[string]string{
		"issue_date":    "2026-08-14",
		"supplier_tin":  "99999999-1201",
		"supplier_name": "ADEYEMI TRADING LIMITED",
		"buyer_tin":     "99999999-1202",
		"buyer_name":    "HONEYWELL GROUP",
		"currency":      "NGN",
		"subtotal":      "1800.00",
		"vat":           "135.00",
		"total":         "1935.00",
	} {
		if _, ok := checkAI(field, value, pages); !ok {
			t.Errorf("control: checkAI refused the printed %s value %q", field, value)
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
	joined := onePage(1, tok("Honeywell Group", 1, 0.10, 0.300, 0.26, 0.313))
	if _, ok := checkAI("buyer_name", "Honeywell Group", joined); !ok {
		t.Error("control: checkAI refused the name printed in one token")
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

	if _, ok := checkAI("buyer_name", "HONEYWELL GROUPS", nameToken); !ok {
		t.Error("control: checkAI refused the whole printed name")
	}
	if _, ok := checkAI("total", "1935.00", amountToken); !ok {
		t.Error("control: checkAI refused the whole printed amount")
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
	control := onePage(1, tok("BILL TO", 1, 0.10, 0.10, 0.30, 0.12), value)
	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", control); !ok {
		t.Error("control: checkAI refused the value behind a non-payment line")
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
	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", onePage(1, top, again)); !ok {
		t.Error("control: checkAI refused the same two occurrences with no payment label between them")
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

	labelled := onePage(1, tok("Invoice No: 20417", 1, 0.10, 0.10, 0.35, 0.12))
	if _, ok := checkAI("invoice_number", "20417", labelled); !ok {
		t.Error("control: checkAI refused 20417 behind an invoice-number label")
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

	digits := onePage(1, tok("Invoice No: 1500", 1, 0.10, 0.10, 0.40, 0.12))
	if _, ok := checkAI("invoice_number", "1500", digits); !ok {
		t.Error("control: checkAI refused the all-digit 1500 behind the same label")
	}
}

func TestAICheck_ATwoReadingDateFailsTheFormatCheck(t *testing.T) {
	pages := onePage(1, tok("Date 03/12/2026", 1, 0.10, 0.10, 0.40, 0.12))
	if _, ok := checkAI("issue_date", "03/12/2026", pages); ok {
		t.Error("checkAI accepted a numeric date with two valid day-first/month-first readings")
	}
	oneReading := onePage(1, tok("Date 25/12/2026", 1, 0.10, 0.10, 0.40, 0.12))
	if reading, ok := checkAI("issue_date", "25/12/2026", oneReading); !ok || reading.Value != "2026-12-25" {
		t.Errorf("control: checkAI(25/12/2026) = %+v, %v; want checked as 2026-12-25", reading, ok)
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

func TestCallAI_DropsAKeyThatIsNotAHeaderField(t *testing.T) {
	r := &recordingAI{enabled: true, answer: map[string]any{
		"invoice_number": "INV-1",
		"line_items":     "whatever the model felt like",
		"not_a_field":    "also dropped",
	}}

	out, failed := callAI(context.Background(), r, ai.Request{})

	if failed {
		t.Fatal("callAI failed on a well-formed answer")
	}
	if len(out) == 0 {
		t.Fatal("callAI returned nothing, so the drop assertions below prove nothing")
	}
	if got, ok := out["invoice_number"]; !ok || got != "INV-1" {
		t.Errorf("invoice_number = %q, %v; want INV-1 kept", got, ok)
	}
	for _, k := range []string{"line_items", "not_a_field"} {
		if _, ok := out[k]; ok {
			t.Errorf("%q survived callAI; only HeaderFields may", k)
		}
	}
}
