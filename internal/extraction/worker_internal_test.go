// worker_internal_test.go: the worker specs that need no database. Package extraction so they
// can name the unexported args type; everything needing a pool is in worker_db_test.go.
package extraction

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
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
// borrowed-not-owned contract permits (pagereader.go:63-65). No shipped reader does this today
// -- PDFium allocates a fresh buffer per page and Docling sets no image at all -- so the guard
// is on the contract, which is what stops a future reader from being unsafe.
type rtReader struct {
	pages []rtPage
	res   PageResult
	err   error
	// failAfter is the 1-based page after which Read gives up with err. Zero reads every page.
	failAfter int

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
		if r.failAfter > 0 && r.calls == r.failAfter {
			return PageResult{}, r.err
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

// --- EXTR-17-01: adversarial ------------------------------------------------------------

// Two pages carrying the same number is a malformed read, not readText's to repair: a collector
// keyed by page number, or one that deduped, silently loses a page's line items.
func TestReadText_KeepsDuplicatePageNumbers(t *testing.T) {
	r := &rtReader{
		pages: []rtPage{
			{number: 2, tokens: []Token{rtToken("Invoice", 2)}},
			{number: 2, tokens: []Token{rtToken("Total", 2)}},
			{number: 5, tokens: []Token{rtToken("Page 5", 5)}},
		},
		res: PageResult{Pages: 3, TextChars: 21, PagesWithText: 3},
	}

	pages, tokenPages, _, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	if len(pages) != 3 || len(tokenPages) != 3 {
		t.Fatalf("readText returned %d page(s) and %d token page(s), want 3 and 3 -- a repeated page number is not a page to drop", len(pages), len(tokenPages))
	}
	var got []int
	for i := range pages {
		if pages[i].Number != tokenPages[i].Number {
			t.Errorf("pages[%d] is page %d but tokenPages[%d] is page %d", i, pages[i].Number, i, tokenPages[i].Number)
		}
		got = append(got, pages[i].Number)
	}
	if want := []int{2, 2, 5}; !slices.Equal(got, want) {
		t.Errorf("readText returned pages %v, want %v", got, want)
	}
	// The two page 2s are distinct rows, not one merged one.
	if a, b := len(tokenPages[0].Tokens), len(tokenPages[1].Tokens); a != 1 || b != 1 {
		t.Errorf("the two page-2 token pages carry %d and %d token(s), want 1 and 1 -- they were merged", a, b)
	}
}

// A scanned table: Tables set, Tokens empty. The mirror of the tokens-and-no-tables case, and
// the one that catches a readText skipping a page it judged to hold no text.
func TestReadText_KeepsAPageWithTablesButNoTokens(t *testing.T) {
	tbl := []Table{{Rows: 1, Cols: 1, Cells: []TableCell{{Row: 0, Col: 0, RowSpan: 1, ColSpan: 1, Text: "Widget"}}}}
	r := &rtReader{
		pages: []rtPage{
			{number: 1, tokens: []Token{rtToken("Invoice", 1)}},
			{number: 2, tables: tbl},
		},
		res: PageResult{Pages: 2, TextChars: 7, PagesWithText: 1},
	}

	pages, tokenPages, _, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	if len(pages) != 2 || len(tokenPages) != 2 {
		t.Fatalf("readText returned %d page(s) and %d token page(s), want 2 and 2 -- a page whose text is all table is still a page", len(pages), len(tokenPages))
	}
	if !reflect.DeepEqual(pages[1].Tables, tbl) {
		t.Errorf("pages[1].Tables = %+v, want the table the reader emitted, as-is", pages[1].Tables)
	}
	if n := len(tokenPages[1].Tokens); n != 0 {
		t.Errorf("tokenPages[1] carries %d token(s), want 0", n)
	}
	if tokenPages[1].Number != 2 {
		t.Errorf("tokenPages[1] is page %d, want 2 -- the token page must exist even with no tokens, or every later index is off by one", tokenPages[1].Number)
	}
}

// PageResult is the reader's self-report. Length comes from the callbacks that actually ran, so
// a reader that miscounts cannot shorten or lengthen what the caller iterates.
func TestReadText_LengthComesFromTheCallbacksNotThePageResult(t *testing.T) {
	r := &rtReader{
		pages: []rtPage{
			{number: 1, tokens: []Token{rtToken("Invoice", 1)}},
			{number: 2, tokens: []Token{rtToken("Total", 2)}},
		},
		res: PageResult{Pages: 99, TextChars: 12, PagesWithText: 99},
	}

	pages, tokenPages, res, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	if len(pages) != 2 || len(tokenPages) != 2 {
		t.Fatalf("readText returned %d page(s) and %d token page(s), want 2 and 2 -- the reader's own Pages count is a self-report, never the length", len(pages), len(tokenPages))
	}
	// Passed through verbatim, not corrected: EXTR-17-02 classifies on what the reader said.
	if res.Pages != 99 || res.PagesWithText != 99 {
		t.Errorf("readText returned PageResult %+v, want the reader's own {Pages:99 PagesWithText:99} unaltered", res)
	}
}

// ImagePNG is the one field readText clears. A rebuild that named fields one by one would drop
// the geometry every Region scales by and nothing else here would notice.
func TestReadText_CarriesEveryOtherPageFieldThrough(t *testing.T) {
	r := rtOutOfOrder()

	pages, _, _, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("readText returned %d page(s), want 3; the field sweep below would be vacuous", len(pages))
	}
	for i, p := range pages {
		if p.WidthPt != 595 || p.HeightPt != 842 {
			t.Errorf("pages[%d] (page %d) is %vx%v pt, want 595x842", i, p.Number, p.WidthPt, p.HeightPt)
		}
		if p.ImageWidth != 1240 || p.ImageHeight != 1754 {
			t.Errorf("pages[%d] (page %d) rendered %dx%d px, want 1240x1754 -- the pixel geometry is what a canvas scales a Region by", i, p.Number, p.ImageWidth, p.ImageHeight)
		}
	}
	// The tokens are the reader's own slice, not a copy: CollectTokens takes it as-is and the
	// resolver reads both slices expecting one document.
	if n := len(pages[2].Tokens); n != 2 {
		t.Errorf("pages[2] (page %d) carries %d token(s), want 2", pages[2].Number, n)
	}
}

// The error arrives with pages already collected and pages still unread -- the shape a sidecar
// dying mid-document has. The pages that did arrive go with it.
func TestReadText_DiscardsAPartialReadThatFailsMidStream(t *testing.T) {
	boom := errors.New("extr-17 fake reader: sidecar died on page 2")
	r := &rtReader{
		pages: []rtPage{
			{number: 1, tokens: []Token{rtToken("Invoice", 1)}},
			{number: 2, tokens: []Token{rtToken("Total", 2)}},
			{number: 3, tokens: []Token{rtToken("Page 3", 3)}},
		},
		res:       PageResult{Pages: 3, TextChars: 4242},
		err:       boom,
		failAfter: 1,
	}

	pages, tokenPages, res, err := readText(context.Background(), r, Document{ContentType: "application/pdf"})
	if !errors.Is(err, boom) {
		t.Fatalf("readText returned error %v, want %v", err, boom)
	}
	// Floor: one page in, two never read. Without it this asserts over a read that collected
	// nothing, which the zero-page spec already covers.
	if r.calls != 1 {
		t.Fatalf("the fake reader delivered %d page(s) before failing, want 1", r.calls)
	}
	if pages != nil || tokenPages != nil {
		t.Errorf("readText returned %d page(s) and %d token page(s) alongside its error, want nil and nil -- half a document yields half the line items under a total that disagrees with them", len(pages), len(tokenPages))
	}
	if res != (PageResult{}) {
		t.Errorf("readText returned PageResult %+v alongside its error, want the zero value", res)
	}
}

// --- EXTR-15-02: the lifted render gate ---------------------------------------------------

// TestExtractWorker_PagesNotRenderedGateIsScopedToRenderableFormats is EXTR-18-04's ratchet,
// converted by its named owner. That ratchet froze the pages_not_rendered gate as "EXTR-15's to
// lift"; EXTR-15-02 lifts it, so the scan now requires the Ingest guard to sit INSIDE the
// RendersPageImages branch instead of forbidding the edit. The control needle and the count of
// one are kept: without them a moved or duplicated gate reads as a clean pass.
func TestExtractWorker_PagesNotRenderedGateIsScopedToRenderableFormats(t *testing.T) {
	raw, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatalf("read worker.go: %v", err)
	}
	src := string(raw)

	// Control needle: prove the scan reads the right file, or every absence below reads as a
	// clean pass over a moved or renamed Work method.
	if !strings.Contains(src, "func (w *ExtractWorker) Work(") {
		t.Fatalf("worker.go carries no Work method; this scan is reading the wrong file")
	}

	if n := strings.Count(src, "FailurePagesNotRendered"); n != 1 {
		t.Fatalf("worker.go names FailurePagesNotRendered %d time(s), want exactly 1; the gate has moved or been duplicated", n)
	}

	guard := regexp.MustCompile(
		`if images, tokenPages, _, err = w\.Pages\.Ingest\(ctx, args\.TenantID, doc\); err != nil \{\s*kind = FailurePagesNotRendered\s*\}`)
	loc := guard.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("worker.go no longer guards w.Pages.Ingest's error branch with `kind = FailurePagesNotRendered`; the gate keeps its shape, only its enclosing branch changes")
	}

	// Parsed rather than matched by regex: the predicate may sit in the enclosing if's
	// condition or its init, and a scan that pinned one spelling would refuse the other.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "worker.go", raw, 0)
	if err != nil {
		t.Fatalf("parse worker.go: %v", err)
	}

	var gates int
	nested := false
	ast.Inspect(f, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		head := src[fset.Position(ifs.Pos()).Offset:fset.Position(ifs.Body.Pos()).Offset]
		if !strings.Contains(head, "RendersPageImages") {
			return true
		}
		gates++
		if fset.Position(ifs.Body.Pos()).Offset <= loc[0] && loc[1] <= fset.Position(ifs.Body.End()).Offset {
			nested = true
		}
		return true
	})

	if gates == 0 {
		t.Fatalf("worker.go never consults RendersPageImages; a format with no page images still reaches w.Pages.Ingest and still dead-letters at pages_not_rendered")
	}
	if !nested {
		t.Errorf("worker.go calls RendersPageImages in %d branch(es), none of which encloses the w.Pages.Ingest guard; the render, the page rows and the layout must all sit inside that branch", gates)
	}
}

// --- EXTR-19-04: where the boxless identity is computed, and what gates the rule lookup -----

// wgWorkerSource parses worker.go once and returns its bytes alongside the parse. Every scan
// below reads both: an offset comes from the source text, the branch it sits in from the AST.
func wgWorkerSource(t *testing.T) (string, *ast.File, *token.FileSet) {
	t.Helper()
	raw, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatalf("read worker.go: %v", err)
	}
	// Control needle: prove the scan reads the right file, or every absence below reads as a
	// clean pass over a moved or renamed Work method.
	if !strings.Contains(string(raw), "func (w *ExtractWorker) Work(") {
		t.Fatalf("worker.go carries no Work method; this scan is reading the wrong file")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "worker.go", raw, 0)
	if err != nil {
		t.Fatalf("parse worker.go: %v", err)
	}
	return string(raw), f, fset
}

// wgIfHead is one enclosing if statement and the text of its head.
type wgIfHead struct {
	stmt *ast.IfStmt
	head string
}

// wgEnclosingIfs is every if statement whose BODY spans off, innermost first. Ordered, not
// first-match: the rule lookup sits inside `if err == nil` inside a switch case, and the
// layout-write guard sits inside the boxless branch, so the two scans want different ancestors.
func wgEnclosingIfs(src string, f *ast.File, fset *token.FileSet, off int) []wgIfHead {
	var out []wgIfHead
	ast.Inspect(f, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		lo := fset.Position(ifs.Body.Pos()).Offset
		hi := fset.Position(ifs.Body.End()).Offset
		if off < lo || off >= hi {
			return true
		}
		out = append(out, wgIfHead{stmt: ifs, head: src[fset.Position(ifs.Pos()).Offset:lo]})
		return true
	})
	slices.SortFunc(out, func(a, b wgIfHead) int {
		return fset.Position(b.stmt.Body.Pos()).Offset - fset.Position(a.stmt.Body.Pos()).Offset
	})
	return out
}

// wgSoleOffset is the offset of needle, which must occur exactly once. The count is the guard: a
// second occurrence -- including one in a comment -- makes "the call sits in this branch"
// ambiguous, and a scan taking the first hit would report whichever came earlier in the file.
func wgSoleOffset(t *testing.T, src, needle string) int {
	t.Helper()
	if n := strings.Count(src, needle); n != 1 {
		t.Fatalf("worker.go names %q %d time(s), want exactly 1 (comments count -- do not repeat it in one)", needle, n)
	}
	return strings.Index(src, needle)
}

// wgBoxlessBranch is the one enclosing branch whose head negates RendersPageImages.
func wgBoxlessBranch(t *testing.T, src string, f *ast.File, fset *token.FileSet, off int, what string) wgIfHead {
	t.Helper()
	var found []wgIfHead
	for _, e := range wgEnclosingIfs(src, f, fset, off) {
		if strings.Contains(e.head, "!RendersPageImages") {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		var heads []string
		for _, e := range wgEnclosingIfs(src, f, fset, off) {
			heads = append(heads, strings.TrimSpace(e.head))
		}
		t.Fatalf("%s sits in %d branch(es) negating RendersPageImages, want exactly 1; its enclosing heads are %q", what, len(found), heads)
	}
	return found[0]
}

// EXTR-19-04 AC-7. The identity is computed once, on the boxless branch. A BoxlessFingerprint
// reachable by a renderable format would overwrite the v1 layout a PDF's learned rules key on.
func TestExtractWorker_TheBoxlessIdentityIsComputedOnlyOffTheBoxlessBranch(t *testing.T) {
	src, f, fset := wgWorkerSource(t)

	off := wgSoleOffset(t, src, "BoxlessFingerprint(")
	wgBoxlessBranch(t, src, f, fset, off, "BoxlessFingerprint")

	// Three sites: the Pages.Ingest gate, the page-row/v1-layout gate, and the boxless branch.
	// The predicate has to be spelled at each rather than hidden behind a helper, or a
	// collapsed gate is invisible to this scan.
	if n := strings.Count(src, "RendersPageImages"); n < 3 {
		t.Errorf("worker.go names RendersPageImages %d time(s), want at least 3 -- the render gate, the page-row gate and the boxless branch", n)
	}
}

// EXTR-19-04 AC-7. The lookup is gated on the identity, not the format. Gated on the format, a
// boxless document keeps no learned rules however good its identity is.
func TestExtractWorker_TheRuleLookupIsGatedOnTheIdentityNotTheFormat(t *testing.T) {
	src, f, fset := wgWorkerSource(t)

	off := wgSoleOffset(t, src, "w.Rules(ctx, args.TenantID, fingerprint)")
	enclosing := wgEnclosingIfs(src, f, fset, off)
	if len(enclosing) == 0 {
		t.Fatalf("the rule lookup sits in no if branch at all; it must run only for a job that has an identity")
	}
	head := strings.TrimSpace(enclosing[0].head)
	if !strings.Contains(head, `fingerprint != ""`) {
		t.Errorf("the rule lookup is gated by `%s`, want a head testing `fingerprint != \"\"`", head)
	}
	if strings.Contains(head, "RendersPageImages") {
		t.Errorf("the rule lookup is still gated by `%s`; a boxless document with an identity of its own must reach it", head)
	}
}

// EXTR-19-04. The two spellings the layout write depends on that no behavioural spec names on
// its own. The `err :=` half is ALSO covered behaviourally -- a shadowed error means the switch
// runs and the job SUCCEEDS with no layout, which TestExtractWorker_FailureKindPerStage's
// boxless arm reds on -- so this is the cheaper second oracle, not the only one.
func TestExtractWorker_TheBoxlessLayoutWriteReportsItsFailureToTheClassifier(t *testing.T) {
	src, f, fset := wgWorkerSource(t)

	off := wgSoleOffset(t, src, "FailureLayoutNotWritten")
	branch := wgBoxlessBranch(t, src, f, fset, off, "kind = FailureLayoutNotWritten")

	// Every other stage guard in Work leads with err == nil, so an upstream failure skips this
	// one instead of running over a zero Document.
	if !strings.Contains(branch.head, "err == nil") {
		t.Errorf("the boxless branch is guarded by `%s`, want a head leading with `err == nil` as the render, page-row and readText guards do", strings.TrimSpace(branch.head))
	}

	// A `:=` on the transaction call declares a NEW err, the classification block never sees the
	// failure, and the job succeeds with no layout. FuncLit bodies are skipped: inside the
	// transaction closure `err :=` is correct and necessary.
	var shadows []string
	scan := func(n ast.Node) {
		ast.Inspect(n, func(m ast.Node) bool {
			if m == nil {
				return false
			}
			if _, isLit := m.(*ast.FuncLit); isLit {
				return false
			}
			as, ok := m.(*ast.AssignStmt)
			if !ok || as.Tok != token.DEFINE {
				return true
			}
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == "err" {
					shadows = append(shadows, src[fset.Position(as.Pos()).Offset:fset.Position(as.End()).Offset])
				}
			}
			return true
		})
	}
	for _, stmt := range branch.stmt.Body.List {
		scan(stmt)
	}
	if len(shadows) != 0 {
		t.Errorf("the boxless branch declares a new err with `:=` (%q); use `=` so the classification block sees the failure and the job does not succeed with no layout", shadows)
	}
}
