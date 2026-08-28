// pdfium.go: PDFiumReader, the native-PDF PageReader.
package extraction

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"sync/atomic"
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

// pdfiumRenderDPI puts US-Letter on a 1275x1651 grid: legible at full review width, and about
// 113 KiB of grayscale PNG per invoice page. RGBA is 10x that and JPEG q85 8x, on line art.
const pdfiumRenderDPI = 150

// maxPages refuses a document rather than letting River dead-letter it as a timeout: 800 x the
// 300 ms per-page cost is 240 s, half the 480 s of render budget inside worker.go's 600 s.
const maxPages = 800

// Render-bitmap releases, read by TestPDFiumReader_HoldsOnePageAtATime.
var pdfiumCleanups atomic.Int64

// PDFiumReader holds no mutable state: a read borrows a pool instance for its own duration and
// returns it, so two reads share nothing.
type PDFiumReader struct {
	// dpi overrides pdfiumRenderDPI. Only AC-4's alignment sweep sets it, through
	// NewPDFiumReaderAtDPIForTest.
	dpi int
}

var _ PageReader = (*PDFiumReader)(nil)

func NewPDFiumReader() *PDFiumReader { return &PDFiumReader{} }

func (r *PDFiumReader) Name() string { return pdfiumReaderName }

func (r *PDFiumReader) Version() string { return pdfiumReaderVersion }

func (r *PDFiumReader) renderDPI() int {
	if r.dpi == 0 {
		return pdfiumRenderDPI
	}
	return r.dpi
}

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
		if count.PageCount > maxPages {
			return fmt.Errorf("pdfium: document has %d pages, over the %d page limit", count.PageCount, maxPages)
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

			ref := requests.Page{ByIndex: &requests.PageByIndex{Document: opened.Document, Index: i}}
			text, err := inst.GetPageTextStructured(&requests.GetPageTextStructured{
				Page: ref,
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

			if err := r.renderPage(inst, ref, Page{
				Number:   i + 1,
				WidthPt:  size.Width,
				HeightPt: size.Height,
				Tokens:   tokens,
			}, onPage); err != nil {
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

// renderPage renders one page, hands it to onPage, then releases the render bitmap. Cleanup is
// mandatory under the wasm backend and runs on every path, including onPage's error.
func (r *PDFiumReader) renderPage(inst pdfium.Pdfium, ref requests.Page, page Page, onPage func(Page) error) error {
	rendered, err := inst.RenderPageInDPI(&requests.RenderPageInDPI{
		Page:        ref,
		DPI:         r.renderDPI(),
		ImageFormat: requests.RenderImageFormatGrayscale,
	})
	if err != nil {
		return fmt.Errorf("pdfium: page %d render: %w", page.Number, err)
	}
	defer func() {
		rendered.Cleanup()
		pdfiumCleanups.Add(1)
	}()

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, rendered.Result.RenderedImage); err != nil {
		return fmt.Errorf("pdfium: page %d encode: %w", page.Number, err)
	}

	// The render's own grid, not pageWidthPt * dpi / 72: go-pdfium ceils that product, so
	// US-Letter at DPI 150 is 1651 rows and not 1650.
	grid := rendered.Result.RenderedImage.Bounds()
	page.ImagePNG = encoded.Bytes()
	page.ImageWidth = grid.Dx()
	page.ImageHeight = grid.Dy()

	return onPage(page)
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
