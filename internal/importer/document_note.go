package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
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

// notePositive stores a read amount positive (printed notes show -750.00), keeping its scale.
func notePositive(v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	d, err := decimal.NewFromString(strings.TrimSpace(*v))
	if err != nil {
		return nil, errors.New("reads " + fmt.Sprintf("%q", *v) + ", which is not an amount")
	}
	if !d.IsNegative() {
		return v, nil
	}
	out := d.Abs().StringFixed(max(0, -d.Exponent()))
	return &out, nil
}

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

// NoteOriginals is a compile stub that answers wrongly until CRDDBTNOTE-05-05 lands.
func (s *Store) NoteOriginals(ctx context.Context, documentID string) (NoteOriginals, error) {
	return NoteOriginals{}, nil
}

// NoteOriginalsHandler is a compile stub that answers wrongly until CRDDBTNOTE-05-05 lands.
func NoteOriginalsHandler(read func(ctx context.Context, documentID string) (NoteOriginals, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
