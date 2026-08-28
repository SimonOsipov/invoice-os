// pdfium_extractor.go: PDFiumExtractor, the Extractor over PDFiumReader. This story's only
// verdict is whether a document carries a text layer at all; EXTR-04 fills the field vocabulary.
package extraction

import "context"

// pdfiumTextLayerField is the only field this extractor emits. Persisted as
// extraction_field_results.field_name (TestPDFiumExtractor_ReportsAScanAsUnreadable).
const pdfiumTextLayerField = "document_text_layer"

// PDFiumExtractor composes a PageReader rather than reading pages itself: a Field carries a
// name unique within its result (law E07) and a token does not, so one object cannot be both
// seams. Name and Version are the reader's own -- the verdict IS the read.
type PDFiumExtractor struct {
	// reader overrides the default PDFiumReader. Only
	// TestPDFiumExtractor_ChecksCancellationBeforeTheWasmPool sets it.
	reader PageReader
}

// Pointer only; TestPDFiumExtractor_OnlyThePointerSatisfiesExtractor rejects a value receiver.
var _ Extractor = (*PDFiumExtractor)(nil)

func NewPDFiumExtractor() *PDFiumExtractor { return &PDFiumExtractor{} }

func (e *PDFiumExtractor) Name() string { return pdfiumReaderName }

func (e *PDFiumExtractor) Version() string { return pdfiumReaderVersion }

func (e *PDFiumExtractor) pageReader() PageReader {
	if e.reader == nil {
		return &PDFiumReader{}
	}
	return e.reader
}

// Extract reports one document-level verdict: no text anywhere is unreadable, anything else is
// an empty non-nil slice for EXTR-04 to fill (law E04). ctx.Err() is the FIRST statement, so a
// cancelled call reaches no reader at all (law E12).
//
// PagesWithText is read in no branch: the verdict is document-level (D-9), and
// TestPDFiumExtractor_DoesNotFlagAHybridDocument fails the day that changes.
func (e *PDFiumExtractor) Extract(ctx context.Context, doc Document) ([]Field, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// The page images are dropped: only the text totals decide the verdict.
	res, err := e.pageReader().Read(ctx, doc, func(Page) error { return nil })
	if err != nil {
		return nil, err
	}

	if res.TextChars == 0 {
		return []Field{{Name: pdfiumTextLayerField, Reason: ReasonUnreadable}}, nil
	}
	return []Field{}, nil
}
