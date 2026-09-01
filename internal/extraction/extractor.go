package extraction

import (
	"context"
)

// OpenDocument returns one document's bytes -- this package's read seam onto stored content
// (UploadHandler's injected store is the write one). internal/extraction never imports
// internal/document (deps_test.go).
type OpenDocument func(ctx context.Context, documentID string) (Document, error)

// Extractor is the versioned seam between the extraction worker and any way of reading a
// document. Every Extract is single-shot: no internal retry or backoff
// ([extractors-are-single-shot]) — retry is the River job's business.
//
// An extractor touches no database, object store or network ([extractors-are-io-free]): bytes
// arrive already opened, and the caller writes every result.
type Extractor interface {
	// Name is the stable extractor key, persisted as extraction_jobs.extractor
	// (NOT NULL, CHECK char_length > 0).
	Name() string

	// Version is the extractor's contract version, persisted as
	// extraction_jobs.extractor_version (NOT NULL, CHECK char_length > 0).
	Version() string

	// Extract returns one FieldResult per field it looked for: the decided reading, plus the
	// alternatives an ambiguous one keeps. It must not retain or mutate doc.Bytes past the
	// call. On success the slice is non-nil (possibly empty), so no caller has to coerce a nil
	// slice away from a JSON null; on error it is nil and the caller records the job failed.
	Extract(ctx context.Context, doc Document) ([]FieldResult, error)
}

// Document is the input. Bytes are owned by the caller.
type Document struct {
	Bytes       []byte
	ContentType string // documents.declared_content_type; empty when unknown
}

// Field is one extracted field. Value is nil when nothing was found, Region when no region
// can be pointed at.
type Field struct {
	Name   string
	Value  *string
	Region *Region
	Reason Reason
}

// Region is a normalised box on one page: X0/Y0/X1/Y1 in [0,1], TOP-LEFT origin, Page
// 1-based — the shape extraction_field_results_bbox_normalised enforces.
type Region struct {
	Page           int
	X0, Y0, X1, Y1 float64
}

// Reason is why a field is flagged for a human, persisted as
// extraction_field_results.reason_code. A string enum, unlike internal/submission's Reason,
// which is a struct.
type Reason string

// The four non-empty values are the reason_code CHECK set: ('unreadable', 'ambiguous',
// 'inconsistent', 'missing'). ReasonNone is the empty string rather than a sentinel word: no
// doubt means a NULL reason_code.
const (
	ReasonNone         Reason = ""
	ReasonUnreadable   Reason = "unreadable"
	ReasonAmbiguous    Reason = "ambiguous"
	ReasonInconsistent Reason = "inconsistent"
	ReasonMissing      Reason = "missing"
)
