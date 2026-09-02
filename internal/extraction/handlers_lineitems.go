// handlers_lineitems.go: POST /v1/extractions/{id}/line-items -- one transaction that replaces
// the whole line set of the invoice filed from the job's document, appends a "line_items"
// correction row, and audits the pair. Declarations only until the write lands.
//
// msgMalformedJobID and msgInvalidBody are handlers_correction.go's own; this route reuses them
// rather than retyping the same sentence under a second constant.
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

// LineItemInput is one posted line. A named type with a brace-free body: wireMirrors'
// goStructKeys reads a brace-free body only.
type LineItemInput struct {
	Description *string `json:"description"`
	Quantity    *string `json:"quantity"`
	UnitPrice   *string `json:"unit_price"`
	LineTotal   *string `json:"line_total"`
}

// LineItemsRequest is the POST body. Lines is nil when the key was absent or JSON null;
// encoding/json leaves a present-but-empty array as a non-nil, zero-length slice, which is what
// lets a nil Lines and an empty one mean different things to this replace-all route.
type LineItemsRequest struct {
	Lines []LineItemInput `json:"lines"`
}

// LineItemsResponse is the 201 body: what was written plus the invoice it reached.
type LineItemsResponse struct {
	ID        string          `json:"id"`
	InvoiceID string          `json:"invoice_id"`
	Lines     []LineItemInput `json:"lines"`
	CreatedAt time.Time       `json:"created_at"`
}

// ApplyLineItemsToInvoice replaces the whole line set of the invoice filed from documentID, on
// the caller's transaction. A func value, not an internal/invoice import: deps_test.go fences
// this package to internal/platform/*, in two scans.
type ApplyLineItemsToInvoice func(ctx context.Context, tx pgx.Tx, documentID string, lines []LineItemInput) (invoiceID string, err error)

// The refusal wire this route adds.
const (
	msgNoLinesKey   = "lines is required"
	msgTooManyLines = "lines must not exceed 999"
)

// maxPostedLines is a stated policy guard on an unbounded replace-all body, not a derived bound.
const maxPostedLines = 999

var errLineItemsNotImplemented = errors.New("extraction: line items handler not implemented")

// normalizeLines never returns nil: a nil slice marshals to JSON null, and a 201 body's lines
// must read as an array even for an empty set.
func normalizeLines(lines []LineItemInput) []LineItemInput {
	return lines
}

// canonicalLineJSON is the correction row's value: keys in
// description, quantity, unit_price, line_total order, null for an absent cell, no whitespace.
func canonicalLineJSON(lines []LineItemInput) string {
	return ""
}

// LineItemsHandler returns POST /v1/extractions/{id}/line-items.
func LineItemsHandler(pool *pgxpool.Pool, apply ApplyLineItemsToInvoice, record RecordFieldCorrected, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		status, msg := statusForErr(errLineItemsNotImplemented)
		writeError(w, status, msg)
	}
}
