// pageone_test.go: the page-1 read over a counting fake reader and over the real PDFium pool.
package extraction_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// poReader emits the listed page numbers in order and wraps onPage's error, as a reader may.
type poReader struct {
	pages    []int
	err      error
	closeErr error // returned instead of onPage's error, as a reader whose cleanup fails would
	reads    int
	emitted  []int
	ctx      context.Context
}

func (r *poReader) Name() string    { return "po-fake" }
func (r *poReader) Version() string { return "v0" }

func (r *poReader) Read(ctx context.Context, _ extraction.Document, onPage func(extraction.Page) error) (extraction.PageResult, error) {
	r.reads++
	r.ctx = ctx
	if r.err != nil {
		return extraction.PageResult{}, r.err
	}
	for _, n := range r.pages {
		r.emitted = append(r.emitted, n)
		if err := onPage(extraction.Page{Number: n, Tokens: []extraction.Token{{Text: fmt.Sprintf("page %d", n)}}}); err != nil {
			if r.closeErr != nil {
				return extraction.PageResult{}, r.closeErr
			}
			return extraction.PageResult{}, fmt.Errorf("po-fake: page %d: %w", n, err)
		}
	}
	return extraction.PageResult{Pages: len(r.pages)}, nil
}

// poOpen serves body for any document id.
func poOpen(body []byte) extraction.OpenDocument {
	return func(context.Context, string) (extraction.Document, error) {
		return extraction.Document{Bytes: body, ContentType: "application/pdf"}, nil
	}
}

type poCallerKey struct{}

func TestPageOneReader_ReturnsTheTokensTheLayoutReaderRecorded(t *testing.T) {
	want := rvCorpusPages(t, fxLearnedTypedTotal)[0]
	if want.Number != 1 || len(want.Tokens) == 0 {
		t.Fatalf("%s page 1 reads as Number %d with %d token(s); the comparison below would be vacuous", fxLearnedTypedTotal, want.Number, len(want.Tokens))
	}
	body := fxRead(t, fxLearnedTypedTotal)

	var gotID string
	var gotCaller any
	open := func(ctx context.Context, id string) (extraction.Document, error) {
		gotID, gotCaller = id, ctx.Value(poCallerKey{})
		return extraction.Document{Bytes: body, ContentType: "application/pdf"}, nil
	}
	ctx := context.WithValue(t.Context(), poCallerKey{}, "operator")

	page, ok, err := extraction.PageOneReader(open, extraction.NewPDFiumReader())(ctx, "doc-1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want page 1 and no error", ok, err)
	}
	// The opener scopes its row lookup and its document.read row by the identity on ctx.
	if gotID != "doc-1" || gotCaller != "operator" {
		t.Errorf("open saw id %q and caller %v, want %q and the caller's context", gotID, gotCaller, "doc-1")
	}
	if !reflect.DeepEqual(page, want) {
		t.Errorf("kept page %d, %vx%v pt, %d token(s); want page %d, %vx%v pt, %d token(s) as the reader recorded them",
			page.Number, page.WidthPt, page.HeightPt, len(page.Tokens), want.Number, want.WidthPt, want.HeightPt, len(want.Tokens))
	}
}

func TestPageOneReader_StopsAfterPageOne(t *testing.T) {
	fake := &poReader{pages: []int{1, 2, 3}}
	page, ok, err := extraction.PageOneReader(poOpen(nil), fake)(t.Context(), "doc-1")
	if err != nil || !ok {
		t.Fatalf("fake: ok=%v err=%v, want page 1 and no error", ok, err)
	}
	if fake.reads != 1 || !slices.Equal(fake.emitted, []int{1}) {
		t.Errorf("fake: %d read(s) emitted pages %v, want 1 read emitting [1] alone", fake.reads, fake.emitted)
	}
	if page.Number != 1 || len(page.Tokens) != 1 || page.Tokens[0].Text != "page 1" {
		t.Errorf("fake: kept %+v, want page 1 and its one token", page)
	}

	pages := rvCorpusPages(t, fxNative3)
	if len(pages) != 3 {
		t.Fatalf("%s reads as %d page(s), want 3; nothing below could stop early", fxNative3, len(pages))
	}
	page, ok, err = extraction.PageOneReader(poOpen(fxRead(t, fxNative3)), extraction.NewPDFiumReader())(t.Context(), "doc-3")
	if err != nil || !ok {
		t.Fatalf("pdfium: ok=%v err=%v, want page 1 and no error", ok, err)
	}
	if !reflect.DeepEqual(page, pages[0]) {
		t.Errorf("pdfium: kept page %d with %d token(s), want page 1 with %d", page.Number, len(page.Tokens), len(pages[0].Tokens))
	}
}

func TestPageOneReader_PassesTheOpenErrorThrough(t *testing.T) {
	openErr := errors.New("object store down")
	open := func(context.Context, string) (extraction.Document, error) {
		return extraction.Document{}, fmt.Errorf("open doc-1: %w", openErr)
	}
	fake := &poReader{pages: []int{1}}

	page, ok, err := extraction.PageOneReader(open, fake)(t.Context(), "doc-1")
	if !errors.Is(err, openErr) {
		t.Errorf("err = %v, want the open error", err)
	}
	if ok || !reflect.DeepEqual(page, extraction.TokenPage{}) {
		t.Errorf("ok=%v page=%+v, want no page", ok, page)
	}
	if fake.reads != 0 {
		t.Errorf("the reader ran %d time(s) after a failed open, want 0", fake.reads)
	}
}

func TestPageOneReader_PassesAReadErrorThrough(t *testing.T) {
	readErr := errors.New("pdfium: page count: broken xref")
	fake := &poReader{err: readErr}
	page, ok, err := extraction.PageOneReader(poOpen(nil), fake)(t.Context(), "doc-1")
	if !errors.Is(err, readErr) || ok || !reflect.DeepEqual(page, extraction.TokenPage{}) {
		t.Errorf("fake: ok=%v err=%v page=%+v, want the read error and no page", ok, err, page)
	}

	// Bytes that are not a PDF fail inside the reader: an error, never ok=false.
	_, ok, err = extraction.PageOneReader(poOpen(fxRead(t, dxFixture)), extraction.NewPDFiumReader())(t.Context(), "doc-docx")
	if err == nil || ok {
		t.Errorf("pdfium over %s: ok=%v err=%v, want an error", dxFixture, ok, err)
	}
}

func TestPageOneReader_AReadErrorAfterPageOneKeepsNoPage(t *testing.T) {
	closeErr := errors.New("pdfium: close document: wasm trap")
	fake := &poReader{pages: []int{1, 2}, closeErr: closeErr}
	page, ok, err := extraction.PageOneReader(poOpen(nil), fake)(t.Context(), "doc-1")
	if !slices.Equal(fake.emitted, []int{1}) {
		t.Fatalf("the reader emitted %v, want [1]; page 1 never reached onPage and nothing below is tested", fake.emitted)
	}
	// Any read error learns nothing, even after onPage saw page 1.
	if !errors.Is(err, closeErr) || ok || !reflect.DeepEqual(page, extraction.TokenPage{}) {
		t.Errorf("ok=%v err=%v page=%+v, want the reader's error and no page", ok, err, page)
	}
}

func TestPageOneReader_HandsTheCallersContextToOpenAndTheReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var openCtx context.Context
	open := func(c context.Context, _ string) (extraction.Document, error) {
		openCtx = c
		return extraction.Document{}, nil
	}
	fake := &poReader{pages: []int{1}}

	if _, ok, err := extraction.PageOneReader(open, fake)(ctx, "doc-1"); err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want page 1 and no error", ok, err)
	}
	// Identity, not a value probe: context.WithoutCancel keeps the operator and drops the route's deadline.
	if openCtx != ctx {
		t.Errorf("open got %T, want the caller's context unchanged", openCtx)
	}
	if fake.ctx != ctx {
		t.Errorf("the reader got %T, want the caller's context unchanged", fake.ctx)
	}
}

func TestPageOneReader_ADocumentWithNoPageOneIsNotAnError(t *testing.T) {
	empty := &poReader{}
	page, ok, err := extraction.PageOneReader(poOpen(nil), empty)(t.Context(), "doc-1")
	if err != nil || ok || !reflect.DeepEqual(page, extraction.TokenPage{}) {
		t.Errorf("zero pages: ok=%v err=%v page=%+v, want no page and no error", ok, err, page)
	}
	if empty.reads != 1 {
		t.Errorf("zero pages: the reader ran %d time(s), want 1", empty.reads)
	}

	late := &poReader{pages: []int{2, 3}}
	page, ok, err = extraction.PageOneReader(poOpen(nil), late)(t.Context(), "doc-1")
	if err != nil || ok || !reflect.DeepEqual(page, extraction.TokenPage{}) {
		t.Errorf("pages [2 3]: ok=%v err=%v page=%+v, want no page and no error", ok, err, page)
	}
	if !slices.Equal(late.emitted, []int{2}) {
		t.Errorf("pages [2 3]: the reader emitted %v, want [2] alone", late.emitted)
	}
}

func TestPageOneReader_AnExhaustedPoolReturnsAtItsDeadline(t *testing.T) {
	body := fxRead(t, fxLearnedTypedTotal)

	// onPage runs while the instance is borrowed, so two blocked holders take both instances.
	held := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	var wg sync.WaitGroup
	// LIFO: release runs before the wait, so no goroutine outlives the test into the shared pool.
	t.Cleanup(wg.Wait)
	t.Cleanup(releaseAll)

	for range 2 {
		wg.Go(func() {
			_, err := extraction.NewPDFiumReader().Read(context.Background(), extraction.Document{Bytes: body}, func(extraction.Page) error {
				select {
				case held <- struct{}{}:
				default:
				}
				<-release
				return nil
			})
			if err != nil {
				t.Errorf("holder read: %v", err)
			}
		})
	}
	for range 2 {
		select {
		case <-held:
		case <-time.After(30 * time.Second):
			t.Fatal("a holder never reached onPage; the pool is not exhausted and the deadline below proves nothing")
		}
	}

	type result struct {
		ok      bool
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	read := extraction.PageOneReader(poOpen(body), extraction.NewPDFiumReader())
	wg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, ok, err := read(ctx, "doc-1")
		done <- result{ok: ok, err: err, elapsed: time.Since(start)}
	})

	var got result
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the page-1 read blocked past its deadline with both pool instances held")
	}
	if got.err == nil || got.ok {
		t.Fatalf("ok=%v err=%v, want an error and no page", got.ok, got.err)
	}
	t.Logf("exhausted pool: err=%q after %s", got.err, got.elapsed)
	// The pool reports expiry as its own timeout error, never context.DeadlineExceeded.
	if !strings.Contains(got.err.Error(), "get instance") {
		t.Errorf("err = %q, want the pool borrow's %q", got.err, "get instance")
	}
	if got.elapsed >= 2*time.Second {
		t.Errorf("the read returned after %s, want well under 2s for a 250ms deadline", got.elapsed)
	}

	releaseAll()
	wg.Wait()
	page, ok, err := read(t.Context(), "doc-1")
	if err != nil || !ok || len(page.Tokens) == 0 {
		t.Errorf("after release: ok=%v err=%v tokens=%d, want page 1 with tokens from the same reader", ok, err, len(page.Tokens))
	}
}
