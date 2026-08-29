// docling_test.go: T-05-1..10, 15..17. DoclingReader's Read is a stub (docling.go) until
// EXTR-03-05 lands for real, so every spec here is red against it -- on the target assertion,
// not a compile error.
package extraction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- wire body builders -------------------------------------------------------

// dcBox mirrors the sidecar's box shape: a pointer per field, omitted (not zeroed) when the
// service has no box for a token or cell.
type dcBox struct {
	X0 *float64 `json:"x0,omitempty"`
	Y0 *float64 `json:"y0,omitempty"`
	X1 *float64 `json:"x1,omitempty"`
	Y1 *float64 `json:"y1,omitempty"`
}

type dcToken struct {
	Text string `json:"text"`
	dcBox
}

type dcCell struct {
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	RowSpan int    `json:"row_span"`
	ColSpan int    `json:"col_span"`
	Text    string `json:"text"`
	dcBox
}

type dcTable struct {
	Rows  int      `json:"rows"`
	Cols  int      `json:"cols"`
	Cells []dcCell `json:"cells"`
}

type dcPage struct {
	Number   int       `json:"number"`
	WidthPt  float64   `json:"width_pt"`
	HeightPt float64   `json:"height_pt"`
	Tokens   []dcToken `json:"tokens"`
	Tables   []dcTable `json:"tables"`
}

type dcBody struct {
	Pages []dcPage `json:"pages"`
}

func dcFloat(f float64) *float64 { return &f }

// dcSimplePage is one page carrying a single boxed token, text "page-N-token".
func dcSimplePage(n int) dcPage {
	return dcPage{
		Number:   n,
		WidthPt:  612,
		HeightPt: 792,
		Tokens: []dcToken{{
			Text:  fmt.Sprintf("page-%d-token", n),
			dcBox: dcBox{X0: dcFloat(0.1), Y0: dcFloat(0.1), X1: dcFloat(0.2), Y1: dcFloat(0.2)},
		}},
		Tables: []dcTable{},
	}
}

func dcMarshal(t *testing.T, b dcBody) []byte {
	t.Helper()
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal wire body: %v", err)
	}
	return out
}

// --- server / reader harness --------------------------------------------------

func dcServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func dcJSONHandler(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}
}

func dcNewReader(t *testing.T, baseURL string) *extraction.DoclingReader {
	t.Helper()
	r, err := extraction.NewDoclingReader(baseURL)
	if err != nil {
		t.Fatalf("NewDoclingReader(%q): %v", baseURL, err)
	}
	return r
}

func dcDoc(body string) extraction.Document {
	return extraction.Document{Bytes: []byte(body), ContentType: "application/pdf"}
}

// dcClonePage deep-copies a Page's slices so a later mutation elsewhere cannot be mistaken for
// one here.
func dcClonePage(p extraction.Page) extraction.Page {
	p.Tokens = append([]extraction.Token(nil), p.Tokens...)
	p.Tables = append([]extraction.Table(nil), p.Tables...)
	p.ImagePNG = bytes.Clone(p.ImagePNG)
	return p
}

// dcAssertPagesMatch checks got against the wire pages that produced it: ascending Number, the
// one token's text and box, and Tables collapsed to nil for a wire "tables": [].
func dcAssertPagesMatch(t *testing.T, got []extraction.Page, want []dcPage) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d page(s), want %d", len(got), len(want))
	}
	for i, p := range got {
		w := want[i]
		if p.Number != w.Number {
			t.Errorf("page %d: Number = %d, want %d", i, p.Number, w.Number)
		}
		if p.Tables != nil {
			t.Errorf("page %d: Tables = %d table(s), want nil for a wire \"tables\": []", i, len(p.Tables))
		}
		if len(p.Tokens) != len(w.Tokens) {
			t.Fatalf("page %d: %d token(s), want %d", i, len(p.Tokens), len(w.Tokens))
		}
		for j, tok := range p.Tokens {
			wt := w.Tokens[j]
			if tok.Text != wt.Text {
				t.Errorf("page %d token %d: Text = %q, want %q", i, j, tok.Text, wt.Text)
			}
			if wt.X0 != nil {
				if got, want := tok.Region, (extraction.Region{Page: w.Number, X0: *wt.X0, Y0: *wt.Y0, X1: *wt.X1, Y1: *wt.Y1}); got != want {
					t.Errorf("page %d token %d: Region = %+v, want %+v", i, j, got, want)
				}
			}
		}
	}
}

// --- T-05-1..3: ordering -------------------------------------------------------

func TestDoclingReader_CallsOnPageOncePerPageInAscendingOrder(t *testing.T) {
	pages := []dcPage{dcSimplePage(1), dcSimplePage(2), dcSimplePage(3)}
	srv := dcServer(t, dcJSONHandler(dcMarshal(t, dcBody{Pages: pages})))
	r := dcNewReader(t, srv.URL)

	var got []extraction.Page
	res, err := r.Read(t.Context(), dcDoc("x"), func(p extraction.Page) error {
		got = append(got, p)
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Pages != 3 {
		t.Errorf("PageResult.Pages = %d, want 3", res.Pages)
	}
	if len(got) != 3 {
		t.Fatalf("onPage was called %d time(s), want 3", len(got))
	}

	var nums []int
	for _, p := range got {
		nums = append(nums, p.Number)
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(nums, want) {
		t.Errorf("onPage saw pages %v, want %v in ascending order", nums, want)
	}
	dcAssertPagesMatch(t, got, pages)
}

// T-05-2: DoclingReader has no render to offer, unlike PDFiumReader.
func TestDoclingReader_NeverSetsImagePNG(t *testing.T) {
	pages := []dcPage{dcSimplePage(1), dcSimplePage(2), dcSimplePage(3)}
	srv := dcServer(t, dcJSONHandler(dcMarshal(t, dcBody{Pages: pages})))
	r := dcNewReader(t, srv.URL)

	var got []extraction.Page
	if _, err := r.Read(t.Context(), dcDoc("x"), func(p extraction.Page) error {
		got = append(got, p)
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("onPage was never called; the ImagePNG clause below would examine nothing")
	}
	for _, p := range got {
		if p.ImagePNG != nil {
			t.Errorf("page %d carries ImagePNG (%d byte(s)), want nil", p.Number, len(p.ImagePNG))
		}
	}
}

// T-05-3: convert.py already sorts server-side; this proves the Go sort is a real defence and
// not dead code.
func TestDoclingReader_SortsPagesRegardlessOfWireOrder(t *testing.T) {
	wire := []dcPage{dcSimplePage(3), dcSimplePage(1), dcSimplePage(2)}
	srv := dcServer(t, dcJSONHandler(dcMarshal(t, dcBody{Pages: wire})))
	r := dcNewReader(t, srv.URL)

	var got []extraction.Page
	if _, err := r.Read(t.Context(), dcDoc("x"), func(p extraction.Page) error {
		got = append(got, p)
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	var nums []int
	for _, p := range got {
		nums = append(nums, p.Number)
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(nums, want) {
		t.Fatalf("onPage saw pages %v in this order, want %v ascending regardless of wire order", nums, want)
	}
	dcAssertPagesMatch(t, got, []dcPage{dcSimplePage(1), dcSimplePage(2), dcSimplePage(3)})
}

// T-05-4: the onPage error is returned unwrapped (identity, mirroring
// TestPDFiumReader_CleansUpOnAnOnPageError), and the loop stops at the refusing page.
func TestDoclingReader_OnPageErrorAbortsAndZeroesResult(t *testing.T) {
	pages := []dcPage{dcSimplePage(1), dcSimplePage(2), dcSimplePage(3)}
	srv := dcServer(t, dcJSONHandler(dcMarshal(t, dcBody{Pages: pages})))
	r := dcNewReader(t, srv.URL)
	refused := errors.New("onPage refused page 2")

	var calls []int
	res, err := r.Read(t.Context(), dcDoc("x"), func(p extraction.Page) error {
		calls = append(calls, p.Number)
		if p.Number == 2 {
			return refused
		}
		return nil
	})

	if err != refused {
		t.Errorf("Read returned %v, want the onPage error itself", err)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(calls, want) {
		t.Errorf("onPage saw pages %v, want %v -- the read must stop at the refusing page", calls, want)
	}
	if res != (extraction.PageResult{}) {
		t.Errorf("Read failed but returned %+v, want a zero PageResult", res)
	}
}

// --- T-05-5/6: TextChars / PagesWithText ---------------------------------------

// T-05-5: two pages, one with text and one without.
func TestDoclingReader_TotalsAcrossHybridPages(t *testing.T) {
	body := dcMarshal(t, dcBody{Pages: []dcPage{
		{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []dcToken{{Text: "Hello World"}}, Tables: []dcTable{}},
		{Number: 2, WidthPt: 612, HeightPt: 792, Tokens: []dcToken{}, Tables: []dcTable{}},
	}})
	srv := dcServer(t, dcJSONHandler(body))
	r := dcNewReader(t, srv.URL)

	res, err := r.Read(t.Context(), dcDoc("x"), func(extraction.Page) error { return nil })
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Pages != 2 {
		t.Errorf("Pages = %d, want 2", res.Pages)
	}
	if res.PagesWithText != 1 {
		t.Errorf("PagesWithText = %d, want 1: page 2 carries no tokens", res.PagesWithText)
	}
	// "Hello World" hand-counted: H-e-l-l-o (5) + W-o-r-l-d (5), the space between excluded: 10.
	if res.TextChars != 10 {
		t.Errorf("TextChars = %d, want 10", res.TextChars)
	}
}

// T-05-6: " a b " is five runes; only 'a' and 'b' are non-whitespace.
func TestDoclingReader_TextCharsCountsNonWhitespaceRunesOnly(t *testing.T) {
	body := dcMarshal(t, dcBody{Pages: []dcPage{
		{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []dcToken{{Text: " a b "}}, Tables: []dcTable{}},
	}})
	srv := dcServer(t, dcJSONHandler(body))
	r := dcNewReader(t, srv.URL)

	res, err := r.Read(t.Context(), dcDoc("x"), func(extraction.Page) error { return nil })
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.TextChars != 2 {
		t.Errorf("TextChars = %d, want 2, not len(\" a b \")==5", res.TextChars)
	}
}

// --- T-05-7: ctx checked before dispatch ---------------------------------------

// T-05-7: "zero requests" alone does not prove the guard exists -- net/http itself never
// dials on a cancelled context. The *url.Error clause is the discriminating one (design §8).
func TestDoclingReader_ChecksCtxBeforeDispatching(t *testing.T) {
	var requests atomic.Int64
	srv := dcServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	r := dcNewReader(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Read(ctx, dcDoc("x"), func(extraction.Page) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Read on a cancelled context returned %v, want context.Canceled", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("the server recorded %d request(s), want 0", n)
	}

	var ue *url.Error
	if errors.As(err, &ue) {
		t.Errorf("Read built and dispatched an HTTP request before checking ctx.Err(): %v -- net/http "+
			"produces a zero-request *url.Error on its own, so the request counter alone does not prove "+
			"the guard exists", ue)
	}
}

// --- T-05-8/16: non-2xx error mapping ------------------------------------------

// T-05-8.
func TestDoclingReader_500NamesStatusAndReason(t *testing.T) {
	srv := dcServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	})
	r := dcNewReader(t, srv.URL)

	_, err := r.Read(t.Context(), dcDoc("x"), func(extraction.Page) error { return nil })
	if err == nil {
		t.Fatalf("Read returned no error for a 500 response")
	}
	for _, want := range []string{"500", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// T-05-16: the 422 case must be distinguishable from the 500 case by its own status and reason.
func TestDoclingReader_422NamesStatusAndReasonDistinctFrom500(t *testing.T) {
	srv := dcServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":"encrypted document"}`))
	})
	r := dcNewReader(t, srv.URL)

	_, err := r.Read(t.Context(), dcDoc("x"), func(extraction.Page) error { return nil })
	if err == nil {
		t.Fatalf("Read returned no error for a 422 response")
	}
	for _, want := range []string{"422", "encrypted document"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	for _, unwanted := range []string{"500", "boom"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("422 error %q unexpectedly mentions %q, the 500 case's text", err.Error(), unwanted)
		}
	}
}

// --- T-05-9: malformed JSON -----------------------------------------------------

// T-05-9: a decode error, never a panic.
func TestDoclingReader_MalformedJSONIsADecodeErrorNotAPanic(t *testing.T) {
	srv := dcServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"pages": [`)) // truncated, invalid JSON
	})
	r := dcNewReader(t, srv.URL)

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("Read panicked on malformed JSON instead of returning an error: %v", rec)
		}
	}()

	_, err := r.Read(t.Context(), dcDoc("x"), func(extraction.Page) error { return nil })
	if err == nil {
		t.Fatalf("Read returned no error for a malformed JSON body")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error %q does not mention decode; want the wrapped json.Decoder error", err.Error())
	}
}

// --- T-05-10: table dimensions and a merged span --------------------------------

// T-05-10: Rows/Cols come from the wire's own ints, not len(Cells) -- the cell list below is
// deliberately sparse. Spans are sent explicitly; there is no 0 -> 1 normalisation.
func TestDoclingReader_TableDimensionsAndMergedSpanSurvive(t *testing.T) {
	merged := dcCell{
		Row: 0, Col: 0, RowSpan: 1, ColSpan: 2, Text: "merged",
		dcBox: dcBox{X0: dcFloat(0.1), Y0: dcFloat(0.1), X1: dcFloat(0.3), Y1: dcFloat(0.15)},
	}
	page := dcPage{
		Number: 1, WidthPt: 612, HeightPt: 792,
		Tokens: []dcToken{{Text: "x", dcBox: dcBox{X0: dcFloat(0), Y0: dcFloat(0), X1: dcFloat(0.1), Y1: dcFloat(0.1)}}},
		Tables: []dcTable{{
			Rows: 3, Cols: 4,
			Cells: []dcCell{
				merged,
				{Row: 0, Col: 2, RowSpan: 1, ColSpan: 1, Text: "c"},
				{Row: 1, Col: 0, RowSpan: 1, ColSpan: 1, Text: "e"},
			},
		}},
	}
	srv := dcServer(t, dcJSONHandler(dcMarshal(t, dcBody{Pages: []dcPage{page}})))
	r := dcNewReader(t, srv.URL)

	var got extraction.Page
	if _, err := r.Read(t.Context(), dcDoc("x"), func(p extraction.Page) error {
		got = p
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(got.Tables) != 1 {
		t.Fatalf("Page.Tables has %d table(s), want 1", len(got.Tables))
	}
	tbl := got.Tables[0]
	if tbl.Rows != 3 || tbl.Cols != 4 {
		t.Errorf("Table = {Rows:%d Cols:%d}, want {Rows:3 Cols:4}", tbl.Rows, tbl.Cols)
	}

	var foundMerged bool
	for _, c := range tbl.Cells {
		if c.Row == 0 && c.Col == 0 {
			foundMerged = true
			if c.ColSpan != 2 {
				t.Errorf("the merged cell's ColSpan = %d, want 2", c.ColSpan)
			}
			if c.RowSpan != 1 {
				t.Errorf("the merged cell's RowSpan = %d, want 1", c.RowSpan)
			}
		}
	}
	if !foundMerged {
		t.Fatalf("no cell at (0,0) among %d cell(s); the merged-cell clause above examined nothing", len(tbl.Cells))
	}
}

// --- T-05-15: no fixed Client.Timeout -------------------------------------------

// T-05-15: a 20s server sleep under a 60s ctx deadline must succeed. Parallel per design §8's
// cost note.
func TestDoclingReader_WaitsOutASlowColdStart(t *testing.T) {
	t.Parallel()

	body := dcMarshal(t, dcBody{Pages: []dcPage{dcSimplePage(1)}})
	srv := dcServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})
	r := dcNewReader(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := r.Read(ctx, dcDoc("x"), func(extraction.Page) error { return nil })
	if err != nil {
		t.Fatalf("Read with a 60s deadline over a 20s-sleeping server: %v", err)
	}
	if res.Pages != 1 {
		t.Errorf("Read succeeded but reported %d page(s), want 1", res.Pages)
	}
}

// --- T-05-17: doc.Bytes is read fresh on every call ------------------------------

// T-05-17, strengthened per design §8: nothing DoclingReader emits aliases doc.Bytes, so the
// only retention it can have is caching the outgoing body. One Document, mutated between two
// Read calls; the server must see the mutation and the first read's copied pages must not.
func TestDoclingReader_RetainsNoReferenceToDocBytes(t *testing.T) {
	pages := []dcPage{dcSimplePage(1), dcSimplePage(2), dcSimplePage(3)}
	body := dcMarshal(t, dcBody{Pages: pages})

	var seen [][]byte
	srv := dcServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		seen = append(seen, bytes.Clone(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})
	r := dcNewReader(t, srv.URL)

	doc := extraction.Document{Bytes: []byte("body A"), ContentType: "application/pdf"}

	var firstPages []extraction.Page
	if _, err := r.Read(t.Context(), doc, func(p extraction.Page) error {
		firstPages = append(firstPages, dcClonePage(p))
		return nil
	}); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	dcAssertPagesMatch(t, firstPages, pages)

	copy(doc.Bytes, []byte("body B")) // same backing array Read was given, mutated in place

	if _, err := r.Read(t.Context(), doc, func(extraction.Page) error { return nil }); err != nil {
		t.Fatalf("second Read: %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("the server saw %d request(s), want 2", len(seen))
	}
	if got := string(seen[0]); got != "body A" {
		t.Errorf("the first request body was %q, want %q", got, "body A")
	}
	if got := string(seen[1]); got != "body B" {
		t.Errorf("the second request body was %q, want %q -- Read must send doc.Bytes as it is now, "+
			"not a copy taken on the first call", got, "body B")
	}

	// The first read's copied pages must not have changed just because a second Read ran.
	dcAssertPagesMatch(t, firstPages, pages)
}
