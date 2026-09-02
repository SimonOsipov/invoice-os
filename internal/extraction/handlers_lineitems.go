// handlers_lineitems.go: POST /v1/extractions/{id}/line-items -- one transaction replacing an
// invoice's whole line set, appending the correction row and auditing the pair.
package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// normalizeLines never returns nil: a nil slice marshals to JSON null, and a 201 body's lines
// must read as an array even for an empty set.
func normalizeLines(lines []LineItemInput) []LineItemInput {
	if lines == nil {
		return []LineItemInput{}
	}
	return lines
}

// canonicalLineJSON is the correction row's value. encoding/json already gives the stable form:
// struct fields marshal in declaration order and none carries omitempty, so every object emits
// description, quantity, unit_price, line_total with null for an absent cell and no whitespace.
// An empty set collapses to "[]", which clears the value CHECK (char_length(value) > 0).
func canonicalLineJSON(lines []LineItemInput) string {
	b, err := json.Marshal(normalizeLines(lines))
	if err != nil {
		return "[]"
	}
	return string(b)
}

// LineItemsHandler returns POST /v1/extractions/{id}/line-items. Identity is checked FIRST,
// before the path value and the body are read, so an unauthenticated caller learns nothing about
// which jobs exist (TestLineItemsHandler_UnauthenticatedIs401BeforeParsing).
func LineItemsHandler(pool *pgxpool.Pool, apply ApplyLineItemsToInvoice, record RecordFieldCorrected, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := auth.IdentityFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// The correction and detail routes' message: all three bind the same {id}, and a second
		// spelling would tell a caller the wrong field.
		parsed, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, msgMalformedJobID)
			return
		}

		var req LineItemsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, msgInvalidBody)
			return
		}
		// A nil slice and an empty array mean different things here: only the second is a
		// legitimate "remove every line", so an absent or null key is refused rather than read
		// as one.
		if req.Lines == nil {
			writeError(w, http.StatusBadRequest, msgNoLinesKey)
			return
		}
		if len(req.Lines) > maxPostedLines {
			writeError(w, http.StatusBadRequest, msgTooManyLines)
			return
		}

		out, err := writeLineItems(r.Context(), pool, lineItemsWrite{
			apply: apply, record: record, caller: caller,
			jobID: parsed.String(), lines: req.Lines,
		})
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "extraction: replace line items",
					slog.String("job", parsed.String()), slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

// lineItemsWrite is what one committed line-set replacement needs. A struct rather than five
// arguments; nothing outside this file builds one.
type lineItemsWrite struct {
	apply  ApplyLineItemsToInvoice
	record RecordFieldCorrected
	caller auth.Identity
	jobID  string
	lines  []LineItemInput
}

// writeLineItems runs the three writes on ONE transaction, so the line set, the correction row
// and the audit row commit together or not at all.
func writeLineItems(ctx context.Context, pool *pgxpool.Pool, in lineItemsWrite) (LineItemsResponse, error) {
	var out LineItemsResponse
	err := db.WithinRequestTenantTx(ctx, pool, func(tx pgx.Tx) error {
		// No tenant_id predicate: tenant_isolation supplies it, so another tenant's job is
		// zero rows here and reads exactly like an absent one.
		var documentID string
		if err := tx.QueryRow(ctx,
			`SELECT document_id FROM extraction_jobs WHERE id = $1`, in.jobID).Scan(&documentID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		invoiceID, err := in.apply(ctx, tx, documentID, in.lines)
		if err != nil {
			return err
		}

		// One row for the whole set, carrying its canonical JSON. Region stays nil: the
		// pointed_has_region CHECK admits a region only for a pointed correction.
		appended, err := appendCorrectionTx(ctx, tx, in.caller.TenantID, in.jobID, Correction{
			FieldName: "line_items",
			Value:     canonicalLineJSON(in.lines),
			Method:    MethodTyped,
			Region:    nil,
			Actor:     in.caller.Subject,
		})
		if err != nil {
			return err
		}

		if err := in.record(ctx, tx, in.caller.Subject, FieldCorrection{
			InvoiceID: invoiceID, FieldName: "line_items", Method: MethodTyped,
		}); err != nil {
			return err
		}

		out = LineItemsResponse{
			ID:        appended.ID,
			InvoiceID: invoiceID,
			Lines:     normalizeLines(in.lines),
			CreatedAt: appended.CreatedAt,
		}
		return nil
	})
	if err != nil {
		return LineItemsResponse{}, err
	}
	return out, nil
}
