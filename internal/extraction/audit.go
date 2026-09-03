// audit.go: the terminal-outcome audit seam. A func value, not an internal/audit import:
// deps_test.go fences this package to internal/platform/*, in two scans.
package extraction

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// FailureKind is why an extraction reached dead_lettered, classified by the stage that failed.
// Derived from control flow, never by parsing extraction_jobs.last_error: that column is free
// text, which the audit-payload rule forbids and the jobs table already holds.
type FailureKind string

// One value per error path in Work()'s `if err == nil` chain. Rendering the pages and
// committing the page rows are two stages that fail for different reasons and are fixed in
// different places. text_not_read is the text read's path; EXTR-17-02 wires the stage that
// sets it.
const (
	FailureDocumentUnavailable FailureKind = "document_unavailable"
	FailurePagesNotRendered    FailureKind = "pages_not_rendered"
	FailurePageRowsNotWritten  FailureKind = "page_rows_not_written"
	FailureExtractFailed       FailureKind = "extract_failed"
	FailureTextNotRead         FailureKind = "text_not_read"
)

// Valid reports whether k is one of the five kinds. "" is invalid: a success carries no kind,
// and the adapter gates its failure branch on this, refusing a half-filled failure payload.
func (k FailureKind) Valid() bool {
	switch k {
	case FailureDocumentUnavailable, FailurePagesNotRendered,
		FailurePageRowsNotWritten, FailureExtractFailed, FailureTextNotRead:
		return true
	}
	return false
}

// ExtractionAudit is one terminal outcome. The adapter picks the event name from Succeeded, so
// this package spells none — a const here would break the repo-wide audit.Record scan.
type ExtractionAudit struct {
	Succeeded        bool
	DocumentID       string
	ExtractionJobID  string
	Extractor        string
	ExtractorVersion string
	FieldCount       int
	FlaggedCount     int
	State            string
	FailureKind      FailureKind
}

// RecordExtractionAudit writes one audit row on the worker's own transaction, so the row shares
// that transaction's fate.
type RecordExtractionAudit func(ctx context.Context, tx pgx.Tx, ev ExtractionAudit) error
