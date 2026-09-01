// handlers_correction.go: POST /v1/extractions/{id}/fields/{name}/corrections -- one
// transaction that appends the correction row, applies the value to the invoice filed from the
// document, and audits the pair. Declarations only until the write lands.
package extraction

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyFieldToInvoice writes one corrected field onto the invoice filed from documentID, on the
// caller's transaction. A func value, not an internal/invoice import: deps_test.go fences this
// package to internal/platform/*, in two scans.
type ApplyFieldToInvoice func(ctx context.Context, tx pgx.Tx, documentID, field, value string) (invoiceID string, err error)

// FieldCorrection is what the audit seam records. The event name is spelled in cmd/submission:
// a literal here reads as a non-literal to the repo-wide audit.Record scan and lands the call
// site in no bucket.
type FieldCorrection struct {
	InvoiceID string
	FieldName string
	Method    CorrectionMethod
}

// RecordFieldCorrected writes one audit row on the handler's own transaction, so the row shares
// the correction's fate.
type RecordFieldCorrected func(ctx context.Context, tx pgx.Tx, subject string, c FieldCorrection) error

// CorrectionRequest is the POST body. Region is a named pointer, never an inline struct:
// wireMirrors' goStructKeys reads a brace-free body only.
type CorrectionRequest struct {
	Value       string            `json:"value"`
	Method      CorrectionMethod  `json:"method"`
	Region      *ExtractionRegion `json:"region"`
	AnchorLabel string            `json:"anchor_label"`
}

// CorrectionResponse is the 201 body: what was appended plus the invoice it reached. The
// three-layer field state is the detail read's wire, and only a Detail re-read reflects a
// demotion.
type CorrectionResponse struct {
	ID        string            `json:"id"`
	FieldName string            `json:"field_name"`
	Value     string            `json:"value"`
	Method    CorrectionMethod  `json:"method"`
	Region    *ExtractionRegion `json:"region"`
	InvoiceID string            `json:"invoice_id"`
	CreatedAt time.Time         `json:"created_at"`
}

// The three outcomes the invoice seam reports. Named sentinels, so statusForErr maps each by
// identity and everything unrecognised stays a 500.
var (
	ErrNoInvoiceForDocument = errors.New("extraction: no invoice was filed from this document")
	ErrInvoiceNotEditable   = errors.New("extraction: the invoice is past the states an edit may reach")
	ErrValueRefused         = errors.New("extraction: the invoice refused the value")
)

var errCorrectionNotImplemented = errors.New("extraction: correction handler not implemented")

// CorrectionHandler returns POST /v1/extractions/{id}/fields/{name}/corrections.
func CorrectionHandler(pool *pgxpool.Pool, apply ApplyFieldToInvoice, record RecordFieldCorrected, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		status, msg := statusForErr(errCorrectionNotImplemented)
		writeError(w, status, msg)
	}
}
