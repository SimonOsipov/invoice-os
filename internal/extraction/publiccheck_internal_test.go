package extraction

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// pcReader scripts a PageReader: pages are delivered in order; after each onPage the PNG buffer
// is overwritten, as a real reader reuses it.
type pcReader struct {
	pages []Page
	chars int
	err   error
	calls int
	onRun func()
}

func (r *pcReader) Name() string    { return "pc" }
func (r *pcReader) Version() string { return "1" }
func (r *pcReader) Read(_ context.Context, _ Document, onPage func(Page) error) (PageResult, error) {
	r.calls++
	if r.onRun != nil {
		r.onRun()
	}
	if r.err != nil {
		return PageResult{}, r.err
	}
	for _, p := range r.pages {
		if err := onPage(p); err != nil {
			return PageResult{}, err
		}
		for i := range p.ImagePNG {
			p.ImagePNG[i] = 'X'
		}
	}
	return PageResult{Pages: len(r.pages), TextChars: r.chars}, nil
}

// pcAI answers by purpose and records the call order into *order.
type pcAI struct {
	enabled bool
	order   *[]string
	header  map[string]any
	lines   map[string]any
	failOn  ai.Purpose
	reqs    []ai.Request
	ctxs    []context.Context
}

func (a *pcAI) Enabled() bool { return a.enabled }
func (a *pcAI) Call(ctx context.Context, req ai.Request) (map[string]any, error) {
	a.ctxs = append(a.ctxs, ctx)
	a.reqs = append(a.reqs, req)
	*a.order = append(*a.order, string(req.Purpose))
	if req.Purpose == a.failOn {
		return nil, ai.ErrUnavailable
	}
	if req.Purpose == ai.PurposeLineItems {
		return a.lines, nil
	}
	return a.header, nil
}

type pcJev struct {
	order *[]string
	resp  jev.Response
	err   error
	calls int
	ctxs  []context.Context
}

func (j *pcJev) Enabled() bool { return true }
func (j *pcJev) Ask(ctx context.Context, _ jev.Request) (jev.Response, error) {
	j.ctxs = append(j.ctxs, ctx)
	j.calls++
	*j.order = append(*j.order, "jev")
	return j.resp, j.err
}

func pcTextPage() Page {
	return Page{Number: 1, Tokens: []Token{
		tok("Invoice INV-9", 1, 0.10, 0.10, 0.40, 0.12),
		tok("Widget", 1, 0.10, 0.30, 0.30, 0.32),
		tok("Gadget", 1, 0.10, 0.34, 0.30, 0.36),
	}}
}

func pcRender(pngs ...string) *pcReader {
	r := &pcReader{}
	for i, p := range pngs {
		r.pages = append(r.pages, Page{Number: i + 1, ImagePNG: []byte(p)})
	}
	return r
}

func pcValue(fields []FieldResult, name string) string {
	for _, f := range fields {
		if f.Name == name && f.Value != nil {
			return *f.Value
		}
	}
	return ""
}

func TestReadForCheck_TextDocumentAsksHeaderThenLinesThenJev(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order,
		header: map[string]any{"invoice_number": "INV-9"},
		lines:  map[string]any{"line_items": []any{map[string]any{"description": "Widget"}}}}
	j := &pcJev{order: &order, resp: jev.Response{Answers: map[string]jev.Answer{
		documentTypeQuestionID: {Type: jev.TypeChoice, Choice: "credit note", Confidence: 0.95},
	}}}
	r := CheckReaders{Text: &pcReader{pages: []Page{pcTextPage()}, chars: 20}, AI: a, Jev: j}

	fields, docType, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{string(ai.PurposeDocument), string(ai.PurposeLineItems), "jev"}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("call order = %v, want %v", order, want)
	}
	if got := pcValue(fields, "invoice_number"); got != "INV-9" {
		t.Errorf("invoice_number = %q, want INV-9", got)
	}
	if docType != "credit note" {
		t.Errorf("docType = %q, want credit note", docType)
	}
}

func TestReadForCheck_FailedHeaderCallIsUnavailable(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order, failOn: ai.PurposeDocument}
	j := &pcJev{order: &order}
	r := CheckReaders{Text: &pcReader{pages: []Page{pcTextPage()}, chars: 20}, AI: a, Jev: j}

	_, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf")
	if !errors.Is(err, ErrCheckAIUnavailable) {
		t.Fatalf("err = %v, want ErrCheckAIUnavailable", err)
	}
	if len(a.reqs) != 1 || j.calls != 0 {
		t.Errorf("AI calls = %d (want 1, header only), jev calls = %d (want 0)", len(a.reqs), j.calls)
	}
}

func TestReadForCheck_FailedLineCallIsUnavailable(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order, failOn: ai.PurposeLineItems, header: map[string]any{}}
	j := &pcJev{order: &order}
	r := CheckReaders{Text: &pcReader{pages: []Page{pcTextPage()}, chars: 20}, AI: a, Jev: j}

	_, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf")
	if !errors.Is(err, ErrCheckAIUnavailable) {
		t.Fatalf("err = %v, want ErrCheckAIUnavailable", err)
	}
	if j.calls != 0 {
		t.Errorf("jev calls = %d, want 0", j.calls)
	}
}

func TestReadForCheck_NoTextPDFSendsFirstAndLastPageFromMemory(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order, header: map[string]any{"invoice_number": "INV-9"}}
	render := pcRender("p1", "p2", "p3")
	r := CheckReaders{Text: &pcReader{}, Render: render, AI: a}

	fields, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.reqs) != 1 || len(a.reqs[0].Pages) != 2 ||
		!bytes.Equal(a.reqs[0].Pages[0], []byte("p1")) || !bytes.Equal(a.reqs[0].Pages[1], []byte("p3")) {
		t.Fatalf("AI requests = %+v, want one with Pages [p1 p3]", a.reqs)
	}
	if got := pcValue(fields, "invoice_number"); got != "INV-9" {
		t.Errorf("invoice_number = %q, want INV-9 (the image reading)", got)
	}
}

func TestReadForCheck_NoTextOnePagePDFSendsThatPage(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order, header: map[string]any{"invoice_number": "INV-9"}}
	r := CheckReaders{Text: &pcReader{}, Render: pcRender("p1"), AI: a}

	if _, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf"); err != nil {
		t.Fatal(err)
	}
	if len(a.reqs) != 1 || len(a.reqs[0].Pages) != 1 || !bytes.Equal(a.reqs[0].Pages[0], []byte("p1")) {
		t.Fatalf("AI requests = %+v, want one with Pages [p1]", a.reqs)
	}
}

func pcIsPoorScan(fields []FieldResult) bool {
	return len(fields) == 1 && fields[0].Name == "document_text_layer" && fields[0].Reason == ReasonUnreadable
}

func TestReadForCheck_NoTextDOCXIsAPoorScan(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order}
	render := pcRender("p1")
	r := CheckReaders{Text: &pcReader{}, Render: render, AI: a}

	fields, _, err := ReadForCheck(context.Background(), r, []byte("PK"), "docx")
	if err != nil {
		t.Fatal(err)
	}
	if !pcIsPoorScan(fields) || len(a.reqs) != 0 || render.calls != 0 {
		t.Errorf("fields = %+v, AI calls = %d, render calls = %d; want the poor-scan marker and 0, 0", fields, len(a.reqs), render.calls)
	}
}

func TestReadForCheck_NoTextPDFWithAIOffIsAPoorScan(t *testing.T) {
	var order []string
	a := &pcAI{enabled: false, order: &order}
	render := pcRender("p1")
	r := CheckReaders{Text: &pcReader{}, Render: render, AI: a}

	fields, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !pcIsPoorScan(fields) || render.calls != 0 || len(a.reqs) != 0 {
		t.Errorf("fields = %+v, render calls = %d; want the poor-scan marker and 0", fields, render.calls)
	}
}

func TestReadForCheck_ReaderErrorIsReturned(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order}
	boom := errors.New("docling down")
	r := CheckReaders{Text: &pcReader{err: boom}, AI: a}

	if _, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if len(a.reqs) != 0 {
		t.Errorf("AI calls = %d, want 0", len(a.reqs))
	}
}

func TestReadForCheck_NilTextReaderIsAnError(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order}
	if _, _, err := ReadForCheck(context.Background(), CheckReaders{AI: a}, []byte("%PDF"), "pdf"); err == nil {
		t.Fatal("err = nil, want an error")
	}
	if len(a.reqs) != 0 {
		t.Errorf("AI calls = %d, want 0", len(a.reqs))
	}
}

func TestReadForCheck_ContextEndedDuringTheReadReturnsItsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	a := &pcAI{enabled: false, order: &order}
	r := CheckReaders{Text: &pcReader{pages: []Page{pcTextPage()}, chars: 20, onRun: cancel}, AI: a}

	fields, _, err := ReadForCheck(ctx, r, []byte("%PDF"), "pdf")
	if !errors.Is(err, context.Canceled) || fields != nil {
		t.Fatalf("fields = %+v, err = %v; want nil and context.Canceled", fields, err)
	}
}

func TestReadForCheck_NilOrFailingJevLeavesNoVerdict(t *testing.T) {
	for name, jv := range map[string]JevAsker{
		"nil":     nil,
		"failing": &pcJev{order: new([]string), err: errors.New("jev down")},
	} {
		t.Run(name, func(t *testing.T) {
			var order []string
			a := &pcAI{enabled: true, order: &order, header: map[string]any{"invoice_number": "INV-9"}}
			r := CheckReaders{Text: &pcReader{pages: []Page{pcTextPage()}, chars: 20}, AI: a, Jev: jv}

			fields, docType, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf")
			if err != nil || docType != "" || pcValue(fields, "invoice_number") != "INV-9" {
				t.Errorf("fields number = %q, docType = %q, err = %v; want INV-9, \"\", nil", pcValue(fields, "invoice_number"), docType, err)
			}
		})
	}
}

func TestReadForCheck_LineAnswerBecomesLineItemFields(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order, header: map[string]any{},
		lines: map[string]any{"line_items": []any{
			map[string]any{"description": "Widget"},
			map[string]any{"description": "Gadget"},
		}}}
	r := CheckReaders{Text: &pcReader{pages: []Page{pcTextPage()}, chars: 20}, AI: a}

	fields, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got := pcValue(fields, "line_items[1].description"); got != "Widget" {
		t.Errorf("line_items[1].description = %q, want Widget", got)
	}
	if got := pcValue(fields, "line_items[2].description"); got != "Gadget" {
		t.Errorf("line_items[2].description = %q, want Gadget", got)
	}
}

// pcMarked reports whether a call made under ctx would be recorded as a public check.
func pcMarked(ctx context.Context) bool {
	var got airecord.Call
	airecord.Record(ctx, func(c airecord.Call) { got = c }, nil, airecord.Call{Attempts: 1})
	return got.PublicCheck
}

func TestReadForCheck_EveryAICallCarriesThePublicCheckMark(t *testing.T) {
	var order []string
	a := &pcAI{enabled: true, order: &order, header: map[string]any{}}
	j := &pcJev{order: &order}
	r := CheckReaders{Text: &pcReader{pages: []Page{pcTextPage()}, chars: 20}, AI: a, Jev: j}
	if _, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf"); err != nil {
		t.Fatal(err)
	}
	ctxs := append(append([]context.Context{}, a.ctxs...), j.ctxs...)
	if len(ctxs) != 3 {
		t.Fatalf("calls = %d, want 3", len(ctxs))
	}
	for i, c := range ctxs {
		if !pcMarked(c) {
			t.Errorf("call %d is not marked as a public check", i)
		}
	}

	a2 := &pcAI{enabled: true, order: &order, header: map[string]any{}}
	r2 := CheckReaders{Text: &pcReader{}, Render: pcRender("p1"), AI: a2}
	if _, _, err := ReadForCheck(context.Background(), r2, []byte("%PDF"), "pdf"); err != nil {
		t.Fatal(err)
	}
	if len(a2.ctxs) != 1 || !pcMarked(a2.ctxs[0]) {
		t.Errorf("the image call is not marked as a public check")
	}
}

func TestReadForCheck_NoTextPDFImagePathFailuresAreNotAReading(t *testing.T) {
	var order []string
	boom := errors.New("pdfium 500")
	cases := []struct {
		name    string
		render  *pcReader
		ai      *pcAI
		wantErr error
		poor    bool
		aiCalls int
	}{
		{"render error", &pcReader{err: boom}, &pcAI{enabled: true, order: &order}, boom, false, 0},
		{"render yields no page", &pcReader{}, &pcAI{enabled: true, order: &order}, nil, true, 0},
		{"image call fails", pcRender("p1"), &pcAI{enabled: true, order: &order, failOn: ai.PurposeDocument}, ErrCheckAIUnavailable, false, 1},
		{"image answer is empty", pcRender("p1"), &pcAI{enabled: true, order: &order, header: map[string]any{}}, nil, true, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := CheckReaders{Text: &pcReader{}, Render: c.render, AI: c.ai}
			fields, _, err := ReadForCheck(context.Background(), r, []byte("%PDF"), "pdf")
			if !errors.Is(err, c.wantErr) || (c.wantErr == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if c.poor != pcIsPoorScan(fields) {
				t.Errorf("fields = %+v, poor scan = %v, want %v", fields, pcIsPoorScan(fields), c.poor)
			}
			if len(c.ai.reqs) != c.aiCalls {
				t.Errorf("AI calls = %d, want %d", len(c.ai.reqs), c.aiCalls)
			}
		})
	}
}
