// export_test.go: the DB-backed worker suite lives in package extraction_test, which cannot
// name the unexported args type. These constructors hand it a value of that type instead.
// Compiled only under go test, so the production surface is unchanged.
package extraction

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// NewExtractArgsForTest builds the args EnqueueTx takes. The return type is river.JobArgs, so
// the caller never writes the concrete name.
func NewExtractArgsForTest(tenantID, documentID, key string) river.JobArgs {
	return extractArgs{TenantID: tenantID, DocumentID: documentID, IdempotencyKey: key}
}

// NewExtractJobForTest builds the job Work takes, for the specs that call Work directly
// rather than through a River client.
func NewExtractJobForTest(riverJobID int64, attempt, maxAttempts int, tenantID, documentID, key string) *river.Job[extractArgs] {
	return &river.Job[extractArgs]{
		JobRow: &rivertype.JobRow{ID: riverJobID, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   extractArgs{TenantID: tenantID, DocumentID: documentID, IdempotencyKey: key},
	}
}

// NewPDFiumReaderAtDPIForTest builds a reader that renders at a non-default DPI. AC-4's
// alignment sweep runs 100/150/200 to prove the box space and the pixel space are one space at
// any resolution.
func NewPDFiumReaderAtDPIForTest(dpi int) *PDFiumReader { return &PDFiumReader{dpi: dpi} }

// PDFiumCleanupsForTest reads the render-bitmap release counter.
func PDFiumCleanupsForTest() int64 { return pdfiumCleanups.Load() }

// NewPDFiumExtractorWithReaderForTest builds an extractor over a substitute PageReader.
// TestPDFiumExtractor_ChecksCancellationBeforeTheWasmPool counts the calls that reach it.
func NewPDFiumExtractorWithReaderForTest(r PageReader) *PDFiumExtractor {
	return &PDFiumExtractor{reader: r}
}

// NewDoclingExtractorWithReaderForTest builds an extractor over a substitute PageReader,
// bypassing NewDoclingExtractor's baseURL requirement.
// TestDoclingExtractor_ChecksCancellationBeforeTheReader counts the calls that reach it.
func NewDoclingExtractorWithReaderForTest(r PageReader) *DoclingExtractor {
	return &DoclingExtractor{reader: r}
}

// ColumnBandForTest exposes the band Fingerprint sorts and hashes by, so the corpus specs
// assert the production thirds rather than a reimplementation of them.
func ColumnBandForTest(r Region) int { return columnBand(r) }

// AnchorLabelIDsForTest returns every anchorLexicon ID whose pattern matches text.
func AnchorLabelIDsForTest(text string) []string {
	var ids []string
	for _, m := range anchorLabelMatchers {
		if m.RE.MatchString(text) {
			ids = append(ids, m.ID)
		}
	}
	return ids
}
