package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

const noteDateHandOff = "Issue this note from its original invoice to carry on."

// ImportDocumentNote files a read credit or debit note as a draft note on its original.
// Every refusal before CreateBatch mints no batch; a failure after it ends the batch failed.
func (s *Service) ImportDocumentNote(ctx context.Context, in DocumentImport) (BatchResult, error) {
	if err := s.batch.EntityVisible(ctx, in.EntityID); err != nil {
		return BatchResult{}, err
	}
	ex, err := s.batch.SettledExtraction(ctx, in.DocumentID)
	if err != nil {
		return BatchResult{}, err
	}
	verdict := map[string]string{"credit_note": "credit note", "debit_note": "debit note"}[in.DocumentType]
	if verdict == "" || ex.DocumentType == nil || *ex.DocumentType != verdict {
		return BatchResult{}, ErrNoteTypeNotRead
	}
	// ceiling: check-then-create race across two tabs; lock the document row if a second invoice is ever seen
	filed, err := s.batch.DocumentHasInvoice(ctx, in.DocumentID)
	if err != nil {
		return BatchResult{}, err
	}
	if filed {
		return BatchResult{}, ErrDocumentAlreadyFiled
	}
	orig, err := s.inv.NoteOriginal(ctx, in.OriginalInvoiceID)
	if err != nil {
		return BatchResult{}, err
	}
	if !strings.EqualFold(orig.EntityID, in.EntityID) {
		return BatchResult{}, invoice.ErrOriginalOtherEntity
	}

	// A note stores no due date, so an unreadable one refuses nothing.
	readable := ex
	readable.Fields = make([]extractedField, 0, len(ex.Fields))
	for _, f := range ex.Fields {
		if f.Name != "due_date" {
			readable.Fields = append(readable.Fields, f)
		}
	}
	note, rowErr := readingCreateInput(in.EntityID, in.DocumentID, readable)
	if rowErr != nil {
		msg := strings.TrimSuffix(rowErr.Message, "Enter this invoice manually to carry on.") + noteDateHandOff
		return BatchResult{}, fmt.Errorf("%w: %s", ErrValidation, msg)
	}
	if err := notePositiveInput(&note); err != nil {
		return BatchResult{}, err
	}
	note.InvoiceNumber = strings.TrimSpace(in.InvoiceNumber)
	note.DocumentType = submission.DocumentType(in.DocumentType)
	note.OriginalInvoiceID = &orig.ID

	existing, err := s.batch.ExistingNumbers(ctx, in.EntityID, []string{note.InvoiceNumber})
	if err != nil {
		return BatchResult{}, err
	}
	if _, taken := existing[note.InvoiceNumber]; taken {
		return BatchResult{}, invoice.ErrDuplicateNumber
	}

	batchID, err := s.batch.CreateBatch(ctx, in.EntityID, ex.Filename, in.DocumentID, 0)
	if err != nil {
		return BatchResult{}, err
	}
	note.ImportBatchID = &batchID
	created, err := s.inv.CreateDocumentNote(ctx, orig.ID, note)
	if err != nil {
		_ = s.batch.Finalize(ctx, batchID, 1, 1, 0, nil, "failed")
		return BatchResult{}, err
	}
	if err := s.batch.Finalize(ctx, batchID, 1, 1, 0, []RowError{}, "completed"); err != nil {
		return BatchResult{}, err
	}
	s.recordImportRuleBreaks(ctx, ex, created)
	return BatchResult{
		ID: batchID, Status: "completed", RowsTotal: 1, RowsValid: 1, ReadyInvoices: 1,
		Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{},
	}, nil
}

// notePositiveInput stores every read amount of the note positive, naming the first that is no amount.
func notePositiveInput(in *invoice.CreateInput) error {
	for _, f := range []struct {
		name string
		v    **string
	}{{"subtotal", &in.Subtotal}, {"VAT", &in.VAT}, {"total", &in.Total}} {
		if err := positive(f.name, f.v); err != nil {
			return err
		}
	}
	for i := range in.LineItems {
		li := &in.LineItems[i]
		for _, f := range []struct {
			name string
			v    **string
		}{{"line quantity", &li.Quantity}, {"line unit price", &li.UnitPrice}, {"line total", &li.LineTotal}, {"line tax", &li.LineTax}} {
			if err := positive(f.name, f.v); err != nil {
				return err
			}
		}
	}
	return nil
}

func positive(name string, v **string) error {
	p, err := notePositive(*v)
	if err != nil {
		return fmt.Errorf("%w: The %s on this document %s.", ErrValidation, name, err.Error())
	}
	*v = p
	return nil
}

var notePositive = invoice.NotePositive

// NoteOriginalRow is one invoice the review can offer as the original of a note.
type NoteOriginalRow struct {
	ID            string     `json:"id"`
	InvoiceNumber string     `json:"invoice_number"`
	IssueDate     *time.Time `json:"issue_date"`
	Currency      *string    `json:"currency"`
	Total         *string    `json:"total"`
	BuyerName     *string    `json:"buyer_name"`
	NoteCount     int        `json:"note_count"`
}

// NoteOriginals is the answer of GET /v1/imports/document/note-originals.
type NoteOriginals struct {
	EntityID       string            `json:"entity_id"`
	FiledInvoiceID *string           `json:"filed_invoice_id"`
	Originals      []NoteOriginalRow `json:"originals"`
}

// noteOriginalsCap bounds the picker's list.
const noteOriginalsCap = 500

// NoteOriginals reads the invoices the review can offer as the original of the document's note.
// ceiling: LIMIT 500, the match sorted first; revisit when an entity passes 500 accepted invoices.
func (s *Store) NoteOriginals(ctx context.Context, documentID string) (NoteOriginals, error) {
	out := NoteOriginals{Originals: []NoteOriginalRow{}}
	// Its own tx: SettledExtraction's SELECTs stay literal in that method (TestSettledExtraction_SaysWhyItIsNotCorrectionAware).
	ex, err := s.SettledExtraction(ctx, documentID)
	if err != nil {
		return NoteOriginals{Originals: []NoteOriginalRow{}}, err
	}
	var read *string
	for _, f := range ex.Fields {
		if f.Name == "original_invoice_number" {
			read = f.Value
		}
	}
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT entity_id FROM import_batches WHERE document_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
			documentID).Scan(&out.EntityID); err != nil {
			return err
		}
		var filed string
		switch err := tx.QueryRow(ctx,
			`SELECT id FROM invoices WHERE source_document_id = $1 ORDER BY created_at, id LIMIT 1`,
			documentID).Scan(&filed); {
		case err == nil:
			out.FiledInvoiceID = &filed
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT i.id, i.invoice_number, i.issue_date, i.currency, i.total::text, i.buyer_name,
			        (SELECT count(*) FROM invoices n WHERE n.original_invoice_id = i.id)
			   FROM invoices i
			  WHERE i.entity_id = $1 AND i.status = 'accepted' AND i.irn IS NOT NULL
			    AND i.document_type = 'commercial_invoice'
			  ORDER BY coalesce(i.invoice_number = $2::text, false) DESC, i.created_at DESC, i.id DESC
			  LIMIT $3`,
			out.EntityID, read, noteOriginalsCap)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r NoteOriginalRow
			if err := rows.Scan(&r.ID, &r.InvoiceNumber, &r.IssueDate, &r.Currency, &r.Total, &r.BuyerName, &r.NoteCount); err != nil {
				return err
			}
			out.Originals = append(out.Originals, r)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return NoteOriginals{Originals: []NoteOriginalRow{}}, ErrNotFound
	}
	if err != nil {
		return NoteOriginals{Originals: []NoteOriginalRow{}}, err
	}
	return out, nil
}

// NoteOriginalsHandler answers GET /v1/imports/document/note-originals?document_id=<uuid>.
func NoteOriginalsHandler(read func(ctx context.Context, documentID string) (NoteOriginals, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		documentID := r.URL.Query().Get("document_id")
		if documentID == "" {
			writeError(w, http.StatusBadRequest, "document_id is required")
			return
		}
		parsed, err := uuid.Parse(documentID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			return
		}
		got, err := read(r.Context(), parsed.String())
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "importer: read note originals", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		if got.Originals == nil {
			got.Originals = []NoteOriginalRow{}
		}
		writeJSON(w, http.StatusOK, got)
	}
}
