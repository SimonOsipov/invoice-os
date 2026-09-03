// worker_internal_test.go: the worker specs that need no database. Package extraction so they
// can name the unexported args type; everything needing a pool is in worker_db_test.go.
package extraction

import (
	"context"
	"errors"
	"go/ast"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

// The kind is persisted as river_job.kind, so a rename orphans every already-queued row.
func TestExtractArgs_KindIsStable(t *testing.T) {
	if got, want := (extractArgs{}).Kind(), "extraction_extract"; got != want {
		t.Errorf("Kind() = %q, want %q", got, want)
	}
}

func TestExtractArgs_InsertOptsPinQueueAndAttempts(t *testing.T) {
	opts := (extractArgs{}).InsertOpts()
	if opts.Queue != QueueName {
		t.Errorf("InsertOpts().Queue = %q, want QueueName (%q)", opts.Queue, QueueName)
	}
	if opts.MaxAttempts != 3 {
		t.Errorf("InsertOpts().MaxAttempts = %d, want 3", opts.MaxAttempts)
	}
}

// QueueName is persisted as river_job.queue, so it is pinned to its literal exactly as Kind is:
// TestExtractArgs_InsertOptsPinQueueAndAttempts compares the opts against QueueName and is
// therefore blind to QueueName itself being wrong.
func TestExtractArgs_QueueIsNotRiverDefault(t *testing.T) {
	if got, want := QueueName, "extraction"; got != want {
		t.Errorf("QueueName = %q, want %q", got, want)
	}
	// Not implied by the literal above: a River release that renamed its default queue onto
	// this name would silently put extraction work back on the submission pool.
	if river.QueueDefault == QueueName {
		t.Errorf("river.QueueDefault is now %q, the same queue extraction claims; the extraction pool is no longer isolated", river.QueueDefault)
	}
}

// EXTR-05-06 AC-6: flaggedCount stops at the top level. An alternative never carries its own
// reason by FieldResult's own contract, but the count must not descend into Alternatives
// regardless -- two alternatives here, alongside the one decided field that is actually
// flagged, so a loop that flattened Alternatives in would inflate the count past 1.
func TestExtractWorker_FlaggedCountIgnoresAlternatives(t *testing.T) {
	v := "x"
	results := []FieldResult{
		{Field: Field{Name: "invoice_number", Value: &v, Reason: ReasonNone}},
		{
			Field: Field{Name: "issue_date", Value: &v, Reason: ReasonAmbiguous},
			Alternatives: []Field{
				{Name: "issue_date", Value: &v},
				{Name: "issue_date", Value: &v},
			},
		},
	}
	if got, want := flaggedCount(results), 1; got != want {
		t.Errorf("flaggedCount = %d, want %d -- one decided field is flagged; its two alternatives must not inflate the count", got, want)
	}
}

// River resolves cmp.Or(workUnit.Timeout(), clientJobTimeout), so a per-worker Timeout wins
// over the client default without raising it for SubmitWorker and PollWorker too.
func TestExtractWorker_TimeoutExceedsRiverDefault(t *testing.T) {
	got := (&ExtractWorker{}).Timeout(nil)
	if want := 10 * time.Minute; got != want {
		t.Errorf("Timeout() = %v, want %v", got, want)
	}
	if got <= river.JobTimeoutDefault {
		t.Errorf("Timeout() = %v, want > river.JobTimeoutDefault (%v)", got, river.JobTimeoutDefault)
	}
}

// --- EXTR-08-03 T3-4: the success emission is the closure's last statement -------------

// wiCalleeName spells a call target as written: "w.Audit", "queue.OncePerJob".
func wiCalleeName(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			return x.Name + "." + e.Sel.Name
		}
	}
	return ""
}

// wiDirectCalls returns the calls to want made by body itself. It does not descend into
// nested function literals: a call from an inner closure is that closure's last statement,
// not this one's.
func wiDirectCalls(body *ast.BlockStmt, want string) []*ast.CallExpr {
	var out []*ast.CallExpr
	visit := func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if c, ok := n.(*ast.CallExpr); ok && wiCalleeName(c.Fun) == want {
			out = append(out, c)
		}
		return true
	}
	for _, st := range body.List {
		ast.Inspect(st, visit)
	}
	return out
}

// AC-D2. On the success path the audit write is the LAST statement inside the
// queue.OncePerJob closure, so the marker, the field results, the advance to succeeded and the
// audit row share one transaction and one fate. A statement placed after it would commit
// outside that guarantee's reach. Mirrors internal/submission's
// TestSubmissionAudit_FailureWriteIsLastInItsClosure.
func TestExtractWorker_AuditWriteIsLastInItsClosure(t *testing.T) {
	fset, files, parsed := auditPkgFiles(t)

	var scanned, onceClosures, auditClosures, matched int
	var sites []string

	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if ok && lit.Body != nil {
				scanned++
				if len(wiDirectCalls(lit.Body, "w.Audit")) > 0 {
					auditClosures++
				}
			}

			call, ok := n.(*ast.CallExpr)
			if !ok || wiCalleeName(call.Fun) != "queue.OncePerJob" {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.FuncLit)
				if !ok || lit.Body == nil {
					continue
				}
				onceClosures++
				calls := wiDirectCalls(lit.Body, "w.Audit")
				if len(calls) == 0 {
					continue
				}
				matched++
				loc := fset.Position(lit.Pos()).String()
				sites = append(sites, loc)
				if len(calls) != 1 {
					t.Errorf("the queue.OncePerJob closure at %s calls w.Audit %d times, want exactly 1 -- one terminal success is one event", loc, len(calls))
					continue
				}
				if len(lit.Body.List) == 0 {
					t.Errorf("the closure at %s has an empty body yet matched a call -- the walk is wrong", loc)
					continue
				}
				last := lit.Body.List[len(lit.Body.List)-1]
				ret, isReturn := last.(*ast.ReturnStmt)
				if !isReturn {
					t.Errorf("the queue.OncePerJob closure at %s ends in %T, want its w.Audit call to be the final return -- anything after it commits outside OncePerJob's reach", loc, last)
					continue
				}
				if len(ret.Results) != 1 || ret.Results[0] != calls[0] {
					t.Errorf("the queue.OncePerJob closure at %s ends in a return that is not its w.Audit call (returns %d expression(s)) -- the audit write must be the last statement", loc, len(ret.Results))
				}
			}
			return true
		})
	}

	// Floors: a broken walk finds no literals and reports every claim above as satisfied.
	if scanned < 10 {
		t.Fatalf("walked %d function literal(s) across %v, want >= 10 -- the walk is broken, so the counts below are vacuous", scanned, parsed)
	}
	if onceClosures < 1 {
		t.Fatalf("found %d queue.OncePerJob closure(s), want >= 1 -- the success path is not where this walk thinks it is", onceClosures)
	}
	if matched != 1 {
		t.Fatalf("queue.OncePerJob closures calling w.Audit = %d, want exactly 1 (found at %v) -- the set is closed; a second success-path emitter is a decision, not something this test waves through", matched, sites)
	}
	// The two terminal points, and only those two: the OncePerJob closure and the
	// dead_lettered branch's own transaction.
	if auditClosures != 2 {
		t.Errorf("closures calling w.Audit = %d, want exactly 2 -- one per terminal point", auditClosures)
	}
}

// TestMockExtractor_DefaultResultFlaggedCountIgnoresAlternatives (RED-FIRST): AC-9, over the
// REAL default result rather than a hand-built one. TestExtractWorker_FlaggedCountIgnoresAlternatives
// above proves the loop stops at the top level; nothing connected that to what the mock actually
// ships, so a mock whose alternatives were lifted into decided fields would pass it.
func TestMockExtractor_DefaultResultFlaggedCountIgnoresAlternatives(t *testing.T) {
	results, err := NewMockExtractor().Extract(context.Background(),
		Document{Bytes: []byte("no fixture claims these bytes"), ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	var decided, alternatives, flagged int
	for _, r := range results {
		decided++
		alternatives += len(r.Alternatives)
		if r.Reason != ReasonNone {
			flagged++
		}
	}
	// Floors: with no alternatives to descend into, the count below cannot tell a loop that
	// stops at the top level from one that does not.
	if alternatives == 0 {
		t.Fatalf("the default result carries %d decided field(s) and no alternatives; this spec would pass on a flaggedCount that flattened them", decided)
	}

	if got, want := flaggedCount(results), 7; got != want {
		t.Errorf("flaggedCount over the default result = %d, want %d -- %d decided field(s) carry a reason and %d alternative(s) must not be counted",
			got, want, flagged, alternatives)
	}
}

// --- EXTR-17-01: the text-read seam -----------------------------------------------------

// The named seam is satisfied by (*Store).AnchorRulesFor with no adapter. A func type nothing
// references drifts silently.
var _ LoadAnchorRules = (&Store{}).AnchorRulesFor

// rtPage is one page the fake reader hands to onPage, plus the tokens its TokenPage must carry.
type rtPage struct {
	number int
	tokens []Token
	tables []Table
}

// rtReader is a PageReader driven entirely by the spec. It reuses ONE image buffer across every
// page and scribbles over it after each callback returns, which is what Page.ImagePNG's
// borrowed-not-owned contract permits (pagereader.go:70-72). No shipped reader does this today
// -- PDFium allocates a fresh buffer per page and Docling sets no image at all -- so the guard
// is on the contract, which is what stops a future reader from being unsafe.
type rtReader struct {
	pages []rtPage
	res   PageResult
	err   error

	buf   []byte
	calls int
}

func (*rtReader) Name() string    { return "extr-17-fake-reader" }
func (*rtReader) Version() string { return "v1" }

func (r *rtReader) Read(_ context.Context, _ Document, onPage func(Page) error) (PageResult, error) {
	r.buf = []byte{0xDE, 0xAD, 0xBE, 0xEF}
	for _, p := range r.pages {
		r.calls++
		if err := onPage(Page{
			Number:      p.number,
			WidthPt:     595,
			HeightPt:    842,
			ImageWidth:  1240,
			ImageHeight: 1754,
			ImagePNG:    r.buf,
			Tokens:      p.tokens,
			Tables:      p.tables,
		}); err != nil {
			return PageResult{}, err
		}
		// The buffer is invalid the moment the callback returns.
		for i := range r.buf {
			r.buf[i] = 0x00
		}
	}
	if r.err != nil {
		return PageResult{}, r.err
	}
	return r.res, nil
}

func rtToken(s string, page int) Token {
	return Token{Text: s, Region: Region{Page: page, X0: 0.1, Y0: 0.1, X1: 0.2, Y1: 0.2}}
}

// rtOutOfOrder emits 3, 1, 2 with page 1 carrying nothing at all. Three pages in a non-ascending
// order is the smallest fixture that can tell "kept the callback order" from "sorted"; the empty
// page is what catches a readText that skips a page with no tokens.
func rtOutOfOrder() *rtReader {
	return &rtReader{
		pages: []rtPage{
			{number: 3, tokens: []Token{rtToken("Total", 3)}},
			{number: 1},
			{number: 2, tokens: []Token{rtToken("Invoice", 2), rtToken("INV-1", 2)}},
		},
		res: PageResult{Pages: 3, TextChars: 4242, PagesWithText: 2},
	}
}

// EXTR-17-01 AC-7. Page.ImagePNG is borrowed for the length of the onPage call, so a readText
// that kept the slice hands its caller bytes that belong to a later page, or to nothing.
func TestReadText_DoesNotRetainBorrowedPageImages(t *testing.T) {
	r := rtOutOfOrder()

	pages, _, _, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	// Floor: an empty result satisfies the loop below without asserting anything.
	if len(pages) != 3 {
		t.Fatalf("readText returned %d page(s), want 3; the nil-image assertion below would be vacuous", len(pages))
	}
	for i, p := range pages {
		if p.ImagePNG != nil {
			t.Errorf("pages[%d] (page %d) carries %d image byte(s), want nil: the buffer is borrowed for the length of the onPage call and the reader has already overwritten it", i, p.Number, len(p.ImagePNG))
		}
	}
}

// EXTR-17-01 AC-8. The two slices are one document seen twice, so they are asserted by index
// alignment, not by a fixture whose pages happen to arrive in order. This fails a readText that
// sorted one slice, dropped a page, or skipped the empty one.
func TestReadText_PagesAndTokenPagesAreIndexAligned(t *testing.T) {
	r := rtOutOfOrder()

	pages, tokenPages, _, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	if len(pages) != len(tokenPages) {
		t.Fatalf("readText returned %d page(s) and %d token page(s); the two are one document seen twice and every caller indexes them together", len(pages), len(tokenPages))
	}
	if len(pages) != 3 {
		t.Fatalf("readText returned %d page(s), want 3 -- the reader emitted 3, including one with no tokens", len(pages))
	}
	for i := range pages {
		if pages[i].Number != tokenPages[i].Number {
			t.Errorf("pages[%d] is page %d but tokenPages[%d] is page %d; the two slices are out of alignment", i, pages[i].Number, i, tokenPages[i].Number)
		}
	}

	// Callback order, not sorted: PageReader's own contract is ascending, and a second,
	// divergent ordering policy here would disagree with CollectTokens, which never sorts.
	var got []int
	for _, p := range pages {
		got = append(got, p.Number)
	}
	if want := []int{3, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("readText returned pages %v, want %v -- append order, never a sort", got, want)
	}

	// Not vacuous on the token side: the empty page must still carry its geometry across.
	for i, tp := range tokenPages {
		if tp.WidthPt != 595 || tp.HeightPt != 842 {
			t.Errorf("tokenPages[%d] (page %d) is %vx%v pt, want 595x842", i, tp.Number, tp.WidthPt, tp.HeightPt)
		}
	}
	if n := len(tokenPages[2].Tokens); n != 2 {
		t.Errorf("tokenPages[2] (page %d) carries %d token(s), want 2", tokenPages[2].Number, n)
	}
	if n := len(tokenPages[1].Tokens); n != 0 {
		t.Errorf("tokenPages[1] (page %d) carries %d token(s), want 0", tokenPages[1].Number, n)
	}
}

// EXTR-17-01 AC-4/AC-6. Tables are copied through as-is, a page with tokens and no tables is
// neither an error nor a skip, and PageResult is returned because the caller classifies on
// res.TextChars.
func TestReadText_KeepsTablesPerPage(t *testing.T) {
	cells := []TableCell{
		{Row: 0, Col: 0, RowSpan: 1, ColSpan: 2, Text: "Description"},
		{Row: 1, Col: 0, RowSpan: 1, ColSpan: 1, Text: "Widget"},
		{Row: 1, Col: 1, RowSpan: 1, ColSpan: 1, Text: "1,000.00"},
	}
	r := &rtReader{
		pages: []rtPage{
			{number: 1, tokens: []Token{rtToken("Invoice", 1)}},
			{number: 2, tokens: []Token{rtToken("Total", 2)}, tables: []Table{{Rows: 2, Cols: 2, Cells: cells}}},
		},
		res: PageResult{Pages: 2, TextChars: 4242, PagesWithText: 2},
	}

	pages, tokenPages, res, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	if len(pages) != 2 || len(tokenPages) != 2 {
		t.Fatalf("readText returned %d page(s) and %d token page(s), want 2 and 2 -- a page carrying no table is not a page to skip", len(pages), len(tokenPages))
	}
	if pages[0].Tables != nil {
		t.Errorf("pages[0] carries %d table(s), want none: nil Tables is PDFium's normal shape and must not be invented", len(pages[0].Tables))
	}
	if !reflect.DeepEqual(pages[1].Tables, []Table{{Rows: 2, Cols: 2, Cells: cells}}) {
		t.Errorf("pages[1].Tables = %+v, want the 2x2 table the reader emitted, as-is", pages[1].Tables)
	}

	// The reader's own totals reach the caller: the classifier branches on res.TextChars, and
	// a dropped PageResult sends every document down the no-text-layer path.
	if res.TextChars != 4242 || res.Pages != 2 || res.PagesWithText != 2 {
		t.Errorf("readText returned PageResult %+v, want the reader's own {Pages:2 TextChars:4242 PagesWithText:2}", res)
	}
}

// EXTR-17-01 AC-9. A half-read document otherwise yields half the line items with totals that
// silently disagree with the invoice, so the pages already collected go with the error.
func TestReadText_DiscardsEverythingOnError(t *testing.T) {
	boom := errors.New("extr-17 fake reader: sidecar went away")
	r := &rtReader{
		pages: []rtPage{
			{number: 1, tokens: []Token{rtToken("Invoice", 1)}},
			{number: 2, tokens: []Token{rtToken("Total", 2)}},
		},
		res: PageResult{Pages: 2, TextChars: 4242},
		err: boom,
	}

	pages, tokenPages, res, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if !errors.Is(err, boom) {
		t.Fatalf("readText returned error %v, want %v", err, boom)
	}
	// Floor: the reader must actually have emitted pages before failing, or "discards
	// everything" is asserted over a read that collected nothing in the first place.
	if r.calls != 2 {
		t.Fatalf("the fake reader delivered %d page(s) before failing, want 2; nothing was collected, so the assertions below are vacuous", r.calls)
	}
	if pages != nil {
		t.Errorf("readText returned %d page(s) alongside its error, want nil", len(pages))
	}
	if tokenPages != nil {
		t.Errorf("readText returned %d token page(s) alongside its error, want nil", len(tokenPages))
	}
	if res != (PageResult{}) {
		t.Errorf("readText returned PageResult %+v alongside its error, want the zero value", res)
	}
}

// EXTR-17-01 AC-9. A zero-page read is a document with no text layer, which EXTR-17-02
// classifies from res.TextChars. Calling it text_not_read here would label a scanned PDF an
// infrastructure failure.
func TestReadText_ZeroPagesIsNotAnError(t *testing.T) {
	r := &rtReader{res: PageResult{}}

	pages, tokenPages, res, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText over a zero-page read returned %v, want nil: a document with no text layer is a classification, not a read failure", err)
	}
	if r.calls != 0 {
		t.Fatalf("the fake reader delivered %d page(s), want 0", r.calls)
	}
	if pages != nil {
		t.Errorf("readText returned %d page(s), want nil", len(pages))
	}
	if tokenPages != nil {
		t.Errorf("readText returned %d token page(s), want nil", len(tokenPages))
	}
	if res.TextChars != 0 || res.Pages != 0 {
		t.Errorf("readText returned PageResult %+v, want the zero value the reader reported", res)
	}
}
