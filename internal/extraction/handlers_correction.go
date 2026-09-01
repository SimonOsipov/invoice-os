// handlers_correction.go: POST /v1/extractions/{id}/fields/{name}/corrections -- one
// transaction that appends the correction row, applies the value to the invoice filed from the
// document, and audits the pair.
package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// The refusal wire. 400 is malformed input, 422 is a well-formed request this route declines.
const (
	msgMalformedJobID   = "id must be a well-formed uuid"
	msgInvalidBody      = "invalid request body"
	msgBlankValue       = "value must not be blank"
	msgUnknownMethod    = "method must be one of typed, chosen, pointed, undone"
	msgRegionDisagrees  = "only a pointed correction carries a region"
	msgRegionBox        = "region must be a normalised box: page at least 1, 0 <= x0 <= x1 <= 1 and 0 <= y0 <= y1 <= 1"
	msgBadIssueDate     = "issue_date must be a date this system can read"
	msgUnknownField     = "this document field is not one we file on an invoice"
	msgInvoiceNumberSet = "invoice_number identifies the invoice and is not corrected here"
	msgSupplierField    = "supplier_tin and supplier_name come from the client record, not from the document"
)

// The three fields HeaderFields names that no correction may reach. invoice_number is what the
// invoice is filed under; updateContentTx re-derives supplier_tin and supplier_name from the
// client entity on every write and never reads the input, so accepting either would store the
// client record's value under a cell claiming the human typed it.
var lockedFields = map[string]string{
	"invoice_number": msgInvoiceNumberSet,
	"supplier_tin":   msgSupplierField,
	"supplier_name":  msgSupplierField,
}

// refuseField answers the field-name vocabulary, before the body is read: the reason a caller
// gets back must be the real one, not whatever the body happens to be wrong about too.
func refuseField(name string) (msg string, refused bool) {
	known := false
	for _, f := range HeaderFields {
		if f == name {
			known = true
			break
		}
	}
	if !known {
		return msgUnknownField, true
	}
	if msg, ok := lockedFields[name]; ok {
		return msg, true
	}
	return "", false
}

// normalisedBox mirrors extraction_field_corrections_bbox_normalised and the page CHECK, so a
// caller sending pixel coordinates reads a 400 rather than a 23514 surfacing as a 500. A
// degenerate zero-area box is admitted, exactly as the constraint admits it.
func normalisedBox(r ExtractionRegion) bool {
	return r.Page >= 1 &&
		r.X0 >= 0 && r.X0 <= r.X1 && r.X1 <= 1 &&
		r.Y0 >= 0 && r.Y0 <= r.Y1 && r.Y1 <= 1
}

func validMethod(m CorrectionMethod) bool {
	switch m {
	case MethodTyped, MethodChosen, MethodPointed, MethodUndone:
		return true
	}
	return false
}

// CorrectionHandler returns POST /v1/extractions/{id}/fields/{name}/corrections. Identity is
// checked FIRST, before any path value or body is read, so an unauthenticated caller learns
// nothing about which field names exist.
func CorrectionHandler(pool *pgxpool.Pool, apply ApplyFieldToInvoice, record RecordFieldCorrected, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := auth.IdentityFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// The detail and page routes' message: all three bind the same {id}, and a second
		// spelling would tell a caller the wrong field.
		parsed, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, msgMalformedJobID)
			return
		}

		field := r.PathValue("name")
		if msg, refused := refuseField(field); refused {
			writeError(w, http.StatusUnprocessableEntity, msg)
			return
		}

		var req CorrectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, msgInvalidBody)
			return
		}
		// The DB admits a single space by design (the value CHECK counts characters), so this
		// is the only place a blank correction is closed: a stored space renders as an empty
		// cell while the cell claims a human changed it.
		value := strings.TrimSpace(req.Value)
		if value == "" {
			writeError(w, http.StatusBadRequest, msgBlankValue)
			return
		}
		if !validMethod(req.Method) {
			writeError(w, http.StatusBadRequest, msgUnknownMethod)
			return
		}
		// Both directions, mirroring extraction_field_corrections_pointed_has_region.
		if (req.Method == MethodPointed) != (req.Region != nil) {
			writeError(w, http.StatusBadRequest, msgRegionDisagrees)
			return
		}
		if req.Region != nil && !normalisedBox(*req.Region) {
			writeError(w, http.StatusBadRequest, msgRegionBox)
			return
		}
		// issue_date is the one field the handler parses: UpdateInput.IssueDate is a
		// *time.Time, so an unreadable date cannot reach the invoice as text. TWO readings is
		// refused as well as none -- picking one silently is how a tax period ends up wrong.
		if field == "issue_date" {
			readings := ShapeDate.Normalize(value)
			if len(readings) != 1 {
				writeError(w, http.StatusBadRequest, msgBadIssueDate)
				return
			}
			value = readings[0]
		}

		out, err := writeCorrection(r.Context(), pool, correctionWrite{
			apply: apply, record: record, caller: caller,
			jobID: parsed.String(), field: field, value: value, req: req,
		})
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "extraction: correct field",
					slog.String("job", parsed.String()), slog.String("field", field), slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

// correctionWrite is what one committed correction needs. A struct rather than nine arguments;
// nothing outside this file builds one.
type correctionWrite struct {
	apply  ApplyFieldToInvoice
	record RecordFieldCorrected
	caller auth.Identity
	jobID  string
	field  string
	value  string
	req    CorrectionRequest
}

// writeCorrection runs the three writes on ONE transaction, so the correction row, the invoice
// field and the audit row commit together or not at all.
func writeCorrection(ctx context.Context, pool *pgxpool.Pool, in correctionWrite) (CorrectionResponse, error) {
	var out CorrectionResponse
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

		invoiceID, err := in.apply(ctx, tx, documentID, in.field, in.value)
		if err != nil {
			return err
		}

		// appendCorrectionTx, not CorrectionStore.Append: the wrapper opens a transaction of
		// its own, which is the one thing this route cannot have.
		appended, err := appendCorrectionTx(ctx, tx, in.caller.TenantID, in.jobID, Correction{
			FieldName:   in.field,
			Value:       in.value,
			Method:      in.req.Method,
			Region:      regionFromWire(in.req.Region),
			AnchorLabel: strings.TrimSpace(in.req.AnchorLabel),
			Actor:       in.caller.Subject,
		})
		if err != nil {
			return err
		}

		// The handler's own step, after the invoice seam returns: a confirming correction
		// no-ops on the invoice and still records the human action.
		if err := in.record(ctx, tx, in.caller.Subject, FieldCorrection{
			InvoiceID: invoiceID, FieldName: in.field, Method: in.req.Method,
		}); err != nil {
			return err
		}

		out = CorrectionResponse{
			ID:        appended.ID,
			FieldName: in.field,
			Value:     in.value,
			Method:    in.req.Method,
			Region:    in.req.Region,
			InvoiceID: invoiceID,
			CreatedAt: appended.CreatedAt,
		}
		return nil
	})
	if err != nil {
		return CorrectionResponse{}, err
	}
	return out, nil
}

// regionFromWire converts the wire box to the domain one the correction store binds.
func regionFromWire(r *ExtractionRegion) *Region {
	if r == nil {
		return nil
	}
	return &Region{Page: r.Page, X0: r.X0, Y0: r.Y0, X1: r.X1, Y1: r.Y1}
}
