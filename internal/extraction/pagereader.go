package extraction

import (
	"context"
)

// PageReader turns one document's bytes into positioned text and page images. EXTR-03's
// Docling reader implements it too, so everything downstream is identical either way.
//
// A second port, not a wider Extractor: a Field is one named invoice field with a unique name
// (law E07), while tokens are thousands of unnamed text runs.
//
// Read is single-shot and I/O-free, the two laws Extractor carries ([extractors-are-single-shot],
// [extractors-are-io-free]): bytes arrive already opened, and the caller writes every result.
type PageReader interface {
	// Name is the stable reader key, persisted as extraction_jobs.extractor.
	Name() string

	// Version is the reader's contract version, persisted as extraction_jobs.extractor_version.
	Version() string

	// Read calls onPage once per page, in ascending page order, and returns the document's
	// totals. A non-nil error from onPage aborts the read. onPage is a callback rather than a
	// []Page return so no shape permits holding every page image in memory at once.
	Read(ctx context.Context, doc Document, onPage func(Page) error) (PageResult, error)
}

// Token is one run of text with a box. Region is EXTR-01's, reused: a token becomes a Field
// with no conversion and inherits extraction_field_results_bbox_normalised's guarantees.
type Token struct {
	Text   string
	Region Region
}

// Page is one page: its geometry, its render and its text.
type Page struct {
	Number      int     // 1-based, matches Region.Page
	WidthPt     float64 // page box in PDF points
	HeightPt    float64
	ImageWidth  int // rendered pixels; the number a canvas must scale a Region by
	ImageHeight int

	// ImagePNG is borrowed, not owned: valid only for the duration of the onPage call that
	// carried it. Retaining it defeats the one-page-at-a-time memory posture.
	ImagePNG []byte

	Tokens []Token
}

// PageResult is what a whole read totals up.
type PageResult struct {
	Pages     int
	TextChars int // non-whitespace characters, whole document

	// PagesWithText is the per-page signal for hybrid documents (D-9). Extract passes a no-op
	// onPage, so this is the only route by which it sees per-page structure. Carried, not
	// acted on: a blank verso in a native PDF has no text either.
	PagesWithText int
}
