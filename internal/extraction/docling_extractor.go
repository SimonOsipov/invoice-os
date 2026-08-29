// docling_extractor.go: DoclingExtractor, the Extractor over DoclingReader. Mirrors
// PDFiumExtractor's shape exactly -- same document-level document_text_layer verdict, EXTR-04
// fills the field vocabulary for both readers alike.
package extraction

import (
	"context"
	"errors"
)

// doclingTextLayerField is the only field this extractor emits, matching pdfiumTextLayerField:
// both extractors report the same verdict over different readers.
const doclingTextLayerField = "document_text_layer"

// DoclingExtractor composes a PageReader rather than reading pages itself, for the same reason
// PDFiumExtractor does (pdfium_extractor.go): a Field's Name is unique within its result (law
// E07), a token is not, and one object cannot be both seams.
type DoclingExtractor struct {
	// reader is the sidecar reader NewDoclingExtractor built, or a substitute only test code
	// sets via NewDoclingExtractorWithReaderForTest. Unlike PDFiumReader, DoclingReader has no
	// meaningful zero value -- it needs a baseURL -- so there is no nil-defaulting fallback
	// here the way PDFiumExtractor.pageReader() has one.
	reader PageReader
}

// Pointer only; TestDoclingExtractor_OnlyThePointerSatisfiesExtractor rejects a value receiver.
var _ Extractor = (*DoclingExtractor)(nil)

// NewDoclingExtractor validates and builds the sidecar reader eagerly, so a malformed baseURL
// fails at construction rather than on the first Extract call.
func NewDoclingExtractor(baseURL string) (*DoclingExtractor, error) {
	r, err := NewDoclingReader(baseURL)
	if err != nil {
		return nil, err
	}
	return &DoclingExtractor{reader: r}, nil
}

func (e *DoclingExtractor) Name() string { return doclingReaderName }

func (e *DoclingExtractor) Version() string { return doclingReaderVersion }

func (e *DoclingExtractor) pageReader() PageReader { return e.reader }

// Extract is a stub for EXTR-03-06's test-spec stage: it never reaches pageReader(), so the
// behaviour specs in docling_extractor_test.go fail on their own assertions rather than on a
// compile error. The real body lands with the accompanying feat commit.
func (e *DoclingExtractor) Extract(ctx context.Context, doc Document) ([]Field, error) {
	return nil, errors.New("docling: Extract not implemented")
}
