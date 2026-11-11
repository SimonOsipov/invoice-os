package publiccheck_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

const today = "2026-11-08"

func fixedNow() time.Time { return time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC) }

// loader records every call and answers with rules for each requested date.
type loader struct {
	calls  [][]string
	custom []bool // custom == nil per call
	rules  []validation.Rule
	err    error
}

func (l *loader) load(_ context.Context, dates []string, custom []validation.Rule) (map[string]validation.RuleSet, error) {
	l.calls = append(l.calls, slices.Clone(dates))
	l.custom = append(l.custom, custom == nil)
	if l.err != nil {
		return nil, l.err
	}
	out := map[string]validation.RuleSet{}
	for _, d := range dates {
		out[d] = validation.RuleSet{Version: 4, Rules: slices.Clone(l.rules)}
	}
	return out, nil
}

func requiredRule(key, target string) validation.Rule {
	return validation.Rule{Key: key, Type: validation.TypeRequired, Target: target, Params: json.RawMessage(`{}`),
		Severity: "error", Message: key + " failed", Scope: "document", Enabled: true}
}

type mapper struct {
	calls int
	run   func(ctx context.Context)
}

func (m *mapper) Enabled() bool { return true }
func (m *mapper) Call(ctx context.Context, _ ai.Request) (map[string]any, error) {
	m.calls++
	if m.run != nil {
		m.run(ctx)
	}
	return map[string]any{}, nil
}

// pdfText reads one page of text, or fails.
type pdfText struct {
	calls int
	chars int
	err   error
	onRun func()
}

func (r *pdfText) Name() string    { return "stub" }
func (r *pdfText) Version() string { return "1" }
func (r *pdfText) Read(_ context.Context, _ extraction.Document, onPage func(extraction.Page) error) (extraction.PageResult, error) {
	r.calls++
	if r.onRun != nil {
		r.onRun()
	}
	if r.err != nil {
		return extraction.PageResult{}, r.err
	}
	if r.chars > 0 {
		// The AI's values must appear on the page to be kept.
		toks := []extraction.Token{
			{Text: "INV-9", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.1, X1: 0.2, Y1: 0.12}},
			{Text: "Supplier TIN", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.2, X1: 0.25, Y1: 0.22}},
			{Text: "12345678-0001", Region: extraction.Region{Page: 1, X0: 0.27, Y0: 0.2, X1: 0.45, Y1: 0.22}},
		}
		if err := onPage(extraction.Page{Number: 1, Tokens: toks}); err != nil {
			return extraction.PageResult{}, err
		}
	}
	return extraction.PageResult{Pages: 1, TextChars: r.chars}, nil
}

type fakeAI struct {
	header map[string]any
	fail   bool
	calls  int
	run    func(ctx context.Context)
}

func (a *fakeAI) Enabled() bool { return true }
func (a *fakeAI) Call(ctx context.Context, req ai.Request) (map[string]any, error) {
	a.calls++
	if a.run != nil {
		a.run(ctx)
	}
	if a.fail {
		return nil, ai.ErrUnavailable
	}
	if req.Purpose == ai.PurposeLineItems {
		return map[string]any{}, nil
	}
	return a.header, nil
}

type fakeJev struct{ choice string }

func (j *fakeJev) Enabled() bool { return true }
func (j *fakeJev) Ask(context.Context, jev.Request) (jev.Response, error) {
	return jev.Response{Answers: map[string]jev.Answer{
		"document_type": {Type: jev.TypeChoice, Choice: j.choice, Confidence: 0.95}, // extraction.documentTypeQuestionID
	}}, nil
}

type rig struct {
	l  *loader
	m  *mapper
	tx *pdfText
	rd *pdfText // render reader, must stay unused
	ai *fakeAI
	c  *publiccheck.Checker
}

func newRig() *rig {
	r := &rig{l: &loader{rules: []validation.Rule{requiredRule("buyer-tin-required", "buyer.tin")}},
		m: &mapper{}, tx: &pdfText{chars: 20}, rd: &pdfText{}, ai: &fakeAI{header: map[string]any{"invoice_number": "INV-9"}}}
	r.c = &publiccheck.Checker{
		LoadRules: r.l.load,
		Engine:    validation.NewDefaultEngine(),
		Mapper:    r.m,
		Readers:   extraction.CheckReaders{Text: r.tx, Render: r.rd, AI: r.ai},
		Now:       fixedNow,
	}
	return r
}

func (r *rig) untouched(t *testing.T) {
	t.Helper()
	if len(r.l.calls) != 0 || r.m.calls != 0 || r.tx.calls != 0 || r.rd.calls != 0 || r.ai.calls != 0 {
		t.Errorf("loader %d, mapper %d, text %d, render %d, ai %d calls, want all 0",
			len(r.l.calls), r.m.calls, r.tx.calls, r.rd.calls, r.ai.calls)
	}
}

func csvReq(csv string, kv ...string) publiccheck.Request {
	m := map[string]string{"invoice_number": "Inv", "total": "Total"}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return publiccheck.Request{File: []byte(csv), Format: "csv", Mapping: m}
}

// padded pads csv with one blank-number row so the file is exactly n bytes.
func padded(csv string, n int) []byte {
	return []byte(csv + "," + strings.Repeat("x", n-len(csv)-2) + "\n")
}

const threeInvoices = "Inv,Total,BTIN,Date\nA,1,12345678-0001,\nB,2,,\nC,3,12345678-0002,\n"

func TestCheck_NeedsNoIdentityAndReturnsNumberFailuresAndCounts(t *testing.T) {
	r := newRig()
	// A has two rows, so the row count (4) differs from the invoice count (3).
	const fourRows = "Inv,Total,BTIN,Date\nA,1,12345678-0001,\nB,2,,\nA,1,12345678-0001,\nC,3,12345678-0002,\n"
	res, err := r.c.Check(context.Background(), csvReq(fourRows, "buyer_tin", "BTIN"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Invoices) != 2 || res.InvoicesTotal != 3 || res.RowsTotal != 4 {
		t.Fatalf("invoices %d total %d rows %d, want 2 3 4", len(res.Invoices), res.InvoicesTotal, res.RowsTotal)
	}
	a, b := res.Invoices[0], res.Invoices[1]
	if a.Number != "A" || len(a.Failures) != 0 {
		t.Errorf("A = %+v, want no failures", a)
	}
	if b.Number != "B" || len(b.Failures) != 1 || b.Failures[0].RuleKey != "buyer-tin-required" ||
		b.Failures[0].Path != "buyer.tin" || b.Failures[0].Message == "" {
		t.Errorf("B = %+v, want buyer-tin-required on buyer.tin", b)
	}
}

func TestCheck_CarriesTheEvaluatedPayload(t *testing.T) {
	r := newRig()
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total,Buyer\nA,1075.00,Beta Ltd\n", "buyer_name", "Buyer"))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Invoices[0].Payload
	if got, ok := p["total"].(float64); !ok || got != 1075 {
		t.Errorf("total = %#v, want float64 1075 (the JSON-decoded shape the batch handler evaluates)", p["total"])
	}
	if b, _ := p["buyer"].(map[string]any); b["name"] != "Beta Ltd" {
		t.Errorf("buyer = %#v, want name Beta Ltd", p["buyer"])
	}
}

func TestCheck_AFileWithNoInvoiceNumberExplainsWhy(t *testing.T) {
	r := newRig()
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total\n,1\n,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Invoices) != 0 || res.InvoicesTotal != 0 || res.RowsTotal != 2 {
		t.Errorf("invoices %d total %d rows %d, want 0 0 2", len(res.Invoices), res.InvoicesTotal, res.RowsTotal)
	}
	want := []importer.RowError{
		{Row: 2, Message: "blank invoice number: row cannot be grouped"},
		{Row: 3, Message: "blank invoice number: row cannot be grouped"},
	}
	if !reflect.DeepEqual(res.BlankNumberRows, want) {
		t.Errorf("BlankNumberRows = %+v, want %+v", res.BlankNumberRows, want)
	}
}

func TestCheck_AProblemInvoiceIsOneFailureWithNoPayload(t *testing.T) {
	r := newRig()
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total,Date\nA,1,31/02/2026\nB,2,2026-10-01\n", "issue_date", "Date"))
	if err != nil {
		t.Fatal(err)
	}
	a := res.Invoices[0]
	if len(a.Failures) != 1 || a.Failures[0].RuleKey != "" || a.Failures[0].Path != "issue_date" || a.Payload != nil {
		t.Errorf("A = %+v, want one failure on issue_date and nil payload", a)
	}
	if want := [][]string{{"2026-10-01"}}; !slices.EqualFunc(r.l.calls, want, slices.Equal) {
		t.Errorf("loader dates = %v, want %v", r.l.calls, want)
	}
}

func TestCheck_ADocumentIsOneInvoice(t *testing.T) {
	r := newRig()
	r.ai.header = map[string]any{"invoice_number": "INV-9", "supplier_tin": "12345678-0001"}
	r.c.Readers.Jev = &fakeJev{choice: "credit note"}
	res, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Invoices) != 1 || res.Invoices[0].Number != "INV-9" || res.InvoicesTotal != 1 || res.RowsTotal != 0 || res.DocumentType != "credit note" {
		t.Fatalf("res = %+v, want one invoice INV-9, totals 1 and 0, credit note", res)
	}
	if s, _ := res.Invoices[0].Payload["supplier"].(map[string]any); s["tin"] != "12345678-0001" {
		t.Errorf("supplier = %#v, want tin 12345678-0001", res.Invoices[0].Payload["supplier"])
	}
	if len(r.l.calls) != 1 {
		t.Errorf("loader calls = %d, want 1 (the note is evaluated)", len(r.l.calls))
	}
}

func TestCheck_LoadsRulesOnceWithDistinctDatesAndNoCustomRules(t *testing.T) {
	r := newRig()
	for _, csv := range []string{
		"Inv,Total,Date\nA,1,2026-10-01\nB,2,2026-10-01\n",
		"Inv,Total,Date\nA,1,2026-10-01\nB,2,\n",
	} {
		if _, err := r.c.Check(context.Background(), csvReq(csv, "issue_date", "Date")); err != nil {
			t.Fatal(err)
		}
	}
	want := [][]string{{"2026-10-01"}, {"2026-10-01", today}}
	if !slices.EqualFunc(r.l.calls, want, slices.Equal) || !slices.Equal(r.l.custom, []bool{true, true}) {
		t.Errorf("loader dates %v custom==nil %v, want %v [true true]", r.l.calls, r.l.custom, want)
	}
}

func TestCheck_RefusesAFileOneByteOverFifteenMiB(t *testing.T) {
	r := newRig()
	req := csvReq("")
	req.File = padded(threeInvoices, 15<<20+1)
	if _, err := r.c.Check(context.Background(), req); !errors.Is(err, publiccheck.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	r.untouched(t)
}

func TestCheck_AcceptsAFileOfExactlyFifteenMiB(t *testing.T) {
	r := newRig()
	req := csvReq("")
	req.File = padded(threeInvoices, 15<<20)
	req.Mapping["buyer_tin"] = "BTIN"
	res, err := r.c.Check(context.Background(), req)
	if err != nil || len(res.Invoices) != 2 {
		t.Fatalf("invoices %d err %v, want 2 nil", len(res.Invoices), err)
	}
}

func TestSuggestMapping_RefusesAFileOneByteOverFifteenMiB(t *testing.T) {
	r := newRig()
	_, err := r.c.SuggestMapping(context.Background(), padded(threeInvoices, 15<<20+1), "csv")
	if !errors.Is(err, publiccheck.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	r.untouched(t)
}

func TestCheck_RefusesAnUnknownFormat(t *testing.T) {
	r := newRig()
	if _, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("x"), Format: "png"}); !errors.Is(err, publiccheck.ErrFormat) {
		t.Fatalf("err = %v, want ErrFormat", err)
	}
	r.untouched(t)
}

func TestCheck_RefusesBytesThatAreNotTheirFormat(t *testing.T) {
	cfb := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	png := []byte("\x89PNG\r\n\x1a\n0000")
	for _, tc := range []struct {
		name, format string
		file         []byte
	}{
		{"pdf with PNG bytes", "pdf", png},
		{"pdf with a short magic", "pdf", []byte("%PDF")},
		{"docx with PDF bytes", "docx", []byte("%PDF-1.4")},
		{"xlsx not a zip", "xlsx", []byte("not a zip")},
		{"docx with a CFB header", "docx", cfb},
		{"xlsx with a CFB header", "xlsx", cfb},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig()
			_, err := r.c.Check(context.Background(), publiccheck.Request{File: tc.file, Format: tc.format})
			if !errors.Is(err, publiccheck.ErrFormat) {
				t.Fatalf("err = %v, want ErrFormat", err)
			}
			r.untouched(t)
		})
	}
}

func TestCheck_ACsvIsNotSignatureChecked(t *testing.T) {
	r := newRig()
	_, err := r.c.Check(context.Background(), csvReq("invoice_number,Total\nINV-1,100\n"))
	if errors.Is(err, publiccheck.ErrFormat) {
		t.Fatalf("err = %v, a csv must not be refused for its bytes", err)
	}
}

func TestSuggestMapping_RefusesAnXlsxThatIsNotAZip(t *testing.T) {
	r := newRig()
	if _, err := r.c.SuggestMapping(context.Background(), []byte("not a zip"), "xlsx"); !errors.Is(err, publiccheck.ErrFormat) {
		t.Fatalf("err = %v, want ErrFormat", err)
	}
	r.untouched(t)
}

func TestCheck_ACorruptSpreadsheetIsBadFile(t *testing.T) {
	r := newRig()
	_, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("PK\x03\x04 not a workbook"), Format: "xlsx", Mapping: map[string]string{"invoice_number": "Inv"}})
	if !errors.Is(err, publiccheck.ErrBadFile) || errors.Is(err, publiccheck.ErrFormat) {
		t.Fatalf("err = %v, want ErrBadFile", err)
	}
	r.untouched(t)
}

func TestCheck_AnAIOutageIsReadUnavailable(t *testing.T) {
	r := newRig()
	r.ai.fail = true
	_, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"})
	if !errors.Is(err, publiccheck.ErrReadUnavailable) {
		t.Fatalf("err = %v, want ErrReadUnavailable", err)
	}
	if len(r.l.calls) != 0 {
		t.Errorf("loader calls = %d, want 0", len(r.l.calls))
	}
}

func TestCheck_ADoclingRefusalOfTheFileIsBadFile(t *testing.T) {
	r := newRig()
	r.tx.err = fmt.Errorf("docling: /v1/read returned 422: encrypted: %w", extraction.ErrDocumentUnreadable)
	_, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"})
	if !errors.Is(err, publiccheck.ErrBadFile) || errors.Is(err, publiccheck.ErrReadUnavailable) {
		t.Fatalf("err = %v, want ErrBadFile and not ErrReadUnavailable", err)
	}
}

func TestCheck_ACorruptPDFInTheImagePathIsBadFile(t *testing.T) {
	r := newRig()
	r.tx.chars = 0
	r.c.Readers.Render = extraction.NewPDFiumEndsReader()
	_, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4 not really a pdf"), Format: "pdf"})
	if !errors.Is(err, publiccheck.ErrBadFile) || errors.Is(err, publiccheck.ErrReadUnavailable) {
		t.Fatalf("err = %v, want ErrBadFile and not ErrReadUnavailable", err)
	}
}

func TestCheck_ARenderFailureInTheImagePathIsReadUnavailable(t *testing.T) {
	r := newRig()
	r.tx.chars = 0
	r.rd.err = errors.New("pdfium: get instance: pool closed")
	_, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"})
	if !errors.Is(err, publiccheck.ErrReadUnavailable) || errors.Is(err, publiccheck.ErrBadFile) {
		t.Fatalf("err = %v, want ErrReadUnavailable and not ErrBadFile", err)
	}
}

func TestCheck_ADoclingErrorIsReadUnavailable(t *testing.T) {
	r := newRig()
	r.tx.err = errors.New("docling 500")
	_, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"})
	if !errors.Is(err, publiccheck.ErrReadUnavailable) {
		t.Fatalf("err = %v, want ErrReadUnavailable", err)
	}
	if len(r.l.calls) != 0 {
		t.Errorf("loader calls = %d, want 0", len(r.l.calls))
	}
}

func TestCheck_MarksItsAICallsAsPublicCheck(t *testing.T) {
	var got []airecord.Call
	sink := func(c airecord.Call) { got = append(got, c) }
	record := func(ctx context.Context) { airecord.Record(ctx, sink, nil, airecord.Call{Attempts: 1}) }

	r := newRig()
	r.ai.run = record
	if _, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"}); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || slices.ContainsFunc(got, func(c airecord.Call) bool { return !c.PublicCheck }) {
		t.Errorf("Check recorded %+v, want every call a public check", got)
	}

	got = nil
	r.m.run = record
	if _, err := r.c.SuggestMapping(context.Background(), []byte(threeInvoices), "csv"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].PublicCheck {
		t.Errorf("SuggestMapping recorded %+v, want one public check", got)
	}
}

func TestCheck_ARuleSetLoadErrorIsReturned(t *testing.T) {
	r := newRig()
	r.l.err = validation.ErrNoActiveRuleSet
	res, err := r.c.Check(context.Background(), csvReq(threeInvoices))
	if !errors.Is(err, validation.ErrNoActiveRuleSet) || res.Invoices != nil || res.InvoicesTotal != 0 {
		t.Errorf("res %+v err %v, want ErrNoActiveRuleSet and an empty result", res, err)
	}
}

func TestCheck_AnEngineFaultIsReturned(t *testing.T) {
	r := newRig()
	r.l.rules = []validation.Rule{{Key: "x", Type: "no-such-type", Target: "total", Enabled: true, Scope: "document"}}
	res, err := r.c.Check(context.Background(), csvReq(threeInvoices))
	if err == nil || res.Invoices != nil || res.InvoicesTotal != 0 {
		t.Errorf("res %+v err %v, want an error and an empty result", res, err)
	}
}

func TestCheck_APoorScanDocumentIsAProblemWithThePoorScanSentence(t *testing.T) {
	r := newRig()
	r.tx.chars = 0
	r.c.Readers.AI = nil
	res, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Invoices) != 1 || len(res.Invoices[0].Failures) != 1 {
		t.Fatalf("res = %+v, want one invoice with one failure", res)
	}
	f := res.Invoices[0].Failures[0]
	// importer.poorScanMessage
	if f.Path != "invoice_number" || !strings.Contains(f.Message, "too poor to read") || res.Invoices[0].Payload != nil {
		t.Errorf("failure = %+v, want the poor-scan sentence on invoice_number", f)
	}
	if len(r.l.calls) != 0 {
		t.Errorf("loader calls = %d, want 0", len(r.l.calls))
	}
}

func TestCheck_CompanyProfileRulesAreNotChecked(t *testing.T) {
	r := newRig()
	// Unsorted on purpose; every one of the eight targets carries a rule.
	targets := []string{"supplier.state", "supplier.lga", "supplier.telephone", "supplier.email", "supplier.street",
		"supplier.city", "supplier.postal_zone", "supplier.country"}
	r.l.rules = []validation.Rule{requiredRule("buyer-tin-required", "buyer.tin")}
	for _, tg := range targets {
		r.l.rules = append(r.l.rules, requiredRule(strings.ReplaceAll(tg, ".", "-")+"-required", tg))
	}
	// Two dates make the loader hand back the same rules twice: the keys must come out distinct.
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total,Date\nA,1,2026-10-01\nB,2,2026-10-02\n", "issue_date", "Date"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Invoices) != 2 {
		t.Fatalf("%d invoices, want 2", len(res.Invoices))
	}
	for _, inv := range res.Invoices {
		if len(inv.Failures) != 1 || inv.Failures[0].RuleKey != "buyer-tin-required" {
			t.Errorf("%s failures = %+v, want only buyer-tin-required", inv.Number, inv.Failures)
		}
	}
	want := []string{"supplier-city-required", "supplier-country-required", "supplier-email-required", "supplier-lga-required",
		"supplier-postal_zone-required", "supplier-state-required", "supplier-street-required", "supplier-telephone-required"}
	if !slices.Equal(res.NotChecked, want) {
		t.Errorf("NotChecked = %v, want %v (sorted, distinct)", res.NotChecked, want)
	}
}

func TestCheck_NotCheckedHasNoEmptyKeyForRepeatedRules(t *testing.T) {
	r := newRig()
	r.l.rules = []validation.Rule{requiredRule("supplier-city-required", "supplier.city"), requiredRule("supplier-city-required", "supplier.city")}
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total,Date\nA,1,2026-10-01\n", "issue_date", "Date"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"supplier-city-required"}; !slices.Equal(res.NotChecked, want) {
		t.Errorf("NotChecked = %q, want %q", res.NotChecked, want)
	}
}

func TestCheck_NotCheckedOmitsDisabledSupplierContactRules(t *testing.T) {
	r := newRig()
	off := requiredRule("supplier-city-required", "supplier.city")
	off.Enabled = false
	r.l.rules = []validation.Rule{off, requiredRule("supplier-email-required", "supplier.email")}
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total,Date\nA,1,2026-10-01\n", "issue_date", "Date"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotChecked) == 0 {
		t.Fatal("NotChecked is empty, want the enabled rule")
	}
	if want := []string{"supplier-email-required"}; !slices.Equal(res.NotChecked, want) {
		t.Errorf("NotChecked = %v, want %v", res.NotChecked, want)
	}
}

func TestCheck_AnEmptyFileIsBadFile(t *testing.T) {
	r := newRig()
	for _, f := range []string{"csv", "pdf"} {
		if _, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte{}, Format: f}); !errors.Is(err, publiccheck.ErrBadFile) {
			t.Errorf("%s: err = %v, want ErrBadFile", f, err)
		}
	}
	if _, err := r.c.SuggestMapping(context.Background(), nil, "csv"); !errors.Is(err, publiccheck.ErrBadFile) {
		t.Errorf("SuggestMapping: err = %v, want ErrBadFile", err)
	}
	r.untouched(t)
}

func TestSuggestMapping_RefusesANonSpreadsheetFormat(t *testing.T) {
	r := newRig()
	for _, f := range []string{"pdf", "docx", "png"} {
		if _, err := r.c.SuggestMapping(context.Background(), []byte("x"), f); !errors.Is(err, publiccheck.ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", f, err)
		}
	}
	r.untouched(t)
}

func TestCheck_AHeaderOnlyFileLoadsNoRules(t *testing.T) {
	r := newRig()
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total\n"))
	if err != nil || len(res.Invoices) != 0 || res.InvoicesTotal != 0 || res.RowsTotal != 0 {
		t.Errorf("res %+v err %v, want an empty result", res, err)
	}
	r.untouched(t)
}

func TestCheck_EveryCheckedInvoiceAProblemLoadsNoRules(t *testing.T) {
	r := newRig()
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total,Date\nA,1,31/02/2026\nB,2,32/01/2026\nC,3,2026-10-01\n", "issue_date", "Date"))
	if err != nil || len(res.Invoices) != 2 {
		t.Fatalf("invoices %d err %v, want 2 nil", len(res.Invoices), err)
	}
	for _, inv := range res.Invoices {
		if len(inv.Failures) != 1 {
			t.Errorf("%s failures = %+v, want one", inv.Number, inv.Failures)
		}
	}
	r.untouched(t)
}

func TestCheck_ACancelledContextStopsBeforeTheDecodeAndBeforeTheRuleLoad(t *testing.T) {
	r := newRig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.c.Check(ctx, csvReq(threeInvoices)); !errors.Is(err, context.Canceled) {
		t.Errorf("(a) err = %v, want context.Canceled", err)
	}
	if _, err := r.c.Check(ctx, publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"}); !errors.Is(err, context.Canceled) {
		t.Errorf("(a) pdf err = %v, want context.Canceled", err)
	}
	r.untouched(t)

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	r.c.Now = func() time.Time { cancel(); return fixedNow() }
	if _, err := r.c.Check(ctx, csvReq(threeInvoices)); !errors.Is(err, context.Canceled) {
		t.Errorf("(b) err = %v, want context.Canceled", err)
	}
	r.untouched(t)
}

func TestSuggestMapping_ACancelledContextIsReturned(t *testing.T) {
	r := newRig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.c.SuggestMapping(ctx, []byte(threeInvoices), "csv"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	r.untouched(t)
}

func TestPublicCheck_ImportingDocumentOrDocadapterWouldOpenABucketWrite(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	imports := strings.Fields(string(out))
	const mod = "github.com/SimonOsipov/invoice-os/internal/"
	if !slices.Contains(imports, mod+"importer") {
		t.Fatalf("imports %v lack internal/importer: the scan reads nothing", imports)
	}
	for _, banned := range []string{"document", "docadapter"} {
		if slices.Contains(imports, mod+banned) {
			t.Errorf("publiccheck imports internal/%s", banned)
		}
	}
}

func TestChecker_HoldsNoObjectStoreSinkOrLogger(t *testing.T) {
	fields := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			out = append(out, rt.Field(i).Name+" "+rt.Field(i).Type.String())
		}
		return out
	}
	wantChecker := []string{
		"LoadRules func(context.Context, []string, []validation.Rule) (map[string]validation.RuleSet, error)",
		"Engine *validation.Engine",
		"Mapper importer.MappingSuggester",
		"Readers extraction.CheckReaders",
		"Now func() time.Time",
	}
	if got := fields(publiccheck.Checker{}); !slices.Equal(got, wantChecker) {
		t.Errorf("Checker fields = %q, want %q", got, wantChecker)
	}
	wantReaders := []string{"Text extraction.PageReader", "Render extraction.PageReader",
		"AI extraction.AIReader", "Jev extraction.JevAsker"}
	if got := fields(extraction.CheckReaders{}); !slices.Equal(got, wantReaders) {
		t.Errorf("CheckReaders fields = %q, want %q", got, wantReaders)
	}
}

// A context that ends during the read comes back as the context's own error, not as an outage.
func TestCheck_AContextEndedDuringTheReadIsNotAnOutage(t *testing.T) {
	r := newRig()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.tx.err = errors.New("docling: request aborted")
	r.tx.onRun = cancel
	_, err := r.c.Check(ctx, publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"})
	if err != context.Canceled || errors.Is(err, publiccheck.ErrReadUnavailable) {
		t.Errorf("pdf err = %v, want context.Canceled itself, not ErrReadUnavailable", err)
	}
	r.tx.err, r.tx.onRun = nil, nil
	_, err = r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF-1.4"), Format: "pdf"})
	if err != nil {
		t.Fatalf("control: the same read with a live context failed: %v", err)
	}
}

// A mapping the check refuses is the caller's mistake, not a bad file: ErrValidation, never ErrBadFile.
func TestCheck_ARefusedMappingIsValidationNotBadFile(t *testing.T) {
	r := newRig()
	for name, mapping := range map[string]map[string]string{
		"no invoice number":    {"total": "Total"},
		"a key the check bars": {"invoice_number": "Inv", "supplier_email": "Total"},
	} {
		req := publiccheck.Request{File: []byte("Inv,Total\nA,1\n"), Format: "csv", Mapping: mapping}
		_, err := r.c.Check(context.Background(), req)
		if !errors.Is(err, importer.ErrValidation) || errors.Is(err, publiccheck.ErrBadFile) {
			t.Errorf("%s: err = %v, want importer.ErrValidation and not ErrBadFile", name, err)
		}
	}
	r.untouched(t)
}

func TestCheck_NotCheckedIsPerInvoice(t *testing.T) {
	r := newRig()
	r.c.LoadRules = func(_ context.Context, dates []string, _ []validation.Rule) (map[string]validation.RuleSet, error) {
		out := map[string]validation.RuleSet{}
		for _, d := range dates {
			rs := validation.RuleSet{Version: 4}
			if strings.HasPrefix(d, "2027") {
				rs.Rules = []validation.Rule{requiredRule("supplier-email-required", "supplier.email"), requiredRule("supplier-city-required", "supplier.city")}
			}
			out[d] = rs
		}
		return out, nil
	}
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total,Date\nA,1,2026-06-01\nB,2,2027-02-01\nC,3,2026-07-01\n", "issue_date", "Date"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Invoices) != 2 {
		t.Fatalf("%d invoices, want the first two of three (MaxInvoices)", len(res.Invoices))
	}
	if res.Invoices[0].NotChecked != nil {
		t.Errorf("invoice 1 NotChecked = %v, want nil", res.Invoices[0].NotChecked)
	}
	want := []string{"supplier-city-required", "supplier-email-required"}
	if !slices.Equal(res.Invoices[1].NotChecked, want) {
		t.Errorf("invoice 2 NotChecked = %v, want %v", res.Invoices[1].NotChecked, want)
	}
	if !slices.Equal(res.NotChecked, want) {
		t.Errorf("Result.NotChecked = %v, want the union %v", res.NotChecked, want)
	}
}

func TestCheck_AProblemInvoiceHasNoNotCheckedList(t *testing.T) {
	r := newRig()
	r.l.rules = []validation.Rule{requiredRule("supplier-email-required", "supplier.email")}
	res, err := r.c.Check(context.Background(), csvReq("Inv,Total,Date\nA,1,31/02/2026\nB,2,2026-10-01\n", "issue_date", "Date"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Invoices[0].Payload != nil || res.Invoices[0].NotChecked != nil {
		t.Errorf("problem invoice = %+v, want nil payload and nil NotChecked", res.Invoices[0])
	}
	if len(res.Invoices[1].NotChecked) != 1 {
		t.Errorf("checked invoice NotChecked = %v, want one key", res.Invoices[1].NotChecked)
	}
}

// bigText reads one page whose prompt is over the bound: 6,000 tokens of about 50 bytes.
type bigText struct{}

func (bigText) Name() string    { return "stub" }
func (bigText) Version() string { return "1" }
func (bigText) Read(_ context.Context, _ extraction.Document, onPage func(extraction.Page) error) (extraction.PageResult, error) {
	p := extraction.Page{Number: 1}
	for i := 0; i < 6_000; i++ {
		p.Tokens = append(p.Tokens, extraction.Token{
			Text:   fmt.Sprintf("row%05d %s", i, strings.Repeat("a", 40)),
			Region: extraction.Region{Page: 1, X0: 0.1, Y0: float64(i) / 7000, X1: 0.3, Y1: 0.9},
		})
	}
	return extraction.PageResult{Pages: 1, TextChars: 20}, onPage(p)
}

func TestCheck_ATooLongDocumentWrapsErrDocumentTooLong(t *testing.T) {
	r := newRig()
	r.c.Readers.Text = bigText{}
	_, err := r.c.Check(context.Background(), publiccheck.Request{File: []byte("%PDF"), Format: "pdf"})
	if !errors.Is(err, publiccheck.ErrDocumentTooLong) || errors.Is(err, publiccheck.ErrReadUnavailable) {
		t.Fatalf("err = %v, want ErrDocumentTooLong and not ErrReadUnavailable", err)
	}
	if r.ai.calls != 0 || len(r.l.calls) != 0 {
		t.Errorf("AI calls %d, loader calls %d, want 0", r.ai.calls, len(r.l.calls))
	}
}
