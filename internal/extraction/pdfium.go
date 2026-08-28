// pdfium.go: PDFiumReader, the native-PDF PageReader. Text only -- rendering is EXTR-02-06, so
// Page.ImagePNG and its dimensions stay zero here.
package extraction

import (
	"context"
	"fmt"
	"unicode"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

// Stamped into extraction_jobs.extractor / .extractor_version. Pinned by
// TestPDFiumReader_PinsNameAndVersion.
const (
	pdfiumReaderName    = "pdfium"
	pdfiumReaderVersion = "v1"
)

// PDFiumReader holds no state: a read borrows a pool instance for its own duration and returns
// it, so two reads share nothing.
type PDFiumReader struct{}

var _ PageReader = (*PDFiumReader)(nil)

func NewPDFiumReader() *PDFiumReader { return &PDFiumReader{} }

func (r *PDFiumReader) Name() string { return pdfiumReaderName }

func (r *PDFiumReader) Version() string { return pdfiumReaderVersion }

// Read hands doc.Bytes to pdfium by reference and calls onPage once per page in ascending
// order. onPage must not be nil.
//
// ctx.Err() is tested before the pool is touched, so a cancelled call never pays the wasm
// compile (law E12). The totals are assigned only once the whole document is through, so any
// failure returns a zero PageResult.
func (r *PDFiumReader) Read(ctx context.Context, doc Document, onPage func(Page) error) (PageResult, error) {
	if err := ctx.Err(); err != nil {
		return PageResult{}, err
	}

	var result PageResult
	err := withPDFiumInstance(ctx, func(inst pdfium.Pdfium) error {
		opened, err := inst.OpenDocument(&requests.OpenDocument{File: &doc.Bytes})
		if err != nil {
			return fmt.Errorf("pdfium: open document: %w", err)
		}
		defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: opened.Document})

		count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: opened.Document})
		if err != nil {
			return fmt.Errorf("pdfium: page count: %w", err)
		}

		totals := PageResult{Pages: count.PageCount}
		for i := range count.PageCount {
			if err := ctx.Err(); err != nil {
				return err
			}

			size, err := inst.FPDF_GetPageSizeByIndex(&requests.FPDF_GetPageSizeByIndex{
				Document: opened.Document,
				Index:    i,
			})
			if err != nil {
				return fmt.Errorf("pdfium: page %d size: %w", i+1, err)
			}

			text, err := inst.GetPageTextStructured(&requests.GetPageTextStructured{
				Page: requests.Page{ByIndex: &requests.PageByIndex{Document: opened.Document, Index: i}},
				Mode: requests.GetPageTextStructuredModeRects,
			})
			if err != nil {
				return fmt.Errorf("pdfium: page %d text: %w", i+1, err)
			}

			tokens, chars := pdfiumTokens(text.Rects, i+1, size.Width, size.Height)
			totals.TextChars += chars
			if chars > 0 {
				totals.PagesWithText++
			}

			if err := onPage(Page{
				Number:   i + 1,
				WidthPt:  size.Width,
				HeightPt: size.Height,
				Tokens:   tokens,
			}); err != nil {
				return err
			}
		}

		result = totals
		return nil
	})
	if err != nil {
		return PageResult{}, err
	}
	return result, nil
}

// pdfiumTokens converts one page's text rects into tokens and counts the non-whitespace
// characters they carry. An empty rect is dropped: law E08 and Field.Value both refuse an
// empty string, and a token converts to a Field with no conversion.
func pdfiumTokens(rects []*responses.GetPageTextStructuredRect, page int, widthPt, heightPt float64) ([]Token, int) {
	tokens := make([]Token, 0, len(rects))
	chars := 0

	for _, rect := range rects {
		if rect == nil || rect.Text == "" {
			continue
		}
		for _, r := range rect.Text {
			if !unicode.IsSpace(r) {
				chars++
			}
		}

		// pdfium measures from the page bottom and a Region from the top, hence heightPt - y.
		// PixelPosition is this same position scaled with no flip, so it cannot stand in.
		pos := rect.PointPosition
		tokens = append(tokens, Token{
			Text: rect.Text,
			Region: Region{
				Page: page,
				X0:   pos.Left / widthPt,
				Y0:   (heightPt - pos.Top) / heightPt,
				X1:   pos.Right / widthPt,
				Y1:   (heightPt - pos.Bottom) / heightPt,
			},
		})
	}
	return tokens, chars
}
