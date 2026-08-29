// docling.go: DoclingReader, the PageReader over the Docling sidecar's POST /v1/read.
package extraction

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
)

const (
	doclingReaderName    = "docling"
	doclingReaderVersion = "v1"
	doclingReadPath      = "/v1/read"

	// Cap on the body read back from a non-2xx response, for the error message only.
	doclingErrBodyMax = 4 << 10
)

// DoclingReader holds no per-read state: a read builds its own request and decodes its own
// response, so two reads share nothing but the HTTP client.
type DoclingReader struct {
	baseURL string
	client  *http.Client
}

var _ PageReader = (*DoclingReader)(nil)

// NewDoclingReader validates the base URL at construction so EXTR-03-07 can make a malformed
// DOCLING_URL fatal at boot (T-07-5) instead of shipping a client pointed at nothing.
func NewDoclingReader(baseURL string) (*DoclingReader, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("docling: parse base URL %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("docling: base URL %q has scheme %q, want http or https", baseURL, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("docling: base URL %q has no host", baseURL)
	}

	return &DoclingReader{
		baseURL: baseURL,
		// No Client.Timeout. A cold start blocks on the converter lock rather than returning
		// 503, so a first request can take minutes; ctx is the only clock.
		client: &http.Client{},
	}, nil
}

func (r *DoclingReader) Name() string { return doclingReaderName }

func (r *DoclingReader) Version() string { return doclingReaderVersion }

// Read posts doc.Bytes to the sidecar and calls onPage once per page in ascending order.
//
// ctx.Err() is tested before the request is built, so a cancelled call never dispatches (law
// E12). The totals are assigned only once the whole document is through, so any failure
// returns a zero PageResult.
func (r *DoclingReader) Read(ctx context.Context, doc Document, onPage func(Page) error) (PageResult, error) {
	if err := ctx.Err(); err != nil {
		return PageResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+doclingReadPath, bytes.NewReader(doc.Bytes))
	if err != nil {
		return PageResult{}, fmt.Errorf("docling: build %s request: %w", doclingReadPath, err)
	}
	// Empty when unknown; the sidecar already falls back to .pdf, so no header beats a blank one.
	if doc.ContentType != "" {
		req.Header.Set("Content-Type", doc.ContentType)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return PageResult{}, fmt.Errorf("docling: post %s: %w", doclingReadPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PageResult{}, doclingStatusError(resp)
	}

	var wire doclingResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return PageResult{}, fmt.Errorf("docling: decode %s response: %w", doclingReadPath, err)
	}

	// The service sorts already; this is the defence, not the mechanism.
	slices.SortStableFunc(wire.Pages, func(a, b doclingPage) int { return cmp.Compare(a.Number, b.Number) })

	totals := PageResult{Pages: len(wire.Pages)}
	for _, wp := range wire.Pages {
		tokens, chars := doclingTokens(wp.Tokens, wp.Number)
		totals.TextChars += chars
		if chars > 0 {
			totals.PagesWithText++
		}

		if err := onPage(Page{
			Number:   wp.Number,
			WidthPt:  wp.WidthPt,
			HeightPt: wp.HeightPt,
			Tokens:   tokens,
			Tables:   doclingTables(wp.Tables, wp.Number),
		}); err != nil {
			return PageResult{}, err
		}
	}
	return totals, nil
}

// doclingStatusError names the status and the service's own reason, so a 422 (unreadable
// document) is distinguishable from a 500 without a sentinel error type.
func doclingStatusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, doclingErrBodyMax))

	reason := strings.TrimSpace(string(body))
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error != "" {
		reason = payload.Error
	}
	if reason == "" {
		reason = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("docling: %s returned %d: %s", doclingReadPath, resp.StatusCode, reason)
}

// doclingTokens converts one page's wire tokens and counts the non-whitespace runes they carry,
// the same predicate pdfiumTokens uses.
//
// A boxless token is kept with a zero box on the right page: every DOCX token arrives with no
// box at all, and dropping them would report every DOCX as unreadable.
func doclingTokens(wire []doclingToken, page int) ([]Token, int) {
	tokens := make([]Token, 0, len(wire))
	chars := 0

	for _, wt := range wire {
		for _, r := range wt.Text {
			if !unicode.IsSpace(r) {
				chars++
			}
		}

		region := Region{Page: page}
		if boxed := wt.region(page); boxed != nil {
			region = *boxed
		}
		tokens = append(tokens, Token{Text: wt.Text, Region: region})
	}
	return tokens, chars
}

// doclingTables collapses a wire "tables": [] to a nil slice, so Tables == nil means "no tables"
// for this reader exactly as it does for PDFiumReader.
func doclingTables(wire []doclingTable, page int) []Table {
	var out []Table
	for _, wt := range wire {
		cells := make([]TableCell, 0, len(wt.Cells))
		for _, wc := range wt.Cells {
			cells = append(cells, TableCell{
				Row:     wc.Row,
				Col:     wc.Col,
				RowSpan: wc.RowSpan,
				ColSpan: wc.ColSpan,
				Text:    wc.Text,
				Region:  wc.region(page),
			})
		}
		out = append(out, Table{Rows: wt.Rows, Cols: wt.Cols, Cells: cells})
	}
	return out
}

// doclingResponse is the wire body of POST /v1/read. "reader", "version" and the live
// service's "docling_version" are ignored -- encoding/json drops unknown fields by default.
type doclingResponse struct {
	Pages []doclingPage `json:"pages"`
}

type doclingPage struct {
	Number   int            `json:"number"`
	WidthPt  float64        `json:"width_pt"`
	HeightPt float64        `json:"height_pt"`
	Tokens   []doclingToken `json:"tokens"`
	Tables   []doclingTable `json:"tables"`
}

// doclingBox is embedded, not repeated. All four are *float64: the service omits the keys
// when there is no box rather than sending zeros, and a zero box is a legal box.
type doclingBox struct {
	X0 *float64 `json:"x0"`
	Y0 *float64 `json:"y0"`
	X1 *float64 `json:"x1"`
	Y1 *float64 `json:"y1"`
}

// region is nil unless all four fields are present; a partial box is no box.
func (b doclingBox) region(page int) *Region {
	if b.X0 == nil || b.Y0 == nil || b.X1 == nil || b.Y1 == nil {
		return nil
	}
	return &Region{Page: page, X0: *b.X0, Y0: *b.Y0, X1: *b.X1, Y1: *b.Y1}
}

type doclingToken struct {
	Text string `json:"text"`
	doclingBox
}

type doclingTable struct {
	Rows  int           `json:"rows"`
	Cols  int           `json:"cols"`
	Cells []doclingCell `json:"cells"`
}

type doclingCell struct {
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	RowSpan int    `json:"row_span"`
	ColSpan int    `json:"col_span"`
	Text    string `json:"text"`
	doclingBox
}
