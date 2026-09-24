package extraction

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// vcChecked is the eight header fields the value check asks: HeaderFields minus the supplier pair.
var vcChecked = []string{"invoice_number", "issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total"}

var vcValues = map[string]string{
	"invoice_number": "INV-2026-001", "issue_date": "2026-03-12", "supplier_tin": "12345678-0001",
	"supplier_name": "Acme Supplies Ltd", "buyer_tin": "87654321-0001", "buyer_name": "Zenith Holdings Limited",
	"currency": "NGN", "subtotal": "1800.00", "vat": "135.00", "total": "1935.00",
}

// vcAsker records every Ask and answers with a fixed response and error.
type vcAsker struct {
	enabled bool
	resp    jev.Response
	err     error
	calls   []jev.Request
}

func (a *vcAsker) Enabled() bool { return a.enabled }

func (a *vcAsker) Ask(_ context.Context, req jev.Request) (jev.Response, error) {
	a.calls = append(a.calls, req)
	return a.resp, a.err
}

// vcCheckable is the test's own oracle for AC-1, independent of valueCheckRequest.
func vcCheckable(r FieldResult) bool {
	return slices.Contains(HeaderFields, r.Name) && r.Name != "supplier_tin" && r.Name != "supplier_name" &&
		r.Reason == ReasonNone && r.Value != nil
}

func vcDecided(name string) FieldResult {
	return FieldResult{
		Field:        Field{Name: name, Value: mgStr(vcValues[name]), Region: &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}},
		Alternatives: []Field{},
	}
}

func vcRows(names ...string) []FieldResult {
	out := make([]FieldResult, len(names))
	for i, n := range names {
		out[i] = vcDecided(n)
	}
	return out
}

func vcAnswers(noul float64, names ...string) jev.Response {
	resp := jev.Response{Answers: map[string]jev.Answer{}}
	for _, n := range names {
		resp.Answers[n] = jev.Answer{Type: jev.TypeNoul, Noul: noul}
	}
	return resp
}

func vcPages() []TokenPage {
	return onePage(1, tok("Invoice INV-2026-001", 1, 0.10, 0.10, 0.40, 0.12))
}

func vcIDs(req jev.Request) []string {
	ids := make([]string, 0, len(req.Questions))
	for id := range req.Questions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func TestValueCheck_AsksEveryDecidedHeaderFieldButTheSupplierPair(t *testing.T) {
	results := vcRows(HeaderFields...)
	results = append(results,
		FieldResult{Field: Field{Name: "line_items[1].description", Value: mgStr("Cement 50kg")}, Alternatives: []Field{}},
		FieldResult{Field: Field{Name: "document_text_layer", Value: mgStr("true")}, Alternatives: []Field{}},
	)

	req, asked := valueCheckRequest(vcPages(), results)

	if len(req.Questions) == 0 {
		t.Fatal("no question asked, want the eight checked header fields")
	}
	want := slices.Clone(vcChecked)
	slices.Sort(want)
	if got := vcIDs(req); !slices.Equal(got, want) {
		t.Errorf("question ids = %v, want %v", got, want)
	}
	for _, absent := range []string{"supplier_tin", "supplier_name", "line_items[1].description", "document_text_layer"} {
		if _, ok := req.Questions[absent]; ok {
			t.Errorf("%s was asked, want it absent", absent)
		}
	}
	if len(asked) != len(vcChecked) {
		t.Fatalf("asked indexes = %v, want %d of them", asked, len(vcChecked))
	}
	for _, i := range asked {
		if _, ok := req.Questions[results[i].Name]; !ok {
			t.Errorf("asked index %d (%s) has no question under its name", i, results[i].Name)
		}
	}
}

func TestValueCheck_NeverAsksAFlaggedOrEmptyField(t *testing.T) {
	ambiguous := vcDecided("issue_date")
	ambiguous.Reason = ReasonAmbiguous
	ambiguous.Alternatives = []Field{{Name: "issue_date", Value: mgStr("2026-12-03")}}
	inconsistent := vcDecided("subtotal")
	inconsistent.Reason = ReasonInconsistent
	unreadable := vcDecided("vat")
	unreadable.Reason = ReasonUnreadable
	missing := FieldResult{Field: Field{Name: "buyer_tin", Reason: ReasonMissing}, Alternatives: []Field{}}
	nilValue := FieldResult{Field: Field{Name: "total"}, Alternatives: []Field{}}
	results := []FieldResult{ambiguous, inconsistent, unreadable, missing, nilValue, vcDecided("invoice_number")}

	req, asked := valueCheckRequest(vcPages(), results)

	// The decided invoice_number is the control: without it, asking nothing would pass.
	if got := vcIDs(req); !slices.Equal(got, []string{"invoice_number"}) {
		t.Errorf("question ids = %v, want only [invoice_number]", got)
	}
	if !slices.Equal(asked, []int{5}) {
		t.Errorf("asked indexes = %v, want [5]", asked)
	}
	for _, name := range []string{"issue_date", "subtotal", "vat", "buyer_tin", "total"} {
		if _, ok := req.Questions[name]; ok {
			t.Errorf("%s was asked, want it skipped", name)
		}
	}
}

func TestValueCheck_AsksTheInvoiceNumber(t *testing.T) {
	req, asked := valueCheckRequest(vcPages(), vcRows("invoice_number"))

	if got := vcIDs(req); !slices.Equal(got, []string{"invoice_number"}) {
		t.Errorf("question ids = %v, want [invoice_number]", got)
	}
	if !slices.Equal(asked, []int{0}) {
		t.Errorf("asked indexes = %v, want [0]", asked)
	}
}

func TestValueCheck_AsksAValueOnlyTheAIRead(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "invoice_number", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Invoice Number: 20417", 1, 0.10, 0.10, 0.40, 0.12))
	results := mergeWith(engine, map[string]any{"invoice_number": "20417"}, pages, nil)
	if results[0].Reason != ReasonNone || results[0].Value == nil {
		t.Fatalf("mergeAI Row 5 did not decide the AI value: %+v", results[0])
	}

	req, _ := valueCheckRequest(pages, results)

	q, ok := req.Questions["invoice_number"]
	if !ok {
		t.Fatalf("invoice_number was not asked; questions = %v", vcIDs(req))
	}
	if !reflect.DeepEqual(q, ValueCheckQuestion("invoice_number", "20417")) {
		t.Errorf("question = %+v, want ValueCheckQuestion(invoice_number, 20417)", q)
	}
	if !strings.Contains(q.Instructions, "Value: 20417.") {
		t.Errorf("instructions %q do not carry the AI-read value", q.Instructions)
	}
}

func TestValueCheck_OneRequestCarriesTheDoclingTextAndOneNoulPerField(t *testing.T) {
	pages := []TokenPage{
		{Number: 1, Tokens: []Token{tok("Invoice INV-2026-001", 1, 0.10, 0.10, 0.40, 0.12)}},
		{Number: 2, Tokens: []Token{tok("Total 1935.00", 2, 0.50, 0.80, 0.90, 0.82)}},
	}
	results := vcRows("invoice_number", "currency", "total")

	req, _ := valueCheckRequest(pages, results)

	if req.Purpose != jev.PurposeValueCheck {
		t.Errorf("Purpose = %q, want %q", req.Purpose, jev.PurposeValueCheck)
	}
	want := DoclingPromptText(pages)
	if want == "" || req.State != want {
		t.Errorf("State = %q, want DoclingPromptText(pages) = %q", req.State, want)
	}
	if len(req.Questions) != 3 {
		t.Fatalf("questions = %v, want 3", vcIDs(req))
	}
	for _, r := range results {
		q, ok := req.Questions[r.Name]
		if !ok {
			t.Errorf("%s not asked", r.Name)
			continue
		}
		if q.Type != jev.TypeNoul {
			t.Errorf("%s Type = %q, want %q", r.Name, q.Type, jev.TypeNoul)
		}
		if !reflect.DeepEqual(q, ValueCheckQuestion(r.Name, *r.Value)) {
			t.Errorf("%s question = %+v, want ValueCheckQuestion(%s, %s)", r.Name, q, r.Name, *r.Value)
		}
		if !strings.Contains(q.Instructions, *r.Value) {
			t.Errorf("%s instructions %q do not carry its value", r.Name, q.Instructions)
		}
	}
}

func TestValueCheck_TheQuestionComposesInstructionsFieldAndValue(t *testing.T) {
	if valueCheckInstructions == "" || valueCheckTrue == "" || valueCheckFalse == "" {
		t.Fatal("a value-check wording constant is empty")
	}

	q := ValueCheckQuestion("total", "1935.00")

	if want := valueCheckInstructions + " Field: total. Value: 1935.00."; q.Instructions != want {
		t.Errorf("Instructions = %q, want %q", q.Instructions, want)
	}
	if q.True != valueCheckTrue {
		t.Errorf("True = %q, want %q", q.True, valueCheckTrue)
	}
	if q.False != valueCheckFalse {
		t.Errorf("False = %q, want %q", q.False, valueCheckFalse)
	}
	if q.Type != jev.TypeNoul {
		t.Errorf("Type = %q, want %q", q.Type, jev.TypeNoul)
	}
	if len(q.Options) != 0 || q.Default != "" {
		t.Errorf("Options = %v, Default = %q, want both empty", q.Options, q.Default)
	}
}

func TestValueCheck_TheThresholdIsTheMeasuredHalf(t *testing.T) {
	if valueCheckThreshold != 0.5 {
		t.Errorf("valueCheckThreshold = %v, want 0.5", float64(valueCheckThreshold))
	}
}

func TestValueCheck_TheBoundaryIsInclusive(t *testing.T) {
	cases := []struct {
		noul    float64
		flagged bool
	}{
		{0.5, true},
		{math.Nextafter(0.5, 1), false},
		{0.0, true},
		{1.0, false},
	}
	for _, c := range cases {
		out := applyValueCheck(vcRows("total"), []int{0}, vcAnswers(c.noul, "total"))
		if len(out) != 1 {
			t.Fatalf("noul %v: %d rows out, want 1", c.noul, len(out))
		}
		want := ReasonNone
		if c.flagged {
			want = ReasonUnreadable
		}
		if out[0].Reason != want {
			t.Errorf("noul %v: Reason = %q, want %q", c.noul, out[0].Reason, want)
		}
	}
}

func TestValueCheck_ADoubtFlagsAndNeverRewrites(t *testing.T) {
	results := vcRows("invoice_number", "vat", "total")
	results[1].Region = &Region{Page: 1, X0: 0.60, Y0: 0.70, X1: 0.80, Y1: 0.72}
	results[1].Alternatives = []Field{{Name: "vat", Value: mgStr("153.00"), Region: &Region{Page: 1, X0: 0.60, Y0: 0.74, X1: 0.80, Y1: 0.76}}}
	before := cloneResults(results)
	resp := vcAnswers(0.9, "invoice_number", "total")
	resp.Answers["vat"] = jev.Answer{Type: jev.TypeNoul, Noul: 0.1}

	out := applyValueCheck(results, []int{0, 1, 2}, resp)

	if len(out) != 3 {
		t.Fatalf("%d rows out, want 3", len(out))
	}
	vat := out[1]
	if vat.Reason != ReasonUnreadable {
		t.Errorf("vat Reason = %q, want %q", vat.Reason, ReasonUnreadable)
	}
	if vat.Name != "vat" || vat.Value == nil || *vat.Value != *before[1].Value {
		t.Errorf("vat = %+v, want its value %q kept", vat.Field, *before[1].Value)
	}
	if !reflect.DeepEqual(vat.Region, before[1].Region) {
		t.Errorf("vat Region = %+v, want %+v", vat.Region, before[1].Region)
	}
	if len(vat.Alternatives) == 0 || !reflect.DeepEqual(vat.Alternatives, before[1].Alternatives) {
		t.Errorf("vat Alternatives = %+v, want %+v", vat.Alternatives, before[1].Alternatives)
	}
	for _, i := range []int{0, 2} {
		if !reflect.DeepEqual(out[i], before[i]) {
			t.Errorf("%s = %+v, want it unchanged %+v", before[i].Name, out[i], before[i])
		}
	}
	if !reflect.DeepEqual(results, before) {
		t.Errorf("the input slice was modified:\ngot:  %+v\nwant: %+v", results, before)
	}
}

func TestValueCheck_NilOrOffAskerAsksNothing(t *testing.T) {
	results := vcRows("invoice_number", "vat", "total")
	before := cloneResults(results)
	doubt := vcAnswers(0, "invoice_number", "vat", "total")

	off := &vcAsker{enabled: false, resp: doubt}
	if out, _ := checkDocument(t.Context(), off, vcPages(), results); !reflect.DeepEqual(out, before) {
		t.Errorf("off asker: output = %+v, want the input", out)
	}
	if len(off.calls) != 0 {
		t.Errorf("off asker: %d Ask calls, want 0", len(off.calls))
	}
	if out, _ := checkDocument(t.Context(), nil, vcPages(), results); !reflect.DeepEqual(out, before) {
		t.Errorf("nil asker: output = %+v, want the input", out)
	}

	// Control: the same answer through an enabled asker does flag.
	on := &vcAsker{enabled: true, resp: doubt}
	out, _ := checkDocument(t.Context(), on, vcPages(), results)
	if len(on.calls) != 1 {
		t.Fatalf("enabled asker: %d Ask calls, want 1", len(on.calls))
	}
	if flaggedCount(out) != 3 {
		t.Errorf("enabled asker: %d flagged, want 3", flaggedCount(out))
	}
}

func TestValueCheck_NothingCheckableAsksOnlyTheDocumentType(t *testing.T) {
	reasons := []Reason{ReasonMissing, ReasonAmbiguous, ReasonUnreadable, ReasonInconsistent}
	var results []FieldResult
	for i, name := range HeaderFields {
		r := vcDecided(name)
		r.Reason = reasons[i%len(reasons)]
		if r.Reason == ReasonMissing {
			r.Value, r.Region = nil, nil
		}
		results = append(results, r)
	}
	results = append(results,
		FieldResult{Field: Field{Name: "line_items[1].description", Value: mgStr("Cement 50kg")}, Alternatives: []Field{}},
		FieldResult{Field: Field{Name: "document_text_layer", Value: mgStr("true")}, Alternatives: []Field{}},
	)
	before := cloneResults(results)
	a := &vcAsker{enabled: true, resp: vcAnswers(0, HeaderFields...)}

	out, _ := checkDocument(t.Context(), a, vcPages(), results)

	if !reflect.DeepEqual(out, before) {
		t.Errorf("output = %+v, want the input", out)
	}
	if len(a.calls) != 1 {
		t.Fatalf("%d Ask calls, want 1", len(a.calls))
	}
	req := a.calls[0]
	if got := vcIDs(req); !slices.Equal(got, []string{"document_type"}) {
		t.Errorf("question ids = %v, want [document_type]", got)
	}
	if req.Purpose != jev.PurposeDocumentType {
		t.Errorf("Purpose = %q, want %q", req.Purpose, jev.PurposeDocumentType)
	}
	if want := DoclingPromptText(vcPages()); want == "" || req.State != want {
		t.Errorf("State = %q, want DoclingPromptText(vcPages()) = %q", req.State, want)
	}

	// Control: one decided header field makes the same results checkable.
	a.calls = nil
	checkDocument(t.Context(), a, vcPages(), append(cloneResults(before), vcDecided("invoice_number")))
	if len(a.calls) != 1 {
		t.Fatalf("with a decided invoice_number: %d Ask calls, want 1", len(a.calls))
	}
	if got := vcIDs(a.calls[0]); !slices.Equal(got, []string{"document_type", "invoice_number"}) {
		t.Errorf("with a decided invoice_number: question ids = %v, want [document_type invoice_number]", got)
	}
	if a.calls[0].Purpose != jev.PurposeValueCheck {
		t.Errorf("with a decided invoice_number: Purpose = %q, want %q", a.calls[0].Purpose, jev.PurposeValueCheck)
	}
}

func TestValueCheck_AnyAskErrorLeavesTheResults(t *testing.T) {
	names := []string{"invoice_number", "vat", "total"}
	errs := map[string]error{
		"refused":           fmt.Errorf("%w: refused", jev.ErrCheckSkipped),
		"unavailable":       fmt.Errorf("%w: unavailable", jev.ErrCheckSkipped),
		"deadline exceeded": fmt.Errorf("%w: unavailable: %w", jev.ErrCheckSkipped, context.DeadlineExceeded),
		"plain":             errors.New("x"),
	}
	for label, err := range errs {
		results := vcRows(names...)
		before := cloneResults(results)
		a := &vcAsker{enabled: true, resp: vcAnswers(0, names...), err: err}

		out, _ := checkDocument(t.Context(), a, vcPages(), results)

		if len(a.calls) != 1 {
			t.Errorf("%s: %d Ask calls, want 1", label, len(a.calls))
		}
		if !reflect.DeepEqual(out, before) {
			t.Errorf("%s: output = %+v, want the input", label, out)
		}
	}

	// Control: the same doubting answer with no error flags every field.
	a := &vcAsker{enabled: true, resp: vcAnswers(0, names...)}
	if out, _ := checkDocument(t.Context(), a, vcPages(), vcRows(names...)); flaggedCount(out) != len(names) {
		t.Errorf("no error: %d flagged, want %d", flaggedCount(out), len(names))
	}
}

func TestValueCheck_AnUnusableAnswerLeavesTheResults(t *testing.T) {
	names := []string{"invoice_number", "vat", "total"}
	missing := vcAnswers(0, "invoice_number", "vat")
	choice := vcAnswers(0, names...)
	choice.Answers["total"] = jev.Answer{Type: jev.TypeChoice, Choice: "tax invoice", Confidence: 1}

	for label, resp := range map[string]jev.Response{"missing id": missing, "choice type": choice} {
		results := vcRows(names...)
		before := cloneResults(results)
		a := &vcAsker{enabled: true, resp: resp}

		out, _ := checkDocument(t.Context(), a, vcPages(), results)

		if len(a.calls) != 1 {
			t.Errorf("%s: %d Ask calls, want 1", label, len(a.calls))
		}
		if !reflect.DeepEqual(out, before) {
			t.Errorf("%s: output = %+v, want the input with no field partly flagged", label, out)
		}
	}

	a := &vcAsker{enabled: true, resp: vcAnswers(0, names...)}
	if out, _ := checkDocument(t.Context(), a, vcPages(), vcRows(names...)); flaggedCount(out) != len(names) {
		t.Errorf("complete answer: %d flagged, want %d", flaggedCount(out), len(names))
	}
}

func TestValueCheck_TheFlaggedCountGrowsByTheDoubtedFields(t *testing.T) {
	results := vcRows("invoice_number", "issue_date", "currency", "vat", "total")
	results = append(results, FieldResult{Field: Field{Name: "buyer_tin", Reason: ReasonMissing}, Alternatives: []Field{}})
	resp := vcAnswers(0.9, "invoice_number", "issue_date", "currency")
	for _, n := range []string{"vat", "total"} {
		resp.Answers[n] = jev.Answer{Type: jev.TypeNoul, Noul: 0.2}
	}
	in := flaggedCount(results)
	if in != 1 {
		t.Fatalf("flaggedCount(in) = %d, want 1", in)
	}

	out, _ := checkDocument(t.Context(), &vcAsker{enabled: true, resp: resp}, vcPages(), results)

	if got := flaggedCount(out) - in; got != 2 {
		t.Errorf("flaggedCount grew by %d, want 2", got)
	}
}

func TestValueCheck_AnAIWithheldValueIsNotAskedAndADoubtedValueStays(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "buyer_name", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1,
		tok("ACCOUNT NAME", 1, 0.10, 0.10, 0.30, 0.12),
		tok("ZENITH HOLDINGS LIMITED", 1, 0.10, 0.20, 0.50, 0.22),
	)
	results := mergeWith(engine, map[string]any{"buyer_name": "ZENITH HOLDINGS LIMITED"}, pages, nil)
	if r := results[0]; r.Value != nil || r.Reason != ReasonUnreadable || len(r.Alternatives) == 0 || r.Alternatives[0].Value == nil {
		t.Fatalf("mergeAI Row 8 did not withhold the AI value: %+v", r)
	}
	results = append(results, vcDecided("vat"))
	before := cloneResults(results)
	a := &vcAsker{enabled: true, resp: vcAnswers(0, "vat")}

	out, _ := checkDocument(t.Context(), a, pages, results)

	if len(a.calls) != 1 {
		t.Fatalf("%d Ask calls, want 1", len(a.calls))
	}
	if _, ok := a.calls[0].Questions["buyer_name"]; ok {
		t.Error("the Row-8 buyer_name was asked")
	}
	if _, ok := a.calls[0].Questions["vat"]; !ok {
		t.Error("the decided vat was not asked")
	}
	if len(out) != 2 {
		t.Fatalf("%d rows out, want 2", len(out))
	}
	if !reflect.DeepEqual(out[0], before[0]) || out[0].Value != nil {
		t.Errorf("buyer_name = %+v, want it unchanged with a nil Value", out[0])
	}
	if out[1].Reason != ReasonUnreadable || out[1].Value == nil || *out[1].Value != *before[1].Value {
		t.Errorf("vat = %+v, want unreadable with its value %q kept", out[1].Field, *before[1].Value)
	}
}

func TestValueCheck_ADoubtAddsNoRegionToARowWithout(t *testing.T) {
	results := vcRows("vat", "total")
	results[0].Region = nil
	before := cloneResults(results)

	out := applyValueCheck(results, []int{0, 1}, vcAnswers(0, "vat", "total"))

	if len(out) != 2 {
		t.Fatalf("%d rows out, want 2", len(out))
	}
	for i, o := range out {
		if o.Reason != ReasonUnreadable {
			t.Errorf("%s Reason = %q, want %q", o.Name, o.Reason, ReasonUnreadable)
		}
		if !reflect.DeepEqual(o.Region, before[i].Region) {
			t.Errorf("%s Region = %+v, want %+v", o.Name, o.Region, before[i].Region)
		}
	}
	if out[0].Region != nil || out[1].Region == nil {
		t.Errorf("Regions = %+v, %+v; want nil kept nil and non-nil kept", out[0].Region, out[1].Region)
	}
}

func TestValueCheck_AnAnswerForAnUnaskedIDChangesNothing(t *testing.T) {
	results := vcRows("supplier_tin", "supplier_name", "invoice_number")
	results = append(results, FieldResult{Field: Field{Name: "line_items[1].description", Value: mgStr("Cement 50kg")}, Alternatives: []Field{}})
	before := cloneResults(results)
	resp := vcAnswers(0, "supplier_tin", "supplier_name", "line_items[1].description")
	resp.Answers["invoice_number"] = jev.Answer{Type: jev.TypeNoul, Noul: 0.2}
	a := &vcAsker{enabled: true, resp: resp}

	out, _ := checkDocument(t.Context(), a, vcPages(), results)

	if len(a.calls) != 1 || len(out) != len(before) {
		t.Fatalf("%d Ask calls and %d rows out, want 1 and %d", len(a.calls), len(out), len(before))
	}
	// Control: the one asked field is flagged.
	if out[2].Reason != ReasonUnreadable {
		t.Errorf("invoice_number Reason = %q, want %q", out[2].Reason, ReasonUnreadable)
	}
	for _, i := range []int{0, 1, 3} {
		if !reflect.DeepEqual(out[i], before[i]) {
			t.Errorf("unasked %s = %+v, want it unchanged", before[i].Name, out[i])
		}
	}
}

func TestValueCheck_AnEmptyDecidedValueIsAsked(t *testing.T) {
	empty := vcDecided("buyer_name")
	empty.Value = mgStr("")
	results := []FieldResult{empty, vcDecided("total")}

	req, asked := valueCheckRequest(vcPages(), results)

	if got := vcIDs(req); !slices.Equal(got, []string{"buyer_name", "total"}) {
		t.Fatalf("question ids = %v, want [buyer_name total]", got)
	}
	if !slices.Equal(asked, []int{0, 1}) {
		t.Errorf("asked indexes = %v, want [0 1]", asked)
	}
	if got := req.Questions["buyer_name"].Instructions; !strings.HasSuffix(got, " Field: buyer_name. Value: .") {
		t.Errorf("buyer_name instructions = %q, want the empty value composed", got)
	}
}

// dtNonInvoice is the seven names a verdict may carry, written out independently of documentTypeOptions.
var dtNonInvoice = []string{"receipt", "proforma", "quotation", "credit note", "delivery note", "statement", "purchase order"}

func dtAnswer(choice string, confidence float64) jev.Response {
	return jev.Response{Answers: map[string]jev.Answer{
		"document_type": {Type: jev.TypeChoice, Choice: choice, Confidence: confidence},
	}}
}

func TestDocumentType_TheQuestionIsAChoiceOverTheEightTypes(t *testing.T) {
	q := DocumentTypeQuestion()

	if q.Type != jev.TypeChoice {
		t.Errorf("Type = %q, want %q", q.Type, jev.TypeChoice)
	}
	if q.Instructions == "" || q.Instructions != documentTypeInstructions {
		t.Errorf("Instructions = %q, want %q", q.Instructions, documentTypeInstructions)
	}
	names := make([]string, len(q.Options))
	for i, o := range q.Options {
		names[i] = o.Name
		if o.Description == "" {
			t.Errorf("option %q has an empty description", o.Name)
		}
	}
	if want := append([]string{"tax invoice"}, dtNonInvoice...); !slices.Equal(names, want) {
		t.Errorf("option names = %v, want %v", names, want)
	}
	if q.Default != "tax invoice" {
		t.Errorf("Default = %q, want %q", q.Default, "tax invoice")
	}
	if q.True != "" || q.False != "" {
		t.Errorf("True = %q, False = %q, want both empty", q.True, q.False)
	}
}

func TestDocumentType_OneRequestCarriesTheValueQuestionsAndTheTypeQuestion(t *testing.T) {
	names := []string{"invoice_number", "vat", "total"}
	results := vcRows(names...)
	a := &vcAsker{enabled: true, resp: vcAnswers(1, names...)}

	checkDocument(t.Context(), a, vcPages(), results)

	if len(a.calls) != 1 {
		t.Fatalf("%d Ask calls, want 1", len(a.calls))
	}
	req := a.calls[0]
	if got := vcIDs(req); !slices.Equal(got, []string{"document_type", "invoice_number", "total", "vat"}) {
		t.Errorf("question ids = %v, want [document_type invoice_number total vat]", got)
	}
	want, _ := valueCheckRequest(vcPages(), results)
	if len(want.Questions) != len(names) {
		t.Fatalf("valueCheckRequest asked %v, want the three names", vcIDs(want))
	}
	for id, q := range want.Questions {
		if !reflect.DeepEqual(req.Questions[id], q) {
			t.Errorf("%s question = %+v, want valueCheckRequest's %+v", id, req.Questions[id], q)
		}
	}
	dt, ok := req.Questions["document_type"]
	if !ok {
		t.Fatal("document_type was not asked")
	}
	if dt.Type != jev.TypeChoice || !reflect.DeepEqual(dt, DocumentTypeQuestion()) {
		t.Errorf("document_type question = %+v, want DocumentTypeQuestion()", dt)
	}
	if s := DoclingPromptText(vcPages()); s == "" || req.State != s {
		t.Errorf("State = %q, want DoclingPromptText(vcPages()) = %q", req.State, s)
	}
}

func TestDocumentType_NoHeaderFieldCollidesWithTheQuestionID(t *testing.T) {
	if len(HeaderFields) == 0 || documentTypeQuestionID != "document_type" {
		t.Fatalf("HeaderFields = %v, documentTypeQuestionID = %q", HeaderFields, documentTypeQuestionID)
	}
	for _, f := range HeaderFields {
		if f == documentTypeQuestionID {
			t.Errorf("header field %q collides with the document-type question id", f)
		}
	}
}

func TestDocumentType_TheThresholdIsTheHighestSwept(t *testing.T) {
	if documentTypeThreshold != 0.9 {
		t.Errorf("documentTypeThreshold = %v, want 0.9", float64(documentTypeThreshold))
	}
}

func TestDocumentType_TheBoundaryIsInclusive(t *testing.T) {
	cases := []struct {
		confidence float64
		want       string
	}{
		{0.9, "receipt"},
		{1.0, "receipt"},
		{math.Nextafter(0.9, 0), ""},
		{0.5, ""},
	}
	for _, c := range cases {
		if got := documentTypeVerdict(dtAnswer("receipt", c.confidence)); got != c.want {
			t.Errorf("receipt at %v: verdict = %q, want %q", c.confidence, got, c.want)
		}
	}
}

func TestDocumentType_ATaxInvoiceNeverRecords(t *testing.T) {
	for _, c := range []float64{1.0, 0.95} {
		if got := documentTypeVerdict(dtAnswer("tax invoice", c)); got != "" {
			t.Errorf("tax invoice at %v: verdict = %q, want none", c, got)
		}
	}
	// Control: the same answer naming a receipt records.
	if got := documentTypeVerdict(dtAnswer("receipt", 1.0)); got != "receipt" {
		t.Errorf("receipt at 1: verdict = %q, want %q", got, "receipt")
	}
}

func TestDocumentType_EverySevenNonInvoiceTypeRecordsItsName(t *testing.T) {
	if len(dtNonInvoice) != 7 {
		t.Fatalf("dtNonInvoice has %d names, want 7", len(dtNonInvoice))
	}
	for _, name := range dtNonInvoice {
		if got := documentTypeVerdict(dtAnswer(name, 1)); got != name {
			t.Errorf("%s at 1: verdict = %q, want %q", name, got, name)
		}
	}
}

func TestDocumentType_AnUnusableTypeAnswerRecordsNothing(t *testing.T) {
	cases := map[string]jev.Response{
		"absent":     {Answers: map[string]jev.Answer{}},
		"noul typed": {Answers: map[string]jev.Answer{"document_type": {Type: jev.TypeNoul, Noul: 0}}},
		"other":      dtAnswer("other", 1),
		"empty name": dtAnswer("", 1),
	}
	for label, resp := range cases {
		if got := documentTypeVerdict(resp); got != "" {
			t.Errorf("%s: verdict = %q, want none", label, got)
		}
	}
	// Control: a usable answer records.
	if got := documentTypeVerdict(dtAnswer("receipt", 1)); got != "receipt" {
		t.Errorf("receipt at 1: verdict = %q, want %q", got, "receipt")
	}
}

func TestDocumentType_TheTwoAnswersAreIndependent(t *testing.T) {
	// A usable doubt with no type answer still flags.
	a := &vcAsker{enabled: true, resp: vcAnswers(0.1, "vat")}
	out, verdict := checkDocument(t.Context(), a, vcPages(), vcRows("vat"))
	if len(out) != 1 || out[0].Reason != ReasonUnreadable {
		t.Errorf("no type answer: rows = %+v, want vat %q", out, ReasonUnreadable)
	}
	if verdict != "" {
		t.Errorf("no type answer: verdict = %q, want none", verdict)
	}

	// A usable type answer with the value answer missing still records.
	results := vcRows("vat")
	before := cloneResults(results)
	b := &vcAsker{enabled: true, resp: dtAnswer("receipt", 1)}
	out, verdict = checkDocument(t.Context(), b, vcPages(), results)
	if !reflect.DeepEqual(out, before) || flaggedCount(out) != 0 {
		t.Errorf("no value answer: rows = %+v, want the input unflagged", out)
	}
	if verdict != "receipt" {
		t.Errorf("no value answer: verdict = %q, want %q", verdict, "receipt")
	}
}

func TestDocumentType_OffNilOrErrorRecordsNothing(t *testing.T) {
	names := []string{"invoice_number", "vat", "total"}
	resp := vcAnswers(0, names...)
	resp.Answers["document_type"] = jev.Answer{Type: jev.TypeChoice, Choice: "receipt", Confidence: 1}

	results := vcRows(names...)
	before := cloneResults(results)
	if out, verdict := checkDocument(t.Context(), nil, vcPages(), results); !reflect.DeepEqual(out, before) || verdict != "" {
		t.Errorf("nil asker: rows = %+v, verdict = %q; want the input and none", out, verdict)
	}
	off := &vcAsker{enabled: false, resp: resp}
	if out, verdict := checkDocument(t.Context(), off, vcPages(), results); !reflect.DeepEqual(out, before) || verdict != "" {
		t.Errorf("off asker: rows = %+v, verdict = %q; want the input and none", out, verdict)
	}
	if len(off.calls) != 0 {
		t.Errorf("off asker: %d Ask calls, want 0", len(off.calls))
	}

	errs := map[string]error{
		"refused":           fmt.Errorf("%w: refused", jev.ErrCheckSkipped),
		"unavailable":       fmt.Errorf("%w: unavailable", jev.ErrCheckSkipped),
		"deadline exceeded": fmt.Errorf("%w: unavailable: %w", jev.ErrCheckSkipped, context.DeadlineExceeded),
		"plain":             errors.New("x"),
	}
	for label, err := range errs {
		results := vcRows(names...)
		before := cloneResults(results)
		a := &vcAsker{enabled: true, resp: resp, err: err}

		out, verdict := checkDocument(t.Context(), a, vcPages(), results)

		if len(a.calls) != 1 {
			t.Errorf("%s: %d Ask calls, want 1", label, len(a.calls))
		}
		if !reflect.DeepEqual(out, before) || verdict != "" {
			t.Errorf("%s: rows = %+v, verdict = %q; want the input and none", label, out, verdict)
		}
	}

	// Control: the same answer with no error flags and records.
	a := &vcAsker{enabled: true, resp: resp}
	out, verdict := checkDocument(t.Context(), a, vcPages(), vcRows(names...))
	if flaggedCount(out) != len(names) || verdict != "receipt" {
		t.Errorf("no error: %d flagged, verdict %q; want %d and %q", flaggedCount(out), verdict, len(names), "receipt")
	}
}
