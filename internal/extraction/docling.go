// docling.go: DoclingReader, the PageReader over the Docling sidecar's POST /v1/read. Read is
// a stub pending EXTR-03-05's real implementation; NewDoclingReader's URL validation is real
// since EXTR-03-07 (T-07-5) needs a malformed DOCLING_URL to fail at boot.
package extraction

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// Read is a stub: EXTR-03-05's real implementation lands separately. Every T-05-* spec in
// docling_test.go is red against this until then.
func (r *DoclingReader) Read(ctx context.Context, doc Document, onPage func(Page) error) (PageResult, error) {
	return PageResult{}, errors.New("docling: Read not implemented")
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
