package invoice

import (
	"context"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

type NoteLineInput struct{ LineID, Quantity, UnitPrice string }

type NoteInput struct {
	DocumentType  submission.DocumentType
	InvoiceNumber string
	IssueDate     *time.Time
	Lines         []NoteLineInput
}

// noteRefusal is a compile stub; the executor replaces it.
func noteRefusal(inv Invoice) error { return ErrValidation }

// IssueNote is a compile stub; the executor replaces it.
func (s *Store) IssueNote(ctx context.Context, originalID string, in NoteInput) (Invoice, error) {
	return Invoice{}, ErrValidation
}
